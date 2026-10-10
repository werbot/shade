package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// e2eServeValue is the real value the proxy must hide on the way out and put back on the
// way in: an ssh login and host, the shape the builtin USER/HOST rules match. Synthetic on
// purpose — a real credential has no business in a test.
const (
	e2eServeLiteral = "ssh staging@db.prod.local"
	e2eServeValue   = "staging@db.prod.local"
)

// proxyClient bounds every request: a proxy that stops answering must fail the test, not
// hang it. The streams here are tiny, so one deadline covers the whole exchange.
var proxyClient = &http.Client{Timeout: 15 * time.Second}

// sseStub stands in for the model provider: it records every request body it receives and
// answers with an event stream. answer derives the stream from the request, so the
// two-clients test can hand each client its own token back.
type sseStub struct {
	mu     sync.Mutex
	bodies []string
	answer func(body string) string
}

func (s *sseStub) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	b, _ := io.ReadAll(r.Body)
	body := string(b)
	s.mu.Lock()
	s.bodies = append(s.bodies, body)
	s.mu.Unlock()
	// count_tokens is forwarded like messages but answers with a token count, not a stream.
	if strings.HasSuffix(r.URL.Path, "/count_tokens") {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"input_tokens":1}`)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.WriteHeader(http.StatusOK)
	_, _ = io.WriteString(w, s.answer(body))
}

// received is everything the upstream saw, joined.
func (s *sseStub) received() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return strings.Join(s.bodies, "\n")
}

// proxyFixture is a running `shade serve` with its stub upstream: what a client needs to
// send a request through it.
type proxyFixture struct {
	base string
	home string
	dir  string
	stub *sseStub
}

// newProxyFixture stands up the stub, writes the global config that points shade serve at
// it, and starts the built binary on an OS-chosen port (--port 0) so a run never races
// another for a port. The wrap line carries the real bound port back to the client.
func newProxyFixture(t *testing.T, answer func(string) string) *proxyFixture {
	t.Helper()
	f := &proxyFixture{home: t.TempDir(), dir: gitDir(t), stub: &sseStub{answer: answer}}
	up := httptest.NewServer(f.stub)
	t.Cleanup(up.Close)

	cfg := fmt.Sprintf("upstream = %q\napi_key_env = \"\"\n", up.URL)
	if err := os.WriteFile(filepath.Join(f.home, "config.toml"), []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	p := startServe(t, buildShade(t), f.home, f.dir)
	line := waitForLine(t, p.out, 10*time.Second)
	const prefix = "export ANTHROPIC_BASE_URL="
	if !strings.HasPrefix(line, prefix) {
		t.Fatalf("wrap line = %q (stderr: %q)", line, p.err.String())
	}
	f.base = strings.TrimPrefix(line, prefix)
	return f
}

// postMessages sends one Anthropic request through the proxy and returns the status and the
// answer body. It reports an error rather than failing, so it is safe to call concurrently.
func postMessages(base, text string) (int, string, error) {
	body := fmt.Sprintf(
		`{"model":"claude-opus-5","max_tokens":100,"messages":[{"role":"user","content":[{"type":"text","text":%s}]}]}`,
		jsonString(text))
	req, err := http.NewRequest(http.MethodPost, base+"/v1/messages", strings.NewReader(body))
	if err != nil {
		return 0, "", err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := proxyClient.Do(req)
	if err != nil {
		return 0, "", err
	}
	defer resp.Body.Close()
	out, err := io.ReadAll(resp.Body)
	if err != nil {
		return 0, "", err
	}
	return resp.StatusCode, string(out), nil
}

// sseFrame renders one server-sent event the way the provider does.
func sseFrame(event, data string) string {
	return "event: " + event + "\ndata: " + data + "\n\n"
}

// jsonString marshals without HTML escaping, so a fixture reads "<HOST_1>", not "<".
func jsonString(v any) string {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(v)
	return strings.TrimSuffix(b.String(), "\n")
}

// streamWithDeltas is a complete Anthropic answer whose single text block is the deltas
// concatenated. Passing the placeholder in pieces exercises a token split across frames.
func streamWithDeltas(deltas ...string) string {
	var b strings.Builder
	b.WriteString(sseFrame("message_start", `{"type":"message_start","message":{"id":"msg_1","type":"message","role":"assistant","model":"claude-opus-5","content":[],"stop_reason":null,"stop_sequence":null,"usage":{"input_tokens":1,"output_tokens":1}}}`))
	b.WriteString(sseFrame("content_block_start", `{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`))
	for _, d := range deltas {
		b.WriteString(sseFrame("content_block_delta", jsonString(map[string]any{
			"type": "content_block_delta", "index": 0,
			"delta": map[string]any{"type": "text_delta", "text": d},
		})))
	}
	b.WriteString(sseFrame("content_block_stop", `{"type":"content_block_stop","index":0}`))
	b.WriteString(sseFrame("message_delta", `{"type":"message_delta","delta":{"stop_reason":"end_turn","stop_sequence":null},"usage":{"output_tokens":1}}`))
	b.WriteString(sseFrame("message_stop", `{"type":"message_stop"}`))
	return b.String()
}

// collectedText is the concatenation of every text_delta the client received: what a client
// would render, joined across frame boundaries.
func collectedText(stream string) string {
	var sb strings.Builder
	for _, block := range strings.Split(stream, "\n\n") {
		var data string
		for _, line := range strings.Split(block, "\n") {
			if v, ok := strings.CutPrefix(line, "data: "); ok {
				data = v
			}
		}
		var frame struct {
			Delta struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"delta"`
		}
		if data == "" || json.Unmarshal([]byte(data), &frame) != nil {
			continue
		}
		if frame.Delta.Type == "text_delta" {
			sb.WriteString(frame.Delta.Text)
		}
	}
	return sb.String()
}

