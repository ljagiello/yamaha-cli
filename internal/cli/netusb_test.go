package cli

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ljagiello/yamaha-cli/internal/config"
	"github.com/ljagiello/yamaha-cli/pkg/yxc"
)

// newNetUSBTestState builds a *state pointing at srv. NetUSB commands
// don't need the on-disk features cache (they don't validate against
// it), so this is shorter than the tuner equivalent.
func newNetUSBTestState(t *testing.T, srv *httptest.Server) *state {
	t.Helper()
	c, err := yxc.New(srv.URL)
	if err != nil {
		t.Fatalf("yxc.New: %v", err)
	}
	return &state{
		cfg:    &config.Config{Devices: map[string]config.Device{}},
		alias:  "test",
		device: config.Device{Host: srv.URL, DefaultZone: "main"},
		zone:   "main",
		client: c,
	}
}

// shortenSeekBracket swaps fastSeekBracket to a test-friendly value and
// restores on cleanup. Tests need this so the start/end pair completes
// quickly without sleeping for 200 ms.
func shortenSeekBracket(t *testing.T, d time.Duration) {
	t.Helper()
	prev := fastSeekBracket
	fastSeekBracket = d
	t.Cleanup(func() { fastSeekBracket = prev })
}

// TestRunNetUSB_PlayHitsSetPlaybackOnce asserts a single-verb command
// (play) fires exactly one setPlayback request with playback=play.
func TestRunNetUSB_PlayHitsSetPlaybackOnce(t *testing.T) {
	var (
		hits     int32
		gotQuery string
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/netusb/setPlayback") {
			t.Errorf("unexpected path %q", r.URL.Path)
			http.NotFound(w, r)
			return
		}
		atomic.AddInt32(&hits, 1)
		gotQuery = r.URL.RawQuery
		_, _ = w.Write([]byte(`{"response_code":0}`))
	}))
	defer srv.Close()

	s := newNetUSBTestState(t, srv)
	cmd := newNetUSBPlaybackCmd("play")
	cmd.SetContext(context.Background())
	setStateOnCmd(cmd, s)
	cmd.SetOut(&strings.Builder{})
	cmd.SetErr(&strings.Builder{})

	if err := cmd.RunE(cmd, nil); err != nil {
		t.Fatalf("RunE: %v", err)
	}
	if got := atomic.LoadInt32(&hits); got != 1 {
		t.Fatalf("setPlayback hits: got %d, want 1", got)
	}
	if gotQuery != "playback=play" {
		t.Errorf("query: got %q, want %q", gotQuery, "playback=play")
	}
}

// TestRunNetUSB_ToggleMapsToPlayPause asserts the CLI verb "toggle" maps
// to playback=play_pause on the wire — the README convention.
func TestRunNetUSB_ToggleMapsToPlayPause(t *testing.T) {
	var gotQuery string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.RawQuery
		_, _ = w.Write([]byte(`{"response_code":0}`))
	}))
	defer srv.Close()

	s := newNetUSBTestState(t, srv)
	cmd := newNetUSBPlaybackCmd("toggle")
	cmd.SetContext(context.Background())
	setStateOnCmd(cmd, s)
	cmd.SetOut(&strings.Builder{})
	cmd.SetErr(&strings.Builder{})

	if err := cmd.RunE(cmd, nil); err != nil {
		t.Fatalf("RunE: %v", err)
	}
	if gotQuery != "playback=play_pause" {
		t.Errorf("query: got %q, want %q", gotQuery, "playback=play_pause")
	}
}

