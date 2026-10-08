package store_test

import (
	"errors"
	"sync"
	"testing"

	"github.com/werbot/shade/internal/store"
)

func TestAllocateIsStableForSameValue(t *testing.T) {
	p := project(t)
	a, err := s.Allocate(ctx, p.ID, "HOST", []byte("db.prod.local"))
	if err != nil {
		t.Fatal(err)
	}
	b, err := s.Allocate(ctx, p.ID, "HOST", []byte("db.prod.local"))
	if err != nil {
		t.Fatal(err)
	}
	if a != b {
		t.Fatalf("same value must keep one placeholder: %q vs %q", a, b)
	}
	if a != "<HOST_1>" {
		t.Fatalf("want <HOST_1>, got %q", a)
	}
}

func TestAllocateDistinctValuesGetDistinctPlaceholders(t *testing.T) {
	p := project(t)
	a, err := s.Allocate(ctx, p.ID, "HOST", []byte("a.local"))
	if err != nil {
		t.Fatal(err)
	}
	b, err := s.Allocate(ctx, p.ID, "HOST", []byte("b.local"))
	if err != nil {
		t.Fatal(err)
	}
	if a == b {
		t.Fatal("distinct values must not share a placeholder")
	}
}

func TestAllocateIsScopedPerType(t *testing.T) {
	p := project(t)
	h, err := s.Allocate(ctx, p.ID, "HOST", []byte("value-x"))
	if err != nil {
		t.Fatal(err)
	}
	u, err := s.Allocate(ctx, p.ID, "USER", []byte("value-x"))
	if err != nil {
		t.Fatal(err)
	}
	if h == u {
		t.Fatal("same text under different types must not collide")
	}
}

func TestAllocateSurvivesReopen(t *testing.T) {
	home := t.TempDir()
	key := testKey()
	s1, err := store.Open(home, key)
	if err != nil {
		t.Fatal(err)
	}
	p, err := s1.ProjectForPath(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s1.Allocate(ctx, p.ID, "HOST", []byte("db.prod.local")); err != nil {
		t.Fatal(err)
	}
	if err := s1.Close(); err != nil {
		t.Fatal(err)
	}

	s2, err := store.Open(home, key)
	if err != nil {
		t.Fatal(err)
	}
	defer s2.Close()
	again, err := s2.Allocate(ctx, p.ID, "HOST", []byte("db.prod.local"))
	if err != nil {
		t.Fatal(err)
	}
	if again != "<HOST_1>" {
		t.Fatalf("placeholder must survive restart, got %q", again)
	}
}

func TestAllocateIsStableUnderConcurrency(t *testing.T) {
	p := project(t)
	const workers = 8
	got := make([]string, workers)
	errs := make([]error, workers)
	var wg sync.WaitGroup
	for i := range workers {
		wg.Go(func() {
			got[i], errs[i] = s.Allocate(ctx, p.ID, "HOST", []byte("db.prod.local"))
		})
	}
	wg.Wait()
	for i := range workers {
		if errs[i] != nil {
			t.Fatalf("worker %d: %v", i, errs[i])
		}
		if got[i] != "<HOST_1>" {
			t.Fatalf("worker %d: got %q, want <HOST_1>", i, got[i])
		}
	}
}

func TestResolveReturnsOriginal(t *testing.T) {
	p := project(t)
	if _, err := s.Allocate(ctx, p.ID, "HOST", []byte("db.prod.local")); err != nil {
		t.Fatal(err)
	}
	got, err := s.Resolve(ctx, p.ID, "HOST", 1)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "db.prod.local" {
		t.Fatalf("got %q", got)
	}
}

func TestResolveProlongsLastSeenAtButNotHits(t *testing.T) {
	p := project(t)
	if _, err := s.Allocate(ctx, p.ID, "HOST", []byte("db.prod.local")); err != nil {
		t.Fatal(err)
	}
	// we roll the marks back into the past, as if the entity had not been touched for ages
	if _, err := s.DB().Exec(
		`UPDATE entities SET last_seen_at=1, hits=1 WHERE project_id=?`, p.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Resolve(ctx, p.ID, "HOST", 1); err != nil {
		t.Fatal(err)
	}
	var lastSeenAt, hits int64
	if err := s.DB().QueryRow(
		`SELECT last_seen_at, hits FROM entities WHERE project_id=?`, p.ID).Scan(&lastSeenAt, &hits); err != nil {
		t.Fatal(err)
	}
	if lastSeenAt <= 1 {
		t.Fatalf("Resolve must extend last_seen_at, otherwise retention will prune a live entity: %d", lastSeenAt)
	}
	if hits != 1 {
		t.Fatalf("Resolve must not change hits — that is the counter of issues into the prompt: %d", hits)
	}
}

func TestResolveUnknownNumber(t *testing.T) {
	p := project(t)
	if _, err := s.Resolve(ctx, p.ID, "HOST", 99); !errors.Is(err, store.ErrNoEntity) {
		t.Fatalf("want ErrNoEntity, got %v", err)
	}
}
