---
name: shade-rules
description: Use when adding, editing, importing or debugging a shade rule — the rule anatomy, the test-before-you-save loop, what a gitleaks import drops, and the false positives to expect.
---

# Writing shade rules

A rule turns a match in the text into a placeholder. It is applied on every
anonymization path — `anon`, the Claude Code hooks, the MCP tools — and belongs
either to the current project or, with `--global`, to every project.

The tokens a rule produces are covered by the placeholder contract in the `shade`
skill: a rule changes what gets replaced, never how a token is treated. shade's
directive — the `SessionStart` context, or the `shade://directive` MCP resource —
is the authoritative statement of that contract.

## Anatomy

Every rule has a **type** — which placeholder it produces — a **kind** and a
**pattern**, followed by an optional chain of filters.

| `kind` | Pattern | Notes |
| --- | --- | --- |
| `regex` | RE2 | `--secret-group N` replaces only the Nth capture group, leaving the surrounding match visible for context |
| `literal` | A fixed string | Quoted internally — nothing to escape in the config |
| `entropy` | — | Flags a value by Shannon entropy alone, with no pattern at all |

The type must come from the closed placeholder set: `SECRET TOKEN KEY PERSON
EMAIL PHONE CARD IBAN SSN MAC IP HOST PATH USER DB URL TICKET ORG ADDR DSN`. An
unknown type fails to compile. The set is closed on purpose — RE2 has no
lookaround, so without it a regex could not tell `<EMAIL_1>` from `<div_1>`.

A match must pass every filter that is set:

- **keywords** — a cheap prefilter. A rule with keywords is skipped whole when
  none of them appears in the text, so list the words that always accompany the
  value. This is the cheapest way to cut false positives.
- **entropy_min** — rejects low-entropy matches; layers a threshold on any regex.
- **validator** — a named guard: `luhn`, `ip_global`, `ipv6_global`, `phone`,
  `random_enough`. An unknown name suppresses the rule (fail-closed), so a typo
  disables a rule instead of letting its matches through unchecked.
- **allowlist** — regexes tested against the match; a hit drops it.

## Test before you save

`shade rules test` compiles the rule and runs it against raw sample text, writing
nothing to the database:

```
$ shade rules test --pattern 'AKIA[0-9A-Z]{16}' --type TOKEN --sample 'key=AKIAIOSFODNN7EXAMPLE'
offset	type	rule	fragment
4-24	TOKEN	adhoc	"AKIAIOSFODNN7EXAMPLE"
```

A sample is given by `--sample` or as a file argument. The other defaults are
`--kind regex` and `--type SECRET`; `--sample` and a file together are an error.
`shade test` does the same against the whole active rule set, with `--rules NAME`
to narrow it to one.

Always run it before saving. A pattern that does not compile must be caught here:
one unreadable row makes the project's entire rule set fail to load, and it is
then fixed only by editing the database by hand.

Then save, and confirm what landed:

```
shade rules add --name aws_key --type TOKEN --pattern 'AKIA[0-9A-Z]{16}'
shade rules list
shade rules enable --name email --global
shade rules disable --name phone_loose
```

A user rule sorts before the builtins, so on an overlap its type wins while the
bounds stay the union of both matches. Builtin rules can be disabled but never
deleted: they are re-seeded on every start, so editing one in place does not
survive. `shade rules export` writes the project's own active rules as TOML and
omits the builtins deliberately — they restore themselves on every machine.

## RE2, not PCRE

The engine is Go's RE2, so lookaround, backreferences and conditionals do not
compile: `(?=`, `(?<=`, `(?<!` and `\1` are all errors. Rewrite around the
message rather than making the pattern cleverer — match the whole `token=value`
pair and replace just the value with `secret_group`.

## Importing gitleaks

```
shade rules import gitleaks.toml [--global]
imported: 1, skipped: 2
skipped "lookahead": rule "lookahead": pattern "token(?=\\s*=)": error parsing regexp: invalid or unsupported Perl syntax: `(?=`
skipped "unknown-type": rule "unknown-type": unknown type "NOPE"
```

The report is per rule and the run never stops at the first failure: a gitleaks
config almost always holds something the engine cannot express, and the rest of
the set must not be lost because of it.

Ported: `regex` with `secretGroup`, `entropy`, `keywords`, and allowlist
`regexes`. Dropped: PCRE syntax, `paths`, `regexTarget`, `stopwords` and
`condition` — shade's allowlist matches against the value itself, and the engine
sees no file context. Import is one-way: `export` writes shade's own format,
since validators, kind and ordering have no gitleaks equivalent.

## Typical false positives

A rule that fires on ordinary text is worse than a rule that is missing, so the
defaults are conservative: of the ten PII rules only `phone` and `card` ship
active, and the pure-entropy rule in the credentials group ships disabled.

- **Entropy alone** flags any long random-looking string — a git SHA, a UUID, a
  base64 blob, a minified bundle. Give the rule keywords, or drop `entropy` and
  keep a regex with a validator.
- **Bare digit runs** catch order numbers and timestamps. `luhn` narrows a card
  pattern to numbers with a valid checksum.
- **IPs** match version strings and private ranges. `ip_global` and `ipv6_global`
  accept public addresses only.
- **Phone** patterns match dates and IDs; the `phone` validator caps the match at
  8–15 digits. `phone_loose` therefore ships off.
- **`random_enough`** separates a real secret from a declaration like `token:
  string`, but a long ordinary word passes it — pair it with a pattern, not as a
  guard on its own.

When a rule still misfires, tighten the pattern or the validator rather than
piling on allowlist entries: an allowlist entry broad enough to silence a false
positive also silences the real values next to it.
