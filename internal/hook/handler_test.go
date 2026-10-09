package hook_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/werbot/shade/internal/core"
	"github.com/werbot/shade/internal/crypt"
	"github.com/werbot/shade/internal/directive"
	"github.com/werbot/shade/internal/hook"
	"github.com/werbot/shade/internal/placeholder"
	"github.com/werbot/shade/internal/rules"
	"github.com/werbot/shade/internal/store"
)

// ctx is shared by the tests of the package: Handle keeps no state between calls,
// there is nothing to cancel.
var ctx = context.Background()

// gitDir — a project: t.TempDir() with an initialized git repository. The path is
// returned with symlinks resolved, exactly as ProjectForPath stores the root
// (/private/var/... on macOS, not /var/...).
func gitDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if out, err := exec.Command("git", "init", "-q", dir).CombinedOutput(); err != nil {
		t.Fatalf("git init %s: %v: %s", dir, err, out)
	}
	resolved, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatal(err)
	}
	return resolved
}

// newHandler assembles a handler whose Opener is the real engine on the home
// directory of the test. SHADE_HOME is set as well: a test that accidentally reaches
// store.Home() must land in the temporary home, not in the developer's ~/.shade.
func newHandler(t *testing.T, home string) hook.Handler {
	t.Helper()
	t.Setenv("SHADE_HOME", home)
	return hook.Handler{
		Home: home,
		Open: func(ctx context.Context, dir string) (hook.Engine, error) {
			return core.New(ctx, home, dir, "hook")
		},
	}
}

// openStore opens a second, test-owned store on the same home: the journal is read
// after the handler has already closed the engine it opened.
func openStore(t *testing.T, home string) *store.Store {
	t.Helper()
	key, err := crypt.LoadOrCreateKey(home)
	if err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(home, key)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	return st
}

// projectIDIn returns the id of the project the handler registered for the root.
func projectIDIn(t *testing.T, st *store.Store, root string) int64 {
	t.Helper()
	var id int64
	if err := st.DB().QueryRowContext(ctx,
		`SELECT id FROM projects WHERE root_path=?`, root).Scan(&id); err != nil {
		t.Fatalf("project of %s: %v", root, err)
	}
	return id
}

