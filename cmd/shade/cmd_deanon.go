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
	// A journal failure does not take the answer away: under fail_open_log the policy explicitly allows
	// to hand out an incomplete answer, and a broken record has no right to cancel it.
	// The policy and the codes below do not change — the warning goes to stderr.
	if res.RecordErr != nil {
		fmt.Fprintf(stdio.Err, "deanon: journal not recorded: %v\n", res.RecordErr)
	}

	// blocked — the answer is not handed out: partially restored text does not
	// leave. Partial output is allowed by fail_open_log, and then the policy has
	// a visible difference, not just a return code.
	blocked := len(res.Unresolved) > 0 && cfg.FailPolicy != config.FailOpenLog

	if len(res.Unresolved) > 0 {
		// Tokens, and only they: stdout goes on to the model, and stderr is read
		// by a human, and a value in it would bring the secret back into the log.
		for _, tok := range res.Unresolved {
			fmt.Fprintf(stdio.Err, "deanon: unresolved placeholder %s\n", tok.Raw)
		}
		if !blocked {
			fmt.Fprintln(stdio.Err, "deanon: fail_policy=fail_open_log — unresolved placeholders do not block the output")
		}
	}

	// The answer is blocked, not the diagnostics: under --json the document is printed
	// under any policy, while an empty text together with code 3 expresses the block. Otherwise
	// the declared form would be unreachable exactly when it is needed, and
	// a machine consumer would have to parse stderr — the very thing that
	// --json was supposed to avoid. In text mode an empty answer still stays
	// an empty stdout.
	answer := res.Text
	if blocked {
		answer = ""
	}
	if a.json {
		out := jsonDeanonResult{Text: answer, Unresolved: make([]jsonUnresolved, 0, len(res.Unresolved))}
		for _, tok := range res.Unresolved {
			out.Unresolved = append(out.Unresolved, jsonUnresolved{Type: tok.Type, Raw: tok.Raw})
		}
		if err := writeJSON(stdio.Out, out); err != nil {
			return fail(stdio, "deanon", 1, err)
		}
	} else {
		fmt.Fprint(stdio.Out, answer)
	}
	if blocked {
		return 3
	}
	return 0
}
