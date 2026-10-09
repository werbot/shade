package hook_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/werbot/shade/internal/core"
	"github.com/werbot/shade/internal/directive"
	"github.com/werbot/shade/internal/hook"
)

func TestHandlerFailsOpenOnEngineError(t *testing.T) {
	e := &fakeEngine{restoreErr: errors.New("store is gone")}
	h := newFakeHandler(t, e)
	res, err := h.Handle(ctx, hook.Event{
		Name: hook.EventPreToolUse, CWD: "/repo", ToolName: "Bash",
		ToolInput: json.RawMessage(`{"command":"<HOST_1>"}`),
	})
	if err != nil {
		t.Fatalf("a runtime failure must not come back as an error: %v", err)
	}
	if res.HookSpecificOutput != nil {
		t.Fatalf("a failed hook keeps Claude Code's default behaviour: %+v", res)
	}
	if !strings.Contains(res.SystemMessage, "shade:") || !strings.Contains(res.SystemMessage, "store is gone") {
		t.Fatalf("the failure must be visible: %+v", res)
	}
	if !e.closed {
		t.Fatal("the engine must be closed on the fail-open path too")
	}

	// The engine cannot be opened at all — the same answer, not an error.
	h = hook.Handler{
		Home: t.TempDir(),
		Open: func(context.Context, string) (hook.Engine, error) { return nil, errors.New("cannot open") },
	}
	res, err = h.Handle(ctx, hook.Event{Name: hook.EventSessionStart, CWD: "/repo"})
	if err != nil {
		t.Fatalf("a failed open must not come back as an error: %v", err)
	}
	if res.HookSpecificOutput != nil || !strings.Contains(res.SystemMessage, "cannot open") {
		t.Fatalf("got %+v", res)
	}
}

func TestSessionStartReturnsDirective(t *testing.T) {
	home, repo := t.TempDir(), gitDir(t)
	h := newHandler(t, home)
	res, err := h.Handle(ctx, hook.Event{Name: hook.EventSessionStart, CWD: repo})
	if err != nil {
		t.Fatal(err)
	}
	if res.HookSpecificOutput == nil || res.HookSpecificOutput.AdditionalContext != directive.Text() {
		t.Fatalf("got %+v", res)
	}
	// Opening the engine is what registers the project: without it the rest of the
	// session would issue placeholders for a project that is not in the database.
	st := openStore(t, home)
	projectIDIn(t, st, repo)
}

// A frame cut inside an open token is left alone: restoring it would show a value
// assembled from half a token and strand the closing bracket in the next frame.
func TestMessageDisplayLeavesATruncatedTokenAlone(t *testing.T) {
	e := &fakeEngine{restoreFn: func(s string) (core.Result, error) {
		// The real finder restores the canonical form, the one truncated at
		// max_tokens (no closing bracket) and the HTML-escaped one.
		s = strings.ReplaceAll(s, "&lt;HOST_1&gt;", "db.prod.local")
		s = strings.ReplaceAll(s, "<HOST_1>", "db.prod.local")
		return core.Result{Text: strings.ReplaceAll(s, "<HOST_1", "db.prod.local")}, nil
	}}
	h := newFakeHandler(t, e)

	res, err := h.Handle(ctx, hook.Event{Name: hook.EventMessageDisplay, Delta: "<HOST_1"})
	if err != nil {
		t.Fatal(err)
	}
	if res.HookSpecificOutput != nil {
		t.Fatalf("a frame cut inside a token must not be restored: %+v", res)
	}

	// The final frame carries the rest of the message, and a truncated token in it is
	// exactly what the tolerant finder is built to restore.
	res, err = h.Handle(ctx, hook.Event{Name: hook.EventMessageDisplay, Delta: "<HOST_1", Final: true})
	if err != nil {
		t.Fatal(err)
	}
	if res.HookSpecificOutput == nil || res.HookSpecificOutput.DisplayContent != "db.prod.local" {
		t.Fatalf("the final frame must be restored: %+v", res)
	}

	// A closed token is restored in any frame, and a frame without tokens stays silent.
	res, err = h.Handle(ctx, hook.Event{Name: hook.EventMessageDisplay, Delta: "host is <HOST_1>\n"})
	if err != nil {
		t.Fatal(err)
	}
	if res.HookSpecificOutput == nil || res.HookSpecificOutput.DisplayContent != "host is db.prod.local\n" {
		t.Fatalf("got %+v", res)
	}
	// An HTML-escaped closed token at the very end of a non-final frame is closed, not
	// cut: the tolerant finder matches the escaped form whole, so it must be restored.
	res, err = h.Handle(ctx, hook.Event{Name: hook.EventMessageDisplay, Delta: "x &lt;HOST_1&gt;"})
	if err != nil {
		t.Fatal(err)
	}
	if res.HookSpecificOutput == nil || res.HookSpecificOutput.DisplayContent != "x db.prod.local" {
		t.Fatalf("the escaped closed token was left on screen: %+v", res)
	}

	res, err = h.Handle(ctx, hook.Event{Name: hook.EventMessageDisplay, Delta: "no tokens here\n"})
	if err != nil {
		t.Fatal(err)
	}
	if res.HookSpecificOutput != nil {
		t.Fatalf("nothing to show must stay silent: %+v", res)
	}
}

// An event the adapter does not know is not an error: Claude Code grows new events,
// and a hook that failed on one of them would break the session.
func TestUnknownEventIsSilent(t *testing.T) {
	h := newFakeHandler(t, &fakeEngine{})
	res, err := h.Handle(ctx, hook.Event{Name: "PreCompact", CWD: "/repo"})
	if err != nil {
		t.Fatal(err)
	}
	if res.HookSpecificOutput != nil || res.Decision != "" || res.SystemMessage != "" {
		t.Fatalf("got %+v", res)
	}
}

// A misconfigured handler is the one failure that is an error rather than a warning:
// there is no way to answer any event without an opener.
func TestHandlerWithoutOpenerIsAnError(t *testing.T) {
	if _, err := (hook.Handler{}).Handle(ctx, hook.Event{Name: hook.EventSessionStart}); err == nil {
		t.Fatal("a handler without an opener must fail loudly")
	}
}
