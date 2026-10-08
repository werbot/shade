package store_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/werbot/shade/internal/store"
)

func TestProjectForPathUsesGitRoot(t *testing.T) {
	dir := t.TempDir()
	run(t, dir, "git", "init")
	sub := filepath.Join(dir, "a", "b")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	p1, err := s.ProjectForPath(ctx, sub)
	if err != nil {
		t.Fatal(err)
	}
	p2, err := s.ProjectForPath(ctx, dir)
	if err != nil {
		t.Fatal(err)
	}
	if p1.ID != p2.ID {
		t.Fatal("subdirectory must resolve to the same project")
	}
}

func TestProjectForPathFallsBackToCwd(t *testing.T) {
	dir := t.TempDir() // not a git repository
	p, err := s.ProjectForPath(ctx, dir)
	if err != nil {
		t.Fatal(err)
	}
	if p.RootPath != dir {
		t.Fatalf("want fallback to %s, got %s", dir, p.RootPath)
	}
}

// project creates a project for a fresh directory in the shared store of the package:
// every test gets its own project and its own counter of numbers.
func project(t *testing.T) store.Project {
	t.Helper()
	p, err := s.ProjectForPath(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return p
}

// run executes a command in the directory dir and fails on its error.
func run(t *testing.T, dir, name string, args ...string) {
	t.Helper()
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("%s %v in %s: %v\n%s", name, args, dir, err, out)
	}
}
