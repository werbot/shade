package proxy

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/werbot/shade/internal/directive"
	"github.com/werbot/shade/internal/placeholder"
)

// postChat drives the whole handler with a minimal, valid chat-completions request body.
func postChat(s *Server) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, pathChatCompletions,
		strings.NewReader(`{"messages":[{"role":"user","content":"hi"}]}`)))
	return rec
}

// openAIToolArguments returns choices[0].message.tool_calls[0].function.arguments as a string.
func openAIToolArguments(t *testing.T, doc map[string]any) string {
	t.Helper()
	choices, ok := doc["choices"].([]any)
	if !ok || len(choices) == 0 {
		t.Fatalf("choices = %#v, want a non-empty array", doc["choices"])
	}
	msg, ok := choices[0].(map[string]any)["message"].(map[string]any)
	if !ok {
		t.Fatalf("choices[0].message = %#v, want an object", choices[0])
	}
	calls, ok := msg["tool_calls"].([]any)
	if !ok || len(calls) == 0 {
		t.Fatalf("tool_calls = %#v, want a non-empty array", msg["tool_calls"])
	}
	fn, _ := calls[0].(map[string]any)["function"].(map[string]any)
	args, _ := fn["arguments"].(string)
	return args
}

// openAIRequestToolArguments returns the first tool-call arguments string anywhere in a request's
// messages.
func openAIRequestToolArguments(t *testing.T, doc map[string]any) string {
	t.Helper()
	msgs, _ := doc["messages"].([]any)
	for _, m := range msgs {
		calls, _ := m.(map[string]any)["tool_calls"].([]any)
		for _, c := range calls {
			fn, _ := c.(map[string]any)["function"].(map[string]any)
			if args, ok := fn["arguments"].(string); ok {
				return args
			}
		}
	}
	t.Fatal("no tool-call arguments in the request")
	return ""
}

// TestAnonymizeOpenAIRewritesMessagesAndToolDescriptions pins the request field table: a message
// content (a string and a {type:"text"} part) and a tool's function.description go through the
// engine, while model, parameters and an image_url part are left alone.
func TestAnonymizeOpenAIRewritesMessagesAndToolDescriptions(t *testing.T) {
	s := walkerServer(t, nil)
	eng := &probeEngine{pairs: [][2]string{
		{"say hi", "<TEXT_1>"},
		{"ssh root@10.0.0.1", "<HOST_1>"},
	}}
	body := []byte(`{
		"model": "gpt-5",
		"messages": [
			{"role": "user", "content": "say hi"},
			{"role": "user", "content": [
				{"type": "text", "text": "run ssh root@10.0.0.1"},
				{"type": "image_url", "image_url": {"url": "data:image/png;base64,IMAGEBYTES"}}
			]}
		],
		"tools": [
			{"type": "function", "function": {
				"name": "Bash",
				"description": "run ssh root@10.0.0.1 on the host",
				"parameters": {"type": "object", "description": "ssh root@10.0.0.1"}
			}}
		]
	}`)

	out, err := s.anonymizeOpenAI(t.Context(), eng, body)
	if err != nil {
		t.Fatalf("anonymizeOpenAI: %v", err)
	}
	doc := decodeJSON(t, out)

	if doc["model"] != "gpt-5" {
		t.Errorf("model = %v, want it untouched", doc["model"])
	}
	// The first message is a user, so the directive goes in as a new first system message.
	msgs, ok := doc["messages"].([]any)
	if !ok || len(msgs) != 3 {
		t.Fatalf("messages = %#v, want the injected system plus the two originals", doc["messages"])
	}
	if msgs[0].(map[string]any)["role"] != "system" {
		t.Fatalf("messages[0] = %#v, want the injected system", msgs[0])
	}
	if got := msgs[1].(map[string]any)["content"]; got != "<TEXT_1>" {
		t.Errorf("string content = %v, want %q", got, "<TEXT_1>")
	}
	parts, ok := msgs[2].(map[string]any)["content"].([]any)
	if !ok || len(parts) != 2 {
		t.Fatalf("message content = %#v, want two parts", msgs[2].(map[string]any)["content"])
	}
	if got := parts[0].(map[string]any)["text"]; got != "run <HOST_1>" {
		t.Errorf("text part = %v, want %q", got, "run <HOST_1>")
	}
	img, _ := parts[1].(map[string]any)["image_url"].(map[string]any)
	if img["url"] != "data:image/png;base64,IMAGEBYTES" {
		t.Errorf("image_url = %#v, want it untouched", img)
	}

	tool := doc["tools"].([]any)[0].(map[string]any)["function"].(map[string]any)
	if got := tool["description"]; got != "run <HOST_1> on the host" {
		t.Errorf("description = %v, want %q", got, "run <HOST_1> on the host")
	}
	if tool["name"] != "Bash" {
		t.Errorf("tool name = %v, want it untouched", tool["name"])
	}
	params, _ := tool["parameters"].(map[string]any)
	if params["description"] != "ssh root@10.0.0.1" {
		t.Errorf("parameters = %#v, want it untouched", params)
	}
	if !eng.saw("say hi") || !eng.saw("ssh root@10.0.0.1") {
		t.Error("the walker never routed a message text or a tool description through the engine")
	}
	if eng.saw("IMAGEBYTES") {
		t.Error("the walker routed an image_url through the engine; it must be left whole")
	}
}

