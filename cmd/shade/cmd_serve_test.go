package main

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"net"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

// serveProc is a running `shade serve` subprocess with its captured streams. Wait is guarded
// by a Once so a test that already waited does not race its own cleanup.
type serveProc struct {
	cmd  *exec.Cmd
	out  *lockedBuffer
	err  *lockedBuffer
	once sync.Once
}

// startServe launches the built CLI as `shade serve --project dir --port 0`. Port 0 lets the
// OS choose a free port, so a run never collides with another. The process is stopped and
// reaped on cleanup, with a bounded wait.
func startServe(t *testing.T, bin, home, dir string) *serveProc {
	t.Helper()
	p := &serveProc{out: &lockedBuffer{}, err: &lockedBuffer{}}
	p.cmd = exec.Command(bin, "serve", "--project", dir, "--port", "0")
	p.cmd.Dir = dir
	p.cmd.Env = mcpEnv(home)
	p.cmd.Stdout = p.out
	p.cmd.Stderr = p.err
	if err := p.cmd.Start(); err != nil {
		t.Fatalf("starting serve: %v", err)
	}
	t.Cleanup(func() {
		if p.cmd.Process != nil {
			_ = p.cmd.Process.Signal(syscall.SIGTERM)
		}
		p.wait(t, 10*time.Second)
	})
	return p
}

// wait blocks until the process leaves, killing it if it outlasts the deadline: a stuck server
// must fail the test rather than hang it.
func (p *serveProc) wait(t *testing.T, d time.Duration) {
	t.Helper()
	p.once.Do(func() {
		done := make(chan error, 1)
		go func() { done <- p.cmd.Wait() }()
		select {
		case <-done:
		case <-time.After(d):
			_ = p.cmd.Process.Kill()
			<-done
		}
	})
}

// waitForLine waits, bounded, for the first complete line on stdout.
func waitForLine(t *testing.T, b *lockedBuffer, d time.Duration) string {
	t.Helper()
	deadline := time.Now().Add(d)
	for {
		if s := b.String(); strings.Contains(s, "\n") {
			return strings.TrimRight(strings.SplitN(s, "\n", 2)[0], "\r")
		}
		if time.Now().After(deadline) {
			t.Fatalf("no line on stdout within %s", d)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// TestServePrintsTheWrapLine — the only line stdout carries is the export the user copies, and
// while the server runs its marker covers the project. A real process is needed: the line and
// the marker come from the running server, not from a parsed argument.
func TestServePrintsTheWrapLine(t *testing.T) {
	bin := buildShade(t)
	home, dir := t.TempDir(), gitDir(t)
	p := startServe(t, bin, home, dir)

	line := waitForLine(t, p.out, 10*time.Second)
	const prefix = "export ANTHROPIC_BASE_URL=http://127.0.0.1:"
	if !strings.HasPrefix(line, prefix) {
		t.Fatalf("wrap line = %q, want prefix %q (stderr: %q)", line, prefix, p.err.String())
	}
	if port, err := strconv.Atoi(strings.TrimPrefix(line, prefix)); err != nil || port <= 0 {
		t.Fatalf("wrap line carries no usable port: %q", line)
	}
	if got := p.err.String(); got != "" {
		t.Fatalf("stderr must stay empty, got %q", got)
	}
	proj, st := testStore(t, home, dir)
	if ok, err := st.ProxyCovers(context.Background(), proj.RootPath); err != nil || !ok {
		t.Fatalf("a running serve must cover its project: ok=%v err=%v", ok, err)
	}
}

// TestServeClearsItsMarkerOnSignals — on SIGINT or SIGTERM the marker row is gone, so the gate
// is not left silenced by a proxy that has stopped. The meta row is read directly: ProxyCovers
// would answer false even on a leftover marker, because the pid is dead by then, and that
// would hide a marker the process failed to clear.
func TestServeClearsItsMarkerOnSignals(t *testing.T) {
	bin := buildShade(t)
	for _, sig := range []syscall.Signal{syscall.SIGINT, syscall.SIGTERM} {
		t.Run(sig.String(), func(t *testing.T) {
			home, dir := t.TempDir(), gitDir(t)
			p := startServe(t, bin, home, dir)
			waitForLine(t, p.out, 10*time.Second)

			proj, st := testStore(t, home, dir)
			if ok, err := st.ProxyCovers(context.Background(), proj.RootPath); err != nil || !ok {
				t.Fatalf("serve must write its marker while running: ok=%v err=%v", ok, err)
			}

			if err := p.cmd.Process.Signal(sig); err != nil {
				t.Fatalf("signaling: %v", err)
			}
			p.wait(t, 10*time.Second)

			var value string
			err := st.DB().QueryRow(`SELECT value FROM meta WHERE key='proxy'`).Scan(&value)
			if !errors.Is(err, sql.ErrNoRows) {
				t.Fatalf("the marker survived the signal: value=%q err=%v", value, err)
			}
		})
	}
}

// TestServeRejectsAnOccupiedPort — an occupied port is exit 1, before any marker is written: a
// port that never opened a listener must not leave a marker claiming otherwise.
func TestServeRejectsAnOccupiedPort(t *testing.T) {
	bin := buildShade(t)
	home, dir := t.TempDir(), gitDir(t)

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	port := ln.Addr().(*net.TCPAddr).Port

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, "serve", "--project", dir, "--port", strconv.Itoa(port))
	cmd.Dir = dir
	cmd.Env = mcpEnv(home)
	var out, errOut bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errOut
	err = cmd.Run()
	if ctx.Err() != nil {
		t.Fatalf("serve hung on an occupied port (stderr: %q)", errOut.String())
	}
	var ee *exec.ExitError
	if !errors.As(err, &ee) || ee.ExitCode() != 1 {
		t.Fatalf("err=%v, want exit 1 (stderr: %q)", err, errOut.String())
	}
	if out.Len() != 0 {
		t.Fatalf("stdout must stay empty, got %q", out.String())
	}
	_, st := testStore(t, home, dir)
	var value string
	err = st.DB().QueryRow(`SELECT value FROM meta WHERE key='proxy'`).Scan(&value)
	if !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("an occupied port left a marker: value=%q err=%v", value, err)
	}
}

// An unknown flag is a call error: exit 2, before a listener is bound.
func TestServeRejectsUnknownFlag(t *testing.T) {
	code, out, errOut := runCLI(t, t.TempDir(), t.TempDir(), []string{"serve", "--nope"}, "")
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
// directory: parseInputArgs answers the same input with exit 2 for every other command.
func TestServeRejectsAnEmptyProject(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
	}{
		{"joined", []string{"serve", "--project="}},
		{"separate", []string{"serve", "--project", ""}},
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
