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

func decoderFeatures() *yxc.Features {
	return &yxc.Features{
		ResponseCode: 0,
		System: yxc.SystemFeatures{
			ZoneNum:   1,
			InputList: []yxc.InputItem{{ID: "hdmi1"}},
		},
		Zone: []yxc.ZoneFeatures{{
			ID:                  "main",
			FuncList:            []string{"power", "surr_decoder_type"},
			InputList:           []string{"hdmi1"},
			SurrDecoderTypeList: []string{"auto", "dolby_pl", "dolby_pl2_movie", "dts_neural_x"},
		}},
	}
}

func newDecoderTestState(t *testing.T, srv *httptest.Server, deviceID string) *state {
	t.Helper()
	cachePath := resolvedCachePath(t, deviceID)
	writeCachedFeatures(t, cachePath, decoderFeatures())

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

func TestRunDecoder_HappyPath(t *testing.T) {
	resetFeatureLoader(t)
	redirectCacheDir(t)

	const deviceID = "00A0DEDC0001"

	var (
		setHits  int32
		gotQuery string
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/system/getDeviceInfo"):
			_, _ = w.Write([]byte(`{"response_code":0,"device_id":"` + deviceID + `","model_name":"RX-V583"}`))
		case strings.HasSuffix(r.URL.Path, "/system/getFeatures"):
			payload, _ := json.Marshal(decoderFeatures())
			_, _ = w.Write(payload)
		case strings.HasSuffix(r.URL.Path, "/main/setSurroundDecoderType"):
			atomic.AddInt32(&setHits, 1)
			gotQuery = r.URL.RawQuery
			_, _ = w.Write([]byte(`{"response_code":0}`))
		default:
			t.Errorf("unexpected request %s", r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	s := newDecoderTestState(t, srv, deviceID)

	cmd := newDecoderCmd()
	cmd.SetContext(context.Background())
	setStateOnCmd(cmd, s)
	cmd.SetOut(&strings.Builder{})
	cmd.SetErr(&strings.Builder{})
	cmd.SetArgs([]string{"dts_neural_x"})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if got := atomic.LoadInt32(&setHits); got != 1 {
		t.Fatalf("setSurroundDecoderType hits: got %d, want 1", got)
	}
	if gotQuery != "type=dts_neural_x" {
		t.Errorf("setSurroundDecoderType query: got %q, want %q", gotQuery, "type=dts_neural_x")
	}
}

func TestRunDecoder_UnknownTypeRejected(t *testing.T) {
	resetFeatureLoader(t)
	redirectCacheDir(t)

	const deviceID = "00A0DEDC0002"

	var setHits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/system/getDeviceInfo"):
			_, _ = w.Write([]byte(`{"response_code":0,"device_id":"` + deviceID + `","model_name":"RX-V583"}`))
		case strings.HasSuffix(r.URL.Path, "/system/getFeatures"):
			payload, _ := json.Marshal(decoderFeatures())
			_, _ = w.Write(payload)
		case strings.HasSuffix(r.URL.Path, "/main/setSurroundDecoderType"):
			atomic.AddInt32(&setHits, 1)
			_, _ = w.Write([]byte(`{"response_code":0}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	s := newDecoderTestState(t, srv, deviceID)

	err := validateSurroundDecoder(context.Background(), s, "dolbi_pl") // typo
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
	if got := atomic.LoadInt32(&setHits); got != 0 {
		t.Errorf("setSurroundDecoderType hits: got %d, want 0", got)
	}
}
