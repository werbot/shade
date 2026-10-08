package rules

import "math"

// Shannon computes the Shannon entropy of a string in bits per byte: for a string of one
// repeated byte it is zero, for a string of n distinct bytes it is log2(n).
// It is computed over bytes, not runes: it is compared with the threshold of the rule,
// which is set for a «random» ASCII secret.
func Shannon(s string) float64 {
	var counts [256]int
	for i := 0; i < len(s); i++ {
		counts[s[i]]++
	}
	total := float64(len(s))
	var h float64
	for _, c := range counts {
		if c == 0 {
			continue
		}
		p := float64(c) / total
		h -= p * math.Log2(p)
	}
	return h
}
