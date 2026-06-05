package yxc

import (
	"encoding/json"
	"fmt"
	"strings"
)

// DeviceInfo mirrors `/system/getDeviceInfo`.
type DeviceInfo struct {
	ResponseCode      int         `json:"response_code"`
	ModelName         string      `json:"model_name"`
	DeviceID          string      `json:"device_id"`
	SystemVersion     json.Number `json:"system_version"`
	APIVersion        json.Number `json:"api_version"`
	NetModuleVersion  string      `json:"netmodule_version,omitempty"`
	NetModuleChecksum string      `json:"netmodule_checksum,omitempty"`
}

// Status mirrors the relevant fields from `/<zone>/getStatus`.
// Only fields needed by Phase 1 commands are modelled; the rest are
// preserved on the wire but ignored here.
type Status struct {
	ResponseCode    int           `json:"response_code"`
	Power           PowerState    `json:"power"` // on | standby (see enums.go)
	Volume          int           `json:"volume"`
	Mute            bool          `json:"mute"`
	Input           string        `json:"input"`
	SoundProgram    string        `json:"sound_program,omitempty"`
	SurrDecoderType string        `json:"surr_decoder_type,omitempty"`
	Sleep           int           `json:"sleep,omitempty"`
	MaxVolume       int           `json:"max_volume,omitempty"`
	ActualVolume    *ActualVolume `json:"actual_volume,omitempty"`
}

// ActualVolume is the dB / numeric representation of the current volume,
// included in `getStatus` on receivers that support it.
type ActualVolume struct {
	Mode  string  `json:"mode"` // "db" | "numeric"
	Value float64 `json:"value"`
	Unit  string  `json:"unit"`
}

// InputItem describes one entry of `system.input_list` from getFeatures.
type InputItem struct {
	ID                 string `json:"id"`
	DistributionEnable bool   `json:"distribution_enable"`
	RenameEnable       bool   `json:"rename_enable"`
	AccountEnable      bool   `json:"account_enable"`
	PlayInfoType       string `json:"play_info_type"`
}

// RangeStep describes a numeric range with a step size.
//
// On the wire min/max/step are JSON numbers — sometimes integers
// (e.g. volume 0..161 step 1) and sometimes fractional (e.g.
// actual_volume_db -80.5..16.5 step 0.5). We use float64 so both fit;
// integer ranges round-trip exactly.
type RangeStep struct {
	ID   string  `json:"id"`
	Min  float64 `json:"min"`
	Max  float64 `json:"max"`
	Step float64 `json:"step"`
}

// SystemFeatures models `system` from getFeatures.
type SystemFeatures struct {
	FuncList  []string    `json:"func_list"`
	ZoneNum   int         `json:"zone_num"`
	InputList []InputItem `json:"input_list"`
}

// ZoneFeatures models one entry of `zone[]` from getFeatures.
type ZoneFeatures struct {
	ID                   string      `json:"id"` // "main" | "zone2"
	ZoneB                bool        `json:"zone_b,omitempty"`
	FuncList             []string    `json:"func_list"`
	InputList            []string    `json:"input_list"`
	SoundProgramList     []string    `json:"sound_program_list,omitempty"`
	SurrDecoderTypeList  []string    `json:"surr_decoder_type_list,omitempty"`
	ToneControlModeList  []string    `json:"tone_control_mode_list,omitempty"`
	LinkControlList      []string    `json:"link_control_list,omitempty"`
	SceneNum             int         `json:"scene_num,omitempty"`
	CursorList           []string    `json:"cursor_list,omitempty"`
	MenuList             []string    `json:"menu_list,omitempty"`
	ActualVolumeModeList []string    `json:"actual_volume_mode_list,omitempty"`
	RangeStep            []RangeStep `json:"range_step,omitempty"`
}

// TunerFeatures models the `tuner` block from getFeatures.
type TunerFeatures struct {
	FuncList  []string    `json:"func_list,omitempty"`
	RangeStep []RangeStep `json:"range_step,omitempty"`
	Preset    *struct {
		Type string `json:"type"`
		Num  int    `json:"num"`
	} `json:"preset,omitempty"`
}

