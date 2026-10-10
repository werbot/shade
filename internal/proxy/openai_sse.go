package proxy

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/werbot/shade/internal/config"
	"github.com/werbot/shade/internal/placeholder"
)

// openAIToolKey identifies one tool call's arguments: the choice index it sits under and its own
// index. The same call index can appear under different choices.
type openAIToolKey struct {
	choice json.Number
	call   json.Number
}

// pipeOpenAISSE rewrites an upstream chat-completions stream to the client. A delta.content goes
// through a Flusher, so a placeholder split across chunks is held at the last "<" and restored
// whole. A delta.tool_calls[].function.arguments fragment is accumulated per (choice, call) and
// emitted restored as one chunk immediately before the frame carrying the choice's
// finish_reason — OpenAI has no content_block_stop, so finish_reason is the end signal. The
// terminator frame data: [DONE] is forwarded last, and a usage chunk (sent only under
// stream_options.include_usage) passes through untouched.
//
// mode selects how content is relayed, exactly as on the Anthropic side: incremental emits it as
// it arrives, so a closed token of an unknown type has already reached the client before it is
// seen and the answer cannot be refused; buffered collects it per choice and emits one restored
// chunk before the finish_reason, so an unresolved token refuses the whole answer under
// fail_closed. A data line that is not [DONE] and not parseable JSON is forwarded as it came: a
// gateway's own page inside a stream must not be mangled.
//
// The flusher and the accumulators are local to this call: two requests never share them. dst is
// flushed after every frame, so an idle watchdog never ends the session. A stream that ends
// without [DONE] is not silent: the held tail is dropped (a raw token must never reach the
// client) and the loss is reported on diag, and the caller is told the stream broke so it can
// say so to the client in the format's own terminal frame.
func (s *Server) pipeOpenAISSE(ctx context.Context, e Engine, cfg config.Config, src io.Reader, dst streamDst) error {
	br := bufio.NewReader(src)
	fl := placeholder.NewFlusher()
	// bs is non-nil only in buffered mode; its byIndex holds each choice's collected content and
	// its policy decides a refusal.
	bs := newBufferedState(cfg)
	toolArgs := make(map[openAIToolKey][]byte)

	for {
		frame, err := readFrame(br)
		if err != nil {
			// No [DONE]: the answer is incomplete. The held tail cannot go out, so it is dropped
			// and the loss is made visible instead of silent.
			s.flushOpenAIWithheld(fl, bs, "at stream end")
			fmt.Fprintf(s.opts.Diag, "proxy: stream ended early: %v\n", err)
			return errStreamBroken
		}
		if isOpenAIDone(frame) {
			// The terminator ends the content stream: the held tail is dropped and reported, and
			// [DONE] goes out last.
			s.flushOpenAIWithheld(fl, bs, "at [DONE]")
			return writeRaw(dst, frame.raw)
		}
		data, ok := parseFrameData(frame)
		if !ok {
			// Not a JSON object — a gateway's own page inside the stream. Forward as it came.
			if err := writeRaw(dst, frame.raw); err != nil {
				return err
			}
			continue
		}
		out, err := s.rewriteOpenAIChunk(ctx, e, fl, bs, toolArgs, data, dst)
		if err != nil {
			// A refusal is not a broken stream: the refusal frame and [DONE] are already out and
			// the stream ended exactly where it was told to.
			if errors.Is(err, errRefused) {
				return nil
			}
			return err
		}
		if out == nil {
			if err := writeRaw(dst, frame.raw); err != nil {
				return err
			}
			continue
		}
		// A rewritten frame is a bare JSON payload, so it goes back out as a data frame.
		if err := writeData(dst, out); err != nil {
			return err
		}
	}
}

// rewriteOpenAIChunk rewrites one parsed data frame and returns the bytes to write for it, or nil
// to forward the frame as it arrived. Content goes through the flusher (incremental) or into the
// choice's buffer (buffered); tool-call arguments are always accumulated. When a choice carries a
// non-empty finish_reason, the accumulated content and arguments are emitted, restored,
// immediately before it.
func (s *Server) rewriteOpenAIChunk(ctx context.Context, e Engine, fl *placeholder.Flusher, bs *bufferedState, toolArgs map[openAIToolKey][]byte, data map[string]any, dst streamDst) ([]byte, error) {
	choices, _ := data["choices"].([]any)
	changed, finish := false, false
	for _, c := range choices {
		choice, ok := c.(map[string]any)
		if !ok {
			continue
		}
		if delta, ok := choice["delta"].(map[string]any); ok {
			if accumulateOpenAIToolCalls(choice, delta, toolArgs) {
				changed = true
			}
			if bs != nil {
				if accumulateOpenAIContent(choice, delta, bs.byIndex) {
					changed = true
				}
			} else if s.rewriteOpenAIContent(ctx, e, fl, delta) {
				changed = true
			}
		}
		if fr, _ := choice["finish_reason"].(string); fr != "" {
			finish = true
		}
	}
	if finish {
		blocked, err := s.emitOpenAIPending(ctx, e, bs, toolArgs, dst)
		if err != nil {
			return nil, err
		}
		if blocked {
			return nil, errRefused
		}
	}
	if !changed {
		return nil, nil
	}
	return marshalNoEscape(data)
}

