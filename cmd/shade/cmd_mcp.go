package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/werbot/shade/internal/core"
	"github.com/werbot/shade/internal/mcp"
	"github.com/werbot/shade/internal/store"
)

func init() {
	Register(Command{
		Name: "mcp",
		Help: "serve the model context protocol over stdio",
		Run:  runMCP,
	})
}

// runMCP serves the MCP protocol on stdin/stdout. The project is resolved once, before
// serving: an MCP client starts the server from anywhere, so the project is a property of
// this process and not of a call. stdout belongs to the protocol, so the diagnostics the
// server emits go to stdio.Err — the IO seam of the package, not os.Stderr directly.
func runMCP(args []string, stdio IO) int {
	var project string
	pos, err := parseFlags(args, map[string]*string{"--project": &project}, nil)
	if err != nil {
		return fail(stdio, "mcp", 2, err)
	}
	if err := noExtraArgs(pos); err != nil {
		return fail(stdio, "mcp", 2, err)
	}
	// An explicitly empty --project is a call error, not a fallback to the working
	// directory: parseInputArgs answers the same input with exit 2 for every other
	// command, and a "project" that is really the cwd creates placeholder rows that
	// nothing else can reach.
	for _, arg := range args {
		if name, _, _ := strings.Cut(arg, "="); name == "--project" && project == "" {
			return fail(stdio, "mcp", 2, errors.New("--project requires a directory"))
		}
	}
	if project != "" {
		if err := checkDir(project); err != nil {
			return fail(stdio, "mcp", 2, fmt.Errorf("--project: %w", err))
		}
	}
	if project == "" {
		project, err = os.Getwd()
		if err != nil {
			return fail(stdio, "mcp", 1, fmt.Errorf("working directory: %w", err))
		}
	}
	home := store.Home()
	open := func(ctx context.Context) (mcp.Engine, error) {
		return core.New(ctx, home, project, "mcp")
	}
	if err := mcp.NewServer(home, open, stdio.Err).Run(context.Background(), &sdk.StdioTransport{}); err != nil {
		return fail(stdio, "mcp", 1, err)
	}
	return 0
}
