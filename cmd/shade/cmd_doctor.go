package main

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/werbot/shade/internal/store"
)

func init() {
	Register(Command{
		Name: "doctor",
		Help: "check that the environment is ready",
		Run:  runDoctor,
	})
}

// runDoctor prints the state of the environment and returns 1 if it is unusable.
//
// The check goes through the engine, not through stat: readiness is the ability
// to open the database and read the rules of the project, not the presence of files on disk.
// The side effect is the same as with any other command: the missing key and database
// are created, so the report describes the state after the check.
func runDoctor(_ []string, stdio IO) int {
	home := store.Home()
	status, err := homeStatus(home)
	if err != nil {
		fmt.Fprintf(stdio.Err, "directory: %s — %v\n", home, err)
		return 1
	}
	fmt.Fprintf(stdio.Out, "directory: %s — %s\n", home, status)

	// A key that cannot be read will be filtered out by openEngine below: a separate check
	// errors here would only duplicate its refusal.
	keyPath := filepath.Join(home, "key")
	keyState := "ready"
	if _, err := os.Stat(keyPath); errors.Is(err, fs.ErrNotExist) {
		keyState = "created"
	}

	wd, err := os.Getwd()
	if err != nil {
		fmt.Fprintf(stdio.Err, "working directory: %v\n", err)
		return 1
	}
	e, err := openEngine(context.Background(), wd)
	if err != nil {
		fmt.Fprintf(stdio.Err, "environment is unusable: %v\n", err)
		return 1
	}
	defer e.Close()

	fmt.Fprintf(stdio.Out, "key: %s\n", keyState)
	fmt.Fprintf(stdio.Out, "database: %s\n", filepath.Join(home, "shade.db"))
	fmt.Fprintf(stdio.Out, "project: %s\n", e.RootPath())
	fmt.Fprintf(stdio.Out, "rules: %d active\n", e.RuleCount())
	return 0
}

// homeStatus describes the state of directory dir as a human-readable string.
// A missing directory is not an error: it will be created on first run.
// The write permission check stays here, not in the engine: MkdirAll on top of
// of a file would return "not a directory", from which the cause is not visible.
func homeStatus(dir string) (string, error) {
	if dir == "" {
		return "", errors.New("home directory is not defined, set SHADE_HOME")
	}
	info, err := os.Stat(dir)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return "not created, it will be created on first run", nil
	case err != nil:
		return "", err
	case !info.IsDir():
		return "", errors.New("not a directory")
	case info.Mode().Perm()&0o200 == 0:
		return "", errors.New("no write permission")
	}
	return "available", nil
}
