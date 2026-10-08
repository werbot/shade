package main

import (
	"context"
	"fmt"
	"io"
	"os"

	"github.com/werbot/shade/internal/rules"
)

func init() {
	Register(Command{
		Name: "test",
		Help: "run the active rule set against a sample",
		Run:  runSelfTest,
	})
}

// runSelfTest is a debug run: it shows what the anonymizer catches and where.
// The run follows the same path as Anonymize, but without issuing placeholders: the command
// check must not create entities in the store.
func runSelfTest(args []string, stdio IO) int {
	var only, sample string
	pos, err := parseFlags(args,
		map[string]*string{"--rules": &only, "--sample": &sample}, nil)
	if err != nil {
		return fail(stdio, "test", 2, err)
	}
	if err := sampleArgs(pos, sample); err != nil {
		return fail(stdio, "test", 2, err)
	}
	text, err := readSample(pos, sample, stdio.In)
	if err != nil {
		return fail(stdio, "test", 1, err)
	}

	ctx := context.Background()
	e, err := openEngine(ctx, "")
	if err != nil {
		return fail(stdio, "test", 1, err)
	}
	defer e.Close()

	clean, spans, err := e.Scan(text, only)
	if err != nil {
		// The only cause of a Scan error is a name missing from the active set,
		// that is, a typo in the argument, not an environment failure.
		return fail(stdio, "test", 2, err)
	}
	printSpans(stdio.Out, clean, spans)
	return 0
}

// sampleArgs checks the shape of the sample arguments: it is given in exactly one way.
// Separate from reading, because a wrong call and an unreadable file are different
// return codes.
func sampleArgs(pos []string, sample string) error {
	switch {
	case sample != "" && len(pos) > 0:
		return fmt.Errorf("--sample and %s are set at the same time", pos[0])
	case len(pos) > 1:
		return fmt.Errorf("unexpected argument %q", pos[1])
	}
	return nil
}

// readSample gets the sample: --sample, a file argument or stdin.
func readSample(pos []string, sample string, stdin io.Reader) (string, error) {
	if sample != "" {
		return sample, nil
	}
	if len(pos) == 1 {
		b, err := os.ReadFile(pos[0])
		if err != nil {
			return "", err
		}
		return string(b), nil
	}
	b, err := io.ReadAll(stdin)
	if err != nil {
		return "", fmt.Errorf("reading stdin: %w", err)
	}
	return string(b), nil
}

// printSpans prints the found spans as a table: offset, type, rule,
// fragment. The fragment is printed — both commands are local and debug, the text
// the user sees their own. text is the one whose coordinates the spans are in.
//
// The fragment is printed via %q: a rule like ssh_key finds a multi-line
// secret, and without escaping it would break the table into rows.
func printSpans(w io.Writer, text string, spans []rules.Span) {
	if len(spans) == 0 {
		// The message does not contain the sample: otherwise "nothing found" would be
		// indistinguishable from printing the whole sample.
		fmt.Fprintln(w, "no matches")
		return
	}
	fmt.Fprintln(w, "offset\ttype\trule\tfragment")
	for _, s := range spans {
		fmt.Fprintf(w, "%d-%d\t%s\t%s\t%q\n", s.Start, s.End, s.Type, s.Rule, text[s.Start:s.End])
	}
}
