package cli

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// runYncaSubcmdOut runs an `ynca` subcommand and returns its stdout, for the
// listing/discovery commands whose output we assert on.
func runYncaSubcmdOut(t *testing.T, addr string, args ...string) string {
	t.Helper()
	cmd := newYncaCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(io.Discard)
	cmd.SetArgs(args)
	cmd.SetContext(context.WithValue(context.Background(), stateKey, newYncaState(t, addr)))
	if err := cmd.Execute(); err != nil {
		t.Fatalf("ynca %v: %v", args, err)
	}
	return out.String()
}

func TestYncaSubcmd_Decoder(t *testing.T) {
	shrinkYNCATimeouts(t, 500*time.Millisecond, 500*time.Millisecond)

	addr, last := yncaSubcmdServer(t, nil)
	runYncaSubcmd(t, addr, "decoder", "Dolby Surround")
	if got := last.get(); got != "@MAIN:2CHDECODER=Dolby Surround" {
		t.Errorf("decoder set sent %q, want @MAIN:2CHDECODER=Dolby Surround", got)
	}

	// No-arg lists the known decoders.
	addr2, _ := yncaSubcmdServer(t, nil)
	out := runYncaSubcmdOut(t, addr2, "decoder")
	if !strings.Contains(out, "DTS Neural:X") {
		t.Errorf("decoder list missing a known value: %s", out)
	}
}

func TestYncaSubcmd_DSPToggles(t *testing.T) {
	shrinkYNCATimeouts(t, 500*time.Millisecond, 500*time.Millisecond)

	// pure-direct uses the "On" spelling.
	addr, last := yncaSubcmdServer(t, nil)
	runYncaSubcmd(t, addr, "pure-direct", "on")
	if got := last.get(); got != "@MAIN:PUREDIRMODE=On" {
		t.Errorf("pure-direct on sent %q, want @MAIN:PUREDIRMODE=On", got)
	}

	// extra-bass uses "Auto" as its on value — the per-entry onValue.
	addr2, last2 := yncaSubcmdServer(t, nil)
	runYncaSubcmd(t, addr2, "extra-bass", "on")
	if got := last2.get(); got != "@MAIN:EXBASS=Auto" {
		t.Errorf("extra-bass on sent %q, want @MAIN:EXBASS=Auto", got)
	}

	// off is always "Off".
	addr3, last3 := yncaSubcmdServer(t, nil)
	runYncaSubcmd(t, addr3, "adaptive-drc", "off")
	if got := last3.get(); got != "@MAIN:ADAPTIVEDRC=Off" {
		t.Errorf("adaptive-drc off sent %q, want @MAIN:ADAPTIVEDRC=Off", got)
	}
}

func TestYncaSubcmd_ToneAndReset(t *testing.T) {
	shrinkYNCATimeouts(t, 500*time.Millisecond, 500*time.Millisecond)

	addr, last := yncaSubcmdServer(t, nil)
	runYncaSubcmd(t, addr, "tone", "bass", "3")
	if got := last.get(); got != "@MAIN:SPBASS=3.0" {
		t.Errorf("tone bass 3 sent %q, want @MAIN:SPBASS=3.0", got)
	}

	// Negative values come after `--` (cobra would otherwise read -2.5 as a flag).
	addr2, last2 := yncaSubcmdServer(t, nil)
	runYncaSubcmd(t, addr2, "tone", "treble", "--", "-2.5")
	if got := last2.get(); got != "@MAIN:SPTREBLE=-2.5" {
		t.Errorf("tone treble -2.5 sent %q, want @MAIN:SPTREBLE=-2.5", got)
	}

	// reset writes both channels to 0; the last write is treble.
	addr3, last3 := yncaSubcmdServer(t, nil)
	runYncaSubcmd(t, addr3, "tone", "reset")
	if got := last3.get(); got != "@MAIN:SPTREBLE=0.0" {
		t.Errorf("tone reset last write %q, want @MAIN:SPTREBLE=0.0", got)
	}
}

