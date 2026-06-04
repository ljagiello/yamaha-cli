package ynca

import (
	"context"
	"fmt"
	"math"
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

// Power is a subunit's PWR value.
type Power string

const (
	PowerOn      Power = "On"
	PowerStandby Power = "Standby"
)

// ParsePower normalises a wire PWR value (case-insensitive).
func ParsePower(v string) (Power, error) {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "on":
		return PowerOn, nil
	case "standby":
		return PowerStandby, nil
	}
	return "", fmt.Errorf("ynca: unrecognised power %q", v)
}

// volumeStep is the YNCA volume granularity in dB. The reference library
// rounds set values to this step; receivers report on the same grid.
const volumeStep = 0.5

// formatVolume renders a dB value onto the YNCA volume grid: rounded to
// the nearest 0.5 dB and printed with one decimal, normalising -0.0 to
// "0.0" (a port of ynca's number_to_string_with_stepsize).
func formatVolume(db float64) string {
	rounded := math.Round(db/volumeStep) * volumeStep
	if rounded == 0 {
		rounded = 0 // collapse a possible -0.0
	}
	return strconv.FormatFloat(rounded, 'f', 1, 64)
}

// parseMute maps a wire MUTE value to a boolean. YNCA reports "On"/"Off"
// and, on some models, attenuation levels ("Att -20 dB"); anything other
// than "Off" counts as muted.
func parseMute(v string) bool {
	return !strings.EqualFold(strings.TrimSpace(v), "off")
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

// GetMute reports whether the subunit is muted.
func (c *Client) GetMute(ctx context.Context, subunit string) (bool, error) {
	v, err := c.get(ctx, subunit, "MUTE")
	if err != nil {
		return false, err
	}
	return parseMute(v), nil
}

// SetMute mutes or unmutes the subunit.
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

// VolumeUp nudges the volume up one device step (@<subunit>:VOL=Up).
// Relative and therefore not idempotent — callers must not auto-retry it.
func (c *Client) VolumeUp(ctx context.Context, subunit string) error {
	return c.put(ctx, subunit, "VOL", "Up")
}

// VolumeDown nudges the volume down one device step.
func (c *Client) VolumeDown(ctx context.Context, subunit string) error {
	return c.put(ctx, subunit, "VOL", "Down")
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
type Status struct {
	Subunit   string
	Power     Power
	Volume    float64
	VolumeRaw string // raw wire value (kept when Volume couldn't be parsed)
	Mute      bool
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
	st := &Status{Subunit: subunit, Raw: make(map[string]string, len(lines))}
	for _, ln := range lines {
		su, fn, val, perr := parseLine(ln)
		if perr != nil || !strings.EqualFold(su, subunit) {
			continue
		}
		st.Raw[fn] = val
		switch strings.ToUpper(fn) {
		case "PWR":
			st.Power, _ = ParsePower(val)
		case "VOL":
			st.VolumeRaw = val
			if db, e := strconv.ParseFloat(strings.TrimSpace(val), 64); e == nil {
				st.Volume = db
			}
		case "MUTE":
			st.Mute = parseMute(val)
		case "INP":
			st.Input = val
		case "SOUNDPRG":
			st.SoundPrg = val
		}
	}
	return st, nil
}
