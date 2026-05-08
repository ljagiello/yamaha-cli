package cli

import (
	"errors"

	"github.com/spf13/cobra"

	"github.com/ljagiello/yamaha-cli/pkg/yxc"
)

// newRebootCmd builds the `yamaha reboot` subcommand.
//
// Reboot is a destructive, system-wide operation (the zone flag is
// ignored) so we require an explicit `--yes` confirmation regardless of
// TTY mode. This avoids the surprising "I just bumped this script's
// pipeline and now my receiver power-cycled" scenario that would result
// from an implicit-on-TTY-only prompt.
//
// The receiver acknowledges with `{"response_code":0}` and then drops
// the connection as it reboots. Because Do() is already past the response
// parse by the time the connection closes, a clean ack returns nil. If the
// receiver instead reboots before we've parsed the response we may see a
// transport error — we treat that as success on the rationale that the
// device clearly received the request (otherwise we'd have gotten a clean
// failure earlier). Debug logging surfaces it to the curious.
func newRebootCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "reboot",
		Short: "Request a system reboot of the receiver (system-wide; --zone is ignored)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			s := stateFromCmd(cmd)
			if s == nil {
				return errors.New("reboot: no state on context")
			}
			ctx := cmd.Context()

			yes, _ := cmd.Flags().GetBool("yes")
			if !yes {
				return newUsageError("reboot is destructive; pass --yes to confirm")
			}

			err := runWithRediscover(ctx, s, func(c *yxc.Client) error {
				return c.RequestSystemReboot(ctx)
			})
			if err != nil {
				// Receivers commonly drop the TCP connection mid-reboot
				// after acknowledging the request. Treat post-ack
				// transport errors as success — the reboot took.
				if yxc.IsTransport(err) {
					if s.debug != nil && s.debug.Enabled() {
						s.debug.Tracef("→ reboot transport error after ack (treating as success): %v", err)
					}
					return printResult(cmd, map[string]any{})
				}
				return err
			}
			return printResult(cmd, map[string]any{})
		},
	}
	cmd.Flags().Bool("yes", false, "confirm the destructive reboot operation")
	return cmd
}
