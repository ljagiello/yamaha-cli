package cli

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/ljagiello/yamaha-cli/internal/config"
	"github.com/ljagiello/yamaha-cli/pkg/yxc"
)

// resetFeatureLoader empties the package-level singleton so tests don't
// see state from a previous test in the same run. The singleton is the
// process-local memo for loadFeatures.
func resetFeatureLoader(t *testing.T) {
	t.Helper()
	prev := fl
	fl = &featureLoader{}
	t.Cleanup(func() { fl = prev })
}

// redirectCacheDir points os.UserCacheDir at a temporary directory for the
// duration of the test. yxc.FeaturesCache uses os.UserCacheDir() as the
// default root, and on Darwin that resolves under $HOME/Library/Caches; on
// Linux it honours XDG_CACHE_HOME. Setting both keeps the test portable.
func redirectCacheDir(t *testing.T) string {
	t.Helper()
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)
	t.Setenv("XDG_CACHE_HOME", tmp)
	t.Setenv("XDG_CONFIG_HOME", tmp)
	return tmp
}

// resolvedCachePath returns the on-disk path that yxc.FeaturesCache will
// pick for deviceID under the (test-redirected) UserCacheDir. We mirror
// the logic in pkg/yxc/cache.go: <UserCacheDir>/yamaha-cli/<id>-features.json.
func resolvedCachePath(t *testing.T, deviceID string) string {
	t.Helper()
	root, err := os.UserCacheDir()
	if err != nil {
		t.Fatalf("UserCacheDir: %v", err)
	}
	dir := filepath.Join(root, "yamaha-cli")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir cache: %v", err)
	}
	return filepath.Join(dir, deviceID+"-features.json")
}

// thinFeatures returns a Features blob that lacks hdmi3 — the "stale
// cache" scenario from the README DoD: cache deliberately omits an input the
// firmware now supports.
func thinFeatures() *yxc.Features {
	return &yxc.Features{
		ResponseCode: 0,
		System: yxc.SystemFeatures{
			ZoneNum:   1,
			InputList: []yxc.InputItem{{ID: "hdmi1"}, {ID: "hdmi2"}},
		},
		Zone: []yxc.ZoneFeatures{{
			ID:        "main",
			FuncList:  []string{"power", "volume"},
			InputList: []string{"hdmi1", "hdmi2"},
		}},
	}
}

// fatFeatures returns a Features blob that contains hdmi3 — the refreshed
// payload the device returns after the auto-refresh.
func fatFeatures() *yxc.Features {
	return &yxc.Features{
		ResponseCode: 0,
		System: yxc.SystemFeatures{
			ZoneNum:   1,
			InputList: []yxc.InputItem{{ID: "hdmi1"}, {ID: "hdmi2"}, {ID: "hdmi3"}},
		},
		Zone: []yxc.ZoneFeatures{{
			ID:        "main",
			FuncList:  []string{"power", "volume", "prepare_input_change"},
			InputList: []string{"hdmi1", "hdmi2", "hdmi3"},
		}},
	}
}

func inputListFeatures() *yxc.Features {
	return &yxc.Features{
		ResponseCode: 0,
		System: yxc.SystemFeatures{
			ZoneNum: 1,
			InputList: []yxc.InputItem{
				{
					ID:                 "pandora",
					DistributionEnable: true,
					AccountEnable:      true,
					PlayInfoType:       "netusb",
				},
				{
					ID:                 "hdmi2",
					DistributionEnable: true,
					RenameEnable:       true,
					PlayInfoType:       "none",
				},
			},
		},
		Zone: []yxc.ZoneFeatures{{
			ID:        "main",
			FuncList:  []string{"power", "volume", "prepare_input_change"},
			InputList: []string{"pandora", "hdmi2"},
		}},
	}
}

// writeCachedFeatures persists feats at the cache path that
// yxc.FeaturesCache will look up for deviceID. The format must match
// what cache.save writes (json.Encoder with indent), but encoding/json
// round-trips cleanly so a plain Marshal works for tests.
func writeCachedFeatures(t *testing.T, path string, feats *yxc.Features) {
	t.Helper()
	raw, err := json.Marshal(feats)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if err := os.WriteFile(path, raw, 0o644); err != nil {
		t.Fatalf("write cache: %v", err)
	}
}

func TestBuildInputListPayloadIncludesCurrentAndMetadata(t *testing.T) {
	rows := buildInputListPayload(inputListFeatures(), "main", "hdmi2")
	if len(rows) != 2 {
		t.Fatalf("rows: got %d, want 2", len(rows))
	}

	if rows[0]["current"] != "" || rows[0]["input"] != "pandora" {
		t.Errorf("pandora row current/input = %q/%q", rows[0]["current"], rows[0]["input"])
	}
	if rows[0]["type"] != "service" || rows[0]["notes"] != "account setup, link" {
		t.Errorf("pandora metadata row = %#v", rows[0])
	}
	if rows[1]["current"] != "*" || rows[1]["input"] != "hdmi2" {
		t.Errorf("hdmi2 row current/input = %q/%q", rows[1]["current"], rows[1]["input"])
	}
	if rows[1]["type"] != "hdmi" || rows[1]["notes"] != "link, rename" {
		t.Errorf("hdmi2 metadata row = %#v", rows[1])
	}
}

