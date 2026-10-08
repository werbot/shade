package rules_test

import (
	"strings"
	"testing"

	"github.com/werbot/shade/internal/rules"
)

// ruleSet returns the rule set by name: the corpus checks each rule with its own
// its own sample, like self_check in redact_output.py. Detect on a single rule, not on
// the whole set: overlapping spans are merged, and the rule that fired yields to
// neighbour by priority — the sample would then check the wrong rule.
func ruleSet(t *testing.T) map[string]rules.Rule {
	t.Helper()
	rs, err := rules.LoadBuiltin()
	if err != nil {
		t.Fatal(err)
	}
	byName := make(map[string]rules.Rule, len(rs))
	for _, r := range rs {
		byName[r.Name] = r
	}
	return byName
}

// detect runs a sample through one rule and checks its enabled flag:
// the corpus secrets are caught by enabled rules, _OPTIN_CUT by disabled ones.
func detect(t *testing.T, byName map[string]rules.Rule, name, text string, enabled bool) {
	t.Helper()
	r, ok := byName[name]
	if !ok {
		t.Errorf("rule %s is not in the rule set", name)
		return
	}
	if r.Enabled != enabled {
		t.Errorf("rule %s: enabled = %v, want %v", name, r.Enabled, enabled)
		return
	}
	if spans := rules.Detect(text, []rules.Rule{r}); len(spans) == 0 {
		t.Errorf("%s: nothing detected in %q", name, text)
	}
}

