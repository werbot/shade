package hook_test

import (
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"testing"

	"github.com/werbot/shade/internal/core"
	"github.com/werbot/shade/internal/hook"
)

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
