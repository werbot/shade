package main

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestParseListenArgs — the arguments serve and ui share: the project falls back to the working
// directory, port 0 is a real value (the OS picks one) and not "not given", and every bad call
// is exit 2 with a message that names the culprit.
func TestParseListenArgs(t *testing.T) {
	dir, cwd := t.TempDir(), t.TempDir()
	t.Chdir(cwd)
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct {
		name   string
		args   []string
		want   listenArgs
		errSub string // empty — the call is valid
	}{
		{"project", []string{"--project", dir}, listenArgs{dir, 9000}, ""},
		{"port zero", []string{"--project", dir, "--port", "0"}, listenArgs{dir, 0}, ""},
		{"highest port", []string{"--project", dir, "--port", "65535"}, listenArgs{dir, 65535}, ""},
		{"working directory", nil, listenArgs{wd, 9000}, ""},
		{"port too high", []string{"--port", "65536"}, listenArgs{}, "--port"},
		{"port not a number", []string{"--port", "abc"}, listenArgs{}, "--port"},
		{"port negative", []string{"--port", "-1"}, listenArgs{}, "--port"},
		{"port negative joined", []string{"--port=-1"}, listenArgs{}, "--port"},
		{"project missing", []string{"--project", filepath.Join(cwd, "missing")}, listenArgs{}, "--project"},
		{"project empty joined", []string{"--project="}, listenArgs{}, "--project requires a directory"},
		{"project empty separate", []string{"--project", ""}, listenArgs{}, "--project requires a directory"},
		{"extra argument", []string{"x"}, listenArgs{}, `unexpected argument "x"`},
		{"unknown flag", []string{"--nope"}, listenArgs{}, "--nope"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, code, err := parseListenArgs(tc.args, 9000)
			if tc.errSub == "" {
				if err != nil || code != 0 || got != tc.want {
					t.Fatalf("parseListenArgs(%q) = %+v, %d, %v; want %+v, 0, nil", tc.args, got, code, err, tc.want)
				}
				return
			}
			if code != 2 || err == nil || !strings.Contains(err.Error(), tc.errSub) {
				t.Fatalf("parseListenArgs(%q) = code %d, err %v; want code 2 and an error containing %q", tc.args, code, err, tc.errSub)
			}
		})
	}
}

// loopback is a listener on a free loopback port; serveHTTP closes it.
func loopback(t *testing.T) net.Listener {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	return ln
}

// serveAsync runs serveHTTP in the background and returns the channel its result arrives on.
func serveAsync(ctx context.Context, ln net.Listener, h http.Handler) <-chan error {
	done := make(chan error, 1)
	go func() { done <- serveHTTP(ctx, ln, h) }()
	return done
}

// within waits for a result, bounded: a server or a client that never answers must fail the
// test, not hang it.
func within(t *testing.T, ch <-chan error) error {
	t.Helper()
	select {
	case err := <-ch:
		return err
	case <-time.After(5 * time.Second):
		t.Fatal("no result within 5s")
		return nil
	}
}

// getOK reports anything but a 200 answer as an error. It returns the error instead of failing
// the test, so it is safe to call from a goroutine; proxyClient bounds the request.
func getOK(url string) error {
	resp, err := proxyClient.Get(url)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("status %d, want 200", resp.StatusCode)
	}
	return nil
}

// TestServeHTTPStopsCleanlyOnCancel — the server answers until its context is cancelled, and
// the stop that follows is a success, not an error.
func TestServeHTTPStopsCleanlyOnCancel(t *testing.T) {
	ln := loopback(t)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := serveAsync(ctx, ln, http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))

	if err := getOK("http://" + ln.Addr().String()); err != nil {
		t.Fatalf("request before the cancel: %v", err)
	}
	cancel()
	if err := within(t, done); err != nil {
		t.Fatalf("serveHTTP = %v, want nil after a cancel", err)
	}
}

// TestServeHTTPWaitsForAnInFlightRequest — a cancel does not cut a request that is already being
// served: the server returns only after the answer has gone out.
func TestServeHTTPWaitsForAnInFlightRequest(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	ln := loopback(t)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := serveAsync(ctx, ln, http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		close(entered)
		<-release
	}))

	answer := make(chan error, 1)
	go func() { answer <- getOK("http://" + ln.Addr().String()) }()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("the request never reached the handler")
	}

	cancel()
	select {
	case err := <-done:
		t.Fatalf("serveHTTP returned %v while a request was in flight", err)
	case <-time.After(100 * time.Millisecond):
	}

	close(release)
	if err := within(t, answer); err != nil {
		t.Fatalf("in-flight request: %v", err)
	}
	if err := within(t, done); err != nil {
		t.Fatalf("serveHTTP = %v, want nil once the request finished", err)
	}
}

// TestServeHTTPReturnsTheServeError — a server that cannot serve fails with the listener's own
// error, instead of waiting for a cancel that no one will send.
func TestServeHTTPReturnsTheServeError(t *testing.T) {
	ln := loopback(t)
	if err := ln.Close(); err != nil {
		t.Fatal(err)
	}
	if err := within(t, serveAsync(t.Context(), ln, http.NotFoundHandler())); !errors.Is(err, net.ErrClosed) {
		t.Fatalf("serveHTTP = %v, want the closed listener error", err)
	}
}
