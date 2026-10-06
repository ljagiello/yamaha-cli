package discover

import (
	"context"
	"fmt"
	"strings"
	"unicode"
)

// TraceFunc receives one line per discovery step: each M-SEARCH sent,
// each reply with its source and Location, and each description fetched
// or skipped, with the reason. (*debuglog.Logger).Tracef fits.
type TraceFunc func(format string, args ...any)

type traceKey struct{}

// WithTrace returns a copy of ctx whose Search, LookupByUDN and Describe
// calls report each step to fn. fn may be called from several goroutines
// at once. A nil fn leaves tracing off.
func WithTrace(ctx context.Context, fn TraceFunc) context.Context {
	if fn == nil {
		return ctx
	}
	return context.WithValue(ctx, traceKey{}, fn)
}

// ContextTrace returns the TraceFunc installed in ctx by WithTrace, or
// nil if there is none.
func ContextTrace(ctx context.Context) TraceFunc {
	fn, _ := ctx.Value(traceKey{}).(TraceFunc)
	return fn
}

// tracef formats one trace line and hands it to ctx's TraceFunc with
// control characters escaped: lines carry Location headers and names
// from LAN peers, and a newline or terminal escape in one must neither
// forge another trace line nor reach the terminal raw.
func tracef(ctx context.Context, format string, args ...any) {
	if fn := ContextTrace(ctx); fn != nil {
		fn("%s", escapeControl(fmt.Sprintf(format, args...)))
	}
}

// escapeControl replaces each control character in s (C0, DEL and C1)
// with a Go-style \x or \u escape.
func escapeControl(s string) string {
	if !strings.ContainsFunc(s, unicode.IsControl) {
		return s
	}
	var b strings.Builder
	for _, r := range s {
		switch {
		case !unicode.IsControl(r):
			b.WriteRune(r)
		case r < 0x80:
			fmt.Fprintf(&b, `\x%02x`, r)
		default:
			fmt.Fprintf(&b, `\u%04x`, r)
		}
	}
	return b.String()
}
