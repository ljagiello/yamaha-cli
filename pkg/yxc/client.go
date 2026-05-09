package yxc

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// Version is set at link time by the calling binary, e.g.
//
//	go build -ldflags='-X github.com/ljagiello/yamaha-cli/pkg/yxc.Version=1.2.3'
//
// It is included in the User-Agent header.
var Version = "dev"

// Default timings for transport behaviour.
const (
	defaultTimeout     = 5 * time.Second
	defaultRetryWait   = 250 * time.Millisecond
	defaultRateLimit   = 100 * time.Millisecond
	apiPathPrefix      = "/YamahaExtendedControl/v1/"
	headerUserAgentFmt = "yamaha-cli/%s"
)

// Client is a YXC HTTP client.
//
// Client is safe for concurrent use: methods may be called from multiple
// goroutines simultaneously. Internal state is read-only after construction
// except for an intra-process rate-limit timestamp, which is guarded by a
// mutex.
//
// Construct with New. Configure with Option values.
type Client struct {
	baseURL    *url.URL
	httpClient *http.Client
	userAgent  string
	eventPort  int // 0 == disabled

	mu       sync.Mutex
	lastCall time.Time
}

// Option configures a Client.
type Option func(*Client)

// WithHTTPClient overrides the http.Client used for all requests.
//
// If the supplied client has a zero Timeout, the previously-configured
// timeout (e.g. the New() default, or one set by WithTimeout earlier in
// the option list) is preserved. This makes WithHTTPClient + WithTimeout
// composition order-independent for the common case.
//
// The supplied *http.Client is copied by value before any modification,
// so callers can pass a shared client without worrying about side
// effects from this option. Note: callers that genuinely want "no
// timeout" cannot express that via WithHTTPClient alone — use a Client
// constructed with no WithTimeout (or one whose default the caller
// overrides afterwards).
func WithHTTPClient(hc *http.Client) Option {
	return func(c *Client) {
		if hc == nil {
			return
		}
		clone := *hc
		if clone.Timeout == 0 && c.httpClient != nil && c.httpClient.Timeout != 0 {
			clone.Timeout = c.httpClient.Timeout
		}
		c.httpClient = &clone
	}
}

// WithTimeout sets the http.Client.Timeout. Default: 5s.
func WithTimeout(d time.Duration) Option {
	return func(c *Client) {
		if d > 0 {
			c.httpClient.Timeout = d
		}
	}
}

// WithUserAgent overrides the User-Agent header. Default: "yamaha-cli/<Version>".
func WithUserAgent(ua string) Option {
	return func(c *Client) {
		if ua != "" {
			c.userAgent = ua
		}
	}
}

// WithEventPort sets the local UDP port the receiver will use to push
// events. When set, EventDo requests will include `X-AppName: MusicCast`
// and `X-AppPort: <port>` headers.
func WithEventPort(port int) Option {
	return func(c *Client) {
		c.eventPort = port
	}
}

// New constructs a Client targeting the given base URL.
//
// baseURL may be a host ("192.168.1.116"), a host:port, or a fully-qualified
// URL ("http://192.168.1.116/"). Schemes other than http are rejected.
func New(baseURL string, opts ...Option) (*Client, error) {
	u, err := normaliseBaseURL(baseURL)
	if err != nil {
		return nil, err
	}

	c := &Client{
		baseURL: u,
		httpClient: &http.Client{
			Timeout: defaultTimeout,
		},
		userAgent: fmt.Sprintf(headerUserAgentFmt, Version),
	}
	for _, o := range opts {
		o(c)
	}
	return c, nil
}

// BaseURL returns the resolved base URL the client targets.
func (c *Client) BaseURL() string {
	return c.baseURL.String()
}

// Do issues a YXC GET for the given method (e.g. "system/getDeviceInfo",
// "main/setVolume") with the given query parameters, and returns the raw
// JSON body on success. On YXC `response_code != 0` it returns a typed
// *Error.
func (c *Client) Do(ctx context.Context, method string, params url.Values) (json.RawMessage, error) {
	return c.do(ctx, method, params, false)
}

// EventDo issues a YXC GET that subscribes to push events. It adds the
// X-AppName / X-AppPort headers required by the receiver to direct UDP
// event traffic back to the caller. Requires WithEventPort to have been
// supplied.
func (c *Client) EventDo(ctx context.Context, method string, params url.Values) (json.RawMessage, error) {
	if c.eventPort == 0 {
		return nil, errors.New("yxc: EventDo requires WithEventPort to be configured")
	}
	return c.do(ctx, method, params, true)
}

// do is the internal request engine. It implements the rate-limit, single
// retry on transient errors, response_code parsing, and header policy.
func (c *Client) do(ctx context.Context, method string, params url.Values, eventSubscription bool) (json.RawMessage, error) {
	if ctx == nil {
		return nil, errors.New("yxc: nil context")
	}
	method = strings.TrimPrefix(method, "/")

	// Build the URL once; same for both attempts.
	reqURL := *c.baseURL
	reqURL.Path = apiPathPrefix + method
	if len(params) > 0 {
		reqURL.RawQuery = params.Encode()
	}

	// Rate-limit before each *attempt*. The retry path also waits.
	c.rateLimitWait(ctx)
	body, err := c.doOnce(ctx, reqURL.String(), method, eventSubscription)
	c.mark()

	if err != nil && shouldRetry(ctx, err) {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(defaultRetryWait):
		}
		c.rateLimitWait(ctx)
		body, err = c.doOnce(ctx, reqURL.String(), method, eventSubscription)
		c.mark()
	}
	return body, err
}

