package rules_test

import (
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/werbot/shade/internal/rules"
)

func TestImportReportsUnsupportedRuleWithoutAborting(t *testing.T) {
	f, err := os.Open("testdata/gitleaks_sample.toml")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	imported, skipped, err := rules.ImportTOML(f)
	if err != nil {
		t.Fatalf("import must not abort: %v", err)
	}
	if len(imported) != 1 {
		t.Fatalf("want 1 imported, got %d", len(imported))
	}
	if len(skipped) != 2 {
		t.Fatalf("want 2 skipped, got %+v", skipped)
	}
	for _, s := range skipped {
		if s.RuleID == "" || s.Reason == "" {
			t.Fatalf("skipped entry must name rule and reason: %+v", s)
		}
	}
}

func TestImportMapsGitleaksFieldNames(t *testing.T) {
	td := `[[rules]]
id = "aws-access-token"
regex = '''AKIA([0-9A-Z]{4})'''
secretGroup = 1
keywords = ["akia"]
entropy = 0.0
`
	imported, skipped, err := rules.ImportTOML(strings.NewReader(td))
	if err != nil || len(skipped) != 0 || len(imported) != 1 {
		t.Fatalf("err=%v imported=%+v skipped=%+v", err, imported, skipped)
	}
	if imported[0].SecretGroup != 1 || imported[0].Keywords[0] != "akia" {
		t.Fatalf("got %+v", imported[0])
	}
}

// TestImportDefaults — fields absent from a gitleaks rule must get
// the values of the import decisions: type SECRET, kind regex (in gitleaks entropy is
// a threshold on top of a match, not a separate rule kind), order 0 — before
// the builtin ones, which take order from 10, enabled and not builtin.
func TestImportDefaults(t *testing.T) {
	td := `[[rules]]
id = "r"
regex = '''x([0-9]+)'''
secretGroup = 1
`
	imported, skipped, err := rules.ImportTOML(strings.NewReader(td))
	if err != nil || len(skipped) != 0 || len(imported) != 1 {
		t.Fatalf("err=%v imported=%+v skipped=%+v", err, imported, skipped)
	}
	got := imported[0]
	if got.Type != "SECRET" || got.Kind != "regex" || got.Order != 0 || !got.Enabled || got.Builtin {
		t.Fatalf("got %+v", got)
	}
}

// TestImportReportsSecretGroupOutsidePattern — a secret group outside the
// the pattern makes the rule find nothing: bounds will drop every match. Such a
// rule must get into the report instead of being imported as a quiet stub.
func TestImportReportsSecretGroupOutsidePattern(t *testing.T) {
	td := `[[rules]]
id = "no-group"
regex = '''AKIA[0-9A-Z]{4}'''
secretGroup = 1
`
	imported, skipped, err := rules.ImportTOML(strings.NewReader(td))
	if err != nil {
		t.Fatal(err)
	}
	if len(imported) != 0 || len(skipped) != 1 {
		t.Fatalf("imported=%+v skipped=%+v", imported, skipped)
	}
	if skipped[0].RuleID != "no-group" || skipped[0].Reason == "" {
		t.Fatalf("got %+v", skipped[0])
	}
}

// TestImportReadsBothAllowlistForms — gitleaks accepts both the old single
// the [rules.allowlist] table and the new [[rules.allowlists]] array; other people's
// configs occur in both forms.
func TestImportReadsBothAllowlistForms(t *testing.T) {
	td := `[[rules]]
id = "old"
regex = '''old([0-9]+)'''
secretGroup = 1

[rules.allowlist]
regexes = ["0000"]
regex = "1111"

[[rules]]
id = "new"
regex = '''new([0-9]+)'''
secretGroup = 1

[[rules.allowlists]]
regexes = ["2222"]

[[rules.allowlists]]
regexes = ["3333"]
`
	imported, skipped, err := rules.ImportTOML(strings.NewReader(td))
	if err != nil || len(skipped) != 0 {
		t.Fatalf("err=%v skipped=%+v", err, skipped)
	}
	want := map[string][]string{
		"old": {"0000", "1111"},
		"new": {"2222", "3333"},
	}
	if len(imported) != len(want) {
		t.Fatalf("got %+v", imported)
	}
	for _, s := range imported {
		// The presence of the key is mandatory: want[unknown ID] gives nil, and
		// slices.Equal(nil, nil) — true, and a rule with someone else's name would pass
		// the check on an empty allowlist.
		w, ok := want[s.ID]
		if !ok {
			t.Fatalf("unexpected rule %q", s.ID)
		}
		if !slices.Equal(s.Allowlist, w) {
			t.Errorf("%s: allowlist = %v, want %v", s.ID, s.Allowlist, w)
		}
	}
}

