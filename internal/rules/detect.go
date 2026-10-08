package rules

import (
	"regexp"
	"slices"
	"strings"
)

// Span is a fragment found: byte bounds in the source text, the
// the placeholder and the name of the rule that found it.
type Span struct {
	Start int
	End   int
	Type  string
	Rule  string
}

// Detect finds all rule matches in the text and returns non-overlapping
// spans in the order they follow in the text.
//
// Rules are applied from specific to general (OrderIdx ascending), and this
// same order serves as the priority on overlap: Merge walks the spans,
// sorted stably by Start, so with an equal start the first one is — and
// hence also the winner — the more specific rule goes.
func Detect(text string, rs []Rule) []Span {
	ordered := slices.Clone(rs)
	slices.SortStableFunc(ordered, func(a, b Rule) int { return a.OrderIdx - b.OrderIdx })

	// The text is lowercased once per call: keywords are a
	// a cheap prefilter, and its cost must not depend on the number of rules.
	lower := strings.ToLower(text)

	var spans []Span
	for _, r := range ordered {
		if !hasKeyword(lower, r.Keywords) {
			continue
		}
		for _, m := range r.re.FindAllStringSubmatchIndex(text, -1) {
			g := 2 * r.SecretGroup
			// A group outside the pattern or one that did not take part in the match gives
			// no bounds; zero-length bounds mean an empty match.
			if g+1 >= len(m) || m[g] < 0 || m[g] == m[g+1] {
				continue
			}
			start, end := m[g], m[g+1]
			matched := text[start:end]
			if r.Kind == "entropy" && Shannon(matched) < r.EntropyMin {
				continue
			}
			if !Validate(r.Validator, matched) {
				continue
			}
			if slices.ContainsFunc(r.allow, func(a *regexp.Regexp) bool { return a.MatchString(matched) }) {
				continue
			}
			spans = append(spans, Span{Start: start, End: end, Type: r.Type, Rule: r.Name})
		}
	}
	return Merge(spans)
}

// hasKeyword reports whether the text passes the prefilter of the rule. An empty
// keywords list imposes no prefilter.
func hasKeyword(lower string, keywords []string) bool {
	if len(keywords) == 0 {
		return true
	}
	for _, k := range keywords {
		if strings.Contains(lower, strings.ToLower(k)) {
			return true
		}
	}
	return false
}

// Merge collapses overlapping spans. The sort is stable by Start:
// with equal bounds the priority is set by the order of the slice, that is the more
// the specific rule. The winning span absorbs the overlapped one entirely — by
// the bounds spans are not cut, otherwise in place of one secret there would be two
// stubs.
func Merge(spans []Span) []Span {
	ordered := slices.Clone(spans)
	slices.SortStableFunc(ordered, func(a, b Span) int { return a.Start - b.Start })

	var out []Span
	for _, s := range ordered {
		if n := len(out); n > 0 && s.Start < out[n-1].End {
			continue
		}
		out = append(out, s)
	}
	return out
}
