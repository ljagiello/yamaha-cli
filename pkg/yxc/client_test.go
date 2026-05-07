package yxc

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// newTestClient returns a Client targeting srv with the rate-limit and
// timeouts tuned down so the tests stay fast.
func newTestClient(t *testing.T, srv *httptest.Server, opts ...Option) *Client {
	t.Helper()
	all := append([]Option{
		WithTimeout(2 * time.Second),
	}, opts...)
	c, err := New(srv.URL, all...)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return c
}

// TestDo_Success verifies a happy-path getStatus call returns the canned
// JSON unchanged.
func TestDo_Success(t *testing.T) {
	const body = `{"response_code":0,"power":"on","volume":60,"mute":false,"input":"hdmi2","sound_program":"standard"}`

	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		if r.URL.Path != "/YamahaExtendedControl/v1/main/getStatus" {
			t.Errorf("unexpected path %q", r.URL.Path)
		}
		if got := r.Header.Get("User-Agent"); !strings.HasPrefix(got, "yamaha-cli/") {
			t.Errorf("User-Agent header missing or wrong: %q", got)
		}
		if r.Header.Get("X-AppName") != "" || r.Header.Get("X-AppPort") != "" {
			t.Errorf("non-event request unexpectedly carried event headers")
		}
		_, _ = w.Write([]byte(body))
	}))
	defer srv.Close()

	c := newTestClient(t, srv)
	raw, err := c.Do(context.Background(), "main/getStatus", nil)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	if string(raw) != body {
		t.Fatalf("body mismatch:\n got %s\nwant %s", raw, body)
	}
	if got := atomic.LoadInt32(&hits); got != 1 {
		t.Fatalf("expected 1 hit, got %d", got)
	}

	// Decode into the typed Status as a smoke test.
	var s Status
	if err := json.Unmarshal(raw, &s); err != nil {
		t.Fatalf("Status unmarshal: %v", err)
	}
	if s.Power != "on" || s.Volume != 60 || s.Input != "hdmi2" {
		t.Fatalf("Status fields wrong: %+v", s)
	}
}

