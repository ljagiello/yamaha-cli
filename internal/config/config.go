// Package config loads, saves, and resolves the yamaha-cli multi-device YAML
// configuration.
//
// The on-disk schema is documented in PLAN.v6.md ("Configuration"). It lives
// at $XDG_CONFIG_HOME/yamaha-cli/config.yaml (with a $HOME/.config fallback
// when os.UserConfigDir fails) and looks like:
//
//	default_device: living-room
//	devices:
//	  living-room:
//	    host: 192.168.1.116
//	    udn: uuid:9ab0c000-f668-11de-9976-00a0defbe863
//	    default_zone: main
//
// All writes go through Save, which writes to <path>.tmp first and then
// renames over the destination so concurrent invocations cannot corrupt the
// file.
package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

// ErrNoDevice is returned by Resolve when no device can be determined from
// flags, environment variables, or config. Callers (e.g. the first-run
// wizard) treat this as a sentinel rather than a hard error.
var ErrNoDevice = errors.New("no device configured")

// Device is a single configured receiver entry.
type Device struct {
	Host        string `yaml:"host"`
	UDN         string `yaml:"udn,omitempty"`
	DefaultZone string `yaml:"default_zone,omitempty"`
}

// Config is the top-level YAML schema.
type Config struct {
	DefaultDevice string            `yaml:"default_device,omitempty"`
	Devices       map[string]Device `yaml:"devices,omitempty"`
}

// Path returns the absolute path to the config file. It uses
// os.UserConfigDir when available and falls back to $HOME/.config when that
// fails (e.g. on systems where XDG isn't set and HOME is the only signal).
func Path() string {
	if dir, err := os.UserConfigDir(); err == nil && dir != "" {
		return filepath.Join(dir, "yamaha-cli", "config.yaml")
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".config", "yamaha-cli", "config.yaml")
}

// Load reads and parses the config file. A missing file is not an error;
// Load returns an empty *Config so callers can treat first-run identically
// to "no devices configured yet".
func Load() (*Config, error) {
	return loadFrom(Path())
}

func loadFrom(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return &Config{}, nil
		}
		return nil, fmt.Errorf("read config %s: %w", path, err)
	}
	var c Config
	if err := yaml.Unmarshal(data, &c); err != nil {
		return nil, fmt.Errorf("parse config %s: %w", path, err)
	}
	return &c, nil
}

// Save atomically writes the config to disk. It creates parent directories
// (mode 0755) as needed, writes the YAML to <path>.tmp (mode 0644), and
// renames it into place. The temp file is cleaned up on any failure before
// the rename.
func Save(c *Config) error {
	return saveTo(Path(), c)
}

func saveTo(path string, c *Config) error {
	if c == nil {
		return errors.New("config: Save called with nil config")
	}
	data, err := yaml.Marshal(c)
	if err != nil {
		return fmt.Errorf("marshal config: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create config dir: %w", err)
	}
	tmp := path + ".tmp"
	// Best-effort cleanup of any stale temp file from a previous crash.
	_ = os.Remove(tmp)
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return fmt.Errorf("write temp config: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		// Don't leave the temp file behind if rename failed.
		_ = os.Remove(tmp)
		return fmt.Errorf("rename temp config: %w", err)
	}
	return nil
}

// Resolve picks the active device using the priority order from PLAN.v6.md
// ("Resolution order for the active device"):
//
//  1. --host flag         → anonymous (alias=="")
//  2. YAMAHA_HOST env     → anonymous (alias=="")
//  3. --device flag       → look up in c.Devices
//  4. YAMAHA_DEVICE env   → look up in c.Devices
//  5. default_device      → look up in c.Devices
//  6. single-device       → if exactly one device exists, use it
//  7. none of the above   → return ErrNoDevice
//
// Rules 1 and 2 produce an "anonymous" device with no UDN and no alias; the
// caller should treat alias=="" as "do not engage DHCP-resilience flow".
//
// The hostFlag/deviceFlag arguments are the raw flag strings (empty when
// unset). hostEnv/deviceEnv are the env var values. Resolve does not read
// the environment itself — that's the caller's job, so tests stay
// hermetic.
func Resolve(c *Config, hostFlag, deviceFlag, hostEnv, deviceEnv string) (alias string, dev Device, err error) {
	// Rule 1: --host flag.
	if hostFlag != "" {
		return "", Device{Host: hostFlag}, nil
	}
	// Rule 2: YAMAHA_HOST env.
	if hostEnv != "" {
		return "", Device{Host: hostEnv}, nil
	}
	// Rules 3-6 require a config; if c is nil treat it as empty.
	if c == nil {
		c = &Config{}
	}
	// Rule 3: --device flag.
	if deviceFlag != "" {
		d, ok := c.Devices[deviceFlag]
		if !ok {
			return "", Device{}, fmt.Errorf("device %q not found in config", deviceFlag)
		}
		return deviceFlag, d, nil
	}
	// Rule 4: YAMAHA_DEVICE env.
	if deviceEnv != "" {
		d, ok := c.Devices[deviceEnv]
		if !ok {
			return "", Device{}, fmt.Errorf("device %q (from YAMAHA_DEVICE) not found in config", deviceEnv)
		}
		return deviceEnv, d, nil
	}
	// Rule 5: default_device from config.
	if c.DefaultDevice != "" {
		d, ok := c.Devices[c.DefaultDevice]
		if !ok {
			return "", Device{}, fmt.Errorf("default_device %q not found in devices", c.DefaultDevice)
		}
		return c.DefaultDevice, d, nil
	}
	// Rule 6: single-device shortcut.
	if len(c.Devices) == 1 {
		for name, d := range c.Devices {
			return name, d, nil
		}
	}
	// Rule 7: nothing matched.
	return "", Device{}, ErrNoDevice
}
