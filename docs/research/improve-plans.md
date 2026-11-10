# Improvement plans (audit of 2026-11-10)

Written by the improve skill's audit at commit `a73e814c`. Each plan is self-contained: an executor needs only the
plan file and the repo. They are in `docs/research/` at the owner's choice (not the skill's default `plans/`).
Each plan carries the spec lines (`Difficulty`, `Guarantee`, `Replaces`, `Adds`) the lead needs to turn it into an
obor spec. Executors: read the plan fully before starting, honor its STOP conditions, update your row when done.

## Execution order & status

| Plan | Title | Priority | Effort | Depends on | Status |
|------|-------|----------|--------|------------|--------|
| [001](improve-001-nightly-race-run.md) | The nightly runs the suite once under the race detector, non-gating | P1 | S | none | DONE (T-1169, 2ecee62) |
| [002](improve-002-web-host-allowlist.md) | obor web answers only to loopback names and `--allow-host` names | P1 | S | none | DONE (T-1170, 2ecee62) |
| [003](improve-003-contract-docs-drift.md) | `docs/module-map.md` and `AGENTS.md` say what the code is today | P2 | S | none | DONE (T-1171, 2ecee62) |
| [004](improve-004-linux-decision.md) | Decide Linux: run the suite there once, then test it or stop claiming it | P3 | S-M | none | DONE (decided: stop claiming Linux, T-1172) |
| [005](improve-005-app-rig-in-process.md) | `internal/app`'s rig tests run in `testing/synctest` bubbles on in-memory connections (A1 step 2; 11 pieces, P0 the pilot) | P1 | L (P0: M) | none; its pieces: P1-P9 after P0, P10 last | DONE (T-1175, T-1185-T-1194, a9cc53b) |
| [006](improve-006-cli-version-change.md) | Serve tells the lead when an agent CLI or the terminal changed version (A8) | P2 | M | none | DONE (T-1176, T-1195) |
| [007](improve-007-accepted-findings-file.md) | `obor review close --accept` writes `docs/accepted.md`, not `BACKLOG.md` (A7) | P2 | S-M | none | DONE (T-1178) |
| [008](improve-008-status-obor-skew.md) | `obor status` names the agents whose calls come from another obor release (A5) | P2 | S | none | DONE (T-1177, T-1196, T-1197) |

Status values: TODO | IN PROGRESS | DONE | BLOCKED (with one-line reason) | REJECTED (with one-line rationale)

## Dependency notes

- None of the four depends on another, and they touch different files: 001 `Makefile`, `.obor/config.json`,
  `tests/release/makefile_test.go` and one sentence of `AGENTS.md` (line 71); 002 `internal/web`, `cmd/obor/web*.go`,
  `cmd/obor/follow_test.go`, `internal/help/help.go`, `skills/lead/commands.md`; 003 `docs/module-map.md` and one
  sentence of `AGENTS.md` (lines 22-23); 004 one new file in `docs/research/`.
- 001 and 003 both edit `AGENTS.md`, in different paragraphs. Run them one after the other, or expect a trivial merge.
- 001 reports data races; fixing each one is a later task of its own.
- Three of the plans leave a sentence of `docs/design.md` for the lead (it is protected): each plan's Maintenance
  notes names it.

### Plans 005-008 (the owner's four architecture ideas, planned 2026-11-10 at `65389c8c`)

- **A5 and A8 are two plans, not one.** They do not share data: 008 reads the callers' obor version from request
  envelopes (serve has it in memory); 006 reads each CLI's own version with `--version` and herdr's from the
  snapshot, which serve does not hold today for the CLIs. They also end in different places (a status line; a line
  to the lead). Their one shared file is `internal/app/app.go`, each adding one field to a different struct.
- **A8's first step is answered**: every agent CLI prints its version with `--version` in 23-80 ms, so the kind
  protocol and the manifest need no new field (plan 006, "Why this matters").
