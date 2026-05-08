package cli

import (
	"errors"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/ljagiello/yamaha-cli/pkg/yxc"
)

func newSceneCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "scene <1-N>",
		Short: "Recall scene number N for the active zone",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			s := stateFromCmd(cmd)
			if s == nil {
				return errors.New("scene: no state on context")
			}
			ctx := cmd.Context()
			raw := strings.TrimSpace(args[0])

			n, err := strconv.Atoi(raw)
			if err != nil {
				return newUsageError("invalid scene number %q (want an integer)", raw)
			}

			feats, err := loadFeatures(ctx, s, s.refreshFeats)
			if err != nil {
				return err
			}
			z := feats.ZoneByID(s.zone)
			if z == nil {
				// One refresh on miss in case the cache is older than the
				// zone catalog (e.g. firmware added zone2).
				feats, err = loadFeatures(ctx, s, true)
				if err != nil {
					return err
				}
				z = feats.ZoneByID(s.zone)
			}
			if z == nil {
				return newUsageError("zone %q not found in device features", s.zone)
			}
			if z.SceneNum <= 0 {
				return newUsageError("zone %q does not advertise any scenes", s.zone)
			}
			if n < 1 || n > z.SceneNum {
				return &ValidationError{
					Kind:    "scene",
					Unknown: raw,
					Suggestions: []string{
						"1.." + strconv.Itoa(z.SceneNum),
					},
				}
			}

			err = runWithRediscover(ctx, s, func(c *yxc.Client) error {
				return c.RecallScene(ctx, s.zone, n)
			})
			if err != nil {
				return err
			}
			return printResult(cmd, map[string]any{})
		},
	}
}
