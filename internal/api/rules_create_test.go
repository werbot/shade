package api_test

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/werbot/shade/internal/rules"
)

func TestCreateRule(t *testing.T) {
	f := newFixture(t)
	e := f.engine(t)
	// The longest name there is: 64 characters, a capital first and then every kind of character the
	// rest of a name may hold. The other names here are short and lower case.
	longest := "Q" + strings.Repeat("Ab.9_-", 10) + "xyz"
	for _, tc := range []struct {
		name, body string
		want       ruleWire
	}{
		{"required fields only", `{"name":"acme","type":"TICKET","pattern":"ACME-\\d{6}"}`,
			ruleWire{Name: "acme", Type: "TICKET", Kind: "regex", Pattern: `ACME-\d{6}`, Enabled: true, Scope: "project"}},
		{"global scope", `{"name":"shared","type":"TICKET","pattern":"S-\\d+","scope":"global"}`,
			ruleWire{Name: "shared", Type: "TICKET", Kind: "regex", Pattern: `S-\d+`, Enabled: true, Scope: "global"}},
		{"every field", `{"name":"k.1_v-2","type":"SECRET","kind":"regex","pattern":"key=(\\w+)","secret_group":1,"scope":"project"}`,
			ruleWire{Name: "k.1_v-2", Type: "SECRET", Kind: "regex", Pattern: `key=(\w+)`, SecretGroup: 1, Enabled: true, Scope: "project"}},
		{"literal kind", `{"name":"lit","type":"HOST","kind":"literal","pattern":"db.prod"}`,
			ruleWire{Name: "lit", Type: "HOST", Kind: "literal", Pattern: "db.prod", Enabled: true, Scope: "project"}},
		{"longest name", fmt.Sprintf(`{"name":%q,"type":"TICKET","pattern":"L-\\d+"}`, longest),
			ruleWire{Name: longest, Type: "TICKET", Kind: "regex", Pattern: `L-\d+`, Enabled: true, Scope: "project"}},
		{"digit first", `{"name":"9lives","type":"SECRET","pattern":"x"}`,
			ruleWire{Name: "9lives", Type: "SECRET", Kind: "regex", Pattern: "x", Enabled: true, Scope: "project"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := f.do(http.MethodPost, "/api/rules", tc.body)
			if rec.Code != http.StatusCreated {
				t.Fatalf("status = %d, want 201: %s", rec.Code, rec.Body)
			}
			if typ := mediaType(t, rec); typ != "application/json" {
				t.Errorf("media type = %q, want application/json", typ)
			}
			if got := decodeJSON[ruleWire](t, rec); got != tc.want {
				t.Errorf("answer = %+v, want %+v", got, tc.want)
			}
			list := decodeJSON[rulesWire](t, f.do(http.MethodGet, "/api/rules", ""))
			if !slices.Contains(list.Rules, tc.want) {
				t.Errorf("GET /api/rules lacks %+v", tc.want)
			}
		})
	}

	// Order 0, as `shade rules add` sets it: ahead of the builtin rules, which start at 10, so the
	// rule of the user wins where they overlap. Only an enabled rule is in the set.
	set, err := e.Store().RulesForProject(t.Context(), e.ProjectID())
	if err != nil {
		t.Fatal(err)
	}
	if i := slices.IndexFunc(set, func(r rules.Rule) bool { return r.Name == "acme" }); i < 0 || set[i].OrderIdx != 0 {
		t.Errorf("acme in the rule set of the project: index %d, want a rule of order 0", i)
	}
}

func TestCreateRuleRejectsBadInput(t *testing.T) {
	f := newFixture(t)
	e := f.engine(t)
	before := len(stored(t, e))
	const named = `{"name":%q,"type":"SECRET","pattern":"x"}`
	for _, tc := range []struct{ name, body, text string }{
		{"empty name", fmt.Sprintf(named, ""), "name"},
		{"dot dot", fmt.Sprintf(named, ".."), "name"},
		{"space", fmt.Sprintf(named, "a b"), "name"},
		{"not ASCII", fmt.Sprintf(named, "имя"), "ASCII letters"},
		{"leading dash", fmt.Sprintf(named, "-x"), "starting with a letter or a digit"},
		{"65 characters", fmt.Sprintf(named, strings.Repeat("a", 65)), "1 to 64 characters"},
		{"unknown scope", `{"name":"ok","type":"SECRET","pattern":"x","scope":"galaxy"}`, "scope"},
		{"unknown field", `{"name":"ok","type":"SECRET","pattern":"x","colour":"red"}`, "colour"},
		{"second document", `{"name":"ok","type":"SECRET","pattern":"x"} {}`, ""},
		{"stray brace", `{"name":"ok","type":"SECRET","pattern":"x"}}`, ""},
		{"not json", `name=ok`, ""},
		{"empty type", `{"name":"ok","type":"","pattern":"x"}`, "type"},
		{"empty pattern", `{"name":"ok","type":"SECRET","pattern":""}`, "pattern"},
		{"unknown type", `{"name":"ok","type":"NOPE","pattern":"x"}`, "NOPE"},
		{"unknown kind", `{"name":"ok","type":"SECRET","kind":"magic","pattern":"x"}`, "magic"},
		{"broken regexp", `{"name":"ok","type":"SECRET","pattern":"("}`, "("},
		{"secret group beyond the pattern", `{"name":"ok","type":"SECRET","pattern":"x","secret_group":2}`, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := f.do(http.MethodPost, "/api/rules", tc.body)
			wantError(t, rec, http.StatusBadRequest)
			if msg := decodeJSON[map[string]string](t, rec)["error"]; !strings.Contains(msg, tc.text) {
				t.Errorf("error = %q, want %q in it", msg, tc.text)
			}
			if n := len(stored(t, e)); n != before {
				t.Errorf("the store holds %d rules, had %d: the refused request wrote one", n, before)
			}
		})
	}
}

func TestCreateRuleConflicts(t *testing.T) {
	f := newFixture(t)
	e := f.engine(t)
	builtin := enabledBuiltin(t, f)
	post := func(name, scope, pattern string) *httptest.ResponseRecorder {
		return f.do(http.MethodPost, "/api/rules",
			fmt.Sprintf(`{"name":%q,"type":"SECRET","pattern":%q,"scope":%q}`, name, pattern, scope))
	}
	created := func(rec *httptest.ResponseRecorder) {
		t.Helper()
		if rec.Code != http.StatusCreated {
			t.Fatalf("status = %d, want 201: %s", rec.Code, rec.Body)
		}
	}

	created(post("dup", "project", "first"))
	wantError(t, post("dup", "project", "second"), http.StatusConflict)
	if got := stored(t, e)["project/dup"].Pattern; got != "first" {
		t.Errorf("pattern = %q after a refused create, want the first one kept", got)
	}
	// The other scope has not got the name yet, and holds it against a second create all the same.
	created(post("dup", "global", "x"))
	wantError(t, post("dup", "global", "x"), http.StatusConflict)

	// A builtin name is taken in the global scope, and the rule keeps its definition. In the
	// project it is free: a rule there shadows the global one.
	before := stored(t, e)["global/"+builtin]
	wantError(t, post(builtin, "global", "x"), http.StatusConflict)
	if got := stored(t, e)["global/"+builtin]; got != before {
		t.Errorf("builtin rule %q is %+v after a refused create, was %+v", builtin, got, before)
	}
	created(post(builtin, "project", "x"))
}
