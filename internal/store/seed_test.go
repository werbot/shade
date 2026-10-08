package store_test

import (
	"testing"

	"github.com/werbot/shade/internal/rules"
)

// TestSeedBuiltinIsIdempotent — seeding happens on every start of the engine, so
// must be idempotent: neither a second row nor a second record per rule.
func TestSeedBuiltinIsIdempotent(t *testing.T) {
	s := openTemp(t)
	if err := s.SeedBuiltin(ctx); err != nil {
		t.Fatal(err)
	}
	if err := s.SeedBuiltin(ctx); err != nil {
		t.Fatal(err)
	}

	rows, err := s.ListRules(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]int{}
	for _, r := range rows {
		if r.Builtin {
			seen[r.Name]++
		}
	}
	if len(seen) == 0 {
		t.Fatal("nothing seeded")
	}
	for name, n := range seen {
		if n != 1 {
			t.Fatalf("rule %s seeded %d times", name, n)
		}
	}
}

// TestSeedBuiltinKeepsUserEdit — an edit of a builtin rule made by the user
// a repeated seeding does not undo: the rule "sees, edits, disables".
func TestSeedBuiltinKeepsUserEdit(t *testing.T) {
	s := openTemp(t)
	if err := s.SeedBuiltin(ctx); err != nil {
		t.Fatal(err)
	}
	// the user disabled a builtin rule
	if err := s.SetRuleEnabled(ctx, nil, "assignment", false); err != nil {
		t.Fatal(err)
	}
	if err := s.SeedBuiltin(ctx); err != nil {
		t.Fatal(err)
	}

	rows, err := s.ListRules(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	var saw bool
	for _, r := range rows {
		if r.Name == "assignment" {
			saw = true
			if r.Enabled {
				t.Fatal("re-seeding re-enabled a rule the user disabled")
			}
		}
	}
	if !saw {
		t.Fatal("rule vanished")
	}
}

// TestSeededRulesWorkFromTheDatabase — the seeded rules must work from
// the database as a whole: the designed rule set is checked by the corpus on LoadBuiltin,
// while here we check that the pattern, the type and the validator survived the write into SQLite.
func TestSeededRulesWorkFromTheDatabase(t *testing.T) {
	s := openTemp(t)
	if err := s.SeedBuiltin(ctx); err != nil {
		t.Fatal(err)
	}
	p, err := s.ProjectForPath(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	rs, err := s.RulesForProject(ctx, p.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(rs) == 0 {
		t.Fatal("there are no enabled rules")
	}
	if spans := rules.Detect("password="+"Xk7pQ2mZr9Tv", rs); len(spans) == 0 {
		t.Fatal("the seeded assignment rule did not fire")
	}
}

// TestSeededOptInRulesStayOutOfDetect — nine opt-in rules are seeded, but they do not
// must not reach Detect: otherwise emails, paths and addresses would be cut for everyone by
// by default.
func TestSeededOptInRulesStayOutOfDetect(t *testing.T) {
	s := openTemp(t)
	if err := s.SeedBuiltin(ctx); err != nil {
		t.Fatal(err)
	}
	p, err := s.ProjectForPath(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	rs, err := s.RulesForProject(ctx, p.ID)
	if err != nil {
		t.Fatal(err)
	}
	off := []string{"email", "ssn", "iban", "home_path", "mac_addr",
		"public_ip", "public_ip6", "phone_loose", "entropy"}
	for _, r := range rs {
		for _, name := range off {
			if r.Name == name {
				t.Fatalf("disabled rule %s reached Detect", name)
			}
		}
	}
}
