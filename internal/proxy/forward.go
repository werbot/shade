package proxy

// The forwarding path: what every handler does once it has matched its path. It is one file
// because it is one path — read, anonymize, send, hand back — and the format-specific parts it
// calls are named by the shape, not branched on here.

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/werbot/shade/internal/config"
	"github.com/werbot/shade/internal/placeholder"
)

// shape is the format-specific steps the shared forward runs: how a request body is
// anonymized, how an answer is restored, how a stream is piped, and how a stream that broke
// is closed out. Anthropic and OpenAI differ only in these, so the forwarding, the error arms
// and the policy are one path, not two.
type shape struct {
	anonymize func(context.Context, Engine, []byte) ([]byte, error)
	restore   func(context.Context, Engine, []byte) ([]byte, []placeholder.Token, error)
	pipe      func(context.Context, Engine, config.Config, io.Reader, streamDst) error
	fail      func(streamDst, []byte) error
}

// anthropicShape is the Anthropic Messages format.
func (s *Server) anthropicShape() shape {
	return shape{anonymize: s.anonymizeAnthropic, restore: s.restoreAnthropic, pipe: s.pipeAnthropicSSE, fail: failAnthropicStream}
}

// openaiShape is the OpenAI Chat Completions format.
func (s *Server) openaiShape() shape {
	return shape{anonymize: s.anonymizeOpenAI, restore: s.restoreOpenAI, pipe: s.pipeOpenAISSE, fail: failOpenAIStream}
}

// failAnthropicStream closes a broken Anthropic stream with the terminal error frame of the
// protocol. The frame is the same one a refusal writes, so the client reads both the same way.
func failAnthropicStream(dst streamDst, body []byte) error { return writeEvent(dst, "error", body) }

// failOpenAIStream closes a broken OpenAI stream with the refusal frame and its [DONE] — the
// form writeOpenAIRefusal already emits, since the format has no event: error of its own.
func failOpenAIStream(dst streamDst, body []byte) error { return writeOpenAIRefusal(dst, body) }

// forward is what every handler does: read the body, anonymize it with the directive injected,
// send it upstream and hand the answer back with the real values put in. A body that cannot be
// anonymized is refused with a 502 that names no client text (spec §11) and never reaches the
// upstream.
//
// A non-success status is sanitized: its body is anonymized rather than cut, so the wording
// a client retries on survives while no value travels in it. A success is either an event
// stream, which is rewritten frame by frame with a safe-boundary flush, or a JSON answer
// whose values are restored. An answer that is neither — a gateway's own page — is handed on
// unmodified. A restore the engine itself failed is not: the policy decides, so a broken store
// never silently hands the client an answer that may still hold placeholders.
func (s *Server) forward(w http.ResponseWriter, r *http.Request, sh shape) {
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

	anon, err := sh.anonymize(ctx, eng, body)
	if err != nil {
		writeShadeError(w, "request body could not be anonymized")
		return
	}
	// The config is read once for the whole request: the upstream, the credential, the fail
	// policy and the stream mode all come out of it, so no two arms can read different
	// values. A config that cannot be read is a request that never goes out — and it fails
	// closed by never reaching a policy at all.
	cfg, err := config.Load(s.opts.Home, s.opts.Project)
	if err != nil {
		writeShadeError(w, "the configuration could not be read")
		return
	}
	req, err := s.upstreamRequest(ctx, r, anon, cfg)
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
		s.streamSSE(ctx, eng, sh, cfg, resp, w)
		return
	}
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		writeShadeError(w, "upstream answer could not be read")
		return
	}
	restored, unresolved, err := sh.restore(ctx, eng, raw)
	if errors.Is(err, errNotAJSONAnswer) {
		// Not a JSON object — a stream, or a gateway's own page. It is not this arm's to
		// rewrite, so it goes on as it arrived. The line says so: under fail_closed the
		// policy promised a refusal for a token that could not be restored, and this arm
		// neither restores nor refuses, so silence here would read as "nothing was found".
		fmt.Fprintf(s.opts.Diag, "proxy: the answer is not a json object (%s, %d bytes): forwarded as it came\n",
			resp.Header.Get("Content-Type"), len(raw))
		writeUpstreamAnswer(w, resp, raw)
		return
	}
	if err != nil {
		// The engine refused, not the parser. The answer may still hold placeholders, so the
		// policy decides: fail_open_log hands it on as it arrived, fail_closed refuses with
		// the types collected before the failure, or a generic line when there are none.
		fmt.Fprintf(s.opts.Diag, "proxy: restore failed: %v\n", err)
		if cfg.FailPolicy == config.FailOpenLog {
			writeUpstreamAnswer(w, resp, raw)
			return
		}
		blocked, reason := s.refuseUnresolved(ctx, eng, cfg.FailPolicy, unresolved)
		if !blocked {
			reason = errorBody("shade_unresolved", "the answer could not be restored")
		}
		writeErrorBody(w, reason)
		return
	}
	if blocked, reason := s.refuseUnresolved(ctx, eng, cfg.FailPolicy, unresolved); blocked {
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

// streamSSE hands an event stream to the client frame by frame. The status and headers go out
// first, so once the first frame is written the answer can no longer be replaced by an error: a
// failure past that point is reported in-band, by the format's own terminal frame. sh.pipe is
// the format's stream rewriter; a refusal and a complete stream both leave it with nothing to
// add.
func (s *Server) streamSSE(ctx context.Context, eng Engine, sh shape, cfg config.Config, resp *http.Response, w http.ResponseWriter) {
	copyResponseHeaders(w, resp.Header)
	w.WriteHeader(resp.StatusCode)
	dst, ok := w.(streamDst)
	if !ok {
		// Every net/http server and httptest recorder implements Flush; a writer that does
		// not simply buffers the stream rather than losing it.
		dst = nopFlushWriter{w}
	}
	dst.Flush()
	if err := sh.pipe(ctx, eng, cfg, resp.Body, dst); errors.Is(err, errStreamBroken) {
		// The upstream stopped before its terminator: the answer is cut off mid-flight, and a
		// client that only sees the stream stop does not retry it. So the break is said out
		// loud, in the frame the format reserves for a failure, carrying the refusal body
		// and nothing else.
		_ = sh.fail(dst, errorBody("shade_stream_incomplete", "the upstream stream ended before it was complete"))
	}
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
