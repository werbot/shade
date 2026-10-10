package proxy

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/werbot/shade/internal/core"
	"github.com/werbot/shade/internal/placeholder"
)

// answerEngine is the Engine stand-in for the answer path. It replaces configured
// substrings, reports the tokens it was told to leave unresolved — one per entry whose Raw
// appears in the text, so a test drives the count and the distinct types — records every
// blocked type and can fail either write. Each knob drives one branch of the restore, the
// policy or the error path.
type answerEngine struct {
	fakeEngine
	replace    [][2]string
	unresolved []placeholder.Token
	recordErr  error  // Result.RecordErr from both Anonymize and Restore
	anonErr    error  // Anonymize's returned error
	anonErrOn  string // Anonymize fails only on a text containing this, so the request body still goes through
	blockErr   error  // RecordBlocked's returned error

	mu      sync.Mutex
	seen    []string
	blocked []string
}

func (e *answerEngine) Anonymize(_ context.Context, text string) (core.Result, error) {
	e.record(text)
	if e.anonErr != nil && strings.Contains(text, e.anonErrOn) {
		return core.Result{}, e.anonErr
	}
	return core.Result{Text: e.substitute(text), RecordErr: e.recordErr}, nil
}

func (e *answerEngine) Restore(_ context.Context, text string) (core.Result, error) {
	e.record(text)
	var unresolved []placeholder.Token
	for _, tok := range e.unresolved {
		if strings.Contains(text, tok.Raw) {
			unresolved = append(unresolved, tok)
		}
	}
	return core.Result{Text: e.substitute(text), Unresolved: unresolved, RecordErr: e.recordErr}, nil
}

func (e *answerEngine) RecordBlocked(_ context.Context, typ string) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.blocked = append(e.blocked, typ)
	return e.blockErr
}

func (e *answerEngine) substitute(text string) string {
	for _, p := range e.replace {
		text = strings.ReplaceAll(text, p[0], p[1])
	}
	return text
}

func (e *answerEngine) record(text string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.seen = append(e.seen, text)
}

// saw reports whether the engine was ever handed a string containing substr.
func (e *answerEngine) saw(substr string) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	return slices.ContainsFunc(e.seen, func(s string) bool { return strings.Contains(s, substr) })
}

// blockedTypes copies the recorded blocked types out under the lock.
func (e *answerEngine) blockedTypes() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]string(nil), e.blocked...)
}

// answerServer is handlerServer with the two knobs this task's tests need: a diagnostic
// writer and extra config lines (fail_policy). No fail_policy means the default, which is
// fail_closed.
func answerServer(t *testing.T, base string, eng Engine, diag io.Writer, extraConfig string) *Server {
	t.Helper()
	if diag == nil {
		diag = io.Discard
	}
	home := t.TempDir()
	configBody := fmt.Sprintf("upstream = %q\napi_key_env = \"\"\n%s", base, extraConfig)
	if err := os.WriteFile(filepath.Join(home, "config.toml"), []byte(configBody), 0o600); err != nil {
		t.Fatal(err)
	}
	s, err := NewServer(
		Options{Home: home, Project: t.TempDir(), Diag: diag},
		func(context.Context) (Engine, error) { return eng, nil },
	)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// answerUpstream starts a stubbed upstream returning body and points a proxy at it. The
// engine and diag are the caller's, so each test drives its branch.
func answerUpstream(t *testing.T, status int, body []byte, eng Engine, diag io.Writer, extraConfig string) *Server {
	t.Helper()
	up := &capturingUpstream{
		status: status,
		header: http.Header{"Content-Type": {"application/json"}},
		body:   body,
	}
	ts := httptest.NewServer(up)
	t.Cleanup(ts.Close)
	return answerServer(t, ts.URL, eng, diag, extraConfig)
}

// postMessages drives the whole handler with a minimal, valid request body.
func postMessages(s *Server) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, pathMessages,
		strings.NewReader(`{"messages":[{"role":"user","content":"hi"}]}`)))
	return rec
}

func TestRestoreNonStreamingResponse(t *testing.T) {
	const value = "db.prod.local"
	body := []byte(`{"id":"msg_1","content":[` +
		`{"type":"text","text":"the host is <HOST_1>"},` +
		`{"type":"thinking","thinking":"<HOST_1>"}]}`)
	s := answerUpstream(t, http.StatusOK, body, &answerEngine{replace: [][2]string{{"<HOST_1>", value}}}, nil, "")

	rec := postMessages(s)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	doc := decodeJSON(t, rec.Body.Bytes())
	blocks, ok := doc["content"].([]any)
	if !ok || len(blocks) != 2 {
		t.Fatalf("content = %#v, want two blocks", doc["content"])
	}
	if got := blocks[0].(map[string]any)["text"]; got != "the host is "+value {
		t.Errorf("text block = %v, want the value put back", got)
	}
	if got := blocks[1].(map[string]any)["thinking"]; got != "<HOST_1>" {
		t.Errorf("thinking = %v, want it forwarded untouched", got)
	}
}

