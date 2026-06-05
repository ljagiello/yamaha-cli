package cli

import (
	"context"
	"errors"
	"math"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/ljagiello/yamaha-cli/pkg/yxc"
)

func newVolumeCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "volume <int|±N|up|down>",
		Short: "Set volume (absolute integer, signed delta, or up/down)",
		Args:  cobra.ExactArgs(1),
		RunE:  runVolume,
	}
	cmd.Flags().Bool("db", false, "interpret the argument as decibels (absolute only)")
	cmd.Flags().Bool("percent", false, "interpret the argument as a 0..100 percentage (absolute only)")
	cmd.Flags().Int("step", 0, "override the step for up/down/+N/-N (default: device step)")
	return cmd
}

func runVolume(cmd *cobra.Command, args []string) error {
	s := stateFromCmd(cmd)
	if s == nil {
		return errors.New("volume: no state on context")
	}
	ctx := cmd.Context()

	dbFlag, _ := cmd.Flags().GetBool("db")
	percentFlag, _ := cmd.Flags().GetBool("percent")
	stepFlag, _ := cmd.Flags().GetInt("step")

	if dbFlag && percentFlag {
		return newUsageError("--db and --percent are mutually exclusive")
	}

	raw := strings.TrimSpace(args[0])
	arg, err := parseVolumeArg(s, ctx, raw, dbFlag, percentFlag, stepFlag)
	if err != nil {
		return err
	}

	err = runWithRediscover(ctx, s, func(c *yxc.Client) error {
		return c.SetVolume(ctx, s.zone, arg)
	})
	if err != nil {
		return err
	}
	return printResult(cmd, map[string]any{})
}

// parseVolumeArg turns the user's positional + flag combo into a
// yxc.VolumeArg. Combining --db/--percent with up/down/+N/-N is a usage
// error ("Volume command").
func parseVolumeArg(s *state, ctx context.Context, raw string, dbFlag, percentFlag bool, stepFlag int) (yxc.VolumeArg, error) {
	switch raw {
	case "up", "UP", "Up":
		if dbFlag || percentFlag {
			return yxc.VolumeArg{}, newUsageError("--db/--percent only apply to absolute values")
		}
		return yxc.VolumeUp(stepIfPositive(stepFlag)), nil
	case "down", "DOWN", "Down":
		if dbFlag || percentFlag {
			return yxc.VolumeArg{}, newUsageError("--db/--percent only apply to absolute values")
		}
		return yxc.VolumeDown(stepIfPositive(stepFlag)), nil
	}

	// Signed deltas: leading + or -.
	if len(raw) > 1 && (raw[0] == '+' || raw[0] == '-') {
		if dbFlag || percentFlag {
			return yxc.VolumeArg{}, newUsageError("--db/--percent only apply to absolute values")
		}
		n, err := strconv.Atoi(raw)
		if err != nil {
			return yxc.VolumeArg{}, newUsageError("invalid signed volume %q", raw)
		}
		step := absInt(n)
		if stepFlag > 0 {
			step = stepFlag
		}
		if n >= 0 {
			return yxc.VolumeUp(step), nil
		}
		return yxc.VolumeDown(step), nil
	}

	// Absolute. May be int (with --db / --percent / plain) or float (with
	// --db). Resolve the integer wire value via the device's range.
	feats, err := loadFeatures(ctx, s, false)
	if err != nil {
		return yxc.VolumeArg{}, err
	}
	min, max, _, ok := feats.VolumeRange(s.zone)
	if !ok {
		return yxc.VolumeArg{}, errors.New("volume: device features missing volume range_step")
	}

	if dbFlag {
		f, ferr := strconv.ParseFloat(raw, 64)
		if ferr != nil {
			return yxc.VolumeArg{}, newUsageError("invalid db value %q", raw)
		}
		// Inverse of Features.VolumeIntToDB, sharing the same dB scale
		// (device range_step, or the RX-V fallback when absent).
		n := feats.VolumeDBToInt(s.zone, f)
		return yxc.VolumeAbsolute(clampInt(n, min, max)), nil
	}
	if percentFlag {
		f, ferr := strconv.ParseFloat(raw, 64)
		if ferr != nil {
			return yxc.VolumeArg{}, newUsageError("invalid percent value %q", raw)
		}
		if f < 0 || f > 100 {
			return yxc.VolumeArg{}, newUsageError("--percent must be in [0,100]")
		}
		n := min + int(math.Round(f/100*float64(max-min)))
		return yxc.VolumeAbsolute(clampInt(n, min, max)), nil
	}

	n, err2 := strconv.Atoi(raw)
	if err2 != nil {
		return yxc.VolumeArg{}, newUsageError("invalid volume %q (want integer, ±N, up, or down)", raw)
	}
	return yxc.VolumeAbsolute(clampInt(n, min, max)), nil
}

func clampInt(n, lo, hi int) int {
	if n < lo {
		return lo
	}
	if n > hi {
		return hi
	}
	return n
}

func stepIfPositive(n int) int {
	if n > 0 {
		return n
	}
	return 0 // tells yxc.VolumeUp/Down to omit the step parameter
}

func absInt(n int) int {
	if n < 0 {
		return -n
	}
	return n
}
