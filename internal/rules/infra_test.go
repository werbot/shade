package rules_test

import (
	"slices"
	"strings"
	"testing"

	"github.com/werbot/shade/internal/rules"
)

// types joins the span types sorted: the test checks the set of placeholders,
// not the order the rules happened to fire in.
func types(spans []rules.Span) string {
	out := make([]string, len(spans))
	for i, s := range spans {
		out[i] = s.Type
	}
	slices.Sort(out)
	return strings.Join(out, ",")
}

// TestBuiltinSSHRulesCatchUserAndHost — the ssh/scp/sftp anchor cuts both the
// login and the FQDN host on the same line. The anchor must also work mid-line,
// after `--` and flags: `kubectl exec -it pod -- ssh -n default user@host`.
//
// The rule set is the enabled subset, as on the live path (RulesForProject):
// Detect itself does not look at the enabled flag, and the disabled email rule
// at order 380 would otherwise swallow user@host as a single EMAIL span.
func TestBuiltinSSHRulesCatchUserAndHost(t *testing.T) {
	rs := enabledRules(t)
	for _, in := range []string{
		"ssh alice@db.prod.local",
		"ssh -o StrictHostKeyChecking=no -i ~/.ssh/id_ed25519 alice@db.prod.local",
		"scp alice@db.prod.local:/var/log/app.log .",
		"kubectl exec -it pod -- ssh -n default alice@db.prod.local",
	} {
		spans := rules.Detect(in, rs)
		if types(spans) != "HOST,USER" {
			t.Fatalf("%q: %v", in, spans)
		}
	}
}

// TestBuiltinSSHRulesStayQuietOnLookalikes — without the anchor any word would
// read as a login and a FQDN would read as a filename. An already-anonymized
// line has no span either. `localhost` and an IP stay out of the HOST type:
// public_ip covers public addresses and is disabled by default.
func TestBuiltinSSHRulesStayQuietOnLookalikes(t *testing.T) {
	rs := enabledRules(t)
	for _, in := range []string{
		"ssh-keyscan db.prod.local",
		"myssh alice@db.prod.local",
		"cat app.config.js",
		"echo \"alice@db.prod.local\"",
		"ssh <USER_1>@<HOST_1>",
		"ssh localhost",
		"ssh 10.0.0.7",
	} {
		if spans := rules.Detect(in, rs); len(spans) != 0 {
			t.Fatalf("%q must not be cut: %v", in, spans)
		}
	}
}
