package main

import (
	"strings"
	"testing"
)

func TestRulesAddIsRejectedIfRegexDoesNotCompile(t *testing.T) {
	home, dir := t.TempDir(), gitDir(t)
	code, _, stderr := runCLI(t, home, dir, []string{"rules", "add", "--name", "bad",
		"--type", "HOST", "--pattern", `(?<!\d)x`, "--global"}, "")
	if code != 1 {
		t.Fatalf("want exit 1, got %d", code)
	}
	if !strings.Contains(stderr, "bad") {
		t.Fatalf("got %q", stderr)
	}

	// nothing was written: the rule does not appear in the list
	code, stdout, _ := runCLI(t, home, dir, []string{"rules", "list"}, "")
	if code != 0 {
		t.Fatal("list failed")
	}
	if strings.Contains(stdout, "bad") {
		t.Fatalf("failed rule was persisted: %q", stdout)
	}
}

// addAcme creates a user rule in the given scope. Shared across tests,
// where the rule is setup, not the subject of the check.
func addAcme(t *testing.T, home, dir string, global bool) {
	t.Helper()
	args := []string{"rules", "add", "--name", "acme", "--type", "TICKET",
		"--pattern", `ACME-\d{6}`}
	if global {
		args = append(args, "--global")
	}
	if code, _, stderr := runCLI(t, home, dir, args, ""); code != 0 {
		t.Fatalf("setup rules add: %d %s", code, stderr)
	}
}

func TestRulesAddThenListThenDisable(t *testing.T) {
	home, dir := t.TempDir(), gitDir(t)
	addAcme(t, home, dir, true)

	if code, stdout, _ := runCLI(t, home, dir, []string{"rules", "list"}, ""); code != 0 ||
		!strings.Contains(stdout, "acme") {
		t.Fatalf("list: %d %q", code, stdout)
	}
	if code, _, stderr := runCLI(t, home, dir,
		[]string{"rules", "disable", "--name", "acme", "--global"}, ""); code != 0 {
		t.Fatalf("disable: %d %s", code, stderr)
	}
	if code, stdout, _ := runCLI(t, home, dir,
		[]string{"test", "--sample", "see ACME-123456"}, ""); code != 0 ||
		strings.Contains(stdout, "acme") {
		t.Fatalf("disabled rule still fires: %q", stdout)
	}
}

// The flip side of the previous test: an enabled user rule must
// be visible to `shade test`. Without it an implementation that never reads the database
// would pass the whole set: a disabled rule is absent from memory exactly as it
// is not in the output.
func TestSelfTestShowsEnabledUserRule(t *testing.T) {
	home, dir := t.TempDir(), gitDir(t)
	addAcme(t, home, dir, true)

	code, stdout, stderr := runCLI(t, home, dir,
		[]string{"test", "--sample", "see ACME-123456"}, "")
	if code != 0 {
		t.Fatalf("test: %d %s", code, stderr)
	}
	if !strings.Contains(stdout, "acme") || !strings.Contains(stdout, "TICKET") {
		t.Fatalf("the user rule did not fire: %q", stdout)
	}
}

// A rule added without --global lives in the current project: in its own directory
// visible, in another it is not.
func TestRulesAddedWithoutGlobalIsProjectScoped(t *testing.T) {
	home, dir, other := t.TempDir(), gitDir(t), gitDir(t)
	addAcme(t, home, dir, false)

	if code, stdout, _ := runCLI(t, home, dir, []string{"rules", "list"}, ""); code != 0 ||
		!strings.Contains(stdout, "acme") {
		t.Fatalf("the rule is missing in its own project: %d %q", code, stdout)
	}
	if code, stdout, _ := runCLI(t, home, other, []string{"rules", "list"}, ""); code != 0 ||
		strings.Contains(stdout, "acme") {
		t.Fatalf("a project rule is visible in another project: %d %q", code, stdout)
	}
}

