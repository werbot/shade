package main

import (
	"cmp"
	"fmt"
	"io"
	"slices"
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