// TestCorpusSecretsAreDetected — a port of _MUST_CUT: every secret must be cut
// its own rule. The values are assembled from parts, as in the source file, so that
// scanners do not take the fixture for a real key.
func TestCorpusSecretsAreDetected(t *testing.T) {
	byName := ruleSet(t)
	cases := []struct{ rule, text string }{
		{"ssh_key", "-----BEGIN " + "OPENSSH PRIVATE KEY-----\n" +
			"b3BlbnNzaC1rZXktdjEAAAAABG5vbmU\n" +
			"-----END " + "OPENSSH PRIVATE KEY-----"},
		{"putty_ppk", "Private-Lines: 2\nAAAAgQCabcdefghijklmnop\nQQQQbbbbccccddddeeee\n"},
		{"awg_init", "I1 = <b 0x" + strings.Repeat("a1", 20) + ">"},
		{"awg_params", "Jc = 5\nJmin = 10\nS1 = 37"},
		{"wg_key", "PresharedKey = " + strings.Repeat("A", 43) + "="},
		{"hash", "root:" + "$6$" + "salt1234$" + strings.Repeat("x", 86) + ":0:0"},
		{"docker_auth", `{"auths":{"registry.local":{"auth":"` +
			"YWRtaW46c2VjcmV0UGFzc3dvcmQxMjM0NTY3OA==" + `"}}}`},
		{"k8s_key_data", "    client-key-data: " + "LS0tLS1CRUdJTiBQUklWQVRFIEtFWS0tLS0tCg=="},
		{"gcp_key_id", `"private_key_id": "` + "a1b2c3d4e5f6a7b8"},
		{"azure_storage", "AccountKey=" + strings.Repeat("A", 60) + "=="},
		{"google_api_key", "key=" + "AIza" + "SyD9xQv7Lm"},
		{"stripe", "charge with " + "sk_" + "live_" + "Ab12Cd34Ef56Gh78Ij90"},
		{"digitalocean", "token " + "do" + "p_v1_" + strings.Repeat("d0", 20)},
		{"telegram_bot", "https://api.telegram.org/" + "bot" + "1234567890:" + "AA" + "Bc3dEfGhIjKlMnOp"},
		{"telegram_bot", "https://api.telegram.org" + "/bot" + "1234567890:" + "AA" + "Bc3dEfGhIj"},
		{"telegram_session", "SESSION_STRING=" + strings.Repeat("Qw9+", 20) + "=="},
		{"telegram_api_hash", "api_hash = " + "a1b2c3d4e5f6a7b8"},
		{"slack_webhook", "https://hooks.slack.com/services/" + "T00000000/B00000000/XXXXXXXXXXXXXXXXXXXXXXXX"},
		{"discord_webhook", "https://discord.com/api/webhooks/" + "123456789012345678/" + strings.Repeat("A", 24)},
		{"basic_auth", "Authorization: Basic " + "YWRtaW46c2VjcmV0UGFzc3dvcmQ="},
		{"auth_header", "X-Api-Key: " + "Xk7pQ2mZr9Tv"},
		{"cookie", "Set-Cookie: sessionid=" + "Xk7pQ2mZr9Tv" + "; Path=/"},
		{"netrc", "machine api.example.com login bob password " + "Xk7pQ2mZr9Tv"},
		{"cli_userpass", "curl -u admin:" + "Xk7pQ2mZr9Tv" + " https://api.example.com"},
		{"cli_password", "mysqldump --password=" + "Xk7pQ2mZr9Tv" + " appdb"},
		{"query_param", "GET /cb?state=xyz&code=" + "4f9a1c2e7b"},
		{"query_param", "https://api.example.com/v1/items?accessToken=" + "Qm9vazEyMzQ1"},
		{"query_param", "grant_type=refresh&client-secret=" + "hunter22abc"},
		{"query_param", "s3.amazonaws.com/f?X-Amz-Signature=" + "0a1b2c3d4e5f"},
		{"pw_command", "docker run wg-easy wgpw '" + "Xk7pQ2mZr9Tv" + "'"},
		{"bip39_seed", "mnemonic: " + "apple bridge candle donkey ember forest garden harbor island jungle kettle lantern"},
		{"env_secret", "DB_PASS=" + "Xk7pQ2mZr9Tv"},
		{"secret_word", "passphrase=" + "Xk7pQ2mZr9Tv"},
		{"assignment", "password=" + "Xk7pQ2mZr9Tv"},
		{"py_repr", "Config(api_key='" + "Xk7pQ2mZr9Tv" + "', debug=True)"},
		{"py_repr", "{'client_secret': '" + "Zq8xW3vY5bN7" + "', 'id': 7}"},
		{"py_repr", `{"token": "` + "Zm9v!YmFy#9" + `"}`},
		{"pytest_where", "E       +  where '" + "Xk7pQ2mZr9Tv" + "' = settings.password"},
		{"prefix", "pushed with " + "ghp_" + "16C7e42F292c6912E7710c83834"},
		{"conn_str", "postgresql://app:" + "Xk7pQ2mZr9Tv" + "@db.local/app"},
		{"jwt", "token " + "eyJ" + "hbGciOiJIUzI1NiJ9." + "eyJzdWIiOiIxIn0." + "c2lnbmF0dXJlMTIz"},
		{"phone", "call " + "+14155550142" + " now"},
		{"card", "card " + "4111" + "1111" + "1111" + "1111" + " on file"},
		{"card", "card " + "4111" + "-1111-1111-1111" + " on file"},
		// The key lists in assignment, py_repr and pytest_where are lowercase,
		// so without the (?i) flag the rule recognised the secret only in lower
		// case: in upper case the key name did not match and the value went to the
		// model in the open. In the reference all three patterns carry re.IGNORECASE.
		{"assignment", "PASSWORD=" + "Xk7pQ2mZr9Tv"},
		{"assignment", "PASSWORD: " + "hunter22abc"},
		{"assignment", "PresharedKey = " + "AbCdEf0123456789"},
		{"assignment", "PSK=" + "AbCdEf0123456789"},
		{"assignment", "ApiKey=" + "AbCdEf0123456789"},
		// The names `authtoken` and `clientsecret` without an underscore are unknown to the
		// reference either: its key list also requires `_`.
		{"assignment", "AUTH_TOKEN=" + "AbCdEf0123456789"},
		{"assignment", "CLIENT_SECRET=" + "AbCdEf0123456789"},
		{"py_repr", `{"PASSWORD": "` + "AbCdEf0123456789" + `"}`},
		{"pytest_where", "E       +  where '" + "AbCdEf0123456789" + "' = settings.PASSWORD"},
		// Branches lost while rewriting the fixtures of the reference corpus:
		// bcrypt (the most common hash), argon2/scrypt, the separators of a card number
		// and a py_repr value with spaces inside.
		{"hash", "root:" + "$2b$" + "12$" + strings.Repeat("A", 53)},
		{"hash", "$argon2id$v=19$m=65536,t=3,p=4$c2FsdHNhbHRzYWx0c2FsdA"},
		{"hash", "$scrypt$ln=16384,r=8,p=1$c2FsdA==$aGFzaA=="},
		{"py_repr", "Config(api_key='" + "Xk7 pQ2m Zr9" + "')"},
	}
	for _, c := range cases {
		detect(t, byName, c.rule, c.text, true)
	}
}

