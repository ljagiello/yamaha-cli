package yxc

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"
)

// VolumeArg describes how to express a setVolume call. Construct one of
// VolumeAbsolute or VolumeStep — never both. The zero value is invalid.
type VolumeArg struct {
	// absolute, when set, is the integer volume to send as `volume=<n>`.
	absolute *int
	// dir, when set, is "up" or "down" (sent as `volume=up` etc.).
	dir string
	// step is included when non-zero alongside dir, as `step=<n>`.
	step int
}

// VolumeAbsolute is `setVolume?volume=<n>`. The caller is responsible for
// clamping n into the device's reported range.
func VolumeAbsolute(n int) VolumeArg {
	return VolumeArg{absolute: &n}
}

// VolumeUp builds `setVolume?volume=up[&step=N]`. Pass step=0 to omit
// the parameter (the receiver applies its default step).
func VolumeUp(step int) VolumeArg {
	return VolumeArg{dir: "up", step: step}
}

// VolumeDown builds `setVolume?volume=down[&step=N]`.
func VolumeDown(step int) VolumeArg {
	return VolumeArg{dir: "down", step: step}
}

// values renders the VolumeArg into url.Values. Returns an error if the
// argument is the zero value.
func (v VolumeArg) values() (url.Values, error) {
	out := url.Values{}
	switch {
	case v.absolute != nil:
		out.Set("volume", strconv.Itoa(*v.absolute))
	case v.dir == "up" || v.dir == "down":
		out.Set("volume", v.dir)
		if v.step > 0 {
			out.Set("step", strconv.Itoa(v.step))
		}
	default:
		return nil, errors.New("yxc: VolumeArg is empty (use VolumeAbsolute / VolumeUp / VolumeDown)")
	}
	return out, nil
}

// validZone normalises and validates a zone identifier (case-insensitive).
//
// It accepts the four canonical YXC zone ids — main, zone2, zone3, zone4 —
// rather than gating on a device-specific allowlist. Which zones a given
// receiver actually has is determined authoritatively by getFeatures
// (Features.Zone[] / system.zone_num) at the CLI layer, and the receiver
// itself rejects an unsupported zone with response_code != 0. Keeping this
// a pure syntactic normaliser is what lets zone3/zone4 work on larger
// receivers (AVENTAGE / RX-A) without touching every call site.
func validZone(zone string) (string, error) {
	switch strings.ToLower(zone) {
	case "main":
		return "main", nil
	case "zone2":
		return "zone2", nil
	case "zone3":
		return "zone3", nil
	case "zone4":
		return "zone4", nil
	default:
		return "", fmt.Errorf("yxc: invalid zone %q (want main|zone2|zone3|zone4)", zone)
	}
}

// GetStatus returns the current playback/volume state for the named zone.
func (c *Client) GetStatus(ctx context.Context, zone string) (*Status, error) {
	z, err := validZone(zone)
	if err != nil {
		return nil, err
	}
	raw, err := c.Do(ctx, z+"/getStatus", nil)
	if err != nil {
		return nil, err
	}
	var s Status
	if err := json.Unmarshal(raw, &s); err != nil {
		return nil, fmt.Errorf("yxc: %s/getStatus: %w", z, err)
	}
	return &s, nil
}

// SetPower sets the zone power. Accepts "on", "standby", or "toggle".
func (c *Client) SetPower(ctx context.Context, zone, power string) error {
	z, err := validZone(zone)
	if err != nil {
		return err
	}
	switch power {
	case "on", "standby", "toggle":
	default:
		return fmt.Errorf("yxc: invalid power %q (want on|standby|toggle)", power)
	}
	v := url.Values{}
	v.Set("power", power)
	_, err = c.Do(ctx, z+"/setPower", v)
	return err
}

// SetVolume sets the zone volume per the supplied VolumeArg.
func (c *Client) SetVolume(ctx context.Context, zone string, vol VolumeArg) error {
	z, err := validZone(zone)
	if err != nil {
		return err
	}
	v, err := vol.values()
	if err != nil {
		return err
	}
	_, err = c.Do(ctx, z+"/setVolume", v)
	return err
}

