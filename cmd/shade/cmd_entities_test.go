package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

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

// The flag value must not only be parsed, but applied: with age = 0
// the forty-day entity is still deleted, and "deleted 1" adds up. Here
// the entity is aged by just an hour, so the right answer is zero deleted.
func TestEntitiesPruneOlderThanKeepsFresh(t *testing.T) {
	home, dir := t.TempDir(), gitDir(t)
	if code, _, stderr := runCLI(t, home, dir, []string{"anon"}, "password="+secretValue); code != 0 {
		t.Fatalf("setup: code=%d, err=%q", code, stderr)
	}
	backdate(t, home, dir, time.Hour)

	code, out, stderr := runCLI(t, home, dir, []string{"entities", "prune", "--older-than", "30d"}, "")
	if code != 0 {
		t.Fatalf("code=%d, err=%q", code, stderr)
	}
	if !strings.Contains(out, "entities deleted: 0") {
		t.Fatalf("an entity an hour old did not survive the 30d age: %q", out)
	}
}

// An age overflow must not turn into a boundary in the future:
// a negative time.Duration in PruneEntities would wipe all entities of the project, and
// values live only in value_enc — the loss cannot be rolled back. That is why the input
// is rejected, and the table stays untouched.
func TestEntitiesPruneRejectsOverflowingAge(t *testing.T) {
	home, dir := t.TempDir(), gitDir(t)
	if code, _, stderr := runCLI(t, home, dir, []string{"anon"}, "password="+secretValue); code != 0 {
		t.Fatalf("setup: code=%d, err=%q", code, stderr)
	}
	code, _, stderr := runCLI(t, home, dir, []string{"entities", "prune", "--older-than", "200000d"}, "")
	if code != 2 {
		t.Fatalf("code %d, expected 2 (%s)", code, stderr)
	}
	code, list, _ := runCLI(t, home, dir, []string{"entities", "list"}, "")
	if code != 0 || !strings.Contains(list, "<SECRET_1>") {
		t.Fatalf("the entity is lost: code=%d, %q", code, list)
	}
}

// Boundary: an age that is still representable in time.Duration must not be rejected.
func TestEntitiesPruneAcceptsLargestAge(t *testing.T) {
	home, dir := t.TempDir(), gitDir(t)
	if code, _, stderr := runCLI(t, home, dir, []string{"anon"}, "password="+secretValue); code != 0 {
		t.Fatalf("setup: code=%d, err=%q", code, stderr)
	}
	code, out, stderr := runCLI(t, home, dir, []string{"entities", "prune", "--older-than", "106751d"}, "")
	if code != 0 {
		t.Fatalf("a representable age was rejected: code %d (%s)", code, stderr)
	}
	if !strings.Contains(out, "entities deleted: 0") {
		t.Fatalf("got %q", out)
	}
}

// An age from the config is an operational error (code 1), not a call error: fixing
// it to the user in a file, not in the arguments.
func TestEntitiesPruneConfigAgeErrorIsNotUsageError(t *testing.T) {
	home, dir := t.TempDir(), gitDir(t)
	if err := os.WriteFile(filepath.Join(dir, ".shade.toml"),
		[]byte("entities_ttl = \"200000d\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	code, _, stderr := runCLI(t, home, dir, []string{"entities", "prune"}, "")
	if code != 1 {
		t.Fatalf("code %d, expected 1 (%s)", code, stderr)
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
		{"overflowing age", []string{"entities", "prune", "--older-than", "200000d"}},
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
		{"overflowing age", []string{"audit", "--since", "200000d"}},
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
