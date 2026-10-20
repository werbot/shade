package api_test

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"github.com/werbot/shade/internal/core"
)

// A candidate is the definition and the text and nothing else, so it has no use for the store.
// The page asks at every pause of the typing, and an engine for each would open the database for
// nothing.
func TestCandidateModeDoesNotOpenTheEngine(t *testing.T) {
	f := newFixture(t)
	refuseEngine(t, f)

	postTest(t, f, "see ACME-123456", ticket)
	for _, pattern := range []string{"(", ""} {
		rec := f.do(http.MethodPost, "/api/test", testBody(t, "see ACME-123456", map[string]any{"pattern": pattern}))
		wantError(t, rec, http.StatusBadRequest)
	}
}

func TestAllRulesModeFailsWith500WhenTheEngineCannotBeOpened(t *testing.T) {
	f := newFixture(t)
	f.open = func(context.Context) (*core.Engine, error) { return nil, errors.New("store is locked") }
	wantError(t, f.do(http.MethodPost, "/api/test", testBody(t, "x", nil)), http.StatusInternalServerError)
}