// requestText reads the first message text out of an Anthropic request body. It runs inside
// the stub's handler goroutine, so it must not touch t.
func requestText(body string) string {
	var req struct {
		Messages []struct {
			Content []struct {
				Text string `json:"text"`
			} `json:"content"`
		} `json:"messages"`
	}
	if json.Unmarshal([]byte(body), &req) != nil {
		return ""
	}
	for _, m := range req.Messages {
		for _, c := range m.Content {
			if c.Text != "" {
				return c.Text
			}
		}
	}
	return ""
}

// TestServeAnonymizesThePromptAndRestoresTheAnswer is the phase's acceptance criterion: a
// real `shade serve` process, the built binary, a stub upstream answering with a
// placeholder. The request that leaves carries the token, the answer that returns carries
// the real value.
func TestServeAnonymizesThePromptAndRestoresTheAnswer(t *testing.T) {
	f := newProxyFixture(t, func(string) string { return streamWithDeltas("<USER_1>@<HOST_1>") })
	code, got, err := postMessages(f.base, e2eServeLiteral)
	if err != nil {
		t.Fatal(err)
	}
	if code != http.StatusOK {
		t.Fatalf("proxy status = %d, want 200: %s", code, got)
	}
	// 1. What left for the upstream carried the token, never the value.
	sent := f.stub.received()
	if !strings.Contains(sent, "<USER_1>@<HOST_1>") {
		t.Fatalf("the upstream did not receive the tokens: %s", sent)
	}
	if strings.Contains(sent, e2eServeValue) {
		t.Fatalf("the literal value left for the upstream: %s", sent)
	}
	// 2. What came back to the client carried the real value, not the token.
	if text := collectedText(got); text != e2eServeValue {
		t.Fatalf("the client received %q, want %q", text, e2eServeValue)
	}
	if strings.Contains(got, "<HOST_1>") {
		t.Fatalf("a placeholder reached the client: %s", got)
	}
}

// TestServeRestoresAPlaceholderSplitAcrossFrames — the placeholder is cut in the middle of
// the token: "<HOST" ends one frame and "_1>" opens the next. The flusher must hold the
// open "<" tail and restore the token whole.
func TestServeRestoresAPlaceholderSplitAcrossFrames(t *testing.T) {
	f := newProxyFixture(t, func(string) string { return streamWithDeltas("ssh <USER_1>@<HOST", "_1>") })
	code, got, err := postMessages(f.base, e2eServeLiteral)
	if err != nil {
		t.Fatal(err)
	}
	if code != http.StatusOK {
		t.Fatalf("proxy status = %d, want 200: %s", code, got)
	}
	if text := collectedText(got); text != e2eServeLiteral {
		t.Fatalf("the split placeholder was not restored: collected %q, want %q", text, e2eServeLiteral)
	}
	if strings.Contains(got, "<HOST") {
		t.Fatalf("a placeholder fragment reached the client: %s", got)
	}
}

// TestServeSendsNoLiteralUpstream is the canary: no literal value appears anywhere in what
// the stub received.
func TestServeSendsNoLiteralUpstream(t *testing.T) {
	f := newProxyFixture(t, func(string) string { return streamWithDeltas("ok") })
	if code, got, err := postMessages(f.base, e2eServeLiteral); err != nil {
		t.Fatal(err)
	} else if code != http.StatusOK {
		t.Fatalf("proxy status = %d: %s", code, got)
	}
	sent := f.stub.received()
	for _, literal := range []string{"staging", "db.prod.local", e2eServeValue, e2eServeLiteral} {
		if strings.Contains(sent, literal) {
			t.Fatalf("the upstream received the literal %q: %s", literal, sent)
		}
	}
	if !strings.Contains(sent, "<USER_1>") || !strings.Contains(sent, "<HOST_1>") {
		t.Fatalf("the upstream received no tokens: %s", sent)
	}
}

// TestServeTwoClientsAtOnce runs two concurrent clients whose values differ. The stub
// echoes each request's own token back, so a client that received the other's value — a
// shared flusher or accumulator, the Review Focus 5 hazard — fails here.
func TestServeTwoClientsAtOnce(t *testing.T) {
	f := newProxyFixture(t, func(body string) string { return streamWithDeltas(requestText(body)) })

	clients := []string{"ssh alpha@host-a.example", "ssh beta@host-b.example"}
	bodies := make([]string, len(clients))
	var wg sync.WaitGroup
	for i, text := range clients {
		wg.Add(1)
		go func() {
			defer wg.Done()
			code, got, err := postMessages(f.base, text)
			if err != nil {
				t.Errorf("client %d: %v", i, err)
				return
			}
			if code != http.StatusOK {
				t.Errorf("client %d: status = %d", i, code)
			}
			bodies[i] = got
		}()
	}
	wg.Wait()

	for i, text := range clients {
		if got := collectedText(bodies[i]); got != text {
			t.Errorf("client %d received %q, want its own %q", i, got, text)
		}
	}
	sent := f.stub.received()
	for _, text := range clients {
		if strings.Contains(sent, strings.TrimPrefix(text, "ssh ")) {
			t.Fatalf("the upstream received a literal: %s", sent)
		}
	}
}
