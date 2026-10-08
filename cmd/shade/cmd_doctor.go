package main

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"

	"github.com/werbot/shade/internal/crypt"
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
// Nothing may be created in the process: the key and the database are exactly what the user
// came to ask about, and a key created along the way would erase the difference between "configured"
// and "was just configured".
func runDoctor(args []string, stdio IO) int {
	if len(args) != 0 {
		return fail(stdio, "doctor", 2, fmt.Errorf("unexpected arguments: %v", args))
	}
	home := store.Home()
	status, err := homeStatus(home)
	if err != nil {
		fmt.Fprintf(stdio.Err, "directory: %s — %v\n", home, err)
		return 1
	}
	fmt.Fprintf(stdio.Out, "directory: %s — %s\n", home, status)

	// The presence of the key is the boundary of the check: the key and the database are created by one call
	// core.New, so "there is no key" also means "there is no database". Going further is not allowed —
	// the check would create what it checks, and telling a configured environment
	// from a just-configured one would become impossible.
	if _, err := os.Stat(crypt.KeyPath(home)); errors.Is(err, fs.ErrNotExist) {
		fmt.Fprintln(stdio.Out, "key: missing, it will be created on first run")
		fmt.Fprintln(stdio.Out, "database: not created")
		fmt.Fprintln(stdio.Out, "rules: unknown, the database is not created")
		return 0
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

	fmt.Fprintln(stdio.Out, "key: readable")
	fmt.Fprintf(stdio.Out, "database: %s\n", store.DBPath(home))
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
