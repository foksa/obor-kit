# T-xx — <title: one goal>

Difficulty: <hard | medium | simple>
<!-- Obor picks the model from this line (config `difficulty`); `obor spec set` refuses a spec without it.
hard: state machines, crash/replay, concurrency, cross-package redesign. medium: a specified feature or fix in one or
two packages. simple: mechanical with a full oracle. Optional: a line `Model: <model>[, <model>…]` directly under
Difficulty (e.g. `Model: opus`) tries those models first, then the level's. Any model may name an effort,
`<model>:<effort>` (`Model: opus:high`, `sonnet:low`): the agent starts at that effort. A `hard` spec is reviewed
once it merges, no line needed (T-932); the line `Review: on merge` under the `Model:` line (or under Difficulty when
there is none) asks for the same review on a medium or simple task that is judgement-heavy (identity, MCP, launch). A
fix of a review's findings: the line `Fixes: RV-<n>[ <what to check>]` in its place, never with `Review: on merge`
(its verify is the review; T-924, T-928; `obor spec set` refuses the pair, T-932), links the task to that open
review (obor dispatch refuses another review); serve verifies once the linked tasks merged, the free text the
reviewer's note. Optional: a line `Ref: <id>` names the task in an external tracker (a Jira or Linear id, e.g.
`Ref: JIRA-12`); `obor status --json` carries it as the task's `ref`. Optional: a line `Priority: high` puts a waiting
dispatch first for the next free lane (`obor spec set` refuses any other value, T-1030). Delete this comment. -->

<1–3 sentences: why, and the source of truth — design doc section, review finding R-xx #n, file:line.>

Guarantee: <per step: kernel | rule | refusal — docs/design.md "Change rules">
Replaces: <the mechanism this makes redundant, deleted in this task; or why it stays and until which release; or none>
Adds: <each new state, step, verb, fact, Message field, config key; or none. A new state or value: every consumer
that reads the field it appears in, and what each does with it (a test per consumer)>

## Do (<files/area>)
- <concrete change 1>
- <concrete change 2>

## Tests
- <tests to add, with the fixture approach (a fake, not real git, outside `internal/checks` and the CLI)>
- Goes: <the tests this change makes redundant, deleted in this task; or none>
- Fix tasks: one test per entry point / branch in the finding's Extent; the first action after setup is the named
  entry, no conditional steps.
- Identity, MCP or launch tasks: the project's live agent check, if it has one, summary lines in the report. Delete this line otherwise.

## Protected files this task may edit
<!-- Optional: delete it when the task edits no protected file (protected_files in .obor/config.json). One exact path
per backtick span, no globs: the worker's guard lets the lane edit these for this task only. Which files a task owns
may still be said in prose above, for planning: nothing reads it. -->
`<exact/path>`

## Out of scope
- <tempting adjacent work that belongs to another task>

## Report
Summary / Self-check / Skipped / Open questions — the report you pass to `task_done`.
