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
	root := repoRoot(ctx, dir)
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

// repoRoot returns the top of the git repository for dir; if dir is not in
// a repository or git is unavailable — dir itself.
func repoRoot(ctx context.Context, dir string) string {
	cmd := exec.CommandContext(ctx, "git", "rev-parse", "--show-toplevel")
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		return dir
	}
	if root := strings.TrimSpace(string(out)); root != "" {
		return root
	}
	return dir
}
