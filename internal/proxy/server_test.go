package proxy

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/werbot/shade/internal/core"
)

// fakeEngine is the Engine stand-in: the router and the client must be testable without
// a database, so the engine does nothing here. Its methods hand the text back unchanged;
// the tasks that anonymize and restore replace it with an engine that rewrites text.
type fakeEngine struct{ root string }

func (f *fakeEngine) Anonymize(_ context.Context, text string) (core.Result, error) {
	return core.Result{Text: text}, nil
}

func (f *fakeEngine) Restore(_ context.Context, text string) (core.Result, error) {
	return core.Result{Text: text}, nil
}

func (f *fakeEngine) RecordBlocked(context.Context, string) error { return nil }

func (f *fakeEngine) RootPath() string { return f.root }

func (f *fakeEngine) Close() error { return nil }

// recordingUpstream is an httptest upstream that keeps what actually arrived. The router
// and the client are judged on the request the upstream received, not on the one the
// proxy meant to send.
type recordingUpstream struct {
	mu       sync.Mutex
	method   string
	path     string
	query    string
	header   http.Header
	body     string
	transfer []string
}

func (u *recordingUpstream) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	b, _ := io.ReadAll(r.Body)
	u.mu.Lock()
	u.method = r.Method
	u.path = r.URL.Path
	u.query = r.URL.RawQuery
	u.header = r.Header.Clone()
	u.body = string(b)
	u.transfer = append([]string(nil), r.TransferEncoding...)
	u.mu.Unlock()
	w.WriteHeader(http.StatusOK)
}

// snapshot is what the upstream saw, copied out under the lock: the request outlives the
// handler goroutine, and reading it after Do races with nothing only by accident.
func (u *recordingUpstream) snapshot() recordingUpstream {
	u.mu.Lock()
	defer u.mu.Unlock()
	return recordingUpstream{
		method:   u.method,
		path:     u.path,
		query:    u.query,
		header:   u.header.Clone(),
		body:     u.body,
		transfer: append([]string(nil), u.transfer...),
	}
}

// newTestServer assembles a proxy whose config lives in a temporary home. configBody is
// the whole file: a test sets upstream and api_key_env here, because upstreamRequest reads
// them from config.Load on every request.
func newTestServer(t *testing.T, configBody string) *Server {
	t.Helper()
	home := t.TempDir()
	if err := os.WriteFile(filepath.Join(home, "config.toml"), []byte(configBody), 0o600); err != nil {
		t.Fatal(err)
	}
	srv, err := NewServer(
		Options{Home: home, Project: t.TempDir(), Diag: io.Discard},
		func(context.Context) (Engine, error) { return &fakeEngine{root: home}, nil },
	)
	if err != nil {
		t.Fatal(err)
	}
	return srv
}

// forward builds the upstream request for in, sends it through the server's own client,
// and returns what the upstream saw. It is the whole client path, minus the handler the
// later tasks add.
func forward(t *testing.T, s *Server, up *recordingUpstream, in *http.Request, body []byte) recordingUpstream {
	t.Helper()
	req, _, err := s.upstreamRequest(t.Context(), in, body)
	if err != nil {
		t.Fatalf("upstreamRequest: %v", err)
	}
	resp, err := s.client.Do(req)
	if err != nil {
		t.Fatalf("do upstream: %v", err)
	}
	defer resp.Body.Close()
	if _, err := io.Copy(io.Discard, resp.Body); err != nil {
		t.Fatalf("drain upstream response: %v", err)
	}
	return up.snapshot()
}

// upstreamServer starts a recording upstream and points the config at it.
func upstreamServer(t *testing.T, apiKeyEnv, base string) (*recordingUpstream, *Server) {
	t.Helper()
	up := &recordingUpstream{}
	hs := httptest.NewServer(up)
	t.Cleanup(hs.Close)
	configBody := fmt.Sprintf("upstream = %q\napi_key_env = %q\n", hs.URL+base, apiKeyEnv)
	return up, newTestServer(t, configBody)
}

func TestRouterMatchesPathAndIgnoresQuery(t *testing.T) {
	s := newTestServer(t, "")
	// The paths the client sends, with the query it attaches; the query must not change
	// the handler, and neither may a missing trailing slash be needed to match.
	for _, target := range []string{
		"/v1/messages?beta=true",
		"/v1/messages/count_tokens",
		"/v1/chat/completions",
	} {
		rec := httptest.NewRecorder()
		s.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, target, nil))
		if rec.Code != http.StatusNotImplemented {
			t.Errorf("%s: status = %d, want %d", target, rec.Code, http.StatusNotImplemented)
		}
	}
}

