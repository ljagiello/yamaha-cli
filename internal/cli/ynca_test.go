package cli

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ljagiello/yamaha-cli/internal/config"
	"github.com/ljagiello/yamaha-cli/pkg/discover"
	"github.com/ljagiello/yamaha-cli/pkg/yxc"
)

// ynaSplitCRLF mirrors the unexported splitCRLF in pkg/ynca so this
// test package can drive a fake YNCA server without depending on
// internals.
func yncaSplitCRLF(data []byte, atEOF bool) (advance int, token []byte, err error) {
	for i := 0; i+1 < len(data); i++ {
		if data[i] == '\r' && data[i+1] == '\n' {
			return i + 2, data[:i], nil
		}
	}
	if atEOF && len(data) > 0 {
		if data[len(data)-1] == '\r' {
			return len(data), data[:len(data)-1], nil
		}
		return len(data), data, nil
	}
	return 0, nil, nil
}

// startFakeYNCA starts a TCP listener on 127.0.0.1 that, for each
// accepted connection, reads CRLF-framed lines and writes back the
// reply chosen by handler. Empty replies are skipped (used to test
// timeout paths). Returns the host:port address.
func startFakeYNCA(t *testing.T, handler func(line string) string) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	var wg sync.WaitGroup
	go func() {
		for {
			conn, err := l.Accept()
			if err != nil {
				return
			}
			wg.Add(1)
			go func(c net.Conn) {
				defer wg.Done()
				defer c.Close()
				scanner := bufio.NewScanner(c)
				scanner.Buffer(make([]byte, 0, 4096), 64*1024)
				scanner.Split(yncaSplitCRLF)
				for scanner.Scan() {
					reply := handler(scanner.Text())
					if reply == "" {
						continue
					}
					if _, err := io.WriteString(c, reply+"\r\n"); err != nil {
						return
					}
				}
			}(conn)
		}
	}()
	t.Cleanup(func() {
		_ = l.Close()
		wg.Wait()
	})
	return l.Addr().String()
}

// newYncaState builds a *state targeting a fake YNCA server (host:port).
// The yxc.Client is a placeholder — ynca does not use it.
func newYncaState(t *testing.T, addr string) *state {
	t.Helper()
	c, err := yxc.New("127.0.0.1:1") // never actually called
	if err != nil {
		t.Fatalf("yxc.New: %v", err)
	}
	return &state{
		device: config.Device{Host: addr},
		zone:   "main",
		client: c,
	}
}

// shrinkYNCATimeouts swaps the package-level YNCA timeouts to short
// values for the duration of one test, restoring on cleanup.
func shrinkYNCATimeouts(t *testing.T, probe, send time.Duration) {
	t.Helper()
	prevP, prevS := yncaProbeTimeout, yncaSendTimeout
	yncaProbeTimeout = probe
	yncaSendTimeout = send
	t.Cleanup(func() {
		yncaProbeTimeout = prevP
		yncaSendTimeout = prevS
	})
}

// TestYnca_RoundTrip exercises the happy path: the fake server replies
// to the probe and to the user's command, and the reply is forwarded
// to stdout verbatim.
func TestYnca_RoundTrip(t *testing.T) {
	shrinkYNCATimeouts(t, 500*time.Millisecond, 500*time.Millisecond)

	addr := startFakeYNCA(t, func(line string) string {
		switch line {
		case "@SYS:VERSION=?":
			return "@SYS:VERSION=2.87/1.81"
		case "@MAIN:PWR=?":
			return "@MAIN:PWR=On"
		}
		return "@UNDEFINED"
	})

	cmd := newYncaCmd()
	var stdout, stderr bytes.Buffer
	cmd.SetOut(&stdout)
	cmd.SetErr(&stderr)
	cmd.SetArgs([]string{"@MAIN:PWR=?"})

	s := newYncaState(t, addr)
	cmd.SetContext(context.WithValue(context.Background(), stateKey, s))

	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	got := strings.TrimSpace(stdout.String())
	if got != "@MAIN:PWR=On" {
		t.Errorf("stdout: got %q want %q", got, "@MAIN:PWR=On")
	}
}

