package cli

import (
	"errors"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/ljagiello/yamaha-cli/pkg/ynca"
)

// newYncaSceneCmd is the YNCA twin of the YXC `scene` command. It folds two
// related operations under one verb because a scene is only useful if you
// know which numbers exist: with no argument it lists the zone's configured
// scene names (a read), and with a number it recalls that scene (a mutation).
//
// Listing is the no-arg default — rather than erroring on a missing argument —
// so a user who forgets the scene number gets the catalog instead of a usage
// slap, mirroring how the typed input/sound/decoder commands list their valid
// values. The arg form is the recall; everything routes through the
// zone→subunit mapping so --zone selects the target.
func newYncaSceneCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "scene [n]",
		Short: "Recall a scene for the zone over YNCA, or list scene names",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			s := stateFromCmd(cmd)
			if s == nil {
				return errors.New("ynca scene: no state on context")
			}
			ctx := cmd.Context()
			subunit := yncaSubunitForZone(s.zone)

			// No argument → list scene names. This is a pure read (a
			// SCENENAME fan-out query), so the whole op is safe to retry
			// under DHCP rediscovery via runYNCAWithRediscover.
			if len(args) == 0 {
				var names []ynca.SceneName
				err := runYNCAWithRediscover(ctx, s, yncaSendTimeout, func(c *ynca.Client) error {
					got, e := c.GetSceneNames(ctx, subunit)
					if e != nil {
						return e
					}
					names = got
					return nil
				})
				if err != nil {
					return friendlyYNCAError("@"+subunit+":SCENENAME=?", err)
				}
				// Emit one row per scene keyed by num+name rather than reusing
				// buildNameListPayload: scene numbers are load-bearing (you
				// recall by number, not by name), so the index must survive
				// into the output. An empty catalog still prints an empty list
				// so a script sees "no scenes" rather than null.
				rows := make([]map[string]any, 0, len(names))
				for _, n := range names {
					rows = append(rows, map[string]any{"num": n.Num, "name": n.Name})
				}
				return printResult(cmd, rows)
			}

			// Argument present → recall scene N. Parse first so a malformed
			// number fails as a usage error (exit 64) before any I/O, and
			// reject non-positive indices here since the device would answer
			// an out-of-range scene with an opaque @RESTRICTED.
			n, err := strconv.Atoi(strings.TrimSpace(args[0]))
			if err != nil {
				return newUsageError("invalid scene number %q (want an integer)", args[0])
			}
			if n < 1 {
				return newUsageError("scene number must be >= 1, got %d", n)
			}

			// RecallScene is a non-idempotent mutation, so route it through
			// runYNCASet (settle host, run once, friendly errors).
			if err := runYNCASet(ctx, s, "@"+subunit+":"+ynca.FuncScene, func(c *ynca.Client) error {
				return c.RecallScene(ctx, subunit, n)
			}); err != nil {
				return err
			}
			return printResult(cmd, map[string]any{})
		},
	}
}
