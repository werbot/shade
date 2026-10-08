package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"slices"

	"github.com/werbot/shade/internal/rules"
)

// adHocRule is the name under which a rule from the `rules test` flags is compiled.
// It does not get into the database, but a Compile error names the rule, and without a name
// the message would just say "pattern ...".
const adHocRule = "adhoc"

// runRulesImport reads a gitleaks config and moves the rules into the chosen scope.
// The report is printed in full: the user must see both what made it and what
// the engine did not understand.
func runRulesImport(args []string, stdio IO) int {
	const name = "rules import"
	var global bool
	pos, err := parseFlags(args, nil, map[string]*bool{"--global": &global})
	if err != nil {
		return fail(stdio, name, 2, err)
	}
	if len(pos) != 1 {
		return fail(stdio, name, 2, errors.New("exactly one rules file is required"))
	}
	file, err := os.Open(pos[0])
	if err != nil {
		return fail(stdio, name, 1, err)
	}
	defer file.Close()
	imported, skipped, err := rules.ImportTOML(file)
	if err != nil {
		return fail(stdio, name, 1, err)
	}

	ctx := context.Background()
	e, err := openEngine(ctx, "")
	if err != nil {
		return fail(stdio, name, 1, err)
	}
	defer e.Close()

	area := scope(e, global)
	stored := 0
	for _, spec := range imported {
		// An error in one rule is a skip with a reason, not a command failure: foreign
		// rules must not be lost because of a single conflict.
		if err := e.Store().AddRule(ctx, area, spec); err != nil {
			skipped = append(skipped, rules.ImportError{RuleID: spec.ID, Reason: err.Error()})
			continue
		}
		stored++
	}
	fmt.Fprintf(stdio.Out, "imported: %d, skipped: %d\n", stored, len(skipped))
	for _, s := range skipped {
		fmt.Fprintf(stdio.Out, "skipped %q: %s\n", s.RuleID, s.Reason)
	}
	return 0
}

// runRulesExport prints the active rule set of the project as TOML in our format.
//
// RulesForProject is used, so neither disabled rules
// nor rules of other projects get into the file, while builtin ones are dropped separately: they
// restore themselves on every machine and would be dead weight in the file.
// The "export → import" circle is not closed: import reads the gitleaks format, while export
// writes ours, and there is no reverse mapping (validator, kind, order, enabled) in
// gitleaks has none.
func runRulesExport(args []string, stdio IO) int {
	const name = "rules export"
	pos, err := parseFlags(args, nil, nil)
	if err != nil {
		return fail(stdio, name, 2, err)
	}
	if err := noExtraArgs(pos); err != nil {
		return fail(stdio, name, 2, err)
	}

	ctx := context.Background()
	e, err := openEngine(ctx, "")
	if err != nil {
		return fail(stdio, name, 1, err)
	}
	defer e.Close()

	rs, err := e.Store().RulesForProject(ctx, e.ProjectID())
	if err != nil {
		return fail(stdio, name, 1, err)
	}
	rs = slices.DeleteFunc(rs, func(r rules.Rule) bool { return r.Builtin })
	if err := rules.ExportTOML(stdio.Out, rs); err != nil {
		return fail(stdio, name, 1, err)
	}
	return 0
}

// runRulesTest checks a rule before saving it: the rule is assembled from the flags
// and is run against the raw sample, nothing is written to the database. The sample is exactly
// raw, without Guard: the pattern is checked, not the anonymizer, and foreign placeholders
// in it is ordinary text.
func runRulesTest(args []string, stdio IO) int {
	const name = "rules test"
	var pattern, kind, ruleType, sample string
	pos, err := parseFlags(args, map[string]*string{
		"--pattern": &pattern, "--kind": &kind, "--type": &ruleType, "--sample": &sample,
	}, nil)
	if err != nil {
		return fail(stdio, name, 2, err)
	}
	if err := sampleArgs(pos, sample); err != nil {
		return fail(stdio, name, 2, err)
	}
	if pattern == "" {
		return fail(stdio, name, 2, errors.New("--pattern is required"))
	}
	// A rule type and kind are required by Compile, but the spec declares for the command
	// only --pattern, --kind and --sample: the rest is taken by default.
	if kind == "" {
		kind = "regex"
	}
	if ruleType == "" {
		ruleType = "SECRET"
	}

	rule, err := rules.Compile(rules.Spec{
		ID: adHocRule, Type: ruleType, Kind: kind, Pattern: pattern, Enabled: true,
	})
	if err != nil {
		return fail(stdio, name, 1, err)
	}
	text, err := readSample(pos, sample, stdio.In)
	if err != nil {
		return fail(stdio, name, 1, err)
	}
	printSpans(stdio.Out, text, rules.Detect(text, []rules.Rule{rule}))
	return 0
}
