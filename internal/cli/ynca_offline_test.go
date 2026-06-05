package cli

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ljagiello/yamaha-cli/pkg/ynca"
)

// --- diff: transcript parsing (offline, no device) ---

func TestParseTranscript(t *testing.T) {
	path := filepath.Join(t.TempDir(), "dump.txt")
	content := strings.Join([]string{
		"# a dump header comment",
		"@MAIN:PWR=On",
		"@MAIN:VOL=-30.0",
		"# @ZONE3:PWR=? -> @UNDEFINED", // commented control reply: must be ignored
		"@tun:band=FM",                 // lower-case: must upper-case to @TUN:BAND
		"garbage line without at",      // not an @ line: skip
		"@BAD",                         // malformed (no :/=): skip
		`@MAIN:INP=HDMI2",`,            // trailing junk some logs add: tolerate
		"",                             // blank: skip
	}, "\n")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	set, err := parseTranscript(path)
	if err != nil {
		t.Fatalf("parseTranscript: %v", err)
	}
	want := []string{"@MAIN:PWR", "@MAIN:VOL", "@TUN:BAND", "@MAIN:INP"}
	for _, k := range want {
		if _, ok := set[k]; !ok {
			t.Errorf("parseTranscript missing %q; got %v", k, keysOf(set))
		}
	}
	// The commented @UNDEFINED line must NOT register ZONE3:PWR as supported.
	if _, ok := set["@ZONE3:PWR"]; ok {
		t.Error("commented @UNDEFINED reply leaked into the supported set")
	}
	if len(set) != len(want) {
		t.Errorf("set has %d entries, want %d: %v", len(set), len(want), keysOf(set))
	}
}

func TestSplitSubunitFunc(t *testing.T) {
	su, fn := splitSubunitFunc("@MAIN:PWR")
	if su != "MAIN" || fn != "PWR" {
		t.Errorf("splitSubunitFunc = %q/%q, want MAIN/PWR", su, fn)
	}
}

func keysOf(m map[string]struct{}) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

// --- info: zone/tuner presence probing (@UNDEFINED → absent, @RESTRICTED → present) ---

func TestCollectYncaInfo_PresenceMatrix(t *testing.T) {
	addr := startFakeYNCA(t, func(line string) string {
		switch line {
		case "@SYS:MODELNAME=?":
			return "@SYS:MODELNAME=RX-V583"
		case "@SYS:VERSION=?":
			return "@SYS:VERSION=2.87/1.81"
		case "@MAIN:PWR=?":
			return "@MAIN:PWR=On" // present
		case "@ZONE2:PWR=?":
			return "@RESTRICTED" // exists but not allowed now → still present
		case "@ZONE3:PWR=?", "@ZONE4:PWR=?":
			return "@UNDEFINED" // absent
		case "@TUN:BAND=?":
			return "@TUN:BAND=FM" // tuner present
		}
		return "@UNDEFINED"
	})
	c, err := ynca.New(addr)
	if err != nil {
		t.Fatalf("ynca.New: %v", err)
	}
	defer func() { _ = c.Close() }()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	payload, err := collectYncaInfo(ctx, c, "zone3")
	if err != nil {
		t.Fatalf("collectYncaInfo: %v", err)
	}

	if payload["model"] != "RX-V583" {
		t.Errorf("model = %v, want RX-V583", payload["model"])
	}
	zones, _ := payload["zones"].([]string)
	if got := strings.Join(zones, ","); got != "main,zone2" {
		t.Errorf("zones = %q, want main,zone2 (zone2's @RESTRICTED counts as present; zone3/4 absent)", got)
	}
	if payload["tuner"] != true {
		t.Errorf("tuner = %v, want true", payload["tuner"])
	}
	// The requested zone (zone3) is absent — info must report that, not hide it.
	if payload["requested_zone_present"] != false {
		t.Errorf("requested_zone_present = %v, want false for absent zone3", payload["requested_zone_present"])
	}
}

// TestCollectYncaInfo_TransportErrorAborts: a transport failure mid-probe
// must surface as an error, not be silently reported as "zone absent".
func TestCollectYncaInfo_TransportErrorAborts(t *testing.T) {
	addr := startFakeYNCA(t, func(line string) string {
		if line == "@SYS:MODELNAME=?" {
			return "@SYS:MODELNAME=RX-V583"
		}
		if line == "@SYS:VERSION=?" {
			return "@SYS:VERSION=2.87/1.81"
		}
		return "" // never reply → the zone PWR probe times out (transport-ish)
	})
	c, err := ynca.New(addr, ynca.WithTimeout(200*time.Millisecond))
	if err != nil {
		t.Fatalf("ynca.New: %v", err)
	}
	defer func() { _ = c.Close() }()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if _, err := collectYncaInfo(ctx, c, "main"); err == nil {
		t.Error("collectYncaInfo: want error when a zone probe gets no reply, got nil")
	}
}

// --- now-playing payload shaping ---

func TestBuildYNCANowPlayingPayload(t *testing.T) {
	np := &ynca.NowPlaying{
		Subunit:      "SPOTIFY",
		Artist:       "Daft Punk",
		Album:        "", // empty fields must be dropped
		Song:         "One More Time",
		PlaybackInfo: ynca.PlaybackInfoPlay,
		ElapsedRaw:   "",
	}
	p := buildYNCANowPlayingPayload(np)
	if p["source"] != "SPOTIFY" || p["artist"] != "Daft Punk" || p["song"] != "One More Time" {
		t.Errorf("payload core fields wrong: %v", p)
	}
	if _, ok := p["album"]; ok {
		t.Error("empty album should be dropped from the payload")
	}
	if p["playback"] != "Play" {
		t.Errorf("playback = %v, want Play", p["playback"])
	}

	// Unknown playback state must be omitted (Known() gate).
	np2 := &ynca.NowPlaying{Subunit: "USB", PlaybackInfo: ynca.PlaybackInfoUnknown}
	p2 := buildYNCANowPlayingPayload(np2)
	if _, ok := p2["playback"]; ok {
		t.Error("unknown playback state should be omitted, not emitted")
	}
}

// --- dump: built-in catalog invariants ---

func TestDefaultDumpCommands(t *testing.T) {
	cmds := defaultDumpCommands()
	if len(cmds) == 0 {
		t.Fatal("defaultDumpCommands is empty")
	}
	for _, line := range cmds {
		if !strings.HasPrefix(line, "@") || !strings.HasSuffix(line, "=?") {
			t.Errorf("catalog entry %q is not an @SUB:FUNC=? GET", line)
		}
		if !strings.Contains(line, ":") {
			t.Errorf("catalog entry %q has no subunit separator", line)
		}
	}
	// Spot-check coverage breadth: a zone fan-out, the tuner RDS group, and a
	// streaming source's metadata must all be present.
	for _, want := range []string{"@MAIN:BASIC=?", "@TUN:RDSINFO=?", "@SPOTIFY:METAINFO=?"} {
		if !slices.Contains(cmds, want) {
			t.Errorf("catalog missing %q", want)
		}
	}
	// Put-only functions must never appear (the dump must stay read-only).
	for _, forbidden := range []string{"@MAIN:SCENE=?", "@TUN:MEM=?", "@SPOTIFY:PLAYBACK=?"} {
		if slices.Contains(cmds, forbidden) {
			t.Errorf("catalog includes put-only function %q — dump must be read-only", forbidden)
		}
	}
}
