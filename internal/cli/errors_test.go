package cli

import (
	"errors"
	"fmt"
	"testing"

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
