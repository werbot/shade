package rules_test

import (
	"strings"
	"testing"

	"github.com/werbot/shade/internal/rules"
)

func TestDetectKeepsByteOffsetsOnCyrillic(t *testing.T) {
	r, err := rules.Compile(rules.Spec{ID: "host", Type: "HOST", Kind: "regex", Pattern: `db\.prod\.local`})
	if err != nil {
		t.Fatal(err)
	}
	text := "хост: db.prod.local — продакшн"
	spans := rules.Detect(text, []rules.Rule{r})
	if len(spans) != 1 {
		t.Fatalf("got %+v", spans)
	}
	if text[spans[0].Start:spans[0].End] != "db.prod.local" {
		t.Fatalf("span %d..%d = %q", spans[0].Start, spans[0].End, text[spans[0].Start:spans[0].End])
	}
}

func TestDetectAppliesKeywordsPreFilter(t *testing.T) {
	r, _ := rules.Compile(rules.Spec{ID: "k", Type: "SECRET", Kind: "regex",
		Pattern: `Xk7pQ2mZ`, Keywords: []string{"zzz"}})
	if got := rules.Detect("Xk7pQ2mZ appears", []rules.Rule{r}); len(got) != 0 {
		t.Fatalf("keyword prefilter not applied: %+v", got)
	}
}

func TestDetectOrdersByOrderIdx(t *testing.T) {
	generic, _ := rules.Compile(rules.Spec{ID: "generic", Type: "SECRET", Kind: "regex",
		Pattern: `\w+`, Order: 200})
	specific, _ := rules.Compile(rules.Spec{ID: "specific", Type: "TOKEN", Kind: "regex",
		Pattern: `AKIA[0-9A-Z]{4}`, Order: 10})
	spans := rules.Detect("AKIA1234", []rules.Rule{generic, specific})
	if len(spans) != 1 || spans[0].Type != "TOKEN" {
		t.Fatalf("specific rule must win: %+v", spans)
	}
}

func TestMergeSwallowsNestedSpan(t *testing.T) {
	spans := []rules.Span{{Start: 0, End: 10, Type: "DSN"}, {Start: 2, End: 6, Type: "SECRET"}}
	merged := rules.Merge(spans)
	if len(merged) != 1 || merged[0].Type != "DSN" {
		t.Fatalf("got %+v", merged)
	}
}

func TestMergeKeepsAdjacentSpansApart(t *testing.T) {
	// A span starting exactly where the previous one ends does not overlap
	// with it: merging would glue two different rules into one.
	spans := []rules.Span{{Start: 0, End: 4, Type: "TOKEN"}, {Start: 4, End: 8, Type: "HOST"}}
	if got := rules.Merge(spans); len(got) != 2 {
		t.Fatalf("adjacent spans were merged: %+v", got)
	}
}

func TestCompileRejectsLookaround(t *testing.T) {
	if _, err := rules.Compile(rules.Spec{ID: "bad", Type: "SECRET", Kind: "regex",
		Pattern: `(?<!\d)1`}); err == nil {
		t.Fatal("lookbehind must fail to compile")
	}
}

func TestCompileRejectsPatternErrorBeforeTypeError(t *testing.T) {
	// A rule has both a bad pattern and a bad type: the message must name the pattern,
	// otherwise the rule author will fix the wrong field.
	_, err := rules.Compile(rules.Spec{ID: "bad", Type: "NOSUCHTYPE", Kind: "regex",
		Pattern: `(?<!\d)1`})
	if err == nil || !strings.Contains(err.Error(), "pattern") {
		t.Fatalf("want pattern error first, got %v", err)
	}
}

func TestCompileRejectsUnknownType(t *testing.T) {
	if _, err := rules.Compile(rules.Spec{ID: "bad", Type: "NOSUCHTYPE", Kind: "regex",
		Pattern: `x`}); err == nil {
		t.Fatal("a type outside the closed set must be a compile error")
	}
}

func TestCompileRejectsUnknownKind(t *testing.T) {
	if _, err := rules.Compile(rules.Spec{ID: "bad", Type: "SECRET", Kind: "fuzzy",
		Pattern: `x`}); err == nil {
		t.Fatal("an unknown kind must be a compile error")
	}
}

func TestCompileRejectsBadAllowlist(t *testing.T) {
	// A swallowed allowlist error would leave a nil regex in the rule, and
	// Detect would panic on the very first match.
	if _, err := rules.Compile(rules.Spec{ID: "bad", Type: "SECRET", Kind: "regex",
		Pattern: `x`, Allowlist: []string{`(`}}); err == nil {
		t.Fatal("a broken allowlist pattern must be a compile error")
	}
}

func TestCompileLiteralIsQuoted(t *testing.T) {
	// literal must escape metacharacters: a dot is a dot, not «any
	// character», otherwise the rule would match someone else's text.
	r, err := rules.Compile(rules.Spec{ID: "lit", Type: "HOST", Kind: "literal", Pattern: `a.b`})
	if err != nil {
		t.Fatal(err)
	}
	if got := rules.Detect("axb", []rules.Rule{r}); len(got) != 0 {
		t.Fatalf("literal matched as a regex: %+v", got)
	}
	if got := rules.Detect("a.b", []rules.Rule{r}); len(got) != 1 {
		t.Fatalf("literal did not match the exact text: %+v", got)
	}
}

