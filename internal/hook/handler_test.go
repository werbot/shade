package hook_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/werbot/shade/internal/core"
	"github.com/werbot/shade/internal/crypt"
	"github.com/werbot/shade/internal/hook"
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

// fakeEngine is the Engine stand-in: it hands back canned restore results and
// remembers whether it was closed. Only the tests that do not need the journal use it —
// the gate, SessionStart and PostToolUse tests run through the real engine, because
// those responses are produced by core, not by the event walker.
type fakeEngine struct {
	root        string
	restoreFn   func(string) (core.Result, error)
	restoreErr  error
	closed      bool
	proxyCovers func(context.Context, string) (bool, error)
}

func (f *fakeEngine) Anonymize(_ context.Context, text string) (core.Result, error) {
	return core.Result{Text: text}, nil
}

func (f *fakeEngine) ProxyCovers(ctx context.Context, upstreamURL string) (bool, error) {
	if f.proxyCovers != nil {
		return f.proxyCovers(ctx, upstreamURL)
	}
	return false, nil
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
