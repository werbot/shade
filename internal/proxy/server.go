// Package proxy is the fourth adapter beside hook and MCP: an HTTP proxy that stands
// between a client and a model provider, anonymizing the request on the way out and
// restoring the real values on the way back. It is what `shade serve` runs.
package proxy

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/werbot/shade/internal/config"
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
// directive injected before it goes out; the answer is handed back with the real values put
// in, an event stream frame by frame.
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
// directive injected, send it upstream and hand the answer back with the real values put
// in. A body that cannot be anonymized is refused with a 502 that names no client text
// (spec §11) and never reaches the upstream.
//
// A non-success status is sanitized: its body is anonymized rather than cut, so the wording
// a client retries on survives while no value travels in it. A success is either an event
// stream, which is rewritten frame by frame with a safe-boundary flush, or a JSON answer
// whose values are restored. An answer that is neither — a gateway's own page — is handed on
// unmodified. A restore the engine itself failed is not: the policy decides, so a broken store
// never silently hands the client an answer that may still hold placeholders.
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

	if resp.StatusCode >= 300 {
		raw, err := io.ReadAll(resp.Body)
		if err != nil {
			writeShadeError(w, "upstream answer could not be read")
			return
		}
		writeUpstreamAnswer(w, resp, s.sanitizeError(ctx, eng, resp.StatusCode, raw))
		return
	}
	// A stream is decided by the response's media type, not the request's: the request is
	// always application/json, and stream:true only shows up in the answer.
	if isEventStream(resp.Header.Get("Content-Type")) {
		s.streamAnthropic(ctx, eng, resp, w)
		return
	}
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		writeShadeError(w, "upstream answer could not be read")
		return
	}
	// The policy is read before the restore, so an engine failure can be answered under it.
	// It is read here rather than carried from upstreamRequest, which loads the same config:
	// a config that cannot be read here could not have been read there either, so the request
	// would have failed before an answer arrived. A second read only fails on a race with an
	// edit on disk, and then an empty policy fails closed, which is safe.
	var policy string
	if cfg, err := config.Load(s.opts.Home, s.opts.Project); err == nil {
		policy = cfg.FailPolicy
	}
	restored, unresolved, err := s.restoreAnthropic(ctx, eng, raw)
	if errors.Is(err, errNotAJSONAnswer) {
		// Not a JSON object — a stream, or a gateway's own page. It is not this arm's to
		// rewrite, so it goes on as it arrived.
		writeUpstreamAnswer(w, resp, raw)
		return
	}
	if err != nil {
		// The engine refused, not the parser. The answer may still hold placeholders, so the
		// policy decides: fail_open_log hands it on as it arrived, fail_closed refuses with
		// the types collected before the failure, or a generic line when there are none.
		fmt.Fprintf(s.opts.Diag, "proxy: restore failed: %v\n", err)
		if policy == config.FailOpenLog {
			writeUpstreamAnswer(w, resp, raw)
			return
		}
		blocked, reason := s.refuseUnresolved(ctx, eng, policy, unresolved)
		if !blocked {
			reason = errorBody("shade_unresolved", "the answer could not be restored")
		}
		writeErrorBody(w, reason)
		return
	}
	if blocked, reason := s.refuseUnresolved(ctx, eng, policy, unresolved); blocked {
		writeErrorBody(w, reason)
		return
	}
	writeUpstreamAnswer(w, resp, restored)
}

// writeUpstreamAnswer hands the upstream's status and headers to the client along with body.
func writeUpstreamAnswer(w http.ResponseWriter, resp *http.Response, body []byte) {
	copyResponseHeaders(w, resp.Header)
	w.WriteHeader(resp.StatusCode)
	_, _ = w.Write(body)
}

// isEventStream reports whether the upstream answered with an event stream. The media type
// may carry parameters (charset), so only its prefix is compared.
func isEventStream(contentType string) bool {
	return strings.HasPrefix(strings.ToLower(strings.TrimSpace(contentType)), "text/event-stream")
}

// streamAnthropic hands an event stream to the client frame by frame. The status and headers
// go out first, so once the first frame is written the answer can no longer be replaced by an
// error: a failure past that point is reported by pipeAnthropicSSE where it can be (an early
// end) and the connection is left as it is.
func (s *Server) streamAnthropic(ctx context.Context, eng Engine, resp *http.Response, w http.ResponseWriter) {
	copyResponseHeaders(w, resp.Header)
	w.WriteHeader(resp.StatusCode)
	dst, ok := w.(streamDst)
	if !ok {
		// Every net/http server and httptest recorder implements Flush; a writer that does
		// not simply buffers the stream rather than losing it.
		dst = nopFlushWriter{w}
	}
	dst.Flush()
	_ = s.pipeAnthropicSSE(ctx, eng, resp.Body, dst)
}

// nopFlushWriter adapts a ResponseWriter that does not implement Flush.
type nopFlushWriter struct{ io.Writer }

func (nopFlushWriter) Flush() {}

// copyResponseHeaders copies the upstream's headers. Content-Length is left out: every arm
// rewrites the body, so the upstream's length no longer describes what is written and a
// stale one would truncate the answer on a real server.
func copyResponseHeaders(w http.ResponseWriter, h http.Header) {
	for k, vs := range h {
		if http.CanonicalHeaderKey(k) == "Content-Length" {
			continue
		}
		for _, v := range vs {
			w.Header().Add(k, v)
		}
	}
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
