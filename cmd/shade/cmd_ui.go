package main

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"os"
	"os/signal"
	"syscall"

	"github.com/werbot/shade/internal/api"
	"github.com/werbot/shade/internal/core"
	"github.com/werbot/shade/internal/store"
	"github.com/werbot/shade/web"
)

// defaultUIPort is the port `shade ui` listens on when --port is not given.
const defaultUIPort = 8788

func init() {
	Register(Command{
		Name: "ui",
		Help: "serve the web UI on localhost",
		Run:  runUI,
	})
}

// runUI serves the web UI — the REST API and the built page — on the loopback interface. It
// follows serve in resolving the project once, before serving, and differs in what it runs: no
// proxy and no liveness marker, so the one line stdout carries is the address to open in a
// browser.
func runUI(args []string, stdio IO) int {
	la, code, err := parseListenArgs(args, defaultUIPort)
	if err != nil {
		return fail(stdio, "ui", code, err)
	}
	// A binary without the page has nothing to serve, so it fails before it binds a port.
	if err := pageBuilt(web.Dist()); err != nil {
		return fail(stdio, "ui", 1, err)
	}

	ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", la.port))
	if err != nil {
		return fail(stdio, "ui", 1, err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	open := func(ctx context.Context) (*core.Engine, error) {
		return core.New(ctx, store.Home(), la.project, "ui")
	}
	// The guard takes the port the listener bound: with --port 0 the requested one is 0, and the
	// Host check would refuse every request.
	h := api.New(api.Options{Port: portOf(ln), Static: web.Dist(), Open: open})

	fmt.Fprintf(stdio.Out, "http://127.0.0.1:%d\n", portOf(ln))

	if err := serveHTTP(ctx, ln, h); err != nil {
		return fail(stdio, "ui", 1, err)
	}
	return 0
}

// pageBuilt reports whether the embedded page exists. A checkout that was never built embeds
// .gitkeep only, so a missing index.html is the sign, and the error says how to build the page.
func pageBuilt(static fs.FS) error {
	if _, err := fs.Stat(static, "index.html"); err != nil {
		return errors.New("the web UI is not built into this binary; run make build")
	}
	return nil
}
