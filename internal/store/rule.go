package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/werbot/shade/internal/rules"
)

// RuleRow is a row of the rule list: without the compiled regexps. Kind, Pattern and
// SecretGroup are the definition the UI shows; CLI and MCP ignore them.
// ProjectID == nil means a global rule.
type RuleRow struct {
	Name        string
	Type        string
	Kind        string
	Pattern     string
	SecretGroup int
	Enabled     bool
	Builtin     bool
	ProjectID   *int64
}

// ruleFields is the projection of a rules row in the compile rules of Spec.
const ruleFields = `name, type, kind, pattern, secret_group, keywords,
	entropy_min, validator, allowlist, order_idx, enabled, builtin`

// RulesForProject collects the rules applicable to the project: the global ones plus
// the rules of the project itself. To avoid fetching them with two queries, the project ones are read
// the same SELECT — the global ones come first, and when folded into a map a same-named
// a project rule overrides the global one entirely.
//
// Disabled rules are filtered out here, and not in Detect: the builtin rule set
// seeds nine opt-in rules with enabled = false, and without the filter they
// would fire for everyone. Detect stays a pure function of the passed set
// — `shade rules test` rests on this, as it needs to run a rule before its
// enabling.
func (s *Store) RulesForProject(ctx context.Context, projectID int64) ([]rules.Rule, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+ruleFields+` FROM rules
		WHERE enabled = 1 AND (project_id IS NULL OR project_id = ?)
		ORDER BY project_id IS NOT NULL`, projectID)
	if err != nil {
		return nil, fmt.Errorf("rules of project %d: %w", projectID, err)
	}
	defer rows.Close()

	byName := map[string]rules.Rule{}
	for rows.Next() {
		spec, err := scanSpec(rows)
		if err != nil {
			return nil, fmt.Errorf("rules of project %d: %w", projectID, err)
		}
		r, err := rules.Compile(spec)
		if err != nil {
			return nil, err
		}
		byName[r.Name] = r
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("rules of project %d: %w", projectID, err)
	}
	return sortedRules(byName), nil
}

// ListRules returns rule rows for the CLI, the MCP rules_list tool, the UI and SeedBuiltin.
// projectID == nil means all the rules of the store, otherwise the global ones plus the rules of
// the given project.
func (s *Store) ListRules(ctx context.Context, projectID *int64) ([]RuleRow, error) {
	q := `SELECT name, type, kind, pattern, secret_group, enabled, builtin, project_id FROM rules`
	var args []any
	if projectID != nil {
		q += ` WHERE project_id IS NULL OR project_id = ?`
		args = append(args, *projectID)
	}
	q += ` ORDER BY project_id IS NOT NULL, name`

	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("listing rules: %w", err)
	}
	defer rows.Close()

	var out []RuleRow
	for rows.Next() {
		var (
			row RuleRow
			pid sql.NullInt64
		)
		if err := rows.Scan(&row.Name, &row.Type, &row.Kind, &row.Pattern, &row.SecretGroup,
			&row.Enabled, &row.Builtin, &pid); err != nil {
			return nil, fmt.Errorf("listing rules: %w", err)
		}
		if pid.Valid {
			id := pid.Int64
			row.ProjectID = &id
		}
		out = append(out, row)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("listing rules: %w", err)
	}
	return out, nil
}

// AddRule adds a rule to the scope of projectID (nil — global).
// Adding a rule with the same name in the same scope again updates
// the row: uniqueness is held by partial indexes, and a bare INSERT would fail on
// them an error that the user would not understand. The exception is a builtin rule:
// its row is not updated but rejected (see refuseBuiltin). A rule the engine cannot compile
// is rejected too (ErrInvalidRule).
func (s *Store) AddRule(ctx context.Context, projectID *int64, r rules.Spec) error {
	return s.writeRule(ctx, projectID, r, true)
}

// CreateRule inserts a rule and never overwrites: a taken name in the scope, including
// a name held by a builtin rule, is ErrRuleExists.
func (s *Store) CreateRule(ctx context.Context, projectID *int64, r rules.Spec) error {
	return s.writeRule(ctx, projectID, r, false)
}

