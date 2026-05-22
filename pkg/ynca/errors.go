package ynca

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
)

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
	// Application / known-non-transport outcomes — never rediscover.
	if errors.Is(err, context.Canceled) {
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
