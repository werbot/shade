package api_test

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/werbot/shade/internal/core"
	"github.com/werbot/shade/internal/rules"
	"github.com/werbot/shade/internal/store"
)

// stored is what the store holds for the project of e, keyed "scope/name" like the rows of the
// page: a global and a project rule may share a name. It is read through an engine of the test's
// own, so it shows what another process, the CLI, would see of the handler's writes.
func stored(t *testing.T, e *core.Engine) map[string]store.RuleRow {
	t.Helper()
	pid := e.ProjectID()
	rows, err := e.Store().ListRules(t.Context(), &pid)
	if err != nil {
		t.Fatal(err)
	}
	byKey := make(map[string]store.RuleRow, len(rows))
	for _, row := range rows {
		scope := "project"
		if row.ProjectID == nil {
			scope = "global"
		}
		byKey[scope+"/"+row.Name] = row
	}
	return byKey
}

// seed puts rules in the store the way the CLI does, to be addressed through the API.
func seed(t *testing.T, e *core.Engine, global bool, names ...string) {
	t.Helper()
	var scope *int64
	if !global {
		pid := e.ProjectID()
		scope = &pid
	}
	for _, name := range names {
		spec := rules.Spec{ID: name, Type: "SECRET", Kind: "regex", Pattern: "x", Enabled: true}
		if err := e.Store().AddRule(t.Context(), scope, spec); err != nil {
			t.Fatal(err)
		}
	}
}

// enabledBuiltin is the name of the first builtin rule the list shows as enabled, to switch off
// and on again. Picking it from the list keeps the tests off the names of the seed data.
func enabledBuiltin(t *testing.T, f *fixture) string {
	t.Helper()
	list := decodeJSON[rulesWire](t, f.do(http.MethodGet, "/api/rules", ""))
	i := slices.IndexFunc(list.Rules, func(r ruleWire) bool { return r.Builtin && r.Enabled })
	if i < 0 {
		t.Fatal("the list has no enabled builtin rule")
	}
	return list.Rules[i].Name
}

// wantNoContent checks the answer of a write that went through: a 204 and nothing else.
func wantNoContent(t *testing.T, rec *httptest.ResponseRecorder) {
	t.Helper()
	if rec.Code != http.StatusNoContent || rec.Body.Len() != 0 {
		t.Fatalf("status = %d with body %q, want 204 and no body", rec.Code, rec.Body)
	}
}

// A request that is refused for its shape is answered before the engine is opened: the scope, the
// body and the required fields are checked first and the store last, so it costs no engine.
func TestRefusedRequestsDoNotOpenTheEngine(t *testing.T) {
	f := newFixture(t)
	for _, tc := range []struct{ name, method, target, body string }{
		{"create: bad name", http.MethodPost, "/api/rules", `{"name":"a b","type":"SECRET","pattern":"x"}`},
		{"create: no type", http.MethodPost, "/api/rules", `{"name":"ok","pattern":"x"}`},
		{"create: no pattern", http.MethodPost, "/api/rules", `{"name":"ok","type":"SECRET"}`},
		{"create: unknown scope", http.MethodPost, "/api/rules", `{"name":"ok","type":"SECRET","pattern":"x","scope":"galaxy"}`},
		{"toggle: no scope", http.MethodPatch, "/api/rules/x", `{"enabled":true}`},
		{"toggle: no enabled", http.MethodPatch, "/api/rules/x?scope=project", `{}`},
		{"delete: no scope", http.MethodDelete, "/api/rules/x", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			refuseEngine(t, f)
			wantError(t, f.do(tc.method, tc.target, tc.body), http.StatusBadRequest)
		})
	}
}

func TestRequestBodyLimit(t *testing.T) {
	f := newFixture(t)
	e := f.engine(t)
	before := len(stored(t, e))
	pad := strings.Repeat("a", 1<<20)
	for _, tc := range []struct{ name, method, target, body string }{
		{"create", http.MethodPost, "/api/rules", `{"name":"x","type":"SECRET","pattern":"` + pad},
		{"toggle", http.MethodPatch, "/api/rules/x?scope=project", `{"enabled":true,"pad":"` + pad},
		// The document is whole here, and it is the blanks after it that cross the cap.
		{"blanks after the document", http.MethodPatch, "/api/rules/x?scope=project", `{"enabled":true}` + strings.Repeat(" ", 1<<20)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			wantError(t, f.do(tc.method, tc.target, tc.body), http.StatusRequestEntityTooLarge)
		})
	}
	if n := len(stored(t, e)); n != before {
		t.Errorf("the store holds %d rules, had %d: a refused request wrote one", n, before)
	}
}