// SetMute mutes or unmutes the zone.
func (c *Client) SetMute(ctx context.Context, zone string, on bool) error {
	z, err := validZone(zone)
	if err != nil {
		return err
	}
	v := url.Values{}
	if on {
		v.Set("enable", "true")
	} else {
		v.Set("enable", "false")
	}
	_, err = c.Do(ctx, z+"/setMute", v)
	return err
}

// PrepareInputChange issues `<zone>/prepareInputChange?input=<input>`.
// On RX-V583 this primes the receiver before a `setInput` call when the
// zone's func_list advertises "prepare_input_change".
func (c *Client) PrepareInputChange(ctx context.Context, zone, input string) error {
	z, err := validZone(zone)
	if err != nil {
		return err
	}
	if input == "" {
		return errors.New("yxc: PrepareInputChange: empty input")
	}
	v := url.Values{}
	v.Set("input", input)
	_, err = c.Do(ctx, z+"/prepareInputChange", v)
	return err
}

// SetInput switches the zone to the given input. If features is non-nil
// and the zone's func_list contains "prepare_input_change", a
// PrepareInputChange call is issued first. Pass features=nil to skip the
// auto-prepare (useful for the `raw` subcommand).
func (c *Client) SetInput(ctx context.Context, zone, input string, features *Features) error {
	z, err := validZone(zone)
	if err != nil {
		return err
	}
	if input == "" {
		return errors.New("yxc: SetInput: empty input")
	}
	if features != nil && features.ZoneHasFunc(z, "prepare_input_change") {
		if err := c.PrepareInputChange(ctx, z, input); err != nil {
			return err
		}
	}
	v := url.Values{}
	v.Set("input", input)
	_, err = c.Do(ctx, z+"/setInput", v)
	return err
}

// SetSoundProgram selects a DSP program for the zone (e.g. "standard",
// "straight", "2ch_stereo"). Validation against the zone's
// sound_program_list is the caller's responsibility (see pkg/yxc/validate).
func (c *Client) SetSoundProgram(ctx context.Context, zone, program string) error {
	z, err := validZone(zone)
	if err != nil {
		return err
	}
	if program == "" {
		return errors.New("yxc: SetSoundProgram: empty program")
	}
	v := url.Values{}
	v.Set("program", program)
	_, err = c.Do(ctx, z+"/setSoundProgram", v)
	return err
}

// SetSleep sets the sleep timer. Valid receiver values are 0 (off), 30,
// 60, 90, and 120 minutes; we send whatever the caller passes and let the
// device reject anything else (response_code != 0).
func (c *Client) SetSleep(ctx context.Context, zone string, minutes int) error {
	z, err := validZone(zone)
	if err != nil {
		return err
	}
	v := url.Values{}
	v.Set("sleep", strconv.Itoa(minutes))
	_, err = c.Do(ctx, z+"/setSleep", v)
	return err
}

// SetSurroundDecoder selects the zone's surround decoder type, e.g.
// "auto", "dolby_pl", "dts_neural_x". Validation against the zone's
// surr_decoder_type_list is the caller's responsibility (see
// pkg/yxc/validate).
func (c *Client) SetSurroundDecoder(ctx context.Context, zone, decoderType string) error {
	z, err := validZone(zone)
	if err != nil {
		return err
	}
	if decoderType == "" {
		return errors.New("yxc: SetSurroundDecoder: empty decoderType")
	}
	v := url.Values{}
	v.Set("type", decoderType)
	_, err = c.Do(ctx, z+"/setSurroundDecoderType", v)
	return err
}

// RecallScene recalls scene number num for the named zone. Scene numbers
// are 1-indexed; the upper bound is the zone's scene_num from getFeatures.
func (c *Client) RecallScene(ctx context.Context, zone string, num int) error {
	z, err := validZone(zone)
	if err != nil {
		return err
	}
	if num < 1 {
		return fmt.Errorf("yxc: RecallScene: num must be >= 1, got %d", num)
	}
	v := url.Values{}
	v.Set("num", strconv.Itoa(num))
	_, err = c.Do(ctx, z+"/recallScene", v)
	return err
}

