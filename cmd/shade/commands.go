package main

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"slices"
	"strings"

	"github.com/werbot/shade/internal/core"
	"github.com/werbot/shade/internal/store"
)

// IO holds the input/output streams a subcommand uses.
type IO struct {
	In  io.Reader
	Out io.Writer
	Err io.Writer
}

// Command is a registered CLI subcommand.
type Command struct {
	Name string
	Help string
	Run  func(args []string, stdio IO) int
}

// commands is the subcommand registry in registration order.
var commands []Command

// Register adds a subcommand to the registry.
func Register(c Command) {
	commands = append(commands, c)
}

// Dispatch runs the subcommand named by args[0] and returns the exit code.
// Without arguments and with --help it prints the command list; for an unknown
// command it writes a message to Err and returns 2.
func Dispatch(args []string, stdio IO) int {
	if len(args) == 0 || args[0] == "--help" {
		printUsage(stdio.Out)
		return 0
	}
	if i := slices.IndexFunc(commands, func(c Command) bool { return c.Name == args[0] }); i >= 0 {
		return commands[i].Run(args[1:], stdio)
	}
	fmt.Fprintf(stdio.Err, "shade: unknown command %q\n", args[0])
	printUsage(stdio.Err)
	return 2
}

// printUsage prints the command list sorted by name.
func printUsage(w io.Writer) {
	fmt.Fprintln(w, "Usage: shade <command> [arguments]")
	fmt.Fprintln(w, "Commands:")
	registered := slices.Clone(commands)
	slices.SortFunc(registered, func(a, b Command) int { return cmp.Compare(a.Name, b.Name) })
	for _, c := range registered {
		fmt.Fprintf(w, "  %s — %s\n", c.Name, c.Help)
	}
}

// inputArgs are the parsed arguments of commands that work with a single text.
type inputArgs struct {
	path    string // file argument; empty — read stdin
	project string // project resolution directory; empty — the current directory
	json    bool
}

// parseInputArgs parses the common command arguments: [--json] [--project DIR] [FILE].
func parseInputArgs(args []string) (inputArgs, error) {
	var a inputArgs
	for i := 0; i < len(args); i++ {
		switch arg := args[i]; {
		case arg == "--json":
			a.json = true
		case arg == "--project":
			i++
			// A value starting with a dash is the next flag, not the
			// directory: otherwise `--project --json` would silently swallow --json.
			if i == len(args) || args[i] == "" || strings.HasPrefix(args[i], "-") {
				return inputArgs{}, errors.New("--project requires a directory")
			}
			if err := checkDir(args[i]); err != nil {
				return inputArgs{}, fmt.Errorf("--project: %w", err)
			}
			a.project = args[i]
		case strings.HasPrefix(arg, "-"):
			return inputArgs{}, fmt.Errorf("unknown flag %q", arg)
		case a.path == "":
			a.path = arg
		default:
			return inputArgs{}, fmt.Errorf("unexpected argument %q", arg)
		}
	}
	return a, nil
}

// checkDir verifies that dir is an existing directory. Without it ProjectForPath
// would fall back to the absolute path of a nonexistent directory (git silently
// returns empty) and would create a row in projects that nothing else can be
// match: the placeholders of such a "project" are unreachable.
func checkDir(dir string) error {
	info, err := os.Stat(dir)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return fmt.Errorf("%s is not a directory", dir)
	}
	return nil
}

// writeJSON prints the value to w. SetEscapeHTML(false): otherwise the angle brackets
// the placeholders will go into unicode escape sequences and the output will stop
// be readable by eye.
func writeJSON(w io.Writer, v any) error {
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	return enc.Encode(v)
}

// readInput reads the command text: from the file argument or from stdin.
func readInput(a inputArgs, stdin io.Reader) (string, error) {
	if a.path == "" {
		b, err := io.ReadAll(stdin)
		if err != nil {
			return "", fmt.Errorf("reading stdin: %w", err)
		}
		return string(b), nil
	}
	b, err := os.ReadFile(a.path)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// openEngine assembles the engine for the project of directory project, and without it — of the current
// directory. The directory comes from outside, and is not taken from os.Getwd inside the engine:
// the phase 2 adapters pass the cwd from the hook payload there, not the process environment.
func openEngine(ctx context.Context, project string) (*core.Engine, error) {
	dir := project
	if dir == "" {
		wd, err := os.Getwd()
		if err != nil {
			return nil, fmt.Errorf("working directory: %w", err)
		}
		dir = wd
	}
	return core.New(ctx, store.Home(), dir, "cli")
}

// fail prints a command error to stderr and returns its exit code.
func fail(stdio IO, name string, code int, err error) int {
	fmt.Fprintf(stdio.Err, "%s: %v\n", name, err)
	return code
}
