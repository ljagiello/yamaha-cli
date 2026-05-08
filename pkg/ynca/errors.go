package ynca

import (
	"errors"
	"fmt"
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
