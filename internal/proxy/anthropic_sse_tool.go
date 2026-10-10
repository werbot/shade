package proxy

// The tool_use side of the Anthropic stream: a tool call's arguments are buffered whole and
// restored before the client sees them, because a raw token in partial_json would be executed
// rather than shown (spec §7).

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"

	"github.com/werbot/shade/internal/jsonwalk"
)

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