func TestRulesRejectsBadArgs(t *testing.T) {
	home, dir := t.TempDir(), gitDir(t)
	for _, tc := range []struct {
		name string
		args []string
		want string
	}{
		{"add without --type", []string{"rules", "add", "--name", "x", "--pattern", "a"}, "--type"},
		{"add without --pattern", []string{"rules", "add", "--name", "x", "--type", "HOST"}, "--pattern"},
		{"add without --name", []string{"rules", "add", "--type", "HOST", "--pattern", "a"}, "--name"},
		{"rm without --name", []string{"rules", "rm"}, "--name"},
		{"list with --global", []string{"rules", "list", "--global"}, "--global"},
		{"list with an unexpected argument", []string{"rules", "list", "file"}, "file"},
		{"unknown subcommand", []string{"rules", "nope"}, "nope"},
		{"rules without a subcommand", []string{"rules"}, "list"},
		{"add with an unexpected argument", []string{"rules", "add", "--name", "x",
			"--type", "HOST", "--pattern", "a", "file"}, "file"},
		// A value starting with a dash, in the separate form, is the next
		// flag, not the value: otherwise `--name --global` would swallow --global.
		{"value with a dash", []string{"rules", "add", "--name", "-x",
			"--type", "HOST", "--pattern", "a"}, "--name"},
		// A boolean flag does not take a value in any form.
		{"a value on a boolean flag", []string{"rules", "rm", "--name", "x", "--global=1"},
			"--global"},
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

// rm refuses a builtin rule and a typo in the name, and both refusals must
// reach the user in words: a builtin rule can be disabled, but not
// is deleted, whereas a name not found is a typo, not a database failure.
func TestRulesRmBuiltinAndUnknown(t *testing.T) {
	home, dir := t.TempDir(), gitDir(t)
	for _, tc := range []struct {
		name  string
		args  []string
		wants []string
	}{
		{"builtin", []string{"rules", "rm", "--name", "assignment", "--global"},
			[]string{"assignment", "builtin"}},
		{"unknown", []string{"rules", "rm", "--name", "nope", "--global"},
			[]string{"nope", "not found"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			code, _, stderr := runCLI(t, home, dir, tc.args, "")
			if code != 1 {
				t.Fatalf("code %d, expected 1 (%s)", code, stderr)
			}
			for _, want := range tc.wants {
				if !strings.Contains(stderr, want) {
					t.Fatalf("stderr does not explain the refusal, missing %q: %q", want, stderr)
				}
			}
		})
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

// End-to-end path "rule from the DB → anonymization → restoration".
// Internal hosts are not caught by the builtin rule set: the user adds them.
// The input is a line with the host itself: the original fixture of the plan had none, and
// the HOST rule had nothing to fire on.
func TestAnonHonoursProjectRule(t *testing.T) {
	home, dir := t.TempDir(), gitDir(t)
	if code, _, stderr := runCLI(t, home, dir, []string{"rules", "add", "--name",
		"internal-host", "--type", "HOST", "--pattern", `db\.prod\.local`,
		"--global"}, ""); code != 0 {
		t.Fatalf("rules add: %d %s", code, stderr)
	}
	code, masked, stderr := runCLI(t, home, dir, []string{"anon"}, "ssh db.prod.local\n")
	if code != 0 {
		t.Fatalf("anon: %d %s", code, stderr)
	}
	if !strings.Contains(masked, "<HOST_1>") {
		t.Fatalf("got %q", masked)
	}
	if strings.Contains(masked, "db.prod.local") {
		t.Fatalf("leaked: %q", masked)
	}

	code, restored, stderr := runCLI(t, home, dir, []string{"deanon"}, masked)
	if code != 0 {
		t.Fatalf("deanon: %d %s", code, stderr)
	}
	if !strings.Contains(restored, "db.prod.local") {
		t.Fatalf("not restored: %q", restored)
	}
}

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

// A pattern starting with a dash is created only by the --flag=value form: in the
// in the separate form parseFlags treats such a value as the next flag.
func TestRulesAddAcceptsDashLeadingPattern(t *testing.T) {
	home, dir := t.TempDir(), gitDir(t)
	code, _, stderr := runCLI(t, home, dir, []string{"rules", "add", "--name", "pem",
		"--type", "SECRET", "--pattern=-{5}BEGIN", "--global"}, "")
	if code != 0 {
		t.Fatalf("add: %d %s", code, stderr)
	}
	code, stdout, stderr := runCLI(t, home, dir, []string{"test", "--sample=-----BEGIN RSA KEY"}, "")
	if code != 0 {
		t.Fatalf("test: %d %s", code, stderr)
	}
	if !strings.Contains(stdout, "pem") {
		t.Fatalf("the rule with a leading dash did not fire: %q", stdout)
	}
}
