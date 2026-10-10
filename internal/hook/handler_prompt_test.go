package hook_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/werbot/shade/internal/hook"
	"github.com/werbot/shade/internal/store"
)

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

// promptWithSecrets is a prompt the builtin rules catch: the ssh login and host give a
// USER and a HOST span, so the gate has something to block.
const promptWithSecrets = "ssh alice@db.example.com"

// coverProject writes a fresh, live marker for root into the home's store: the gate reads
// it through the real engine the handler opens on the same home.
func coverProject(t *testing.T, home, root string) {
	t.Helper()
	st := openStore(t, home)
	if err := st.SetProxyMarker(ctx, store.ProxyMarker{
		PID: os.Getpid(), Port: 8787, RootPath: root, TS: time.Now().Unix(),
	}); err != nil {
		t.Fatal(err)
	}
}

// auto with no marker is the fail-safe default: nothing anonymizes the traffic, so the
// prompt is blocked exactly as `on` would block it.
func TestAutoBlocksWhenNoProxyRuns(t *testing.T) {
	home, repo := t.TempDir(), gitDir(t)
	writeConfig(t, repo, ".shade.toml", "prompt_gate = \"auto\"\n")
	h := newHandler(t, home)
	res, err := h.Handle(ctx, hook.Event{
		Name: hook.EventUserPromptSubmit, CWD: repo, Prompt: promptWithSecrets,
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Decision != "block" || !strings.Contains(res.Reason, "HOST") {
		t.Fatalf("auto without a proxy must block: %+v", res)
	}
}

// A live proxy that names this project wraps its traffic, so auto has nothing left to
// block: the prompt goes through untouched.
func TestAutoStaysQuietWhenAProxyCoversTheProject(t *testing.T) {
	home, repo := t.TempDir(), gitDir(t)
	writeConfig(t, repo, ".shade.toml", "prompt_gate = \"auto\"\n")
	coverProject(t, home, repo)
	h := newHandler(t, home)
	res, err := h.Handle(ctx, hook.Event{
		Name: hook.EventUserPromptSubmit, CWD: repo, Prompt: promptWithSecrets,
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Decision != "" || res.SystemMessage != "" {
		t.Fatalf("a covered project must stay quiet: %+v", res)
	}
}

// A proxy serving another repository does not wrap this one's traffic, so it must not
// silence this project's gate.
func TestAutoBlocksForAProxyOfAnotherProject(t *testing.T) {
	home, repo, other := t.TempDir(), gitDir(t), gitDir(t)
	writeConfig(t, repo, ".shade.toml", "prompt_gate = \"auto\"\n")
	coverProject(t, home, other)
	h := newHandler(t, home)
	res, err := h.Handle(ctx, hook.Event{
		Name: hook.EventUserPromptSubmit, CWD: repo, Prompt: promptWithSecrets,
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Decision != "block" {
		t.Fatalf("a proxy of another project must not silence the gate: %+v", res)
	}
}

// The explicit modes ignore the marker: off stays quiet with or without a live proxy, on
// blocks with or without one.
func TestOffAndOnIgnoreTheMarker(t *testing.T) {
	for _, tc := range []struct {
		gate  string
		cover bool
		want  bool // block?
	}{
		{"off", false, false},
		{"off", true, false},
		{"on", false, true},
		{"on", true, true},
	} {
		t.Run(fmt.Sprintf("%s/covered=%v", tc.gate, tc.cover), func(t *testing.T) {
			home, repo := t.TempDir(), gitDir(t)
			writeConfig(t, repo, ".shade.toml", "prompt_gate = \""+tc.gate+"\"\n")
			if tc.cover {
				coverProject(t, home, repo)
			}
			h := newHandler(t, home)
			res, err := h.Handle(ctx, hook.Event{
				Name: hook.EventUserPromptSubmit, CWD: repo, Prompt: promptWithSecrets,
			})
			if err != nil {
				t.Fatal(err)
			}
			if (res.Decision == "block") != tc.want {
				t.Fatalf("gate %s covered=%v: %+v", tc.gate, tc.cover, res)
			}
		})
	}
}

// The marker is consulted only in auto: off and on answer from the config alone. A
// ProxyCovers that fails must therefore never surface under them — were the store asked,
// the failure would fail open into a systemMessage.
func TestOffAndOnDoNotAskForTheProxy(t *testing.T) {
	for _, gate := range []string{"off", "on"} {
		t.Run(gate, func(t *testing.T) {
			e := &fakeEngine{proxyCovers: func(context.Context) (bool, error) {
				return false, errors.New("the store is gone")
			}}
			e.root = t.TempDir()
			writeConfig(t, e.root, ".shade.toml", "prompt_gate = \""+gate+"\"\n")
			h := newFakeHandler(t, e)
			res, err := h.Handle(ctx, hook.Event{
				Name: hook.EventUserPromptSubmit, CWD: e.root, Prompt: promptWithSecrets,
			})
			if err != nil {
				t.Fatal(err)
			}
			if res.SystemMessage != "" {
				t.Fatalf("gate %s must not consult the store: %+v", gate, res)
			}
		})
	}
}
