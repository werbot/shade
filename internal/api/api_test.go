package api_test

import (
	"context"
	"encoding/json"
	"errors"
	"mime"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/werbot/shade/internal/api"
	"github.com/werbot/shade/internal/core"
)

// testPage is the built page of the tests: one file of each kind the real build has, and the
// .gitkeep that dist/ holds on every checkout and the embed takes along. assets/fonts/ is a
// directory in a directory, which no build emits yet.
var testPage = fstest.MapFS{
	"index.html":            {Data: []byte("<!doctype html><title>shade</title>")},
	"assets/app.js":         {Data: []byte("console.log('shade');")},
	"assets/app.css":        {Data: []byte("body{margin:0}")},
	"assets/fonts/ui.woff2": {Data: []byte("font")},
	".gitkeep":              {},
}

// pageFiles are the URLs of testPage, the file each one serves and the media type it comes with.
var pageFiles = []struct{ url, file, mediaType string }{
	{"/", "index.html", "text/html"},
	{"/assets/app.js", "assets/app.js", "text/javascript"},
	{"/assets/app.css", "assets/app.css", "text/css"},
}

// fixture is the handler over a real engine on a temporary home, as `shade ui` runs it. The
// handler and a test each open their own engine on the one store, the way the UI and the CLI do.
type fixture struct {
	home, dir string
	// open is what the handler calls for an engine. It starts as a real engine on home and dir; a
	// test replaces it to make the open fail or to watch what the handler does with the engine.
	open    api.Opener
	handler http.Handler
}

func newFixture(t *testing.T) *fixture {
	f := &fixture{home: t.TempDir(), dir: t.TempDir()}
	f.open = func(ctx context.Context) (*core.Engine, error) {
		return core.New(ctx, f.home, f.dir, "ui")
	}
	f.handler = api.New(api.Options{
		Port:   testPort,
		Static: testPage,
		Open:   func(ctx context.Context) (*core.Engine, error) { return f.open(ctx) },
	})
	return f
}

// engine opens an engine of the test's own on the fixture's store, to put rules in and to read
// them back. It is closed with the test.
func (f *fixture) engine(t *testing.T) *core.Engine {
	t.Helper()
	e, err := core.New(t.Context(), f.home, f.dir, "ui")
	if err != nil {
		t.Fatalf("core.New: %v", err)
	}
	t.Cleanup(func() { e.Close() })
	return e
}

// refuseEngine makes the fixture fail the test when the handler opens an engine. It is for the
// requests that have no use for the store: one refused for its shape, and a candidate rule, which
// is the definition and the text and nothing else and is tried at every pause of the typing.
func refuseEngine(t *testing.T, f *fixture) {
	t.Helper()
	f.open = func(context.Context) (*core.Engine, error) {
		t.Error("the engine was opened")
		return nil, errors.New("not to be opened")
	}
}

// recordEngines makes the fixture append every engine the handler opens to opened, in order, to
// see what became of them when the request ended.
func recordEngines(f *fixture, opened *[]*core.Engine) {
	open := f.open
	f.open = func(ctx context.Context) (*core.Engine, error) {
		e, err := open(ctx)
		if err == nil {
			*opened = append(*opened, e)
		}
		return e, err
	}
}

// do sends one request to the handler as the page would from the bound address: the Host is that
// of the listener, and a POST or a PATCH declares JSON even with no body, which is what Guard asks
// of every one. hdr are header names and values in turn, set last so a test may replace a default.
func (f *fixture) do(method, target, body string, hdr ...string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, target, strings.NewReader(body))
	r.Host = hostByIP
	if method == http.MethodPost || method == http.MethodPatch {
		r.Header.Set("Content-Type", "application/json")
	}
	for pair := range slices.Chunk(hdr, 2) {
		r.Header.Set(pair[0], pair[1])
	}
	rec := httptest.NewRecorder()
	f.handler.ServeHTTP(rec, r)
	return rec
}

// mediaType is the media type of the answer without its parameters. It is parsed, not matched by
// a prefix, so "text/javascript-x" and a missing type both fail.
func mediaType(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	header := rec.Result().Header.Get("Content-Type")
	typ, _, err := mime.ParseMediaType(header)
	if err != nil {
		t.Fatalf("Content-Type = %q: %v", header, err)
	}
	return typ
}

