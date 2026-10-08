package rules

// validators is the registry of match validators by name. At this step it is empty:
// the names are filled by the builtin rule set (guards like luhn, ip_global).
var validators = map[string]func(string) bool{}

// Validate checks a match with the validator of the rule. An empty name means
// a rule without a validator and passes the match. An unknown name suppresses
// the rule (fail-closed): a typo in the name must disable the rule, not
// pass its matches without a check.
func Validate(name, matched string) bool {
	if name == "" {
		return true
	}
	fn, ok := validators[name]
	if !ok {
		return false
	}
	return fn(matched)
}
