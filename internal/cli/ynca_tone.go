package cli

import (
	"errors"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/ljagiello/yamaha-cli/pkg/ynca"
)

// newYncaToneCmd is the YNCA twin of `tone`: it drives the zone bass/treble
// tone controls (@<zone>:SPBASS / @<zone>:SPTREBLE) on receivers that only
// speak YNCA. Unlike the YXC twin it deliberately omits a client-side range
// gate — the supported dB range is model-specific, so an out-of-range value
// surfaces as the receiver's own @RESTRICTED reply rather than a guessed
// bound, keeping behaviour correct across every model.
func newYncaToneCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "tone <bass|treble> <-N..+N> | tone reset",
		Short: "Adjust the zone bass/treble tone control over YNCA",
		// Custom Args validator mirroring the YXC twin: a lone argument is
		// only meaningful as "reset" (both channels to 0); otherwise the
		// command takes a channel plus a value. Catching the arity here keeps
		// the RunE body free to assume a well-formed shape.
		Args: func(cmd *cobra.Command, args []string) error {
			switch len(args) {
			case 1:
				if strings.ToLower(strings.TrimSpace(args[0])) == "reset" {
					return nil
				}
				return newUsageError("tone: single argument must be %q (got %q)", "reset", args[0])
			case 2:
				return nil
			default:
				return newUsageError("tone: requires 1 (reset) or 2 (<bass|treble> <value>) args, got %d", len(args))
			}
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			s := stateFromCmd(cmd)
			if s == nil {
				return errors.New("ynca tone: no state on context")
			}
			ctx := cmd.Context()
			subunit := yncaSubunitForZone(s.zone)

			// `tone reset` → zero both channels. The two SetTone calls run
			// inside one runYNCASet so they share a single settled host and a
			// single connection; bass is sent first and treble only if it
			// succeeds, since a partial reset is better surfaced as an error
			// than silently left half-applied.
			if len(args) == 1 {
				err := runYNCASet(ctx, s, "@"+subunit+":TONE", func(c *ynca.Client) error {
					if e := c.SetTone(ctx, subunit, ynca.FuncSpBass, 0); e != nil {
						return e
					}
					return c.SetTone(ctx, subunit, ynca.FuncSpTreble, 0)
				})
				if err != nil {
					return err
				}
				return printResult(cmd, map[string]any{})
			}

			// Resolve the channel to its wire FUNCTION up front so a typo is
			// reported as a usage error before any network round-trip.
			channel := strings.ToLower(strings.TrimSpace(args[0]))
			var fn string
			switch channel {
			case "bass":
				fn = ynca.FuncSpBass
			case "treble":
				fn = ynca.FuncSpTreble
			default:
				return newUsageError("invalid tone channel %q (want bass|treble|reset)", args[0])
			}

			// Parse a signed dB value. strconv.ParseFloat rejects a leading
			// "+", so strip it first (matching the YXC twin's parseSignedInt
			// intent for the float grid SetTone rounds onto). No range check:
			// the device clamps/rejects per its own model-specific bounds.
			rawVal := strings.TrimSpace(args[1])
			db, perr := strconv.ParseFloat(strings.TrimPrefix(rawVal, "+"), 64)
			if perr != nil {
				return newUsageError("invalid tone value %q (want a dB number in the device's tone range)", rawVal)
			}

			err := runYNCASet(ctx, s, "@"+subunit+":"+fn, func(c *ynca.Client) error {
				return c.SetTone(ctx, subunit, fn, db)
			})
			if err != nil {
				return err
			}
			return printResult(cmd, map[string]any{})
		},
	}
}
