// Package core — the shade core: anonymizing text before sending it to the model and
// restoring the real values in its response.
package core

import (
	"context"
	"errors"

	"github.com/werbot/shade/internal/crypt"
	"github.com/werbot/shade/internal/placeholder"
	"github.com/werbot/shade/internal/rules"
	"github.com/werbot/shade/internal/store"
)

// Engine — the anonymization core: the store, the project within whose boundaries
// the placeholders, and the active rule set of this project.
type Engine struct {
	store   *store.Store
	project store.Project
	ruleSet []rules.Rule
	// adapter — the source of the call (cli | hook | mcp | proxy). Until Task 14 the field is not
	// read: it is Task 14 that appends a record to audit on an unresolved
	// placeholder. This is a planned sequence, not a stub for the future.
	adapter string
}

// Result — the outcome of an anonymization or a restore.
type Result struct {
	Text string
	// Unresolved — placeholders whose values are not in the project store.
	// Only Restore fills it.
	Unresolved []placeholder.Token
	// Spans — the fragments found. The boundaries are byte offsets in the text after
	// Guard, that is, in the one that went to Detect, not in the original: if on
	// the input had placeholders of its own, their length in these coordinates is different.
	// A span that covered a Guard substitution is already clipped to its boundaries here, and
	// hence one rule can give several adjacent spans.
	Spans []rules.Span
}

// New assembles an engine for the project that owns the directory dir.
//
// dir comes from outside rather than from os.Getwd inside: the project directory is
// a property of the adapter. The CLI passes --project or the current directory here, the hook in
// phase 2 — the cwd from the payload, which does not match the environment of the hook process.
func New(ctx context.Context, home, dir, adapter string) (*Engine, error) {
	// The directory must exist before the key is loaded: crypt.LoadOrCreateKey does not
	// create it, and on a clean machine the first run would fail with ENOENT.
	if err := store.EnsureHome(home); err != nil {
		return nil, err
	}
	key, err := crypt.LoadOrCreateKey(home)
	if err != nil {
		return nil, err
	}
	s, err := store.Open(home, key)
	if err != nil {
		return nil, err
	}
	e, err := build(ctx, s, dir, adapter)
	if err != nil {
		// Otherwise the database connection would be left hanging: New returned an error, but
		// the caller has no way to close the store. The close error is not lost —
		// is joined with the original one, which names the real cause.
		return nil, errors.Join(err, s.Close())
	}
	return e, nil
}

// Close releases the engine's store.
func (e *Engine) Close() error { return e.store.Close() }

// RootPath returns the engine's project root. The layers above use it to find
// the project config: .shade.toml lies in the repository root, and not in the directory
// from which the command was started.
func (e *Engine) RootPath() string { return e.project.RootPath }

// RuleCount returns the number of rules active in the engine's project.
func (e *Engine) RuleCount() int { return len(e.ruleSet) }

// Store hands out the engine's store: the `shade rules` commands edit rules directly.
// A second store assembler in the CLI would duplicate EnsureHome → key → SeedBuiltin,
// that is exactly the path on which `rules list` on a clean machine must be
// seeded.
func (e *Engine) Store() *store.Store { return e.store }

// ProjectID — the project within whose boundaries the engine issues placeholders. It is
// the default scope for `shade rules` without --global; resolving it in the CLI
// again would mean repeating ProjectForPath in a second way.
func (e *Engine) ProjectID() int64 { return e.project.ID }

// build finishes the engine on top of an open store: it resolves the project of the
// directory dir, seeds the builtin rule set and reads the active set.
//
// The rules are read from the database, not from memory: the user edits them through
// `shade rules`, and the engine must see the edit.
func build(ctx context.Context, s *store.Store, dir, adapter string) (*Engine, error) {
	project, err := s.ProjectForPath(ctx, dir)
	if err != nil {
		return nil, err
	}
	if err := s.SeedBuiltin(ctx); err != nil {
		return nil, err
	}
	ruleSet, err := s.RulesForProject(ctx, project.ID)
	if err != nil {
		return nil, err
	}
	return &Engine{store: s, project: project, ruleSet: ruleSet, adapter: adapter}, nil
}
