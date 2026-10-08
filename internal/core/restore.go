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
// The error means the response could not be assembled (Resolve refused not because
// the value is missing). A failure to write the trace of an unresolved token is not an error: the trace
// must be visible, but not fatal, because under fail_open_log the policy
// explicitly allows handing over an incomplete response, and a broken journal has no right to
// take the response away. The failure travels to Result.RecordErr.
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

	// The trace of an unresolved token is not lost silently, but it does not take the response away either:
	// the errors of all writes are joined into RecordErr, and the returned error
	// stays for the single case where the text could not be assembled.
	for _, tok := range unresolved {
		res.RecordErr = errors.Join(res.RecordErr,
			e.store.RecordUnresolved(ctx, e.project.ID, e.adapter, tok.Type, tok.Raw))
	}
	return res, nil
}
