package proxy

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/werbot/shade/internal/config"
)

// bufferedState is the per-call state of buffered stream mode: the fail policy and the raw
// text collected per block index. It is nil in incremental mode, where text is emitted as it
// arrives and a refusal is impossible (spec §7).
type bufferedState struct {
	policy  string
	byIndex map[json.Number][]byte
}

// newBufferedState returns the buffered-mode state for a stream, or nil for incremental mode.
// The fail policy is the one the caller read for the whole request — the same read the
// non-streaming answer makes (server.go), so the two arms cannot drift.
func newBufferedState(cfg config.Config) *bufferedState {
	if cfg.StreamMode != config.StreamBuffered {
		return nil
	}
	return &bufferedState{policy: cfg.FailPolicy, byIndex: make(map[json.Number][]byte)}
}

// withheld is the number of bytes collected but never emitted, for the diagnostic line when a
// block's content_block_stop never arrived.
func (bs *bufferedState) withheld() int {
	n := 0
	for _, text := range bs.byIndex {
		n += len(text)
	}
	return n
}

// accumulateTextDelta appends a text fragment to the block's buffer. The frame is not
// forwarded: the block's text is emitted once, restored whole, at its content_block_stop.
func accumulateTextDelta(data, delta map[string]any, byIndex map[json.Number][]byte) {
	idx, ok := data["index"].(json.Number)
	if !ok {
		return
	}
	text, _ := delta["text"].(string)
	byIndex[idx] = append(byIndex[idx], text...)
}

// finishTextBlock emits the buffered text of one block as a single restored text_delta,
// immediately before the block's content_block_stop, so a content_block_delta still precedes
// the stop and message_delta. Under fail_closed an unresolved token refuses the whole answer
// with an error frame and blocked=true; fail_open_log lets the text through with the tokens
// that stayed, named on diag by refuseUnresolved. A hard engine failure follows the
// non-streaming answer: fail_open_log forwards the raw text, anything else refuses with a
// generic body naming no value.
func (s *Server) finishTextBlock(ctx context.Context, e Engine, bs *bufferedState, frame sseFrame, dst streamDst) (blocked bool, err error) {
	data, ok := parseFrameData(frame)
	if !ok {
		return false, nil
	}
	idx, ok := data["index"].(json.Number)
	if !ok {
		return false, nil
	}
	text, ok := bs.byIndex[idx]
	if !ok {
		return false, nil
	}
	delete(bs.byIndex, idx)
	if len(text) == 0 {
		return false, nil
	}
	res, rerr := e.Restore(ctx, string(text))
	if rerr != nil {
		fmt.Fprintf(s.opts.Diag, "proxy: restore failed: %v\n", rerr)
		if bs.policy == config.FailOpenLog {
			return false, emitTextDelta(dst, idx, string(text))
		}
		return true, writeEvent(dst, "error", errorBody("shade_unresolved", "the answer could not be restored"))
	}
	s.reportRecordErr(res.RecordErr)
	if blocked, reason := s.refuseUnresolved(ctx, e, bs.policy, res.Unresolved); blocked {
		return true, writeEvent(dst, "error", reason)
	}
	return false, emitTextDelta(dst, idx, res.Text)
}

// emitTextDelta writes the block's one restored text_delta, standing in for the deltas that
// were buffered.
func emitTextDelta(dst streamDst, idx json.Number, text string) error {
	out := map[string]any{
		"type":  "content_block_delta",
		"index": idx,
		"delta": map[string]any{"type": "text_delta", "text": text},
	}
	body, err := marshalNoEscape(out)
	if err != nil {
		return nil
	}
	return writeEvent(dst, "content_block_delta", body)
}