// TestAnonymizeOpenAIPrependsTheDirectiveSystemMessage pins where the directive lands: appended
// to an existing string system message, or as a new first system message when the first message
// is not a system, or is a system whose content is not a string.
func TestAnonymizeOpenAIPrependsTheDirectiveSystemMessage(t *testing.T) {
	cases := []struct {
		name       string
		body       string
		wantAppend bool
		wantLen    int
	}{
		{
			name:       "appended to an existing string system",
			body:       `{"messages":[{"role":"system","content":"be terse"},{"role":"user","content":"hi"}]}`,
			wantAppend: true,
			wantLen:    2,
		},
		{
			name:    "a new system is prepended when there is none",
			body:    `{"messages":[{"role":"user","content":"hi"}]}`,
			wantLen: 2,
		},
		{
			name:    "a new system is prepended over a non-string system",
			body:    `{"messages":[{"role":"system","content":[{"type":"text","text":"be terse"}]},{"role":"user","content":"hi"}]}`,
			wantLen: 3,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := walkerServer(t, nil)
			out, err := s.anonymizeOpenAI(t.Context(), &fakeEngine{}, []byte(tc.body))
			if err != nil {
				t.Fatalf("anonymizeOpenAI: %v", err)
			}
			doc := decodeJSON(t, out)
			msgs, ok := doc["messages"].([]any)
			if !ok {
				t.Fatalf("messages = %#v, want an array", doc["messages"])
			}
			first, _ := msgs[0].(map[string]any)
			if first["role"] != "system" {
				t.Fatalf("messages[0].role = %v, want system", first["role"])
			}
			content, ok := first["content"].(string)
			if !ok {
				t.Fatalf("messages[0].content = %#v, want a string", first["content"])
			}
			if !strings.Contains(content, directive.Text()) {
				t.Errorf("messages[0].content = %q, want it to carry the directive", content)
			}
			if len(msgs) != tc.wantLen {
				t.Errorf("messages = %d, want %d", len(msgs), tc.wantLen)
			}
			if tc.wantAppend {
				if content != "be terse\n\n"+directive.Text() {
					t.Errorf("system content = %q, want the directive appended", content)
				}
				return
			}
			if content != directive.Text() {
				t.Errorf("new system content = %q, want exactly the directive", content)
			}
		})
	}
}

// TestAnonymizeOpenAIRewritesToolCallArguments pins the tool-call path: arguments is a JSON
// string, so it is parsed, its strings walked and it is rebuilt — a text edit of the raw string
// would miss nothing here but would break the JSON. The number inside survives.
func TestAnonymizeOpenAIRewritesToolCallArguments(t *testing.T) {
	s := walkerServer(t, nil)
	eng := &probeEngine{pairs: [][2]string{{"ssh root@10.0.0.1", "<HOST_1>"}}}
	body := []byte(`{"messages":[{"role":"assistant","content":null,"tool_calls":[{"id":"call_1","type":"function","function":{"name":"Bash","arguments":"{\"cmd\":\"ssh root@10.0.0.1\",\"n\":5}"}}]}]}`)

	out, err := s.anonymizeOpenAI(t.Context(), eng, body)
	if err != nil {
		t.Fatalf("anonymizeOpenAI: %v", err)
	}
	args := openAIRequestToolArguments(t, decodeJSON(t, out))
	if strings.Contains(args, "ssh root@10.0.0.1") {
		t.Errorf("the arguments still carry the value: %q", args)
	}
	var parsed map[string]any
	if err := json.Unmarshal([]byte(args), &parsed); err != nil {
		t.Fatalf("arguments are not valid JSON: %q: %v", args, err)
	}
	if parsed["cmd"] != "<HOST_1>" {
		t.Errorf("arguments.cmd = %v, want %q", parsed["cmd"], "<HOST_1>")
	}
	if n, ok := parsed["n"].(float64); !ok || n != 5 {
		t.Errorf("arguments.n = %#v, want the number 5", parsed["n"])
	}
	if !eng.saw("ssh root@10.0.0.1") {
		t.Error("the walker never routed a tool-call argument through the engine")
	}
}

