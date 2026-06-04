package cli

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"

	"github.com/ljagiello/yamaha-cli/internal/config"
	"github.com/ljagiello/yamaha-cli/pkg/ynca"
	"github.com/ljagiello/yamaha-cli/pkg/yxc"
)

// TestErrorExitCode covers the full mapping table documented in the README
// ("Exit codes"). Each entry is a single error → exit-code expectation.
func TestErrorExitCode(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want int
	}{
		{"nil", nil, 0},
		{"validation", &ValidationError{Kind: "input", Unknown: "bogus"}, 1},
		{"power-on-timeout", &PowerOnTimeoutError{Zone: "main", Elapsed: "10s"}, 1},
		{"usage", &usageError{msg: "bad flag"}, 2},
		{"no-device-configured", &noDeviceConfiguredError{}, 64},
		{"unreachable", &unreachableError{alias: "living-room", udn: "uuid:x"}, 69},
		{"yxc-code-5", &yxc.Error{Code: 5, Message: "device not ready"}, 70},
		{"yxc-code-6", &yxc.Error{Code: 6, Message: "not found"}, 70},
		{"ynca-undefined", &ynca.ErrUndefinedCommand{Line: "@MAIN:FOO=?"}, 70},
		{"ynca-undefined-wrapped", fmt.Errorf("ctx: %w", &ynca.ErrUndefinedCommand{Line: "@MAIN:FOO=?"}), 70},
		{"ynca-restricted", &ynca.ErrRestricted{Line: "@MAIN:VOL=Up"}, 75},
		{"ynca-restricted-wrapped", fmt.Errorf("ctx: %w", &ynca.ErrRestricted{Line: "@MAIN:VOL=Up"}), 75},
		{"ynca-transport", realYNCATransportError(t), 69},
		{"cancelled", &cancelledError{}, 130},
		{"wrapped-power-on-timeout", fmt.Errorf("foo: %w", &PowerOnTimeoutError{}), 1},
		{"unknown", errors.New("something exploded"), 1},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := ErrorExitCode(tc.err); got != tc.want {
				t.Errorf("ErrorExitCode(%v) = %d, want %d", tc.err, got, tc.want)
			}
		})
	}
}

// TestFriendlyYNCAError verifies the CLI rewrites @UNDEFINED/@RESTRICTED
// into actionable messages while preserving the typed cause so the
// exit-code mapping (70 / 75) still applies through the wrap.
func TestFriendlyYNCAError(t *testing.T) {
	t.Run("undefined", func(t *testing.T) {
		base := &ynca.ErrUndefinedCommand{Line: "@MAIN:FOO=?"}
		got := friendlyYNCAError("@MAIN:FOO=?", base)
		if !strings.Contains(got.Error(), "not supported") {
			t.Errorf("message = %q, want it to mention 'not supported'", got.Error())
		}
		var u *ynca.ErrUndefinedCommand
		if !errors.As(got, &u) {
			t.Error("wrapped error no longer unwraps to *ynca.ErrUndefinedCommand")
		}
		if code := ErrorExitCode(got); code != 70 {
			t.Errorf("exit code = %d, want 70", code)
		}
	})
	t.Run("restricted", func(t *testing.T) {
		base := &ynca.ErrRestricted{Line: "@MAIN:VOL=Up"}
		got := friendlyYNCAError("@MAIN:VOL=Up", base)
		if !strings.Contains(got.Error(), "not allowed right now") {
			t.Errorf("message = %q, want it to mention 'not allowed right now'", got.Error())
		}
		if code := ErrorExitCode(got); code != 75 {
			t.Errorf("exit code = %d, want 75", code)
		}
	})
	t.Run("passthrough", func(t *testing.T) {
		base := errors.New("some transport thing")
		if got := friendlyYNCAError("@MAIN:PWR=?", base); got != base {
			t.Errorf("non-control error mutated: %v", got)
		}
	})
}

// realYNCATransportError synthesises a genuine YNCA transport error by
// dialing a closed port, so the exit-code mapping runs against the same
// value path production hits.
func realYNCATransportError(t *testing.T) error {
	t.Helper()
	c, err := ynca.New("127.0.0.1:1", ynca.WithTimeout(200*time.Millisecond))
	if err != nil {
		t.Fatalf("ynca.New: %v", err)
	}
	_, err = c.Send(context.Background(), "@SYS:VERSION=?")
	if err == nil {
		t.Fatal("expected ynca transport error against closed port")
	}
	if !ynca.IsTransport(err) {
		t.Fatalf("expected ynca transport error, got %T: %v", err, err)
	}
	return err
}

