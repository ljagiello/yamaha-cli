package yxc

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// TestSubscribe_FullCycle drives the full Subscribe flow against a
// real UDP listener and an httptest endpoint. It verifies:
//
//   - X-AppName / X-AppPort headers are attached on the subscription call.
//   - X-AppPort matches the bound UDP port (i.e. the Subscriber wires
//     its socket through to the Client).
//   - A synthetic UDP packet sent to that port surfaces on the channel.
//   - Cancelling the context closes the channel.
func TestSubscribe_FullCycle(t *testing.T) {
	type captured struct {
		appName string
		appPort string
	}
	var (
		mu  sync.Mutex
		hit captured
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		hit = captured{
			appName: r.Header.Get("X-AppName"),
			appPort: r.Header.Get("X-AppPort"),
		}
		mu.Unlock()
		_, _ = w.Write([]byte(`{"response_code":0,"power":"on","volume":42,"mute":false,"input":"hdmi2"}`))
	}))
	defer srv.Close()

	c := newTestClient(t, srv)
	sub := &Subscriber{
		BackoffMin:  5 * time.Millisecond,
		BackoffMax:  20 * time.Millisecond,
		SilentAfter: 5 * time.Second,
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	ch, err := sub.Subscribe(ctx, c, []string{"main"})
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}

	// Wait for the initial subscribe control event so we know the call
	// has been issued.
	select {
	case ev := <-ch:
		if ev.Kind != "subscribe" {
			t.Fatalf("expected subscribe control event, got %+v", ev)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for subscribe event")
	}

	mu.Lock()
	gotName := hit.appName
	gotPort := hit.appPort
	mu.Unlock()
	if gotName != "MusicCast" {
		t.Errorf("X-AppName: got %q want MusicCast", gotName)
	}
	if gotPort == "" {
		t.Fatalf("X-AppPort missing")
	}
	port, err := strconv.Atoi(gotPort)
	if err != nil || port <= 0 {
		t.Fatalf("X-AppPort %q invalid: %v", gotPort, err)
	}

	// Send a synthetic UDP packet to the bound port.
	const payload = `{"main":{"volume":50}}`
	udp, err := net.Dial("udp4", "127.0.0.1:"+gotPort)
	if err != nil {
		t.Fatalf("dial udp: %v", err)
	}
	if _, err := udp.Write([]byte(payload)); err != nil {
		t.Fatalf("write udp: %v", err)
	}
	_ = udp.Close()

	// Read the event off the channel.
	select {
	case ev := <-ch:
		if ev.Kind != "" {
			t.Fatalf("expected data event, got control %+v", ev)
		}
		if string(ev.Raw) != payload {
			t.Fatalf("payload mismatch:\n got %s\nwant %s", ev.Raw, payload)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for UDP event")
	}

	// Cancel and verify the channel closes after a final shutdown event.
	cancel()
	deadline := time.After(3 * time.Second)
	gotShutdown := false
	for {
		select {
		case ev, ok := <-ch:
			if !ok {
				if !gotShutdown {
					t.Error("channel closed without shutdown control event")
				}
				return
			}
			if ev.Kind == "shutdown" {
				gotShutdown = true
			}
		case <-deadline:
			t.Fatal("timed out waiting for channel close after cancel")
		}
	}
}

// TestSubscribe_ReconnectOnSubscribeFail verifies that an initial
// subscription failure triggers backoff + reconnect events, and the
// pump recovers once the server returns OK.
func TestSubscribe_ReconnectOnSubscribeFail(t *testing.T) {
	var attempts int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		n := atomic.AddInt32(&attempts, 1)
		if n < 3 {
			http.Error(w, "boom", http.StatusInternalServerError)
			return
		}
		_, _ = w.Write([]byte(`{"response_code":0}`))
	}))
	defer srv.Close()

	c := newTestClient(t, srv)
	sub := &Subscriber{
		BackoffMin:  5 * time.Millisecond,
		BackoffMax:  10 * time.Millisecond,
		SilentAfter: 5 * time.Second,
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	ch, err := sub.Subscribe(ctx, c, []string{"main"})
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}

	// Drain control events until we see a successful subscribe.
	deadline := time.After(3 * time.Second)
	sawReconnect := false
	for {
		select {
		case ev, ok := <-ch:
			if !ok {
				t.Fatal("channel closed before subscribe succeeded")
			}
			if ev.Kind == "reconnect" {
				sawReconnect = true
			}
			if ev.Kind == "subscribe" {
				if !sawReconnect {
					t.Error("expected at least one reconnect before subscribe")
				}
				if got := atomic.LoadInt32(&attempts); got < 3 {
					t.Errorf("expected >=3 server attempts, got %d", got)
				}
				return
			}
		case <-deadline:
			t.Fatalf("timed out (attempts=%d)", atomic.LoadInt32(&attempts))
		}
	}
}

