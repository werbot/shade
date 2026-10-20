package rules_test

import (
	"slices"
	"strings"
	"testing"

	"github.com/werbot/shade/internal/rules"
)

// A spec with a pattern alone is a whole ad-hoc rule: the kind and the type come from the
// defaults, the name is the ad-hoc one.
func TestTestSampleFillsTheDefaults(t *testing.T) {
	got, err := rules.TestSample(rules.Spec{Pattern: "ab+"}, "xabbby")
	if err != nil {
		t.Fatal(err)
	}
	want := []rules.Span{{Start: 1, End: 5, Type: "SECRET", Rule: rules.AdHocName}}
	if !slices.Equal(got, want) {
		t.Fatalf("got %+v, want %+v", got, want)
	}
}

// What the caller set is not overwritten by the defaults. The "kind" row needs a pattern
// that reads differently as a regex: the dot of a literal matches a dot and nothing else. The
// name is the one exception: a spec that carries an ID is still the ad-hoc rule, so the caller
// cannot name it.
func TestTestSampleKeepsWhatIsGiven(t *testing.T) {
	for _, tc := range []struct {
		name string
		spec rules.Spec
		text string
		want rules.Span
	}{
		{"type", rules.Spec{Type: "TICKET", Kind: "literal", Pattern: "ACME-1"}, "see ACME-1 now",
			rules.Span{Start: 4, End: 10, Type: "TICKET", Rule: rules.AdHocName}},
		{"kind", rules.Spec{Kind: "literal", Pattern: "a.c"}, "abc a.c",
			rules.Span{Start: 4, End: 7, Type: "SECRET", Rule: rules.AdHocName}},
		{"secret group", rules.Spec{Pattern: `key=(\w+)`, SecretGroup: 1}, "key=abc",
			rules.Span{Start: 4, End: 7, Type: "SECRET", Rule: rules.AdHocName}},
		{"its own name is not kept", rules.Spec{ID: "x", Pattern: "ab+"}, "xabbby",
			rules.Span{Start: 1, End: 5, Type: "SECRET", Rule: rules.AdHocName}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := rules.TestSample(tc.spec, tc.text)
			if err != nil {
				t.Fatal(err)
			}
			if want := []rules.Span{tc.want}; !slices.Equal(got, want) {
				t.Fatalf("got %+v, want %+v", got, want)
			}
		})
	}
}

// A rule that does not compile is an error, not an empty match list.
func TestTestSampleReportsACompileError(t *testing.T) {
	spans, err := rules.TestSample(rules.Spec{Pattern: "("}, "x")
	if err == nil {
		t.Fatalf("a pattern that does not compile must fail, got %+v", spans)
	}
	if !strings.Contains(err.Error(), "(") {
		t.Fatalf("the error does not name the pattern: %v", err)
	}
	// The author of a broken pattern reads which rule it was, and the ad-hoc one has no other name.
	if !strings.Contains(err.Error(), rules.AdHocName) {
		t.Fatalf("the error does not name the rule: %v", err)
	}
}

// TestSample is Compile and Detect over one rule and nothing else: the answer is the one
// Detect gives for the same rule written out in full.
func TestTestSampleMatchesDetect(t *testing.T) {
	const text = "xabbby"
	rule, err := rules.Compile(rules.Spec{
		ID: rules.AdHocName, Type: "SECRET", Kind: "regex", Pattern: "ab+", Enabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	got, err := rules.TestSample(rules.Spec{Pattern: "ab+"}, text)
	if err != nil {
		t.Fatal(err)
	}
	if want := rules.Detect(text, []rules.Rule{rule}); !slices.Equal(got, want) {
		t.Fatalf("got %+v, want %+v", got, want)
	}
}
