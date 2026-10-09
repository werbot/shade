package mcp

import (
	"context"
	"fmt"
	"io"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/werbot/shade/internal/config"
	"github.com/werbot/shade/internal/placeholder"
	"github.com/werbot/shade/internal/rules"
)

// addTextTools registers the tools that take one text and return one. Each call opens
// the engine and closes it after: the opener brings up an engine for the project of this
// process, and the call is the unit that owns it.
func addTextTools(srv *sdk.Server, home string, open Opener, diag io.Writer) {
	sdk.AddTool(srv, &sdk.Tool{
		Name: "anonymize",
		Description: "Replace the sensitive values in the text with placeholders. " +
			"Hand the result to the model in place of the original text.",
	}, func(ctx context.Context, _ *sdk.CallToolRequest, in textInput) (*sdk.CallToolResult, anonOutput, error) {
		return anonymize(ctx, open, diag, in)
	})

	sdk.AddTool(srv, &sdk.Tool{
		Name: "deanonymize",
		Description: "Restore the real values in place of the placeholders, " +
			"for text the model produced.",
	}, func(ctx context.Context, _ *sdk.CallToolRequest, in textInput) (*sdk.CallToolResult, deanonOutput, error) {
		return deanonymize(ctx, home, open, diag, in)
	})
}

// anonymize replaces the values and returns the text with placeholders. A failure of the
// auxiliary counter write does not take the text away: it goes to diag, the same way
// `shade anon` reports it on stderr, and the ready text is still delivered.
func anonymize(ctx context.Context, open Opener, diag io.Writer, in textInput) (*sdk.CallToolResult, anonOutput, error) {
	e, err := open(ctx)
	if err != nil {
		return nil, anonOutput{}, err
	}
	defer e.Close()

	res, err := e.Anonymize(ctx, in.Text)
	if err != nil {
		return nil, anonOutput{}, err
	}
	if res.RecordErr != nil {
		fmt.Fprintf(diag, "mcp: stats not recorded: %v\n", res.RecordErr)
	}
	return nil, anonOutput{Text: res.Text, Spans: toSpans(res.Spans)}, nil
}

// deanonymize restores the values. Under any policy but fail_open_log a text with an
// unresolved token is blocked: isError marks the refusal while unresolved still names
// the tokens — that is what the CLI expresses with exit 3. A failure of the journal
// write does not take the answer away either; it goes to diag.
func deanonymize(ctx context.Context, home string, open Opener, diag io.Writer, in textInput) (*sdk.CallToolResult, deanonOutput, error) {
	e, err := open(ctx)
	if err != nil {
		return nil, deanonOutput{}, err
	}
	defer e.Close()

	cfg, err := config.Load(home, e.RootPath())
	if err != nil {
		return nil, deanonOutput{}, err
	}
	res, err := e.Restore(ctx, in.Text)
	if err != nil {
		return nil, deanonOutput{}, err
	}
	if res.RecordErr != nil {
		fmt.Fprintf(diag, "mcp: journal not recorded: %v\n", res.RecordErr)
	}
	if len(res.Unresolved) > 0 && cfg.FailPolicy != config.FailOpenLog {
		// The answer is blocked, not the diagnostics: isError marks the refusal while
		// unresolved still names the tokens — that is what the CLI expresses with exit 3.
		return &sdk.CallToolResult{IsError: true}, deanonOutput{Text: "", Unresolved: toTokens(res.Unresolved)}, nil
	}
	return nil, deanonOutput{Text: res.Text, Unresolved: toTokens(res.Unresolved)}, nil
}

// toSpans maps the found fragments to their wire shape. An empty input still gives a
// non-nil slice: null reads as "the field is absent" in a machine contract.
func toSpans(spans []rules.Span) []spanInfo {
	out := make([]spanInfo, 0, len(spans))
	for _, s := range spans {
		out = append(out, spanInfo{Type: s.Type, Rule: s.Rule})
	}
	return out
}

// toTokens maps the unresolved placeholders to their wire shape. raw is the token, so a
// value can never get into the answer.
func toTokens(toks []placeholder.Token) []tokenInfo {
	out := make([]tokenInfo, 0, len(toks))
	for _, tok := range toks {
		out = append(out, tokenInfo{Type: tok.Type, Raw: tok.Raw})
	}
	return out
}
