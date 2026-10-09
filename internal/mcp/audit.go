package mcp

import (
	"context"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/werbot/shade/internal/config"
	"github.com/werbot/shade/internal/store"
)

// addAuditTools registers the read-only view of the journal.
func addAuditTools(srv *sdk.Server, open Opener) {
	sdk.AddTool(srv, &sdk.Tool{
		Name: "unresolved_report",
		Description: "List the placeholders that arrived from the model without a value in " +
			"this project's store, newest first — what `shade audit --unresolved` prints. Reads only.",
	}, func(ctx context.Context, _ *sdk.CallToolRequest, in unresolvedInput) (*sdk.CallToolResult, unresolvedOutput, error) {
		return unresolvedReport(ctx, open, in)
	})
}

// unresolvedReport lists the journal rows about unresolved placeholders. The journal keeps
// no values: detail on such a row is the placeholder token itself, and only it.
//
// The age is parsed before the engine is opened, as the CLI does: a typo in it must not
// create the state directory, the key and the database.
func unresolvedReport(ctx context.Context, open Opener, in unresolvedInput) (*sdk.CallToolResult, unresolvedOutput, error) {
	filter := store.AuditFilter{Unresolved: true}
	if in.Since != "" {
		age, err := config.ParseAge(in.Since)
		if err != nil {
			return nil, unresolvedOutput{}, err
		}
		filter.Since = time.Now().Add(-age).Unix()
	}
	limit := in.Limit
	if limit <= 0 {
		limit = store.DefaultListLimit
	}

	e, err := open(ctx)
	if err != nil {
		return nil, unresolvedOutput{}, err
	}
	defer e.Close()

	entries, err := e.Store().Audit(ctx, e.ProjectID(), limit, filter)
	if err != nil {
		return nil, unresolvedOutput{}, err
	}
	return nil, unresolvedOutput{Entries: toAuditInfos(entries)}, nil
}

// toAuditInfos maps the journal rows to their wire shape. An empty input still gives a
// non-nil slice: null reads as "the field is absent" in a machine contract.
func toAuditInfos(entries []store.AuditEntry) []auditInfo {
	out := make([]auditInfo, 0, len(entries))
	for _, en := range entries {
		out = append(out, auditInfo{
			Time: en.TS, Adapter: en.Adapter, Type: en.Type, Action: en.Action, Detail: en.Detail,
		})
	}
	return out
}
