package ynca

import (
	"bufio"
	"context"
	"errors"
	"io"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// newFakeYNCA starts a TCP listener that, for each accepted connection,
// reads CRLF-framed lines and writes back whatever handler returns. If
// handler returns an empty string, no reply is sent for that line.
//
// The listener is closed via t.Cleanup. The returned address is
// suitable to pass to New() (host:port form).
func newFakeYNCA(t *testing.T, handler func(line string) string) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}

	var wg sync.WaitGroup
	go func() {
		for {
			conn, err := l.Accept()
			if err != nil {
				return
			}
			wg.Add(1)
			go func(c net.Conn) {
				defer wg.Done()
				defer c.Close()
				scanner := bufio.NewScanner(c)
				scanner.Buffer(make([]byte, 0, 4096), 64*1024)
				scanner.Split(splitCRLF)
				for scanner.Scan() {
					line := scanner.Text()
					reply := handler(line)
					if reply == "" {
						continue
					}
					if _, err := io.WriteString(c, reply+"\r\n"); err != nil {
						return
					}
				}
			}(conn)
		}
	}()
	t.Cleanup(func() {
		_ = l.Close()
		wg.Wait()
	})
	return l.Addr().String()
}

func TestSend_RoundTrip(t *testing.T) {
	t.Parallel()
	var got string
	var mu sync.Mutex
	addr := newFakeYNCA(t, func(line string) string {
		mu.Lock()
		got = line
		mu.Unlock()
		if line == "@MAIN:PWR=?" {
			return "@MAIN:PWR=On"
		}
		return "@UNDEFINED"
	})

	c, err := New(addr)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer c.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	reply, err := c.Send(ctx, "@MAIN:PWR=?")
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	if reply != "@MAIN:PWR=On" {
		t.Errorf("reply = %q, want %q", reply, "@MAIN:PWR=On")
	}
	mu.Lock()
	if got != "@MAIN:PWR=?" {
		t.Errorf("server received %q, want %q", got, "@MAIN:PWR=?")
	}
	mu.Unlock()
}

// TestSendMulti_DrainsToFence verifies a fan-out GET collects every report
// line up to (and excluding) the @SYS:VERSION fence echo.
func TestSendMulti_DrainsToFence(t *testing.T) {
	t.Parallel()
	var sawSentinel atomic.Bool
	addr := newFakeYNCA(t, func(line string) string {
		switch line {
		case "@MAIN:BASIC=?":
			// Fan-out: several report lines for one GET.
			return "@MAIN:PWR=On\r\n@MAIN:INP=HDMI2\r\n@MAIN:VOL=-30.0"
		case "@SYS:VERSION=?":
			sawSentinel.Store(true)
			return "@SYS:VERSION=1.00/2.00"
		}
		return "@UNDEFINED"
	})

	c, err := New(addr)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer c.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	lines, err := c.SendMulti(ctx, "@MAIN:BASIC=?")
	if err != nil {
		t.Fatalf("SendMulti: %v", err)
	}
	want := []string{"@MAIN:PWR=On", "@MAIN:INP=HDMI2", "@MAIN:VOL=-30.0"}
	if len(lines) != len(want) {
		t.Fatalf("got %d lines %v, want %d %v", len(lines), lines, len(want), want)
	}
	for i := range want {
		if lines[i] != want[i] {
			t.Errorf("line %d = %q, want %q", i, lines[i], want[i])
		}
	}
	if !sawSentinel.Load() {
		t.Error("server never received the @SYS:VERSION=? fence")
	}
}

// TestSendMulti_EmptyBeforeFence covers a command that produces no report
// lines (just the fence echo) — e.g. a PUT routed through SendMulti.
func TestSendMulti_EmptyBeforeFence(t *testing.T) {
	t.Parallel()
	addr := newFakeYNCA(t, func(line string) string {
		if line == "@SYS:VERSION=?" {
			return "@SYS:VERSION=1.00/2.00"
		}
		return "" // no reply to the command itself
	})
	c, err := New(addr)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer c.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	lines, err := c.SendMulti(ctx, "@MAIN:PWR=On")
	if err != nil {
		t.Fatalf("SendMulti: %v", err)
	}
	if len(lines) != 0 {
		t.Errorf("got %v, want no report lines", lines)
	}
}

// TestSendMulti_NoFenceReturnsNoReply: if the receiver answers the command
// but never echoes the @SYS:VERSION=? fence, the drain must return
// ErrNoReply promptly (bounded by the timeout) rather than hang — a hang
// here would freeze `ynca status` forever.
func TestSendMulti_NoFenceReturnsNoReply(t *testing.T) {
	t.Parallel()
	addr := newFakeYNCA(t, func(line string) string {
		switch line {
		case "@MAIN:BASIC=?":
			return "@MAIN:PWR=On"
		case "@SYS:VERSION=?":
			return "" // never echo the fence
		}
		return "@UNDEFINED"
	})
	c, err := New(addr, WithTimeout(300*time.Millisecond))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer c.Close()

	start := time.Now()
	_, err = c.SendMulti(context.Background(), "@MAIN:BASIC=?")
	elapsed := time.Since(start)
	if !errors.Is(err, ErrNoReply) {
		t.Fatalf("SendMulti err = %v, want ErrNoReply", err)
	}
	if elapsed > 2*time.Second {
		t.Errorf("SendMulti took %v; expected to return near the 300ms timeout, not hang", elapsed)
	}
}

