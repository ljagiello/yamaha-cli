package cli

import "testing"

func TestCanonicalZone(t *testing.T) {
	ok := []struct {
		in   string
		want string
	}{
		{"main", "main"},
		{"MAIN", "main"},
		{" zone2 ", "zone2"},
		{"Zone3", "zone3"},
		{"ZONE4", "zone4"},
	}
	for _, tc := range ok {
		got, err := canonicalZone(tc.in)
		if err != nil {
			t.Errorf("canonicalZone(%q) unexpected error: %v", tc.in, err)
			continue
		}
		if got != tc.want {
			t.Errorf("canonicalZone(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}

	for _, bad := range []string{"", "zone9", "zone", "kitchen", "main2"} {
		if _, err := canonicalZone(bad); err == nil {
			t.Errorf("canonicalZone(%q): expected error, got nil", bad)
		} else if ErrorExitCode(err) != 2 {
			t.Errorf("canonicalZone(%q): exit code = %d, want 2 (usage)", bad, ErrorExitCode(err))
		}
	}
}
