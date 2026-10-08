package store

import (
	"database/sql"
	_ "embed"
	"fmt"
)

// schemaVersion is the schema version, written into meta when the schema is applied.
const schemaVersion = "1"

//go:embed schema.sql
var schema string

// migrate applies the schema. It is idempotent: every object is created through
// IF NOT EXISTS, so a repeated run changes nothing.
func migrate(db *sql.DB) error {
	if _, err := db.Exec(schema); err != nil {
		return fmt.Errorf("applying the schema: %w", err)
	}
	if _, err := db.Exec(`INSERT OR REPLACE INTO meta(key, value) VALUES('schema_version', ?)`, schemaVersion); err != nil {
		return fmt.Errorf("writing the schema version: %w", err)
	}
	return nil
}
