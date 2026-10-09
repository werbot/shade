// Package skills holds the two Claude Code skills shade installs: the placeholder
// contract for the model and the guide to writing rules.
package skills

import (
	"embed"
	"io/fs"
)

//go:embed shade shade-rules
var dirs embed.FS

// Files returns the skill files keyed by their path inside the Claude Code plugin
// directory. The plugin layout is a convention of Claude Code — plugin.json declares
// nothing about skills — and it is the same directory shade init already fills with
// the hooks, so an installed skill and an installed hook cannot go out of step.
func Files() map[string][]byte {
	files := make(map[string][]byte, 2)
	err := fs.WalkDir(dirs, ".", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		body, err := dirs.ReadFile(path)
		if err != nil {
			return err
		}
		files["skills/"+path] = body
		return nil
	})
	if err != nil {
		// The tree is compiled into the binary, so a walk failure is a bug in shade
		// rather than a condition the caller could handle.
		panic(err)
	}
	return files
}
