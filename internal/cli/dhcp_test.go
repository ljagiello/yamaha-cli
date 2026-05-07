package cli

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"testing"
	"time"

	"github.com/ljagiello/yamaha-cli/internal/config"
	"github.com/ljagiello/yamaha-cli/pkg/discover"
	"github.com/ljagiello/yamaha-cli/pkg/yxc"
)

// stubLookup swaps out lookupByUDNFn for the duration of a test and
// records each call. The previous value is restored via t.Cleanup.
type stubLookup struct {
	calls   int
	lastUDN string
	dev     discover.Device
	err     error
}

func (s *stubLookup) install(t *testing.T) {
	t.Helper()
	prev := lookupByUDNFn
	lookupByUDNFn = func(ctx context.Context, udn string, timeout time.Duration) (discover.Device, error) {
		s.calls++
		s.lastUDN = udn
		return s.dev, s.err
	}
	t.Cleanup(func() { lookupByUDNFn = prev })
}

// newTransportError builds an error that yxc.IsTransport returns true for.
// We construct it by routing a real *net.OpError through the package's
// transportError type via a tiny exported test seam: dialing a closed
// port. Faster path: use the public yxc.New + a closed listener.
func newTransportError(t *testing.T) error {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	addr := l.Addr().String()
	_ = l.Close()

	c, err := yxc.New("http://"+addr, yxc.WithTimeout(200*time.Millisecond))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	_, err = c.Do(context.Background(), "system/getDeviceInfo", nil)
	if err == nil {
		t.Fatalf("expected transport error from closed port")
	}
	if !yxc.IsTransport(err) {
		t.Fatalf("expected yxc.IsTransport to be true for %v (%T)", err, err)
	}
	return err
}

// newStateForTest returns a *state suitable for runWithRediscover
// scenarios. Caller mutates fields per case.
func newStateForTest(t *testing.T, alias, udn, host string) *state {
	t.Helper()
	c, err := yxc.New("http://" + host)
	if err != nil {
		t.Fatalf("yxc.New: %v", err)
	}
	cfg := &config.Config{
		Devices: map[string]config.Device{},
	}
	dev := config.Device{Host: host, UDN: udn, DefaultZone: "main"}
	if alias != "" {
		cfg.Devices[alias] = dev
	}
	return &state{
		cfg:    cfg,
		alias:  alias,
		device: dev,
		zone:   "main",
		client: c,
	}
}

// TestRunWithRediscover_Anonymous verifies that when alias=="" (i.e. the
// device was resolved via --host / YAMAHA_HOST), no rediscovery is
// attempted: op runs once, lookup is never called, and the original
// transport error propagates as-is.
func TestRunWithRediscover_Anonymous(t *testing.T) {
	stub := &stubLookup{}
	stub.install(t)

	transportErr := newTransportError(t)
	s := newStateForTest(t, "", "uuid:abc", "192.0.2.1")

	var calls int
	op := func(_ *yxc.Client) error {
		calls++
		return transportErr
	}
	err := runWithRediscover(context.Background(), s, op)
	if err != transportErr {
		t.Fatalf("expected original error, got %v", err)
	}
	if calls != 1 {
		t.Errorf("op should run exactly once, got %d", calls)
	}
	if stub.calls != 0 {
		t.Errorf("lookup should not be called, got %d", stub.calls)
	}
}

// TestRunWithRediscover_NoUDN verifies that an aliased device without a
// saved UDN (pre-v5 config) skips the rediscover path entirely.
func TestRunWithRediscover_NoUDN(t *testing.T) {
	stub := &stubLookup{}
	stub.install(t)

	transportErr := newTransportError(t)
	s := newStateForTest(t, "living-room", "", "192.0.2.1")

	var calls int
	op := func(_ *yxc.Client) error {
		calls++
		return transportErr
	}
	err := runWithRediscover(context.Background(), s, op)
	if err != transportErr {
		t.Fatalf("expected original error untouched, got %v", err)
	}
	if calls != 1 {
		t.Errorf("op should run exactly once, got %d", calls)
	}
	if stub.calls != 0 {
		t.Errorf("lookup should not be called, got %d", stub.calls)
	}
}

