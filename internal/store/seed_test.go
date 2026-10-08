package store_test

import (
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/werbot/shade/internal/rules"
	"github.com/werbot/shade/internal/store"
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

// TestAddRuleBuiltinRefusalIsSentinel — SeedBuiltin rests on this recognition:
// a builtin rule rejection must be recognised by errors.Is, and its text is for
// the user must not change (a sentinel through %w would add its text to
// the message a human reads).
func TestAddRuleBuiltinRefusalIsSentinel(t *testing.T) {
	s := openTemp(t)
	spec := rules.Spec{ID: "sentinel-probe", Type: "SECRET", Kind: "regex", Pattern: `x`, Builtin: true}
	if err := s.AddRule(ctx, nil, spec); err != nil {
		t.Fatal(err)
	}
	err := s.AddRule(ctx, nil, spec)
	if !errors.Is(err, store.ErrBuiltinRule) {
		t.Fatalf("the builtin rule rejection was not recognised by the sentinel: %v", err)
	}
	if !strings.Contains(err.Error(), "builtin") || !strings.Contains(err.Error(), "sentinel-probe") {
		t.Fatalf("the rejection text changed: %q", err)
	}
	if err := s.RemoveRule(ctx, nil, "sentinel-probe"); !errors.Is(err, store.ErrBuiltinRule) {
		t.Fatalf("the builtin rule deletion rejection was not recognised by the sentinel: %v", err)
	}
}

// TestSeedBuiltinSurvivesConcurrentStart — a cold start of two sessions in one
// project: everyone sees the builtin rule as missing and inserts it
// in a race. The loser gets a "builtin" rejection and must take it as
// "already seeded", otherwise the run fails with exit code 1 and an empty stdout (final-review.md,
// Important 1). The test on processes is in the report; here the race is on a single store.
func TestSeedBuiltinSurvivesConcurrentStart(t *testing.T) {
	s := openTemp(t)
	const goroutines = 8
	start := make(chan struct{})
	errs := make([]error, goroutines)
	var wg sync.WaitGroup
	for i := range goroutines {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			errs[i] = s.SeedBuiltin(ctx)
		}()
	}
	close(start)
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Fatalf("seeding %d: %v", i, err)
		}
	}

	specs, err := rules.Builtin()
	if err != nil {
		t.Fatal(err)
	}
	rows, err := s.ListRules(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	var builtin int
	for _, r := range rows {
		if r.Builtin {
			builtin++
		}
	}
	if builtin != len(specs) {
		t.Fatalf("builtin rows %d, expected %d", builtin, len(specs))
	}
}

// TestSeedBuiltinDoesNotSwallowRealFailures — the tolerance is narrow: only
// only an "already builtin" rejection. Any other insert failure must come back
// an error: an unseeded rule is a rule that will not catch a secret, and
// a silent success is worse than a failure here. Injected with a trigger, and not with DROP TABLE:
// migrate would recreate a deleted table through CREATE TABLE IF NOT EXISTS, and
// the trigger survives a reopen.
func TestSeedBuiltinDoesNotSwallowRealFailures(t *testing.T) {
	s := openTemp(t)
	if _, err := s.DB().ExecContext(ctx,
		`CREATE TRIGGER rules_no_write BEFORE INSERT ON rules
		 BEGIN SELECT RAISE(ABORT, 'rule writing unavailable'); END`); err != nil {
		t.Fatal(err)
	}

	err := s.SeedBuiltin(ctx)
	if err == nil {
		t.Fatal("the insert failure was swallowed: the rule set is not seeded, yet SeedBuiltin is green")
	}
	if !strings.Contains(err.Error(), "rule writing unavailable") {
		t.Fatalf("the error does not name the cause of the failure: %v", err)
	}
	if errors.Is(err, store.ErrBuiltinRule) {
		t.Fatalf("a real failure was taken for a builtin rule rejection: %v", err)
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
