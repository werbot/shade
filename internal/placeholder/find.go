package placeholder

import (
	"regexp"
	"strconv"
	"strings"
)

// findRe — the canonical form of a token: <TYPE_N>, N is a positive integer with no
// leading zeros.
var findRe = regexp.MustCompile(`<(` + typesAlt + `)_([1-9][0-9]*)>`)

// normalizedRe — the six deformations from the spec, item 7: HTML-escape, fullwidth
// brackets, spaces and a newline inside the token, lowercase, truncation at
// max_tokens (there is no closing bracket — the end of the token is marked by a word boundary).
// Spaces are allowed only before a real bracket, otherwise the trailing space
// of a truncated token would end up in Raw. Only the type is case-insensitive: the brackets and
// the prefixes of HTML entities must match exactly.
//
// ponytail: \b in Go is ASCII-only, so a truncated <KEY_1, glued without
// space with a Cyrillic word would also pass for a token. We change it to an explicit
// a terminator if such false positives start getting in the way.
var normalizedRe = regexp.MustCompile(
	`(?:&lt;|＜|<)\s*((?i:` + typesAlt + `))\s*_\s*([1-9][0-9]*)(?:\s*(?:&gt;|＞|>)|\b)`)

// Find looks only for the canonical form of a token — the first tier of restoration,
// an exact match without deformations.
func Find(text string) []Token {
	return toTokens(text, findRe.FindAllStringSubmatchIndex(text, -1))
}

// FindNormalized looks for tokens, surviving the deformations of the model. The match bounds are
// positions in the source text (the regexp runs over it too), so the substitution
// during restoration runs over the live text, not over the normalized copy.
func FindNormalized(text string) []Token {
	return toTokens(text, normalizedRe.FindAllStringSubmatchIndex(text, -1))
}

// toTokens parses the results of both regexps: they have two groups each — type
// and number.
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
