package api_test

import (
	"encoding/json"
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

// throughGuard runs r through a Guard on testPort that wraps a fresh probe.
func throughGuard(r *http.Request) (*httptest.ResponseRecorder, *probe) {
	return throughGuardOn(testPort, r)
}

// throughGuardOn is throughGuard for a Guard on another port.
func throughGuardOn(port int, r *http.Request) (*httptest.ResponseRecorder, *probe) {
	p := &probe{}
	rec := httptest.NewRecorder()
	api.Guard(port, p).ServeHTTP(rec, r)
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
