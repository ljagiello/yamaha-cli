package yxc

import "sort"

// Levenshtein returns the edit distance between a and b.
//
// Cost is 1 per insertion, deletion, or substitution. The implementation
// uses two rolling rows so memory is O(min(len(a), len(b))).
func Levenshtein(a, b string) int {
	// Operate on rune slices so multi-byte characters count as one edit.
	ar := []rune(a)
	br := []rune(b)
	if len(ar) < len(br) {
		ar, br = br, ar
	}
	if len(br) == 0 {
		return len(ar)
	}

	prev := make([]int, len(br)+1)
	curr := make([]int, len(br)+1)
	for j := range prev {
		prev[j] = j
	}

	for i := 1; i <= len(ar); i++ {
		curr[0] = i
		for j := 1; j <= len(br); j++ {
			cost := 1
			if ar[i-1] == br[j-1] {
				cost = 0
			}
			del := prev[j] + 1
			ins := curr[j-1] + 1
			sub := prev[j-1] + cost
			curr[j] = min3(del, ins, sub)
		}
		prev, curr = curr, prev
	}
	return prev[len(br)]
}

func min3(a, b, c int) int {
	m := a
	if b < m {
		m = b
	}
	if c < m {
		m = c
	}
	return m
}

// DidYouMean returns the n closest candidates to unknown, sorted by
// ascending Levenshtein distance. Ties are broken by alphabetical order
// for determinism. If n <= 0 or candidates is empty, returns nil.
func DidYouMean(unknown string, candidates []string, n int) []string {
	if n <= 0 || len(candidates) == 0 {
		return nil
	}
	type scored struct {
		s string
		d int
	}
	all := make([]scored, 0, len(candidates))
	for _, c := range candidates {
		all = append(all, scored{s: c, d: Levenshtein(unknown, c)})
	}
	sort.SliceStable(all, func(i, j int) bool {
		if all[i].d != all[j].d {
			return all[i].d < all[j].d
		}
		return all[i].s < all[j].s
	})
	if n > len(all) {
		n = len(all)
	}
	out := make([]string, n)
	for i := 0; i < n; i++ {
		out[i] = all[i].s
	}
	return out
}
