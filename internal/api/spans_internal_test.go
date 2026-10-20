package api

import (
	"slices"
	"testing"

	"github.com/werbot/shade/internal/rules"
)

// wide has a rune of each width, as bytes and as UTF-16 units:
//
//	a  [0,1) bytes, [0,1) units
//	б  [1,3) bytes, [1,2) units
//	😀 [3,7) bytes, [2,4) units
//	z  [7,8) bytes, [4,5) units
const wide = "aб😀z"

// An offset of the matcher lies on the edge of a rune, because a regexp works on runes, so the
// requests cannot show what the conversion does with one that does not. The conversion must round
// it up to the end of its rune and must not fail, and the same goes for an offset past the text.
func TestUTF16SpansConvertsByteOffsets(t *testing.T) {
	span := func(start, end int) rules.Span { return rules.Span{Start: start, End: end, Type: "T", Rule: "r"} }
	view := func(start, end int) spanView { return spanView{Start: start, End: end, Type: "T", Rule: "r"} }

	for _, tc := range []struct {
		name string
		in   []rules.Span
		want []spanView
	}{
		{"on the edges of the runes, adjacent",
			[]rules.Span{span(0, 1), span(1, 3), span(3, 7), span(7, 8)},
			[]spanView{view(0, 1), view(1, 2), view(2, 4), view(4, 5)}},
		{"inside a rune, rounded up to its end",
			[]rules.Span{span(2, 5)},
			[]spanView{view(2, 4)}},
		{"past the end of the text",
			[]rules.Span{span(7, 100), span(120, 130)},
			[]spanView{view(4, 5), view(5, 5)}},
		{"no spans", nil, []spanView{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := utf16Spans(wide, tc.in)
			if got == nil {
				t.Fatal("the result is nil, want a slice: nil is marshalled as null")
			}
			if !slices.Equal(got, tc.want) {
				t.Errorf("got %+v, want %+v", got, tc.want)
			}
		})
	}
}
