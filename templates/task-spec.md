# T-xx — <title: one goal>

Difficulty: <hard | medium | simple>
<!-- Obor picks the lane type from this line (config `difficulty`); `obor spec set` refuses a spec without it.
hard: state machines, crash/replay, concurrency, cross-package redesign. medium: a specified feature or fix in one or
two packages. simple: mechanical with a full oracle. Delete this comment. -->

<1–3 sentences: why, and the source of truth — design doc section, review finding R-xx #n, file:line.>

## Do (<files/area>)
- <concrete change 1>
- <concrete change 2>

## Tests
- <tests to add, with the fixture approach>
- Fix tasks: one test per entry point / branch in the finding's Extent; the first action after setup is the named
  entry, no conditional steps.

## Protected files this task may edit
<!-- Optional: delete it when the task edits no protected file (protected_files in .obor/config.json). One exact path
per backtick span, no globs: the worker's guard lets the lane edit these for this task only. Which files a task owns
may still be said in prose above, for planning: nothing reads it. -->
`<exact/path>`

## Out of scope
- <tempting adjacent work that belongs to another task>

## Final message
Summary / Self-check / Skipped / Open questions — the same text as the `task_done` tool's report.
