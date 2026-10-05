package config

import (
	"bytes"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// FuzzLoadConfig feeds loadFrom arbitrary file contents, as a
// hand-edited or corrupted config would. Whatever it accepts must save
// and load back unchanged.
func FuzzLoadConfig(f *testing.F) {
	for _, seed := range []string{
		"default_device: living-room\n" +
			"devices:\n" +
			"  living-room:\n" +
			"    host: 192.168.1.116\n" +
			"    udn: uuid:9ab0c000-f668-11de-9976-00a0defbe863\n" +
			"    device_id: 00A0DEFBE863\n" +
			"    default_zone: main\n",
		"devices: {}\n",
		"devices:\n  a: null\n",
		"devices:\n  \"<<\":\n    host: x\n",
		"devices:\n  a: &d {host: x}\n  b: *d\n",
		"devices:\n  a: {host: x}\n  a: {host: y}\n",
		"default_device: [1, 2]\n",
		"default_device: \xff\n",
		"default_device: !!binary /w==\n",
		"devices:\n\ta: {}\n",
		"a: &a [*a]\n",
		"- 1\n- 2\n",
		"default_device: " + strings.Repeat("a", 2000) + "\n",
		"",
		"\x00",
		"\xef\xbb\xbf",
	} {
		f.Add([]byte(seed))
	}
	dir := f.TempDir()
	f.Fuzz(func(t *testing.T, data []byte) {
		path := filepath.Join(dir, "input.yaml")
		if err := os.WriteFile(path, data, 0o644); err != nil {
			t.Fatal(err)
		}
		c, err := loadFrom(path)
		if err != nil {
			return
		}
		checkSaveLoadsBack(t, dir, c)
	})
}

// FuzzSaveLoadRoundTrip saves arbitrary field values. The alias is
// free-form user input; UDN and device_id come from the receiver.
func FuzzSaveLoadRoundTrip(f *testing.F) {
	f.Add("living-room", "192.168.1.116", "uuid:9ab0c000-f668-11de-9976-00a0defbe863", "00A0DEFBE863", "main", "living-room")
	f.Add("", "", "", "", "", "")
	for _, s := range []string{
		"<<", "~", "null", "true", "0x10", "1e3", "- x", "#x", "x: y", "&a", "*a", "!!str", "'", "\"",
		"\xff", "\x00", "\x1b[31m", "a\rb", "a\r\nb", "\t\nx", "\n", "\n\n", " x ", "x\n",
		"\xef\xbb\xbfx", "a\xc2\x85b", "a\xe2\x80\xa8b",
	} {
		f.Add(s, s, s, s, s, s)
	}
	dir := f.TempDir()
	f.Fuzz(func(t *testing.T, alias, host, udn, deviceID, zone, def string) {
		checkSaveLoadsBack(t, dir, &Config{
			DefaultDevice: def,
			Devices: map[string]Device{
				alias: {Host: host, UDN: udn, DeviceID: deviceID, DefaultZone: zone},
			},
		})
	})
}

// checkSaveLoadsBack saves c over an existing config file in dir.
// saveTo may refuse c, but then the file must be untouched; otherwise
// loadFrom must return c. A fuzz worker runs its inputs one at a time,
// so one dir per fuzz target is safe and spares a temp dir per input.
func checkSaveLoadsBack(t *testing.T, dir string, c *Config) {
	t.Helper()
	path := filepath.Join(dir, "config.yaml")
	prev := []byte("default_device: previous\n")
	if err := os.WriteFile(path, prev, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := saveTo(path, c); err != nil {
		if got, _ := os.ReadFile(path); !bytes.Equal(got, prev) {
			t.Fatalf("saveTo failed (%v) but changed the file to:\n%s", err, got)
		}
		return
	}
	got, err := loadFrom(path)
	if err != nil {
		data, _ := os.ReadFile(path)
		t.Fatalf("saveTo(%#v) wrote a file loadFrom rejects: %v\n%s", c, err, data)
	}
	want := *c
	// omitempty drops an empty devices map, so it loads back as nil.
	if len(want.Devices) == 0 {
		want.Devices = nil
	}
	if !reflect.DeepEqual(got, &want) {
		data, _ := os.ReadFile(path)
		t.Fatalf("round trip changed the config:\n got %#v\nwant %#v\n%s", got, &want, data)
	}
}
