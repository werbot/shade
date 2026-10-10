package hook

import (
	"context"
	"errors"

	"github.com/werbot/shade/internal/core"
	"github.com/werbot/shade/internal/rules"
)

// Engine is the part of the core the hook needs. It is an interface rather than
// *core.Engine so the event handlers can be tested without a database, and so the
// package keeps its one-way dependency: hook knows core, core knows nothing of hook.
type Engine interface {
	Anonymize(ctx context.Context, text string) (core.Result, error)
	Restore(ctx context.Context, text string) (core.Result, error)
	Scan(text, only string) (string, []rules.Span, error)
	RootPath() string
	RecordBlocked(ctx context.Context, typ string) error
	ProxyCovers(ctx context.Context) (bool, error)
	Close() error
}

// Opener brings up an engine for the project that owns dir. The hook does not open the
// store itself: which home, which key and which adapter to use is the caller's
// business, and this is the seam the package is built around.
type Opener func(ctx context.Context, dir string) (Engine, error)

// Handler answers Claude Code hook events. Home is the shade state directory: the
// config is read from Home and from the project root the engine resolves.
type Handler struct {
	Home string
	Open Opener
}

// Handle answers one event. A non-nil error means the handler cannot work at all —
// Open is nil, which is a programming mistake and shows up in the tests. Every runtime
// failure is fail-open instead: a hook that failed would break the session, so an
// unopenable engine, an unreadable config or a refused store come back as a
// systemMessage, with no hookSpecificOutput and a nil error, and the process exits 0.
func (h Handler) Handle(ctx context.Context, ev Event) (Response, error) {
	if h.Open == nil {
		return Response{}, errors.New("hook handler: Open is nil")
	}
	handle := h.handlerFor(ev.Name)
	if handle == nil {
		// An unknown event is not an error: Claude Code grows new events, and a hook
		// that failed on one of them would break the session.
		return Response{}, nil
	}
	e, err := h.Open(ctx, ev.CWD)
	if err != nil {
		return failOpen(err), nil
	}
	defer e.Close()
	res, err := handle(ctx, e, ev)
	if err != nil {
		return failOpen(err), nil
	}
	return res, nil
}

// handlerFor maps an event to its handler. A nil result means there is no such event.
func (h Handler) handlerFor(name string) func(context.Context, Engine, Event) (Response, error) {
	switch name {
	case EventSessionStart:
		return h.sessionStart
	case EventUserPromptSubmit:
		return h.userPromptSubmit
	case EventPreToolUse:
		return h.preToolUse
	case EventPostToolUse:
		return h.postToolUse
	case EventMessageDisplay:
		return h.messageDisplay
	default:
		return nil
	}
}

// failOpen turns a runtime failure into the warning the hook prints. It is the only
// thing the response carries: no hookSpecificOutput, so Claude Code keeps its default
// behaviour, and no error, so the caller prints the message once and exits 0.
func failOpen(err error) Response {
	return Response{SystemMessage: "shade: " + err.Error()}
}

// noteRecordErr adds the engine's auxiliary-write failure to the warning. The answer
// itself is already decided: a lost statistics row or a lost trace of an unresolved
// token must be visible — otherwise it is visible nowhere — but it must not take the
// answer away (see core.Result.RecordErr).
func noteRecordErr(res Response, err error) Response {
	if err == nil {
		return res
	}
	note := "shade: journal write failed: " + err.Error()
	if res.SystemMessage == "" {
		res.SystemMessage = note
	} else {
		res.SystemMessage += "; " + note
	}
	return res
}
