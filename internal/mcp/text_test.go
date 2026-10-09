package mcp_test

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/werbot/shade/internal/placeholder"
)

func TestAnonymizeReplacesValuesWithTokens(t *testing.T) {
	cs := toolSession(t, t.TempDir(), fakeEngine{secret: testSecret, token: testToken})
	res := callTool(t, cs, "anonymize", map[string]any{"text": "write to " + testSecret + " now"})

	out := structured[anonWire](t, res)
	if out.Text != "write to "+testToken+" now" {
		t.Errorf("text: got %q", out.Text)
	}
	want := []spanWire{{Type: "EMAIL", Rule: "email"}}
	if !slices.Equal(out.Spans, want) {
		t.Errorf("spans: got %+v, want %+v", out.Spans, want)
	}
}

func TestAnonymizeEmptyTextIsNotAnError(t *testing.T) {
	cs := toolSession(t, t.TempDir(), fakeEngine{secret: testSecret, token: testToken})
	res := callTool(t, cs, "anonymize", map[string]any{"text": ""})
	if res.IsError {
		t.Fatalf("an empty text is a legitimate input, not an error")
	}

	// null reads as "the field is absent" in a machine contract, so the list must be [].
	if got := rawResult(t, res); !strings.Contains(got, `"spans":[]`) {
		t.Errorf("spans must serialize as []: %s", got)
	}
	out := structured[anonWire](t, res)
	if out.Text != "" || out.Spans == nil || len(out.Spans) != 0 {
		t.Errorf("got text=%q spans=%v, want empty text and empty spans", out.Text, out.Spans)
	}
}

func TestAnonymizeKeepsNonASCIITextByteForByte(t *testing.T) {
	const text = "привет 👋\nмир " + testSecret
	cs := toolSession(t, t.TempDir(), fakeEngine{secret: testSecret, token: testToken})
	res := callTool(t, cs, "anonymize", map[string]any{"text": text})

	want := "привет 👋\nмир " + testToken
	if got := structured[anonWire](t, res).Text; got != want {
		t.Errorf("text:\n got %q\nwant %q", got, want)
	}
}

func TestAnonymizeHandlesLargeText(t *testing.T) {
	const size = 4 << 20
	text := strings.Repeat("a", size) + " " + testSecret
	cs := toolSession(t, t.TempDir(), fakeEngine{secret: testSecret, token: testToken})
	res := callTool(t, cs, "anonymize", map[string]any{"text": text})

	out := structured[anonWire](t, res)
	if !strings.HasSuffix(out.Text, " "+testToken) {
		t.Errorf("the value at the end was not replaced: %q", out.Text[len(out.Text)-32:])
	}
	if want := size + 1 + len(testToken); len(out.Text) != want {
		t.Errorf("text length: got %d, want %d", len(out.Text), want)
	}
}

func TestAnonymizeMissingTextIsASchemaError(t *testing.T) {
	cs := toolSession(t, t.TempDir(), fakeEngine{secret: testSecret, token: testToken})
	res := callTool(t, cs, "anonymize", map[string]any{})
	if !res.IsError {
		t.Fatalf("a missing text is rejected by the schema before the handler runs, got %+v", res)
	}
	// The message must come from the validator and name the property. Parsing the content
	// block keeps the check off the wrapper, whose own "type":"text" would match any
	// substring search for the property name.
	var body struct {
		Content []struct {
			Text string `json:"text"`
		} `json:"content"`
	}
	if err := json.Unmarshal([]byte(rawResult(t, res)), &body); err != nil {
		t.Fatalf("unmarshaling the error result: %v", err)
	}
	if len(body.Content) == 0 {
		t.Fatalf("the error carries no message: %s", rawResult(t, res))
	}
	msg := body.Content[0].Text
	if !strings.Contains(msg, "missing properties") || !strings.Contains(msg, `"text"`) {
		t.Errorf("the error must name the missing property: %q", msg)
	}
}

func TestDeanonymizeRestoresValues(t *testing.T) {
	cs := toolSession(t, t.TempDir(), fakeEngine{secret: testSecret, token: testToken})
	res := callTool(t, cs, "deanonymize", map[string]any{"text": "mail " + testToken})
	if res.IsError {
		t.Fatalf("a resolvable token is not an error")
	}

	out := structured[deanonWire](t, res)
	if out.Text != "mail "+testSecret {
		t.Errorf("text: got %q", out.Text)
	}
	if out.Unresolved == nil || len(out.Unresolved) != 0 {
		t.Errorf("unresolved: got %v, want []", out.Unresolved)
	}
}