func TestDetectUsesSecretGroup(t *testing.T) {
	r, err := rules.Compile(rules.Spec{ID: "kv", Type: "SECRET", Kind: "regex",
		Pattern: `token=([A-Za-z0-9]+)`, SecretGroup: 1})
	if err != nil {
		t.Fatal(err)
	}
	text := "token=Xk7pQ2mZ"
	spans := rules.Detect(text, []rules.Rule{r})
	if len(spans) != 1 {
		t.Fatalf("got %+v", spans)
	}
	if got := text[spans[0].Start:spans[0].End]; got != "Xk7pQ2mZ" {
		t.Fatalf("the span must point at the group, not at the whole match: %q", got)
	}
}

func TestDetectDropsAllowlistedMatch(t *testing.T) {
	r, err := rules.Compile(rules.Spec{ID: "host", Type: "HOST", Kind: "regex",
		Pattern: `\w+\.local`, Allowlist: []string{`^db\.`}})
	if err != nil {
		t.Fatal(err)
	}
	spans := rules.Detect("db.local app.local", []rules.Rule{r})
	if len(spans) != 1 || spans[0].Start != len("db.local ") {
		t.Fatalf("allowlist did not drop the match: %+v", spans)
	}
}

func TestDetectSkipsEmptyMatch(t *testing.T) {
	// An empty match is not data: a zero-length span would make Allocate create
	// a placeholder for an empty string.
	r, err := rules.Compile(rules.Spec{ID: "maybe", Type: "SECRET", Kind: "regex", Pattern: `x*`})
	if err != nil {
		t.Fatal(err)
	}
	if got := rules.Detect("abc", []rules.Rule{r}); len(got) != 0 {
		t.Fatalf("an empty match must not produce a span: %+v", got)
	}
}

func TestDetectEntropyKindFiltersLowEntropy(t *testing.T) {
	r, err := rules.Compile(rules.Spec{ID: "entropy-secret", Type: "SECRET", Kind: "entropy",
		Pattern: `[A-Za-z0-9]{8}`, EntropyMin: 3})
	if err != nil {
		t.Fatal(err)
	}
	text := "aaaaaaaa Xk7pQ2mZ"
	spans := rules.Detect(text, []rules.Rule{r})
	if len(spans) != 1 {
		t.Fatalf("got %+v", spans)
	}
	if got := text[spans[0].Start:spans[0].End]; got != "Xk7pQ2mZ" {
		t.Fatalf("a low-entropy match must be dropped: %q", got)
	}
}

// An intersection must cover the union of the bounds: the loser by priority
// span is not dropped, otherwise its non-overlapping bytes go to the model
// unmasked.
func TestMergeUnionsOverlappingBounds(t *testing.T) {
	specific := rules.Span{Start: 2, End: 10, Type: "TOKEN", Rule: "specific"}
	generic := rules.Span{Start: 0, End: 4, Type: "SECRET", Rule: "generic"}
	got := rules.Merge([]rules.Span{specific, generic})
	if len(got) != 1 || got[0].Start != 0 || got[0].End != 10 {
		t.Fatalf("the bounds must be the union [0,10): %+v", got)
	}
	if got[0].Type != "TOKEN" || got[0].Rule != "specific" {
		t.Fatalf("the more specific span sets the type: %+v", got[0])
	}
}

// Equal Start, the specific rule is shorter than the general one: the tail must also be
// covered, and the type must stay with the specific rule.
func TestMergeCoversTailOfBroaderRule(t *testing.T) {
	generic, _ := rules.Compile(rules.Spec{ID: "generic", Type: "SECRET", Kind: "regex", Pattern: `\w+`, Order: 200})
	specific, _ := rules.Compile(rules.Spec{ID: "specific", Type: "TOKEN", Kind: "regex", Pattern: `AKIA[0-9A-Z]{4}`, Order: 10})
	text := "AKIA1234ZZZZZZZZZZ"
	spans := rules.Detect(text, []rules.Rule{generic, specific})
	if len(spans) != 1 || spans[0].Start != 0 || spans[0].End != len(text) || spans[0].Type != "TOKEN" {
		t.Fatalf("the tail of length %d must be covered, and the type must be TOKEN: %+v", len(text), spans)
	}
}

// Detect is a pure function of the set passed in: it does not look at Enabled.
// The filter of disabled rules lives in RulesForProject, otherwise `shade rules test`
// (Task 13) could not check a rule before enabling it.
func TestDetectIgnoresEnabledFlag(t *testing.T) {
	r, err := rules.Compile(rules.Spec{ID: "off", Type: "SECRET", Kind: "regex", Pattern: `Zq9`})
	if err != nil {
		t.Fatal(err)
	}
	if spans := rules.Detect("Zq9", []rules.Rule{r}); len(spans) != 1 {
		t.Fatalf("a disabled rule passed explicitly must fire: %+v", spans)
	}
}
