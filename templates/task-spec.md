# T-xx — <title: one goal>

Difficulty: <hard | medium | simple>
<!-- Obor picks the model from this line (config `difficulty`); `obor spec set` refuses a spec without it.
hard: state machines, crash/replay, concurrency, cross-package redesign. medium: a specified feature or fix in one or
two packages. simple: mechanical with a full oracle. Optional: a line `Model: <model>[, <model>…]` directly under
Difficulty (e.g. `Model: opus`) tries those models first, then the level's. Any model may name an effort,
`<model>:<effort>` (`Model: opus:high`, `sonnet:low`): the agent starts at that effort. Delete this comment. -->

<1–3 sentences: why, and the source of truth — design doc section, review finding R-xx #n, file:line.>

Guarantee: <per step: kernel | rule | refusal — docs/design.md "Change rules">
Replaces: <the mechanism this makes redundant, deleted in this task; or why it stays and until which release; or none>
Adds: <each new state, step, verb, fact, Message field, config key; or none>

## Do (<files/area>)
- <concrete change 1>
- <concrete change 2>

## Tests
- <tests to add, with the fixture approach>
- Fix tasks: one test per entry point / branch in the finding's Extent; the first action after setup is the named
  entry, no conditional steps.
- Identity, MCP or launch tasks: `make conformance TIER=live TURN=1 KIND=<kind>` for each affected kind (claude, codex,
  opencode) before `task_done`, summary lines in the report. Delete this line otherwise.

## Protected files this task may edit
<!-- Optional: delete it when the task edits no protected file (protected_files in .obor/config.json). One exact path
per backtick span, no globs: the worker's guard lets the lane edit these for this task only. Which files a task owns
may still be said in prose above, for planning: nothing reads it. -->
`<exact/path>`

## Out of scope
- <tempting adjacent work that belongs to another task>

## Report
Summary / Self-check / Skipped / Open questions — the report you pass to `task_done`.
