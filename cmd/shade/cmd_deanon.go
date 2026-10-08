package main

import (
	"context"
	"fmt"

	"github.com/werbot/shade/internal/config"
	"github.com/werbot/shade/internal/store"
)

func init() {
	Register(Command{
		Name: "deanon",
		Help: "restore the real values in place of the placeholders",
		Run:  runDeanon,
	})
}

// jsonDeanonResult is the --json output shape. Only tokens get into unresolved:
// Raw is filled by the placeholder lookup; a value can never get into it.
type jsonDeanonResult struct {
	Text       string           `json:"text"`
	Unresolved []jsonUnresolved `json:"unresolved"`
}

type jsonUnresolved struct {
	Type string `json:"type"`
	Raw  string `json:"raw"`
}

// runDeanon restores the values and returns 3 if something is left
// unresolved.
func runDeanon(args []string, stdio IO) int {
	a, err := parseInputArgs(args)
	if err != nil {
		return fail(stdio, "deanon", 2, err)
	}
	text, err := readInput(a, stdio.In)
	if err != nil {
		return fail(stdio, "deanon", 1, err)
	}
	ctx := context.Background()
	e, err := openEngine(ctx, a.project)
	if err != nil {
		return fail(stdio, "deanon", 1, err)
	}
	defer e.Close()

	cfg, err := config.Load(store.Home(), e.RootPath())
	if err != nil {
		return fail(stdio, "deanon", 1, err)
	}
	res, err := e.Restore(ctx, text)
	if err != nil {
		return fail(stdio, "deanon", 1, err)
	}

	if len(res.Unresolved) > 0 {
		// Tokens, and only they: stdout goes on to the model, and stderr is read
		// by a human, and a value in it would bring the secret back into the log.
		for _, tok := range res.Unresolved {
			fmt.Fprintf(stdio.Err, "deanon: unresolved placeholder %s\n", tok.Raw)
		}
		// fail_closed blocks the answer entirely: text that could not be
		// restored is not handed out in any form — neither with holes nor partially.
		// Whoever needs partial output picks fail_open_log: then the
		// policy has a visible difference, not just a return code.
		if cfg.FailPolicy != "fail_open_log" {
			return 3
		}
		fmt.Fprintln(stdio.Err, "deanon: fail_policy=fail_open_log — unresolved placeholders do not block the output")
	}

	if !a.json {
		fmt.Fprint(stdio.Out, res.Text)
		return 0
	}
	out := jsonDeanonResult{Text: res.Text, Unresolved: make([]jsonUnresolved, 0, len(res.Unresolved))}
	for _, tok := range res.Unresolved {
		out.Unresolved = append(out.Unresolved, jsonUnresolved{Type: tok.Type, Raw: tok.Raw})
	}
	if err := writeJSON(stdio.Out, out); err != nil {
		return fail(stdio, "deanon", 1, err)
	}
	return 0
}
