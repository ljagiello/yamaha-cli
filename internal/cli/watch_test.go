package cli

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ljagiello/yamaha-cli/internal/config"
	"github.com/ljagiello/yamaha-cli/pkg/yxc"
)

// shrinkWatchTimers tightens the subscriber timers so tests don't sit
// for 30 seconds of UDP silence before timing out.
func shrinkWatchTimers(t *testing.T) {
	t.Helper()
	prevMin, prevMax, prevSilent := watchBackoffMin, watchBackoffMax, watchSilentAfter
	watchBackoffMin = 5 * time.Millisecond
	watchBackoffMax = 20 * time.Millisecond
	watchSilentAfter = 5 * time.Second
	t.Cleanup(func() {
		watchBackoffMin = prevMin
		watchBackoffMax = prevMax
		watchSilentAfter = prevSilent
	})
}

// captureSubscribeServer is an httptest.Server that handles the
// subscribe HTTP GET, captures the X-AppPort header so the test can
// drive UDP packets back, and replies with a canned getStatus body.
type captureSubscribeServer struct {
	srv  *httptest.Server
	mu   sync.Mutex
	port int // X-AppPort the subscriber sent
	got  chan struct{}
}

func newCaptureSubscribeServer(t *testing.T) *captureSubscribeServer {
	t.Helper()
	c := &captureSubscribeServer{got: make(chan struct{}, 4)}
	c.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if p := r.Header.Get("X-AppPort"); p != "" {
			n, _ := strconv.Atoi(p)
			c.mu.Lock()
			if c.port == 0 {
				c.port = n
				select {
				case c.got <- struct{}{}:
				default:
				}
			}
			c.mu.Unlock()
		}
		// All YXC methods reply success; we only need the subscribe to
		// land.
		_, _ = io.WriteString(w, `{"response_code":0}`)
	}))
	t.Cleanup(c.srv.Close)
	return c
}

// awaitPort blocks until the X-AppPort is captured or t fails.
func (c *captureSubscribeServer) awaitPort(t *testing.T) int {
	t.Helper()
	select {
	case <-c.got:
	case <-time.After(2 * time.Second):
		t.Fatal("subscribe never landed on fake server")
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.port
}

func newWatchState(t *testing.T, srv *httptest.Server) *state {
	t.Helper()
	u, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatalf("url.Parse: %v", err)
	}
	c, err := yxc.New(u.Scheme + "://" + u.Host)
	if err != nil {
		t.Fatalf("yxc.New: %v", err)
	}
	return &state{
		device: config.Device{Host: u.Host},
		alias:  "test",
		zone:   "main",
		client: c,
	}
}

// TestWatch_NDJSONForward verifies the end-to-end behaviour: a
// synthetic UDP event arrives, watch decodes it, and emits one NDJSON
// line on stdout with the expected wrapper shape.
func TestWatch_NDJSONForward(t *testing.T) {
	shrinkWatchTimers(t)

	cap := newCaptureSubscribeServer(t)
	cmd := newWatchCmd()

	// Pipe stdout so we can read line-by-line as the goroutine writes.
	r, w := io.Pipe()
	cmd.SetOut(w)
	cmd.SetErr(io.Discard)
	cmd.SetArgs([]string{})
	// Force JSON output (otherwise on a non-TTY auto picks JSON anyway,
	// but be explicit).
	cmd.PersistentFlags().String("output", "json", "")

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	cmd.SetContext(context.WithValue(ctx, stateKey, newWatchState(t, cap.srv)))

	// Run watch in a goroutine. It returns nil after ctx cancel.
	done := make(chan error, 1)
	go func() {
		done <- cmd.Execute()
		_ = w.Close()
	}()

	// Wait for the subscribe HTTP call so we know the UDP port is bound.
	port := cap.awaitPort(t)

	// Send a synthetic UDP event packet.
	udp, err := net.Dial("udp4", "127.0.0.1:"+strconv.Itoa(port))
	if err != nil {
		t.Fatalf("dial udp: %v", err)
	}
	const payload = `{"main":{"volume":50}}`
	if _, err := udp.Write([]byte(payload)); err != nil {
		t.Fatalf("write udp: %v", err)
	}
	_ = udp.Close()

	// Read NDJSON lines until we see the data event (skip the initial
	// "subscribe" control line). 5s budget.
	scanner := bufio.NewScanner(r)
	deadline := time.AfterFunc(5*time.Second, func() {
		_ = w.CloseWithError(io.EOF)
	})
	defer deadline.Stop()

	var found map[string]any
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		var obj map[string]any
		if err := json.Unmarshal(line, &obj); err != nil {
			t.Fatalf("non-JSON line: %q (%v)", line, err)
		}
		if _, hasDelta := obj["delta"]; hasDelta {
			found = obj
			break
		}
	}

	if found == nil {
		t.Fatal("never saw a data event line")
	}
	if got := found["device"]; got != "test" {
		t.Errorf("device: got %v want test", got)
	}
	delta, ok := found["delta"].(map[string]any)
	if !ok {
		t.Fatalf("delta not an object: %v", found["delta"])
	}
	main, ok := delta["main"].(map[string]any)
	if !ok {
		t.Fatalf("delta.main not an object: %v", delta["main"])
	}
	if main["volume"].(float64) != 50 {
		t.Errorf("delta.main.volume: got %v want 50", main["volume"])
	}

	// Drain the pipe in the background so the watch goroutine isn't
	// blocked on the writer side after cancel.
	go func() {
		for scanner.Scan() {
		}
	}()

	// Cancel the context; the command should return cleanly.
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("Execute: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("watch did not exit within 3s of cancel")
	}
}

