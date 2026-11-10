# Plan 001: The nightly runs the suite once under the race detector, as a non-gating line

> **Executor instructions**: Follow this plan step by step. Run every verification command and confirm the expected
> result before moving to the next step. If anything in the "STOP conditions" section occurs, stop and report; do not
> improvise. When done, update the status row for this plan in `docs/research/improve-plans.md`, unless the lead
> dispatched you and keeps the index.
>
> **Drift check (run first)**: `git diff --stat a73e814c..HEAD -- Makefile .obor/config.json tests/release/makefile_test.go AGENTS.md`
> If any of these changed since this plan was written, compare the "Current state" excerpts against the live files
> before proceeding; on a mismatch, treat it as a STOP condition.

## Status

- **Priority**: P1
- **Effort**: S
- **Risk**: LOW
- **Depends on**: none
- **Category**: tests
- **Planned at**: commit `a73e814c`, 2026-11-10

Spec lines, if the lead turns this into an obor spec: `Difficulty: simple` · `Guarantee: rule (a nightly report line;
it gates nothing)` · `Replaces: none` · `Adds: commands.race, one nightly.also_run entry, the make target check-race`.

## Why this matters

`obor serve` is a concurrent Go daemon (one mutex over the app state, a goroutine per socket request, the herdr
event stream, watchers). The Go race detector has never run on it: no Makefile target passes `-race`, and the
project's guard denies `go test -race` to every agent. Yet the guard's own message and `AGENTS.md` say "the gate and
nightly run the rest". The flake log (`.obor/state/flakes.jsonl`) held 219 entries at planning time, and the
project's working assumption (`docs/observations.md`) is that flaky reds track machine load; without a race run
nobody can tell a timing flake from a real data race. After this plan the
nightly prints one extra line with the number of data race reports, and keeps the full output when there is one.

## Current state

Files:

- `Makefile`: every check target. No target contains `-race` (`grep -n -- '-race' Makefile` prints nothing).
- `.obor/config.json`: this project's obor config. `nightly.also_run` lists the non-gating nightly commands;
  `commands.<name>` defines each.
- `tests/release/makefile_test.go`: tests of Makefile targets, behind `//go:build release`.
- `AGENTS.md`: the rules for agents; its "Tests" section has the sentence this plan makes true.

`Makefile` helper every check target starts with (lines 110-112):

```make
GO_CACHE = c=$$($(GO) env GOCACHE 2>/dev/null); \
	{ mkdir -p "$$c" && : > "$$c/.obor-probe" && rm -f "$$c/.obor-probe"; } 2>/dev/null || \
	  { GOCACHE="$${TMPDIR:-/tmp}/obor-go-build"; export GOCACHE; }
```

The pattern of an existing test-running target (`Makefile`, `check-retest`):

```make
check-retest:
	@$(GO_CACHE); \
	rc=0; out=$$($(GO) test -count=1 -tags integration $${CHECK_CPUS:+-p $$CHECK_CPUS} -run "$$TESTS" $$OBOR_FAILED_PKGS 2>&1) || rc=$$?; printf '%s\n' "$$out" >&2; \
	n=$$(printf '%s\n' "$$out" | grep -c '^ok' || true); f=$$(printf '%s\n' "$$out" | grep -c '^FAIL[[:space:]]' || true); \
	echo "go test: $$n pass, $$f fail"; test "$$rc" = 0 || exit "$$rc"
```

`.obor/config.json`, the nightly block and one existing non-gating command:

```json
  "nightly": {
    "at": "02:00",
    "slow_check": { "cmd": "make -s vulncheck", "timeout": 600 },
    "also_run": ["conformance", "release_tests", "check_budget", "trim_go_cache"]
  },
  "commands": {
    "check_budget": { "cmd": "make -s check-budget", "timeout": 1800 },
```

How the nightly reports a non-gating command (`internal/checks/nightly.go:119-140`, do not change it): it runs the
command, takes the **last non-blank line** of its output, strips a leading `<name>: ` and prints
`<name> (non-gating) <that line>`; on a nonzero exit it prints `WARN <name> (rc N, non-gating): <that line>` instead
and never turns the nightly RED. The runner (`internal/checks/runner.go:149-153`) keeps the full output of a command
that exits nonzero in `.obor/state/logs/nightly-<name>.log` and removes that file when the command passes. So a
command named `race` whose last line is `race: 3 data race report(s), …` and whose exit is 1 gives a WARN line and a
kept log `.obor/state/logs/nightly-race.log`. That is the design this plan uses.

`AGENTS.md:70-71`:

```
- No load runs (T-543): one plain `go test <pkgs>` at a time — `-count` > 1, `-race` and two test commands at once
  (`&`, `xargs -P`, `parallel`) are denied by the guard for every role; the gate and nightly run the rest.
```

