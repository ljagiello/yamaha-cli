package cli

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/ljagiello/yamaha-cli/pkg/ynca"
)

// This file captures a receiver's full state as a replayable transcript —
// the highest-leverage tooling move from the ynca reference library
// (dumper.py): one canonical @SUBUNIT:FUNCTION=VALUE format flows from a
// live dump into checked-in logs, the replay fake (used by tests), and `ynca
// diff`. yamaha-cli already had every primitive (SendMulti's VERSION fence,
// the mutex client, parseLine); this wires them into a capture command so
// reverse-engineering a receiver no longer means typing lines into the REPL
// one at a time.
//
// Every command is issued through SendMulti, which appends a @SYS:VERSION=?
// fence and drains to its echo — so a single-value GET and a BASIC fan-out
// are handled uniformly, and an unsupported function's @UNDEFINED reply is
// captured (as a comment) rather than stranding bytes in the buffer.

// yncaDumpDelay is the pause between commands, keeping the dump gentle on a
// busy receiver. A var so tests can shrink it.
var yncaDumpDelay = 80 * time.Millisecond

func newYncaDumpCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "dump",
		Short: "Capture the receiver's state as a replayable YNCA transcript",
		Long: "dump sends a catalog of GET commands and records every reply in the\n" +
			"canonical @SUBUNIT:FUNCTION=VALUE wire format. The result is a\n" +
			"transcript you can check in, diff against another receiver with\n" +
			"`ynca diff`, or replay in tests.\n\n" +
			"With no --commands file it uses a built-in catalog spanning the\n" +
			"system, all zones, the tuner, and the streaming sources. Provide\n" +
			"--commands FILE (one GET per line, '#' comments allowed) to scope\n" +
			"the dump. Only GETs should be listed; a value-setting line is sent\n" +
			"as-is, so keep the catalog read-only.",
		Args: cobra.NoArgs,
		RunE: runYncaDump,
	}
	cmd.Flags().String("commands", "", "file of GET lines to send (default: built-in catalog)")
	cmd.Flags().String("out", "", "write the transcript to this file (default: stdout)")
	return cmd
}

func runYncaDump(cmd *cobra.Command, _ []string) error {
	s := stateFromCmd(cmd)
	if s == nil {
		return errors.New("ynca dump: no state on context")
	}
	ctx := cmd.Context()
	if s.device.Host == "" {
		return errors.New("ynca dump: no device host")
	}

	commandsFile, _ := cmd.Flags().GetString("commands")
	commands, err := loadDumpCommands(commandsFile)
	if err != nil {
		return err
	}

	// Settle the host first (rediscover-safe), then dump once against the
	// resolved IP. The dump is read-only, so a mid-dump transport failure
	// just ends the capture — nothing is double-executed.
	if err := yncaSettleHost(ctx, s); err != nil {
		return err
	}
	c, err := s.newYNCAClient(s.device.Host, yncaSendTimeout)
	if err != nil {
		return err
	}
	defer func() { _ = c.Close() }()

	// Resolve the output sink before talking to the device, so a bad path
	// fails fast.
	out := cmd.OutOrStdout()
	if outFile, _ := cmd.Flags().GetString("out"); strings.TrimSpace(outFile) != "" {
		f, ferr := os.Create(outFile)
		if ferr != nil {
			return fmt.Errorf("ynca dump: create %s: %w", outFile, ferr)
		}
		defer func() { _ = f.Close() }()
		out = f
	}

	bw := bufio.NewWriter(out)
	defer func() { _ = bw.Flush() }()

	fmt.Fprintf(bw, "# yamaha-cli YNCA dump\n# device: %s\n# commands: %d\n", s.device.Host, len(commands))

	for _, line := range commands {
		lines, serr := c.SendMulti(ctx, line)
		if serr != nil {
			// A transport failure ends the dump; an application reply does
			// not (SendMulti only errors on transport / no-reply). Record
			// what happened and stop so the file isn't silently truncated.
			fmt.Fprintf(bw, "# %s -> ERROR: %v\n", line, serr)
			if ynca.IsTransport(serr) || errors.Is(serr, ynca.ErrNoReply) {
				_ = bw.Flush()
				return friendlyYNCAError(line, serr)
			}
			continue
		}
		writeDumpReplies(bw, line, lines)
		select {
		case <-ctx.Done():
			_ = bw.Flush()
			return ctx.Err()
		case <-time.After(yncaDumpDelay):
		}
	}
	return nil
}

