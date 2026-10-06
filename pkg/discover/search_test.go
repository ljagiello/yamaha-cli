package discover

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strings"
	"sync"
	"testing"
	"time"
)

// searchTestWait is the wait handed to searchIface. Its resend interval,
// (wait - MX) / searchSends, comes to 200ms.
const searchTestWait = 1600 * time.Millisecond

// loopbackBind binds the searching socket to loopback, where a test
// responder stands in for the multicast group.
var loopbackBind = bindAddr{ip: net.IPv4(127, 0, 0, 1)}

// mSearchResponder records every M-SEARCH it receives and hands each
// one, numbered from 1, to respond.
type mSearchResponder struct {
	addr    *net.UDPAddr
	mu      sync.Mutex
	reqs    []string
	arrived []time.Time
}

// startMSearchResponder starts a responder (see startUDPResponder) and
// points ssdpGroupAddr at it for the test.
func startMSearchResponder(t *testing.T, respond func(n int, req string, from *net.UDPAddr)) *mSearchResponder {
	t.Helper()
	r := startUDPResponder(t, respond)
	prev := ssdpGroupAddr
	ssdpGroupAddr = r.addr
	t.Cleanup(func() { ssdpGroupAddr = prev })
	return r
}

// startUDPResponder listens on 127.0.0.1 and calls respond for each
// datagram it receives. Cleanup waits for the read loop so respond never
// runs after the test ends.
func startUDPResponder(t *testing.T, respond func(n int, req string, from *net.UDPAddr)) *mSearchResponder {
	t.Helper()
	conn, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatalf("listen udp: %v", err)
	}
	r := &mSearchResponder{addr: conn.LocalAddr().(*net.UDPAddr)}
	done := make(chan struct{})
	go func() {
		defer close(done)
		buf := make([]byte, 2048)
		for {
			n, from, err := conn.ReadFromUDP(buf)
			if err != nil {
				return
			}
			r.mu.Lock()
			r.reqs = append(r.reqs, string(buf[:n]))
			r.arrived = append(r.arrived, time.Now())
			count := len(r.reqs)
			r.mu.Unlock()
			if respond != nil {
				respond(count, string(buf[:n]), from)
			}
		}
	}()
	t.Cleanup(func() {
		_ = conn.Close()
		<-done
	})
	return r
}

func (r *mSearchResponder) requests() ([]string, []time.Time) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.reqs...), append([]time.Time(nil), r.arrived...)
}

// sendRaw sends payload to dst from a fresh loopback socket.
func sendRaw(t *testing.T, dst *net.UDPAddr, payload string) {
	t.Helper()
	conn, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Errorf("listen: %v", err)
		return
	}
	defer conn.Close()
	if _, err := conn.WriteToUDP([]byte(payload), dst); err != nil {
		t.Errorf("send to %v: %v", dst, err)
	}
}

// A single lost M-SEARCH or reply used to mean "no receivers found": the
// search must resend, and a reply to a later send must still count.
func TestSearchIface_ResendsUntilAnswered(t *testing.T) {
	const loc = "http://127.0.0.1:49154/MediaRenderer/desc.xml"
	// Answer only the third M-SEARCH, as if the first two were lost.
	r := startMSearchResponder(t, func(n int, _ string, from *net.UDPAddr) {
		if n == 3 {
			replySSDP(t, net.IPv4(127, 0, 0, 1), from, loc)
		}
	})

	locs, err := searchIface(context.Background(), loopbackBind, mediaRendererST, searchTestWait)
	if err != nil {
		t.Fatalf("searchIface: %v", err)
	}
	if len(locs) != 1 || locs[0] != loc {
		t.Fatalf("locations: got %v want [%s]", locs, loc)
	}
	reqs, _ := r.requests()
	if len(reqs) != searchSends {
		t.Fatalf("M-SEARCH count: got %d want %d", len(reqs), searchSends)
	}
	for i, req := range reqs {
		for _, want := range []string{
			"M-SEARCH * HTTP/1.1\r\n",
			"HOST: " + ssdpGroupAddr.String() + "\r\n",
			"MAN: \"ssdp:discover\"\r\n",
			fmt.Sprintf("MX: %d\r\n", searchMX),
			"ST: " + mediaRendererST + "\r\n",
		} {
			if !strings.Contains(req, want) {
				t.Errorf("M-SEARCH %d missing %q:\n%s", i+1, want, req)
			}
		}
	}
}

// Losses on Wi-Fi come in bursts, so the resends are spread over the
// window rather than sent back to back. Only lower bounds are asserted:
// scheduling can delay a send but never make it early.
func TestSearchIface_SpreadsResends(t *testing.T) {
	r := startMSearchResponder(t, nil)
	if _, err := searchIface(context.Background(), loopbackBind, mediaRendererST, searchTestWait); err != nil {
		t.Fatalf("searchIface: %v", err)
	}
	_, arrived := r.requests()
	if len(arrived) != searchSends {
		t.Fatalf("M-SEARCH count: got %d want %d", len(arrived), searchSends)
	}
	// (1600ms wait - 1s MX) / 3 sends, spelled out rather than taken from
	// resendInterval so a regression there can't hide.
	const interval = 200 * time.Millisecond
	// Loopback delivery jitter can only shrink a gap by a hair.
	const slack = 20 * time.Millisecond
	for i := 1; i < len(arrived); i++ {
		if gap := arrived[i].Sub(arrived[i-1]); gap < interval-slack {
			t.Errorf("gap before send %d: %v, want >= %v", i+1, gap, interval)
		}
	}
}

