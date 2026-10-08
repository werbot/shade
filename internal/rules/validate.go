package rules

import "net/netip"

// validators is the registry of match validators by name: the builtin guards,
// referred to by Spec.Validator. Without them a rule like «card number»
// cuts with a single pattern everything that looks like a number.
var validators = map[string]func(string) bool{
	"luhn":        luhn,
	"ip_global":   ipv4Global,
	"ipv6_global": ipv6Global,
	"phone":       phone,
}

// Validate checks a match with the validator of the rule. An empty name means
// a rule without a validator and passes the match. An unknown name suppresses
// the rule (fail-closed): a typo in the name must disable the rule, not
// pass its matches without a check.
func Validate(name, matched string) bool {
	if name == "" {
		return true
	}
	fn, ok := validators[name]
	if !ok {
		return false
	}
	return fn(matched)
}

// luhn checks the checksum of a card number — a port of _luhn_ok and _redact_card
// from redact_output.py. Separators (space and hyphen) are filtered out, any other
// a non-digit character, like a length outside 13..19 digits, is a rejection: an empty string also
// must not pass, otherwise the validator would pass matches without a check.
func luhn(s string) bool {
	digits := make([]byte, 0, len(s))
	for i := 0; i < len(s); i++ {
		switch c := s[i]; {
		case c >= '0' && c <= '9':
			digits = append(digits, c-'0')
		case c != ' ' && c != '-':
			return false
		}
	}
	if len(digits) < 13 || len(digits) > 19 {
		return false
	}
	// The count goes right to left, like enumerate(reversed(digits)) in the original:
	// every second digit is doubled, counting from the check digit, and a result
	// above nine is reduced by nine.
	sum := 0
	for i := 0; i < len(digits); i++ {
		d := digits[len(digits)-1-i]
		if i%2 == 1 {
			if d *= 2; d > 9 {
				d -= 9
			}
		}
		sum += int(d)
	}
	return sum%10 == 0
}

// ipv4Global passes only a public IPv4 address: private ranges
// (10/8, 172.16/12, 192.168/16) Go considers global — IsGlobalUnicast for
// RFC1918 returns true, — so private ranges are filtered out explicitly. An address
// written as 4-in-6 does not pass here: it has its own validator.
func ipv4Global(s string) bool {
	addr, err := netip.ParseAddr(s)
	return err == nil && addr.Is4() && globalUnicast(addr)
}

// ipv6Global does the same for IPv6. Is6 would include 4-in-6 addresses too, so
// they are filtered out separately.
func ipv6Global(s string) bool {
	addr, err := netip.ParseAddr(s)
	return err == nil && addr.Is6() && !addr.Is4In6() && globalUnicast(addr)
}

// globalUnicast is the condition «the address is public» shared by both IP validators.
// IsGlobalUnicast itself filters out loopback, link-local, unspecified and multicast
// (RFC 1122/4632/4291), but not private ranges; IsPrivate covers what is left
// (RFC 1918 for IPv4 and RFC 4193 for IPv6).
func globalUnicast(addr netip.Addr) bool {
	return addr.IsGlobalUnicast() && !addr.IsPrivate()
}

// phone passes a sequence of 8..15 digits — that is how many digits a number
// in E.164 format without the country code. Separators (spaces, hyphens, parentheses) are
// do not count, the shape of the match is set by the regex of the rule.
func phone(s string) bool {
	n := 0
	for i := 0; i < len(s); i++ {
		if c := s[i]; c >= '0' && c <= '9' {
			n++
		}
	}
	return n >= 8 && n <= 15
}
