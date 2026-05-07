package cli

import (
	"testing"

	"github.com/ljagiello/yamaha-cli/internal/config"
)

// TestSlugify covers the behaviour the first-run wizard relies on when
// turning a device's friendly name into a default alias.
func TestSlugify(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		// PLAN.v6 "First-run flow": "RX-V583 FBE863" → "rx-v583-fbe863"
		{"RX-V583 FBE863", "rx-v583-fbe863"},
		// Whitespace + punctuation → single dashes, no leading/trailing.
		{"  My Living Room!!  ", "my-living-room"},
		// Empty input is special-cased to "device" so wizard always has a
		// non-empty default it can prompt with.
		{"", "device"},
		// Unicode falls into the non-alnum bucket and is treated as a
		// separator. RX-V583's actual implementation keeps only ASCII
		// alnum, so "Café" becomes "caf" (the trailing dash is trimmed).
		{"Café", "caf"},
		// All-separator input collapses to the "device" fallback rather
		// than returning an empty string.
		{"!!!", "device"},
		// Repeated separators collapse to a single dash.
		{"living   room", "living-room"},
	}
	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			if got := slugify(tc.in); got != tc.want {
				t.Errorf("slugify(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// TestUniqueAlias covers the alias-collision logic that the wizard uses
// to suggest a numbered suffix when the default is already taken.
func TestUniqueAlias(t *testing.T) {
	t.Run("nil-cfg", func(t *testing.T) {
		if got := uniqueAlias("living-room", nil); got != "living-room" {
			t.Errorf("got %q, want living-room", got)
		}
	})

	t.Run("empty-devices", func(t *testing.T) {
		cfg := &config.Config{}
		if got := uniqueAlias("living-room", cfg); got != "living-room" {
			t.Errorf("got %q, want living-room", got)
		}
	})

	t.Run("single-collision", func(t *testing.T) {
		cfg := &config.Config{Devices: map[string]config.Device{
			"living-room": {Host: "1.1.1.1"},
		}}
		if got := uniqueAlias("living-room", cfg); got != "living-room-2" {
			t.Errorf("got %q, want living-room-2", got)
		}
	})

	t.Run("collision-chain", func(t *testing.T) {
		cfg := &config.Config{Devices: map[string]config.Device{
			"living-room":   {Host: "1.1.1.1"},
			"living-room-2": {Host: "1.1.1.2"},
			"living-room-3": {Host: "1.1.1.3"},
		}}
		if got := uniqueAlias("living-room", cfg); got != "living-room-4" {
			t.Errorf("got %q, want living-room-4", got)
		}
	})

	t.Run("free-base-with-suffixes-taken", func(t *testing.T) {
		// Edge case: the base itself is free, so we just return it
		// regardless of any incidental suffix collisions.
		cfg := &config.Config{Devices: map[string]config.Device{
			"living-room-2": {Host: "1.1.1.2"},
		}}
		if got := uniqueAlias("living-room", cfg); got != "living-room" {
			t.Errorf("got %q, want living-room", got)
		}
	})
}
