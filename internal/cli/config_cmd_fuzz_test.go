package cli

import (
	"context"
	"net"
	"net/url"
	"strings"
	"testing"
	"unicode"

	"github.com/ljagiello/yamaha-cli/internal/config"
)

// FuzzValidateConfigHost checks that a host `config add` accepts works
// unchanged everywhere the CLI puts it: the YXC base URL, the UPnP
// description URL Describe falls back to, and the YNCA dial address.
func FuzzValidateConfigHost(f *testing.F) {
	label63 := strings.Repeat("a", 63)
	for _, seed := range []string{
		"192.0.2.10", "receiver.lan", "rx-v583", "RX-V583", "my_host.local", "localhost",
		label63, label63 + "." + label63 + "." + label63 + "." + strings.Repeat("a", 61),
		"", "192.0.2.10:80", "[::1]", "::1", "fe80::1", "::ffff:192.0.2.10", "192.0.2.10%en0",
		"192.0.2.300", "192.0.2", "010.0.0.1", "http://x", "x/y", "bad host", "a..b", ".x", "x.",
		"-x", "x-", "x.-y", label63 + "a", "x\ny", "x\r\nHost: y", "x\x00", "rx-v583.lan\xc3\xa9", "\xff",
	} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, host string) {
		if err := validateConfigHost(host); err != nil {
			if code := ErrorExitCode(err); code != 2 {
				t.Fatalf("validateConfigHost(%q): exit code %d, want 2 (err: %v)", host, code, err)
			}
			return
		}
		for _, r := range host {
			if strings.ContainsRune(":/[]", r) || unicode.IsSpace(r) || unicode.IsControl(r) {
				t.Fatalf("validateConfigHost accepted %q, which contains %q", host, r)
			}
		}
		u, err := url.Parse("http://" + host + "/YamahaExtendedControl/v1/")
		if err != nil {
			t.Fatalf("YXC base URL for accepted host %q: %v", host, err)
		}
		if u.Hostname() != host {
			t.Fatalf("YXC base URL for %q has hostname %q", host, u.Hostname())
		}
		u, err = url.Parse("http://" + net.JoinHostPort(host, "49154") + "/MediaRenderer/desc.xml")
		if err != nil {
			t.Fatalf("description URL for accepted host %q: %v", host, err)
		}
		if u.Hostname() != host || u.Port() != "49154" {
			t.Fatalf("description URL for %q has hostname %q, port %q", host, u.Hostname(), u.Port())
		}
		h, p, err := net.SplitHostPort(net.JoinHostPort(host, "50000"))
		if err != nil || h != host || p != "50000" {
			t.Fatalf("YNCA address for %q splits into (%q, %q, %v)", host, h, p, err)
		}
	})
}

// FuzzConfigAddAlias runs `config add` with an arbitrary alias. It must
// either save a config that loads back with the trimmed alias, or save
// nothing: exit 2 for a blank alias, exit 1 for one the config file
// cannot hold (such as "<<", a YAML merge key).
func FuzzConfigAddAlias(f *testing.F) {
	for _, seed := range []string{
		"living-room", "Living Room", "sal\xc3\xb3n", "<<", " << ", "~", "null", "a: b", "#x", "-x", "--force",
		"\t\nx", "x\n\t\ny", "\n", "", "  ", "\x00", "\xff", "\x1b[31m",
	} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, alias string) {
		isolateFromUserEnv(t)
		stubDescribe(t, nil)

		// "--" keeps an alias such as "-x" from being read as a flag.
		_, _, err := execConfigAdd(context.Background(), "--host", "192.0.2.10", "--", alias)
		cfg, loadErr := config.Load()
		if loadErr != nil {
			t.Fatalf("config add %q (err: %v) left a config Load rejects: %v", alias, err, loadErr)
		}
		trimmed := strings.TrimSpace(alias)
		if err != nil {
			want := 1
			if trimmed == "" {
				want = 2
			}
			if code := ErrorExitCode(err); code != want {
				t.Fatalf("config add %q: exit code %d, want %d (err: %v)", alias, code, want, err)
			}
			if len(cfg.Devices) != 0 || cfg.DefaultDevice != "" {
				t.Fatalf("config add %q failed (%v) but saved %+v", alias, err, cfg)
			}
			return
		}
		want := config.Device{Host: "192.0.2.10", UDN: probedRXV583.UDN, DefaultZone: "main"}
		if got := cfg.Devices[trimmed]; len(cfg.Devices) != 1 || got != want || cfg.DefaultDevice != trimmed {
			t.Fatalf("config add %q saved %+v, want only %q -> %+v as the default", alias, cfg, trimmed, want)
		}
	})
}
