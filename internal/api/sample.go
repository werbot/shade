package api

import (
	"net/http"

	"github.com/werbot/shade/internal/rules"
)

// candidate is the rule the form is typing: a definition with no name and no scope, since nothing
// is stored. An empty type or kind is filled in by rules.TestSample.
type candidate struct {
	Type        string `json:"type"`
	Kind        string `json:"kind"`
	Pattern     string `json:"pattern"`
	SecretGroup int    `json:"secret_group"`
}

// testRequest is the body of POST /api/test: the text, and the rule to try on it. A request with
// no rule tries the active rules of the project.
type testRequest struct {
	Text string     `json:"text"`
	Rule *candidate `json:"rule"` // a pointer, to tell a missing rule from an empty one
}

// testResponse is the answer: the text the spans index, and the spans.
type testResponse struct {
	Text  string     `json:"text"`
	Spans []spanView `json:"spans"`
}

// testText finds in the text of the request what a candidate rule, or else the active rules, would
// catch. Nothing is stored and nothing is logged: the text is the user's own and goes back only to
// them.
func (s *server) testText(w http.ResponseWriter, r *http.Request) {
	var req testRequest
	if err := decode(r, &req); err != nil {
		bodyError(w, err)
		return
	}

	// A candidate is tried on the raw text and nothing else, as `shade rules test` does: no Guard
	// and no engine, since there is no rule set to read.
	if c := req.Rule; c != nil {
		spec := rules.Spec{Type: c.Type, Kind: c.Kind, Pattern: c.Pattern, SecretGroup: c.SecretGroup}
		spans, err := rules.TestSample(spec, req.Text)
		if err != nil {
			// A pattern is broken half of the time while it is typed, so this is an answer the page
			// shows and not a fault.
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, testResponse{Text: req.Text, Spans: utf16Spans(req.Text, spans)})
		return
	}

	e, ok := s.engine(w, r)
	if !ok {
		return
	}
	defer e.Close()

	// Scan, not Anonymize: no placeholder is issued and no value is stored. The spans index the text
	// after Guard, so that is the text which goes back.
	text, spans, err := e.Scan(req.Text, "")
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, testResponse{Text: text, Spans: utf16Spans(text, spans)})
}