func TestRestoreNonStreamingToolUseInput(t *testing.T) {
	const value = "db.prod.local"
	body := []byte(`{"content":[{"type":"tool_use","id":"tu_1","name":"Bash","input":{` +
		`"command":"ssh <HOST_1>","env":{"TOKEN":"<USER_1>"},"timeout":5}}]}`)
	eng := &answerEngine{replace: [][2]string{{"<HOST_1>", value}, {"<USER_1>", "secret-token"}}}
	s := answerUpstream(t, http.StatusOK, body, eng, nil, "")

	rec := postMessages(s)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	blocks, _ := decodeJSON(t, rec.Body.Bytes())["content"].([]any)
	if len(blocks) != 1 {
		t.Fatalf("content blocks = %d, want 1", len(blocks))
	}
	input, _ := blocks[0].(map[string]any)["input"].(map[string]any)
	if got := input["command"]; got != "ssh "+value {
		t.Errorf("input.command = %v, want the value put back", got)
	}
	env, _ := input["env"].(map[string]any)
	if got := env["TOKEN"]; got != "secret-token" {
		t.Errorf("input.env.TOKEN = %v, want the value put back", got)
	}
	if n, ok := input["timeout"].(json.Number); !ok || n.String() != "5" {
		t.Errorf("input.timeout = %#v, want the number 5", input["timeout"])
	}
}

// TestFailClosedAnswersBadGateway is the canary. Under fail_closed an answer with a token
// that cannot be restored is refused 502 with a body that names the types and carries
// neither a value nor a token — in the body or on the diagnostic writer.
//
// The one field a value is allowed — required — to appear in is the body of a *successful*
// answer, asserted positively at the end so a later reader cannot turn this into a blanket
// ban, the same shape the hook and MCP tests carry.
func TestFailClosedAnswersBadGateway(t *testing.T) {
	const (
		value = "db.prod.local"
		token = "<HOST_1>"
	)
	answer := []byte(`{"content":[{"type":"text","text":"the host is ` + token + `"}]}`)

	var diag bytes.Buffer
	eng := &answerEngine{unresolved: []placeholder.Token{{Type: "HOST", Raw: token}}}
	rec := postMessages(answerUpstream(t, http.StatusOK, answer, eng, &diag, ""))

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
	if got := diag.String(); strings.Contains(got, value) || strings.Contains(got, token) {
		t.Errorf("diag must carry neither the value nor the token: %q", got)
	}

	// The exception, positively: a successful answer's body is where the value belongs.
	ok := postMessages(answerUpstream(t, http.StatusOK, answer, &answerEngine{replace: [][2]string{{token, value}}}, nil, ""))
	if !strings.Contains(ok.Body.String(), value) {
		t.Errorf("a successful answer must carry the restored value: %q", ok.Body.String())
	}
}

// TestRefusalJournalsABlockedRow pins the journal: one blocked row per distinct type, not
// per token. The answer carries EMAIL twice and HOST once — three tokens, two types — and
// the journal write itself fails, so the same case shows the failure reaching diag without
// changing the answer.
func TestRefusalJournalsABlockedRow(t *testing.T) {
	const email, host = "<EMAIL_1>", "<HOST_1>"
	answer := []byte(`{"content":[` +
		`{"type":"text","text":"write ` + email + `"},` +
		`{"type":"text","text":"reach ` + host + `"},` +
		`{"type":"text","text":"cc ` + email + `"}]}`)
	var diag bytes.Buffer
	eng := &answerEngine{
		unresolved: []placeholder.Token{{Type: "EMAIL", Raw: email}, {Type: "HOST", Raw: host}},
		blockErr:   errors.New("journal is read-only"),
	}
	rec := postMessages(answerUpstream(t, http.StatusOK, answer, eng, &diag, ""))

	if rec.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusBadGateway)
	}
	if got := eng.blockedTypes(); !slices.Equal(got, []string{"EMAIL", "HOST"}) {
		t.Errorf("blocked types = %v, want one row per distinct type, in order", got)
	}
	const want = `{"type":"error","error":{"type":"shade_unresolved","message":"3 placeholders could not be restored: EMAIL, HOST"}}`
	if got := rec.Body.String(); got != want {
		t.Errorf("body = %q, want %q", got, want)
	}
	if got := diag.String(); !strings.Contains(got, "stats not recorded") || !strings.Contains(got, "journal is read-only") {
		t.Errorf("diag = %q, want the stats warning", got)
	}
}

// TestFailOpenLogPassesTheTokensThrough pins fail_open_log: the answer goes out as it came,
// tokens and all, and diag names the types that stayed — never a value, never a token.
func TestFailOpenLogPassesTheTokensThrough(t *testing.T) {
	const value, token = "db.prod.local", "<HOST_1>"
	answer := []byte(`{"content":[{"type":"text","text":"the host is ` + token + `"}]}`)
	var diag bytes.Buffer
	eng := &answerEngine{
		unresolved: []placeholder.Token{{Type: "HOST", Raw: token}},
		recordErr:  errors.New("trace write failed"),
	}
	rec := postMessages(answerUpstream(t, http.StatusOK, answer, eng, &diag, "fail_policy = \"fail_open_log\"\n"))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	if got := rec.Body.String(); got != string(answer) {
		t.Errorf("body = %q, want the answer passed through unchanged", got)
	}
	if got := eng.blockedTypes(); len(got) != 0 {
		t.Errorf("fail_open_log journaled %v, want nothing", got)
	}
	line := diag.String()
	if !strings.Contains(line, "fail_open_log") || !strings.Contains(line, "HOST") {
		t.Errorf("diag = %q, want the types that stayed unresolved", line)
	}
	if !strings.Contains(line, "stats not recorded") {
		t.Errorf("diag = %q, want the failed trace reported", line)
	}
	if strings.Contains(line, value) || strings.Contains(line, token) {
		t.Errorf("diag = %q, must carry neither the value nor the token", line)
	}
}

