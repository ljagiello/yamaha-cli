package cli

import (
	"context"
	"errors"
	"slices"

	"github.com/spf13/cobra"

	"github.com/ljagiello/yamaha-cli/pkg/ynca"
	"github.com/ljagiello/yamaha-cli/pkg/yxc"
)

// This file adds a fast model/capability snapshot for both backends.
// getFeatures/getDeviceInfo were plumbed but had no user command, so the
// only way to see what a receiver supports was `raw system/getFeatures`
// (~30 KiB of JSON). `yamaha info` distils that into a readable header plus
// the active zone's input/sound-program/decoder lists; `ynca info` does the
// equivalent for a legacy receiver by reading MODELNAME/VERSION and probing
// which zones (and the tuner) actually answer — which also validates --zone
// against the device instead of mapping zone3/4 blindly.

// newInfoCmd builds the top-level `yamaha info` (YXC) command.
func newInfoCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "info",
		Short: "Show device model/firmware and zone capabilities",
		Long: "info prints a compact model/firmware/device-id header plus, for the\n" +
			"active zone, the inputs, sound programs, surround decoders, scene\n" +
			"count, and volume range the receiver advertises (sourced from the\n" +
			"cached getFeatures). Use --all-zones to show every zone.",
		Args: cobra.NoArgs,
		RunE: runInfo,
	}
	cmd.Flags().Bool("all-zones", false, "show capabilities for every zone, not just the active one")
	return cmd
}

func runInfo(cmd *cobra.Command, _ []string) error {
	s := stateFromCmd(cmd)
	if s == nil {
		return errors.New("info: no state on context")
	}
	ctx := cmd.Context()
	allZones, _ := cmd.Flags().GetBool("all-zones")

	var di *yxc.DeviceInfo
	if err := runWithRediscover(ctx, s, func(c *yxc.Client) error {
		got, e := c.GetDeviceInfo(ctx)
		if e != nil {
			return e
		}
		di = got
		return nil
	}); err != nil {
		return err
	}

	out := map[string]any{
		"model_name":     di.ModelName,
		"device_id":      di.DeviceID,
		"system_version": di.SystemVersion.String(),
		"api_version":    di.APIVersion.String(),
	}

	// Features are best-effort: a sparse-firmware device still gets the
	// header above even if getFeatures is unavailable.
	if feats, ferr := loadFeatures(ctx, s, s.refreshFeats); ferr == nil && feats != nil {
		out["zone_num"] = feats.System.ZoneNum
		if allZones {
			zones := make([]map[string]any, 0, len(feats.Zone))
			for i := range feats.Zone {
				zones = append(zones, buildZoneInfoPayload(feats, feats.Zone[i].ID))
			}
			out["zones"] = zones
		} else {
			out["zone"] = buildZoneInfoPayload(feats, s.zone)
		}
	}
	return printResult(cmd, out)
}

// buildZoneInfoPayload renders one zone's advertised capabilities.
func buildZoneInfoPayload(feats *yxc.Features, zone string) map[string]any {
	z := feats.ZoneByID(zone)
	if z == nil {
		return map[string]any{"id": zone, "present": false}
	}
	payload := map[string]any{
		"id":             z.ID,
		"present":        true,
		"inputs":         z.InputList,
		"sound_programs": z.SoundProgramList,
	}
	if len(z.SurrDecoderTypeList) > 0 {
		payload["surround_decoders"] = z.SurrDecoderTypeList
	}
	if z.SceneNum > 0 {
		payload["scene_num"] = z.SceneNum
	}
	if min, max, step, ok := feats.VolumeRange(zone); ok {
		payload["volume_range"] = map[string]any{"min": min, "max": max, "step": step}
	}
	return payload
}

// newYncaInfoCmd builds `ynca info` for a legacy receiver.
func newYncaInfoCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "info",
		Short: "Show the receiver model, firmware, and present zones/tuner over YNCA",
		Long: "info reads the model name and firmware version and probes which\n" +
			"zones and the tuner actually answer, so you can see a legacy\n" +
			"receiver's real layout — and whether the requested --zone exists —\n" +
			"without guessing. All reads; nothing is changed.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			s := stateFromCmd(cmd)
			if s == nil {
				return errors.New("ynca info: no state on context")
			}
			ctx := cmd.Context()

			var payload map[string]any
			err := runYNCAWithRediscover(ctx, s, yncaSendTimeout, func(c *ynca.Client) error {
				p, e := collectYncaInfo(ctx, c, s.zone)
				if e != nil {
					return e
				}
				payload = p
				return nil
			})
			if err != nil {
				return friendlyYNCAError("@SYS:MODELNAME=?", err)
			}
			return printResult(cmd, payload)
		},
	}
}

// collectYncaInfo reads model/version and probes each candidate zone (plus
// the tuner) for presence by issuing a cheap GET and treating an @UNDEFINED
// reply as "absent". The requested zone is flagged so the user learns up
// front whether --zone maps to a real subunit on this model.
func collectYncaInfo(ctx context.Context, c *ynca.Client, requestedZone string) (map[string]any, error) {
	// MODELNAME is authoritative: if even this @UNDEFINEDs or errors at the
	// transport layer, surface it rather than returning a hollow payload.
	model, err := c.GetModelName(ctx)
	if err != nil {
		return nil, err
	}
	out := map[string]any{"model": model}
	if v, e := c.Probe(ctx); e == nil {
		out["version"] = v
	}

	zoneSubunits := map[string]string{
		"main":  ynca.SubunitMain,
		"zone2": ynca.SubunitZone2,
		"zone3": ynca.SubunitZone3,
		"zone4": ynca.SubunitZone4,
	}
	present := make([]string, 0, len(zoneSubunits))
	for _, zone := range []string{"main", "zone2", "zone3", "zone4"} {
		ok, perr := subunitPresent(ctx, c, zoneSubunits[zone], ynca.FuncPower)
		if perr != nil {
			return nil, perr // transport error: abort rather than mis-report
		}
		if ok {
			present = append(present, zone)
		}
	}
	out["zones"] = present

	tunerOK, perr := subunitPresent(ctx, c, ynca.SubunitTuner, ynca.FuncBand)
	if perr != nil {
		return nil, perr
	}
	out["tuner"] = tunerOK

	// Validate the requested zone against what actually answered. This is
	// advisory (absent ≠ definitively unsupported on every firmware), so we
	// report rather than fail.
	out["requested_zone"] = requestedZone
	out["requested_zone_present"] = slices.Contains(present, requestedZone)
	return out, nil
}

// subunitPresent reports whether a subunit exists by GET-probing one of its
// functions: a value (or an @RESTRICTED — the function exists but isn't
// allowed right now) means present; @UNDEFINED means the subunit/function
// pair doesn't exist on this device. A transport error is returned so the
// caller can abort rather than mis-report a zone as absent.
func subunitPresent(ctx context.Context, c *ynca.Client, subunit, function string) (bool, error) {
	_, err := c.Send(ctx, "@"+subunit+":"+function+"=?")
	if err == nil {
		return true, nil
	}
	var undef *ynca.ErrUndefinedCommand
	if errors.As(err, &undef) {
		return false, nil
	}
	var restricted *ynca.ErrRestricted
	if errors.As(err, &restricted) {
		return true, nil
	}
	return false, err
}
