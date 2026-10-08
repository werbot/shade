// Package core — the shade core: anonymizing text before sending it to the model and
// restoring the real values in its response.
package core

import (
	"context"
	"fmt"
	"os"

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
	Spans []rules.Span
}

// New assembles an engine for the project that owns the current directory.
func New(ctx context.Context, home, adapter string) (*Engine, error) {
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
	e, err := build(ctx, s, adapter)
	if err != nil {
		// Otherwise the database connection would be left hanging: New returned an error, but
		// and the caller has no way to close the store.
		s.Close()
		return nil, err
	}
	return e, nil
}

// Close releases the engine's store.
func (e *Engine) Close() error { return e.store.Close() }

// build finishes the engine on top of an open store: it resolves the project of the
// the current directory, seeds the builtin rule set and reads the active set.
//
// The rules are read from the database, not from memory: the user edits them through
// `shade rules`, and the engine must see the edit.
func build(ctx context.Context, s *store.Store, adapter string) (*Engine, error) {
	wd, err := os.Getwd()
	if err != nil {
		return nil, fmt.Errorf("working directory: %w", err)
	}
	project, err := s.ProjectForPath(ctx, wd)
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
