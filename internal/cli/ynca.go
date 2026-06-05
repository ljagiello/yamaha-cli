package cli

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/ljagiello/yamaha-cli/pkg/ynca"
	"github.com/ljagiello/yamaha-cli/pkg/yxc"
)

// yncaProbeTimeout bounds the Probe call we issue once per invocation to
// confirm the receiver speaks YNCA before the user's actual command.
//
// Exposed as a var (not a const) so tests can shrink it. Production code
// never reassigns.
var yncaProbeTimeout = 3 * time.Second

// yncaSendTimeout bounds a single Send round trip.
var yncaSendTimeout = 3 * time.Second

// newYncaCmd builds the `yamaha ynca` command tree.
//
// YNCA is Yamaha's older line-based TCP control protocol (port 50000),
// the only protocol some pre-MusicCast receivers speak. The parent doubles
// as the raw passthrough — `yamaha ynca <line>` sends one YNCA line and
// prints the reply — while the typed subcommands (status/power/volume/
// mute/input/sound) give a YNCA-only receiver the same first-class control
// surface YXC devices get. A raw line always begins with "@" or
// "SUBUNIT:FUNC", so it never collides with a subcommand name.
//
// Error replies map to distinct exit codes: `@UNDEFINED` (unsupported)
// exits 70, `@RESTRICTED` (valid but not allowed now) exits 75, a
// closed/unreachable socket exits 69.
func newYncaCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "ynca [line]",
		Short: "Control a receiver over the legacy YNCA protocol (TCP/50000)",
		Long: "ynca speaks the legacy line-based YNCA protocol on TCP/50000 —\n" +
			"the only control protocol some pre-MusicCast receivers support.\n\n" +
			"With no subcommand it is a raw passthrough: send one YNCA line and\n" +
			"print the reply (the leading '@' is optional). The typed subcommands\n" +
			"act on the --zone-mapped subunit (main→MAIN, zone2→ZONE2, …).\n\n" +
			"Examples:\n" +
			"  yamaha ynca status\n" +
			"  yamaha ynca power on\n" +
			"  yamaha ynca volume -30\n" +
			"  yamaha ynca @SYS:VERSION=?\n" +
			"  yamaha ynca MAIN:PWR=?\n",
		Args: cobra.MaximumNArgs(1),
		RunE: runYncaRaw,
	}
	cmd.AddCommand(newYncaStatusCmd())
	cmd.AddCommand(newYncaPowerCmd())
	cmd.AddCommand(newYncaVolumeCmd())
	cmd.AddCommand(newYncaMuteCmd())
	cmd.AddCommand(newYncaInputCmd())
	cmd.AddCommand(newYncaSoundCmd())
	cmd.AddCommand(newYncaReplCmd())
	return cmd
}

// yncaSubunitForZone maps a canonical zone id to its YNCA subunit name.
func yncaSubunitForZone(zone string) string {
	switch zone {
	case "zone2":
		return "ZONE2"
	case "zone3":
		return "ZONE3"
	case "zone4":
		return "ZONE4"
	default:
		return "MAIN"
	}
}

// yncaProbe runs a `@SYS:VERSION=?` handshake against c, translating a
// "reachable but not YNCA" outcome into a *yxc.Error so ErrorExitCode
// returns 70.
func yncaProbe(ctx context.Context, s *state, c *ynca.Client) error {
	probeCtx, cancel := context.WithTimeout(ctx, yncaProbeTimeout)
	defer cancel()
	if _, perr := c.Probe(probeCtx); perr != nil {
		if errors.Is(perr, ynca.ErrUnsupported) {
			return &yxc.Error{
				Code:    -1,
				Message: fmt.Sprintf("device %s does not support YNCA (TCP/50000)", s.device.Host),
				Method:  "ynca/probe",
			}
		}
		return perr
	}
	return nil
}

