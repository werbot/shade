package proxy

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"

	"github.com/werbot/shade/internal/jsonwalk"
	"github.com/werbot/shade/internal/placeholder"
)

// restoreOpenAI rewrites what goes back to the client and reports the tokens it could not
// resolve: choices[].message.content and the JSON string in each tool call's function.arguments.
// The parsing is the one anonymizeOpenAI uses, so an answer with nothing to restore comes back
// byte-for-byte rather than re-marshalled into different whitespace and key order.
//
// Two failures are told apart by the caller, exactly as on the Anthropic side. A body that is not
// a JSON object wraps errNotAJSONAnswer and is forwarded unchanged. An engine failure is returned
// as is, with the tokens collected so far, so the policy can refuse with their types.
func (s *Server) restoreOpenAI(ctx context.Context, e Engine, body []byte) ([]byte, []placeholder.Token, error) {
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
	if err := s.restoreOpenAIChoices(ctx, e, doc["choices"], &st); err != nil {
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

// restoreOpenAIChoices restores the message of every choice. An answer may carry several choices
// (n > 1); each is walked the same way.
func (s *Server) restoreOpenAIChoices(ctx context.Context, e Engine, v any, st *restoreState) error {
	choices, _ := v.([]any)
	for _, c := range choices {
		choice, ok := c.(map[string]any)
		if !ok {
			continue
		}
		msg, ok := choice["message"].(map[string]any)
		if !ok {
			continue
		}
		if err := s.restoreOpenAIContent(ctx, e, msg, st); err != nil {
			return err
		}
		if err := s.restoreOpenAIToolCalls(ctx, e, msg["tool_calls"], st); err != nil {
			return err
		}
	}
	return nil
}

// restoreOpenAIContent restores a message content: a plain string, or the text parts of an array.
// A part of a type this build does not know is left whole, the same choice the request walker
// makes.
func (s *Server) restoreOpenAIContent(ctx context.Context, e Engine, msg map[string]any, st *restoreState) error {
	switch c := msg["content"].(type) {
	case string:
		out, err := s.restoreText(ctx, e, c, st)
		if err != nil {
			return err
		}
		msg["content"] = out
		st.changed = st.changed || out != c
	case []any:
		for _, p := range c {
			part, ok := p.(map[string]any)
			if !ok {
				continue
			}
			if typ, _ := part["type"].(string); typ == "text" {
				if err := s.restoreField(ctx, e, part, "text", st); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

// restoreOpenAIToolCalls restores the arguments of every tool call in a message.
func (s *Server) restoreOpenAIToolCalls(ctx context.Context, e Engine, v any, st *restoreState) error {
	calls, _ := v.([]any)
	for _, c := range calls {
		call, ok := c.(map[string]any)
		if !ok {
			continue
		}
		fn, ok := call["function"].(map[string]any)
		if !ok {
			continue
		}
		if err := s.restoreOpenAIToolArguments(ctx, e, fn, st); err != nil {
			return err
		}
	}
	return nil
}

// restoreOpenAIToolArguments restores the arguments of one tool call. They are a JSON string, so
// it is parsed, every string inside walked and it is rebuilt. Arguments that do not parse are
// left as they arrived.
func (s *Server) restoreOpenAIToolArguments(ctx context.Context, e Engine, fn map[string]any, st *restoreState) error {
	raw, ok := fn["arguments"].(string)
	if !ok {
		return nil
	}
	v, ok := parseJSONString(raw)
	if !ok {
		return nil
	}
	var failed error
	out, changed := jsonwalk.RewriteValue(v, func(str string) (string, bool) {
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
	if !changed {
		return nil
	}
	body, err := marshalNoEscape(out)
	if err != nil {
		return err
	}
	fn["arguments"] = string(body)
	st.changed = true
	return nil
}
