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

// presetFeatures returns a Features payload exposing a NetUSB preset
// count, so preset recall has something to bounds-check against.
func presetFeatures() *yxc.Features {
	return &yxc.Features{
		ResponseCode: 0,
		System: yxc.SystemFeatures{
			ZoneNum:   1,
			InputList: []yxc.InputItem{{ID: "server"}},
		},
		Zone: []yxc.ZoneFeatures{{
			ID:        "main",
			FuncList:  []string{"power", "volume"},
			InputList: []string{"server"},
		}},
		NetUSB: &yxc.NetUSBFeatures{
			Preset: &struct {
				Num int `json:"num"`
			}{Num: 40},
		},
	}
}

// newPresetTestState builds a *state pointing at srv with the on-disk
// features cache pre-populated for the device ID.
func newPresetTestState(t *testing.T, srv *httptest.Server, deviceID string) *state {
	t.Helper()
	cachePath := resolvedCachePath(t, deviceID)
	writeCachedFeatures(t, cachePath, presetFeatures())

	c, err := yxc.New(srv.URL)
	if err != nil {
		t.Fatalf("yxc.New: %v", err)
	}
	return &state{
		cfg:    &config.Config{Devices: map[string]config.Device{}},
		alias:  "test",
		device: config.Device{Host: srv.URL, DefaultZone: "main", DeviceID: deviceID},
		zone:   "main",
		client: c,
	}
}