// doOnce performs exactly one HTTP attempt with no retry logic.
func (c *Client) doOnce(ctx context.Context, fullURL, method string, eventSubscription bool) (json.RawMessage, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, fullURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", c.userAgent)
	if eventSubscription {
		req.Header.Set("X-AppName", "MusicCast")
		req.Header.Set("X-AppPort", fmt.Sprintf("%d", c.eventPort))
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		// Distinguish ctx cancellation from genuine transport failures.
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, ctxErr
		}
		if isTransientNetErr(err) {
			return nil, &transportError{err: err}
		}
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		// Drain a bit so the connection can be reused, but cap to avoid
		// reading multi-MB error pages.
		_, _ = io.CopyN(io.Discard, resp.Body, 1<<14)
		return nil, &httpStatusError{Status: resp.StatusCode, Method: method}
	}

	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		// EOF mid-body counts as transient — the device may have been
		// momentarily unreachable.
		if isTransientNetErr(err) {
			return nil, &transportError{err: err}
		}
		return nil, err
	}

	// Parse only response_code to decide success vs failure; full
	// payload typing is the caller's responsibility.
	var head struct {
		ResponseCode int `json:"response_code"`
	}
	if err := json.Unmarshal(raw, &head); err != nil {
		return nil, fmt.Errorf("yxc: %s: invalid JSON response: %w", method, err)
	}
	if head.ResponseCode != codeOK {
		return nil, newYXCError(head.ResponseCode, method)
	}
	return json.RawMessage(raw), nil
}

// rateLimitWait blocks until at least defaultRateLimit has elapsed since
// the last completed request on this Client, or ctx is cancelled. The
// caller is responsible for calling mark() once the request has completed
// — measuring the gap from completion (rather than from request-start)
// keeps the on-the-wire spacing reliable when keep-alive lets a second
// request reach the receiver faster than the first.
func (c *Client) rateLimitWait(ctx context.Context) {
	c.mu.Lock()
	wait := defaultRateLimit - time.Since(c.lastCall)
	c.mu.Unlock()
	if wait <= 0 {
		return
	}
	select {
	case <-ctx.Done():
		// Caller will see ctx.Err() on the request itself.
	case <-time.After(wait):
	}
}

func (c *Client) mark() {
	c.mu.Lock()
	c.lastCall = time.Now()
	c.mu.Unlock()
}

// shouldRetry decides whether to attempt a single retry.
//
// We retry on:
//   - transportError (wrapping net.OpError, ECONNRESET, io.ErrUnexpectedEOF)
//   - context.DeadlineExceeded (per-request timeout fired; one more chance)
//
// We do NOT retry on:
//   - YXC *Error (device-side decision)
//   - httpStatusError (4xx/5xx — device replied)
//   - context.Canceled (user pressed Ctrl-C)
//   - any other error
func shouldRetry(ctx context.Context, err error) bool {
	if err == nil {
		return false
	}
	// Caller-cancelled: never retry.
	if errors.Is(err, context.Canceled) {
		return false
	}
	// Per-call deadline expired: retry once. The retry itself uses the
	// same context, so if it's still expired it'll fail again immediately.
	if errors.Is(err, context.DeadlineExceeded) {
		// But if the *parent* ctx is gone, don't retry.
		if ctx.Err() != nil && !errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return false
		}
		return true
	}
	if IsTransport(err) {
		return true
	}
	return false
}

// isTransientNetErr returns true for network-layer errors worth retrying:
// connection refused, no route to host, ECONNRESET, unexpected EOF, etc.
//
// We are deliberately permissive here: the only errors we *don't* want to
// retry are application-level (YXC response_code, HTTP non-2xx) which take
// different code paths above.
func isTransientNetErr(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, io.EOF) {
		return true
	}
	var ne net.Error
	if errors.As(err, &ne) {
		return true
	}
	var oe *net.OpError
	if errors.As(err, &oe) {
		return true
	}
	// Plain "connection reset by peer" can surface as a syscall.Errno
	// wrapped in an *os.SyscallError inside *net.OpError; the As checks
	// above catch it. Match a substring as a final safety net for
	// stdlib quirks.
	if s := err.Error(); strings.Contains(s, "connection reset") ||
		strings.Contains(s, "broken pipe") {
		return true
	}
	return false
}

// normaliseBaseURL accepts "host", "host:port" or a full URL and returns
// a *url.URL with scheme=http and no trailing slash on Path.
func normaliseBaseURL(s string) (*url.URL, error) {
	if s == "" {
		return nil, errors.New("yxc: empty base URL")
	}
	if !strings.Contains(s, "://") {
		s = "http://" + s
	}
	u, err := url.Parse(s)
	if err != nil {
		return nil, fmt.Errorf("yxc: invalid base URL %q: %w", s, err)
	}
	if u.Scheme != "http" {
		return nil, fmt.Errorf("yxc: unsupported scheme %q (only http is supported)", u.Scheme)
	}
	if u.Host == "" {
		return nil, fmt.Errorf("yxc: base URL %q has no host", s)
	}
	u.Path = strings.TrimRight(u.Path, "/")
	u.RawQuery = ""
	u.Fragment = ""
	return u, nil
}