The existing pattern for testing a Makefile target with a stub `go` (`tests/release/makefile_test.go`,
`TestOborKitGoFlagsTrimpath`, around line 1079): it writes an executable shell stub, runs
`exec.Command("make", "-s", "-C", repo, "<target>", "GO="+stub)` with `repo, _ := filepath.Abs("../..")`, and asserts
on the output. The helper `writeExec(t, path, script)` exists in that package. Model the new test on it.

Conventions: comments say what a thing is and why in full sentences, citing a task id where one exists; Makefile
recipes are one `@`-prefixed shell line continued with `; \`; a failing step prints a line starting `FAIL`.

## Commands you will need

| Purpose | Command | Expected on success |
|---|---|---|
| The new target on one small package | `make -s check-race RACE_PKGS=./internal/spec` | last line `race: 0 data race report(s), 0 package(s) with failing tests under -race`, exit 0 |
| The new test alone | `go test -count=1 -tags release,integration -run '^TestOborKitCheckRace$' ./tests/release` | `ok  	obor/tests/release` |
| Config check | `bash bin/obor config validate` | exit 0, no line starting `FAIL` |
| The Makefile's own class check | `make -s check-release` | last line `go test: 1 pass, 0 fail` |

Run one test command at a time (the guard denies two at once). Never run `make check` (the full suite) yourself.

## Scope

**In scope** (the only files you should modify):
- `Makefile`
- `.obor/config.json`
- `tests/release/makefile_test.go`
- `AGENTS.md` (one sentence)

**Out of scope** (do NOT touch):
- `internal/checks/nightly.go`, `internal/checks/runner.go`: the nightly's reporting already does what this needs.
- The guard's deny rule in `.obor/config.json` (`rules.deny_commands`) and its message: agents still must not run
  `-race` themselves; the message becomes true.
- `docs/design.md`: protected, the lead's. Its sentence about the nightly's non-gating commands is listed under
  Maintenance notes for the lead.
- Fixing any data race the new run reports. Each one is its own task.

## Git workflow

- If you are an obor worker (your job came through `job_next`): work in your lane, make no commits, finish with
  `task_done`.
- Otherwise: work in a separate git worktree on a branch `advisor/001-nightly-race-run` cut from `staging`, never in
  the main checkout (the project's live `obor serve` runs `dist/obor` from there, and `make check-release` rebuilds
  it). Commit message style: one sentence saying what is true now, e.g. `The nightly runs the suite under the race
  detector as a non-gating line`. Do not push.

## Steps

### Step 1: Add the `check-race` target

In `Makefile`, after the `check-retest` target, add:

```make
# check-race (commands.race, the nightly's non-gating race line): the suite once under the race detector. Only a data
# race counts here: a test that fails on timing under -race's slowdown is the nightly check's business, not this
# line's, so the last line is the number of race reports and the exit is 1 only when there is one (the nightly then
# keeps the whole output, .obor/state/logs/nightly-race.log). A run in which no package ran (-race needs cgo off
# macOS; a build error) is a FAIL line and exit 2, never "0 reports". RACE_PKGS narrows it to a task's own package.
RACE_PKGS ?= ./...
check-race:
	@$(GO_CACHE); \
	out=$$($(GO) test -race -count=1 -tags integration $${CHECK_CPUS:+-p $$CHECK_CPUS} $(RACE_PKGS) 2>&1) || true; printf '%s\n' "$$out" >&2; \
	ran=$$(printf '%s\n' "$$out" | grep -c -e '^ok' -e '^FAIL[[:space:]]' || true); \
	if [ "$$ran" = 0 ] || printf '%s\n' "$$out" | grep -q -e '\[build failed\]' -e '\[setup failed\]'; then echo "FAIL race: the race build did not run"; exit 2; fi; \
	n=$$(printf '%s\n' "$$out" | grep -c 'WARNING: DATA RACE' || true); \
	f=$$(printf '%s\n' "$$out" | grep -c '^FAIL[[:space:]]' || true); \
	echo "race: $$n data race report(s), $$f package(s) with failing tests under -race"; test "$$n" = 0
