package proxy

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/werbot/shade/internal/core"
	"github.com/werbot/shade/internal/directive"
)

// probeEngine is the Engine stand-in for the walker tests. It records every string it is
// handed and replaces the configured substrings; with no pairs it returns the text
// unchanged. The record proves which fields the walker routed through the engine, the
// replacement proves the result was rebuilt from the engine's answer.
type probeEngine struct {
	fakeEngine
	pairs [][2]string
	seen  []string
}

func (e *probeEngine) Anonymize(_ context.Context, text string) (core.Result, error) {
	e.seen = append(e.seen, text)
	for _, p := range e.pairs {
		text = strings.ReplaceAll(text, p[0], p[1])
	}
	return core.Result{Text: text}, nil
}

// saw reports whether the engine was ever handed a string containing substr.
func (e *probeEngine) saw(substr string) bool {
	for _, s := range e.seen {
		if strings.Contains(s, substr) {
			return true
		}
	}
	return false
}

// walkerServer is a server only for calling anonymizeAnthropic directly: it needs no
// upstream and no engine, just a place for the diagnostic line to go.
func walkerServer(t *testing.T, diag io.Writer) *Server {
	t.Helper()
	if diag == nil {
		diag = io.Discard
	}
	s, err := NewServer(Options{Diag: diag}, func(context.Context) (Engine, error) {
		return &fakeEngine{}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// handlerServer builds a proxy whose upstream is base and whose Opener returns eng.
func handlerServer(t *testing.T, base string, eng Engine) *Server {
	t.Helper()
	home := t.TempDir()
	configBody := fmt.Sprintf("upstream = %q\napi_key_env = \"\"\n", base)
	if err := os.WriteFile(filepath.Join(home, "config.toml"), []byte(configBody), 0o600); err != nil {
		t.Fatal(err)
	}
	s, err := NewServer(
		Options{Home: home, Project: t.TempDir(), Diag: io.Discard},
		func(context.Context) (Engine, error) { return eng, nil },
	)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// capturingUpstream records the body it received and answers with a canned response, so a
// handler test can see both what the proxy sent and what it handed back.
type capturingUpstream struct {
	mu      sync.Mutex
	calls   int
	reqBody []byte

	status int
	header http.Header
	body   []byte
}

func (u *capturingUpstream) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	b, _ := io.ReadAll(r.Body)
	u.mu.Lock()
	u.calls++
	u.reqBody = b
	status, header, body := u.status, u.header, u.body
	u.mu.Unlock()

	if status == 0 {
		status = http.StatusOK
	}
	for k, vs := range header {
		for _, v := range vs {
			w.Header().Add(k, v)
		}
	}
	w.WriteHeader(status)
	_, _ = w.Write(body)
}

func (u *capturingUpstream) snapshot() (calls int, reqBody []byte) {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.calls, bytes.Clone(u.reqBody)
}

func readFixture(t *testing.T) []byte {
	t.Helper()
	b, err := os.ReadFile("testdata/request_messages.json")
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func decodeJSON(t *testing.T, raw []byte) map[string]any {
	t.Helper()
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	m, ok := v.(map[string]any)
	if !ok {
		t.Fatalf("decoded value is %T, want an object", v)
	}
	return m
}

// countDirective counts the blocks whose text is exactly the directive, at any depth.
func countDirective(t *testing.T, raw []byte) int {
	t.Helper()
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	return countString(v, directive.Text())
}

func countString(v any, want string) int {
	switch x := v.(type) {
	case string:
		if x == want {
			return 1
		}
	case []any:
		n := 0
		for _, e := range x {
			n += countString(e, want)
		}
		return n
	case map[string]any:
		n := 0
		for _, e := range x {
			n += countString(e, want)
		}
		return n
	}
	return 0
}

func TestAnonymizeAnthropicRewritesSystemAndHistory(t *testing.T) {
	s := walkerServer(t, nil)
	eng := &probeEngine{pairs: [][2]string{
		{"say hi", "<TEXT_1>"},
		{"cc_version=2.1.296.cf4", "<BILLING_1>"},
	}}

	out, err := s.anonymizeAnthropic(t.Context(), eng, readFixture(t))
	if err != nil {
		t.Fatalf("anonymizeAnthropic: %v", err)
	}

	if bytes.Contains(out, []byte("say hi")) {
		t.Error("a message text survived: it must be anonymized")
	}
	if !bytes.Contains(out, []byte("<TEXT_1>")) {
		t.Error("the message token is missing from the body")
	}
	if bytes.Contains(out, []byte("cc_version=2.1.296.cf4")) {
		t.Error("a system text survived: it must be anonymized")
	}
	if !bytes.Contains(out, []byte("<BILLING_1>")) {
		t.Error("the system token is missing from the body")
	}
	if !eng.saw("say hi") {
		t.Error("the walker never routed a message text through the engine")
	}
	if !eng.saw("cc_version=2.1.296.cf4") {
		t.Error("the walker never routed a system text through the engine")
	}
	if n := countDirective(t, out); n != 1 {
		t.Errorf("directive blocks = %d, want 1", n)
	}
}

func TestAnonymizeAnthropicKeepsStructuralFields(t *testing.T) {
	s := walkerServer(t, nil)
	// One pair per structural value: if the walker routed any of them through the engine,
	// the value would be replaced and the engine would report having seen it.
	eng := &probeEngine{pairs: [][2]string{
		{"claude-opus-5", "<MODEL>"},
		{"https://json-schema.org/draft/2020-12/schema", "<SCHEMA>"},
		{"Bash", "<NAME>"},
		{"\n\nHuman:", "<STOP>"},
	}}
	body := []byte(`{
		"model": "claude-opus-5",
		"stop_sequences": ["\n\nHuman:"],
		"messages": [{"role": "user", "content": "hi"}],
		"tools": [{"name": "Bash", "description": "run", "input_schema": {"$schema": "https://json-schema.org/draft/2020-12/schema", "type": "object"}}]
	}`)

	out, err := s.anonymizeAnthropic(t.Context(), eng, body)
	if err != nil {
		t.Fatalf("anonymizeAnthropic: %v", err)
	}
	doc := decodeJSON(t, out)

	if doc["model"] != "claude-opus-5" {
		t.Errorf("model = %v, want it untouched", doc["model"])
	}
	stops, ok := doc["stop_sequences"].([]any)
	if !ok || len(stops) != 1 || stops[0] != "\n\nHuman:" {
		t.Errorf("stop_sequences = %#v, want it untouched", doc["stop_sequences"])
	}
	tools, ok := doc["tools"].([]any)
	if !ok || len(tools) != 1 {
		t.Fatalf("tools = %#v, want one tool", doc["tools"])
	}
	tool, _ := tools[0].(map[string]any)
	if tool["name"] != "Bash" {
		t.Errorf("tool name = %v, want it untouched", tool["name"])
	}
	schema, _ := tool["input_schema"].(map[string]any)
	if schema["$schema"] != "https://json-schema.org/draft/2020-12/schema" {
		t.Errorf("input_schema = %#v, want it untouched", tool["input_schema"])
	}
	if tool["description"] != "run" {
		t.Errorf("description = %v, want it passed through unchanged by a no-op engine", tool["description"])
	}
	for _, lit := range []string{"claude-opus-5", "https://json-schema.org/draft/2020-12/schema", "Bash", "\n\nHuman:"} {
		if eng.saw(lit) {
			t.Errorf("the walker routed the structural value %q through the engine", lit)
		}
	}
}

func TestAnonymizeAnthropicRewritesToolInputStrings(t *testing.T) {
	s := walkerServer(t, nil)
	eng := &probeEngine{pairs: [][2]string{
		{"ssh root@10.0.0.1", "<HOST_1>"},
		{"secret-token", "<USER_1>"},
	}}
	body := []byte(`{"messages": [{"role": "assistant", "content": [
		{"type": "tool_use", "id": "tu_1", "name": "Bash", "input": {
			"command": "ssh root@10.0.0.1",
			"env": {"TOKEN": "secret-token"},
			"timeout": 5
		}}
	]}]}`)

	out, err := s.anonymizeAnthropic(t.Context(), eng, body)
	if err != nil {
		t.Fatalf("anonymizeAnthropic: %v", err)
	}
	doc := decodeJSON(t, out)
	msgs, _ := doc["messages"].([]any)
	blocks, _ := msgs[0].(map[string]any)["content"].([]any)
	input, _ := blocks[0].(map[string]any)["input"].(map[string]any)

	if input["command"] != "<HOST_1>" {
		t.Errorf("input.command = %v, want %q", input["command"], "<HOST_1>")
	}
	env, _ := input["env"].(map[string]any)
	if env["TOKEN"] != "<USER_1>" {
		t.Errorf("input.env.TOKEN = %v, want %q", env["TOKEN"], "<USER_1>")
	}
	if n, ok := input["timeout"].(json.Number); !ok || n.String() != "5" {
		t.Errorf("input.timeout = %#v, want the number 5", input["timeout"])
	}
	if !eng.saw("secret-token") {
		t.Error("the walker never routed a nested tool-input string through the engine")
	}
}

func TestAnonymizeAnthropicLeavesImageAndThinkingBlocks(t *testing.T) {
	s := walkerServer(t, nil)
	const (
		thinkingText = "ssh root@10.0.0.1"
		signature    = "sig-abc-123"
		imageData    = "iVBORw0KGgoAAAANSUhEUg=="
		docData      = "JVBERi0xLjQK"
	)
	// Values that would be rewritten if the walker entered these blocks.
	eng := &probeEngine{pairs: [][2]string{
		{thinkingText, "<HOST_1>"},
		{signature, "<SIG>"},
		{imageData, "<IMG>"},
		{docData, "<DOC>"},
	}}
	body := []byte(`{"messages": [{"role": "assistant", "content": [
		{"type": "thinking", "thinking": "ssh root@10.0.0.1", "signature": "sig-abc-123"},
		{"type": "image", "source": {"type": "base64", "media_type": "image/png", "data": "iVBORw0KGgoAAAANSUhEUg=="}},
		{"type": "document", "source": {"type": "base64", "media_type": "application/pdf", "data": "JVBERi0xLjQK"}}
	]}]}`)

	out, err := s.anonymizeAnthropic(t.Context(), eng, body)
	if err != nil {
		t.Fatalf("anonymizeAnthropic: %v", err)
	}
	doc := decodeJSON(t, out)
	msgs, _ := doc["messages"].([]any)
	blocks, _ := msgs[0].(map[string]any)["content"].([]any)

	thinking, _ := blocks[0].(map[string]any)
	if thinking["thinking"] != thinkingText {
		t.Errorf("thinking = %v, want byte-for-byte %q", thinking["thinking"], thinkingText)
	}
	if thinking["signature"] != signature {
		t.Errorf("signature = %v, want byte-for-byte %q", thinking["signature"], signature)
	}
	image, _ := blocks[1].(map[string]any)
	imageSrc, _ := image["source"].(map[string]any)
	if imageSrc["data"] != imageData {
		t.Errorf("image data = %v, want byte-for-byte %q", imageSrc["data"], imageData)
	}
	document, _ := blocks[2].(map[string]any)
	docSrc, _ := document["source"].(map[string]any)
	if docSrc["data"] != docData {
		t.Errorf("document data = %v, want byte-for-byte %q", docSrc["data"], docData)
	}
	for _, lit := range []string{thinkingText, signature, imageData, docData} {
		if eng.saw(lit) {
			t.Errorf("the walker routed %q through the engine; the block must be left whole", lit)
		}
	}
}

func TestAnonymizeAnthropicInjectsTheDirectiveOnce(t *testing.T) {
	directiveJSON := strconv.Quote(directive.Text())
	cases := []struct {
		name       string
		system     string
		wantBlocks int
	}{
		{"string", `"system": ` + directiveJSON + `,`, 2},
		{"array", `"system": [{"type": "text", "text": ` + directiveJSON + `}],`, 2},
		{"absent", ``, 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := walkerServer(t, nil)
			body := []byte(`{` + tc.system + `"messages": [{"role": "user", "content": "hi"}]}`)
			before := countDirective(t, body)

			out, err := s.anonymizeAnthropic(t.Context(), &fakeEngine{}, body)
			if err != nil {
				t.Fatalf("anonymizeAnthropic: %v", err)
			}
			if after := countDirective(t, out); after != before+1 {
				t.Errorf("directive blocks = %d, want the input's %d plus one", after, before)
			}
			doc := decodeJSON(t, out)
			blocks, ok := doc["system"].([]any)
			if !ok {
				t.Fatalf("system = %#v, want an array after injection", doc["system"])
			}
			if len(blocks) != tc.wantBlocks {
				t.Errorf("system blocks = %d, want %d", len(blocks), tc.wantBlocks)
			}
			last, _ := blocks[len(blocks)-1].(map[string]any)
			if last["type"] != "text" || last["text"] != directive.Text() {
				t.Errorf("last system block = %#v, want the directive block", last)
			}
		})
	}
}

func TestAnonymizeAnthropicKeepsTheBodyShape(t *testing.T) {
	s := walkerServer(t, nil)
	body := []byte(`{"max_tokens": 64000, "temperature": 1.0, "top_p": 0.95, "stream": true, "big": 12345678901234567890, "nothing": null, "messages": [{"role": "user", "content": "hi"}]}`)

	out, err := s.anonymizeAnthropic(t.Context(), &fakeEngine{}, body)
	if err != nil {
		t.Fatalf("anonymizeAnthropic: %v", err)
	}
	doc := decodeJSON(t, out)

	for key, want := range map[string]string{
		"max_tokens":  "64000",
		"temperature": "1.0",
		"top_p":       "0.95",
		"big":         "12345678901234567890",
	} {
		n, ok := doc[key].(json.Number)
		if !ok {
			t.Errorf("%s = %#v, want a json.Number", key, doc[key])
			continue
		}
		if n.String() != want {
			t.Errorf("%s = %s, want %s", key, n.String(), want)
		}
	}
	if b, ok := doc["stream"].(bool); !ok || !b {
		t.Errorf("stream = %#v, want the boolean true", doc["stream"])
	}
	if v, ok := doc["nothing"]; !ok || v != nil {
		t.Errorf("nothing = %#v, want null", doc["nothing"])
	}
}

func TestAnonymizeAnthropicLeavesAnUnusableSystemShape(t *testing.T) {
	var diag bytes.Buffer
	s := walkerServer(t, &diag)
	body := []byte(`{"system": 42, "messages": [{"role": "user", "content": "hi"}]}`)

	out, err := s.anonymizeAnthropic(t.Context(), &fakeEngine{}, body)
	if err != nil {
		t.Fatalf("anonymizeAnthropic: %v", err)
	}
	doc := decodeJSON(t, out)
	if n, ok := doc["system"].(json.Number); !ok || n.String() != "42" {
		t.Errorf("system = %#v, want the number 42 left untouched", doc["system"])
	}
	line := diag.String()
	if !strings.Contains(line, "system") || !strings.Contains(line, "directive") {
		t.Errorf("diag = %q, want a line naming the skipped system", line)
	}
	if strings.Contains(line, "42") {
		t.Errorf("diag = %q, must not carry a value", line)
	}
}

func TestCountTokensBodyIsAnonymizedLikeMessages(t *testing.T) {
	pairs := [][2]string{{"ssh root@10.0.0.1", "<HOST_1>"}}
	body := []byte(`{"model": "claude-opus-5", "messages": [{"role": "user", "content": "run ssh root@10.0.0.1 now"}]}`)

	forward := func(target string) []byte {
		t.Helper()
		up := &capturingUpstream{}
		ts := httptest.NewServer(up)
		t.Cleanup(ts.Close)
		s := handlerServer(t, ts.URL, &probeEngine{pairs: pairs})

		rec := httptest.NewRecorder()
		s.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, target, bytes.NewReader(body)))
		if rec.Code != http.StatusOK {
			t.Fatalf("%s: status = %d, want %d", target, rec.Code, http.StatusOK)
		}
		calls, got := up.snapshot()
		if calls != 1 || len(got) == 0 {
			t.Fatalf("%s: upstream saw %d calls, %d body bytes", target, calls, len(got))
		}
		return got
	}

	countTokens := forward(pathCountTokens)
	if bytes.Contains(countTokens, []byte("ssh root@10.0.0.1")) {
		t.Error("the secret reached the upstream on count_tokens")
	}
	if !bytes.Contains(countTokens, []byte("<HOST_1>")) {
		t.Error("the token is missing from the count_tokens body")
	}
	if n := countDirective(t, countTokens); n != 1 {
		t.Errorf("count_tokens directive blocks = %d, want 1", n)
	}

	// The same body on /v1/messages must reach the upstream the same way: count_tokens is
	// not a second code path.
	messages := forward(pathMessages)
	if !bytes.Equal(countTokens, messages) {
		t.Errorf("count_tokens and messages forwarded different bodies:\n count_tokens=%s\n messages=%s", countTokens, messages)
	}
}

func TestCountTokensResponsePassesThroughVerbatim(t *testing.T) {
	up := &capturingUpstream{
		status: http.StatusOK,
		header: http.Header{"Content-Type": {"application/json"}, "X-Upstream": {"kept"}},
		body:   []byte(`{"input_tokens": 42}`),
	}
	ts := httptest.NewServer(up)
	t.Cleanup(ts.Close)
	s := handlerServer(t, ts.URL, &fakeEngine{})

	body := []byte(`{"model": "claude-opus-5", "messages": [{"role": "user", "content": "hi"}]}`)
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, pathCountTokens, bytes.NewReader(body)))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	if got := rec.Header().Get("Content-Type"); got != "application/json" {
		t.Errorf("Content-Type = %q, want it passed through", got)
	}
	if got := rec.Header().Get("X-Upstream"); got != "kept" {
		t.Errorf("X-Upstream = %q, want it passed through", got)
	}
	if got := rec.Body.String(); got != `{"input_tokens": 42}` {
		t.Errorf("body = %q, want it verbatim", got)
	}
}

func TestUnparseableBodyIsRefusedWithoutUpstream(t *testing.T) {
	const want = `{"type":"error","error":{"type":"shade_error","message":"request body could not be anonymized"}}`
	for _, tc := range []struct{ name, body string }{
		{"not json", `{not json`},
		{"not an object", `["not", "an", "object"]`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			up := &capturingUpstream{}
			ts := httptest.NewServer(up)
			t.Cleanup(ts.Close)
			s := handlerServer(t, ts.URL, &fakeEngine{})

			rec := httptest.NewRecorder()
			s.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, pathMessages, strings.NewReader(tc.body)))

			if rec.Code != http.StatusBadGateway {
				t.Errorf("status = %d, want %d", rec.Code, http.StatusBadGateway)
			}
			if got := rec.Body.String(); got != want {
				t.Errorf("body = %q, want %q", got, want)
			}
			if calls, _ := up.snapshot(); calls != 0 {
				t.Errorf("upstream was called %d times, want 0", calls)
			}
		})
	}
}
