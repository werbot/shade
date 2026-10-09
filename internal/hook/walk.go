package hook

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
)

// Rewriter maps one JSON string to its replacement. The second result is false when
// the string is left alone, which lets RewriteJSON skip re-marshalling the document.
type Rewriter func(s string) (string, bool)

// RewriteJSON walks every string in a JSON document and applies f to it, leaving
// object keys, numbers, booleans and null untouched: a type the hook never meant to
// rewrite must not be re-encoded. It reports whether anything changed; an unchanged
// document is returned byte-for-byte, so a caller that prints only on change never
// perturbs the tool's data. When something did change the document is re-marshalled
// whole, so untouched strings have their escapes normalised and keys are re-sorted —
// the result is semantically identical, not byte-identical. A document that does not
// parse, or that has trailing data after it, is an error rather than something to
// pass along.
func RewriteJSON(raw json.RawMessage, f Rewriter) (out json.RawMessage, changed bool, err error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	// Without UseNumber a number becomes a float64: 1.0 turns into 1 and
	// 12345678901234567890 into 1.2345678901234567e+19.
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return nil, false, fmt.Errorf("rewrite json: %w", err)
	}
	if err := dec.Decode(new(any)); err != io.EOF {
		return nil, false, errors.New("rewrite json: trailing data after the json document")
	}

	v, changed = rewriteValue(v, f)
	if !changed {
		return raw, false, nil
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	// HTML escaping would rewrite the angle brackets of a placeholder like <HOST_1>.
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, false, fmt.Errorf("rewrite json: %w", err)
	}
	return bytes.TrimSuffix(buf.Bytes(), []byte("\n")), true, nil
}

// rewriteValue applies f to the strings inside v and reports whether any of them
// changed. Numbers, booleans and null fall through untouched.
func rewriteValue(v any, f Rewriter) (any, bool) {
	switch x := v.(type) {
	case string:
		return f(x)
	case []any:
		changed := false
		for i, e := range x {
			var c bool
			x[i], c = rewriteValue(e, f)
			changed = changed || c
		}
		return x, changed
	case map[string]any:
		changed := false
		for k, e := range x {
			var c bool
			x[k], c = rewriteValue(e, f)
			changed = changed || c
		}
		return x, changed
	default:
		return v, false
	}
}
