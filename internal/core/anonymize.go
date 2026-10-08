package core

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/werbot/shade/internal/placeholder"
	"github.com/werbot/shade/internal/rules"
)

// Anonymize replaces the secrets it finds with placeholders, storing the values in
// the project store. The same value always gets the same
// placeholder, so a repeated run over the text does not change the token.
func (e *Engine) Anonymize(ctx context.Context, text string) (Result, error) {
	masked, restore, spans := scanText(text, e.ruleSet)
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

// Scan finds in the text everything Anonymize would catch, but writes nothing to the
// store and issues no placeholders: this is the debug path `shade test` and
// the future scan tool in MCP. A debug run must not create entities.
//
// The text after Guard is returned — exactly the one whose bytes index the offsets
// of the spans. Without foreign placeholders this is the input as is, and with them the placeholders are
// replaced with Guard service substitutions and it is shorter than the input: returning
// it would mean handing over coordinates that do not fit it (to say
// restore(masked) — an error, the substitution and the placeholder have different lengths). A span
// does not cover the substitution: clip trims the spans to its boundaries, so
// the fragment at the offsets is always meaningful.
//
// a non-empty only narrows the run to one rule of the active set; if the name is not
// in the set is the only cause of the error.
func (e *Engine) Scan(text, only string) (string, []rules.Span, error) {
	ruleSet := e.ruleSet
	if only != "" {
		ruleSet = slices.DeleteFunc(slices.Clone(ruleSet), func(r rules.Rule) bool {
			return r.Name != only
		})
		if len(ruleSet) == 0 {
			return "", nil, fmt.Errorf("rule %q not found", only)
		}
	}
	masked, _, spans := scanText(text, ruleSet)
	return masked, spans, nil
}

// scanText is the step shared with Anonymize: hide foreign placeholders, find spans in
// the mask and clip them to its boundaries. There must be no copy of this sequence:
// a diverged Guard would break the invariant "rules do not see the substitution".
func scanText(text string, ruleSet []rules.Rule) (masked string, restore func(string) string, spans []rules.Span) {
	// Guard comes first: already existing placeholders must not fall under the
	// rules and get a new number.
	masked, hidden, restore := placeholder.Guard(text)
	return masked, restore, clip(rules.Detect(masked, ruleSet), hidden)
}

// clip cuts out of the spans the areas occupied by Guard substitutions. A substitution is
// a foreign placeholder: replacing it means losing it forever, and
// discarding the span entirely — leaving the real secret next to it out in the open
// (`password="<EMAIL_1>AbCdEf0123456789"` is caught only thanks to the mask).
// That is why the span splits into parts outside the substitution, and each part gets
// its own placeholder.
//
// hidden and spans are sorted and do not overlap within themselves, so the cursor over
// hidden moves only forward and the pass is linear.
func clip(spans []rules.Span, hidden [][2]int) []rules.Span {
	if len(hidden) == 0 {
		return spans
	}
	var out []rules.Span
	h := 0
	for _, span := range spans {
		for h < len(hidden) && hidden[h][1] <= span.Start {
			h++
		}
		start := span.Start
		for i := h; i < len(hidden) && hidden[i][0] < span.End; i++ {
			if hidden[i][0] > start {
				out = append(out, rules.Span{Start: start, End: hidden[i][0], Type: span.Type, Rule: span.Rule})
			}
			start = max(start, hidden[i][1])
		}
		if start < span.End {
			out = append(out, rules.Span{Start: start, End: span.End, Type: span.Type, Rule: span.Rule})
		}
	}
	return out
}
