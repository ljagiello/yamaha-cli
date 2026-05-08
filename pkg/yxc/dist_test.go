package yxc

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
)

// TestGetDistributionInfo verifies decoding.
func TestGetDistributionInfo(t *testing.T) {
	const body = `{"response_code":0,"group_id":"abc","role":"server","server_zone":"main","client_list":[{"ip_address":"10.0.0.2","zone":"main"},{"ip_address":"10.0.0.3"}],"build_device":"foo","audio_dropout_count":3}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/YamahaExtendedControl/v1/dist/getDistributionInfo" {
			t.Errorf("unexpected path %q", r.URL.Path)
		}
		_, _ = w.Write([]byte(body))
	}))
	defer srv.Close()

	c := newTestClient(t, srv)
	d, err := c.GetDistributionInfo(context.Background())
	if err != nil {
		t.Fatalf("GetDistributionInfo: %v", err)
	}
	if d.GroupID != "abc" || d.Role != "server" || len(d.ClientList) != 2 {
		t.Fatalf("decode wrong: %+v", d)
	}
	if d.ClientList[0].IPAddress != "10.0.0.2" || d.ClientList[0].Zone != "main" {
		t.Fatalf("client[0] wrong: %+v", d.ClientList[0])
	}
	if d.AudioDropoutCount != 3 {
		t.Fatalf("dropout: %d", d.AudioDropoutCount)
	}
}

// TestSetServerInfo verifies the multi-client query construction.
func TestSetServerInfo(t *testing.T) {
	t.Run("add with clients", func(t *testing.T) {
		var got url.Values
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			got = r.URL.Query()
			_, _ = w.Write([]byte(`{"response_code":0}`))
		}))
		defer srv.Close()

		c := newTestClient(t, srv)
		err := c.SetServerInfo(context.Background(), ServerInfo{
			GroupID:    "g1",
			Type:       "add",
			Zone:       "main",
			ClientList: []string{"10.0.0.2", "10.0.0.3"},
		})
		if err != nil {
			t.Fatalf("SetServerInfo: %v", err)
		}
		if got.Get("group_id") != "g1" || got.Get("type") != "add" || got.Get("zone") != "main" {
			t.Fatalf("base params wrong: %v", got)
		}
		if got.Get("client_list[0].ip_address") != "10.0.0.2" {
			t.Fatalf("client[0]: %v", got)
		}
		if got.Get("client_list[1].ip_address") != "10.0.0.3" {
			t.Fatalf("client[1]: %v", got)
		}
	})

	t.Run("rejects empty group", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			t.Fatal("unexpected server hit")
		}))
		defer srv.Close()
		c := newTestClient(t, srv)
		if err := c.SetServerInfo(context.Background(), ServerInfo{Type: "add"}); err == nil {
			t.Fatal("expected error")
		}
	})

	t.Run("rejects bad type", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			t.Fatal("unexpected server hit")
		}))
		defer srv.Close()
		c := newTestClient(t, srv)
		if err := c.SetServerInfo(context.Background(), ServerInfo{GroupID: "g", Type: "weird"}); err == nil {
			t.Fatal("expected error")
		}
	})
}

// TestSetClientInfo verifies request shape and validation.
func TestSetClientInfo(t *testing.T) {
	t.Run("ok", func(t *testing.T) {
		var got url.Values
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			got = r.URL.Query()
			_, _ = w.Write([]byte(`{"response_code":0}`))
		}))
		defer srv.Close()

		c := newTestClient(t, srv)
		err := c.SetClientInfo(context.Background(), "g1", "main", "10.0.0.1")
		if err != nil {
			t.Fatalf("SetClientInfo: %v", err)
		}
		if got.Get("group_id") != "g1" || got.Get("zone") != "main" || got.Get("server_ip_address") != "10.0.0.1" {
			t.Fatalf("params wrong: %v", got)
		}
	})

	t.Run("rejects empty group", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			t.Fatal("unexpected server hit")
		}))
		defer srv.Close()
		c := newTestClient(t, srv)
		if err := c.SetClientInfo(context.Background(), "", "main", "10.0.0.1"); err == nil {
			t.Fatal("expected error")
		}
	})

	t.Run("rejects empty server", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			t.Fatal("unexpected server hit")
		}))
		defer srv.Close()
		c := newTestClient(t, srv)
		if err := c.SetClientInfo(context.Background(), "g", "main", ""); err == nil {
			t.Fatal("expected error")
		}
	})
}

// TestStartStopDistribution verifies the bare URL shapes.
func TestStartStopDistribution(t *testing.T) {
	t.Run("start", func(t *testing.T) {
		var got string
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			got = r.URL.Path + "?" + r.URL.RawQuery
			_, _ = w.Write([]byte(`{"response_code":0}`))
		}))
		defer srv.Close()
		c := newTestClient(t, srv)
		if err := c.StartDistribution(context.Background(), 0); err != nil {
			t.Fatalf("StartDistribution: %v", err)
		}
		if got != "/YamahaExtendedControl/v1/dist/startDistribution?num=0" {
			t.Fatalf("URL: %s", got)
		}
	})
	t.Run("start rejects negative", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			t.Fatal("unexpected server hit")
		}))
		defer srv.Close()
		c := newTestClient(t, srv)
		if err := c.StartDistribution(context.Background(), -1); err == nil {
			t.Fatal("expected error")
		}
	})
	t.Run("stop", func(t *testing.T) {
		var got string
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			got = r.URL.Path
			_, _ = w.Write([]byte(`{"response_code":0}`))
		}))
		defer srv.Close()
		c := newTestClient(t, srv)
		if err := c.StopDistribution(context.Background()); err != nil {
			t.Fatalf("StopDistribution: %v", err)
		}
		if got != "/YamahaExtendedControl/v1/dist/stopDistribution" {
			t.Fatalf("URL: %s", got)
		}
	})
}
