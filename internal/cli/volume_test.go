package cli

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/ljagiello/yamaha-cli/internal/config"
	"github.com/ljagiello/yamaha-cli/pkg/yxc"
)

// volumeFeatures returns a Features payload with a known volume range so
// volume parsing has something to clamp against.
func volumeFeatures() *yxc.Features {
	return &yxc.Features{
		ResponseCode: 0,
		System: yxc.SystemFeatures{
			ZoneNum:   1,
			InputList: []yxc.InputItem{{ID: "hdmi1"}},
		},
		Zone: []yxc.ZoneFeatures{{
			ID:        "main",
			FuncList:  []string{"power", "volume"},
			InputList: []string{"hdmi1"},
			RangeStep: []yxc.RangeStep{{ID: "volume", Min: 0, Max: 161, Step: 1}},
		}},
	}
}

// newVolumeTestState builds a *state whose YXC client targets srv and
// pre-populates the on-disk features cache so loadFeatures() works without
// extra network round-trips.
func newVolumeTestState(t *testing.T, srv *httptest.Server, deviceID string) *state {
	t.Helper()

	cachePath := resolvedCachePath(t, deviceID)
	writeCachedFeatures(t, cachePath, volumeFeatures())

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

// TestRunVolume_PlusFiveOneRequest asserts the the README acceptance
// criterion: `yamaha volume +5` issues exactly one setVolume request with
// volume=up and step=5.
func TestRunVolume_PlusFiveOneRequest(t *testing.T) {
	resetFeatureLoader(t)
	redirectCacheDir(t)

	const deviceID = "00A0DECAFE01"

	var (
		setVolumeHits int32
		gotQuery      string
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/system/getDeviceInfo"):
			_, _ = w.Write([]byte(`{"response_code":0,"device_id":"` + deviceID + `","model_name":"RX-V583"}`))
		case strings.HasSuffix(r.URL.Path, "/system/getFeatures"):
			payload, _ := json.Marshal(volumeFeatures())
			_, _ = w.Write(payload)
		case strings.HasSuffix(r.URL.Path, "/main/setVolume"):
			atomic.AddInt32(&setVolumeHits, 1)
			gotQuery = r.URL.RawQuery
			_, _ = w.Write([]byte(`{"response_code":0}`))
		default:
			t.Errorf("unexpected request %s", r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	s := newVolumeTestState(t, srv, deviceID)

	cmd := newVolumeCmd()
	cmd.SetContext(context.Background())
	setStateOnCmd(cmd, s)
	cmd.SetOut(&strings.Builder{})
	cmd.SetErr(&strings.Builder{})
	if err := cmd.ParseFlags(nil); err != nil {
		t.Fatalf("ParseFlags: %v", err)
	}

	if err := runVolume(cmd, []string{"+5"}); err != nil {
		t.Fatalf("runVolume: %v", err)
	}
	if got := atomic.LoadInt32(&setVolumeHits); got != 1 {
		t.Fatalf("setVolume hits: got %d, want 1", got)
	}
	// query string ordering is alphabetical because url.Values.Encode
	// sorts keys: step=5&volume=up.
	if gotQuery != "step=5&volume=up" {
		t.Errorf("setVolume query: got %q, want %q", gotQuery, "step=5&volume=up")
	}
}

// TestRunVolume_DBWithDeltaErrors asserts the README: combining --db with a
// signed delta (+5) is a usage error (exit 2). No setVolume request must
// fire.
func TestRunVolume_DBWithDeltaErrors(t *testing.T) {
	resetFeatureLoader(t)
	redirectCacheDir(t)

	const deviceID = "00A0DECAFE02"

	var setVolumeHits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/system/getDeviceInfo"):
			_, _ = w.Write([]byte(`{"response_code":0,"device_id":"` + deviceID + `","model_name":"RX-V583"}`))
		case strings.HasSuffix(r.URL.Path, "/system/getFeatures"):
			payload, _ := json.Marshal(volumeFeatures())
			_, _ = w.Write(payload)
		case strings.HasSuffix(r.URL.Path, "/main/setVolume"):
			atomic.AddInt32(&setVolumeHits, 1)
			_, _ = w.Write([]byte(`{"response_code":0}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	s := newVolumeTestState(t, srv, deviceID)

	cmd := newVolumeCmd()
	cmd.SetContext(context.Background())
	setStateOnCmd(cmd, s)
	cmd.SetOut(&strings.Builder{})
	cmd.SetErr(&strings.Builder{})

	// Flip --db on the cobra command so runVolume sees it.
	if err := cmd.Flags().Set("db", "true"); err != nil {
		t.Fatalf("set --db: %v", err)
	}

	err := runVolume(cmd, []string{"+5"})
	if err == nil {
		t.Fatal("expected usage error, got nil")
	}
	var uerr *usageError
	if !errors.As(err, &uerr) {
		t.Fatalf("expected *usageError, got %v (%T)", err, err)
	}
	if got := ErrorExitCode(err); got != 2 {
		t.Errorf("ErrorExitCode: got %d, want 2", got)
	}
	if got := atomic.LoadInt32(&setVolumeHits); got != 0 {
		t.Errorf("setVolume hits: got %d, want 0 (usage error must not fire setVolume)", got)
	}
}
