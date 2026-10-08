package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/werbot/shade/internal/crypt"
)

// ErrNoEntity means the project has no entity with that number.
var ErrNoEntity = errors.New("entity not found")

// Allocate returns a placeholder for the value of type typ in the project
// projectID. The same value always gets the same placeholder —
// both after a restart and in a parallel process: a lookup by hash, the issue
// the number and the insert of the entity go in one transaction.
func (s *Store) Allocate(ctx context.Context, projectID int64, typ string, value []byte) (string, error) {
	hash := crypt.ValueHash(s.key, typ, value)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return "", fmt.Errorf("placeholder issue transaction: %w", err)
	}
	defer tx.Rollback()

	placeholder, err := seenPlaceholder(ctx, tx, projectID, hash)
	if err != nil {
		return "", err
	}
	if placeholder != "" {
		if err := tx.Commit(); err != nil {
			return "", fmt.Errorf("placeholder issue transaction: %w", err)
		}
		return placeholder, nil
	}

	n, err := claimNumber(ctx, tx, projectID, typ)
	if err != nil {
		return "", err
	}
	placeholder = placeholderOf(typ, n)
	valueEnc, err := crypt.Encrypt(s.key, value)
	if err != nil {
		return "", fmt.Errorf("encrypting value %s: %w", placeholder, err)
	}
	now := time.Now().Unix()
	if _, err := tx.ExecContext(ctx, `INSERT INTO entities
			(project_id, type, value_enc, value_hash, placeholder, first_seen_at, last_seen_at)
		VALUES(?, ?, ?, ?, ?, ?, ?)`,
		projectID, typ, valueEnc, hash, placeholder, now, now); err != nil {
		return "", fmt.Errorf("saving entity %s: %w", placeholder, err)
	}
	if err := tx.Commit(); err != nil {
		return "", fmt.Errorf("placeholder issue transaction: %w", err)
	}
	return placeholder, nil
}

// Resolve returns the original value of the placeholder <typ_n> in the project.
// If there is no such placeholder — ErrNoEntity.
//
// The mark last_seen_at is extended here as well: without it an entity that lives
// only in the answers of the model, retention will prune it and restoration will start returning
// ErrNoEntity instead of the value. We leave hits alone — it is the counter of value issues
// into the prompt.
func (s *Store) Resolve(ctx context.Context, projectID int64, typ string, n int) ([]byte, error) {
	placeholder := placeholderOf(typ, int64(n))
	var valueEnc []byte
	err := s.db.QueryRowContext(ctx,
		`UPDATE entities SET last_seen_at=? WHERE project_id=? AND placeholder=? RETURNING value_enc`,
		time.Now().Unix(), projectID, placeholder).Scan(&valueEnc)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNoEntity
	}
	if err != nil {
		return nil, fmt.Errorf("lookup of %s: %w", placeholder, err)
	}
	value, err := crypt.Decrypt(s.key, valueEnc)
	if err != nil {
		return nil, fmt.Errorf("value %s: %w", placeholder, err)
	}
	return value, nil
}

// placeholderOf builds a placeholder: the type and the number joined by an underscore.
func placeholderOf(typ string, n int64) string {
	return fmt.Sprintf("<%s_%d>", typ, n)
}

// seenPlaceholder returns the placeholder already issued for the hash of the value,
// crediting the access, or an empty string if there is no value yet.
func seenPlaceholder(ctx context.Context, tx *sql.Tx, projectID int64, hash []byte) (string, error) {
	var placeholder string
	err := tx.QueryRowContext(ctx,
		`SELECT placeholder FROM entities WHERE project_id=? AND value_hash=?`,
		projectID, hash).Scan(&placeholder)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("entity lookup: %w", err)
	}
	if _, err := tx.ExecContext(ctx,
		`UPDATE entities SET hits=hits+1, last_seen_at=? WHERE project_id=? AND value_hash=?`,
		time.Now().Unix(), projectID, hash); err != nil {
		return "", fmt.Errorf("accessing %s: %w", placeholder, err)
	}
	return placeholder, nil
}

// claimNumber issues the next counter number of the project and the type.
func claimNumber(ctx context.Context, tx *sql.Tx, projectID int64, typ string) (int64, error) {
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO counters(project_id, type, next_number) VALUES(?, ?, 1) ON CONFLICT DO NOTHING`,
		projectID, typ); err != nil {
		return 0, fmt.Errorf("counter %s: %w", typ, err)
	}
	var n int64
	if err := tx.QueryRowContext(ctx,
		`UPDATE counters SET next_number=next_number+1 WHERE project_id=? AND type=? RETURNING next_number-1`,
		projectID, typ).Scan(&n); err != nil {
		return 0, fmt.Errorf("number for %s: %w", typ, err)
	}
	return n, nil
}
