package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/werbot/shade/internal/store"
)

// lockedBuffer is a bytes.Buffer safe to write from the transport's read goroutine while
// the test reads it: the stdout tee and the assertion run at the same time.
type lockedBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *lockedBuffer) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *lockedBuffer) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

// mcpEnv is the environment of the server subprocess: the state directory, and nothing
// that names another one.
func mcpEnv(home string) []string {
	env := slices.DeleteFunc(os.Environ(), func(e string) bool {
		return strings.HasPrefix(e, "SHADE_HOME=")
	})
	return append(env, "SHADE_HOME="+home)
}

// mcpCommandClient connects the SDK client over a CommandTransport — the transport an
// SDK-based client uses to spawn a stdio server. It owns the child's stdout, so a test
// using it cannot see the raw stream; TestMCPProcessContract's purity check needs its own
// process for that.
func mcpCommandClient(t *testing.T, bin, home, workDir string, args ...string) *sdk.ClientSession {
	t.Helper()
	cmd := exec.Command(bin, append([]string{"mcp"}, args...)...)
	cmd.Dir = workDir
	cmd.Env = mcpEnv(home)

	client := sdk.NewClient(&sdk.Implementation{Name: "test", Version: "v0"}, nil)
	cs, err := client.Connect(t.Context(), &sdk.CommandTransport{Command: cmd}, nil)
	if err != nil {
		t.Fatalf("connecting the client: %v", err)
	}
	t.Cleanup(func() { cs.Close() })
	return cs
}

// mcpTeeClient connects a real SDK client over the child's pipes with stdout teed into
// raw, so a test can assert every byte of it belongs to the protocol. workDir is the
// client's working directory, which is what --project must override.
func mcpTeeClient(t *testing.T, bin, home, workDir string, args ...string) (*sdk.ClientSession, *lockedBuffer, *lockedBuffer) {
	t.Helper()
	cmd := exec.Command(bin, append([]string{"mcp"}, args...)...)
	cmd.Dir = workDir
	cmd.Env = mcpEnv(home)

	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	errOut := &lockedBuffer{}
	cmd.Stderr = errOut
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}

	raw := &lockedBuffer{}
	client := sdk.NewClient(&sdk.Implementation{Name: "test", Version: "v0"}, nil)
	cs, err := client.Connect(t.Context(), &sdk.IOTransport{
		Reader: io.NopCloser(io.TeeReader(stdout, raw)),
		Writer: stdin,
	}, nil)
	if err != nil {
		t.Fatalf("connecting the client: %v", err)
	}
	t.Cleanup(func() {
		cs.Close()
		done := make(chan struct{})
		go func() { cmd.Wait(); close(done) }()
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			cmd.Process.Kill()
			<-done
		}
	})
	return cs, raw, errOut
}

// callMCPTool runs a tool and fails on a protocol error. A tool that reports a failure the
// protocol way (isError) still returns a result and no error.
func callMCPTool(t *testing.T, cs *sdk.ClientSession, name string, args map[string]any) *sdk.CallToolResult {
	t.Helper()
	res, err := cs.CallTool(t.Context(), &sdk.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatalf("call %s: %v", name, err)
	}
	return res
}

// answerText is the `text` field of a tool's structured content.
func answerText(t *testing.T, res *sdk.CallToolResult) string {
	t.Helper()
	b, err := json.Marshal(res.StructuredContent)
	if err != nil {
		t.Fatalf("marshaling structuredContent: %v", err)
	}
	var out struct {
		Text string `json:"text"`
	}
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatalf("unmarshaling structuredContent %s: %v", b, err)
	}
	return out.Text
}

// hasEntity reports whether the project of dir holds an entity with the given placeholder.
func hasEntity(t *testing.T, home, dir, placeholder string) bool {
	t.Helper()
	p, st := testStore(t, home, dir)
	list, err := st.ListEntities(context.Background(), p.ID, store.DefaultListLimit)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range list {
		if e.Placeholder == placeholder {
			return true
		}
	}
	return false
}

