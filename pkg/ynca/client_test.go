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
