package store_test

import (
	"strings"
	"testing"

	"github.com/werbot/shade/internal/rules"
	"github.com/werbot/shade/internal/store"
)

func TestRulesForProjectMergesGlobalAndProject(t *testing.T) {
	// Enabled: true is required: RulesForProject returns only enabled
	// rule, and without the flag the test would check not the overlap of scopes but whether
	// that disabled rules pass the filter.
	p := project(t)
	s.AddRule(ctx, nil, rules.Spec{ID: "global-rule", Type: "SECRET", Kind: "regex", Pattern: `G1`, Enabled: true})
	s.AddRule(ctx, nil, rules.Spec{ID: "shadowed", Type: "SECRET", Kind: "regex", Pattern: `OLD`, Enabled: true})
	s.AddRule(ctx, &p.ID, rules.Spec{ID: "shadowed", Type: "HOST", Kind: "regex", Pattern: `NEW`, Enabled: true})
	s.AddRule(ctx, &p.ID, rules.Spec{ID: "project-rule", Type: "HOST", Kind: "regex", Pattern: `P1`, Enabled: true})

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
		Kind: "regex", Pattern: `ACME-\d{6}`, Enabled: true}); err != nil {
		t.Fatal(err)
	}
	if err := s.AddRule(ctx, &p.ID, rules.Spec{ID: "acme", Type: "TICKET",
		Kind: "regex", Pattern: `ACME-\d{8}`, Enabled: true}); err != nil {
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

// RulesForProject is what goes into Detect. A disabled rule must not reach
// it: nine opt-in rules of the rule set are seeded with enabled = false, and
// otherwise they would fire for everyone by default.
func TestRulesForProjectSkipsDisabled(t *testing.T) {
	// A store of its own, not the shared s: the test counts enabled rules exactly, while
	// global rules of neighbouring tests of the package live in the same s and would break
	// the count. The assertion is not weakened by that.
	st := openTemp(t)
	p, err := st.ProjectForPath(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	st.AddRule(ctx, nil, rules.Spec{ID: "on", Type: "SECRET", Kind: "regex",
		Pattern: `A1`, Enabled: true})
	st.AddRule(ctx, nil, rules.Spec{ID: "off", Type: "SECRET", Kind: "regex",
		Pattern: `B1`}) // Enabled is the zero value, that is false

	rs, err := st.RulesForProject(ctx, p.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(rs) != 1 || rs[0].Name != "on" {
		t.Fatalf("a disabled rule must not reach Detect: %+v", rs)
	}
}

// ListRules, on the contrary, shows everything — the idempotency of seeding rests on it
// and the output of `shade rules list`, where the user must see the disabled ones too.
func TestListRulesShowsDisabled(t *testing.T) {
	s.AddRule(ctx, nil, rules.Spec{ID: "off", Type: "SECRET", Kind: "regex",
		Pattern: `C1`})
	rows, err := s.ListRules(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range rows {
		if r.Name == "off" {
			return
		}
	}
	t.Fatal("ListRules must show disabled rules")
}

func TestRulesForProjectExcludesOtherProjects(t *testing.T) {
	// A project rule is not a shared resource: otherwise one rule would leak into
	// anonymization of another directory. Enabled: true — otherwise the rule would not pass
	// the filter of disabled rules and the test would become vacuously true.
	other, mine := project(t), project(t)
	if err := s.AddRule(ctx, &other.ID, rules.Spec{ID: "other-only", Type: "HOST",
		Kind: "regex", Pattern: `O1`, Enabled: true}); err != nil {
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

// ruleSnap is a snapshot of a rule row: the fields that an upsert would rewrite, and
// the mark of a builtin one. Comparing before and after the rejection catches a change of any.
type ruleSnap struct {
	Type, Kind, Pattern string
	OrderIdx            int
	Builtin, Enabled    bool
}

func snapshotRule(t *testing.T, s *store.Store, name string) ruleSnap {
	t.Helper()
	var snap ruleSnap
	err := s.DB().QueryRow(`SELECT type, kind, pattern, order_idx, builtin, enabled
		FROM rules WHERE name = ? AND project_id IS NULL`, name).
		Scan(&snap.Type, &snap.Kind, &snap.Pattern, &snap.OrderIdx, &snap.Builtin, &snap.Enabled)
	if err != nil {
		t.Fatalf("snapshot of rule %q: %v", name, err)
	}
	return snap
}

// An insert on top of a builtin row is rejected: without the guard the upsert would rewrite
// pattern, type and order_idx and reset builtin, while SeedBuiltin no longer has the original
// would not return. A rule that used to catch a secret would die for good.
func TestAddRuleRefusesToOverwriteBuiltin(t *testing.T) {
	st := openTemp(t)
	if err := st.SeedBuiltin(ctx); err != nil {
		t.Fatal(err)
	}
	before := snapshotRule(t, st, "assignment")

	err := st.AddRule(ctx, nil, rules.Spec{ID: "assignment", Type: "HOST",
		Kind: "regex", Pattern: `zzz`, Order: 99, Enabled: false})
	if err == nil {
		t.Fatal("a builtin rule must not be replaced")
	}
	if !strings.Contains(err.Error(), "assignment") || !strings.Contains(err.Error(), "builtin") {
		t.Fatalf("the error does not explain the rejection: %v", err)
	}
	if after := snapshotRule(t, st, "assignment"); after != before {
		t.Fatalf("the builtin rule row changed:\nwas  %+v\nnow  %+v", before, after)
	}

	// The ban is narrow: a custom rule is still updated.
	for _, pattern := range []string{`OLD`, `NEW`} {
		if err := st.AddRule(ctx, nil, rules.Spec{ID: "mine", Type: "SECRET",
			Kind: "regex", Pattern: pattern, Enabled: true}); err != nil {
			t.Fatalf("a custom rule must be updated: %v", err)
		}
	}
	if got := snapshotRule(t, st, "mine"); got.Pattern != "NEW" {
		t.Fatalf("the custom rule upsert is broken: %+v", got)
	}
}

// Seeding into a clean store is not touched by the guard: SeedBuiltin calls AddRule only
// for names that are not in the global scope yet. The check counts rows, and not
// relies on reasoning.
func TestSeedBuiltinStillFillsEmptyStore(t *testing.T) {
	st := openTemp(t)
	if err := st.SeedBuiltin(ctx); err != nil {
		t.Fatalf("seeding: %v", err)
	}
	specs, err := rules.Builtin()
	if err != nil {
		t.Fatal(err)
	}
	rows, err := st.ListRules(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	seeded := 0
	for _, row := range rows {
		if row.Builtin {
			seeded++
		}
	}
	if seeded != len(specs) {
		t.Fatalf("seeded %d builtin rules out of %d", seeded, len(specs))
	}
}