// TestYnca_LeadingAtOptional verifies that `@`-less arguments are
// accepted: the underlying ynca.Send adds it. The fake server still
// sees a leading `@`.
func TestYnca_LeadingAtOptional(t *testing.T) {
	shrinkYNCATimeouts(t, 500*time.Millisecond, 500*time.Millisecond)

	var seen string
	var mu sync.Mutex
	addr := startFakeYNCA(t, func(line string) string {
		if line == "@SYS:VERSION=?" {
			return "@SYS:VERSION=2.87/1.81"
		}
		mu.Lock()
		seen = line
		mu.Unlock()
		return "@MAIN:PWR=Standby"
	})

	cmd := newYncaCmd()
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	cmd.SetArgs([]string{"MAIN:PWR=?"}) // no leading @

	cmd.SetContext(context.WithValue(context.Background(), stateKey, newYncaState(t, addr)))
	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if seen != "@MAIN:PWR=?" {
		t.Errorf("server saw %q, want %q", seen, "@MAIN:PWR=?")
	}
}

// TestYnca_Unsupported verifies that a server replying with @UNDEFINED
// to the probe (i.e. it does not speak YNCA) maps to exit code 70.
func TestYnca_Unsupported(t *testing.T) {
	shrinkYNCATimeouts(t, 500*time.Millisecond, 500*time.Millisecond)

	addr := startFakeYNCA(t, func(_ string) string {
		// Reject every command — the probe will see @UNDEFINED.
		return "@UNDEFINED"
	})

	cmd := newYncaCmd()
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	cmd.SetArgs([]string{"@MAIN:PWR=?"})

	cmd.SetContext(context.WithValue(context.Background(), stateKey, newYncaState(t, addr)))
	err := cmd.Execute()
	if err == nil {
		t.Fatal("expected error from unsupported device")
	}
	if got := ErrorExitCode(err); got != 70 {
		t.Errorf("exit code: got %d want 70 (err=%v)", got, err)
	}
}

// TestYnca_ContextCancel ensures Send returns a context-cancelled error
// when the parent context is cancelled mid-flight, which the exit-code
// mapper translates to 130.
func TestYnca_ContextCancel(t *testing.T) {
	shrinkYNCATimeouts(t, 2*time.Second, 2*time.Second)

	addr := startFakeYNCA(t, func(line string) string {
		if line == "@SYS:VERSION=?" {
			return "@SYS:VERSION=2.87/1.81"
		}
		// Delay long enough for the cancel to fire.
		time.Sleep(500 * time.Millisecond)
		return "@MAIN:PWR=On"
	})

	cmd := newYncaCmd()
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	cmd.SetArgs([]string{"@MAIN:PWR=?"})

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(150 * time.Millisecond)
		cancel()
	}()
	cmd.SetContext(context.WithValue(ctx, stateKey, newYncaState(t, addr)))

	err := cmd.Execute()
	if err == nil {
		t.Fatal("expected context-cancelled error")
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got %v", err)
	}
}

