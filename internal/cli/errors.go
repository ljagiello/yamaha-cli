package cli

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/ljagiello/yamaha-cli/internal/config"
	"github.com/ljagiello/yamaha-cli/pkg/ynca"
	"github.com/ljagiello/yamaha-cli/pkg/yxc"
)

// ValidationError is returned when a user-supplied argument doesn't match the
// candidates we read from the cached features file (input names, sound
// programs, etc.). Maps to exit code 1.
type ValidationError struct {
	Kind        string   // e.g. "input", "sound program"
	Unknown     string   // the bad argument the user typed
	Suggestions []string // closest candidates from yxc.DidYouMean
}

func (e *ValidationError) Error() string {
	if len(e.Suggestions) == 0 {
		return fmt.Sprintf("unknown %s %q", e.Kind, e.Unknown)
	}
	return fmt.Sprintf("unknown %s %q; did you mean: %s?", e.Kind, e.Unknown, strings.Join(e.Suggestions, ", "))
}

// PowerOnTimeoutError is returned when `power on` polls past its budget
// without seeing power=on. Exit code 1.
type PowerOnTimeoutError struct {
	Zone    string
	Elapsed string
}

func (e *PowerOnTimeoutError) Error() string {
	return fmt.Sprintf("device did not report power=on within %s; check the receiver", e.Elapsed)
}

// usageError is a thin sentinel for "user passed invalid flags / args" cases
// that aren't caught by cobra itself (e.g. mixing --db with +N). Exit code 2.
type usageError struct{ msg string }

func (e *usageError) Error() string { return e.msg }

func newUsageError(format string, args ...any) error {
	return &usageError{msg: fmt.Sprintf(format, args...)}
}

// noDeviceConfiguredError carries the non-TTY first-run message. Exit code 64.
type noDeviceConfiguredError struct{}

func (e *noDeviceConfiguredError) Error() string {
	return "no device configured; run 'yamaha discover' or pass --host"
}

// unreachableError indicates a network failure where DHCP-resilience also
// gave up. Exit code 69.
type unreachableError struct {
	alias string
	udn   string
	cause error
}

func (e *unreachableError) Error() string {
	if e.alias != "" && e.udn != "" {
		return fmt.Sprintf("device %q (UDN %s) not reachable; check power and network", e.alias, e.udn)
	}
	if e.alias != "" {
		return fmt.Sprintf("device %q not reachable; check power and network", e.alias)
	}
	return "device not reachable; check power and network"
}

func (e *unreachableError) Unwrap() error { return e.cause }

// cancelledError marks user-driven SIGINT cancellation so the exit-code
// mapper can return 130 even after the error has bubbled through wrappers.
type cancelledError struct{ cause error }

func (e *cancelledError) Error() string { return "cancelled by user" }
func (e *cancelledError) Unwrap() error { return e.cause }

// ErrorExitCode maps the various error categories returned by RunE to the
// sysexits-lite codes documented in the README.
func ErrorExitCode(err error) int {
	if err == nil {
		return 0
	}
	// User cancellation always wins over the underlying transport-error
	// flavour: the request was probably mid-flight when SIGINT fired.
	var ce *cancelledError
	if errors.As(err, &ce) {
		return 130
	}
	if errors.Is(err, context.Canceled) {
		return 130
	}
	// no-device + non-TTY → 64.
	if errors.Is(err, config.ErrNoDevice) {
		return 64
	}
	var nde *noDeviceConfiguredError
	if errors.As(err, &nde) {
		return 64
	}
	// Unreachable / DHCP-rediscover failed.
	var ue *unreachableError
	if errors.As(err, &ue) {
		return 69
	}
	if yxc.IsTransport(err) {
		return 69
	}
	// YNCA transport failures (dial/connection errors on TCP/50000) are
	// unreachable too — map them to 69 like their YXC twin instead of
	// letting them fall through to the generic exit 1.
	if ynca.IsTransport(err) {
		return 69
	}
	// YXC response_code != 0.
	if _, ok := yxc.AsYXC(err); ok {
		return 70
	}
	// Legacy YNCA control replies. @UNDEFINED means the command does not
	// exist on this device — unsupported feature, same class as a YXC
	// non-zero response_code → 70. @RESTRICTED means the command is valid
	// but not allowed in the current device state (e.g. the zone is in
	// standby); that's a transient, user-fixable condition, so it maps to
	// EX_TEMPFAIL (75) to signal "retry after fixing the device state"
	// distinctly from an outright-unsupported command.
	var yUndef *ynca.ErrUndefinedCommand
	if errors.As(err, &yUndef) {
		return 70
	}
	var yRestricted *ynca.ErrRestricted
	if errors.As(err, &yRestricted) {
		return 75
	}
	// Validation / power-on timeout / generic.
	var ve *ValidationError
	if errors.As(err, &ve) {
		return 1
	}
	var pe *PowerOnTimeoutError
	if errors.As(err, &pe) {
		return 1
	}
	// Cobra usage errors / bad flags.
	var uerr *usageError
	if errors.As(err, &uerr) {
		return 2
	}
	if isCobraUsageError(err) {
		return 2
	}
	return 1
}

// isCobraUsageError matches on the standard prefixes cobra returns for
// invalid flag / unknown-command / "requires N arg" cases. Cobra doesn't
// expose typed errors for these, so a string check is the pragmatic option.
func isCobraUsageError(err error) bool {
	s := err.Error()
	if strings.HasPrefix(s, "unknown flag") ||
		strings.HasPrefix(s, "unknown command") ||
		strings.HasPrefix(s, "unknown shorthand flag") ||
		strings.HasPrefix(s, "flag needs an argument") ||
		strings.HasPrefix(s, "invalid argument") {
		return true
	}
	return strings.Contains(s, "requires ") && strings.Contains(s, "arg")
}
