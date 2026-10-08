package core_test

import (
	"strings"
	"testing"
)

// Scan is a debug path: it must find the same things as Anonymize and write nothing
// to the store. The side effect here is exactly the subject of the check: without
// it, an implementation on top of a single Anonymize would pass the test.
func TestScanFindsWithoutAllocating(t *testing.T) {
	e := newEngine(t)
	text := "password=" + secret

	clean, spans, err := e.Scan(text, "")
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if len(spans) != 1 || spans[0].Type != "SECRET" || spans[0].Rule != "assignment" {
		t.Fatalf("spans: %+v", spans)
	}
	if got := clean[spans[0].Start:spans[0].End]; got != secret {
		t.Fatalf("fragment %q at offsets %d-%d", got, spans[0].Start, spans[0].End)
	}

	// The value got no number during the run: Anonymize on the same text
	// must issue the first placeholder of its own type.
	res, err := e.Anonymize(ctx, text)
	if err != nil {
		t.Fatalf("Anonymize: %v", err)
	}
	if !strings.Contains(res.Text, "<SECRET_1>") {
		t.Fatalf("Scan created an entity in the store: %q", res.Text)
	}
}

// The span offsets lie in the coordinates of the text after Guard: a foreign placeholder
// is longer than the service substitution, and returning the restored one instead of this text
// would mean handing over coordinates that do not fit it.
func TestScanOffsetsStayInGuardedCoordinates(t *testing.T) {
	e := newEngine(t)
	src := "хост <HOST_99> password=" + secret

	clean, spans, err := e.Scan(src, "")
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if len(spans) != 1 {
		t.Fatalf("spans: %+v", spans)
	}
	if got := clean[spans[0].Start:spans[0].End]; got != secret {
		t.Fatalf("fragment %q at offsets %d-%d", got, spans[0].Start, spans[0].End)
	}
}

// only narrows the run to the named rule of the active set; if the name is not in the set
// it is an error, not an empty result: a typo and "the rule found nothing" are
// different things.
func TestScanHonoursOnly(t *testing.T) {
	e := newEngine(t)
	text := "password=" + secret

	if _, spans, err := e.Scan(text, "assignment"); err != nil || len(spans) != 1 {
		t.Fatalf("only=assignment: %v, spans %+v", err, spans)
	}
	if _, spans, err := e.Scan(text, "ssh_key"); err != nil || len(spans) != 0 {
		t.Fatalf("only=ssh_key: %v, spans %+v", err, spans)
	}
	// A disabled opt-in rule is not part of the active set, so for Scan
	// it is simply not there: the set here is the same as the anonymizer has.
	if _, _, err := e.Scan(text, "email"); err == nil {
		t.Fatal("a disabled rule must not be visible to Scan")
	}
	if _, _, err := e.Scan(text, "nope"); err == nil || !strings.Contains(err.Error(), "nope") {
		t.Fatalf("unknown rule: %v", err)
	}
}
