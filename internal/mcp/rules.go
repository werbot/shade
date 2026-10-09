package mcp

import (
	"context"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/werbot/shade/internal/rules"
	"github.com/werbot/shade/internal/store"
)

// addRuleTools registers the tools that work with the rule set.
func addRuleTools(srv *sdk.Server, open Opener) {
	sdk.AddTool(srv, &sdk.Tool{
		Name: "rules_list",
		Description: "List the rules that apply to this project: the global ones and the " +
			"project's own, including the disabled. Reads only.",
	}, func(ctx context.Context, _ *sdk.CallToolRequest, _ any) (*sdk.CallToolResult, rulesOutput, error) {
		return rulesList(ctx, open)
	})

	sdk.AddTool(srv, &sdk.Tool{
		Name: "rules_test",
		Description: "Check a rule before saving it: compile the pattern and run it over the " +
			"sample. Nothing is stored and no rule set is touched.",
	}, func(_ context.Context, _ *sdk.CallToolRequest, in rulesTestInput) (*sdk.CallToolResult, spansOutput, error) {
		return rulesTest(in)
	})
}

// rulesList answers with the global rules and the project's own together. It opens the
// engine for the project — the engine is where the project of this process is resolved —
// not for its rule set: ListRules already returns both scopes in one call.
func rulesList(ctx context.Context, open Opener) (*sdk.CallToolResult, rulesOutput, error) {
	e, err := open(ctx)
	if err != nil {
		return nil, rulesOutput{}, err
	}
	defer e.Close()

	id := e.ProjectID()
	rows, err := e.Store().ListRules(ctx, &id)
	if err != nil {
		return nil, rulesOutput{}, err
	}
	return nil, rulesOutput{Rules: toRuleInfos(rows)}, nil
}

// toRuleInfos maps the store rows to their wire shape. scope is derived from ProjectID: a
// nil project id on a row is the global scope.
func toRuleInfos(rows []store.RuleRow) []ruleInfo {
	out := make([]ruleInfo, 0, len(rows))
	for _, row := range rows {
		scope := "global"
		if row.ProjectID != nil {
			scope = "project"
		}
		out = append(out, ruleInfo{
			Name: row.Name, Type: row.Type, Enabled: row.Enabled,
			Builtin: row.Builtin, Scope: scope,
		})
	}
	return out
}

// rulesTest compiles the rule from the request and runs it over the sample, exactly as the
// local `shade rules test` does. No engine and no store are opened: the rule is ad-hoc, the
// sample is the raw text, and nothing is stored. kind and type default to regex and SECRET,
// the same defaults the CLI takes.
func rulesTest(in rulesTestInput) (*sdk.CallToolResult, spansOutput, error) {
	kind := in.Kind
	if kind == "" {
		kind = "regex"
	}
	typ := in.Type
	if typ == "" {
		typ = "SECRET"
	}
	rule, err := rules.Compile(rules.Spec{
		ID: rules.AdHocName, Type: typ, Kind: kind, Pattern: in.Pattern, Enabled: true,
	})
	if err != nil {
		return nil, spansOutput{}, err
	}
	return nil, spansOutput{Spans: toSpans(rules.Detect(in.Sample, []rules.Rule{rule}))}, nil
}
