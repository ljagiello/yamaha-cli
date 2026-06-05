package ynca

import (
	"context"
	"fmt"
	"strconv"
	"strings"
)

// This file grows the YNCA client from a raw-line passthrough into a
// typed control layer, so the CLI can drive a legacy YNCA-only receiver
// (pre-MusicCast units that never spoke YXC) through the same kind of
// typed get/set surface the YXC client offers — instead of forcing
// callers to hand-build "@MAIN:PWR=On" strings.
//
// It is a deliberately small port of the ynca reference library's
// descriptor model: a value codec per function (Power/Mute/Volume/string)
// plus typed getters/setters built on Send and the multi-line SendMulti.

// Power is a subunit's PWR value. The typed enum surface (Mute, Input,
// SoundProgram, …) lives in enums.go; Power stays here next to its
// getter/setter for historical reasons. PowerUnknown is the sentinel a
// non-erroring parse maps an unrecognised value to, so Status can tell
// "not reported" apart from a real reading.
type Power string

const (
	PowerOn      Power = "On"
	PowerStandby Power = "Standby"
	PowerUnknown Power = UnknownValue
)

// Known reports whether p is one of the modelled power states.
func (p Power) Known() bool { return p == PowerOn || p == PowerStandby }

// ParsePower normalises a wire PWR value (case-insensitive). Returns an
// error on an unrecognised value — used by GetPower, where garbage should
// surface rather than be silently swallowed.
func ParsePower(v string) (Power, error) {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "on":
		return PowerOn, nil
	case "standby":
		return PowerStandby, nil
	}
	return "", fmt.Errorf("ynca: unrecognised power %q", v)
}

// ParsePowerState normalises a wire PWR value to a Power, mapping anything
// unrecognised to PowerUnknown (no error). Used when assembling Status,
// where an unexpected value should be preserved-as-unknown rather than
// abort the whole status read.
func ParsePowerState(v string) Power {
	if p, err := ParsePower(v); err == nil {
		return p
	}
	return PowerUnknown
}

// get issues "@<subunit>:<function>=?" and returns the parsed value.
func (c *Client) get(ctx context.Context, subunit, function string) (string, error) {
	reply, err := c.Send(ctx, "@"+subunit+":"+function+"=?")
	if err != nil {
		return "", err
	}
	_, _, value, perr := parseLine(reply)
	if perr != nil {
		return "", perr
	}
	return value, nil
}

// put issues "@<subunit>:<function>=<value>". The receiver echoes the new
// value, which we discard; typed errors (@RESTRICTED/@UNDEFINED) surface
// through Send.
func (c *Client) put(ctx context.Context, subunit, function, value string) error {
	_, err := c.Send(ctx, "@"+subunit+":"+function+"="+value)
	return err
}

// GetModelName reads the receiver's model name (@SYS:MODELNAME), e.g.
// "RX-V583".
func (c *Client) GetModelName(ctx context.Context) (string, error) {
	return c.get(ctx, SubunitSystem, FuncModelName)
}

// GetPower reads the subunit's power state.
func (c *Client) GetPower(ctx context.Context, subunit string) (Power, error) {
	v, err := c.get(ctx, subunit, "PWR")
	if err != nil {
		return "", err
	}
	return ParsePower(v)
}

// SetPower sets the subunit's power state.
func (c *Client) SetPower(ctx context.Context, subunit string, p Power) error {
	return c.put(ctx, subunit, "PWR", string(p))
}

// GetMuteState reads the subunit's precise MUTE state, preserving the
// attenuation levels (Att -20/-40 dB) that some RX-V receivers report.
func (c *Client) GetMuteState(ctx context.Context, subunit string) (Mute, error) {
	v, err := c.get(ctx, subunit, "MUTE")
	if err != nil {
		return MuteUnknown, err
	}
	return ParseMute(v), nil
}

// GetMute reports whether the subunit is muted (any non-Off state). Thin
// boolean convenience over GetMuteState, preserved for the toggle path and
// script consumers that only need yes/no.
func (c *Client) GetMute(ctx context.Context, subunit string) (bool, error) {
	m, err := c.GetMuteState(ctx, subunit)
	if err != nil {
		return false, err
	}
	return m.Muted(), nil
}

// SetMute mutes or unmutes the subunit. The device only accepts On/Off on
// write (the attenuation levels are read-back states, not settable), so the
// input stays a plain bool.
func (c *Client) SetMute(ctx context.Context, subunit string, on bool) error {
	v := "Off"
	if on {
		v = "On"
	}
	return c.put(ctx, subunit, "MUTE", v)
}

// GetVolume reads the subunit's volume in dB.
func (c *Client) GetVolume(ctx context.Context, subunit string) (float64, error) {
	v, err := c.get(ctx, subunit, "VOL")
	if err != nil {
		return 0, err
	}
	db, perr := strconv.ParseFloat(strings.TrimSpace(v), 64)
	if perr != nil {
		return 0, fmt.Errorf("ynca: unparseable volume %q: %w", v, perr)
	}
	return db, nil
}

