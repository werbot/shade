package placeholder

import "strings"

// maxTail is the cap on the held-back tail. The longest legal token <PERSON_1> is 10
// bytes; 64 leaves room for the deformations normalizedRe tolerates (< PERSON_1 >,
// truncation at max_tokens). A longer tail cannot be a token and is emitted as is — the
// spec's own escape hatch for text that contains a "<" but is not a placeholder.
const maxTail = 64

// Flusher holds back the tail of a text stream that could still become a placeholder.
// The spec never flushes an unclosed "<": the tail from it waits for ">" — or for the
// length cap to rule it out — while everything left of the last "<" can no longer start a
// token and is safe to hand on.
type Flusher struct{ buf string }

// NewFlusher returns an empty Flusher.
func NewFlusher() *Flusher { return &Flusher{} }

// Write accepts the next piece of the stream and returns the text that is safe to emit
// now. The tail from the last "<" stays buffered while it is still open — no ">" follows it
// — and within maxTail; a closed token, or one past the cap, is emitted whole so the caller
// can restore it. As a result the buffer is always either empty or an unclosed "<"-tail
// that begins at the last "<", and Flush relies on exactly that.
func (f *Flusher) Write(piece string) string {
	f.buf += piece
	i := strings.LastIndexByte(f.buf, '<')
	if i >= 0 && len(f.buf)-i <= maxTail && strings.IndexByte(f.buf[i:], '>') < 0 {
		out := f.buf[:i]
		f.buf = f.buf[i:]
		return out
	}
	out := f.buf
	f.buf = ""
	return out
}

// Flush ends the stream. The spec never flushes an unclosed "<" — that is what keeps a raw
// placeholder fragment off the screen — and after Write the buffer is either empty or
// exactly such an unclosed "<"-tail. So a non-empty buffer is withheld: returned as "" and
// reported as its byte length for the diagnostic line. A legitimate-looking tail such as
// "</b" at the very end of a stream is withheld too — the documented trade of that rule,
// not an accident. Only an empty buffer flushes as empty.
func (f *Flusher) Flush() (out string, withheld int) {
	if f.buf == "" {
		return "", 0
	}
	withheld = len(f.buf)
	f.buf = ""
	return "", withheld
}
