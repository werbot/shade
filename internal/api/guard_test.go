package api_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"maps"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/werbot/shade/internal/api"
)

// testPort is the port the guard under test takes for the one its listener bound.
const testPort = 4321

var (
	hostByIP   = "127.0.0.1:" + strconv.Itoa(testPort)
	hostByName = "localhost:" + strconv.Itoa(testPort)
)

// mutations are the methods whose Origin and Sec-Fetch-Site are checked: the three the API
// serves, a PUT, and a verb nobody defined. The last two must not be a way around the checks.
var mutations = []string{http.MethodPost, http.MethodPatch, http.MethodDelete, http.MethodPut, "PURGE"}

// probe is the handler Guard wraps. It counts the requests that reach it and keeps the result
// of reading their whole body, which is where the size cap shows.
type probe struct {
	calls   int
	readErr error
}

func (p *probe) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	p.calls++
	_, p.readErr = io.ReadAll(r.Body)
	w.WriteHeader(http.StatusOK)
}

// guardRequest builds a request for Guard. host is set as given, an empty one included: the
// Host header is often what a test is about.
func guardRequest(method, target, host string, hdr http.Header) *http.Request {
	r := httptest.NewRequest(method, target, nil)
	r.Host = host
	maps.Copy(r.Header, hdr)
	return r
}

// throughGuard runs r through a Guard that wraps a fresh probe.
func throughGuard(r *http.Request) (*httptest.ResponseRecorder, *probe) {
	p := &probe{}
	rec := httptest.NewRecorder()
	api.Guard(testPort, p).ServeHTTP(rec, r)
	return rec, p
}

// wantAnswer checks the answer to one request. A 200 is the probe's, so the request must have
// reached it once. Any other status is a refusal: it must not have reached the probe, and its
// body is the JSON error of the API, which is how the reply helpers are covered.
func wantAnswer(t *testing.T, rec *httptest.ResponseRecorder, p *probe, status int) {
	t.Helper()
	if rec.Code != status {
		t.Errorf("status = %d, want %d", rec.Code, status)
	}
	if status == http.StatusOK {
		if p.calls != 1 {
			t.Errorf("the handler was reached %d times, want once", p.calls)
		}
		return
	}
	if p.calls != 0 {
		t.Error("a refused request reached the handler")
	}
	if ct := rec.Result().Header.Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Errorf("Content-Type = %q, want application/json", ct)
	}
	var body map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil || body["error"] == "" {
		t.Errorf("body = %q, want a JSON object with an error", rec.Body.String())
	}
}

// eachMutation sends one request per mutating method, with a JSON Content-Type so only the
// header under test can refuse it. A nil values leaves the header out.
func eachMutation(t *testing.T, host, key string, values []string, status int) {
	t.Helper()
	for _, method := range mutations {
		t.Run(method, func(t *testing.T) {
			hdr := http.Header{"Content-Type": {"application/json"}}
			if values != nil {
				hdr[key] = values
			}
			rec, p := throughGuard(guardRequest(method, "/api/rules", host, hdr))
			wantAnswer(t, rec, p, status)
		})
	}
}

func TestGuardRefusesAForeignHost(t *testing.T) {
	port := strconv.Itoa(testPort)
	for _, tc := range []struct {
		name, host string
		status     int
	}{
		{"the address", hostByIP, http.StatusOK},
		{"the name", hostByName, http.StatusOK},
		{"a rebound name on the right port", "evil.example:" + port, http.StatusForbidden},
		{"no port", "127.0.0.1", http.StatusForbidden},
		{"another port", "127.0.0.1:" + strconv.Itoa(testPort+1), http.StatusForbidden},
		{"localhost as a subdomain", "localhost.evil.example:" + port, http.StatusForbidden},
		{"another letter case", "LOCALHOST:" + port, http.StatusForbidden},
		{"no Host at all", "", http.StatusForbidden},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec, p := throughGuard(guardRequest(http.MethodGet, "/api/rules", tc.host, nil))
			wantAnswer(t, rec, p, tc.status)
		})
	}
}

