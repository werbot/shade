# shade

`shade` sits between your client and the LLM: it replaces sensitive values in
outgoing prompts with stable placeholders, then puts the real values back into
the model's answer.

The provider sees `password="<SECRET_1>"`. Your terminal sees
`password="correct-horse-battery-staple"`. The value itself never leaves the
machine: it is stored encrypted in a local SQLite database scoped to your
project.

```
$ shade anon req.txt
password="<SECRET_1>"
card: <CARD_1>
phone: <PHONE_1>

$ shade anon req.txt | shade deanon
password="correct-horse-battery-staple"
card: 4242 4242 4242 4242
phone: +1 415 555 0132
```

## 📖 Contents

- [Why](#-why)
- [How it works](#-how-it-works)
- [Claude Code](#-claude-code)
- [Architecture](#-architecture)
- [Install](#-install)
- [Quick start](#-quick-start)
- [Command reference](#-command-reference)
- [Exit codes](#-exit-codes)
- [Configuration](#-configuration)
- [Rules](#-rules)
- [Placeholders](#-placeholders)
- [Security model](#-security-model)
- [Development](#-development)
- [Status](#-status)
- [License](#-license)

## 🎯 Why

Pasting logs, configs and stack traces into a chat window hands your secrets to
someone else's infrastructure. Redacting by hand is lossy — you cannot get the
value back, so the model's answer is useless for anything that has to be applied
to the real system.

`shade` makes redaction reversible. The zero-information paths:

- **Stable placeholders.** The same value always maps to the same token, across
  restarts and parallel processes, so a follow-up question in the same
  conversation still refers to `<SECRET_1>`.
- **Local by default.** Values live in `$SHADE_HOME/shade.db`, encrypted with a
  key that never leaves the machine.
- **Fail-closed or fail-open, your call.** By default an answer that still
  contains an unresolvable placeholder is *blocked* rather than handed over with
  a hole in it.

One caveat on the Claude Code integration: the prompt you type is not anonymized —
a hook cannot rewrite a prompt — so what is shaded there is what the tools bring
back. See [Claude Code](#-claude-code).

## 🔄 How it works

```mermaid
flowchart TB
    Client["Client / chat UI"] -->|prompt| Anon["shade anon"]
    Anon -->|"prompt with placeholders"| Model["LLM"]
    Model -->|"answer with placeholders"| Deanon["shade deanon"]
    Deanon -->|"answer with real values"| Client
    Anon <--> DB[("shade.db<br/>values encrypted")]
    Deanon <--> DB
```

Anonymization is a linear pipeline over the text:

```mermaid
flowchart LR
    In["text"] --> Guard["Guard<br/>hide placeholders<br/>already in the input"]
    Guard --> Detect["Detect<br/>keyword prefilter, then<br/>regex → entropy → validator → allowlist"]
    Detect --> Merge["Merge<br/>union of overlapping spans"]
    Merge --> Clip["clip<br/>trim spans around<br/>hidden placeholders"]
    Clip --> Alloc["Allocate<br/>value hash → placeholder<br/>AES-256-GCM value"]
    Alloc --> Out["text with placeholders"]
```

Two details in that pipeline are worth knowing, because both exist to prevent
data loss rather than to improve matching:

- **Guard runs first.** If the input already contains placeholders — a tool
  output, the history of the dialogue — they are masked before the rules see the
  text, so a repeated run cannot turn `<EMAIL_1>` into `<EMAIL_2>`. Because the
  mask is not opaque to a regex, the spans that cover a masked region are
  *clipped* to its bounds instead of being dropped: a secret sitting next to a
  placeholder is still caught.
- **Merge takes the union, not the winner.** When two rules overlap, the
  higher-priority rule decides the placeholder *type*; the *bounds* are the union
  of both. Discarding the loser's bytes would leave text open that the engine had
  already classified as sensitive.

Restore runs the opposite way, right to left, because each substitution changes
the length of the text.

## 🤖 Claude Code

`shade init` installs the hooks, and from then on the session is shaded without
you doing anything. It writes the plugin into `$SHADE_HOME/claude` and prints a
diff of every settings file it touches:

```
$ shade init
/Users/you/.shade/claude/.claude-plugin/marketplace.json:
+{ ... }
/Users/you/.shade/claude/.claude-plugin/plugin.json:
+{ ... }
/Users/you/.shade/claude/hooks/hooks.json:
+{ "hooks": { ... one entry per event ... } }
/Users/you/.claude/settings.json:
+{
+  "extraKnownMarketplaces": {
+    "shade": {
+      "source": {
+        "path": "/Users/you/.shade/claude",
+        "source": "directory"
+      }
+    }
+  }
+}
/Users/you/code/my-app/.claude/settings.json:
+{
+  "enabledPlugins": {
+    "shade@shade": true
+  }
+}
next Claude Code launch: confirm the shade plugin when asked, the hooks stay off until it is trusted
```

The marketplace declaration always goes in the **user** settings, because Claude
Code does not honour it in a project file; `enabledPlugins` goes to the project of
the git root, or to the user settings under `--global`. The command is idempotent —
a second run prints only the reminder — and `--dry-run` prints the same diff
without writing anything. `--keep-old-hook` leaves an existing `PostToolUse` hook
in place, which is worth doing only if you want two rewrites on one event; shade
removes it by default, since two of them are non-deterministic.

| Event | What shade does |
| --- | --- |
| `SessionStart` | Registers the project and hands the model the directive that explains the tokens |
| `UserPromptSubmit` | With `prompt_gate = "on"`, blocks a prompt whose content looks sensitive and names the types; silent otherwise |
| `PreToolUse` | Puts the real values back into the tool arguments, so the tool runs for real |
| `PostToolUse` | Replaces values in the tool output with placeholders, so the model reads tokens |
| `MessageDisplay` | Shows the real values on your screen; the transcript and the model keep the tokens |

Two limits are worth knowing before you rely on it:

- **The prompt you type is not anonymized.** A hook cannot rewrite the prompt, so
  the gate can only block it — and only when you turn it on. A secret pasted
  straight into the prompt reaches the model unless `prompt_gate = "on"` stops it.
  What is protected is what the tools bring back: files, command output, logs.
- **Tool arguments are restored on trust.** The model writes those arguments, so a
  token it mangled is still matched; the other direction — tool output, which the
  outside world writes — is only ever anonymized.

## 🧱 Architecture

```mermaid
flowchart TB
    CLI["cmd/shade<br/>anon · deanon · hook · init · rules · entities · audit · test · doctor · version"]
    CLI --> Core["internal/core<br/>Engine: Anonymize · Restore · Scan"]
    CLI --> Hook["internal/hook<br/>Claude Code events → responses"]
    CLI --> Set["internal/settings<br/>settings.json · plugin files"]
    Hook --> Core
    Hook --> Dir["internal/directive<br/>the model directive"]
    Core --> Rules["internal/rules<br/>compile · detect · merge · validators"]
    Core --> PH["internal/placeholder<br/>format · find · guard"]
    Core --> Store["internal/store<br/>SQLite, projects, entities, rules"]
    Core --> Cfg["internal/config<br/>layered TOML"]
    Store --> Crypt["internal/crypt<br/>AES-256-GCM · HMAC-SHA256"]
```

| Package | Responsibility |
| --- | --- |
| `cmd/shade` | CLI: argument parsing, command registry, exit codes, output shapes |
| `internal/core` | `Engine` — glues rules, placeholders and the store into `Anonymize`/`Restore` |
| `internal/hook` | The Claude Code adapter: event payloads, per-event responses, the JSON walker |
| `internal/settings` | Claude Code `settings.json`: load, merge, diff, and the plugin files |
| `internal/directive` | The one text that tells the model how to treat the tokens |
| `internal/rules` | Compiles rule specs, finds matches, merges spans, runs validators |
| `internal/placeholder` | The `<TYPE_N>` format, tolerant token search, the guard |
| `internal/store` | SQLite schema, migrations, projects, entities, rules, stats, audit |
| `internal/config` | Three-layer config: defaults → global file → project file |
| `internal/crypt` | Key file, AES-256-GCM for values, keyed HMAC for lookups |

`core.Engine` takes the project directory as an argument rather than calling
`os.Getwd` internally, so the same engine can serve a CLI run (`shade anon` in
the current directory) and a hook or MCP adapter that passes the `cwd` from its
own payload.

## 📦 Install

Requires Go 1.27.1 or newer.

```
make build          # CGO_ENABLED=0 go build -o shade ./cmd/shade
./shade version
```

The build is pure Go end to end — SQLite comes from `modernc.org/sqlite`, so
`CGO_ENABLED=0` produces a static binary.

There are no releases or prebuilt packages yet; build from source.

## 🚀 Quick start

State lives in `$SHADE_HOME`, defaulting to `~/.shade`. It is created on first
run; nothing has to be initialized by hand.

```
$ shade doctor
directory: /Users/you/.shade — available
key: readable
database: /Users/you/.shade/shade.db
project: /Users/you/code/my-app
rules: 39 active
```

`doctor` never creates anything — it reports what it finds, so "configured" stays
distinguishable from "just configured".

Anonymize a prompt, then restore the answer:

```
$ shade anon req.txt
password="<SECRET_1>"
card: <CARD_1>
phone: <PHONE_1>
```

Piping the result back restores the real values:

```
$ shade anon req.txt | shade deanon
password="correct-horse-battery-staple"
card: 4242 4242 4242 4242
phone: +1 415 555 0132
```

Re-running `anon` on the same text yields the same placeholders, so a
conversation stays consistent. See what the rules catch and where:

```
$ shade test --sample 'AKIAIOSFODNN7EXAMPLE'
offset	type	rule	fragment
4-20	TOKEN	prefix	"IOSFODNN7EXAMPLE"
```

`--json` gives a machine-readable shape — `{"text": ..., "spans": [{"type": ..., "rule": ...}]}`
for `anon`, `{"text": ..., "unresolved": [...]}` for `deanon`. `anon`, `deanon`,
`test` and `rules import` read a file argument or stdin and accept `--project DIR`;
`hook` reads its event from stdin, and `init` takes neither.

## 🧭 Command reference

| Command | Purpose |
| --- | --- |
| `shade anon [--json] [--project DIR] [FILE]` | Replace secrets with placeholders |
| `shade deanon [--json] [--project DIR] [FILE]` | Restore real values |
| `shade hook` | Answer a Claude Code hook event read from stdin (always exits 0 for a hook event) |
| `shade init [--global] [--dry-run] [--keep-old-hook]` | Install the Claude Code hooks: generate the plugin and wire it into the settings |
| `shade doctor` | Report the state of the environment without modifying it |
| `shade test [--rules NAME] [--sample TEXT] [FILE]` | Run the active rule set against a sample, no writes |
| `shade rules list` | List rules of the scope |
| `shade rules add --name N --type T --pattern P [--kind K] [--secret-group N] [--global]` | Add a rule |
| `shade rules rm --name N [--global]` | Delete a rule (builtin rules cannot be deleted) |
| `shade rules enable\|disable --name N [--global]` | Toggle a rule |
| `shade rules import [--global] FILE` | Import a gitleaks TOML |
| `shade rules export` | Print non-builtin active rules as TOML |
| `shade rules test --pattern P [--kind K] [--type T] [--sample S] [FILE]` | Check a rule before saving it |
| `shade entities list` | List entities of the project (placeholders, types, hit counts) |
| `shade entities prune [--older-than 30d]` | Delete entities not seen for the given age |
| `shade audit [--since 7d] [--unresolved]` | Journal of unresolved placeholders |
| `shade version` | Print the build version |

Rules, entities and the journal are scoped to the project: `--global` edits the
rule set shared by every project, while the default scope is the project of the
current directory.

## 🚦 Exit codes

| Code | Meaning |
| --- | --- |
| `0` | Success |
| `1` | Operational error: unreadable file, unusable environment, a rule that fails to compile against its own pattern |
| `2` | Usage error: unknown command or flag, missing required flag, malformed argument |
| `3` | `deanon` refused to hand over the answer: it still contains unresolvable placeholders, and `fail_policy` is not `fail_open_log` |

Code `3` is the interesting one. A placeholder with no entity in the store means
the response has a hole where a value should be — the text is blocked, the tokens
are listed on stderr, and under `--json` an empty `text` expresses the block.
Under `fail_open_log` the same situation produces the partial text, a warning on
stderr and exit `0`.

`shade hook` is the exception to the table: answering a hook event always exits
`0`. A non-zero exit at Claude Code means "block" or "message the model", and a
hook that failed must do neither — a runtime failure comes back as a
`systemMessage` on stdout, and an unreadable payload is silence. Its own usage
errors are not an exception: `shade hook` with a stray argument still exits `2`.

An auxiliary write failure never changes the code: if the hit counter or the
journal cannot be written, the result is still delivered and the failure is
reported on stderr. A ready prompt or answer is worth more than a statistics row.

## 🔧 Configuration

Settings are read from three layers, each overriding the previous one:

1. built-in defaults,
2. `$SHADE_HOME/config.toml` — global,
3. `<project root>/.shade.toml` — per project.

A missing file is a skipped layer, not an error. The project root is the top of
the git repository, which is also the boundary of a project: placeholders issued
in one repository are invisible to another. Unknown keys are ignored, so a config
written for a future version will not break today's binary.

| Key | Default | Effect |
| --- | --- | --- |
| `fail_policy` | `fail_closed` | `fail_closed` blocks an answer with unresolved placeholders (exit 3); `fail_open_log` lets it through with a warning |
| `entities_ttl` | `90d` | Default age for `shade entities prune` |
| `prompt_gate` | `off` | `on` makes the `UserPromptSubmit` hook block a prompt whose content looks sensitive, naming the types. `auto` means "unless a proxy is active" and is not live until the proxy exists, so `off` and `auto` behave the same today |

```toml
fail_policy = "fail_open_log"
entities_ttl = "30d"
prompt_gate = "on"
```

`SHADE_HOME` overrides the state directory (`~/.shade`).

## 📜 Rules

A rule has a **type** (which placeholder it produces), a **kind**, and a
**pattern**:

| `kind` | Pattern | Notes |
| --- | --- | --- |
| `regex` | RE2 regular expression | `secret_group` selects the capture group to replace; the rest of the match stays visible for context |
| `literal` | Fixed string | Quoted internally, no escaping needed in the config |
| `entropy` | — | Flags a value by Shannon entropy alone |

Above the pattern sits a chain of filters, all of which must pass:

- **keywords** — a cheap prefilter. A rule with keywords is skipped entirely
  when none of them appears in the text.
- **entropy** — `entropy_min` rejects low-entropy matches, so a threshold can be
  layered on top of any regex.
- **validator** — a named guard for values that are simply not what they look
  like. Unknown names fail closed: a typo disables a rule instead of letting it
  match unchecked.

| Validator | Accepts |
| --- | --- |
| `luhn` | Card numbers with a valid checksum, 13–19 digits |
| `ip_global` / `ipv6_global` | Public addresses only; private and 4-in-6 ranges are rejected |
| `phone` | 8–15 digits, E.164 without the country code |
| `random_enough` | Long, or contains a digit — separates a real secret from `token: string` |

**Built-in rules** ship embedded in the binary and are re-seeded on every start
without touching rules you have edited.

| Group | Rules | Active by default |
| --- | --- | --- |
| `keys` — key material (SSH, WireGuard, Putty, k8s, cloud keys) | 18 | 18 |
| `credentials` — headers, CLI flags, netrc, assignments, vendor tokens | 18 | 17 |
| `pii` — personal and network identifiers | 10 | 2 |
| `infra` — the login and host of an `ssh`/`scp`/`sftp` command line | 2 | 2 |

48 rules in total, 39 of them active out of the box.

PII rules are intentionally conservative: only `phone` and `card` are on out of
the box. The remaining eight (`email`, `ssn`, `iban`, `mac_addr`, `home_path`,
`public_ip`, `public_ip6`, `phone_loose`) are shipped disabled, since a rule that
fires on ordinary text is worse than no rule. The same reasoning disables the
`entropy` rule in the credentials group. Enable what you need per scope:

```
$ shade rules enable --name email --global
```

Built-in rules can be disabled but never deleted, and `shade rules export`
deliberately omits them — they restore themselves on every machine and would be
dead weight in the file.

**gitleaks import** reads another tool's TOML and reports what it could not
translate instead of failing the whole run:

```
$ shade rules import gitleaks.toml
imported: 1, skipped: 2
skipped "lookahead": pattern "token(?=\s*=)": error parsing regexp: invalid or unsupported Perl syntax: `(?=`
skipped "unknown-type": unknown type "NOPE"
```

Lookaround, paths, `regexTarget`, stopwords and `condition` have no equivalent
here, so such rules are narrowed to the match or skipped with a reason. Import is
one-way: `export` writes `shade`'s own format, since there is no reverse mapping
for validators, kind and ordering.

## 🔖 Placeholders

The canonical form is `<TYPE_N>`, with `N` a per-project, per-type counter.

```
SECRET  TOKEN   KEY    PERSON  EMAIL  PHONE  CARD   IBAN   SSN    MAC
IP      HOST    PATH   USER    DB     URL    TICKET ORG    ADDR   DSN
```

The type list is closed on purpose. A regex cannot tell `<EMAIL_1>` from
`<div_1>` without lookaround, and RE2 has none — so the set of types defines what
counts as a placeholder, and ordinary markup is left alone.

Recognition is tolerant of how models mangle tokens in their output: HTML-escaped
brackets (`&lt;`), fullwidth brackets (`＜`), spaces or a newline inside the
token, a lowercase type, and truncation at the token limit where the closing
bracket is missing and the token ends at a word boundary. A token glued to a
letter or digit (`<KEY_1яяя`) is rejected rather than guessed at — treating it as
a token would substitute a value where none was requested.

## 🔒 Security model

- **Key.** 32 random bytes in `$SHADE_HOME/key`, mode `0600`. It is published
  atomically via a hard link and never regenerated: overwriting it would make
  every stored value unreadable.
- **Values at rest.** AES-256-GCM with a fresh random nonce per record. The key
  is also the GCM key, so the database alone is not enough to read values.
- **Lookup without decryption.** A keyed HMAC-SHA256 over `type ‖ 0x00 ‖ value`
  gives a deterministic index, which is what makes placeholders stable without
  ever decrypting existing rows. The type is part of the hash, so one value used
  in two roles gets two placeholders.
- **The journal never stores values.** It records tokens and types only. A value
  cannot appear in the audit table, the exports or the listings — `entities
  list` has no value field at all, so a leak there is impossible by type rather
  than by discipline.
- **Nothing sensitive goes to stderr or stdout unredacted.** Only `anon`'s output
  reaches stdout, and its secrets are already placeholders by then.

Retention is per project and explicit: `shade entities prune` drops entities by
last-seen age. A pruned value is unrecoverable, since the plaintext exists only
in `value_enc`.

## 🧪 Development

```
make test    # go test ./...
make lint    # go vet ./... plus a gofmt check
make build
```

Tests live next to the code they cover and are mostly end-to-end: the CLI suite
drives real commands against a temporary `SHADE_HOME`, and the rule tests run
against a corpus rather than single strings.

Dependencies are deliberately few — `github.com/BurntSushi/toml` for config and
rules, `modernc.org/sqlite` for a CGO-free SQLite driver. Everything else comes
from the standard library.

## 📌 Status

Phases 1 and 2 are complete — the core and the CLI, then the Claude Code hook
adapter. The rest is ahead of us; the code is already sliced for it.

- [x] Core engine — `Anonymize`, `Restore`, `Scan`
- [x] Rule engine — keyword prefilter, entropy threshold, validators, span merging
- [x] Encrypted store — projects, entities, audit journal, SQLite migrations
- [x] Builtin rule set — 48 rules across `keys`, `credentials`, `pii`, `infra` (39 active by default)
- [x] gitleaks rule import with a per-rule skip report
- [x] CLI — `anon`, `deanon`, `hook`, `init`, `rules`, `entities`, `audit`, `test`, `doctor`, `version`
- [x] Claude Code hooks — `SessionStart`, `UserPromptSubmit`, `PreToolUse`, `PostToolUse` and `MessageDisplay`, installed by `shade init`, which records `hook` as the adapter in the journal
- [ ] MCP and proxy adapters — `core.Engine` already takes the adapter name, and only `cli` and `hook` reach it so far
- [ ] MCP `scan` tool — `Engine.Scan` is the read-only path it would use
- [ ] Rule packages — the `packages` table exists; nothing writes to it yet
- [ ] Usage UI — `rule_hits` is written on every anonymization; nothing reads it yet
- [ ] Streaming mode and LLM provider integration — the config keys are declared but have no consumer

## 📄 License

MIT — see [LICENSE](LICENSE).
