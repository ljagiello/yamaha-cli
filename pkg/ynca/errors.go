package ynca

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
)

// DialError wraps a failure to establish the TCP connection to the
// receiver. It is always a transport failure (the host is unreachable),
// which IsTransport reports as such even when the underlying cause is a
// timeout — a dial timeout errors.Is-matches context.DeadlineExceeded and
// would otherwise be indistinguishable from a slow-but-reachable
// per-command deadline (which is deliberately NOT treated as transport).
type DialError struct {
	Addr string
	Err  error
}

func (e *DialError) Error() string { return fmt.Sprintf("ynca: dial %s: %v", e.Addr, e.Err) }
func (e *DialError) Unwrap() error { return e.Err }

// ProtocolError is returned when the receiver sends a line that does
// not conform to the `@SUBUNIT:FUNCTION=value` grammar.
type ProtocolError struct {
	Line string
}

func (e *ProtocolError) Error() string {
	return fmt.Sprintf("ynca: malformed reply: %q", e.Line)
}

// ErrUndefinedCommand is the receiver's `@UNDEFINED` reply: the
// subunit/function pair does not exist on this device.
type ErrUndefinedCommand struct {
	Line string
}

func (e *ErrUndefinedCommand) Error() string {
	return fmt.Sprintf("ynca: undefined command (reply=%q)", e.Line)
}

// ErrRestricted is the receiver's `@RESTRICTED` reply: the function
// exists but is not currently allowed (e.g. the zone is in standby).
type ErrRestricted struct {
	Line string
}

func (e *ErrRestricted) Error() string {
	return fmt.Sprintf("ynca: restricted (reply=%q)", e.Line)
}

// ErrNoReply is returned by Send when the receiver does not produce a
// reply line within the configured timeout.
var ErrNoReply = errors.New("ynca: no reply within timeout")

// ErrUnsupported is returned by Probe when the receiver is reachable
// on TCP/50000 but does not speak YNCA.
var ErrUnsupported = errors.New("ynca: device does not support YNCA")

// IsTransport reports whether err looks like a network-layer failure —
// dial timeouts, connection refused, EOF on a stale conn, etc. It
// returns false for application-level outcomes (ErrUndefinedCommand,
// ErrRestricted, ProtocolError), for "reached but wrong protocol"
// (ErrUnsupported), for "no reply within timeout" (ErrNoReply), and
// for context cancellation. The CLI uses this to decide whether to
// trigger DHCP rediscovery.
func IsTransport(err error) bool {
	if err == nil {
		return false
	}
	// User cancellation is never transport — don't rediscover on Ctrl-C,
	// even if it landed mid-dial.
	if errors.Is(err, context.Canceled) {
		return false
	}
	// A dial failure means the host is unreachable: always transport, even
	// when the underlying cause is a timeout. A dial timeout
	// errors.Is-matches context.DeadlineExceeded, so without this explicit
	// check it would be excluded by the guard below and a powered-off /
	// unreachable receiver would surface as a generic exit 1 instead of
	// the documented "device not reachable" (exit 69).
	var de *DialError
	if errors.As(err, &de) {
		return true
	}
	// A per-command deadline against a reachable-but-slow device is NOT
	// transport. context.DeadlineExceeded implements net.Error
	// (Timeout()==true), so the net.Error fallthrough below would
	// otherwise classify it as transport and trigger an SSDP scan on every
	// slow command. The YXC twin (yxc.IsTransport) only matches its own
	// *transportError, so this guard keeps the two classifiers symmetric.
	if errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	if errors.Is(err, ErrUnsupported) || errors.Is(err, ErrNoReply) {
		return false
	}
	var und *ErrUndefinedCommand
	if errors.As(err, &und) {
		return false
	}
	var res *ErrRestricted
	if errors.As(err, &res) {
		return false
	}
	var pe *ProtocolError
	if errors.As(err, &pe) {
		return false
	}
	// Network shapes.
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
		return true
	}
	var oe *net.OpError
	if errors.As(err, &oe) {
		return true
	}
	var ne net.Error
	return errors.As(err, &ne)
}
