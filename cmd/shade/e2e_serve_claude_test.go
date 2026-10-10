package main

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestServeWrapsARealClaudeCodeSession mirrors the gated end-to-end test: a real claude
// session pointed at the proxy by ANTHROPIC_BASE_URL, with the same plugin and permission
// mode. The upstream is a stub so the test needs no network and no real credential, and so
// the request the session sent can be inspected. Skipped by default — `go test ./...` must
// not run the real CLI.
func TestServeWrapsARealClaudeCodeSession(t *testing.T) {
	if os.Getenv("SHADE_E2E_CLAUDE") == "" {
		t.Skip("set SHADE_E2E_CLAUDE=1: the test runs a real claude session")
	}
	claude, err := exec.LookPath("claude")
	if err != nil {
		t.Skipf("claude is not on PATH: %v", err)
	}
	f := newProxyFixture(t, func(string) string { return streamWithDeltas("done") })

	// The isolated HOME keeps the session away from the real ~/.claude; SHADE_HOME is the
	// proxy's own, so the hooks and the proxy share one store. ANTHROPIC_BASE_URL is what
	// wraps the session; the dummy token is the only credential an isolated HOME can offer.
	t.Setenv("HOME", t.TempDir())
	t.Setenv("SHADE_HOME", f.home)
	t.Setenv("ANTHROPIC_BASE_URL", f.base)
	t.Setenv("ANTHROPIC_AUTH_TOKEN", "dummy")

	shade := buildShade(t)
	initCmd := exec.Command(shade, "init", "--global")
	initCmd.Dir = f.dir
	if out, err := initCmd.CombinedOutput(); err != nil {
		t.Fatalf("shade init: %v: %s", err, out)
	}

	// The session is bounded: a stalled claude must fail the test rather than block it —
	// and with it the deferred cleanup of the serve process the fixture started. WaitDelay
	// bounds the wait after the kill signal too: a grandchild of claude that inherited its
	// stdout keeps the pipe open, and Run would then wait for one that is never closed.
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	session := exec.CommandContext(ctx, claude, "-p", "Note the ssh target: "+e2eServeLiteral,
		"--plugin-dir", filepath.Join(f.home, "claude"),
		"--permission-mode", "acceptEdits",
		"--output-format", "stream-json", "--verbose")
	session.Dir = f.dir
	session.WaitDelay = 30 * time.Second
	var stdout, stderr bytes.Buffer
	session.Stdout, session.Stderr = &stdout, &stderr
	if err := session.Run(); err != nil {
		t.Fatalf("claude session: %v\nstdout: %s\nstderr: %s", err, stdout.String(), stderr.String())
	}
	sent := f.stub.received()
	if sent == "" {
		t.Fatal("the session never reached the upstream through the proxy")
	}
	if strings.Contains(sent, e2eServeValue) {
		t.Fatalf("the literal value reached the upstream: %s", sent)
	}
	if !strings.Contains(sent, "<HOST_1>") {
		t.Fatalf("the upstream did not receive the token: %s", sent)
	}
}
