package main

import (
	"context"
	"fmt"
	"time"

	"github.com/werbot/shade/internal/config"
	"github.com/werbot/shade/internal/store"
)

func init() {
	Register(Command{
		Name: "entities",
		Help: "view the entities of a project and prune the old ones",
		Run:  runEntities,
	})
}

// runEntities splits the two subcommands with a switch, not a table like `rules`:
// there are two subcommands, both in this file, and there is nothing to assemble a registry from.
func runEntities(args []string, stdio IO) int {
	if len(args) == 0 {
		fmt.Fprintln(stdio.Err, "Usage: shade entities list|prune [--older-than 30d]")
		return 2
	}
	switch args[0] {
	case "list":
		return runEntitiesList(args[1:], stdio)
	case "prune":
		return runEntitiesPrune(args[1:], stdio)
	}
	fmt.Fprintf(stdio.Err, "entities: unknown subcommand %q\n", args[0])
	return 2
}

func runEntitiesList(args []string, stdio IO) int {
	const name = "entities list"
	pos, err := parseFlags(args, nil, nil)
	if err != nil {
		return fail(stdio, name, 2, err)
	}
	if err := noExtraArgs(pos); err != nil {
		return fail(stdio, name, 2, err)
	}

	ctx := context.Background()
	e, err := openEngine(ctx, "")
	if err != nil {
		return fail(stdio, name, 1, err)
	}
	defer e.Close()

	list, err := e.Store().ListEntities(ctx, e.ProjectID(), store.DefaultListLimit)
	if err != nil {
		return fail(stdio, name, 1, err)
	}
	fmt.Fprintln(stdio.Out, "placeholder\ttype\tfirst\tlast\thits")
	for _, ent := range list {
		fmt.Fprintf(stdio.Out, "%s\t%s\t%s\t%s\t%d\n",
			ent.Placeholder, ent.Type, stamp(ent.FirstSeen), stamp(ent.LastSeen), ent.Hits)
	}
	// The hidden tail is named explicitly: otherwise "there are no more entities" would read
	// as a fact, not as a slice of the list.
	if len(list) == store.DefaultListLimit {
		fmt.Fprintf(stdio.Out, "showing the first %d, the rest are hidden\n", store.DefaultListLimit)
	}
	return 0
}

func runEntitiesPrune(args []string, stdio IO) int {
	const name = "entities prune"
	var olderThan string
	pos, err := parseFlags(args, map[string]*string{"--older-than": &olderThan}, nil)
	if err != nil {
		return fail(stdio, name, 2, err)
	}
	if err := noExtraArgs(pos); err != nil {
		return fail(stdio, name, 2, err)
	}

	// An explicit age is parsed before the store is opened: openEngine creates the directory
	// the state directory, the key and the database, and a typo in the age must not create them.
	var age time.Duration
	if olderThan != "" {
		if age, err = config.ParseAge(olderThan); err != nil {
			return fail(stdio, name, 2, err)
		}
	}

	ctx := context.Background()
	e, err := openEngine(ctx, "")
	if err != nil {
		return fail(stdio, name, 1, err)
	}
	defer e.Close()

	if olderThan == "" {
		cfg, err := config.Load(store.Home(), e.RootPath())
		if err != nil {
			return fail(stdio, name, 1, err)
		}
		ttl := cfg.EntitiesTTL
		if ttl == "" {
			// An empty string is an explicit entities_ttl = "" in the config: take the default
			// of the defaults layer, not a second copy of it here.
			ttl = config.Default().EntitiesTTL
		}
		// An unparsable age from the config is an operational error, not a call
		// error: the user has to fix it in the file, not in the arguments.
		if age, err = config.ParseAge(ttl); err != nil {
			return fail(stdio, name, 1, err)
		}
	}

	n, err := e.Store().PruneEntities(ctx, e.ProjectID(), age)
	if err != nil {
		return fail(stdio, name, 1, err)
	}
	fmt.Fprintf(stdio.Out, "entities deleted: %d\n", n)
	return 0
}
