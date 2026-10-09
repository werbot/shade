// Package mcp exposes shade to a model over the Model Context Protocol. It is the
// third adapter beside hook and proxy: the model calls the tools and reads the
// directive resource, and every answer comes from the same core.
package mcp

import (
	"context"
	"io"
	"runtime/debug"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/werbot/shade/internal/core"
	"github.com/werbot/shade/internal/directive"
	"github.com/werbot/shade/internal/rules"
	"github.com/werbot/shade/internal/store"
)

// DirectiveURI is the resource that carries the placeholder contract. The model reads
// it instead of receiving it in a prompt, so the text lives in one place.
const DirectiveURI = "shade://directive"

// Engine is the part of the core the MCP server needs. RecordBlocked is not here:
// MCP does not block, it reports.
type Engine interface {
	Anonymize(ctx context.Context, text string) (core.Result, error)
	Restore(ctx context.Context, text string) (core.Result, error)
	Scan(text, only string) (string, []rules.Span, error)
	RootPath() string
	Store() *store.Store
	ProjectID() int64
	Close() error
}

// The interface above mirrors *core.Engine: a drift between the two must be a build
// failure here, not a surprise in a tool handler.
var _ Engine = (*core.Engine)(nil)

// Opener brings up an engine for the project of this process. The directory is fixed
// when the server starts, so the opener takes none: for the lifetime of the process
// there is exactly one project.
type Opener func(ctx context.Context) (Engine, error)

// NewServer builds the server, registers the directive resource and the tools. Each
// tool group is registered by its own function so this stays a list of what exists.
//
// home is the shade state directory the config for deanonymize is read from, and
// diag receives diagnostics: stdout carries the protocol.
func NewServer(home string, open Opener, diag io.Writer) *sdk.Server {
	srv := sdk.NewServer(&sdk.Implementation{Name: "shade", Version: version()}, nil)
	srv.AddResource(&sdk.Resource{
		URI:         DirectiveURI,
		Name:        "directive",
		Description: "The placeholder contract to hand the model",
		MIMEType:    "text/markdown",
	}, readDirective)
	addTextTools(srv, home, open, diag)
	addRuleTools(srv, open)
	addAuditTools(srv, open)
	return srv
}

// readDirective serves the directive. URI and MIMEType are left empty on purpose:
// the server fills them in from the registered resource.
func readDirective(context.Context, *sdk.ReadResourceRequest) (*sdk.ReadResourceResult, error) {
	return &sdk.ReadResourceResult{Contents: []*sdk.ResourceContents{{Text: directive.Text()}}}, nil
}

// version reports the build version for the Implementation, "(devel)" for a local
// build — the same answer the version command gives, read here because cmd/shade is
// package main and cannot be imported.
func version() string {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return "(devel)"
	}
	return info.Main.Version
}
