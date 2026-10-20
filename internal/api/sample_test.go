package api_test

import (
	"encoding/json"
	"net/http"
	"slices"
	"strings"
	"testing"
	"unicode/utf16"

	"github.com/werbot/shade/internal/rules"
)

// secret is a value the builtin rules catch in an assignment. It is the constant of the same name
// in internal/core/engine_test.go: a package cannot import the test files of another.
const secret = "AbCdEf0123456789"

// The answer of POST /api/test as the page reads it. The shapes are declared here, not imported,
// for the reason given at rulesWire.
type testWire struct {
	Text  string     `json:"text"`
	Spans []spanWire `json:"spans"`
}

type spanWire struct {
	Start int    `json:"start"`
	End   int    `json:"end"`
	Type  string `json:"type"`
	Rule  string `json:"rule"`
}

// ticket is the candidate rule of the tests, as the form sends it.
var ticket = map[string]any{"type": "TICKET", "kind": "regex", "pattern": `ACME-\d{6}`}

// fragment is what the page shows for a span: the text cut by UTF-16 code units, as String.slice
// does it. A span outside the text fails the test where it is read, not with a panic.
func fragment(t *testing.T, text string, s spanWire) string {
	t.Helper()
	units := utf16.Encode([]rune(text))
	if s.Start < 0 || s.Start > s.End || s.End > len(units) {
		t.Fatalf("span %+v is outside the text of %d UTF-16 units", s, len(units))
	}
	return string(utf16.Decode(units[s.Start:s.End]))
}

// testBody is the body the page sends: the text, and the candidate rule when the form has one.
func testBody(t *testing.T, text string, rule map[string]any) string {
	t.Helper()
	req := map[string]any{"text": text}
	if rule != nil {
		req["rule"] = rule
	}
	body, err := json.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}
	return string(body)
}

// postTest sends a text to POST /api/test and reads the 200 it must come back with.
func postTest(t *testing.T, f *fixture, text string, rule map[string]any) testWire {
	t.Helper()
	rec := f.do(http.MethodPost, "/api/test", testBody(t, text, rule))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body)
	}
	if typ := mediaType(t, rec); typ != "application/json" {
		t.Errorf("media type = %q, want application/json", typ)
	}
	return decodeJSON[testWire](t, rec)
}

// The page cuts the text with String.slice, which counts UTF-16 code units. A Cyrillic letter is
// one unit and two bytes, an emoji is two units and four bytes, so the byte offsets of the matcher
// would cut the wrong piece, and the count of runes would miss by one per emoji.
func TestCandidateOffsetsAreUTF16(t *testing.T) {
	f := newFixture(t)
	refuseEngine(t, f)
	for _, tc := range []struct {
		name, text string
		spans      []spanWire
		pieces     []string
	}{
		{"cyrillic before", "привет ACME-123456 мир",
			[]spanWire{{7, 18, "TICKET", "adhoc"}}, []string{"ACME-123456"}},
		{"emoji before", "😀 ACME-123456",
			[]spanWire{{3, 14, "TICKET", "adhoc"}}, []string{"ACME-123456"}},
		// The second span is counted past the first span and past both kinds of wide text.
		{"two spans after wide text", "😀 ACME-123456 привет ACME-654321",
			[]spanWire{{3, 14, "TICKET", "adhoc"}, {22, 33, "TICKET", "adhoc"}},
			[]string{"ACME-123456", "ACME-654321"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := postTest(t, f, tc.text, ticket)
			if !slices.Equal(got.Spans, tc.spans) {
				t.Fatalf("spans = %+v, want %+v", got.Spans, tc.spans)
			}
			for i, s := range got.Spans {
				if piece := fragment(t, got.Text, s); piece != tc.pieces[i] {
					t.Errorf("span %d cuts %q, want %q", i, piece, tc.pieces[i])
				}
			}
		})
	}
}

// A candidate is tried on the text as it was typed. Guard belongs to the active rules: here a
// placeholder of the text stays one, a NUL stays a single NUL, and the offsets index the text the
// page sent.
func TestCandidateReadsTheRawText(t *testing.T) {
	f := newFixture(t)
	refuseEngine(t, f)
	const text = "<EMAIL_1> \x00 ACME-123456"
	got := postTest(t, f, text, ticket)
	if got.Text != text {
		t.Errorf("text = %q, want the text of the request, %q", got.Text, text)
	}
	if want := []spanWire{{12, 23, "TICKET", "adhoc"}}; !slices.Equal(got.Spans, want) {
		t.Fatalf("spans = %+v, want %+v", got.Spans, want)
	}
	if piece := fragment(t, got.Text, got.Spans[0]); piece != "ACME-123456" {
		t.Errorf("span cuts %q, want ACME-123456", piece)
	}
}

// The form sends four fields and each one changes what is found, so a mapping that dropped one
// would test a rule other than the one the user is about to save.
func TestCandidateTakesTheWholeDefinition(t *testing.T) {
	f := newFixture(t)
	refuseEngine(t, f)
	for _, tc := range []struct {
		name, text string
		rule       map[string]any
		want       spanWire
	}{
		{"kind", "abc a.c", map[string]any{"kind": "literal", "pattern": "a.c"}, spanWire{4, 7, "SECRET", "adhoc"}},
		{"secret group", "key=abc", map[string]any{"pattern": `key=(\w+)`, "secret_group": 1}, spanWire{4, 7, "SECRET", "adhoc"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := postTest(t, f, tc.text, tc.rule)
			if want := []spanWire{tc.want}; !slices.Equal(got.Spans, want) {
				t.Errorf("spans = %+v, want %+v", got.Spans, want)
			}
		})
	}
}

