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

	"github.com/werbot/shade/internal/crypt"
	"github.com/werbot/shade/internal/store"
)

// runCLI is the only test entry point into the CLI for all commands: home → SHADE_HOME,
// dir → working directory (the project is resolved from it), a Dispatch call with
// captured streams. The directory is put back via t.Cleanup.
func runCLI(t *testing.T, home, dir string, args []string, stdin string) (int, string, string) {
	t.Helper()
	t.Setenv("SHADE_HOME", home)
	prev, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.Chdir(prev); err != nil {
			t.Errorf("returning to %s: %v", prev, err)
		}
	})

	var out, errb bytes.Buffer
	code := Dispatch(args, IO{In: strings.NewReader(stdin), Out: &out, Err: &errb})
	return code, out.String(), errb.String()
}

// gitDir — a "project": t.TempDir() with an initialized git repository.
// Without a repository ProjectForPath would take the directory itself, and two subdirectories of one
// of the project would have split the placeholder numbering.
func gitDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if out, err := exec.Command("git", "init", "-q", dir).CombinedOutput(); err != nil {
		t.Fatalf("git init %s: %v: %s", dir, err, out)
	}
	return dir
}

// testStore opens the test store and resolves the project of directory dir: the journal,
// row ages and triggers are set only through the opened Store.DB(),
// a separate API for the sake of tests is not introduced.
func testStore(t *testing.T, home, dir string) (store.Project, *store.Store) {
	t.Helper()
	t.Setenv("SHADE_HOME", home)
	key, err := crypt.LoadOrCreateKey(home)
	if err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(home, key)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	p, err := st.ProjectForPath(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	return p, st
}

// backdate moves last_seen_at of all entities of the project into the past: prune
// compares the age with the current time, and a test cannot wait a month.
func backdate(t *testing.T, home, dir string, d time.Duration) {
	t.Helper()
	p, st := testStore(t, home, dir)
	if _, err := st.DB().ExecContext(context.Background(),
		`UPDATE entities SET last_seen_at=? WHERE project_id=?`,
		time.Now().Add(-d).Unix(), p.ID); err != nil {
		t.Fatal(err)
	}
}

// seedAudit puts an entry with the given time and action into the journal: the CLI writes
// only unresolved, while the filters must be checked on the second kind of entry too.
func seedAudit(t *testing.T, home, dir string, ts int64, action, detail string) {
	t.Helper()
	p, st := testStore(t, home, dir)
	direction := "to_model"
	if action == "unresolved" {
		direction = "from_model"
	}
	if _, err := st.DB().ExecContext(context.Background(),
		`INSERT INTO audit(ts, project_id, direction, adapter, action, detail)
		 VALUES(?, ?, ?, 'cli', ?, ?)`,
		ts, p.ID, direction, action, detail); err != nil {
		t.Fatal(err)
	}
}

func TestDispatchUnknownCommand(t *testing.T) {
	var out, errb bytes.Buffer
	code := Dispatch([]string{"nope"}, IO{In: strings.NewReader(""), Out: &out, Err: &errb})
	if code != 2 {
		t.Fatalf("want exit 2, got %d", code)
	}
	if !strings.Contains(errb.String(), "nope") {
		t.Fatalf("stderr must name the command, got %q", errb.String())
	}
}

func TestDispatchKnownCommand(t *testing.T) {
	called := false
	Register(Command{Name: "probe", Help: "test", Run: func([]string, IO) int {
		called = true
		return 0
	}})
	var out, errb bytes.Buffer
	if code := Dispatch([]string{"probe"}, IO{In: strings.NewReader(""), Out: &out, Err: &errb}); code != 0 || !called {
		t.Fatalf("code=%d called=%v", code, called)
	}
}

func TestDispatchWithoutArgsPrintsUsage(t *testing.T) {
	var out, errb bytes.Buffer
	if code := Dispatch(nil, IO{In: strings.NewReader(""), Out: &out, Err: &errb}); code != 0 {
		t.Fatalf("want exit 0, got %d", code)
	}
	for _, want := range []string{"version", "doctor"} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("the command list does not contain %q: %q", want, out.String())
		}
	}
	if errb.Len() != 0 {
		t.Fatalf("stderr must be empty, got %q", errb.String())
	}
}

