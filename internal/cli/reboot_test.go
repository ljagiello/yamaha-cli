package cli

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ljagiello/yamaha-cli/internal/config"
	"github.com/ljagiello/yamaha-cli/pkg/yxc"
)

func newRebootTestState(t *testing.T, srv *httptest.Server) *state {
	t.Helper()
	c, err := yxc.New(srv.URL)
	if err != nil {
		t.Fatalf("yxc.New: %v", err)
	}
	return &state{
		cfg:    &config.Config{Devices: map[string]config.Device{}},
		alias:  "test",
		device: config.Device{Host: srv.URL, DefaultZone: "main"},
		zone:   "main",
		client: c,
	}
}

// TestRunReboot_HappyPath asserts that `yamaha reboot --yes` issues exactly
// one requestSystemReboot request when the receiver responds with a clean
// {"response_code":0}.
func TestRunReboot_HappyPath(t *testing.T) {
	resetFeatureLoader(t)
	redirectCacheDir(t)

	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/system/requestSystemReboot") {
			atomic.AddInt32(&hits, 1)
			_, _ = w.Write([]byte(`{"response_code":0}`))
			return
		}
		http.NotFound(w, r)
	}))
	defer srv.Close()

	s := newRebootTestState(t, srv)

	cmd := newRebootCmd()
	cmd.SetContext(context.Background())
	setStateOnCmd(cmd, s)
	cmd.SetOut(&strings.Builder{})
	cmd.SetErr(&strings.Builder{})
	cmd.SetArgs([]string{"--yes"})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if got := atomic.LoadInt32(&hits); got != 1 {
		t.Errorf("requestSystemReboot hits: got %d, want 1", got)
	}
}

// TestRunReboot_RequiresYes asserts that without --yes the command returns
// a *usageError (exit 2) and never fires the request.
func TestRunReboot_RequiresYes(t *testing.T) {
	resetFeatureLoader(t)
	redirectCacheDir(t)

	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/system/requestSystemReboot") {
			atomic.AddInt32(&hits, 1)
			_, _ = w.Write([]byte(`{"response_code":0}`))
			return
		}
		http.NotFound(w, r)
	}))
	defer srv.Close()

	s := newRebootTestState(t, srv)

	cmd := newRebootCmd()
	cmd.SetContext(context.Background())
	setStateOnCmd(cmd, s)
	cmd.SetOut(&strings.Builder{})
	cmd.SetErr(&strings.Builder{})
	cmd.SetArgs([]string{})

	err := cmd.Execute()
	if err == nil {
		t.Fatal("expected usageError, got nil")
	}
	var uerr *usageError
	if !errors.As(err, &uerr) {
		t.Fatalf("expected *usageError, got %v (%T)", err, err)
	}
	if got := atomic.LoadInt32(&hits); got != 0 {
		t.Errorf("requestSystemReboot hits: got %d, want 0", got)
	}
}

// TestRunReboot_TransportErrorAfterAckTolerated covers the case where the
// receiver acknowledges and then drops the connection mid-reboot. The CLI
// must treat the post-ack transport error as success because the reboot
// already took effect.
//
// We simulate it by running an raw TCP listener that accepts a connection,
// reads enough to confirm the request hit, then closes the socket without
// writing a valid HTTP response. The yxc client surfaces this as a
// transportError, which reboot.go must swallow.
func TestRunReboot_TransportErrorAfterAckTolerated(t *testing.T) {
	resetFeatureLoader(t)
	redirectCacheDir(t)

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()

	done := make(chan struct{})
	go func() {
		defer close(done)
		conn, aerr := ln.Accept()
		if aerr != nil {
			return
		}
		// Drain a little of the request so the client knows the server
		// engaged, then drop the connection without a valid response —
		// emulating "ack then reboot drops TCP".
		buf := make([]byte, 1024)
		_, _ = conn.Read(buf)
		_ = conn.Close()
	}()

	// Short timeout: the server never writes a response, so the request
	// will hang until either the client times out or the connection
	// closes. Either path surfaces a transportError, which is what the
	// reboot command must tolerate.
	c, err := yxc.New("http://"+ln.Addr().String(), yxc.WithTimeout(500*time.Millisecond))
	if err != nil {
		t.Fatalf("yxc.New: %v", err)
	}
	s := &state{
		cfg:    &config.Config{Devices: map[string]config.Device{}},
		alias:  "test",
		device: config.Device{Host: "http://" + ln.Addr().String(), DefaultZone: "main"},
		zone:   "main",
		client: c,
	}

	cmd := newRebootCmd()
	cmd.SetContext(context.Background())
	setStateOnCmd(cmd, s)
	cmd.SetOut(&strings.Builder{})
	cmd.SetErr(&strings.Builder{})
	cmd.SetArgs([]string{"--yes"})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute: expected nil (transport error after ack should be tolerated), got %v", err)
	}
	<-done
}
