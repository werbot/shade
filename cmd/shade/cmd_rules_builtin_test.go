package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The worst case of overwriting a builtin rule: without the guard in AddRule the rule
// assignment stops catching the secret, and anon lets the value out in the clear.
// The check on anon is the subject of the test here — the return code of `add` would let the
// the regression through, because the insert goes through successfully.
func TestRulesAddDoesNotDisableBuiltin(t *testing.T) {
	home, dir := t.TempDir(), gitDir(t)
	sample := "password=" + secretValue

	if code, masked, stderr := runCLI(t, home, dir, []string{"anon"}, sample+"\n"); code != 0 ||
		!strings.Contains(masked, "<SECRET_") {
		t.Fatalf("setup: %d %q %s", code, masked, stderr)
	}

	addCode, _, addErr := runCLI(t, home, dir, []string{"rules", "add", "--name", "assignment",
		"--type", "HOST", "--pattern", "zzz", "--global"}, "")

	// The leak is checked first: the regression matters not because `add` returned the wrong
	// code, but that the builtin rule went dark and the value went out in the clear. With
	// the reverse order a guard mutation would fail on the code check, before reaching
	// the subject of the test.
	code, masked, stderr := runCLI(t, home, dir, []string{"anon"}, sample+"\n")
	if code != 0 {
		t.Fatalf("anon: %d %s", code, stderr)
	}
	if strings.Contains(masked, secretValue) {
		t.Fatalf("the builtin rule went dark, the value went out in the clear: %q", masked)
	}
	if addCode != 1 || !strings.Contains(addErr, "assignment") {
		t.Fatalf("replacing a builtin rule was not rejected: code %d, %s", addCode, addErr)
	}
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
