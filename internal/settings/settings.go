// Package settings reads and edits the Claude Code settings file and generates the
// plugin files that shade installs. Claude Code reads the hooks of a plugin and the
// hooks of settings.json in two different formats: the plugin file wraps them in a
// "hooks" object (see plugin.go), the settings file does not.
package settings

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

// PluginName and MarketplaceName are the names shade registers with Claude Code.
// Both are "shade": the plugin is published by the marketplace of the same name.
const (
	PluginName      = "shade"
	MarketplaceName = "shade"
)

// File is a Claude Code settings file: a JSON object with arbitrary keys. It is a
// map and not a struct because shade edits only a few of the keys and must leave
// the rest of the user's file exactly as it was.
type File map[string]any

// Load reads the settings file at path. A missing file gives an empty File, not an
// error: init runs on a fresh machine as often as on a configured one, and "no
// settings yet" is not a failure to report.
func Load(path string) (File, error) {
	b, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return File{}, nil
	}
	if err != nil {
		return nil, err
	}
	f := File{}
	if err := json.Unmarshal(b, &f); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return f, nil
}

// Bytes renders the file the way shade writes it: two-space indent, keys sorted
// (encoding/json sorts map keys) and a trailing newline. The result is
// deterministic, so a repeated merge produces the byte-identical file.
func (f File) Bytes() ([]byte, error) {
	b, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(b, '\n'), nil
}

// Save writes the file to path, creating the parent directory. The settings file
// of a project lives in .claude/, which the repository may not have yet.
func (f File) Save(path string) error {
	b, err := f.Bytes()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, b, 0o644)
}

// EnablePlugin sets the enabledPlugins entry id, which Claude Code spells
// plugin-id@marketplace-id, to on. Every other key, including foreign plugins, is
// left alone.
func (f File) EnablePlugin(id string, on bool) {
	f.object("enabledPlugins")[id] = on
}

// AddMarketplace declares the marketplace name at the directory dir in
// extraKnownMarketplaces. Claude Code reads the declaration only from the user
// settings, never from the project ones.
func (f File) AddMarketplace(name, dir string) {
	f.object("extraKnownMarketplaces")[name] = map[string]any{
		"source": map[string]any{
			"source": "directory",
			"path":   dir,
		},
	}
}

// RemoveHooks drops every hook of the event whose command matches and returns how
// many it removed. A group left without hooks and an event left without groups are
// dropped too: Claude Code reads an empty array as "configured", while the user
// sees a hook that does nothing.
//
// match is a predicate and not a string because the user's path to the same hook
// may be spelled differently: ~/… against an absolute path.
func (f File) RemoveHooks(event string, match func(command string) bool) int {
	hooks, ok := f["hooks"].(map[string]any)
	if !ok {
		return 0
	}
	groups, ok := hooks[event].([]any)
	if !ok {
		return 0
	}

	removed := 0
	kept := make([]any, 0, len(groups))
	for _, g := range groups {
		group, ok := g.(map[string]any)
		if !ok {
			kept = append(kept, g)
			continue
		}
		entries, ok := group["hooks"].([]any)
		if !ok {
			kept = append(kept, g)
			continue
		}
		keptEntries := make([]any, 0, len(entries))
		for _, e := range entries {
			entry, ok := e.(map[string]any)
			command, _ := entry["command"].(string)
			if ok && match(command) {
				removed++
				continue
			}
			keptEntries = append(keptEntries, e)
		}
		if len(keptEntries) == 0 {
			continue
		}
		group["hooks"] = keptEntries
		kept = append(kept, group)
	}
	if removed == 0 {
		return 0
	}
	if len(kept) == 0 {
		delete(hooks, event)
		return removed
	}
	hooks[event] = kept
	return removed
}

// object returns the JSON object stored under key, creating it when the key is
// missing or holds a value of another type. A malformed value of a foreign key is
// replaced rather than merged: shade cannot guess what the user meant by it.
func (f File) object(key string) map[string]any {
	if m, ok := f[key].(map[string]any); ok {
		return m
	}
	m := map[string]any{}
	f[key] = m
	return m
}