// TestMCPProcessContract pins the two properties an MCP client depends on: the project is
// the one --project names and not the client's working directory, and stdout carries
// nothing but JSON-RPC — a stray diagnostic line breaks the client's session.
func TestMCPProcessContract(t *testing.T) {
	bin := buildShade(t)
	home := t.TempDir()
	cwd, flagged := gitDir(t), gitDir(t)

	t.Run("the flagged project issues the token", func(t *testing.T) {
		cs := mcpCommandClient(t, bin, home, cwd, "--project", flagged)
		res := callMCPTool(t, cs, "anonymize", map[string]any{"text": "ssh deploy@db.prod.local"})
		text := answerText(t, res)
		if strings.Contains(text, "db.prod.local") {
			t.Fatalf("the raw value leaked: %q", text)
		}
		if !strings.Contains(text, "<USER_1>") || !strings.Contains(text, "<HOST_1>") {
			t.Fatalf("the answer has no placeholders: %q", text)
		}
		// The token must live in the flagged project's store; the client's working
		// directory is a different project and must know nothing of it. Checking only
		// that a token came back would pass even if --project were ignored.
		if !hasEntity(t, home, flagged, "<HOST_1>") {
			t.Fatal("the flagged project has no entity for <HOST_1>: --project was ignored")
		}
		if hasEntity(t, home, cwd, "<HOST_1>") {
			t.Fatal("the working directory's project got the entity: --project was ignored")
		}
	})

	t.Run("stdout carries only protocol", func(t *testing.T) {
		cs, raw, errOut := mcpTeeClient(t, bin, home, cwd, "--project", flagged)
		callMCPTool(t, cs, "anonymize", map[string]any{"text": "ssh deploy@db.prod.local"})

		lines := strings.Split(strings.TrimSpace(raw.String()), "\n")
		if len(lines) < 2 {
			t.Fatalf("captured %d lines of stdout, want a protocol exchange", len(lines))
		}
		for i, line := range lines {
			var msg map[string]any
			if err := json.Unmarshal([]byte(line), &msg); err != nil {
				t.Fatalf("stdout line %d is not JSON-RPC: %q (%v)", i+1, line, err)
			}
			if msg["jsonrpc"] != "2.0" {
				t.Fatalf("stdout line %d is not a JSON-RPC message: %q", i+1, line)
			}
		}
		if got := errOut.String(); got != "" {
			t.Fatalf("stderr must stay empty, got %q", got)
		}
	})
}

// An unknown flag is a call error: exit 2, before a server is started.
func TestMCPRejectsUnknownFlag(t *testing.T) {
	code, out, errOut := runCLI(t, t.TempDir(), t.TempDir(), []string{"mcp", "--nope"}, "")
	if code != 2 {
		t.Fatalf("code=%d, want 2 (%s)", code, errOut)
	}
	if !strings.Contains(errOut, "--nope") {
		t.Fatalf("stderr must name the flag: %q", errOut)
	}
	if out != "" {
		t.Fatalf("stdout must stay empty, got %q", out)
	}
}

// An explicitly empty --project is a call error, not a silent fallback to the working
// directory: parseInputArgs answers the same input with exit 2 for every other command,
// and two parsers of one syntax must not give two answers.
func TestMCPRejectsEmptyProject(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
	}{
		{"joined", []string{"mcp", "--project="}},
		{"separate", []string{"mcp", "--project", ""}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			code, out, errOut := runCLI(t, t.TempDir(), t.TempDir(), tc.args, "")
			if code != 2 {
				t.Fatalf("code=%d, want 2 (%s)", code, errOut)
			}
			if !strings.Contains(errOut, "--project requires a directory") {
				t.Fatalf("stderr must name the problem: %q", errOut)
			}
			if out != "" {
				t.Fatalf("stdout must stay empty, got %q", out)
			}
		})
	}
}

// An unknown tool is a protocol error; the session survives it and the next call is
// answered — the server must not die on a name it does not know.
func TestMCPRejectsUnknownTool(t *testing.T) {
	bin := buildShade(t)
	home, dir := t.TempDir(), gitDir(t)
	cs := mcpCommandClient(t, bin, home, dir)

	if _, err := cs.CallTool(t.Context(), &sdk.CallToolParams{Name: "no_such_tool"}); err == nil {
		t.Fatal("an unknown tool must be a protocol error")
	}
	res := callMCPTool(t, cs, "anonymize", map[string]any{"text": "ssh deploy@db.prod.local"})
	if text := answerText(t, res); !strings.Contains(text, "<HOST_1>") {
		t.Fatalf("the session stopped answering after the error: %q", text)
	}
}
