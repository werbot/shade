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

// runDeanon restores the values and returns 3 if something is left
// unresolved: silently handing out text with a hole where a value was is not allowed.
func runDeanon(args []string, stdio IO) int {
	a, err := parseInputArgs(args, false)
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
	fmt.Fprint(stdio.Out, res.Text)
	if len(res.Unresolved) == 0 {
		return 0
	}
	// stderr gets only the tokens, without values: stdout goes on to the model, and
	// a human reads stderr, and a value in it would bring the secret back into the log.
	for _, tok := range res.Unresolved {
		fmt.Fprintf(stdio.Err, "deanon: unresolved placeholder %s\n", tok.Raw)
	}
	if cfg.FailPolicy == "fail_open_log" {
		fmt.Fprintln(stdio.Err, "deanon: fail_policy=fail_open_log — unresolved placeholders do not block the output")
		return 0
	}
	return 3
}