- **All four can start at once**: 005's pilot (P0), 006, 007 and 008 own different files:
  - 005 P0: `internal/herdr/socket.go`, `internal/herdr/panes.go`, `internal/herdr/herdrtest/socket.go`,
    `internal/herdr/socket_test.go`, `internal/app/rig_ext_test.go`, `internal/app/rig_test.go`, three
    `internal/app/*_ext_test.go` files. P1-P9: other `internal/app/*_ext_test.go` files only, one family each, no
    rig file (plan 005's "Pieces" table has the list). P10: four `_ext_test.go` files, the rig, `tests/budget.json`,
    one sentence each in `AGENTS.md` and `docs/module-map.md`.
  - 006: `internal/app/versions.go` (new) and its test, `internal/app/{app,loop,project,rules,describe}.go`,
    `internal/app/describe_test.go`, `internal/app/event/steps.go`, `internal/launch/versions.go` (new) and its
    test, `cmd/obor/serve.go`, one entry in `docs/module-map.md`.
  - 007: `cmd/obor/reviewcycle.go` and its integration test, `cmd/obor/review.go` (a comment),
    `internal/app/{reviewclose,review}.go` and their in-package tests, `internal/app/port/workflow.go` (comments),
    `skills/lead/{SKILL,commands,review-brief}.md`, `templates/README.md`, `templates/BACKLOG.md`.
  - 008: `internal/version/`, `internal/app/{app,status}.go`, `internal/app/status_test.go`,
    `internal/app/wire/status.go`, `internal/view/view.go`, `cmd/obor/status.go`, `cmd/obor/status_test.go`.
- Shared files, all trivial merges: `internal/app/app.go` (006 and 008, different structs); `docs/module-map.md`
  (006 and 005's P10, different sentences; P10 runs last anyway).
- Against the tasks running at planning time: T-1170 (`internal/web`) and T-1171 (`docs/module-map.md`) had merged;
  none of the four touches `internal/web`, the `Makefile` or the config. `AGENTS.md` is touched only by 005's P10
  (one sentence in "Tests"), after T-1169 and T-1172.
- **005 needs one decision before its pilot is dispatched**: P0 adds an optional field to the herdr client
  (`herdr.Socket.Conn`, about 15 production lines) so the fake herdr can be reached without a unix socket. The
  plan's section "One decision for the lead" has the alternative and why it costs more.
- **007 leaves the one-time move to the lead**: the task does not touch `BACKLOG.md` (the lead edits it in the main
  checkout); the plan's Maintenance notes have the command (a dry run moved 67 of the 91 Open bullets, 38 KB to 20 KB).
- 006 and 008 each leave sentences of `docs/design.md` and `docs/state-model.md` for the lead (Maintenance notes).
- Numbering: 005 is A1 step 2, 006 is A8, 007 is A7, 008 is A5.

## How the audit was done

Read directly by one session, no subagents. Read in depth: `AGENTS.md`, `BACKLOG.md`, `docs/decisions.md`,
`docs/observations.md`, `docs/module-map.md`, `internal/web/web.go`, `bin/obor`, `install.sh`, `internal/run/run.go`,
`internal/project/lock.go`, `internal/checks/nightly.go`, the socket's request handling in `internal/app/status.go`,
the Makefile's check targets, the web page's HTML sinks, this project's config and its state files (the last nightly,
the flake log). Findings already accepted in `BACKLOG.md` or decided in `docs/decisions.md` were not re-reported.

Not audited: the merge loop's logic in `internal/app` and the shell guard `internal/guard/shell.go` beyond samples
(each release's reviews cover them); performance (earlier reviews measured it); `scripts/release`, `scripts/publish`,
`scripts/ship`; `opencode/obor.js`; the web page beyond its HTML sinks and API client. `staticcheck` could not be
run (the session's permission check refused fetching it), so whether it finds anything is open. The test suite was
not run by the audit; the nightly on `a73e814c` was GREEN with one flake saved by its retest.

## Architecture ideas (not yet plans)

In [improve-architecture.md](improve-architecture.md), each with its facts, a first step and what that step decides.
Where an idea starts with a plan above:

| Idea | First step | Linked plan |
|------|------------|-------------|
| A1. A single-threaded, deterministic core | The nightly's race count, then an in-process path for the test rig | 001 is step 1; 005 is step 2 |
| A2. Second implementations (rex or herdr-core, pi) against the contract tables | A "what obor needs from a terminal" list, a research note | none |
| A3. obor-kit's serve follows a quieter build | Decide only if one of its four costs recurs | none |
| A4. The review cycle as its own leaf package | A split in the `app/identity` / `app/outbox` pattern | none |
| A5. Version skew visible in `obor status` | A status line from data serve already has | 008 |
| A6. `obor stats`: weekly numbers, rule firings first | List the rule steps the log records | none |
| A7. Accepted findings out of `BACKLOG.md` | Where `obor review close --accept` writes | 007 |
| A8. Serve says when an agent CLI or the terminal changed version | Whether a kind can report its CLI version | 006 (answered: `--version`, no manifest field) |
| A9. Needs-you answered from another device | One more write route on `obor web` | after 002 and a login |
| One serve, several devices | Viewer devices, then remote checks, then remote lanes | 002 (viewers), 004 (a Linux check machine) |

## Findings considered and rejected

- The shim's 2 s `has hook` watchdog misfiring under load and denying agents' tools: zero occurrences of its denial
  text in 902 agent transcripts of the past 8 days.
- The shim-hook tests' flakes (`TestShimHookSkipsHangingDevBuild` and three siblings, 16 flake-log entries): nearly
  all pass on the retest; their 1 s bound was already raised twice (0.3 s, 0.5 s), and a third raise continues a fix
  chain for seconds of retest time.
- `project.Lock.Held()` takes the flock for an instant, so a probe can make a concurrent `Try` fail: a microsecond
  window, and serve starts rarely.
- The nightly sweep rewrites `flakes.jsonl` without a temp file while a check may append: once a day, loses at most
  one flake entry.
- `{@html}` in the web page (`web/src/lib/md.ts`): the renderer escapes everything first and builds its own tags.
- The app socket trusts its callers: the socket file is owner-only, and same-user trust is the design (the guard is
  an accident guard, not a security boundary).
- A GitHub Actions workflow for the whole suite as a finding of its own: folded into plan 004 as option B, since
  whether Linux is a target is the owner's decision first.
