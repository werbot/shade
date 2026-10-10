package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/werbot/shade/internal/core"
	"github.com/werbot/shade/internal/crypt"
	"github.com/werbot/shade/internal/proxy"
	"github.com/werbot/shade/internal/store"
)

// defaultServePort is the port `shade serve` listens on when --port is not given.
const defaultServePort = 8787

func init() {
	Register(Command{
		Name: "serve",
		Help: "run the anonymizing proxy for a coding agent",
		Run:  runServe,
	})
}

// runServe brings up the HTTP proxy on the loopback interface. It follows the mcp command —
// the project is resolved once, before serving, because the process is started from
// anywhere — and differs in what it publishes: a liveness marker in the store, so the hook's
// gate can tell that this project's traffic is already being anonymized. stdout carries the
// one line the user copies to point their agent at the proxy.
func runServe(args []string, stdio IO) int {
	var project, portArg string
	pos, err := parseFlags(args, map[string]*string{"--project": &project, "--port": &portArg}, nil)
	if err != nil {
		return fail(stdio, "serve", 2, err)
	}
	if err := noExtraArgs(pos); err != nil {
		return fail(stdio, "serve", 2, err)
	}
	// An explicitly empty --project is a call error, not a fallback to the working
	// directory: parseInputArgs answers the same input with exit 2 for every other
	// command, and a "project" that is really the cwd creates placeholder rows that
	// nothing else can reach.
	for _, arg := range args {
		if name, _, _ := strings.Cut(arg, "="); name == "--project" && project == "" {
			return fail(stdio, "serve", 2, errors.New("--project requires a directory"))
		}
	}
	if project != "" {
		if err := checkDir(project); err != nil {
			return fail(stdio, "serve", 2, fmt.Errorf("--project: %w", err))
		}
	}
	if project == "" {
		project, err = os.Getwd()
		if err != nil {
			return fail(stdio, "serve", 1, fmt.Errorf("working directory: %w", err))
		}
	}
	port := defaultServePort
	if portArg != "" {
		port, err = strconv.Atoi(portArg)
		if err != nil || port < 0 || port > 65535 {
			return fail(stdio, "serve", 2, fmt.Errorf("--port: %q is not a port", portArg))
		}
	}

	// The listener is bound before the marker is written: an occupied port must fail
	// without leaving a marker that claims a proxy is serving this project.
	ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		return fail(stdio, "serve", 1, err)
	}
	defer ln.Close()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	home := store.Home()
	// The root path, not the project directory: the hook matches the marker against the
	// project root it resolves from its own working directory, so a subdirectory here and a
	// root there must still name the same project.
	keeper, err := startMarkerKeeper(ctx, home, store.ProjectRoot(ctx, project), portOf(ln), stdio.Err)
	if err != nil {
		return fail(stdio, "serve", 1, err)
	}
	defer keeper.stop()

	open := func(ctx context.Context) (proxy.Engine, error) {
		return core.New(ctx, home, project, "proxy")
	}
	srv, err := proxy.NewServer(proxy.Options{Home: home, Project: project, Diag: stdio.Err}, open)
	if err != nil {
		return fail(stdio, "serve", 1, err)
	}

	fmt.Fprintf(stdio.Out, "export ANTHROPIC_BASE_URL=http://127.0.0.1:%d\n", portOf(ln))

	httpSrv := &http.Server{
		Handler: srv,
		// A header must arrive promptly, but the body of a streamed answer is long-lived,
		// so there is no overall read or write timeout.
		ReadHeaderTimeout: 10 * time.Second,
	}
	errCh := make(chan error, 1)
	go func() { errCh <- httpSrv.Serve(ln) }()

	select {
	case err := <-errCh:
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			return fail(stdio, "serve", 1, err)
		}
		return 0
	case <-ctx.Done():
		// A signal: give in-flight requests a moment to finish before the marker is cleared.
		shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = httpSrv.Shutdown(shutdown)
		return 0
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

// markerKeeper publishes and refreshes the liveness marker of a running proxy, and clears it
// on the way out. It owns its own store: the marker is store-level state, not engine-level, so
// it is opened once for the process rather than through the per-request engine.
type markerKeeper struct {
	store  *store.Store
	root   string
	port   int
	diag   io.Writer
	cancel context.CancelFunc
	done   chan struct{}
	once   sync.Once
}

// startMarkerKeeper opens a store, writes the first marker and starts the heartbeat that
// rewrites it every store.ProxyHeartbeat. parent drives the heartbeat; cancelling it, or stop,
// ends the loop.
func startMarkerKeeper(parent context.Context, home, root string, port int, diag io.Writer) (*markerKeeper, error) {
	// The same open sequence as core.New: the directory and the key must exist before the
	// store can be opened.
	if err := store.EnsureHome(home); err != nil {
		return nil, err
	}
	key, err := crypt.LoadOrCreateKey(home)
	if err != nil {
		return nil, err
	}
	st, err := store.Open(home, key)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithCancel(parent)
	k := &markerKeeper{store: st, root: root, port: port, diag: diag, cancel: cancel, done: make(chan struct{})}
	if err := k.touch(ctx); err != nil {
		cancel()
		st.Close()
		return nil, err
	}
	go k.run(ctx)
	return k, nil
}

// touch rewrites the marker with the current time: the freshness a reader sees.
func (k *markerKeeper) touch(ctx context.Context) error {
	return k.store.SetProxyMarker(ctx, store.ProxyMarker{
		PID:      os.Getpid(),
		Port:     k.port,
		RootPath: k.root,
		TS:       time.Now().Unix(),
	})
}

// run is the heartbeat: it keeps the marker younger than three of its own periods, so a
// SIGKILLed proxy's marker goes stale on its own.
func (k *markerKeeper) run(ctx context.Context) {
	defer close(k.done)
	t := time.NewTicker(store.ProxyHeartbeat)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if err := k.touch(ctx); err != nil {
				fmt.Fprintf(k.diag, "serve: refreshing the marker: %v\n", err)
			}
		}
	}
}

// stop ends the heartbeat, clears the marker and closes the store. A second call is a no-op.
func (k *markerKeeper) stop() {
	k.once.Do(func() {
		k.cancel()
		<-k.done
		_ = k.store.ClearProxyMarker(context.Background())
		_ = k.store.Close()
	})
}
