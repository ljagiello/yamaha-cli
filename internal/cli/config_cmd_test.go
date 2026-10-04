package cli

import (
	"bytes"
	"context"
	"errors"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/ljagiello/yamaha-cli/internal/config"
	"github.com/ljagiello/yamaha-cli/pkg/discover"
)

var probedRXV583 = discover.Device{
	Name:  "RX-V583 FBE863",
	Model: "RX-V583",
	Host:  "192.168.1.116",
	UDN:   "uuid:9ab0c000-f668-11de-9976-00a0defbe863",
}

// stubDescribe replaces the UPnP probe: it finds probedRXV583, or fails
// with err when err is non-nil. It returns the hosts it was asked to probe.
func stubDescribe(t *testing.T, err error) *[]string {
	t.Helper()
	var probed []string
	prev := describeFn
	describeFn = func(_ context.Context, host string, _ time.Duration) (discover.Device, error) {
		probed = append(probed, host)
		if err != nil {
			return discover.Device{}, err
		}
		return probedRXV583, nil
	}
	t.Cleanup(func() { describeFn = prev })
	return &probed
}

// execConfigAdd runs `yamaha config add <args...>` through the real root
// command and returns what it wrote to stdout and stderr.
func execConfigAdd(ctx context.Context, args ...string) (stdout, stderr string, err error) {
	root := newRootCmd()
	root.SetArgs(append([]string{"config", "add"}, args...))
	var out, errOut bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&errOut)
	err = root.ExecuteContext(ctx)
	return out.String(), errOut.String(), err
}

func seedConfig(t *testing.T, cfg *config.Config) {
	t.Helper()
	if err := config.Save(cfg); err != nil {
		t.Fatalf("seed config: %v", err)
	}
}

func loadConfig(t *testing.T) *config.Config {
	t.Helper()
	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	return cfg
}

func TestConfigAdd_SavesProbedDeviceAsFirstDefault(t *testing.T) {
	isolateFromUserEnv(t)
	probed := stubDescribe(t, nil)

	stdout, stderr, err := execConfigAdd(context.Background(), " living-room ", "--host", "192.168.1.116")
	if err != nil {
		t.Fatalf("config add: %v", err)
	}
	if !reflect.DeepEqual(*probed, []string{"192.168.1.116"}) {
		t.Errorf("probed hosts: got %v", *probed)
	}

	cfg := loadConfig(t)
	want := config.Device{Host: "192.168.1.116", UDN: probedRXV583.UDN, DefaultZone: "main"}
	if got := cfg.Devices["living-room"]; got != want {
		t.Errorf("saved device: got %+v want %+v", got, want)
	}
	if cfg.DefaultDevice != "living-room" {
		t.Errorf("default_device: got %q want living-room", cfg.DefaultDevice)
	}

	if stdout != "" {
		t.Errorf("stdout should be empty, got %q", stdout)
	}
	wantStderr := "Found RX-V583 FBE863 (RX-V583, 192.168.1.116)\n" +
		"Saved living-room → 192.168.1.116 (" + config.Path() + ")\n"
	if stderr != wantStderr {
		t.Errorf("stderr:\ngot  %q\nwant %q", stderr, wantStderr)
	}
}

func TestConfigAdd_ProbeFailureStillSavesWithoutUDN(t *testing.T) {
	isolateFromUserEnv(t)
	stubDescribe(t, errors.New("connection refused"))

	_, stderr, err := execConfigAdd(context.Background(), "nr-800", "--host", "192.168.1.164")
	if err != nil {
		t.Fatalf("config add must succeed when the probe fails: %v", err)
	}

	cfg := loadConfig(t)
	want := config.Device{Host: "192.168.1.164", DefaultZone: "main"}
	if got := cfg.Devices["nr-800"]; got != want {
		t.Errorf("saved device: got %+v want %+v", got, want)
	}
	wantStderr := "warning: could not read device description from 192.168.1.164: connection refused; " +
		"saved without a UDN, so DHCP resilience is disabled for \"nr-800\"\n" +
		"Saved nr-800 → 192.168.1.164 (" + config.Path() + ")\n"
	if stderr != wantStderr {
		t.Errorf("stderr:\ngot  %q\nwant %q", stderr, wantStderr)
	}
}

func TestConfigAdd_ExistingAliasNeedsForce(t *testing.T) {
	isolateFromUserEnv(t)
	seed := &config.Config{
		DefaultDevice: "living-room",
		Devices: map[string]config.Device{
			"living-room": {Host: "192.168.1.116", UDN: "uuid:old", DeviceID: "00A0DEFBE863", DefaultZone: "main"},
		},
	}
	seedConfig(t, seed)
	probed := stubDescribe(t, nil)

	_, _, err := execConfigAdd(context.Background(), "living-room", "--host", "192.168.1.120")
	if err == nil || !strings.Contains(err.Error(), `alias "living-room" already exists in config; pass --force to overwrite`) {
		t.Fatalf("expected already-exists error, got %v", err)
	}
	if code := ErrorExitCode(err); code != 1 {
		t.Errorf("exit code: got %d want 1", code)
	}
	if len(*probed) != 0 {
		t.Errorf("probe must not run for a rejected alias, probed %v", *probed)
	}
	if got := loadConfig(t); !reflect.DeepEqual(got, seed) {
		t.Errorf("config changed without --force: got %+v", got)
	}

	if _, _, err := execConfigAdd(context.Background(), "living-room", "--host", "192.168.1.120", "--force", "--default-zone", "zone2"); err != nil {
		t.Fatalf("config add --force: %v", err)
	}
	want := config.Device{Host: "192.168.1.120", UDN: probedRXV583.UDN, DefaultZone: "zone2"}
	if got := loadConfig(t).Devices["living-room"]; got != want {
		t.Errorf("overwritten device: got %+v want %+v", got, want)
	}
}

