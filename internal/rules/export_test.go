package rules

import (
	"slices"
	"strings"
	"testing"

	"github.com/BurntSushi/toml"
)

// The export is read by the same schema as the builtin rule set: the file format is one, and
// they cannot diverge — otherwise the exported set would stop being a rule set.
func TestExportTOMLKeepsEveryField(t *testing.T) {
	r, err := Compile(Spec{
		ID: "acme", Type: "TICKET", Kind: "regex", Pattern: `ACME-(\d{6})`,
		SecretGroup: 1, Keywords: []string{"acme"}, Allowlist: []string{`^ACME-0+$`},
		EntropyMin: 3.5, Validator: "random_enough", Order: 7, Enabled: true,
	})
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}

	var buf strings.Builder
	if err := ExportTOML(&buf, []Rule{r}); err != nil {
		t.Fatalf("ExportTOML: %v", err)
	}
	var parsed builtinFile
	if err := toml.Unmarshal([]byte(buf.String()), &parsed); err != nil {
		t.Fatalf("the export does not parse: %v\n%s", err, buf.String())
	}
	if len(parsed.Rules) != 1 {
		t.Fatalf("rules in the file %d:\n%s", len(parsed.Rules), buf.String())
	}
	got := parsed.Rules[0].spec()
	if got.ID != "acme" || got.Type != "TICKET" || got.Kind != "regex" ||
		got.Pattern != `ACME-(\d{6})` || got.SecretGroup != 1 ||
		got.EntropyMin != 3.5 || got.Validator != "random_enough" ||
		got.Order != 7 || !got.Enabled ||
		!slices.Equal(got.Keywords, []string{"acme"}) ||
		!slices.Equal(got.Allowlist, []string{`^ACME-0+$`}) {
		t.Fatalf("fields did not survive the export: %+v\n%s", got, buf.String())
	}
}

// Optional fields with a zero value are not written to the file: that is how the
// the builtin files, and `secret_group = 0` and `order = 0` would be noise in them.
// The numeric fields are pointers for this: omitempty on a number in BurntSushi/toml
// is inert.
func TestExportTOMLOmitsEmptyFields(t *testing.T) {
	r, err := Compile(Spec{ID: "plain", Type: "SECRET", Kind: "literal",
		Pattern: "x", Enabled: true})
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	var buf strings.Builder
	if err := ExportTOML(&buf, []Rule{r}); err != nil {
		t.Fatalf("ExportTOML: %v", err)
	}
	for _, key := range []string{"secret_group", "keywords", "allowlist",
		"entropy_min", "validator", "order", "enabled"} {
		if strings.Contains(buf.String(), key) {
			t.Fatalf("empty field %q made it into the file:\n%s", key, buf.String())
		}
	}
	if !strings.Contains(buf.String(), `id = "plain"`) {
		t.Fatalf("the rule was lost:\n%s", buf.String())
	}
}
