package cli

import (
	"bufio"
	"bytes"
	"context"
	"io"
	"net"
	"sync/atomic"
	"testing"
	"time"
)

// watchFakeYNCA starts a fake that answers the wake/probe handshake and, for
// every *session* connection (one that never sends @SYS:VERSION=?), pushes
// one report then closes after a short delay — forcing the watch supervisor
// to reconnect. The probe connection (which does send @SYS:VERSION=?) is left
// for the client to close. connCount counts every accepted connection.
//
// The drop delay (120ms) is deliberately well past the client's wake-drain
// window (~40-80ms) so the probe's @SYS:VERSION=? always arrives first and a
// probe connection is never dropped mid-handshake.
func watchFakeYNCA(t *testing.T) (addr string, connCount *atomic.Int32) {
	t.Helper()
	var conns atomic.Int32
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	go func() {
		for {
			conn, aerr := l.Accept()
			if aerr != nil {
				return
			}
			conns.Add(1)
			go func(c net.Conn) {
				defer func() { _ = c.Close() }()
				var sawVersion atomic.Bool
				go func() {
					time.Sleep(120 * time.Millisecond)
					if !sawVersion.Load() { // a session connection: push + drop
						_, _ = io.WriteString(c, "@MAIN:VOL=-40.0\r\n")
						_ = c.Close()
					}
				}()
				sc := bufio.NewScanner(c)
				sc.Split(yncaSplitCRLF)
				for sc.Scan() {
					switch sc.Text() {
					case "@SYS:MODELNAME=?":
						_, _ = io.WriteString(c, "@SYS:MODELNAME=RX-V583\r\n")
					case "@SYS:VERSION=?":
						sawVersion.Store(true)
						_, _ = io.WriteString(c, "@SYS:VERSION=2.87/1.81\r\n")
					}
				}
			}(conn)
		}
	}()
	t.Cleanup(func() { _ = l.Close() })
	return l.Addr().String(), &conns
}

// TestYncaWatch_ReconnectsThenCancels exercises the supervisor loop: a
// dropped connection must emit a "reconnect" control line and re-dial, and
// a cancelled context must return nil (exit 0) rather than loop.
func TestYncaWatch_ReconnectsThenCancels(t *testing.T) {
	shrinkYNCATimeouts(t, 500*time.Millisecond, 500*time.Millisecond)
	withShrunkWatchBackoff(t)

	addr, conns := watchFakeYNCA(t)

	cmd := newYncaCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(io.Discard)
	cmd.SetArgs([]string{"watch"})
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel) // ensure the watch goroutine always stops, even on failure
	cmd.SetContext(context.WithValue(ctx, stateKey, newYncaState(t, addr)))

	done := make(chan error, 1)
	go func() { done <- cmd.Execute() }()

	// The probe (1) plus at least two session connections (≥3 total) proves
	// at least one reconnect happened.
	reconnected := waitUntil(3*time.Second, func() bool { return conns.Load() >= 3 })
	cancel()

	var err error
	select {
	case err = <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("watch did not return within 2s of cancel")
	}

	if !reconnected {
		t.Fatalf("watch never reconnected (accepted %d connections, want ≥3)", conns.Load())
	}
	if err != nil {
		t.Fatalf("watch returned %v, want nil on cancel", err)
	}
	if !bytes.Contains(out.Bytes(), []byte(`"event":"reconnect"`)) {
		t.Errorf("watch output never emitted a reconnect event:\n%s", out.String())
	}
}

// TestYncaWatch_RediscoversAfterDialFailures pins the DHCP-resilience path:
// after yncaWatchRediscoverAfter consecutive dial failures the supervisor
// must re-settle the host (yncaWatchSettle), not back off against a stale IP
// forever. yncaWatchSettle is stubbed so the test needs no SSDP infra, and
// the dial target (a refusing port) makes every Session dial fail fast.
func TestYncaWatch_RediscoversAfterDialFailures(t *testing.T) {
	shrinkYNCATimeouts(t, 200*time.Millisecond, 200*time.Millisecond)
	withShrunkWatchBackoff(t)

	prevAfter := yncaWatchRediscoverAfter
	yncaWatchRediscoverAfter = 1
	prevSettle := yncaWatchSettle
	var calls atomic.Int32
	resettled := make(chan struct{}, 1)
	yncaWatchSettle = func(_ context.Context, _ *state) error {
		// Call #1 is the initial settle; #2 is the loop re-settle we want.
		if calls.Add(1) >= 2 {
			select {
			case resettled <- struct{}{}:
			default:
			}
		}
		return nil
	}
	t.Cleanup(func() {
		yncaWatchRediscoverAfter = prevAfter
		yncaWatchSettle = prevSettle
	})

	cmd := newYncaCmd()
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	cmd.SetArgs([]string{"watch"})
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	// A port that refuses connections → every Session dial returns *DialError.
	cmd.SetContext(context.WithValue(ctx, stateKey, newYncaState(t, "127.0.0.1:1")))

	done := make(chan error, 1)
	go func() { done <- cmd.Execute() }()

	gotResettle := false
	select {
	case <-resettled:
		gotResettle = true
	case <-time.After(3 * time.Second):
	}
	cancel()

	var err error
	select {
	case err = <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("watch did not return within 2s of cancel")
	}

	if !gotResettle {
		t.Fatal("watch never re-settled the host after repeated dial failures")
	}
	if err != nil {
		t.Fatalf("watch returned %v, want nil on cancel", err)
	}
}

// withShrunkWatchBackoff shrinks the watch backoff to keep reconnect-driven
// tests fast, restoring on cleanup.
func withShrunkWatchBackoff(t *testing.T) {
	t.Helper()
	pMin, pMax := yncaWatchBackoffMin, yncaWatchBackoffMax
	yncaWatchBackoffMin = 2 * time.Millisecond
	yncaWatchBackoffMax = 5 * time.Millisecond
	t.Cleanup(func() {
		yncaWatchBackoffMin = pMin
		yncaWatchBackoffMax = pMax
	})
}

// waitUntil polls cond until it is true (returns true) or the deadline
// elapses (returns false). Unlike a t.Fatal helper, it never aborts the test
// goroutine, so the caller can cancel + join before asserting — avoiding a
// leaked goroutine that would race a later test.
func waitUntil(within time.Duration, cond func() bool) bool {
	deadline := time.Now().Add(within)
	for time.Now().Before(deadline) {
		if cond() {
			return true
		}
		time.Sleep(2 * time.Millisecond)
	}
	return false
}
