package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// noRule — a sample that no builtin set catches. On a sample with
// with a secret "nothing found" would be indistinguishable from a command failure.
const noRule = "обычный текст без секретов"

func TestSelfTestShowsRuleThatFired(t *testing.T) {
	home, dir := t.TempDir(), gitDir(t)
	code, stdout, stderr := runCLI(t, home, dir, []string{"test", "--sample",
		"password=" + secretValue}, "")
	if code != 0 {
		t.Fatalf("exit %d: %s", code, stderr)
	}
	if !strings.Contains(stdout, "assignment") {
		t.Fatalf("rule name missing: %q", stdout)
	}
}

// The fragment is printed: the command is local, and the user sees their own text.
func TestSelfTestPrintsFragment(t *testing.T) {
	home, dir := t.TempDir(), gitDir(t)
	code, stdout, stderr := runCLI(t, home, dir,
		[]string{"test", "--sample", "password=" + secretValue}, "")
	if code != 0 {
		t.Fatalf("exit %d: %s", code, stderr)
	}
	if !strings.Contains(stdout, secretValue) {
		t.Fatalf("the table has no matched fragment: %q", stdout)
	}
}

// Span offsets live in the coordinates of the text after Guard, not of the restored text:
// a foreign placeholder is longer than the service substitution, and printing the fragment over
// the restored text would shift it by exactly the difference in lengths.
func TestSelfTestOffsetsSurviveForeignPlaceholder(t *testing.T) {
	home, dir := t.TempDir(), gitDir(t)
	code, stdout, stderr := runCLI(t, home, dir, []string{"test", "--sample",
		"see <HOST_99> and password=" + secretValue}, "")
	if code != 0 {
		t.Fatalf("exit %d: %s", code, stderr)
	}
	if !strings.Contains(stdout, secretValue) {
		t.Fatalf("the fragment was computed in the wrong coordinates: %q", stdout)
	}
}

func TestSelfTestWithoutMatches(t *testing.T) {
	home, dir := t.TempDir(), gitDir(t)
	code, stdout, stderr := runCLI(t, home, dir, []string{"test", "--sample", noRule}, "")
	if code != 0 {
		t.Fatalf("exit %d: %s", code, stderr)
	}
	if !strings.Contains(stdout, "no matches") {
		t.Fatalf("the no-matches message is missing: %q", stdout)
	}
	// The message must not contain the sample: otherwise an empty run would be visible
	// by the return code alone, and "nothing found" would become indistinguishable from
	// printing the whole sample.
	if strings.Contains(stdout, noRule) {
		t.Fatalf("the message contains the sample: %q", stdout)
	}
}

// --rules narrows the run to the named rule of the active set.
func TestSelfTestSelectsRule(t *testing.T) {
	home, dir := t.TempDir(), gitDir(t)
	addAcme(t, home, dir, true)

	code, stdout, stderr := runCLI(t, home, dir,
		[]string{"test", "--rules", "acme", "--sample", "see ACME-123456"}, "")
	if code != 0 {
		t.Fatalf("test --rules acme: %d %s", code, stderr)
	}
	if !strings.Contains(stdout, "acme") {
		t.Fatalf("the rule acme did not fire: %q", stdout)
	}
	code, stdout, _ = runCLI(t, home, dir,
		[]string{"test", "--rules", "assignment", "--sample", "see ACME-123456"}, "")
	if code != 0 || strings.Contains(stdout, "acme") {
		t.Fatalf("--rules did not narrow the set: %d %q", code, stdout)
	}
}

// The sample is read from the argument file, and without --sample and a file — from stdin.
func TestSelfTestReadsFileAndStdin(t *testing.T) {
	home, dir := t.TempDir(), gitDir(t)
	path := filepath.Join(t.TempDir(), "sample.txt")
	if err := os.WriteFile(path, []byte("password="+secretValue), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name  string
		args  []string
		stdin string
	}{
		{"file", []string{"test", path}, ""},
		{"stdin", []string{"test"}, "password=" + secretValue},
	} {
		t.Run(tc.name, func(t *testing.T) {
			code, stdout, stderr := runCLI(t, home, dir, tc.args, tc.stdin)
			if code != 0 {
				t.Fatalf("exit %d: %s", code, stderr)
			}
			if !strings.Contains(stdout, "assignment") {
				t.Fatalf("no rule matched: %q", stdout)
			}
		})
	}
}