// yncaSettleHost runs a rediscover-safe Probe so a DHCP-shifted receiver's
// new IP is found and persisted BEFORE a non-idempotent mutation is sent.
// Replaying a read (the probe) can't double-execute anything, so it's safe
// to retry under DHCP rediscovery — unlike the mutation that follows.
func yncaSettleHost(ctx context.Context, s *state) error {
	return runYNCAWithRediscover(ctx, s, yncaSendTimeout, func(c *ynca.Client) error {
		return yncaProbe(ctx, s, c)
	})
}

// runYNCASet performs a YNCA mutation with the same two-phase safety the
// raw passthrough uses: settle the host with a rediscover-safe probe, then
// run op exactly once against the resolved host WITHOUT retry — YNCA
// mutations aren't all idempotent (VOL=Up), so a post-write transport
// error must not double-execute. op's typed errors are made friendly.
func runYNCASet(ctx context.Context, s *state, label string, op func(*ynca.Client) error) error {
	if s.device.Host == "" {
		return errors.New("ynca: no device host")
	}
	if err := yncaSettleHost(ctx, s); err != nil {
		return err
	}
	c, err := s.newYNCAClient(s.device.Host, yncaSendTimeout)
	if err != nil {
		return err
	}
	defer func() { _ = c.Close() }()
	if err := op(c); err != nil {
		return friendlyYNCAError(label, err)
	}
	return nil
}

// runYncaRaw is the parent command's raw line passthrough. With no
// argument it prints help; with one it sends that YNCA line verbatim.
func runYncaRaw(cmd *cobra.Command, args []string) error {
	s := stateFromCmd(cmd)
	if s == nil {
		return errors.New("ynca: no state on context")
	}
	if len(args) == 0 {
		return cmd.Help()
	}
	ctx := cmd.Context()

	line := strings.TrimSpace(args[0])
	if line == "" {
		return newUsageError("ynca: empty command line")
	}
	if s.device.Host == "" {
		return errors.New("ynca: no device host")
	}

	// Phase 1: confirm reachability + YNCA support (rediscover-safe read).
	if err := yncaSettleHost(ctx, s); err != nil {
		return err
	}

	// Phase 2: send the user's command exactly once. A raw line may be
	// state-mutating and non-idempotent, so we don't retry it under
	// rediscovery — s.device.Host already reflects any new IP Phase 1
	// persisted.
	c, err := s.newYNCAClient(s.device.Host, yncaSendTimeout)
	if err != nil {
		return err
	}
	defer func() { _ = c.Close() }()
	reply, err := c.Send(ctx, line)
	if err != nil {
		return friendlyYNCAError(line, err)
	}
	fmt.Fprintln(cmd.OutOrStdout(), reply)
	return nil
}

func newYncaStatusCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Print zone power/volume/mute/input/sound over YNCA",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			s := stateFromCmd(cmd)
			if s == nil {
				return errors.New("ynca status: no state on context")
			}
			ctx := cmd.Context()
			subunit := yncaSubunitForZone(s.zone)

			var st *ynca.Status
			// Both the probe and GetStatus are reads, so the whole op is
			// safe to retry under rediscovery.
			err := runYNCAWithRediscover(ctx, s, yncaSendTimeout, func(c *ynca.Client) error {
				if perr := yncaProbe(ctx, s, c); perr != nil {
					return perr
				}
				got, e := c.GetStatus(ctx, subunit)
				if e != nil {
					return e
				}
				st = got
				return nil
			})
			if err != nil {
				return friendlyYNCAError("@"+subunit+":BASIC=?", err)
			}
			return printResult(cmd, buildYNCAStatusPayload(s.zone, st))
		},
	}
}

func newYncaPowerCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "power on|off|toggle",
		Short: "Power the zone on/off (standby) or toggle, over YNCA",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			s := stateFromCmd(cmd)
			if s == nil {
				return errors.New("ynca power: no state on context")
			}
			ctx := cmd.Context()
			subunit := yncaSubunitForZone(s.zone)

			arg := strings.ToLower(strings.TrimSpace(args[0]))
			switch arg {
			case "on", "off", "standby", "toggle":
			default:
				return newUsageError("invalid power argument %q (want on|off|toggle)", args[0])
			}

			err := runYNCASet(ctx, s, "@"+subunit+":PWR", func(c *ynca.Client) error {
				switch arg {
				case "on":
					return c.SetPower(ctx, subunit, ynca.PowerOn)
				case "off", "standby":
					return c.SetPower(ctx, subunit, ynca.PowerStandby)
				default: // toggle
					cur, e := c.GetPower(ctx, subunit)
					if e != nil {
						return e
					}
					if cur == ynca.PowerOn {
						return c.SetPower(ctx, subunit, ynca.PowerStandby)
					}
					return c.SetPower(ctx, subunit, ynca.PowerOn)
				}
			})
			if err != nil {
				return err
			}
			return printResult(cmd, map[string]any{})
		},
	}
}

func newYncaVolumeCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "volume <dB|up|down>",
		Short: "Set the zone volume in dB, or nudge up/down, over YNCA",
		Long: "Set an absolute volume in dB (rounded to the YNCA 0.5 dB grid), or\n" +
			"nudge up/down one step. Because dB values are negative, pass them\n" +
			"after `--`, e.g. `yamaha ynca volume -- -30.5`.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			s := stateFromCmd(cmd)
			if s == nil {
				return errors.New("ynca volume: no state on context")
			}
			ctx := cmd.Context()
			subunit := yncaSubunitForZone(s.zone)
			raw := strings.ToLower(strings.TrimSpace(args[0]))

			var op func(*ynca.Client) error
			switch raw {
			case "up":
				op = func(c *ynca.Client) error { return c.VolumeUp(ctx, subunit) }
			case "down":
				op = func(c *ynca.Client) error { return c.VolumeDown(ctx, subunit) }
			default:
				db, perr := strconv.ParseFloat(strings.TrimPrefix(raw, "+"), 64)
				if perr != nil {
					return newUsageError("invalid volume %q (want dB value, up, or down)", args[0])
				}
				op = func(c *ynca.Client) error { return c.SetVolume(ctx, subunit, db) }
			}

			if err := runYNCASet(ctx, s, "@"+subunit+":VOL", op); err != nil {
				return err
			}
			return printResult(cmd, map[string]any{})
		},
	}
}

func newYncaMuteCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "mute on|off|toggle",
		Short: "Mute, unmute, or toggle mute for the zone, over YNCA",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			s := stateFromCmd(cmd)
			if s == nil {
				return errors.New("ynca mute: no state on context")
			}
			ctx := cmd.Context()
			subunit := yncaSubunitForZone(s.zone)
			arg := strings.ToLower(strings.TrimSpace(args[0]))

			err := runYNCASet(ctx, s, "@"+subunit+":MUTE", func(c *ynca.Client) error {
				switch arg {
				case "on", "true":
					return c.SetMute(ctx, subunit, true)
				case "off", "false":
					return c.SetMute(ctx, subunit, false)
				case "toggle":
					cur, e := c.GetMute(ctx, subunit)
					if e != nil {
						return e
					}
					return c.SetMute(ctx, subunit, !cur)
				}
				return newUsageError("invalid mute argument %q (want on|off|toggle)", args[0])
			})
			if err != nil {
				return err
			}
			return printResult(cmd, map[string]any{})
		},
	}
}

func newYncaInputCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "input <name>",
		Short: "Switch the zone input over YNCA (e.g. HDMI2, TUNER)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			s := stateFromCmd(cmd)
			if s == nil {
				return errors.New("ynca input: no state on context")
			}
			ctx := cmd.Context()
			subunit := yncaSubunitForZone(s.zone)
			name := strings.TrimSpace(args[0])
			if name == "" {
				return newUsageError("ynca input: empty input name")
			}
			err := runYNCASet(ctx, s, "@"+subunit+":INP", func(c *ynca.Client) error {
				return c.SetInput(ctx, subunit, name)
			})
			if err != nil {
				return err
			}
			return printResult(cmd, map[string]any{})
		},
	}
}

func newYncaSoundCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "sound <program>",
		Short: "Select the zone DSP sound program over YNCA",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			s := stateFromCmd(cmd)
			if s == nil {
				return errors.New("ynca sound: no state on context")
			}
			ctx := cmd.Context()
			subunit := yncaSubunitForZone(s.zone)
			program := strings.TrimSpace(args[0])
			if program == "" {
				return newUsageError("ynca sound: empty program")
			}
			err := runYNCASet(ctx, s, "@"+subunit+":SOUNDPRG", func(c *ynca.Client) error {
				return c.SetSoundProgram(ctx, subunit, program)
			})
			if err != nil {
				return err
			}
			return printResult(cmd, map[string]any{})
		},
	}
}

func newYncaReplCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "repl",
		Short: "Interactive YNCA prompt over one persistent connection",
		Long: "Open an interactive prompt that holds a single YNCA connection and\n" +
			"sends one line per input — far quicker than re-connecting per\n" +
			"`yamaha ynca <line>`, and handy for reverse-engineering what a\n" +
			"YNCA-only receiver supports. Reads lines from stdin; Ctrl-D, 'exit'\n" +
			"or 'quit' ends the session.",
		Args: cobra.NoArgs,
		RunE: runYncaRepl,
	}
}

// runYncaRepl drives the interactive YNCA prompt. It settles the host once
// (rediscover-safe probe), then reuses a single connection for every line
// the user enters — ynca.Send transparently reconnects if the socket
// drops mid-session.
func runYncaRepl(cmd *cobra.Command, _ []string) error {
	s := stateFromCmd(cmd)
	if s == nil {
		return errors.New("ynca repl: no state on context")
	}
	ctx := cmd.Context()
	if s.device.Host == "" {
		return errors.New("ynca repl: no device host")
	}
	if err := yncaSettleHost(ctx, s); err != nil {
		return err
	}

	c, err := s.newYNCAClient(s.device.Host, yncaSendTimeout)
	if err != nil {
		return err
	}
	defer func() { _ = c.Close() }()

	out := cmd.OutOrStdout()
	errOut := cmd.ErrOrStderr()
	interactive := isStdinTTY()
	if interactive {
		fmt.Fprintln(out, "YNCA interactive — one line per command; Ctrl-D, 'exit' or 'quit' to leave.")
	}

	in := bufio.NewScanner(cmd.InOrStdin())
	for {
		if interactive {
			fmt.Fprint(out, "ynca> ")
		}
		if !in.Scan() {
			break
		}
		line := strings.TrimSpace(in.Text())
		if line == "" {
			continue
		}
		if line == "exit" || line == "quit" {
			break
		}
		reply, serr := c.Send(ctx, line)
		if serr != nil {
			fmt.Fprintln(errOut, "error:", friendlyYNCAError(line, serr))
			continue
		}
		fmt.Fprintln(out, reply)
	}
	return in.Err()
}

// buildYNCAStatusPayload renders a *ynca.Status as the same stable map
// shape the YXC `status` command uses, so output stays consistent across
// the two backends.
func buildYNCAStatusPayload(zone string, st *ynca.Status) map[string]any {
	out := map[string]any{
		"zone":      zone,
		"power":     strings.ToLower(string(st.Power)),
		"mute":      st.Mute,
		"volume_db": st.Volume,
	}
	if st.Input != "" {
		out["input"] = st.Input
	}
	if st.SoundPrg != "" {
		out["sound_program"] = st.SoundPrg
	}
	return out
}

// friendlyYNCAError rewrites the receiver's terse control replies into a
// message a human can act on, while preserving the underlying typed error
// via %w so ErrorExitCode still maps @UNDEFINED→70 and @RESTRICTED→75.
// Anything else (transport, no-reply, protocol) passes through untouched.
func friendlyYNCAError(line string, err error) error {
	var undef *ynca.ErrUndefinedCommand
	if errors.As(err, &undef) {
		return fmt.Errorf("%q is not supported on this device (receiver replied @UNDEFINED): %w", line, err)
	}
	var restricted *ynca.ErrRestricted
	if errors.As(err, &restricted) {
		return fmt.Errorf("%q is not allowed right now (receiver replied @RESTRICTED) — is the target zone/device powered on?: %w", line, err)
	}
	return err
}
