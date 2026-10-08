package main

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"slices"
	"strconv"
	"strings"
	"time"

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
// The `--flag=value` form is accepted just as in parseFlags: two parsers of one
// syntax two parsers of one CLI must not — `--rules=R` works, while
// `--project=P` would be an "unknown flag".
func parseInputArgs(args []string) (inputArgs, error) {
	var a inputArgs
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if !strings.HasPrefix(arg, "-") {
			if a.path != "" {
				return inputArgs{}, fmt.Errorf("unexpected argument %q", arg)
			}
			a.path = arg
			continue
		}
		name, value, joined := strings.Cut(arg, "=")
		switch name {
		case "--json":
			if joined {
				return inputArgs{}, fmt.Errorf("%s does not take a value", name)
			}
			a.json = true
		case "--project":
			if !joined {
				i++
				// A value starting with a dash is the next flag, not the
				// directory: otherwise `--project --json` would silently swallow --json.
				if i == len(args) || strings.HasPrefix(args[i], "-") {
					return inputArgs{}, errors.New("--project requires a directory")
				}
				value = args[i]
			}
			if value == "" {
				return inputArgs{}, errors.New("--project requires a directory")
			}
			if err := checkDir(value); err != nil {
				return inputArgs{}, fmt.Errorf("--project: %w", err)
			}
			a.project = value
		default:
			return inputArgs{}, fmt.Errorf("unknown flag %q", arg)
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

// parseFlags parses a command arguments against its flag description: strs are flags with
// a value (the value is stored into the destination by pointer), bools are boolean flags.
// Returns the positional arguments. The flag sets of the `rules` commands overlap,
// and a copy of this loop per command would drift from its neighbours at the first
// edit — and a divergence here means a silently accepted flag.
//
// An unfamiliar flag is an error: a flag accepted and ignored would read as
// supported. A value starting with a dash is too: otherwise `--name --global`
// would swallow --global as the rule name. For such values there is the form
// `--flag=value`: it separates the value at the first "=" and is therefore good for
// a pattern starting with a dash (`-{5}BEGIN`), — the separate form counts it as
// the next flag.
func parseFlags(args []string, strs map[string]*string, bools map[string]*bool) ([]string, error) {
	var pos []string
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if !strings.HasPrefix(arg, "-") {
			pos = append(pos, arg)
			continue
		}
		name, value, joined := strings.Cut(arg, "=")
		if p, ok := bools[name]; ok {
			if joined {
				return nil, fmt.Errorf("%s does not take a value", name)
			}
			*p = true
			continue
		}
		p, ok := strs[name]
		if !ok {
			return nil, fmt.Errorf("unknown flag %q", arg)
		}
		if joined {
			*p = value
			continue
		}
		i++
		if i == len(args) || strings.HasPrefix(args[i], "-") {
			return nil, fmt.Errorf("%s requires a value", arg)
		}
		*p = args[i]
	}
	return pos, nil
}

// noExtraArgs rejects positional arguments: a command that has none must not
// must take a typo for a file.
func noExtraArgs(pos []string) error {
	if len(pos) > 0 {
		return fmt.Errorf("unexpected argument %q", pos[0])
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

// defaultListLimit is how many rows the list commands print (`entities list`,
// `audit`). The spec declares no flag for this, and unbounded output on a
// a large project it is unreadable.
const defaultListLimit = 100

// maxAgeDays is how many days fit in a time.Duration. Beyond that n*24h
// overflows int64 and gives a negative age.
const maxAgeDays = int64(math.MaxInt64) / int64(24*time.Hour)

// parseAge converts an age like 30d into a duration. The unit d is a day: ages in
// the CLI are given in days (`--older-than 30d`, `--since 7d`, `entities_ttl`), while
// time.ParseDuration does not know such a unit.
//
// The overflow is checked explicitly, rather than relying on "nobody will
// write such an age": a negative age in PruneEntities turns into a boundary in
// the future, and `--older-than 200000d` would wipe all entities of the project — the values
// live only in value_enc, so the loss cannot be rolled back.
func parseAge(s string) (time.Duration, error) {
	days, ok := strings.CutSuffix(s, "d")
	if !ok {
		return 0, fmt.Errorf("age %q: expected a number of days with a d suffix, for example 30d", s)
	}
	n, err := strconv.Atoi(days)
	if err != nil || n < 0 {
		return 0, fmt.Errorf("age %q: expected a number of days with a d suffix, for example 30d", s)
	}
	if int64(n) > maxAgeDays {
		return 0, fmt.Errorf("age %q: too large, %dd is the maximum", s, maxAgeDays)
	}
	return time.Duration(n) * 24 * time.Hour, nil
}

// stamp prints a moment from unix seconds in local time: the database stores time
// as Unix, while a human needs a readable form.
func stamp(unix int64) string { return time.Unix(unix, 0).Format(time.DateTime) }
