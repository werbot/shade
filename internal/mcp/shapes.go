package mcp

// The input and output shapes of the tools. The json tags are the wire contract the CLI
// --json output shares; the jsonschema tags become the descriptions the model reads, so
// they are part of the answer and not a comment.

// textInput is the argument of every tool that works on one text.
type textInput struct {
	Text string `json:"text" jsonschema:"the text to work on"`
}

// scanInput is the argument of scan: the text, and an optional rule to narrow the run to.
type scanInput struct {
	Text string `json:"text"`
	Rule string `json:"rule,omitempty" jsonschema:"narrow the run to one rule of the active set"`
}

// spanInfo names a fragment found: its type and the rule that found it. The fragment and
// its offsets stay behind — the same bar the scan tool keeps (§11).
type spanInfo struct {
	Type string `json:"type" jsonschema:"the placeholder type of the fragment"`
	Rule string `json:"rule" jsonschema:"the name of the rule that found it"`
}

// spansOutput is the shared answer of the scan and rules_test tools: what was found, not
// where.
type spansOutput struct {
	Spans []spanInfo `json:"spans" jsonschema:"the fragments found, in the order they follow in the text"`
}

// anonOutput is the answer of anonymize: the text with placeholders, and what was
// replaced.
type anonOutput struct {
	Text  string     `json:"text" jsonschema:"the text with the values replaced by placeholders"`
	Spans []spanInfo `json:"spans" jsonschema:"the fragments that were replaced"`
}

// tokenInfo is an unresolved placeholder: its type and the token itself. A value can
// never get in here — raw is the token, and only it.
type tokenInfo struct {
	Type string `json:"type" jsonschema:"the placeholder type of the token"`
	Raw  string `json:"raw" jsonschema:"the token as it appeared in the text"`
}

// deanonOutput is the answer of deanonymize: the restored text, and the tokens whose
// values were not in the store.
type deanonOutput struct {
	Text       string      `json:"text" jsonschema:"the text with the values restored"`
	Unresolved []tokenInfo `json:"unresolved" jsonschema:"the placeholders whose values are not in this project's store"`
}
