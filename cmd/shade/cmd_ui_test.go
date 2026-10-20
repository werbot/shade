package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"testing/fstest"
	"time"
)

// uiRule is a valid rule definition: the body the tests post to a running ui.
const uiRule = `{"name":"ui_probe","type":"TICKET","pattern":"PROBE-\\d+"}`

// needsPage skips a test that runs the built binary as `shade ui`. The binary embeds whatever
// web/dist holds, and on a checkout where `make web` never ran that is .gitkeep only, which ui
// refuses to serve.
func needsPage(t *testing.T) {
	t.Helper()
	if _, err := os.Stat("../../web/dist/index.html"); err != nil {
		t.Skip("run make web")
	}
}

// startUI launches `shade ui` on a port the OS chooses and returns the process with the address
// it printed.
func startUI(t *testing.T, home, dir string) (*serveProc, string) {
	t.Helper()
	needsPage(t)
	p := startMode(t, buildShade(t), "ui", home, dir)
	return p, waitForLine(t, p.out, 10*time.Second)
}

// uiRequest builds a request to a running ui. A body is sent as JSON, the one content type the
// API accepts.
func uiRequest(t *testing.T, method, url, body string) *http.Request {
	t.Helper()
	req, err := http.NewRequest(method, url, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	return req
}

// send sends req and returns the status and the body of the answer. proxyClient bounds it.
func send(t *testing.T, req *http.Request) (int, string) {
	t.Helper()
	resp, err := proxyClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return resp.StatusCode, string(body)
}

// TestPageBuiltRefusesAnEmptyFS — a checkout that was never built embeds .gitkeep only, and ui
// must not start on it: the error names the command that builds the page. The check runs over an
// fs.FS, so it needs neither a process nor an earlier `make web`.
func TestPageBuiltRefusesAnEmptyFS(t *testing.T) {
	for _, tc := range []struct {
		name   string
		files  fstest.MapFS
		errSub string // empty — the page is built
	}{
		{"empty", fstest.MapFS{}, "make build"},
		{"gitkeep only", fstest.MapFS{".gitkeep": {}}, "make build"},
		{"built", fstest.MapFS{".gitkeep": {}, "index.html": {}}, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := pageBuilt(tc.files)
			if tc.errSub == "" {
				if err != nil {
					t.Fatalf("pageBuilt = %v, want nil", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.errSub) {
				t.Fatalf("pageBuilt = %v, want an error containing %q", err, tc.errSub)
			}
		})
	}
}

// TestUIRejectsBadArguments — a bad call is exit 2 with the culprit named, before the page is
// looked at and before a port is bound. The name is what tells this exit 2 from the one an
// unregistered command gets.
func TestUIRejectsBadArguments(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
		want string
	}{
		{"unknown flag", []string{"ui", "--nope"}, "--nope"},
		{"port out of range", []string{"ui", "--port", "70000"}, "--port"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			code, out, errOut := runCLI(t, t.TempDir(), t.TempDir(), tc.args, "")
			if code != 2 {
				t.Fatalf("code=%d, want 2 (%s)", code, errOut)
			}
			if !strings.Contains(errOut, tc.want) {
				t.Fatalf("stderr must name %q: %q", tc.want, errOut)
			}
			if out != "" {
				t.Fatalf("stdout must stay empty, got %q", out)
			}
		})
	}
}

// TestUIPrintsItsAddress — the only line stdout carries is the address to open, with the port the
// OS really gave (--port 0), and the page answers on it. A real process is needed: the line and
// the Host check both depend on the bound port.
func TestUIPrintsItsAddress(t *testing.T) {
	p, addr := startUI(t, t.TempDir(), gitDir(t))

	if !regexp.MustCompile(`^http://127\.0\.0\.1:\d+$`).MatchString(addr) {
		t.Fatalf("stdout line = %q, want http://127.0.0.1:<port> (stderr: %q)", addr, p.err.String())
	}
	if got := p.err.String(); got != "" {
		t.Fatalf("stderr must stay empty, got %q", got)
	}
	if code, body := send(t, uiRequest(t, http.MethodGet, addr, "")); code != http.StatusOK || !strings.Contains(body, `id="app"`) {
		t.Fatalf("GET %s = %d %q, want 200 and the page", addr, code, body)
	}
}