// Every send must leave the responders MX seconds to answer before the
// wait ends, or replies to the last send arrive after the read deadline.
func TestResendInterval_LastSendLeavesMXBeforeDeadline(t *testing.T) {
	for _, wait := range []time.Duration{time.Second, 1600 * time.Millisecond, 3 * time.Second, 10 * time.Second} {
		interval := resendInterval(wait)
		if interval < 0 {
			t.Errorf("wait %v: negative interval %v", wait, interval)
		}
		lastSend := time.Duration(searchSends-1) * interval
		if lastSend+searchMX*time.Second > wait {
			t.Errorf("wait %v: last send at %v + MX %ds overruns the wait", wait, lastSend, searchMX)
		}
	}
}

func TestSearchIface_DedupsAndSkipsUnusableReplies(t *testing.T) {
	const loc = "http://127.0.0.1:49154/MediaRenderer/desc.xml"
	startMSearchResponder(t, func(n int, _ string, from *net.UDPAddr) {
		if n != 1 {
			return
		}
		sendRaw(t, from, "not an HTTP response")
		sendRaw(t, from, "HTTP/1.1 200 OK\r\nST: "+mediaRendererST+"\r\n\r\n") // no Location
		replySSDP(t, net.IPv4(127, 0, 0, 1), from, loc)
		replySSDP(t, net.IPv4(127, 0, 0, 1), from, loc)
	})

	locs, err := searchIface(context.Background(), loopbackBind, mediaRendererST, searchTestWait)
	if err != nil {
		t.Fatalf("searchIface: %v", err)
	}
	if len(locs) != 1 || locs[0] != loc {
		t.Fatalf("locations: got %v want [%s]", locs, loc)
	}
}

func TestSearchIface_NoRepliesReturnsEmptyAfterWait(t *testing.T) {
	startMSearchResponder(t, nil)
	start := time.Now()
	locs, err := searchIface(context.Background(), loopbackBind, mediaRendererST, time.Second)
	if err != nil {
		t.Fatalf("searchIface: %v", err)
	}
	if len(locs) != 0 {
		t.Fatalf("locations: got %v want none", locs)
	}
	if elapsed := time.Since(start); elapsed < time.Second {
		t.Errorf("returned after %v, before the 1s wait elapsed", elapsed)
	}
}

// go-ssdp blocked for the whole wait whatever the context said; Ctrl-C
// during a scan must now return promptly.
func TestSearchIface_ReturnsPromptlyOnCancel(t *testing.T) {
	startMSearchResponder(t, nil)
	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(100*time.Millisecond, cancel)

	start := time.Now()
	_, err := searchIface(ctx, loopbackBind, mediaRendererST, 10*time.Second)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err: got %v want context.Canceled", err)
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Errorf("returned %v after cancel at 100ms; want prompt return", elapsed)
	}
}

func TestSearchIface_SendFailureIsAnError(t *testing.T) {
	prev := ssdpGroupAddr
	t.Cleanup(func() { ssdpGroupAddr = prev })
	// The search socket is udp4, so Go refuses an IPv6 destination on
	// every platform and the very first send fails.
	ssdpGroupAddr = &net.UDPAddr{IP: net.IPv6loopback, Port: 1900}

	if _, err := searchIface(context.Background(), loopbackBind, mediaRendererST, time.Second); err == nil {
		t.Fatal("expected an error when no M-SEARCH could be sent")
	}
}

// --debug must show what the search did: each send, and each reply with
// its source and Location, so "nothing found" can be told apart from
// "found but rejected".
func TestSearchIface_TracesSendsAndReplies(t *testing.T) {
	const loc = "http://127.0.0.1:49154/MediaRenderer/desc.xml"
	startMSearchResponder(t, func(n int, _ string, from *net.UDPAddr) {
		if n == 1 {
			replySSDP(t, net.IPv4(127, 0, 0, 1), from, loc)
		}
	})
	var mu sync.Mutex
	var lines []string
	ctx := WithTrace(context.Background(), func(format string, args ...any) {
		mu.Lock()
		defer mu.Unlock()
		lines = append(lines, fmt.Sprintf(format, args...))
	})

	if _, err := searchIface(ctx, loopbackBind, mediaRendererST, searchTestWait); err != nil {
		t.Fatalf("searchIface: %v", err)
	}
	mu.Lock()
	out := strings.Join(lines, "\n")
	mu.Unlock()
	for _, want := range []string{
		fmt.Sprintf("M-SEARCH 1/%d", searchSends),
		fmt.Sprintf("M-SEARCH %d/%d", searchSends, searchSends),
		"127.0.0.1",
		"location=" + loc,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("trace missing %q:\n%s", want, out)
		}
	}
}

func TestContextTrace(t *testing.T) {
	if got := ContextTrace(context.Background()); got != nil {
		t.Error("ContextTrace on a bare context should be nil")
	}
	if got := ContextTrace(WithTrace(context.Background(), nil)); got != nil {
		t.Error("WithTrace(nil) should leave tracing off")
	}
	called := false
	ctx := WithTrace(context.Background(), func(string, ...any) { called = true })
	ContextTrace(ctx)("x")
	if !called {
		t.Error("ContextTrace did not return the installed hook")
	}
	tracef(context.Background(), "no hook: must not panic")
}
