package yxc

import (
	"encoding/json"
	"testing"
	"time"
)

func TestParseAndKnown(t *testing.T) {
	// Each parser maps known wire values to themselves and unknowns to the
	// type's Unknown sentinel; Known() agrees.
	if got := ParsePowerState("on"); got != PowerOn || !got.Known() {
		t.Errorf("ParsePowerState(on) = %q known=%v", got, got.Known())
	}
	if got := ParsePowerState("hibernate"); got != PowerUnknown || got.Known() {
		t.Errorf("ParsePowerState(hibernate) = %q known=%v, want PowerUnknown/false", got, got.Known())
	}
	if got := ParsePlaybackState("pause"); got != PlaybackStatePause || !got.Known() {
		t.Errorf("ParsePlaybackState(pause) = %q", got)
	}
	if got := ParsePlaybackState("buffering"); got != PlaybackStateUnknown {
		t.Errorf("ParsePlaybackState(buffering) = %q, want Unknown", got)
	}
	if got := ParseRepeatMode("all"); got != RepeatAll || !got.Known() {
		t.Errorf("ParseRepeatMode(all) = %q", got)
	}
	if got := ParseShuffleMode("albums"); got != ShuffleAlbums || !got.Known() {
		t.Errorf("ParseShuffleMode(albums) = %q", got)
	}
	if got := ParseDistRole("none"); got != DistRoleNone || !got.Known() {
		t.Errorf("ParseDistRole(none) = %q", got)
	}
	if got := ParseDistRole("relay"); got.Known() {
		t.Errorf("ParseDistRole(relay) Known()=true, want false")
	}
}

// TestEnumUnmarshalPreservesUnknown is the key forward-compat property:
// an unrecognised firmware value is preserved verbatim in the typed field
// (NOT rewritten to the Unknown sentinel), so the CLI can still render it.
func TestEnumUnmarshalPreservesUnknown(t *testing.T) {
	var st Status
	if err := json.Unmarshal([]byte(`{"power":"hibernate"}`), &st); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if string(st.Power) != "hibernate" {
		t.Errorf("Power = %q, want raw \"hibernate\" preserved", st.Power)
	}
	if st.Power.Known() {
		t.Errorf("unknown power Known()=true, want false")
	}
}

func TestPlayInfoDurations(t *testing.T) {
	pi := &PlayInfo{PlayTime: 83, TotalTime: 300}
	if pi.Elapsed() != 83*time.Second {
		t.Errorf("Elapsed = %v, want 1m23s", pi.Elapsed())
	}
	if pi.Total() != 300*time.Second {
		t.Errorf("Total = %v, want 5m", pi.Total())
	}
	// Negative buffering sentinels clamp to zero.
	neg := &PlayInfo{PlayTime: -60}
	if neg.Elapsed() != 0 {
		t.Errorf("Elapsed(-60) = %v, want 0", neg.Elapsed())
	}
}
