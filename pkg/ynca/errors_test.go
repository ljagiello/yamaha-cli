package ynca

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"testing"
)

// customNetErr is a minimal net.Error implementation used to exercise
// the final errors.As(err, &net.Error) fallthrough in IsTransport.
// Distinct from *net.OpError so it only matches the interface branch.
type customNetErr struct {
	msg     string
	timeout bool
}

func (e *customNetErr) Error() string   { return e.msg }
func (e *customNetErr) Timeout() bool   { return e.timeout }
func (e *customNetErr) Temporary() bool { return false }

// TestIsTransport covers every classification branch so the CLI's
// DHCP-rediscovery trigger stays predictable. The key non-obvious
// case is context.DeadlineExceeded — it implements net.Error and
// would otherwise fall through into the "yes, transport" branch and
// fire an SSDP scan on every per-Send timeout.
func TestIsTransport(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},

		// Context outcomes — never rediscover.
		{"context.Canceled", context.Canceled, false},
		{"context.DeadlineExceeded", context.DeadlineExceeded, false},
		{"wrapped context.Canceled", fmt.Errorf("wrapped: %w", context.Canceled), false},
		{"wrapped context.DeadlineExceeded", fmt.Errorf("wrapped: %w", context.DeadlineExceeded), false},

		// Application sentinels.
		{"ErrUnsupported", ErrUnsupported, false},
		{"ErrNoReply", ErrNoReply, false},

		// Application typed errors.
		{"ErrUndefinedCommand", &ErrUndefinedCommand{Line: "@UNDEFINED"}, false},
		{"ErrRestricted", &ErrRestricted{Line: "@RESTRICTED"}, false},
		{"ProtocolError", &ProtocolError{Line: "garbage"}, false},

		// Network shapes — these SHOULD rediscover.
		{"io.EOF", io.EOF, true},
		{"io.ErrUnexpectedEOF", io.ErrUnexpectedEOF, true},
		{"net.OpError", &net.OpError{Op: "dial", Err: errors.New("connection refused")}, true},
		{"wrapped dial OpError", fmt.Errorf("ynca: dial 1.2.3.4:50000: %w",
			&net.OpError{Op: "dial", Err: errors.New("no route to host")}), true},

		// DialError — a dial failure is always transport, even when the
		// underlying cause is a timeout that errors.Is-matches
		// context.DeadlineExceeded (the powered-off-receiver case).
		{"DialError (refused)", &DialError{Addr: "1.2.3.4:50000",
			Err: &net.OpError{Op: "dial", Err: errors.New("connection refused")}}, true},
		{"DialError (timeout)", &DialError{Addr: "1.2.3.4:50000", Err: context.DeadlineExceeded}, true},
		// ...but a user cancel mid-dial is still not transport.
		{"DialError (user cancel)", &DialError{Addr: "1.2.3.4:50000", Err: context.Canceled}, false},

		// Custom net.Error impl that doesn't match the io / *net.OpError
		// branches above — exercises the final errors.As(err, &net.Error)
		// fallthrough. Without this case, the fallthrough could be
		// deleted and the test suite would still pass.
		{"custom net.Error", &customNetErr{msg: "boom", timeout: false}, true},
		{"custom net.Error (timeout)", &customNetErr{msg: "i/o timeout", timeout: true}, true},

		// Unknown plain error — not transport.
		{"plain error", errors.New("something else"), false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := IsTransport(tc.err); got != tc.want {
				t.Errorf("IsTransport(%v) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}