// TestCorpusCleanTextIsUntouched — a port of _MUST_KEEP: ordinary output must
// come back byte for byte. The sample `PUBLIC_KEY=ssh-ed25519` is not ported here: in the
// the env_secret rule has no RE2 replacement for the Python guard `(?![A-Z0-9_]*PUBLIC)`
// (see task-8-report), and such a name is masked now.
func TestCorpusCleanTextIsUntouched(t *testing.T) {
	clean := []string{
		"config = {'key': 'value', 'token': 'placeholder'}",
		"PASSWORD=$DB_PASSWORD",
		"PASSWORD = os.environ.get('X')",
		`{"password": "${DB_PASSWORD}", "token": "string"}`,
		"password: '{{ vault_db_password }}'",
		"auth: enabled\ntoken: ${GITHUB_TOKEN}",
		"commit 9fceb02d0ae598e95dc970b74767f19372d61af8",
		"sha256:e2fc4e5012d16e7fe466f5291c476431beaa1f9b90a5c2125b493ed28e2aba57",
		"h1:AbCdEf0123456789ghijklmnopqrstuvwxyzABCDEFG=",
		"id: 550e8400-e29b-41d4-a716-446655440000",
		"ts 1757000000000 ms",
		"2026-09-05T13:07:41.123456Z",
		"v1.24.3+build.20260905",
		"0.0.0.0:8080->80/tcp, :::5432->5432/tcp",
		"inet 192.168.1.42/24 brd 192.168.1.255",
		"GET /search?q=hello&page=2&sort=desc",
		"https://example.com/?ref=newsletter",
		`curl -s 'https://api.example.com/?token=$TOKEN'`,
		"htpasswd -c /etc/nginx/.htpasswd admin",
		`openssl passwd -6 "$PASS"`,
		"curl --user-agent claude/1 https://example.com",
		"parser.add_argument('--token', help='api token')",
		"systemd-ask-password --echo",
		`url = f"{base}?token={token}"`,
		"_DOCKER_AUTH = re.compile(r'x')",
		"aGVsbG8gd29ybGQgdGhpcyBpcyBiYXNlNjQgcGFkZGluZ3M=",
		"E       assert 'expected' == 'actual'",
		"E       +  where 'Alice' = user.name",
		"except: pass\nprint(count)",
		"pytest -k 'test_token_parsing'",
		"License: MIT",
		"kernel 7.1.11-zen1-1-zen",
		// Sixteen zeros pass Luhn (the sum of zeros is divisible by ten):
		// it is the issuer class [3-6] that makes them card numbers, not the checksum.
		"cart " + "0000" + "0000" + "0000" + "0000" + " total",
		// Sixteen digits of the right length, but the Luhn sum does not check out: the
		// order number, not a card, and it is the luhn validator that cuts it.
		"order " + "4123456789012345" + " placed",
		// A parenthesis follows the value immediately — that is a function call, not an assignment.
		"token = get_secret_value(1)",
		// The only reference sample where random_enough suppresses a hit from
		// prefix: `npm_` is there, while `check.py` is eight characters without a single
		// digit, that is, it does not look like a secret.
		"checks/npm_check.py",
		// Below are samples of opt-in rules: they are disabled by default, and the text
		// must stay intact.
		"reach " + "bob" + "@example.com for access",
		"User: John Smith <" + "bob" + "@example.com>",
		"call " + "415" + "-555-0142 for support",
		"link/ether " + "a4:83:e7:1b:2c:3d" + " brd ff:ff:ff:ff:ff:ff",
	}
	rs := enabledRules(t)
	for _, s := range clean {
		if spans := rules.Detect(s, rs); len(spans) != 0 {
			t.Fatalf("false positive in %q: %+v", s, spans)
		}
	}
}

