// Package proxy is the fourth adapter beside hook and MCP: an HTTP proxy that stands
// between a client and a model provider, anonymizing the request on the way out and
// restoring the real values on the way back. It is what `shade serve` runs.
package proxy

import (
	"context"
	"errors"
	"io"
	"net/http"

	"github.com/werbot/shade/internal/core"
)

// The paths the proxy routes. Matching is on r.URL.Path, so the query Claude Code attaches
// (?beta=true) does not pick the handler: the same one serves the whole path. A trailing
// slash is not trimmed — the client sends these exact paths, and a near-miss that landed
// on the wrong handler would be worse than a 404.
const (
	pathMessages        = "/v1/messages"
	pathCountTokens     = "/v1/messages/count_tokens"
	pathChatCompletions = "/v1/chat/completions"
	pathHello           = "/api/hello"
)

// Engine is the part of the core the proxy needs. RecordBlocked is here, unlike the MCP
// server's engine: a 502 under fail_closed is a refusal, and the spec's data model gives
// a refusal a row in the journal. Unresolved tokens are journalled by core.Restore itself.
type Engine interface {
	Anonymize(ctx context.Context, text string) (core.Result, error)
	Restore(ctx context.Context, text string) (core.Result, error)
	RecordBlocked(ctx context.Context, typ string) error
	RootPath() string
	Close() error
}

// The interface above mirrors *core.Engine: a drift between the two must be a build
// failure, not a surprise in a handler.
var _ Engine = (*core.Engine)(nil)

// Opener brings up an engine for the project this server was started for. Like the hook
// and the MCP server, the proxy opens one per request: a long-lived process with a cached
// rule set would stop seeing `shade rules add` typed in another terminal.
type Opener func(ctx context.Context) (Engine, error)

// Options is everything that does not change between requests.
type Options struct {
	Home    string    // shade home: config, key, store
	Project string    // project directory, resolved once when the server starts
	Diag    io.Writer // diagnostics; never the client, never a value
}

// Server answers the client's requests. It holds only what is stable for the process: the
// engine seam, the resolved options and the upstream client.
type Server struct {
	opts   Options
	open   Opener
	client *http.Client
}

// NewServer builds the server and its upstream client. open must be non-nil: a proxy that
// cannot bring up an engine could only answer refusals, which is a mistake at wiring time,
// not at request time.
func NewServer(o Options, open Opener) (*Server, error) {
	if open == nil {
		return nil, errors.New("proxy: Opener is nil")
	}
	if o.Diag == nil {
		o.Diag = io.Discard
	}
	return &Server{
		opts: o,
		open: open,
		client: &http.Client{
			// No overall timeout: a streamed answer is long by nature. A redirect is a
			// refusal, not a reason to go round again: the base URL is documented to answer
			// at its exact address, so a 3xx means the gateway is not where it was said to be.
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
	}, nil
}

// ServeHTTP routes by path. The four known paths reach their handlers; anything else is a
// 404. The two Anthropic handlers anonymize and forward; chat_completions is still a
// skeleton, its turn arriving with the OpenAI task.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch r.URL.Path {
	case pathMessages:
		s.handleMessages(w, r)
	case pathCountTokens:
		s.handleCountTokens(w, r)
	case pathChatCompletions:
		s.handleChatCompletions(w, r)
	case pathHello:
		s.handleHello(w, r)
	default:
		http.NotFound(w, r)
	}
}

// handleMessages serves the Anthropic Messages endpoint. The body is anonymized and the
// directive injected before it goes out; the upstream's answer is handed back as it
// arrived. Restoration of the answer and the streaming pipe arrive with the later tasks.
func (s *Server) handleMessages(w http.ResponseWriter, r *http.Request) {
	s.forwardAnthropic(w, r)
}

// handleCountTokens serves the Anthropic token-counting endpoint. It shares the whole body
// of handleMessages: count_tokens differs only by the absent max_tokens, and the counter
// must count exactly what the real request would carry.
func (s *Server) handleCountTokens(w http.ResponseWriter, r *http.Request) {
	s.forwardAnthropic(w, r)
}

// forwardAnthropic is what both Anthropic handlers do: read the body, anonymize it with the
// directive injected, send it upstream and pass the answer back unmodified. A body that
// cannot be anonymized is refused with a 502 that names no client text (spec §11) and never
// reaches the upstream.
func (s *Server) forwardAnthropic(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	body, err := io.ReadAll(r.Body)
	if err != nil {
		writeShadeError(w, "request body could not be read")
		return
	}
	eng, err := s.open(ctx)
	if err != nil {
		writeShadeError(w, "engine could not be opened")
		return
	}
	defer eng.Close()

	anon, err := s.anonymizeAnthropic(ctx, eng, body)
	if err != nil {
		writeShadeError(w, "request body could not be anonymized")
		return
	}
	req, err := s.upstreamRequest(ctx, r, anon)
	if err != nil {
		writeShadeError(w, "upstream request could not be built")
		return
	}
	resp, err := s.client.Do(req)
	if err != nil {
		writeShadeError(w, "upstream request failed")
		return
	}
	defer resp.Body.Close()

	for k, vs := range resp.Header {
		for _, v := range vs {
			w.Header().Add(k, v)
		}
	}
	w.WriteHeader(resp.StatusCode)
	_, _ = io.Copy(w, resp.Body)
}

// apiError is the refusal body. The shape follows the Anthropic error object so a client
// reads it the same way, but the type is shade's own: the request never reached the
// provider.
type apiError struct {
	Type  string       `json:"type"`
	Error apiErrorBody `json:"error"`
}

// apiErrorBody is the "error" member of an apiError.
type apiErrorBody struct {
	Type    string `json:"type"`
	Message string `json:"message"`
}

// writeShadeError refuses the request with a fixed message. It names no client text —
// never a value, never a parse offset — because a refusal that quotes the body it refused
// is a leak (spec §11).
func writeShadeError(w http.ResponseWriter, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusBadGateway)
	body, err := marshalNoEscape(apiError{
		Type:  "error",
		Error: apiErrorBody{Type: "shade_error", Message: message},
	})
	if err != nil {
		return
	}
	_, _ = w.Write(body)
}

// handleChatCompletions serves the OpenAI Chat Completions endpoint.
func (s *Server) handleChatCompletions(w http.ResponseWriter, r *http.Request) {
	notImplemented(w)
}

// handleHello answers the probe Claude Code sends before its first request. The
// documentation allows rejecting it, but a 200 is one line and keeps the client happy.
func (s *Server) handleHello(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusOK)
}

// notImplemented is the body of a handler whose turn has not come yet. It is replaced
// whole by the task that implements the endpoint.
func notImplemented(w http.ResponseWriter) {
	http.Error(w, "proxy: not implemented", http.StatusNotImplemented)
}
