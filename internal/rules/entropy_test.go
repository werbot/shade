package rules_test

import (
	"testing"

	"github.com/werbot/shade/internal/rules"
)

func TestShannon(t *testing.T) {
	if got := rules.Shannon("aaaaaaaa"); got > 0.01 {
		t.Fatalf("got %v", got)
	}
	if got := rules.Shannon("Xk7pQ2mZr9Tv"); got < 3.5 {
		t.Fatalf("got %v", got)
	}
}
