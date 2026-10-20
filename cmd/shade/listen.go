package main

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"
)

// listenArgs are the arguments of the commands that listen on a loopback port: the project
// they work on and the port they bind.
type listenArgs struct {
	project string // resolved directory, never empty
	port    int
}

// parseListenArgs reads --project and --port. On error the int is the exit code:
// 2 for a bad call, 1 when the working directory cannot be read.
func parseListenArgs(args []string, defaultPort int) (listenArgs, int, error) {
	var project, portArg string
	pos, err := parseFlags(args, map[string]*string{"--project": &project, "--port": &portArg}, nil)
	if err != nil {
		return listenArgs{}, 2, err
	}
	if err := noExtraArgs(pos); err != nil {
		return listenArgs{}, 2, err
	}
	// An explicitly empty --project is a call error, not a fallback to the working
	// directory: parseInputArgs answers the same input with exit 2 for every other
	// command, and a "project" that is really the cwd creates placeholder rows that
	// nothing else can reach.
	for _, arg := range args {
		if name, _, _ := strings.Cut(arg, "="); name == "--project" && project == "" {
			return listenArgs{}, 2, errors.New("--project requires a directory")
		}
	}
	if project != "" {
		if err := checkDir(project); err != nil {
			return listenArgs{}, 2, fmt.Errorf("--project: %w", err)
		}
	}
	if project == "" {
		project, err = os.Getwd()
		if err != nil {
			return listenArgs{}, 1, fmt.Errorf("working directory: %w", err)
		}
	}
	port := defaultPort
	if portArg != "" {
		port, err = strconv.Atoi(portArg)
		if err != nil || port < 0 || port > 65535 {
			return listenArgs{}, 2, fmt.Errorf("--port: %q is not a port", portArg)
		}
	}
	return listenArgs{project: project, port: port}, 0, nil
}

// serveHTTP serves h on ln until ctx is cancelled, then waits up to 5 seconds for
// in-flight requests. A clean stop returns nil; a Serve failure is returned as is.
func serveHTTP(ctx context.Context, ln net.Listener, h http.Handler) error {
	srv := &http.Server{
		Handler: h,
		// A header must arrive promptly, but the body of a streamed answer is long-lived,
		// so there is no overall read or write timeout.
		ReadHeaderTimeout: 10 * time.Second,
	}
	errCh := make(chan error, 1)
	go func() { errCh <- srv.Serve(ln) }()

	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
		// Cancelled, in practice by a signal: give in-flight requests a moment to finish before
		// the caller cleans up.
		shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdown)
		return nil
	}
}

// portOf is the port the listener actually bound. --port 0 lets the OS choose one, and both
// the marker and the wrap line must name the real one, not the requested zero.
func portOf(ln net.Listener) int {
	if a, ok := ln.Addr().(*net.TCPAddr); ok {
		return a.Port
	}
	return 0
}
