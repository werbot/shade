package placeholder

import (
	"regexp"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

// normalizedRe — the six deformations from the spec, item 7: HTML-escape, fullwidth
// brackets, spaces and a newline inside the token, lowercase, truncation at
// max_tokens (there is no closing bracket — the end of the token is marked by a word boundary).
// Spaces are allowed only before a real bracket, otherwise the trailing space
// of a truncated token would end up in Raw. Only the type is case-insensitive: the brackets and
// the prefixes of HTML entities must match exactly.
//
// The canonical form (the first tier of the spec) is a special case of this regexp, not
// a separate search: a separate strict regexp lived here for the sake of an "exact
// match", but no production caller called it, and its own
// guarantee (a closing bracket is mandatory) would contradict truncation at
// max_tokens. Tiers 1–2 are collapsed into a single pass.
//
// The third group is that very word boundary: it is empty when the token closed on \b,
// and does not participate when the closing bracket is in place. By it FindNormalized
// distinguishes truncation at max_tokens from a junction with a letter (see dropGlued).
var normalizedRe = regexp.MustCompile(
	`(?:&lt;|＜|<)\s*((?i:` + typesAlt + `))\s*_\s*([1-9][0-9]*)(?:\s*(?:&gt;|＞|>)|(\b))`)

// FindNormalized looks for tokens, surviving the deformations of the model. The match bounds are
// positions in the source text (the regexp runs over it too), so the substitution
// during restoration runs over the live text, not over the normalized copy.
func FindNormalized(text string) []Token {
	locs := normalizedRe.FindAllStringSubmatchIndex(text, -1)
	return toTokens(text, dropGlued(text, locs))
}

// dropGlued discards truncated tokens glued to a letter or a digit.
// \b in Go is defined via ASCII \w, for it a Cyrillic letter is not a word,
// so <KEY_1 in "<KEY_1яяя" would look like the end of a token. Accepting such a match
// means substituting someone else's value (or blocking the response via unresolved)
// where the token was never written at all.
func dropGlued(text string, locs [][]int) [][]int {
	kept := locs[:0]
	for _, loc := range locs {
		// loc[6] >= 0 — the match closed on a word boundary, not on a bracket.
		if loc[6] >= 0 && gluedToWord(text, loc[1]) {
			continue
		}
		kept = append(kept, loc)
	}
	return kept
}

// gluedToWord reports that right after the match comes a letter or a digit of any
// alphabet — that is, the token did not break off but merged with the word.
func gluedToWord(text string, end int) bool {
	if end >= len(text) {
		return false
	}
	r, _ := utf8.DecodeRuneInString(text[end:])
	return unicode.IsLetter(r) || unicode.IsDigit(r)
}

// toTokens parses the search results: the regexp has two groups — type and number.
func toTokens(text string, locs [][]int) []Token {
	if len(locs) == 0 {
		return nil
	}
	toks := make([]Token, 0, len(locs))
	for _, loc := range locs {
		n, err := strconv.Atoi(text[loc[4]:loc[5]])
		if err != nil {
			// The number is longer than int — the token is not ours, better to skip than to guess.
			continue
		}
		toks = append(toks, Token{
			Type:  strings.ToUpper(text[loc[2]:loc[3]]),
			N:     n,
			Start: loc[0],
			End:   loc[1],
			Raw:   text[loc[0]:loc[1]],
		})
	}
	return toks
}
