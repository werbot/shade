package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

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

// export takes the active set and drops the builtin rules: a builtin
// rule set restores itself, while in a file it would be dead weight.
func TestRulesExportDropsBuiltin(t *testing.T) {
	home, dir := t.TempDir(), gitDir(t)
	addAcme(t, home, dir, true)

	code, stdout, stderr := runCLI(t, home, dir, []string{"rules", "export"}, "")
	if code != 0 {
		t.Fatalf("export: %d %s", code, stderr)
	}
	for _, want := range []string{"acme", "TICKET", `ACME-`} {
		if !strings.Contains(stdout, want) {
			t.Fatalf("the export has no %q: %q", want, stdout)
		}
	}
	if strings.Contains(stdout, "assignment") {
		t.Fatalf("a builtin rule got into the export: %q", stdout)
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

// listLines returns all `rules list` output rows for the named rule: the same
// a name can sit in two scopes at once, and the scope check must see both.
// The table columns are the command's contract, and reading them as text is cheaper than
// open the database the second way.
func listLines(t *testing.T, home, dir, rule string) []string {
	t.Helper()
	code, stdout, stderr := runCLI(t, home, dir, []string{"rules", "list"}, "")
	if code != 0 {
		t.Fatalf("rules list: %d %s", code, stderr)
	}
	var out []string
	for _, line := range strings.Split(stdout, "\n") {
		if fields := strings.Split(line, "\t"); fields[0] == rule {
			out = append(out, line)
		}
	}
	if len(out) == 0 {
		t.Fatalf("the rule %q is missing from the list: %q", rule, stdout)
	}
	return out
}
