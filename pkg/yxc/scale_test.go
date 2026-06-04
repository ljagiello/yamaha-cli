package yxc

import "testing"

// featsWithDBRange builds a Features whose "main" zone reports both the
// integer "volume" range_step and a fractional "actual_volume_db" one,
// matching the live RX-V583 shape. "zone2" deliberately omits the dB
// range so the fallback path is exercised.
func featsWithDBRange() *Features {
	return &Features{
		Zone: []ZoneFeatures{
			{
				ID: "main",
				RangeStep: []RangeStep{
					{ID: "volume", Min: 0, Max: 161, Step: 1},
					{ID: "actual_volume_db", Min: -80.5, Max: 16.5, Step: 0.5},
				},
			},
			{
				ID: "zone2",
				RangeStep: []RangeStep{
					{ID: "volume", Min: 0, Max: 161, Step: 1},
				},
			},
		},
	}
}

func TestVolumeDBScale_DevicePreferred(t *testing.T) {
	f := featsWithDBRange()
	baseline, step, exact := f.VolumeDBScale("main")
	if !exact {
		t.Fatalf("VolumeDBScale(main): exact=false, want true (device reports actual_volume_db)")
	}
	if baseline != -80.5 || step != 0.5 {
		t.Errorf("VolumeDBScale(main) = (%v, %v), want (-80.5, 0.5)", baseline, step)
	}
}

func TestVolumeDBScale_FallbackWhenAbsent(t *testing.T) {
	f := featsWithDBRange()
	baseline, step, exact := f.VolumeDBScale("zone2")
	if exact {
		t.Errorf("VolumeDBScale(zone2): exact=true, want false (no actual_volume_db)")
	}
	if baseline != DefaultVolumeDBBaseline || step != DefaultVolumeDBStep {
		t.Errorf("VolumeDBScale(zone2) = (%v, %v), want default (%v, %v)",
			baseline, step, DefaultVolumeDBBaseline, DefaultVolumeDBStep)
	}
}

func TestVolumeDBScale_NilSafe(t *testing.T) {
	var f *Features
	baseline, step, exact := f.VolumeDBScale("main")
	if exact || baseline != DefaultVolumeDBBaseline || step != DefaultVolumeDBStep {
		t.Errorf("VolumeDBScale on nil = (%v, %v, %v), want default scale, exact=false", baseline, step, exact)
	}
}

func TestVolumeIntToDB(t *testing.T) {
	f := featsWithDBRange()
	// 0 → -80.5, 161 → -80.5 + 0.5*161 = 0.0, 99 → -31.0
	cases := []struct {
		zone string
		n    int
		want float64
	}{
		{"main", 0, -80.5},
		{"main", 161, 0.0},
		{"main", 99, -31.0},
		{"zone2", 0, -80.5}, // fallback baseline
	}
	for _, tc := range cases {
		if got := f.VolumeIntToDB(tc.zone, tc.n); got != tc.want {
			t.Errorf("VolumeIntToDB(%s, %d) = %v, want %v", tc.zone, tc.n, got, tc.want)
		}
	}
}

// TestVolumeDBRoundTrip asserts VolumeDBToInt is the inverse of
// VolumeIntToDB for on-grid values — the property the duplicated
// hand-rolled conversions used to risk diverging on.
func TestVolumeDBToInt_InverseOfIntToDB(t *testing.T) {
	f := featsWithDBRange()
	for _, zone := range []string{"main", "zone2"} {
		for n := 0; n <= 161; n++ {
			db := f.VolumeIntToDB(zone, n)
			if got := f.VolumeDBToInt(zone, db); got != n {
				t.Fatalf("round-trip %s n=%d: dB=%v back to %d", zone, n, db, got)
			}
		}
	}
}

func TestVolumeDBToInt_RoundsToNearestStep(t *testing.T) {
	f := featsWithDBRange()
	// -80.4 is 0.1 above baseline; nearest 0.5-step is index 0.
	if got := f.VolumeDBToInt("main", -80.4); got != 0 {
		t.Errorf("VolumeDBToInt(main, -80.4) = %d, want 0", got)
	}
	// -80.25 is exactly half a step; math.Round goes away from zero → 1 step.
	if got := f.VolumeDBToInt("main", -80.25); got != 1 {
		t.Errorf("VolumeDBToInt(main, -80.25) = %d, want 1", got)
	}
}
