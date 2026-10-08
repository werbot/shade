package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Tests of the `rules import`/`export` commands — next to their implementation
// (cmd_rules_io.go), just like cmd_rules_test.go next to cmd_rules.go.

func TestRulesImportReportsSkippedAndStoresImported(t *testing.T) {
	home, dir := t.TempDir(), gitDir(t)
	path := filepath.Join(t.TempDir(), "gitleaks.toml")
	// The second rule deliberately does not compile: the report must name the reason, and
	// the first is to reach the database without getting lost because of a neighbour.
	if err := os.WriteFile(path, []byte(`
[[rules]]
id = "acme-ticket"
regex = '''ACME-\d{6}'''

[[rules]]
id = "broken"
regex = '''(?<!\d)x'''
`), 0o600); err != nil {
		t.Fatal(err)
	}

	code, stdout, stderr := runCLI(t, home, dir, []string{"rules", "import", path}, "")
	if code != 0 {
		t.Fatalf("import: %d %s", code, stderr)
	}
	for _, want := range []string{"imported: 1", "skipped: 1", "broken"} {
		if !strings.Contains(stdout, want) {
			t.Fatalf("the report has no %q: %q", want, stdout)
		}
	}

	list := func() string {
		code, out, stderr := runCLI(t, home, dir, []string{"rules", "list"}, "")
		if code != 0 {
			t.Fatalf("list: %d %s", code, stderr)
		}
		return out
	}
	if !strings.Contains(list(), "acme-ticket") {
		t.Fatalf("the imported rule was not saved: %q", list())
	}
	if strings.Contains(list(), "broken") {
		t.Fatalf("a rejected rule got into the database: %q", list())
	}
}

func TestRulesImportUnreadableFileFails(t *testing.T) {
	home, dir := t.TempDir(), gitDir(t)
	code, _, stderr := runCLI(t, home, dir,
		[]string{"rules", "import", filepath.Join(dir, "nope.toml")}, "")
	if code != 1 {
		t.Fatalf("code %d, expected 1 (%s)", code, stderr)
	}
}

// listLine returns the `rules list` output row for the named rule: the columns
// of the table are the command's contract, and checking them as text is cheaper than opening the database
// the second way.
func listLine(t *testing.T, home, dir, rule string) string {
	t.Helper()
	code, stdout, stderr := runCLI(t, home, dir, []string{"rules", "list"}, "")
	if code != 0 {
		t.Fatalf("rules list: %d %s", code, stderr)
	}
	for _, line := range strings.Split(stdout, "\n") {
		if fields := strings.Split(line, "\t"); fields[0] == rule {
			return line
		}
	}
	t.Fatalf("the rule %q is missing from the list: %q", rule, stdout)
	return ""
}

// Import with a name collision: a rule named after a builtin is a skip with a
// with a reason, and not a refusal of the whole command or a rewrite of the builtin definition.
// The neighbouring new rule is written at the same time.
func TestRulesImportSkipsBuiltinCollision(t *testing.T) {
	home, dir := t.TempDir(), gitDir(t)
	path := filepath.Join(t.TempDir(), "gitleaks.toml")
	body := `
[[rules]]
id = "jwt"
regex = '''[A-Za-z0-9-_]{10,}\.[A-Za-z0-9-_]{10,}\.[A-Za-z0-9-_]{10,}'''

[[rules]]
id = "acme-ticket"
regex = '''ACME-\d{6}'''
`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}

	code, stdout, stderr := runCLI(t, home, dir, []string{"rules", "import", "--global", path}, "")
	if code != 0 {
		t.Fatalf("import: %d %s", code, stderr)
	}
	for _, want := range []string{"imported: 1", "skipped: 1", "jwt", "builtin"} {
		if !strings.Contains(stdout, want) {
			t.Fatalf("the report has no %q: %q", want, stdout)
		}
	}

	// The builtin definition is intact: TOKEN is still there, it stayed builtin.
	if got := listLine(t, home, dir, "jwt"); !strings.Contains(got, "TOKEN") ||
		strings.Contains(got, "no") {
		t.Fatalf("the builtin rule jwt was changed by the import: %q", got)
	}
	// The new rule is not merely written, it is compiled: the active set sees it.
	code, stdout, stderr = runCLI(t, home, dir, []string{"test", "--sample", "see ACME-123456"}, "")
	if code != 0 || !strings.Contains(stdout, "acme-ticket") {
		t.Fatalf("the imported rule does not work: %d %q %s", code, stdout, stderr)
	}
}
