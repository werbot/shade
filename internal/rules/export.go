package rules

import (
	"fmt"
	"io"

	"github.com/BurntSushi/toml"
)

// exportFile is the shape of the exported file: an array [[rules]], as in the builtin
// the rule set. The format belongs to the rules package, so the encoder lives next to
// the importer: it also translates a Rule into the file keys.
type exportFile struct {
	Rules []exportRule `toml:"rules"`
}

// exportRule is a rule for output. The keys and their order repeat builtin/*.toml,
// and not the storage schema: the export is a rule set file, and it will be read by the same
// the schema as the builtin set.
//
// There is no enabled key here: RulesForProject returns only enabled rules, and
// a missing key means an enabled rule. A disabled rule does not get into the export
// does not get in — the price paid by the caller that took the set through
// RulesForProject; if disabled rules ever need to be exported, a separate
// storage method that returns specs together with the flag.
//
// The numeric fields are pointers, not omitempty on the value: isEmpty in BurntSushi/toml
// does not look at numbers, and `secret_group = 0` would go into the file as is.
type exportRule struct {
	ID          string   `toml:"id"`
	Type        string   `toml:"type"`
	Kind        string   `toml:"kind"`
	Pattern     string   `toml:"pattern"`
	SecretGroup *int     `toml:"secret_group,omitempty"`
	Keywords    []string `toml:"keywords,omitempty"`
	Allowlist   []string `toml:"allowlist,omitempty"`
	EntropyMin  *float64 `toml:"entropy_min,omitempty"`
	Validator   string   `toml:"validator,omitempty"`
	Order       *int     `toml:"order,omitempty"`
}

// nonZero returns a pointer to the value and nil for zero: an optional field with
// a zero value is not written to the file, and it is later read back as the same zero.
func nonZero[T comparable](v T) *T {
	var zero T
	if v == zero {
		return nil
	}
	return &v
}

// ExportTOML writes rules as TOML in our format. Selecting the rules is the
// the caller: the encoder writes exactly what it was given, and a silently dropped
// record would be indistinguishable from an error for it.
func ExportTOML(w io.Writer, rs []Rule) error {
	f := exportFile{Rules: make([]exportRule, 0, len(rs))}
	for _, r := range rs {
		f.Rules = append(f.Rules, exportRule{
			ID:          r.Name,
			Type:        r.Type,
			Kind:        r.Kind,
			Pattern:     r.Pattern,
			SecretGroup: nonZero(r.SecretGroup),
			Keywords:    r.Keywords,
			Allowlist:   r.Allowlist,
			EntropyMin:  nonZero(r.EntropyMin),
			Validator:   r.Validator,
			Order:       nonZero(r.OrderIdx),
		})
	}
	if err := toml.NewEncoder(w).Encode(f); err != nil {
		return fmt.Errorf("exporting rules: %w", err)
	}
	return nil
}
