package rules

import (
	"embed"
	"fmt"

	"github.com/BurntSushi/toml"
)

//go:embed builtin/*.toml
var builtinFS embed.FS

// builtinFile is the shape of the rule set file: an array [[rules]].
type builtinFile struct {
	Rules []builtinRule `toml:"rules"`
}

// builtinRule is a rule in TOML. Enabled is a pointer: a missing key means
// an enabled rule, and there is no other way to tell it from an explicit false.
type builtinRule struct {
	ID          string   `toml:"id"`
	Type        string   `toml:"type"`
	Kind        string   `toml:"kind"`
	Pattern     string   `toml:"pattern"`
	SecretGroup int      `toml:"secret_group"`
	Keywords    []string `toml:"keywords"`
	Allowlist   []string `toml:"allowlist"`
	EntropyMin  float64  `toml:"entropy_min"`
	Validator   string   `toml:"validator"`
	Order       int      `toml:"order"`
	Enabled     *bool    `toml:"enabled"`
}

// spec translates a rule of the file into an engine spec.
func (b builtinRule) spec() Spec {
	return Spec{
		ID:          b.ID,
		Type:        b.Type,
		Kind:        b.Kind,
		Pattern:     b.Pattern,
		SecretGroup: b.SecretGroup,
		Keywords:    b.Keywords,
		Allowlist:   b.Allowlist,
		EntropyMin:  b.EntropyMin,
		Validator:   b.Validator,
		Order:       b.Order,
		Enabled:     b.Enabled == nil || *b.Enabled,
		Builtin:     true,
	}
}

// Builtin parses the embedded rule set into rule specs. All rules are returned,
// together with the disabled ones: the user must see them and be able to enable them, and
// the filter of disabled rules is the business of RulesForProject.
func Builtin() ([]Spec, error) {
	files, err := builtinFS.ReadDir("builtin")
	if err != nil {
		return nil, fmt.Errorf("builtin rule set: %w", err)
	}
	var specs []Spec
	for _, file := range files {
		raw, err := builtinFS.ReadFile("builtin/" + file.Name())
		if err != nil {
			return nil, fmt.Errorf("builtin rule set %s: %w", file.Name(), err)
		}
		var parsed builtinFile
		if err := toml.Unmarshal(raw, &parsed); err != nil {
			return nil, fmt.Errorf("builtin rule set %s: %w", file.Name(), err)
		}
		for _, r := range parsed.Rules {
			specs = append(specs, r.spec())
		}
	}
	return specs, nil
}

// LoadBuiltin compiles the builtin rule set. A rule that does not compile
// brings down the whole rule set: the builtin set cannot contain broken rules — in
// contrast to an imported one, where such a rule shows up in the report as skipped.
func LoadBuiltin() ([]Rule, error) {
	specs, err := Builtin()
	if err != nil {
		return nil, err
	}
	out := make([]Rule, 0, len(specs))
	for _, spec := range specs {
		r, err := Compile(spec)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, nil
}