// TestRunWithRediscover_Success verifies the happy path: op fails with a
// transport error, lookup returns a new IP, op runs again and succeeds,
// and the config is written to disk atomically.
func TestRunWithRediscover_Success(t *testing.T) {
	// Redirect XDG_CONFIG_HOME so config.Save and config.Path are scoped
	// to a temp dir.
	tmp := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", tmp)
	// Defensive: also unset HOME-derived UserConfigDir paths on systems
	// where os.UserConfigDir falls through.
	t.Setenv("HOME", tmp)

	stub := &stubLookup{
		dev: discover.Device{Host: "192.0.2.99", UDN: "uuid:abc"},
	}
	stub.install(t)

	transportErr := newTransportError(t)
	s := newStateForTest(t, "living-room", "uuid:abc", "192.0.2.1")

	// Pre-save a baseline config so persistRediscoveredHost preserves it.
	if err := config.Save(s.cfg); err != nil {
		t.Fatalf("config.Save: %v", err)
	}

	var calls int
	op := func(c *yxc.Client) error {
		calls++
		if calls == 1 {
			return transportErr
		}
		// Second call should be using the rebuilt client targeting the new host.
		if c.BaseURL() != "http://192.0.2.99" {
			t.Errorf("retry client BaseURL: got %q want http://192.0.2.99", c.BaseURL())
		}
		return nil
	}
	if err := runWithRediscover(context.Background(), s, op); err != nil {
		t.Fatalf("runWithRediscover: %v", err)
	}
	if calls != 2 {
		t.Errorf("op should run twice, got %d", calls)
	}
	if stub.calls != 1 {
		t.Errorf("lookup should run once, got %d", stub.calls)
	}
	if stub.lastUDN != "uuid:abc" {
		t.Errorf("lookup udn: got %q want uuid:abc", stub.lastUDN)
	}

	// state.device should be updated to point at the new host.
	if s.device.Host != "192.0.2.99" {
		t.Errorf("state.device.Host: got %q want 192.0.2.99", s.device.Host)
	}
	if s.cfg.Devices["living-room"].Host != "192.0.2.99" {
		t.Errorf("cfg.Devices[living-room].Host: got %q want 192.0.2.99",
			s.cfg.Devices["living-room"].Host)
	}

	// And persisted to disk.
	loaded, err := config.Load()
	if err != nil {
		t.Fatalf("config.Load: %v", err)
	}
	if got := loaded.Devices["living-room"].Host; got != "192.0.2.99" {
		t.Errorf("persisted Host: got %q want 192.0.2.99", got)
	}
	if got := loaded.Devices["living-room"].UDN; got != "uuid:abc" {
		t.Errorf("persisted UDN: got %q want uuid:abc", got)
	}
	// Sanity: no leftover .tmp file.
	if entries, _ := os.ReadDir(tmp); len(entries) > 0 {
		// We're not going to walk the whole tree; just ensure that under
		// our config dir the .tmp file was renamed away.
		path := config.Path()
		if _, err := os.Stat(path + ".tmp"); err == nil {
			t.Errorf(".tmp file still on disk at %s", path+".tmp")
		}
	}
}

