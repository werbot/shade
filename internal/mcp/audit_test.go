package mcp_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/werbot/shade/internal/store"
)

// The wire shapes are declared here, not imported from internal/mcp, for the same reason
// as the other tools': the field names are the contract, so a rename inside the package
// must fail these tests rather than silently change what a client receives.
type unresolvedWire struct {
	Entries []auditWire `json:"entries"`
}

type auditWire struct {
	Time    int64  `json:"time"`
	Adapter string `json:"adapter"`
	Type    string `json:"type"`
	Action  string `json:"action"`
	Detail  string `json:"detail"`
}

// journalStore opens a store with one project for the unresolved_report tests.
func journalStore(t *testing.T) (*store.Store, int64) {
	t.Helper()
	st := openStore(t)
	p, err := st.ProjectForPath(context.Background(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return st, p.ID
}

// addUnresolvedAt writes an unresolved journal row with an explicit ts: RecordUnresolved
// stamps the moment it runs, and a test of the since bound needs a row that is genuinely
// old.
func addUnresolvedAt(t *testing.T, st *store.Store, pid, ts int64, adapter, typ, token string) {
	t.Helper()
	_, err := st.DB().Exec(
		`INSERT INTO audit(ts, project_id, direction, adapter, rule, type, action, detail)
		 VALUES(?, ?, 'from_model', ?, NULL, ?, 'unresolved', ?)`,
		ts, pid, adapter, typ, token)
	if err != nil {
		t.Fatal(err)
	}
}

// TestUnresolvedReportSelectsOnlyUnresolved is the filter and the whole point of the tool:
// a journal holding both unresolved and blocked rows comes back with the unresolved ones
// alone, and with the placeholder token in detail rather than a value.
func TestUnresolvedReportSelectsOnlyUnresolved(t *testing.T) {
	st, pid := journalStore(t)
	ctx := context.Background()
	if err := st.RecordUnresolved(ctx, pid, "claude", "EMAIL", "<EMAIL_1>"); err != nil {
		t.Fatal(err)
	}
	if err := st.RecordBlocked(ctx, pid, "claude", "SECRET"); err != nil {
		t.Fatal(err)
	}
	if err := st.RecordUnresolved(ctx, pid, "claude", "HOST", "<HOST_2>"); err != nil {
		t.Fatal(err)
	}

	cs := toolSession(t, t.TempDir(), fakeEngine{store: st, projectID: pid})
	res := callTool(t, cs, "unresolved_report", map[string]any{})
	if res.IsError {
		t.Fatalf("unresolved_report must not fail: %s", rawResult(t, res))
	}

	got := structured[unresolvedWire](t, res).Entries
	if len(got) != 2 {
		t.Fatalf("entries: got %+v, want the two unresolved rows", got)
	}
	tokens := map[string]bool{}
	for _, e := range got {
		if e.Action != "unresolved" {
			t.Errorf("a blocked row survived the filter: %+v", e)
		}
		tokens[e.Detail] = true
	}
	if !tokens["<EMAIL_1>"] || !tokens["<HOST_2>"] {
		t.Errorf("detail must carry the placeholder tokens: %+v", got)
	}
}

// TestUnresolvedReportNarrowsBySinceAndLimit: since drops a row older than the age, and
// limit caps how many come back.
func TestUnresolvedReportNarrowsBySinceAndLimit(t *testing.T) {
	st, pid := journalStore(t)
	now := time.Now().Unix()
	addUnresolvedAt(t, st, pid, now-2*24*3600, "claude", "EMAIL", "<EMAIL_1>")
	addUnresolvedAt(t, st, pid, now, "claude", "HOST", "<HOST_2>")

	cs := toolSession(t, t.TempDir(), fakeEngine{store: st, projectID: pid})

	got := structured[unresolvedWire](t, callTool(t, cs, "unresolved_report", map[string]any{"since": "1d"})).Entries
	if len(got) != 1 || got[0].Detail != "<HOST_2>" {
		t.Errorf("since 1d must drop the two-day-old row: %+v", got)
	}

	got = structured[unresolvedWire](t, callTool(t, cs, "unresolved_report", map[string]any{"limit": 1})).Entries
	if len(got) != 1 || got[0].Detail != "<HOST_2>" {
		t.Errorf("limit 1 must leave the newest row alone: %+v", got)
	}
}

// TestUnresolvedReportRejectsABadAge: an age without the d suffix is a tool error, the
// same refusal `shade audit --since` gives.
func TestUnresolvedReportRejectsABadAge(t *testing.T) {
	st, pid := journalStore(t)
	cs := toolSession(t, t.TempDir(), fakeEngine{store: st, projectID: pid})
	res := callTool(t, cs, "unresolved_report", map[string]any{"since": "7"})
	if !res.IsError {
		t.Fatalf("an age without the d suffix must fail the tool: %s", rawResult(t, res))
	}
	if raw := rawResult(t, res); !strings.Contains(raw, "d suffix") {
		t.Errorf("the error must explain the expected form: %s", raw)
	}
}

// TestUnresolvedReportEmptyIsAList: a journal with no unresolved row answers with an empty
// list, not null — null reads as "the field is absent" in a machine contract.
func TestUnresolvedReportEmptyIsAList(t *testing.T) {
	st, pid := journalStore(t)
	cs := toolSession(t, t.TempDir(), fakeEngine{store: st, projectID: pid})
	res := callTool(t, cs, "unresolved_report", map[string]any{})
	if got := rawResult(t, res); !strings.Contains(got, `"entries":[]`) {
		t.Errorf("entries must serialize as []: %s", got)
	}
}
