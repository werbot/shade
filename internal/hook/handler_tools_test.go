package hook_test

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/werbot/shade/internal/core"
	"github.com/werbot/shade/internal/hook"
	"github.com/werbot/shade/internal/placeholder"
)

func TestPreToolUseDeniesUnresolvedUnderFailClosed(t *testing.T) {
	e := &fakeEngine{restoreFn: func(s string) (core.Result, error) {
		return core.Result{Text: s, Unresolved: []placeholder.Token{{Type: "HOST", Raw: "<HOST_9>"}}}, nil
	}}
	h := newFakeHandler(t, e)
	res, err := h.Handle(ctx, hook.Event{
		Name: hook.EventPreToolUse, CWD: "/repo", ToolName: "Bash",
		ToolInput: json.RawMessage(`{"command":"ssh <HOST_9>"}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	got := res.HookSpecificOutput
	if got == nil || got.HookEventName != hook.EventPreToolUse || got.PermissionDecision != "deny" {
		t.Fatalf("want a deny, got %+v", res)
	}
	if got.UpdatedInput != nil {
		t.Fatalf("a denied call must not carry updatedInput: %s", got.UpdatedInput)
	}
	if !strings.Contains(got.PermissionDecisionReason, "<HOST_9>") {
		t.Fatalf("the deny must name the token that has no value: %q", got.PermissionDecisionReason)
	}
	if !e.closed {
		t.Fatal("the engine must be closed")
	}
}

// fail_open_log hands the argument over with the values it could restore and names
// the tokens it could not: the policy comes from the config, not from the handler.
func TestPreToolUseRestoresWhatItCanUnderFailOpenLog(t *testing.T) {
	e := &fakeEngine{restoreFn: func(s string) (core.Result, error) {
		return core.Result{
			Text:       strings.ReplaceAll(s, "<USER_1>", "deploy"),
			Unresolved: []placeholder.Token{{Type: "HOST", N: 9, Raw: "<HOST_9>"}},
		}, nil
	}}
	h := newFakeHandler(t, e)
	writeConfig(t, h.Home, "config.toml", "fail_policy = \"fail_open_log\"\n")
	res, err := h.Handle(ctx, hook.Event{
		Name: hook.EventPreToolUse, CWD: "/repo", ToolName: "Bash",
		ToolInput: json.RawMessage(`{"command":"ssh <USER_1>@<HOST_9>"}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	got := res.HookSpecificOutput
	if got == nil || got.PermissionDecision != "" {
		t.Fatalf("fail_open_log must not deny: %+v", res)
	}
	if !strings.Contains(string(got.UpdatedInput), "deploy") {
		t.Fatalf("the value it could restore was not put back: %s", got.UpdatedInput)
	}
	if !strings.Contains(res.SystemMessage, "<HOST_9>") {
		t.Fatalf("the token left without a value must be named: %q", res.SystemMessage)
	}
}

func TestPreToolUseRestoresAndSkipsUnchangedInput(t *testing.T) {
	e := &fakeEngine{restoreFn: func(s string) (core.Result, error) {
		return core.Result{Text: strings.ReplaceAll(s, "<HOST_1>", "db.prod.local")}, nil
	}}
	h := newFakeHandler(t, e)

	// A changed argument comes back as the whole object.
	res, err := h.Handle(ctx, hook.Event{
		Name: hook.EventPreToolUse, CWD: "/repo", ToolName: "Bash",
		ToolInput: json.RawMessage(`{"command":"ssh <HOST_1>","timeout":1.0}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.HookSpecificOutput == nil || !strings.Contains(string(res.HookSpecificOutput.UpdatedInput), "db.prod.local") {
		t.Fatalf("the argument was not restored: %+v", res)
	}
	// The shape of the document is preserved: the number is still a number.
	if !strings.Contains(string(res.HookSpecificOutput.UpdatedInput), "1.0") {
		t.Fatalf("the object was re-encoded: %s", res.HookSpecificOutput.UpdatedInput)
	}

	for _, name := range []string{"WebFetch", "WebSearch", "Write", "ToolSearch", "ExitPlanMode", "AskUserQuestion"} {
		res, err := h.Handle(ctx, hook.Event{
			Name: hook.EventPreToolUse, CWD: "/repo", ToolName: name,
			ToolInput: json.RawMessage(`{"x":"<HOST_1>"}`),
		})
		if err != nil {
			t.Fatal(err)
		}
		if res.HookSpecificOutput != nil {
			t.Fatalf("%s is on the skip list: %+v", name, res)
		}
	}

	// Nothing to restore — nothing to print: an unchanged object would be a rewritten
	// object competing with the neighbouring rewrites.
	res, err = h.Handle(ctx, hook.Event{
		Name: hook.EventPreToolUse, CWD: "/repo", ToolName: "Bash",
		ToolInput: json.RawMessage(`{"command":"ls -la","timeout":1.0}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.HookSpecificOutput != nil {
		t.Fatalf("an unchanged object must stay silent: %+v", res)
	}
}

func TestPostToolUseAnonymizesToolResponse(t *testing.T) {
	home, repo := t.TempDir(), gitDir(t)
	h := newHandler(t, home)
	res, err := h.Handle(ctx, hook.Event{
		Name: hook.EventPostToolUse, CWD: repo, ToolName: "Bash",
		ToolResponse: json.RawMessage(`{"stdout":"ssh deploy@db.prod.local","exit":0}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	got := res.HookSpecificOutput
	if got == nil || got.HookEventName != hook.EventPostToolUse {
		t.Fatalf("got %+v", res)
	}
	var out struct {
		Stdout string `json:"stdout"`
		Exit   int    `json:"exit"`
	}
	if err := json.Unmarshal(got.UpdatedToolOutput, &out); err != nil {
		t.Fatal(err)
	}
	if out.Exit != 0 {
		t.Fatalf("the shape of the response changed: %s", got.UpdatedToolOutput)
	}
	types := map[string]bool{}
	for _, tok := range placeholder.FindNormalized(out.Stdout) {
		types[tok.Type] = true
	}
	if !types["HOST"] || !types["USER"] {
		t.Fatalf("the response was not anonymized: %q", out.Stdout)
	}

	// An unchanged response is not printed: an updatedToolOutput that repeats the
	// original competes with the neighbouring rewrites, and PostToolUse hooks run in
	// parallel — last write wins.
	res, err = h.Handle(ctx, hook.Event{
		Name: hook.EventPostToolUse, CWD: repo, ToolName: "Bash",
		ToolResponse: json.RawMessage(`{"stdout":"total 0","exit":0}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.HookSpecificOutput != nil {
		t.Fatalf("an unchanged response must stay silent: %+v", res)
	}
}

// The tool output is the only channel that carries real values, and it is the tool's,
// not the model's: no other field of the answer may repeat them.
func TestPostToolUseOutputCarriesNoRawValue(t *testing.T) {
	home, repo := t.TempDir(), gitDir(t)
	h := newHandler(t, home)
	res, err := h.Handle(ctx, hook.Event{
		Name: hook.EventPostToolUse, CWD: repo, ToolName: "Bash",
		ToolResponse: json.RawMessage(`{"stdout":"ssh deploy@db.prod.local"}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	out, err := res.Bytes()
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(out, []byte("db.prod.local")) || bytes.Contains(out, []byte("deploy")) {
		t.Fatalf("a real value left the hook: %s", out)
	}
}