// TestRunNetUSB_FFEmitsStartThenEnd asserts the README acceptance
// criterion: `netusb ff` produces *two* HTTP requests, the first carrying
// fast_forward_start and the second fast_forward_end ~200 ms later. The
// test shortens the bracket so it completes near-instantly.
func TestRunNetUSB_FFEmitsStartThenEnd(t *testing.T) {
	shortenSeekBracket(t, 5*time.Millisecond)

	var (
		mu       sync.Mutex
		queries  []string
		hitTimes []time.Time
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		queries = append(queries, r.URL.RawQuery)
		hitTimes = append(hitTimes, time.Now())
		mu.Unlock()
		_, _ = w.Write([]byte(`{"response_code":0}`))
	}))
	defer srv.Close()

	s := newNetUSBTestState(t, srv)
	cmd := newNetUSBPlaybackCmd("ff")
	cmd.SetContext(context.Background())
	setStateOnCmd(cmd, s)
	cmd.SetOut(&strings.Builder{})
	cmd.SetErr(&strings.Builder{})

	if err := cmd.RunE(cmd, nil); err != nil {
		t.Fatalf("RunE: %v", err)
	}

	mu.Lock()
	gotN := len(queries)
	q0, q1 := "", ""
	if gotN >= 2 {
		q0 = queries[0]
		q1 = queries[1]
	}
	delta := time.Duration(0)
	if gotN >= 2 {
		delta = hitTimes[1].Sub(hitTimes[0])
	}
	mu.Unlock()

	if gotN != 2 {
		t.Fatalf("setPlayback hits: got %d, want 2 (start + end)", gotN)
	}
	if q0 != "playback=fast_forward_start" {
		t.Errorf("first query: got %q, want fast_forward_start", q0)
	}
	if q1 != "playback=fast_forward_end" {
		t.Errorf("second query: got %q, want fast_forward_end", q1)
	}
	// And the bracket should have separated them by *at least* the
	// shortened bracket duration. We allow generous slack so CI doesn't
	// flake.
	if delta < 5*time.Millisecond {
		t.Errorf("start/end delta too small: %v (want >= 5ms)", delta)
	}
}

// TestRunNetUSB_RewEmitsStartThenEnd mirrors the FF test for fast-reverse;
// the start/end vocabulary differs.
func TestRunNetUSB_RewEmitsStartThenEnd(t *testing.T) {
	shortenSeekBracket(t, 5*time.Millisecond)

	var (
		mu      sync.Mutex
		queries []string
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		queries = append(queries, r.URL.RawQuery)
		mu.Unlock()
		_, _ = w.Write([]byte(`{"response_code":0}`))
	}))
	defer srv.Close()

	s := newNetUSBTestState(t, srv)
	cmd := newNetUSBPlaybackCmd("rew")
	cmd.SetContext(context.Background())
	setStateOnCmd(cmd, s)
	cmd.SetOut(&strings.Builder{})
	cmd.SetErr(&strings.Builder{})

	if err := cmd.RunE(cmd, nil); err != nil {
		t.Fatalf("RunE: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(queries) != 2 {
		t.Fatalf("got %d queries, want 2", len(queries))
	}
	if queries[0] != "playback=fast_reverse_start" {
		t.Errorf("first query: got %q", queries[0])
	}
	if queries[1] != "playback=fast_reverse_end" {
		t.Errorf("second query: got %q", queries[1])
	}
}

// TestRunNetUSB_ShuffleHitsToggleShuffle asserts shuffle is just a toggle
// against the receiver and emits {} on success.
func TestRunNetUSB_ShuffleHitsToggleShuffle(t *testing.T) {
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/netusb/toggleShuffle") {
			t.Errorf("unexpected path %q", r.URL.Path)
		}
		atomic.AddInt32(&hits, 1)
		_, _ = w.Write([]byte(`{"response_code":0}`))
	}))
	defer srv.Close()

	s := newNetUSBTestState(t, srv)
	cmd := newNetUSBShuffleCmd()
	cmd.SetContext(context.Background())
	setStateOnCmd(cmd, s)
	cmd.SetOut(&strings.Builder{})
	cmd.SetErr(&strings.Builder{})

	if err := cmd.RunE(cmd, nil); err != nil {
		t.Fatalf("RunE: %v", err)
	}
	if got := atomic.LoadInt32(&hits); got != 1 {
		t.Errorf("toggleShuffle hits: got %d, want 1", got)
	}
}