// rewriteOpenAIContent runs one delta.content through the flusher and restores what is safe to
// emit, writing it back into the delta. An empty result is written as an empty string rather than
// dropping the frame: the frame also carries the choice's role and id, which the client needs. It
// reports whether the delta changed, so an untouched frame is forwarded byte-for-byte.
func (s *Server) rewriteOpenAIContent(ctx context.Context, e Engine, fl *placeholder.Flusher, delta map[string]any) bool {
	text, ok := delta["content"].(string)
	if !ok {
		return false
	}
	out := fl.Write(text)
	restored := out
	if out != "" {
		restored = s.restoreStream(ctx, e, out)
	}
	delta["content"] = restored
	return restored != text
}

// accumulateOpenAIContent collects a delta.content into the choice's buffer and blanks it in the
// outgoing delta, so the raw text — which may hold a token — never leaves before the choice's
// finish_reason, where the whole text is emitted restored. It reports whether the delta changed.
func accumulateOpenAIContent(choice, delta map[string]any, content map[json.Number][]byte) bool {
	text, ok := delta["content"].(string)
	if !ok || text == "" {
		return false
	}
	delta["content"] = ""
	if idx, ok := choice["index"].(json.Number); ok {
		content[idx] = append(content[idx], text...)
	}
	return true
}

// accumulateOpenAIToolCalls buffers the argument fragments of every tool call in the delta and
// blanks them in the outgoing delta: the arguments are only safe once the whole JSON is parsed
// and restored. The id, name and type are left, so the client still learns which call this is. It
// reports whether the delta changed.
func accumulateOpenAIToolCalls(choice, delta map[string]any, toolArgs map[openAIToolKey][]byte) bool {
	calls, _ := delta["tool_calls"].([]any)
	if len(calls) == 0 {
		return false
	}
	choiceIdx, _ := choice["index"].(json.Number)
	changed := false
	for _, c := range calls {
		call, ok := c.(map[string]any)
		if !ok {
			continue
		}
		fn, _ := call["function"].(map[string]any)
		args, ok := fn["arguments"].(string)
		if !ok || args == "" {
			continue
		}
		fn["arguments"] = ""
		changed = true
		if callIdx, ok := call["index"].(json.Number); ok {
			key := openAIToolKey{choice: choiceIdx, call: callIdx}
			toolArgs[key] = append(toolArgs[key], args...)
		}
	}
	return changed
}

// writeData sends one data frame — a payload and its terminating blank line — and flushes. OpenAI
// frames carry no event: line.
func writeData(dst streamDst, payload []byte) error {
	frame := make([]byte, 0, len(payload)+8)
	frame = append(frame, "data: "...)
	frame = append(frame, payload...)
	frame = append(frame, "\n\n"...)
	if _, err := dst.Write(frame); err != nil {
		return err
	}
	dst.Flush()
	return nil
}

// isOpenAIDone reports whether a frame is the data: [DONE] terminator.
func isOpenAIDone(frame sseFrame) bool {
	return strings.TrimSpace(string(frame.data)) == "[DONE]"
}

// flushOpenAIWithheld ends the content stream: any held tail (an unclosed "<") and any collected
// buffered text are dropped — a raw token must never reach the client — and the loss is reported
// on diag. where names the point of the stream in the line.
func (s *Server) flushOpenAIWithheld(fl *placeholder.Flusher, bs *bufferedState, where string) {
	withheld := 0
	if _, n := fl.Flush(); n > 0 {
		withheld += n
	}
	if bs != nil {
		withheld += bs.withheld()
	}
	if withheld > 0 {
		fmt.Fprintf(s.opts.Diag, "proxy: %d bytes withheld %s\n", withheld, where)
	}
}