// TestErrorBodyIsAnonymizedNotCut pins §9: an upstream error body keeps its wording — a
// client retries on it — while the value inside becomes a token rather than the body being
// cut. The failed counter write must reach diag without entering the body.
func TestErrorBodyIsAnonymizedNotCut(t *testing.T) {
	const value = "db.prod.local"
	body := []byte(`{"type":"error","error":{"type":"overloaded_error","message":"could not reach ` + value + `"}}`)
	var diag bytes.Buffer
	eng := &answerEngine{replace: [][2]string{{value, "<HOST_1>"}}, recordErr: errors.New("counter write failed")}
	rec := postMessages(answerUpstream(t, 529, body, eng, &diag, ""))

	if rec.Code != 529 {
		t.Fatalf("status = %d, want 529", rec.Code)
	}
	got := rec.Body.String()
	if strings.Contains(got, value) {
		t.Errorf("the error body leaked the value: %q", got)
	}
	if !strings.Contains(got, "overloaded_error") || !strings.Contains(got, "could not reach") {
		t.Errorf("the wording must survive: %q", got)
	}
	if !strings.Contains(got, "<HOST_1>") {
		t.Errorf("the value must be replaced by a token: %q", got)
	}
	if !strings.Contains(diag.String(), "stats not recorded") {
		t.Errorf("diag = %q, want the failed counter write reported", diag.String())
	}
}

// TestErrorBodyFallsBackWhenAnonymizeFails pins the safe side: when anonymizing the error
// body fails, the body is replaced with a bare status line, so no original text reaches the
// client either way.
func TestErrorBodyFallsBackWhenAnonymizeFails(t *testing.T) {
	const value = "db.prod.local"
	body := []byte(`{"error":{"message":"could not reach ` + value + `"}}`)
	eng := &answerEngine{anonErr: errors.New("rule set is broken"), anonErrOn: "could not reach"}
	rec := postMessages(answerUpstream(t, http.StatusBadGateway, body, eng, nil, ""))

	if rec.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusBadGateway)
	}
	if got := rec.Body.String(); got != "502 Bad Gateway" {
		t.Errorf("body = %q, want the bare status line", got)
	}
	if got := rec.Body.String(); strings.Contains(got, value) || strings.Contains(got, "could not reach") {
		t.Errorf("the fallback leaked the body: %q", got)
	}
}

// TestNonJSONAnswerIsPassedThroughUnmodified pins the intermediate state: an answer this
// arm cannot parse — an event stream, whose pipe is a later task, or a gateway's own page —
// is handed on whole rather than mangled or refused. Claude Code is mid-conversation, so a
// 502 here would be worse than passing the bytes on as they arrived.
func TestNonJSONAnswerIsPassedThroughUnmodified(t *testing.T) {
	body := []byte("event: content_block_delta\ndata: {\"delta\":{\"text\":\"<HOST_1>\"}}\n\n")
	eng := &answerEngine{replace: [][2]string{{"<HOST_1>", "db.prod.local"}}}
	rec := postMessages(answerUpstream(t, http.StatusOK, body, eng, nil, ""))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	if got := rec.Body.String(); got != string(body) {
		t.Errorf("body = %q, want the answer passed through unchanged", got)
	}
	if eng.saw("<HOST_1>") {
		t.Error("a stream must not be routed through the engine before the streaming task lands")
	}
}

func TestRetryAfterAndRateLimitHeadersAreForwarded(t *testing.T) {
	up := &capturingUpstream{
		status: http.StatusTooManyRequests,
		header: http.Header{
			"Content-Type":                          {"application/json"},
			"Retry-After":                           {"7"},
			"X-Should-Retry":                        {"true"},
			"Anthropic-Ratelimit-Unified-Remaining": {"0"},
		},
		body: []byte(`{"type":"error","error":{"type":"rate_limit_error","message":"slow down"}}`),
	}
	ts := httptest.NewServer(up)
	t.Cleanup(ts.Close)
	s := answerServer(t, ts.URL, &answerEngine{}, nil, "")

	rec := postMessages(s)
	for k, want := range map[string]string{
		"Retry-After":                           "7",
		"X-Should-Retry":                        "true",
		"Anthropic-Ratelimit-Unified-Remaining": "0",
	} {
		if got := rec.Header().Get(k); got != want {
			t.Errorf("%s = %q, want %q", k, got, want)
		}
	}
}
