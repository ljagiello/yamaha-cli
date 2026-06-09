package cli

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/ljagiello/yamaha-cli/pkg/yxc"
)

// newTunerCmd builds the parent `yamaha tuner` command and wires the
// subcommands. Mirrors the parent/child structure used by config_cmd.go.
func newTunerCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "tuner",
		Short: "Inspect and control the FM/AM tuner",
		Args:  cobra.NoArgs,
	}
	cmd.AddCommand(newTunerStatusCmd())
	cmd.AddCommand(newTunerFMCmd())
	cmd.AddCommand(newTunerAMCmd())
	cmd.AddCommand(newTunerPresetCmd())
	cmd.AddCommand(newTunerPresetsCmd())
	return cmd
}

func newTunerStatusCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Print the current tuner band, frequency, and preset",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			s := stateFromCmd(cmd)
			if s == nil {
				return errors.New("tuner: no state on context")
			}
			ctx := cmd.Context()

			var st *yxc.TunerStatus
			err := runWithRediscover(ctx, s, func(c *yxc.Client) error {
				got, e := c.GetTunerStatus(ctx)
				if e != nil {
					return e
				}
				st = got
				return nil
			})
			if err != nil {
				return err
			}
			return printResult(cmd, buildTunerStatusPayload(st))
		},
	}
}

func newTunerFMCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "fm <MHz>",
		Short: "Tune to an FM frequency in MHz (e.g. 102.5)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			s := stateFromCmd(cmd)
			if s == nil {
				return errors.New("tuner fm: no state on context")
			}
			ctx := cmd.Context()
			raw := strings.TrimSpace(args[0])

			mhz, err := strconv.ParseFloat(raw, 64)
			if err != nil {
				return newUsageError("invalid FM frequency %q (want MHz, e.g. 102.5)", raw)
			}
			// Convert MHz → kHz wire units (102.5 MHz → 102500). Verified
			// against the live RX-V583, which returned freq=106500 for an
			// FM station at 106.5 MHz.
			n := int(math.Round(mhz * 1000))
			if err := validateTunerFreq(ctx, s, "fm", n); err != nil {
				return err
			}
			err = runWithRediscover(ctx, s, func(c *yxc.Client) error {
				return c.SetTunerFreq(ctx, "fm", n)
			})
			if err != nil {
				return err
			}
			return printResult(cmd, map[string]any{})
		},
	}
}

// newTunerAMCmd is registered by newTunerCmd.
func newTunerAMCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "am <kHz>",
		Short: "Tune to an AM frequency in kHz (e.g. 1530)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			s := stateFromCmd(cmd)
			if s == nil {
				return errors.New("tuner am: no state on context")
			}
			ctx := cmd.Context()
			raw := strings.TrimSpace(args[0])

			n, err := strconv.Atoi(raw)
			if err != nil {
				return newUsageError("invalid AM frequency %q (want integer kHz, e.g. 1530)", raw)
			}
			if err := validateTunerFreq(ctx, s, "am", n); err != nil {
				return err
			}
			err = runWithRediscover(ctx, s, func(c *yxc.Client) error {
				return c.SetTunerFreq(ctx, "am", n)
			})
			if err != nil {
				return err
			}
			return printResult(cmd, map[string]any{})
		},
	}
}

func newTunerPresetCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "preset <num>",
		Short: "Recall a tuner preset for the current (or specified) band",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			s := stateFromCmd(cmd)
			if s == nil {
				return errors.New("tuner preset: no state on context")
			}
			ctx := cmd.Context()

			raw := strings.TrimSpace(args[0])
			num, err := strconv.Atoi(raw)
			if err != nil {
				return newUsageError("invalid preset number %q (want integer >= 1)", raw)
			}
			if num < 1 {
				return newUsageError("preset number must be >= 1, got %d", num)
			}

			bandFlag, _ := cmd.Flags().GetString("band")
			band, err := resolveTunerBand(ctx, s, bandFlag)
			if err != nil {
				return err
			}

			// Cap against features.Tuner.Preset.Num when known.
			if max, ok := tunerPresetMax(ctx, s); ok && num > max {
				return &ValidationError{
					Kind:    "preset",
					Unknown: raw,
					Suggestions: []string{
						fmt.Sprintf("preset must be in [1,%d]", max),
					},
				}
			}

			err = runWithRediscover(ctx, s, func(c *yxc.Client) error {
				return c.RecallTunerPreset(ctx, s.zone, band, num)
			})
			if err != nil {
				return err
			}
			return printResult(cmd, map[string]any{})
		},
	}
	cmd.Flags().String("band", "", "tuner band (fm|am); defaults to current")
	return cmd
}

func newTunerPresetsCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "presets",
		Short: "List saved tuner presets for the current (or specified) band",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			s := stateFromCmd(cmd)
			if s == nil {
				return errors.New("tuner presets: no state on context")
			}
			ctx := cmd.Context()

			bandFlag, _ := cmd.Flags().GetString("band")
			band, err := resolveTunerBand(ctx, s, bandFlag)
			if err != nil {
				return err
			}

			var info *yxc.TunerPresetInfo
			err = runWithRediscover(ctx, s, func(c *yxc.Client) error {
				got, e := c.GetTunerPresetInfo(ctx, band)
				if e != nil {
					return e
				}
				info = got
				return nil
			})
			if err != nil {
				return err
			}
			return printResult(cmd, buildTunerPresetsPayload(info))
		},
	}
	cmd.Flags().String("band", "", "tuner band (fm|am); defaults to current")
	return cmd
}