// TestSubscribe_RejectsInvalid verifies argument validation up-front.
func TestSubscribe_RejectsInvalid(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		t.Fatal("unexpected server hit")
	}))
	defer srv.Close()
	c := newTestClient(t, srv)

	sub := &Subscriber{}
	if _, err := sub.Subscribe(context.Background(), nil, []string{"main"}); err == nil {
		t.Error("expected error for nil client")
	}
	if _, err := sub.Subscribe(context.Background(), c, nil); err == nil {
		t.Error("expected error for empty zones")
	}
	if _, err := sub.Subscribe(context.Background(), c, []string{"zone3"}); err == nil {
		t.Error("expected error for invalid zone")
	}
}

// TestNextBackoff_Caps verifies the backoff helper.
func TestNextBackoff_Caps(t *testing.T) {
	cases := []struct {
		cur, maxDur, want time.Duration
	}{
		{1 * time.Second, 60 * time.Second, 2 * time.Second},
		{32 * time.Second, 60 * time.Second, 60 * time.Second},
		{60 * time.Second, 60 * time.Second, 60 * time.Second},
		{0, 60 * time.Second, 60 * time.Second}, // overflow guard
	}
	for _, tc := range cases {
		t.Run(fmt.Sprintf("%v", tc.cur), func(t *testing.T) {
			got := nextBackoff(tc.cur, tc.maxDur)
			if got != tc.want {
				t.Errorf("nextBackoff(%v,%v) = %v, want %v", tc.cur, tc.maxDur, got, tc.want)
			}
		})
	}
}

// TestSubscribeRenewIntervalDefault pins the production default for
// the renewal cadence to 8 minutes. The receiver expires push
// subscriptions at ~10 minutes; a regression that shrinks the default
// would burn unnecessary HTTP cycles, and one that grows it past 10
// would cause silent subscription loss.
func TestSubscribeRenewIntervalDefault(t *testing.T) {
	if got, want := subscribeRenewIntervalDef, 8*time.Minute; got != want {
		t.Errorf("subscribeRenewIntervalDef = %v, want %v", got, want)
	}
	// And the field-level default-resolution must produce the same
	// value when Subscriber.RenewInterval is left zero.
	var s Subscriber
	if s.RenewInterval != 0 {
		t.Errorf("zero-value Subscriber.RenewInterval = %v, want 0 (so default applies)", s.RenewInterval)
	}
}

// TestSubscribe_RenewEmitsControlEvent locks two related guarantees:
//   - Subscriber.RenewInterval is plumbed through end-to-end (the
//     v3-review finding #10 — was a hardcoded const).
//   - On a successful periodic renewal the subscriber emits a "renew"
//     control event so consumers can distinguish it from the initial
//     "subscribe" and from "reconnect" (the v3-review finding #9).
func TestSubscribe_RenewEmitsControlEvent(t *testing.T) {
	var subscribeHits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		subscribeHits.Add(1)
		_, _ = w.Write([]byte(`{"response_code":0,"power":"on"}`))
	}))
	defer srv.Close()

	c := newTestClient(t, srv)
	sub := &Subscriber{
		BackoffMin:    5 * time.Millisecond,
		BackoffMax:    20 * time.Millisecond,
		SilentAfter:   5 * time.Second,
		RenewInterval: 50 * time.Millisecond,
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	ch, err := sub.Subscribe(ctx, c, []string{"main"})
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}

	// Drain control events until we see at least one "renew". A
	// "subscribe" arrives first; "renew" should follow within a few
	// shortened-interval ticks.
	deadline := time.After(2 * time.Second)
	sawRenew := false
	for !sawRenew {
		select {
		case ev := <-ch:
			if ev.Kind == "renew" {
				sawRenew = true
			}
		case <-deadline:
			t.Fatalf("timed out waiting for renew event (subscribe hits=%d)", subscribeHits.Load())
		}
	}

	// At least 2 subscribe HTTP hits (initial bind + at least one renewal).
	if got := subscribeHits.Load(); got < 2 {
		t.Errorf("subscribe HTTP hits: got %d, want >= 2 (initial + renewal)", got)
	}
}

