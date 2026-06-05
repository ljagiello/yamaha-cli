package ynca

import "testing"

func TestFormatStepped(t *testing.T) {
	cases := []struct {
		name     string
		value    float64
		decimals int
		step     float64
		want     string
	}{
		{"volume grid", -30.4, 1, 0.5, "-30.5"},
		{"volume zero no neg", -0.1, 1, 0.5, "0.0"},
		{"fm 0.2 grid 2dp", 98.4, 2, 0.2, "98.40"},
		{"fm rounds to grid", 98.44, 2, 0.2, "98.40"},
		{"am 10k grid int", 1530, 0, 10, "1530"},
		{"am rounds up", 1538, 0, 10, "1540"},
		{"no step passthrough", 3.14159, 2, 0, "3.14"},
	}
	for _, tc := range cases {
		if got := formatStepped(tc.value, tc.decimals, tc.step); got != tc.want {
			t.Errorf("%s: formatStepped(%v,%d,%v) = %q, want %q",
				tc.name, tc.value, tc.decimals, tc.step, got, tc.want)
		}
	}
}

// formatVolume must remain a faithful thin wrapper over formatStepped so the
// two never drift.
func TestFormatVolumeMatchesStepped(t *testing.T) {
	for _, v := range []float64{-80, -30.5, -0.1, 0, 16.5} {
		if formatVolume(v) != formatStepped(v, 1, volumeStep) {
			t.Errorf("formatVolume(%v)=%q != formatStepped=%q", v, formatVolume(v), formatStepped(v, 1, volumeStep))
		}
	}
}

func TestVolNudge(t *testing.T) {
	cases := map[float64]string{
		0.5: "Up",      // default step → bare Up
		0:   "Up",      // unspecified → bare Up
		1:   "Up 1 dB", // device-supported whole steps carry the dB
		2:   "Up 2 dB",
		5:   "Up 5 dB",
		3:   "Up", // unsupported step falls back to bare Up
	}
	for step, want := range cases {
		if got := volNudge("Up", step); got != want {
			t.Errorf("volNudge(Up, %v) = %q, want %q", step, got, want)
		}
	}
	if got := volNudge("Down", 2); got != "Down 2 dB" {
		t.Errorf("volNudge(Down, 2) = %q, want Down 2 dB", got)
	}
}

func TestSleepWireRoundTrip(t *testing.T) {
	for _, m := range []int{0, 30, 60, 90, 120} {
		wire, ok := sleepWire(m)
		if !ok {
			t.Fatalf("sleepWire(%d) not ok", m)
		}
		back, ok := sleepMinutes(wire)
		if !ok || back != m {
			t.Errorf("sleepMinutes(%q) = %d ok=%v, want %d", wire, back, ok, m)
		}
	}
	if _, ok := sleepWire(45); ok {
		t.Error("sleepWire(45) should be rejected")
	}
}