func TestYncaSubcmd_Sleep(t *testing.T) {
	shrinkYNCATimeouts(t, 500*time.Millisecond, 500*time.Millisecond)

	addr, last := yncaSubcmdServer(t, nil)
	runYncaSubcmd(t, addr, "sleep", "60")
	if got := last.get(); got != "@MAIN:SLEEP=60 min" {
		t.Errorf("sleep 60 sent %q, want @MAIN:SLEEP=60 min", got)
	}

	addr2, last2 := yncaSubcmdServer(t, nil)
	runYncaSubcmd(t, addr2, "sleep", "off")
	if got := last2.get(); got != "@MAIN:SLEEP=Off" {
		t.Errorf("sleep off sent %q, want @MAIN:SLEEP=Off", got)
	}
}

func TestYncaSubcmd_Scene(t *testing.T) {
	shrinkYNCATimeouts(t, 500*time.Millisecond, 500*time.Millisecond)

	addr, last := yncaSubcmdServer(t, nil)
	runYncaSubcmd(t, addr, "scene", "3")
	if got := last.get(); got != "@MAIN:SCENE=Scene 3" {
		t.Errorf("scene 3 sent %q, want @MAIN:SCENE=Scene 3", got)
	}
}

func TestYncaSubcmd_SystemPower(t *testing.T) {
	shrinkYNCATimeouts(t, 500*time.Millisecond, 500*time.Millisecond)

	addr, last := yncaSubcmdServer(t, nil)
	runYncaSubcmd(t, addr, "system", "power", "on")
	if got := last.get(); got != "@SYS:PWR=On" {
		t.Errorf("system power on sent %q, want @SYS:PWR=On", got)
	}
}

func TestYncaSubcmd_VolumeStep(t *testing.T) {
	shrinkYNCATimeouts(t, 500*time.Millisecond, 500*time.Millisecond)

	addr, last := yncaSubcmdServer(t, nil)
	runYncaSubcmd(t, addr, "volume", "up", "--step", "2")
	if got := last.get(); got != "@MAIN:VOL=Up 2 dB" {
		t.Errorf("volume up --step 2 sent %q, want @MAIN:VOL=Up 2 dB", got)
	}

	// No step → bare Up (one device step).
	addr2, last2 := yncaSubcmdServer(t, nil)
	runYncaSubcmd(t, addr2, "volume", "down")
	if got := last2.get(); got != "@MAIN:VOL=Down" {
		t.Errorf("volume down sent %q, want @MAIN:VOL=Down", got)
	}
}

func TestYncaSubcmd_Tuner(t *testing.T) {
	shrinkYNCATimeouts(t, 500*time.Millisecond, 500*time.Millisecond)

	// 98.6 is on the 0.2 MHz grid ynca uses; an off-grid value would snap.
	addr, last := yncaSubcmdServer(t, nil)
	runYncaSubcmd(t, addr, "tuner", "fm", "98.6")
	if got := last.get(); got != "@TUN:FMFREQ=98.60" {
		t.Errorf("tuner fm 98.6 sent %q, want @TUN:FMFREQ=98.60", got)
	}

	addr2, last2 := yncaSubcmdServer(t, nil)
	runYncaSubcmd(t, addr2, "tuner", "band", "fm")
	if got := last2.get(); got != "@TUN:BAND=FM" {
		t.Errorf("tuner band fm sent %q, want @TUN:BAND=FM", got)
	}

	addr3, last3 := yncaSubcmdServer(t, nil)
	runYncaSubcmd(t, addr3, "tuner", "preset", "3")
	if got := last3.get(); got != "@TUN:PRESET=3" {
		t.Errorf("tuner preset 3 sent %q, want @TUN:PRESET=3", got)
	}
}

