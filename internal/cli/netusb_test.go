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
	"github.com/ljagiello/yamaha-cli/pkg/discover"
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

// TestRunNetUSB_FFCtxCancelStillSendsEnd asserts the README claim
// "end-request fires even on ctx cancel (fresh 2s context)". When the
// user hits Ctrl-C between the start and end frames, the receiver
// would otherwise be stuck in fast-seek mode; the cleanup path must
// still emit fast_forward_end.
//
// This test also locks the v3-review fix that the cancellation cleanup
// must NOT route through runWithRediscover (which would persist a
// rediscovered host as a side effect of SIGINT). The stub server is
// alive throughout, so a working rediscover would be silent — but if
// the production code regresses to use runWithRediscover, that's a
// separate review concern; this test only asserts the user-visible
// behaviour: the end-request still fires, against the original host.
func TestRunNetUSB_FFCtxCancelStillSendsEnd(t *testing.T) {
	// Bracket longer than the cancel scheduling so the goroutine is
	// guaranteed to be inside the timer wait when ctx is cancelled.
	shortenSeekBracket(t, 200*time.Millisecond)

	// v3-review #6: also assert the cancellation path skips
	// runWithRediscover. Install a lookupByUDNFn counter; if rediscover
	// fires during the cancel cleanup the count goes up. Production
	// must call s.client.SetPlayback directly on the cancel path so
	// SIGINT cleanup never has the side effect of persisting a
	// rediscovered host.
	var lookupHits atomic.Int32
	prevLookup := lookupByUDNFn
	lookupByUDNFn = func(ctx context.Context, udn string, timeout time.Duration) (discover.Device, error) {
		lookupHits.Add(1)
		return discover.Device{}, errors.New("test: lookup must not be called on cancel path")
	}
	t.Cleanup(func() { lookupByUDNFn = prevLookup })

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
	cmd := newNetUSBPlaybackCmd("ff")
	ctx, cancel := context.WithCancel(context.Background())
	cmd.SetContext(ctx)
	setStateOnCmd(cmd, s)
	cmd.SetOut(&strings.Builder{})
	cmd.SetErr(&strings.Builder{})

	// Kick off the bracketed seek; cancel once the start request has
	// landed (signalled by len(queries) becoming 1). Polling the
	// counter rather than sleeping a fixed 40ms keeps the test
	// deterministic on slow CI runners.
	done := make(chan error, 1)
	go func() {
		done <- cmd.RunE(cmd, nil)
	}()
	deadline := time.Now().Add(2 * time.Second)
	for {
		mu.Lock()
		landed := len(queries) >= 1
		mu.Unlock()
		if landed {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("start request did not land within 2s — production may have changed shape")
		}
		time.Sleep(2 * time.Millisecond)
	}
	cancel()

	if err := <-done; err != nil {
		t.Fatalf("RunE: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(queries) != 2 {
		t.Fatalf("setPlayback hits: got %d (%v), want 2 (start + end after cancel)", len(queries), queries)
	}
	if queries[0] != "playback=fast_forward_start" {
		t.Errorf("first query: got %q, want fast_forward_start", queries[0])
	}
	if queries[1] != "playback=fast_forward_end" {
		t.Errorf("second query: got %q, want fast_forward_end (must fire even after ctx cancel)", queries[1])
	}
	// And the cancel-cleanup must have skipped runWithRediscover.
	if got := lookupHits.Load(); got != 0 {
		t.Errorf("lookupByUDNFn calls on cancel path: got %d, want 0 (cleanup must not persist a rediscovered host)", got)
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
