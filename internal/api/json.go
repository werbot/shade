package api

import (
	"encoding/json"
	"net/http"
)

// writeJSON answers with the status and v as JSON. The encoder keeps its escaping of <, > and &,
// which the CLI's writers switch off to keep placeholders readable: the page parses the body, so
// the escape costs it nothing.
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	// The status is out already, so a failed write has nowhere to be reported.
	_ = json.NewEncoder(w).Encode(v)
}

// writeError answers with the one error shape of the API, {"error": msg}.
func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}