func TestYncaSubcmd_TransportResolvesSource(t *testing.T) {
	shrinkYNCATimeouts(t, 500*time.Millisecond, 500*time.Millisecond)

	// The current input is Spotify, so `play` must target @SPOTIFY:PLAYBACK.
	addr, last := yncaSubcmdServer(t, map[string]string{"@MAIN:INP=?": "@MAIN:INP=Spotify"})
	runYncaSubcmd(t, addr, "play")
	if got := last.get(); got != "@SPOTIFY:PLAYBACK=Play" {
		t.Errorf("play sent %q, want @SPOTIFY:PLAYBACK=Play", got)
	}

	// An explicit --source overrides the current input.
	addr2, last2 := yncaSubcmdServer(t, nil)
	runYncaSubcmd(t, addr2, "next", "--source", "NET RADIO")
	if got := last2.get(); got != "@NETRADIO:PLAYBACK=Skip Fwd" {
		t.Errorf("next --source NET RADIO sent %q, want @NETRADIO:PLAYBACK=Skip Fwd", got)
	}
}

func TestYncaSubcmd_InputListsOnNoArg(t *testing.T) {
	shrinkYNCATimeouts(t, 500*time.Millisecond, 500*time.Millisecond)
	addr, _ := yncaSubcmdServer(t, nil)
	out := runYncaSubcmdOut(t, addr, "input")
	if !strings.Contains(out, "HDMI2") || !strings.Contains(out, "TUNER") {
		t.Errorf("input list missing known values: %s", out)
	}
}

func TestYncaSubcmd_InputCanonicalisesCase(t *testing.T) {
	shrinkYNCATimeouts(t, 500*time.Millisecond, 500*time.Millisecond)
	addr, last := yncaSubcmdServer(t, nil)
	runYncaSubcmd(t, addr, "input", "hdmi2")
	if got := last.get(); got != "@MAIN:INP=HDMI2" {
		t.Errorf("input hdmi2 sent %q, want @MAIN:INP=HDMI2 (case-canonicalised)", got)
	}
}

func TestYncaSubcmd_ListOffline(t *testing.T) {
	// `ynca list` needs no device; run it with an unreachable address to
	// prove it never dials.
	out := runYncaSubcmdOut(t, "127.0.0.1:1", "list", "tuner")
	if !strings.Contains(out, "FMFREQ") || !strings.Contains(out, "BAND") {
		t.Errorf("ynca list tuner missing entries: %s", out)
	}
}

func TestYncaSubcmd_Dump(t *testing.T) {
	shrinkYNCATimeouts(t, 500*time.Millisecond, 500*time.Millisecond)
	prev := yncaDumpDelay
	yncaDumpDelay = 0 // no inter-command pause in tests
	t.Cleanup(func() { yncaDumpDelay = prev })

	// A small commands file scopes the dump (the built-in catalog would send
	// a hundred-plus lines).
	cmdsFile := filepath.Join(t.TempDir(), "cmds.txt")
	if err := os.WriteFile(cmdsFile, []byte("# a couple of GETs\n@MAIN:PWR=?\n@MAIN:VOL=?\n@ZONE9:PWR=?\n"), 0o644); err != nil {
		t.Fatalf("write cmds: %v", err)
	}

	addr := startFakeYNCA(t, func(line string) string {
		switch line {
		case "@SYS:VERSION=?":
			return "@SYS:VERSION=2.87/1.81"
		case "@SYS:MODELNAME=?":
			return "@SYS:MODELNAME=RX-V583"
		case "@MAIN:PWR=?":
			return "@MAIN:PWR=On"
		case "@MAIN:VOL=?":
			return "@MAIN:VOL=-40.0"
		}
		return "@UNDEFINED" // e.g. the bogus ZONE9
	})

	out := runYncaSubcmdOut(t, addr, "dump", "--commands", cmdsFile)
	// Value replies are recorded verbatim (replayable); the unsupported one
	// is captured as a comment, not a value line.
	if !strings.Contains(out, "@MAIN:PWR=On") || !strings.Contains(out, "@MAIN:VOL=-40.0") {
		t.Errorf("dump missing value lines:\n%s", out)
	}
	if !strings.Contains(out, "# @ZONE9:PWR=? -> @UNDEFINED") {
		t.Errorf("dump did not comment the @UNDEFINED reply:\n%s", out)
	}
}