// SetRuleEnabled enables and disables a rule. A builtin rule can only
// is edited: it cannot be deleted, but it can be disabled.
func (s *Store) SetRuleEnabled(ctx context.Context, projectID *int64, name string, enabled bool) error {
	cond, args := scopeCond(projectID)
	res, err := s.db.ExecContext(ctx,
		`UPDATE rules SET enabled = ?, updated_at = ? WHERE name = ? AND `+cond,
		append([]any{enabled, time.Now().Unix(), name}, args...)...)
	if err != nil {
		return fmt.Errorf("rule %q: %w", name, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("rule %q: %w", name, err)
	}
	// A typo in a rule name must not look like a successful edit.
	if n == 0 {
		return ruleError{fmt.Sprintf("rule %q not found", name), ErrRuleNotFound}
	}
	return nil
}

// RemoveRule deletes a rule of the current scope. Builtin rules are not deleted:
// they can only be disabled (SetRuleEnabled), otherwise the user would lose
// a rule with no way to get the original pattern back.
func (s *Store) RemoveRule(ctx context.Context, projectID *int64, name string) error {
	cond, args := scopeCond(projectID)
	res, err := s.db.ExecContext(ctx,
		`DELETE FROM rules WHERE name = ? AND builtin = 0 AND `+cond,
		append([]any{name}, args...)...)
	if err != nil {
		return fmt.Errorf("rule %q: %w", name, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("rule %q: %w", name, err)
	}
	if n > 0 {
		return nil
	}
	// Nothing is deleted: the row is either builtin or absent altogether. We distinguish
	// by builtin, so that the message explains what to do next. The name is substituted
	// into the query just like in the DELETE above: without it the placeholder name stays
	// unrelated, and instead of the cause the user gets a driver error.
	var builtin bool
	err = s.db.QueryRowContext(ctx,
		`SELECT builtin FROM rules WHERE name = ? AND `+cond,
		append([]any{name}, args...)...).Scan(&builtin)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return ruleError{fmt.Sprintf("rule %q not found", name), ErrRuleNotFound}
	case err != nil:
		return fmt.Errorf("rule %q: %w", name, err)
	case builtin:
		return ruleError{fmt.Sprintf("rule %q is builtin: it can be disabled, but not deleted", name), ErrBuiltinRule}
	default:
		// The branch is unreachable outside a race: the row can lose builtin = 0 between
		// DELETE and SELECT. A safety net so that such a "not deleted" looks
		// success; neither "not found" nor "builtin" would be true here.
		return fmt.Errorf("rule %q was not deleted", name)
	}
}

// scopeCond is the WHERE condition selecting the rules of one scope: nil means
// the global one (project_id IS NULL).
func scopeCond(projectID *int64) (string, []any) {
	if projectID == nil {
		return "project_id IS NULL", nil
	}
	return "project_id = ?", []any{*projectID}
}

// scanSpec reads a rules row into Spec.
func scanSpec(rows *sql.Rows) (rules.Spec, error) {
	var (
		spec            rules.Spec
		keywords, allow string
	)
	if err := rows.Scan(&spec.ID, &spec.Type, &spec.Kind, &spec.Pattern, &spec.SecretGroup,
		&keywords, &spec.EntropyMin, &spec.Validator, &allow, &spec.Order,
		&spec.Enabled, &spec.Builtin); err != nil {
		return rules.Spec{}, err
	}
	kw, err := parseJSONList(keywords)
	if err != nil {
		return rules.Spec{}, fmt.Errorf("rule %q: keywords: %w", spec.ID, err)
	}
	al, err := parseJSONList(allow)
	if err != nil {
		return rules.Spec{}, fmt.Errorf("rule %q: allowlist: %w", spec.ID, err)
	}
	spec.Keywords, spec.Allowlist = kw, al
	return spec, nil
}

// sortedRules returns the rules in the order of application: by OrderIdx, and for equal ones by
// by name, so that the slice does not depend on the iteration over the map.
func sortedRules(byName map[string]rules.Rule) []rules.Rule {
	out := make([]rules.Rule, 0, len(byName))
	for _, r := range byName {
		out = append(out, r)
	}
	slices.SortStableFunc(out, func(a, b rules.Rule) int {
		if a.OrderIdx != b.OrderIdx {
			return a.OrderIdx - b.OrderIdx
		}
		return strings.Compare(a.Name, b.Name)
	})
	return out
}
