package yxc

import (
	"testing"
)

// makeFeatures returns a small *Features fixture with two zones, one of
// which has both an integer "volume" range_step and a fractional
// "actual_volume_db" range_step (matching the live RX-V583 shape).
func makeFeatures() *Features {
	return &Features{
		System: SystemFeatures{
			InputList: []InputItem{{ID: "hdmi1"}, {ID: "hdmi2"}},
		},
		Zone: []ZoneFeatures{
			{
				ID:        "main",
				FuncList:  []string{"power", "volume", "prepare_input_change"},
				InputList: []string{"hdmi1", "hdmi2", "hdmi3"},
				RangeStep: []RangeStep{
					{ID: "volume", Min: 0, Max: 161, Step: 1},
					{ID: "actual_volume_db", Min: -80.5, Max: 16.5, Step: 0.5},
				},
			},
			{
				ID:        "zone2",
				FuncList:  []string{"power", "volume"},
				InputList: []string{"hdmi1"},
				RangeStep: []RangeStep{
					{ID: "volume", Min: 0, Max: 100, Step: 1},
					// Intentionally no actual_volume_db: zone2 commonly omits it.
				},
			},
		},
	}
}

// TestVolumeRangeDB_Present asserts the lookup returns the actual_volume_db
// range_step values verbatim when present.
func TestVolumeRangeDB_Present(t *testing.T) {
	f := makeFeatures()
	mn, mx, step, ok := f.VolumeRangeDB("main")
	if !ok {
		t.Fatalf("VolumeRangeDB(main): ok=false, want true")
	}
	if mn != -80.5 || mx != 16.5 || step != 0.5 {
		t.Errorf("VolumeRangeDB(main): got (%v,%v,%v), want (-80.5,16.5,0.5)", mn, mx, step)
	}
}

// TestVolumeRangeDB_AbsentReturnsFalse asserts the lookup returns
// (0,0,0,false) when the requested zone has no actual_volume_db
// range_step. Callers must fall back to the integer-step convention.
func TestVolumeRangeDB_AbsentReturnsFalse(t *testing.T) {
	f := makeFeatures()
	if _, _, _, ok := f.VolumeRangeDB("zone2"); ok {
		t.Errorf("VolumeRangeDB(zone2): ok=true, want false")
	}
}

// TestVolumeRangeDB_UnknownZone asserts unknown zone names return false.
func TestVolumeRangeDB_UnknownZone(t *testing.T) {
	f := makeFeatures()
	if _, _, _, ok := f.VolumeRangeDB("zone3"); ok {
		t.Errorf("VolumeRangeDB(zone3): ok=true on unknown zone")
	}
}

// TestVolumeRangeDB_NilFeatures asserts the method is nil-safe.
func TestVolumeRangeDB_NilFeatures(t *testing.T) {
	var f *Features
	if _, _, _, ok := f.VolumeRangeDB("main"); ok {
		t.Errorf("VolumeRangeDB on nil Features: ok=true, want false")
	}
}
