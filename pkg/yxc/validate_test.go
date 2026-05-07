package yxc

import (
	"reflect"
	"testing"
)

func TestLevenshtein(t *testing.T) {
	cases := []struct {
		a, b string
		want int
	}{
		{"", "", 0},
		{"abc", "abc", 0},
		{"abc", "", 3},
		{"", "abc", 3},
		{"kitten", "sitting", 3},
		{"hdmi2", "hdm2", 1},
		{"hdmi2", "hdmi1", 1},
		{"hdmi2", "tuner", 5},
		{"flaw", "lawn", 2},
	}
	for _, tc := range cases {
		if got := Levenshtein(tc.a, tc.b); got != tc.want {
			t.Errorf("Levenshtein(%q, %q) = %d, want %d", tc.a, tc.b, got, tc.want)
		}
	}
}

func TestDidYouMean(t *testing.T) {
	candidates := []string{"hdmi1", "hdmi2", "hdmi3", "tuner"}

	got := DidYouMean("hdm2", candidates, 3)
	want := []string{"hdmi2", "hdmi1", "hdmi3"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("DidYouMean(hdm2): got %v want %v", got, want)
	}

	// n larger than the candidate list returns everything.
	got = DidYouMean("hdmi", candidates, 10)
	if len(got) != 4 {
		t.Errorf("DidYouMean truncation: got %d want 4 (%v)", len(got), got)
	}

	// Empty / zero inputs.
	if DidYouMean("anything", nil, 3) != nil {
		t.Error("nil candidates should return nil")
	}
	if DidYouMean("anything", candidates, 0) != nil {
		t.Error("n=0 should return nil")
	}
}

func TestDidYouMean_Stable(t *testing.T) {
	// Two candidates equidistant from the input — alphabetical tiebreak.
	got := DidYouMean("ab", []string{"zz", "aa", "bb", "cc"}, 4)
	// distances: zz=2, aa=1, bb=1, cc=2 → bb,aa first by distance, then
	// alphabetical: aa, bb, cc, zz.
	want := []string{"aa", "bb", "cc", "zz"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("stability: got %v want %v", got, want)
	}
}
