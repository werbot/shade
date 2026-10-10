package proxy

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"

	"github.com/werbot/shade/internal/jsonwalk"
	"github.com/werbot/shade/internal/placeholder"
)

// errNotAJSONAnswer marks a body the walker cannot read as a JSON object: an event stream, a
// gateway's own page, an empty body. The handler forwards such a body unchanged. A failure
// from the engine is a different error — the store refused for a reason other than a missing
// value — and is answered by the fail policy instead.
var errNotAJSONAnswer = errors.New("not a json answer")

// restoreState collects what a walk of an answer produces: the tokens that could not be
// resolved, in the order they first appear, and whether any field was rewritten at all.
// The second is what lets an answer with nothing to restore come back byte-for-byte,
// rather than re-marshalled into different whitespace and key order.
type restoreState struct {
	unresolved []placeholder.Token
	changed    bool
}

// restoreAnthropic rewrites what goes back to the client and reports the tokens it could
// not resolve. The parsing is the one anonymizeAnthropic uses: UseNumber so a large id does
// not come back in exponential form, unknown fields and unknown block types left verbatim.
// The thinking and signature blocks are forwarded untouched for the same reason they are
// not anonymized, and an answer with nothing to restore comes back byte-for-byte.
//
// Two failures are told apart by the caller. A body that is not a JSON object wraps
// errNotAJSONAnswer and is forwarded unchanged. An engine failure is returned as is, together
// with the tokens collected so far, so the policy can refuse with their types; the error is
// never echoed to the client, since it can name a parse offset and spec §11 forbids leaking
// any of the body.
func (s *Server) restoreAnthropic(ctx context.Context, e Engine, body []byte) ([]byte, []placeholder.Token, error) {
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.UseNumber()
	var doc map[string]any
	if err := dec.Decode(&doc); err != nil {
		return nil, nil, fmt.Errorf("%w: %w", errNotAJSONAnswer, err)
	}
	if doc == nil {
		return nil, nil, fmt.Errorf("%w: body is not a json object", errNotAJSONAnswer)
	}
	if err := dec.Decode(new(any)); err != io.EOF {
		return nil, nil, fmt.Errorf("%w: trailing data after the json document", errNotAJSONAnswer)
	}

	var st restoreState
	if err := s.restoreContent(ctx, e, doc, &st); err != nil {
		return nil, st.unresolved, err
	}
	if !st.changed {
		return body, st.unresolved, nil
	}
	out, err := marshalNoEscape(doc)
	if err != nil {
		return nil, st.unresolved, err
	}
	return out, st.unresolved, nil
}

// restoreContent restores an answer's content. The Messages API defines it as an array of
// blocks, and a block of a type this build does not know is left whole, so a future block type
// reaches the client untouched rather than mangled — the same choice the request walker makes.
// A bare string is accepted as well: it is a form the request walker already understands, so a
// gateway that answers with one must not reach the client with a raw token in it.
func (s *Server) restoreContent(ctx context.Context, e Engine, doc map[string]any, st *restoreState) error {
	if text, ok := doc["content"].(string); ok {
		out, err := s.restoreText(ctx, e, text, st)
		if err != nil {
			return err
		}
		doc["content"] = out
		st.changed = st.changed || out != text
		return nil
	}
	blocks, _ := doc["content"].([]any)
	for _, b := range blocks {
		if err := s.restoreBlock(ctx, e, b, st); err != nil {
			return err
		}
	}
	return nil
}

// restoreBlock restores one content block by its type. thinking/signature, image, document
// and every unknown type are skipped, exactly as on the way in: rewriting a signature
// invalidates it, and the bytes inside an image or document are not text.
func (s *Server) restoreBlock(ctx context.Context, e Engine, v any, st *restoreState) error {
	block, ok := v.(map[string]any)
	if !ok {
		return nil
	}
	switch typ, _ := block["type"].(string); typ {
	case "text":
		return s.restoreField(ctx, e, block, "text", st)
	case "tool_use":
		return s.restoreToolInput(ctx, e, block, st)
	}
	return nil
}

// restoreToolInput walks tool_use.input whole: the arguments were written by the model, so
// every string inside can hold a token, at any depth.
func (s *Server) restoreToolInput(ctx context.Context, e Engine, block map[string]any, st *restoreState) error {
	in, ok := block["input"]
	if !ok {
		return nil
	}
	var failed error
	out, changed := jsonwalk.RewriteValue(in, func(str string) (string, bool) {
		if failed != nil {
			return str, false
		}
		res, err := e.Restore(ctx, str)
		if err != nil {
			failed = err
			return str, false
		}
		s.reportRecordErr(res.RecordErr)
		st.unresolved = append(st.unresolved, res.Unresolved...)
		return res.Text, res.Text != str
	})
	if failed != nil {
		return failed
	}
	st.changed = st.changed || changed
	block["input"] = out
	return nil
}

// restoreField restores one string field in place, leaving it untouched when it is absent
// or not a string.
func (s *Server) restoreField(ctx context.Context, e Engine, obj map[string]any, key string, st *restoreState) error {
	str, ok := obj[key].(string)
	if !ok {
		return nil
	}
	out, err := s.restoreText(ctx, e, str, st)
	if err != nil {
		return err
	}
	obj[key] = out
	st.changed = st.changed || out != str
	return nil
}

// restoreText runs one text through the engine, collecting the tokens it could not resolve
// and reporting a failed auxiliary write. A RecordErr is not fatal: the restored text is
// ready and matters more than a journal row.
func (s *Server) restoreText(ctx context.Context, e Engine, text string, st *restoreState) (string, error) {
	res, err := e.Restore(ctx, text)
	if err != nil {
		return "", err
	}
	s.reportRecordErr(res.RecordErr)
	st.unresolved = append(st.unresolved, res.Unresolved...)
	return res.Text, nil
}
