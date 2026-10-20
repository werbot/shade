package api

import (
	"cmp"
	"errors"
	"fmt"
	"net/http"
	"regexp"

	"github.com/werbot/shade/internal/core"
	"github.com/werbot/shade/internal/rules"
	"github.com/werbot/shade/internal/store"
)

// ruleName is what the name of a created rule must look like (design §4). The name goes into the
// path /api/rules/{name}, and the mux redirects a raw "." or "..", so such a name could not be
// addressed again.
var ruleName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)

// createRequest is the body of POST /api/rules. The definition is what `shade rules add` takes;
// kind and scope may be left out, and mean "regex" and "project".
type createRequest struct {
	Name        string `json:"name"`
	Type        string `json:"type"`
	Kind        string `json:"kind"`
	Pattern     string `json:"pattern"`
	SecretGroup int    `json:"secret_group"`
	Scope       string `json:"scope"`
}

// validate checks the name and the two fields every rule needs. rules.Compile, which the store runs
// before it writes, does the rest, but it takes an empty pattern (a regexp that matches everywhere)
// and calls an empty type "unknown", which tells the author less than "required".
func (req createRequest) validate() error {
	switch {
	case !ruleName.MatchString(req.Name):
		return fmt.Errorf("name must match %s", ruleName)
	case req.Type == "":
		return errors.New("type is required")
	case req.Pattern == "":
		return errors.New("pattern is required")
	}
	return nil
}

// createRule stores a new rule and answers with it. A request refused for its shape never opens
// the store.
func (s *server) createRule(w http.ResponseWriter, r *http.Request) {
	var req createRequest
	if err := decode(r, &req); err != nil {
		bodyError(w, err)
		return
	}
	if err := req.validate(); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	scope := cmp.Or(req.Scope, "project")
	if !scopeOK(w, scope) {
		return
	}
	e, ok := s.engine(w, r)
	if !ok {
		return
	}
	defer e.Close()

	// Order 0, as in `shade rules add`: builtin rules start at 10, so the rule of the user applies
	// before them and its type wins where they overlap.
	spec := rules.Spec{
		ID: req.Name, Type: req.Type, Kind: cmp.Or(req.Kind, "regex"), Pattern: req.Pattern,
		SecretGroup: req.SecretGroup, Enabled: true,
	}
	if err := e.Store().CreateRule(r.Context(), scopeID(e, scope), spec); err != nil {
		storeError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, ruleView{
		Name: spec.ID, Type: spec.Type, Kind: spec.Kind, Pattern: spec.Pattern,
		SecretGroup: spec.SecretGroup, Enabled: spec.Enabled, Scope: scope,
	})
}

// toggleRule switches a rule on or off. The scope is required, with no default: the name may be
// held by a global rule and by the project rule that shadows it, and the request must say which.
func (s *server) toggleRule(w http.ResponseWriter, r *http.Request) {
	scope := r.URL.Query().Get("scope")
	if !scopeOK(w, scope) {
		return
	}
	var body struct {
		Enabled *bool `json:"enabled"` // a pointer, to tell a missing field from false
	}
	if err := decode(r, &body); err != nil {
		bodyError(w, err)
		return
	}
	if body.Enabled == nil {
		writeError(w, http.StatusBadRequest, "enabled is required")
		return
	}
	e, ok := s.engine(w, r)
	if !ok {
		return
	}
	defer e.Close()

	if err := e.Store().SetRuleEnabled(r.Context(), scopeID(e, scope), r.PathValue("name"), *body.Enabled); err != nil {
		storeError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// deleteRule removes a rule of the scope the request names. A builtin rule is refused: it can be
// switched off, but not deleted.
func (s *server) deleteRule(w http.ResponseWriter, r *http.Request) {
	scope := r.URL.Query().Get("scope")
	if !scopeOK(w, scope) {
		return
	}
	e, ok := s.engine(w, r)
	if !ok {
		return
	}
	defer e.Close()

	if err := e.Store().RemoveRule(r.Context(), scopeID(e, scope), r.PathValue("name")); err != nil {
		storeError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// scopeOK checks the scope word of a request. It answers the 400 itself when the word is neither
// "project" nor "global", and the handler only returns.
func scopeOK(w http.ResponseWriter, scope string) bool {
	if scope != "project" && scope != "global" {
		writeError(w, http.StatusBadRequest, `scope must be "project" or "global"`)
		return false
	}
	return true
}

// scopeID turns a scope word that scopeOK passed into the scope the store takes: nil is the
// global one, otherwise the project of the engine. It is `scope` of cmd/shade/cmd_rules.go, which
// a package main cannot export.
func scopeID(e *core.Engine, scope string) *int64 {
	if scope == "global" {
		return nil
	}
	id := e.ProjectID()
	return &id
}

// storeError answers for a refusal of the store with the status its kind stands for and the text
// the store wrote for the user. What it does not know is the server's fault.
func storeError(w http.ResponseWriter, err error) {
	status := http.StatusInternalServerError
	switch {
	case errors.Is(err, store.ErrInvalidRule):
		status = http.StatusBadRequest
	case errors.Is(err, store.ErrRuleExists):
		status = http.StatusConflict
	case errors.Is(err, store.ErrRuleNotFound):
		status = http.StatusNotFound
	case errors.Is(err, store.ErrBuiltinRule):
		status = http.StatusForbidden
	}
	writeError(w, status, err.Error())
}
