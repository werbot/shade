---
name: shade
description: Use when the conversation holds a shade token such as <EMAIL_1> or <SECRET_1> — what a token is, how to keep it intact, and how to anonymize and restore text with the shade CLI.
---

# shade tokens

shade sits between the user and you. On the way in it swaps real values for
placeholders; on the way back it swaps the real values in again. What you are
shown is a stand-in, not the value itself — the user's own screen shows the real
values behind the tokens.

## A token is the value

`<EMAIL_1>`, `<SECRET_1>`, `<PATH_1>` — angle brackets, an upper-case type, a
number. Read one as the concrete value it replaces, never as a word to process.

- **Keep the token byte for byte.** Everything between `<` and `>` is an
  identifier, as fixed as a variable name: no translation, no rearrangement of
  its parts, no case change, no spaces added, no line break inside the brackets.
- **The number is identity.** `<KEY_1>` and `<KEY_2>` are two different values,
  and the same number means the same value everywhere in the project — across
  turns, restarts and parallel sessions. Two tokens are never interchangeable.
- **Put the token where a tool needs the value.** Arguments you write are
  restored before the tool runs, so `cat <PATH_1>` opens the real file and
  `ssh <USER_1>@<HOST_1>` reaches the real host. Hand the token over as it
  stands; escaping, quoting or interpolating it yourself only breaks the match.
- **Do not work out what it hides.** The value is not derivable from the token
  and usually not relevant: the user can see it, and reasoning about it is
  guessing. Use `<SECRET_1>` the way you would use the value it names.
- **Never mint a token.** Only shade issues them. A token you invent has nothing
  behind it, and shade reads it the same way it reads a token you mistyped: as a
  hole in the text.

Breaking a token is the one mistake with no recovery. shade finds no value for it
and reads it as a hole; under the default policy that answer is not handed over
at all. Keeping tokens intact is not politeness, it is what makes the answer
usable.

## The directive is the authority

This skill describes the tokens; the normative statement of how to treat them is
shade's **directive**. It arrives as injected context on `SessionStart`, and over
MCP as the resource `shade://directive`. Where this skill and the directive
disagree, follow the directive — and re-read it before work where a mistake is
expensive: writing a file, issuing a request, generating code that will embed
the value.

## Anonymizing and restoring by hand

Outside Claude Code the pipeline is the CLI:

```
shade anon   req.txt   # real values -> <TYPE_N> tokens
shade deanon ans.txt   # <TYPE_N> tokens -> real values
```

Both read a file argument or stdin and take `--project DIR`; `--json` gives a
machine-readable shape. `shade deanon` exits `3` when the text still holds a
token with no value behind it, so a mangled token fails loudly instead of
passing through silently.
