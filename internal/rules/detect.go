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
		for _, m := range matches(r, text) {
			start, end, ok := bounds(m, r.SecretGroup)
			if !ok {
				continue
			}
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

// matches finds the matches of a rule, continuing the search from the end of the secret group,
// and not from the end of the whole match.
//
// For rules with a consuming trailing context (boundaries instead of lookaround,
// which RE2 lacks) the end of the match lies past the separator: `card` with
// `(?:[^\d.]|$)` eats the comma between numbers, `assignment` — the space after
// the value. FindAll would continue the search after this character, and the second secret across
// one separator would stay unnoticed. The end of the group, when there is no
// the trailing context coincides with the end of the match, so for the remaining
// rules the behaviour is the same.
//
// ponytail: the search window starts one character before resume — the leading context of the rule
// ((?:^|[^\w]), \b) needs a character before the match, — so on the rule
// costs O(number of matches × length of the text). For tool output this
// is enough; on streaming megabytes, go back to a single FindAll plus a point
// check of the tails.
func matches(r Rule, text string) [][]int {
	var out [][]int
	for resume := 0; resume <= len(text); {
		m := nextMatch(r.re, text, max(0, resume-1))
		if m == nil {
			break
		}
		// The next search must start past the previous one: without that an empty
		// or a missing group would spin the loop in place.
		_, end, ok := bounds(m, r.SecretGroup)
		if !ok || end <= resume {
			end = max(m[1], resume+1)
		}
		out = append(out, m)
		resume = end
	}
	return out
}

// bounds returns the bounds of the secret group in the match. ok = false if the group is not
// in the pattern, it did not take part in the match, or it is empty — there is nothing to mask.
func bounds(m []int, group int) (start, end int, ok bool) {
	g := 2 * group
	if g+1 >= len(m) || m[g] < 0 || m[g] == m[g+1] {
		return 0, 0, false
	}
	return m[g], m[g+1], true
}

// nextMatch returns the first match of the text starting no earlier than base.
//
// A match exactly at the window boundary is dropped and the window shifts: `^` there
// would match the start of the slice rather than of the text. The dropped match starts
// inside the previous group (one character to the left), that is, it is already covered —
// FindAll did not return it either. The shift does not lose matches from the same window:
// the FindAll list does not overlap and goes in ascending start.
func nextMatch(re *regexp.Regexp, text string, base int) []int {
	for ; base <= len(text); base++ {
		for _, m := range re.FindAllStringSubmatchIndex(text[base:], -1) {
			if base > 0 && m[0] == 0 {
				continue
			}
			for i, off := range m {
				if off >= 0 {
					m[i] = off + base
				}
			}
			return m
		}
	}
	return nil
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