// TestSubscribe_AllowsLegitimateSource verifies the filter doesn't
// accidentally reject UDP packets from the registered receiver. Sending
// from 127.0.0.1 against a Subscriber whose receiver is also at
// 127.0.0.1 should surface as a normal data event.
func TestSubscribe_AllowsLegitimateSource(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"response_code":0,"power":"on"}`))
	}))
	defer srv.Close()

	c := newTestClient(t, srv)
	sub := &Subscriber{
		BackoffMin:  5 * time.Millisecond,
		BackoffMax:  20 * time.Millisecond,
		SilentAfter: 5 * time.Second,
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	ch, err := sub.Subscribe(ctx, c, []string{"main"})
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}

	// Wait for the initial subscribe event so the bound port is known.
	port := 0
	deadline := time.After(2 * time.Second)
	for port == 0 {
		select {
		case ev := <-ch:
			if ev.Kind == "subscribe" {
				port = c.currentEventPort()
			}
		case <-deadline:
			t.Fatalf("timed out waiting for subscribe event")
		}
	}

	udp, err := net.Dial("udp4", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		t.Fatalf("dial udp: %v", err)
	}
	if _, err := udp.Write([]byte(`{"main":{"volume":1}}`)); err != nil {
		t.Fatalf("write udp: %v", err)
	}
	_ = udp.Close()

	select {
	case ev := <-ch:
		if ev.Kind != "" {
			t.Fatalf("expected data event from allowed source, got %+v", ev)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("legitimate packet from allowed source was dropped")
	}
}

// TestSubscribe_DropsSpoofedSource exercises the actual drop branch of
// the source-filter (the LAN-spoofing defense the v3-review #4 fix
// introduced). We swap resolveExpectedAddrsFn to return a non-loopback
// allow-set so the supervisor's filter rejects every packet our test
// can send (which all originate from 127.0.0.1).
//
// Mutation test: removing the filter — `if len(expected) > 0 { ... }` —
// from events.go::run causes this test to fail loudly.
func TestSubscribe_DropsSpoofedSource(t *testing.T) {
	// Force the allow-set to a definitely-not-loopback address so any
	// packet our test sends from 127.0.0.1 fails the comparison.
	allowed := netip.MustParseAddr("192.0.2.1") // RFC5737 TEST-NET-1
	prev := resolveExpectedAddrsFn
	resolveExpectedAddrsFn = func(_ *Client) map[netip.Addr]struct{} {
		return map[netip.Addr]struct{}{allowed: {}}
	}
	t.Cleanup(func() { resolveExpectedAddrsFn = prev })

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"response_code":0,"power":"on"}`))
	}))
	defer srv.Close()

	c := newTestClient(t, srv)
	sub := &Subscriber{
		BackoffMin:  5 * time.Millisecond,
		BackoffMax:  20 * time.Millisecond,
		SilentAfter: 5 * time.Second,
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	ch, err := sub.Subscribe(ctx, c, []string{"main"})
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}

	// Wait for the initial subscribe control event so the bound port
	// is known.
	port := 0
	deadline := time.After(2 * time.Second)
	for port == 0 {
		select {
		case ev := <-ch:
			if ev.Kind == "subscribe" {
				port = c.currentEventPort()
			}
		case <-deadline:
			t.Fatalf("timed out waiting for subscribe event")
		}
	}

	// Send a synthetic UDP packet from 127.0.0.1. The supervisor's
	// allow-set is {192.0.2.1}, so this packet must be dropped before
	// reaching the consumer channel.
	udp, err := net.Dial("udp4", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		t.Fatalf("dial udp: %v", err)
	}
	if _, err := udp.Write([]byte(`{"main":{"volume":1}}`)); err != nil {
		t.Fatalf("write udp: %v", err)
	}
	_ = udp.Close()

	// Wait long enough that a legitimate packet would have arrived.
	// Anything appearing on ch other than control events ("subscribe",
	// "renew", "shutdown") within this window is a regression.
	timeout := time.After(500 * time.Millisecond)
	for {
		select {
		case ev := <-ch:
			if ev.Kind == "" {
				t.Fatalf("spoofed packet leaked through filter: %+v", ev)
			}
			// Drain renew/shutdown control events; they're benign here.
		case <-timeout:
			return
		}
	}
}

