package ynca

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"
)

// testCtx returns a context bounded by a generous test deadline, cancelled
// via t.Cleanup.
func testCtx(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	t.Cleanup(cancel)
	return ctx
}

// This file provides a transcript-seeded replay fake — the YNCA test
// counterpart to the ynca reference library's debug_server.py. Instead of
// hand-scripting raw byte responses per test (the older newFakeYNCA style),
// a test loads a @SUBUNIT:FUNCTION=VALUE transcript (the exact format `ynca
// dump` emits) into an in-memory store and the fake answers GETs, echoes the
// @SYS:VERSION=? fence, and synthesises the fan-out groups (BASIC, METAINFO,
// SCENENAME, RDSINFO, INPNAME) from the stored fields. This closes the
// record → replay → test loop: a transcript captured from a real receiver
// becomes a reusable fixture.

// replayGroups are the fan-out group names a GET expands into every stored
// function for the subunit (the device answers a group GET with many report
// lines, which SendMulti drains to the VERSION fence).
var replayGroups = map[string]bool{
	GroupBasic: true, GroupMetaInfo: true, GroupSceneName: true,
	GroupRdsInfo: true, GroupInpName: true,
}

// replayStore is a subunit→function→value map plus a mutex, since SETs
// mutate it while the per-connection handler goroutine reads.
type replayStore struct {
	mu  sync.Mutex
	m   map[string]map[string]string
	ver string
}

// parseReplayTranscript builds a store from a @SUB:FUNC=value transcript,
// skipping comments ('#') and bare control lines.
func parseReplayTranscript(transcript string) *replayStore {
	st := &replayStore{m: map[string]map[string]string{}, ver: "1.00/1.00"}
	for raw := range strings.SplitSeq(transcript, "\n") {
		line := strings.TrimSpace(raw)
		if line == "" || strings.HasPrefix(line, "#") || !strings.HasPrefix(line, "@") {
			continue
		}
		su, fn, val, err := parseLine(line)
		if err != nil {
			continue
		}
		su, fn = strings.ToUpper(su), strings.ToUpper(fn)
		if su == SubunitSystem && fn == FuncVersion {
			st.ver = val
		}
		if st.m[su] == nil {
			st.m[su] = map[string]string{}
		}
		st.m[su][fn] = val
	}
	return st
}

// answer produces the reply for one wire line: a VERSION fence echo, a
// group fan-out, a single GET, or a SET (which mutates the store and echoes).
func (st *replayStore) answer(line string) string {
	su, fn, val, err := parseLine(line)
	if err != nil {
		return "@UNDEFINED"
	}
	su, fn = strings.ToUpper(su), strings.ToUpper(fn)

	st.mu.Lock()
	defer st.mu.Unlock()

	if su == SubunitSystem && fn == FuncVersion {
		return "@" + SubunitSystem + ":" + FuncVersion + "=" + st.ver
	}

	if val == "?" { // a GET
		if replayGroups[fn] {
			funcs := st.m[su]
			if len(funcs) == 0 {
				return "@UNDEFINED"
			}
			var lines []string
			for k, v := range funcs {
				lines = append(lines, "@"+su+":"+k+"="+v)
			}
			return strings.Join(lines, "\r\n")
		}
		if v, ok := st.m[su][fn]; ok {
			return "@" + su + ":" + fn + "=" + v
		}
		return "@UNDEFINED"
	}

	// A SET: record and echo.
	if st.m[su] == nil {
		st.m[su] = map[string]string{}
	}
	st.m[su][fn] = val
	return line
}

// newReplayYNCA starts a fake seeded from transcript and returns its address.
func newReplayYNCA(t *testing.T, transcript string) string {
	t.Helper()
	store := parseReplayTranscript(transcript)
	return newFakeYNCA(t, store.answer)
}

// rxv583Transcript is a small captured-style fixture used by the replay
// tests. It mixes a zone (MAIN), the tuner (TUN), and a streaming source
// (SPOTIFY) so the fan-out groups exercise real parsing paths.
const rxv583Transcript = `# yamaha-cli YNCA dump (fixture)
@SYS:MODELNAME=RX-V583
@SYS:VERSION=2.87/1.81
@MAIN:PWR=On
@MAIN:VOL=-30.5
@MAIN:MUTE=Att -20 dB
@MAIN:INP=HDMI2
@MAIN:SOUNDPRG=Standard
@MAIN:2CHDECODER=Dolby Surround
@MAIN:SLEEP=60 min
@MAIN:SPBASS=2.0
@TUN:BAND=FM
@TUN:FMFREQ=98.50
@TUN:PRESET=3
@SPOTIFY:ARTIST=Daft Punk
@SPOTIFY:ALBUM=Discovery
@SPOTIFY:SONG=One More Time
@SPOTIFY:PLAYBACKINFO=Play
`

