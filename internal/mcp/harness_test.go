package mcp_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/werbot/shade/internal/core"
	"github.com/werbot/shade/internal/mcp"
	"github.com/werbot/shade/internal/placeholder"
	"github.com/werbot/shade/internal/rules"
	"github.com/werbot/shade/internal/store"
)

// The harness the tool tests share: a fake engine, a connected session and the wire
// shapes. Kept apart from the tests themselves so each file stays one concern.

const (
	testSecret = "user@example.com"
	testToken  = "<EMAIL_1>"
)

// fakeEngine stands in for the core without a database: it swaps one literal secret for
// one literal token, so the tests exercise argument parsing and output shaping rather
// than the engine. The real path is the end-to-end test of Task 9. Store and ProjectID
// exist only to satisfy Engine — the text tools never call them.
type fakeEngine struct {
	secret     string // the value Anonymize hides and Restore brings back
	token      string // the placeholder that stands for it, e.g. <EMAIL_1>
	unresolved []placeholder.Token
	store      *store.Store
	projectID  int64
	root       string
}

func (f fakeEngine) Anonymize(_ context.Context, text string) (core.Result, error) {
	out := strings.ReplaceAll(text, f.secret, f.token)
	var spans []rules.Span
	if out != text {
		spans = append(spans, rules.Span{Type: "EMAIL", Rule: "email"})
	}
	return core.Result{Text: out, Spans: spans}, nil
}

func (f fakeEngine) Restore(_ context.Context, text string) (core.Result, error) {
	return core.Result{
		Text:       strings.ReplaceAll(text, f.token, f.secret),
		Unresolved: f.unresolved,
	}, nil
}

// Scan mirrors the core contract the tool relies on: a name outside the active set is the
// only error, and the spans carry the offsets the handler must drop. The returned text is
// the masked one; for an input with no foreign placeholder it is the input as is, so a
// handler that passed it through instead of dropping it would show up in the leak canary.
func (f fakeEngine) Scan(text, only string) (string, []rules.Span, error) {
	if only != "" && only != "email" {
		return "", nil, fmt.Errorf("rule %q not found", only)
	}
	var spans []rules.Span
	if i := strings.Index(text, f.secret); i >= 0 {
		spans = append(spans, rules.Span{Start: i, End: i + len(f.secret), Type: "EMAIL", Rule: "email"})
	}
	return text, spans, nil
}

func (f fakeEngine) RootPath() string    { return f.root }
func (f fakeEngine) Store() *store.Store { return f.store }
func (f fakeEngine) ProjectID() int64    { return f.projectID }
func (f fakeEngine) Close() error        { return nil }

// toolSession brings up the server with a fake engine and returns the connected client
// session. home is the state directory config.Load reads for deanonymize.
func toolSession(t *testing.T, home string, f fakeEngine) *sdk.ClientSession {
	t.Helper()
	ctx := t.Context()

	server := mcp.NewServer(home, func(context.Context) (mcp.Engine, error) { return f, nil }, io.Discard)
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

// callTool runs a tool and fails on a protocol error. A tool that reports a failure the
// protocol way (isError) still returns a result and no error.
func callTool(t *testing.T, cs *sdk.ClientSession, name string, args map[string]any) *sdk.CallToolResult {
	t.Helper()
	res, err := cs.CallTool(t.Context(), &sdk.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatalf("call %s: %v", name, err)
	}
	return res
}

// The wire shapes are declared here, not imported from internal/mcp: the field names are
// the contract the CLI --json output and the MCP schema share, so a rename inside the
// package must fail these tests rather than silently change what a client receives.
type anonWire struct {
	Text  string     `json:"text"`
	Spans []spanWire `json:"spans"`
}

type spanWire struct {
	Type string `json:"type"`
	Rule string `json:"rule"`
}

type deanonWire struct {
	Text       string      `json:"text"`
	Unresolved []tokenWire `json:"unresolved"`
}

type tokenWire struct {
	Type string `json:"type"`
	Raw  string `json:"raw"`
}

// structured decodes the structured content of a result into the declared wire shape.
func structured[T any](t *testing.T, res *sdk.CallToolResult) T {
	t.Helper()
	b, err := json.Marshal(res.StructuredContent)
	if err != nil {
		t.Fatalf("marshaling structuredContent: %v", err)
	}
	var out T
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatalf("unmarshaling structuredContent %s: %v", b, err)
	}
	return out
}

// rawResult is the whole response as it would travel the wire, structured content and
// text content alike — the input to the leak canary.
func rawResult(t *testing.T, res *sdk.CallToolResult) string {
	t.Helper()
	b, err := json.Marshal(res)
	if err != nil {
		t.Fatalf("marshaling result: %v", err)
	}
	return string(b)
}

// writeConfig writes the global config the tools read through config.Load.
func writeConfig(t *testing.T, home, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(home, "config.toml"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}
