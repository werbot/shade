# Rules before starting work
0. Always use the orchestrator, agents and subagents.
1. Use the built-in Orca browser for testing and work.
2. When the work is finished, release unused resources.
3. If you don't know the answer, don't make it up!

## Skills workflow

Priority: this CLAUDE.md > skills > default behavior.
Where a skill conflicts with a rule in this file, the rule in this file wins.

### Superpowers (/using-superpowers)
- At the start of every session, load /using-superpowers and check whether a skill applies before acting.
- New feature or change: brainstorming → writing-plans → test-driven-development → executing-plans. Bug: systematic-debugging first. Before declaring a task done: verification-before-completion.
- SDD artifacts (specs, plans, the progress ledger, review diffs) go to `.superpowers/sdd/<task-slug>/`. That directory is gitignored (`.superpowers/sdd/.gitignore` = `*`), so these files never enter the diff. `docs/superpowers/{specs,plans}/` stays empty (`.gitkeep` only).
- Autonomous and agent-team runs: never stop to ask the user questions. The Team Lead runs brainstorming once, answers its own questions from the task prompt, and records the decisions in the task-tracking file in `.superpowers/sdd/<task-slug>/` (defined in the prompt). Teammates skip brainstorming and planning skills and work only inside their assigned zone.
- Never spawn agents beyond the ones defined in the prompt; reuse open ones. This overrides subagent-driven-development.

### Karpathy guidelines (/karpathy-guidelines)
- Apply to every coding task: think before coding (state assumptions), simplicity first, surgical changes, goal-driven execution with verifiable success criteria.
- In autonomous runs, write assumptions into the task-tracking file instead of asking the user.
- Dead code: remove only what the current task orphans or replaces (see "Cleanliness"). Do not delete unrelated pre-existing dead code; report it.

### Ponytail (/ponytail)
- Use ponytail in full mode: reuse existing components, utilities and the standard library before writing new code.
- Ponytail limits what is built beyond the spec, never the spec itself. Requirements from the task (visual and functional parity, reusable components, tests) are not over-engineering.
- Before each commit, run /ponytail-review on the diff and apply the findings. Once per phase, run /ponytail-audit, then /ponytail-debt to collect deferred shortcuts.

## Code quality standards (/principe)

All code you write or change must comply with the principles in /principe. If you have not read them in this session, read them before starting the task.

### Structure
- No monoliths: one module, component or function is responsible for one thing.
- Don't overload code: if a file has grown (guideline: more than 300 lines) or a function does several things, split it.
- Reuse existing components, utilities and the design system; don't duplicate them.

### Cleanliness
- Don't leave legacy: when replacing an implementation, delete the old one. No "for compatibility" wrappers, no "just in case" leftovers.
- Don't leave dead code behind (anything your change leaves unused): unused functions, imports, variables, commented-out blocks, stub flags.
- Delete only what is confirmed unused: check both the graph (callers) and a project-wide search.
- The legacy rule applies to what you replace as part of the task. Don't touch someone else's outdated code; if it gets in the way, report it.

### Safe changes
- Don't break working logic: make minimal changes within the task and don't refactor unrelated code.
- All previously created tests must pass. Before finishing, run the tests, the linter and the build.
- Don't bend tests to fit the code. If a test has to change, explain why.
- New logic ships together with new tests.

### Commits
- Create all commits only through /git-commit-helper. Never call `git commit` directly.
- Commit per task as it lands, on the phase branch — don't hoard a whole phase in the working tree until the end.
- Don't bypass hooks (`--no-verify`) and don't force-push unless explicitly asked.
- Commit only after tests, linter and build have passed.

### Before finishing a task
- [ ] Complies with /principe
- [ ] No monoliths or overloaded files
- [ ] No legacy or dead code
- [ ] Working logic is untouched
- [ ] Tests, linter and build pass
- [ ] Diff reviewed with /ponytail-review
- [ ] Verified per verification-before-completion
- [ ] Commit made through /git-commit-helper, message in English