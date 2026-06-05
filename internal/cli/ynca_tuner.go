package cli

import (
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/ljagiello/yamaha-cli/pkg/ynca"
)

// This file gives a YNCA-only receiver the AM/FM tuner command surface its
// YXC twin already has (`yamaha tuner …`). All of it acts on the fixed @TUN
// subunit, not the active zone, so these commands ignore --zone. Reads go
// through runYNCAWithRediscover (retry-safe); the tune/preset mutations go
// through runYNCASet (sent once, since a relative preset Up/Down is not
// idempotent).

// newYncaTunerCmd builds the `ynca tuner` parent and wires its subcommands,
// mirroring the parent/child shape of the YXC tuner command.
func newYncaTunerCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "tuner",
		Short: "Inspect and control the AM/FM tuner over YNCA",
		Args:  cobra.NoArgs,
	}
	cmd.AddCommand(newYncaTunerStatusCmd())
	cmd.AddCommand(newYncaTunerBandCmd())
	cmd.AddCommand(newYncaTunerFMCmd())
	cmd.AddCommand(newYncaTunerAMCmd())
	cmd.AddCommand(newYncaTunerPresetCmd())
	return cmd
}

func newYncaTunerStatusCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Print the tuner band, frequency, preset, and RDS info",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			s := stateFromCmd(cmd)
			if s == nil {
				return errors.New("ynca tuner status: no state on context")
			}
			ctx := cmd.Context()

			var st *ynca.TunerStatus
			var rds *ynca.RDSInfo
			// Both reads; safe to retry under rediscovery. RDS is best-effort
			// (AM has none; a station may broadcast none), so its error is
			// swallowed rather than failing the whole status.
			err := runYNCAWithRediscover(ctx, s, yncaSendTimeout, func(c *ynca.Client) error {
				got, e := c.GetTunerStatus(ctx)
				if e != nil {
					return e
				}
				st = got
				if r, re := c.GetRDSInfo(ctx); re == nil {
					rds = r
				}
				return nil
			})
			if err != nil {
				return friendlyYNCAError("@"+ynca.SubunitTuner+":BAND=?", err)
			}
			return printResult(cmd, buildYNCATunerPayload(st, rds))
		},
	}
}

func newYncaTunerBandCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "band <am|fm>",
		Short: "Select the tuner band over YNCA",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			s := stateFromCmd(cmd)
			if s == nil {
				return errors.New("ynca tuner band: no state on context")
			}
			ctx := cmd.Context()
			band := strings.ToLower(strings.TrimSpace(args[0]))
			if band != "am" && band != "fm" {
				return newUsageError("invalid band %q (want am|fm)", args[0])
			}
			if err := runYNCASet(ctx, s, "@"+ynca.SubunitTuner+":"+ynca.FuncBand, func(c *ynca.Client) error {
				return c.SetBand(ctx, band)
			}); err != nil {
				return err
			}
			return printResult(cmd, map[string]any{})
		},
	}
}

func newYncaTunerFMCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "fm <MHz>",
		Short: "Tune to an FM frequency in MHz (e.g. 98.5), over YNCA",
		Long: "Tune to an FM frequency in MHz. The value is aligned to the 0.2 MHz\n" +
			"grid the receiver uses; an out-of-range frequency surfaces a\n" +
			"device-side @RESTRICTED.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			s := stateFromCmd(cmd)
			if s == nil {
				return errors.New("ynca tuner fm: no state on context")
			}
			ctx := cmd.Context()
			mhz, err := strconv.ParseFloat(strings.TrimSpace(args[0]), 64)
			if err != nil {
				return newUsageError("invalid FM frequency %q (want MHz, e.g. 98.5)", args[0])
			}
			if err := runYNCASet(ctx, s, "@"+ynca.SubunitTuner+":"+ynca.FuncFMFreq, func(c *ynca.Client) error {
				return c.SetFMFreq(ctx, mhz)
			}); err != nil {
				return err
			}
			return printResult(cmd, map[string]any{})
		},
	}
}

func newYncaTunerAMCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "am <kHz>",
		Short: "Tune to an AM frequency in kHz (e.g. 1530), over YNCA",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			s := stateFromCmd(cmd)
			if s == nil {
				return errors.New("ynca tuner am: no state on context")
			}
			ctx := cmd.Context()
			khz, err := strconv.Atoi(strings.TrimSpace(args[0]))
			if err != nil {
				return newUsageError("invalid AM frequency %q (want integer kHz, e.g. 1530)", args[0])
			}
			if err := runYNCASet(ctx, s, "@"+ynca.SubunitTuner+":"+ynca.FuncAMFreq, func(c *ynca.Client) error {
				return c.SetAMFreq(ctx, khz)
			}); err != nil {
				return err
			}
			return printResult(cmd, map[string]any{})
		},
	}
}

func newYncaTunerPresetCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "preset <n|up|down>",
		Short: "Recall a tuner preset, or step to the next/previous, over YNCA",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			s := stateFromCmd(cmd)
			if s == nil {
				return errors.New("ynca tuner preset: no state on context")
			}
			ctx := cmd.Context()
			raw := strings.ToLower(strings.TrimSpace(args[0]))

			var op func(*ynca.Client) error
			switch raw {
			case "up":
				op = func(c *ynca.Client) error { return c.PresetUp(ctx) }
			case "down":
				op = func(c *ynca.Client) error { return c.PresetDown(ctx) }
			default:
				n, err := strconv.Atoi(raw)
				if err != nil {
					return newUsageError("invalid preset %q (want a number, up, or down)", args[0])
				}
				if n < 1 {
					return newUsageError("preset number must be >= 1, got %d", n)
				}
				op = func(c *ynca.Client) error { return c.RecallPreset(ctx, n) }
			}

			if err := runYNCASet(ctx, s, "@"+ynca.SubunitTuner+":"+ynca.FuncPreset, op); err != nil {
				return err
			}
			return printResult(cmd, map[string]any{})
		},
	}
}

// buildYNCATunerPayload renders a TunerStatus (+ optional RDS) as a stable
// map. The frequency is emitted both raw and human-formatted; RDS fields are
// included only when non-empty so AM / a station without RDS stays quiet.
func buildYNCATunerPayload(st *ynca.TunerStatus, rds *ynca.RDSInfo) map[string]any {
	out := map[string]any{
		"band": string(st.Band),
	}
	switch st.Band {
	case ynca.BandFM:
		out["freq_mhz"] = st.FreqMHz
		out["freq_human"] = fmt.Sprintf("%.2f MHz", st.FreqMHz)
	case ynca.BandAM:
		out["freq_khz"] = st.FreqKHz
		out["freq_human"] = fmt.Sprintf("%d kHz", st.FreqKHz)
	}
	if st.Preset != "" {
		out["preset"] = st.Preset
	}
	if rds != nil {
		if rds.ProgramService != "" {
			out["rds_station"] = rds.ProgramService
		}
		if rds.ProgramType != "" {
			out["rds_type"] = rds.ProgramType
		}
		if rds.RadioTextA != "" {
			out["rds_text_a"] = rds.RadioTextA
		}
		if rds.RadioTextB != "" {
			out["rds_text_b"] = rds.RadioTextB
		}
	}
	return out
}