// decodeJSON reads the body of the answer as the page does.
func decodeJSON[T any](t *testing.T, rec *httptest.ResponseRecorder) T {
	t.Helper()
	var v T
	if err := json.Unmarshal(rec.Body.Bytes(), &v); err != nil {
		t.Fatalf("body = %q: %v", rec.Body.String(), err)
	}
	return v
}

// wantError checks a refusal of the API: the status, and the JSON object with an error in it.
func wantError(t *testing.T, rec *httptest.ResponseRecorder, status int) {
	t.Helper()
	if rec.Code != status {
		t.Errorf("status = %d, want %d", rec.Code, status)
	}
	if typ := mediaType(t, rec); typ != "application/json" {
		t.Errorf("media type = %q, want application/json", typ)
	}
	if body := decodeJSON[map[string]string](t, rec); body["error"] == "" {
		t.Errorf("body = %q, want a JSON object with an error", rec.Body.String())
	}
}

func TestStaticPage(t *testing.T) {
	f := newFixture(t)
	for _, tc := range pageFiles {
		rec := f.do(http.MethodGet, tc.url, "")
		if rec.Code != http.StatusOK {
			t.Errorf("GET %s: status = %d, want 200", tc.url, rec.Code)
		}
		if got := mediaType(t, rec); got != tc.mediaType {
			t.Errorf("GET %s: media type = %q, want %q", tc.url, got, tc.mediaType)
		}
		if got := rec.Result().Header.Get("X-Content-Type-Options"); got != "nosniff" {
			t.Errorf("GET %s: X-Content-Type-Options = %q, want nosniff", tc.url, got)
		}
		if got, want := rec.Body.String(), string(testPage[tc.file].Data); got != want {
			t.Errorf("GET %s: body = %q, want %q", tc.url, got, want)
		}
	}
	if rec := f.do(http.MethodHead, "/", ""); rec.Code != http.StatusOK || rec.Body.Len() != 0 {
		t.Errorf("HEAD /: status = %d with %d body bytes, want 200 with none", rec.Code, rec.Body.Len())
	}
	// There is no fallback to index.html: the page has one screen and no client-side router.
	if rec := f.do(http.MethodGet, "/nope", ""); rec.Code != http.StatusNotFound {
		t.Errorf("GET /nope: status = %d, want 404", rec.Code)
	}
}

// The page is the built files and nothing else, though FileServerFS would also list a directory
// and serve the .gitkeep. The 200 rows are the controls: a handler that refuses everything fails.
func TestPageServesOnlyTheBuiltFiles(t *testing.T) {
	f := newFixture(t)
	for _, tc := range []struct {
		method, target string
		status         int
	}{
		{http.MethodGet, "/", http.StatusOK},
		{http.MethodHead, "/", http.StatusOK},
		{http.MethodGet, "/assets/app.js", http.StatusOK},
		{http.MethodHead, "/assets/app.js", http.StatusOK},
		{http.MethodGet, "/assets/fonts/ui.woff2", http.StatusOK},
		{http.MethodGet, "/assets", http.StatusNotFound},
		{http.MethodGet, "/assets/", http.StatusNotFound},
		{http.MethodGet, "/assets/fonts", http.StatusNotFound},
		{http.MethodGet, "/assets/fonts/", http.StatusNotFound},
		{http.MethodGet, "/.gitkeep", http.StatusNotFound},
	} {
		t.Run(tc.method+" "+tc.target, func(t *testing.T) {
			if rec := f.do(tc.method, tc.target, ""); rec.Code != tc.status {
				t.Errorf("status = %d, want %d", rec.Code, tc.status)
			}
		})
	}
}

// The page must start on every machine, so its types are pinned (see pageTypes). This test makes
// the host's own tables call every file of the page text/plain before the handler is built.
func TestPageTypesDoNotDependOnTheHost(t *testing.T) {
	for _, ext := range []string{".html", ".js", ".css"} {
		hostType := mime.TypeByExtension(ext)
		t.Cleanup(func() { mime.AddExtensionType(ext, hostType) })
		if err := mime.AddExtensionType(ext, "text/plain"); err != nil {
			t.Fatal(err)
		}
	}

	f := newFixture(t)
	for _, tc := range pageFiles {
		if got := mediaType(t, f.do(http.MethodGet, tc.url, "")); got != tc.mediaType {
			t.Errorf("GET %s: media type = %q on a host that calls it text/plain, want %q", tc.url, got, tc.mediaType)
		}
	}
}

