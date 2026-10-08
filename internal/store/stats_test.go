package store_test

import (
	"bytes"
	"encoding/json"
	"testing"
	"time"

	"github.com/werbot/shade/internal/store"
)

// allocate creates an entity and returns its placeholder.
func allocate(t *testing.T, projectID int64, typ, value string) string {
	t.Helper()
	ph, err := s.Allocate(ctx, projectID, typ, []byte(value))
	if err != nil {
		t.Fatal(err)
	}
	return ph
}

// age moves last_seen_at of the entity into the past. A row can be aged only
// through the open Store.DB(): PruneEntities compares with the current time, while
// a test cannot wait a month.
func age(t *testing.T, projectID int64, placeholder string, d time.Duration) {
	t.Helper()
	if _, err := s.DB().ExecContext(ctx,
		`UPDATE entities SET last_seen_at=? WHERE project_id=? AND placeholder=?`,
		time.Now().Add(-d).Unix(), projectID, placeholder); err != nil {
		t.Fatal(err)
	}
}

func TestPruneEntitiesRemovesOnlyOld(t *testing.T) {
	p := project(t)
	old := allocate(t, p.ID, "HOST", "old.local")
	fresh := allocate(t, p.ID, "HOST", "new.local")
	age(t, p.ID, old, 100*24*time.Hour)

	n, err := s.PruneEntities(ctx, p.ID, 30*24*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("deleted %d, expected 1", n)
	}
	list, err := s.ListEntities(ctx, p.ID, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0].Placeholder != fresh || list[0].Hits != 1 {
		t.Fatalf("%+v remained, one fresh %s was expected", list, fresh)
	}
}

func TestListEntitiesNeverExposesValues(t *testing.T) {
	p := project(t)
	const value = "db.prod.local"
	ph := allocate(t, p.ID, "HOST", value)

	list, err := s.ListEntities(ctx, p.ID, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 {
		t.Fatalf("got %+v", list)
	}
	// The value must be in the database: without this check the assertion below would be
	// green on an empty store too, that is it would check nothing.
	var enc []byte
	if err := s.DB().QueryRowContext(ctx,
		`SELECT value_enc FROM entities WHERE project_id=? AND placeholder=?`,
		p.ID, ph).Scan(&enc); err != nil {
		t.Fatal(err)
	}
	if len(enc) == 0 {
		t.Fatal("the value was not saved — the leak check is empty")
	}

	// JSON — a stand-in for "any serialization", not the output format: escaping
	// disabled, as in writeJSON of the CLI, otherwise < would travel as &lt; and the check for
	// placeholder would not pass for any shape of the structure.
	var buf bytes.Buffer
	enc2 := json.NewEncoder(&buf)
	enc2.SetEscapeHTML(false)
	if err := enc2.Encode(list); err != nil {
		t.Fatal(err)
	}
	got := buf.Bytes()
	if bytes.Contains(got, []byte(value)) {
		t.Fatalf("EntityInfo returned the value: %s", got)
	}
	if !bytes.Contains(got, []byte(ph)) {
		t.Fatalf("the placeholder %s is not in the list: %s", ph, got)
	}

	// The set of fields is checked in full: a field carrying a value must fail the test
	// by the very fact of appearing, and not only by being filled — "a leak is impossible
	// by type" rests exactly on the fact that no such field exists.
	var rows []map[string]any
	if err := json.Unmarshal(got, &rows); err != nil {
		t.Fatal(err)
	}
	wantKeys := []string{"Placeholder", "Type", "Hits", "FirstSeen", "LastSeen"}
	if len(rows) != 1 || len(rows[0]) != len(wantKeys) {
		t.Fatalf("the set of fields changed: %s", got)
	}
	for _, k := range wantKeys {
		if _, ok := rows[0][k]; !ok {
			t.Fatalf("EntityInfo has no field %s: %s", k, got)
		}
	}
}

func TestBumpRuleHitCountsPerRuleAndDay(t *testing.T) {
	p := project(t)
	for range 2 {
		if err := s.BumpRuleHit(ctx, p.ID, "assignment", "2026-01-01"); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.BumpRuleHit(ctx, p.ID, "assignment", "2026-01-02"); err != nil {
		t.Fatal(err)
	}
	if got := ruleHits(t, p.ID, "assignment", "2026-01-01"); got != 2 {
		t.Fatalf("the counter for 2026-01-01 is %d, expected 2", got)
	}
	if got := ruleHits(t, p.ID, "assignment", "2026-01-02"); got != 1 {
		t.Fatalf("the counter for 2026-01-02 is %d, expected 1", got)
	}
}

// ruleHits reads the counter directly: the API has no stats reader, its
// its consumer is the UI of phase 5, while the CLI does not print it.
func ruleHits(t *testing.T, projectID int64, rule, day string) int {
	t.Helper()
	var n int
	if err := s.DB().QueryRowContext(ctx,
		`SELECT count FROM rule_hits WHERE project_id=? AND rule=? AND day=?`,
		projectID, rule, day).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestRecordUnresolvedKeepsOnlyToken(t *testing.T) {
	p := project(t)
	const token = "<HOST_99>"
	if err := s.RecordUnresolved(ctx, p.ID, "cli", "HOST", token); err != nil {
		t.Fatal(err)
	}
	entries, err := s.Audit(ctx, p.ID, 10, store.AuditFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("got %+v", entries)
	}
	en := entries[0]
	if en.Action != "unresolved" || en.Direction != "from_model" ||
		en.Adapter != "cli" || en.Type != "HOST" || en.Detail != token {
		t.Fatalf("got %+v", en)
	}
	if en.Rule != "" {
		t.Fatalf("rule must be empty: %+v", en)
	}
}

// The filters must be applied in SQL before LIMIT: with cutting in Go three fresh
// blocks would push out three old unresolved records, and --unresolved
// would return nothing on a journal where such records do exist.
func TestAuditFiltersBeforeLimit(t *testing.T) {
	p := project(t)
	now := time.Now().Unix()
	old := now - 30*24*3600
	for i := range 3 {
		insertAudit(t, p.ID, old+int64(i), "unresolved", "<HOST_9>")
		insertAudit(t, p.ID, now, "blocked", "")
	}

	got, err := s.Audit(ctx, p.ID, 3, store.AuditFilter{Unresolved: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 {
		t.Fatalf("unresolved %d, expected 3: %+v", len(got), got)
	}
	for _, en := range got {
		if en.Action != "unresolved" {
			t.Fatalf("--unresolved let %q through", en.Action)
		}
	}

	recent, err := s.Audit(ctx, p.ID, 10, store.AuditFilter{Since: now - 7*24*3600})
	if err != nil {
		t.Fatal(err)
	}
	if len(recent) != 3 {
		t.Fatalf("fresh %d, expected 3: %+v", len(recent), recent)
	}
	for _, en := range recent {
		if en.Action != "blocked" {
			t.Fatalf("--since let an old record through: %+v", en)
		}
	}
}

// insertAudit puts a journal record with the given time and action: the CLI writes
// only unresolved, while the filters must be checked on the second kind of entry too.
func insertAudit(t *testing.T, projectID, ts int64, action, detail string) {
	t.Helper()
	direction := "to_model"
	if action == "unresolved" {
		direction = "from_model"
	}
	if _, err := s.DB().ExecContext(ctx,
		`INSERT INTO audit(ts, project_id, direction, adapter, action, detail)
		 VALUES(?, ?, ?, 'cli', ?, ?)`,
		ts, projectID, direction, action, detail); err != nil {
		t.Fatal(err)
	}
}
