package api

import (
	"maps"
	"net/http"
	"slices"

	"github.com/werbot/shade/internal/placeholder"
	"github.com/werbot/shade/internal/store"
)

// ruleView is a rule on the wire: the definition the page shows, and the flags it switches.
// scope is "global" or "project", the words of the MCP rules_list.
type ruleView struct {
	Name        string `json:"name"`
	Type        string `json:"type"`
	Kind        string `json:"kind"`
	Pattern     string `json:"pattern"`
	SecretGroup int    `json:"secret_group"`
	Enabled     bool   `json:"enabled"`
	Builtin     bool   `json:"builtin"`
	Scope       string `json:"scope"`
}

// rulesView is the answer of GET /api/rules. types is what the form offers, project is the root
// of the project the "project" scope of a rule means.
type rulesView struct {
	Project string     `json:"project"`
	Types   []string   `json:"types"`
	Rules   []ruleView `json:"rules"`
}

// listRules answers with the rules of the project of this process, the disabled ones too: the
// global rules first, then the project's own, each group by name.
func (s *server) listRules(w http.ResponseWriter, r *http.Request) {
	e, ok := s.engine(w, r)
	if !ok {
		return
	}
	defer e.Close()

	// A nil project would list the rules of every project in the store, not just this one's.
	pid := e.ProjectID()
	rows, err := e.Store().ListRules(r.Context(), &pid)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, rulesView{
		Project: e.RootPath(),
		Types:   slices.Sorted(maps.Keys(placeholder.Types)),
		Rules:   toRuleViews(rows),
	})
}

// toRuleViews maps the store rows to their wire shape. scope is derived from ProjectID: a nil
// project id on a row is the global scope. The slice is never nil, so the page reads [] and
// not null.
func toRuleViews(rows []store.RuleRow) []ruleView {
	out := make([]ruleView, 0, len(rows))
	for _, row := range rows {
		scope := "global"
		if row.ProjectID != nil {
			scope = "project"
		}
		out = append(out, ruleView{
			Name: row.Name, Type: row.Type, Kind: row.Kind, Pattern: row.Pattern,
			SecretGroup: row.SecretGroup, Enabled: row.Enabled, Builtin: row.Builtin, Scope: scope,
		})
	}
	return out
}
