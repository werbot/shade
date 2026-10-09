package main

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/werbot/shade/internal/settings"
)

// e2ePrompt drives the one path the phase's acceptance criterion is about: the model
// reads the ssh target from a file (PostToolUse tokenizes it) and then runs it
// (PreToolUse restores it). The prompt names the target as ours on purpose — a bare
// "run the command in this file" reads as a prompt injection, and the model refuses it
// as a template with unresolved placeholders.
const e2ePrompt = "ssh-config.txt contains our staging ssh login. Read it, then connect " +
	"by running that ssh command with the Bash tool. Report the output."

// e2eRawValue is the real value behind the tokens: the user@host of an ssh line, the
// shape the builtin USER/HOST rules match. Synthetic on purpose — the session runs a
// stub ssh, and a real credential has no business in a test.
const e2eRawValue = "deploy@db.prod.local"

// TestClaudeCodeEndToEndAnonymizesAndRestores is the acceptance criterion of phase 2,
// and it is skipped by default: `go test ./...` must not start a real session or reach
// the network. The spec lists it as its own level of testing (§12).
func TestClaudeCodeEndToEndAnonymizesAndRestores(t *testing.T) {
	if os.Getenv("SHADE_E2E_CLAUDE") == "" {
		t.Skip("set SHADE_E2E_CLAUDE=1: the test runs a real claude session")
	}
	claude, err := exec.LookPath("claude")
	if err != nil {
		t.Skipf("claude is not on PATH: %v", err)
	}
	shade := buildShade(t)

	// Every path the session touches is temporary. HOME keeps the run away from the
	// real ~/.claude — its PostToolUse hook rewrites tool output and would compete
	// with shade's own — and SHADE_HOME keeps it away from the real store.
	home, shadeHome, repo, stubDir := t.TempDir(), t.TempDir(), gitDir(t), t.TempDir()
	sshLog := filepath.Join(t.TempDir(), "ssh.log")
	if err := os.WriteFile(filepath.Join(repo, "ssh-config.txt"),
		[]byte("ssh "+e2eRawValue+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// The stub stands in for ssh: it records the arguments it was called with and
	// exits 0, which is what lets the test tell the restored values from the tokens.
	// It appends rather than truncates — the model may probe ssh before it runs the
	// target (a bare `ssh -V`, say), and an overwriting stub would let that probe
	// erase the evidence the test is looking for.
	const stub = "#!/bin/sh\nprintf '%s\\n' \"$@\" >> \"$SSH_LOG\"\n"
	if err := os.WriteFile(filepath.Join(stubDir, "ssh"), []byte(stub), 0o755); err != nil {
		t.Fatal(err)
	}

	// The subprocesses inherit the environment of the test, so t.Setenv is the whole
	// setup. The ambient ANTHROPIC_* variables are left alone on purpose: the isolated
	// HOME has no ~/.claude/settings.json, so claude takes its credentials from the
	// environment, and stripping them would leave the session unauthenticated.
	t.Setenv("HOME", home)
	t.Setenv("SHADE_HOME", shadeHome)
	t.Setenv("SSH_LOG", sshLog)
	t.Setenv("PATH", stubDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	// init runs as a subprocess and not in process: it writes the absolute path of
	// the running binary into the hook command, and under `go test` that binary is
	// the test binary, which has no hook subcommand — the hooks would silently do
	// nothing and the test would pass or fail for the wrong reason.
	initCmd := exec.Command(shade, "init", "--global")
	initCmd.Dir = repo
	if out, err := initCmd.CombinedOutput(); err != nil {
		t.Fatalf("shade init: %v: %s", err, out)
	}

	// --plugin-dir loads the generated plugin without the trust dialog the
	// marketplace-registration path asks for. acceptEdits keeps the auto-mode
	// classifier out of the session — it denies the ssh call as an unrequested
	// remote exec even under --allowedTools — while --allowedTools pre-approves
	// that one call so the model does not have to ask for it in print mode.
	session := exec.Command(claude,
		"-p", e2ePrompt,
		"--plugin-dir", filepath.Join(shadeHome, "claude"),
		"--permission-mode", "acceptEdits",
		"--allowedTools", "Bash(ssh:*)",
		"--output-format", "stream-json",
		"--verbose",
	)
	session.Dir = repo
	var stdout, stderr bytes.Buffer
	session.Stdout, session.Stderr = &stdout, &stderr
	if err := session.Run(); err != nil {
		// An unauthenticated session exits 1 with the error JSON on stdout and an
		// empty stderr, so this is the branch that has to name the cause: the
		// isolated HOME has no ~/.claude/settings.json, and claude then needs its
		// credentials in the environment — ANTHROPIC_BASE_URL plus
		// ANTHROPIC_AUTH_TOKEN, or ANTHROPIC_API_KEY.
		t.Fatalf("claude session: %v (is claude authenticated? the isolated HOME "+
			"has no ~/.claude/settings.json, so the credentials must come from "+
			"ANTHROPIC_BASE_URL plus ANTHROPIC_AUTH_TOKEN, or ANTHROPIC_API_KEY)\n"+
			"stdout: %s\nstderr: %s", err, stdout.String(), stderr.String())
	}
	stream := stdout.String()

	// 1. The tool ran for real: PreToolUse restored what the model wrote.
	got, err := os.ReadFile(sshLog)
	if err != nil {
		t.Fatalf("the ssh stub was never called, so the tool did not run: %v", err)
	}
	if !strings.Contains(string(got), e2eRawValue) {
		t.Fatalf("the tool was executed without the real values: %q", got)
	}
	// 2. The model worked with tokens: PostToolUse tokenized what Read returned.
	if !strings.Contains(stream, "<USER_1>") || !strings.Contains(stream, "<HOST_1>") {
		t.Fatal("the model never saw the tokens")
	}
	// 3. The model never emitted the real value as a tool argument.
	raw, tokenized := countToolInputs(stream)
	if raw != 0 {
		t.Fatalf("%d tool call(s) carried the real value instead of a token", raw)
	}
	if tokenized == 0 {
		t.Fatal("no tool call carried a token: the restore path was never exercised")
	}
}

// countToolInputs counts the tool calls whose arguments carry the real value and those
// that carry a placeholder. The tool_use block records what the model emitted — the
// token — and not the updatedInput PreToolUse returns, so this is the field the third
// assertion can be made on. Assistant *text* is not: MessageDisplay rewrites the
// displayed text back to the real values on purpose (§9), and the stored transcript
// keeps the tokens — only the display shows them.
func countToolInputs(stream string) (raw, tokenized int) {
	for _, line := range strings.Split(stream, "\n") {
		var msg struct {
			Type    string `json:"type"`
			Message struct {
				Content []struct {
					Type  string          `json:"type"`
					Input json.RawMessage `json:"input"`
				} `json:"content"`
			} `json:"message"`
		}
		if json.Unmarshal([]byte(line), &msg) != nil || msg.Type != "assistant" {
			continue
		}
		for _, c := range msg.Message.Content {
			if c.Type != "tool_use" {
				continue
			}
			args := string(c.Input)
			if strings.Contains(args, e2eRawValue) {
				raw++
			}
			if strings.Contains(args, "<USER_1>") || strings.Contains(args, "<HOST_1>") {
				tokenized++
			}
		}
	}
	return raw, tokenized
}

// TestClaudeCodeAcceptsTheGeneratedPlugin is the guard the install path lacked: it
// asks Claude Code itself to validate the manifest shade writes. The pre-fix manifest
// was rejected for a missing owner object — silently, with init still exiting 0 and
// the plugin never loading, so no hook ever ran. `claude plugin validate` exits
// non-zero on exactly that error, and needs no credentials. Skipped by default:
// `go test ./...` must not run the real CLI.
func TestClaudeCodeAcceptsTheGeneratedPlugin(t *testing.T) {
	if os.Getenv("SHADE_E2E_CLAUDE") == "" {
		t.Skip("set SHADE_E2E_CLAUDE=1: the test runs the real claude CLI")
	}
	claude, err := exec.LookPath("claude")
	if err != nil {
		t.Skipf("claude is not on PATH: %v", err)
	}
	// An isolated HOME keeps the validation away from the real ~/.claude; validate
	// needs no credentials, so a fresh home is enough.
	t.Setenv("HOME", t.TempDir())
	// The manifest is validated on its own, so the binary path inside the hooks does
	// not matter — only the shape of the three files does.
	dir := t.TempDir()
	for name, body := range settings.PluginFiles("/usr/local/bin/shade") {
		path := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, body, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	out, err := exec.Command(claude, "plugin", "validate", dir).CombinedOutput()
	if err != nil {
		t.Fatalf("claude rejected the plugin shade writes: %v\n%s", err, out)
	}
}

// TestCountToolInputs covers the counter's filters. The e2e test that exercises it is
// skipped by default, so without this a parser that always returned (0, 1) would make
// the third assertion vacuous and nothing in `go test ./...` would notice. Cases 3, 4
// and 5 each flip to 1 under exactly one removed guard, so every filter is pinned by a
// case that fails when it goes.
func TestCountToolInputs(t *testing.T) {
	cases := []struct {
		name      string
		stream    string
		raw, toks int
	}{
		{
			name:   "a tool call carrying the real value",
			stream: `{"type":"assistant","message":{"content":[{"type":"tool_use","input":{"command":"ssh ` + e2eRawValue + `"}}]}}`,
			raw:    1,
		},
		{
			name:   "a tool call carrying the token",
			stream: `{"type":"assistant","message":{"content":[{"type":"tool_use","input":{"command":"ssh <USER_1>@<HOST_1>"}}]}}`,
			toks:   1,
		},
		{
			// A tool call in a message that is not an assistant one: only the
			// message-type guard keeps it out of the count.
			name:   "a tool call in a message that is not an assistant one",
			stream: `{"type":"user","message":{"content":[{"type":"tool_use","input":{"command":"ssh ` + e2eRawValue + `"}}]}}`,
		},
		{
			// A block that is not a tool call but does carry an input: only the
			// block-type guard keeps it out of the count.
			name:   "a content block that is not a tool call, carrying an input",
			stream: `{"type":"assistant","message":{"content":[{"type":"text","input":{"command":"ssh ` + e2eRawValue + `"}}]}}`,
		},
		{
			// Valid JSON whose content array fails to decode on its second element:
			// the first is already in the slice when the error comes back, so only
			// the unmarshal guard keeps it out of the count.
			name:   "a line that fails to unmarshal after a tool call was decoded",
			stream: `{"type":"assistant","message":{"content":[{"type":"tool_use","input":{"command":"ssh ` + e2eRawValue + `"}},{"type":123}]}}`,
		},
		{
			// A junk line must be skipped and not end the walk: the tool call that
			// counts comes after it, and the blocks in between are ignored. This one
			// pins no single guard — it fails only against a parser that stops on a
			// line it cannot read.
			name: "junk first, then ignored blocks, then a tool call",
			stream: `not json at all
{"type":"assistant","message":{"content":[{"type":"text","text":"ssh ` + e2eRawValue + `"}]}}
{"type":"user","message":{"content":[{"type":"tool_result","content":"ssh ` + e2eRawValue + `"}]}}
{"type":"assistant","message":{"content":[{"type":"tool_use","input":{"command":"ssh <USER_1>@<HOST_1>"}}]}}`,
			toks: 1,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			raw, toks := countToolInputs(tc.stream)
			if raw != tc.raw || toks != tc.toks {
				t.Errorf("countToolInputs = (%d, %d), want (%d, %d)", raw, toks, tc.raw, tc.toks)
			}
		})
	}
}