// realTransportError synthesises a genuine *yxc.transportError by
// dialing a port that nothing is listening on, so wrapTransportError
// tests run against the same value path production hits.
func realTransportError(t *testing.T) error {
	t.Helper()
	// Use a guaranteed-closed local port. Pick high port + 0 second
	// timeout via WithTimeout so the failure is quick.
	c, err := yxc.New("127.0.0.1:1", yxc.WithTimeout(200*time.Millisecond))
	if err != nil {
		t.Fatalf("yxc.New: %v", err)
	}
	_, err = c.Do(context.Background(), "main/getStatus", url.Values{})
	if err == nil {
		t.Fatal("expected transport error against closed port")
	}
	if !yxc.IsTransport(err) {
		t.Fatalf("expected yxc transport error, got %T: %v", err, err)
	}
	return err
}

// TestWrapTransportError_RawWrappedToUnreachable proves raw yxc
// transport errors get converted into the friendly *unreachableError
// before printError sees them — fixing the leak of raw `net/http`
// error strings (Client.Timeout / "Get http://..." / etc) to users.
func TestWrapTransportError_RawWrappedToUnreachable(t *testing.T) {
	t.Parallel()
	root := &cobra.Command{Use: "yamaha"}
	root.SetContext(context.Background())
	setStateOnCmd(root, &state{
		alias:  "living-room",
		device: config.Device{Host: "192.0.2.1", UDN: "uuid:abc"},
	})

	tErr := realTransportError(t)
	got := wrapTransportError(root, tErr)

	var ue *unreachableError
	if !errors.As(got, &ue) {
		t.Fatalf("wrapped err = %T (%v), want *unreachableError", got, got)
	}
	if ue.alias != "living-room" || ue.udn != "uuid:abc" {
		t.Errorf("unreachableError fields = (alias=%q, udn=%q), want (living-room, uuid:abc)", ue.alias, ue.udn)
	}
	// The cause must still be reachable for callers that unwrap.
	if !errors.Is(got, tErr) {
		t.Errorf("wrapped err does not unwrap to original transport error")
	}
	// And ErrorExitCode must still resolve to 69.
	if code := ErrorExitCode(got); code != 69 {
		t.Errorf("exit code = %d, want 69", code)
	}
}

// TestWrapTransportError_AnonymousHost covers the --host / YAMAHA_HOST
// path: state has no alias and no saved UDN. The wrap must still
// produce *unreachableError with the bare-bones "device not reachable"
// message — this is what the most common --host failure mode renders
// to the user.
func TestWrapTransportError_AnonymousHost(t *testing.T) {
	t.Parallel()
	root := &cobra.Command{Use: "yamaha"}
	root.SetContext(context.Background())
	// Anonymous state: zero-valued alias and device.UDN.
	setStateOnCmd(root, &state{
		alias:  "",
		device: config.Device{Host: "192.0.2.1"},
	})

	tErr := realTransportError(t)
	got := wrapTransportError(root, tErr)

	var ue *unreachableError
	if !errors.As(got, &ue) {
		t.Fatalf("wrapped err = %T (%v), want *unreachableError", got, got)
	}
	if ue.alias != "" || ue.udn != "" {
		t.Errorf("anonymous unreachableError should have empty alias/udn, got alias=%q udn=%q",
			ue.alias, ue.udn)
	}
	const wantMsg = "device not reachable; check power and network"
	if got := ue.Error(); got != wantMsg {
		t.Errorf("rendered message: got %q want %q", got, wantMsg)
	}
	if code := ErrorExitCode(got); code != 69 {
		t.Errorf("exit code = %d, want 69", code)
	}
}

// TestWrapTransportError_LeavesUnreachableAlone confirms that an error
// that's already an *unreachableError (e.g. from runWithRediscover)
// isn't double-wrapped.
func TestWrapTransportError_LeavesUnreachableAlone(t *testing.T) {
	t.Parallel()
	root := &cobra.Command{Use: "yamaha"}
	root.SetContext(context.Background())
	orig := &unreachableError{alias: "kitchen", udn: "uuid:xyz", cause: realTransportError(t)}
	got := wrapTransportError(root, orig)
	if got != orig {
		t.Errorf("wrapTransportError mutated already-wrapped error: %v", got)
	}
}

// TestWrapTransportError_NonTransportPassthrough confirms non-transport
// errors fall through untouched.
func TestWrapTransportError_NonTransportPassthrough(t *testing.T) {
	t.Parallel()
	root := &cobra.Command{Use: "yamaha"}
	root.SetContext(context.Background())
	plain := errors.New("nope")
	if got := wrapTransportError(root, plain); got != plain {
		t.Errorf("non-transport error mutated: %v", got)
	}
}
