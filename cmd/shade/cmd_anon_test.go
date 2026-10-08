package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// secretValue is a value that the enabled builtin rule set catches (the rule
// assignment). Internal hosts and database names are not caught by the builtin rules:
// the user adds them with a rule of their own, and the end-to-end path "rule from the DB →
// anonymization → restoration" is checked in Task 13.
const secretValue = "Xk7pQ2mZr9Tv"

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

func TestDeanonExitCodeThreeOnUnresolved(t *testing.T) {
	home, dir := t.TempDir(), gitDir(t)
	code, stdout, stderr := runCLI(t, home, dir, []string{"deanon"}, "see <HOST_99>\n")
	if code != 3 {
		t.Fatalf("want exit 3, got %d", code)
	}
	if !strings.Contains(stderr, "HOST_99") {
		t.Fatalf("stderr must name the token: %q", stderr)
	}
	if strings.Contains(stdout, secretValue) {
		t.Fatal("stdout must not contain values")
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

func TestAnonRejectsBadArgs(t *testing.T) {
	home, dir := t.TempDir(), gitDir(t)
	for _, args := range [][]string{
		{"anon", "--nope"},
		{"anon", "--project"},
		{"anon", "a.txt", "b.txt"},
		{"deanon", "--json"}, // the flag exists only on anon
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

// fail_open_log from the project config lowers code 3 to a warning: the output
// is handed over as is, but the unresolved token must be named.
func TestDeanonFailOpenLogDowngradesExitCode(t *testing.T) {
	home, dir := t.TempDir(), gitDir(t)
	if err := os.WriteFile(filepath.Join(dir, ".shade.toml"),
		[]byte("fail_policy = \"fail_open_log\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	code, stdout, stderr := runCLI(t, home, dir, []string{"deanon"}, "see <HOST_99>\n")
	if code != 0 {
		t.Fatalf("code %d, expected 0 under fail_open_log (%s)", code, stderr)
	}
	if !strings.Contains(stdout, "<HOST_99>") || !strings.Contains(stderr, "HOST_99") {
		t.Fatalf("stdout=%q stderr=%q", stdout, stderr)
	}
}
