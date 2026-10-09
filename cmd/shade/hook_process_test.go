package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
)

// shadeBinDir holds the CLI built for the whole test binary; TestMain removes it so a
// per-test t.TempDir() cannot pull the binary out from under a later test.
var shadeBinDir string

func TestMain(m *testing.M) {
	code := m.Run()
	if shadeBinDir != "" {
		os.RemoveAll(shadeBinDir)
	}
	os.Exit(code)
}

var (
	shadeBinOnce sync.Once
	shadeBinPath string
	shadeBinErr  error
)

// buildShade builds the CLI once. The in-process tests see the handler through Dispatch;
// only a real subprocess sees exactly what Claude Code sees — the bytes on stdout and
// the exit code.
func buildShade(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("go"); err != nil {
		t.Skipf("go is not on PATH: %v", err)
	}
	shadeBinOnce.Do(func() {
		dir, err := os.MkdirTemp("", "shade-bin")
		if err != nil {
			shadeBinErr = err
			return
		}
		shadeBinDir = dir
		root, err := filepath.Abs("../..")
		if err != nil {
			shadeBinErr = err
			return
		}
		shadeBinPath = filepath.Join(dir, "shade")
		cmd := exec.Command("go", "build", "-o", shadeBinPath, "./cmd/shade")
		cmd.Dir = root
		if out, err := cmd.CombinedOutput(); err != nil {
			shadeBinErr = fmt.Errorf("go build: %v: %s", err, out)
		}
	})
	if shadeBinErr != nil {
		t.Fatalf("building the CLI: %v", shadeBinErr)
	}
	return shadeBinPath
}

// runHookProcess runs the built CLI as Claude Code does: the payload on stdin, the state
// directory in the environment, and the working directory somewhere other than the
// payload's cwd.
func runHookProcess(t *testing.T, bin, home, workDir, payload string) (int, string, string) {
	t.Helper()
	env := slices.DeleteFunc(os.Environ(), func(e string) bool {
		return strings.HasPrefix(e, "SHADE_HOME=")
	})
	cmd := exec.Command(bin, "hook")
	cmd.Dir = workDir
	cmd.Env = append(env, "SHADE_HOME="+home)
	cmd.Stdin = strings.NewReader(payload)
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	if err := cmd.Run(); err != nil {
		var exit *exec.ExitError
		if !errors.As(err, &exit) {
			t.Fatalf("running the hook: %v", err)
		}
	}
	return cmd.ProcessState.ExitCode(), out.String(), errb.String()
}

// TestHookProcessContract checks the contract Claude Code depends on: exit 0, stderr
// empty, and stdout carrying at most one JSON document — a stray log line or a second
// object would be read as a decision.
func TestHookProcessContract(t *testing.T) {
	bin := buildShade(t)
	home, repo, workDir := t.TempDir(), gitDir(t), t.TempDir()

	t.Run("session start prints one document", func(t *testing.T) {
		payload := fmt.Sprintf(`{"hook_event_name":"SessionStart","cwd":%q,"source":"startup"}`, repo)
		code, out, errOut := runHookProcess(t, bin, home, workDir, payload)
		if code != 0 {
			t.Fatalf("code=%d err=%q", code, errOut)
		}
		if errOut != "" {
			t.Fatalf("stderr must be empty, got %q", errOut)
		}
		var doc map[string]any
		if err := json.Unmarshal([]byte(out), &doc); err != nil {
			t.Fatalf("stdout is not exactly one JSON document: %q (%v)", out, err)
		}
		if doc["hookSpecificOutput"] == nil {
			t.Fatalf("the answer carries no hookSpecificOutput: %q", out)
		}
	})

	t.Run("junk on stdin", func(t *testing.T) {
		code, out, errOut := runHookProcess(t, bin, home, workDir, "not json at all")
		if code != 0 || out != "" {
			t.Fatalf("code=%d out=%q", code, out)
		}
		if errOut != "" {
			t.Fatalf("stderr must be empty, got %q", errOut)
		}
	})

	t.Run("post tool use masks the ssh target", func(t *testing.T) {
		payload := fmt.Sprintf(
			`{"hook_event_name":"PostToolUse","cwd":%q,"tool_name":"Bash",`+
				`"tool_response":{"stdout":"ssh alice@db.prod.local\n"}}`, repo)
		code, out, errOut := runHookProcess(t, bin, home, workDir, payload)
		if code != 0 {
			t.Fatalf("code=%d err=%q", code, errOut)
		}
		if errOut != "" {
			t.Fatalf("stderr must be empty, got %q", errOut)
		}
		if strings.Contains(out, "db.prod.local") {
			t.Fatalf("the raw host leaked to stdout: %q", out)
		}
		if !strings.Contains(out, "<HOST_1>") || !strings.Contains(out, "<USER_1>") {
			t.Fatalf("the answer has no placeholders: %q", out)
		}
		var doc map[string]any
		if err := json.Unmarshal([]byte(out), &doc); err != nil {
			t.Fatalf("stdout is not exactly one JSON document: %q (%v)", out, err)
		}
	})
}