// writeConfig puts a config file into dir: the project layer of config.Load is
// <root>/.shade.toml, the global one is <home>/config.toml.
func writeConfig(t *testing.T, dir, name, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestUserPromptSubmitGateBlocksAndCountsTypesOnce(t *testing.T) {
	home, repo := t.TempDir(), gitDir(t)
	writeConfig(t, repo, ".shade.toml", "prompt_gate = \"on\"\n")
	h := newHandler(t, home)
	// Two ssh targets: four spans, two distinct types. One journal row per type,
	// not per span, is the property under test.
	const prompt = "ssh deploy@db.prod.local && ssh root@db2.prod.local"
	res, err := h.Handle(ctx, hook.Event{
		Name: hook.EventUserPromptSubmit, CWD: repo, Prompt: prompt,
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Decision != "block" || !strings.Contains(res.Reason, "HOST") || !strings.Contains(res.Reason, "USER") {
		t.Fatalf("gate: %+v", res)
	}
	if strings.Contains(res.Reason, "db.prod.local") || strings.Contains(res.Reason, "deploy") {
		t.Fatalf("the reason must carry types, never values: %q", res.Reason)
	}
	// The engine the handler opened is closed by now — the journal is read
	// through a second, test-owned store on the same home.
	st := openStore(t, home)
	pid := projectIDIn(t, st, repo)
	rows, err := st.Audit(ctx, pid, 100, store.AuditFilter{})
	if err != nil || len(rows) != 2 || rows[0].Action != "blocked" || rows[0].Direction != "to_model" {
		t.Fatalf("audit: %+v err=%v", rows, err)
	}
	perType := map[string]int{}
	for _, r := range rows {
		perType[r.Type]++
		if r.Adapter != "hook" || r.Rule != "" || r.Detail != "" {
			t.Fatalf("a blocked row carries more than the type: %+v", r)
		}
	}
	if perType["HOST"] != 1 || perType["USER"] != 1 {
		t.Fatalf("one row per distinct type expected, got %+v", rows)
	}
}

// The gate is off by default: without a config the prompt goes through untouched,
// and nothing is written to the journal.
func TestUserPromptSubmitGateOffIsSilent(t *testing.T) {
	home, repo := t.TempDir(), gitDir(t)
	h := newHandler(t, home)
	res, err := h.Handle(ctx, hook.Event{
		Name: hook.EventUserPromptSubmit, CWD: repo, Prompt: "ssh deploy@db.prod.local",
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Decision != "" || res.Reason != "" {
		t.Fatalf("the gate must be silent when it is off: %+v", res)
	}
	st := openStore(t, home)
	pid := projectIDIn(t, st, repo)
	rows, err := st.Audit(ctx, pid, 100, store.AuditFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 0 {
		t.Fatalf("a prompt the gate never saw must not reach the journal: %+v", rows)
	}
}

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

func TestHandlerFailsOpenOnEngineError(t *testing.T) {
	e := &fakeEngine{restoreErr: errors.New("store is gone")}
	h := newFakeHandler(t, e)
	res, err := h.Handle(ctx, hook.Event{
		Name: hook.EventPreToolUse, CWD: "/repo", ToolName: "Bash",
		ToolInput: json.RawMessage(`{"command":"<HOST_1>"}`),
	})
	if err != nil {
		t.Fatalf("a runtime failure must not come back as an error: %v", err)
	}
	if res.HookSpecificOutput != nil {
		t.Fatalf("a failed hook keeps Claude Code's default behaviour: %+v", res)
	}
	if !strings.Contains(res.SystemMessage, "shade:") || !strings.Contains(res.SystemMessage, "store is gone") {
		t.Fatalf("the failure must be visible: %+v", res)
	}
	if !e.closed {
		t.Fatal("the engine must be closed on the fail-open path too")
	}

	// The engine cannot be opened at all — the same answer, not an error.
	h = hook.Handler{
		Home: t.TempDir(),
		Open: func(context.Context, string) (hook.Engine, error) { return nil, errors.New("cannot open") },
	}
	res, err = h.Handle(ctx, hook.Event{Name: hook.EventSessionStart, CWD: "/repo"})
	if err != nil {
		t.Fatalf("a failed open must not come back as an error: %v", err)
	}
	if res.HookSpecificOutput != nil || !strings.Contains(res.SystemMessage, "cannot open") {
		t.Fatalf("got %+v", res)
	}
}

func TestSessionStartReturnsDirective(t *testing.T) {
	home, repo := t.TempDir(), gitDir(t)
	h := newHandler(t, home)
	res, err := h.Handle(ctx, hook.Event{Name: hook.EventSessionStart, CWD: repo, Source: "startup"})
	if err != nil {
		t.Fatal(err)
	}
	if res.HookSpecificOutput == nil || res.HookSpecificOutput.AdditionalContext != directive.Text() {
		t.Fatalf("got %+v", res)
	}
	// Opening the engine is what registers the project: without it the rest of the
	// session would issue placeholders for a project that is not in the database.
	st := openStore(t, home)
	projectIDIn(t, st, repo)
}

// A frame cut inside an open token is left alone: restoring it would show a value
// assembled from half a token and strand the closing bracket in the next frame.
func TestMessageDisplayLeavesATruncatedTokenAlone(t *testing.T) {
	e := &fakeEngine{restoreFn: func(s string) (core.Result, error) {
		// The real finder restores both the canonical form and the one truncated at
		// max_tokens, which has no closing bracket.
		s = strings.ReplaceAll(s, "<HOST_1>", "db.prod.local")
		return core.Result{Text: strings.ReplaceAll(s, "<HOST_1", "db.prod.local")}, nil
	}}
	h := newFakeHandler(t, e)

	res, err := h.Handle(ctx, hook.Event{Name: hook.EventMessageDisplay, Delta: "<HOST_1"})
	if err != nil {
		t.Fatal(err)
	}
	if res.HookSpecificOutput != nil {
		t.Fatalf("a frame cut inside a token must not be restored: %+v", res)
	}

	// The final frame carries the rest of the message, and a truncated token in it is
	// exactly what the tolerant finder is built to restore.
	res, err = h.Handle(ctx, hook.Event{Name: hook.EventMessageDisplay, Delta: "<HOST_1", Final: true})
	if err != nil {
		t.Fatal(err)
	}
	if res.HookSpecificOutput == nil || res.HookSpecificOutput.DisplayContent != "db.prod.local" {
		t.Fatalf("the final frame must be restored: %+v", res)
	}

	// A closed token is restored in any frame, and a frame without tokens stays silent.
	res, err = h.Handle(ctx, hook.Event{Name: hook.EventMessageDisplay, Delta: "host is <HOST_1>\n"})
	if err != nil {
		t.Fatal(err)
	}
	if res.HookSpecificOutput == nil || res.HookSpecificOutput.DisplayContent != "host is db.prod.local\n" {
		t.Fatalf("got %+v", res)
	}
	res, err = h.Handle(ctx, hook.Event{Name: hook.EventMessageDisplay, Delta: "no tokens here\n"})
	if err != nil {
		t.Fatal(err)
	}
	if res.HookSpecificOutput != nil {
		t.Fatalf("nothing to show must stay silent: %+v", res)
	}
}

// An event the adapter does not know is not an error: Claude Code grows new events,
// and a hook that failed on one of them would break the session.
func TestUnknownEventIsSilent(t *testing.T) {
	h := newFakeHandler(t, &fakeEngine{})
	res, err := h.Handle(ctx, hook.Event{Name: "PreCompact", CWD: "/repo"})
	if err != nil {
		t.Fatal(err)
	}
	if res.HookSpecificOutput != nil || res.Decision != "" || res.SystemMessage != "" {
		t.Fatalf("got %+v", res)
	}
}

// A misconfigured handler is the one failure that is an error rather than a warning:
// there is no way to answer any event without an opener.
func TestHandlerWithoutOpenerIsAnError(t *testing.T) {
	if _, err := (hook.Handler{}).Handle(ctx, hook.Event{Name: hook.EventSessionStart}); err == nil {
		t.Fatal("a handler without an opener must fail loudly")
	}
}

// TestParallelHandlersDoNotLockTheDatabase — two tools of one session run in parallel,
// and the hook is a short-lived process per event: several engines on one home at once
// is the normal case, not a hypothesis. Every goroutine both writes (the unresolved
// trace and the entities of its own value) and reads.
func TestParallelHandlersDoNotLockTheDatabase(t *testing.T) {
	home, repo := t.TempDir(), gitDir(t)
	h := newHandler(t, home)
	const n = 8

	values := make([]string, n)
	for i := range n {
		values[i] = fmt.Sprintf("ssh deploy@host%d.prod.local", i)
	}
	docs := make([]string, n)
	errs := make([]error, n)
	var wg sync.WaitGroup
	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			res, err := h.Handle(ctx, hook.Event{
				Name: hook.EventPreToolUse, CWD: repo, ToolName: "Bash",
				ToolInput: json.RawMessage(fmt.Sprintf(`{"command":"ssh <HOST_%d>"}`, 900+i)),
			})
			if err != nil {
				errs[i] = err
				return
			}
			if res.SystemMessage != "" {
				errs[i] = errors.New(res.SystemMessage)
				return
			}
			res, err = h.Handle(ctx, hook.Event{
				Name: hook.EventPostToolUse, CWD: repo, ToolName: "Bash",
				ToolResponse: json.RawMessage(fmt.Sprintf(`{"stdout":%q}`, values[i])),
			})
			if err != nil {
				errs[i] = err
				return
			}
			if res.SystemMessage != "" {
				errs[i] = errors.New(res.SystemMessage)
				return
			}
			if res.HookSpecificOutput == nil {
				errs[i] = errors.New("the tool response was not anonymized")
				return
			}
			docs[i] = string(res.HookSpecificOutput.UpdatedToolOutput)
		}()
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Fatalf("goroutine %d: %v", i, err)
		}
	}

	// Every value must be in entities: only a store row can give it back, and the
	// restored text is compared as a whole.
	e, err := core.New(ctx, home, repo, "hook")
	if err != nil {
		t.Fatal(err)
	}
	defer e.Close()
	for i, doc := range docs {
		var out struct {
			Stdout string `json:"stdout"`
		}
		if err := json.Unmarshal([]byte(doc), &out); err != nil {
			t.Fatalf("goroutine %d: %v", i, err)
		}
		back, err := e.Restore(ctx, out.Stdout)
		if err != nil {
			t.Fatalf("goroutine %d: %v", i, err)
		}
		if back.Text != values[i] || len(back.Unresolved) != 0 {
			t.Fatalf("goroutine %d: %q restored to %q", i, out.Stdout, back.Text)
		}
	}
}

// fakeEngine is the Engine stand-in: it hands back canned restore results and
// remembers whether it was closed. Only the tests that do not need the journal use it —
// the gate, SessionStart and PostToolUse tests run through the real engine, because
// those responses are produced by core, not by the event walker.
type fakeEngine struct {
	root       string
	restoreFn  func(string) (core.Result, error)
	restoreErr error
	closed     bool
}

func (f *fakeEngine) Anonymize(_ context.Context, text string) (core.Result, error) {
	return core.Result{Text: text}, nil
}

func (f *fakeEngine) Restore(_ context.Context, text string) (core.Result, error) {
	if f.restoreErr != nil {
		return core.Result{}, f.restoreErr
	}
	if f.restoreFn == nil {
		return core.Result{Text: text}, nil
	}
	return f.restoreFn(text)
}

func (f *fakeEngine) Scan(text, _ string) (string, []rules.Span, error) { return text, nil, nil }

func (f *fakeEngine) RootPath() string { return f.root }

func (f *fakeEngine) RecordBlocked(context.Context, string) error { return nil }

func (f *fakeEngine) Close() error {
	f.closed = true
	return nil
}

// newFakeHandler serves the fake engine on a home directory with no config: the
// handler then reads the defaults of config.Default().
func newFakeHandler(t *testing.T, e *fakeEngine) hook.Handler {
	t.Helper()
	if e.root == "" {
		e.root = t.TempDir()
	}
	return hook.Handler{
		Home: t.TempDir(),
		Open: func(context.Context, string) (hook.Engine, error) { return e, nil },
	}
}