// TestRunPresetRecall_HappyPath asserts the README acceptance criterion:
// a valid preset recall fires exactly one /netusb/recallPreset request
// with zone=main and the user's num.
func TestRunPresetRecall_HappyPath(t *testing.T) {
	resetFeatureLoader(t)
	redirectCacheDir(t)

	const deviceID = "00A0DEPRESET1"
	var (
		recallHits int32
		gotQuery   string
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/system/getFeatures"):
			payload, _ := json.Marshal(presetFeatures())
			_, _ = w.Write(payload)
		case strings.HasSuffix(r.URL.Path, "/netusb/recallPreset"):
			atomic.AddInt32(&recallHits, 1)
			gotQuery = r.URL.RawQuery
			_, _ = w.Write([]byte(`{"response_code":0}`))
		default:
			t.Errorf("unexpected request %s", r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	s := newPresetTestState(t, srv, deviceID)
	cmd := newPresetRecallCmd()
	cmd.SetContext(context.Background())
	setStateOnCmd(cmd, s)
	cmd.SetOut(&strings.Builder{})
	cmd.SetErr(&strings.Builder{})

	if err := cmd.RunE(cmd, []string{"3"}); err != nil {
		t.Fatalf("RunE: %v", err)
	}
	if got := atomic.LoadInt32(&recallHits); got != 1 {
		t.Fatalf("recallPreset hits: got %d, want 1", got)
	}
	const want = "num=3&zone=main"
	if gotQuery != want {
		t.Errorf("recallPreset query: got %q, want %q", gotQuery, want)
	}
}

// TestRunPresetRecall_ZeroRejected covers the README "preset recall 0 →
// ValidationError, zero HTTP calls" path. The validator rejects num<1
// without consulting the device.
func TestRunPresetRecall_ZeroRejected(t *testing.T) {
	resetFeatureLoader(t)
	redirectCacheDir(t)

	const deviceID = "00A0DEPRESET0"
	var (
		recallHits   int32
		featuresHits int32
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/system/getFeatures"):
			atomic.AddInt32(&featuresHits, 1)
			payload, _ := json.Marshal(presetFeatures())
			_, _ = w.Write(payload)
		case strings.HasSuffix(r.URL.Path, "/netusb/recallPreset"):
			atomic.AddInt32(&recallHits, 1)
			_, _ = w.Write([]byte(`{"response_code":0}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	s := newPresetTestState(t, srv, deviceID)
	cmd := newPresetRecallCmd()
	cmd.SetContext(context.Background())
	setStateOnCmd(cmd, s)
	cmd.SetOut(&strings.Builder{})
	cmd.SetErr(&strings.Builder{})

	err := cmd.RunE(cmd, []string{"0"})
	if err == nil {
		t.Fatal("expected ValidationError, got nil")
	}
	var ve *ValidationError
	if !errors.As(err, &ve) {
		t.Fatalf("expected *ValidationError, got %T (%v)", err, err)
	}
	if got := atomic.LoadInt32(&recallHits); got != 0 {
		t.Errorf("recallPreset hits: got %d, want 0", got)
	}
	// The README explicitly calls out "zero HTTP calls" — this includes
	// the features fetch. The 0 short-circuit must fire *before*
	// loadFeatures.
	if got := atomic.LoadInt32(&featuresHits); got != 0 {
		t.Errorf("getFeatures hits: got %d, want 0 (num<1 short-circuit)", got)
	}
}

// TestRunPresetRecall_OutOfRangeRejected covers the upper-bound check
// against features.NetUSB.Preset.Num. preset 99 with max=40 is rejected.
func TestRunPresetRecall_OutOfRangeRejected(t *testing.T) {
	resetFeatureLoader(t)
	redirectCacheDir(t)

	const deviceID = "00A0DEPRESET2"
	var recallHits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/system/getFeatures"):
			payload, _ := json.Marshal(presetFeatures())
			_, _ = w.Write(payload)
		case strings.HasSuffix(r.URL.Path, "/netusb/recallPreset"):
			atomic.AddInt32(&recallHits, 1)
			_, _ = w.Write([]byte(`{"response_code":0}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	s := newPresetTestState(t, srv, deviceID)
	cmd := newPresetRecallCmd()
	cmd.SetContext(context.Background())
	setStateOnCmd(cmd, s)
	cmd.SetOut(&strings.Builder{})
	cmd.SetErr(&strings.Builder{})

	err := cmd.RunE(cmd, []string{"99"})
	if err == nil {
		t.Fatal("expected ValidationError, got nil")
	}
	var ve *ValidationError
	if !errors.As(err, &ve) {
		t.Fatalf("expected *ValidationError, got %T (%v)", err, err)
	}
	if got := atomic.LoadInt32(&recallHits); got != 0 {
		t.Errorf("recallPreset hits: got %d, want 0", got)
	}
}

// TestRunPresetRecall_MalformedRejected covers the parsing branch — a
// non-numeric arg is a usage error (exit 2).
func TestRunPresetRecall_MalformedRejected(t *testing.T) {
	resetFeatureLoader(t)
	redirectCacheDir(t)

	const deviceID = "00A0DEPRESET3"
	srv := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {
		t.Fatal("unexpected wire call on malformed input")
	}))
	defer srv.Close()

	s := newPresetTestState(t, srv, deviceID)
	cmd := newPresetRecallCmd()
	cmd.SetContext(context.Background())
	setStateOnCmd(cmd, s)
	cmd.SetOut(&strings.Builder{})
	cmd.SetErr(&strings.Builder{})

	err := cmd.RunE(cmd, []string{"twelve"})
	if err == nil {
		t.Fatal("expected usage error, got nil")
	}
	var uerr *usageError
	if !errors.As(err, &uerr) {
		t.Fatalf("expected *usageError, got %T (%v)", err, err)
	}
}

// TestRunPresetList_RendersRows asserts the list renderer emits one row
// per preset entry with num/input/text fields populated.
func TestRunPresetList_RendersRows(t *testing.T) {
	resetFeatureLoader(t)
	redirectCacheDir(t)

	const deviceID = "00A0DEPRESETLS"
	const body = `{"response_code":0,"preset_info":[{"input":"server","text":"Jazz"},{"input":"net_radio","text":"BBC R6"},{"input":"server","text":""}]}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/netusb/getPresetInfo") {
			t.Errorf("unexpected path %q", r.URL.Path)
		}
		_, _ = w.Write([]byte(body))
	}))
	defer srv.Close()

	s := newPresetTestState(t, srv, deviceID)
	cmd := newPresetListCmd()
	cmd.SetContext(context.Background())
	setStateOnCmd(cmd, s)
	out := &strings.Builder{}
	cmd.SetOut(out)
	cmd.SetErr(&strings.Builder{})

	if err := cmd.RunE(cmd, nil); err != nil {
		t.Fatalf("RunE: %v", err)
	}
	got := out.String()
	for _, want := range []string{"Jazz", "BBC R6", "server", "net_radio"} {
		if !strings.Contains(got, want) {
			t.Errorf("rendered output missing %q: %s", want, got)
		}
	}
}

// TestBuildPresetListPayload exercises the converter: empty input → nil,
// populated input → numbered rows.
func TestBuildPresetListPayload(t *testing.T) {
	if got := buildPresetListPayload(nil); got != nil {
		t.Errorf("nil input: got %v, want nil", got)
	}
	info := &yxc.PresetInfo{
		PresetInfo: []yxc.NetUSBPreset{
			{Input: "server", Text: "A"},
			{Input: "net_radio", Text: "B"},
		},
	}
	rows := buildPresetListPayload(info)
	if len(rows) != 2 {
		t.Fatalf("rows: got %d, want 2", len(rows))
	}
	if rows[0]["num"] != 1 {
		t.Errorf("row 0 num: got %v, want 1", rows[0]["num"])
	}
	if rows[1]["num"] != 2 {
		t.Errorf("row 1 num: got %v, want 2", rows[1]["num"])
	}
	if rows[0]["text"] != "A" || rows[1]["input"] != "net_radio" {
		t.Errorf("rows: got %+v", rows)
	}
}
