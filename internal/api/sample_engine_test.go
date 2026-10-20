package api_test

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"github.com/werbot/shade/internal/core"
)

// refuseEngine makes the fixture fail the test when the handler opens an engine.
func refuseEngine(t *testing.T, f *fixture) {
	t.Helper()
	f.open = func(context.Context) (*core.Engine, error) {
		t.Error("the engine was opened")
		return nil, errors.New("not to be opened")
	}
}

// A candidate is the definition and the text and nothing else, so it has no use for the store.
// The page asks at every pause of the typing, and an engine for each would open the database for
// nothing.
func TestCandidateModeDoesNotOpenTheEngine(t *testing.T) {
	f := newFixture(t)
	refuseEngine(t, f)

	postTest(t, f, "see ACME-123456", ticket)
	rec := f.do(http.MethodPost, "/api/test", testBody(t, "see ACME-123456", map[string]any{"pattern": "("}))
	wantError(t, rec, http.StatusBadRequest)
}

// The active rules need an engine, and the page asks at every pause of the typing, so a store
// left open would pile up one file handle per ask (see TestEngineIsClosedWhenTheRequestEnds).
func TestAllRulesModeClosesTheEngine(t *testing.T) {
	f := newFixture(t)
	var opened *core.Engine
	open := f.open
	f.open = func(ctx context.Context) (*core.Engine, error) {
		e, err := open(ctx)
		opened = e
		return e, err
	}
	postTest(t, f, "connect password="+secret, nil)
	if opened == nil {
		t.Fatal("the handler opened no engine")
	}
	if _, err := opened.Store().ListRules(t.Context(), nil); err == nil {
		t.Error("the engine still answers after the request: it was not closed")
	}
}

func TestAllRulesModeFailsWith500WhenTheEngineCannotBeOpened(t *testing.T) {
	f := newFixture(t)
	f.open = func(context.Context) (*core.Engine, error) { return nil, errors.New("store is locked") }
	wantError(t, f.do(http.MethodPost, "/api/test", testBody(t, "x", nil)), http.StatusInternalServerError)
}
