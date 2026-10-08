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
// Rules are applied from specific to general (OrderIdx ascending), and in
// the same order their matches land in the slice, that is, the more specific
// rule earlier in it, and, by the Merge contract, more important on overlap.
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

// Merge collapses overlapping spans.
//
// Input contract: the order of the slice sets the priority — earlier wins
// (Detect puts spans in the order of the rules, from specific to general). The contract
// output: spans do not overlap and go in ascending Start.
//
// The intersection of two spans gives the union of the bounds. The loser by priority
// decides not which bytes stay open, but only which type
// will get a placeholder: it cannot be dropped, because beyond the bounds
// the winner there would remain bytes that the engine already deemed sensitive, and
// the anonymizer is the last barrier before the model. Adjacent spans (Start == End
// the previous one) do not overlap and are not merged.
func Merge(spans []Span) []Span {
	// The position in the input slice is the priority. The sort by Start
	// is stable, so the order of spans equal by Start is preserved, but with different
	// Start the original order is lost, and the priority has to be carried along.
	type ranked struct {
		span Span
		pos  int
	}
	rankedSpans := make([]ranked, len(spans))
	for i, s := range spans {
		rankedSpans[i] = ranked{span: s, pos: i}
	}
	slices.SortStableFunc(rankedSpans, func(a, b ranked) int { return a.span.Start - b.span.Start })

	var out []ranked
	for _, r := range rankedSpans {
		last := len(out) - 1
		if last < 0 || r.span.Start >= out[last].span.End {
			out = append(out, r)
			continue
		}
		winner := out[last]
		// Start does not need to be extended: the sort guarantees it is already
		// the minimum in the group.
		if r.span.End > winner.span.End {
			winner.span.End = r.span.End
		}
		if r.pos < winner.pos {
			winner.pos, winner.span.Type, winner.span.Rule = r.pos, r.span.Type, r.span.Rule
		}
		out[last] = winner
	}

	merged := make([]Span, len(out))
	for i, r := range out {
		merged[i] = r.span
	}
	return merged
}
