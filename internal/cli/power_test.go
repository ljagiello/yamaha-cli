package cli

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ljagiello/yamaha-cli/pkg/yxc"
)

// statusServer returns an httptest.Server whose getStatus handler reports
// "standby" for the first standbyHits requests and "on" thereafter. The
// returned getCounter records every getStatus hit so tests can assert on
// poll counts.
func statusServer(t *testing.T, standbyHits int32) (srv *httptest.Server, getCounter *atomic.Int32) {
	t.Helper()
	getCounter = &atomic.Int32{}
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/main/getStatus") {
			http.Error(w, "unexpected path", http.StatusNotFound)
			return
		}
		hits := getCounter.Add(1)
		power := "on"
		if hits <= standbyHits {
			power = "standby"
		}
		fmt.Fprintf(w, `{"response_code":0,"power":%q,"volume":0,"mute":false,"input":"hdmi1"}`, power)
	}))
	t.Cleanup(srv.Close)
	return srv, getCounter
}

// powerStateFor builds a minimal *state targeting srv. Anonymous (alias=""):
// runWithRediscover skips the rediscover path on transport errors, but
// these tests don't need it — the server always responds.
func powerStateFor(t *testing.T, srv *httptest.Server) *state {
	t.Helper()
	u, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatalf("parse server URL: %v", err)
	}
	c, err := yxc.New(u.Host)
	if err != nil {
		t.Fatalf("yxc.New: %v", err)
	}
	return &state{zone: "main", client: c}
}

// shortenPowerTimers swaps the package-level power-on timers to
// test-friendly values for the duration of one test, restoring on cleanup.
// Production callers never touch these vars.
func shortenPowerTimers(t *testing.T, total, tick time.Duration) {
	t.Helper()
	prevTotal, prevTick := powerOnTimeout, powerPollInterval
	powerOnTimeout = total
	powerPollInterval = tick
	t.Cleanup(func() {
		powerOnTimeout = prevTotal
		powerPollInterval = prevTick
	})
}

// TestWaitForPowerOn_SettlesAfterStandbyHits asserts that waitForPowerOn
// returns nil once the device transitions to power=on, exercising the
// happy path documented in the README ("Power-on wait").
func TestWaitForPowerOn_SettlesAfterStandbyHits(t *testing.T) {
	shortenPowerTimers(t, 2*time.Second, 10*time.Millisecond)
	srv, hits := statusServer(t, 3) // 3 polls return standby, then on
	s := powerStateFor(t, srv)

	if err := waitForPowerOn(context.Background(), s); err != nil {
		t.Fatalf("expected nil, got %v", err)
	}
	if got := hits.Load(); got < 4 {
		t.Errorf("expected ≥4 polls (3 standby + 1 on), got %d", got)
	}
}

// TestWaitForPowerOn_Timeout asserts the README's "10 s timeout → exit 1
// via *PowerOnTimeoutError" path: when the device never reports on, the
// loop deadline fires and returns the typed error.
func TestWaitForPowerOn_Timeout(t *testing.T) {
	shortenPowerTimers(t, 100*time.Millisecond, 10*time.Millisecond)
	srv, _ := statusServer(t, 1<<30) // effectively never reaches "on"
	s := powerStateFor(t, srv)

	start := time.Now()
	err := waitForPowerOn(context.Background(), s)
	elapsed := time.Since(start)

	if err == nil {
		t.Fatalf("expected *PowerOnTimeoutError, got nil")
	}
	var poe *PowerOnTimeoutError
	if !errors.As(err, &poe) {
		t.Fatalf("expected *PowerOnTimeoutError, got %T (%v)", err, err)
	}
	if poe.Zone != "main" {
		t.Errorf("Zone: got %q, want main", poe.Zone)
	}
	// Should have fired within the budget (with some slack for slow CI).
	if elapsed > 2*time.Second {
		t.Errorf("waited too long: %v", elapsed)
	}
	// And ErrorExitCode should map this to exit 1.
	if got := ErrorExitCode(err); got != 1 {
		t.Errorf("ErrorExitCode: got %d, want 1", got)
	}
}

// TestWaitForPowerOn_Cancelled asserts that SIGINT during the poll loop
// returns context.Canceled — which Execute() then wraps as
// *cancelledError so the exit-code mapper returns 130.
func TestWaitForPowerOn_Cancelled(t *testing.T) {
	shortenPowerTimers(t, 5*time.Second, 50*time.Millisecond)
	srv, _ := statusServer(t, 1<<30) // never reaches "on"
	s := powerStateFor(t, srv)

	ctx, cancel := context.WithCancel(context.Background())
	// Cancel after a short delay so at least one tick has fired.
	go func() {
		time.Sleep(80 * time.Millisecond)
		cancel()
	}()

	start := time.Now()
	err := waitForPowerOn(ctx, s)
	elapsed := time.Since(start)

	if err == nil {
		t.Fatalf("expected error, got nil")
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got %v", err)
	}
	if elapsed > 500*time.Millisecond {
		t.Errorf("cancellation should be near-instant; took %v", elapsed)
	}
}

// TestPollPowerOnce_ReportsState exercises the happy path of the
// inner-loop helper directly. Useful for catching shape regressions in
// the getStatus parsing without driving the full deadline machinery.
func TestPollPowerOnce_ReportsState(t *testing.T) {
	srv, _ := statusServer(t, 0) // always reports power=on
	s := powerStateFor(t, srv)

	on, err := pollPowerOnce(context.Background(), s)
	if err != nil {
		t.Fatalf("pollPowerOnce: %v", err)
	}
	if !on {
		t.Errorf("expected power=on, got false")
	}
}

// TestShouldWaitForOn covers the off→on transition truth table. The
// runtime relies on this to skip the post-power-off wait.
func TestShouldWaitForOn(t *testing.T) {
	cases := []struct {
		arg, prior string
		want       bool
	}{
		{"on", "", true},
		{"on", "standby", true},
		{"on", "on", true}, // already on, but still "wants on"; loop returns immediately
		{"toggle", "standby", true},
		{"toggle", "off", true},
		{"toggle", "on", false}, // toggling on→standby; no wait
		{"standby", "on", false},
		{"unknown", "", false},
	}
	for _, tc := range cases {
		t.Run(fmt.Sprintf("arg=%s,prior=%s", tc.arg, tc.prior), func(t *testing.T) {
			if got := shouldWaitForOn(tc.arg, tc.prior); got != tc.want {
				t.Errorf("got %v, want %v", got, tc.want)
			}
		})
	}
}

// TestMapPowerArg covers the CLI→YXC vocabulary mapping.
func TestMapPowerArg(t *testing.T) {
	cases := []struct {
		in, want string
		err      bool
	}{
		{"on", "on", false},
		{"off", "standby", false},
		{"standby", "standby", false},
		{"toggle", "toggle", false},
		{"foo", "", true},
		{"", "", true},
	}
	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			got, err := mapPowerArg(tc.in)
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
