package api_test

import (
	"mime"
	"net/http"
	"testing"
	"testing/fstest"
)

// testPage is the built page of the tests: one file of each kind the real build has, and the
// .gitkeep that dist/ holds on every checkout and the embed takes along. assets/fonts/ is a
// directory in a directory and assets/.x is a dotfile in one, which no build emits yet.
var testPage = fstest.MapFS{
	"index.html":            {Data: []byte("<!doctype html><title>shade</title>")},
	"assets/app.js":         {Data: []byte("console.log('shade');")},
	"assets/app.css":        {Data: []byte("body{margin:0}")},
	"assets/fonts/ui.woff2": {Data: []byte("font")},
	"assets/.x":             {},
	".gitkeep":              {},
}

// pageFiles are the URLs of testPage, the file each one serves and the media type it comes with.
var pageFiles = []struct{ url, file, mediaType string }{
	{"/", "index.html", "text/html"},
	{"/assets/app.js", "assets/app.js", "text/javascript"},
	{"/assets/app.css", "assets/app.css", "text/css"},
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
		{http.MethodGet, "/assets/.x", http.StatusNotFound},
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