func newReplayClient(t *testing.T) *Client {
	t.Helper()
	c, err := New(newReplayYNCA(t, rxv583Transcript))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })
	return c
}

func TestReplay_StatusTriStateMute(t *testing.T) {
	c := newReplayClient(t)
	st, err := c.GetStatus(testCtx(t), SubunitMain)
	if err != nil {
		t.Fatalf("GetStatus: %v", err)
	}
	if st.Power != PowerOn {
		t.Errorf("Power = %q, want On", st.Power)
	}
	// The attenuation mute must survive as its typed value, not flatten to On.
	if st.MuteState != MuteAtt20 {
		t.Errorf("MuteState = %q, want %q", st.MuteState, MuteAtt20)
	}
	if !st.Mute {
		t.Error("Mute bool = false, want true for an attenuation mute")
	}
	if st.Input != "HDMI2" || st.SoundPrg != "Standard" {
		t.Errorf("Input/SoundPrg = %q/%q", st.Input, st.SoundPrg)
	}
}

func TestReplay_Decoder(t *testing.T) {
	c := newReplayClient(t)
	d, err := c.GetDecoder(testCtx(t), SubunitMain)
	if err != nil {
		t.Fatalf("GetDecoder: %v", err)
	}
	if d != "Dolby Surround" {
		t.Errorf("GetDecoder = %q, want Dolby Surround", d)
	}
}

func TestReplay_Sleep(t *testing.T) {
	c := newReplayClient(t)
	m, err := c.GetSleep(testCtx(t), SubunitMain)
	if err != nil {
		t.Fatalf("GetSleep: %v", err)
	}
	if m != 60 {
		t.Errorf("GetSleep = %d, want 60", m)
	}
}

func TestReplay_Tone(t *testing.T) {
	c := newReplayClient(t)
	db, err := c.GetTone(testCtx(t), SubunitMain, FuncSpBass)
	if err != nil {
		t.Fatalf("GetTone: %v", err)
	}
	if db != 2.0 {
		t.Errorf("GetTone = %v, want 2.0", db)
	}
}

func TestReplay_TunerStatus(t *testing.T) {
	c := newReplayClient(t)
	st, err := c.GetTunerStatus(testCtx(t))
	if err != nil {
		t.Fatalf("GetTunerStatus: %v", err)
	}
	if st.Band != BandFM {
		t.Errorf("Band = %q, want FM", st.Band)
	}
	if st.FreqMHz != 98.5 {
		t.Errorf("FreqMHz = %v, want 98.5", st.FreqMHz)
	}
	if st.Preset != "3" {
		t.Errorf("Preset = %q, want 3", st.Preset)
	}
}

func TestReplay_NowPlaying(t *testing.T) {
	c := newReplayClient(t)
	np, err := c.GetNowPlaying(testCtx(t), SubunitSpotify)
	if err != nil {
		t.Fatalf("GetNowPlaying: %v", err)
	}
	if np.Artist != "Daft Punk" || np.Album != "Discovery" || np.Song != "One More Time" {
		t.Errorf("metadata = %q / %q / %q", np.Artist, np.Album, np.Song)
	}
	if np.PlaybackInfo != PlaybackInfoPlay {
		t.Errorf("PlaybackInfo = %q, want Play", np.PlaybackInfo)
	}
}

func TestReplay_SetRoundTrips(t *testing.T) {
	c := newReplayClient(t)
	ctx := testCtx(t)
	// A SET then GET should reflect the new value through the store.
	if err := c.SetDecoder(ctx, SubunitMain, "DTS Neural:X"); err != nil {
		t.Fatalf("SetDecoder: %v", err)
	}
	d, err := c.GetDecoder(ctx, SubunitMain)
	if err != nil {
		t.Fatalf("GetDecoder: %v", err)
	}
	if d != "DTS Neural:X" {
		t.Errorf("after SetDecoder, GetDecoder = %q, want DTS Neural:X", d)
	}
	// An unsupported subunit GET surfaces @UNDEFINED → typed error.
	if _, err := c.GetDecoder(ctx, SubunitZone4); err == nil {
		t.Error("GetDecoder on absent ZONE4 subunit: want error")
	}
}
