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
func runVersion(_ []string, stdio IO) int {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		fmt.Fprintln(stdio.Out, "(devel)")
		return 0
	}
	fmt.Fprintln(stdio.Out, info.Main.Version)
	return 0
}
