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
	db  *sql.DB
	key []byte // encryption key of values; the caller owns it
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

// EnsureHome creates the state directory if it is missing. It is idempotent.
// It is called before crypt.LoadOrCreateKey: that one does not create the directory.
func EnsureHome(home string) error {
	if err := os.MkdirAll(home, 0o755); err != nil {
		return fmt.Errorf("state directory %s: %w", home, err)
	}
	return nil
}

// dbName is the name of the database file in the state directory.
const dbName = "shade.db"

// DBPath returns the path of the database in the directory home.
//
// Exported for the same reason as crypt.KeyPath: the calling code must not
// repeat the name of the file, otherwise the copy would silently drift from the original.
func DBPath(home string) string { return filepath.Join(home, dbName) }

// Open creates the directory home, opens the shade.db database in it and applies the schema.
// The key is needed to encrypt the values of entities (see Allocate).
func Open(home string, key []byte) (*Store, error) {
	if err := EnsureHome(home); err != nil {
		return nil, err
	}
	path := DBPath(home)
	// _txlock=immediate: a write transaction takes the write lock at once, not on
	// the first INSERT. Otherwise two parallel sessions in WAL get
	// SQLITE_BUSY when a read transaction is upgraded to a write instead of waiting.
	dsn := "file:" + path + "?_txlock=immediate&_pragma=journal_mode(WAL)" +
		"&_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("opening the database %s: %w", path, err)
	}
	if err := migrate(db); err != nil {
		db.Close()
		return nil, err
	}
	return &Store{db: db, key: key}, nil
}

// Close closes the database.
func (s *Store) Close() error { return s.db.Close() }

// DB returns the database connection for queries of the layers above.
func (s *Store) DB() *sql.DB { return s.db }
