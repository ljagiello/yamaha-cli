package cli

import (
	"context"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/ljagiello/yamaha-cli/pkg/yxc"
)

func newStatusCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Print zone power, input, volume, and mute state",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			s := stateFromCmd(cmd)
			if s == nil {
				return fmt.Errorf("status: no state on context")
			}
			ctx := cmd.Context()

			var status *yxc.Status
			err := runWithRediscover(ctx, s, func(c *yxc.Client) error {
				st, e := c.GetStatus(ctx, s.zone)
				if e != nil {
					return e
				}
				status = st
				return nil
			})
			if err != nil {
				return err
			}

			out := buildStatusPayload(ctx, s, status)
			return printResult(cmd, out)
		},
	}
}

// buildStatusPayload converts a *yxc.Status into the canonical map shape
// the CLI renders. Keeps field naming stable across JSON/YAML/table.
func buildStatusPayload(ctx context.Context, s *state, st *yxc.Status) map[string]any {
	out := map[string]any{
		"zone":   s.zone,
		"power":  st.Power,
		"input":  st.Input,
		"mute":   st.Mute,
		"volume": st.Volume,
	}
	if st.SoundProgram != "" {
		out["sound_program"] = st.SoundProgram
	}
	// dB and percent are derivable. Prefer the device-supplied
	// actual_volume when present (it already accounts for the
	// firmware-side conversion); otherwise compute from the integer step.
	if st.ActualVolume != nil {
		out["volume_db"] = st.ActualVolume.Value
	} else {
		out["volume_db"] = volumeIntToDB(st.Volume)
	}
	out["volume_percent"] = volumePercent(ctx, s, st.Volume)
	return out
}

// volumeIntToDB converts the YXC integer volume (0..161) to dB. One step
// is 0.5 dB and 0 is -80.5 dB, per the live-device readings recorded in
//
//	("Verified device capabilities").
func volumeIntToDB(n int) float64 {
	return -80.5 + 0.5*float64(n)
}

// volumePercent converts the integer volume to a 0..100 percentage using
// the device's reported max from getFeatures. Falls back to /161 when the
// max isn't available (e.g. features fetch failed earlier in the flow).
func volumePercent(ctx context.Context, s *state, n int) int {
	max := 161
	if feats, err := loadFeaturesQuiet(ctx, s); err == nil && feats != nil {
		if _, m, _, ok := feats.VolumeRange(s.zone); ok && m > 0 {
			max = m
		}
	}
	if max <= 0 {
		return 0
	}
	pct := (100 * n) / max
	if pct < 0 {
		pct = 0
	}
	if pct > 100 {
		pct = 100
	}
	return pct
}
