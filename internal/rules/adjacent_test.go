package rules_test

import (
	"testing"

	"github.com/werbot/shade/internal/rules"
)

// TestCorpusFindsAdjacentSecrets — rules with a consuming trailing context
// (boundaries instead of Python lookaround) must not lose the second secret
// separated by one character. This used to be lost: the engine continued the search from
// the end of the whole match, that is past the separator already eaten by the guard, and
// the second secret stayed behind the start of the next search.
func TestCorpusFindsAdjacentSecrets(t *testing.T) {
	byName := ruleSet(t)
	cases := []struct{ rule, text string }{
		{"card", "4111 1111 1111 1111 5500 0000 0000 0004"},
		{"card", "4111111111111111,5500000000000004"},
		// The last digit of the first card is in the issuer class [3-6]: it is on such
		// pairs the adjacent number was lost (see TestCorpusAdjacentSecretMatrix).
		{"card", "4111" + "1111" + "1111" + "1145 5500 0000 0000 0004"},
		{"phone", "+1 415 555 0142 +1 415 555 0143"},
		{"phone_loose", "415-555-0142 415-555-0143"},
		{"assignment", "password=" + "AbCdEf0123456789" + " token=" + "Zm9vYmFyQmF6UXV4"},
	}
	for _, c := range cases {
		r, ok := byName[c.rule]
		if !ok {
			t.Errorf("rule %s is not in the rule set", c.rule)
			continue
		}
		if spans := rules.Detect(c.text, []rules.Rule{r}); len(spans) != 2 {
			t.Errorf("%s: want 2 spans in %q, got %+v", c.rule, c.text, spans)
		}
	}
}

// TestCorpusAdjacentSecretMatrix — the matrix for N1. An adjacent secret was lost when
// the last digit of the first secret is in the class that the second
// the next match: a synthetic `^` at the search window boundary gave
// a match from zero, it ate the separator together with the second number, and
// the real number stayed behind the start of the next search. The loss depends both on
// the last digit, and on the separator (a comma and a newline are not part of
// the inner class `[ -]?` of the card number, so the defect did not show on them),
// so all ten digits and six separators are checked.
//
// The expectations are not «two spans in general», but what the reference finds on these pairs
// (a run of `redact_output.py`): for cards always two, for phone_loose with the separator
// `-` — none at all, because the hyphen is forbidden by the guard of the rule itself.
func TestCorpusAdjacentSecretMatrix(t *testing.T) {
	byName := ruleSet(t)
	card, ok := byName["card"]
	if !ok {
		t.Fatal("rule card is not in the rule set")
	}
	loose, ok := byName["phone_loose"]
	if !ok {
		t.Fatal("rule phone_loose is not in the rule set")
	}
	seps := []string{",", " ", "  ", "-", "\n", " | "}
	const second = "5500000000000004"
	for last := byte('0'); last <= '9'; last++ {
		first := cardEndingWith(t, last)
		for _, sep := range seps {
			text := first + sep + second
			if spans := rules.Detect(text, []rules.Rule{card}); len(spans) != 2 {
				t.Errorf("card last=%c sep=%q: want 2 spans, got %+v", last, sep, spans)
			}
		}
	}
	for _, sep := range seps {
		want := 2
		if sep == "-" {
			want = 0
		}
		text := "415-555-0142" + sep + "415-555-0143"
		if spans := rules.Detect(text, []rules.Rule{loose}); len(spans) != want {
			t.Errorf("phone_loose sep=%q: want %d spans, got %+v", sep, want, spans)
		}
	}
}

// cardEndingWith returns a card number passing Luhn with the given last
// digit: the defect depended exactly on whether it falls into the issuer class.
func cardEndingWith(t *testing.T, last byte) string {
	t.Helper()
	for mid := byte('0'); mid <= '9'; mid++ {
		s := "41111111111111" + string(mid) + string(last)
		if rules.Validate("luhn", s) {
			return s
		}
	}
	t.Fatalf("there is no card passing Luhn with last digit %c", last)
	return ""
}
