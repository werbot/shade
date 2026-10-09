package store

import (
	"context"
	"fmt"
	"time"
)

// EntityInfo is a row of the project entity list. There is no field with the value here
// on purpose: a leak is impossible by type, and not by discipline — and the query is
// reads only these columns, value_enc never gets into it.
type EntityInfo struct {
	Placeholder string
	Type        string
	Hits        int
	FirstSeen   int64
	LastSeen    int64
}

// ListEntities returns up to limit entities of the project, starting with the freshest by
// last_seen_at.
func (s *Store) ListEntities(ctx context.Context, projectID int64, limit int) ([]EntityInfo, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT placeholder, type, hits, first_seen_at, last_seen_at
		   FROM entities WHERE project_id=? ORDER BY last_seen_at DESC, id DESC LIMIT ?`,
		projectID, limit)
	if err != nil {
		return nil, fmt.Errorf("listing entities of project %d: %w", projectID, err)
	}
	defer rows.Close()

	var out []EntityInfo
	for rows.Next() {
		var e EntityInfo
		if err := rows.Scan(&e.Placeholder, &e.Type, &e.Hits, &e.FirstSeen, &e.LastSeen); err != nil {
			return nil, fmt.Errorf("entity list row: %w", err)
		}
		out = append(out, e)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("listing entities of project %d: %w", projectID, err)
	}
	return out, nil
}

// PruneEntities deletes the entities of the project that were not seen for longer than olderThan, and
// returns the number of deleted rows. Zero is not an error: there is nothing to prune.
func (s *Store) PruneEntities(ctx context.Context, projectID int64, olderThan time.Duration) (int64, error) {
	res, err := s.db.ExecContext(ctx,
		`DELETE FROM entities WHERE project_id=? AND last_seen_at < ?`,
		projectID, time.Now().Add(-olderThan).Unix())
	if err != nil {
		return 0, fmt.Errorf("pruning entities of project %d: %w", projectID, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("number of deleted entities: %w", err)
	}
	return n, nil
}

// BumpRuleHit credits a hit of a rule for the day day (YYYY-MM-DD).
// The caller counts the day: the calendar is a property of whoever keeps the statistics, while with
// argument, so that a test can check it.
func (s *Store) BumpRuleHit(ctx context.Context, projectID int64, rule, day string) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO rule_hits(project_id, rule, day, count) VALUES(?, ?, ?, 1)
		 ON CONFLICT(project_id, rule, day) DO UPDATE SET count = count + 1`,
		projectID, rule, day)
	if err != nil {
		return fmt.Errorf("counter of rule %q for %s: %w", rule, day, err)
	}
	return nil
}

// RecordUnresolved leaves a trace of an unresolved placeholder in the journal.
// A token goes into detail, and not a value: Unresolved carries only tokens, and
// the value never appears in any column of the journal.
func (s *Store) RecordUnresolved(ctx context.Context, projectID int64, adapter, typ, token string) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO audit(ts, project_id, direction, adapter, rule, type, action, detail)
		 VALUES(?, ?, 'from_model', ?, NULL, ?, 'unresolved', ?)`,
		time.Now().Unix(), projectID, adapter, typ, token)
	if err != nil {
		return fmt.Errorf("writing to the journal %s: %w", token, err)
	}
	return nil
}

// RecordBlocked leaves a trace of a text the adapter refused to pass on. One row per
// type, and no detail: a prompt is blocked before the anonymization, so it has no
// tokens, and the values are never written to the journal at all.
func (s *Store) RecordBlocked(ctx context.Context, projectID int64, adapter, typ string) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO audit(ts, project_id, direction, adapter, rule, type, action, detail)
		 VALUES(?, ?, 'to_model', ?, NULL, ?, 'blocked', NULL)`,
		time.Now().Unix(), projectID, adapter, typ)
	if err != nil {
		return fmt.Errorf("writing a blocked %s to the journal: %w", typ, err)
	}
	return nil
}

// AuditEntry is a journal record. Empty rule/type/detail arrive empty
// strings: they are nullable in the schema, and the consumer has no way to tell NULL from empty.
type AuditEntry struct {
	TS        int64
	Direction string
	Adapter   string
	Rule      string
	Type      string
	Action    string
	Detail    string
}

// AuditFilter narrows the selection of the journal.
type AuditFilter struct {
	// Since is the lower time bound in unix seconds; 0 means no bound.
	Since int64
	// Unresolved selects only records about unresolved placeholders.
	Unresolved bool
}

// Audit returns the last limit records of the project, narrowed by the filter.
//
// Both conditions go into SQL before LIMIT: cutting in Go would return "the last N
// records among which the fitting ones survived", that is on a journal with a stream of
// foreign records the filter might return nothing at all.
func (s *Store) Audit(ctx context.Context, projectID int64, limit int, f AuditFilter) ([]AuditEntry, error) {
	q := `SELECT ts, direction, adapter, COALESCE(rule, ''), COALESCE(type, ''),
	             action, COALESCE(detail, '')
	        FROM audit WHERE project_id=?`
	args := []any{projectID}
	if f.Since > 0 {
		q += ` AND ts >= ?`
		args = append(args, f.Since)
	}
	if f.Unresolved {
		q += ` AND action = 'unresolved'`
	}
	q += ` ORDER BY ts DESC, id DESC LIMIT ?`
	args = append(args, limit)

	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("journal of project %d: %w", projectID, err)
	}
	defer rows.Close()

	var out []AuditEntry
	for rows.Next() {
		var e AuditEntry
		if err := rows.Scan(&e.TS, &e.Direction, &e.Adapter, &e.Rule, &e.Type, &e.Action, &e.Detail); err != nil {
			return nil, fmt.Errorf("journal row: %w", err)
		}
		out = append(out, e)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("journal of project %d: %w", projectID, err)
	}
	return out, nil
}
