package store

import (
	"database/sql"
	_ "embed"
	"errors"
	"fmt"
	"time"

	"modernc.org/sqlite"
)

// schemaVersion is the schema version, written into meta when the schema is applied.
const schemaVersion = "1"

//go:embed schema.sql
var schema string

// SQLite codes for "the database is locked by another session": SQLITE_BUSY and SQLITE_LOCKED.
// Code() returns an extended code, so we compare the low byte.
const (
	sqliteBusy   = 5
	sqliteLocked = 6
)

// migrate applies the schema. It is idempotent: every object is created through
// IF NOT EXISTS, so a repeated run changes nothing.
//
// A lock is waited out by retrying, although the DSN carries busy_timeout(5000): the test from
// 16 parallel processes on a cold SHADE_HOME still caught SQLITE_BUSY on
// applying the schema — the first start of two sessions in one project. The retry here
// safe exactly because the schema is idempotent.
func migrate(db *sql.DB) error {
	if err := execBusy(db, schema); err != nil {
		return fmt.Errorf("applying the schema: %w", err)
	}
	if err := execBusy(db, `INSERT OR REPLACE INTO meta(key, value) VALUES('schema_version', ?)`, schemaVersion); err != nil {
		return fmt.Errorf("writing the schema version: %w", err)
	}
	return nil
}

// execBusy runs a query while waiting out a locked database: five attempts with pauses
// 10, 20, 40 and 80 ms. The bound is needed so that a real fault (a long
// writer, a read-only directory) is given as an error, and not turned
// into an endless wait.
func execBusy(db *sql.DB, query string, args ...any) error {
	delay := 10 * time.Millisecond
	for attempt := 0; ; attempt++ {
		_, err := db.Exec(query, args...)
		if err == nil || attempt == 4 || !busy(err) {
			return err
		}
		time.Sleep(delay)
		delay *= 2
	}
}

// busy recognises a lock by the driver code, and not by the text of the message.
func busy(err error) bool {
	var serr *sqlite.Error
	if !errors.As(err, &serr) {
		return false
	}
	switch serr.Code() & 0xff {
	case sqliteBusy, sqliteLocked:
		return true
	}
	return false
}
