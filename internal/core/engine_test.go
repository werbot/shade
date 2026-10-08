package core_test

import (
	"context"
	"strings"
	"testing"

	"github.com/werbot/shade/internal/core"
	"github.com/werbot/shade/internal/placeholder"
)

// ctx is shared by the tests of the package: the engine keeps no state between calls,
// there is nothing to cancel in the tests.
var ctx = context.Background()

// secret is the value that the enabled builtin rule set catches (the rule
// assignment). Bare host and database names are not caught by the builtin rules at all
// — the user adds them with their own rule (Task 13), so fixtures with
// db.prod.local give not a single span and would test emptiness.
const secret = "AbCdEf0123456789"

// newEngine assembles an engine on a fresh SHADE_HOME and an isolated project.
// The project directory is a separate t.TempDir(): outside a git repository ProjectForPath
// takes the directory itself, and the placeholder counters do not overlap between tests.
func newEngine(t *testing.T) *core.Engine {
	t.Helper()
	t.Chdir(t.TempDir())
	e, err := core.New(ctx, t.TempDir(), "cli")
	if err != nil {
		t.Fatalf("core.New: %v", err)
	}
	t.Cleanup(func() { e.Close() })
	return e
}

func TestAnonymizeReplacesAndIsStable(t *testing.T) {
	e := newEngine(t)
	r1, err := e.Anonymize(ctx, "connect to db.prod.local with password="+secret+" now")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(r1.Text, secret) {
		t.Fatalf("the secret was not anonymized: %q", r1.Text)
	}
	if len(r1.Spans) != 1 {
		t.Fatalf("expected one span, got %+v", r1.Spans)
	}
	toks := placeholder.Find(r1.Text)
	if len(toks) != 1 || toks[0].Type != r1.Spans[0].Type {
		t.Fatalf("the placeholder is not where the span is: %q, spans %+v", r1.Text, r1.Spans)
	}

	r2, err := e.Anonymize(ctx, "again db.prod.local with password="+secret+" please")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(r2.Text, toks[0].Raw) {
		t.Fatalf("the placeholder is unstable: %q vs %q", r1.Text, r2.Text)
	}
}

func TestAnonymizeLeavesCleanTextByteIdentical(t *testing.T) {
	e := newEngine(t)
	for _, s := range []string{"", "обычный текст без секретов", "config = {'key': 'value'}"} {
		got, err := e.Anonymize(ctx, s)
		if err != nil {
			t.Fatal(err)
		}
		if got.Text != s {
			t.Fatalf("%q -> %q", s, got.Text)
		}
		if len(got.Spans) != 0 {
			t.Fatalf("%q -> spans %+v", s, got.Spans)
		}
	}
}

func TestAnonymizeKeepsExistingPlaceholder(t *testing.T) {
	e := newEngine(t)
	// Guard hides an already existing placeholder: the rules must not turn
	// <EMAIL_1> into <EMAIL_2>, and the same function must put it back.
	src := "хост db.prod.local, юзер <EMAIL_1> password=" + secret
	got, err := e.Anonymize(ctx, src)
	if err != nil {
		t.Fatal(err)
	}
	// Exact equality: the span addresses the text after Guard, and a substitution by
	// the offsets of the original text would give garbage here.
	if want := "хост db.prod.local, юзер <EMAIL_1> password=<SECRET_1>"; got.Text != want {
		t.Fatalf("\n want %q\n got  %q", want, got.Text)
	}
	if len(got.Unresolved) != 0 {
		t.Fatalf("a hidden placeholder is not a lost value: %+v", got.Unresolved)
	}
}

// A repeated value and a second secret in one text: it checks that the cursor
// of the anonymization does not slip on the second span, and the restore, going right
// to left, does not drift in offsets after the first substitution.
func TestAnonymizeAndRestoreSeveralSecrets(t *testing.T) {
	e := newEngine(t)
	src := "password=" + secret + " and password=ZzYyXx9876543210 and password=" + secret
	anon, err := e.Anonymize(ctx, src)
	if err != nil {
		t.Fatal(err)
	}
	want := "password=<SECRET_1> and password=<SECRET_2> and password=<SECRET_1>"
	if anon.Text != want {
		t.Fatalf("\n want %q\n got  %q", want, anon.Text)
	}

	back, err := e.Restore(ctx, anon.Text)
	if err != nil {
		t.Fatal(err)
	}
	if back.Text != src {
		t.Fatalf("round trip broken:\n want %q\n got  %q", src, back.Text)
	}
	if len(back.Unresolved) != 0 {
		t.Fatalf("got %+v", back.Unresolved)
	}
}

func TestRestoreRoundTrip(t *testing.T) {
	e := newEngine(t)
	src := "хост db.prod.local, юзер <EMAIL_1> password=" + secret
	anon, err := e.Anonymize(ctx, src)
	if err != nil {
		t.Fatal(err)
	}
	back, err := e.Restore(ctx, anon.Text)
	if err != nil {
		t.Fatal(err)
	}
	if back.Text != src {
		t.Fatalf("round trip broken:\n want %q\n got  %q", src, back.Text)
	}
	// this project never issued <EMAIL_1>: the restore leaves it as is
	// and reports it — silently handing over text with a hole would be worse.
	if len(back.Unresolved) != 1 || back.Unresolved[0].Type != "EMAIL" || back.Unresolved[0].N != 1 {
		t.Fatalf("got %+v", back.Unresolved)
	}
}

func TestRestoreDoesNotReanonymizeExistingPlaceholder(t *testing.T) {
	e := newEngine(t)
	src := "host db.prod.local password=" + secret
	anon, err := e.Anonymize(ctx, src)
	if err != nil {
		t.Fatal(err)
	}
	// re-anonymizing already anonymized text must not change the token
	again, err := e.Anonymize(ctx, anon.Text)
	if err != nil {
		t.Fatal(err)
	}
	if again.Text != anon.Text {
		t.Fatalf("%q -> %q", anon.Text, again.Text)
	}
	// and must not declare it unresolved
	if len(again.Unresolved) != 0 {
		t.Fatalf("unexpected unresolved: %+v", again.Unresolved)
	}
}

func TestRestoreReportsUnknownToken(t *testing.T) {
	e := newEngine(t)
	got, err := e.Restore(ctx, "see <HOST_99> there")
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Unresolved) != 1 || got.Unresolved[0].Type != "HOST" || got.Unresolved[0].N != 99 {
		t.Fatalf("got %+v", got.Unresolved)
	}
	if got.Text != "see <HOST_99> there" {
		t.Fatalf("an unknown token must stay in the text: %q", got.Text)
	}
}

func TestRestoreLeavesForeignPlaceholderAlone(t *testing.T) {
	e := newEngine(t)
	// the type is not from our set — not our placeholder
	got, err := e.Restore(ctx, "see <div_1> and <MyClass_2>")
	if err != nil {
		t.Fatal(err)
	}
	if got.Text != "see <div_1> and <MyClass_2>" {
		t.Fatalf("got %q", got.Text)
	}
	if len(got.Unresolved) != 0 {
		t.Fatalf("got %+v", got.Unresolved)
	}
}
