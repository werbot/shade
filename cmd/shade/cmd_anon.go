package main

import (
	"context"
	"fmt"
)

func init() {
	Register(Command{
		Name: "anon",
		Help: "replace secrets with placeholders",
		Run:  runAnon,
	})
}

// jsonAnonResult is the --json output shape. The fields are lowercase: this is a contract for
// external consumers, not a serialization of the internal structures of the engine.
type jsonAnonResult struct {
	Text  string     `json:"text"`
	Spans []jsonSpan `json:"spans"`
}

type jsonSpan struct {
	Type string `json:"type"`
	Rule string `json:"rule"`
}

// runAnon anonymizes text from a file or stdin and prints the result.
// Only the anonymization result goes to stdout — the secrets in it are already replaced
// with placeholders, so the values never leave the process in any branch.
func runAnon(args []string, stdio IO) int {
	a, err := parseInputArgs(args)
	if err != nil {
		return fail(stdio, "anon", 2, err)
	}
	text, err := readInput(a, stdio.In)
	if err != nil {
		return fail(stdio, "anon", 1, err)
	}
	ctx := context.Background()
	e, err := openEngine(ctx, a.project)
	if err != nil {
		return fail(stdio, "anon", 1, err)
	}
	defer e.Close()

	res, err := e.Anonymize(ctx, text)
	if err != nil {
		return fail(stdio, "anon", 1, err)
	}
	if !a.json {
		fmt.Fprint(stdio.Out, res.Text)
		return 0
	}
	out := jsonAnonResult{Text: res.Text, Spans: make([]jsonSpan, 0, len(res.Spans))}
	for _, s := range res.Spans {
		out.Spans = append(out.Spans, jsonSpan{Type: s.Type, Rule: s.Rule})
	}
	if err := writeJSON(stdio.Out, out); err != nil {
		return fail(stdio, "anon", 1, err)
	}
	return 0
}
