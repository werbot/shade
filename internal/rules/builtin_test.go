package rules_test

import (
	"slices"
	"testing"

	"github.com/werbot/shade/internal/rules"
)

// TestBuiltinCoversEveryPortedRule — completeness of the port: every rule from
// _RULES of the source file must be present in the rule set under its own name.
func TestBuiltinCoversEveryPortedRule(t *testing.T) {
	rs, err := rules.LoadBuiltin()
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]bool{}
	for _, r := range rs {
		got[r.Name] = true
	}
	want := []string{
		"ssh_key", "putty_ppk", "awg_init", "awg_params", "wg_key", "hash",
		"docker_auth", "k8s_key_data", "gcp_key_id", "azure_storage",
		"google_api_key", "stripe", "digitalocean", "telegram_bot",
		"telegram_session", "telegram_api_hash", "slack_webhook",
		"discord_webhook", "basic_auth", "auth_header", "cookie", "netrc",
		"cli_userpass", "cli_password", "query_param", "pw_command",
		"bip39_seed", "env_secret", "secret_word", "assignment", "py_repr",
		"pytest_where", "prefix", "conn_str", "jwt", "phone", "card",
		"email", "ssn", "iban", "home_path", "mac_addr", "public_ip",
		"public_ip6", "phone_loose", "entropy",
	}
	for _, name := range want {
		if !got[name] {
			t.Errorf("rule %s missing from builtin set", name)
		}
	}
}

// TestBuiltinDefaultEnabledSet — all rules are enabled except the nine opt-in ones: in
// TOML enabled rules have no enabled flag, and a missing key must mean
// an enabled rule specifically, not «disabled by a zero value».
func TestBuiltinDefaultEnabledSet(t *testing.T) {
	off := []string{"email", "ssn", "iban", "home_path", "mac_addr",
		"public_ip", "public_ip6", "phone_loose", "entropy"}
	rs, err := rules.LoadBuiltin()
	if err != nil {
		t.Fatal(err)
	}
	if len(rs) == 0 {
		t.Fatal("empty rule set")
	}
	for _, r := range rs {
		if want := !slices.Contains(off, r.Name); r.Enabled != want {
			t.Errorf("rule %s: enabled = %v, want %v", r.Name, r.Enabled, want)
		}
	}
}
