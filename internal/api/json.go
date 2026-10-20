package api

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
)

// decode reads the body as exactly one JSON document into dst. A field dst does not have is an
// error, so a misspelled name is not taken for an absent one, and so is any data after the
// document, so a second one cannot ride along unread.
func decode(r *http.Request, dst any) error {
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		return err
	}
	// The body must end here. dec.More would miss a stray closing bracket and swallow a read
	// error; the next token is either the end or something that is refused. A read error is
	// handed on as it is, because it may be the size cap, which bodyError tells from bad JSON.
	switch _, err := dec.Token(); {
	case err == nil:
		return errors.New("unexpected data after the JSON document")
	case !errors.Is(err, io.EOF):
		return err
	}
	return nil
}

// bodyError answers for a body that could not be read as a document: 413 when the size cap of
// Guard cut it off, 400 for anything else, with the reason in the text.
func bodyError(w http.ResponseWriter, err error) {
	status := http.StatusBadRequest
	if errors.As(err, new(*http.MaxBytesError)) {
		status = http.StatusRequestEntityTooLarge
	}
	writeError(w, status, err.Error())
}

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
