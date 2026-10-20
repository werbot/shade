package store

import "errors"

// ErrBuiltinRule is a rejection because the rule is builtin. A sentinel, not a text: on
// on a parallel start two processes see the builtin rule as missing and
// insert it in a race, and the loser must tell "already seeded" from
// a real rejection (see SeedBuiltin).
var ErrBuiltinRule = errors.New("builtin rule")

// The other rule rejections are sentinels too: the API picks the status of its answer by
// errors.Is, and not by the text.
var (
	ErrRuleNotFound = errors.New("rule not found")
	ErrRuleExists   = errors.New("rule exists")
	ErrInvalidRule  = errors.New("invalid rule")
)

// ruleError carries ready-made user-facing rejection text and is recognised by
// errors.Is(err, kind), where kind is one of the sentinels above. Wrapping through %w is not
// possible: the sentinel would add its own text to the message the user reads.
type ruleError struct {
	msg  string
	kind error
}

func (e ruleError) Error() string { return e.msg }

func (e ruleError) Is(target error) bool { return target == e.kind }
