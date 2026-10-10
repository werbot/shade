package proxy

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/werbot/shade/internal/config"
	"github.com/werbot/shade/internal/placeholder"
)

// sanitizeError anonymizes an upstream error body instead of cutting it: a client matches
// error wording to decide whether to retry, and a cut body would cost that. The body is
// handed to the same Anonymize the request path uses, as one string — whether it is JSON or
// HTML makes no difference to the rules. When anonymizing fails the body is replaced with a
// bare status line, so the original text never reaches the client either way.
func (s *Server) sanitizeError(ctx context.Context, e Engine, status int, body []byte) []byte {
	res, err := e.Anonymize(ctx, string(body))
	if err != nil {
		// The error itself is not shown: it can name a fragment of the body it refused.
		return statusLine(status)
	}
	s.reportRecordErr(res.RecordErr)
	return []byte(res.Text)
}

// statusLine is the body sanitizeError falls back to: the bare status, no upstream text.
func statusLine(status int) []byte {
	return []byte(strconv.Itoa(status) + " " + http.StatusText(status))
}

// refuseUnresolved is the single place the fail policy is read, so the non-streaming
// answer, the buffered stream and the OpenAI path cannot drift. A fail_closed refusal with
// a non-empty list journals one blocked row per distinct type and returns the body of the
// refusing answer; fail_open_log journals nothing, names the types on diag and lets the
// answer through. An empty list is never a refusal, whatever the policy.
func (s *Server) refuseUnresolved(ctx context.Context, e Engine, policy string, unresolved []placeholder.Token) (bool, []byte) {
	if len(unresolved) == 0 {
		return false, nil
	}
	types := unresolvedTypes(unresolved)
	message := fmt.Sprintf("%d placeholders could not be restored: %s", len(unresolved), strings.Join(types, ", "))
	if policy == config.FailOpenLog {
		fmt.Fprintf(s.opts.Diag, "proxy: fail_open_log: %s\n", message)
		return false, nil
	}
	for _, typ := range types {
		if err := e.RecordBlocked(ctx, typ); err != nil {
			s.reportRecordErr(err)
		}
	}
	return true, errorBody("shade_unresolved", message)
}

// unresolvedTypes names the distinct types in the order they first appear. Types, never a
// token and never a value: the refusal is read by a client and the diag line by a human, and
// neither may carry client text (spec §11).
func unresolvedTypes(unresolved []placeholder.Token) []string {
	seen := make(map[string]bool, len(unresolved))
	types := make([]string, 0, len(unresolved))
	for _, tok := range unresolved {
		if seen[tok.Type] {
			continue
		}
		seen[tok.Type] = true
		types = append(types, tok.Type)
	}
	return types
}

// reportRecordErr shows a failed auxiliary write on the diagnostic writer. The write is not
// fatal — the answer is ready and goes out regardless — but a lost journal row must be
// visible somewhere, and it must never reach the client.
func (s *Server) reportRecordErr(err error) {
	if err == nil {
		return
	}
	fmt.Fprintf(s.opts.Diag, "proxy: stats not recorded: %v\n", err)
}

// apiError is the refusal body. The shape follows the Anthropic error object so a client
// reads it the same way, but the type is shade's own: the request never reached the
// provider.
type apiError struct {
	Type  string       `json:"type"`
	Error apiErrorBody `json:"error"`
}

// apiErrorBody is the "error" member of an apiError.
type apiErrorBody struct {
	Type    string `json:"type"`
	Message string `json:"message"`
}

// errorBody renders an apiError. Marshalling a struct of strings cannot fail; on the
// impossible error the body is empty and the caller still writes the status.
func errorBody(errType, message string) []byte {
	body, err := marshalNoEscape(apiError{
		Type:  "error",
		Error: apiErrorBody{Type: errType, Message: message},
	})
	if err != nil {
		return nil
	}
	return body
}

// writeErrorBody frames a refusal: the 502, the JSON content type and the body. It is the
// one place an error response is written, so the request-side refusals and the fail_closed
// refusal of an answer cannot drift apart.
func writeErrorBody(w http.ResponseWriter, body []byte) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Del("Content-Length")
	w.WriteHeader(http.StatusBadGateway)
	_, _ = w.Write(body)
}

// writeShadeError refuses the request with a fixed message. It names no client text —
// never a value, never a parse offset — because a refusal that quotes the body it refused
// is a leak (spec §11).
func writeShadeError(w http.ResponseWriter, message string) {
	writeErrorBody(w, errorBody("shade_error", message))
}