func TestDoctor(t *testing.T) {
	t.Run("clean environment", func(t *testing.T) {
		home := t.TempDir()
		t.Setenv("SHADE_HOME", home)
		var out, errb bytes.Buffer
		if code := runDoctor(nil, IO{Out: &out, Err: &errb}); code != 0 {
			t.Fatalf("code=%d, err=%q", code, errb.String())
		}
		for _, want := range []string{"key: missing", "database: not created", "rules: unknown"} {
			if !strings.Contains(out.String(), want) {
				t.Fatalf("the report has no %q: %q", want, out.String())
			}
		}
		// The check must not create anything: otherwise it answers itself the
		// question it was asked, and "configured" is indistinguishable from "got configured
		// just now". The absence of a row in the output does not prove this — we look
		// at the state directory.
		entries, err := os.ReadDir(home)
		if err != nil {
			t.Fatal(err)
		}
		if len(entries) != 0 {
			names := make([]string, 0, len(entries))
			for _, e := range entries {
				names = append(names, e.Name())
			}
			t.Fatalf("doctor created in the state directory: %v", names)
		}
	})
	t.Run("ready environment", func(t *testing.T) {
		home, dir := t.TempDir(), gitDir(t)
		if code, _, stderr := runCLI(t, home, dir, []string{"anon"}, "текст без секретов\n"); code != 0 {
			t.Fatalf("setup: code=%d, err=%q", code, stderr)
		}
		var out, errb bytes.Buffer
		if code := runDoctor(nil, IO{Out: &out, Err: &errb}); code != 0 {
			t.Fatalf("code=%d, err=%q", code, errb.String())
		}
		for _, want := range []string{"key: readable", "database: ", "project: ", "rules: "} {
			if !strings.Contains(out.String(), want) {
				t.Fatalf("the report has no %q: %q", want, out.String())
			}
		}
		if strings.Contains(out.String(), "rules: 0 active") {
			t.Fatalf("the builtin rule set was not seeded: %q", out.String())
		}
	})
	t.Run("directory is unavailable", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "file")
		if err := os.WriteFile(path, nil, 0o600); err != nil {
			t.Fatal(err)
		}
		t.Setenv("SHADE_HOME", path)
		var out, errb bytes.Buffer
		if code := runDoctor(nil, IO{Out: &out, Err: &errb}); code != 1 {
			t.Fatalf("code=%d, want 1", code)
		}
		if !strings.Contains(errb.String(), "not a directory") {
			t.Fatalf("stderr does not explain the problem: %q", errb.String())
		}
	})
}

// The exit code contract: wrong arguments are 2. doctor takes no
// arguments, so any argument is a call error, not a reason to do something.
func TestDoctorRejectsArgs(t *testing.T) {
	t.Setenv("SHADE_HOME", t.TempDir())
	var out, errb bytes.Buffer
	if code := runDoctor([]string{"--json"}, IO{Out: &out, Err: &errb}); code != 2 {
		t.Fatalf("code %d, expected 2 (%s)", code, errb.String())
	}
	if errb.Len() == 0 {
		t.Fatal("code 2 without an explanation in stderr")
	}
}

// version takes no arguments, just like doctor: an accepted and ignored
// argument would read as supported, and `version extra` would return 0.
func TestVersionRejectsArgs(t *testing.T) {
	var out, errb bytes.Buffer
	if code := runVersion(nil, IO{Out: &out, Err: &errb}); code != 0 {
		t.Fatalf("without arguments: code %d (%s)", code, errb.String())
	}
	errb.Reset()
	if code := runVersion([]string{"extra"}, IO{Out: &out, Err: &errb}); code != 2 {
		t.Fatalf("code %d, expected 2 (%s)", code, errb.String())
	}
	if errb.Len() == 0 {
		t.Fatal("code 2 without an explanation in stderr")
	}
}

func TestHomeStatus(t *testing.T) {
	t.Run("no directory", func(t *testing.T) {
		status, err := homeStatus(filepath.Join(t.TempDir(), "nope"))
		if err != nil {
			t.Fatalf("a missing directory is not an error, got %v", err)
		}
		if !strings.Contains(status, "will be created") {
			t.Fatalf("status=%q", status)
		}
	})
	t.Run("empty path", func(t *testing.T) {
		if _, err := homeStatus(""); err == nil {
			t.Fatal("an empty path must be an error")
		}
	})
	t.Run("no write permission", func(t *testing.T) {
		dir := filepath.Join(t.TempDir(), "ro")
		if err := os.Mkdir(dir, 0o500); err != nil {
			t.Fatal(err)
		}
		if _, err := homeStatus(dir); err == nil {
			t.Fatal("a directory without write permission must be an error")
		}
	})
}
