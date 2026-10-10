package proxy

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/werbot/shade/internal/directive"
	"github.com/werbot/shade/internal/jsonwalk"
)

// anonymizeOpenAI rewrites the text-carrying fields of a chat-completions request and injects
// the directive. It is the OpenAI field table over the same machinery the Anthropic walker uses:
// a message content (a string or {type:"text"} parts), a tool call's arguments (a JSON string)
// and a tool's function.description. Structural fields are left alone on purpose: rewriting a
// model name, a stop sequence, seed or response_format would break the request.
//
// A body that is not a JSON object, or that does not parse, comes back as an error and the
// handler refuses it without contacting the upstream. The error is never echoed to the client.
func (s *Server) anonymizeOpenAI(ctx context.Context, e Engine, body []byte) ([]byte, error) {
	dec := json.NewDecoder(bytes.NewReader(body))
	// Without UseNumber a number becomes a float64: seed 5 survives, but a large id would come
	// back in exponential form and 1.0 would turn into 1.
	dec.UseNumber()
	var doc map[string]any
	if err := dec.Decode(&doc); err != nil {
		return nil, fmt.Errorf("anonymize openai: %w", err)
	}
	if doc == nil {
		return nil, errors.New("anonymize openai: body is not a json object")
	}
	if err := dec.Decode(new(any)); err != io.EOF {
		return nil, errors.New("anonymize openai: trailing data after the json document")
	}

	if err := s.anonymizeOpenAIMessages(ctx, e, doc["messages"]); err != nil {
		return nil, err
	}
	if err := s.anonymizeOpenAITools(ctx, e, doc["tools"]); err != nil {
		return nil, err
	}
	if err := injectOpenAIDirective(doc); err != nil {
		return nil, err
	}
	return marshalNoEscape(doc)
}

// anonymizeOpenAIMessages rewrites the content and tool-call arguments of every message. The role
// is not examined: a shape the upstream accepts is not refused here.
func (s *Server) anonymizeOpenAIMessages(ctx context.Context, e Engine, v any) error {
	msgs, _ := v.([]any)
	for _, m := range msgs {
		msg, ok := m.(map[string]any)
		if !ok {
			continue
		}
		if err := s.anonymizeOpenAIContent(ctx, e, msg); err != nil {
			return err
		}
		if err := s.anonymizeOpenAIToolCalls(ctx, e, msg["tool_calls"]); err != nil {
			return err
		}
	}
	return nil
}

// anonymizeOpenAIContent rewrites a message content: a plain string, or the text parts of an
// array. A part of a type this build does not know — image_url — is left whole, so it reaches the
// model untouched rather than mangled.
func (s *Server) anonymizeOpenAIContent(ctx context.Context, e Engine, msg map[string]any) error {
	switch c := msg["content"].(type) {
	case string:
		out, err := anonymizeText(ctx, e, c)
		if err != nil {
			return err
		}
		msg["content"] = out
	case []any:
		for _, p := range c {
			part, ok := p.(map[string]any)
			if !ok {
				continue
			}
			if typ, _ := part["type"].(string); typ == "text" {
				if err := s.anonymizeField(ctx, e, part, "text"); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

// anonymizeOpenAIToolCalls rewrites the arguments of every tool call in a message. The id, type
// and function name are structural: the model matches a call by them.
func (s *Server) anonymizeOpenAIToolCalls(ctx context.Context, e Engine, v any) error {
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
		if err := s.anonymizeOpenAIToolArguments(ctx, e, fn); err != nil {
			return err
		}
	}
	return nil
}

// anonymizeOpenAIToolArguments rewrites the arguments of one tool call. They are a JSON string,
// so it is parsed, every string inside walked and it is rebuilt — a text edit of the raw string
// would miss an escaped token. Arguments that do not parse (an empty string is the common case)
// are left as they are: refusing the request over a tool call the upstream would accept would
// break it.
func (s *Server) anonymizeOpenAIToolArguments(ctx context.Context, e Engine, fn map[string]any) error {
	raw, ok := fn["arguments"].(string)
	if !ok {
		return nil
	}
	v, ok := parseJSONString(raw)
	if !ok {
		return nil
	}
	var failed error
	out, _ := jsonwalk.RewriteValue(v, func(str string) (string, bool) {
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
	body, err := marshalNoEscape(out)
	if err != nil {
		return err
	}
	fn["arguments"] = string(body)
	return nil
}

// anonymizeOpenAITools anonymizes each tool's function.description. name and parameters are
// structural: the model matches a tool by name, and a rewritten schema is a broken schema.
func (s *Server) anonymizeOpenAITools(ctx context.Context, e Engine, v any) error {
	tools, _ := v.([]any)
	for _, t := range tools {
		tool, ok := t.(map[string]any)
		if !ok {
			continue
		}
		fn, ok := tool["function"].(map[string]any)
		if !ok {
			continue
		}
		if err := s.anonymizeField(ctx, e, fn, "description"); err != nil {
			return err
		}
	}
	return nil
}

// injectOpenAIDirective puts the directive where the model will see it: appended to the first
// message when it is a system message with string content, otherwise inserted as a new first
// system message. A system message of any other shape (an array of parts) is left exactly as it
// is and the directive goes in as a new message — the same treatment the Anthropic side gives an
// unusual system. The directive is always added, never deduplicated (spec §8).
func injectOpenAIDirective(doc map[string]any) error {
	msgs, _ := doc["messages"].([]any)
	if len(msgs) > 0 {
		if first, ok := msgs[0].(map[string]any); ok {
			if role, _ := first["role"].(string); role == "system" {
				if content, ok := first["content"].(string); ok {
					first["content"] = content + "\n\n" + directive.Text()
					return nil
				}
			}
		}
	}
	doc["messages"] = append([]any{systemMessage()}, msgs...)
	return nil
}

// systemMessage is the message the proxy inserts when it cannot append to an existing system.
func systemMessage() map[string]any {
	return map[string]any{"role": "system", "content": directive.Text()}
}

// parseJSONString decodes one JSON string, with UseNumber so a number inside keeps its spelling.
// It reports false for anything that does not parse, and the caller then leaves the string as it
// arrived rather than guessing.
func parseJSONString(raw string) (any, bool) {
	dec := json.NewDecoder(strings.NewReader(raw))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return nil, false
	}
	return v, true
}
