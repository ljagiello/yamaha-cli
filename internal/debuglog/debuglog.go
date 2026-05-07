// Package debuglog provides a tiny stderr tracer for YXC HTTP traffic and
// related events.
//
// Activated by the --debug flag or YAMAHA_DEBUG env var. Disabled loggers
// have a fast no-op path so production hot paths (volume, status) pay
// nothing for the calls. Output goes to a caller-supplied io.Writer
// (typically os.Stderr) so stdout stays clean and parseable when piped.
//
// One line per event, prefixed with "→" for outbound and "←" for inbound:
//
//	→ GET http://192.168.1.116/YamahaExtendedControl/v1/main/setVolume?volume=up&step=5
//	← 200 {"response_code":0}
//	→ retry
//	→ rediscover urn:schemas-upnp-org:device:MediaRenderer:1
package debuglog

import (
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
)

// responseBodyPreviewBytes caps how many bytes of the HTTP response we
// echo on a single line. 512 is enough for a YXC response (<200 bytes
// typical) while keeping `--debug` output legible on a narrow terminal.
const responseBodyPreviewBytes = 512

// IsTruthy parses a boolean-ish env var value the way the plan calls for:
// case-insensitive 1/true/yes/on → true; everything else (including empty,
// 0, false, no, off, garbage) → false.
func IsTruthy(s string) bool {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "1", "true", "yes", "on":
		return true
	}
	return false
}

// Enabled combines a CLI --debug flag with the YAMAHA_DEBUG env var. The
// flag wins outright when set; otherwise IsTruthy(env) decides. Returning
// a bool (rather than a Logger) lets callers decide whether to construct
// the trace writer at all.
func Enabled(flagSet bool, env string) bool {
	return flagSet || IsTruthy(env)
}

// Logger emits trace lines when enabled. Construct via New. The zero
// value is not usable.
type Logger struct {
	mu      sync.Mutex // serializes Fprintf calls so multi-line output doesn't interleave
	w       io.Writer
	enabled bool
}

// New returns a Logger that writes to w when enabled is true. If enabled
// is false, every method is a cheap no-op (no formatting, no writes).
//
// Pass os.Stderr from main; pass a *bytes.Buffer in tests.
func New(w io.Writer, enabled bool) *Logger {
	return &Logger{w: w, enabled: enabled}
}

// Enabled reports whether the logger will emit.
func (l *Logger) Enabled() bool {
	if l == nil {
		return false
	}
	return l.enabled
}

// Request traces an outbound HTTP request. Headers are intentionally
// omitted from the line — they're noise for the common case (User-Agent
// is constant, X-AppName/X-AppPort only on subscribe). Add a verbose mode
// later if needed.
func (l *Logger) Request(method, url string, _ http.Header) {
	if l == nil || !l.enabled {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if method == "" {
		method = "GET"
	}
	fmt.Fprintf(l.w, "→ %s %s\n", method, url)
}

// Response traces an inbound HTTP response. The body is truncated to
// responseBodyPreviewBytes; longer payloads get an ellipsis suffix so the
// caller can tell truncation happened.
func (l *Logger) Response(status int, body []byte) {
	if l == nil || !l.enabled {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()

	preview := body
	suffix := ""
	if len(preview) > responseBodyPreviewBytes {
		preview = preview[:responseBodyPreviewBytes]
		suffix = "…"
	}
	// Strip trailing newlines so the trace stays single-line per response.
	preview = trimTrailingNewlines(preview)
	fmt.Fprintf(l.w, "← %d %s%s\n", status, preview, suffix)
}

// Tracef emits a free-form one-liner. Used for retry, rediscover, and
// other non-HTTP events ("→ retry", "→ rediscover urn:…"). Add the arrow
// glyph yourself in the format string — Tracef doesn't impose one.
func (l *Logger) Tracef(format string, args ...any) {
	if l == nil || !l.enabled {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	msg := fmt.Sprintf(format, args...)
	if !strings.HasSuffix(msg, "\n") {
		msg += "\n"
	}
	_, _ = io.WriteString(l.w, msg)
}

func trimTrailingNewlines(b []byte) []byte {
	for len(b) > 0 && (b[len(b)-1] == '\n' || b[len(b)-1] == '\r') {
		b = b[:len(b)-1]
	}
	return b
}
