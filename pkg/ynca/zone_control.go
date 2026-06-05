package ynca

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// This file extends the typed control layer with the zone functions the
// original backend lacked but the YXC backend already exposes: surround
// decoder, tone (bass/treble), sleep timer, scene recall, and the boolean
// DSP toggles. Each is a thin port of the corresponding ynca reference
// function on ZoneBase (subunits/zone.py). System power reuses the existing
// GetPower/SetPower against the "SYS" subunit, so it needs nothing here.

// GetDecoder reads the zone's 2-channel surround decoder (@<zone>:2CHDECODER).
func (c *Client) GetDecoder(ctx context.Context, subunit string) (TwoChDecoder, error) {
	v, err := c.get(ctx, subunit, Func2ChDecoder)
	if err != nil {
		return TwoChDecoderUnknown, err
	}
	return ParseTwoChDecoder(v), nil
}

// SetDecoder selects the zone's 2-channel surround decoder. The value is
// sent verbatim; an unsupported decoder surfaces a device-side @RESTRICTED
// rather than failing client-side, since the valid set is model-specific.
func (c *Client) SetDecoder(ctx context.Context, subunit, decoder string) error {
	if strings.TrimSpace(decoder) == "" {
		return fmt.Errorf("ynca: SetDecoder: empty decoder")
	}
	return c.put(ctx, subunit, Func2ChDecoder, decoder)
}

// GetTone reads a zone tone control in dB. function is FuncSpBass or
// FuncSpTreble.
func (c *Client) GetTone(ctx context.Context, subunit, function string) (float64, error) {
	v, err := c.get(ctx, subunit, function)
	if err != nil {
		return 0, err
	}
	db, perr := strconv.ParseFloat(strings.TrimSpace(v), 64)
	if perr != nil {
		return 0, fmt.Errorf("ynca: unparseable tone %q: %w", v, perr)
	}
	return db, nil
}

// SetTone sets a zone tone control (FuncSpBass/FuncSpTreble) in dB, rounded
// to the YNCA 0.5 dB grid. The receiver clamps to its own supported range
// (no client-side range gate, since it varies by model).
func (c *Client) SetTone(ctx context.Context, subunit, function string, db float64) error {
	return c.put(ctx, subunit, function, formatStepped(db, 1, volumeStep))
}

// sleepMinutes maps a wire SLEEP value (Off/30 min/…) to a minute count and
// reports whether it was recognised.
func sleepMinutes(wire string) (int, bool) {
	switch strings.TrimSpace(wire) {
	case "Off":
		return 0, true
	case "30 min":
		return 30, true
	case "60 min":
		return 60, true
	case "90 min":
		return 90, true
	case "120 min":
		return 120, true
	}
	return 0, false
}

// sleepWire maps a minute count to the SLEEP wire value, and reports whether
// it is a value the receiver accepts (Off/30/60/90/120).
func sleepWire(minutes int) (string, bool) {
	switch minutes {
	case 0:
		return "Off", true
	case 30, 60, 90, 120:
		return fmt.Sprintf("%d min", minutes), true
	}
	return "", false
}

// GetSleep reads the zone's sleep timer in minutes (0 == Off). A wire value
// outside the known set returns an error so a caller can surface it rather
// than silently coerce.
func (c *Client) GetSleep(ctx context.Context, subunit string) (int, error) {
	v, err := c.get(ctx, subunit, FuncSleep)
	if err != nil {
		return 0, err
	}
	if m, ok := sleepMinutes(v); ok {
		return m, nil
	}
	return 0, fmt.Errorf("ynca: unrecognised sleep value %q", v)
}

// SetSleep sets the zone's sleep timer (0/30/60/90/120 minutes; 0 == Off).
func (c *Client) SetSleep(ctx context.Context, subunit string, minutes int) error {
	wire, ok := sleepWire(minutes)
	if !ok {
		return fmt.Errorf("ynca: SetSleep: minutes must be one of 0/30/60/90/120, got %d", minutes)
	}
	return c.put(ctx, subunit, FuncSleep, wire)
}

// RecallScene recalls a numbered scene for the zone (@<zone>:SCENE=Scene N).
// The scene index is validated as positive client-side; the device rejects
// an out-of-range index with @RESTRICTED.
func (c *Client) RecallScene(ctx context.Context, subunit string, n int) error {
	if n < 1 {
		return fmt.Errorf("ynca: RecallScene: scene number must be >= 1, got %d", n)
	}
	return c.put(ctx, subunit, FuncScene, fmt.Sprintf("Scene %d", n))
}

// SceneName pairs a scene number with its user-assigned name.
type SceneName struct {
	Num  int
	Name string
}

// GetSceneNames reads the zone's configured scene names via the SCENENAME
// fan-out group, returning them ordered by scene number. Receivers that
// don't model scene names answer with no SCENE<n>NAME lines, yielding an
// empty slice (not an error).
func (c *Client) GetSceneNames(ctx context.Context, subunit string) ([]SceneName, error) {
	lines, err := c.SendMulti(ctx, "@"+subunit+":"+GroupSceneName+"=?")
	if err != nil {
		return nil, err
	}
	var names []SceneName
	for _, ln := range lines {
		su, fn, val, perr := parseLine(ln)
		if perr != nil || !strings.EqualFold(su, subunit) {
			continue
		}
		if n, ok := sceneNameIndex(fn); ok {
			names = append(names, SceneName{Num: n, Name: val})
		}
	}
	sort.Slice(names, func(i, j int) bool { return names[i].Num < names[j].Num })
	return names, nil
}

// sceneNameIndex extracts the scene number from a "SCENE<n>NAME" function
// token, e.g. "SCENE3NAME" → 3.
func sceneNameIndex(fn string) (int, bool) {
	u := strings.ToUpper(fn)
	if !strings.HasPrefix(u, "SCENE") || !strings.HasSuffix(u, "NAME") {
		return 0, false
	}
	mid := u[len("SCENE") : len(u)-len("NAME")]
	n, err := strconv.Atoi(mid)
	if err != nil || n < 1 {
		return 0, false
	}
	return n, true
}

// GetZoneSwitch reads a boolean-ish DSP toggle (e.g. PUREDIRMODE, ENHANCER,
// EXBASS) as its raw wire value. Callers interpret "Off" as off and any
// other recognised value (On/Auto) as on.
func (c *Client) GetZoneSwitch(ctx context.Context, subunit, function string) (string, error) {
	return c.get(ctx, subunit, function)
}

// SetZoneSwitch sets a boolean DSP toggle. Because the "on" spelling differs
// per function — most use "On", but EXBASS/ADAPTIVEDRC/3DCINEMA use "Auto" —
// the caller supplies the onValue; off is always "Off".
func (c *Client) SetZoneSwitch(ctx context.Context, subunit, function, onValue string, on bool) error {
	v := "Off"
	if on {
		v = onValue
	}
	return c.put(ctx, subunit, function, v)
}