// While a pattern is typed it is broken half of the time, so this is a state of the page and not
// a fault: the page shows the text under the field, and it is the text of rules.TestSample. A
// missing pattern is refused the way POST /api/rules refuses it: an empty one compiles and finds
// nothing, and "nothing found" would read as an answer about a rule that cannot be saved.
func TestCandidateErrorIsABadRequest(t *testing.T) {
	f := newFixture(t)
	refuseEngine(t, f)
	_, broken := rules.TestSample(rules.Spec{Pattern: "("}, "x")
	for _, tc := range []struct {
		name string
		rule map[string]any
		want string
	}{
		{"broken pattern", map[string]any{"pattern": "("}, broken.Error()},
		{"no pattern", map[string]any{}, "pattern is required"},
		{"empty pattern", map[string]any{"pattern": ""}, "pattern is required"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := f.do(http.MethodPost, "/api/test", testBody(t, "x", tc.rule))
			wantError(t, rec, http.StatusBadRequest)
			if msg := decodeJSON[map[string]string](t, rec)["error"]; msg != tc.want {
				t.Errorf("error = %q, want %q", msg, tc.want)
			}
		})
	}
}

// The active rules find a value the builtin set knows, name the rule that did, and store nothing.
func TestAllRulesModeFindsASecret(t *testing.T) {
	f := newFixture(t)
	e := f.engine(t)
	text := "connect password=" + secret + " now"

	got := postTest(t, f, text, nil)
	if got.Text != text {
		t.Errorf("text = %q, want the text of the request: it holds no placeholder for Guard to hide", got.Text)
	}
	if len(got.Spans) != 1 {
		t.Fatalf("spans = %+v, want one", got.Spans)
	}
	span := got.Spans[0]
	if span.Type != "SECRET" {
		t.Errorf("type = %q, want SECRET", span.Type)
	}
	if span.Rule == "" || span.Rule == "adhoc" {
		t.Errorf("rule = %q, want the name of a builtin rule", span.Rule)
	}
	if piece := fragment(t, got.Text, span); piece != secret {
		t.Errorf("span cuts %q, want %q", piece, secret)
	}

	// Scan, not Anonymize: a test run must not put the value the user typed into the store.
	entities, err := e.Store().ListEntities(t.Context(), e.ProjectID(), 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(entities) != 0 {
		t.Errorf("the store holds %d entities after a test run, want none", len(entities))
	}
}

// Review Focus 2. Scan finds in the text after Guard, where a foreign placeholder has become a
// short \x00S<n>\x00 and a NUL of the text is doubled, and its offsets index that text. It goes
// back with them: the page cuts the text of the answer, not the one it sent. In the second case
// the placeholders shorten the text so much that the same offsets of the request fall into the
// wide text before the value, and a conversion made over the request would miss.
func TestAllRulesModeIndexesTheGuardedText(t *testing.T) {
	f := newFixture(t)
	for _, tc := range []struct{ name, text string }{
		{"placeholder before the value", `password="<EMAIL_1>` + secret + `"`},
		{"placeholders, wide text and a NUL before the value",
			"<EMAIL_1><EMAIL_2><EMAIL_3> привет 😀 \x00 password=" + secret},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := postTest(t, f, tc.text, nil)
			if !strings.Contains(got.Text, "\x00") || got.Text == tc.text {
				t.Errorf("text = %q, want the text after Guard, with a NUL in it, and not the request %q", got.Text, tc.text)
			}
			if len(got.Spans) != 1 {
				t.Fatalf("spans = %+v, want one", got.Spans)
			}
			if piece := fragment(t, got.Text, got.Spans[0]); piece != secret {
				t.Errorf("span cuts %q of the answer text, want %q", piece, secret)
			}
		})
	}
}

// Review Focus 1. A null in spans crashes the page, which reads its length. An answer that found
// nothing, to an empty text as well, carries the text and an empty array.
func TestEmptyResultIsAnEmptyArray(t *testing.T) {
	f := newFixture(t)
	for _, tc := range []struct {
		name, text string
		rule       map[string]any
	}{
		{"candidate, no match", "no ticket here", ticket},
		{"active rules, no match", "no secret here", nil},
		{"active rules, empty text", "", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := f.do(http.MethodPost, "/api/test", testBody(t, tc.text, tc.rule))
			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body)
			}
			// The raw body, not the decoded one: null and a missing field both decode to a nil slice.
			want := `{"text":"` + tc.text + `","spans":[]}`
			if got := strings.TrimSpace(rec.Body.String()); got != want {
				t.Errorf("body = %s, want %s: spans is an empty array, never null or missing", got, want)
			}
		})
	}
}

// The body is decoded before anything else, so a body that is refused costs no engine. An empty
// body is no JSON document at all: with the rule optional, reading it as {} would answer a request
// that sent nothing with a 200.
func TestTestRejectsMalformedBodies(t *testing.T) {
	f := newFixture(t)
	refuseEngine(t, f)
	for _, tc := range []struct{ name, body string }{
		{"unknown field", `{"text":"x","colour":"red"}`},
		{"unknown field of the rule", `{"text":"x","rule":{"pattern":"x","colour":"red"}}`},
		{"second document", `{"text":"x"} {}`},
		{"empty body", ""},
		{"not json", "text=x"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			wantError(t, f.do(http.MethodPost, "/api/test", tc.body), http.StatusBadRequest)
		})
	}
}

// Pasted text is what fills a request, so the cap of Guard is met here before anywhere else. It is
// told apart from a bad body, and costs no engine.
func TestTestBodyLimit(t *testing.T) {
	f := newFixture(t)
	refuseEngine(t, f)
	body := `{"text":"` + strings.Repeat("a", 1<<20)
	wantError(t, f.do(http.MethodPost, "/api/test", body), http.StatusRequestEntityTooLarge)
}
