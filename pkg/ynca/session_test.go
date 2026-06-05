package ynca

import (
	"bufio"
	"context"
	"io"
	"net"
	"sync"
	"testing"
	"time"
)

// pushServer accepts one connection, drains anything the client writes (the
// wake/keep-alive pings) so the socket never blocks, and writes each line in
// pushes with a small gap to simulate the receiver's unsolicited reports.
// It holds the connection open until the client closes it.
func pushServer(t *testing.T, pushes []string) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	var wg sync.WaitGroup
	go func() {
		for {
			conn, aerr := l.Accept()
			if aerr != nil {
				return
			}
			wg.Add(1)
			go func(c net.Conn) {
				defer wg.Done()
				defer c.Close()
				// Drain client writes in the background.
				go func() {
					sc := bufio.NewScanner(c)
					sc.Split(splitCRLF)
					for sc.Scan() { //nolint:revive // intentional drain
					}
				}()
				for _, line := range pushes {
					if _, werr := io.WriteString(c, line+"\r\n"); werr != nil {
						return
					}
					time.Sleep(5 * time.Millisecond)
				}
				// Keep the connection open until the client hangs up.
				buf := make([]byte, 1)
				_, _ = c.Read(buf)
			}(conn)
		}
	}()
	t.Cleanup(func() { _ = l.Close(); wg.Wait() })
	return l.Addr().String()
}

func TestSessionStreamsReportsAndSuppressesKeepAliveEcho(t *testing.T) {
	addr := pushServer(t, []string{
		"@MAIN:VOL=-40.0",
		"@SYS:MODELNAME=RX-V583", // keep-alive echo — must be suppressed
		"@MAIN:INP=HDMI1",
	})

	sess, err := NewSession(addr, WithSessionWake(), WithKeepAlive(0))
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var mu sync.Mutex
	var got []Report
	done := make(chan struct{})
	go func() {
		_ = sess.Run(ctx, func(r Report) {
			mu.Lock()
			got = append(got, r)
			n := len(got)
			mu.Unlock()
			if n == 2 { // we expect exactly the two MAIN reports
				cancel()
			}
		})
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Session.Run did not return after cancel")
	}

	mu.Lock()
	defer mu.Unlock()
	if len(got) != 2 {
		t.Fatalf("got %d reports, want 2 (MODELNAME keep-alive must be suppressed): %+v", len(got), got)
	}
	if got[0].Function != "VOL" || got[1].Function != "INP" {
		t.Errorf("reports = %q,%q; want VOL,INP", got[0].Function, got[1].Function)
	}
	for _, r := range got {
		if r.Subunit == SubunitSystem && r.Function == FuncModelName {
			t.Error("a SYS:MODELNAME report leaked through (keep-alive echo not suppressed)")
		}
	}
}

func TestSessionCancelStopsRun(t *testing.T) {
	addr := pushServer(t, nil) // no pushes; just holds the connection open

	sess, err := NewSession(addr, WithKeepAlive(0))
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())

	errc := make(chan error, 1)
	go func() { errc <- sess.Run(ctx, func(Report) {}) }()

	// Let the reader settle, then cancel.
	time.Sleep(20 * time.Millisecond)
	cancel()

	select {
	case err := <-errc:
		if err != context.Canceled {
			t.Errorf("Run returned %v, want context.Canceled", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not return within 2s of cancel")
	}
}

func TestSessionKeepAlivePings(t *testing.T) {
	// A short keep-alive must produce a @SYS:MODELNAME=? write we can observe
	// server-side, proving the heartbeat fires.
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	gotPing := make(chan struct{}, 1)
	var wg sync.WaitGroup
	go func() {
		conn, aerr := l.Accept()
		if aerr != nil {
			return
		}
		wg.Add(1)
		defer wg.Done()
		defer conn.Close()
		sc := bufio.NewScanner(conn)
		sc.Split(splitCRLF)
		for sc.Scan() {
			if sc.Text() == "@SYS:MODELNAME=?" {
				select {
				case gotPing <- struct{}{}:
				default:
				}
			}
		}
	}()
	t.Cleanup(func() { _ = l.Close(); wg.Wait() })

	sess, err := NewSession(l.Addr().String(), WithKeepAlive(20*time.Millisecond))
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	go func() { _ = sess.Run(t.Context(), func(Report) {}) }()

	select {
	case <-gotPing:
	case <-time.After(2 * time.Second):
		t.Fatal("keep-alive ping never arrived")
	}
}
