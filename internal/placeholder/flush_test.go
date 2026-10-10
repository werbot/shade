package placeholder_test

import (
	"strings"
	"testing"

	"github.com/werbot/shade/internal/placeholder"
)

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
// emoji, so a cut can land inside a rune and the buffer must still not reorder or drop bytes.
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

// TestFlusherGivesUpOnATailThatIsTooLong: a tail past the cap cannot be a token, so it is
// emitted as is instead of being held forever.
func TestFlusherGivesUpOnATailThatIsTooLong(t *testing.T) {
	f := placeholder.NewFlusher()
	tail := "<" + strings.Repeat("E", 100)
	if got := f.Write(tail); got != tail {
		t.Fatalf("Write = %q, want the whole long tail", got)
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

// TestFlushEmitsATailThatIsNotAPlaceholderPrefix: the tail broke off before it could be a
// placeholder — after "/" no type can follow — so it is ordinary text, not a withheld prefix.
func TestFlushEmitsATailThatIsNotAPlaceholderPrefix(t *testing.T) {
	f := placeholder.NewFlusher()
	if got := f.Write("bold</b>"); got != "bold" {
		t.Fatalf("Write = %q, want %q", got, "bold")
	}
	out, withheld := f.Flush()
	if out != "</b>" || withheld != 0 {
		t.Fatalf("Flush = %q, %d; want %q, 0", out, withheld, "</b>")
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
