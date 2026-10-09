package settings_test

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/werbot/shade/internal/settings"
)

// shadeBin is the absolute path the tests install into the hooks. It is not the
// binary under test: the point of the fixtures is the shape of the files.
const shadeBin = "/usr/local/bin/shade"

func TestPluginFilesMatchFixtures(t *testing.T) {
	got := settings.PluginFiles(shadeBin)
	for _, name := range []string{".claude-plugin/marketplace.json", ".claude-plugin/plugin.json", "hooks/hooks.json"} {
		want, err := os.ReadFile(filepath.Join("testdata", filepath.Base(name)))
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(got[name], want) {
			t.Fatalf("%s:\n got %s\nwant %s", name, got[name], want)
		}
	}
}

// The marketplace manifest must carry an owner object: Claude Code 2.1.295 rejects
// the whole file without it, and rejects it silently — init reports success while the
// plugin never loads. The fixture pins the bytes; this names the field, so a fixture
// edited in step with the code cannot drop it unnoticed.
func TestMarketplaceDeclaresItsOwner(t *testing.T) {
	files := settings.PluginFiles(shadeBin)
	var doc struct {
		Owner struct {
			Name string `json:"name"`
		} `json:"owner"`
	}
	if err := json.Unmarshal(files[".claude-plugin/marketplace.json"], &doc); err != nil {
		t.Fatal(err)
	}
	if doc.Owner.Name == "" {
		t.Fatalf("the marketplace has no owner: %s", files[".claude-plugin/marketplace.json"])
	}
}

func TestPluginHooksCallTheBinaryWithoutAShell(t *testing.T) {
	files := settings.PluginFiles(shadeBin)
	var doc struct {
		Hooks map[string][]struct {
			Matcher string `json:"matcher"`
			Hooks   []struct {
				Command string   `json:"command"`
				Args    []string `json:"args"`
			} `json:"hooks"`
		} `json:"hooks"`
	}
	if err := json.Unmarshal(files["hooks/hooks.json"], &doc); err != nil {
		t.Fatal(err)
	}
	for _, event := range []string{"SessionStart", "UserPromptSubmit", "PreToolUse", "PostToolUse", "MessageDisplay"} {
		groups := doc.Hooks[event]
		if len(groups) != 1 || len(groups[0].Hooks) != 1 {
			t.Fatalf("%s: %+v", event, groups)
		}
		h := groups[0].Hooks[0]
		if h.Command != shadeBin || len(h.Args) != 1 || h.Args[0] != "hook" {
			t.Fatalf("%s must call the binary by absolute path with exec form: %+v", event, h)
		}
	}
}
