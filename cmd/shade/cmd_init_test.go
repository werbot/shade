package main

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// oldHookSettings is a user settings file holding the hook shade replaces: the
// previous redact_output.py on PostToolUse, next to a foreign hook on the same event.
// The two must be told apart — dropping the foreign one would be data loss.
const oldHookSettings = `{
  "hooks": {
    "PostToolUse": [
      {"matcher": "*", "hooks": [{"type": "command", "command": "~/.claude/hooks/redact_output.py"}]},
      {"matcher": "Bash", "hooks": [{"type": "command", "command": "~/.claude/hooks/audit_bash.sh"}]}
    ]
  }
}
`

// seedSettings writes the user settings file: the one Claude Code reads from HOME,
// not from SHADE_HOME.
func seedSettings(t *testing.T, home, content string) string {
	t.Helper()
	path := filepath.Join(home, ".claude", "settings.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// readFile reads a file a test expects to exist.
func readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// missing asserts that path does not exist.
func missing(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Stat(path); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("%s must not exist: %v", path, err)
	}
}

func TestInitIsIdempotent(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home) // Claude Code reads HOME: without this the test edits the real one
	repo := gitDir(t)

	code, out, errOut := runCLI(t, home, repo, []string{"init"}, "")
	if code != 0 {
		t.Fatalf("code=%d err=%s", code, errOut)
	}
	if !strings.Contains(out, "hooks/hooks.json") {
		t.Fatalf("the diff must show created plugin files: %s", out)
	}
	code, out, _ = runCLI(t, home, repo, []string{"init"}, "")
	if code != 0 {
		t.Fatalf("the second run failed: code=%d", code)
	}
	// No line may begin with "+": a plain search for "+" anywhere would also match a
	// temp path, and the second run legitimately still prints the trust reminder.
	for _, line := range strings.Split(out, "\n") {
		if strings.HasPrefix(line, "+") {
			t.Fatalf("a second init must be a no-op: %s", out)
		}
	}
}

func TestInitDryRunPrintsButWritesNothing(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	repo := gitDir(t)

	code, out, _ := runCLI(t, home, repo, []string{"init", "--dry-run"}, "")
	if code != 0 || !strings.Contains(out, "hooks/hooks.json") {
		t.Fatalf("dry run must show what it would create: code=%d out=%q", code, out)
	}
	if _, err := os.Stat(filepath.Join(home, "claude", "hooks", "hooks.json")); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("dry run wrote a plugin file: %v", err)
	}
	if _, err := os.Stat(filepath.Join(repo, ".claude", "settings.json")); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("dry run wrote the project settings: %v", err)
	}
	// The directories too, not only the files: creating them is a write as well.
	missing(t, filepath.Join(home, "claude"))
	missing(t, filepath.Join(home, ".claude", "settings.json"))
}

func TestInitRemovesTheOldRedactHook(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	repo := gitDir(t)
	userSettings := seedSettings(t, home, oldHookSettings)

	if code, _, errOut := runCLI(t, home, repo, []string{"init"}, ""); code != 0 {
		t.Fatalf("code=%d err=%s", code, errOut)
	}
	got := readFile(t, userSettings)
	if strings.Contains(got, "redact_output.py") {
		t.Fatalf("the old hook stayed: %s", got)
	}
	if !strings.Contains(got, "audit_bash.sh") {
		t.Fatalf("a foreign hook on the same event was dropped: %s", got)
	}
}

func TestInitKeepsTheOldHookOnFlag(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	repo := gitDir(t)
	userSettings := seedSettings(t, home, oldHookSettings)

	code, out, errOut := runCLI(t, home, repo, []string{"init", "--keep-old-hook"}, "")
	if code != 0 {
		t.Fatalf("code=%d err=%s", code, errOut)
	}
	if !strings.Contains(readFile(t, userSettings), "redact_output.py") {
		t.Fatal("the old hook was removed despite --keep-old-hook")
	}
	// The exact message, not "warning": the temporary-build-dir warning always fires
	// under `go test`, so a search for the word alone could never fail.
	if !strings.Contains(out, keepOldWarning) {
		t.Fatalf("the flag must warn about the competing rewrites: %s", out)
	}
}

