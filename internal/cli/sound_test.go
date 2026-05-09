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

// soundFeatures returns a Features payload with a known sound_program_list
// so the validator has something to check against.
func soundFeatures() *yxc.Features {
	return &yxc.Features{
		ResponseCode: 0,
		System: yxc.SystemFeatures{
			ZoneNum:   1,
			InputList: []yxc.InputItem{{ID: "hdmi1"}},
		},
		Zone: []yxc.ZoneFeatures{{
			ID:               "main",
			FuncList:         []string{"power", "sound_program"},
			InputList:        []string{"hdmi1"},
			SoundProgramList: []string{"standard", "straight", "2ch_stereo", "movie"},
		}},
	}
}

func newSoundTestState(t *testing.T, srv *httptest.Server, deviceID string) *state {
	t.Helper()
	cachePath := resolvedCachePath(t, deviceID)
	writeCachedFeatures(t, cachePath, soundFeatures())

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

// TestRunSound_HappyPath asserts that a valid sound program issues exactly
// one setSoundProgram request with the chosen program.
func TestRunSound_HappyPath(t *testing.T) {
	resetFeatureLoader(t)
	redirectCacheDir(t)

	const deviceID = "00A0DEC50001"

	var (
		setHits  int32
		gotQuery string
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/system/getDeviceInfo"):
			_, _ = w.Write([]byte(`{"response_code":0,"device_id":"` + deviceID + `","model_name":"RX-V583"}`))
		case strings.HasSuffix(r.URL.Path, "/system/getFeatures"):
			payload, _ := json.Marshal(soundFeatures())
			_, _ = w.Write(payload)
		case strings.HasSuffix(r.URL.Path, "/main/setSoundProgram"):
			atomic.AddInt32(&setHits, 1)
			gotQuery = r.URL.RawQuery
			_, _ = w.Write([]byte(`{"response_code":0}`))
		default:
			t.Errorf("unexpected request %s", r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	s := newSoundTestState(t, srv, deviceID)

	cmd := newSoundCmd()
	cmd.SetContext(context.Background())
	setStateOnCmd(cmd, s)
	cmd.SetOut(&strings.Builder{})
	cmd.SetErr(&strings.Builder{})
	cmd.SetArgs([]string{"movie"})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if got := atomic.LoadInt32(&setHits); got != 1 {
		t.Fatalf("setSoundProgram hits: got %d, want 1", got)
	}
	if gotQuery != "program=movie" {
		t.Errorf("setSoundProgram query: got %q, want %q", gotQuery, "program=movie")
	}
}

// TestRunSound_NoArgListsPrograms asserts that `yamaha sound` with no
// argument prints the zone's sound_program_list (sourced from cached
// features) and never fires setSoundProgram.
func TestRunSound_NoArgListsPrograms(t *testing.T) {
	resetFeatureLoader(t)
	redirectCacheDir(t)

	const deviceID = "00A0DEC50003"

	var setHits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/system/getDeviceInfo"):
			_, _ = w.Write([]byte(`{"response_code":0,"device_id":"` + deviceID + `","model_name":"RX-V583"}`))
		case strings.HasSuffix(r.URL.Path, "/system/getFeatures"):
			payload, _ := json.Marshal(soundFeatures())
			_, _ = w.Write(payload)
		case strings.HasSuffix(r.URL.Path, "/main/setSoundProgram"):
			atomic.AddInt32(&setHits, 1)
			_, _ = w.Write([]byte(`{"response_code":0}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	s := newSoundTestState(t, srv, deviceID)

	cmd := newSoundCmd()
	cmd.SetContext(context.Background())
	setStateOnCmd(cmd, s)
	out := &strings.Builder{}
	cmd.SetOut(out)
	cmd.SetErr(&strings.Builder{})
	cmd.Flags().String("output", "json", "")
	cmd.SetArgs(nil)

	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if got := atomic.LoadInt32(&setHits); got != 0 {
		t.Errorf("setSoundProgram hits: got %d, want 0", got)
	}
	body := out.String()
	for _, want := range []string{"standard", "straight", "2ch_stereo", "movie"} {
		if !strings.Contains(body, want) {
			t.Errorf("output missing %q; got:\n%s", want, body)
		}
	}
}

// TestRunSound_UnknownProgramRejected asserts that an unknown program
// returns a *ValidationError with suggestions and never fires
// setSoundProgram.
func TestRunSound_UnknownProgramRejected(t *testing.T) {
	resetFeatureLoader(t)
	redirectCacheDir(t)

	const deviceID = "00A0DEC50002"

	var setHits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/system/getDeviceInfo"):
			_, _ = w.Write([]byte(`{"response_code":0,"device_id":"` + deviceID + `","model_name":"RX-V583"}`))
		case strings.HasSuffix(r.URL.Path, "/system/getFeatures"):
			payload, _ := json.Marshal(soundFeatures())
			_, _ = w.Write(payload)
		case strings.HasSuffix(r.URL.Path, "/main/setSoundProgram"):
			atomic.AddInt32(&setHits, 1)
			_, _ = w.Write([]byte(`{"response_code":0}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	s := newSoundTestState(t, srv, deviceID)

	err := validateSoundProgram(context.Background(), s, "movei") //nolint:misspell // intentional typo of "movie" to test suggestion
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
		t.Errorf("setSoundProgram hits: got %d, want 0", got)
	}
}
