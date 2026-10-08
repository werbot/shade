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
	// The expectation is the canonical form: the path is stored without symlinks, otherwise one
	// a directory named in different ways would create a second project (see below).
	want, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatal(err)
	}
	p, err := s.ProjectForPath(ctx, dir)
	if err != nil {
		t.Fatal(err)
	}
	if p.RootPath != want {
		t.Fatalf("want fallback to %s, got %s", want, p.RootPath)
	}
}

func TestProjectForPathCanonicalizesSameDir(t *testing.T) {
	dir := t.TempDir()
	want, err := s.ProjectForPath(ctx, dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(dir, link); err != nil {
		t.Fatal(err)
	}
	// dir + "/sub/.." is built by concatenation, not filepath.Join: Join cleans the path
	// on its own, and the test would stop checking anything.
	for _, spelling := range []string{dir + "/sub/..", link} {
		got, err := s.ProjectForPath(ctx, spelling)
		if err != nil {
			t.Fatal(err)
		}
		if got.ID != want.ID {
			t.Fatalf("directory %q gave project %d, while %q gave %d: one value would get two placeholders",
				spelling, got.ID, dir, want.ID)
		}
	}
}

func TestProjectForPathRelativeAndAbsoluteAgree(t *testing.T) {
	dir := t.TempDir()
	want, err := s.ProjectForPath(ctx, dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Chdir(dir)
	got, err := s.ProjectForPath(ctx, ".")
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != want.ID {
		t.Fatalf(`"." from the directory gave project %d, while the absolute path gave %d`, got.ID, want.ID)
	}
}

func TestProjectForPathRejectsEmptyDir(t *testing.T) {
	if _, err := s.ProjectForPath(ctx, ""); err == nil {
		t.Fatal("an empty dir must be an error, not the project of the process working directory")
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
