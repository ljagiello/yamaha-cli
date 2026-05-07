package cli

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ljagiello/yamaha-cli/internal/config"
	"github.com/ljagiello/yamaha-cli/pkg/discover"
	"github.com/ljagiello/yamaha-cli/pkg/yxc"
)

// TestResolveDeviceID_PrefersConfigDeviceID asserts the v2 fast-path
// performance promise: if config.Device.DeviceID is populated (e.g. by
// a prior wizard / discover --add or by persistDeviceID's first fetch),
// resolveDeviceID short-circuits and never issues a getDeviceInfo HTTP
// call. This is what saves one round-trip per command.
func TestResolveDeviceID_PrefersConfigDeviceID(t *testing.T) {
	resetFeatureLoader(t)
	redirectCacheDir(t)

	const deviceID = "00A0DECACHED1"
	cachePath := resolvedCachePath(t, deviceID)
	writeCachedFeatures(t, cachePath, fatFeatures())

	var deviceInfoHits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/system/getDeviceInfo") {
			atomic.AddInt32(&deviceInfoHits, 1)
		}
		// Anything reaching the network in this test is a regression.
		http.Error(w, "unexpected request", http.StatusInternalServerError)
	}))
	defer srv.Close()

	c, err := yxc.New(srv.URL)
	if err != nil {
		t.Fatalf("yxc.New: %v", err)
	}
	s := &state{
		cfg:    &config.Config{Devices: map[string]config.Device{}},
		alias:  "test",
		device: config.Device{Host: srv.URL, DefaultZone: "main", DeviceID: deviceID},
		zone:   "main",
		client: c,
	}

	feats, err := loadFeatures(context.Background(), s, false)
	if err != nil {
		t.Fatalf("loadFeatures: %v", err)
	}
	if feats == nil {
		t.Fatal("expected non-nil features from on-disk cache")
	}
	if got := atomic.LoadInt32(&deviceInfoHits); got != 0 {
		t.Errorf("getDeviceInfo hits: got %d, want 0 (DeviceID came from config)", got)
	}
}

// TestResolveDeviceID_PersistsAfterFetch asserts that the first command
// against an alias-resolved device (with no DeviceID in config) fetches
// device_id once and writes it back atomically — so the next command
// benefits from the fast path covered above.
func TestResolveDeviceID_PersistsAfterFetch(t *testing.T) {
	resetFeatureLoader(t)
	redirectCacheDir(t)

	const deviceID = "00A0DEFFRESH"

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/system/getDeviceInfo"):
			_, _ = w.Write([]byte(`{"response_code":0,"device_id":"` + deviceID + `","model_name":"RX-V583"}`))
		case strings.HasSuffix(r.URL.Path, "/system/getFeatures"):
			payload, _ := json.Marshal(fatFeatures())
			_, _ = w.Write(payload)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	c, err := yxc.New(srv.URL)
	if err != nil {
		t.Fatalf("yxc.New: %v", err)
	}
	cfg := &config.Config{
		Devices: map[string]config.Device{
			"living-room": {Host: srv.URL, DefaultZone: "main"}, // no DeviceID
		},
	}
	s := &state{
		cfg:    cfg,
		alias:  "living-room",
		device: cfg.Devices["living-room"],
		zone:   "main",
		client: c,
	}

	if _, err := loadFeatures(context.Background(), s, false); err != nil {
		t.Fatalf("loadFeatures: %v", err)
	}

	// Verify the config in memory was updated.
	if got := s.cfg.Devices["living-room"].DeviceID; got != deviceID {
		t.Errorf("in-memory config DeviceID: got %q, want %q", got, deviceID)
	}
	// Verify s.device was updated too — the next call should read from
	// the fast path above.
	if s.device.DeviceID != deviceID {
		t.Errorf("s.device.DeviceID: got %q, want %q", s.device.DeviceID, deviceID)
	}
	// Verify the config was persisted to disk atomically. config.Path()
	// resolves under our redirected XDG_CONFIG_HOME / HOME.
	disk, err := config.Load()
	if err != nil {
		t.Fatalf("config.Load: %v", err)
	}
	if got := disk.Devices["living-room"].DeviceID; got != deviceID {
		t.Errorf("on-disk config DeviceID: got %q, want %q", got, deviceID)
	}
	// And it must live where Path says (sanity).
	if _, err := filepath.Abs(config.Path()); err != nil {
		t.Errorf("config.Path: %v", err)
	}
}

