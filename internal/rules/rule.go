// Package rules is the anonymization rule engine of shade: compiling specs,
// finding matches in the text and merging overlapping spans.
package rules

import (
	"fmt"
	"regexp"

	"github.com/werbot/shade/internal/placeholder"
)

// Rule is a compiled rule: the spec plus ready-made regexes.
type Rule struct {
	Name        string
	Type        string
	Kind        string
	Pattern     string
	SecretGroup int
	Keywords    []string
	EntropyMin  float64
	Validator   string
	Allowlist   []string
	OrderIdx    int
	Enabled     bool
	Builtin     bool

	re    *regexp.Regexp
	allow []*regexp.Regexp
}

// Spec is a rule before compilation. ID is the rule name (in Rule it lives in Name, in
// the database — in the name column), Order is the priority (in Rule it is OrderIdx, in the database —
// order_idx).
type Spec struct {
	ID          string
	Type        string
	Kind        string
	Pattern     string
	SecretGroup int
	Keywords    []string
	Allowlist   []string
	EntropyMin  float64
	Validator   string
	Order       int
	Enabled     bool
	Builtin     bool
}

// Compile validates the spec and builds the regexes of the rule. An error instead of
// a silent skip: a rule that did not compile must be
// be visible to the author, instead of quietly finding nothing.
//
// The order of the checks matters: the pattern is checked first, because it is what
// the rule author will go and fix, and the message must name this field even
// when the type is broken at the same time.
func Compile(s Spec) (Rule, error) {
	pattern := s.Pattern
	if s.Kind == "literal" {
		pattern = regexp.QuoteMeta(pattern)
	}
	re, err := regexp.Compile(pattern)
	if err != nil {
		return Rule{}, fmt.Errorf("rule %q: pattern %q: %w", s.ID, s.Pattern, err)
	}
	if !placeholder.Types[s.Type] {
		return Rule{}, fmt.Errorf("rule %q: unknown type %q", s.ID, s.Type)
	}
	if s.Kind != "regex" && s.Kind != "literal" && s.Kind != "entropy" {
		return Rule{}, fmt.Errorf("rule %q: unknown kind %q", s.ID, s.Kind)
	}
	allow := make([]*regexp.Regexp, 0, len(s.Allowlist))
	for _, a := range s.Allowlist {
		ra, err := regexp.Compile(a)
		if err != nil {
			return Rule{}, fmt.Errorf("rule %q: allowlist %q: %w", s.ID, a, err)
		}
		allow = append(allow, ra)
	}
	return Rule{
		Name:        s.ID,
		Type:        s.Type,
		Kind:        s.Kind,
		Pattern:     s.Pattern,
		SecretGroup: s.SecretGroup,
		Keywords:    s.Keywords,
		EntropyMin:  s.EntropyMin,
		Validator:   s.Validator,
		Allowlist:   s.Allowlist,
		OrderIdx:    s.Order,
		Enabled:     s.Enabled,
		Builtin:     s.Builtin,
		re:          re,
		allow:       allow,
	}, nil
}
