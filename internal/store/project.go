package store

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// Project is a shade project: a directory within whose bounds the placeholders
// are issued independently of other directories.
type Project struct {
	ID       int64
	RootPath string
	Name     string
}

// ProjectForPath returns the project for the directory dir, creating it on the first
// access. The top of the git repository counts as the root, and outside a repository —
// dir itself. A repeated call for the same root returns the same project,
// so a subdirectory and the root do not split the numbering of placeholders.
func (s *Store) ProjectForPath(ctx context.Context, dir string) (Project, error) {
	// cmd.Dir = "" means the working directory of the process for os/exec, so
	// an empty dir would silently bind the mapping to another project. In phase 2 cwd
	// comes from the payload of the hook, where an empty value is a real scenario.
	if dir == "" {
		return Project{}, errors.New("project directory is not set")
	}
	root := ProjectRoot(ctx, dir)
	p := Project{RootPath: root, Name: filepath.Base(root)}
	// name is written again with the same value: otherwise ON CONFLICT DO NOTHING
	// would not return the row through RETURNING and a separate SELECT would be needed.
	err := s.db.QueryRowContext(ctx, `INSERT INTO projects(root_path, name, created_at)
		VALUES(?, ?, ?)
		ON CONFLICT(root_path) DO UPDATE SET name = excluded.name
		RETURNING id`, p.RootPath, p.Name, time.Now().Unix()).Scan(&p.ID)
	if err != nil {
		return Project{}, fmt.Errorf("project for %s: %w", dir, err)
	}
	return p, nil
}

// ProjectRoot returns the root of the project dir belongs to: the top of the git
// repository, and dir itself outside a repository. The result is canonical —
// absolute and free of symlinks — so two spellings of one directory give one root.
//
// This is the single definition of "project root". ProjectForPath keys the rows of
// projects off it and the install command places the project settings by it; a
// second copy of the resolution would drift from this one at the first edit, and
// the two would disagree about where the settings go.
func ProjectRoot(ctx context.Context, dir string) string {
	root := canonicalPath(dir)
	if top := gitToplevel(ctx, root); top != "" {
		root = canonicalPath(top)
	}
	return root
}

// canonicalPath makes the path absolute and free of symlinks, falling back to
// absolute, if the symlinks did not resolve (the directory may not exist yet).
//
// Without it the same directory, named in different ways, gives two rows in
// projects and two counters, that is one value gets two placeholders:
// git returns a path with resolved symlinks (`/private/tmp/...` on macOS), while
// the passed string may be relative (".") or go through a symlink.
func canonicalPath(path string) string {
	abs, err := filepath.Abs(path)
	if err != nil {
		return path
	}
	resolved, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return abs
	}
	return resolved
}

// gitToplevel returns the top of the git repository for dir, and an empty string
// if dir is not in a repository or git is unavailable.
func gitToplevel(ctx context.Context, dir string) string {
	cmd := exec.CommandContext(ctx, "git", "rev-parse", "--show-toplevel")
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}