// NetUSBFeatures models the `netusb` block from getFeatures.
type NetUSBFeatures struct {
	FuncList []string `json:"func_list,omitempty"`
	Preset   *struct {
		Num int `json:"num"`
	} `json:"preset,omitempty"`
	PlayQueue *struct {
		Size int `json:"size"`
	} `json:"play_queue,omitempty"`
	NetRadioType string `json:"net_radio_type,omitempty"`
}

// Features mirrors `/system/getFeatures`. Only the subset needed for
// Phase 1 (zone func/input/sound lists, ranges, tuner, netusb) is modelled.
type Features struct {
	ResponseCode int             `json:"response_code"`
	System       SystemFeatures  `json:"system"`
	Zone         []ZoneFeatures  `json:"zone"`
	Tuner        *TunerFeatures  `json:"tuner,omitempty"`
	NetUSB       *NetUSBFeatures `json:"netusb,omitempty"`
}

// ZoneByID returns the named zone (main | zone2 | zone3 | zone4) or nil
// if the device doesn't advertise it in getFeatures.
func (f *Features) ZoneByID(id string) *ZoneFeatures {
	if f == nil {
		return nil
	}
	for i := range f.Zone {
		if strings.EqualFold(f.Zone[i].ID, id) {
			return &f.Zone[i]
		}
	}
	return nil
}

// SystemInputIDs returns the ID list from system.input_list — this is the
// authoritative set of inputs on the device. Per-zone lists are subsets.
func (f *Features) SystemInputIDs() []string {
	if f == nil {
		return nil
	}
	out := make([]string, 0, len(f.System.InputList))
	for _, in := range f.System.InputList {
		out = append(out, in.ID)
	}
	return out
}

// VolumeRange returns the integer volume range for the named zone, or false
// if unavailable. The returned values come from range_step{id:"volume"}.
func (f *Features) VolumeRange(zone string) (min, max, step int, ok bool) {
	z := f.ZoneByID(zone)
	if z == nil {
		return 0, 0, 0, false
	}
	for _, r := range z.RangeStep {
		if r.ID == "volume" {
			return int(r.Min), int(r.Max), int(r.Step), true
		}
	}
	return 0, 0, 0, false
}

// VolumeRangeDB returns the dB volume range for the named zone, or false
// if unavailable. Sourced from range_step{id:"actual_volume_db"} which
// receivers expose alongside the integer "volume" range_step (e.g. RX-V583
// reports min=-80.5, max=16.5, step=0.5 — Yamaha A-series goes to -99.5
// on some lines).
//
// Callers that need the dB scale should always prefer this over a hardcoded
// constant; when the device omits the entry, fall back to the integer
// "volume" range_step convention (one int step ≈ 0.5 dB, baseline = -80.5)
// only as a last resort.
func (f *Features) VolumeRangeDB(zone string) (min, max, step float64, ok bool) {
	z := f.ZoneByID(zone)
	if z == nil {
		return 0, 0, 0, false
	}
	for _, r := range z.RangeStep {
		if r.ID == "actual_volume_db" {
			return r.Min, r.Max, r.Step, true
		}
	}
	return 0, 0, 0, false
}

// ZoneHasFunc reports whether the named zone advertises the given func
// (e.g. "prepare_input_change") in its `func_list`.
func (f *Features) ZoneHasFunc(zone, fn string) bool {
	z := f.ZoneByID(zone)
	if z == nil {
		return false
	}
	for _, x := range z.FuncList {
		if x == fn {
			return true
		}
	}
	return false
}

// ZoneInputs returns the per-zone input ID list for the named zone, or nil.
func (f *Features) ZoneInputs(zone string) []string {
	z := f.ZoneByID(zone)
	if z == nil {
		return nil
	}
	return z.InputList
}

// ZoneSoundPrograms returns the per-zone sound_program list, or nil.
func (f *Features) ZoneSoundPrograms(zone string) []string {
	z := f.ZoneByID(zone)
	if z == nil {
		return nil
	}
	return z.SoundProgramList
}

// String exists so a Features instance prints something helpful in errors.
func (f *Features) String() string {
	if f == nil {
		return "<nil Features>"
	}
	return fmt.Sprintf("Features(zones=%d, inputs=%d)", len(f.Zone), len(f.System.InputList))
}
