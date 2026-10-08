package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"maps"
	"slices"
	"strconv"

	"github.com/werbot/shade/internal/core"
	"github.com/werbot/shade/internal/rules"
)

func init() {
	Register(Command{
		Name: "rules",
		Help: "view and edit anonymization rules",
		Run:  runRules,
	})
}

// rulesSub holds the `shade rules` subcommands. A table, not a switch: the commands live in
// two files (editing and I/O), and the registry has to be assembled in one place.
var rulesSub = map[string]struct {
	help string
	run  func([]string, IO) int
}{
	"list":    {"list the rules of the scope", runRulesList},
	"add":     {"add a rule", runRulesAdd},
	"rm":      {"delete a rule", runRulesRm},
	"enable":  {"enable a rule", runRulesEnable},
	"disable": {"disable a rule", runRulesDisable},
	"import":  {"import rules from a gitleaks TOML", runRulesImport},
	"export":  {"print the rules as TOML", runRulesExport},
	"test":    {"check a rule before saving it", runRulesTest},
}

func runRules(args []string, stdio IO) int {
	if len(args) == 0 {
		printRulesUsage(stdio.Err)
		return 2
	}
	sub, ok := rulesSub[args[0]]
	if !ok {
		fmt.Fprintf(stdio.Err, "rules: unknown subcommand %q\n", args[0])
		printRulesUsage(stdio.Err)
		return 2
	}
	return sub.run(args[1:], stdio)
}

func printRulesUsage(w io.Writer) {
	fmt.Fprintln(w, "Usage: shade rules <subcommand> [arguments]")
	fmt.Fprintln(w, "Subcommands:")
	for _, name := range slices.Sorted(maps.Keys(rulesSub)) {
		fmt.Fprintf(w, "  %s — %s\n", name, rulesSub[name].help)
	}
}

// scope turns --global into a rule scope: nil is the global row
// (project_id IS NULL), otherwise the project of the current directory.
func scope(e *core.Engine, global bool) *int64 {
	if global {
		return nil
	}
	id := e.ProjectID()
	return &id
}

func runRulesList(args []string, stdio IO) int {
	const name = "rules list"
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

	id := e.ProjectID()
	rows, err := e.Store().ListRules(ctx, &id)
	if err != nil {
		return fail(stdio, name, 1, err)
	}
	fmt.Fprintln(stdio.Out, "name\ttype\tenabled\tbuiltin\tproject")
	for _, row := range rows {
		project := "—"
		if row.ProjectID != nil {
			project = strconv.FormatInt(*row.ProjectID, 10)
		}
		fmt.Fprintf(stdio.Out, "%s\t%s\t%s\t%s\t%s\n",
			row.Name, row.Type, yesNo(row.Enabled), yesNo(row.Builtin), project)
	}
	return 0
}

// yesNo prints a flag in words: a human reads the output, and a column of true/false
// cannot be told apart from a rule name at a glance.
func yesNo(v bool) string {
	if v {
		return "yes"
	}
	return "no"
}

