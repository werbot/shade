// Package store is the persistent storage of shade: SQLite in the SHADE_HOME directory.
package store

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"

	_ "modernc.org/sqlite" // the driver registers itself under the name "sqlite"
)

// Store is an open database of shade.
type Store struct {
	db *sql.DB
}

// Home returns the state directory of shade: SHADE_HOME, otherwise ~/.shade.
// An empty string means that the home directory is not defined.
func Home() string {
	if dir := os.Getenv("SHADE_HOME"); dir != "" {
		return dir
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".shade")
}

// Open creates the directory home, opens the shade.db database in it and applies the schema.
func Open(home string) (*Store, error) {
	if err := os.MkdirAll(home, 0o755); err != nil {
		return nil, fmt.Errorf("state directory %s: %w", home, err)
	}
	path := filepath.Join(home, "shade.db")
	dsn := "file:" + path + "?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("opening the database %s: %w", path, err)
	}
	if err := migrate(db); err != nil {
		db.Close()
		return nil, err
	}
	return &Store{db: db}, nil
}

// Close closes the database.
func (s *Store) Close() error { return s.db.Close() }

// DB returns the database connection for queries of the layers above.
func (s *Store) DB() *sql.DB { return s.db }