// TestYnca_RediscoversOnDHCPShift simulates the user-visible bug the
// factory + runYNCAWithRediscover wiring closes: a configured device's
// IP changes, the original host stops answering on TCP/50000, and a
// stubbed SSDP scan reports the new IP. Previously `yamaha ynca` died
// here with exit 69 while every YXC command transparently recovered.
func TestYnca_RediscoversOnDHCPShift(t *testing.T) {
	shrinkYNCATimeouts(t, 300*time.Millisecond, 300*time.Millisecond)

	// Live fake at the "new" IP — answers probe + the user's command.
	newAddr := startFakeYNCA(t, func(line string) string {
		switch line {
		case "@SYS:VERSION=?":
			return "@SYS:VERSION=2.87/1.81"
		case "@MAIN:PWR=?":
			return "@MAIN:PWR=On"
		}
		return "@UNDEFINED"
	})

	// Bind a port and immediately close it so the first dial gets
	// connection-refused (the receiver "moved").
	deadL, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	deadAddr := deadL.Addr().String()
	_ = deadL.Close()

	// Stub the SSDP lookup to "find" the device at newAddr.
	prevLookup := lookupByUDNFn
	lookupByUDNFn = func(_ context.Context, _ string, _ time.Duration) (discover.Device, error) {
		return discover.Device{Host: newAddr}, nil
	}
	t.Cleanup(func() { lookupByUDNFn = prevLookup })

	// Redirect the config file so persistRediscoveredHost writes into a
	// scratch dir; the package's config.Save path uses XDG/HOME.
	scratch := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", scratch)
	t.Setenv("HOME", scratch)
	// Pre-seed the config so Save preserves the alias entry.
	_ = os.MkdirAll(filepath.Join(scratch, "yamaha-cli"), 0o755)

	cfg := &config.Config{
		Devices: map[string]config.Device{
			"living-room": {Host: deadAddr, UDN: "uuid:test-1"},
		},
	}
	if err := config.Save(cfg); err != nil {
		t.Fatalf("seed config: %v", err)
	}

	cmd := newYncaCmd()
	var stdout bytes.Buffer
	cmd.SetOut(&stdout)
	cmd.SetErr(io.Discard)
	cmd.SetArgs([]string{"@MAIN:PWR=?"})

	yxcClient, err := yxc.New("127.0.0.1:1")
	if err != nil {
		t.Fatalf("yxc.New: %v", err)
	}
	s := &state{
		cfg:    cfg,
		alias:  "living-room",
		device: config.Device{Host: deadAddr, UDN: "uuid:test-1"},
		zone:   "main",
		client: yxcClient,
	}
	cmd.SetContext(context.WithValue(context.Background(), stateKey, s))

	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute after rediscovery: %v", err)
	}
	got := strings.TrimSpace(stdout.String())
	if got != "@MAIN:PWR=On" {
		t.Errorf("stdout: got %q want %q", got, "@MAIN:PWR=On")
	}
	// Confirm the config was rewritten to the new IP.
	if s.device.Host != newAddr {
		t.Errorf("s.device.Host = %q, want %q (config not updated)", s.device.Host, newAddr)
	}
}

// TestYnca_SendNotRetriedOnTransport guards the Phase-1/Phase-2 split:
// Probe runs through DHCP rediscovery (it's idempotent), but Send runs
// exactly once even on a transport-shaped failure. State-mutating YNCA
// commands (`@MAIN:VOL=Up`) must not trigger rediscovery — if bytes
// hit the wire but the reply was lost, retrying would double-execute.
//
// The fake server here replies normally to the probe, then closes the
// connection on the first user-Send. The client sees io.EOF, which
// ynca.IsTransport classifies as transport. With the split, Send
// surfaces the EOF directly and no SSDP lookup runs. Without the split
// (the pre-fix shape), runYNCAWithRediscover would have triggered an
// SSDP scan and retried — verified by counting lookupCalls.
func TestYnca_SendNotRetriedOnTransport(t *testing.T) {
	shrinkYNCATimeouts(t, 500*time.Millisecond, 500*time.Millisecond)

	// Custom fake YNCA: replies to probe, closes the conn on Send so
	// the client sees io.EOF (transport-classified). Count the user-
	// command bytes that hit the wire.
	var sendBytes atomic.Int64
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	var wg sync.WaitGroup
	go func() {
		for {
			conn, err := l.Accept()
			if err != nil {
				return
			}
			wg.Add(1)
			go func(c net.Conn) {
				defer wg.Done()
				defer c.Close()
				scanner := bufio.NewScanner(c)
				scanner.Buffer(make([]byte, 0, 4096), 64*1024)
				scanner.Split(yncaSplitCRLF)
				for scanner.Scan() {
					line := scanner.Text()
					if line == "@SYS:VERSION=?" {
						_, _ = io.WriteString(c, "@SYS:VERSION=2.87/1.81\r\n")
						continue
					}
					// Any other command: count + abruptly close to surface
					// io.EOF on the client. Note ynca.Send has its own
					// stale-conn redial, so the user-Send may legitimately
					// hit the wire twice within a single Send call. We
					// only care that no THIRD attempt happens after
					// rediscovery.
					if line == "@MAIN:VOL=Up" {
						sendBytes.Add(1)
					}
					return
				}
			}(conn)
		}
	}()
	t.Cleanup(func() { _ = l.Close(); wg.Wait() })
	addr := l.Addr().String()

	// SSDP stub. With the split, Phase-2 Send should NEVER reach
	// rediscovery. We track calls to verify.
	var lookupCalls atomic.Int64
	prevLookup := lookupByUDNFn
	lookupByUDNFn = func(_ context.Context, _ string, _ time.Duration) (discover.Device, error) {
		lookupCalls.Add(1)
		return discover.Device{Host: addr, UDN: "uuid:test-1"}, nil
	}
	t.Cleanup(func() { lookupByUDNFn = prevLookup })

	// Config-resolved state so DHCP rediscovery would otherwise be
	// eligible (alias + UDN set).
	scratch := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", scratch)
	t.Setenv("HOME", scratch)
	cfg := &config.Config{
		Devices: map[string]config.Device{
			"living-room": {Host: addr, UDN: "uuid:test-1"},
		},
	}
	if err := config.Save(cfg); err != nil {
		t.Fatalf("config.Save: %v", err)
	}

	yxcClient, err := yxc.New("127.0.0.1:1")
	if err != nil {
		t.Fatalf("yxc.New: %v", err)
	}
	s := &state{
		cfg:    cfg,
		alias:  "living-room",
		device: config.Device{Host: addr, UDN: "uuid:test-1"},
		zone:   "main",
		client: yxcClient,
	}

	cmd := newYncaCmd()
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	cmd.SetArgs([]string{"@MAIN:VOL=Up"})
	cmd.SetContext(context.WithValue(context.Background(), stateKey, s))

	err = cmd.Execute()
	if err == nil {
		t.Fatal("expected transport error from Send (server closes conn), got nil")
	}
	// The KEY assertion: no SSDP rediscovery was triggered by the Send
	// failure. With the old wrapping, runYNCAWithRediscover would have
	// fired a lookup and retried.
	if got := lookupCalls.Load(); got != 0 {
		t.Errorf("SSDP lookup ran %d times after Send transport failure; want 0 (Send must not trigger rediscovery)", got)
	}
	// Bytes-on-wire bound: ynca.Send has internal stale-conn retry, so
	// 1 or 2 attempts are both legitimate from a single Send call. What
	// we forbid is a THIRD attempt that would only happen if a full
	// rediscovery+retry kicked in.
	if got := sendBytes.Load(); got > 2 {
		t.Errorf("server saw @MAIN:VOL=Up %d times; want ≤2 (no rediscover-retry)", got)
	}
}