```

Add `check-race` to the `.PHONY` line, and one clause to the header comment at the top of the file, next to the
`make check-budget` clause: `` `make check-race` → the nightly's non-gating race line (commands.race), ``.

**Verify**: `make -s check-race RACE_PKGS=./internal/spec` → last line is exactly
`race: 0 data race report(s), 0 package(s) with failing tests under -race`; `echo $?` prints `0`.

### Step 2: Test the target's decision with a stub `go`

In `tests/release/makefile_test.go` add `TestOborKitCheckRace`, modelled on `TestOborKitGoFlagsTrimpath`. The stub is
a shell script that answers `go env GOCACHE` with a temp directory path and, for any other call, prints a canned
output file. Table (canned output → expected exit code and last output line):

| Case | Canned `go test` output | Exit | Last line |
|---|---|---|---|
| clean | `ok  \tobor/internal/a\t0.1s` | 0 | `race: 0 data race report(s), 0 package(s) with failing tests under -race` |
| timing failure only | `--- FAIL: TestX (1.00s)`, `FAIL`, `FAIL\tobor/internal/a\t1.2s`, `ok  \tobor/internal/b\t0.1s` | 0 | `race: 0 data race report(s), 1 package(s) with failing tests under -race` |
| a race | `WARNING: DATA RACE` twice, then `FAIL\tobor/internal/a\t1.2s` | 1 | `race: 2 data race report(s), 1 package(s) with failing tests under -race` |
| build did not run | `go: -race requires cgo; enable cgo by setting CGO_ENABLED=1` | 2 | `FAIL race: the race build did not run` |
| build failed | `FAIL\tobor/internal/a [build failed]` | 2 | `FAIL race: the race build did not run` |

Each case is a `t.Run` with `t.Parallel()`, its own `t.TempDir()`, and an assert that the stub was called with
`-race` in its arguments (have the stub append `"$*"` to a log file, as the header comment of that test file
describes for its fake go).

**Verify**: `go test -count=1 -tags release,integration -run '^TestOborKitCheckRace$' ./tests/release` → `ok`.

### Step 3: Name the command in the project's config

In `.obor/config.json`:
- under `commands`, add `"race": { "cmd": "make -s check-race", "timeout": 1800 }` (no `heavy` key, like
  `check_budget`);
- in `nightly.also_run`, insert `"race"` directly before `"trim_go_cache"` (the trim empties the Go build cache, so
  the race build must come before it).

**Verify**: `jq -r '.nightly.also_run | join(" ")' .obor/config.json` →
`conformance release_tests check_budget race trim_go_cache`; and `bash bin/obor config validate` exits 0 with no line
starting `FAIL`.

### Step 4: Make the `AGENTS.md` sentence say where

In `AGENTS.md` line 71, replace `the gate and nightly run the rest.` with
`` the gate and nightly run the rest (the nightly runs the suite under `-race` as a non-gating line, `make check-race`). ``

**Verify**: `grep -c 'make check-race' AGENTS.md` → `1`.

### Step 5: Run the Makefile's own class check

**Verify**: `make -s check-release` → last line `go test: 1 pass, 0 fail`.

## Test plan

- New: `TestOborKitCheckRace` in `tests/release/makefile_test.go`, the five cases above. It is a decision test over
  canned output; it starts `make` and a shell stub only, never a real race build.
- Changed or deleted tests: none.
- The real behaviour (a real `-race` build) is covered once by step 1's run on `./internal/spec`; quote its last line
  in your report.

## Done criteria

- [ ] `grep -c 'check-race' Makefile` prints at least `3` (header clause, `.PHONY`, the target)
- [ ] `make -s check-race RACE_PKGS=./internal/spec` exits 0 with the line from step 1
- [ ] `go test -count=1 -tags release,integration -run '^TestOborKitCheckRace$' ./tests/release` passes
- [ ] `jq -e '.commands.race.cmd == "make -s check-race"' .obor/config.json` prints `true`
- [ ] `bash bin/obor config validate` exits 0
- [ ] `make -s check-release` ends `go test: 1 pass, 0 fail`
- [ ] `git status --short` lists only the four in-scope files (and the index, if you keep it)

## STOP conditions

Stop and report back (do not improvise) if:

- The guard denies `make -s check-race …` (the deny rule matches commands that start with `go … test`; if it also
  catches this, the lead runs the verification).
- Step 1's run prints `FAIL race: the race build did not run` on the owner's Mac: the assumption "on macOS `-race`
  needs no cgo" is false for this toolchain.
- Step 1's run on `./internal/spec` reports a data race (count above 0). Report the output; do not fix it here.
- `bash bin/obor config validate` prints a `FAIL` for `nightly.also_run` or `commands.race`.
- `make -s check-release` fails in a test other than the one you added, twice.

## Maintenance notes

- The first nights will show how long the whole suite takes under `-race` and how many reports there are. If the
  command hits its 1800 s timeout, narrow it in the config instead of raising the timeout:
  `"cmd": "make -s check-race RACE_PKGS='./internal/app/... ./internal/herdr/... ./internal/launch/... ./internal/run/... ./internal/checks/...'"`.
- A `WARN race (rc 1, non-gating): N data race report(s) …` line means `.obor/state/logs/nightly-race.log` holds the
  reports. Each distinct race is one task; the file is replaced by the next failing night and removed by a clean one.
- "Packages with failing tests under -race" above the nightly check's own count is expected: timing-bound tests slow
  down under the detector. It is information, not a failure.
- For the lead: `docs/design.md` (around line 605) lists what the nightly runs from `nightly.also_run` ("the static
  tier and the budget"); add the race line there.
- Making the line gating is the owner's call once the count has been zero for a while.
- The count is also the first measurement of `docs/research/improve-architecture.md` A1 (a single-threaded core):
  many reports say the lock discipline leaks in practice; zero for two weeks says A1's value is in the tests only.
