package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/werbot/shade/internal/crypt"
	"github.com/werbot/shade/internal/store"
)

// testStore opens the test store and resolves the project of directory dir: the journal and
// row ages are edited only through the opened Store.DB(), a separate API for the sake of
// tests is not introduced.
func testStore(t *testing.T, home, dir string) (store.Project, *store.Store) {
	t.Helper()
	t.Setenv("SHADE_HOME", home)
	key, err := crypt.LoadOrCreateKey(home)
	if err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(home, key)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	p, err := st.ProjectForPath(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	return p, st
}

// backdate moves last_seen_at of all entities of the project into the past: prune
// compares the age with the current time, and a test cannot wait a month.
func backdate(t *testing.T, home, dir string, d time.Duration) {
	t.Helper()
	p, st := testStore(t, home, dir)
	if _, err := st.DB().ExecContext(context.Background(),
		`UPDATE entities SET last_seen_at=? WHERE project_id=?`,
		time.Now().Add(-d).Unix(), p.ID); err != nil {
		t.Fatal(err)
	}
}

// seedAudit puts an entry with the given time and action into the journal: the CLI writes
// only unresolved, while the filters must be checked on the second kind of entry too.
func seedAudit(t *testing.T, home, dir string, ts int64, action, detail string) {
	t.Helper()
	p, st := testStore(t, home, dir)
	direction := "to_model"
	if action == "unresolved" {
		direction = "from_model"
	}
	if _, err := st.DB().ExecContext(context.Background(),
		`INSERT INTO audit(ts, project_id, direction, adapter, action, detail)
		 VALUES(?, ?, ?, 'cli', ?, ?)`,
		ts, p.ID, direction, action, detail); err != nil {
		t.Fatal(err)
	}
}

func TestEntitiesListShowsPlaceholdersOnly(t *testing.T) {
	home, dir := t.TempDir(), gitDir(t)
	if code, _, stderr := runCLI(t, home, dir, []string{"anon"}, "password="+secretValue); code != 0 {
		t.Fatalf("setup: code=%d, err=%q", code, stderr)
	}
	code, out, stderr := runCLI(t, home, dir, []string{"entities", "list"}, "")
	if code != 0 {
		t.Fatalf("code=%d, err=%q", code, stderr)
	}
	if !strings.Contains(out, "<SECRET_1>") {
		t.Fatalf("the list has no placeholder: %q", out)
	}
	if strings.Contains(out, secretValue) {
		t.Fatalf("the value leaked into the output: %q", out)
	}
}

func TestEntitiesListOnEmptyProject(t *testing.T) {
	home, dir := t.TempDir(), gitDir(t)
	code, out, stderr := runCLI(t, home, dir, []string{"entities", "list"}, "")
	if code != 0 {
		t.Fatalf("code=%d, err=%q", code, stderr)
	}
	if !strings.Contains(out, "placeholder") {
		t.Fatalf("the table header is missing: %q", out)
	}
}

// A fresh entity must survive pruning with the default from the config (90d): without
// otherwise a "deleted 0" would prove nothing — prune could be wiping everything.
func TestEntitiesPruneKeepsFresh(t *testing.T) {
	home, dir := t.TempDir(), gitDir(t)
	if code, _, stderr := runCLI(t, home, dir, []string{"anon"}, "password="+secretValue); code != 0 {
		t.Fatalf("setup: code=%d, err=%q", code, stderr)
	}
	backdate(t, home, dir, 40*24*time.Hour)

	code, out, stderr := runCLI(t, home, dir, []string{"entities", "prune"}, "")
	if code != 0 {
		t.Fatalf("code=%d, err=%q", code, stderr)
	}
	if !strings.Contains(out, "entities deleted: 0") {
		t.Fatalf("got %q", out)
	}
	code, list, _ := runCLI(t, home, dir, []string{"entities", "list"}, "")
	if code != 0 || !strings.Contains(list, "<SECRET_1>") {
		t.Fatalf("the entity disappeared: code=%d, %q", code, list)
	}
}

// A forty-day entity must disappear with --older-than 30d: without this
// the "deleted 0" check above also passes on a prune that deletes nothing.
func TestEntitiesPruneRemovesOlderThan(t *testing.T) {
	home, dir := t.TempDir(), gitDir(t)
	if code, _, stderr := runCLI(t, home, dir, []string{"anon"}, "password="+secretValue); code != 0 {
		t.Fatalf("setup: code=%d, err=%q", code, stderr)
	}
	backdate(t, home, dir, 40*24*time.Hour)

	code, out, stderr := runCLI(t, home, dir, []string{"entities", "prune", "--older-than", "30d"}, "")
	if code != 0 {
		t.Fatalf("code=%d, err=%q", code, stderr)
	}
	if !strings.Contains(out, "entities deleted: 1") {
		t.Fatalf("got %q", out)
	}
}

// The default comes from Config.EntitiesTTL, not from a hardcoded constant: the age in
// in the project config must win.
func TestEntitiesPruneHonoursConfigTTL(t *testing.T) {
	home, dir := t.TempDir(), gitDir(t)
	if code, _, stderr := runCLI(t, home, dir, []string{"anon"}, "password="+secretValue); code != 0 {
		t.Fatalf("setup: code=%d, err=%q", code, stderr)
	}
	backdate(t, home, dir, 40*24*time.Hour)
	if err := os.WriteFile(filepath.Join(dir, ".shade.toml"),
		[]byte("entities_ttl = \"30d\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	code, out, stderr := runCLI(t, home, dir, []string{"entities", "prune"}, "")
	if code != 0 {
		t.Fatalf("code=%d, err=%q", code, stderr)
	}
	if !strings.Contains(out, "entities deleted: 1") {
		t.Fatalf("the age from the config was not applied: %q", out)
	}
}

func TestEntitiesRejectsBadArgs(t *testing.T) {
	home, dir := t.TempDir(), gitDir(t)
	for _, tc := range []struct {
		name string
		args []string
	}{
		{"age without a suffix", []string{"entities", "prune", "--older-than", "30"}},
		{"negative age", []string{"entities", "prune", "--older-than=-1d"}},
		{"foreign flag", []string{"entities", "list", "--json"}},
		{"unexpected argument", []string{"entities", "list", "extra"}},
		{"unknown subcommand", []string{"entities", "nope"}},
		{"without a subcommand", []string{"entities"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			code, _, stderr := runCLI(t, home, dir, tc.args, "")
			if code != 2 {
				t.Fatalf("code %d, expected 2 (%s)", code, stderr)
			}
			if stderr == "" {
				t.Fatal("code 2 without an explanation in stderr")
			}
		})
	}
}

func TestAuditShowsUnresolved(t *testing.T) {
	home, dir := t.TempDir(), gitDir(t)
	// deanon with a foreign placeholder: under fail_closed the answer is blocked (code 3),
	// but a trace in the journal must remain.
	if code, _, _ := runCLI(t, home, dir, []string{"deanon"}, "see <HOST_99>"); code != 3 {
		t.Fatalf("setup: code %d, expected 3", code)
	}
	code, out, stderr := runCLI(t, home, dir, []string{"audit"}, "")
	if code != 0 {
		t.Fatalf("code=%d, err=%q", code, stderr)
	}
	for _, want := range []string{"unresolved", "from_model", "cli", "HOST", "<HOST_99>"} {
		if !strings.Contains(out, want) {
			t.Fatalf("the journal has no %q: %q", want, out)
		}
	}
}

func TestAuditSinceSkipsOldEntries(t *testing.T) {
	home, dir := t.TempDir(), gitDir(t)
	runCLI(t, home, dir, []string{"deanon"}, "see <HOST_99>")
	seedAudit(t, home, dir, time.Now().Add(-30*24*time.Hour).Unix(), "unresolved", "<HOST_77>")

	code, all, stderr := runCLI(t, home, dir, []string{"audit"}, "")
	if code != 0 {
		t.Fatalf("code=%d, err=%q", code, stderr)
	}
	if !strings.Contains(all, "<HOST_99>") || !strings.Contains(all, "<HOST_77>") {
		t.Fatalf("without --since the journal is incomplete: %q", all)
	}

	code, out, stderr := runCLI(t, home, dir, []string{"audit", "--since", "7d"}, "")
	if code != 0 {
		t.Fatalf("code=%d, err=%q", code, stderr)
	}
	if !strings.Contains(out, "<HOST_99>") {
		t.Fatalf("a fresh entry was cut off: %q", out)
	}
	if strings.Contains(out, "<HOST_77>") {
		t.Fatalf("--since 7d skipped an entry a month old: %q", out)
	}
}

func TestAuditUnresolvedOnly(t *testing.T) {
	home, dir := t.TempDir(), gitDir(t)
	runCLI(t, home, dir, []string{"deanon"}, "see <HOST_99>")
	seedAudit(t, home, dir, time.Now().Unix(), "blocked", "<HOST_55>")

	code, out, stderr := runCLI(t, home, dir, []string{"audit", "--unresolved"}, "")
	if code != 0 {
		t.Fatalf("code=%d, err=%q", code, stderr)
	}
	if !strings.Contains(out, "<HOST_99>") {
		t.Fatalf("an unresolved entry was cut off: %q", out)
	}
	if strings.Contains(out, "<HOST_55>") {
		t.Fatalf("--unresolved skipped a block: %q", out)
	}
}

func TestAuditRejectsBadArgs(t *testing.T) {
	home, dir := t.TempDir(), gitDir(t)
	for _, tc := range []struct {
		name string
		args []string
	}{
		{"foreign flag", []string{"audit", "--json"}},
		{"age without a suffix", []string{"audit", "--since", "7"}},
		{"unexpected argument", []string{"audit", "extra"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			code, _, stderr := runCLI(t, home, dir, tc.args, "")
			if code != 2 {
				t.Fatalf("code %d, expected 2 (%s)", code, stderr)
			}
			if stderr == "" {
				t.Fatal("code 2 without an explanation in stderr")
			}
		})
	}
}
