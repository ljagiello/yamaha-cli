package cli

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/ljagiello/yamaha-cli/pkg/yxc"
)

// newPresetCmd builds the parent `yamaha preset` command. Today this only
// covers NetUSB / MusicCast presets; the tuner has its own (band-aware)
// presets under `yamaha tuner preset`.
func newPresetCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "preset",
		Short: "List and recall NetUSB MusicCast presets",
		Args:  cobra.NoArgs,
	}
	cmd.AddCommand(newPresetListCmd())
	cmd.AddCommand(newPresetRecallCmd())
	return cmd
}

func newPresetListCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List saved NetUSB MusicCast presets",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			s := stateFromCmd(cmd)
			if s == nil {
				return errors.New("preset list: no state on context")
			}
			ctx := cmd.Context()

			var info *yxc.PresetInfo
			err := runWithRediscover(ctx, s, func(c *yxc.Client) error {
				got, e := c.GetPresetInfo(ctx)
				if e != nil {
					return e
				}
				info = got
				return nil
			})
			if err != nil {
				return err
			}
			return printResult(cmd, buildPresetListPayload(info))
		},
	}
}

func newPresetRecallCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "recall <num>",
		Short: "Recall a NetUSB MusicCast preset by 1-indexed slot",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			s := stateFromCmd(cmd)
			if s == nil {
				return errors.New("preset recall: no state on context")
			}
			ctx := cmd.Context()
			raw := strings.TrimSpace(args[0])
			num, err := strconv.Atoi(raw)
			if err != nil {
				return newUsageError("invalid preset number %q (want integer >= 1)", raw)
			}
			if err := validateNetUSBPreset(ctx, s, num); err != nil {
				return err
			}
			err = runWithRediscover(ctx, s, func(c *yxc.Client) error {
				return c.RecallNetUSBPreset(ctx, s.zone, num)
			})
			if err != nil {
				return err
			}
			return printResult(cmd, map[string]any{})
		},
	}
}

// validateNetUSBPreset bounds-checks the user's preset number against the
// device's reported max (features.NetUSB.Preset.Num). When the max is
// unknown (older firmware / sparse features) we still reject num<1 — the
// device would reject it too, but failing locally avoids a wasted
// round-trip.
func validateNetUSBPreset(ctx context.Context, s *state, num int) error {
	if num < 1 {
		return &ValidationError{
			Kind:    "preset",
			Unknown: strconv.Itoa(num),
			Suggestions: []string{
				"preset must be >= 1",
			},
		}
	}
	feats, err := loadFeatures(ctx, s, false)
	if err != nil {
		// Don't block recall on a features fetch failure — the device
		// is the authority. Surface the error from the actual recall
		// call instead.
		return nil //nolint:nilerr // intentional: features unavailable falls through
	}
	if feats == nil || feats.NetUSB == nil || feats.NetUSB.Preset == nil {
		return nil
	}
	max := feats.NetUSB.Preset.Num
	if max > 0 && num > max {
		return &ValidationError{
			Kind:    "preset",
			Unknown: strconv.Itoa(num),
			Suggestions: []string{
				fmt.Sprintf("preset must be in [1,%d]", max),
			},
		}
	}
	return nil
}

// buildPresetListPayload converts PresetInfo into a slice of stable
// per-row maps. Slot numbers are 1-indexed and follow the array order.
func buildPresetListPayload(info *yxc.PresetInfo) []map[string]any {
	if info == nil {
		return nil
	}
	rows := make([]map[string]any, 0, len(info.PresetInfo))
	for i, p := range info.PresetInfo {
		rows = append(rows, map[string]any{
			"num":   i + 1,
			"input": p.Input,
			"text":  p.Text,
		})
	}
	return rows
}
