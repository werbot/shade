package proxy

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"

	"github.com/werbot/shade/internal/config"
	"github.com/werbot/shade/internal/placeholder"
)

// errRefused ends a buffered stream early: the collected text could not be restored and the
// policy refused the answer. The error frame is already written, so the caller stops without
// reporting an early end — the stream ended exactly where it was told to.
var errRefused = errors.New("proxy: stream refused")

// pipeAnthropicSSE rewrites an upstream event stream to the client. The text of a text_delta
// goes through a Flusher, so a placeholder split across frames is held at the last "<" and
// restored whole; a tool_use block's arguments are buffered whole and restored before the
// client sees them, because the raw token would otherwise be executed (spec §7).
//
// mode selects how text is relayed. In incremental mode each text_delta is restored and sent
// as it arrives, so a closed token of an unknown type has already reached the client before it
// is seen and the answer cannot be refused. In buffered mode the text of a block is collected
// and restored as one piece at its content_block_stop, before message_delta, so an unresolved
// token refuses the whole answer (spec §7) — the behaviour a non-interactive client wants.
//
// The flusher and the accumulators are local to this call: two requests never share them. dst
// is flushed after every frame, so ping reaches the client at once and an idle watchdog never
// ends the session. A stream that ends without message_stop is not silent: the held tail is
// dropped (a raw token must never reach the client) and the loss is reported on diag, and the
// caller is told the stream broke so it can say so to the client in the protocol's own frame.
func (s *Server) pipeAnthropicSSE(ctx context.Context, e Engine, cfg config.Config, src io.Reader, dst streamDst) error {
	br := bufio.NewReader(src)
	fl := placeholder.NewFlusher()
	// tools maps a tool_use block's index to the partial_json accumulated for it. A nil
	// value marks a block that has started but has no arguments yet.
	tools := make(map[json.Number][]byte)
	// bs is non-nil only in buffered mode, where text is collected per block and restored as
	// one piece at the block's stop.
	bs := newBufferedState(cfg)

	for {
		frame, err := readFrame(br)
		if err != nil {
			// No message_stop: the answer is incomplete. The held tail cannot go out — an
			// unclosed "<" is never flushed (spec §7) — so it is dropped and the loss is made
			// visible instead of silent.
			if _, withheld := fl.Flush(); withheld > 0 {
				fmt.Fprintf(s.opts.Diag, "proxy: %d bytes withheld at stream end\n", withheld)
			}
			fmt.Fprintf(s.opts.Diag, "proxy: stream ended early: %v\n", err)
			return errStreamBroken
		}
		if err := s.handleSSEFrame(ctx, e, fl, tools, bs, frame, dst); err != nil {
			// A refusal is not a broken stream: the error frame is already out and the answer
			// ended exactly where it was told to, so it is not reported as an early end.
			if errors.Is(err, errRefused) {
				return nil
			}
			return err
		}
		// message_stop and error both end the stream. An error event is terminal in the
		// protocol: the read loop stops here rather than reading to EOF and reporting an early
		// end that never happened, and no message_stop is emitted after it.
		if frame.event == "message_stop" || frame.event == "error" {
			return nil
		}
	}
}

// handleSSEFrame dispatches one event. Events the pipe does not rewrite — ping, message_start,
// message_delta, an unknown event — go on byte-for-byte; a content_block_start goes on whole
// unless it carries text of its own. bs is the buffered-mode state, nil in incremental mode.
func (s *Server) handleSSEFrame(ctx context.Context, e Engine, fl *placeholder.Flusher, tools map[json.Number][]byte, bs *bufferedState, frame sseFrame, dst streamDst) error {
	switch frame.event {
	case "content_block_start":
		s.startToolBlock(frame, tools)
		return s.startTextBlock(ctx, e, fl, bs, frame, dst)
	case "content_block_delta":
		return s.handleBlockDelta(ctx, e, fl, tools, bs, frame, dst)
	case "content_block_stop":
		// In buffered mode the block's collected text is restored and emitted just before its
		// stop, so a content_block_delta still precedes the stop and message_delta.
		if bs != nil {
			blocked, err := s.finishTextBlock(ctx, e, bs, frame, dst)
			if err != nil {
				return err
			}
			if blocked {
				return errRefused
			}
		}
		if err := s.finishToolBlock(ctx, e, tools, frame, dst); err != nil {
			return err
		}
		return writeRaw(dst, frame.raw)
	case "message_stop":
		// Text still held cannot be completed now: the flusher's tail from a possible
		// placeholder, and in buffered mode a block whose deltas arrived without a
		// content_block_stop. Neither goes to the client — a raw token never may — and the
		// loss is reported.
		withheld := 0
		if _, n := fl.Flush(); n > 0 {
			withheld += n
		}
		if bs != nil {
			withheld += bs.withheld()
		}
		if withheld > 0 {
			fmt.Fprintf(s.opts.Diag, "proxy: %d bytes withheld at message_stop\n", withheld)
		}
		return writeRaw(dst, frame.raw)
	case "error":
		return s.handleErrorEvent(ctx, e, frame, dst)
	default:
		return writeRaw(dst, frame.raw)
	}
}

