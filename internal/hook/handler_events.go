package hook

import (
	"context"
	"encoding/json"
	"errors"
	"maps"
	"slices"
	"strings"

	"github.com/werbot/shade/internal/config"
	"github.com/werbot/shade/internal/core"
	"github.com/werbot/shade/internal/directive"
	"github.com/werbot/shade/internal/jsonwalk"
	"github.com/werbot/shade/internal/placeholder"
	"github.com/werbot/shade/internal/rules"
)

// skipTools are the tools whose arguments the hook never restores. The list governs
// the restore side only — PreToolUse — where skipping means "leave this tool's
// arguments as the model wrote them". Its five entries carry queries or prose, so
// restoring in them is a no-op or harmless. Write is deliberately not on it: Write's
// whole argument is the content to be written, so skipping would leave a file holding
// the literal token the model was told to write.
var skipTools = map[string]bool{
	"WebFetch":        true,
	"WebSearch":       true,
	"ToolSearch":      true,
	"ExitPlanMode":    true,
	"AskUserQuestion": true,
}

// sessionStart registers the project — opening the engine is what creates it — and
// hands the model the one text that tells it how to treat the tokens.
func (h Handler) sessionStart(_ context.Context, _ Engine, _ Event) (Response, error) {
	return Response{HookSpecificOutput: &Specific{
		HookEventName:     EventSessionStart,
		AdditionalContext: directive.Text(),
	}}, nil
}

// userPromptSubmit is the gate. A hook cannot rewrite the prompt, so the only choices
// are silence and a block; the answer names the types of what was found, never the
// values, and the journal gets one row per type.
func (h Handler) userPromptSubmit(ctx context.Context, e Engine, ev Event) (Response, error) {
	cfg, err := config.Load(h.Home, e.RootPath())
	if err != nil {
		return Response{}, err
	}
	// True until shade serve can report whether it wraps this traffic: auto must stay off
	// (today's behaviour) rather than block every session before the proxy check exists.
	// The serve task replaces this with the real answer.
	if !cfg.PromptGateEnabled(true) {
		return Response{}, nil
	}
	// Scan, not Anonymize: the gate is a diagnostic, and a blocked prompt must not
	// leave entities in the store behind it.
	_, spans, err := e.Scan(ev.Prompt, "")
	if err != nil {
		return Response{}, err
	}
	if len(spans) == 0 {
		return Response{}, nil
	}
	types := distinctTypes(spans)
	var recordErr error
	for _, typ := range types {
		// One row per distinct type, not per span: three hosts in one prompt are one
		// HOST row.
		recordErr = errors.Join(recordErr, e.RecordBlocked(ctx, typ))
	}
	// The block stands even when the journal write failed: letting a prompt with
	// secrets through because a row could not be written would be the wrong trade.
	return noteRecordErr(Response{
		Decision: "block",
		Reason:   "shade: the prompt contains sensitive data: " + strings.Join(types, ", "),
	}, recordErr), nil
}

// preToolUse restores the real values in the tool arguments: the model writes the
// arguments and may have broken a token, while the tool needs the real value to run.
func (h Handler) preToolUse(ctx context.Context, e Engine, ev Event) (Response, error) {
	if skipTools[ev.ToolName] {
		return Response{}, nil
	}
	cfg, err := config.Load(h.Home, e.RootPath())
	if err != nil {
		return Response{}, err
	}
	r, err := rewriteJSON(ev.ToolInput, func(s string) (core.Result, error) {
		return e.Restore(ctx, s)
	})
	if err != nil {
		return Response{}, err
	}
	if len(r.Unresolved) > 0 {
		tokens := tokenList(r.Unresolved)
		if cfg.FailPolicy == config.FailOpenLog {
			res := Response{SystemMessage: "shade: unresolved placeholders: " + tokens}
			if r.Changed {
				res.HookSpecificOutput = &Specific{HookEventName: EventPreToolUse, UpdatedInput: r.Out}
			}
			return noteRecordErr(res, r.RecordErr), nil
		}
		// fail_closed: running `ssh <HOST_1>` is not an option, and passing the call
		// through silently would run exactly that.
		return noteRecordErr(Response{HookSpecificOutput: &Specific{
			HookEventName:            EventPreToolUse,
			PermissionDecision:       "deny",
			PermissionDecisionReason: "shade: unresolved placeholders: " + tokens,
		}}, r.RecordErr), nil
	}
	if !r.Changed {
		// Nothing was restored — nothing is printed: an unchanged object would be a
		// rewritten object competing with the neighbouring rewrites.
		return noteRecordErr(Response{}, r.RecordErr), nil
	}
	return noteRecordErr(Response{HookSpecificOutput: &Specific{
		HookEventName: EventPreToolUse,
		UpdatedInput:  r.Out,
	}}, r.RecordErr), nil
}

