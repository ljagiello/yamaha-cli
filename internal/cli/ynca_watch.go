package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/spf13/cobra"

	"github.com/ljagiello/yamaha-cli/internal/output"
	"github.com/ljagiello/yamaha-cli/pkg/ynca"
)

// This file is the user-facing surface of the YNCA Session/reader work: it
// streams the unsolicited @SUB:FUNC=value reports a legacy receiver pushes
// on front-panel/remote/app changes — the live-state observation the YXC
// `watch` command has always had but the YNCA backend silently discarded.
//
// It owns its own connection (a ynca.Session, not the shared request/response
// client) and supervises it with reconnect/backoff, mirroring the YXC
// Subscriber model: Session.Run handles one connection and returns on drop;
// this loop reconnects until the context is cancelled (SIGINT → exit 0).

// YNCA watch tuning. Vars so integration tests can shrink them; production
// never reassigns.
var (
	yncaWatchBackoffMin = 1 * time.Second
	yncaWatchBackoffMax = 30 * time.Second
)

// newYncaWatchCmd builds `ynca watch`.
func newYncaWatchCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "watch",
		Short: "Stream live YNCA push reports as NDJSON (table on a TTY)",
		Long: "watch holds a YNCA connection and prints every state report the\n" +
			"receiver pushes — volume/input/power changes from the front panel,\n" +
			"the remote, or another app — as NDJSON (one object per line), or as\n" +
			"compact 'ts SUB.FUNC = value' lines on a terminal. A 30s keep-alive\n" +
			"holds the connection open; the stream reconnects automatically if it\n" +
			"drops. SIGINT exits cleanly.",
		Args: cobra.NoArgs,
		RunE: runYncaWatch,
	}
}

func runYncaWatch(cmd *cobra.Command, _ []string) error {
	s := stateFromCmd(cmd)
	if s == nil {
		return errors.New("ynca watch: no state on context")
	}
	ctx := cmd.Context()
	if s.device.Host == "" {
		return errors.New("ynca watch: no device host")
	}

	// Settle the host first (rediscover-safe probe), so a DHCP-shifted
	// receiver's current IP is found and persisted before we open the
	// long-lived session against it.
	if err := yncaSettleHost(ctx, s); err != nil {
		return err
	}

	format, err := resolveFormat(cmd)
	if err != nil {
		return err
	}
	useTable := format == output.FormatTable || (format == output.FormatAuto && isStdoutTTY())

	out := cmd.OutOrStdout()
	alias := s.alias
	if alias == "" {
		alias = s.device.Host
	}

	opts := []ynca.SessionOption{
		ynca.WithSessionTimeout(yncaSendTimeout),
		ynca.WithSessionWake(),
	}
	if s.debug != nil && s.debug.Enabled() {
		opts = append(opts, ynca.WithSessionCommLog(yncaCommLog(s)))
	}

	handler := func(r ynca.Report) {
		writeYncaWatchLine(out, useTable, alias, &r)
	}

	// Supervise: run one connection at a time, reconnecting with capped
	// backoff until the context is cancelled.
	backoff := yncaWatchBackoffMin
	for {
		sess, serr := ynca.NewSession(s.device.Host, opts...)
		if serr != nil {
			return serr
		}
		runErr := sess.Run(ctx, handler)
		if ctx.Err() != nil {
			// Clean cancellation (SIGINT): exit 0.
			return nil
		}
		// The connection dropped on its own. Emit a control line and back off.
		writeYncaWatchControl(out, useTable, alias, "reconnect", runErr)
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(backoff):
		}
		if backoff *= 2; backoff > yncaWatchBackoffMax {
			backoff = yncaWatchBackoffMax
		}
	}
}

// writeYncaWatchLine renders one push report as NDJSON or a compact table
// line. A bare control report (@UNDEFINED/@RESTRICTED, rare unsolicited)
// renders as a control line.
func writeYncaWatchLine(w io.Writer, useTable bool, alias string, r *ynca.Report) {
	if r.Status != "" {
		writeYncaWatchControl(w, useTable, alias, r.Status, nil)
		return
	}
	if useTable {
		ts := time.Now().Format("15:04:05")
		_, _ = fmt.Fprintf(w, "%s  %s  %s.%s = %s\n", ts, alias, r.Subunit, r.Function, r.Value)
		return
	}
	line := map[string]any{
		"ts":       time.Now().UTC().Format("2006-01-02T15:04:05.000Z"),
		"device":   alias,
		"subunit":  r.Subunit,
		"function": r.Function,
		"value":    r.Value,
	}
	if b, err := json.Marshal(line); err == nil {
		_, _ = io.WriteString(w, string(b)+"\n")
	}
}

// writeYncaWatchControl emits a subscriber control line (reconnect, or an
// unsolicited @UNDEFINED/@RESTRICTED), bracketed in table mode and as a
// {"event":...} object in NDJSON.
func writeYncaWatchControl(w io.Writer, useTable bool, alias, kind string, cause error) {
	if useTable {
		ts := time.Now().Format("15:04:05")
		if cause != nil {
			_, _ = fmt.Fprintf(w, "%s  %s  [%s reason=%q]\n", ts, alias, kind, cause.Error())
			return
		}
		_, _ = fmt.Fprintf(w, "%s  %s  [%s]\n", ts, alias, kind)
		return
	}
	line := map[string]any{
		"ts":     time.Now().UTC().Format("2006-01-02T15:04:05.000Z"),
		"device": alias,
		"event":  kind,
	}
	if cause != nil {
		line["reason"] = cause.Error()
	}
	if b, err := json.Marshal(line); err == nil {
		_, _ = io.WriteString(w, string(b)+"\n")
	}
}

// yncaCommLog adapts the CLI debug logger into a ynca.CommLogger so the --debug
// traces YNCA wire traffic (->/<-) the HTTP debug transport never saw.
func yncaCommLog(s *state) ynca.CommLogger {
	return func(sent bool, line string) {
		if s.debug == nil {
			return
		}
		dir := "<-"
		if sent {
			dir = "->"
		}
		s.debug.Tracef("ynca %s %s", dir, line)
	}
}
