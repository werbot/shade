package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/werbot/shade/internal/crypt"
	"github.com/werbot/shade/internal/store"
)

// secretValue is a value that the enabled builtin rule set catches (the rule
// assignment). Internal hosts and database names are not caught by the builtin rules:
// the user adds them with a rule of their own, and the end-to-end path "rule from the DB →
// anonymization → restoration" is checked in Task 13.
const secretValue = "Xk7pQ2mZr9Tv"

// mixedInput returns text in which one placeholder is resolvable and the second is not.
// On a homogeneous input the "stdout is empty" property has nothing to check: there was nothing
// would have printed even without the block.
func mixedInput(t *testing.T, home, dir string) string {
	t.Helper()
	code, masked, stderr := runCLI(t, home, dir, []string{"anon"},
		"password="+secretValue+"\nsee <HOST_99>\n")
	if code != 0 {
		t.Fatalf("setup: code=%d, err=%q", code, stderr)
	}
	if strings.Contains(masked, secretValue) {
		t.Fatalf("setup: the value is not masked: %q", masked)
	}
	return masked
}

func TestAnonMasksAndDeanonRestores(t *testing.T) {
	home, dir := t.TempDir(), gitDir(t)
	code, masked, stderr := runCLI(t, home, dir, []string{"anon"}, "password="+secretValue+"\n")
	if code != 0 {
		t.Fatalf("anon: %d %s", code, stderr)
	}
	if strings.Contains(masked, secretValue) {
		t.Fatalf("leaked: %q", masked)
	}

	code, restored, stderr := runCLI(t, home, dir, []string{"deanon"}, masked)
	if code != 0 {
		t.Fatalf("deanon: %d %s", code, stderr)
	}
	if !strings.Contains(restored, secretValue) {
		t.Fatalf("not restored: %q", restored)
	}
}

// In text mode fail_closed blocks the whole answer: with a non-empty Unresolved
// nothing goes to stdout. A mixed input makes the check distinguishing —
// resolvable part is contained in the text, and without the block it would have gone outside.
func TestDeanonBlocksStdoutOnUnresolved(t *testing.T) {
	home, dir := t.TempDir(), gitDir(t)
	masked := mixedInput(t, home, dir)

	code, stdout, stderr := runCLI(t, home, dir, []string{"deanon"}, masked)
	if code != 3 {
		t.Fatalf("code %d, expected 3 (%s)", code, stderr)
	}
	if stdout != "" {
		t.Fatalf("fail_closed must not return an answer: stdout=%q", stdout)
	}
	if !strings.Contains(stderr, "HOST_99") {
		t.Fatalf("stderr must name the token: %q", stderr)
	}
	if strings.Contains(stderr, secretValue) {
		t.Fatalf("stderr with a value: %q", stderr)
	}
}

// Under --json the contract for adapters is available even when blocked: text is empty, and
// the list of unresolved tokens is filled. The answer is blocked, not the diagnostics —
// an "empty answer" and an "empty input" can be told apart by the code and by a non-empty
// unresolved, whereas an unreachable form would not have been worth declaring.
func TestDeanonJSONReportsUnresolvedWhenBlocked(t *testing.T) {
	home, dir := t.TempDir(), gitDir(t)
	masked := mixedInput(t, home, dir)

	code, stdout, stderr := runCLI(t, home, dir, []string{"deanon", "--json"}, masked)
	if code != 3 {
		t.Fatalf("code %d, expected 3 (%s)", code, stderr)
	}
	var got struct {
		Text       string                       `json:"text"`
		Unresolved []struct{ Type, Raw string } `json:"unresolved"`
	}
	if err := json.Unmarshal([]byte(stdout), &got); err != nil {
		t.Fatalf("stdout is not JSON: %q (%v)", stdout, err)
	}
	if got.Text != "" {
		t.Fatalf("a blocked answer must be empty: %q", got.Text)
	}
	if len(got.Unresolved) != 1 || got.Unresolved[0].Type != "HOST" || got.Unresolved[0].Raw != "<HOST_99>" {
		t.Fatalf("unresolved: %+v", got.Unresolved)
	}
	if strings.Contains(stdout, secretValue) {
		t.Fatalf("--json leaked a value: %q", stdout)
	}
}

