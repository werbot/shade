package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

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
		if !strings.Contains(out.String(), "key: missing") {
			t.Fatalf("the report has no line about the key: %q", out.String())
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
