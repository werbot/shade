package main

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
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
	t.Run("working directory", func(t *testing.T) {
		t.Setenv("SHADE_HOME", t.TempDir())
		var out, errb bytes.Buffer
		if code := runDoctor(nil, IO{Out: &out, Err: &errb}); code != 0 {
			t.Fatalf("code=%d, err=%q", code, errb.String())
		}
		// On a clean directory the check itself creates the key and the database, so
		// the report describes an environment that is already in place, not its absence.
		for _, want := range []string{"key: created", "database: ", "project: ", "rules: "} {
			if !strings.Contains(out.String(), want) {
				t.Fatalf("the report has no %q: %q", want, out.String())
			}
		}
		if strings.Contains(out.String(), "rules: 0 active") {
			t.Fatalf("the builtin rule set was not seeded: %q", out.String())
		}

		out.Reset()
		if code := runDoctor(nil, IO{Out: &out, Err: &errb}); code != 0 {
			t.Fatalf("second run: code=%d, err=%q", code, errb.String())
		}
		if !strings.Contains(out.String(), "key: ready") {
			t.Fatalf("the key was not reused: %q", out.String())
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
