package cli

import (
	"context"
	"errors"
	"strings"

	"github.com/spf13/cobra"

	"github.com/ljagiello/yamaha-cli/pkg/yxc"
)

// zoneSwitch is a boolean on/off DSP control exposed as a top-level
// command. It is gated on the active zone advertising `fn` in its
// getFeatures func_list — mirroring how `tone` reads tone_control — so a
// control only ever acts on receivers that actually have it. These map to
// per-model YXC endpoints ynca models on the YNCA side but yamaha-cli
// previously only reached via the `raw` escape hatch.
type zoneSwitch struct {
	name  string // CLI command name, e.g. "pure-direct"
	fn    string // getFeatures func_list key gating the control
	short string
	set   func(ctx context.Context, c *yxc.Client, zone string, on bool) error
}

var zoneSwitches = []zoneSwitch{
	{
		name:  "pure-direct",
		fn:    "direct",
		short: "Toggle Pure Direct for the active zone",
		set: func(ctx context.Context, c *yxc.Client, zone string, on bool) error {
			return c.SetPureDirect(ctx, zone, on)
		},
	},
	{
		name:  "enhancer",
		fn:    "enhancer",
		short: "Toggle the Compressed Music Enhancer for the active zone",
		set: func(ctx context.Context, c *yxc.Client, zone string, on bool) error {
			return c.SetEnhancer(ctx, zone, on)
		},
	},
	{
		name:  "extra-bass",
		fn:    "extra_bass",
		short: "Toggle Extra Bass for the active zone",
		set: func(ctx context.Context, c *yxc.Client, zone string, on bool) error {
			return c.SetExtraBass(ctx, zone, on)
		},
	},
	{
		name:  "adaptive-drc",
		fn:    "adaptive_drc",
		short: "Toggle Adaptive DRC (dynamic range) for the active zone",
		set: func(ctx context.Context, c *yxc.Client, zone string, on bool) error {
			return c.SetAdaptiveDRC(ctx, zone, on)
		},
	},
}

// newZoneSwitchCmds builds one cobra command per boolean DSP switch.
func newZoneSwitchCmds() []*cobra.Command {
	cmds := make([]*cobra.Command, 0, len(zoneSwitches))
	for _, sw := range zoneSwitches {
		cmds = append(cmds, newZoneSwitchCmd(sw))
	}
	return cmds
}

func newZoneSwitchCmd(sw zoneSwitch) *cobra.Command {
	return &cobra.Command{
		Use:   sw.name + " on|off",
		Short: sw.short,
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			s := stateFromCmd(cmd)
			if s == nil {
				return errors.New(sw.name + ": no state on context")
			}
			ctx := cmd.Context()

			on, err := parseOnOff(args[0])
			if err != nil {
				return err
			}

			// Gate on getFeatures when we can read it: if the zone clearly
			// doesn't advertise the control, fail fast (exit 2) with a
			// readable message instead of a device-side response_code. If
			// features are unavailable (transport / sparse firmware), defer
			// to the receiver — same lenient policy as validateTunerFreq.
			if feats, ferr := loadFeatures(ctx, s, s.refreshFeats); ferr == nil && feats != nil && !feats.ZoneHasFunc(s.zone, sw.fn) {
				return newUsageError("%s is not supported on zone %q", sw.name, s.zone)
			}

			if err := runWithRediscover(ctx, s, func(c *yxc.Client) error {
				return sw.set(ctx, c, s.zone, on)
			}); err != nil {
				return err
			}
			return printResult(cmd, map[string]any{})
		},
	}
}

// parseOnOff accepts the common on/off spellings and returns the boolean
// the YXC `enable` parameter expects.
func parseOnOff(raw string) (bool, error) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "on", "true", "enable", "enabled", "1":
		return true, nil
	case "off", "false", "disable", "disabled", "0":
		return false, nil
	}
	return false, newUsageError("invalid on/off value %q (want on|off)", raw)
}
