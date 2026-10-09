package config_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/werbot/shade/internal/config"
)

func TestDefaultValues(t *testing.T) {
	c := config.Default()
	if c.FailPolicy != "fail_closed" {
		t.Fatalf("got %q", c.FailPolicy)
	}
	if c.EntitiesTTL != "90d" {
		t.Fatalf("got %q", c.EntitiesTTL)
	}
}

func TestLoadWithoutFilesReturnsDefaults(t *testing.T) {
	c, err := config.Load(t.TempDir(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if c.FailPolicy != "fail_closed" {
		t.Fatalf("got %q", c.FailPolicy)
	}
	if c.EntitiesTTL != "90d" {
		t.Fatalf("got %q", c.EntitiesTTL)
	}
}

func TestProjectFileOverridesGlobalAndDefaults(t *testing.T) {
	home, root := t.TempDir(), t.TempDir()
	write(t, filepath.Join(home, "config.toml"),
		"fail_policy = \"fail_open_log\"\nentities_ttl = \"30d\"\n")
	write(t, filepath.Join(root, ".shade.toml"), "entities_ttl = \"7d\"\n")

	c, err := config.Load(home, root)
	if err != nil {
		t.Fatal(err)
	}
	if c.EntitiesTTL != "7d" {
		t.Fatalf("project must win: %q", c.EntitiesTTL)
	}
	if c.FailPolicy != "fail_open_log" {
		t.Fatalf("global must survive: %q", c.FailPolicy)
	}
}

func TestDefaultSurvivesWhenNeitherFileSetsField(t *testing.T) {
	home, root := t.TempDir(), t.TempDir()
	write(t, filepath.Join(root, ".shade.toml"), "fail_policy = \"fail_open_log\"\n")

	c, err := config.Load(home, root)
	if err != nil {
		t.Fatal(err)
	}
	if c.EntitiesTTL != "90d" {
		t.Fatalf("default must survive: %q", c.EntitiesTTL)
	}
}

func TestLoadBrokenFileFails(t *testing.T) {
	root := t.TempDir()
	write(t, filepath.Join(root, ".shade.toml"), "fail_policy = ")

	_, err := config.Load(t.TempDir(), root)
	if err == nil {
		t.Fatal("broken config must error")
	}
	// The path in the message is mandatory: there are two configs, otherwise it is unclear which one to fix.
	if !strings.Contains(err.Error(), filepath.Join(root, ".shade.toml")) {
		t.Fatalf("error must name the file: %v", err)
	}
}

// TestUnknownKeysAreIgnored — the keys of future phases (stream_mode and others) in the file
// are allowed: we do not keep a field without a consumer, but we must not fail on it.
func TestUnknownKeysAreIgnored(t *testing.T) {
	root := t.TempDir()
	write(t, filepath.Join(root, ".shade.toml"), "stream_mode = true\nupstream = \"http://x\"\n")

	c, err := config.Load(t.TempDir(), root)
	if err != nil {
		t.Fatal(err)
	}
	if c.FailPolicy != "fail_closed" {
		t.Fatalf("got %q", c.FailPolicy)
	}
}

// TestEmptyDirsSkipLayerInsteadOfReadingCwd — an empty layer directory (home comes
// from store.Home() and may be empty) must skip the layer, and not turn into
// a relative path: filepath.Join("", name) would point at the current directory, and
// a config from cwd would be picked up as a foreign one. The test is discriminating — both layer files
// are known to lie in cwd, so removing the guard fails exactly these checks, and not
// hits ErrNotExist, as it would in the package directory.
func TestEmptyDirsSkipLayerInsteadOfReadingCwd(t *testing.T) {
	cwd := t.TempDir()
	write(t, filepath.Join(cwd, "config.toml"), "fail_policy = \"fail_open_log\"\n")
	write(t, filepath.Join(cwd, ".shade.toml"), "entities_ttl = \"7d\"\n")
	t.Chdir(cwd)

	c, err := config.Load("", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if c.FailPolicy != "fail_closed" {
		t.Fatalf("empty home must not read cwd/config.toml: %q", c.FailPolicy)
	}

	c, err = config.Load(t.TempDir(), "")
	if err != nil {
		t.Fatal(err)
	}
	if c.EntitiesTTL != "90d" {
		t.Fatalf("empty project root must not read cwd/.shade.toml: %q", c.EntitiesTTL)
	}
}

// TestPromptGateDefaultIsOffAndUnknownValueFails — a misspelled policy is an error,
// not a silently off gate: on|off|auto guards against a leak, and a typo must be
// visible instead of quietly disabling the gate.
func TestPromptGateDefaultIsOffAndUnknownValueFails(t *testing.T) {
	c, err := config.Load("", "")
	if err != nil || c.PromptGate != "off" || c.PromptGateEnabled() {
		t.Fatalf("default: %+v, enabled=%v, err=%v", c, c.PromptGateEnabled(), err)
	}
	home := t.TempDir()
	if err := os.WriteFile(filepath.Join(home, "config.toml"), []byte("prompt_gate = \"on\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	c, err = config.Load(home, "")
	if err != nil || !c.PromptGateEnabled() {
		t.Fatalf("on: enabled=%v, err=%v", c.PromptGateEnabled(), err)
	}
	if err := os.WriteFile(filepath.Join(home, "config.toml"), []byte("prompt_gate = \"touch\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err = config.Load(home, "")
	if err == nil {
		t.Fatal("a misspelled policy must be an error, not a silently off gate")
	}
	// The message must name the file, as the rest of this package does: with two
	// configs, otherwise it is unclear which one to fix.
	if !strings.Contains(err.Error(), "config.toml") {
		t.Fatalf("error must name the file: %v", err)
	}
}

// TestPromptGateAutoIsOffUntilPhase4 — spec §10: auto means "block unless a proxy is
// active", a meaning that only exists once shade serve lands in phase 4. Until then
// auto must behave as off, and that contract is worth pinning.
func TestPromptGateAutoIsOffUntilPhase4(t *testing.T) {
	home := t.TempDir()
	write(t, filepath.Join(home, "config.toml"), "prompt_gate = \"auto\"\n")

	c, err := config.Load(home, "")
	if err != nil {
		t.Fatal(err)
	}
	if c.PromptGate != "auto" || c.PromptGateEnabled() {
		t.Fatalf("auto: gate=%q, enabled=%v", c.PromptGate, c.PromptGateEnabled())
	}
}

// TestPromptGateTypoInGlobalFailsDespiteProjectOverride — the value is validated per
// layer, so a typo in the global file is an error even when the project file overrides
// it with a valid one: the typo is real, and a gate that guards a leak fails loud.
func TestPromptGateTypoInGlobalFailsDespiteProjectOverride(t *testing.T) {
	home, root := t.TempDir(), t.TempDir()
	write(t, filepath.Join(home, "config.toml"), "prompt_gate = \"touch\"\n")
	write(t, filepath.Join(root, ".shade.toml"), "prompt_gate = \"off\"\n")

	if _, err := config.Load(home, root); err == nil {
		t.Fatal("a typo in the global file must fail even when the project overrides it")
	}
}

func write(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}
