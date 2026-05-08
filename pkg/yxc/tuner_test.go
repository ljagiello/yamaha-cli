package yxc

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestGetTunerStatus exercises the FM payload shape.
func TestGetTunerStatus(t *testing.T) {
	const body = `{"response_code":0,"band":"fm","fm":{"freq":8750,"preset":1,"audio_mode":"stereo"},"am":{"freq":0,"preset":0}}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/YamahaExtendedControl/v1/tuner/getPlayInfo" {
			t.Errorf("unexpected path %q", r.URL.Path)
		}
		_, _ = w.Write([]byte(body))
	}))
	defer srv.Close()

	c := newTestClient(t, srv)
	s, err := c.GetTunerStatus(context.Background())
	if err != nil {
		t.Fatalf("GetTunerStatus: %v", err)
	}
	if s.Band != "fm" || s.FM.Freq != 8750 || s.FM.Audio != "stereo" {
		t.Fatalf("decode wrong: %+v", s)
	}
}

// TestSetTunerFreq verifies query construction across bands and rejects bad inputs.
func TestSetTunerFreq(t *testing.T) {
	cases := []struct {
		name    string
		band    string
		freq    int
		wantQ   string
		wantErr bool
	}{
		{"fm hz", "fm", 8750, "band=fm&num=8750&tuning=direct", false},
		{"am khz", "am", 530, "band=am&num=530&tuning=direct", false},
		{"bad band", "lw", 153, "", true},
		{"zero freq", "fm", 0, "", true},
		{"negative freq", "fm", -1, "", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var got string
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				got = r.URL.RawQuery
				_, _ = w.Write([]byte(`{"response_code":0}`))
			}))
			defer srv.Close()

			c := newTestClient(t, srv)
			err := c.SetTunerFreq(context.Background(), tc.band, tc.freq)
			if tc.wantErr {
				if err == nil {
					t.Fatal("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("SetTunerFreq: %v", err)
			}
			if got != tc.wantQ {
				t.Fatalf("query mismatch:\n got %s\nwant %s", got, tc.wantQ)
			}
		})
	}
}

// TestRecallTunerPreset verifies validation and URL shape.
func TestRecallTunerPreset(t *testing.T) {
	t.Run("ok", func(t *testing.T) {
		var got string
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			got = r.URL.Path + "?" + r.URL.RawQuery
			_, _ = w.Write([]byte(`{"response_code":0}`))
		}))
		defer srv.Close()

		c := newTestClient(t, srv)
		if err := c.RecallTunerPreset(context.Background(), "main", "fm", 2); err != nil {
			t.Fatalf("RecallTunerPreset: %v", err)
		}
		want := "/YamahaExtendedControl/v1/tuner/recallPreset?band=fm&num=2&zone=main"
		if got != want {
			t.Fatalf("URL mismatch:\n got %s\nwant %s", got, want)
		}
	})

	t.Run("rejects num<1", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			t.Fatal("unexpected server hit")
		}))
		defer srv.Close()
		c := newTestClient(t, srv)
		if err := c.RecallTunerPreset(context.Background(), "main", "fm", 0); err == nil {
			t.Fatal("expected error")
		}
	})

	t.Run("rejects bad band", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			t.Fatal("unexpected server hit")
		}))
		defer srv.Close()
		c := newTestClient(t, srv)
		if err := c.RecallTunerPreset(context.Background(), "main", "lw", 1); err == nil {
			t.Fatal("expected error")
		}
	})
}

// TestGetTunerPresetInfo verifies query and decode.
func TestGetTunerPresetInfo(t *testing.T) {
	const body = `{"response_code":0,"preset_info":[{"band":"fm","number":1,"freq":8850},{"band":"fm","number":2,"freq":9020}]}`
	var got string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.URL.RawQuery
		_, _ = w.Write([]byte(body))
	}))
	defer srv.Close()

	c := newTestClient(t, srv)
	p, err := c.GetTunerPresetInfo(context.Background(), "fm")
	if err != nil {
		t.Fatalf("GetTunerPresetInfo: %v", err)
	}
	if got != "band=fm" {
		t.Fatalf("query: %q", got)
	}
	if len(p.PresetInfo) != 2 || p.PresetInfo[0].Number != 1 || p.PresetInfo[0].Freq != 8850 {
		t.Fatalf("decode wrong: %+v", p)
	}
}
