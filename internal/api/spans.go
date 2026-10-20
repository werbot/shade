package api

import (
	"unicode/utf16"
	"unicode/utf8"

	"github.com/werbot/shade/internal/rules"
)

// spanView is a fragment in the units the page slices by: UTF-16 code units.
type spanView struct {
	Start int    `json:"start"`
	End   int    `json:"end"`
	Type  string `json:"type"`
	Rule  string `json:"rule"`
}

// utf16Spans converts byte-offset spans over text in one pass. The spans are ascending
// and non-overlapping, as rules.Detect returns them. The result is never nil, so the page reads
// [] and not null.
//
// An offset inside a rune cannot come from a regexp, which works on runes. One that does is
// rounded up to the end of its rune, and one past the end of the text stops at the end: neither
// fails.
func utf16Spans(text string, spans []rules.Span) []spanView {
	out := make([]spanView, 0, len(spans))
	pos, units := 0, 0 // the cursor: a byte offset into text, and the UTF-16 units before it
	// at moves the cursor to the first rune edge at or after off and returns its UTF-16 position.
	at := func(off int) int {
		for pos < off && pos < len(text) {
			r, size := utf8.DecodeRuneInString(text[pos:])
			pos += size
			units += utf16.RuneLen(r)
		}
		return units
	}
	for _, s := range spans {
		start := at(s.Start)
		out = append(out, spanView{Start: start, End: at(s.End), Type: s.Type, Rule: s.Rule})
	}
	return out
}
