package core_test

import (
	"strings"
	"testing"

	"github.com/werbot/shade/internal/core"
	"github.com/werbot/shade/internal/rules"
)

// ruleHits reads a rule's statistics directly: there is no reader for it in the API, its
// consumer is the phase 5 UI, and the CLI does not print it. The sum over days, not a single
// row: a run may cross the UTC day boundary.
func ruleHits(t *testing.T, e *core.Engine, rule string) int {
	t.Helper()
	var n int
	if err := e.Store().DB().QueryRowContext(ctx,
		`SELECT COALESCE(SUM(count), 0) FROM rule_hits WHERE project_id=? AND rule=?`,
		e.ProjectID(), rule).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestAnonymizeCountsRuleHitOncePerCall(t *testing.T) {
	e := newEngine(t)
	src := "password=" + secret
	if _, err := e.Anonymize(ctx, src); err != nil {
		t.Fatal(err)
	}
	if got := ruleHits(t, e, "assignment"); got != 1 {
		t.Fatalf("after the first run the counter is %d, expected 1", got)
	}
	if _, err := e.Anonymize(ctx, src); err != nil {
		t.Fatal(err)
	}
	if got := ruleHits(t, e, "assignment"); got != 2 {
		t.Fatalf("after the second run the counter is %d, expected 2", got)
	}
}

// A span split into parts around a Guard substitution is one hit
// rule, not two: counting by spans would overstate the statistics, and this property
// is visible only on input with foreign placeholders.
func TestAnonymizeCountsRuleHitOnceForSplitSpan(t *testing.T) {
	e := newEngineWithRule(t, rules.Spec{
		ID: "nul_class", Type: "SECRET", Kind: "regex",
		Pattern: `x=(\S+)`, SecretGroup: 1, Order: 1, Enabled: true,
	})
	anon, err := e.Anonymize(ctx, "x=AbCdEf0123456789<EMAIL_1>ZzYyXx9876543210")
	if err != nil {
		t.Fatal(err)
	}
	if len(anon.Spans) != 2 {
		t.Fatalf("expected the span to split into two parts, got %+v", anon.Spans)
	}
	for _, sp := range anon.Spans {
		if sp.Rule != "nul_class" {
			t.Fatalf("the span is not from the rule under test: %+v", anon.Spans)
		}
	}
	if got := ruleHits(t, e, "nul_class"); got != 1 {
		t.Fatalf("counter %d, expected 1: one rule — one hit", got)
	}
}

// A counter failure does not cancel the anonymization: rule_hits is auxiliary data
// for the phase 5 UI, not a security record. A ready prompt cannot be recovered,
// a lost hit is recoverable, so the error of the auxiliary write does not go out
// go out. The failure is injected by dropping the table: Allocate works, the counter does not.
func TestAnonymizeSurvivesRuleHitFailure(t *testing.T) {
	e := newEngine(t)
	if _, err := e.Store().DB().ExecContext(ctx, `DROP TABLE rule_hits`); err != nil {
		t.Fatal(err)
	}
	got, err := e.Anonymize(ctx, "password="+secret)
	if err != nil {
		t.Fatalf("a counter failure brought down the anonymization: %v", err)
	}
	if strings.Contains(got.Text, secret) {
		t.Fatalf("the secret was not masked: %q", got.Text)
	}
	// The values are stored: the auxiliary write failed, not the main one.
	back, err := e.Restore(ctx, got.Text)
	if err != nil {
		t.Fatal(err)
	}
	if back.Text != "password="+secret {
		t.Fatalf("the value was not stored: %q", back.Text)
	}
}