func TestSelfTestRejectsBadArgs(t *testing.T) {
	home, dir := t.TempDir(), gitDir(t)
	file := filepath.Join(t.TempDir(), "sample.txt")
	if err := os.WriteFile(file, []byte(noRule), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name string
		args []string
		want string
	}{
		// this command has no --json and no --project: an accepted and ignored
		// flag would read as supported.
		{"--json", []string{"test", "--json", "--sample", noRule}, "--json"},
		{"--project", []string{"test", "--project", dir}, "--project"},
		{"unknown rule", []string{"test", "--rules", "nope", "--sample", noRule}, "nope"},
		{"sample and file", []string{"test", "--sample", noRule, file}, "--sample"},
		{"unexpected arguments", []string{"test", file, file}, file},
	} {
		t.Run(tc.name, func(t *testing.T) {
			code, _, stderr := runCLI(t, home, dir, tc.args, "")
			if code != 2 {
				t.Fatalf("code %d, expected 2 (%s)", code, stderr)
			}
			if !strings.Contains(stderr, tc.want) {
				t.Fatalf("stderr does not name %q: %q", tc.want, stderr)
			}
		})
	}
}

// Ad-hoc check of a rule before saving: the fixture of the plan TestTestCommandWithAdHocRule
// moved here — --pattern/--type/--sample belong to `rules test` (spec, :397).
func TestRulesTestAdHocRule(t *testing.T) {
	home, dir := t.TempDir(), gitDir(t)
	code, stdout, stderr := runCLI(t, home, dir, []string{"rules", "test",
		"--pattern", `ACME-\d{6}`, "--type", "TICKET", "--sample", "see ACME-123456"}, "")
	if code != 0 {
		t.Fatalf("exit %d: %s", code, stderr)
	}
	for _, want := range []string{"TICKET", "ACME-123456"} {
		if !strings.Contains(stdout, want) {
			t.Fatalf("got %q, missing %q", stdout, want)
		}
	}
}

// A mandatory distinguishing check: a rule that did not match the sample
// must not print a single span.
func TestRulesTestWithoutMatch(t *testing.T) {
	home, dir := t.TempDir(), gitDir(t)
	code, stdout, stderr := runCLI(t, home, dir, []string{"rules", "test",
		"--pattern", `ACME-\d{6}`, "--sample", noRule}, "")
	if code != 0 {
		t.Fatalf("exit %d: %s", code, stderr)
	}
	if !strings.Contains(stdout, "no matches") {
		t.Fatalf("the no-matches message is missing: %q", stdout)
	}
	if strings.Contains(stdout, "ACME") || strings.Contains(stdout, noRule) {
		t.Fatalf("a rule that did not match printed a span: %q", stdout)
	}
}

// A rule that does not work is an operational error (code 1), not a success with an empty
// with the table: the return code must tell "found nothing" from "the rule did not compile".
// Compile checks are the only place where the spec is validated, so
// both an unparsable pattern and an unknown kind and type fall here.
func TestRulesTestRejectsBadRule(t *testing.T) {
	home, dir := t.TempDir(), gitDir(t)
	for _, tc := range []struct {
		name string
		args []string
		want string
	}{
		{"pattern", []string{"rules", "test", "--pattern", `(?<!\d)x`, "--sample", "x"}, "pattern"},
		{"kind", []string{"rules", "test", "--pattern", "a", "--kind", "fast"}, "kind"},
		{"type", []string{"rules", "test", "--pattern", "a", "--type", "NOPE"}, "NOPE"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			code, _, stderr := runCLI(t, home, dir, tc.args, "")
			if code != 1 {
				t.Fatalf("code %d, expected 1 (%s)", code, stderr)
			}
			if !strings.Contains(stderr, tc.want) {
				t.Fatalf("stderr does not name %q: %q", tc.want, stderr)
			}
		})
	}
}

func TestRulesTestWithoutPattern(t *testing.T) {
	home, dir := t.TempDir(), gitDir(t)
	code, _, stderr := runCLI(t, home, dir,
		[]string{"rules", "test", "--sample", noRule}, "")
	if code != 2 {
		t.Fatalf("code %d, expected 2 (%s)", code, stderr)
	}
	if !strings.Contains(stderr, "--pattern") {
		t.Fatalf("stderr does not name the flag: %q", stderr)
	}
}
