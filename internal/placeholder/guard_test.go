package placeholder_test

import (
	"strings"
	"testing"

	"github.com/werbot/shade/internal/placeholder"
)

func TestGuardHidesExistingPlaceholders(t *testing.T) {
	masked, restore := placeholder.Guard("keep <EMAIL_1> safe")
	if strings.Contains(masked, "<EMAIL_1>") {
		t.Fatal("placeholder must be hidden from rules")
	}
	if got := restore(masked); got != "keep <EMAIL_1> safe" {
		t.Fatalf("restore round-trip broken: %q", got)
	}
}

func TestGuardIsStableWhenNothingToHide(t *testing.T) {
	masked, restore := placeholder.Guard("plain text")
	if masked != "plain text" || restore(masked) != "plain text" {
		t.Fatal("guard must be a no-op without placeholders")
	}
}

// TestGuardHidesDeformedPlaceholders: guard hides deformed tokens too —
// otherwise on a repeated run of the rules &lt;EMAIL_1&gt; will turn into &lt;EMAIL_2&gt;.
func TestGuardHidesDeformedPlaceholders(t *testing.T) {
	const text = "a &lt;EMAIL_1&gt; и < person_2 > b"
	masked, restore := placeholder.Guard(text)
	for _, leak := range []string{"EMAIL_1", "person_2"} {
		if strings.Contains(masked, leak) {
			t.Fatalf("token %q stayed visible to the rules: %q", leak, masked)
		}
	}
	if got := restore(masked); got != text {
		t.Fatalf("round-trip: %q, want %q", got, text)
	}
}

// TestGuardKeepsForgedSentinel: a forgery of the service substitution in the input must not
// must turn into a placeholder on output.
func TestGuardKeepsForgedSentinel(t *testing.T) {
	const text = "keep <EMAIL_1> and \x00S0\x00 literal"
	masked, restore := placeholder.Guard(text)
	if strings.Contains(masked, "<EMAIL_1>") {
		t.Fatal("placeholder must be hidden from rules")
	}
	if got := restore(masked); got != text {
		t.Fatalf("a forged sentinel was restored as a placeholder: %q", got)
	}
}

// TestGuardAllocatesNothingWhenNothingToHide: guard is called on every output
// tool, so on text without placeholders it must not allocate.
func TestGuardAllocatesNothingWhenNothingToHide(t *testing.T) {
	if n := testing.AllocsPerRun(200, func() {
		placeholder.Guard("обычный вывод инструмента: 1 < 2, см. <div_1>")
	}); n != 0 {
		t.Fatalf("allocations on text without placeholders: %v", n)
	}
}
