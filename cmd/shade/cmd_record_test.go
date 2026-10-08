package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// failInserts puts a trigger on the table that kills any insert — that is how
// a side-write failure on the CLI path. A trigger, not DROP TABLE: on a migrate
// a store reopen would recreate the dropped table through
// CREATE TABLE IF NOT EXISTS, while a trigger survives a reopen.
func failInserts(t *testing.T, home, dir, table string) {
	t.Helper()
	_, st := testStore(t, home, dir)
	if _, err := st.DB().ExecContext(context.Background(), fmt.Sprintf(
		`CREATE TRIGGER %s_no_write BEFORE INSERT ON %s BEGIN SELECT RAISE(ABORT, 'write unavailable'); END`,
		table, table)); err != nil {
		t.Fatal(err)
	}
}

// A counter failure does not take the prompt away: the text is produced, the code is 0, and the failure is visible in stderr
// — otherwise the loss of stats is visible nowhere.
func TestAnonKeepsTextWhenStatsFail(t *testing.T) {
	home, dir := t.TempDir(), gitDir(t)
	failInserts(t, home, dir, "rule_hits")

	code, out, stderr := runCLI(t, home, dir, []string{"anon"}, "password="+secretValue)
	if code != 0 {
		t.Fatalf("a counter failure changed the code: %d (%s)", code, stderr)
	}
	if !strings.Contains(out, "<SECRET_1>") {
		t.Fatalf("the prompt is lost: %q", out)
	}
	if strings.Contains(out, secretValue) {
		t.Fatalf("the value leaked: %q", out)
	}
	if !strings.Contains(stderr, "stats not recorded") {
		t.Fatalf("the stats failure is not shown: %q", stderr)
	}
}

// A journal failure does not take the ready answer away: under fail_open_log the policy directly
// allows a partial answer to be returned, and a broken entry has no right to
// cancel it. The policy and the code stay the same, the warning goes to stderr.
func TestDeanonKeepsAnswerWhenAuditFails(t *testing.T) {
	home, dir := t.TempDir(), gitDir(t)
	if code, _, stderr := runCLI(t, home, dir, []string{"anon"}, "password="+secretValue); code != 0 {
		t.Fatalf("setup: code=%d, err=%q", code, stderr)
	}
	if err := os.WriteFile(filepath.Join(dir, ".shade.toml"),
		[]byte("fail_policy = \"fail_open_log\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	failInserts(t, home, dir, "audit")

	code, out, stderr := runCLI(t, home, dir, []string{"deanon"}, "<SECRET_1> and <HOST_99>")
	if code != 0 {
		t.Fatalf("a journal failure changed the code: %d (%s)", code, stderr)
	}
	if !strings.Contains(out, secretValue) {
		t.Fatalf("the value is not restored: %q", out)
	}
	if !strings.Contains(out, "<HOST_99>") {
		t.Fatalf("the unresolved token is lost: %q", out)
	}
	if !strings.Contains(stderr, "journal not recorded") {
		t.Fatalf("the journal failure is not shown: %q", stderr)
	}
}
