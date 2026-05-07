package cli

import (
	"bytes"
	"io"
	"net/http"
	"time"

	"github.com/ljagiello/yamaha-cli/internal/debuglog"
)

// tracingTransport wraps an http.RoundTripper to emit one debug line per
// request and one per response (with a body preview). It is installed on
// the *yxc.Client when --debug / YAMAHA_DEBUG is enabled.
type tracingTransport struct {
	base http.RoundTripper
	log  *debuglog.Logger
}

func newDebugHTTPClient(timeout time.Duration, log *debuglog.Logger) *http.Client {
	return &http.Client{
		Timeout: timeout,
		Transport: &tracingTransport{
			base: http.DefaultTransport,
			log:  log,
		},
	}
}

func (t *tracingTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if t.log != nil && t.log.Enabled() {
		t.log.Request(req.Method, req.URL.String(), req.Header)
	}
	resp, err := t.base.RoundTrip(req)
	if err != nil {
		// The yxc client converts transient errors into transportError
		// and may retry; we can't tell from here. Just log the failure.
		if t.log != nil && t.log.Enabled() {
			t.log.Tracef("← err %s", err.Error())
		}
		return nil, err
	}

	// Read the body, log a preview, then hand a re-readable body back to
	// the caller so the yxc client can JSON-decode it normally.
	if t.log != nil && t.log.Enabled() && resp.Body != nil {
		body, rerr := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if rerr != nil {
			t.log.Tracef("← read err %s", rerr.Error())
			// Best-effort recovery: leave an empty body so the caller
			// gets a proper unmarshal error rather than a panic.
			resp.Body = io.NopCloser(bytes.NewReader(nil))
			return resp, nil
		}
		t.log.Response(resp.StatusCode, body)
		resp.Body = io.NopCloser(bytes.NewReader(body))
	}
	return resp, nil
}
