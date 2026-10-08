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

func TestValidateLuhn(t *testing.T) {
	if !rules.Validate("luhn", "4242424242424242") {
		t.Fatal("valid card rejected")
	}
	if rules.Validate("luhn", "4242424242424243") {
		t.Fatal("invalid card accepted")
	}
}

func TestValidateIPGlobal(t *testing.T) {
	no := []string{"10.0.0.1", "127.0.0.1", "192.168.1.42", "172.16.0.1", "169.254.1.1", "0.0.0.0"}
	for _, s := range no {
		if rules.Validate("ip_global", s) {
			t.Fatalf("%s must not be global", s)
		}
	}
	if !rules.Validate("ip_global", "203.0.113.9") {
		t.Fatal("public ip rejected")
	}
}

func TestValidateLuhnGuard(t *testing.T) {
	// Twelve digits pass Luhn but are not a card number: without a length
	// limit the validator would pass matches without any digits at all, that is, without a check.
	if rules.Validate("luhn", "424242424242") {
		t.Fatal("12-digit number accepted")
	}
	// Junk around the digits is a rejection too: a placeholder that accidentally got into a rule with
	// with this validator must suppress the match rather than count as a sum.
	if rules.Validate("luhn", "4242424242424242xyz") {
		t.Fatal("junk around digits accepted")
	}
}

func TestValidateIPv6Global(t *testing.T) {
	no := []string{"::1", "fe80::1", "fd00::1", "::", "ff02::1", "8.8.8.8", "::ffff:8.8.8.8"}
	for _, s := range no {
		if rules.Validate("ipv6_global", s) {
			t.Fatalf("%s must not be global", s)
		}
	}
	if !rules.Validate("ipv6_global", "2606:4700:4700::1111") {
		t.Fatal("public ipv6 rejected")
	}
}

func TestValidatePhone(t *testing.T) {
	yes := []string{"+1 415 555 0142", "12345678", "123456789012345"}
	for _, s := range yes {
		if !rules.Validate("phone", s) {
			t.Fatalf("%q must pass", s)
		}
	}
	no := []string{"1234567", "1234567890123456"}
	for _, s := range no {
		if rules.Validate("phone", s) {
			t.Fatalf("%q must not pass", s)
		}
	}
}
