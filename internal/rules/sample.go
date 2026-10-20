package rules

import "cmp"

// TestSample compiles s as an ad-hoc rule and runs it over text; nothing is stored.
// An empty Kind reads as "regex" and an empty Type as "SECRET": the defaults of
// `shade rules test`, the MCP rules_test tool and the UI, kept in one place.
func TestSample(s Spec, text string) ([]Span, error) {
	s.ID, s.Enabled = AdHocName, true
	s.Kind = cmp.Or(s.Kind, "regex")
	s.Type = cmp.Or(s.Type, "SECRET")
	rule, err := Compile(s)
	if err != nil {
		return nil, err
	}
	return Detect(text, []Rule{rule}), nil
}
