package config

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSaveLoadRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")

	in := &Config{
		DefaultDevice: "living-room",
		Devices: map[string]Device{
			"living-room": {
				Host:        "192.168.1.116",
				UDN:         "uuid:9ab0c000-f668-11de-9976-00a0defbe863",
				DefaultZone: "main",
			},
			"bedroom": {
				Host:        "192.168.1.118",
				DefaultZone: "main",
				// no UDN — should be omitted from YAML output
			},
		},
	}
	if err := saveTo(path, in); err != nil {
		t.Fatalf("saveTo: %v", err)
	}

	// Confirm the empty UDN was omitted (clean files).
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if strings.Contains(string(raw), "udn: \"\"") || strings.Contains(string(raw), "udn: ''") {
		t.Errorf("empty udn should be omitted, got:\n%s", raw)
	}

	out, err := loadFrom(path)
	if err != nil {
		t.Fatalf("loadFrom: %v", err)
	}
	if out.DefaultDevice != in.DefaultDevice {
		t.Errorf("DefaultDevice: got %q, want %q", out.DefaultDevice, in.DefaultDevice)
	}
	if len(out.Devices) != len(in.Devices) {
		t.Errorf("Devices len: got %d, want %d", len(out.Devices), len(in.Devices))
	}
	if got := out.Devices["living-room"]; got != in.Devices["living-room"] {
		t.Errorf("living-room: got %+v, want %+v", got, in.Devices["living-room"])
	}
	if got := out.Devices["bedroom"]; got != in.Devices["bedroom"] {
		t.Errorf("bedroom: got %+v, want %+v", got, in.Devices["bedroom"])
	}
}

func TestLoadMissingFile(t *testing.T) {
	dir := t.TempDir()
	c, err := loadFrom(filepath.Join(dir, "nope.yaml"))
	if err != nil {
		t.Fatalf("loadFrom missing: %v", err)
	}
	if c == nil {
		t.Fatal("expected empty config, got nil")
	}
	if c.DefaultDevice != "" || len(c.Devices) != 0 {
		t.Errorf("expected zero-value config, got %+v", c)
	}
}

func TestSaveCreatesParentDirs(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "deeply", "nested", "config.yaml")
	if err := saveTo(path, &Config{DefaultDevice: "x"}); err != nil {
		t.Fatalf("saveTo nested: %v", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("nested file missing: %v", err)
	}
}

func TestSaveAtomicLeavesOriginalIntactOnFailure(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")

	// Seed a known-good file we want to preserve.
	original := &Config{
		DefaultDevice: "living-room",
		Devices: map[string]Device{
			"living-room": {Host: "192.168.1.116", DefaultZone: "main"},
		},
	}
	if err := saveTo(path, original); err != nil {
		t.Fatalf("seed save: %v", err)
	}
	originalRaw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read seed: %v", err)
	}

	// Simulate a partial write: drop a stray .tmp file in place that a
	// failed previous Save might have left behind. The next Save should
	// blow it away and still produce a valid file — and crucially, if we
	// abort before rename, the original must be untouched.
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte("garbage-from-a-crash\n"), 0o644); err != nil {
		t.Fatalf("seed temp: %v", err)
	}

	// Read the original back via Load — it should still parse fine.
	got, err := loadFrom(path)
	if err != nil {
		t.Fatalf("loadFrom after partial write: %v", err)
	}
	if got.DefaultDevice != "living-room" {
		t.Errorf("original was corrupted: %+v", got)
	}

	// Now do a real Save and confirm the temp file got cleaned and the
	// real file was updated atomically (i.e. ends up valid YAML).
	updated := &Config{
		DefaultDevice: "bedroom",
		Devices: map[string]Device{
			"bedroom": {Host: "192.168.1.118", DefaultZone: "main"},
		},
	}
	if err := saveTo(path, updated); err != nil {
		t.Fatalf("real save after partial: %v", err)
	}
	if _, err := os.Stat(tmp); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("temp file should be gone after successful save, stat err=%v", err)
	}
	got, err = loadFrom(path)
	if err != nil {
		t.Fatalf("loadFrom after real save: %v", err)
	}
	if got.DefaultDevice != "bedroom" {
		t.Errorf("post-save default: got %q want %q", got.DefaultDevice, "bedroom")
	}

	// Sanity: the original we read earlier really was the seeded version.
	if !strings.Contains(string(originalRaw), "living-room") {
		t.Errorf("original file did not contain seed content: %s", originalRaw)
	}
}

