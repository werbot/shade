package placeholder_test

import (
	"strings"
	"testing"

	"github.com/werbot/shade/internal/placeholder"
)

// maxTail mirrors flush.go's cap. It is a literal, not a reference, so the boundary test
// below pins the exact value the spec fixes (64) rather than following the constant it
// verifies.
const maxTail = 64

// TestFlusherEmitsEverythingBeforeTheLastAngle: text that cannot start a placeholder is
// handed on at once; only the tail from the last "<" stays buffered.
func TestFlusherEmitsEverythingBeforeTheLastAngle(t *testing.T) {
	f := placeholder.NewFlusher()
	if got := f.Write("ровный текст"); got != "ровный текст" {
		t.Fatalf("Write = %q, want the whole piece", got)
	}
	if got := f.Write(" и хвост <"); got != " и хвост " {
		t.Fatalf("Write = %q, want everything before %q", got, "<")
	}
}

// TestFlusherHoldsAnUnfinishedPlaceholder: a piece that ends inside a token leaks nothing —
// the partial token waits in the buffer.
func TestFlusherHoldsAnUnfinishedPlaceholder(t *testing.T) {
	f := placeholder.NewFlusher()
	if got := f.Write("Contact: <EMAIL_1"); got != "Contact: " {
		t.Fatalf("Write = %q, want %q — the partial token must not be emitted", got, "Contact: ")
	}
}

// TestPlaceholderSplitAtEveryPositionIsRestored: whatever the stream boundary, the pieces
// plus the flush rebuild the text byte for byte. The text carries multibyte runes and an
// emoji, so a cut can land inside a rune and the buffer must still not reorder or drop
// bytes. Every cut is covered, not two hand-picked ones.
func TestPlaceholderSplitAtEveryPositionIsRestored(t *testing.T) {
	const text = "привет 👋 <EMAIL_1> мир"
	for cut := 0; cut <= len(text); cut++ {
		f := placeholder.NewFlusher()
		got := f.Write(text[:cut]) + f.Write(text[cut:])
		out, _ := f.Flush()
		if got+out != text {
			t.Fatalf("cut at %d: got %q, want %q", cut, got+out, text)
		}
	}
}

// TestFlusherGivesUpOnATailThatIsTooLong: an unclosed "<" past the cap cannot be a token, so
// Write stops waiting for ">" and emits it as is. Both sides of the boundary are pinned, so
// an off-by-one in the cap arm cannot pass unnoticed.
func TestFlusherGivesUpOnATailThatIsTooLong(t *testing.T) {
	cases := []struct {
		name    string
		tailLen int
		emitted bool
	}{
		{"at the cap", maxTail, false},
		{"past the cap", maxTail + 1, true},
	}
	for _, c := range cases {
		tail := "<" + strings.Repeat("E", c.tailLen-1)
		f := placeholder.NewFlusher()
		got := f.Write(tail)
		want := ""
		if c.emitted {
			want = tail
		}
		if got != want {
			t.Fatalf("%s (tail %d bytes): Write = %q, want %q", c.name, c.tailLen, got, want)
		}
	}
}

// TestFlushWithholdsAPlaceholderPrefix: the unfinished prefix is the one thing this buffer
// exists to keep off the wire — a raw placeholder on screen.
func TestFlushWithholdsAPlaceholderPrefix(t *testing.T) {
	f := placeholder.NewFlusher()
	if got := f.Write("token <EMAIL_1"); got != "token " {
		t.Fatalf("Write = %q, want %q", got, "token ")
	}
	out, withheld := f.Flush()
	if out != "" || withheld != len("<EMAIL_1") {
		t.Fatalf("Flush = %q, %d; want %q, %d", out, withheld, "", len("<EMAIL_1"))
	}
}

// TestFlushWithholdsATailWhateverItsShape: the spec never flushes an unclosed "<" — the
// tail from it waits for ">" or for the length cap. So a non-empty buffer is withheld
// whatever it looks like, including one that could never become a placeholder ("</b",
// "<EM", a bare "<"). This is the spec's rule, not an accident; a tail that did carry a
// closing ">" never reaches Flush, because Write hands it on the moment it closes.
func TestFlushWithholdsATailWhateverItsShape(t *testing.T) {
	for _, tail := range []string{"</b", "<EM", "<"} {
		f := placeholder.NewFlusher()
		if got := f.Write("bold" + tail); got != "bold" {
			t.Fatalf("tail %q: Write = %q, want %q", tail, got, "bold")
		}
		out, withheld := f.Flush()
		if out != "" || withheld != len(tail) {
			t.Fatalf("tail %q: Flush = %q, %d; want %q, %d", tail, out, withheld, "", len(tail))
		}
	}
}

// TestFlusherKeepsNonASCIITextByteForByte: the buffer is a byte window, not a rune one; text
// with no bracket passes through untouched and the empty flush adds nothing.
func TestFlusherKeepsNonASCIITextByteForByte(t *testing.T) {
	const text = "привет 👋 мир"
	f := placeholder.NewFlusher()
	if got := f.Write(text); got != text {
		t.Fatalf("Write = %q, want %q", got, text)
	}
	out, withheld := f.Flush()
	if out != "" || withheld != 0 {
		t.Fatalf("Flush = %q, %d; want %q, 0", out, withheld, "")
	}
}