func TestInitGlobalWritesEnabledPluginsToUserSettings(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	repo := gitDir(t)

	code, _, errOut := runCLI(t, home, repo, []string{"init", "--global"}, "")
	if code != 0 {
		t.Fatalf("code=%d err=%s", code, errOut)
	}
	if got := readFile(t, filepath.Join(home, ".claude", "settings.json")); !strings.Contains(got, "shade@shade") {
		t.Fatalf("--global did not enable the plugin in the user settings: %s", got)
	}
	missing(t, filepath.Join(repo, ".claude", "settings.json"))
}

func TestInitProjectWritesEnabledPluginsToProjectSettings(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	repo := gitDir(t)

	code, _, errOut := runCLI(t, home, repo, []string{"init"}, "")
	if code != 0 {
		t.Fatalf("code=%d err=%s", code, errOut)
	}
	if got := readFile(t, filepath.Join(repo, ".claude", "settings.json")); !strings.Contains(got, "shade@shade") {
		t.Fatalf("the default run did not enable the plugin in the project settings: %s", got)
	}
	// The marketplace declaration is the one key that always belongs to the user
	// settings: Claude Code ignores it in the project file.
	user := readFile(t, filepath.Join(home, ".claude", "settings.json"))
	if !strings.Contains(user, "extraKnownMarketplaces") {
		t.Fatalf("the marketplace was not declared in the user settings: %s", user)
	}
	if strings.Contains(user, "enabledPlugins") {
		t.Fatalf("the default run enabled the plugin in the user settings: %s", user)
	}
}

func TestInitInARepositoryWithoutClaudeDirCreatesIt(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	repo := gitDir(t)
	missing(t, filepath.Join(repo, ".claude"))

	code, _, errOut := runCLI(t, home, repo, []string{"init"}, "")
	if code != 0 {
		t.Fatalf("code=%d err=%s", code, errOut)
	}
	if got := readFile(t, filepath.Join(repo, ".claude", "settings.json")); !strings.Contains(got, "shade@shade") {
		t.Fatalf("the project settings were not created: %s", got)
	}
}

// The project of shade is the repository, and Claude Code reads the project settings
// only from the directory it was launched in: settings written into a subdirectory
// would never be read by a session started at the root, and the plugin would stay off
// with nothing reporting an error.
func TestInitFromASubdirectoryWritesToTheRepoRoot(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	repo := gitDir(t)
	sub := filepath.Join(repo, "internal", "foo")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}

	code, _, errOut := runCLI(t, home, sub, []string{"init"}, "")
	if code != 0 {
		t.Fatalf("code=%d err=%s", code, errOut)
	}
	if got := readFile(t, filepath.Join(repo, ".claude", "settings.json")); !strings.Contains(got, "shade@shade") {
		t.Fatalf("the plugin was not enabled at the repository root: %s", got)
	}
	missing(t, filepath.Join(sub, ".claude", "settings.json"))
}

// The exit code contract: a bad flag and a positional argument are 2, and nothing is
// written before the arguments are parsed.
func TestInitRejectsBadArguments(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	repo := gitDir(t)

	for _, arg := range []string{"--nope", "extra"} {
		code, _, errOut := runCLI(t, home, repo, []string{"init", arg}, "")
		if code != 2 {
			t.Fatalf("%s: code=%d, want 2 (%s)", arg, code, errOut)
		}
		if errOut == "" {
			t.Fatalf("%s: exit 2 without an explanation on stderr", arg)
		}
	}
	missing(t, filepath.Join(home, ".claude"))
	missing(t, filepath.Join(home, "claude"))
}