func TestSend_AddsAtAndCRLF(t *testing.T) {
	t.Parallel()
	// Use a raw listener so we can inspect exact bytes.
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer l.Close()

	gotCh := make(chan []byte, 1)
	go func() {
		conn, err := l.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		buf := make([]byte, 64)
		n, _ := conn.Read(buf)
		gotCh <- append([]byte(nil), buf[:n]...)
		_, _ = io.WriteString(conn, "@MAIN:PWR=On\r\n")
	}()

	c, err := New(l.Addr().String())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer c.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	if _, err := c.Send(ctx, "MAIN:PWR=?"); err != nil {
		t.Fatalf("Send: %v", err)
	}

	select {
	case got := <-gotCh:
		if string(got) != "@MAIN:PWR=?\r\n" {
			t.Errorf("wire bytes = %q, want %q", got, "@MAIN:PWR=?\r\n")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("server never received bytes")
	}
}

func TestProbe_Success(t *testing.T) {
	t.Parallel()
	addr := newFakeYNCA(t, func(line string) string {
		if line == "@SYS:VERSION=?" {
			return "@SYS:VERSION=2.87/1.81"
		}
		return "@UNDEFINED"
	})

	c, err := New(addr)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer c.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	v, err := c.Probe(ctx)
	if err != nil {
		t.Fatalf("Probe: %v", err)
	}
	if v != "2.87/1.81" {
		t.Errorf("version = %q, want %q", v, "2.87/1.81")
	}
}

func TestProbe_NoReply(t *testing.T) {
	t.Parallel()
	addr := newFakeYNCA(t, func(line string) string {
		// Never reply.
		return ""
	})

	c, err := New(addr, WithTimeout(80*time.Millisecond))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer c.Close()

	_, err = c.Probe(context.Background())
	if !errors.Is(err, ErrNoReply) {
		t.Fatalf("err = %v, want ErrNoReply", err)
	}
}

func TestSend_CtxCancellation(t *testing.T) {
	t.Parallel()
	addr := newFakeYNCA(t, func(line string) string {
		// Sleep before replying so ctx has time to cancel.
		time.Sleep(200 * time.Millisecond)
		return "@MAIN:PWR=On"
	})

	c, err := New(addr, WithTimeout(2*time.Second))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer c.Close()

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()

	start := time.Now()
	_, err = c.Send(ctx, "@MAIN:PWR=?")
	elapsed := time.Since(start)

	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	if elapsed > 180*time.Millisecond {
		t.Errorf("Send took %v, expected fast cancel", elapsed)
	}
}

// TestSend_RedialsAfterCancel verifies that a ctx-cancelled Send leaves
// the connection closed so the next Send dials a fresh socket. Before
// the fix, the poisoned conn was reused and the next Send either timed
// out or returned the previous request's late reply.
func TestSend_RedialsAfterCancel(t *testing.T) {
	t.Parallel()
	var accepted int64
	var first int64 // 1 == first conn (sleep), >=2 == subsequent (reply fast)
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	var wg sync.WaitGroup
	go func() {
		for {
			conn, err := l.Accept()
			if err != nil {
				return
			}
			n := atomic.AddInt64(&accepted, 1)
			if n == 1 {
				atomic.StoreInt64(&first, 1)
			}
			wg.Add(1)
			go func(c net.Conn, attempt int64) {
				defer wg.Done()
				defer c.Close()
				scanner := bufio.NewScanner(c)
				scanner.Buffer(make([]byte, 0, 4096), 64*1024)
				scanner.Split(splitCRLF)
				for scanner.Scan() {
					if attempt == 1 {
						// Sleep past the ctx-cancel so the client sees
						// context.Canceled. Reply if we ever wake up so
						// any erroneous reuse on the next Send picks up
						// this late line.
						time.Sleep(300 * time.Millisecond)
						_, _ = io.WriteString(c, "@MAIN:PWR=Off\r\n")
						return
					}
					_, _ = io.WriteString(c, "@MAIN:PWR=On\r\n")
				}
			}(conn, n)
		}
	}()
	t.Cleanup(func() { _ = l.Close(); wg.Wait() })

	c, err := New(l.Addr().String(), WithTimeout(2*time.Second))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer c.Close()

	ctx1, cancel1 := context.WithCancel(context.Background())
	go func() {
		time.Sleep(50 * time.Millisecond)
		cancel1()
	}()
	if _, err := c.Send(ctx1, "@MAIN:PWR=?"); !errors.Is(err, context.Canceled) {
		t.Fatalf("first Send err = %v, want context.Canceled", err)
	}

	// Give the watchdog goroutine a beat to finish so closeLocked has
	// definitively run.
	time.Sleep(10 * time.Millisecond)

	ctx2, cancel2 := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel2()
	reply, err := c.Send(ctx2, "@MAIN:PWR=?")
	if err != nil {
		t.Fatalf("second Send: %v", err)
	}
	if reply != "@MAIN:PWR=On" {
		t.Errorf("reply = %q, want @MAIN:PWR=On (second conn); got the late first-conn reply?", reply)
	}
	if got := atomic.LoadInt64(&accepted); got != 2 {
		t.Errorf("server accepted %d connections, want 2 (fresh dial after cancel)", got)
	}
}

func TestSend_Concurrent(t *testing.T) {
	t.Parallel()
	// Each request carries a unique tag; the server echoes the value.
	// e.g. `@MAIN:INP=tag-0042` -> reply `@MAIN:INP=tag-0042`.
	var seen int64
	addr := newFakeYNCA(t, func(line string) string {
		atomic.AddInt64(&seen, 1)
		return line
	})

	c, err := New(addr, WithTimeout(2*time.Second))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer c.Close()

	const N = 10
	var wg sync.WaitGroup
	errs := make(chan error, N)
	for i := 0; i < N; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			tag := tagFor(i)
			req := "@MAIN:INP=" + tag
			reply, err := c.Send(ctx, req)
			if err != nil {
				errs <- err
				return
			}
			if reply != req {
				errs <- &mismatchErr{want: req, got: reply}
			}
		}(i)
	}
	wg.Wait()
	close(errs)
	for e := range errs {
		t.Errorf("concurrent send: %v", e)
	}
	if got := atomic.LoadInt64(&seen); got != N {
		t.Errorf("server saw %d requests, want %d", got, N)
	}
}