// TestUICreatesARuleTheCLISees — a rule posted over HTTP lands in the store the CLI reads: ui and
// the hook, the proxy and the CLI share one database, and ui opens its project like they do.
func TestUICreatesARuleTheCLISees(t *testing.T) {
	home, dir := t.TempDir(), gitDir(t)
	_, addr := startUI(t, home, dir)

	if code, body := send(t, uiRequest(t, http.MethodPost, addr+"/api/rules", uiRule)); code != http.StatusCreated {
		t.Fatalf("POST /api/rules = %d %q, want 201", code, body)
	}
	code, out, errOut := runCLI(t, home, dir, []string{"rules", "list"}, "")
	if code != 0 || !strings.Contains(out, "ui_probe") {
		t.Fatalf("rules list: code=%d out=%q err=%q, want the rule created over HTTP", code, out, errOut)
	}
}

// TestUIRefusesAForeignHostAndOrigin — a page of another site reaches the loopback port under its
// own name (DNS rebinding) or as a cross-origin form; the guard has to be wired in front of both
// the API and the page, with the port the listener bound.
func TestUIRefusesAForeignHostAndOrigin(t *testing.T) {
	_, addr := startUI(t, t.TempDir(), gitDir(t))

	host := uiRequest(t, http.MethodGet, addr+"/api/rules", "")
	host.Host = "evil.example"
	if code, body := send(t, host); code != http.StatusForbidden {
		t.Fatalf("a foreign Host got %d %q, want 403", code, body)
	}

	// The request is the valid create of the test above, so the Origin is the only thing to refuse.
	origin := uiRequest(t, http.MethodPost, addr+"/api/rules", uiRule)
	origin.Header.Set("Origin", "http://evil.example")
	if code, body := send(t, origin); code != http.StatusForbidden {
		t.Fatalf("a foreign Origin got %d %q, want 403", code, body)
	}
}

// TestUIStopsOnSIGTERM — the stop is clean: exit 0, not the 1 of an error or the -1 of a process
// the test had to kill. The signal goes out after the address line, which comes after the
// handler is installed.
func TestUIStopsOnSIGTERM(t *testing.T) {
	p, _ := startUI(t, t.TempDir(), gitDir(t))

	if err := p.cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatalf("signaling: %v", err)
	}
	p.wait(t, 10*time.Second)
	if code := p.cmd.ProcessState.ExitCode(); code != 0 {
		t.Fatalf("exit code = %d, want 0 (stderr: %q)", code, p.err.String())
	}
}

// TestUIRejectsAnOccupiedPort — an occupied port is exit 1 and no address on stdout: the line
// says the page is reachable, and here it is not.
func TestUIRejectsAnOccupiedPort(t *testing.T) {
	needsPage(t)
	bin, home, dir := buildShade(t), t.TempDir(), gitDir(t)

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()

	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, "ui", "--project", dir, "--port", strconv.Itoa(portOf(ln)))
	cmd.Dir = dir
	cmd.Env = mcpEnv(home)
	var out, errOut bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errOut
	err = cmd.Run()
	if ctx.Err() != nil {
		t.Fatalf("ui hung on an occupied port (stderr: %q)", errOut.String())
	}
	var ee *exec.ExitError
	if !errors.As(err, &ee) || ee.ExitCode() != 1 {
		t.Fatalf("err=%v, want exit 1 (stderr: %q)", err, errOut.String())
	}
	if out.Len() != 0 {
		t.Fatalf("stdout must stay empty, got %q", out.String())
	}
}
