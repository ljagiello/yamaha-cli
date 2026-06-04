package yxc

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"
	"strings"
)

// TunerStatus mirrors `tuner/getPlayInfo`.
//
// Band reflects the active band ("fm" | "am" | "dab"); the corresponding
// AM/FM block carries frequency and preset state. Frequency units differ
// per band — see SetTunerFreq for the convention used by the receiver.
type TunerStatus struct {
	ResponseCode int  `json:"response_code"`
	Band         Band `json:"band"` // fm | am | dab (see enums.go)
	AM           struct {
		Freq   int `json:"freq"`
		Preset int `json:"preset"`
	} `json:"am,omitempty"`
	FM struct {
		Freq   int    `json:"freq"`
		Preset int    `json:"preset"`
		Audio  string `json:"audio_mode,omitempty"`
	} `json:"fm,omitempty"`
}

// TunerPreset is one entry of `tuner/getPresetInfo`.
type TunerPreset struct {
	Band   Band `json:"band"`
	Number int  `json:"number"`
	Freq   int  `json:"freq,omitempty"`
}

// TunerPresetInfo mirrors `tuner/getPresetInfo`.
type TunerPresetInfo struct {
	ResponseCode int           `json:"response_code"`
	PresetInfo   []TunerPreset `json:"preset_info"`
}

// validTunerBand normalises a tuner band identifier (case-insensitive) to
// one of the modelled Band constants.
func validTunerBand(band string) (Band, error) {
	switch b := Band(strings.ToLower(band)); b {
	case BandFM, BandAM, BandDAB:
		return b, nil
	default:
		return "", fmt.Errorf("yxc: invalid tuner band %q (want fm|am|dab)", band)
	}
}

// GetTunerStatus returns the tuner's current play info.
func (c *Client) GetTunerStatus(ctx context.Context) (*TunerStatus, error) {
	raw, err := c.Do(ctx, "tuner/getPlayInfo", nil)
	if err != nil {
		return nil, err
	}
	var s TunerStatus
	if err := json.Unmarshal(raw, &s); err != nil {
		return nil, fmt.Errorf("yxc: tuner/getPlayInfo: %w", err)
	}
	return &s, nil
}

// SetTunerFreq tunes to an absolute frequency. The integer is in kHz
// for both bands:
//
//   - FM: kHz (e.g. 102500 for 102.5 MHz). Verified against the live
//     RX-V583, which returns freq=106500 from getPlayInfo for an FM
//     station at 106.5 MHz.
//   - AM: kHz (e.g. 530 for 530 kHz).
//
// The CLI converts user-friendly inputs (MHz for FM, kHz for AM) into
// the kHz wire value before calling this method. Pass freqHz unchanged.
//
// The request shape is `tuner/setFreq?band=<band>&tuning=direct&num=<n>`.
func (c *Client) SetTunerFreq(ctx context.Context, band string, freqHz int) error {
	b, err := validTunerBand(band)
	if err != nil {
		return err
	}
	if freqHz <= 0 {
		return fmt.Errorf("yxc: SetTunerFreq: freq must be > 0, got %d", freqHz)
	}
	v := url.Values{}
	v.Set("band", string(b))
	v.Set("tuning", "direct")
	v.Set("num", strconv.Itoa(freqHz))
	_, err = c.Do(ctx, "tuner/setFreq", v)
	return err
}

// RecallTunerPreset recalls preset num for the given band, routed to
// the named zone. num is 1-indexed against the band's preset list.
func (c *Client) RecallTunerPreset(ctx context.Context, zone, band string, num int) error {
	z, err := validZone(zone)
	if err != nil {
		return err
	}
	b, err := validTunerBand(band)
	if err != nil {
		return err
	}
	if num < 1 {
		return fmt.Errorf("yxc: RecallTunerPreset: num must be >= 1, got %d", num)
	}
	v := url.Values{}
	v.Set("zone", z)
	v.Set("band", string(b))
	v.Set("num", strconv.Itoa(num))
	_, err = c.Do(ctx, "tuner/recallPreset", v)
	return err
}

// GetTunerPresetInfo returns the preset list for the given band.
func (c *Client) GetTunerPresetInfo(ctx context.Context, band string) (*TunerPresetInfo, error) {
	b, err := validTunerBand(band)
	if err != nil {
		return nil, err
	}
	v := url.Values{}
	v.Set("band", string(b))
	raw, err := c.Do(ctx, "tuner/getPresetInfo", v)
	if err != nil {
		return nil, err
	}
	var p TunerPresetInfo
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, fmt.Errorf("yxc: tuner/getPresetInfo: %w", err)
	}
	return &p, nil
}
