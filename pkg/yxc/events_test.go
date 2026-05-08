package yxc

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
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
