package main

import (
	"runtime/debug"
	"testing"
)

// TestResolveVersion covers both version sources: the goreleaser -ldflags
// stamp ("0.1.0") and the module version Go embeds for `go install …@vX.Y.Z`
// ("v0.1.0"). Both must print the same shape.
func TestResolveVersion(t *testing.T) {
	buildInfo := func(v string) *debug.BuildInfo {
		return &debug.BuildInfo{Main: debug.Module{Version: v}}
	}

	cases := []struct {
		name    string
		stamped string
		bi      *debug.BuildInfo
		want    string
	}{
		{"stamped-wins-over-build-info", "0.1.0", buildInfo("v9.9.9"), "0.1.0"},
		{"stamped-with-v", "v0.1.0", buildInfo("v9.9.9"), "0.1.0"},
		{"build-info-tag", "dev", buildInfo("v0.1.0"), "0.1.0"},
		{"build-info-pseudo-version", "dev", buildInfo("v0.0.0-20261005120000-abcdef123456"), "0.0.0-20261005120000-abcdef123456"},
		{"build-info-dirty", "dev", buildInfo("v0.0.0-20261005120000-abcdef123456+dirty"), "0.0.0-20261005120000-abcdef123456+dirty"},
		{"build-info-devel", "dev", buildInfo("(devel)"), "dev"},
		{"build-info-empty", "dev", buildInfo(""), "dev"},
		{"no-build-info", "dev", nil, "dev"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := resolveVersion(tc.stamped, tc.bi); got != tc.want {
				t.Errorf("resolveVersion(%q, …) = %q, want %q", tc.stamped, got, tc.want)
			}
		})
	}
}
