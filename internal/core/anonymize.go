package core

import (
	"context"
	"strings"

	"github.com/werbot/shade/internal/placeholder"
	"github.com/werbot/shade/internal/rules"
)

// Anonymize replaces the secrets it finds with placeholders, storing the values in
// the project store. The same value always gets the same
// placeholder, so a repeated run over the text does not change the token.
func (e *Engine) Anonymize(ctx context.Context, text string) (Result, error) {
	// Guard comes first: already existing placeholders must not fall under the
	// rules and get a new number.
	masked, restore := placeholder.Guard(text)
	spans := rules.Detect(masked, e.ruleSet)
	if len(spans) == 0 {
		return Result{Text: restore(masked)}, nil
	}

	// Reassembly with a forward cursor: the spans do not overlap and go in ascending
	// Start, so a single cursor is enough and the offsets need not be
	// recomputed after every substitution.
	var b strings.Builder
	b.Grow(len(masked))
	prev := 0
	for _, span := range spans {
		ph, err := e.store.Allocate(ctx, e.project.ID, span.Type, []byte(masked[span.Start:span.End]))
		if err != nil {
			return Result{}, err
		}
		b.WriteString(masked[prev:span.Start])
		b.WriteString(ph)
		prev = span.End
	}
	b.WriteString(masked[prev:])

	// restore undoes the NUL doubling and puts the hidden placeholders back
	// in place. They do not go into Unresolved: these are input tokens for which no replacement is
	// owed to it, and not the values lost by the store.
	return Result{Text: restore(b.String()), Spans: spans}, nil
}
