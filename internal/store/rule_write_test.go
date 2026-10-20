package store_test

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/werbot/shade/internal/rules"
	"github.com/werbot/shade/internal/store"
)

// The UI shows the definition of a rule, so a list row must carry it and not only
// the name and the flags.
func TestListRulesCarriesTheDefinition(t *testing.T) {
	p := project(t)
	spec := rules.Spec{ID: "t_def", Type: "TICKET", Kind: "regex", Pattern: `T-(\d+)`, SecretGroup: 1}
	if err := s.AddRule(ctx, &p.ID, spec); err != nil {
		t.Fatal(err)
	}
	rows, err := s.ListRules(ctx, &p.ID)
	if err != nil {
		t.Fatal(err)
	}
	i := slices.IndexFunc(rows, func(r store.RuleRow) bool { return r.Name == spec.ID })
	if i < 0 {
		t.Fatalf("rule %q is missing from the list", spec.ID)
	}
	if got := rows[i]; got.Kind != spec.Kind || got.Pattern != spec.Pattern || got.SecretGroup != spec.SecretGroup {
		t.Fatalf("the row lost the definition: %+v", got)
	}
}

// A typo in a rule name must be told apart from a real failure (the API answers 404
// for it), and the text the CLI prints stays as it was.
func TestRuleChangesReportNotFound(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
	}{
		{"SetRuleEnabled", s.SetRuleEnabled(ctx, nil, "ghost", true)},
		{"RemoveRule", s.RemoveRule(ctx, nil, "ghost")},
	} {
		if !errors.Is(tc.err, store.ErrRuleNotFound) {
			t.Errorf("%s: %v is not ErrRuleNotFound", tc.name, tc.err)
			continue
		}
		if want := `rule "ghost" not found`; tc.err.Error() != want {
			t.Errorf("%s: text %q, want %q", tc.name, tc.err, want)
		}
	}
}

// Both writers share one validation: a rule the engine cannot compile never reaches
// the table, where RulesForProject would fail on it for the whole project.
func TestAddRuleRefusesWhatTheEngineCannotCompile(t *testing.T) {
	writers := map[string]func(*store.Store, context.Context, *int64, rules.Spec) error{
		"AddRule":    (*store.Store).AddRule,
		"CreateRule": (*store.Store).CreateRule,
	}
	cases := []struct {
		name string
		spec rules.Spec
	}{
		{"pattern", rules.Spec{Type: "SECRET", Kind: "regex", Pattern: `(`}},
		{"type", rules.Spec{Type: "NOPE", Kind: "regex", Pattern: `x`}},
		{"kind", rules.Spec{Type: "SECRET", Kind: "bogus", Pattern: `x`}},
		{"secret group", rules.Spec{Type: "SECRET", Kind: "regex", Pattern: `x`, SecretGroup: 2}},
	}
	st := openTemp(t)
	for writer, write := range writers {
		for _, tc := range cases {
			t.Run(writer+"/"+tc.name, func(t *testing.T) {
				spec := tc.spec
				spec.ID = "bad-" + tc.name
				if err := write(st, ctx, nil, spec); !errors.Is(err, store.ErrInvalidRule) {
					t.Fatalf("want ErrInvalidRule, got %v", err)
				}
				rows, err := st.ListRules(ctx, nil)
				if err != nil {
					t.Fatal(err)
				}
				if slices.ContainsFunc(rows, func(r store.RuleRow) bool { return r.Name == spec.ID }) {
					t.Fatalf("rule %q was stored", spec.ID)
				}
			})
		}
	}
}

// patternIn is the pattern of the rule held by one scope. ListRules of a project also lists
// the global rows, so the rows are told apart by whether they have a project.
func patternIn(t *testing.T, st *store.Store, projectID *int64, name string) string {
	t.Helper()
	rows, err := st.ListRules(ctx, projectID)
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range rows {
		if row.Name == name && (row.ProjectID == nil) == (projectID == nil) {
			return row.Pattern
		}
	}
	t.Fatalf("rule %q is missing from the scope", name)
	return ""
}

// CreateRule is what the UI calls to add a rule: a name that is taken must come back as a
// conflict, and not silently replace the rule (AddRule upserts, which is not what a form wants).
func TestCreateRuleNeverOverwrites(t *testing.T) {
	st := openTemp(t)
	p, err := st.ProjectForPath(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	mine := func(pattern string) rules.Spec {
		return rules.Spec{ID: "mine", Type: "SECRET", Kind: "regex", Pattern: pattern, Enabled: true}
	}
	scopes := []struct {
		name string
		id   *int64
	}{{"global", nil}, {"project", &p.ID}}

	// One name in two scopes is two rows, and each scope creates its own.
	for _, sc := range scopes {
		if err := st.CreateRule(ctx, sc.id, mine("OLD")); err != nil {
			t.Fatalf("creating in the %s scope: %v", sc.name, err)
		}
	}
	for _, sc := range scopes {
		if err := st.CreateRule(ctx, sc.id, mine("NEW")); !errors.Is(err, store.ErrRuleExists) {
			t.Fatalf("%s scope: want ErrRuleExists, got %v", sc.name, err)
		}
		if got := patternIn(t, st, sc.id, "mine"); got != "OLD" {
			t.Fatalf("%s scope: the rule was overwritten, pattern %q", sc.name, got)
		}
	}

	// A builtin row holds its name too: create must not become a replacement of it.
	if err := st.SeedBuiltin(ctx); err != nil {
		t.Fatal(err)
	}
	rows, err := st.ListRules(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	var checked int
	for _, row := range rows {
		if !row.Builtin {
			continue
		}
		checked++
		before := snapshotRule(t, st, row.Name)
		spec := mine("zzz")
		spec.ID = row.Name
		if err := st.CreateRule(ctx, nil, spec); !errors.Is(err, store.ErrRuleExists) {
			t.Fatalf("builtin %q: want ErrRuleExists, got %v", row.Name, err)
		}
		if after := snapshotRule(t, st, row.Name); after != before {
			t.Fatalf("builtin %q changed:\nwas  %+v\nnow  %+v", row.Name, before, after)
		}
	}
	if checked == 0 {
		t.Fatal("no builtin rule was seeded")
	}
}
