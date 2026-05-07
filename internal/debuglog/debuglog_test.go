package debuglog

import (
	"bytes"
	"net/http"
	"strings"
	"testing"
)

func TestIsTruthy(t *testing.T) {
	tests := []struct {
		in   string
		want bool
	}{
		// Truthy values (case-insensitive).
		{"1", true},
		{"true", true},
		{"TRUE", true},
		{"True", true},
		{"yes", true},
		{"YES", true},
		{"on", true},
		{"ON", true},
		{"  on  ", true}, // surrounding whitespace tolerated

		// Falsy values.
		{"", false},
		{"0", false},
		{"false", false},
		{"FALSE", false},
		{"no", false},
		{"off", false},
		{"OFF", false},

		// Garbage falls through to false (not "any non-empty == true").
		{"yeah", false},
		{"2", false},
		{"enabled", false},
		{"\t", false},
	}
	for _, tt := range tests {
		if got := IsTruthy(tt.in); got != tt.want {
			t.Errorf("IsTruthy(%q) = %v, want %v", tt.in, got, tt.want)
		}
	}
}

func TestEnabled(t *testing.T) {
	tests := []struct {
		name    string
		flagSet bool
		env     string
		want    bool
	}{
		{"flag wins over empty env", true, "", true},
		{"flag wins over falsy env", true, "0", true},
		{"flag off + truthy env enables", false, "yes", true},
		{"flag off + falsy env disables", false, "no", false},
		{"flag off + empty env disables", false, "", false},
		{"flag off + garbage env disables", false, "maybe", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Enabled(tt.flagSet, tt.env); got != tt.want {
				t.Errorf("Enabled(%v,%q) = %v, want %v", tt.flagSet, tt.env, got, tt.want)
			}
		})
	}
}

func TestLoggerNoOpWhenDisabled(t *testing.T) {
	var buf bytes.Buffer
	l := New(&buf, false)
	if l.Enabled() {
		t.Error("disabled logger reports Enabled() == true")
	}
	l.Request("GET", "http://example/x", nil)
	l.Response(200, []byte("hello"))
	l.Tracef("→ retry")
	if buf.Len() != 0 {
		t.Errorf("disabled logger wrote %q, expected nothing", buf.String())
	}
}

func TestLoggerNilSafe(t *testing.T) {
	// A nil *Logger should still be safe to call (defensive: cli might
	// thread one through before construction).
	var l *Logger
	if l.Enabled() {
		t.Error("nil logger reports Enabled() == true")
	}
	l.Request("GET", "http://example/x", nil)
	l.Response(200, []byte("hi"))
	l.Tracef("→ retry")
}

func TestLoggerRequest(t *testing.T) {
	var buf bytes.Buffer
	l := New(&buf, true)
	l.Request("GET", "http://192.168.1.116/YamahaExtendedControl/v1/main/getStatus", http.Header{})

	out := buf.String()
	if !strings.HasPrefix(out, "→ GET ") {
		t.Errorf("want '→ GET ' prefix, got %q", out)
	}
	if !strings.Contains(out, "/v1/main/getStatus") {
		t.Errorf("URL missing from trace: %q", out)
	}
	if !strings.HasSuffix(out, "\n") {
		t.Errorf("trace line should end in newline, got %q", out)
	}
}

func TestLoggerRequestDefaultsMethodToGET(t *testing.T) {
	var buf bytes.Buffer
	l := New(&buf, true)
	l.Request("", "http://example/x", nil)
	if !strings.Contains(buf.String(), "→ GET ") {
		t.Errorf("empty method should default to GET, got %q", buf.String())
	}
}

func TestLoggerResponse(t *testing.T) {
	var buf bytes.Buffer
	l := New(&buf, true)
	l.Response(200, []byte(`{"response_code":0}`))
	out := buf.String()
	if !strings.HasPrefix(out, "← 200 ") {
		t.Errorf("want '← 200 ' prefix, got %q", out)
	}
	if !strings.Contains(out, `"response_code":0`) {
		t.Errorf("body missing from trace: %q", out)
	}
}

func TestLoggerResponseTruncatesLongBody(t *testing.T) {
	var buf bytes.Buffer
	l := New(&buf, true)

	// Construct a body well over the 512-byte cap.
	big := bytes.Repeat([]byte("x"), 800)
	l.Response(200, big)

	out := buf.String()
	if !strings.Contains(out, "…") {
		t.Errorf("expected ellipsis on truncated body, got %q", out)
	}
	// Should not contain all 800 'x's.
	if strings.Count(out, "x") >= 800 {
		t.Errorf("body was not truncated, line len=%d", len(out))
	}
}

func TestLoggerResponseStripsTrailingNewlines(t *testing.T) {
	var buf bytes.Buffer
	l := New(&buf, true)
	l.Response(200, []byte("body\n\n"))
	out := buf.String()
	// Exactly one trailing newline (the one we add), not three.
	if strings.Count(out, "\n") != 1 {
		t.Errorf("trailing newlines should be collapsed, got %q", out)
	}
}

func TestLoggerTracef(t *testing.T) {
	var buf bytes.Buffer
	l := New(&buf, true)
	l.Tracef("→ retry")
	l.Tracef("→ rediscover %s", "urn:schemas-upnp-org:device:MediaRenderer:1")

	got := buf.String()
	if !strings.Contains(got, "→ retry\n") {
		t.Errorf("missing retry trace, got %q", got)
	}
	if !strings.Contains(got, "→ rediscover urn:schemas-upnp-org:device:MediaRenderer:1\n") {
		t.Errorf("missing rediscover trace, got %q", got)
	}
}

func TestLoggerTracefAddsTrailingNewline(t *testing.T) {
	var buf bytes.Buffer
	l := New(&buf, true)
	l.Tracef("→ noeol")
	if !strings.HasSuffix(buf.String(), "\n") {
		t.Errorf("Tracef should add trailing newline, got %q", buf.String())
	}
	// And it shouldn't double up when the format already has one.
	buf.Reset()
	l.Tracef("→ alreadyeol\n")
	if strings.Count(buf.String(), "\n") != 1 {
		t.Errorf("Tracef double-newlined: %q", buf.String())
	}
}
