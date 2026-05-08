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

// tunerFeatures returns a Features payload exposing FM/AM frequency ranges
// and a max preset count, so tuner subcommands have something to validate
// against.
func tunerFeatures() *yxc.Features {
	return &yxc.Features{
		ResponseCode: 0,
		System: yxc.SystemFeatures{
			ZoneNum:   1,
			InputList: []yxc.InputItem{{ID: "tuner"}},
		},
		Zone: []yxc.ZoneFeatures{{
			ID:        "main",
			FuncList:  []string{"power", "volume", "tuner"},
			InputList: []string{"tuner"},
			RangeStep: []yxc.RangeStep{{ID: "volume", Min: 0, Max: 161, Step: 1}},
		}},
		Tuner: &yxc.TunerFeatures{
			RangeStep: []yxc.RangeStep{
				// FM range is in kHz: 87500 = 87.5 MHz, 108000 = 108.0 MHz, 200 kHz step.
				{ID: "fm_freq", Min: 87500, Max: 108000, Step: 200},
				{ID: "am_freq", Min: 530, Max: 1710, Step: 9},
			},
			Preset: &struct {
				Type string `json:"type"`
				Num  int    `json:"num"`
			}{Type: "common", Num: 40},
		},
	}
}

// newTunerTestState builds a *state pointing at srv with the on-disk
// features cache pre-populated for the device ID.
func newTunerTestState(t *testing.T, srv *httptest.Server, deviceID string) *state {
	t.Helper()
	cachePath := resolvedCachePath(t, deviceID)
	writeCachedFeatures(t, cachePath, tunerFeatures())

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

// TestRunTunerFM_WireQuery asserts the README acceptance criterion: the
// CLI converts MHz to kHz before issuing setFreq, with the wire
// query in the documented `band=fm&num=102500&tuning=direct` shape.
func TestRunTunerFM_WireQuery(t *testing.T) {
	resetFeatureLoader(t)
	redirectCacheDir(t)

	const deviceID = "00A0DETUNERFM"
	var (
		setFreqHits int32
		gotQuery    string
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/system/getFeatures"):
			payload, _ := json.Marshal(tunerFeatures())
			_, _ = w.Write(payload)
		case strings.HasSuffix(r.URL.Path, "/tuner/setFreq"):
			atomic.AddInt32(&setFreqHits, 1)
			gotQuery = r.URL.RawQuery
			_, _ = w.Write([]byte(`{"response_code":0}`))
		default:
			t.Errorf("unexpected request %s", r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	s := newTunerTestState(t, srv, deviceID)
	cmd := newTunerFMCmd()
	cmd.SetContext(context.Background())
	setStateOnCmd(cmd, s)
	cmd.SetOut(&strings.Builder{})
	cmd.SetErr(&strings.Builder{})

	if err := cmd.RunE(cmd, []string{"102.5"}); err != nil {
		t.Fatalf("RunE: %v", err)
	}
	if got := atomic.LoadInt32(&setFreqHits); got != 1 {
		t.Fatalf("setFreq hits: got %d, want 1", got)
	}
	const want = "band=fm&num=102500&tuning=direct"
	if gotQuery != want {
		t.Errorf("setFreq query: got %q, want %q", gotQuery, want)
	}
}

// TestRunTunerFM_OutOfRangeRejected asserts that frequencies outside the
// device's reported range are rejected as ValidationErrors with no wire
// request fired.
func TestRunTunerFM_OutOfRangeRejected(t *testing.T) {
	resetFeatureLoader(t)
	redirectCacheDir(t)

	const deviceID = "00A0DETUNERR1"
	var setFreqHits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/system/getFeatures"):
			payload, _ := json.Marshal(tunerFeatures())
			_, _ = w.Write(payload)
		case strings.HasSuffix(r.URL.Path, "/tuner/setFreq"):
			atomic.AddInt32(&setFreqHits, 1)
			_, _ = w.Write([]byte(`{"response_code":0}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	s := newTunerTestState(t, srv, deviceID)
	cmd := newTunerFMCmd()
	cmd.SetContext(context.Background())
	setStateOnCmd(cmd, s)
	cmd.SetOut(&strings.Builder{})
	cmd.SetErr(&strings.Builder{})

	// 200 MHz is well outside the [87.5, 108.0] band in tunerFeatures.
	err := cmd.RunE(cmd, []string{"200"})
	if err == nil {
		t.Fatal("expected ValidationError, got nil")
	}
	var ve *ValidationError
	if !errors.As(err, &ve) {
		t.Fatalf("expected *ValidationError, got %T (%v)", err, err)
	}
	if got := atomic.LoadInt32(&setFreqHits); got != 0 {
		t.Errorf("setFreq hits: got %d, want 0 (validation must not fire setFreq)", got)
	}
}

// TestRunTunerFM_MalformedRejected covers the parsing branch — a non-numeric
// argument fails as a usage error before any device call.
func TestRunTunerFM_MalformedRejected(t *testing.T) {
	resetFeatureLoader(t)
	redirectCacheDir(t)

	srv := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {
		t.Fatal("unexpected wire call on malformed input")
	}))
	defer srv.Close()

	s := newTunerTestState(t, srv, "00A0DEMALFORM")
	cmd := newTunerFMCmd()
	cmd.SetContext(context.Background())
	setStateOnCmd(cmd, s)
	cmd.SetOut(&strings.Builder{})
	cmd.SetErr(&strings.Builder{})

	err := cmd.RunE(cmd, []string{"not-a-number"})
	if err == nil {
		t.Fatal("expected usage error, got nil")
	}
	var uerr *usageError
	if !errors.As(err, &uerr) {
		t.Fatalf("expected *usageError, got %T (%v)", err, err)
	}
}

// TestRunTunerStatus_PayloadShape exercises the happy path: getPlayInfo is
// hit once, and the rendered payload includes the band-specific freq +
// preset.
func TestRunTunerStatus_PayloadShape(t *testing.T) {
	resetFeatureLoader(t)
	redirectCacheDir(t)

	const deviceID = "00A0DETUNERST"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/tuner/getPlayInfo"):
			_, _ = w.Write([]byte(`{"response_code":0,"band":"fm","fm":{"freq":102500,"preset":3,"audio_mode":"stereo"}}`))
		default:
			t.Errorf("unexpected request %s", r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	s := newTunerTestState(t, srv, deviceID)
	cmd := newTunerStatusCmd()
	cmd.SetContext(context.Background())
	setStateOnCmd(cmd, s)
	out := &strings.Builder{}
	cmd.SetOut(out)
	cmd.SetErr(&strings.Builder{})

	if err := cmd.RunE(cmd, nil); err != nil {
		t.Fatalf("RunE: %v", err)
	}
	got := out.String()
	if !strings.Contains(got, "band") || !strings.Contains(got, "fm") {
		t.Errorf("missing band: %q", got)
	}
	if !strings.Contains(got, "102500") {
		t.Errorf("missing freq: %q", got)
	}
}

// TestRunTunerPreset_OutOfRangeRejected covers the bounds-check against
// features.Tuner.Preset.Num. preset 99 with max=40 is rejected with no
// wire request.
func TestRunTunerPreset_OutOfRangeRejected(t *testing.T) {
	resetFeatureLoader(t)
	redirectCacheDir(t)

	const deviceID = "00A0DETUNERPP"
	var recallHits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/system/getFeatures"):
			payload, _ := json.Marshal(tunerFeatures())
			_, _ = w.Write(payload)
		case strings.HasSuffix(r.URL.Path, "/tuner/getPlayInfo"):
			// The default-band lookup falls back here; return FM.
			_, _ = w.Write([]byte(`{"response_code":0,"band":"fm","fm":{"freq":87500,"preset":0}}`))
		case strings.HasSuffix(r.URL.Path, "/tuner/recallPreset"):
			atomic.AddInt32(&recallHits, 1)
			_, _ = w.Write([]byte(`{"response_code":0}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	s := newTunerTestState(t, srv, deviceID)
	cmd := newTunerPresetCmd()
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

// TestRunTunerPreset_BandFlagOverride checks that --band fm wins over the
// device's reported band, and that the wire request carries the user's
// choice.
func TestRunTunerPreset_BandFlagOverride(t *testing.T) {
	resetFeatureLoader(t)
	redirectCacheDir(t)

	const deviceID = "00A0DETUNERBF"
	var (
		recallHits int32
		gotQuery   string
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/system/getFeatures"):
			payload, _ := json.Marshal(tunerFeatures())
			_, _ = w.Write(payload)
		case strings.HasSuffix(r.URL.Path, "/tuner/recallPreset"):
			atomic.AddInt32(&recallHits, 1)
			gotQuery = r.URL.RawQuery
			_, _ = w.Write([]byte(`{"response_code":0}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	s := newTunerTestState(t, srv, deviceID)
	cmd := newTunerPresetCmd()
	cmd.SetContext(context.Background())
	setStateOnCmd(cmd, s)
	cmd.SetOut(&strings.Builder{})
	cmd.SetErr(&strings.Builder{})
	if err := cmd.Flags().Set("band", "am"); err != nil {
		t.Fatalf("set band flag: %v", err)
	}

	if err := cmd.RunE(cmd, []string{"5"}); err != nil {
		t.Fatalf("RunE: %v", err)
	}
	if got := atomic.LoadInt32(&recallHits); got != 1 {
		t.Fatalf("recallPreset hits: got %d, want 1", got)
	}
	const want = "band=am&num=5&zone=main"
	if gotQuery != want {
		t.Errorf("recallPreset query: got %q, want %q", gotQuery, want)
	}
}

// TestRunTunerPresets_PayloadIsList exercises the list rendering: a multi-
// row response is rendered as a slice of {band, num, freq} maps.
func TestRunTunerPresets_PayloadIsList(t *testing.T) {
	resetFeatureLoader(t)
	redirectCacheDir(t)

	const deviceID = "00A0DETUNERLS"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/tuner/getPresetInfo"):
			_, _ = w.Write([]byte(`{"response_code":0,"preset_info":[{"band":"fm","number":1,"freq":8850},{"band":"fm","number":2,"freq":9020}]}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	s := newTunerTestState(t, srv, deviceID)
	cmd := newTunerPresetsCmd()
	cmd.SetContext(context.Background())
	setStateOnCmd(cmd, s)
	out := &strings.Builder{}
	cmd.SetOut(out)
	cmd.SetErr(&strings.Builder{})
	if err := cmd.Flags().Set("band", "fm"); err != nil {
		t.Fatalf("set band: %v", err)
	}
	if err := cmd.RunE(cmd, nil); err != nil {
		t.Fatalf("RunE: %v", err)
	}
	got := out.String()
	if !strings.Contains(got, "8850") || !strings.Contains(got, "9020") {
		t.Errorf("rendered output missing expected freqs: %q", got)
	}
}

// TestNormaliseBand covers the band token normalisation (case, trim,
// invalid) used by --band and resolveTunerBand.
func TestNormaliseBand(t *testing.T) {
	cases := []struct {
		in, want string
		err      bool
	}{
		{"fm", "fm", false},
		{"FM", "fm", false},
		{"  am  ", "am", false},
		{"AM", "am", false},
		{"dab", "", true}, // CLI gates to fm/am only
		{"", "", true},
		{"foo", "", true},
	}
	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			got, err := normaliseBand(tc.in)
			if tc.err && err == nil {
				t.Fatalf("expected usage error, got %q", got)
			}
			if !tc.err && err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}

// TestFormatTunerFreq verifies the human-readable rendering used by the
// status / presets payload helpers.
func TestFormatTunerFreq(t *testing.T) {
	cases := []struct {
		band string
		n    int
		want string
	}{
		{"fm", 102500, "102.50 MHz"},
		{"fm", 87500, "87.50 MHz"},
		{"am", 1530, "1530 kHz"},
	}
	for _, tc := range cases {
		t.Run(tc.want, func(t *testing.T) {
			if got := formatTunerFreq(tc.band, tc.n); got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}
