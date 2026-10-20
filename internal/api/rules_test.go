package api_test

import (
	"context"
	"maps"
	"net/http"
	"slices"
	"testing"

	"github.com/werbot/shade/internal/core"
	"github.com/werbot/shade/internal/placeholder"
	"github.com/werbot/shade/internal/rules"
)

// The wire shapes are declared here, not imported from internal/api: the field names are the
// contract the page reads, so a rename inside the package must fail these tests rather than
// silently change what the page receives.
type rulesWire struct {
	Project string     `json:"project"`
	Types   []string   `json:"types"`
	Rules   []ruleWire `json:"rules"`
}

type ruleWire struct {
	Name        string `json:"name"`
	Type        string `json:"type"`
	Kind        string `json:"kind"`
	Pattern     string `json:"pattern"`
	SecretGroup int    `json:"secret_group"`
	Enabled     bool   `json:"enabled"`
	Builtin     bool   `json:"builtin"`
	Scope       string `json:"scope"`
}

func TestListRules(t *testing.T) {
	f := newFixture(t)
	e := f.engine(t)
	pid := e.ProjectID()
	// The definition has no field at its zero value, so a column the answer leaves out shows.
	acme := rules.Spec{ID: "acme", Type: "TICKET", Kind: "regex", Pattern: `ACME-(\d{6})`, SecretGroup: 1, Enabled: true}
	if err := e.Store().AddRule(t.Context(), &pid, acme); err != nil {
		t.Fatal(err)
	}

	rec := f.do(http.MethodGet, "/api/rules", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body)
	}
	if typ := mediaType(t, rec); typ != "application/json" {
		t.Errorf("media type = %q, want application/json", typ)
	}
	list := decodeJSON[rulesWire](t, rec)

	if list.Project != e.RootPath() {
		t.Errorf("project = %q, want %q", list.Project, e.RootPath())
	}
	// The form offers these types, so they are the engine's set, in alphabetical order.
	if want := slices.Sorted(maps.Keys(placeholder.Types)); !slices.Equal(list.Types, want) {
		t.Errorf("types = %v, want %v", list.Types, want)
	}
	if !slices.Contains(list.Types, "SECRET") {
		t.Error("types lack SECRET, the type of a rule that names none")
	}

	want := ruleWire{Name: "acme", Type: "TICKET", Kind: "regex", Pattern: `ACME-(\d{6})`, SecretGroup: 1, Enabled: true, Scope: "project"}
	if !slices.Contains(list.Rules, want) {
		t.Errorf("rules lack %+v", want)
	}
	var seenProject, seenBuiltin, seenDisabled bool
	for _, r := range list.Rules {
		if r.Scope == "project" {
			seenProject = true
		} else if seenProject {
			t.Errorf("global rule %q is listed after a project rule", r.Name)
		}
		if r.Builtin {
			seenBuiltin = true
			seenDisabled = seenDisabled || !r.Enabled
			if r.Scope != "global" {
				t.Errorf("builtin rule %q has scope %q, want global", r.Name, r.Scope)
			}
		}
	}
	if !seenBuiltin {
		t.Error("no builtin rule is listed")
	}
	if !seenDisabled {
		t.Error("no disabled builtin rule is listed: the list must carry the disabled ones")
	}
}

// ListRules(nil) returns the rows of every project in the store, and the page of one project must
// not show the rules of another.
func TestListRulesSkipsOtherProjects(t *testing.T) {
	f := newFixture(t)
	e := f.engine(t)
	other, err := e.Store().ProjectForPath(t.Context(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	elsewhere := rules.Spec{ID: "elsewhere", Type: "TICKET", Kind: "regex", Pattern: `EL-\d+`, Enabled: true}
	if err := e.Store().AddRule(t.Context(), &other.ID, elsewhere); err != nil {
		t.Fatal(err)
	}

	list := decodeJSON[rulesWire](t, f.do(http.MethodGet, "/api/rules", ""))
	if slices.ContainsFunc(list.Rules, func(r ruleWire) bool { return r.Name == elsewhere.ID }) {
		t.Errorf("the rule %q of another project is listed", elsewhere.ID)
	}
}

func TestReadFailureIs500(t *testing.T) {
	f := newFixture(t)
	open := f.open
	// An engine that was closed is handed over as a good one and fails on its first read.
	f.open = func(ctx context.Context) (*core.Engine, error) {
		e, err := open(ctx)
		if err != nil {
			return nil, err
		}
		if err := e.Close(); err != nil {
			t.Error(err)
		}
		return e, nil
	}
	wantError(t, f.do(http.MethodGet, "/api/rules", ""), http.StatusInternalServerError)
}