// TestDeanonymizeBlocksUnderFailClosed is the CLI's exit 3 over MCP: an unresolved token
// of a foreign project. isError marks the refusal, text stays empty, and unresolved
// still names the token — the answer is blocked, not the diagnostics.
func TestDeanonymizeBlocksUnderFailClosed(t *testing.T) {
	f := fakeEngine{
		secret:     testSecret,
		token:      testToken,
		unresolved: []placeholder.Token{{Type: "HOST", Raw: "<HOST_9>"}},
	}
	// No config.toml: fail_closed is the default.
	cs := toolSession(t, t.TempDir(), f)
	res := callTool(t, cs, "deanonymize", map[string]any{"text": "<HOST_9>"})
	if !res.IsError {
		t.Fatalf("fail_closed with an unresolved token must set isError")
	}

	out := structured[deanonWire](t, res)
	if out.Text != "" {
		t.Errorf("text must be empty when blocked: %q", out.Text)
	}
	want := []tokenWire{{Type: "HOST", Raw: "<HOST_9>"}}
	if !slices.Equal(out.Unresolved, want) {
		t.Errorf("unresolved: got %+v, want %+v", out.Unresolved, want)
	}
}

func TestDeanonymizePassesThroughUnderFailOpenLog(t *testing.T) {
	home := t.TempDir()
	writeConfig(t, home, "fail_policy = \"fail_open_log\"\n")

	f := fakeEngine{
		secret:     testSecret,
		token:      testToken,
		unresolved: []placeholder.Token{{Type: "HOST", Raw: "<HOST_9>"}},
	}
	cs := toolSession(t, home, f)
	res := callTool(t, cs, "deanonymize", map[string]any{"text": "<HOST_9>"})
	if res.IsError {
		t.Fatalf("fail_open_log must not mark the result an error")
	}

	out := structured[deanonWire](t, res)
	if out.Text != "<HOST_9>" {
		t.Errorf("text: got %q, want the token as is", out.Text)
	}
	want := []tokenWire{{Type: "HOST", Raw: "<HOST_9>"}}
	if !slices.Equal(out.Unresolved, want) {
		t.Errorf("unresolved: got %+v, want %+v", out.Unresolved, want)
	}
}

// TestToolsDoNotLeakValues is the leak canary. A secret literal must not appear in any
// serialized response of the tools that carry no value: anonymize's text holds tokens,
// its spans hold type and rule, deanonymize's unresolved holds type and the raw token.
//
// The exception is named here on purpose, and asserted positively below: deanonymize's
// own text IS the restoration, so it carries the value and must. A later reader who
// turned this into a blanket ban would break the one tool whose whole job is to hand the
// value back.
func TestToolsDoNotLeakValues(t *testing.T) {
	cs := toolSession(t, t.TempDir(), fakeEngine{secret: testSecret, token: testToken})

	anonRes := callTool(t, cs, "anonymize", map[string]any{"text": "mail " + testSecret})
	// The token in the response is what makes the absence of the value mean "replaced"
	// rather than "nothing came back".
	if got := structured[anonWire](t, anonRes).Text; !strings.Contains(got, testToken) {
		t.Fatalf("the canary is vacuous, the token is not in the response: %q", got)
	}
	if anon := rawResult(t, anonRes); strings.Contains(anon, testSecret) {
		t.Errorf("anonymize leaked the value: %s", anon)
	}

	// unresolved names the token, never the value behind it. The engine did restore it,
	// so the value genuinely exists in play — the tool is what keeps it off the wire.
	blocked := fakeEngine{
		secret:     testSecret,
		token:      testToken,
		unresolved: []placeholder.Token{{Type: "EMAIL", Raw: testToken}},
	}
	blkRes := callTool(t, toolSession(t, t.TempDir(), blocked), "deanonymize", map[string]any{"text": testToken})
	if got := structured[deanonWire](t, blkRes).Unresolved; len(got) != 1 || got[0].Raw != testToken {
		t.Fatalf("the canary is vacuous, the token is not in unresolved: %+v", got)
	}
	if blk := rawResult(t, blkRes); strings.Contains(blk, testSecret) {
		t.Errorf("deanonymize's unresolved leaked the value: %s", blk)
	}

	// The exception, positively: deanonymize's text is the restoration.
	if restored := rawResult(t, callTool(t, cs, "deanonymize", map[string]any{"text": testToken})); !strings.Contains(restored, testSecret) {
		t.Errorf("deanonymize must carry the restored value in text: %s", restored)
	}
}