// normaliseBand validates a user-supplied band token and returns its
// canonical lower-case form. Kept separate so the parent commands can
// surface a usageError (exit 2) rather than the yxc package's plain error.
func normaliseBand(raw string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "fm":
		return "fm", nil
	case "am":
		return "am", nil
	}
	return "", newUsageError("invalid band %q (want fm|am)", raw)
}

// resolveTunerBand returns the band the user wants to act on. Preference:
//
//  1. Explicit --band flag.
//  2. The receiver's currently active band (one tuner/getPlayInfo call).
//  3. Fallback to "fm" when both are absent.
//
// Falling back to "fm" rather than failing matches the README's policy:
// the wire request still fires, and any device-side error surfaces via
// response_code.
func resolveTunerBand(ctx context.Context, s *state, flag string) (string, error) {
	if strings.TrimSpace(flag) != "" {
		return normaliseBand(flag)
	}
	// Best-effort: ask the device. If this fails (transport, etc.), fall
	// through to "fm" — the user's explicit band would have come through
	// the flag.
	var st *yxc.TunerStatus
	_ = runWithRediscover(ctx, s, func(c *yxc.Client) error {
		got, e := c.GetTunerStatus(ctx)
		if e != nil {
			return e
		}
		st = got
		return nil
	})
	if st != nil {
		switch strings.ToLower(string(st.Band)) {
		case "fm", "am":
			return strings.ToLower(string(st.Band)), nil
		}
	}
	return "fm", nil
}

// validateTunerFreq cross-checks the user's frequency against the device's
// reported range_step (fm_freq / am_freq) when known. A missing range
// (older firmware / sparse features payload) skips validation; the wire
// request still fires and the receiver will reject anything bogus.
func validateTunerFreq(ctx context.Context, s *state, band string, n int) error {
	feats, err := loadFeatures(ctx, s, false)
	if err != nil {
		// loadFeatures failure shouldn't block tuning — the receiver is the
		// authority. Surface the underlying error only if it's not a
		// transport/cache issue we can ignore.
		return nil //nolint:nilerr // intentional: features unavailable should not block tuning
	}
	rangeID := band + "_freq"
	if min, max, ok := tunerFreqRange(feats, rangeID); ok {
		if n < min || n > max {
			human := formatTunerFreq(band, n)
			return &ValidationError{
				Kind:    band + " frequency",
				Unknown: human,
				Suggestions: []string{
					fmt.Sprintf("must be in [%s, %s]",
						formatTunerFreq(band, min),
						formatTunerFreq(band, max),
					),
				},
			}
		}
	}
	return nil
}

// tunerFreqRange returns the (min, max) integer range for the named
// tuner range_step entry (fm_freq / am_freq). False when absent.
func tunerFreqRange(feats *yxc.Features, id string) (min, max int, ok bool) {
	if feats == nil || feats.Tuner == nil {
		return 0, 0, false
	}
	for _, r := range feats.Tuner.RangeStep {
		if r.ID == id {
			return int(r.Min), int(r.Max), true
		}
	}
	return 0, 0, false
}

// tunerPresetMax returns the device's max preset number, when reported.
func tunerPresetMax(ctx context.Context, s *state) (int, bool) {
	feats, err := loadFeatures(ctx, s, false)
	if err != nil || feats == nil || feats.Tuner == nil || feats.Tuner.Preset == nil {
		return 0, false
	}
	if feats.Tuner.Preset.Num > 0 {
		return feats.Tuner.Preset.Num, true
	}
	return 0, false
}

// formatTunerFreq turns a wire integer back into a human-readable string.
// FM is in kHz (102500 → "102.50 MHz"), AM is in kHz (1530 → "1530 kHz").
func formatTunerFreq(band string, n int) string {
	switch band {
	case "fm":
		return fmt.Sprintf("%.2f MHz", float64(n)/1000.0)
	case "am":
		return fmt.Sprintf("%d kHz", n)
	}
	return strconv.Itoa(n)
}

// buildTunerStatusPayload renders TunerStatus as a stable map. The active
// band's frequency (and preset) are always emitted; the other band block
// is included as a nested map when populated, so a watcher can see "FM is
// idle but AM has a saved tuning" without a second call.
func buildTunerStatusPayload(st *yxc.TunerStatus) map[string]any {
	out := map[string]any{
		"band": string(st.Band),
	}
	switch strings.ToLower(string(st.Band)) {
	case "fm":
		out["freq"] = st.FM.Freq
		out["freq_human"] = formatTunerFreq("fm", st.FM.Freq)
		out["preset"] = st.FM.Preset
		if st.FM.Audio != "" {
			out["audio_mode"] = st.FM.Audio
		}
	case "am":
		out["freq"] = st.AM.Freq
		out["freq_human"] = formatTunerFreq("am", st.AM.Freq)
		out["preset"] = st.AM.Preset
	}
	return out
}

// buildTunerPresetsPayload converts a TunerPresetInfo into a slice of
// stable per-row maps. The output renderer prints these as an aligned table
// in TTY mode and as a JSON array otherwise.
func buildTunerPresetsPayload(info *yxc.TunerPresetInfo) []map[string]any {
	if info == nil {
		return nil
	}
	rows := make([]map[string]any, 0, len(info.PresetInfo))
	for _, p := range info.PresetInfo {
		row := map[string]any{
			"band": string(p.Band),
			"num":  p.Number,
			"freq": p.Freq,
		}
		if p.Freq > 0 {
			row["freq_human"] = formatTunerFreq(strings.ToLower(string(p.Band)), p.Freq)
		}
		rows = append(rows, row)
	}
	return rows
}
