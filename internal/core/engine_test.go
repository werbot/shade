package core_test

import (
	"context"
	"strings"
	"testing"

	"github.com/werbot/shade/internal/core"
	"github.com/werbot/shade/internal/crypt"
	"github.com/werbot/shade/internal/placeholder"
	"github.com/werbot/shade/internal/rules"
	"github.com/werbot/shade/internal/store"
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

// newEngineWithRule brings up an engine on a fresh SHADE_HOME with an additional
// rule in the global scope. The property "the span is clipped at the substitution" must not
// depend on the builtin rule set: a user rule (Task 13) with a
// class that lets NUL through breaks it in exactly the same way.
func newEngineWithRule(t *testing.T, spec rules.Spec) *core.Engine {
	t.Helper()
	home := t.TempDir()
	if err := store.EnsureHome(home); err != nil {
		t.Fatal(err)
	}
	key, err := crypt.LoadOrCreateKey(home)
	if err != nil {
		t.Fatal(err)
	}
	s, err := store.Open(home, key)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.AddRule(ctx, nil, spec); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	t.Chdir(t.TempDir())
	e, err := core.New(ctx, home, "cli")
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

// A rule's span can cover a Guard substitution: NUL is allowed where `<`
// is forbidden (the value class of py_repr starts with [^"'\\\n$%<{\[]), and
// random_enough lets the substitution through because of a digit. Without clipping the substitution
// would disappear from the text, restore would not find it, and the round trip would silently fall apart
// — with an empty Unresolved.
func TestAnonymizeKeepsHiddenPlaceholderUnderRuleSpan(t *testing.T) {
	e := newEngine(t)
	for _, src := range []string{
		`{"password": "<EMAIL_1>"}`,
		`password="<EMAIL_1>"`,
		`password: '<SECRET_1>'`,
		`password=<EMAIL_1>`,
		`where "<EMAIL_1>" = password`,
		`api_key = "<KEY_1>"`,
	} {
		anon, err := e.Anonymize(ctx, src)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(anon.Text, "\x00") {
			t.Fatalf("%q: the substitution leaked into the result: %q", src, anon.Text)
		}
		back, err := e.Restore(ctx, anon.Text)
		if err != nil {
			t.Fatal(err)
		}
		if back.Text != src {
			t.Fatalf("round trip broken:\n src  %q\n anon %q\n back %q", src, anon.Text, back.Text)
		}
		if len(back.Unresolved) != 1 {
			t.Fatalf("%q: unresolved %+v", src, back.Unresolved)
		}
	}
}

// Clipping does not throw the span away entirely: a secret right next to the substitution must
// be masked, and the substitution must survive.
func TestAnonymizeClipsSpanAroundHiddenPlaceholder(t *testing.T) {
	e := newEngine(t)
	src := `password="<EMAIL_1>` + secret + `"`
	anon, err := e.Anonymize(ctx, src)
	if err != nil {
		t.Fatal(err)
	}
	want := `password="<EMAIL_1><SECRET_1>"`
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
}

// One span split into two parts around a substitution: each part
// is allocated separately, both sides of the secret are masked.
func TestAnonymizeAllocatesEachPartOfSplitSpan(t *testing.T) {
	e := newEngineWithRule(t, rules.Spec{
		ID: "nul_class", Type: "SECRET", Kind: "regex",
		Pattern: `x=(\S+)`, SecretGroup: 1, Order: 1, Enabled: true,
	})
	src := "x=AbCdEf0123456789<EMAIL_1>ZzYyXx9876543210"
	anon, err := e.Anonymize(ctx, src)
	if err != nil {
		t.Fatal(err)
	}
	want := "x=<SECRET_1><EMAIL_1><SECRET_2>"
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
}