func TestGuardChecksTheOriginOfMutations(t *testing.T) {
	for _, tc := range []struct {
		name, host string
		origin     []string // nil: no Origin header
		status     int
	}{
		{"no Origin", hostByIP, nil, http.StatusOK},
		{"its own origin by address", hostByIP, []string{"http://" + hostByIP}, http.StatusOK},
		{"its own origin by name", hostByName, []string{"http://" + hostByName}, http.StatusOK},
		{"another site", hostByIP, []string{"http://evil.example"}, http.StatusForbidden},
		{"null", hostByIP, []string{"null"}, http.StatusForbidden},
		{"the other name of this server", hostByIP, []string{"http://" + hostByName}, http.StatusForbidden},
		{"another scheme", hostByIP, []string{"https://" + hostByIP}, http.StatusForbidden},
		{"an empty Origin", hostByIP, []string{""}, http.StatusForbidden},
		{"a good Origin beside a bad one", hostByIP, []string{"http://" + hostByIP, "http://evil.example"}, http.StatusForbidden},
	} {
		t.Run(tc.name, func(t *testing.T) {
			eachMutation(t, tc.host, "Origin", tc.origin, tc.status)
		})
	}
}

// A link on another site may open the page, and keeping that site from reading it is the
// browser's job. Only the Host of a GET, a HEAD and an OPTIONS is checked.
func TestGuardChecksOnlyTheHostOfGetHeadAndOptions(t *testing.T) {
	hdr := http.Header{"Origin": {"http://evil.example"}, "Sec-Fetch-Site": {"cross-site"}}
	for _, method := range []string{http.MethodGet, http.MethodHead, http.MethodOptions} {
		t.Run(method, func(t *testing.T) {
			rec, p := throughGuard(guardRequest(method, "/api/rules", hostByIP, hdr))
			wantAnswer(t, rec, p, http.StatusOK)
		})
	}
}

func TestGuardChecksSecFetchSite(t *testing.T) {
	for _, tc := range []struct {
		name   string
		site   []string // nil: no Sec-Fetch-Site header
		status int
	}{
		{"absent", nil, http.StatusOK},
		{"same-origin", []string{"same-origin"}, http.StatusOK},
		{"none", []string{"none"}, http.StatusOK},
		{"same-site", []string{"same-site"}, http.StatusForbidden},
		{"cross-site", []string{"cross-site"}, http.StatusForbidden},
		{"empty", []string{""}, http.StatusForbidden},
		{"a value nobody defined", []string{"other"}, http.StatusForbidden},
	} {
		t.Run(tc.name, func(t *testing.T) {
			eachMutation(t, hostByIP, "Sec-Fetch-Site", tc.site, tc.status)
		})
	}
}

func TestGuardRequiresJSONForPostAndPatch(t *testing.T) {
	for _, tc := range []struct {
		name        string
		contentType []string // nil: no Content-Type header
		status      int
	}{
		{"no Content-Type", nil, http.StatusUnsupportedMediaType},
		{"text/plain", []string{"text/plain"}, http.StatusUnsupportedMediaType},
		{"a form", []string{"application/x-www-form-urlencoded"}, http.StatusUnsupportedMediaType},
		{"a multipart form", []string{"multipart/form-data; boundary=x"}, http.StatusUnsupportedMediaType},
		{"JSON with a broken parameter", []string{"application/json; charset"}, http.StatusUnsupportedMediaType},
		{"JSON", []string{"application/json"}, http.StatusOK},
		{"JSON with a charset", []string{"application/json; charset=utf-8"}, http.StatusOK},
		{"JSON in capitals", []string{"Application/JSON"}, http.StatusOK},
	} {
		for _, method := range []string{http.MethodPost, http.MethodPatch} {
			t.Run(method+" "+tc.name, func(t *testing.T) {
				hdr := http.Header{}
				if tc.contentType != nil {
					hdr["Content-Type"] = tc.contentType
				}
				rec, p := throughGuard(guardRequest(method, "/api/rules", hostByIP, hdr))
				wantAnswer(t, rec, p, tc.status)
			})
		}
	}
	for _, method := range []string{http.MethodGet, http.MethodDelete} {
		t.Run(method+" needs none", func(t *testing.T) {
			rec, p := throughGuard(guardRequest(method, "/api/rules", hostByIP, nil))
			wantAnswer(t, rec, p, http.StatusOK)
		})
	}
}