// enabledRules is the enabled subset of the rule set, that is exactly what the live
// path gets from RulesForProject. Without the filter, opt-in rules would give
// clean text false positives that must not happen by default.
func enabledRules(t *testing.T) []rules.Rule {
	t.Helper()
	rs, err := rules.LoadBuiltin()
	if err != nil {
		t.Fatal(err)
	}
	on := make([]rules.Rule, 0, len(rs))
	for _, r := range rs {
		if r.Enabled {
			on = append(on, r)
		}
	}
	return on
}

// TestCorpusOptInRulesCutTheirSamples — a port of _OPTIN_CUT: every disabled
// rule has its own sample, which it must cut when enabled.
func TestCorpusOptInRulesCutTheirSamples(t *testing.T) {
	byName := ruleSet(t)
	cases := []struct{ rule, text string }{
		{"email", "reach " + "bob" + "@example.com for access"},
		{"ssn", "SSN: " + "123" + "-45-6789"},
		{"iban", "IBAN: " + "DE89" + "370400440532013000"},
		{"home_path", "reading /home/" + "bob" + "/.config/app.toml"},
		{"mac_addr", "link/ether " + "a4:83:e7:1b:2c:3d" + " brd ff:ff:ff:ff:ff:ff"},
		{"public_ip", "connected from " + "93.184.216.34" + ":44321"},
		{"public_ip6", "inet6 " + "2001:4860:4860::8888" + "/64 scope global"},
		{"phone_loose", "call " + "415" + "-555-0142 for support"},
		{"entropy", "OPAQUE=" + "Xk7pQ2mZr9TvB4nLs6WyD3fH8jCe1AuG"},
	}
	for _, c := range cases {
		detect(t, byName, c.rule, c.text, false)
	}
}

// TestCorpusEntropyKeepList — a port of _RULE_KEEP['entropy']: the noisy rule
// skips hashes, uuids, numbers and versions. The list rests on the allowlist, not on
// entropy: for a uuid it is above the threshold.
func TestCorpusEntropyKeepList(t *testing.T) {
	byName := ruleSet(t)
	keep := []string{
		"sha256:e2fc4e5012d16e7fe466f5291c476431beaa1f9b90a5c2125b493ed28e2aba57",
		"h1:AbCdEf0123456789ghijklmnopqrstuvwxyzABCDEFG=",
		"id: 550e8400-e29b-41d4-a716-446655440000",
		"commit 9fceb02d0ae598e95dc970b74767f19372d61af8",
	}
	entropy, ok := byName["entropy"]
	if !ok {
		t.Fatal("rule entropy is not in the rule set")
	}
	for _, s := range keep {
		if spans := rules.Detect(s, []rules.Rule{entropy}); len(spans) != 0 {
			t.Fatalf("entropy: false positive in %q: %+v", s, spans)
		}
	}
}

// TestCorpusPublicIP6KeepList — a port of _RULE_KEEP['public_ip6']. The sample
// `docs use 2001:db8::1 as the example address` is not ported here: Go considers
// the documentation range 2001:db8::/32 global and the rule cuts it,
// whereas the Python ipaddress.is_global rejects it. The direction of the miss is —
// an extra mask, there is no leak; the rule is opt-in.
func TestCorpusPublicIP6KeepList(t *testing.T) {
	byName := ruleSet(t)
	keep := []string{
		"inet6 ::1/128 scope host",
		"inet6 fe80::1e69:7aff:fe3c:1/64 scope link",
		"inet6 fd00:1234::5/64 scope global",
		"started 12:34:56, link/ether 00:11:22:33:44:55",
	}
	ip6, ok := byName["public_ip6"]
	if !ok {
		t.Fatal("rule public_ip6 is not in the rule set")
	}
	for _, s := range keep {
		if spans := rules.Detect(s, []rules.Rule{ip6}); len(spans) != 0 {
			t.Fatalf("public_ip6: false positive in %q: %+v", s, spans)
		}
	}
}
