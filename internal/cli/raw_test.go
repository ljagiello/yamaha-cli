package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"

	"github.com/ljagiello/yamaha-cli/internal/config"
	"github.com/ljagiello/yamaha-cli/pkg/yxc"
)

// newRawState builds a minimal *state suitable for the raw command. The
// device URL is parsed and pinned to the test server. Anonymous mode
// (alias=="") so runWithRediscover skips the rediscovery path on errors.
func newRawState(t *testing.T, baseURL string) *state {
	t.Helper()
	u, err := url.Parse(baseURL)
	if err != nil {
		t.Fatalf("parse url: %v", err)
	}
	host := u.Host
	if host == "" {
		host = baseURL
	}
	c, err := yxc.New(u.Scheme + "://" + host)
	if err != nil {
		t.Fatalf("yxc.New: %v", err)
	}
	return &state{
		device: config.Device{Host: host},
		zone:   "main",
		client: c,
	}
}

// rawHandler captures the path and query of the inbound HTTP request
// and replies with a canned JSON body.
type rawHandler struct {
	mu    sync.Mutex
	hits  int
	path  string
	query url.Values
	body  string
}

func newRawHandler(body string) *rawHandler {
	return &rawHandler{body: body}
}

func (h *rawHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	h.mu.Lock()
	h.hits++
	h.path = r.URL.Path
	h.query = r.URL.Query()
	h.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write([]byte(h.body))
}

// TestRaw_PathAndQueryEncoding verifies that `raw foo/bar k=v k=v2`
// produces a request to /YamahaExtendedControl/v1/foo/bar with both
// values for k attached. This is the entire promise of the command.
func TestRaw_PathAndQueryEncoding(t *testing.T) {
	t.Parallel()
	h := newRawHandler(`{"response_code":0,"echo":"ok"}`)
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)

	cmd := newRawCmd()
	var stdout bytes.Buffer
	cmd.SetOut(&stdout)
	cmd.SetErr(io.Discard)
	cmd.SetArgs([]string{"foo/bar", "k=v", "k=v2", "other=hello world"})

	s := newRawState(t, srv.URL)
	ctx := context.WithValue(context.Background(), stateKey, s)
	cmd.SetContext(ctx)

	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute: %v", err)
	}

	h.mu.Lock()
	defer h.mu.Unlock()
	if want := "/YamahaExtendedControl/v1/foo/bar"; h.path != want {
		t.Errorf("path: got %q want %q", h.path, want)
	}
	if got := h.query["k"]; len(got) != 2 || got[0] != "v" || got[1] != "v2" {
		t.Errorf("query[k]: got %v want [v v2]", got)
	}
	if got := h.query.Get("other"); got != "hello world" {
		t.Errorf("query[other]: got %q want \"hello world\"", got)
	}

	// Output should be JSON since we're not on a TTY in tests.
	var decoded map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &decoded); err != nil {
		t.Fatalf("stdout not JSON: %v\noutput=%q", err, stdout.String())
	}
	if decoded["echo"] != "ok" {
		t.Errorf("decoded echo: got %v want ok", decoded["echo"])
	}
}

// TestRaw_BadKVPair verifies that a positional arg without '=' is a
// usage error (exit 2).
func TestRaw_BadKVPair(t *testing.T) {
	t.Parallel()
	cmd := newRawCmd()
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	cmd.SetArgs([]string{"foo/bar", "novalue"})

	// No state needed — the validator runs before any HTTP call. But
	// stateFromCmd is checked first, so wire a stub with a dialable
	// (but unused) URL.
	s := newRawState(t, "http://127.0.0.1:1")
	cmd.SetContext(context.WithValue(context.Background(), stateKey, s))

	err := cmd.Execute()
	if err == nil {
		t.Fatal("expected error for missing '='")
	}
	if !strings.Contains(err.Error(), "key=value") {
		t.Errorf("error message should mention key=value: %v", err)
	}
	if got := ErrorExitCode(err); got != 2 {
		t.Errorf("exit code: got %d want 2", got)
	}
}

// TestParseKVPairs covers the parsing primitive in isolation.
func TestParseKVPairs(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		args    []string
		want    url.Values
		wantErr bool
	}{
		{
			name: "empty",
			args: nil,
			want: url.Values{},
		},
		{
			name: "single",
			args: []string{"a=1"},
			want: url.Values{"a": []string{"1"}},
		},
		{
			name: "repeated",
			args: []string{"a=1", "a=2"},
			want: url.Values{"a": []string{"1", "2"}},
		},
		{
			name: "preserves equals in value",
			args: []string{"k=a=b=c"},
			want: url.Values{"k": []string{"a=b=c"}},
		},
		{
			name:    "missing equals",
			args:    []string{"abc"},
			wantErr: true,
		},
		{
			name:    "leading equals",
			args:    []string{"=v"},
			wantErr: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseKVPairs(tc.args)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected error for %v", tc.args)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got.Encode() != tc.want.Encode() {
				t.Errorf("got %v want %v", got, tc.want)
			}
		})
	}
}
