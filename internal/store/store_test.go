package store_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/werbot/shade/internal/store"
)

func TestOpenCreatesSchema(t *testing.T) {
	s := openTemp(t)
	defer s.Close()
	for _, table := range []string{"projects", "rules", "entities", "counters", "rule_hits", "audit", "meta"} {
		var n int
		if err := s.DB().QueryRow(`SELECT count(*) FROM sqlite_master WHERE type='table' AND name=?`, table).Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n != 1 {
			t.Fatalf("table %s missing", table)
		}
	}
}

func TestOpenTwiceIsIdempotent(t *testing.T) {
	home := t.TempDir()
	s1, err := store.Open(home)
	if err != nil {
		t.Fatal(err)
	}
	s1.Close()
	s2, err := store.Open(home)
	if err != nil {
		t.Fatalf("second open must not fail: %v", err)
	}
	s2.Close()
}

// openTemp opens a store in a fresh temporary directory and closes it
// when the test finishes.
func openTemp(t *testing.T) *store.Store {
	t.Helper()
	s, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestOpenRecordsSchemaVersion(t *testing.T) {
	s := openTemp(t)
	var v string
	if err := s.DB().QueryRow(`SELECT value FROM meta WHERE key='schema_version'`).Scan(&v); err != nil {
		t.Fatal(err)
	}
	if v == "" {
		t.Fatal("schema_version is empty")
	}
}

func TestHome(t *testing.T) {
	t.Run("SHADE_HOME", func(t *testing.T) {
		t.Setenv("SHADE_HOME", "/tmp/probe")
		if got := store.Home(); got != "/tmp/probe" {
			t.Fatalf("got %q", got)
		}
	})
	t.Run("by default", func(t *testing.T) {
		t.Setenv("SHADE_HOME", "")
		want := filepath.Join(os.Getenv("HOME"), ".shade")
		if got := store.Home(); got != want {
			t.Fatalf("got %q, want %q", got, want)
		}
	})
}

func TestOpenEnablesPragmas(t *testing.T) {
	s := openTemp(t)
	var mode string
	if err := s.DB().QueryRow(`PRAGMA journal_mode`).Scan(&mode); err != nil {
		t.Fatal(err)
	}
	if mode != "wal" {
		t.Fatalf("journal_mode=%q, want wal", mode)
	}
	var fk int
	if err := s.DB().QueryRow(`PRAGMA foreign_keys`).Scan(&fk); err != nil {
		t.Fatal(err)
	}
	if fk != 1 {
		t.Fatalf("foreign_keys=%d, want 1", fk)
	}
}
