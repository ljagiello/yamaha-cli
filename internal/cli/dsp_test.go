package cli

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/ljagiello/yamaha-cli/internal/config"
	"github.com/ljagiello/yamaha-cli/pkg/yxc"
)

func TestParseOnOff(t *testing.T) {
	on := []string{"on", "ON", "true", "enable", "enabled", "1", " on "}
	for _, v := range on {
		got, err := parseOnOff(v)
		if err != nil || !got {
			t.Errorf("parseOnOff(%q) = (%v, %v), want (true, nil)", v, got, err)
		}
	}
	off := []string{"off", "OFF", "false", "disable", "0"}
	for _, v := range off {
		got, err := parseOnOff(v)
		if err != nil || got {
			t.Errorf("parseOnOff(%q) = (%v, %v), want (false, nil)", v, got, err)
		}
	}
	for _, v := range []string{"", "maybe", "2"} {
		if _, err := parseOnOff(v); err == nil {
			t.Errorf("parseOnOff(%q): want error", v)
		} else if ErrorExitCode(err) != 2 {
			t.Errorf("parseOnOff(%q): exit %d, want 2", v, ErrorExitCode(err))
		}
	}
}

// dspFeatures advertises pure-direct ("direct") + enhancer on main, but
// NOT extra_bass — so the gate must reject extra-bass on this device.
func dspFeatures() *yxc.Features {
	return &yxc.Features{
		Zone: []yxc.ZoneFeatures{{
			ID:        "main",
			FuncList:  []string{"power", "direct", "enhancer"},
			InputList: []string{"hdmi1"},
		}},
		System: yxc.SystemFeatures{ZoneNum: 1, InputList: []yxc.InputItem{{ID: "hdmi1"}}},
	}
}

func newDSPTestState(t *testing.T, srv *httptest.Server, deviceID string) *state {
	t.Helper()
	cachePath := resolvedCachePath(t, deviceID)
	writeCachedFeatures(t, cachePath, dspFeatures())
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

// TestZoneSwitch_Supported sends the wire call when the zone advertises
// the func.
func TestZoneSwitch_Supported(t *testing.T) {
	resetFeatureLoader(t)
	redirectCacheDir(t)
	const deviceID = "00A0DEEE00DS"

	var setHits int32
	var gotQuery string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/system/getDeviceInfo"):
			_, _ = w.Write([]byte(`{"response_code":0,"device_id":"` + deviceID + `","model_name":"RX-V583"}`))
		case strings.HasSuffix(r.URL.Path, "/system/getFeatures"):
			payload, _ := json.Marshal(dspFeatures())
			_, _ = w.Write(payload)
		case strings.HasSuffix(r.URL.Path, "/main/setPureDirect"):
			atomic.AddInt32(&setHits, 1)
			gotQuery = r.URL.RawQuery
			_, _ = w.Write([]byte(`{"response_code":0}`))
		default:
			t.Errorf("unexpected request %s", r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	s := newDSPTestState(t, srv, deviceID)
	cmd := newZoneSwitchCmd(zoneSwitches[0]) // pure-direct
	cmd.SetContext(context.Background())
	setStateOnCmd(cmd, s)
	cmd.SetOut(&strings.Builder{})
	cmd.SetErr(&strings.Builder{})
	cmd.SetArgs([]string{"on"})

	if err := cmd.RunE(cmd, []string{"on"}); err != nil {
		t.Fatalf("RunE: %v", err)
	}
	if atomic.LoadInt32(&setHits) != 1 {
		t.Errorf("setPureDirect hits = %d, want 1", setHits)
	}
	if gotQuery != "enable=true" {
		t.Errorf("query = %q, want enable=true", gotQuery)
	}
}

// TestZoneSwitch_Unsupported fails fast (exit 2) without a wire call when
// the zone doesn't advertise the func.
func TestZoneSwitch_Unsupported(t *testing.T) {
	resetFeatureLoader(t)
	redirectCacheDir(t)
	const deviceID = "00A0DEEE00DU"

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/system/getDeviceInfo"):
			_, _ = w.Write([]byte(`{"response_code":0,"device_id":"` + deviceID + `","model_name":"RX-V583"}`))
		case strings.HasSuffix(r.URL.Path, "/system/getFeatures"):
			payload, _ := json.Marshal(dspFeatures())
			_, _ = w.Write(payload)
		default:
			t.Errorf("unexpected wire call for unsupported feature: %s", r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	s := newDSPTestState(t, srv, deviceID)
	// zoneSwitches[2] == extra-bass, not in dspFeatures' func_list.
	cmd := newZoneSwitchCmd(zoneSwitches[2])
	cmd.SetContext(context.Background())
	setStateOnCmd(cmd, s)
	cmd.SetOut(&strings.Builder{})
	cmd.SetErr(&strings.Builder{})

	err := cmd.RunE(cmd, []string{"on"})
	if err == nil {
		t.Fatal("expected error for unsupported feature")
	}
	if ErrorExitCode(err) != 2 {
		t.Errorf("exit code = %d, want 2 (usage)", ErrorExitCode(err))
	}
	if !strings.Contains(err.Error(), "not supported") {
		t.Errorf("message = %q, want 'not supported'", err.Error())
	}
}