// TestImportNamesRuleWithoutIDByPosition — a rule without an id has no name at all, and
// the report must name every skipped rule: the position in the file is the only
// available identifier.
func TestImportNamesRuleWithoutIDByPosition(t *testing.T) {
	td := `[[rules]]
regex = '''x([0-9]+)'''
secretGroup = 1
`
	imported, skipped, err := rules.ImportTOML(strings.NewReader(td))
	if err != nil {
		t.Fatal(err)
	}
	if len(imported) != 0 || len(skipped) != 1 {
		t.Fatalf("imported=%+v skipped=%+v", imported, skipped)
	}
	if skipped[0].RuleID != "rules[0]" || skipped[0].Reason == "" {
		t.Fatalf("got %+v", skipped[0])
	}
}

// TestImportReportsRuleWithoutRegex — a gitleaks path-only rule: it has no
// it has none, the importer drops the path field. An empty pattern compiles into
// a zero-width match that bounds drops, that is, the rule
// never finds anything — that is the same class as a secret group outside the pattern.
func TestImportReportsRuleWithoutRegex(t *testing.T) {
	td := `[[rules]]
id = "path-only"
path = '''\.pem$'''
`
	imported, skipped, err := rules.ImportTOML(strings.NewReader(td))
	if err != nil {
		t.Fatal(err)
	}
	if len(imported) != 0 || len(skipped) != 1 {
		t.Fatalf("imported=%+v skipped=%+v", imported, skipped)
	}
	if skipped[0].RuleID != "path-only" || skipped[0].Reason == "" {
		t.Fatalf("got %+v", skipped[0])
	}
}

// TestImportEntropyThresholdFiltersMatches — the entropy threshold from gitleaks must
// work for an imported rule. Its Kind is "regex" (in gitleaks entropy is
// a threshold on top of the regex match, not a separate rule kind), so a gate by
// Kind would make the threshold inert: the rule would match everything the regex caught.
func TestImportEntropyThresholdFiltersMatches(t *testing.T) {
	td := `[[rules]]
id = "noisy"
regex = '''([a-z0-9]{8})'''
secretGroup = 1
entropy = 2.5
`
	imported, skipped, err := rules.ImportTOML(strings.NewReader(td))
	if err != nil || len(skipped) != 0 || len(imported) != 1 {
		t.Fatalf("err=%v imported=%+v skipped=%+v", err, imported, skipped)
	}
	r, err := rules.Compile(imported[0])
	if err != nil {
		t.Fatal(err)
	}
	// "aaaaaaa1" has entropy 0.54 bits, "b7k2m9qz" has 3.0.
	text := "aaaaaaa1 b7k2m9qz"
	spans := rules.Detect(text, []rules.Rule{r})
	if len(spans) != 1 {
		t.Fatalf("want only the high-entropy match, got %+v", spans)
	}
	if got := text[spans[0].Start:spans[0].End]; got != "b7k2m9qz" {
		t.Fatalf("got %q", got)
	}
}

// TestImportFailsOnlyOnBrokenTOML — an unparsable file is the only reason
// return err.
func TestImportFailsOnlyOnBrokenTOML(t *testing.T) {
	if _, _, err := rules.ImportTOML(strings.NewReader("[[rules]\nid = 1")); err == nil {
		t.Fatal("broken TOML must return err")
	}
}
