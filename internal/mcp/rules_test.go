package mcp_test

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/werbot/shade/internal/rules"
	"github.com/werbot/shade/internal/store"
)

// The wire shapes are declared here, not imported from internal/mcp, for the same reason
// as the text tools': the field names are the contract, so a rename inside the package
// must fail these tests rather than silently change what a client receives.
type rulesWire struct {
	Rules []ruleWire `json:"rules"`
}

type ruleWire struct {
	Name    string `json:"name"`
	Type    string `json:"type"`
	Enabled bool   `json:"enabled"`
	Builtin bool   `json:"builtin"`
	Scope   string `json:"scope"`
}

// openStore builds a real store in a temporary directory — rules_list reads the rows from
// it, and only it. The key is deterministic, the same way the store package's own tests
// build theirs.
func openStore(t *testing.T) *store.Store {
	t.Helper()
	s, err := store.Open(t.TempDir(), bytes.Repeat([]byte{7}, 32))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func mustAddRule(t *testing.T, st *store.Store, pid *int64, spec rules.Spec) {
	t.Helper()
	if err := st.AddRule(context.Background(), pid, spec); err != nil {
		t.Fatal(err)
	}
}

// TestRulesListCoversBothScopes: the answer carries the global rows and the project's own
// together, scope is derived from project_id, and a rule taken out of service stays in the
// list with enabled false instead of disappearing.
func TestRulesListCoversBothScopes(t *testing.T) {
	st := openStore(t)
	p, err := st.ProjectForPath(context.Background(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	mustAddRule(t, st, nil, rules.Spec{ID: "global-on", Type: "SECRET", Kind: "regex", Pattern: `g1`, Enabled: true})
	mustAddRule(t, st, nil, rules.Spec{ID: "global-off", Type: "SECRET", Kind: "regex", Pattern: `g2`})
	mustAddRule(t, st, &p.ID, rules.Spec{ID: "proj-on", Type: "HOST", Kind: "regex", Pattern: `p1`, Enabled: true})

	cs := toolSession(t, t.TempDir(), fakeEngine{store: st, projectID: p.ID})
	res := callTool(t, cs, "rules_list", map[string]any{})
	if res.IsError {
		t.Fatalf("rules_list must not fail: %s", rawResult(t, res))
	}

	got := map[string]ruleWire{}
	for _, r := range structured[rulesWire](t, res).Rules {
		got[r.Name] = r
	}
	want := map[string]ruleWire{
		"global-on":  {Name: "global-on", Type: "SECRET", Enabled: true, Scope: "global"},
		"global-off": {Name: "global-off", Type: "SECRET", Enabled: false, Scope: "global"},
		"proj-on":    {Name: "proj-on", Type: "HOST", Enabled: true, Scope: "project"},
	}
	if len(got) != len(want) {
		t.Fatalf("rules: got %+v, want %+v", got, want)
	}
	for name, w := range want {
		if got[name] != w {
			t.Errorf("rule %q: got %+v, want %+v", name, got[name], w)
		}
	}
}

// TestRulesTestCompilesAndMatchesWithoutAStore is the assertion that rules_test opens no
// store: the fake engine's store is nil, so a handler that reached for it would nil-deref
// and fail here instead of passing quietly. The rule name in the span is the ad-hoc
// contract — the constant the CLI and the tool must share.
func TestRulesTestCompilesAndMatchesWithoutAStore(t *testing.T) {
	cs := toolSession(t, t.TempDir(), fakeEngine{})
	res := callTool(t, cs, "rules_test", map[string]any{
		"pattern": `secret-\w+`, "sample": "my secret-abc here",
	})
	if res.IsError {
		t.Fatalf("rules_test must not fail without a store: %s", rawResult(t, res))
	}

	want := []spanWire{{Type: "SECRET", Rule: rules.AdHocName}}
	if got := structured[spansWire](t, res).Spans; len(got) != 1 || got[0] != want[0] {
		t.Errorf("spans: got %+v, want %+v", got, want)
	}
}

// TestRulesTestRejectsABrokenPattern: a pattern that does not compile is a tool error, not
// an empty match list — the same refusal `shade rules add` gives before saving.
func TestRulesTestRejectsABrokenPattern(t *testing.T) {
	cs := toolSession(t, t.TempDir(), fakeEngine{})
	res := callTool(t, cs, "rules_test", map[string]any{"pattern": "(", "sample": "x"})
	if !res.IsError {
		t.Fatalf("a broken pattern must fail the tool, not report no matches: %s", rawResult(t, res))
	}
	if raw := rawResult(t, res); !strings.Contains(raw, "pattern") {
		t.Errorf("the error must name the pattern: %s", raw)
	}
}

// TestRulesTestReturnsNoFragments is the same fence scan keeps: the answer names the type
// and the rule, and never the fragment nor where it lies.
func TestRulesTestReturnsNoFragments(t *testing.T) {
	const frag = "frag-secret-9"
	cs := toolSession(t, t.TempDir(), fakeEngine{})
	res := callTool(t, cs, "rules_test", map[string]any{
		"pattern": `frag-secret-\d`, "sample": "x " + frag + " y",
	})

	raw := rawResult(t, res)
	// The rule in the answer is what makes the absence of the fragment mean "matched and
	// withheld" rather than "nothing came back".
	if !strings.Contains(raw, `"rule":"`+rules.AdHocName+`"`) {
		t.Fatalf("the canary is vacuous, the rule is not in the answer: %s", raw)
	}
	for _, key := range []string{`"start"`, `"end"`} {
		if strings.Contains(raw, key) {
			t.Errorf("the answer must carry no offsets, found %s: %s", key, raw)
		}
	}
	if strings.Contains(raw, frag) {
		t.Errorf("the answer must carry no fragment: %s", raw)
	}
}
