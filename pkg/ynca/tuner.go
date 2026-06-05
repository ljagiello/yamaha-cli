package ynca

import (
	"context"
	"fmt"
	"strconv"
	"strings"
)

// This file adds AM/FM tuner control over the @TUN subunit — band,
// frequency, presets, and RDS — which a YNCA-only receiver exposes but the
// original backend never wrapped (a user could only `input TUNER` then
// hand-craft raw @TUN lines). It is a port of the ynca reference library's
// Tun subunit (subunits/tun.py). DAB/FM models use a separate @DAB subunit
// not modelled here.
//
// Frequencies use the same stepped-number codec as volume/tone: FM is MHz
// on a 0.2 MHz grid, AM is kHz on a 10 kHz grid (the steps ynca's
// FmFreqFunctionMixin / amfreq use).

// TunerStatus is a snapshot of the tuner: the active band plus the
// frequency and preset for that band. Raw holds the verbatim wire values
// read.
type TunerStatus struct {
	Band    Band
	FreqMHz float64 // populated when Band == BandFM
	FreqKHz int     // populated when Band == BandAM
	Preset  string  // raw preset value ("No Preset" when none)
	Raw     map[string]string
}

// RDSInfo is the Radio Data System metadata an FM station broadcasts,
// drained from the @TUN:RDSINFO fan-out group.
type RDSInfo struct {
	ProgramService string // station name (RDSPRGSERVICE)
	ProgramType    string // genre/type (RDSPRGTYPE)
	RadioTextA     string // scrolling text A (RDSTXTA)
	RadioTextB     string // scrolling text B (RDSTXTB)
	Raw            map[string]string
}

// GetBand reads the tuner band (AM/FM).
func (c *Client) GetBand(ctx context.Context) (Band, error) {
	v, err := c.get(ctx, SubunitTuner, FuncBand)
	if err != nil {
		return BandUnknown, err
	}
	return ParseBand(v), nil
}

// SetBand selects the tuner band. band is matched case-insensitively
// against AM/FM.
func (c *Client) SetBand(ctx context.Context, band string) error {
	b := ParseBand(band)
	if !b.Known() {
		return fmt.Errorf("ynca: SetBand: want am|fm, got %q", band)
	}
	return c.put(ctx, SubunitTuner, FuncBand, string(b))
}

// GetFMFreq reads the current FM frequency in MHz.
func (c *Client) GetFMFreq(ctx context.Context) (float64, error) {
	v, err := c.get(ctx, SubunitTuner, FuncFMFreq)
	if err != nil {
		return 0, err
	}
	f, perr := strconv.ParseFloat(strings.TrimSpace(v), 64)
	if perr != nil {
		return 0, fmt.Errorf("ynca: unparseable FM frequency %q: %w", v, perr)
	}
	return f, nil
}

// SetFMFreq tunes to an FM frequency in MHz, aligned to the 0.2 MHz grid.
// Selecting FM also requires the band to be FM; the receiver switches band
// automatically when an FM frequency is set on most models, but callers
// that want to be explicit should SetBand("fm") first.
func (c *Client) SetFMFreq(ctx context.Context, mhz float64) error {
	return c.put(ctx, SubunitTuner, FuncFMFreq, formatStepped(mhz, 2, 0.2))
}

// GetAMFreq reads the current AM frequency in kHz.
func (c *Client) GetAMFreq(ctx context.Context) (int, error) {
	v, err := c.get(ctx, SubunitTuner, FuncAMFreq)
	if err != nil {
		return 0, err
	}
	n, perr := strconv.Atoi(strings.TrimSpace(v))
	if perr != nil {
		return 0, fmt.Errorf("ynca: unparseable AM frequency %q: %w", v, perr)
	}
	return n, nil
}

// SetAMFreq tunes to an AM frequency in kHz, aligned to the 10 kHz grid.
func (c *Client) SetAMFreq(ctx context.Context, khz int) error {
	return c.put(ctx, SubunitTuner, FuncAMFreq, formatStepped(float64(khz), 0, 10))
}

// RecallPreset selects a stored tuner preset by number.
func (c *Client) RecallPreset(ctx context.Context, n int) error {
	if n < 1 {
		return fmt.Errorf("ynca: RecallPreset: preset number must be >= 1, got %d", n)
	}
	return c.put(ctx, SubunitTuner, FuncPreset, strconv.Itoa(n))
}

// PresetUp selects the next stored preset (relative; not idempotent).
func (c *Client) PresetUp(ctx context.Context) error {
	return c.put(ctx, SubunitTuner, FuncPreset, "Up")
}

// PresetDown selects the previous stored preset (relative; not idempotent).
func (c *Client) PresetDown(ctx context.Context) error {
	return c.put(ctx, SubunitTuner, FuncPreset, "Down")
}

// GetTunerStatus reads the active band and, for that band, the current
// frequency and preset. The band GET is authoritative (an @UNDEFINED there
// means the device has no @TUN subunit and surfaces as an error); the
// frequency and preset reads are best-effort so a model that doesn't answer
// one of them still yields a usable status.
func (c *Client) GetTunerStatus(ctx context.Context) (*TunerStatus, error) {
	bandRaw, err := c.get(ctx, SubunitTuner, FuncBand)
	if err != nil {
		return nil, err
	}
	st := &TunerStatus{Band: ParseBand(bandRaw), Raw: map[string]string{FuncBand: bandRaw}}
	switch st.Band {
	case BandFM:
		if v, e := c.get(ctx, SubunitTuner, FuncFMFreq); e == nil {
			st.Raw[FuncFMFreq] = v
			if f, pe := strconv.ParseFloat(strings.TrimSpace(v), 64); pe == nil {
				st.FreqMHz = f
			}
		}
	case BandAM:
		if v, e := c.get(ctx, SubunitTuner, FuncAMFreq); e == nil {
			st.Raw[FuncAMFreq] = v
			if n, pe := strconv.Atoi(strings.TrimSpace(v)); pe == nil {
				st.FreqKHz = n
			}
		}
	}
	if v, e := c.get(ctx, SubunitTuner, FuncPreset); e == nil {
		st.Preset = v
		st.Raw[FuncPreset] = v
	}
	return st, nil
}

// GetRDSInfo drains the @TUN:RDSINFO fan-out group into an RDSInfo. Empty
// fields are normal (a station may broadcast none of it, or the band is AM).
func (c *Client) GetRDSInfo(ctx context.Context) (*RDSInfo, error) {
	lines, err := c.SendMulti(ctx, "@"+SubunitTuner+":"+GroupRdsInfo+"=?")
	if err != nil {
		return nil, err
	}
	info := &RDSInfo{Raw: make(map[string]string, len(lines))}
	for _, ln := range lines {
		su, fn, val, perr := parseLine(ln)
		if perr != nil || !strings.EqualFold(su, SubunitTuner) {
			continue
		}
		info.Raw[fn] = val
		switch strings.ToUpper(fn) {
		case "RDSPRGSERVICE":
			info.ProgramService = val
		case "RDSPRGTYPE":
			info.ProgramType = val
		case "RDSTXTA":
			info.RadioTextA = val
		case "RDSTXTB":
			info.RadioTextB = val
		}
	}
	return info, nil
}