// TestRunWithRediscover_LookupFails verifies that when SSDP can't find the
// device by UDN, the wrapped error is unreachableError and ErrorExitCode
// returns 69.
func TestRunWithRediscover_LookupFails(t *testing.T) {
	stub := &stubLookup{
		err: fmt.Errorf("device with UDN %q not found on LAN", "uuid:abc"),
	}
	stub.install(t)

	transportErr := newTransportError(t)
	s := newStateForTest(t, "living-room", "uuid:abc", "192.0.2.1")

	op := func(_ *yxc.Client) error {
		return transportErr
	}
	err := runWithRediscover(context.Background(), s, op)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	var ue *unreachableError
	if !errors.As(err, &ue) {
		t.Fatalf("expected *unreachableError, got %v (%T)", err, err)
	}
	if ue.alias != "living-room" {
		t.Errorf("unreachable.alias: got %q want living-room", ue.alias)
	}
	if ue.udn != "uuid:abc" {
		t.Errorf("unreachable.udn: got %q want uuid:abc", ue.udn)
	}
	if got := ErrorExitCode(err); got != 69 {
		t.Errorf("ErrorExitCode: got %d want 69", got)
	}
}

// TestRunWithRediscover_LookupCancelled verifies the SIGINT-during-rediscover
// branch: when lookupByUDNFn returns context.Canceled (the user hit Ctrl-C
// during the SSDP scan), runWithRediscover propagates a *cancelledError so
// ErrorExitCode returns 130 — not the transport-unreachable 69 the v1
// review flagged.
func TestRunWithRediscover_LookupCancelled(t *testing.T) {
	stub := &stubLookup{
		err: context.Canceled,
	}
	stub.install(t)

	transportErr := newTransportError(t)
	s := newStateForTest(t, "living-room", "uuid:abc", "192.0.2.1")

	var calls int
	op := func(_ *yxc.Client) error {
		calls++
		return transportErr
	}
	err := runWithRediscover(context.Background(), s, op)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	var ce *cancelledError
	if !errors.As(err, &ce) {
		t.Fatalf("expected *cancelledError, got %v (%T)", err, err)
	}
	if got := ErrorExitCode(err); got != 130 {
		t.Errorf("ErrorExitCode: got %d, want 130", got)
	}
	if calls != 1 {
		t.Errorf("op should run exactly once before lookup, got %d", calls)
	}
	if stub.calls != 1 {
		t.Errorf("lookup should be called once, got %d", stub.calls)
	}
}

// TestRunWithRediscover_ParentCtxCancelledDuringLookup verifies the
// parallel path: even if the stub returned a non-Canceled error, a
// cancelled parent ctx is sufficient to surface as *cancelledError.
func TestRunWithRediscover_ParentCtxCancelledDuringLookup(t *testing.T) {
	// Lookup returns a generic error; what matters is ctx.Err() != nil.
	stub := &stubLookup{
		err: errors.New("boom"),
	}
	stub.install(t)

	transportErr := newTransportError(t)
	s := newStateForTest(t, "living-room", "uuid:abc", "192.0.2.1")

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // immediately cancel before runWithRediscover sees it

	op := func(_ *yxc.Client) error { return transportErr }
	err := runWithRediscover(ctx, s, op)
	var ce *cancelledError
	if !errors.As(err, &ce) {
		t.Fatalf("expected *cancelledError when parent ctx is cancelled, got %v (%T)", err, err)
	}
	if got := ErrorExitCode(err); got != 130 {
		t.Errorf("ErrorExitCode: got %d, want 130", got)
	}
}

// TestRunWithRediscover_NonTransportError verifies that a non-transport
// error from op (e.g. YXC response_code) is returned without consulting
// the lookup at all.
func TestRunWithRediscover_NonTransportError(t *testing.T) {
	stub := &stubLookup{}
	stub.install(t)

	yxcErr := &yxc.Error{Code: 6, Message: "not found", Method: "main/setInput"}
	s := newStateForTest(t, "living-room", "uuid:abc", "192.0.2.1")

	var calls int
	op := func(_ *yxc.Client) error {
		calls++
		return yxcErr
	}
	err := runWithRediscover(context.Background(), s, op)
	if err != yxcErr {
		t.Fatalf("expected exact YXC error, got %v", err)
	}
	if calls != 1 {
		t.Errorf("op should run exactly once, got %d", calls)
	}
	if stub.calls != 0 {
		t.Errorf("lookup should not be called, got %d", stub.calls)
	}
}
