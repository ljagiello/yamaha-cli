package yxc

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestSetPlayback covers the happy path and validation of the playback verb.
func TestSetPlayback(t *testing.T) {
	cases := []struct {
		p       Playback
		wantErr bool
		wantQ   string
	}{
		{PlaybackPlay, false, "playback=play"},
		{PlaybackStop, false, "playback=stop"},
		{PlaybackFastForwardStart, false, "playback=fast_forward_start"},
		{Playback("bogus"), true, ""},
		{Playback(""), true, ""},
	}
	for _, tc := range cases {
		t.Run(string(tc.p), func(t *testing.T) {
			var got string
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				got = r.URL.RawQuery
				_, _ = w.Write([]byte(`{"response_code":0}`))
			}))
			defer srv.Close()

			c := newTestClient(t, srv)
			err := c.SetPlayback(context.Background(), tc.p)
			if tc.wantErr {
				if err == nil {
					t.Fatal("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("SetPlayback: %v", err)
			}
			if got != tc.wantQ {
				t.Fatalf("query mismatch:\n got %s\nwant %s", got, tc.wantQ)
			}
		})
	}
}

// TestGetPlayInfo verifies decoding of the typed PlayInfo struct.
func TestGetPlayInfo(t *testing.T) {
	const body = `{"response_code":0,"input":"server","playback":"play","repeat":"off","shuffle":"on","play_time":42,"total_time":300,"artist":"A","album":"B","track":"T","albumart_url":"/u","albumart_id":7}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/YamahaExtendedControl/v1/netusb/getPlayInfo" {
			t.Errorf("unexpected path %q", r.URL.Path)
		}
		_, _ = w.Write([]byte(body))
	}))
	defer srv.Close()

	c := newTestClient(t, srv)
	pi, err := c.GetPlayInfo(context.Background())
	if err != nil {
		t.Fatalf("GetPlayInfo: %v", err)
	}
	if pi.Input != "server" || pi.Playback != "play" || pi.Repeat != "off" || pi.Shuffle != "on" {
		t.Fatalf("PlayInfo fields wrong: %+v", pi)
	}
	if pi.PlayTime != 42 || pi.TotalTime != 300 {
		t.Fatalf("PlayInfo timings wrong: %+v", pi)
	}
	if pi.Artist != "A" || pi.Album != "B" || pi.Track != "T" || pi.AlbumArtURL != "/u" || pi.AlbumArtID != 7 {
		t.Fatalf("PlayInfo metadata wrong: %+v", pi)
	}
}

// TestRecallNetUSBPreset verifies request shape and validation.
func TestRecallNetUSBPreset(t *testing.T) {
	t.Run("ok", func(t *testing.T) {
		var got string
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			got = r.URL.Path + "?" + r.URL.RawQuery
			_, _ = w.Write([]byte(`{"response_code":0}`))
		}))
		defer srv.Close()

		c := newTestClient(t, srv)
		if err := c.RecallNetUSBPreset(context.Background(), "main", 4); err != nil {
			t.Fatalf("RecallNetUSBPreset: %v", err)
		}
		want := "/YamahaExtendedControl/v1/netusb/recallPreset?num=4&zone=main"
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
		if err := c.RecallNetUSBPreset(context.Background(), "main", 0); err == nil {
			t.Fatal("expected error for num=0")
		}
	})
}

// TestGetListInfo verifies query construction and decoding.
func TestGetListInfo(t *testing.T) {
	const body = `{"response_code":0,"menu_name":"USB","max_line":8,"index":0,"total":2,"menu_layer":1,"playing_index":0,"list_info":[{"text":"a","attribute":1},{"text":"b","thumbnail":"http://x"}]}`
	var got string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.URL.RawQuery
		_, _ = w.Write([]byte(body))
	}))
	defer srv.Close()

	c := newTestClient(t, srv)
	li, err := c.GetListInfo(context.Background(), "usb", 0, 8, "en")
	if err != nil {
		t.Fatalf("GetListInfo: %v", err)
	}
	want := "index=0&input=usb&lang=en&size=8"
	if got != want {
		t.Fatalf("query mismatch:\n got %s\nwant %s", got, want)
	}
	if li.MenuName != "USB" || li.Total != 2 || len(li.ListInfo) != 2 {
		t.Fatalf("ListInfo decode wrong: %+v", li)
	}
	if li.ListInfo[0].Text != "a" || li.ListInfo[0].Attribute != 1 {
		t.Fatalf("first item wrong: %+v", li.ListInfo[0])
	}
	if li.ListInfo[1].Thumbnail != "http://x" {
		t.Fatalf("second item thumbnail wrong: %+v", li.ListInfo[1])
	}
}

// TestGetListInfo_Validation rejects empty input and nonsensical paging.
func TestGetListInfo_Validation(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		t.Fatal("unexpected server hit")
	}))
	defer srv.Close()
	c := newTestClient(t, srv)

	if _, err := c.GetListInfo(context.Background(), "", 0, 8, "en"); err == nil {
		t.Error("expected error for empty input")
	}
	if _, err := c.GetListInfo(context.Background(), "usb", 0, 0, "en"); err == nil {
		t.Error("expected error for size=0")
	}
	if _, err := c.GetListInfo(context.Background(), "usb", -1, 8, "en"); err == nil {
		t.Error("expected error for index<0")
	}
}

// TestSetPlaybackToggles verifies the no-arg toggleRepeat / toggleShuffle.
func TestSetPlaybackToggles(t *testing.T) {
	t.Run("repeat", func(t *testing.T) {
		var got string
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			got = r.URL.Path
			_, _ = w.Write([]byte(`{"response_code":0}`))
		}))
		defer srv.Close()
		c := newTestClient(t, srv)
		if err := c.SetPlaybackRepeat(context.Background()); err != nil {
			t.Fatalf("SetPlaybackRepeat: %v", err)
		}
		if got != "/YamahaExtendedControl/v1/netusb/toggleRepeat" {
			t.Fatalf("path: %q", got)
		}
	})
	t.Run("shuffle", func(t *testing.T) {
		var got string
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			got = r.URL.Path
			_, _ = w.Write([]byte(`{"response_code":0}`))
		}))
		defer srv.Close()
		c := newTestClient(t, srv)
		if err := c.SetPlaybackShuffle(context.Background()); err != nil {
			t.Fatalf("SetPlaybackShuffle: %v", err)
		}
		if got != "/YamahaExtendedControl/v1/netusb/toggleShuffle" {
			t.Fatalf("path: %q", got)
		}
	})
}

// TestGetPresetInfo verifies decoding.
func TestGetPresetInfo(t *testing.T) {
	const body = `{"response_code":0,"preset_info":[{"input":"server","text":"BBC"},{"input":"net_radio","text":"Radio4"}]}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(body))
	}))
	defer srv.Close()

	c := newTestClient(t, srv)
	p, err := c.GetPresetInfo(context.Background())
	if err != nil {
		t.Fatalf("GetPresetInfo: %v", err)
	}
	if len(p.PresetInfo) != 2 {
		t.Fatalf("expected 2 presets, got %d", len(p.PresetInfo))
	}
	if p.PresetInfo[0].Input != "server" || p.PresetInfo[1].Text != "Radio4" {
		t.Fatalf("preset decode wrong: %+v", p)
	}
}