// lastMutation records the most recent non-probe, non-fence line the fake
// server received, so the typed-subcommand tests can assert what got sent.
type lastMutation struct {
	mu   sync.Mutex
	line string
}

func (l *lastMutation) record(line string) {
	if line == "@SYS:VERSION=?" {
		return
	}
	l.mu.Lock()
	l.line = line
	l.mu.Unlock()
}

func (l *lastMutation) get() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.line
}

// TestYncaSubcmd_PowerOn routes `ynca power on` through the parent and
// asserts the absolute PWR set reaches the device.
func TestYncaSubcmd_PowerOn(t *testing.T) {
	shrinkYNCATimeouts(t, 500*time.Millisecond, 500*time.Millisecond)
	var last lastMutation
	addr := startFakeYNCA(t, func(line string) string {
		if line == "@SYS:VERSION=?" {
			return "@SYS:VERSION=2.87/1.81"
		}
		last.record(line)
		return line // echo the set
	})

	cmd := newYncaCmd()
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	cmd.SetArgs([]string{"power", "on"})
	cmd.SetContext(context.WithValue(context.Background(), stateKey, newYncaState(t, addr)))

	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if got := last.get(); got != "@MAIN:PWR=On" {
		t.Errorf("server saw %q, want @MAIN:PWR=On", got)
	}
}

// TestYncaSubcmd_VolumeRoundsToGrid asserts an absolute dB value is
// rounded onto the YNCA 0.5 dB grid before it's sent.
func TestYncaSubcmd_VolumeRoundsToGrid(t *testing.T) {
	shrinkYNCATimeouts(t, 500*time.Millisecond, 500*time.Millisecond)
	var last lastMutation
	addr := startFakeYNCA(t, func(line string) string {
		if line == "@SYS:VERSION=?" {
			return "@SYS:VERSION=2.87/1.81"
		}
		last.record(line)
		return line
	})

	cmd := newYncaCmd()
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	// Negative positionals need the `--` terminator (a cobra/pflag
	// constraint shared with the YXC `volume` command).
	cmd.SetArgs([]string{"volume", "--", "-30.3"})
	cmd.SetContext(context.WithValue(context.Background(), stateKey, newYncaState(t, addr)))

	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if got := last.get(); got != "@MAIN:VOL=-30.5" {
		t.Errorf("server saw %q, want @MAIN:VOL=-30.5", got)
	}
}

