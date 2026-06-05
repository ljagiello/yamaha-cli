package cli

import (
	"errors"

	"github.com/spf13/cobra"

	"github.com/ljagiello/yamaha-cli/pkg/ynca"
)

// yncaZoneSwitch is the YNCA twin of yxc's zoneSwitch: a boolean on/off DSP
// control exposed as a top-level command. Unlike the YXC side — where each
// toggle has a dedicated typed setter — every YNCA toggle is the same
// PUT @<zone>:<FUNCTION>=<On|Off|Auto> shape, so one struct + one generic
// SetZoneSwitch call covers them all.
//
// onValue exists because the "on" spelling is per-function: most toggles use
// "On", but EXBASS/ADAPTIVEDRC/3DCINEMA enable with "Auto". Carrying it on the
// entry keeps SetZoneSwitch's caller honest without a per-function switch here.
type yncaZoneSwitch struct {
	name    string // CLI command name, e.g. "pure-direct"
	fn      string // wire FUNCTION token gating/targeting the control
	onValue string // value written for "on" — "On" for most, "Auto" for some
	short   string
}

// yncaZoneSwitches enumerates the boolean DSP toggles a YNCA-only receiver
// exposes. The order mirrors yxc's zoneSwitches so the two backends present
// the same command surface; the extra three (straight/surround-ai/3d-cinema)
// have no YXC twin and are reachable only here.
var yncaZoneSwitches = []yncaZoneSwitch{
	{
		name:    "pure-direct",
		fn:      ynca.FuncPureDirMode,
		onValue: "On",
		short:   "Toggle Pure Direct for the active zone over YNCA",
	},
	{
		name:    "enhancer",
		fn:      ynca.FuncEnhancer,
		onValue: "On",
		short:   "Toggle the Compressed Music Enhancer for the active zone over YNCA",
	},
	{
		name:    "extra-bass",
		fn:      ynca.FuncExBass,
		onValue: "Auto", // EXBASS enables with Auto, not On
		short:   "Toggle Extra Bass for the active zone over YNCA",
	},
	{
		name:    "adaptive-drc",
		fn:      ynca.FuncAdaptiveDRC,
		onValue: "Auto", // ADAPTIVEDRC enables with Auto, not On
		short:   "Toggle Adaptive DRC (dynamic range) for the active zone over YNCA",
	},
	{
		name:    "straight",
		fn:      ynca.FuncStraight,
		onValue: "On",
		short:   "Toggle Straight decode (bypass DSP) for the active zone over YNCA",
	},
	{
		name:    "surround-ai",
		fn:      ynca.FuncSurroundAI,
		onValue: "On",
		short:   "Toggle SURROUND:AI scene-adaptive processing over YNCA",
	},
	{
		name:    "3d-cinema",
		fn:      ynca.Func3DCinema,
		onValue: "Auto", // 3DCINEMA (CINEMA DSP 3D) enables with Auto, not On
		short:   "Toggle CINEMA DSP 3D height processing over YNCA",
	},
}

// newYncaDSPCmds builds one cobra command per boolean YNCA DSP toggle, so the
// integrator can fan them out under the `ynca` parent the same way
// newZoneSwitchCmds does for the YXC tree.
func newYncaDSPCmds() []*cobra.Command {
	cmds := make([]*cobra.Command, 0, len(yncaZoneSwitches))
	for _, sw := range yncaZoneSwitches {
		cmds = append(cmds, newYncaDSPCmd(sw))
	}
	return cmds
}

// newYncaDSPCmd builds the on|off command for a single toggle. We deliberately
// do NOT pre-check support against the device: YNCA has no feature manifest to
// consult cheaply, and a function the model lacks degrades to a device-side
// @RESTRICTED that friendlyYNCAError already turns into a readable message
// (exit 75). That lenient gate keeps this command working across the widest
// range of firmware without a per-model allow-list to maintain.
func newYncaDSPCmd(sw yncaZoneSwitch) *cobra.Command {
	return &cobra.Command{
		Use:   sw.name + " on|off",
		Short: sw.short,
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			s := stateFromCmd(cmd)
			if s == nil {
				return errors.New("ynca " + sw.name + ": no state on context")
			}
			ctx := cmd.Context()
			subunit := yncaSubunitForZone(s.zone)

			on, err := parseOnOff(args[0])
			if err != nil {
				return err
			}

			// Single PUT, run once without retry — runYNCASet settles the host
			// first (rediscover-safe probe) so a DHCP-shifted receiver is found
			// before the mutation, and makes the typed error friendly.
			err = runYNCASet(ctx, s, "@"+subunit+":"+sw.fn, func(c *ynca.Client) error {
				return c.SetZoneSwitch(ctx, subunit, sw.fn, sw.onValue, on)
			})
			if err != nil {
				return err
			}
			return printResult(cmd, map[string]any{})
		},
	}
}