// The flip side: fail_open_log is what makes a partial output allowed.
// Without it the policy would have no visible difference besides the return code.
func TestDeanonFailOpenLogPrintsText(t *testing.T) {
	home, dir := t.TempDir(), gitDir(t)
	masked := mixedInput(t, home, dir)
	if err := os.WriteFile(filepath.Join(dir, ".shade.toml"),
		[]byte("fail_policy = \"fail_open_log\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	code, stdout, stderr := runCLI(t, home, dir, []string{"deanon"}, masked)
	if code != 0 {
		t.Fatalf("code %d, expected 0 under fail_open_log (%s)", code, stderr)
	}
	if !strings.Contains(stdout, secretValue) {
		t.Fatalf("the output must contain the restored value: %q", stdout)
	}
	if !strings.Contains(stderr, "HOST_99") {
		t.Fatalf("stderr must name the unresolved token: %q", stderr)
	}
}

func TestAnonJSONOutput(t *testing.T) {
	home, dir := t.TempDir(), gitDir(t)
	code, stdout, stderr := runCLI(t, home, dir, []string{"anon", "--json"}, "password="+secretValue+"\n")
	if code != 0 {
		t.Fatalf("exit %d: %s", code, stderr)
	}

	var got struct {
		Text  string                        `json:"text"`
		Spans []struct{ Type, Rule string } `json:"spans"`
	}
	if err := json.Unmarshal([]byte(stdout), &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Spans) == 0 || got.Spans[0].Type == "" {
		t.Fatalf("got %+v", got)
	}
	// --json is the path the adapters will take, so masking
	// is checked here too, and not only in the regular output.
	if strings.Contains(got.Text, secretValue) {
		t.Fatalf("--json leaked a value: %q", got.Text)
	}
	if !strings.Contains(got.Text, "<"+got.Spans[0].Type+"_") {
		t.Fatalf("text has no placeholder: %q", got.Text)
	}
}

func TestDeanonJSONOutput(t *testing.T) {
	t.Run("unresolved token", func(t *testing.T) {
		home, dir := t.TempDir(), gitDir(t)
		masked := mixedInput(t, home, dir)
		if err := os.WriteFile(filepath.Join(dir, ".shade.toml"),
			[]byte("fail_policy = \"fail_open_log\"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		code, stdout, stderr := runCLI(t, home, dir, []string{"deanon", "--json"}, masked)
		if code != 0 {
			t.Fatalf("exit %d: %s", code, stderr)
		}
		var got struct {
			Text       string                       `json:"text"`
			Unresolved []struct{ Type, Raw string } `json:"unresolved"`
		}
		if err := json.Unmarshal([]byte(stdout), &got); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(got.Text, secretValue) {
			t.Fatalf("text is not restored: %q", got.Text)
		}
		if len(got.Unresolved) != 1 || got.Unresolved[0].Type != "HOST" || got.Unresolved[0].Raw != "<HOST_99>" {
			t.Fatalf("unresolved: %+v", got.Unresolved)
		}
		// raw is the found token, not the value: values do not travel in JSON.
		if strings.Contains(got.Unresolved[0].Raw, secretValue) {
			t.Fatalf("raw leaked a value: %q", got.Unresolved[0].Raw)
		}
	})
	t.Run("empty unresolved — an array", func(t *testing.T) {
		home, dir := t.TempDir(), gitDir(t)
		code, masked, stderr := runCLI(t, home, dir, []string{"anon"}, "password="+secretValue+"\n")
		if code != 0 {
			t.Fatalf("setup: code=%d, err=%q", code, stderr)
		}
		code, stdout, stderr := runCLI(t, home, dir, []string{"deanon", "--json"}, masked)
		if code != 0 {
			t.Fatalf("exit %d: %s", code, stderr)
		}
		if !strings.Contains(stdout, `"unresolved":[]`) {
			t.Fatalf("an empty unresolved must be [], not null: %q", stdout)
		}
	})
}

// The project is chosen by directory, not by the process environment: --project points to
// another project, and the placeholder goes to it. The reverse run without the flag must
// not to find the value — otherwise the flag decides nothing.
func TestAnonProjectFlagPicksAnotherProject(t *testing.T) {
	home, dir, other := t.TempDir(), gitDir(t), gitDir(t)
	file := filepath.Join(t.TempDir(), "in.txt")
	if err := os.WriteFile(file, []byte("password="+secretValue+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	code, masked, stderr := runCLI(t, home, dir, []string{"anon", "--project", other, file}, "")
	if code != 0 {
		t.Fatalf("anon: %d %s", code, stderr)
	}
	if strings.Contains(masked, secretValue) {
		t.Fatalf("leaked: %q", masked)
	}

	if code, _, _ := runCLI(t, home, dir, []string{"deanon", "--project", other}, masked); code != 0 {
		t.Fatalf("deanon --project: code %d, the value must be found", code)
	}
	if code, _, _ := runCLI(t, home, dir, []string{"deanon"}, masked); code != 3 {
		t.Fatalf("deanon without --project: code %d, a value from another project", code)
	}
}

// A valid directory is needed by the flag not for beauty: ProjectForPath would fall back to
// absolute path of a nonexistent directory and would have put a row into projects that
// cannot be matched against anything else.
func TestProjectFlagRejectsBadDir(t *testing.T) {
	home, dir := t.TempDir(), gitDir(t)
	missing := filepath.Join(t.TempDir(), "nope")
	file := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}

	for _, bad := range []string{missing, file, "", "--json"} {
		code, _, stderr := runCLI(t, home, dir, []string{"anon", "--project", bad}, "текст\n")
		if code != 2 {
			t.Fatalf("--project %q: code %d, expected 2 (%s)", bad, code, stderr)
		}
	}

	// The check must come before openEngine: otherwise the junk row is already written.
	// We open the database separately and make sure there is no such project in it.
	key, err := crypt.LoadOrCreateKey(home)
	if err != nil {
		t.Fatal(err)
	}
	s, err := store.Open(home, key)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	var n int
	if err := s.DB().QueryRow(`SELECT count(*) FROM projects WHERE root_path LIKE ?`, missing+"%").Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("a path of a nonexistent directory got into projects: %s", missing)
	}
}

func TestBadArgsExitTwo(t *testing.T) {
	home, dir := t.TempDir(), gitDir(t)
	for _, args := range [][]string{
		{"anon", "--nope"},
		{"anon", "--project"},
		{"anon", "a.txt", "b.txt"},
		{"deanon", "--nope"},
		{"deanon", "a.txt", "b.txt"},
	} {
		code, _, stderr := runCLI(t, home, dir, args, "")
		if code != 2 {
			t.Fatalf("%v: code %d, expected 2 (%s)", args, code, stderr)
		}
		if stderr == "" {
			t.Fatalf("%v: code 2 without an explanation in stderr", args)
		}
		// The refusal must come from argument parsing: "unknown command"
		// would mean the command is not registered at all.
		if strings.Contains(stderr, "unknown command") {
			t.Fatalf("%v: %s", args, stderr)
		}
	}
}