// ToneControlArg describes a setToneControl call.
//
// Use the constructor New* helpers (or build a struct literal) to set
// only the fields you want to change; nil pointers for Bass/Treble are
// omitted from the request, an empty Mode is omitted. The receiver
// accepts partial forms (mode-only, bass-only, etc.).
//
// At least one of Mode/Bass/Treble must be non-empty/non-nil; the zero
// value is rejected to avoid no-op requests.
type ToneControlArg struct {
	// Mode is typically "manual" or "auto". Empty omits the parameter.
	Mode string
	// Bass is the bass level in the zone's tone-control range
	// (typically -12..+12). Nil omits the parameter.
	Bass *int
	// Treble is the treble level. Nil omits the parameter.
	Treble *int
}

// SetToneControl sets bass/treble tone for the named zone.
//
// Pass a ToneControlArg with only the fields you want to change (the
// receiver supports partial updates: mode-only, bass-only, treble-only,
// or any combination). The zero value is rejected.
//
// Use IntPtr to construct *int values inline.
func (c *Client) SetToneControl(ctx context.Context, zone string, arg ToneControlArg) error {
	z, err := validZone(zone)
	if err != nil {
		return err
	}
	if arg.Mode == "" && arg.Bass == nil && arg.Treble == nil {
		return errors.New("yxc: SetToneControl: at least one of mode/bass/treble must be set")
	}
	v := url.Values{}
	if arg.Mode != "" {
		v.Set("mode", arg.Mode)
	}
	if arg.Bass != nil {
		v.Set("bass", strconv.Itoa(*arg.Bass))
	}
	if arg.Treble != nil {
		v.Set("treble", strconv.Itoa(*arg.Treble))
	}
	_, err = c.Do(ctx, z+"/setToneControl", v)
	return err
}

// IntPtr returns a pointer to n. Convenience for ToneControlArg literals.
func IntPtr(n int) *int { return &n }

// setZoneEnable issues a boolean `<zone>/<method>?enable=true|false` call.
// It backs the on/off DSP switches (Pure Direct, Enhancer, Extra Bass,
// Adaptive DRC). Callers are responsible for checking that the zone
// advertises the corresponding func in getFeatures — the receiver returns
// response_code != 0 for one it doesn't support.
func (c *Client) setZoneEnable(ctx context.Context, zone, method string, on bool) error {
	z, err := validZone(zone)
	if err != nil {
		return err
	}
	v := url.Values{}
	v.Set("enable", strconv.FormatBool(on))
	_, err = c.Do(ctx, z+"/"+method, v)
	return err
}

// SetPureDirect toggles Pure Direct mode for the zone (func_list "direct").
func (c *Client) SetPureDirect(ctx context.Context, zone string, on bool) error {
	return c.setZoneEnable(ctx, zone, "setPureDirect", on)
}

// SetEnhancer toggles the Compressed Music Enhancer (func_list "enhancer").
func (c *Client) SetEnhancer(ctx context.Context, zone string, on bool) error {
	return c.setZoneEnable(ctx, zone, "setEnhancer", on)
}

// SetExtraBass toggles Extra Bass (func_list "extra_bass"). Present on
// newer / larger receivers; func-gated at the CLI so it only surfaces
// where supported.
func (c *Client) SetExtraBass(ctx context.Context, zone string, on bool) error {
	return c.setZoneEnable(ctx, zone, "setExtraBass", on)
}

// SetAdaptiveDRC toggles Adaptive DRC / dynamic-range compression
// (func_list "adaptive_drc").
func (c *Client) SetAdaptiveDRC(ctx context.Context, zone string, on bool) error {
	return c.setZoneEnable(ctx, zone, "setAdaptiveDrc", on)
}
