package core_test

import "testing"

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
}

// The order of Unresolved must match the order of the tokens in the text: the collection goes
// right to left, so it is reversed.
func TestRestoreReportsUnresolvedInTextOrder(t *testing.T) {
	e := newEngine(t)
	got, err := e.Restore(ctx, "see <HOST_99>, <EMAIL_7> and <KEY_3>")
	if err != nil {
		t.Fatal(err)
	}
	want := []struct {
		Type string
		N    int
	}{{"HOST", 99}, {"EMAIL", 7}, {"KEY", 3}}
	if len(got.Unresolved) != len(want) {
		t.Fatalf("got %+v", got.Unresolved)
	}
	for i, w := range want {
		if got.Unresolved[i].Type != w.Type || got.Unresolved[i].N != w.N {
			t.Fatalf("Unresolved[%d] = %+v, want %s %d", i, got.Unresolved[i], w.Type, w.N)
		}
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
