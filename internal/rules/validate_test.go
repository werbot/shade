package rules_test

import (
	"testing"

	"github.com/werbot/shade/internal/rules"
)

func TestValidateInPhase1(t *testing.T) {
	if !rules.Validate("", "anything") {
		t.Fatal("empty validator name must accept")
	}
	if rules.Validate("nope", "whatever") {
		t.Fatal("unknown validator must reject, not accept")
	}
}
