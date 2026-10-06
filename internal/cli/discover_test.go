package cli

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/ljagiello/yamaha-cli/internal/config"
	"github.com/ljagiello/yamaha-cli/internal/debuglog"
	"github.com/ljagiello/yamaha-cli/pkg/discover"
)

// stubSearch replaces the LAN search with one that finds devs. It
// returns a pointer to whether the last call's context carried a
// discovery trace hook.
func stubSearch(t *testing.T, devs []discover.Device) *bool {
	t.Helper()
	traced := new(bool)
	prev := searchFn
	searchFn = func(ctx context.Context, _ time.Duration) ([]discover.Device, error) {
		if fn := discover.ContextTrace(ctx); fn != nil {
			*traced = true
			fn("→ stub search trace")
		}
		return devs, nil
	}
	t.Cleanup(func() { searchFn = prev })
	return traced
}

// execRoot runs the real root command with args and returns what it
// wrote to stdout and stderr.
func execRoot(args ...string) (stdout, stderr string, err error) {
	root := newRootCmd()
	root.SetArgs(args)
	var out, errOut bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&errOut)
	err = root.ExecuteContext(context.Background())
	return out.String(), errOut.String(), err
}

// assertNoReceiverFound checks the error an empty LAN search produces:
// exit 69, and a message that names the likely cause and the way out
// instead of claiming the (reachable) receiver is unreachable.
func assertNoReceiverFound(t *testing.T, err error) {
	t.Helper()
	var nrf *noReceiverFoundError
	if !errors.As(err, &nrf) {
		t.Fatalf("expected *noReceiverFoundError, got %v (%T)", err, err)
	}
	if code := ErrorExitCode(err); code != 69 {
		t.Errorf("exit code: got %d want 69", code)
	}
	msg := err.Error()
	for _, want := range []string{"SSDP", "49154", "subnet", "--debug", "yamaha config add <alias> --host <ip>"} {
		if !strings.Contains(msg, want) {
			t.Errorf("message missing %q: %s", want, msg)
		}
	}
	if strings.Contains(msg, "not reachable") {
		t.Errorf("message must not claim the receiver is unreachable: %s", msg)
	}
}

// Issue #22 follow-up: with nothing found, `discover --add` printed only
// "device not reachable; check power and network".
func TestDiscoverAdd_NoReceiversExplainsAndPointsAtConfigAdd(t *testing.T) {
	isolateFromUserEnv(t)
	stubSearch(t, nil)

	_, _, err := execRoot("discover", "--add")
	assertNoReceiverFound(t, err)
}

func TestRunWizard_NoReceiversExplainsAndPointsAtConfigAdd(t *testing.T) {
	isolateFromUserEnv(t)
	stubSearch(t, nil)

	var out, errOut bytes.Buffer
	_, _, err := runWizard(context.Background(), &out, &errOut, &config.Config{})
	assertNoReceiverFound(t, err)
}

// Plain `discover` keeps its contract (empty list on stdout, exit 0) and
// says on stderr why the list may be empty.
func TestDiscover_NoReceiversKeepsEmptyListAndWarnsOnStderr(t *testing.T) {
	isolateFromUserEnv(t)
	stubSearch(t, nil)

	stdout, stderr, err := execRoot("discover", "--output", "json")
	if err != nil {
		t.Fatalf("discover: %v", err)
	}
	if strings.TrimSpace(stdout) != "[]" {
		t.Errorf("stdout: got %q want []", stdout)
	}
	if !strings.HasPrefix(stderr, "warning: ") || !strings.Contains(stderr, "yamaha config add <alias> --host <ip>") {
		t.Errorf("stderr: got %q, want a warning pointing at config add", stderr)
	}
}

func TestDiscover_FoundReceiverPrintsNoWarning(t *testing.T) {
	isolateFromUserEnv(t)
	stubSearch(t, []discover.Device{probedRXV583})

	stdout, stderr, err := execRoot("discover", "--output", "json")
	if err != nil {
		t.Fatalf("discover: %v", err)
	}
	if !strings.Contains(stdout, probedRXV583.UDN) {
		t.Errorf("stdout missing the device: %q", stdout)
	}
	if stderr != "" {
		t.Errorf("stderr should be empty, got %q", stderr)
	}
}

func TestDiscover_DebugTracesTheSearch(t *testing.T) {
	isolateFromUserEnv(t)
	traced := stubSearch(t, []discover.Device{probedRXV583})

	_, stderr, err := execRoot("discover", "--debug")
	if err != nil {
		t.Fatalf("discover --debug: %v", err)
	}
	if !*traced || !strings.Contains(stderr, "→ stub search trace") {
		t.Errorf("--debug did not reach the search; traced=%v stderr=%q", *traced, stderr)
	}
}

func TestDiscover_NoTraceWithoutDebug(t *testing.T) {
	isolateFromUserEnv(t)
	traced := stubSearch(t, []discover.Device{probedRXV583})

	if _, _, err := execRoot("discover"); err != nil {
		t.Fatalf("discover: %v", err)
	}
	if *traced {
		t.Error("search was traced without --debug")
	}
}

func TestConfigAdd_DebugTracesTheProbe(t *testing.T) {
	isolateFromUserEnv(t)
	traced := false
	prev := describeFn
	describeFn = func(ctx context.Context, _ string, _ time.Duration) (discover.Device, error) {
		traced = discover.ContextTrace(ctx) != nil
		return probedRXV583, nil
	}
	t.Cleanup(func() { describeFn = prev })

	if _, _, err := execConfigAdd(context.Background(), "living-room", "--host", "192.168.1.116", "--debug"); err != nil {
		t.Fatalf("config add: %v", err)
	}
	if !traced {
		t.Error("--debug did not reach the config add probe")
	}
}

func TestDiscoveryCtx(t *testing.T) {
	ctx := context.Background()
	if got := discoveryCtx(ctx, nil); discover.ContextTrace(got) != nil {
		t.Error("nil logger must not install a trace hook")
	}
	if got := discoveryCtx(ctx, debuglog.New(&bytes.Buffer{}, false)); discover.ContextTrace(got) != nil {
		t.Error("disabled logger must not install a trace hook")
	}
	var buf bytes.Buffer
	fn := discover.ContextTrace(discoveryCtx(ctx, debuglog.New(&buf, true)))
	if fn == nil {
		t.Fatal("enabled logger must install a trace hook")
	}
	fn("→ ssdp M-SEARCH %d/%d", 1, 3)
	if got := buf.String(); got != "→ ssdp M-SEARCH 1/3\n" {
		t.Errorf("trace line: got %q", got)
	}
}
