package hook_test

import (
	"strings"
	"testing"

	"github.com/werbot/shade/internal/hook"
	"github.com/werbot/shade/internal/store"
)

func TestUserPromptSubmitGateBlocksAndCountsTypesOnce(t *testing.T) {
	home, repo := t.TempDir(), gitDir(t)
	writeConfig(t, repo, ".shade.toml", "prompt_gate = \"on\"\n")
	h := newHandler(t, home)
	// Two ssh targets: four spans, two distinct types. One journal row per type,
	// not per span, is the property under test.
	const prompt = "ssh deploy@db.prod.local && ssh root@db2.prod.local"
	res, err := h.Handle(ctx, hook.Event{
		Name: hook.EventUserPromptSubmit, CWD: repo, Prompt: prompt,
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Decision != "block" || !strings.Contains(res.Reason, "HOST") || !strings.Contains(res.Reason, "USER") {
		t.Fatalf("gate: %+v", res)
	}
	if strings.Contains(res.Reason, "db.prod.local") || strings.Contains(res.Reason, "deploy") {
		t.Fatalf("the reason must carry types, never values: %q", res.Reason)
	}
	// The engine the handler opened is closed by now — the journal is read
	// through a second, test-owned store on the same home.
	st := openStore(t, home)
	pid := projectIDIn(t, st, repo)
	rows, err := st.Audit(ctx, pid, 100, store.AuditFilter{})
	if err != nil || len(rows) != 2 || rows[0].Action != "blocked" || rows[0].Direction != "to_model" {
		t.Fatalf("audit: %+v err=%v", rows, err)
	}
	perType := map[string]int{}
	for _, r := range rows {
		perType[r.Type]++
		if r.Adapter != "hook" || r.Rule != "" || r.Detail != "" {
			t.Fatalf("a blocked row carries more than the type: %+v", r)
		}
	}
	if perType["HOST"] != 1 || perType["USER"] != 1 {
		t.Fatalf("one row per distinct type expected, got %+v", rows)
	}
}

// The gate is off by default: without a config the prompt goes through untouched,
// and nothing is written to the journal.
func TestUserPromptSubmitGateOffIsSilent(t *testing.T) {
	home, repo := t.TempDir(), gitDir(t)
	h := newHandler(t, home)
	res, err := h.Handle(ctx, hook.Event{
		Name: hook.EventUserPromptSubmit, CWD: repo, Prompt: "ssh deploy@db.prod.local",
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Decision != "" || res.Reason != "" {
		t.Fatalf("the gate must be silent when it is off: %+v", res)
	}
	st := openStore(t, home)
	pid := projectIDIn(t, st, repo)
	rows, err := st.Audit(ctx, pid, 100, store.AuditFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 0 {
		t.Fatalf("a prompt the gate never saw must not reach the journal: %+v", rows)
	}
}
