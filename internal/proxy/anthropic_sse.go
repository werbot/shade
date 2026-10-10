package proxy

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"

	"github.com/werbot/shade/internal/jsonwalk"
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
// dropped (a raw token must never reach the client) and the loss is reported on diag.
func (s *Server) pipeAnthropicSSE(ctx context.Context, e Engine, mode string, src io.Reader, dst streamDst) error {
	br := bufio.NewReader(src)
	fl := placeholder.NewFlusher()
	// tools maps a tool_use block's index to the partial_json accumulated for it. A nil
	// value marks a block that has started but has no arguments yet.
	tools := make(map[json.Number][]byte)
	// bs is non-nil only in buffered mode, where text is collected per block and restored as
	// one piece at the block's stop.
	bs := s.newBufferedState(mode)

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
			if err == io.EOF {
				return io.ErrUnexpectedEOF
			}
			return err
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
// message_delta, content_block_start, an unknown event — go on byte-for-byte. bs is the
// buffered-mode state, nil in incremental mode.
func (s *Server) handleSSEFrame(ctx context.Context, e Engine, fl *placeholder.Flusher, tools map[json.Number][]byte, bs *bufferedState, frame sseFrame, dst streamDst) error {
	switch frame.event {
	case "content_block_start":
		s.startToolBlock(frame, tools)
		return writeRaw(dst, frame.raw)
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

// startToolBlock records the index of a tool_use block so its arguments can be buffered. A
// text or unknown block needs no accumulator and is forwarded by the caller.
func (s *Server) startToolBlock(frame sseFrame, tools map[json.Number][]byte) {
	data, ok := parseFrameData(frame)
	if !ok {
		return
	}
	block, _ := data["content_block"].(map[string]any)
	if typ, _ := block["type"].(string); typ != "tool_use" {
		return
	}
	if idx, ok := data["index"].(json.Number); ok {
		tools[idx] = nil
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

// handleTextDelta runs the piece through the flusher and restores whatever is safe to emit.
// An empty result means the piece is held for a possible placeholder, and no frame is sent:
// an empty text_delta would only be noise.
func (s *Server) handleTextDelta(ctx context.Context, e Engine, fl *placeholder.Flusher, data, delta map[string]any, frame sseFrame, dst streamDst) error {
	text, _ := delta["text"].(string)
	out := fl.Write(text)
	if out == "" {
		return nil
	}
	res, err := e.Restore(ctx, out)
	restored := out
	if err != nil {
		// Incremental streaming cannot fail closed (spec §7): the bytes are already on their
		// way, so they go on as they came and the failure is only reported.
		fmt.Fprintf(s.opts.Diag, "proxy: restore failed: %v\n", err)
	} else {
		s.reportRecordErr(res.RecordErr)
		restored = res.Text
	}
	delta["text"] = restored
	body, err := marshalNoEscape(data)
	if err != nil {
		return writeRaw(dst, frame.raw)
	}
	return writeEvent(dst, frame.event, body)
}

// accumulateToolDelta appends a partial_json fragment to the block's buffer. The frames are
// never forwarded: the arguments are only safe once the whole JSON is parsed and restored.
func (s *Server) accumulateToolDelta(data, delta map[string]any, tools map[json.Number][]byte) {
	idx, ok := data["index"].(json.Number)
	if !ok {
		return
	}
	partial, _ := delta["partial_json"].(string)
	tools[idx] = append(tools[idx], partial...)
}

// finishToolBlock emits one input_json_delta with the restored arguments, immediately before
// the content_block_stop. Nothing is emitted for a block that buffered no arguments.
func (s *Server) finishToolBlock(ctx context.Context, e Engine, tools map[json.Number][]byte, frame sseFrame, dst streamDst) error {
	data, ok := parseFrameData(frame)
	if !ok {
		return nil
	}
	idx, ok := data["index"].(json.Number)
	if !ok {
		return nil
	}
	partial, ok := tools[idx]
	if !ok {
		return nil
	}
	delete(tools, idx)
	if len(partial) == 0 {
		// The block carried no arguments: its input is already the start frame's {}.
		return nil
	}
	out := map[string]any{
		"type":  "content_block_delta",
		"index": idx,
		"delta": map[string]any{"type": "input_json_delta", "partial_json": s.restoreToolArgs(ctx, e, partial)},
	}
	body, err := marshalNoEscape(out)
	if err != nil {
		return nil
	}
	return writeEvent(dst, "content_block_delta", body)
}

// restoreToolArgs parses the accumulated arguments and restores every string inside, which
// is the only way an escaped token such as <HOST_1> is seen: a text edit of
// partial_json would miss it (Review Focus 3). Arguments that do not parse still go on as
// they arrived rather than being dropped, and the failure is reported without its text — a
// parse error can quote a character of a value.
func (s *Server) restoreToolArgs(ctx context.Context, e Engine, partial []byte) string {
	dec := json.NewDecoder(bytes.NewReader(partial))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		fmt.Fprintf(s.opts.Diag, "proxy: tool arguments could not be restored\n")
		return string(partial)
	}
	var failed error
	out, _ := jsonwalk.RewriteValue(v, func(str string) (string, bool) {
		if failed != nil {
			return str, false
		}
		res, err := e.Restore(ctx, str)
		if err != nil {
			failed = err
			return str, false
		}
		s.reportRecordErr(res.RecordErr)
		return res.Text, res.Text != str
	})
	if failed != nil {
		fmt.Fprintf(s.opts.Diag, "proxy: restore failed: %v\n", failed)
	}
	body, err := marshalNoEscape(out)
	if err != nil {
		return string(partial)
	}
	return string(body)
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