// TestResolveExpectedAddrs verifies the helper that builds the source-
// IP allow-set returns the literal IP for IP-form base URLs and a
// non-empty set for hostname-form URLs that resolve.
// TestSubscribe_NoRaceWithConcurrentDo runs Subscribe (which writes the
// event port) concurrently with a tight loop of Do calls (which now
// reads the port under lock via currentEventPort). Must be clean under
// `go test -race`; before the locking fix this tripped the race
// detector reliably.
func TestSubscribe_NoRaceWithConcurrentDo(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"response_code":0}`))
	}))
	t.Cleanup(srv.Close)

	c, err := New(srv.URL, WithTimeout(2*time.Second))
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	// Pre-stub the resolver so Subscribe doesn't try real DNS.
	prev := resolveExpectedAddrsFn
	resolveExpectedAddrsFn = func(*Client) map[netip.Addr]struct{} { return nil }
	t.Cleanup(func() { resolveExpectedAddrsFn = prev })

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		deadline := time.After(200 * time.Millisecond)
		for {
			select {
			case <-deadline:
				return
			default:
				_, _ = c.Do(ctx, "main/getStatus", nil)
			}
		}
	}()

	sub := &Subscriber{BackoffMin: 10 * time.Millisecond, BackoffMax: 20 * time.Millisecond}
	ch, err := sub.Subscribe(ctx, c, []string{"main"})
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}
	// Drain a few events so Subscribe gets through its initial run.
	timeout := time.After(300 * time.Millisecond)
	for done := false; !done; {
		select {
		case _, ok := <-ch:
			if !ok {
				done = true
			}
		case <-timeout:
			done = true
		}
	}
	cancel()
	wg.Wait()
}

func TestResolveExpectedAddrs(t *testing.T) {
	cases := []struct {
		name    string
		baseURL string
		// We can't predict the full IP set for arbitrary hostnames; we
		// just assert the set is non-empty and contains the expected
		// address when the input is a literal IP.
		wantContains string
		wantNonEmpty bool
	}{
		{
			name:         "literal IPv4",
			baseURL:      "http://192.168.1.1/",
			wantContains: "192.168.1.1",
			wantNonEmpty: true,
		},
		{
			name:         "literal IPv6",
			baseURL:      "http://[fe80::1]/",
			wantContains: "fe80::1",
			wantNonEmpty: true,
		},
		{
			name:         "loopback hostname",
			baseURL:      "http://localhost/",
			wantNonEmpty: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c, err := New(tc.baseURL)
			if err != nil {
				t.Fatalf("New: %v", err)
			}
			got := resolveExpectedAddrs(c)
			if tc.wantNonEmpty && len(got) == 0 {
				t.Fatalf("expected non-empty addr set, got empty")
			}
			if tc.wantContains != "" {
				want, err := netip.ParseAddr(tc.wantContains)
				if err != nil {
					t.Fatalf("parse want: %v", err)
				}
				if _, ok := got[want.Unmap()]; !ok {
					t.Errorf("expected %s in set, got %v", tc.wantContains, got)
				}
			}
		})
	}
}