func TestToggleRule(t *testing.T) {
	f := newFixture(t)
	e := f.engine(t)
	name := enabledBuiltin(t, f)
	key := "global/" + name
	patch := func(target, body string) *httptest.ResponseRecorder {
		return f.do(http.MethodPatch, "/api/rules/"+target, body)
	}

	for _, enabled := range []bool{false, true} {
		wantNoContent(t, patch(name+"?scope=global", fmt.Sprintf(`{"enabled":%t}`, enabled)))
		if got := stored(t, e)[key].Enabled; got != enabled {
			t.Errorf("enabled = %t after setting it to %t", got, enabled)
		}
	}

	// A rule is found in the scope that was asked for and nowhere else.
	wantError(t, patch("nope?scope=global", `{"enabled":false}`), http.StatusNotFound)
	wantError(t, patch(name+"?scope=project", `{"enabled":false}`), http.StatusNotFound)

	for _, tc := range []struct{ name, target, body string }{
		{"no scope", name, `{"enabled":false}`},
		{"unknown scope", name + "?scope=x", `{"enabled":false}`},
		{"no enabled", name + "?scope=global", `{}`},
		{"unknown field", name + "?scope=global", `{"enabled":false,"name":"x"}`},
		{"second document", name + "?scope=global", `{"enabled":false} {}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			wantError(t, patch(tc.target, tc.body), http.StatusBadRequest)
			// The rule is on, and each of these bodies would switch it off if it were taken.
			if !stored(t, e)[key].Enabled {
				t.Error("the refused request switched the rule off")
			}
		})
	}
}

// Review Focus 4: the name travels in the path, so one with a slash, a space or a percent sign
// must reach the store as it was, and a project rule must not be mistaken for the global rule it
// shadows, nor the other way round.
func TestToggleAndDeleteAddressTheRightRow(t *testing.T) {
	f := newFixture(t)
	e := f.engine(t)
	seed(t, e, true, "dup")
	seed(t, e, false, "dup", "a/b c", "100%")

	wantNoContent(t, f.do(http.MethodPatch, "/api/rules/dup?scope=project", `{"enabled":false}`))
	if rows := stored(t, e); rows["project/dup"].Enabled || !rows["global/dup"].Enabled {
		t.Errorf("project dup enabled = %t, global dup enabled = %t, want false and true",
			rows["project/dup"].Enabled, rows["global/dup"].Enabled)
	}

	for _, tc := range []struct{ name, path string }{{"a/b c", "a%2Fb%20c"}, {"100%", "100%25"}} {
		t.Run(tc.path, func(t *testing.T) {
			target := "/api/rules/" + tc.path + "?scope=project"
			wantNoContent(t, f.do(http.MethodPatch, target, `{"enabled":false}`))
			if stored(t, e)["project/"+tc.name].Enabled {
				t.Error("the rule is still enabled after the PATCH")
			}
			wantNoContent(t, f.do(http.MethodDelete, target, ""))
			if _, ok := stored(t, e)["project/"+tc.name]; ok {
				t.Error("the rule is still stored after the DELETE")
			}
		})
	}

	wantNoContent(t, f.do(http.MethodDelete, "/api/rules/dup?scope=project", ""))
	rows := stored(t, e)
	if _, ok := rows["project/dup"]; ok {
		t.Error("the project rule is still stored after the DELETE")
	}
	if global, ok := rows["global/dup"]; !ok || !global.Enabled {
		t.Errorf("the global rule is %+v after the project rule of its name was deleted, want it kept and enabled", global)
	}
	wantNoContent(t, f.do(http.MethodDelete, "/api/rules/dup?scope=global", ""))
	if _, ok := stored(t, e)["global/dup"]; ok {
		t.Error("the global rule is still stored after the DELETE")
	}
}

func TestDeleteRule(t *testing.T) {
	f := newFixture(t)
	e := f.engine(t)
	seed(t, e, false, "mine")
	builtin := enabledBuiltin(t, f)
	del := func(target string) *httptest.ResponseRecorder {
		return f.do(http.MethodDelete, "/api/rules/"+target, "")
	}

	// The scope is required: a request that does not name one must not get one picked for it.
	for _, target := range []string{"mine", "mine?scope=x"} {
		wantError(t, del(target), http.StatusBadRequest)
	}
	wantError(t, del("nope?scope=project"), http.StatusNotFound)
	wantError(t, del(builtin+"?scope=project"), http.StatusNotFound)
	// A builtin rule can be switched off, but not deleted.
	wantError(t, del(builtin+"?scope=global"), http.StatusForbidden)
	rows := stored(t, e)
	if _, ok := rows["global/"+builtin]; !ok {
		t.Errorf("the builtin rule %q is gone", builtin)
	}
	if _, ok := rows["project/mine"]; !ok {
		t.Error("a refused request deleted the rule")
	}

	wantNoContent(t, del("mine?scope=project"))
	if _, ok := stored(t, e)["project/mine"]; ok {
		t.Error("the rule is still stored after the DELETE")
	}
}
