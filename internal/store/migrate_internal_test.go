package store

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"
)

// TestMigrateRetriesWhileBusy — the neighbour holds the write lock for 50 ms, while busy_timeout of
// the migrator is set to 0: it is not SQLite that must wait but the retry in execBusy. The test
// discriminating: the probe below confirms that the lock really is held, so
// without the retry migrate would return SQLITE_BUSY.
func TestMigrateRetriesWhileBusy(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "shade.db")

	// The file straight into WAL: otherwise pragma journal_mode(WAL) would need the write lock at
	// the moment the connection is opened, and the test would measure the wrong window.
	init, err := sql.Open("sqlite", "file:"+path+"?_pragma=journal_mode(WAL)")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := init.ExecContext(ctx, `PRAGMA journal_mode=WAL`); err != nil {
		t.Fatal(err)
	}
	init.Close()

	holder, err := sql.Open("sqlite", "file:"+path+"?_txlock=immediate&_pragma=busy_timeout(0)")
	if err != nil {
		t.Fatal(err)
	}
	defer holder.Close()
	conn, err := holder.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if _, err := conn.ExecContext(ctx, `BEGIN IMMEDIATE`); err != nil {
		t.Fatal(err)
	}
	released := make(chan error, 1)
	go func() {
		time.Sleep(50 * time.Millisecond)
		_, err := conn.ExecContext(ctx, `COMMIT`)
		released <- err
	}()

	db, err := sql.Open("sqlite", "file:"+path+"?_txlock=immediate&_pragma=busy_timeout(0)")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	// The probe: without it the test would pass for a migrate without retries too — the neighbour could have
	// release the lock.
	if _, err := db.ExecContext(ctx, `CREATE TABLE probe(x)`); !busy(err) {
		t.Fatalf("the neighbour does not hold the write lock: %v", err)
	}

	if err := migrate(db); err != nil {
		t.Fatalf("migrate while the lock is taken: %v", err)
	}
	if err := <-released; err != nil {
		t.Fatalf("neighbour COMMIT: %v", err)
	}
	var n int
	if err := db.QueryRowContext(ctx,
		`SELECT count(*) FROM sqlite_master WHERE type='table' AND name='entities'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatal("the schema was not applied after the lock was released")
	}
}