// handleBlockDelta routes a content_block_delta by its delta type. thinking_delta and
// signature_delta are forwarded untouched: rewriting either invalidates the signature.
func (s *Server) handleBlockDelta(ctx context.Context, e Engine, fl *placeholder.Flusher, tools map[json.Number][]byte, bs *bufferedState, frame sseFrame, dst streamDst) error {
	data, ok := parseFrameData(frame)
	if !ok {
		return writeRaw(dst, frame.raw)
	}
	delta, _ := data["delta"].(map[string]any)
	switch typ, _ := delta["type"].(string); typ {
	case "text_delta":
		// Buffered mode holds the text for the block's stop instead of emitting it now.
		if bs != nil {
			accumulateTextDelta(data, delta, bs.byIndex)
			return nil
		}
		return s.handleTextDelta(ctx, e, fl, data, delta, frame, dst)
	case "input_json_delta":
		s.accumulateToolDelta(data, delta, tools)
		return nil
	default:
		return writeRaw(dst, frame.raw)
	}
}

// startTextBlock relays the text a content_block_start may already carry. The Messages API
// opens a block with an empty string, so only a non-standard upstream sends text here — and a
// raw token in it would reach the client exactly as a token in a text_delta would. It goes
// through the same two paths: buffered mode adds it to the block's buffer, so the whole block
// is restored — and may be refused — at its stop, and incremental mode runs it through the
// flusher, so a placeholder that continues in the deltas after it is still restored whole.
func (s *Server) startTextBlock(ctx context.Context, e Engine, fl *placeholder.Flusher, bs *bufferedState, frame sseFrame, dst streamDst) error {
	data, ok := parseFrameData(frame)
	if !ok {
		return writeRaw(dst, frame.raw)
	}
	block, _ := data["content_block"].(map[string]any)
	if typ, _ := block["type"].(string); typ != "text" {
		return writeRaw(dst, frame.raw)
	}
	text, _ := block["text"].(string)
	if text == "" {
		return writeRaw(dst, frame.raw)
	}
	if bs != nil {
		// Buffered mode: the text joins the block's buffer, so the whole block is restored —
		// and may be refused — at its stop. The frame itself goes out with an empty text, as
		// the API defines it: this one is the exception where the frame cannot simply be
		// dropped, so the raw text must be taken out of it rather than left to ride along.
		accumulateTextDelta(data, block, bs.byIndex)
		block["text"] = ""
	} else {
		block["text"] = s.restoreStream(ctx, e, fl.Write(text))
	}
	// The frame goes out either way — the client needs the block it opens — but with only the
	// text that may leave now, so an unclosed "<" is held here rather than leaked.
	body, err := marshalNoEscape(data)
	if err != nil {
		// Unreachable for a document the decoder built from JSON, and the fallback the other
		// rewriters in this package use.
		return writeRaw(dst, frame.raw)
	}
	return writeEvent(dst, frame.event, body)
}

// handleTextDelta runs the piece through the flusher and restores whatever is safe to emit.
// An empty result means the piece is held for a possible placeholder, and no frame is sent:
// an empty text_delta would only be noise.
func (s *Server) handleTextDelta(ctx context.Context, e Engine, fl *placeholder.Flusher, data, delta map[string]any, frame sseFrame, dst streamDst) error {
	text, _ := delta["text"].(string)
	out := fl.Write(text)
	if out == "" {
		return nil
	}
	delta["text"] = s.restoreStream(ctx, e, out)
	body, err := marshalNoEscape(data)
	if err != nil {
		return writeRaw(dst, frame.raw)
	}
	return writeEvent(dst, frame.event, body)
}

// restoreStream restores a piece of text already on its way to the client. Incremental
// streaming cannot fail closed (spec §7): the bytes cannot be taken back, so an engine failure
// lets them go on as they came and is only reported.
func (s *Server) restoreStream(ctx context.Context, e Engine, out string) string {
	res, err := e.Restore(ctx, out)
	if err != nil {
		fmt.Fprintf(s.opts.Diag, "proxy: restore failed: %v\n", err)
		return out
	}
	s.reportRecordErr(res.RecordErr)
	return res.Text
}

// handleErrorEvent anonymizes the provider's message so a value in it cannot travel, while
// the wording a client retries on survives. It reuses sanitizeError; its fallback — a bare
// status line — is the same safe side a 502 body takes when anonymizing fails.
func (s *Server) handleErrorEvent(ctx context.Context, e Engine, frame sseFrame, dst streamDst) error {
	data, ok := parseFrameData(frame)
	if !ok {
		return writeRaw(dst, frame.raw)
	}
	errObj, ok := data["error"].(map[string]any)
	if !ok {
		return writeRaw(dst, frame.raw)
	}
	msg, ok := errObj["message"].(string)
	if !ok {
		return writeRaw(dst, frame.raw)
	}
	errObj["message"] = string(s.sanitizeError(ctx, e, http.StatusBadGateway, []byte(msg)))
	body, err := marshalNoEscape(data)
	if err != nil {
		return writeRaw(dst, frame.raw)
	}
	return writeEvent(dst, frame.event, body)
}
