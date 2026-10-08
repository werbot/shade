package main

import (
	"fmt"
	"runtime/debug"
)

func init() {
	Register(Command{
		Name: "version",
		Help: "print the build version",
		Run:  runVersion,
	})
}

// runVersion prints the module version from the build info. For a local build
// (go run, go build without a version tag) it is "(devel)".
//
// The command takes no arguments, and an extra one is a call error, as with doctor: an accepted and
// an ignored argument would read as supported.
func runVersion(args []string, stdio IO) int {
	if len(args) != 0 {
		return fail(stdio, "version", 2, fmt.Errorf("unexpected arguments: %v", args))
	}
	info, ok := debug.ReadBuildInfo()
	if !ok {
		fmt.Fprintln(stdio.Out, "(devel)")
		return 0
	}
	fmt.Fprintln(stdio.Out, info.Main.Version)
	return 0
}
