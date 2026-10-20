package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/werbot/shade/internal/rules"
)

// ruleColumns and ruleValues are the columns writeRule manages. package_id
// is not filled: rule packages will appear in phase 3.
const (
	ruleColumns = `project_id, name, type, kind, pattern, secret_group, keywords,
		entropy_min, validator, allowlist, order_idx, enabled, builtin, updated_at`
	ruleValues = `?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?`
	// ruleAssign does not touch name and project_id: they are the key of the row, and updating
	// must not move a rule between scopes.
	ruleAssign = `type=excluded.type, kind=excluded.kind, pattern=excluded.pattern,
		secret_group=excluded.secret_group, keywords=excluded.keywords,
		entropy_min=excluded.entropy_min, validator=excluded.validator,
		allowlist=excluded.allowlist, order_idx=excluded.order_idx,
		enabled=excluded.enabled, builtin=excluded.builtin,
		updated_at=excluded.updated_at`
)

// writeRule is the shared body of AddRule (upsert: a taken name is rewritten) and
// CreateRule (a taken name is ErrRuleExists).
func (s *Store) writeRule(ctx context.Context, projectID *int64, r rules.Spec, upsert bool) error {
	if r.ID == "" {
		return errors.New("rule name is not set")
	}
	// Compile before writing. The reverse order would leave a row in the database on
	// which RulesForProject fails entirely: one bad row kills the whole
	// project rule set, and it is only fixed by editing the database by hand.
	if _, err := rules.Compile(r); err != nil {
		return ruleError{err.Error(), ErrInvalidRule}
	}
	keywords, err := jsonList(r.Keywords)
	if err != nil {
		return fmt.Errorf("rule %q: keywords: %w", r.ID, err)
	}
	allowlist, err := jsonList(r.Allowlist)
	if err != nil {
		return fmt.Errorf("rule %q: allowlist: %w", r.ID, err)
	}
	if upsert {
		if err := s.refuseBuiltin(ctx, projectID, r.ID); err != nil {
			return err
		}
	}
	// The conflict target must match the partial index together with its
	// WHERE — otherwise SQLite answers "ON CONFLICT clause does not match any
	// PRIMARY KEY or UNIQUE constraint", and the scopes need different targets.
	target := `ON CONFLICT(name) WHERE project_id IS NULL`
	if projectID != nil {
		target = `ON CONFLICT(project_id, name) WHERE project_id IS NOT NULL`
	}
	action := `DO UPDATE SET ` + ruleAssign
	if !upsert {
		action = `DO NOTHING`
	}
	q := `INSERT INTO rules(` + ruleColumns + `) VALUES(` + ruleValues + `) ` + target + ` ` + action

	// nil in the argument means NULL, that is a global rule.
	res, err := s.db.ExecContext(ctx, q, projectID, r.ID, r.Type, r.Kind,
		r.Pattern, r.SecretGroup, keywords, r.EntropyMin, r.Validator, allowlist,
		r.Order, r.Enabled, r.Builtin, time.Now().Unix())
	if err != nil {
		return fmt.Errorf("rule %q: %w", r.ID, err)
	}
	if upsert {
		return nil
	}
	// DO NOTHING is not an error for SQLite: a taken name shows only as no row inserted.
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("rule %q: %w", r.ID, err)
	}
	if n == 0 {
		return ruleError{fmt.Sprintf("rule %q already exists in this scope", r.ID), ErrRuleExists}
	}
	return nil
}

// refuseBuiltin rejects an upsert over a builtin row. The upsert spares no builtin row:
// ruleAssign carries over pattern, type, order_idx and resets builtin, while SeedBuiltin
// no longer has the original would return — it skips taken names. A rule that used to
// catch a secret dies for good, and the value leaves open. The check lives in the write
// path, and not into the CLI: every caller meets in AddRule — add, import and SeedBuiltin.
func (s *Store) refuseBuiltin(ctx context.Context, projectID *int64, name string) error {
	var builtin bool
	cond, args := scopeCond(projectID)
	err := s.db.QueryRowContext(ctx,
		`SELECT builtin FROM rules WHERE name = ? AND `+cond,
		append([]any{name}, args...)...).Scan(&builtin)
	switch {
	case err == nil && builtin:
		return ruleError{fmt.Sprintf("rule %q is builtin: it can be disabled, but not replaced — pick another name", name), ErrBuiltinRule}
	case err != nil && !errors.Is(err, sql.ErrNoRows):
		return fmt.Errorf("rule %q: %w", name, err)
	}
	return nil
}
