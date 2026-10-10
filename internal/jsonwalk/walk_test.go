package jsonwalk_test

import (
	"bytes"
	"strings"
	"testing"

	"github.com/werbot/shade/internal/jsonwalk"
)

func up(s string) (string, bool) { return strings.ToUpper(s), true }

// The number literals are the point: parsed without UseNumber they come back as
// 1, 1000000 and 1.2345678901234567e+19. The key keeps its spelling too, and
// "<HOST_1>" is not HTML-escaped on the way out.
func TestRewriteJSONKeepsShapeAndNumbers(t *testing.T) {
	cases := []struct{ in, want string }{
		{`{"a":"x"}`, `{"a":"X"}`},
		{`["x",1,true,null,1.0,1e6,12345678901234567890,-0]`, `["X",1,true,null,1.0,1e6,12345678901234567890,-0]`},
		{`{"n":{"m":["x"]}}`, `{"n":{"m":["X"]}}`},
		{`{"<HOST_1>":"x"}`, `{"<HOST_1>":"X"}`},
		{`"x"`, `"X"`},
		{`null`, `null`},
	}
	for _, c := range cases {
		got, _, err := jsonwalk.RewriteJSON([]byte(c.in), up)
		if err != nil {
			t.Fatalf("%s: %v", c.in, err)
		}
		if string(got) != c.want {
			t.Fatalf("%s -> %s, want %s", c.in, got, c.want)
		}
	}
}

func TestRewriteJSONReportsNoChange(t *testing.T) {
	same := func(s string) (string, bool) { return s, false }
	got, changed, err := jsonwalk.RewriteJSON([]byte(`{"a":"x","b":[1,null]}`), same)
	if err != nil || changed {
		t.Fatalf("changed=%v err=%v", changed, err)
	}
	if string(got) != `{"a":"x","b":[1,null]}` {
		t.Fatalf("unchanged input must round-trip: %s", got)
	}
}

// An unchanged document must come back untouched, not re-marshalled: re-marshalling
// would drop the whitespace and sort the keys, and the tool never asked for that.
func TestRewriteJSONRoundTripsUnchangedInputByteForByte(t *testing.T) {
	same := func(s string) (string, bool) { return s, false }
	in := []byte("{\n\t\"z\": \"<HOST_1>\",\n\t\"a\": [1.0, 1e6, 12345678901234567890, -0, null, true]\n}")
	got, changed, err := jsonwalk.RewriteJSON(in, same)
	if err != nil {
		t.Fatal(err)
	}
	if changed {
		t.Fatal("nothing changed, so changed must be false")
	}
	if !bytes.Equal(got, in) {
		t.Fatalf("got %q, want %q", got, in)
	}
}

func TestRewriteJSONRejectsJunk(t *testing.T) {
	for _, raw := range []string{"", "not json", `{"a":"x"} {"b":"y"}`, `{"a":"x"} trailing`} {
		if _, _, err := jsonwalk.RewriteJSON([]byte(raw), up); err == nil {
			t.Fatalf("%q must not parse", raw)
		}
	}
}
