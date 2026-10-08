package placeholder_test

import (
	"testing"

	"github.com/werbot/shade/internal/placeholder"
)

// The canonical form is a special case of the normalized search, so the three
// properties of the former strict Find are checked here on FindNormalized.
func TestFindNormalizedAcceptsKnownTypesOnly(t *testing.T) {
	got := placeholder.FindNormalized("см. <EMAIL_1> и <div_1> и <MyClass_2>")
	if len(got) != 1 || got[0].Type != "EMAIL" || got[0].N != 1 {
		t.Fatalf("got %+v", got)
	}
}

func TestFindNormalizedRejectsLeadingZeros(t *testing.T) {
	if got := placeholder.FindNormalized("<HOST_01>"); len(got) != 0 {
		t.Fatalf("got %+v", got)
	}
}

func TestFindNormalizedMatchesWholeToken(t *testing.T) {
	got := placeholder.FindNormalized("a <SECRET_11> b")
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

// TestFindNormalizedRejectsTokenGluedToWord: truncation at max_tokens is the end
// the token, not a junction with a letter. \b in Go knows only the ASCII word, so
// a Cyrillic letter right next to the number does not count as a boundary: without protection
// tier 2 would substitute the value into text where the token was never written.
func TestFindNormalizedRejectsTokenGluedToWord(t *testing.T) {
	for _, s := range []string{
		"<KEY_1яяя",
		"файл <PATH_2пример",
		"<KEY_1abc",
		"<KEY_1_abc",
		"<KEY_1АБВ",
	} {
		if got := placeholder.FindNormalized(s); len(got) != 0 {
			t.Fatalf("%q: gluing to a letter was taken for a token: %+v", s, got)
		}
	}
}

// TestFindNormalizedKeepsTruncationAtBoundary: truncation at max_tokens at the end
// string and a token followed by a separator must match.
func TestFindNormalizedKeepsTruncationAtBoundary(t *testing.T) {
	cases := []struct {
		in   string
		raws []string
	}{
		{"<KEY_1", []string{"<KEY_1"}},
		{"<KEY_1 <SECRET_2", []string{"<KEY_1", "<SECRET_2"}},
		{"<KEY_1яяя и <SECRET_2", []string{"<SECRET_2"}},
		{"<KEY_1, затем текст", []string{"<KEY_1"}},
		{"хвост <KEY_1\n", []string{"<KEY_1"}},
	}
	for _, c := range cases {
		got := placeholder.FindNormalized(c.in)
		if len(got) != len(c.raws) {
			t.Fatalf("%q: got %+v, want %d tokens", c.in, got, len(c.raws))
		}
		for i, raw := range c.raws {
			if got[i].Raw != raw {
				t.Fatalf("%q: token %d = %q, want %q", c.in, i, got[i].Raw, raw)
			}
			if c.in[got[i].Start:got[i].End] != raw {
				t.Fatalf("%q: bounds %d..%d do not give %q", c.in, got[i].Start, got[i].End, raw)
			}
		}
	}
}

// TestFindNormalizedKeepsClosedTokenNearCyrillic: the closing bracket closes
// token, and Cyrillic right after it is no longer a gluing: dropGlued only concerns
// truncation at max_tokens, where the token is closed by a word boundary.
func TestFindNormalizedKeepsClosedTokenNearCyrillic(t *testing.T) {
	got := placeholder.FindNormalized("файл <KEY_1>яяя")
	if len(got) != 1 || got[0].Raw != "<KEY_1>" {
		t.Fatalf("got %+v", got)
	}
}