// writeDumpReplies records one command's replies. Value-bearing report lines
// are written verbatim (so the file replays cleanly); a bare control reply
// (@UNDEFINED/@RESTRICTED) is written as a comment tagged with the request,
// so it documents what the device rejected without polluting the replayable
// value set.
func writeDumpReplies(w io.Writer, request string, lines []string) {
	if len(lines) == 0 {
		fmt.Fprintf(w, "# %s -> (no reply)\n", request)
		return
	}
	for _, ln := range lines {
		if strings.HasPrefix(ln, "@UNDEFINED") || strings.HasPrefix(ln, "@RESTRICTED") {
			fmt.Fprintf(w, "# %s -> %s\n", request, ln)
			continue
		}
		fmt.Fprintln(w, ln)
	}
}

// loadDumpCommands returns the GET lines to send: the built-in catalog when
// path is empty, otherwise the '#'-commented, one-per-line file at path.
func loadDumpCommands(path string) ([]string, error) {
	if strings.TrimSpace(path) == "" {
		return defaultDumpCommands(), nil
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("ynca dump: open %s: %w", path, err)
	}
	defer func() { _ = f.Close() }()
	var cmds []string
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		cmds = append(cmds, line)
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("ynca dump: read %s: %w", path, err)
	}
	if len(cmds) == 0 {
		return nil, newUsageError("ynca dump: %s contained no commands", path)
	}
	return cmds, nil
}

// defaultDumpCommands assembles the built-in GET catalog from the function
// descriptor registry crossed with the standard subunit set, plus the
// fan-out group GETs. It is intentionally a broad superset; unsupported
// commands simply come back @UNDEFINED and are recorded as comments.
func defaultDumpCommands() []string {
	var cmds []string
	add := func(subunit, function string) {
		cmds = append(cmds, "@"+subunit+":"+function+"=?")
	}

	// System.
	for _, f := range ynca.FunctionsForScope(ynca.ScopeSystem) {
		if f.Cmd.CanGet() {
			add(ynca.SubunitSystem, f.Name)
		}
	}

	// Every zone: the BASIC fan-out, scene names, then each readable zone
	// function.
	zones := []string{ynca.SubunitMain, ynca.SubunitZone2, ynca.SubunitZone3, ynca.SubunitZone4}
	for _, z := range zones {
		add(z, ynca.GroupSceneName)
		for _, f := range ynca.FunctionsForScope(ynca.ScopeZone) {
			if f.Cmd.CanGet() {
				add(z, f.Name)
			}
		}
	}

	// Tuner, including the RDS fan-out group.
	for _, f := range ynca.FunctionsForScope(ynca.ScopeTuner) {
		if f.Cmd.CanGet() {
			add(ynca.SubunitTuner, f.Name)
		}
	}
	add(ynca.SubunitTuner, ynca.GroupRdsInfo)

	// Streaming/network/USB sources: metadata + current transport state.
	sources := []string{
		ynca.SubunitAirPlay, ynca.SubunitBluetMix, ynca.SubunitDeezer, ynca.SubunitIpod,
		ynca.SubunitIpodUSB, ynca.SubunitMCLink, ynca.SubunitNapster, ynca.SubunitNetRadio,
		ynca.SubunitPandora, ynca.SubunitPC, ynca.SubunitRhapsody, ynca.SubunitServer,
		ynca.SubunitSirius, ynca.SubunitSpotify, ynca.SubunitTidal, ynca.SubunitUAW, ynca.SubunitUSB,
	}
	for _, src := range sources {
		add(src, ynca.FuncMetaInfo)
		add(src, ynca.FuncPlaybackInfo)
	}
	return cmds
}
