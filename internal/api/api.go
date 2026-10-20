// Package api is the HTTP handler of `shade ui`: the REST endpoints for the rules and the built
// page, behind the Guard that keeps other sites off the loopback port. Every request opens its
// own engine, the way proxy and mcp do, so a rule written by the CLI is seen at once and a rule
// written here is seen by the hook and the proxy on their next call: the SQLite store is shared.
package api

import (
	"context"
	"io/fs"
	"mime"
	"net/http"

	"github.com/werbot/shade/internal/core"
)

// Opener opens an engine for one request; the handler closes it.
type Opener func(ctx context.Context) (*core.Engine, error)

// Options configure New.
type Options struct {
	Port   int   // the port the listener bound; the Host check allows exactly this one
	Static fs.FS // the built page, rooted at index.html
	Open   Opener
}

// New builds the handler: the API routes and the page, both behind the Guard.
func New(o Options) http.Handler {
	s := &server{open: o.Open}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/rules", s.listRules)
	mux.HandleFunc("POST /api/rules", s.createRule)
	mux.HandleFunc("PATCH /api/rules/{name}", s.toggleRule)
	mux.HandleFunc("DELETE /api/rules/{name}", s.deleteRule)
	mux.HandleFunc("POST /api/test", s.testText)
	// Any other request under /api/ is the API's own 404, in JSON like every other answer. It takes
	// every method, so a preflight and a PUT end here too, with no Access-Control-* header.
	mux.HandleFunc("/api/", func(w http.ResponseWriter, _ *http.Request) {
		writeError(w, http.StatusNotFound, "not found")
	})
	mux.Handle("/", page(o.Static))
	return Guard(o.Port, mux)
}

// pageTypes are the media types the page cannot start without. FileServerFS takes the type of a
// file from mime.TypeByExtension, and the host's own tables (mime.types files, the Windows
// registry) override its built-in ones, so on some machine a script comes as text/plain. The page
// is served under X-Content-Type-Options: nosniff, where a browser refuses a script or a style
// sheet of another type, so page pins these and the answer is the same on every machine.
//
// ponytail: only what the build emits today; add an extension here when it emits another kind
// of file (a font, an icon), or the host's table decides its type.
var pageTypes = map[string]string{
	".html": "text/html; charset=utf-8",
	".js":   "text/javascript; charset=utf-8",
	".css":  "text/css; charset=utf-8",
}

// page serves the built page and nothing else. It is registered as "/" and checks the method
// itself: "GET /" and "/api/" overlap on every GET under /api/ and neither is the more specific,
// so registering both panics, and FileServerFS answers an OPTIONS with the file and a 200.
func page(files fs.FS) http.Handler {
	for ext, typ := range pageTypes {
		// The mime table belongs to the process, and nothing else in shade serves these extensions.
		// The types are constants that parse, so the error cannot happen.
		_ = mime.AddExtensionType(ext, typ)
	}
	fileServer := http.FileServerFS(files)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		fileServer.ServeHTTP(w, r)
	})
}

// server holds what the handlers share: where an engine comes from.
type server struct{ open Opener }

// engine opens the engine of one request. When it cannot, the 500 is answered already and the
// handler only returns; otherwise the handler closes the engine when it is done.
func (s *server) engine(w http.ResponseWriter, r *http.Request) (*core.Engine, bool) {
	e, err := s.open(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "cannot open the engine: "+err.Error())
		return nil, false
	}
	return e, true
}
