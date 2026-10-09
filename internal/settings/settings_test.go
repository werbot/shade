package settings_test

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/werbot/shade/internal/settings"
)

// settingsFixture is the anonymized foreign config the merge and removal tests
// work on: it carries a foreign plugin, a foreign hook on the same event as the
// old one, and keys shade never touches.
var settingsFixture = filepath.Join("testdata", "settings.json")

func load(t *testing.T, path string) settings.File {
	t.Helper()
	f, err := settings.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	return f
}

func TestEnablePluginKeepsForeignSettings(t *testing.T) {
	f := load(t, settingsFixture)
	f.EnablePlugin("shade@shade", true)

	if f["model"] != "claude-sonnet-5" {
		t.Fatalf("a foreign top-level key changed: %v", f["model"])
	}
	perms, ok := f["permissions"].(map[string]any)
	if !ok {
		t.Fatalf("permissions is not an object: %v", f["permissions"])
	}
	allow, ok := perms["allow"].([]any)
	if !ok || len(allow) != 2 || allow[0] != "Bash(git status)" || allow[1] != "Read" {
		t.Fatalf("permissions.allow changed: %v", perms["allow"])
	}
	plugins, ok := f["enabledPlugins"].(map[string]any)
	if !ok {
		t.Fatalf("enabledPlugins is not an object: %v", f["enabledPlugins"])
	}
	if plugins["formatter@anthropic-tools"] != true {
		t.Fatalf("a foreign plugin was dropped: %v", plugins)
	}
	if plugins["shade@shade"] != true {
		t.Fatalf("shade was not enabled: %v", plugins)
	}
}

func TestEnablePluginIsIdempotent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")

	f := load(t, settingsFixture)
	f.EnablePlugin("shade@shade", true)
	if err := f.Save(path); err != nil {
		t.Fatal(err)
	}
	first, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	// The second run reads what the first one wrote, so this is the real contract
	// of a repeated `shade init`, not just two calls on one in-memory map.
	g := load(t, path)
	g.EnablePlugin("shade@shade", true)
	if err := g.Save(path); err != nil {
		t.Fatal(err)
	}
	second, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(first, second) {
		t.Fatalf("a repeated merge changed the file:\n%s\n---\n%s", first, second)
	}
}

func TestRemoveHooksDropsOnlyMatchingCommand(t *testing.T) {
	f := load(t, settingsFixture)

	removed := f.RemoveHooks("PostToolUse", func(command string) bool {
		return strings.Contains(command, "redact_output.py")
	})
	if removed != 1 {
		t.Fatalf("removed %d hooks, want 1", removed)
	}

	b, err := f.Bytes()
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), "redact_output.py") {
		t.Fatalf("the old hook stayed:\n%s", b)
	}
	if !strings.Contains(string(b), "audit_bash.sh") {
		t.Fatalf("a foreign hook on the same event was dropped:\n%s", b)
	}
	hooks, ok := f["hooks"].(map[string]any)
	if !ok {
		t.Fatalf("hooks is not an object: %v", f["hooks"])
	}
	if _, ok := hooks["PostToolUse"]; !ok {
		t.Fatal("the event still holding a foreign hook disappeared")
	}
}

func TestRemoveHooksDropsEmptiedGroups(t *testing.T) {
	f := load(t, settingsFixture)

	removed := f.RemoveHooks("SessionStart", func(command string) bool {
		return strings.Contains(command, "session_start.sh")
	})
	if removed != 1 {
		t.Fatalf("removed %d hooks, want 1", removed)
	}

	hooks, ok := f["hooks"].(map[string]any)
	if !ok {
		t.Fatalf("hooks is not an object: %v", f["hooks"])
	}
	if _, ok := hooks["SessionStart"]; ok {
		t.Fatalf("the emptied event stayed: %v", hooks["SessionStart"])
	}
	b, err := f.Bytes()
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), "SessionStart") {
		t.Fatalf("an empty event would read as configured but do nothing:\n%s", b)
	}
}

func TestLoadMissingFileIsEmptyNotAnError(t *testing.T) {
	f, err := settings.Load(filepath.Join(t.TempDir(), "absent.json"))
	if err != nil {
		t.Fatalf("a missing file must not be an error: %v", err)
	}
	if len(f) != 0 {
		t.Fatalf("want an empty file, got %v", f)
	}
}

func TestBytesIsCanonicalAndStable(t *testing.T) {
	f := settings.File{
		"b": "1",
		"a": map[string]any{"z": "1", "y": "2"},
	}
	want := "{\n  \"a\": {\n    \"y\": \"2\",\n    \"z\": \"1\"\n  },\n  \"b\": \"1\"\n}\n"

	got, err := f.Bytes()
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != want {
		t.Fatalf("not canonical:\n%q\nwant:\n%q", got, want)
	}
	again, err := f.Bytes()
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, again) {
		t.Fatal("two calls gave different bytes")
	}
}

func TestDiffShowsOnlyChangedLines(t *testing.T) {
	before := []byte("{\n  \"a\": 1\n}\n")
	after := []byte("{\n  \"a\": 2\n}\n")

	want := " {\n-  \"a\": 1\n+  \"a\": 2\n }\n"
	if got := settings.Diff(before, after); got != want {
		t.Fatalf("diff:\n%q\nwant:\n%q", got, want)
	}
	if got := settings.Diff(before, before); got != "" {
		t.Fatalf("equal inputs must give an empty diff, got %q", got)
	}
	if got := settings.Diff(nil, []byte("x\n")); got != "+x\n" {
		t.Fatalf("a created file is all additions, got %q", got)
	}
	if got := settings.Diff([]byte("x\n"), nil); got != "-x\n" {
		t.Fatalf("a deleted file is all removals, got %q", got)
	}
}
