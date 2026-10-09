package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// The phase's acceptance criterion is one round trip: an external MCP client anonymizes a
// prompt and restores the model's answer. e2eMCPPrompt is that prompt — an ssh target,
// the shape the builtin USER/HOST rules match, synthetic on purpose: a real credential
// has no business in a test.
const (
	e2eMCPHost   = "db.prod.local"
	e2eMCPPrompt = "connect to ssh user@" + e2eMCPHost
)

// TestMCPEndToEndAnonymizesAndRestores is the acceptance criterion of phase 3. It is the
// only test that runs the real thing end to end: the built binary as a subprocess over
// CommandTransport — a real external MCP client — a real store in a temporary git
// repository, and the answer coming back through the protocol.
func TestMCPEndToEndAnonymizesAndRestores(t *testing.T) {
	bin, home, repo := buildShade(t), t.TempDir(), gitDir(t)
	cs := mcpCommandClient(t, bin, home, repo)

	anon := callMCPTool(t, cs, "anonymize", map[string]any{"text": e2eMCPPrompt})
	anonText := answerText(t, anon)
	if !strings.Contains(anonText, "<USER_1>") || !strings.Contains(anonText, "<HOST_1>") {
		t.Fatalf("anonymize issued no token: %q", anonText)
	}
	// The literal must be absent from the whole answer, not just from `text`: a value
	// smuggled into a span would leak there.
	assertAnswerHides(t, anon, e2eMCPHost)

	// What the model sends back is its own prose with the tokens left in it. Restoring it
	// must put the real values back and leave nothing behind.
	reply := "Sure — run `" + anonText + "` to connect."
	restored := callMCPTool(t, cs, "deanonymize", map[string]any{"text": reply})
	if got := answerText(t, restored); got != "Sure — run `"+e2eMCPPrompt+"` to connect." {
		t.Fatalf("the answer was not restored: %q", got)
	}
}

// TestMCPForeignProjectTokenIsUnresolved pins the project boundary: a token is a
// placeholder only in the project that issued it. The same binary and the same state
// directory, a different --project, and the token has no value there — under the default
// fail_closed that is a refusal: isError with an empty text, the MCP spelling of exit 3.
func TestMCPForeignProjectTokenIsUnresolved(t *testing.T) {
	bin, home := buildShade(t), t.TempDir()
	issuer, stranger := gitDir(t), gitDir(t)

	anon := callMCPTool(t, mcpCommandClient(t, bin, home, issuer), "anonymize",
		map[string]any{"text": e2eMCPPrompt})

	res := callMCPTool(t, mcpCommandClient(t, bin, home, stranger), "deanonymize",
		map[string]any{"text": answerText(t, anon)})
	if !res.IsError {
		t.Fatal("a foreign project's token must be refused under fail_closed")
	}
	if got := answerText(t, res); got != "" {
		t.Fatalf("a refused answer must carry no text, got %q", got)
	}
	if raws := unresolvedRaws(t, res); !slices.Contains(raws, "<USER_1>") || !slices.Contains(raws, "<HOST_1>") {
		t.Fatalf("the refusal must name the unresolved tokens, got %v", raws)
	}
}

// TestMCPConcurrentCallsShareTheStore pays for the engine-per-call decision: every call
// opens its own connection to one database, so one session's parallel calls are several
// writers at once. Twenty of them must all be served — WAL with busy_timeout is what makes
// that hold, and a writer that cannot wait surfaces as "database is locked".
func TestMCPConcurrentCallsShareTheStore(t *testing.T) {
	bin, home, repo := buildShade(t), t.TempDir(), gitDir(t)
	cs := mcpCommandClient(t, bin, home, repo)

	const calls = 20
	host := func(i int) string { return fmt.Sprintf("host%d.example.com", i) }

	ctx := t.Context()
	var wg sync.WaitGroup
	results := make([]*sdk.CallToolResult, calls)
	errs := make([]error, calls)
	for i := range calls {
		wg.Add(1)
		go func() {
			defer wg.Done()
			// The result is read after the wait: answerText calls t.Fatalf, which a test
			// goroutine must not do.
			results[i], errs[i] = cs.CallTool(ctx, &sdk.CallToolParams{
				Name:      "anonymize",
				Arguments: map[string]any{"text": "connect to ssh user@" + host(i)},
			})
		}()
	}
	wg.Wait()

	for i, res := range results {
		if err := errs[i]; err != nil {
			if strings.Contains(err.Error(), "database is locked") {
				t.Fatalf("call %d lost the store to a neighbour: %v", i, err)
			}
			t.Fatalf("call %d failed: %v", i, err)
		}
		text := answerText(t, res)
		if !strings.Contains(text, "<HOST_") {
			t.Errorf("call %d got no token: %q", i, text)
		}
		if strings.Contains(text, host(i)) {
			t.Errorf("call %d leaked its value: %q", i, text)
		}
	}
}

// assertAnswerHides fails if any part of a tool's answer carries the literal: the value
// must not survive in `text`, in a span, or in an unresolved token.
func assertAnswerHides(t *testing.T, res *sdk.CallToolResult, value string) {
	t.Helper()
	b, err := json.Marshal(res)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(b, []byte(value)) {
		t.Fatalf("the value %q leaked into the answer: %s", value, b)
	}
}

// unresolvedRaws lists the tokens a deanonymize answer reports as unresolved.
func unresolvedRaws(t *testing.T, res *sdk.CallToolResult) []string {
	t.Helper()
	b, err := json.Marshal(res.StructuredContent)
	if err != nil {
		t.Fatalf("marshaling structuredContent: %v", err)
	}
	var out struct {
		Unresolved []struct {
			Raw string `json:"raw"`
		} `json:"unresolved"`
	}
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatalf("unmarshaling structuredContent %s: %v", b, err)
	}
	raws := make([]string, 0, len(out.Unresolved))
	for _, u := range out.Unresolved {
		raws = append(raws, u.Raw)
	}
	return raws
}