// TestWatch_TableMode verifies the human-readable single-line output
// format. We send one UDP event with two leaves and assert that two
// "ts  device  path = value" lines appear on stdout.
func TestWatch_TableMode(t *testing.T) {
	shrinkWatchTimers(t)

	cap := newCaptureSubscribeServer(t)
	cmd := newWatchCmd()

	r, w := io.Pipe()
	cmd.SetOut(w)
	cmd.SetErr(io.Discard)
	cmd.SetArgs([]string{})
	cmd.PersistentFlags().String("output", "table", "")

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	cmd.SetContext(context.WithValue(ctx, stateKey, newWatchState(t, cap.srv)))

	done := make(chan error, 1)
	go func() {
		done <- cmd.Execute()
		_ = w.Close()
	}()

	port := cap.awaitPort(t)
	udp, err := net.Dial("udp4", "127.0.0.1:"+strconv.Itoa(port))
	if err != nil {
		t.Fatalf("dial udp: %v", err)
	}
	const payload = `{"main":{"volume":50,"input":"hdmi2"}}`
	if _, err := udp.Write([]byte(payload)); err != nil {
		t.Fatalf("write udp: %v", err)
	}
	_ = udp.Close()

	scanner := bufio.NewScanner(r)
	deadline := time.AfterFunc(5*time.Second, func() {
		_ = w.CloseWithError(io.EOF)
	})
	defer deadline.Stop()

	wantSubstrings := []string{"main.volume = 50", "main.input = hdmi2"}
	found := map[string]bool{}
	for scanner.Scan() {
		line := scanner.Text()
		for _, s := range wantSubstrings {
			if strings.Contains(line, s) {
				found[s] = true
			}
		}
		if len(found) == len(wantSubstrings) {
			break
		}
	}

	// Drain the pipe so the writer goroutine exits cleanly on cancel.
	go func() {
		for scanner.Scan() {
		}
	}()

	cancel()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("watch did not exit after cancel")
	}

	for _, s := range wantSubstrings {
		if !found[s] {
			t.Errorf("missing substring %q in output", s)
		}
	}
}

// TestWatch_CleanShutdown asserts that cancelling the context drains
// the channel and Execute returns nil (exit 0) cleanly.
func TestWatch_CleanShutdown(t *testing.T) {
	shrinkWatchTimers(t)

	cap := newCaptureSubscribeServer(t)
	cmd := newWatchCmd()

	var stdout bytes.Buffer
	cmd.SetOut(&stdout)
	cmd.SetErr(io.Discard)
	cmd.SetArgs([]string{})
	cmd.PersistentFlags().String("output", "json", "")

	ctx, cancel := context.WithCancel(context.Background())
	cmd.SetContext(context.WithValue(ctx, stateKey, newWatchState(t, cap.srv)))

	done := make(chan error, 1)
	go func() { done <- cmd.Execute() }()

	cap.awaitPort(t) // ensure the subscribe landed
	// Give the subscriber a moment to send the "subscribe" event.
	time.Sleep(50 * time.Millisecond)
	cancel()

	select {
	case err := <-done:
		if err != nil {
			t.Errorf("Execute: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("watch did not exit within 3s of cancel")
	}
}

// TestWatch_FlattenForWatch verifies the dot-path flattener directly.
func TestWatch_FlattenForWatch(t *testing.T) {
	t.Parallel()
	in := map[string]any{
		"main": map[string]any{
			"volume": 60,
			"mute":   false,
			"deep": map[string]any{
				"x": "y",
			},
		},
		"system": map[string]any{
			"power": "on",
		},
	}
	got := flattenForWatch(in)
	wantKeys := []string{"main.volume", "main.mute", "main.deep.x", "system.power"}
	for _, k := range wantKeys {
		if _, ok := got[k]; !ok {
			t.Errorf("missing key %q in %v", k, got)
		}
	}
}

// TestWatch_SplitCSV covers the multi-device alias parser.
func TestWatch_SplitCSV(t *testing.T) {
	t.Parallel()
	cases := []struct {
		in   string
		want []string
	}{
		{"", nil},
		{"a", []string{"a"}},
		{"a,b,c", []string{"a", "b", "c"}},
		{"a, b , ,c", []string{"a", "b", "c"}},
	}
	for _, tc := range cases {
		got := splitCSV(tc.in)
		// nil and empty slice both treated as "no aliases"; normalise.
		if len(got) != len(tc.want) {
			t.Errorf("splitCSV(%q) = %v want %v", tc.in, got, tc.want)
			continue
		}
		for i := range got {
			if got[i] != tc.want[i] {
				t.Errorf("splitCSV(%q)[%d] = %q want %q", tc.in, i, got[i], tc.want[i])
			}
		}
	}
}