// TestLoadFeatures_RecoversFromTransportError exercises the v1 headline
// scenario end-to-end: a feature fetch fails with a transport error
// against the saved IP, SSDP rediscovery returns the receiver at a new
// IP, and the second attempt succeeds. This proves the wrap-in-
// runWithRediscover fix is behaviorally — not just structurally —
// correct for the loadFeatures path.
func TestLoadFeatures_RecoversFromTransportError(t *testing.T) {
	resetFeatureLoader(t)
	redirectCacheDir(t)

	const deviceID = "00A0DEMOVED1"

	// "New" host: the receiver after DHCP shuffle. Serves both
	// getDeviceInfo and getFeatures.
	newHost := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/system/getDeviceInfo"):
			_, _ = w.Write([]byte(`{"response_code":0,"device_id":"` + deviceID + `","model_name":"RX-V583"}`))
		case strings.HasSuffix(r.URL.Path, "/system/getFeatures"):
			payload, _ := json.Marshal(fatFeatures())
			_, _ = w.Write(payload)
		default:
			http.NotFound(w, r)
		}
	}))
	defer newHost.Close()

	// "Old" host: closed listener so any request returns a transport
	// error. Bind a real port then close it; the test client retries
	// once (250ms) before lookupByUDNFn fires.
	old := mustClosedListenerURL(t)

	stub := &stubLookup{
		dev: discover.Device{
			Host: hostFromURL(t, newHost.URL),
			UDN:  "uuid:moved-1",
		},
	}
	stub.install(t)

	cfg := &config.Config{
		Devices: map[string]config.Device{
			"living-room": {
				Host:        old,
				UDN:         "uuid:moved-1",
				DefaultZone: "main",
				DeviceID:    deviceID, // skip getDeviceInfo round-trip in this test
			},
		},
	}
	c, err := yxc.New(old, yxc.WithTimeout(200*time.Millisecond))
	if err != nil {
		t.Fatalf("yxc.New: %v", err)
	}
	s := &state{
		cfg:    cfg,
		alias:  "living-room",
		device: cfg.Devices["living-room"],
		zone:   "main",
		client: c,
		debug:  nil,
	}

	feats, err := loadFeatures(context.Background(), s, false)
	if err != nil {
		t.Fatalf("loadFeatures: expected success after rediscover, got %v", err)
	}
	if feats == nil || !isInputAllowed(feats, "main", "hdmi3") {
		t.Fatalf("expected refreshed features after rediscover")
	}
	if stub.calls != 1 {
		t.Errorf("lookupByUDNFn calls: got %d, want 1", stub.calls)
	}
	// Config was rewritten to the new host (via persistRediscoveredHost).
	wantHost := hostFromURL(t, newHost.URL)
	if got := s.cfg.Devices["living-room"].Host; got != wantHost {
		t.Errorf("Host after rediscover: got %q, want %q", got, wantHost)
	}
}

// mustClosedListenerURL binds an ephemeral port, closes it, and returns
// a URL pointing at the now-dead address. Any HTTP call to the URL
// returns a transport error (connection refused) — what we need to
// trigger the rediscover path.
func mustClosedListenerURL(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	addr := l.Addr().String()
	_ = l.Close()
	return "http://" + addr
}

// hostFromURL strips the http:// prefix from an httptest.Server URL,
// matching the form yamaha-cli stores in config.
func hostFromURL(t *testing.T, raw string) string {
	t.Helper()
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("parse url: %v", err)
	}
	return u.Host
}
