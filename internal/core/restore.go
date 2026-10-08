package core

import (
	"context"
	"errors"
	"slices"

	"github.com/werbot/shade/internal/placeholder"
	"github.com/werbot/shade/internal/store"
)

// Restore substitutes the real values in place of the placeholders. A token that
// is not in the project store, stays in the text as is and goes into Unresolved:
// discarding it would mean silently handing over text with a hole where the value was.
//
// The error of writing the trace of an unresolved token is returned together with the assembled
// the result: the response is ready, but the caller must learn about the lost trace. In
// In Anonymize the decision is the opposite — there the auxiliary write is not about security.
func (e *Engine) Restore(ctx context.Context, text string) (Result, error) {
	toks := placeholder.FindNormalized(text)
	// The substitution goes right to left: a replacement changes the length of the fragment, and after
	// the first edit the offsets of the remaining tokens would have shifted. The boundaries are taken from
	// the original text, and to the right of the current token there is nothing left to edit.
	out := text
	var unresolved []placeholder.Token
	for i := len(toks) - 1; i >= 0; i-- {
		tok := toks[i]
		value, err := e.store.Resolve(ctx, e.project.ID, tok.Type, tok.N)
		if errors.Is(err, store.ErrNoEntity) {
			unresolved = append(unresolved, tok)
			continue
		}
		if err != nil {
			return Result{}, err
		}
		out = out[:tok.Start] + string(value) + out[tok.End:]
	}
	// The collection went from the end of the text — reverse it so the order matches the text.
	slices.Reverse(unresolved)
	res := Result{Text: out, Unresolved: unresolved}

	// The trace of an unresolved token is not muted: a lost record is what
	// the caller must learn. The result does not change — the response is already
	// assembled, and the journal is kept for the trace, not for it.
	var errs []error
	for _, tok := range unresolved {
		if err := e.store.RecordUnresolved(ctx, e.project.ID, e.adapter, tok.Type, tok.Raw); err != nil {
			errs = append(errs, err)
		}
	}
	return res, errors.Join(errs...)
}
