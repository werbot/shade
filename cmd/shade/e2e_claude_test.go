package main

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
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
		t.Fatalf("claude session: %v\nstderr: %s", err, stderr.String())
	}
	stream := stdout.String()
	if strings.Contains(stream, `"authentication_failed"`) {
		t.Fatalf("the session is not authenticated: the isolated HOME has no "+
			"~/.claude/settings.json, so claude needs its credentials in the "+
			"environment. stderr: %s", stderr.String())
	}

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
		t.Fatalf("the model never saw the tokens")
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
// displayed text back to the real values on purpose (§9), and the stored message keeps
// the tokens.
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
