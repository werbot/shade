package main

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

func init() {
	Register(Command{
		Name: "doctor",
		Help: "check that the environment is ready",
		Run:  runDoctor,
	})
}

// runDoctor prints a report on the state of the environment and returns 1 if
// the state directory is unavailable. There is no way to check the key, the database and the rules yet:
// the crypto and store layers appear in later tasks.
func runDoctor(_ []string, stdio IO) int {
	home := shadeHome()
	status, err := homeStatus(home)
	if err != nil {
		fmt.Fprintf(stdio.Err, "directory: %s — %v\n", home, err)
		return 1
	}
	fmt.Fprintf(stdio.Out, "directory: %s — %s\n", home, status)
	fmt.Fprintln(stdio.Out, "key: missing (it will be created on first run)")
	fmt.Fprintln(stdio.Out, "database: missing")
	fmt.Fprintln(stdio.Out, "rules: unknown (the database is not created)")
	return 0
}

// shadeHome returns the shade state directory: SHADE_HOME, otherwise ~/.shade.
func shadeHome() string {
	if dir := os.Getenv("SHADE_HOME"); dir != "" {
		return dir
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".shade")
}

// homeStatus describes the state of directory dir as a human-readable string.
// A missing directory is not an error: it will be created on first run.
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
