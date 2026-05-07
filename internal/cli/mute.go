package cli

import (
	"errors"
	"strings"

	"github.com/spf13/cobra"

	"github.com/ljagiello/yamaha-cli/pkg/yxc"
)

func newMuteCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "mute on|off|toggle",
		Short: "Mute, unmute, or toggle mute on the active zone",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			s := stateFromCmd(cmd)
			if s == nil {
				return errors.New("mute: no state on context")
			}
			ctx := cmd.Context()
			arg := strings.ToLower(strings.TrimSpace(args[0]))

			var on bool
			switch arg {
			case "on":
				on = true
			case "off":
				on = false
			case "toggle":
				// Need the current state. One getStatus, then flip.
				err := runWithRediscover(ctx, s, func(c *yxc.Client) error {
					st, e := c.GetStatus(ctx, s.zone)
					if e != nil {
						return e
					}
					on = !st.Mute
					return nil
				})
				if err != nil {
					return err
				}
			default:
				return newUsageError("invalid mute argument %q (want on|off|toggle)", arg)
			}

			err := runWithRediscover(ctx, s, func(c *yxc.Client) error {
				return c.SetMute(ctx, s.zone, on)
			})
			if err != nil {
				return err
			}
			return printResult(cmd, map[string]any{})
		},
	}
}
