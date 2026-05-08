package cli

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/ljagiello/yamaha-cli/internal/config"
	"github.com/ljagiello/yamaha-cli/pkg/yxc"
)

func newSleepTestState(t *testing.T, srv *httptest.Server) *state {
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

// TestRunSleep_HappyPath asserts that `yamaha sleep 60` issues exactly one
// setSleep request with sleep=60.
func TestRunSleep_HappyPath(t *testing.T) {
	resetFeatureLoader(t)
	redirectCacheDir(t)

	var (
		setHits  int32
		gotQuery string
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/main/setSleep"):
			atomic.AddInt32(&setHits, 1)
			gotQuery = r.URL.RawQuery
			_, _ = w.Write([]byte(`{"response_code":0}`))
		default:
			t.Errorf("unexpected request %s", r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	s := newSleepTestState(t, srv)

	cmd := newSleepCmd()
	cmd.SetContext(context.Background())
	setStateOnCmd(cmd, s)
	cmd.SetOut(&strings.Builder{})
	cmd.SetErr(&strings.Builder{})
	cmd.SetArgs([]string{"60"})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if got := atomic.LoadInt32(&setHits); got != 1 {
		t.Fatalf("setSleep hits: got %d, want 1", got)
	}
	if gotQuery != "sleep=60" {
		t.Errorf("setSleep query: got %q, want %q", gotQuery, "sleep=60")
	}
}

// TestRunSleep_OffMapsToZero asserts that the literal "off" maps to sleep=0.
func TestRunSleep_OffMapsToZero(t *testing.T) {
	resetFeatureLoader(t)
	redirectCacheDir(t)

	var (
		setHits  int32
		gotQuery string
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/main/setSleep") {
			atomic.AddInt32(&setHits, 1)
			gotQuery = r.URL.RawQuery
			_, _ = w.Write([]byte(`{"response_code":0}`))
			return
		}
		http.NotFound(w, r)
	}))
	defer srv.Close()

	s := newSleepTestState(t, srv)

	cmd := newSleepCmd()
	cmd.SetContext(context.Background())
	setStateOnCmd(cmd, s)
	cmd.SetOut(&strings.Builder{})
	cmd.SetErr(&strings.Builder{})
	cmd.SetArgs([]string{"off"})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if got := atomic.LoadInt32(&setHits); got != 1 {
		t.Fatalf("setSleep hits: got %d, want 1", got)
	}
	if gotQuery != "sleep=0" {
		t.Errorf("setSleep query: got %q, want %q", gotQuery, "sleep=0")
	}
}

// TestRunSleep_DisallowedValueRejected covers the client-side validator.
// 45 is not in the {0,30,60,90,120} set so we must not fire setSleep.
func TestRunSleep_DisallowedValueRejected(t *testing.T) {
	resetFeatureLoader(t)
	redirectCacheDir(t)

	var setHits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/main/setSleep") {
			atomic.AddInt32(&setHits, 1)
			_, _ = w.Write([]byte(`{"response_code":0}`))
			return
		}
		http.NotFound(w, r)
	}))
	defer srv.Close()

	s := newSleepTestState(t, srv)

	cmd := newSleepCmd()
	cmd.SetContext(context.Background())
	setStateOnCmd(cmd, s)
	cmd.SetOut(&strings.Builder{})
	cmd.SetErr(&strings.Builder{})
	cmd.SetArgs([]string{"45"})

	err := cmd.Execute()
	if err == nil {
		t.Fatal("expected ValidationError, got nil")
	}
	var ve *ValidationError
	if !errors.As(err, &ve) {
		t.Fatalf("expected *ValidationError, got %v (%T)", err, err)
	}
	if got := atomic.LoadInt32(&setHits); got != 0 {
		t.Errorf("setSleep hits: got %d, want 0", got)
	}
}

// TestRunSleep_NonIntegerArgIsUsageError covers the strconv.Atoi error path
// (e.g. `yamaha sleep nope`).
func TestRunSleep_NonIntegerArgIsUsageError(t *testing.T) {
	resetFeatureLoader(t)
	redirectCacheDir(t)

	var setHits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/main/setSleep") {
			atomic.AddInt32(&setHits, 1)
		}
		http.NotFound(w, r)
	}))
	defer srv.Close()

	s := newSleepTestState(t, srv)

	cmd := newSleepCmd()
	cmd.SetContext(context.Background())
	setStateOnCmd(cmd, s)
	cmd.SetOut(&strings.Builder{})
	cmd.SetErr(&strings.Builder{})
	cmd.SetArgs([]string{"nope"})

	err := cmd.Execute()
	if err == nil {
		t.Fatal("expected usageError, got nil")
	}
	var uerr *usageError
	if !errors.As(err, &uerr) {
		t.Fatalf("expected *usageError, got %v (%T)", err, err)
	}
	if got := atomic.LoadInt32(&setHits); got != 0 {
		t.Errorf("setSleep hits: got %d, want 0", got)
	}
}
