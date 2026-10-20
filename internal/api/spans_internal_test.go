package api

import (
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/werbot/shade/internal/rules"
)

// wide has a rune of each width, as bytes and as UTF-16 units. U+FFFD stands for the three-byte
// runes, the width of CJK text.
//
//	a      [0,1) bytes,   [0,1) units
//	б      [1,3) bytes,   [1,2) units
//	U+FFFD [3,6) bytes,   [2,3) units
//	😀     [6,10) bytes,  [3,5) units
//	z      [10,11) bytes, [5,6) units
const wide = "aб\uFFFD😀z"

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
			[]rules.Span{span(0, 1), span(1, 3), span(3, 6), span(6, 10), span(10, 11)},
			[]spanView{view(0, 1), view(1, 2), view(2, 3), view(3, 5), view(5, 6)}},
		{"inside a two-byte and a four-byte rune, rounded up to the end",
			[]rules.Span{span(2, 8)},
			[]spanView{view(2, 5)}},
		{"inside a three-byte rune, rounded up to the end",
			[]rules.Span{span(1, 4)},
			[]spanView{view(1, 3)}},
		{"past the end of the text",
			[]rules.Span{span(10, 100), span(120, 130)},
			[]spanView{view(5, 6), view(6, 6)}},
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

// The conversion walks the text once. For n one-byte spans over n bytes that takes milliseconds,
// even under the race detector, while a cursor that started over for each span would walk n*n/2
// bytes, which takes tens of seconds at this size. The limit sits between the two, far from both,
// so a slow machine does not reach it and the quadratic walk is well past it.
func TestUTF16SpansWalksTheTextOnce(t *testing.T) {
	const n = 1 << 18
	text := strings.Repeat("a", n)
	spans := make([]rules.Span, n)
	for i := range spans {
		spans[i] = rules.Span{Start: i, End: i + 1}
	}

	done := make(chan int, 1)
	go func() { done <- len(utf16Spans(text, spans)) }()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatalf("%d one-byte spans over a text of %d bytes took more than 3s: the text is not walked once", n, n)
	}
}
