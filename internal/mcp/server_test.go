package mcp_test

import (
	"context"
	"errors"
	"io"
	"testing"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/werbot/shade/internal/directive"
	"github.com/werbot/shade/internal/mcp"
)

// session brings up the server on an in-memory transport and returns the connected
// client session. Its callers only read the directive resource or call an unknown tool
// name, so no registered tool handler runs and the opener is never reached. NewServer
// still takes one, and the tools it registers need it, so the opener here fails with an
// error: a tool reached through this helper reports that error instead of panicking on
// a nil func. The tool tests use toolSession, which supplies a fake engine.
func session(t *testing.T) *sdk.ClientSession {
	t.Helper()
	ctx := t.Context()

	noEngine := func(context.Context) (mcp.Engine, error) {
		return nil, errors.New("session: no engine; the tool tests use toolSession")
	}
	server := mcp.NewServer(t.TempDir(), noEngine, io.Discard)
	t1, t2 := sdk.NewInMemoryTransports()
	if _, err := server.Connect(ctx, t1, nil); err != nil {
		t.Fatal(err)
	}

	client := sdk.NewClient(&sdk.Implementation{Name: "test", Version: "v0"}, nil)
	cs, err := client.Connect(ctx, t2, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cs.Close() })
	return cs
}

// TestDirectiveResourceMatchesDirectiveText holds §8's one-source-of-truth claim: the
// resource serves directive.Text() byte for byte, so a copy that drifted would fail
// here instead of silently reaching the model.
func TestDirectiveResourceMatchesDirectiveText(t *testing.T) {
	res, err := session(t).ReadResource(t.Context(), &sdk.ReadResourceParams{URI: mcp.DirectiveURI})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Contents) != 1 {
		t.Fatalf("contents: got %d, want 1", len(res.Contents))
	}

	c := res.Contents[0]
	if c.Text != directive.Text() {
		t.Errorf("text:\n got %q\nwant %q", c.Text, directive.Text())
	}
	if c.URI != mcp.DirectiveURI {
		t.Errorf("URI: got %q, want %q", c.URI, mcp.DirectiveURI)
	}
	if c.MIMEType != "text/markdown" {
		t.Errorf("MIMEType: got %q, want %q", c.MIMEType, "text/markdown")
	}
}

// TestUnknownToolIsAProtocolError pins the behaviour later tasks rely on: the SDK
// answers an unknown tool with an error, not a panic and not an empty result.
func TestUnknownToolIsAProtocolError(t *testing.T) {
	if _, err := session(t).CallTool(t.Context(), &sdk.CallToolParams{Name: "no_such_tool"}); err == nil {
		t.Fatal("CallTool of an unknown tool: got nil error, want a protocol error")
	}
}