func TestHelloProbeAnswersOK(t *testing.T) {
	s := newTestServer(t, "")
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, httptest.NewRequest(http.MethodHead, "/api/hello", nil))
	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusOK)
	}
}

func TestUnknownPathIsNotFound(t *testing.T) {
	s := newTestServer(t, "")
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/embeddings", nil))
	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusNotFound)
	}
}

func TestUpstreamBaseURLKeepsItsPathPrefix(t *testing.T) {
	up, s := upstreamServer(t, "", "/anthropic-aws")
	in := httptest.NewRequest(http.MethodPost, "/v1/messages?beta=true", nil)
	got := forward(t, s, up, in, []byte(`{}`))
	if got.path != "/anthropic-aws/v1/messages" {
		t.Errorf("upstream path = %q, want %q", got.path, "/anthropic-aws/v1/messages")
	}
	if got.query != "beta=true" {
		t.Errorf("upstream query = %q, want %q", got.query, "beta=true")
	}
}

func TestClientCredentialsAreForwardedByDefault(t *testing.T) {
	up, s := upstreamServer(t, "", "")
	in := httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
	in.Header.Set("Authorization", "Bearer subscription-token")
	in.Header.Set("X-Api-Key", "client-key")
	got := forward(t, s, up, in, []byte(`{}`))
	if got.header.Get("Authorization") != "Bearer subscription-token" {
		t.Errorf("upstream Authorization = %q, want the client's", got.header.Get("Authorization"))
	}
	if got.header.Get("X-Api-Key") != "client-key" {
		t.Errorf("upstream X-Api-Key = %q, want the client's", got.header.Get("X-Api-Key"))
	}
}

func TestAPIKeyEnvOverridesTheClientCredential(t *testing.T) {
	t.Setenv("SHADE_TEST_KEY", "sk-from-env")
	up, s := upstreamServer(t, "SHADE_TEST_KEY", "")
	in := httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
	in.Header.Set("Authorization", "Bearer subscription-token")
	in.Header.Set("X-Api-Key", "client-key")
	got := forward(t, s, up, in, []byte(`{}`))
	if got.header.Get("X-Api-Key") != "sk-from-env" {
		t.Errorf("upstream X-Api-Key = %q, want %q", got.header.Get("X-Api-Key"), "sk-from-env")
	}
	if got.header.Get("Authorization") != "" {
		t.Errorf("upstream Authorization = %q, want it replaced by the api key", got.header.Get("Authorization"))
	}
}

func TestVersionAndBetaHeadersAreForwarded(t *testing.T) {
	up, s := upstreamServer(t, "", "")
	in := httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
	in.Header.Set("Anthropic-Version", "2023-06-01")
	in.Header.Set("Anthropic-Beta", "prompt-caching-2024-07-31")
	got := forward(t, s, up, in, []byte(`{}`))
	if got.header.Get("Anthropic-Version") != "2023-06-01" {
		t.Errorf("upstream Anthropic-Version = %q", got.header.Get("Anthropic-Version"))
	}
	if got.header.Get("Anthropic-Beta") != "prompt-caching-2024-07-31" {
		t.Errorf("upstream Anthropic-Beta = %q", got.header.Get("Anthropic-Beta"))
	}
}

func TestHopByHopHeadersAreNotForwarded(t *testing.T) {
	up, s := upstreamServer(t, "", "")
	in := httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
	in.Header.Set("Connection", "keep-alive")
	in.Header.Set("Transfer-Encoding", "chunked")
	in.Header.Set("X-Kept", "yes")
	got := forward(t, s, up, in, []byte(`{}`))
	if got.header.Get("Connection") != "" {
		t.Errorf("upstream Connection = %q, want it dropped", got.header.Get("Connection"))
	}
	if len(got.transfer) != 0 {
		t.Errorf("upstream TransferEncoding = %v, want none", got.transfer)
	}
	// A normal header still arrives: the walk drops the hop-by-hop set, not everything.
	if got.header.Get("X-Kept") != "yes" {
		t.Errorf("upstream X-Kept = %q, want %q", got.header.Get("X-Kept"), "yes")
	}
}
