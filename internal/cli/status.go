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
	// firmware-side conversion); otherwise compute from the integer step
	// using the device's own dB range_step.
	feats, _ := loadFeaturesQuiet(ctx, s)
	if st.ActualVolume != nil {
		out["volume_db"] = st.ActualVolume.Value
	} else {
		out["volume_db"] = volumeIntToDB(feats, s.zone, st.Volume)
	}
	out["volume_percent"] = volumePercent(feats, s.zone, st.Volume)
	return out
}

// volumeIntToDB converts the YXC integer volume to dB.
//
// Prefers range_step{id:"actual_volume_db"} for (min, step) — keeps the
// conversion correct on receivers with a different baseline (e.g. some
// A-series go to -99.5). Falls back to the RX-V/A integer-step convention
// (-80.5 baseline, 0.5 dB step) only when features are unavailable.
func volumeIntToDB(feats *yxc.Features, zone string, n int) float64 {
	if feats != nil {
		if dbMin, _, dbStep, ok := feats.VolumeRangeDB(zone); ok {
			return dbMin + dbStep*float64(n)
		}
	}
	return -80.5 + 0.5*float64(n)
}

// volumePercent converts the integer volume to a 0..100 percentage using
// the device's reported (min, max) from getFeatures. Falls back to a
// safe constant when features aren't loaded.
func volumePercent(feats *yxc.Features, zone string, n int) int {
	mn, mx := 0, 161
	if feats != nil {
		if a, b, _, ok := feats.VolumeRange(zone); ok && b > a {
			mn, mx = a, b
		}
	}
	span := mx - mn
	if span <= 0 {
		return 0
	}
	pct := (100 * (n - mn)) / span
	if pct < 0 {
		pct = 0
	}
	if pct > 100 {
		pct = 100
	}
	return pct
}
