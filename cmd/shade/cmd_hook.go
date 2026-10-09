package main

import (
	"context"
	"fmt"
	"io"

	"github.com/werbot/shade/internal/core"
	"github.com/werbot/shade/internal/hook"
	"github.com/werbot/shade/internal/store"
)

func init() {
	Register(Command{
		Name: "hook",
		Help: "answer a claude code hook event from stdin",
		Run:  runHook,
	})
}

// runHook answers one Claude Code hook event. It always returns 0: a non-zero exit at
// Claude Code means "block" or "message the model", and a failed hook must do neither.
// An unreadable payload is silence with an empty stdout, and a handler failure is
// fail-open already — it comes back as a response carrying a systemMessage.
func runHook(args []string, stdio IO) int {
	if err := noExtraArgs(args); err != nil {
		return fail(stdio, "hook", 2, err)
	}
	raw, err := io.ReadAll(stdio.In)
	if err != nil {
		// fail-open: every runtime failure of this command reports to stderr and still
		// returns 0. A non-zero exit at Claude Code means "block" or "message the
		// model", so a diagnostic must not be turned into a session block.
		return fail(stdio, "hook", 0, fmt.Errorf("reading stdin: %w", err))
	}
	ev, err := hook.ParseEvent(raw)
	if err != nil {
		// The payload is written by Claude Code: an event we cannot read is silence,
		// not an error, and a diagnostic here would reach the session as a message.
		return 0
	}
	home := store.Home()
	h := hook.Handler{
		Home: home,
		// dir is the cwd from the payload, not the working directory of the hook
		// process: Claude Code starts the hook anywhere, and the payload is the only
		// truth about the project.
		Open: func(ctx context.Context, dir string) (hook.Engine, error) {
			return core.New(ctx, home, dir, "hook")
		},
	}
	res, err := h.Handle(context.Background(), ev)
	if err != nil {
		return fail(stdio, "hook", 0, err)
	}
	out, err := res.Bytes()
	if err != nil {
		return fail(stdio, "hook", 0, err)
	}
	if _, err := stdio.Out.Write(out); err != nil {
		return fail(stdio, "hook", 0, err)
	}
	return 0
}
