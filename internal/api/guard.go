package api

import (
	"mime"
	"net/http"
	"slices"
	"strconv"
	"strings"
)

// maxBody caps a request body. A rule is a few hundred bytes, so a megabyte is generous; the
// reader of a larger body fails with *http.MaxBytesError, which the endpoints answer with a 413.
const maxBody = 1 << 20

// Guard is the loopback protection of the UI server (design §6). port is the port the
// listener actually bound; the handler it wraps never sees a refused request.
//
// The checks run in a fixed order and the first one that fails answers for the request: Host,
// then the Origin and Sec-Fetch-Site of a mutation, then the Content-Type of a POST and a PATCH.
// The security headers are set before any of them, so a 403 and a 415 carry them too. A request
// that passes everything reaches next with its body capped.
func Guard(port int, next http.Handler) http.Handler {
	suffix := ":" + strconv.Itoa(port)
	hosts := []string{"127.0.0.1" + suffix, "localhost" + suffix}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		setSecurityHeaders(w, r)
		// A page of another site reaches this port only under its own name (DNS rebinding), so
		// the Host must be exactly one of the two this server answers to.
		if !slices.Contains(hosts, r.Host) {
			writeError(w, http.StatusForbidden, "host not allowed")
			return
		}
		if isMutation(r.Method) && !sameOrigin(r) {
			writeError(w, http.StatusForbidden, "cross-origin request refused")
			return
		}
		// POST and PATCH are the methods whose body the API reads. A browser sends a form or a
		// text/plain body to any site without asking first, so JSON is what keeps a foreign page
		// out even when it gets past the checks above.
		if (r.Method == http.MethodPost || r.Method == http.MethodPatch) && !isJSON(r) {
			writeError(w, http.StatusUnsupportedMediaType, "Content-Type must be application/json")
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, maxBody)
		next.ServeHTTP(w, r)
	})
}

// setSecurityHeaders sets what every answer carries, a refusal included. No Access-Control-*
// header is set anywhere: a page of another site cannot read an answer, and a preflight finds
// nothing that allows its request. Only /api/ answers hold rule definitions, so only they must
// not be cached.
func setSecurityHeaders(w http.ResponseWriter, r *http.Request) {
	h := w.Header()
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Referrer-Policy", "no-referrer")
	h.Set("Content-Security-Policy", "default-src 'self'; frame-ancestors 'none'")
	if strings.HasPrefix(r.URL.Path, "/api/") {
		h.Set("Cache-Control", "no-store")
	}
}

// isMutation tells which methods get the Origin and Sec-Fetch-Site checks: every method but GET,
// HEAD and OPTIONS, which change nothing. Listing the mutating methods instead (POST, PATCH,
// DELETE) would let a PUT, or a verb the API does not know, slip past the checks.
func isMutation(method string) bool {
	return !slices.Contains([]string{http.MethodGet, http.MethodHead, http.MethodOptions}, method)
}

// sameOrigin tells a request the page made itself from one made by another site. Origin must be
// the page's own http://<Host>, which also turns away the "null" that a sandboxed frame sends;
// Sec-Fetch-Site must be same-origin, or none for an address typed by the user. A header that is
// absent passes, since not every client sends them, but a header that is present must match in
// every value, an empty one included.
func sameOrigin(r *http.Request) bool {
	return absentOr(r.Header, "Origin", "http://"+r.Host) &&
		absentOr(r.Header, "Sec-Fetch-Site", "same-origin", "none")
}

// absentOr reports whether the header is missing, or carries nothing but values from allowed.
func absentOr(h http.Header, key string, allowed ...string) bool {
	for _, v := range h.Values(key) {
		if !slices.Contains(allowed, v) {
			return false
		}
	}
	return true
}

// isJSON reports whether the request declares a JSON body. A header that does not parse is not
// JSON, and the error is read first because ParseMediaType returns a type next to some errors.
func isJSON(r *http.Request) bool {
	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	return err == nil && mediaType == "application/json"
}