// SetVolume sets an absolute volume in dB (rounded to the YNCA 0.5 dB grid).
func (c *Client) SetVolume(ctx context.Context, subunit string, db float64) error {
	return c.put(ctx, subunit, "VOL", formatVolume(db))
}

// VolumeUp nudges the volume up by step dB. YNCA accepts the explicit
// steps 1, 2 and 5 dB as "Up <n> dB"; any other value (including the 0.5 dB
// default) sends a bare "Up", which moves one device step. Relative and
// therefore not idempotent — callers must not auto-retry it. Mirrors ynca's
// do_vol_up.
func (c *Client) VolumeUp(ctx context.Context, subunit string, step float64) error {
	return c.put(ctx, subunit, "VOL", volNudge("Up", step))
}

// VolumeDown nudges the volume down by step dB (see VolumeUp for the step
// semantics).
func (c *Client) VolumeDown(ctx context.Context, subunit string, step float64) error {
	return c.put(ctx, subunit, "VOL", volNudge("Down", step))
}

// volNudge builds the relative VOL value for a nudge: "Up"/"Down" for the
// default step, or "Up <n> dB"/"Down <n> dB" for the device-supported whole
// steps 1, 2 and 5 dB.
func volNudge(dir string, step float64) string {
	switch step {
	case 1, 2, 5:
		return fmt.Sprintf("%s %d dB", dir, int(step))
	default:
		return dir
	}
}

// GetInput reads the subunit's selected input (e.g. "HDMI2").
func (c *Client) GetInput(ctx context.Context, subunit string) (string, error) {
	return c.get(ctx, subunit, "INP")
}

// SetInput selects the subunit's input.
func (c *Client) SetInput(ctx context.Context, subunit, input string) error {
	if strings.TrimSpace(input) == "" {
		return fmt.Errorf("ynca: SetInput: empty input")
	}
	return c.put(ctx, subunit, "INP", input)
}

// GetSoundProgram reads the subunit's DSP sound program.
func (c *Client) GetSoundProgram(ctx context.Context, subunit string) (string, error) {
	return c.get(ctx, subunit, "SOUNDPRG")
}

// SetSoundProgram selects the subunit's DSP sound program.
func (c *Client) SetSoundProgram(ctx context.Context, subunit, program string) error {
	if strings.TrimSpace(program) == "" {
		return fmt.Errorf("ynca: SetSoundProgram: empty program")
	}
	return c.put(ctx, subunit, "SOUNDPRG", program)
}

// Status is the decoded state of one subunit, assembled from a single
// "@<subunit>:BASIC=?" fan-out GET.
//
// Fields are initialised to their "unknown" sentinel (Power/MuteState) or
// left empty (Input/SoundPrg) so a caller can tell "the device didn't
// report this" apart from a real reading — mirroring ynca's read-through
// cache, which returns None for un-reported functions rather than a
// zero-value. VolumeRaw == "" is the presence flag for Volume (Go's float64
// zero is a legitimate dB reading).
type Status struct {
	Subunit   string
	Power     Power
	Volume    float64
	VolumeRaw string // raw wire value; "" means VOL was not reported
	Mute      bool   // convenience: MuteState.Muted()
	MuteState Mute   // precise state, incl. attenuation levels
	Input     string
	SoundPrg  string
	// Raw holds every FUNCTION=value line the BASIC GET returned for this
	// subunit, so callers can read fields this struct doesn't model.
	Raw map[string]string
}

// GetStatus issues a single "@<subunit>:BASIC=?" and decodes the fan-out
// of report lines into a Status. YNCA has no single status endpoint, so
// BASIC is the closest equivalent; SendMulti drains its reply with a
// version fence. Unknown/unsupported subunits surface as an error from
// SendMulti (EOF / @UNDEFINED line).
func (c *Client) GetStatus(ctx context.Context, subunit string) (*Status, error) {
	lines, err := c.SendMulti(ctx, "@"+subunit+":BASIC=?")
	if err != nil {
		return nil, err
	}
	st := &Status{
		Subunit:   subunit,
		Power:     PowerUnknown,
		MuteState: MuteUnknown,
		Raw:       make(map[string]string, len(lines)),
	}
	for _, ln := range lines {
		su, fn, val, perr := parseLine(ln)
		if perr != nil || !strings.EqualFold(su, subunit) {
			continue
		}
		st.Raw[fn] = val
		switch strings.ToUpper(fn) {
		case "PWR":
			st.Power = ParsePowerState(val)
		case "VOL":
			st.VolumeRaw = val
			if db, e := strconv.ParseFloat(strings.TrimSpace(val), 64); e == nil {
				st.Volume = db
			}
		case "MUTE":
			st.MuteState = ParseMute(val)
			st.Mute = st.MuteState.Muted()
		case "INP":
			st.Input = val
		case "SOUNDPRG":
			st.SoundPrg = val
		}
	}
	return st, nil
}
