package cli

import (
	"errors"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/ljagiello/yamaha-cli/pkg/ynca"
)

// newYncaSleepCmd is the YNCA twin of the YXC `sleep` command: it sets the
// active zone's auto-off timer over the legacy protocol. We mirror the YXC
// command's surface (same value set, same "off" alias) so a script targeting
// either backend behaves identically — the only difference is the transport.
//
// The accepted minute set {0,30,60,90,120} is enforced here rather than
// deferred to the receiver because the backend's SetSleep already rejects
// anything else; validating first lets us return a structured
// *ValidationError with did-you-mean suggestions instead of a terse
// device-side rejection.
func newYncaSleepCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "sleep <0|30|60|90|120>|off",
		Short: "Set the zone sleep timer (minutes) over YNCA; \"off\" disables it",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			s := stateFromCmd(cmd)
			if s == nil {
				return errors.New("ynca sleep: no state on context")
			}
			ctx := cmd.Context()
			subunit := yncaSubunitForZone(s.zone)
			raw := strings.ToLower(strings.TrimSpace(args[0]))

			// "off" is the user-facing alias for the receiver's 0-minute
			// (disabled) timer; everything else must parse as an integer.
			var minutes int
			if raw == "off" {
				minutes = 0
			} else {
				n, err := strconv.Atoi(raw)
				if err != nil {
					return newUsageError("invalid sleep value %q (want one of 0|30|60|90|120 or \"off\")", raw)
				}
				minutes = n
			}

			// Gate the value before any I/O so an out-of-grid number yields a
			// structured error with suggestions rather than a round-trip to a
			// device that would only reply with a refusal.
			if !isAllowedSleep(minutes) {
				return &ValidationError{
					Kind:        "sleep",
					Unknown:     raw,
					Suggestions: []string{"0", "30", "60", "90", "120", "off"},
				}
			}

			err := runYNCASet(ctx, s, "@"+subunit+":SLEEP", func(c *ynca.Client) error {
				return c.SetSleep(ctx, subunit, minutes)
			})
			if err != nil {
				return err
			}
			return printResult(cmd, map[string]any{})
		},
	}
}