// TestDo_ResponseCodeError verifies a non-zero response_code is mapped to
// a typed *Error and matches the documented sentinel via errors.Is.
func TestDo_ResponseCodeError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"response_code":6}`))
	}))
	defer srv.Close()

	c := newTestClient(t, srv)
	_, err := c.Do(context.Background(), "main/setInput", url.Values{"input": []string{"bogus"}})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
	ye, ok := AsYXC(err)
	if !ok {
		t.Fatalf("AsYXC returned false for %v", err)
	}
	if ye.Code != 6 || ye.Method != "main/setInput" {
		t.Fatalf("unexpected fields: %+v", ye)
	}
}

// TestDo_ResponseCodeMatrix exercises a set of non-sentinel YXC response
// codes to confirm: (a) the typed *Error round-trips with Code == expected,
// (b) errors.Is(err, ErrNotFound) is false for non-6 codes, (c) the message
// surfaces "unknown" for codes outside the documented sentinels.
func TestDo_ResponseCodeMatrix(t *testing.T) {
	cases := []struct {
		code int
	}{
		{1},
		{2},
		{3},
		{99},
	}
	for _, tc := range cases {
		t.Run(http.StatusText(200), func(t *testing.T) { // sub-test name is incidental; tc.code differentiates
			body := []byte(`{"response_code":` + strconv.Itoa(tc.code) + `}`)
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				_, _ = w.Write(body)
			}))
			defer srv.Close()

			c := newTestClient(t, srv)
			_, err := c.Do(context.Background(), "main/setInput", url.Values{"input": []string{"x"}})
			if err == nil {
				t.Fatalf("code=%d: expected error, got nil", tc.code)
			}
			ye, ok := AsYXC(err)
			if !ok {
				t.Fatalf("code=%d: AsYXC returned false for %v (%T)", tc.code, err, err)
			}
			if ye.Code != tc.code {
				t.Errorf("code=%d: ye.Code=%d", tc.code, ye.Code)
			}
			if errors.Is(err, ErrNotFound) {
				t.Errorf("code=%d: errors.Is(ErrNotFound) should be false", tc.code)
			}
			if !strings.Contains(ye.Message, "unknown") {
				t.Errorf("code=%d: expected message to contain 'unknown', got %q", tc.code, ye.Message)
			}
		})
	}
}

// TestDo_RetryOnTransient verifies that a connection error on the first
// attempt triggers exactly one retry, and the second attempt's success is
// returned.
func TestDo_RetryOnTransient(t *testing.T) {
	// Approach: use httptest with a custom hijacker on the first request
	// that closes the connection mid-response, then succeeds on the second.
	var attempts int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := atomic.AddInt32(&attempts, 1)
		if n == 1 {
			// Force a transport error: hijack and close abruptly.
			hj, ok := w.(http.Hijacker)
			if !ok {
				t.Fatal("ResponseWriter does not implement Hijacker")
			}
			conn, _, err := hj.Hijack()
			if err != nil {
				t.Fatalf("hijack: %v", err)
			}
			// Send a bogus partial header then close.
			_, _ = conn.Write([]byte("HTTP/1.1 200 OK\r\nContent-Length: 1000\r\n\r\n"))
			_ = conn.Close()
			return
		}
		_, _ = w.Write([]byte(`{"response_code":0}`))
	}))
	defer srv.Close()

	c := newTestClient(t, srv)
	raw, err := c.Do(context.Background(), "system/getDeviceInfo", nil)
	if err != nil {
		t.Fatalf("Do after retry: %v", err)
	}
	if string(raw) != `{"response_code":0}` {
		t.Fatalf("unexpected body: %s", raw)
	}
	if got := atomic.LoadInt32(&attempts); got != 2 {
		t.Fatalf("expected exactly 2 attempts, got %d", got)
	}
}

// TestDo_NoRetryOnResponseCode verifies that a YXC response_code error
// is NOT retried — exactly one HTTP request hits the server.
func TestDo_NoRetryOnResponseCode(t *testing.T) {
	var attempts int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&attempts, 1)
		_, _ = w.Write([]byte(`{"response_code":5}`))
	}))
	defer srv.Close()

	c := newTestClient(t, srv)
	_, err := c.Do(context.Background(), "main/setPower", url.Values{"power": []string{"on"}})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !errors.Is(err, ErrDeviceNotReady) {
		t.Fatalf("expected ErrDeviceNotReady, got %v", err)
	}
	if got := atomic.LoadInt32(&attempts); got != 1 {
		t.Fatalf("expected exactly 1 attempt, got %d", got)
	}
}

// TestDo_NoRetryOnContextCancelled verifies that user-cancellation does
// not trigger a retry.
func TestDo_NoRetryOnContextCancelled(t *testing.T) {
	var attempts int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&attempts, 1)
		// Simulate a slow response that the client will cancel.
		select {
		case <-r.Context().Done():
		case <-time.After(2 * time.Second):
		}
	}))
	defer srv.Close()

	c := newTestClient(t, srv)
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()
	_, err := c.Do(ctx, "system/getDeviceInfo", nil)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got %v", err)
	}
	if got := atomic.LoadInt32(&attempts); got != 1 {
		t.Fatalf("expected exactly 1 attempt, got %d", got)
	}
}

// TestSetVolume_AbsoluteAndRelative verifies the URL params produced for
// each VolumeArg shape.
func TestSetVolume_AbsoluteAndRelative(t *testing.T) {
	cases := []struct {
		name      string
		arg       VolumeArg
		wantQuery string
	}{
		{"absolute 60", VolumeAbsolute(60), "volume=60"},
		{"absolute 0", VolumeAbsolute(0), "volume=0"},
		{"up step 5", VolumeUp(5), "step=5&volume=up"},
		{"down step 5", VolumeDown(5), "step=5&volume=down"},
		{"up no step", VolumeUp(0), "volume=up"},
		{"down no step", VolumeDown(0), "volume=down"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var got string
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				got = r.URL.RawQuery
				_, _ = w.Write([]byte(`{"response_code":0}`))
			}))
			defer srv.Close()

			c := newTestClient(t, srv)
			if err := c.SetVolume(context.Background(), "main", tc.arg); err != nil {
				t.Fatalf("SetVolume: %v", err)
			}
			if got != tc.wantQuery {
				t.Fatalf("query mismatch:\n got %s\nwant %s", got, tc.wantQuery)
			}
		})
	}
}

// TestSetVolume_EmptyArg returns an error rather than firing a request.
func TestSetVolume_EmptyArg(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		t.Fatal("server hit unexpectedly")
	}))
	defer srv.Close()
	c := newTestClient(t, srv)
	if err := c.SetVolume(context.Background(), "main", VolumeArg{}); err == nil {
		t.Fatal("expected error for zero-value VolumeArg")
	}
}

// TestSetInput_AutoPrepare verifies prepareInputChange is issued before
// setInput when func_list contains "prepare_input_change", and skipped
// otherwise.
func TestSetInput_AutoPrepare(t *testing.T) {
	t.Run("prepare-then-set", func(t *testing.T) {
		var paths []string
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			paths = append(paths, r.URL.Path+"?"+r.URL.RawQuery)
			_, _ = w.Write([]byte(`{"response_code":0}`))
		}))
		defer srv.Close()

		c := newTestClient(t, srv)
		feat := &Features{
			Zone: []ZoneFeatures{{
				ID:       "main",
				FuncList: []string{"power", "prepare_input_change", "volume"},
			}},
		}
		if err := c.SetInput(context.Background(), "main", "hdmi2", feat); err != nil {
			t.Fatalf("SetInput: %v", err)
		}
		if len(paths) != 2 {
			t.Fatalf("expected 2 requests, got %d: %v", len(paths), paths)
		}
		if !strings.HasPrefix(paths[0], "/YamahaExtendedControl/v1/main/prepareInputChange?") {
			t.Errorf("expected prepareInputChange first, got %s", paths[0])
		}
		if !strings.HasPrefix(paths[1], "/YamahaExtendedControl/v1/main/setInput?") {
			t.Errorf("expected setInput second, got %s", paths[1])
		}
	})

	t.Run("set-only-when-not-required", func(t *testing.T) {
		var paths []string
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			paths = append(paths, r.URL.Path)
			_, _ = w.Write([]byte(`{"response_code":0}`))
		}))
		defer srv.Close()

		c := newTestClient(t, srv)
		feat := &Features{
			Zone: []ZoneFeatures{{
				ID:       "main",
				FuncList: []string{"power", "volume"},
			}},
		}
		if err := c.SetInput(context.Background(), "main", "hdmi2", feat); err != nil {
			t.Fatalf("SetInput: %v", err)
		}
		if len(paths) != 1 || paths[0] != "/YamahaExtendedControl/v1/main/setInput" {
			t.Fatalf("expected single setInput, got %v", paths)
		}
	})

	t.Run("nil-features-skips-prepare", func(t *testing.T) {
		var paths []string
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			paths = append(paths, r.URL.Path)
			_, _ = w.Write([]byte(`{"response_code":0}`))
		}))
		defer srv.Close()

		c := newTestClient(t, srv)
		if err := c.SetInput(context.Background(), "main", "hdmi2", nil); err != nil {
			t.Fatalf("SetInput: %v", err)
		}
		if len(paths) != 1 {
			t.Fatalf("expected single request, got %v", paths)
		}
	})
}

// TestNew_BaseURL verifies host/host:port/full-URL all parse.
func TestNew_BaseURL(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"192.168.1.116", "http://192.168.1.116"},
		{"192.168.1.116:80", "http://192.168.1.116:80"},
		{"http://192.168.1.116/", "http://192.168.1.116"},
	}
	for _, tc := range cases {
		c, err := New(tc.in)
		if err != nil {
			t.Errorf("New(%q): %v", tc.in, err)
			continue
		}
		if c.BaseURL() != tc.want {
			t.Errorf("BaseURL: got %q want %q", c.BaseURL(), tc.want)
		}
	}
}

// TestNew_RejectsBadURL ensures bad inputs return an error.
func TestNew_RejectsBadURL(t *testing.T) {
	bad := []string{"", "https://example.com", "://nope"}
	for _, s := range bad {
		if _, err := New(s); err == nil {
			t.Errorf("New(%q): expected error", s)
		}
	}
}

// TestEventDo_Headers verifies X-AppName / X-AppPort are added when
// EventDo is called and only then.
func TestEventDo_Headers(t *testing.T) {
	var name, port string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		name = r.Header.Get("X-AppName")
		port = r.Header.Get("X-AppPort")
		_, _ = w.Write([]byte(`{"response_code":0}`))
	}))
	defer srv.Close()

	c := newTestClient(t, srv, WithEventPort(41100))
	if _, err := c.EventDo(context.Background(), "main/getStatus", nil); err != nil {
		t.Fatalf("EventDo: %v", err)
	}
	if name != "MusicCast" || port != "41100" {
		t.Fatalf("event headers wrong: name=%q port=%q", name, port)
	}
}

// TestEventDo_RequiresPort returns an error when no port has been set.
func TestEventDo_RequiresPort(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		t.Fatal("server hit unexpectedly")
	}))
	defer srv.Close()
	c := newTestClient(t, srv)
	if _, err := c.EventDo(context.Background(), "main/getStatus", nil); err == nil {
		t.Fatal("expected error, got nil")
	}
}

// TestRateLimit verifies that two back-to-back requests are at least
// ~100ms apart.
func TestRateLimit(t *testing.T) {
	var times []time.Time
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		times = append(times, time.Now())
		_, _ = w.Write([]byte(`{"response_code":0}`))
	}))
	defer srv.Close()

	c := newTestClient(t, srv)
	for i := 0; i < 2; i++ {
		if _, err := c.Do(context.Background(), "system/getDeviceInfo", nil); err != nil {
			t.Fatalf("Do %d: %v", i, err)
		}
	}
	if len(times) != 2 {
		t.Fatalf("expected 2 server hits, got %d", len(times))
	}
	gap := times[1].Sub(times[0])
	// Allow some scheduling slack but require ~95ms minimum.
	if gap < 95*time.Millisecond {
		t.Fatalf("rate-limit not enforced: gap=%v", gap)
	}
}

// TestGetFeatures_Fixture parses the live-device fixture and surfaces
// the helper-method outputs the CLI relies on.
func TestGetFeatures_Fixture(t *testing.T) {
	// Read fixture from testdata at the repo root by walking up from the
	// package dir.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.ServeFile(w, &http.Request{URL: &url.URL{Path: "/"}}, "../../testdata/getFeatures.json")
	}))
	defer srv.Close()

	c := newTestClient(t, srv)
	f, err := c.GetFeatures(context.Background())
	if err != nil {
		t.Fatalf("GetFeatures: %v", err)
	}
	if len(f.Zone) != 2 {
		t.Fatalf("expected 2 zones, got %d", len(f.Zone))
	}
	if !f.ZoneHasFunc("main", "prepare_input_change") {
		t.Errorf("main zone missing prepare_input_change")
	}
	min, max, step, ok := f.VolumeRange("main")
	if !ok {
		t.Fatal("VolumeRange(main) not found")
	}
	if min != 0 || max != 161 || step != 1 {
		t.Errorf("volume range wrong: %d..%d step %d", min, max, step)
	}
	inputs := f.SystemInputIDs()
	if len(inputs) != 22 {
		t.Errorf("expected 22 inputs, got %d", len(inputs))
	}
	progs := f.ZoneSoundPrograms("main")
	if len(progs) == 0 {
		t.Error("expected sound programs for main")
	}
}

// TestHTTPNon200_NoRetry verifies a 4xx/5xx response surfaces as an
// httpStatusError without a retry.
func TestHTTPNon200_NoRetry(t *testing.T) {
	var attempts int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&attempts, 1)
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer srv.Close()

	c := newTestClient(t, srv)
	_, err := c.Do(context.Background(), "system/getDeviceInfo", nil)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	var hse *httpStatusError
	if !errors.As(err, &hse) {
		t.Fatalf("expected httpStatusError, got %v (%T)", err, err)
	}
	if hse.Status != http.StatusInternalServerError {
		t.Errorf("status: got %d want %d", hse.Status, http.StatusInternalServerError)
	}
	if got := atomic.LoadInt32(&attempts); got != 1 {
		t.Fatalf("expected exactly 1 attempt, got %d", got)
	}
}

// TestConnectionRefused_RetriesOnce points the client at a closed port
// and verifies it returns a transport error after retrying once.
func TestConnectionRefused_RetriesOnce(t *testing.T) {
	// Bind a listener, capture its address, then close it so the port is
	// almost certainly free for the brief test window.
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	addr := l.Addr().String()
	_ = l.Close()

	c, err := New("http://"+addr, WithTimeout(500*time.Millisecond))
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	start := time.Now()
	_, err = c.Do(context.Background(), "system/getDeviceInfo", nil)
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !IsTransport(err) {
		t.Fatalf("expected transport error, got %v (%T)", err, err)
	}
	// Two attempts + 250ms backoff; this is approximate, just sanity.
	if elapsed < 200*time.Millisecond {
		t.Errorf("expected retry to add backoff (>=200ms), elapsed=%v", elapsed)
	}
}
