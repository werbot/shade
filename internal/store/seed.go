package store

import (
	"context"

	"github.com/werbot/shade/internal/rules"
)

// SeedBuiltin seeds the builtin rule set into the global scope. It is called on
// every start of the engine, so it is idempotent: rules that already exist by name
// are not touched at all — otherwise a repeated seeding would bring the original pattern back after
// the edits and would enable a rule that the user has disabled.
//
// It lives in store, and not in rules, because the dependency is one-way: store already
// knows about rules (Spec, Rule), while the reverse import would be a cycle.
func (s *Store) SeedBuiltin(ctx context.Context) error {
	specs, err := rules.Builtin()
	if err != nil {
		return err
	}
	rows, err := s.ListRules(ctx, nil)
	if err != nil {
		return err
	}
	seeded := make(map[string]bool, len(rows))
	for _, row := range rows {
		// Global scope only: a rule of a project with the same name is no reason
		// skip the global one, they live in different rows.
		if row.ProjectID == nil {
			seeded[row.Name] = true
		}
	}
	for _, spec := range specs {
		if seeded[spec.ID] {
			continue
		}
		if err := s.AddRule(ctx, nil, spec); err != nil {
			return err
		}
	}
	return nil
}
