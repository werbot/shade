package main

import (
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/werbot/shade/internal/crypt"
	"github.com/werbot/shade/internal/store"
)

func TestHookPrintsNothingForAnOrdinaryPrompt(t *testing.T) {
	home, repo := t.TempDir(), gitDir(t)
	payload := fmt.Sprintf(`{"hook_event_name":"UserPromptSubmit","cwd":%q,"prompt":"hello"}`, repo)
	code, out, errOut := runCLI(t, home, repo, []string{"hook"}, payload)
	if code != 0 || out != "" || errOut != "" {
		t.Fatalf("code=%d out=%q err=%q", code, out, errOut)
	}
}

func TestHookAnswersSessionStartWithTheDirective(t *testing.T) {
	home, repo := t.TempDir(), gitDir(t)
	payload := fmt.Sprintf(`{"hook_event_name":"SessionStart","cwd":%q,"source":"startup"}`, repo)
	code, out, _ := runCLI(t, home, repo, []string{"hook"}, payload)
	if code != 0 || !strings.Contains(out, `"hookEventName":"SessionStart"`) {
		t.Fatalf("code=%d out=%q", code, out)
	}
}

// The hook opens the engine for the cwd from the payload, not for the working directory
// of the process: Claude Code starts the hook anywhere, and the payload is the only truth
// about the project. The test runs the command from another directory and looks at which
// project got registered.
func TestHookOpensThePayloadProject(t *testing.T) {
	home, payloadDir, runDir := t.TempDir(), gitDir(t), gitDir(t)
	payload := fmt.Sprintf(`{"hook_event_name":"SessionStart","cwd":%q,"source":"startup"}`, payloadDir)
	if code, _, stderr := runCLI(t, home, runDir, []string{"hook"}, payload); code != 0 {
		t.Fatalf("code=%d err=%q", code, stderr)
	}
	roots := projectRoots(t, home)
	if !slices.Contains(roots, realPath(t, payloadDir)) {
		t.Fatalf("the project of the payload was not registered: %v", roots)
	}
	if slices.Contains(roots, realPath(t, runDir)) {
		t.Fatalf("the project of the working directory was registered: %v", roots)
	}
}

// The hook never breaks the session: an unreadable payload is silence with exit 0, and
// the command takes no arguments — a stray one is a usage error.
func TestHookSurvivesJunkAndRejectsArgs(t *testing.T) {
	home, repo := t.TempDir(), gitDir(t)
	for _, payload := range []string{"", "not json", `{"cwd":"/tmp"}`} {
		code, out, errOut := runCLI(t, home, repo, []string{"hook"}, payload)
		if code != 0 || out != "" || errOut != "" {
			t.Fatalf("payload %q: code=%d out=%q err=%q", payload, code, out, errOut)
		}
	}
	if code, _, stderr := runCLI(t, home, repo, []string{"hook", "extra"}, ""); code != 2 || stderr == "" {
		t.Fatalf("extra argument: code=%d err=%q", code, stderr)
	}
}

// projectRoots returns the root paths of the registered projects: the hook's choice of
// project is visible only through the store, because every response hides it.
func projectRoots(t *testing.T, home string) []string {
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
	defer st.Close()
	rows, err := st.DB().Query(`SELECT root_path FROM projects`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var roots []string
	for rows.Next() {
		var root string
		if err := rows.Scan(&root); err != nil {
			t.Fatal(err)
		}
		roots = append(roots, root)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return roots
}

// realPath resolves symlinks: the store records the canonical path (on macOS a temporary
// directory is /private/var/...), so a raw t.TempDir() would not match.
func realPath(t *testing.T, path string) string {
	t.Helper()
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		t.Fatal(err)
	}
	return resolved
}