// postToolUse anonymizes what the tool returned. The response is written by the
// outside world: there is nothing to trust and nothing to restore in it, only values
// to replace before the model sees them.
func (h Handler) postToolUse(ctx context.Context, e Engine, ev Event) (Response, error) {
	r, err := rewriteJSON(ev.ToolResponse, func(s string) (core.Result, error) {
		return e.Anonymize(ctx, s)
	})
	if err != nil {
		return Response{}, err
	}
	if !r.Changed {
		return noteRecordErr(Response{}, r.RecordErr), nil
	}
	return noteRecordErr(Response{HookSpecificOutput: &Specific{
		HookEventName:     EventPostToolUse,
		UpdatedToolOutput: r.Out,
	}}, r.RecordErr), nil
}

// messageDisplay puts the real values back on the screen. The event changes the screen
// only: the transcript and the model keep the tokens.
func (h Handler) messageDisplay(ctx context.Context, e Engine, ev Event) (Response, error) {
	// A frame cut inside an open token is left alone: restoring it would show a value
	// assembled from half a token, and the closing bracket would be stranded in the
	// next frame. Only the final frame restores a truncated token — the tolerant
	// finder exists for exactly that shape.
	if !ev.Final && endsInsideToken(ev.Delta) {
		return Response{}, nil
	}
	res, err := e.Restore(ctx, ev.Delta)
	if err != nil {
		return Response{}, err
	}
	if res.Text == ev.Delta {
		// Nothing was restored: silence shows the frame as it is, and an unresolved
		// token is no reason to block anything here — the display reaches neither the
		// model nor the transcript.
		return noteRecordErr(Response{}, res.RecordErr), nil
	}
	return noteRecordErr(Response{HookSpecificOutput: &Specific{
		HookEventName:  EventMessageDisplay,
		DisplayContent: res.Text,
	}}, res.RecordErr), nil
}

// rewritten is the outcome of walking one tool payload: the document with its strings
// replaced, whether anything changed at all, the tokens left without a value, and the
// auxiliary-write failures the engine reported.
type rewritten struct {
	Out        json.RawMessage
	Changed    bool
	Unresolved []placeholder.Token
	RecordErr  error
}

// rewriteJSON walks every string of the payload through f and returns the document
// with the strings replaced. A failure of the engine aborts the walk: handing over a
// half-rewritten argument would be worse than not rewriting it at all. A payload
// without the field is valid JSON of the wrong shape, and the spec asks for a silent
// no-op there rather than for a warning.
func rewriteJSON(raw json.RawMessage, f func(string) (core.Result, error)) (rewritten, error) {
	var out rewritten
	if len(raw) == 0 {
		return out, nil
	}
	var failed error
	doc, changed, err := jsonwalk.RewriteJSON(raw, func(s string) (string, bool) {
		if failed != nil {
			return s, false
		}
		res, err := f(s)
		if err != nil {
			failed = err
			return s, false
		}
		out.Unresolved = append(out.Unresolved, res.Unresolved...)
		out.RecordErr = errors.Join(out.RecordErr, res.RecordErr)
		return res.Text, res.Text != s
	})
	if err != nil {
		return rewritten{}, err
	}
	if failed != nil {
		return rewritten{}, failed
	}
	out.Out, out.Changed = doc, changed
	return out, nil
}

// distinctTypes returns the sorted distinct types of the spans: the gate names types,
// not rules and not values.
func distinctTypes(spans []rules.Span) []string {
	seen := make(map[string]bool, len(spans))
	for _, s := range spans {
		seen[s.Type] = true
	}
	return slices.Sorted(maps.Keys(seen))
}

// tokenList joins the raw texts of the tokens: the answer names the placeholders that
// have no value, never the values themselves.
func tokenList(toks []placeholder.Token) string {
	raw := make([]string, 0, len(toks))
	for _, t := range toks {
		raw = append(raw, t.Raw)
	}
	return strings.Join(raw, ", ")
}

// endsInsideToken reports that the frame stops inside a placeholder: the last token
// found by the tolerant finder reaches the end of the frame — trailing whitespace is
// the frame boundary, not text — and carries no closing bracket.
func endsInsideToken(delta string) bool {
	toks := placeholder.FindNormalized(delta)
	if len(toks) == 0 {
		return false
	}
	last := toks[len(toks)-1]
	// The tolerant finder also matches the HTML-escaped form, where the closing
	// bracket is spelled &gt; and the raw text is not just the token.
	if strings.HasSuffix(last.Raw, ">") || strings.HasSuffix(last.Raw, "＞") || strings.HasSuffix(last.Raw, "&gt;") {
		return false
	}
	return last.End == len(strings.TrimRight(delta, " \t\n\f\r"))
}
