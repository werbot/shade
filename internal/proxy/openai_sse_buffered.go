package proxy

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"

	"github.com/werbot/shade/internal/config"
)

// emitOpenAIPending writes the buffered content (buffered mode only) and the accumulated tool
// arguments as restored chunks, immediately before a choice's finish_reason frame. A refusal
// under fail_closed ends the stream: the refusal frame and [DONE] are already out and blocked=true
// stops the loop with no finish_reason frame.
func (s *Server) emitOpenAIPending(ctx context.Context, e Engine, bs *bufferedState, toolArgs map[openAIToolKey][]byte, dst streamDst) (blocked bool, err error) {
	if bs != nil {
		if blocked, err = s.emitBufferedOpenAIContent(ctx, e, bs, dst); err != nil || blocked {
			return blocked, err
		}
	}
	for _, key := range sortedToolKeys(toolArgs) {
		partial := toolArgs[key]
		delete(toolArgs, key)
		if len(partial) == 0 {
			continue
		}
		if err := emitOpenAIToolArgsChunk(dst, key, s.restoreToolArgs(ctx, e, partial)); err != nil {
			return false, err
		}
	}
	return false, nil
}

// emitBufferedOpenAIContent restores each choice's collected content and emits it as one chunk. An
// unresolved token under fail_closed refuses the whole answer; fail_open_log lets the text through
// with the tokens named on diag by refuseUnresolved. A hard engine failure follows the
// non-streaming answer: fail_open_log forwards the raw text, anything else refuses with a generic
// body naming no value.
func (s *Server) emitBufferedOpenAIContent(ctx context.Context, e Engine, bs *bufferedState, dst streamDst) (bool, error) {
	for _, idx := range sortedChoiceIndexes(bs.byIndex) {
		text := bs.byIndex[idx]
		delete(bs.byIndex, idx)
		if len(text) == 0 {
			continue
		}
		res, rerr := e.Restore(ctx, string(text))
		if rerr != nil {
			fmt.Fprintf(s.opts.Diag, "proxy: restore failed: %v\n", rerr)
			if bs.policy == config.FailOpenLog {
				if err := emitOpenAIContentChunk(dst, idx, string(text)); err != nil {
					return false, err
				}
				continue
			}
			return true, writeOpenAIRefusal(dst, errorBody("shade_unresolved", "the answer could not be restored"))
		}
		s.reportRecordErr(res.RecordErr)
		if blocked, reason := s.refuseUnresolved(ctx, e, bs.policy, res.Unresolved); blocked {
			return true, writeOpenAIRefusal(dst, reason)
		}
		if err := emitOpenAIContentChunk(dst, idx, res.Text); err != nil {
			return false, err
		}
	}
	return false, nil
}

// emitOpenAIContentChunk writes one chunk carrying a choice's merged, restored content, standing
// in for the content deltas that were buffered.
func emitOpenAIContentChunk(dst streamDst, choice json.Number, text string) error {
	return writeOpenAIChunk(dst, choice, map[string]any{"content": text})
}

// emitOpenAIToolArgsChunk writes one chunk carrying a tool call's restored arguments, standing in
// for the argument fragments that were accumulated, placed before the finish_reason frame so the
// client has the whole arguments before it ends the call.
func emitOpenAIToolArgsChunk(dst streamDst, key openAIToolKey, args string) error {
	return writeOpenAIChunk(dst, key.choice, map[string]any{
		"tool_calls": []any{map[string]any{
			"index":    key.call,
			"function": map[string]any{"arguments": args},
		}},
	})
}

// writeOpenAIChunk builds one choice chunk with the given delta fields and sends it whole.
func writeOpenAIChunk(dst streamDst, choice json.Number, delta map[string]any) error {
	out := map[string]any{
		"choices": []any{map[string]any{
			"index":         choice,
			"delta":         delta,
			"finish_reason": nil,
		}},
	}
	body, err := marshalNoEscape(out)
	if err != nil {
		return nil
	}
	return writeData(dst, body)
}

// writeOpenAIRefusal ends a buffered stream with a refusal: one data frame carrying the refusal
// body, then the [DONE] terminator. OpenAI has no event: error, so the body is the frame.
func writeOpenAIRefusal(dst streamDst, body []byte) error {
	if err := writeData(dst, body); err != nil {
		return err
	}
	return writeData(dst, []byte("[DONE]"))
}

// sortedChoiceIndexes returns the accumulator's choice indexes in ascending order, so a stream
// with several choices emits them in order rather than in map order.
func sortedChoiceIndexes(byIndex map[json.Number][]byte) []json.Number {
	keys := make([]json.Number, 0, len(byIndex))
	for k := range byIndex {
		keys = append(keys, k)
	}
	slices.SortFunc(keys, compareNumber)
	return keys
}

// sortedToolKeys returns the tool-argument accumulator's keys by choice then call index.
func sortedToolKeys(toolArgs map[openAIToolKey][]byte) []openAIToolKey {
	keys := make([]openAIToolKey, 0, len(toolArgs))
	for k := range toolArgs {
		keys = append(keys, k)
	}
	slices.SortFunc(keys, func(a, b openAIToolKey) int {
		if c := compareNumber(a.choice, b.choice); c != 0 {
			return c
		}
		return compareNumber(a.call, b.call)
	})
	return keys
}

// compareNumber orders two choice or call indexes. They are small non-negative integers on the
// wire, so a string comparison would misorder 10 against 2.
func compareNumber(a, b json.Number) int {
	ai, _ := a.Int64()
	bi, _ := b.Int64()
	return int(ai - bi)
}