// Every request below also has a wrong Content-Type. A request that is foreign gets the 403,
// not a 415 that would tell the sender what to fix.
func TestGuardChecksInOrder(t *testing.T) {
	for _, tc := range []struct {
		name string
		host string
		hdr  http.Header
	}{
		{"Host before Content-Type", "evil.example:" + strconv.Itoa(testPort), http.Header{"Content-Type": {"text/plain"}}},
		{"Origin before Content-Type", hostByIP, http.Header{"Content-Type": {"text/plain"}, "Origin": {"http://evil.example"}}},
		{"Sec-Fetch-Site before Content-Type", hostByIP, http.Header{"Content-Type": {"text/plain"}, "Sec-Fetch-Site": {"cross-site"}}},
	} {
		for _, method := range []string{http.MethodPost, http.MethodPatch} {
			t.Run(method+" "+tc.name, func(t *testing.T) {
				rec, p := throughGuard(guardRequest(method, "/api/rules", tc.host, tc.hdr))
				wantAnswer(t, rec, p, http.StatusForbidden)
			})
		}
	}
}

func TestGuardSetsHeadersOnEveryAnswer(t *testing.T) {
	preflight := http.Header{"Origin": {"http://evil.example"}, "Access-Control-Request-Method": {"POST"}}
	textPlain := http.Header{"Content-Type": {"text/plain"}}
	for _, route := range []struct{ name, target string }{{"api", "/api/rules"}, {"page", "/"}} {
		for _, tc := range []struct {
			name, method, host string
			hdr                http.Header
			status             int
		}{
			{"served", http.MethodGet, hostByIP, nil, http.StatusOK},
			{"preflight", http.MethodOptions, hostByIP, preflight, http.StatusOK},
			{"foreign Host", http.MethodGet, "evil.example:" + strconv.Itoa(testPort), nil, http.StatusForbidden},
			{"wrong Content-Type", http.MethodPost, hostByIP, textPlain, http.StatusUnsupportedMediaType},
		} {
			t.Run(route.name+" "+tc.name, func(t *testing.T) {
				rec, p := throughGuard(guardRequest(tc.method, route.target, tc.host, tc.hdr))
				wantAnswer(t, rec, p, tc.status)
				got := rec.Result().Header
				for key, want := range map[string]string{
					"X-Content-Type-Options":  "nosniff",
					"Referrer-Policy":         "no-referrer",
					"Content-Security-Policy": "default-src 'self'; frame-ancestors 'none'",
				} {
					if v := got.Get(key); v != want {
						t.Errorf("%s = %q, want %q", key, v, want)
					}
				}
				if noStore := got.Get("Cache-Control") == "no-store"; noStore != strings.HasPrefix(route.target, "/api/") {
					t.Errorf("Cache-Control = %q on %s", got.Get("Cache-Control"), route.target)
				}
				for key := range got {
					if strings.HasPrefix(key, "Access-Control-") {
						t.Errorf("unexpected header %s", key)
					}
				}
			})
		}
	}
}

func TestGuardCapsTheBody(t *testing.T) {
	const limit = 1 << 20
	for _, tc := range []struct {
		name   string
		size   int
		capped bool
	}{
		{"at the limit", limit, false},
		{"one byte over", limit + 1, true},
	} {
		for _, method := range []string{http.MethodPost, http.MethodPatch} {
			t.Run(method+" "+tc.name, func(t *testing.T) {
				r := httptest.NewRequest(method, "/api/rules", bytes.NewReader(make([]byte, tc.size)))
				r.Host = hostByIP
				r.Header.Set("Content-Type", "application/json")
				rec, p := throughGuard(r)
				wantAnswer(t, rec, p, http.StatusOK)
				_, tooBig := errors.AsType[*http.MaxBytesError](p.readErr)
				switch {
				case tc.capped && !tooBig:
					t.Errorf("read error = %v, want *http.MaxBytesError", p.readErr)
				case !tc.capped && p.readErr != nil:
					t.Errorf("read error = %v, want none", p.readErr)
				}
			})
		}
	}
}