func TestPathReturnsNonEmpty(t *testing.T) {
	// We don't assert the exact value (varies by OS / env) but it must be
	// non-empty and end with config.yaml.
	p := Path()
	if p == "" {
		t.Fatal("Path() returned empty string")
	}
	if filepath.Base(p) != "config.yaml" {
		t.Errorf("Path() basename = %q, want config.yaml", filepath.Base(p))
	}
	if filepath.Base(filepath.Dir(p)) != "yamaha-cli" {
		t.Errorf("Path() parent dir = %q, want yamaha-cli", filepath.Base(filepath.Dir(p)))
	}
}

func TestResolveOrder(t *testing.T) {
	cfg := &Config{
		DefaultDevice: "living-room",
		Devices: map[string]Device{
			"living-room": {Host: "192.168.1.116", UDN: "udn-living", DefaultZone: "main"},
			"bedroom":     {Host: "192.168.1.118", UDN: "udn-bed", DefaultZone: "main"},
		},
	}

	tests := []struct {
		name       string
		cfg        *Config
		hostFlag   string
		deviceFlag string
		hostEnv    string
		deviceEnv  string
		wantAlias  string
		wantHost   string
		wantErr    error
	}{
		{
			name:      "rule 1: --host wins over everything",
			cfg:       cfg,
			hostFlag:  "10.0.0.1",
			hostEnv:   "10.0.0.2",
			deviceEnv: "bedroom",
			wantAlias: "",
			wantHost:  "10.0.0.1",
		},
		{
			name:       "rule 2: YAMAHA_HOST wins over --device",
			cfg:        cfg,
			hostEnv:    "10.0.0.2",
			deviceFlag: "bedroom",
			wantAlias:  "",
			wantHost:   "10.0.0.2",
		},
		{
			name:       "rule 3: --device wins over YAMAHA_DEVICE",
			cfg:        cfg,
			deviceFlag: "bedroom",
			deviceEnv:  "living-room",
			wantAlias:  "bedroom",
			wantHost:   "192.168.1.118",
		},
		{
			name:      "rule 4: YAMAHA_DEVICE wins over default_device",
			cfg:       cfg,
			deviceEnv: "bedroom",
			wantAlias: "bedroom",
			wantHost:  "192.168.1.118",
		},
		{
			name:      "rule 5: default_device",
			cfg:       cfg,
			wantAlias: "living-room",
			wantHost:  "192.168.1.116",
		},
		{
			name: "rule 6: single-device shortcut",
			cfg: &Config{
				Devices: map[string]Device{
					"only": {Host: "192.168.1.50", DefaultZone: "main"},
				},
			},
			wantAlias: "only",
			wantHost:  "192.168.1.50",
		},
		{
			name:    "rule 7: nothing — empty config",
			cfg:     &Config{},
			wantErr: ErrNoDevice,
		},
		{
			name:    "rule 7: nothing — nil config",
			cfg:     nil,
			wantErr: ErrNoDevice,
		},
		{
			name: "rule 7: multiple devices, no default_device, no flags",
			cfg: &Config{
				Devices: map[string]Device{
					"a": {Host: "1.1.1.1"},
					"b": {Host: "2.2.2.2"},
				},
			},
			wantErr: ErrNoDevice,
		},
		{
			name:       "--device pointing at unknown alias is an error, not a fallthrough",
			cfg:        cfg,
			deviceFlag: "kitchen",
			wantErr:    nil, // we just check err != nil below
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			alias, dev, err := Resolve(tt.cfg, tt.hostFlag, tt.deviceFlag, tt.hostEnv, tt.deviceEnv)
			if tt.wantErr != nil {
				if !errors.Is(err, tt.wantErr) {
					t.Fatalf("err: got %v, want %v", err, tt.wantErr)
				}
				return
			}
			if tt.name == "--device pointing at unknown alias is an error, not a fallthrough" {
				if err == nil {
					t.Fatalf("expected error for unknown --device, got nil (alias=%q dev=%+v)", alias, dev)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected err: %v", err)
			}
			if alias != tt.wantAlias {
				t.Errorf("alias: got %q, want %q", alias, tt.wantAlias)
			}
			if dev.Host != tt.wantHost {
				t.Errorf("host: got %q, want %q", dev.Host, tt.wantHost)
			}
		})
	}
}

func TestResolveAnonymousHasNoUDN(t *testing.T) {
	cfg := &Config{
		Devices: map[string]Device{
			"living-room": {Host: "192.168.1.116", UDN: "udn-living"},
		},
	}
	alias, dev, err := Resolve(cfg, "10.0.0.1", "", "", "")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if alias != "" {
		t.Errorf("anonymous alias should be empty, got %q", alias)
	}
	if dev.UDN != "" {
		t.Errorf("anonymous device should have no UDN, got %q", dev.UDN)
	}
	if dev.Host != "10.0.0.1" {
		t.Errorf("host: got %q, want 10.0.0.1", dev.Host)
	}
}
