package placeholder

import (
	"regexp"
	"strings"
)

// maxTail is the cap on the held-back tail. The longest legal token <PERSON_1> is 10
// bytes; 64 leaves room for the deformations normalizedRe tolerates (< PERSON_1 >,
// truncation at max_tokens). A longer tail cannot be a token and is emitted as is.
const maxTail = 64

// couldBePlaceholderRe matches a tail that could still grow into a placeholder: "<", an
// optional run of spaces, one of the types, then only what can precede the rest of the
// token — an optional "_" with an optional number and trailing spaces. A closing bracket
// is absent, so a *complete* token does not match: it is emitted for the caller to restore.
//
// The alternation is typesAlt, the same one normalizedRe uses, so the shape of a token
// lives in one place. Only a literal "<" is accepted: Write buffers a tail from the last
// "<" byte, so an HTML-escaped or full-width opening bracket can never start it.
var couldBePlaceholderRe = regexp.MustCompile(
	`^<\s*(?i:` + typesAlt + `)\s*(?:_\s*(?:[1-9][0-9]*)?\s*)?$`)

// couldBePlaceholder reports whether tail is an unfinished placeholder — a prefix of a
// token that the end of the stream cut off.
func couldBePlaceholder(tail string) bool {
	return couldBePlaceholderRe.MatchString(tail)
}

// Flusher holds back the tail of a text stream that could still become a placeholder.
// Everything left of the last "<" can no longer start one, so it is safe to hand on.
type Flusher struct{ buf string }

// NewFlusher returns an empty Flusher.
func NewFlusher() *Flusher { return &Flusher{} }

// Write accepts the next piece of the stream and returns the text that is safe to emit
// now. The tail from the last "<" stays buffered.
func (f *Flusher) Write(piece string) string {
	f.buf += piece
	i := strings.LastIndexByte(f.buf, '<')
	if i < 0 || len(f.buf)-i > maxTail {
		out := f.buf
		f.buf = ""
		return out
	}
	out := f.buf[:i]
	f.buf = f.buf[i:]
	return out
}

// Flush ends the stream. A tail that is still a plausible placeholder prefix is
// withheld — a raw placeholder on screen is what this buffer exists to prevent — and
// reported as a byte count for the diagnostic line. Any other tail is emitted.
func (f *Flusher) Flush() (out string, withheld int) {
	out, f.buf = f.buf, ""
	if couldBePlaceholder(out) {
		return "", len(out)
	}
	return out, 0
}
