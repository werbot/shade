// Package placeholder — the dictionary of shade placeholders: the canonical format
// <TYPE_N>, a closed set of types, token search in text and a guard that hides
// already existing placeholders from the anonymization rules.
package placeholder

import (
	"maps"
	"slices"
	"strconv"
	"strings"
)

// Types — the closed set of placeholder types. The list is given explicitly, not derived
// from "anything in uppercase": RE2 cannot do lookaround, and it is exactly
// the closedness of the set prevents taking ordinary text like <div_1> or <MyClass_2>
// for a placeholder.
var Types = map[string]bool{
	"PERSON": true,
	"EMAIL":  true,
	"PHONE":  true,
	"CARD":   true,
	"IBAN":   true,
	"SSN":    true,
	"MAC":    true,
	"IP":     true,
	"HOST":   true,
	"PATH":   true,
	"USER":   true,
	"DB":     true,
	"URL":    true,
	"TICKET": true,
	"ORG":    true,
	"ADDR":   true,
	"SECRET": true,
	"TOKEN":  true,
	"KEY":    true,
	"DSN":    true,
}

// typesAlt — the alternation of types for the regexp. Placeholder == "(" + typesAlt + ")".
// The order of types is deterministic (slices.Sorted): the regexp is built once
// at package initialization, and a random order from the map would make its text
// non-reproducible. No type is a prefix of another, therefore the order
// of alternatives does not affect the result.
var typesAlt = strings.Join(slices.Sorted(maps.Keys(Types)), "|")

// Token — a found placeholder. Start and End are the byte bounds of the fragment in the
// source text: text[Start:End] == Raw. Raw keeps the exact original spelling,
// including &lt; and spaces inside the token.
type Token struct {
	Type  string
	N     int
	Start int
	End   int
	Raw   string
}

// Format builds a placeholder of the canonical form: <TYPE_N>.
func Format(typ string, n int) string {
	return "<" + typ + "_" + strconv.Itoa(n) + ">"
}