func runRulesAdd(args []string, stdio IO) int {
	const name = "rules add"
	var ruleName, ruleType, pattern, kind, group string
	var global bool
	pos, err := parseFlags(args, map[string]*string{
		"--name": &ruleName, "--type": &ruleType, "--pattern": &pattern,
		"--kind": &kind, "--secret-group": &group,
	}, map[string]*bool{"--global": &global})
	if err != nil {
		return fail(stdio, name, 2, err)
	}
	if err := noExtraArgs(pos); err != nil {
		return fail(stdio, name, 2, err)
	}
	// Required flags are checked one by one and in declaration order: the message
	// has to name what is missing, not a consequence in another field.
	if ruleName == "" {
		return fail(stdio, name, 2, errors.New("--name is required"))
	}
	if ruleType == "" {
		return fail(stdio, name, 2, errors.New("--type is required"))
	}
	if pattern == "" {
		return fail(stdio, name, 2, errors.New("--pattern is required"))
	}
	if kind == "" {
		kind = "regex"
	}
	secretGroup := 0
	if group != "" {
		n, err := strconv.Atoi(group)
		// Code boundary: a statically wrong argument is 2, while one that depends on
		// the pattern (`--secret-group 5` with zero groups) stays 1 — it is caught by
		// Compile, because the pattern itself decides. A negative value
		// checked here, not in Compile: it is wrong regardless of the pattern.
		if err != nil || n < 0 {
			return fail(stdio, name, 2, fmt.Errorf("--secret-group: %q — expected a non-negative integer", group))
		}
		secretGroup = n
	}

	// Order 0: builtin rules start at 10, so a user rule
	// applies earlier than the builtin one and its type wins on overlap.
	spec := rules.Spec{
		ID: ruleName, Type: ruleType, Kind: kind, Pattern: pattern,
		SecretGroup: secretGroup, Enabled: true,
	}
	// Compile before writing. The reverse order would leave a row in the database on
	// which RulesForProject fails entirely: one bad row kills the whole
	// project rule set, and it is only fixed by editing the database by hand.
	if _, err := rules.Compile(spec); err != nil {
		return fail(stdio, name, 1, err)
	}

	ctx := context.Background()
	e, err := openEngine(ctx, "")
	if err != nil {
		return fail(stdio, name, 1, err)
	}
	defer e.Close()
	if err := e.Store().AddRule(ctx, scope(e, global), spec); err != nil {
		return fail(stdio, name, 1, err)
	}
	fmt.Fprintf(stdio.Out, "rule %q added\n", ruleName)
	return 0
}

func runRulesRm(args []string, stdio IO) int {
	const name = "rules rm"
	ruleName, global, err := parseNameScope(args)
	if err != nil {
		return fail(stdio, name, 2, err)
	}
	ctx := context.Background()
	e, err := openEngine(ctx, "")
	if err != nil {
		return fail(stdio, name, 1, err)
	}
	defer e.Close()
	// A refusal for a builtin rule is handed out as is: a builtin rule
	// can be disabled, but not deleted, and the command has to say so.
	if err := e.Store().RemoveRule(ctx, scope(e, global), ruleName); err != nil {
		return fail(stdio, name, 1, err)
	}
	fmt.Fprintf(stdio.Out, "rule %q deleted\n", ruleName)
	return 0
}

func runRulesEnable(args []string, stdio IO) int  { return runRulesToggle(args, stdio, true) }
func runRulesDisable(args []string, stdio IO) int { return runRulesToggle(args, stdio, false) }

// runRulesToggle is the shared body of enable and disable: they differ by one
// a store argument.
func runRulesToggle(args []string, stdio IO, enabled bool) int {
	name, verb := "rules disable", "disabled"
	if enabled {
		name, verb = "rules enable", "enabled"
	}
	ruleName, global, err := parseNameScope(args)
	if err != nil {
		return fail(stdio, name, 2, err)
	}
	ctx := context.Background()
	e, err := openEngine(ctx, "")
	if err != nil {
		return fail(stdio, name, 1, err)
	}
	defer e.Close()
	if err := e.Store().SetRuleEnabled(ctx, scope(e, global), ruleName, enabled); err != nil {
		return fail(stdio, name, 1, err)
	}
	fmt.Fprintf(stdio.Out, "rule %q %s\n", ruleName, verb)
	return 0
}

// parseNameScope parses the common flags of commands that work with a single rule:
// --name (required) and --global. rm/enable/disable share one flag set, so the
// is a single parse.
func parseNameScope(args []string) (name string, global bool, err error) {
	pos, err := parseFlags(args, map[string]*string{"--name": &name},
		map[string]*bool{"--global": &global})
	if err != nil {
		return "", false, err
	}
	if err := noExtraArgs(pos); err != nil {
		return "", false, err
	}
	if name == "" {
		return "", false, errors.New("--name is required")
	}
	return name, global, nil
}
