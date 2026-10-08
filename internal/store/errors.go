package store

import "errors"

// ErrBuiltinRule is a rejection because the rule is builtin. A sentinel, not a text: on
// on a parallel start two processes see the builtin rule as missing and
// insert it in a race, and the loser must tell "already seeded" from
// a real rejection (see SeedBuiltin).
var ErrBuiltinRule = errors.New("builtin rule")

// builtinRuleError carries ready-made user-facing rejection text and is recognised
// errors.Is(err, ErrBuiltinRule). Wrapping through %w is not possible: the sentinel would add
// its own text to the message the user reads.
type builtinRuleError struct{ msg string }

func (e builtinRuleError) Error() string { return e.msg }

func (e builtinRuleError) Is(target error) bool { return target == ErrBuiltinRule }
