package yxc

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strconv"
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

// validZone normalises and validates a zone identifier. Accepts "main"
// or "zone2" (case-insensitive).
func validZone(zone string) (string, error) {
	switch zone {
	case "main", "Main", "MAIN":
		return "main", nil
	case "zone2", "Zone2", "ZONE2":
		return "zone2", nil
	default:
		return "", fmt.Errorf("yxc: invalid zone %q (want %q or %q)", zone, "main", "zone2")
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
