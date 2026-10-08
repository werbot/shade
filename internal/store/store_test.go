package store_test

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/werbot/shade/internal/store"
)

// ctx and s are shared by the tests of the package: project(t) creates in this store
// a separate project for every test, so the counters do not intersect.
var (
	ctx = context.Background()
	s   *store.Store
)

func TestMain(m *testing.M) {
	home, err := os.MkdirTemp("", "shade-store-")
	if err != nil {
		panic(err)
	}
	s, err = store.Open(home, testKey())
	if err != nil {
		panic(err)
	}
	code := m.Run()
	s.Close()
	os.RemoveAll(home)
	os.Exit(code)
}

// testKey is a deterministic encryption key for the tests of the package.
func testKey() []byte { return bytes.Repeat([]byte{7}, 32) }

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
	s1, err := store.Open(home, testKey())
	if err != nil {
		t.Fatal(err)
	}
	s1.Close()
	s2, err := store.Open(home, testKey())
	if err != nil {
		t.Fatalf("second open must not fail: %v", err)
	}
	s2.Close()
}

// openTemp opens a store in a fresh temporary directory and closes it
// when the test finishes.
func openTemp(t *testing.T) *store.Store {
	t.Helper()
	s, err := store.Open(t.TempDir(), testKey())
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

func TestEnsureHomeCreatesDirectory(t *testing.T) {
	home := filepath.Join(t.TempDir(), "nested", "shade")
	if err := store.EnsureHome(home); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(home)
	if err != nil {
		t.Fatal(err)
	}
	if !info.IsDir() {
		t.Fatal("not a directory")
	}
	// idempotency: a repeated call is not an error
	if err := store.EnsureHome(home); err != nil {
		t.Fatal(err)
	}
}

func TestOpenCreatesHomeDirectory(t *testing.T) {
	home := filepath.Join(t.TempDir(), "nested", "shade")
	s, err := store.Open(home, testKey())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if info, err := os.Stat(home); err != nil || !info.IsDir() {
		t.Fatalf("Open must create the directory %s: %v", home, err)
	}
}
