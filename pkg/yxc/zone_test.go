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
		if err := c.SetSurroundDecoder(context.Background(), "zone3", "auto"); err == nil {
			t.Fatal("expected error for bad zone")
		}
	})
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