// TestRunNetUSB_RepeatHitsToggleRepeat mirrors the shuffle test for
// repeat.
func TestRunNetUSB_RepeatHitsToggleRepeat(t *testing.T) {
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/netusb/toggleRepeat") {
			t.Errorf("unexpected path %q", r.URL.Path)
		}
		atomic.AddInt32(&hits, 1)
		_, _ = w.Write([]byte(`{"response_code":0}`))
	}))
	defer srv.Close()

	s := newNetUSBTestState(t, srv)
	cmd := newNetUSBRepeatCmd()
	cmd.SetContext(context.Background())
	setStateOnCmd(cmd, s)
	cmd.SetOut(&strings.Builder{})
	cmd.SetErr(&strings.Builder{})

	if err := cmd.RunE(cmd, nil); err != nil {
		t.Fatalf("RunE: %v", err)
	}
	if got := atomic.LoadInt32(&hits); got != 1 {
		t.Errorf("toggleRepeat hits: got %d, want 1", got)
	}
}

// TestRunNetUSB_InfoRendersMetadata exercises the now-playing render: the
// payload must surface artist / album / track when the device reports them.
func TestRunNetUSB_InfoRendersMetadata(t *testing.T) {
	const body = `{"response_code":0,"input":"server","playback":"play","repeat":"off","shuffle":"on","play_time":42,"total_time":300,"artist":"Aphex","album":"Selected","track":"Xtal"}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/netusb/getPlayInfo") {
			t.Errorf("unexpected path %q", r.URL.Path)
		}
		_, _ = w.Write([]byte(body))
	}))
	defer srv.Close()

	s := newNetUSBTestState(t, srv)
	cmd := newNetUSBInfoCmd()
	cmd.SetContext(context.Background())
	setStateOnCmd(cmd, s)
	out := &strings.Builder{}
	cmd.SetOut(out)
	cmd.SetErr(&strings.Builder{})

	if err := cmd.RunE(cmd, nil); err != nil {
		t.Fatalf("RunE: %v", err)
	}
	got := out.String()
	for _, want := range []string{"Aphex", "Selected", "Xtal", "server"} {
		if !strings.Contains(got, want) {
			t.Errorf("rendered output missing %q: %s", want, got)
		}
	}
}

// TestMapPlaybackVerb covers the verb→Playback mapping table used by the
// single-verb commands.
func TestMapPlaybackVerb(t *testing.T) {
	cases := []struct {
		in   string
		want yxc.Playback
		err  bool
	}{
		{"play", yxc.PlaybackPlay, false},
		{"pause", yxc.PlaybackPause, false},
		{"stop", yxc.PlaybackStop, false},
		{"next", yxc.PlaybackNext, false},
		{"prev", yxc.PlaybackPrevious, false},
		{"previous", yxc.PlaybackPrevious, false},
		{"toggle", yxc.PlaybackPlayPause, false},
		{"play_pause", yxc.PlaybackPlayPause, false},
		{"bogus", "", true},
	}
	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			got, err := mapPlaybackVerb(tc.in)
			if tc.err && err == nil {
				t.Fatalf("expected error, got %q", got)
			}
			if !tc.err && err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}

// TestRunNetUSB_BogusVerbRejected exercises the runNetUSBVerb fallback —
// passing an unknown verb to the helper (not via cobra) returns a
// usageError.
func TestRunNetUSB_BogusVerbRejected(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {
		t.Fatal("unexpected wire call on bogus verb")
	}))
	defer srv.Close()

	s := newNetUSBTestState(t, srv)
	err := runNetUSBVerb(context.Background(), s, "bogus")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	var uerr *usageError
	if !errors.As(err, &uerr) {
		t.Fatalf("expected *usageError, got %T (%v)", err, err)
	}
}
