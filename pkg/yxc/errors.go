package yxc

import (
	"errors"
	"fmt"
)

// Known YXC response codes. The receiver returns HTTP 200 with a
// `response_code` field in the body; non-zero values are device-side errors,
// not transport errors, and must not be retried.
const (
	codeOK             = 0
	codeDeviceNotReady = 5
	codeNotFound       = 6
)

// Error is a YXC-level error: HTTP 200 with `response_code != 0` in the body.
//
// Errors of well-known codes are exposed via sentinel values
// (ErrDeviceNotReady, ErrNotFound) and match via errors.Is.
type Error struct {
	Code    int
	Message string
	Method  string // YXC method that produced the error, e.g. "main/setInput"
}

// Error implements error.
func (e *Error) Error() string {
	if e.Method != "" {
		return fmt.Sprintf("yxc: %s: response_code=%d (%s)", e.Method, e.Code, e.Message)
	}
	return fmt.Sprintf("yxc: response_code=%d (%s)", e.Code, e.Message)
}

// Is implements errors.Is so that callers can match the sentinel values
// (ErrDeviceNotReady, ErrNotFound) against any *Error with the same code.
func (e *Error) Is(target error) bool {
	t, ok := target.(*Error)
	if !ok {
		return false
	}
	return e.Code == t.Code
}

// Sentinel errors for well-known YXC response codes.
var (
	// ErrDeviceNotReady (response_code=5) — device is busy or not ready.
	ErrDeviceNotReady = &Error{Code: codeDeviceNotReady, Message: "device not ready"}
	// ErrNotFound (response_code=6) — the requested resource/parameter is not found.
	ErrNotFound = &Error{Code: codeNotFound, Message: "not found"}
)

// newYXCError constructs a *Error for the given code, attaching a known
// message when one is recognised.
func newYXCError(code int, method string) *Error {
	msg := "unknown"
	switch code {
	case codeDeviceNotReady:
		msg = "device not ready"
	case codeNotFound:
		msg = "not found"
	}
	return &Error{Code: code, Message: msg, Method: method}
}

// transportError wraps a low-level network error so retry logic can
// distinguish it from YXC errors and HTTP non-2xx errors.
type transportError struct {
	err error
}

func (e *transportError) Error() string { return "yxc: transport: " + e.err.Error() }
func (e *transportError) Unwrap() error { return e.err }

// httpStatusError represents a non-200 HTTP response from the receiver.
// These are NOT retried — the device is reachable and chose to reply with 4xx/5xx.
type httpStatusError struct {
	Status int
	Method string
}

func (e *httpStatusError) Error() string {
	return fmt.Sprintf("yxc: %s: HTTP %d", e.Method, e.Status)
}

// IsTransport reports whether err is a transient transport error
// (network-layer failure that may succeed on retry).
func IsTransport(err error) bool {
	var te *transportError
	return errors.As(err, &te)
}

// AsYXC returns the underlying *Error if err wraps one.
func AsYXC(err error) (*Error, bool) {
	var ye *Error
	if errors.As(err, &ye) {
		return ye, true
	}
	return nil, false
}