func TestSend_Undefined(t *testing.T) {
	t.Parallel()
	addr := newFakeYNCA(t, func(line string) string {
		return "@UNDEFINED"
	})
	c, err := New(addr)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer c.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	_, err = c.Send(ctx, "@MAIN:NOPE=?")
	var und *ErrUndefinedCommand
	if !errors.As(err, &und) {
		t.Fatalf("err = %v, want *ErrUndefinedCommand", err)
	}
}

func TestSend_Restricted(t *testing.T) {
	t.Parallel()
	addr := newFakeYNCA(t, func(line string) string {
		return "@RESTRICTED"
	})
	c, err := New(addr)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer c.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	_, err = c.Send(ctx, "@ZONE2:VOL=-50.0")
	var rerr *ErrRestricted
	if !errors.As(err, &rerr) {
		t.Fatalf("err = %v, want *ErrRestricted", err)
	}
}

func TestParseLine(t *testing.T) {
	t.Parallel()
	cases := []struct {
		in           string
		sub, fn, val string
		wantErr      bool
	}{
		{"@MAIN:PWR=On", "MAIN", "PWR", "On", false},
		{"@SYS:VERSION=2.87/1.81", "SYS", "VERSION", "2.87/1.81", false},
		{"@MAIN:INP=", "MAIN", "INP", "", false},
		{"MAIN:PWR=On", "", "", "", true},
		{"@MAIN=On", "", "", "", true},
		{"@:FUNC=v", "", "", "", true},
		{"@MAIN:=v", "", "", "", true},
	}
	for _, tc := range cases {
		s, f, v, err := parseLine(tc.in)
		if tc.wantErr {
			if err == nil {
				t.Errorf("parseLine(%q) err=nil, want error", tc.in)
			}
			continue
		}
		if err != nil {
			t.Errorf("parseLine(%q) err=%v", tc.in, err)
			continue
		}
		if s != tc.sub || f != tc.fn || v != tc.val {
			t.Errorf("parseLine(%q) = (%q,%q,%q), want (%q,%q,%q)",
				tc.in, s, f, v, tc.sub, tc.fn, tc.val)
		}
	}
}

func TestNew_BadHost(t *testing.T) {
	t.Parallel()
	if _, err := New(""); err == nil {
		t.Error("New(\"\") err = nil, want error")
	}
}

func TestWithPort(t *testing.T) {
	t.Parallel()
	c, err := New("192.0.2.1", WithPort(60000))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if !strings.HasSuffix(c.addr, ":60000") {
		t.Errorf("addr = %q, want :60000 suffix", c.addr)
	}
}

// --- helpers ---

type mismatchErr struct{ want, got string }

func (e *mismatchErr) Error() string { return "want=" + e.want + " got=" + e.got }

func tagFor(i int) string {
	const hex = "0123456789abcdef"
	return string([]byte{hex[(i>>4)&0xf], hex[i&0xf]})
}