func TestConfigAdd_DefaultDeviceOnlyMovesWithSetDefault(t *testing.T) {
	isolateFromUserEnv(t)
	seedConfig(t, &config.Config{
		DefaultDevice: "living-room",
		Devices:       map[string]config.Device{"living-room": {Host: "192.168.1.116", DefaultZone: "main"}},
	})
	stubDescribe(t, nil)

	if _, _, err := execConfigAdd(context.Background(), "bedroom", "--host", "192.168.1.118"); err != nil {
		t.Fatalf("config add bedroom: %v", err)
	}
	if got := loadConfig(t).DefaultDevice; got != "living-room" {
		t.Errorf("default_device after plain add: got %q want living-room", got)
	}

	if _, _, err := execConfigAdd(context.Background(), "kitchen", "--host", "192.168.1.119", "--set-default"); err != nil {
		t.Fatalf("config add kitchen --set-default: %v", err)
	}
	if got := loadConfig(t).DefaultDevice; got != "kitchen" {
		t.Errorf("default_device after --set-default: got %q want kitchen", got)
	}
}

func TestConfigAdd_HostFallsBackToYAMAHA_HOST(t *testing.T) {
	tests := []struct {
		name     string
		args     []string
		wantHost string
	}{
		{"env only", []string{"living-room"}, "192.168.1.130"},
		{"flag wins over env", []string{"living-room", "--host", "192.168.1.116"}, "192.168.1.116"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			isolateFromUserEnv(t)
			t.Setenv("YAMAHA_HOST", "192.168.1.130")
			probed := stubDescribe(t, nil)

			if _, _, err := execConfigAdd(context.Background(), tt.args...); err != nil {
				t.Fatalf("config add: %v", err)
			}
			if got := loadConfig(t).Devices["living-room"].Host; got != tt.wantHost {
				t.Errorf("saved host: got %q want %q", got, tt.wantHost)
			}
			if !reflect.DeepEqual(*probed, []string{tt.wantHost}) {
				t.Errorf("probed hosts: got %v want [%s]", *probed, tt.wantHost)
			}
		})
	}
}

func TestConfigAdd_UsageErrors(t *testing.T) {
	tests := []struct {
		name    string
		args    []string
		wantErr string
	}{
		{"blank alias", []string{"  ", "--host", "192.168.1.120"}, "alias"},
		{"invalid zone", []string{"den", "--host", "192.168.1.120", "--default-zone", "zone9"}, `invalid zone "zone9"`},
		{"host with scheme", []string{"den", "--host", "http://192.168.1.120"}, "bare IP or hostname"},
		{"host with path", []string{"den", "--host", "192.168.1.120/desc.xml"}, "bare IP or hostname"},
		{"no host", []string{"den"}, "config add requires --host <ip> (or YAMAHA_HOST)"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			isolateFromUserEnv(t)
			// An existing device lets the root's device resolution pass
			// for the "no host" case until config subcommands skip it.
			seed := &config.Config{
				DefaultDevice: "living-room",
				Devices:       map[string]config.Device{"living-room": {Host: "192.168.1.116", DefaultZone: "main"}},
			}
			seedConfig(t, seed)
			probed := stubDescribe(t, nil)

			_, _, err := execConfigAdd(context.Background(), tt.args...)
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("expected error containing %q, got %v", tt.wantErr, err)
			}
			if code := ErrorExitCode(err); code != 2 {
				t.Errorf("exit code: got %d want 2 (err: %v)", code, err)
			}
			if len(*probed) != 0 {
				t.Errorf("probe must not run on a usage error, probed %v", *probed)
			}
			if got := loadConfig(t); !reflect.DeepEqual(got, seed) {
				t.Errorf("config changed on usage error: got %+v", got)
			}
		})
	}
}

// TestConfigAdd_InterruptedProbeSavesNothing pins that Ctrl-C during the
// probe aborts the command instead of being treated as an unreachable
// device and saving a UDN-less entry.
func TestConfigAdd_InterruptedProbeSavesNothing(t *testing.T) {
	isolateFromUserEnv(t)
	stubDescribe(t, context.Canceled)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, _, err := execConfigAdd(ctx, "living-room", "--host", "192.168.1.116")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got %v", err)
	}
	if _, statErr := os.Stat(config.Path()); !errors.Is(statErr, os.ErrNotExist) {
		t.Errorf("config file must not be written after an interrupted probe (stat err: %v)", statErr)
	}
}
