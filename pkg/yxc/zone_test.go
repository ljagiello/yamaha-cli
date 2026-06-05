package yxc

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestSetSurroundDecoder verifies the URL shape and basic validation.
func TestSetSurroundDecoder(t *testing.T) {
	t.Run("ok", func(t *testing.T) {
		var got string
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			got = r.URL.Path + "?" + r.URL.RawQuery
			_, _ = w.Write([]byte(`{"response_code":0}`))
		}))
		defer srv.Close()

		c := newTestClient(t, srv)
		if err := c.SetSurroundDecoder(context.Background(), "main", "auto"); err != nil {
			t.Fatalf("SetSurroundDecoder: %v", err)
		}
		want := "/YamahaExtendedControl/v1/main/setSurroundDecoderType?type=auto"
		if got != want {
			t.Fatalf("URL mismatch:\n got %s\nwant %s", got, want)
		}
	})

	t.Run("rejects empty", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			t.Fatal("unexpected server hit")
		}))
		defer srv.Close()
		c := newTestClient(t, srv)
		if err := c.SetSurroundDecoder(context.Background(), "main", ""); err == nil {
			t.Fatal("expected error for empty decoder")
		}
	})

	t.Run("rejects bad zone", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			t.Fatal("unexpected server hit")
		}))
		defer srv.Close()
		c := newTestClient(t, srv)
		// zone9 is not one of the four canonical zones (main/zone2/zone3/
		// zone4), so validZone rejects it before any wire call.
		if err := c.SetSurroundDecoder(context.Background(), "zone9", "auto"); err == nil {
			t.Fatal("expected error for bad zone")
		}
	})
}

// TestValidZone_AcceptsAllFourZones proves zone3/zone4 are now routed to
// the wire (the unblock for AVENTAGE / RX-A receivers) and that case is
// normalised, while a non-canonical token is still rejected before any
// network call.
func TestValidZone_AcceptsAllFourZones(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want string
	}{
		{"main", "main"},
		{"zone2", "zone2"},
		{"ZONE3", "zone3"},
		{"Zone4", "zone4"},
	} {
		var got string
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			got = r.URL.Path
			_, _ = w.Write([]byte(`{"response_code":0}`))
		}))
		c := newTestClient(t, srv)
		if err := c.SetPower(context.Background(), tc.in, "on"); err != nil {
			srv.Close()
			t.Fatalf("SetPower(%q): %v", tc.in, err)
		}
		want := "/YamahaExtendedControl/v1/" + tc.want + "/setPower"
		if got != want {
			srv.Close()
			t.Fatalf("SetPower(%q) path = %s, want %s", tc.in, got, want)
		}
		srv.Close()
	}
}

// TestSetZoneEnable_Switches verifies the URL shape + enable param for the
// boolean DSP switches, including the zone routing and bool encoding.
func TestSetZoneEnable_Switches(t *testing.T) {
	cases := []struct {
		name    string
		call    func(c *Client) error
		on      bool
		wantURL string
	}{
		{"pure-direct on", func(c *Client) error { return c.SetPureDirect(context.Background(), "main", true) }, true,
			"/YamahaExtendedControl/v1/main/setPureDirect?enable=true"},
		{"enhancer off", func(c *Client) error { return c.SetEnhancer(context.Background(), "zone2", false) }, false,
			"/YamahaExtendedControl/v1/zone2/setEnhancer?enable=false"},
		{"extra-bass on", func(c *Client) error { return c.SetExtraBass(context.Background(), "main", true) }, true,
			"/YamahaExtendedControl/v1/main/setExtraBass?enable=true"},
		{"adaptive-drc on", func(c *Client) error { return c.SetAdaptiveDRC(context.Background(), "main", true) }, true,
			"/YamahaExtendedControl/v1/main/setAdaptiveDrc?enable=true"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var got string
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				got = r.URL.Path + "?" + r.URL.RawQuery
				_, _ = w.Write([]byte(`{"response_code":0}`))
			}))
			defer srv.Close()
			c := newTestClient(t, srv)
			if err := tc.call(c); err != nil {
				t.Fatalf("call: %v", err)
			}
			if got != tc.wantURL {
				t.Fatalf("URL mismatch:\n got %s\nwant %s", got, tc.wantURL)
			}
		})
	}
}

// TestRecallScene verifies URL shape and num >= 1 validation.
func TestRecallScene(t *testing.T) {
	t.Run("ok", func(t *testing.T) {
		var got string
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			got = r.URL.Path + "?" + r.URL.RawQuery
			_, _ = w.Write([]byte(`{"response_code":0}`))
		}))
		defer srv.Close()

		c := newTestClient(t, srv)
		if err := c.RecallScene(context.Background(), "main", 3); err != nil {
			t.Fatalf("RecallScene: %v", err)
		}
		want := "/YamahaExtendedControl/v1/main/recallScene?num=3"
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
		if err := c.RecallScene(context.Background(), "main", 0); err == nil {
			t.Fatal("expected error for num=0")
		}
	})
}

// TestSetToneControl exercises partial updates and the no-op rejection.
func TestSetToneControl(t *testing.T) {
	cases := []struct {
		name    string
		arg     ToneControlArg
		wantQ   string
		wantErr bool
	}{
		{"mode only", ToneControlArg{Mode: "manual"}, "mode=manual", false},
		{"bass only", ToneControlArg{Bass: IntPtr(3)}, "bass=3", false},
		{"treble only", ToneControlArg{Treble: IntPtr(-2)}, "treble=-2", false},
		{"mode+bass+treble", ToneControlArg{Mode: "manual", Bass: IntPtr(0), Treble: IntPtr(5)}, "bass=0&mode=manual&treble=5", false},
		{"empty rejected", ToneControlArg{}, "", true},
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
			err := c.SetToneControl(context.Background(), "main", tc.arg)
			if tc.wantErr {
				if err == nil {
					t.Fatal("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("SetToneControl: %v", err)
			}
			if got != tc.wantQ {
				t.Fatalf("query mismatch:\n got %s\nwant %s", got, tc.wantQ)
			}
		})
	}
}
