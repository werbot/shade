package rules

import (
	"fmt"
	"io"

	"github.com/BurntSushi/toml"
)

// ImportError is a rule that could not be imported, and the reason. Importing
// someone else's config is not interrupted because of one rule: a gitleaks file almost always
// contains something the engine cannot do (lookaround, paths, stopwords), and the
// the whole set cannot be lost because of this.
type ImportError struct {
	RuleID string
	Reason string
}

// gitleaksFile is the readable part of the gitleaks config schema. It has more fields than
// described here: description is dropped (in the rule schema of this phase there is
// there is no column for it), paths, regexTarget, stopwords and condition — see gitleaksAllowlist.
type gitleaksFile struct {
	Rules []gitleaksRule `toml:"rules"`
}

type gitleaksRule struct {
	ID          string   `toml:"id"`
	Type        string   `toml:"type"`
	Regex       string   `toml:"regex"`
	SecretGroup int      `toml:"secretGroup"`
	Entropy     float64  `toml:"entropy"`
	Keywords    []string `toml:"keywords"`
	// Allowlist is the old form (a single table), Allowlists is the new one (an array
	// of tables). gitleaks reads both, and both occur in other people's configs.
	Allowlist  gitleaksAllowlist   `toml:"allowlist"`
	Allowlists []gitleaksAllowlist `toml:"allowlists"`
}

// gitleaksAllowlist — only regexes and a single regex are taken from the allowlist.
// The fields paths, regexTarget, stopwords and condition are deliberately not ported: our
// the allowlist holds regexes against the match itself, while the engine has neither file context
// nor a choice of the matching target. An imported rule with such a field is narrowed
// down to the match, but does not lose its other regexes.
type gitleaksAllowlist struct {
	Regexes []string `toml:"regexes"`
	Regex   string   `toml:"regex"`
}

// patterns returns the allowlist regexes. The slice goes to the caller as is, without
// a copy: nobody mutates it, Compile keeps it the same way as
// Keywords with Pattern.
func (a gitleaksAllowlist) patterns() []string {
	out := a.Regexes
	if a.Regex != "" {
		out = append(out, a.Regex)
	}
	return out
}

// spec translates a gitleaks rule into an engine spec.
//
// Kind is always regex: in gitleaks entropy is a threshold filter on top of a
// a regex, not a separate rule kind, so it goes into EntropyMin, by
// which the engine applies the filter (detect.go). Our Kind == "entropy" is about
// a rule with no regex at all.
//
// Order 0 and Builtin false: builtin rules take order from 10, so
// an imported rule is applied before a builtin one — the user's rule
// more important. Enabled true — an imported rule is enabled right away.
func (g gitleaksRule) spec() Spec {
	typ := g.Type
	if typ == "" {
		typ = "SECRET"
	}
	return Spec{
		ID:          g.ID,
		Type:        typ,
		Kind:        "regex",
		Pattern:     g.Regex,
		SecretGroup: g.SecretGroup,
		Keywords:    g.Keywords,
		Allowlist:   g.allowlist(),
		EntropyMin:  g.Entropy,
		Order:       0,
		Enabled:     true,
	}
}

// allowlist collects the regexes of both forms.
func (g gitleaksRule) allowlist() []string {
	out := g.Allowlist.patterns()
	for _, a := range g.Allowlists {
		out = append(out, a.patterns()...)
	}
	return out
}

// ImportTOML parses a gitleaks config and returns the rule specs that the
// the engine accepted, plus a report of the skipped ones.
//
// The only reason to return err is unparsable TOML: if the file was read,
// an error of a single rule (no id, an uncompilable pattern, an unknown type,
// a secret group outside the pattern) goes to skipped with the rule name and the reason.
func ImportTOML(r io.Reader) (imported []Spec, skipped []ImportError, err error) {
	raw, err := io.ReadAll(r)
	if err != nil {
		return nil, nil, fmt.Errorf("gitleaks import: %w", err)
	}
	var f gitleaksFile
	if err := toml.Unmarshal(raw, &f); err != nil {
		return nil, nil, fmt.Errorf("gitleaks import: %w", err)
	}
	for i, g := range f.Rules {
		// A rule without an id has no name for the report and no name to store in the database: the name is
		// the key of the rule. The position in the file is the only available identifier.
		if g.ID == "" {
			skipped = append(skipped, ImportError{
				RuleID: fmt.Sprintf("rules[%d]", i),
				Reason: "the rule has no id",
			})
			continue
		}
		// An empty regex is a rule that can never fire: regexp.Compile("")
		// gives a zero-width match, and bounds drops an empty group, so
		// that the rule never finds anything. That is what a path-only rule
		// gitleaks: the importer drops the path field, and the rule has no regex.
		if g.Regex == "" {
			skipped = append(skipped, ImportError{RuleID: g.ID, Reason: "no regex"})
			continue
		}
		spec := g.spec()
		if _, err := Compile(spec); err != nil {
			skipped = append(skipped, ImportError{RuleID: spec.ID, Reason: err.Error()})
			continue
		}
		imported = append(imported, spec)
	}
	return imported, skipped, nil
}