func TestRunInputNoArgShowsCurrentAndMetadata(t *testing.T) {
	resetFeatureLoader(t)
	redirectCacheDir(t)

	const deviceID = "00A0DEC60001"
	cachePath := resolvedCachePath(t, deviceID)
	writeCachedFeatures(t, cachePath, inputListFeatures())

	var setInputHits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/main/getStatus"):
			_, _ = w.Write([]byte(`{"response_code":0,"power":"on","volume":60,"mute":false,"input":"hdmi2"}`))
		case strings.HasSuffix(r.URL.Path, "/main/setInput"):
			atomic.AddInt32(&setInputHits, 1)
			_, _ = w.Write([]byte(`{"response_code":0}`))
		default:
			t.Errorf("unexpected request %s", r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	c, err := yxc.New(srv.URL)
	if err != nil {
		t.Fatalf("yxc.New: %v", err)
	}
	s := &state{
		cfg:   &config.Config{Devices: map[string]config.Device{}},
		alias: "test",
		device: config.Device{
			Host:        srv.URL,
			DeviceID:    deviceID,
			DefaultZone: "main",
		},
		zone:   "main",
		client: c,
	}

	cmd := newInputCmd()
	cmd.SetContext(context.Background())
	setStateOnCmd(cmd, s)
	out := &strings.Builder{}
	cmd.SetOut(out)
	cmd.SetErr(&strings.Builder{})
	cmd.Flags().String("output", "table", "")
	cmd.SetArgs(nil)

	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if got := atomic.LoadInt32(&setInputHits); got != 0 {
		t.Errorf("setInput hits: got %d, want 0", got)
	}

	body := out.String()
	for _, want := range []string{"current", "input", "type", "notes", "pandora", "account setup, link", "*        hdmi2"} {
		if !strings.Contains(body, want) {
			t.Errorf("output missing %q; got:\n%s", want, body)
		}
	}
}

// TestRunInputNoArgDegradesWhenStatusFails pins the contract that the
// no-arg list is cache-backed: a getStatus failure (receiver off, network
// down) must not fail the command — it renders the list with an empty
// current column and a stderr warning.
func TestRunInputNoArgDegradesWhenStatusFails(t *testing.T) {
	resetFeatureLoader(t)
	redirectCacheDir(t)

	const deviceID = "00A0DEC60002"
	cachePath := resolvedCachePath(t, deviceID)
	writeCachedFeatures(t, cachePath, inputListFeatures())

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/main/getStatus"):
			http.Error(w, "boom", http.StatusInternalServerError)
		default:
			t.Errorf("unexpected request %s", r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	c, err := yxc.New(srv.URL)
	if err != nil {
		t.Fatalf("yxc.New: %v", err)
	}
	s := &state{
		cfg:   &config.Config{Devices: map[string]config.Device{}},
		alias: "test",
		device: config.Device{
			Host:        srv.URL,
			DeviceID:    deviceID,
			DefaultZone: "main",
		},
		zone:   "main",
		client: c,
	}

	cmd := newInputCmd()
	cmd.SetContext(context.Background())
	setStateOnCmd(cmd, s)
	out := &strings.Builder{}
	errOut := &strings.Builder{}
	cmd.SetOut(out)
	cmd.SetErr(errOut)
	cmd.Flags().String("output", "table", "")
	cmd.SetArgs(nil)

	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute should degrade, not fail: %v", err)
	}

	body := out.String()
	for _, want := range []string{"pandora", "hdmi2"} {
		if !strings.Contains(body, want) {
			t.Errorf("output missing %q; got:\n%s", want, body)
		}
	}
	if strings.Contains(body, "*") {
		t.Errorf("no current marker expected when getStatus fails, got:\n%s", body)
	}
	if !strings.Contains(errOut.String(), "warning: current input unknown") {
		t.Errorf("stderr missing degradation warning, got:\n%s", errOut.String())
	}
}

// TestValidateInput_StaleCacheRefresh asserts the the README acceptance
// criterion: when the cached features omit an input the user names, the
// CLI auto-refreshes (one extra getFeatures), validates, and persists the
// refreshed payload to disk. setInput itself is the caller's job — this
// test covers the validation half (which is what triggers the refresh).
func TestValidateInput_StaleCacheRefresh(t *testing.T) {
	resetFeatureLoader(t)
	redirectCacheDir(t)

	const deviceID = "00A0DEC0FFEE"

	// Pre-populate the on-disk cache with the THIN payload so the first
	// loadFeatures call hits the cache (no network) and returns features
	// without hdmi3.
	cachePath := resolvedCachePath(t, deviceID)
	writeCachedFeatures(t, cachePath, thinFeatures())

	var (
		featuresHits  int32
		setInputHits  int32
		deviceInfoHit int32
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/system/getDeviceInfo"):
			atomic.AddInt32(&deviceInfoHit, 1)
			_, _ = w.Write([]byte(`{"response_code":0,"device_id":"` + deviceID + `","model_name":"RX-V583"}`))
		case strings.HasSuffix(r.URL.Path, "/system/getFeatures"):
			atomic.AddInt32(&featuresHits, 1)
			payload, _ := json.Marshal(fatFeatures())
			_, _ = w.Write(payload)
		case strings.HasSuffix(r.URL.Path, "/main/setInput"):
			atomic.AddInt32(&setInputHits, 1)
			_, _ = w.Write([]byte(`{"response_code":0}`))
		default:
			t.Errorf("unexpected request %s", r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	c, err := yxc.New(srv.URL)
	if err != nil {
		t.Fatalf("yxc.New: %v", err)
	}
	s := &state{
		cfg:    &config.Config{Devices: map[string]config.Device{}},
		alias:  "test",
		device: config.Device{Host: srv.URL, DefaultZone: "main"},
		zone:   "main",
		client: c,
	}

	feats, err := validateInput(context.Background(), s, "hdmi3")
	if err != nil {
		t.Fatalf("validateInput: %v", err)
	}
	if feats == nil {
		t.Fatal("expected non-nil features")
	}
	// We can't easily count "cache miss + auto-refresh" as 2 distinct
	// hits because the on-disk cache short-circuits the first call. The
	// observable behaviour is: exactly one getFeatures hit (the
	// auto-refresh after the validation miss) plus one getDeviceInfo
	// (resolveDeviceID).
	if got := atomic.LoadInt32(&featuresHits); got != 1 {
		t.Errorf("getFeatures hits: got %d, want 1 (auto-refresh)", got)
	}
	if got := atomic.LoadInt32(&deviceInfoHit); got != 1 {
		t.Errorf("getDeviceInfo hits: got %d, want 1", got)
	}

	// Refreshed features must contain hdmi3 — the very thing we fetched
	// for. validateInput returns the refreshed *Features.
	if !isInputAllowed(feats, "main", "hdmi3") {
		t.Errorf("refreshed features still missing hdmi3: %v", feats.ZoneInputs("main"))
	}

	// Cache file on disk must have been overwritten atomically with the
	// refreshed payload.
	raw, rerr := os.ReadFile(cachePath)
	if rerr != nil {
		t.Fatalf("read cache: %v", rerr)
	}
	var disk yxc.Features
	if err := json.Unmarshal(raw, &disk); err != nil {
		t.Fatalf("decode cache: %v", err)
	}
	if !isInputAllowed(&disk, "main", "hdmi3") {
		t.Errorf("on-disk cache still missing hdmi3 after refresh: %v", disk.ZoneInputs("main"))
	}

	// validateInput should not have fired setInput itself; that's the
	// caller's job. We assert it explicitly so a regression doesn't
	// silently shift the boundary.
	if got := atomic.LoadInt32(&setInputHits); got != 0 {
		t.Errorf("setInput hits: got %d, want 0 (validation must not fire setInput)", got)
	}
}

// TestRunInput_TypoZeroSetInput exercises the validation rejection path:
// a user typo never produces a setInput request and the returned error is
// a *ValidationError carrying suggestions.
func TestRunInput_TypoZeroSetInput(t *testing.T) {
	resetFeatureLoader(t)
	redirectCacheDir(t)

	const deviceID = "00A0DEAD0001"

	// Both cache copies (thin and fat) lack "typo", so the auto-refresh
	// branch is exercised AND the final result is a ValidationError.
	cachePath := resolvedCachePath(t, deviceID)
	writeCachedFeatures(t, cachePath, thinFeatures())

	var setInputHits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/system/getDeviceInfo"):
			_, _ = w.Write([]byte(`{"response_code":0,"device_id":"` + deviceID + `","model_name":"RX-V583"}`))
		case strings.HasSuffix(r.URL.Path, "/system/getFeatures"):
			payload, _ := json.Marshal(fatFeatures())
			_, _ = w.Write(payload)
		case strings.HasSuffix(r.URL.Path, "/main/setInput"):
			atomic.AddInt32(&setInputHits, 1)
			_, _ = w.Write([]byte(`{"response_code":0}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	c, err := yxc.New(srv.URL)
	if err != nil {
		t.Fatalf("yxc.New: %v", err)
	}
	s := &state{
		cfg:    &config.Config{Devices: map[string]config.Device{}},
		alias:  "test",
		device: config.Device{Host: srv.URL, DefaultZone: "main"},
		zone:   "main",
		client: c,
	}

	_, err = validateInput(context.Background(), s, "typo")
	if err == nil {
		t.Fatal("expected ValidationError, got nil")
	}
	var ve *ValidationError
	if !errors.As(err, &ve) {
		t.Fatalf("expected *ValidationError, got %v (%T)", err, err)
	}
	if len(ve.Suggestions) == 0 {
		t.Errorf("expected non-empty Suggestions, got none")
	}
	if got := atomic.LoadInt32(&setInputHits); got != 0 {
		t.Errorf("setInput hits: got %d, want 0 (validation failure must not fire setInput)", got)
	}
}
