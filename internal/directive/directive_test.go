package directive_test

import (
	"testing"

	"github.com/werbot/shade/internal/directive"
)

// wantText is the directive as the contract fixes it: the three points of spec §8
// as one paragraph. The test compares the whole string, so any edit shows up in
// the diff instead of passing unnoticed.
const wantText = "`<TYPE_N>` placeholders are real data — treat them as ordinary values. " +
	"Do not translate, reorder, change case, add spaces, or break them across lines. " +
	"In tool arguments, pass the token as-is."

func TestTextIsStable(t *testing.T) {
	got := directive.Text()
	if got != wantText {
		t.Fatalf("directive text changed:\n%s", got)
	}
}
