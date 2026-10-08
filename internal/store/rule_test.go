package store_test

import (
	"testing"

	"github.com/werbot/shade/internal/rules"
)

func TestRulesForProjectMergesGlobalAndProject(t *testing.T) {
	p := project(t)
	s.AddRule(ctx, nil, rules.Spec{ID: "global-rule", Type: "SECRET", Kind: "regex", Pattern: `G1`})
	s.AddRule(ctx, nil, rules.Spec{ID: "shadowed", Type: "SECRET", Kind: "regex", Pattern: `OLD`})
	s.AddRule(ctx, &p.ID, rules.Spec{ID: "shadowed", Type: "HOST", Kind: "regex", Pattern: `NEW`})
	s.AddRule(ctx, &p.ID, rules.Spec{ID: "project-rule", Type: "HOST", Kind: "regex", Pattern: `P1`})

	rs, err := s.RulesForProject(ctx, p.ID)
	if err != nil {
		t.Fatal(err)
	}
	byName := map[string]rules.Rule{}
	for _, r := range rs {
		byName[r.Name] = r
	}
	if byName["global-rule"].Pattern != "G1" {
		t.Fatal("global rule lost")
	}
	if byName["project-rule"].Pattern != "P1" {
		t.Fatal("project rule lost")
	}
	if byName["shadowed"].Pattern != "NEW" || byName["shadowed"].Type != "HOST" {
		t.Fatalf("project must override global: %+v", byName["shadowed"])
	}
}

func TestAddRuleUpsertsByName(t *testing.T) {
	p := project(t)
	if err := s.AddRule(ctx, &p.ID, rules.Spec{ID: "acme", Type: "TICKET",
		Kind: "regex", Pattern: `ACME-\d{6}`}); err != nil {
		t.Fatal(err)
	}
	if err := s.AddRule(ctx, &p.ID, rules.Spec{ID: "acme", Type: "TICKET",
		Kind: "regex", Pattern: `ACME-\d{8}`}); err != nil {
		t.Fatal(err)
	}
	rs, err := s.RulesForProject(ctx, p.ID)
	if err != nil {
		t.Fatal(err)
	}
	found := 0
	for _, r := range rs {
		if r.Name == "acme" {
			found++
			if r.Pattern != `ACME-\d{8}` {
				t.Fatalf("want updated pattern, got %q", r.Pattern)
			}
		}
	}
	if found != 1 {
		t.Fatalf("upsert must not duplicate the row, got %d", found)
	}
}

func TestAddRuleUpsertsGlobalRule(t *testing.T) {
	// The global scope is held by its own partial index, and the ON CONFLICT target
	// has its own: with a project target a repeated insert would fail instead of
	// updates.
	if err := s.AddRule(ctx, nil, rules.Spec{ID: "global-upsert", Type: "SECRET",
		Kind: "regex", Pattern: `OLD`}); err != nil {
		t.Fatal(err)
	}
	if err := s.AddRule(ctx, nil, rules.Spec{ID: "global-upsert", Type: "HOST",
		Kind: "regex", Pattern: `NEW`}); err != nil {
		t.Fatal(err)
	}
	rows, err := s.ListRules(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	found := 0
	for _, r := range rows {
		if r.Name == "global-upsert" {
			found++
		}
	}
	if found != 1 {
		t.Fatalf("upsert must not duplicate the global row, got %d", found)
	}
	var pattern string
	if err := s.DB().QueryRow(
		`SELECT pattern FROM rules WHERE name = ? AND project_id IS NULL`, "global-upsert").Scan(&pattern); err != nil {
		t.Fatal(err)
	}
	if pattern != "NEW" {
		t.Fatalf("want updated pattern, got %q", pattern)
	}
}

func TestRemoveRuleRefusesBuiltin(t *testing.T) {
	s.AddRule(ctx, nil, rules.Spec{ID: "seeded", Type: "SECRET", Kind: "regex",
		Pattern: `x`, Builtin: true})
	if err := s.RemoveRule(ctx, nil, "seeded"); err == nil {
		t.Fatal("builtin rule must not be deletable, only disableable")
	}
}

func TestRulesForProjectExcludesOtherProjects(t *testing.T) {
	// A project rule is not a shared resource: otherwise one rule would leak into
	// anonymization of another directory.
	other, mine := project(t), project(t)
	if err := s.AddRule(ctx, &other.ID, rules.Spec{ID: "other-only", Type: "HOST",
		Kind: "regex", Pattern: `O1`}); err != nil {
		t.Fatal(err)
	}
	rs, err := s.RulesForProject(ctx, mine.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range rs {
		if r.Name == "other-only" {
			t.Fatal("a rule of another project leaked into the selection")
		}
	}
}

func TestSetRuleEnabledPersists(t *testing.T) {
	p := project(t)
	if err := s.AddRule(ctx, &p.ID, rules.Spec{ID: "toggle-me", Type: "SECRET",
		Kind: "regex", Pattern: `T1`, Enabled: true}); err != nil {
		t.Fatal(err)
	}
	if err := s.SetRuleEnabled(ctx, &p.ID, "toggle-me", false); err != nil {
		t.Fatal(err)
	}
	rows, err := s.ListRules(ctx, &p.ID)
	if err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, r := range rows {
		if r.Name == "toggle-me" {
			found = true
			if r.Enabled {
				t.Fatal("rule disable was not saved")
			}
		}
	}
	if !found {
		t.Fatal("the rule disappeared from the project list")
	}
}

func TestRemoveRuleDeletesNonBuiltin(t *testing.T) {
	p := project(t)
	if err := s.AddRule(ctx, &p.ID, rules.Spec{ID: "doomed", Type: "SECRET",
		Kind: "regex", Pattern: `D1`}); err != nil {
		t.Fatal(err)
	}
	if err := s.RemoveRule(ctx, &p.ID, "doomed"); err != nil {
		t.Fatal(err)
	}
	rows, err := s.ListRules(ctx, &p.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range rows {
		if r.Name == "doomed" {
			t.Fatal("rule was not deleted")
		}
	}
}