// TestYncaSubcmd_Status drives `ynca status` and checks the decoded
// payload reaches stdout.
func TestYncaSubcmd_Status(t *testing.T) {
	shrinkYNCATimeouts(t, 500*time.Millisecond, 500*time.Millisecond)
	addr := startFakeYNCA(t, func(line string) string {
		switch line {
		case "@SYS:VERSION=?":
			return "@SYS:VERSION=2.87/1.81"
		case "@MAIN:BASIC=?":
			return "@MAIN:PWR=On\r\n@MAIN:VOL=-30.5\r\n@MAIN:MUTE=Off\r\n@MAIN:INP=HDMI2\r\n@MAIN:SOUNDPRG=Standard"
		}
		return "@UNDEFINED"
	})

	cmd := newYncaCmd()
	var stdout bytes.Buffer
	cmd.SetOut(&stdout)
	cmd.SetErr(io.Discard)
	cmd.SetArgs([]string{"status"})
	cmd.SetContext(context.WithValue(context.Background(), stateKey, newYncaState(t, addr)))

	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	out := stdout.String()
	for _, want := range []string{"HDMI2", "Standard", "on"} {
		if !strings.Contains(out, want) {
			t.Errorf("status output missing %q: %s", want, out)
		}
	}
}

// TestYncaSubcmd_RestrictedExit75 asserts a typed subcommand surfaces a
// @RESTRICTED set as exit 75.
func TestYncaSubcmd_RestrictedExit75(t *testing.T) {
	shrinkYNCATimeouts(t, 500*time.Millisecond, 500*time.Millisecond)
	addr := startFakeYNCA(t, func(line string) string {
		if line == "@SYS:VERSION=?" {
			return "@SYS:VERSION=2.87/1.81"
		}
		return "@RESTRICTED"
	})

	cmd := newYncaCmd()
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	cmd.SetArgs([]string{"input", "HDMI2"})
	cmd.SetContext(context.WithValue(context.Background(), stateKey, newYncaState(t, addr)))

	err := cmd.Execute()
	if err == nil {
		t.Fatal("expected @RESTRICTED error")
	}
	if got := ErrorExitCode(err); got != 75 {
		t.Errorf("exit code = %d, want 75", got)
	}
}

// TestYncaSubcmd_Repl feeds two commands and exits; both replies must
// appear on stdout, proving the persistent-connection loop works.
func TestYncaSubcmd_Repl(t *testing.T) {
	shrinkYNCATimeouts(t, 500*time.Millisecond, 500*time.Millisecond)
	addr := startFakeYNCA(t, func(line string) string {
		switch line {
		case "@SYS:VERSION=?":
			return "@SYS:VERSION=2.87/1.81"
		case "@MAIN:PWR=?":
			return "@MAIN:PWR=On"
		case "@MAIN:INP=?":
			return "@MAIN:INP=HDMI2"
		}
		return "@UNDEFINED"
	})

	cmd := newYncaCmd()
	var stdout bytes.Buffer
	cmd.SetOut(&stdout)
	cmd.SetErr(io.Discard)
	cmd.SetIn(strings.NewReader("@MAIN:PWR=?\n@MAIN:INP=?\nexit\n"))
	cmd.SetArgs([]string{"repl"})
	cmd.SetContext(context.WithValue(context.Background(), stateKey, newYncaState(t, addr)))

	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	out := stdout.String()
	for _, want := range []string{"@MAIN:PWR=On", "@MAIN:INP=HDMI2"} {
		if !strings.Contains(out, want) {
			t.Errorf("repl output missing %q: %s", want, out)
		}
	}
}

// TestYnca_EmptyArg rejects an empty command string with exit code 2.
func TestYnca_EmptyArg(t *testing.T) {
	t.Parallel()
	cmd := newYncaCmd()
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	cmd.SetArgs([]string{"   "})
	cmd.SetContext(context.WithValue(context.Background(), stateKey, newYncaState(t, "127.0.0.1:1")))

	err := cmd.Execute()
	if err == nil {
		t.Fatal("expected usage error")
	}
	if got := ErrorExitCode(err); got != 2 {
		t.Errorf("exit code: got %d want 2", got)
	}
}
