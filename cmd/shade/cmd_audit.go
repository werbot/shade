package main

import (
	"context"
	"fmt"
	"time"

	"github.com/werbot/shade/internal/store"
)

func init() {
	Register(Command{
		Name: "audit",
		Help: "journal of unresolved placeholders and blocks",
		Run:  runAudit,
	})
}

func runAudit(args []string, stdio IO) int {
	const name = "audit"
	var since string
	var unresolved bool
	pos, err := parseFlags(args,
		map[string]*string{"--since": &since},
		map[string]*bool{"--unresolved": &unresolved})
	if err != nil {
		return fail(stdio, name, 2, err)
	}
	if err := noExtraArgs(pos); err != nil {
		return fail(stdio, name, 2, err)
	}

	filter := store.AuditFilter{Unresolved: unresolved}
	if since != "" {
		age, err := parseAge(since)
		if err != nil {
			return fail(stdio, name, 2, err)
		}
		filter.Since = time.Now().Add(-age).Unix()
	}

	ctx := context.Background()
	e, err := openEngine(ctx, "")
	if err != nil {
		return fail(stdio, name, 1, err)
	}
	defer e.Close()

	entries, err := e.Store().Audit(ctx, e.ProjectID(), defaultListLimit, filter)
	if err != nil {
		return fail(stdio, name, 1, err)
	}
	fmt.Fprintln(stdio.Out, "time\tdirection\tadapter\trule\ttype\taction\tdetail")
	for _, en := range entries {
		fmt.Fprintf(stdio.Out, "%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
			stamp(en.TS), en.Direction, en.Adapter, dash(en.Rule), dash(en.Type), en.Action, dash(en.Detail))
	}
	return 0
}

// dash prints an empty column value as "—": an empty cell in a table is not
// distinguishable from a skipped column.
func dash(s string) string {
	if s == "" {
		return "—"
	}
	return s
}