func TestAPIFallsThroughToJSON404(t *testing.T) {
	f := newFixture(t)
	for _, tc := range []struct{ method, target string }{
		{http.MethodGet, "/api/nope"},
		{http.MethodPut, "/api/rules"},
	} {
		t.Run(tc.method+" "+tc.target, func(t *testing.T) {
			wantError(t, f.do(tc.method, tc.target, ""), http.StatusNotFound)
		})
	}
}

func TestPreflightIsRefused(t *testing.T) {
	f := newFixture(t)
	preflight := []string{
		"Origin", "http://evil.example",
		"Access-Control-Request-Method", "POST",
		"Access-Control-Request-Headers", "content-type",
	}
	for _, target := range []string{"/api/rules", "/"} {
		t.Run("OPTIONS "+target, func(t *testing.T) {
			rec := f.do(http.MethodOptions, target, "", preflight...)
			if rec.Code < 400 || rec.Code >= 500 {
				t.Errorf("status = %d, want a 4xx", rec.Code)
			}
			for key := range rec.Result().Header {
				if strings.HasPrefix(key, "Access-Control-") {
					t.Errorf("unexpected header %s", key)
				}
			}
		})
	}
	t.Run("POST /", func(t *testing.T) {
		rec := f.do(http.MethodPost, "/", "")
		wantError(t, rec, http.StatusMethodNotAllowed)
		if got := rec.Result().Header.Get("Allow"); got != "GET, HEAD" {
			t.Errorf("Allow = %q, want %q", got, "GET, HEAD")
		}
	})
}

// calls is a request of each endpoint that needs the engine and, for each write, one the store
// refuses (a taken name, a name that is not there, a rule that is gone). status is the answer when
// the engine opens. They are in the order that works on a fresh store.
var calls = []struct {
	name, method, target, body string
	status                     int
}{
	{"list", http.MethodGet, "/api/rules", "", http.StatusOK},
	{"test the active rules", http.MethodPost, "/api/test", `{"text":"x"}`, http.StatusOK},
	{"create", http.MethodPost, "/api/rules", `{"name":"x","type":"SECRET","pattern":"x"}`, http.StatusCreated},
	{"create refused", http.MethodPost, "/api/rules", `{"name":"x","type":"SECRET","pattern":"x"}`, http.StatusConflict},
	{"toggle", http.MethodPatch, "/api/rules/x?scope=project", `{"enabled":false}`, http.StatusNoContent},
	{"toggle refused", http.MethodPatch, "/api/rules/nope?scope=project", `{"enabled":false}`, http.StatusNotFound},
	{"delete", http.MethodDelete, "/api/rules/x?scope=project", "", http.StatusNoContent},
	{"delete refused", http.MethodDelete, "/api/rules/x?scope=project", "", http.StatusNotFound},
}

func TestOpenFailureIs500(t *testing.T) {
	f := newFixture(t)
	f.open = func(context.Context) (*core.Engine, error) { return nil, errors.New("store is locked") }
	for _, tc := range calls {
		t.Run(tc.name, func(t *testing.T) {
			rec := f.do(tc.method, tc.target, tc.body)
			wantError(t, rec, http.StatusInternalServerError)
			if msg := decodeJSON[map[string]string](t, rec)["error"]; !strings.Contains(msg, "store is locked") {
				t.Errorf("error = %q, want the cause in it", msg)
			}
		})
	}
}

// A request that needs the rules opens a store of its own, so one that is left open is a file
// handle lost per request for as long as the UI runs, a refusal included.
func TestEveryRequestClosesItsEngine(t *testing.T) {
	f := newFixture(t)
	var opened []*core.Engine
	recordEngines(f, &opened)
	for _, tc := range calls {
		before := len(opened)
		// The status shows that each row went the way its name says, so a refusal is the store's.
		if rec := f.do(tc.method, tc.target, tc.body); rec.Code != tc.status {
			t.Fatalf("%s: status = %d, want %d: %s", tc.name, rec.Code, tc.status, rec.Body)
		}
		if len(opened) != before+1 {
			t.Fatalf("%s: the handler opened %d engines, want one", tc.name, len(opened)-before)
		}
		if _, err := opened[before].Store().ListRules(t.Context(), nil); err == nil {
			t.Errorf("%s: the engine still answers after the request: it was not closed", tc.name)
		}
	}
}