// TestRestoreOpenAIResponse pins the answer walker: choices[].message.content and the JSON
// string in tool_calls[].function.arguments both get the values put back.
func TestRestoreOpenAIResponse(t *testing.T) {
	const value = "db.prod.local"
	body := []byte(`{"choices":[{"index":0,"message":{"role":"assistant","content":"the host is <HOST_1>","tool_calls":[{"id":"call_1","type":"function","function":{"name":"Bash","arguments":"{\"cmd\":\"ssh <HOST_1>\"}"}}]},"finish_reason":"tool_calls"}]}`)
	eng := &answerEngine{replace: [][2]string{{"<HOST_1>", value}}}

	out, unresolved, err := walkerServer(t, nil).restoreOpenAI(t.Context(), eng, body)
	if err != nil {
		t.Fatalf("restoreOpenAI: %v", err)
	}
	if len(unresolved) != 0 {
		t.Errorf("unresolved = %+v, want none", unresolved)
	}
	doc := decodeJSON(t, out)
	msg := doc["choices"].([]any)[0].(map[string]any)["message"].(map[string]any)
	if got := msg["content"]; got != "the host is "+value {
		t.Errorf("content = %v, want the value put back", got)
	}
	args := openAIToolArguments(t, doc)
	var parsed map[string]any
	if err := json.Unmarshal([]byte(args), &parsed); err != nil {
		t.Fatalf("arguments are not valid JSON: %q: %v", args, err)
	}
	if parsed["cmd"] != "ssh "+value {
		t.Errorf("arguments.cmd = %v, want the value put back", parsed["cmd"])
	}
}

// TestFailClosedAnswersBadGatewayOpenAI is the OpenAI canary: under fail_closed an answer with a
// token that cannot be restored is refused 502 with a body naming the types and carrying neither
// a value nor a token. The journal write fails too, so the value has a path to diag — asserted to
// carry no value.
func TestFailClosedAnswersBadGatewayOpenAI(t *testing.T) {
	const (
		value = "db.prod.local"
		token = "<HOST_1>"
	)
	answer := []byte(`{"choices":[{"index":0,"message":{"role":"assistant","content":"the host is ` + token + `"}}]}`)

	var diag bytes.Buffer
	eng := &answerEngine{
		unresolved: []placeholder.Token{{Type: "HOST", Raw: token}},
		blockErr:   errors.New("journal is read-only"),
	}
	rec := postChat(answerUpstream(t, http.StatusOK, answer, eng, &diag, ""))

	if rec.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusBadGateway)
	}
	const want = `{"type":"error","error":{"type":"shade_unresolved","message":"1 placeholders could not be restored: HOST"}}`
	if got := rec.Body.String(); got != want {
		t.Errorf("body = %q, want %q", got, want)
	}
	if got := rec.Body.String(); strings.Contains(got, value) || strings.Contains(got, token) {
		t.Errorf("the refusal must carry neither the value nor the token: %q", got)
	}
	line := diag.String()
	if !strings.Contains(line, "stats not recorded") {
		t.Errorf("diag = %q, want the failed journal write reported, so the canary is not vacuous", line)
	}
	if strings.Contains(line, value) {
		t.Errorf("diag must carry no value: %q", line)
	}

	// The exception, positively: a successful answer's body is where the value belongs.
	ok := postChat(answerUpstream(t, http.StatusOK, answer, &answerEngine{replace: [][2]string{{token, value}}}, nil, ""))
	if !strings.Contains(ok.Body.String(), value) {
		t.Errorf("a successful answer must carry the restored value: %q", ok.Body.String())
	}
}
