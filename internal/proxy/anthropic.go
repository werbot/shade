package proxy

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"

	"github.com/werbot/shade/internal/directive"
	"github.com/werbot/shade/internal/jsonwalk"
)

// anonymizeAnthropic rewrites the text-carrying fields of a request body and injects the
// directive into system. Structural fields are left alone on purpose: a blanket walk over
// every string would rewrite a model name or a stop sequence and break the request.
//
// A body that is not a JSON object, or that does not parse, comes back as an error and the
// handler refuses it without contacting the upstream. The error is never echoed to the
// client: it can name a parse offset, and spec §11 forbids leaking any of the body.
func (s *Server) anonymizeAnthropic(ctx context.Context, e Engine, body []byte) ([]byte, error) {
	dec := json.NewDecoder(bytes.NewReader(body))
	// Without UseNumber a number becomes a float64: 64000 would survive, but a large id
	// would come back in exponential form and 1.0 would turn into 1.
	dec.UseNumber()
	var doc map[string]any
	if err := dec.Decode(&doc); err != nil {
		return nil, fmt.Errorf("anonymize anthropic: %w", err)
	}
	if doc == nil {
		return nil, errors.New("anonymize anthropic: body is not a json object")
	}
	if err := dec.Decode(new(any)); err != io.EOF {
		return nil, errors.New("anonymize anthropic: trailing data after the json document")
	}

	if err := s.anonymizeMessages(ctx, e, doc["messages"]); err != nil {
		return nil, err
	}
	if err := s.anonymizeTools(ctx, e, doc["tools"]); err != nil {
		return nil, err
	}
	if err := s.anonymizeSystem(ctx, e, doc); err != nil {
		return nil, err
	}
	return marshalNoEscape(doc)
}

// anonymizeMessages rewrites the content of every message. The role is not examined: the
// recorded body carries a "system" role among the messages, and refusing a shape the
// upstream accepts would break a working request.
func (s *Server) anonymizeMessages(ctx context.Context, e Engine, v any) error {
	msgs, _ := v.([]any)
	for _, m := range msgs {
		msg, ok := m.(map[string]any)
		if !ok {
			continue
		}
		c, ok := msg["content"]
		if !ok {
			continue
		}
		nc, err := s.anonymizeContent(ctx, e, c)
		if err != nil {
			return err
		}
		msg["content"] = nc
	}
	return nil
}

// anonymizeContent rewrites a message content: a plain string, or the text-carrying blocks
// of an array. A block of a type this build does not know is left whole, so a future block
// type reaches the model untouched rather than mangled.
func (s *Server) anonymizeContent(ctx context.Context, e Engine, v any) (any, error) {
	switch c := v.(type) {
	case string:
		out, err := anonymizeText(ctx, e, c)
		return out, err
	case []any:
		for _, b := range c {
			if err := s.anonymizeBlock(ctx, e, b); err != nil {
				return nil, err
			}
		}
	}
	return v, nil
}

// anonymizeBlock rewrites one content block by its type. thinking/signature, image,
// document and every unknown type are skipped: rewriting a thinking signature invalidates
// it, and rewriting the bytes inside an image corrupts it.
func (s *Server) anonymizeBlock(ctx context.Context, e Engine, v any) error {
	block, ok := v.(map[string]any)
	if !ok {
		return nil
	}
	switch typ, _ := block["type"].(string); typ {
	case "text":
		return s.anonymizeField(ctx, e, block, "text")
	case "tool_use":
		return s.anonymizeToolInput(ctx, e, block)
	case "tool_result":
		c, ok := block["content"]
		if !ok {
			return nil
		}
		nc, err := s.anonymizeContent(ctx, e, c)
		if err != nil {
			return err
		}
		block["content"] = nc
	}
	return nil
}

// anonymizeToolInput walks tool_use.input whole: the arguments are written by the model,
// so every string inside can hold a value, at any depth.
func (s *Server) anonymizeToolInput(ctx context.Context, e Engine, block map[string]any) error {
	in, ok := block["input"]
	if !ok {
		return nil
	}
	var failed error
	out, _ := jsonwalk.RewriteValue(in, func(str string) (string, bool) {
		if failed != nil {
			return str, false
		}
		res, err := e.Anonymize(ctx, str)
		if err != nil {
			failed = err
			return str, false
		}
		return res.Text, res.Text != str
	})
	if failed != nil {
		return failed
	}
	block["input"] = out
	return nil
}

// anonymizeTools anonymizes each tool description. name and input_schema are structural:
// the model matches tools by name, and a rewritten schema is a broken schema.
func (s *Server) anonymizeTools(ctx context.Context, e Engine, v any) error {
	tools, _ := v.([]any)
	for _, t := range tools {
		tool, ok := t.(map[string]any)
		if !ok {
			continue
		}
		if err := s.anonymizeField(ctx, e, tool, "description"); err != nil {
			return err
		}
	}
	return nil
}

// anonymizeSystem anonymizes system and leaves it holding exactly one directive block,
// whatever shape it arrived in. The directive is always appended, never deduplicated: the
// proxy has no marker for "a hook already injected this", and a second block costs a few
// tokens while a missing one lets the model break tokens (spec §8).
//
// A system of any other shape is left exactly as it came — it is reported to diagnostics
// and nothing is removed, so the request still goes out and nothing can leak; only the
// directive is skipped.
func (s *Server) anonymizeSystem(ctx context.Context, e Engine, doc map[string]any) error {
	switch sys := doc["system"].(type) {
	case nil:
		doc["system"] = []any{directiveBlock()}
	case string:
		out, err := anonymizeText(ctx, e, sys)
		if err != nil {
			return err
		}
		doc["system"] = []any{textBlock(out), directiveBlock()}
	case []any:
		for _, b := range sys {
			if err := s.anonymizeBlock(ctx, e, b); err != nil {
				return err
			}
		}
		doc["system"] = append(sys, directiveBlock())
	default:
		// The Go type, never the value: a diagnostic must not carry client text.
		fmt.Fprintf(s.opts.Diag, "proxy: system is %T, not a string or an array; directive not injected\n", sys)
	}
	return nil
}

// anonymizeField anonymizes one string field in place, leaving it untouched when it is
// absent or not a string.
func (s *Server) anonymizeField(ctx context.Context, e Engine, obj map[string]any, key string) error {
	str, ok := obj[key].(string)
	if !ok {
		return nil
	}
	out, err := anonymizeText(ctx, e, str)
	if err != nil {
		return err
	}
	obj[key] = out
	return nil
}

// anonymizeText runs one text through the engine. A failure of the engine's auxiliary
// write travels in Result.RecordErr, not here: the anonymized text is ready and matters
// more than a statistics row.
func anonymizeText(ctx context.Context, e Engine, text string) (string, error) {
	res, err := e.Anonymize(ctx, text)
	if err != nil {
		return "", err
	}
	return res.Text, nil
}

// textBlock is one {type:"text"} block of system.
func textBlock(text string) map[string]any {
	return map[string]any{"type": "text", "text": text}
}

// directiveBlock is the block the proxy appends to system, so the model sees the directive.
func directiveBlock() map[string]any {
	return textBlock(directive.Text())
}

// marshalNoEscape renders a document without HTML escaping, as the hook does: <USER_1> is
// no longer a token the model can pass on once it is written <USER_1>.
func marshalNoEscape(v any) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return bytes.TrimSuffix(buf.Bytes(), []byte("\n")), nil
}
