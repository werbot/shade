package placeholder

import (
	"strconv"
	"strings"
)

// Guard hides already existing placeholders from the anonymization rules: on input
// the tool output or the dialogue history, on output text without tokens and the function
// of returning them back. Both are hidden:
// otherwise a repeated run of the rules would turn <EMAIL_1> into <EMAIL_2>.
//
// If there are no placeholders — the common case — the source string and
// the identity function, without a single allocation.
func Guard(text string) (masked string, restore func(string) string) {
	toks := FindNormalized(text)
	if len(toks) == 0 {
		return text, identity
	}

	var b strings.Builder
	b.Grow(len(text) + 8*len(toks))
	raws := make([]string, 0, len(toks))
	prev := 0
	for _, tok := range toks {
		writeEscaped(&b, text[prev:tok.Start])
		b.WriteString(sentinel(len(raws)))
		raws = append(raws, tok.Raw)
		prev = tok.End
	}
	writeEscaped(&b, text[prev:])
	return b.String(), func(s string) string { return unguard(s, raws) }
}

// identity — a replacement for a guard that has nothing to hide.
func identity(s string) string { return s }

// sentinel builds the service substitution \x00S<n>\x00. NUL is chosen because
// it is absent in ordinary text: the rules will not parse the substitution as a phone or
// a ticket number.
func sentinel(n int) string {
	return "\x00S" + strconv.Itoa(n) + "\x00"
}

// writeEscaped writes a piece of text, doubling NUL: otherwise a literal \x00S0\x00
// from the input cannot be told apart from a substitution and restoration would invent a placeholder.
func writeEscaped(b *strings.Builder, s string) {
	b.WriteString(strings.ReplaceAll(s, "\x00", "\x00\x00"))
}

// unguard puts the placeholders back. Parsing goes character by character, not
// by a regexp replacement: a doubled NUL is a literal NUL, only
// only \x00S<number>\x00 with a number from the issued ones is counted.
//
// The fast exit on the absence of NUL is not a dead branch: restoration is also called on
// the text of the model's response, where there usually are no substitutions, and there an extra copy is pointless.
func unguard(masked string, raws []string) string {
	if !strings.Contains(masked, "\x00") {
		return masked
	}
	var b strings.Builder
	b.Grow(len(masked))
	for i := 0; i < len(masked); {
		if masked[i] != 0 {
			b.WriteByte(masked[i])
			i++
			continue
		}
		if i+1 < len(masked) && masked[i+1] == 0 {
			b.WriteByte(0)
			i += 2
			continue
		}
		n, end, ok := sentinelAt(masked, i)
		if !ok || n >= len(raws) {
			b.WriteByte(0)
			i++
			continue
		}
		b.WriteString(raws[n])
		i = end
	}
	return b.String()
}

// sentinelAt parses \x00S<number>\x00 from position i and returns the number and
// the position right after the closing NUL.
func sentinelAt(s string, i int) (n, end int, ok bool) {
	if i+1 >= len(s) || s[i+1] != 'S' {
		return 0, 0, false
	}
	digits := s[i+2:]
	k := 0
	for k < len(digits) && digits[k] >= '0' && digits[k] <= '9' {
		k++
	}
	if k == 0 || k >= len(digits) || digits[k] != 0 {
		return 0, 0, false
	}
	n, err := strconv.Atoi(digits[:k])
	if err != nil {
		return 0, 0, false
	}
	return n, i + 2 + k + 1, true
}
