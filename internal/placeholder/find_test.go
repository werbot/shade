package placeholder_test

import (
	"testing"

	"github.com/werbot/shade/internal/placeholder"
)

func TestFindAcceptsKnownTypesOnly(t *testing.T) {
	got := placeholder.Find("см. <EMAIL_1> и <div_1> и <MyClass_2>")
	if len(got) != 1 || got[0].Type != "EMAIL" || got[0].N != 1 {
		t.Fatalf("got %+v", got)
	}
}

func TestFindRejectsLeadingZeros(t *testing.T) {
	if got := placeholder.Find("<HOST_01>"); len(got) != 0 {
		t.Fatalf("got %+v", got)
	}
}

func TestFindDoesNotMatchSubstringPlaceholder(t *testing.T) {
	got := placeholder.Find("a <SECRET_11> b")
	if len(got) != 1 || got[0].N != 11 {
		t.Fatalf("must match <SECRET_11> whole, got %+v", got)
	}
}

func TestFindNormalizedVariants(t *testing.T) {
	cases := []struct{ in, raw string }{
		{"&lt;PERSON_1&gt;", "&lt;PERSON_1&gt;"},
		{"< PERSON_1 >", "< PERSON_1 >"},
		{"<person_1>", "<person_1>"},
		{"<PERSON_1\n>", "<PERSON_1\n>"},
		{"truncated <PERSON_1", "<PERSON_1"},
		{"fullwidth ＜PERSON_1＞", "＜PERSON_1＞"},
	}
	for _, c := range cases {
		got := placeholder.FindNormalized(c.in)
		if len(got) != 1 || got[0].Type != "PERSON" || got[0].N != 1 {
			t.Fatalf("%q: got %+v", c.in, got)
		}
		// the bounds must point into the source text
		if c.in[got[0].Start:got[0].End] != c.raw {
			t.Fatalf("%q: span %d..%d = %q, want %q", c.in, got[0].Start, got[0].End,
				c.in[got[0].Start:got[0].End], c.raw)
		}
	}
}

func TestFindNormalizedIgnoresOrdinaryText(t *testing.T) {
	for _, s := range []string{"a < b", "<div>", "1 < 2 > 0", "<PERSON_1x>"} {
		if got := placeholder.FindNormalized(s); len(got) != 0 {
			t.Fatalf("%q: false positive %+v", s, got)
		}
	}
}

// TestFindNormalizedOffsetsAreBytes checks the key property of the bounds: Start and
// End — byte positions in the source string. Cyrillic and emoji take more
// than one byte, so counting by runes would diverge from text[Start:End].
func TestFindNormalizedOffsetsAreBytes(t *testing.T) {
	text := "Привет, <PERSON_1>! Токен: &lt;EMAIL_2&gt; ок ✅ < HOST_3 >"
	want := []struct {
		raw, typ string
		n        int
	}{
		{"<PERSON_1>", "PERSON", 1},
		{"&lt;EMAIL_2&gt;", "EMAIL", 2},
		{"< HOST_3 >", "HOST", 3},
	}
	got := placeholder.FindNormalized(text)
	if len(got) != len(want) {
		t.Fatalf("found %d tokens, want %d: %+v", len(got), len(want), got)
	}
	for i, w := range want {
		tok := got[i]
		if tok.Type != w.typ || tok.N != w.n || tok.Raw != w.raw {
			t.Fatalf("token %d: %+v, want %s/%d %q", i, tok, w.typ, w.n, w.raw)
		}
		if tok.Start < 0 || tok.End < tok.Start || tok.End > len(text) {
			t.Fatalf("token %d: bounds %d..%d outside text of length %d", i, tok.Start, tok.End, len(text))
		}
		if s := text[tok.Start:tok.End]; s != w.raw {
			t.Fatalf("token %d: text[%d:%d] = %q, want %q (byte offsets, not runes)",
				i, tok.Start, tok.End, s, w.raw)
		}
	}
}

// TestFindOffsetsAreBytes checks the same for the canonical search.
func TestFindOffsetsAreBytes(t *testing.T) {
	text := "тут <KEY_7> и всё"
	got := placeholder.Find(text)
	if len(got) != 1 {
		t.Fatalf("got %+v", got)
	}
	if s := text[got[0].Start:got[0].End]; s != "<KEY_7>" {
		t.Fatalf("text[%d:%d] = %q", got[0].Start, got[0].End, s)
	}
}
