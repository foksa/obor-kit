# Test suite rework: a minute per merge, a few minutes for the full suite, and a size that holds

Plan (advisor, 2026-11-07 23:00, for the lead). Owner's brief: "a full and proper fix of the tests; stop all tasks if
needed; we went in circles for 5 hours and tests take longer and longer." Everything is stopped (dispatch paused,
promotion off, in-flight work in `keep/T-957`, `keep/T-982`, `keep/T-988`, `keep/T-991`). Agent-neutral: nothing here
depends on an agent kind. Inputs: docs/research/gate-time.md, docs/research/test-audit-external.md,
docs/plan/merge-flow-rework.md, the event log (`.obor/state/obor.db`), the lane and promotion logs
(`.obor/state/logs/`), and one `go test -short -json` per heavy package run at 22:54-22:58 with the swarm stopped
(load 17-27 on 10 cores; files under the session scratchpad `short/*.json`).

**Where the time is today, measured.**

| What | Measured | Source |
|---|---|---|
| Lane test step (`test[.N]`), since 11-06 | n=38, median **597 s**, p90 1442 s, max 2294 s | events, intent→result |
| Promotion full suite, 11-07 | 9 runs, **454-1015 s** | events `promote.*` |
| Slot gate (merge + compile), since 18:00 UTC | n=29, median 30 s, p90 64 s | events `gate*` |
| Per package in a lane (`-short`, 2 cores, load 20-50) | internal/app 300-520 s, internal/checks 375-548 s, cmd/obor 115-217 s, internal/launch 85-106 s, internal/setup 56-81 s, tests/upgrade 41-47 s | `T-979..T-990-check_affected.log` |
| Full-class lane run (a Makefile or testkit change: 32 packages at 2 cores) | cmd/obor, internal/app, tests/release each hit the **10-min package timeout** | `T-988-check.log`, `T-991-check.log` |
| Per package alone (`-short`, 10 cores, load 17-27, this session) | internal/app 90 s wall (241 test-s; 107 tests ≥1 s hold 202 s), internal/checks 84 s wall (55 test-s: ~30 s is the test binary's build), cmd/obor 41 s (130 test-s; 61 tests ≥1 s hold 91 s), internal/launch 23 s, internal/setup 18 s | scratchpad `short/*.json` |
| Promotion, per package (`-p 4`, load 20-30) | internal/checks 537 s, internal/app 398 s, cmd/obor 303 s, internal/config 175 s, tests/release 165 s, internal/launch 128 s, internal/guard 82 s, internal/setup 77 s | `promote-check.log` 22:42 |
| git processes per full run | internal/app 9,969 · internal/checks 9,341 · tests/release 5,177 · cmd/obor 3,295 · launch 2,041 · setup 2,031 · guard 1,127 (33,511 in all) | test-audit-external.md §3.3 |
| Suite size | 48,683 prod lines, 84,427 test lines (internal/app 15,762 vs 38,442; 654 tests in 135 files) | `wc` at HEAD |
| Flake rows since 11-06 | 71 (39 lane, 31 full check); top: `cmd/obor` as a package 7, `internal/checks` 6, `TestServeLoop*` 8 together, `TestLoopFinalOutcomes` 3, `TestPreToolUseHangingBinaryDenies` 2 | `.obor/state/flakes.jsonl` |

The gap between "alone, 10 cores" and "in a lane, 2 cores, loaded" is 3-6×. The lane's CPU budget
(`slots.lane_cpus` default 2: `GOMAXPROCS=2`, `GOFLAGS=-p=2` for every Go process, internal/config/config.go:859,871-879;
internal/checks/lanetest.go:76-78) is the owner's decision and stays: the suite has to be cheap at 2 cores.

## Recommendation, and the risks first

**Do four phases, in this order, and let nothing else merge during phase 0 and the first task of phase 1.** Phase 0 is
a day of deletions and Makefile/config changes that take the lane step from ~10 min to ~4-6 min for a typical task and
the promotion under 10 min. Phase 1 is the one hard task: internal/app's 212 loop-rig tests move from real git to an
in-memory scratch, which is where 9,969 git processes and most of the ≥1 s tests are; it takes internal/app from
300-520 s in a lane to about a minute. Phase 2 does the same shrinking, smaller, in internal/checks and cmd/obor and
deletes the third copy of the merge scenarios. Phase 3 is the nightly budget check and the rule that keeps the size,
without which this plan repeats the pruning wave of 11-04 (net −1,959 lines, regrown +7.8k in three days,
gate-time.md §5).

Target after phase 2, measured by the same commands as the table above: a lane test for a one-package task **60-90 s
at 2 cores** (internal/app -short ≤ 30 s alone, ≤ 80 s in a lane; the test binary's compile is most of the rest);
the promotion's full suite **3-5 min** at `-p 4` (internal/checks ~150 s and cmd/obor ~100 s are then the long poles);
git processes per full run under 12,000 (internal/app under 300). These are estimates from the per-test times in §1
and the measured git floor; §5 says how each phase is verified before the next starts.

Risks, most likely first:

1. **Scenarios lost in the internal/app move.** 5 files (43 references) build a real conflict by editing `shared.txt`
   on both sides; 6 "moved" tests commit to `main` for real. Scripting those outcomes in the fake can hide a case the
   real merge produced for free. Mitigation: T-E's spec lists every test whose assertion depended on git content and
   the internal/checks test that holds that behaviour (the §2 table is the start); the review compares the list, not
   coverage numbers (coverage-based pruning restored 390 lines, gate-time.md §5).
2. **The fake drifts from the adapter.** `concScratch` (internal/app/concurrency_test.go:27-203) already implements
   every `port.Scratch` method in memory for 72 tests, so this is not new ground, but a second fake with scripted
   outcomes is a second place the contract lives. Mitigation: one fake (`fakeScratch`, grown from `concScratch`), the
   outcome names taken from `port.GateOutcome`/`TestOutcome`/`PublishOutcome`, and the contract stated once in
   internal/app/port/effects.go:249-277 comments; internal/checks keeps a scenario table whose names match the fake's
   scripted cases (T-G).
3. **Load stays the multiplier.** A 30 s suite is 90 s at load 50; the plan makes the suite wait-bound rather than
   process-bound, which is exactly what survives load, but it does not control load. `slots.max` stays the owner's
   knob. The budget check (T-J) runs in the nightly at a quiet machine so its numbers are comparable.
4. **The lane step stops compiling.** T-A makes the lane run `go test` only. The slot compiled the same commit C
   seconds earlier (`check-compile`, Makefile:108-111) and `go test` type-checks its packages and runs vet's test
   subset, so nothing is lost for C; a test-only file that breaks the build is still caught by `go test`. The
   duplication removed is `make build` + `gofmt` + `go vet ./...` + `make deadcode` over the whole module at 2 cores.
5. **tests/release behind a build tag** leaves its compile errors to `check-release` and the nightly. The class
   `release` runs it in a lane when its paths change (`.obor/config.json` classes), `scripts/release` runs it before a
   tag, the nightly runs it; a Go refactor that breaks a release test's compile is found at the nightly, not the merge.
6. **T-E is a hard task over 48 test files.** It is one task because every family calls `newLoopRig`
   (loop_test.go:81) and the switch is at that one constructor; the tests that break are the ones that depended on
   git truth (risk 1). `Difficulty: hard`, `Review: on merge`, the lane to itself in phase 1.
7. **Not measured:** the lane step's time after T-A (the slot's identical steps take median 30 s at `-p 4`; at 2 cores
   under load I estimate 60-150 s); internal/checks' test-binary build share (84 s wall vs 55 test-s alone); what
   internal/config's 175 s in the promotion was (45 s in the next run: load, not tests).

## 1. Root causes, ranked by measured seconds

| # | Cause | Seconds | Where |
|---|---|---|---|
| 1 | **Git process storms in rigs that assert no git behaviour.** `newLoopRig` makes a real repo (`testkit.New`: init, add, commit, internal/testkit/testkit.go:101-109), commits a fixture, adds a worktree (`Lane`, testkit.go:164-177), commits the config (`Config`, testkit.go:184-217), then drives the loop through `gitScratch`, "the real adapter's steps over the fixture's git" (internal/app/scratch_test.go:20-24): one `git` process per step at scratch_test.go:64-70, 35 call sites in that file. 212 tests in 48 files use the family (`newLoopRig`, `newDispatchRig`, `newDriveRig`, `newLevelRig`, `newPoolRig`, `newWorkerRig`: dispatch_test.go:55, drive_test.go:25, difficulty_test.go:21, lanetype_test.go:23, driveretry_test.go:13). What they assert is states, steps, jobs and prompts. | 9,969 git processes per run; ≥150 s of process time at 15 ms each (audit §3.3). Alone -short: the 107 tests ≥1 s hold 202 of 241 test-s; the top eight are loop-rig conflict/resolved/moved tests at 3.9-9.7 s. Lane: 300-520 s. | internal/app/loop_test.go:81-121, scratch_test.go |
| 2 | **The lane step repeats the slot's compile tier.** `check-affected` starts with `GO_GATE` (build, gofmt, `go vet ./...`) and ends with `make deadcode` (`go run …/deadcode ./...`), Makefile:84-89, 107, 185-189, over the whole module at 2 cores, right after the slot ran the same four steps on the same commit (`check-compile`, Makefile:108-111; `commands.compile` in `.obor/config.json`). | Slot measurement of the same steps: median 30 s, p90 64 s at `-p 4` (n=29). In the lane at 2 cores: estimate 60-150 s per test step, before any test runs. | Makefile:84-111 |
| 3 | **Release and upgrade tests in the suite.** `go test ./...` includes tests/release (60 tests, 544 test-s, 5,177 git processes, real `make`/`sh`/bare origins; tests/release/release_test.go:68-130) and tests/upgrade's two frozen-binary runs (`TestUpgradeV0230ToV0231`, `TestUpgradeBrokenBuild`, tests/upgrade/upgrade_test.go:140-200; 74 s; host-dependent). `scripts/release` runs `check-release` again before every tag (Makefile comment at `check-release`); `make upgrade-check` runs the real previous release. | Promotion: tests/release 165-251 s; a full-class lane: 600 s timeout (T-991). tests/upgrade in 38% of lane sets at 41-47 s. | tests/release, tests/upgrade, Makefile:42-43 |
| 4 | **internal/checks: every test a fresh repo plus a `/bin/sh` git wrapper.** `newFx` builds a repo, two worktrees and a config commit per test (internal/checks/fixture_test.go:45-66); `newSx` routes every git call through a `git-cut` shell script that counts and may fail a call (scratch_test.go:29-52), so each git call is two process starts, for all tests, not only the cut tests. The seven `TestLaneTest*` take 3.9-4.8 s each (30 of 55 -short test-s); `specgate`/`mismatch`/`replay` build a conflict fixture per test (3+4+3 fixtures; 8-15 s each in the full tier, audit §3.3). 25 Slow marks, yet the package is 375-548 s in a lane under `-short`. | 9,341 git processes; alone -short 84 s wall (55 test-s), full 222 test-s; lane 375-548 s. | internal/checks/fixture_test.go:45-66, scratch_test.go:29-52, lanetest_test.go |
| 5 | **cmd/obor: a binary build per run, 190 subprocess calls, real timeouts, a third copy of the merge scenarios.** `TestMain` builds obor (cmd/obor/main_test.go:22-34); 190 `run(t, …)` calls in 29 files start it (main_test.go:45-67). The shim's hang tests wait out the real 2 s watchdog (bin/obor:105-120, `sleep 2 &` at :113, no env knob) with a 30 s `sleep` stub (shim_test.go:67-75, 377-394). `TestServeLoop*` (6 tests, serve_loop_test.go:322-469) drive the real adapter over real git with the fake herdr: the same scenarios internal/app (6 Moved, 9 Conflict, 9 Resolved tests) and internal/checks (2, 4, 8) hold; they are 8 of the 71 flake rows since 11-06. `TestOpencodePlugin` runs `node --test` (opencode_test.go:32-36; conformance `OC-S2` runs the same, tests/conformance/static.go); `TestClaudePlugin` runs `claude plugin validate` (plugin_wiring_test.go:205-220). | Alone -short 41 s (130 test-s; 61 tests ≥1 s hold 91 s); lane 115-217 s; promotion 303 s; one 10-min timeout (T-988). 3,295 git processes. | cmd/obor/main_test.go, serve_loop_test.go, shim_test.go, bin/obor:105-120 |
| 6 | **Wait and poll rigs.** Four `waitFor` copies (internal/app/app_test.go:204, concurrency_test.go:375, loop_test.go:308, lanescan_test.go:306); the loop rig polls every 5 ms and re-reads and decodes the whole event log each poll (loop_test.go:308-314, 357-373 → `event.ReadLog`); the app's merge loop runs on a 20 ms tick in tests (loop_test.go:239-244; production kicks it, loop.go:276-302) so a scenario of N passes costs N × 20 ms of real time; `waitCap` 60 s (loop_test.go:295) turns a loaded machine into a 60 s red (`TestLoopFinalOutcomes` ×3, `TestLoopRejectUnpins`). 36 `time.After` and 20 `time.Sleep` in internal/app tests; 867 `t.Parallel` calls, so at `GOMAXPROCS=2` the package is a queue of waits. | Not separable from #1 by measurement; the per-test floor of 1.7-3.4 s for `TestLoop*` (gate-time.md §2) is ticks plus git. | internal/app/loop_test.go:239-314 |
| 7 | **Affected-set fan-out.** A leaf change pulls 11 packages (gate-time.md §6); `cmd/obor` and `internal/app` are in nearly every set; `affected_full` globs (`Makefile`, `internal/testkit/**`, `internal/herdr/herdrtest/**`, `.obor/config.json`) force the whole suite into one lane at 2 cores: 32 packages, three 10-min timeouts (T-988, T-991). `keep/T-991` ("a lane test runs only the changed packages; importers are the promotion's") is written and parked. | Median set 6 packages; full-class lane ≈ 20 min. | internal/affected/affected.go:52-57, `.obor/config.json` `merge.affected_full` |
| 8 | **Duplicated scenario layers.** The merge loop's scenarios exist three times: cmd/obor (`TestServeLoop*`, real adapter + git + binary), internal/app (state machine over `gitScratch`, real git), internal/checks (the adapter over real git). Nightly: 2/12/6 tests; promotion: 1/21/8. The CLI layer should prove the CLI, not the loop. | cmd/obor's six: 16.7 test-s and the most flake rows of any family; the internal/app copies are #1. | cmd/obor/serve_loop_test.go, internal/app, internal/checks |
| 9 | **Subprocess tests with real timers elsewhere.** internal/checks runs `sleep 8` under a 1 s timeout (runner_test.go:44,55; nightly_test.go:124) and `sleep 30` cut tests (lanetest_test.go:195,215; lane_test.go:173; lanetable_test.go:89); internal/launch's `TestRestartSuspended` stops a real child and reads real `ps` (3.8 s); internal/setup's init tests run the built binary (2-2.8 s each, 22 tests ≥1 s hold 37 of 50 test-s). | Each 1-5 s; internal/setup 18 s alone, 56-81 s in a lane; internal/launch 23 s alone, 85-128 s in a lane or the promotion. | internal/checks/*_test.go, internal/launch/launch_test.go:1699, internal/setup/oborinit_test.go |

Not a cause: the `-short` tier itself (119 Slow marks skip 603 test-s in a lane; keep it), herdr or other tools run for
real (never; audit §1), and the `herdrtest` socket fake (in-process, 0.2-0.4 s packages).

## 2. Target test architecture

One sentence: **each package tests its own layer with the cheapest fixture that proves it; git runs in exactly one
suite; processes run in exactly three; the lane runs `-short` of the changed packages and nothing else; the promotion
runs everything; the nightly runs the rest and the budget.**

| Package | Tests what | With | Not with | Target (alone / lane 2 cores / promotion `-p 4`) |
|---|---|---|---|---|
| **internal/app** | the state machine: states, steps, folds, jobs, prompts, needs-you, nightly/promotion triggers, concurrency | in-memory ports only: `fakeScratch` (one, grown from `concScratch`: scripted outcomes per call — green, red, conflict, fail, moved, refused ff; a movable target head; pins as a map; `Candidate{Base, Patch}` from fake shas; `Test` with the `onTest` hook `gitScratch` has), `herdrtest` (in-process socket), `fakePanes`/`fakeLanes` (watch_test.go:23,90), the real `Kit` dir for role texts (read-only), a plain temp dir with `.obor/config.json` for the store and log (no `git init`); `Tick` 2 ms (as `newRig`, app_test.go:109-170 already does); one `waitFor` with a cached log read and a 20 s cap | real git, `sh -c` of configured commands (a scripted `sh` outcome is the fake's answer), the built binary | -short ≤ 30 s / ≤ 80 s / ≤ 60 s; 0 git processes except tests that say why (`disk_test`?); 0 Slow marks (nothing in the package should need a second) |
| **internal/checks** | the adapter over real git: Submit, Gate, Test, Resolved, Publish, pins, rerere, moved subjects, mismatch, lane prepare/free, lock rules, nightly and promotion effects, flakes, classify, boundaries | real git on fixture repos: one template repo built in `TestMain` (init, fixture commit, config commit) copied per test with `cp -R` + `git worktree repair` (lanes added per test only when needed); the `git-cut` shim installed only by `arm` (cut tests), plain git otherwise; one conflict fixture shared by the `specgate`/`mismatch`/`replay` tables; configured commands are `echo`/`exit` scripts (`sleep` only where the timeout itself is under test, ≤ 1 s) | serve, the merge loop, the binary | -short ≤ 45 s / ≤ 120 s / ≤ 150 s (full); git processes ≤ 6,000 |
| **cmd/obor** | the CLI contract: exit codes, output lines, hooks and the shim, config/validate, status/send/stop/spec verbs against a fake herdr, plugin wiring files | the one built binary (`TestMain`) for CLI tests; in-process `herdrtest` for verbs that need serve; stub binaries for the shim with the watchdog set by env (`OBOR_HOOK_TIMEOUT`, default 2 s; tests 0.3 s) | the merge loop over real git (`TestServeLoop*` deleted: the loop is internal/app's, the adapter internal/checks'), `node`, `claude` (conformance) | -short ≤ 30 s / ≤ 70 s / ≤ 100 s |
| **internal/setup**, **internal/launch**, **internal/guard**, **internal/view**, **internal/web**, **internal/config**, others | unchanged in kind: setup's init/pane tests (binary + herdrtest), launch's restore/start (herdrtest, fake `ps` except one), guard's hook rules | as today | — | each ≤ 60 s in the promotion; budgets in T-J; a later phase if a package exceeds its budget twice |
| **tests/release** | the kit's own release tooling (Makefile, scripts, install.sh) | as today, behind `//go:build release` | `go test ./...` | runs in the `release` class lane, `scripts/release`, the nightly |
| **tests/upgrade** | the upgrade guide and `make upgrade-check` logic | the pure tests (`TestGuideSteps`, `TestGuideV023`, `TestPrevious`, `TestUpgradeCheckRefusesOldPrev`, 0 s); `TestUpgradeCheck` env-gated as today | the two frozen-binary runs (deleted; `make upgrade-check` is the real check at each release) | ~0 s |
| **tests/conformance** | real tools' behaviour: herdr, claude, codex, opencode (`node --check`, `node --test`), `claude plugin validate` (new `CC-S3`) | `make conformance TIER=static` nightly; `TIER=live` by the task that touches identity/launch (AGENTS.md "Tests") | — | nightly |

**Per-merge vs promotion vs nightly, after the rework.**

| Tier | Runs | Where | Time |
|---|---|---|---|
| Slot `gate` | merge S onto T, `commands.compile` (build, gofmt, vet, deadcode), commit C, pin | serve's worktree, `-p 4` | 30 s median today, unchanged |
| Lane `test` | `go test -short -count=1 <changed packages>` only (T-991's set: the changed packages, no importers), the guard; no build/vet/deadcode | the task's lane, 2 cores | 60-90 s for a one-package task (test binary compile + the suite); a testkit/herdrtest/Makefile change: the whole `-short` suite, estimate 3-4 min at 2 cores after phase 2 (today 20 min, three timeouts) |
| Promotion | `make check`: compile tier + `go test -count=1 -p 4 ./...` (no `-short`, release tag off) + deadcode, one retest of the failing tests | the promote worktree | 3-5 min at load 20-30 (estimate from the per-package targets), ≤ 10 min hard budget |
| Nightly | `make check` + `go test -tags release ./tests/release` + `check-budget` (T-J) + conformance static + govulncheck + trim cache | the nightly worktree, quiet machine | ≤ 15 min; the only tier allowed to be slow |

## 3. The rule that keeps it from regrowing

Four lines for AGENTS.md "Tests" (the lead writes them; docs/design.md:536-572 "Test tiers and receipts" is rewritten
to the §2 table when T-E lands; the owner's rule that tests of a replaced mechanism go with it joins the spec template
`templates/task-spec.md`):

1. **A task that replaces or deletes a mechanism deletes that mechanism's tests in the same diff**, and its report's
   `Tests:` line says which (file, count). The integrator's accept checks the line against the diff: a replacement
   that keeps the old tests is sent back. (observations.md: "Replacing a mechanism grows; deleting one shrinks".)
2. **Every package has a budget** in `tests/budget.json`: wall seconds for `-short` at 2 cores and for the full tier
   at `-p 4`, git processes per run, Slow marks allowed. The nightly's `check_budget` (T-J) measures them on the quiet
   machine (a counting `git` first on `PATH`, `go test -json`) and is RED, with one needs-you line, when a package is
   over budget, a top-level test takes ≥ 1 s without `testkit.Slow`, or a Slow mark has no comment naming the real
   time it needs. A budget is raised only by an owner line in docs/decisions.md; it is lowered by any task that gets
   under it (the task edits the file).
3. **Layers.** internal/app tests start no process: every port is faked in memory (`fakeScratch`, `herdrtest`,
   `fakePanes`/`fakeLanes`). Git behaviour is tested in internal/checks only; CLI behaviour in cmd/obor only; a
   scenario appears in one layer, the layer whose code decides it. A new rig constructor in a package that has one is
   a review send-back ("one rig per package": internal/app's seven wrappers around `newLoopRig` collapse to options).
4. **A test that waits waits on a condition, never a fixed sleep**, with the rig's one `waitFor` (cap 20 s); a real
   timer in a test is a budget exception named in a comment (the timeout under test, the watchdog under test).

Why a budget and not a line count: lines did not predict time (the 11-04 pruning cut 1,959 lines and the gate barely
moved, gate-time.md §5); seconds and process counts are what the lane pays, and the nightly can measure both without a
reviewer. Why the nightly and not the lane: lane numbers are load-dependent 3-6×; the nightly at 02:00 is the one
comparable run per day.

## 4. Phased plan

Each phase leaves the suite green and the lane and promotion measurably faster; §5 names the measurement. "alone"
means `make check` dispatches nothing else (the owner's "stop all tasks if needed"); "∥" means the tasks may run in
parallel lanes with each other (disjoint files, independent lane tests).

**Phase 0: stop the bleeding (one day; T-A alone first, then T-B ∥ T-C ∥ T-D).**

| Task | Difficulty | What | Files | Leaves (expected) |
|---|---|---|---|---|
| **T-A** lane tests only | simple | `check-affected` (the lane's `commands.check_affected`) runs `go test -short -count=1 -p $CHECK_CPUS <pkgs>` and nothing else: no `GO_GATE`, no `make deadcode` (the slot's `check-compile` did both on C; `check`, the promotion's, keeps them); `GO_CACHE` selection stays. `check-readers`/`check-web` the same. | Makefile:88-107, 149-167; `.obor/config.json` `commands.check_affected` (timeout 1200 → 900) | lane step −60..−150 s (estimate; the slot's same steps are 30 s median at `-p 4`) |
| **T-B** changed packages only | medium (written: `keep/T-991`, rebase) | the lane's set is the changed packages; importers run at the promotion | internal/affected, Makefile, docs | median set 6 → 1-2 packages: a one-package task's lane step = that package's `-short` time (internal/app 300-520 s today) |
| **T-C** release and upgrade out of the suite | simple | `//go:build release` on tests/release; `check-release`, `scripts/release` and the nightly pass `-tags release`; `deadcode` gets `-tags conformance,release`; remove T-991's `TEST_TIMEOUT ?= 20m` and its `-timeout` flags (Go's 10-min default again); delete `TestUpgradeV0230ToV0231` and `TestUpgradeBrokenBuild` (tests/upgrade/upgrade_test.go:140-200) and the `offline()` helper if unused | tests/release/*_test.go (tag line), tests/upgrade/upgrade_test.go, Makefile:169-189, `.obor/config.json` `merge.nightly_commands` (+`check_release`) | promotion −165..−250 s CPU (−544 test-s), tests/upgrade 74 s → ~0; no full-class lane can time out on tests/release |
| **T-D** shim watchdog knob | simple | bin/obor `has_hook` reads `OBOR_HOOK_TIMEOUT` (default 2) for its `sleep`; the five hang/stale tests set 0.3; `hang` const 30 s → 3 s (shim_test.go:68) | bin/obor:105-120, cmd/obor/shim_test.go, shim_hashook_test.go, plugin_wiring_test.go | cmd/obor −15 s per run; the hang family's flake rows go |

Phase 0 result (estimate): lane step median 597 s → 250-400 s for a typical task (internal/app's own time is the
floor until phase 1); promotion 454-1015 s → 400-700 s; full-class lane runs no longer hit the tests/release timeout.

**Phase 1: internal/app in memory (the hard task; T-E alone in its phase; T-F after it).**

| Task | Difficulty | What | Files | Leaves (expected) |
|---|---|---|---|---|
| **T-E** `fakeScratch` and the loop rig on it | hard, `Review: on merge` | one in-memory `fakeScratch` (grow `concScratch`, concurrency_test.go:27-203: scripted outcomes per method and task, a target head that `move()` advances, pins, `Candidate{SHA, Target, Base, Patch, Stat, Diff}` from fake shas, `Resolved`/`Publish`/`Landed`/`Unpin`, `Test` with `onTest`, `AfterMerge` counter, `arm` for the failure rule); `newLoopRig` builds a plain project dir (`testkit.NewDir`: config written, no git, no worktree) and wires `fakeScratch`; `Tick` 2 ms; every test that edited `shared.txt`, committed to `main` or read git (79 `.Git(` calls, 7 `Lane` adds in internal/app tests) is rewritten as a scripted outcome, with the internal/checks test that holds the git behaviour named in the spec's table (risk 1); `gitScratch` (scratch_test.go, 391 lines) deleted; `concScratch` replaced by the one fake; the seven rig wrappers become options of one constructor | internal/app/*_test.go (48 files of the family, concurrency_test.go, scratch_test.go), internal/testkit/testkit.go (+`NewDir`) | internal/app -short 90 s → ≤ 30 s alone, 300-520 s → ≤ 80 s in a lane; full ≤ 60 s at the promotion; git processes 9,969 → < 300; the 60 Slow marks in internal/app removed as each test drops under 1 s |
| **T-F** one wait | medium | one `waitFor` for the package (loop_test.go:308, app_test.go:204, lanescan_test.go:306 and the conc rig's), the log read cached by file size (`records()`, loop_test.go:357-373), poll 1 ms, cap 20 s; `time.Sleep`/`time.After` in tests replaced by conditions where they wait for the app (a timer under test stays, with a comment) | internal/app/*_test.go | flakes of the "60 s wait" kind fail in 20 s with the records printed; −CPU per wait (unmeasured; gate-time.md §3) |

Phase 1 result: a one-package internal/app task's lane step ≈ 60-90 s (the goal); the promotion's internal/app
398 s → ~60 s.

**Phase 2: the other two layers (T-G ∥ T-H; may start ∥ T-E since the files are disjoint, if lanes are free).**

| Task | Difficulty | What | Files | Leaves (expected) |
|---|---|---|---|---|
| **T-G** internal/checks on shared fixtures | medium | `TestMain` builds the template repo once; `newFx` copies it (`cp -R`, `git worktree repair`) and adds lanes only when the test uses them; `git-cut` installed by `arm` only (newSx → plain git); one conflict fixture for the `specgate`/`mismatch`/`replay` tables; `TestLaneTest*` 7 → 3 (green restores, red restores, dirty refused; the env and discard cases as subtests of one run); `sleep 8`/`sleep 30` cases → `sleep 1`/`sleep 3` with the timeout scaled; Slow marks re-checked against 1 s | internal/checks/fixture_test.go, scratch_test.go, lanetest_test.go, specgate_test.go, mismatch_test.go, replay_test.go, runner_test.go, nightly_test.go | -short 84 s → ≤ 45 s alone (test-s 55 → ~25), lane 375-548 s → ≤ 120 s; full 222 test-s → ~120; git processes 9,341 → ≤ 6,000 |
| **T-H** cmd/obor is the CLI | medium | delete `TestServeLoop*` and `serveRig` (serve_loop_test.go, 510 lines; each scenario named with its internal/app and internal/checks holder in the spec); `TestOpencodePlugin` keeps its file asserts and drops the `node --test` run (conformance `OC-S2`); `TestClaudePlugin` → conformance `CC-S3` (`claude plugin validate`); `TestGithooks` uses the stub binary `hx.binary` instead of `go build` (internal/setup/githooks_test.go:213,226), the one real-binary hook case stays in cmd/obor | cmd/obor/serve_loop_test.go, opencode_test.go, plugin_wiring_test.go; internal/setup/githooks_test.go; tests/conformance/static.go | cmd/obor -short 41 s → ≤ 30 s alone, lane 115-217 s → ≤ 70 s, promotion 303 s → ~100 s; −8 flake rows/day of the `TestServeLoop*` kind; internal/setup −4.6 s and a compile |

Phase 2 result: promotion 3-5 min at `-p 4` (internal/checks ~150 s and cmd/obor ~100 s the long poles); a
full-class lane run (`-short`, 2 cores) ≈ 3-4 min.

**Phase 3: durability (T-J alone for its Makefile line; T-K, T-L ∥).**

| Task | Difficulty | What | Files | Leaves |
|---|---|---|---|---|
| **T-J** the budget check | medium | `tests/budget.json` (per package: `short_s`, `full_s`, `git_procs`, `slow`), seeded from the phase-2 nightly numbers ×1.5; `make check-budget`: a counting `git` wrapper first on `PATH`, `go test -json -count=1 ./...` (full) and `-short` runs, compares, prints one `FAIL budget: <pkg> <metric> <got> > <max>` per breach and `budget OK: N packages`; `commands.check_budget` in `merge.nightly_commands`; a top-level test ≥ 1 s without `Slow`, or `Slow` without a comment, is a breach | Makefile, `tests/budget.json`, `cmd/obor` (a small `obor budget` reader is not needed: a shell loop over `go test -json` with `jq` is enough, or a Go test helper under tests/budget behind a tag), `.obor/config.json` | the nightly says when the suite regrows, by package and metric, before the lane pays for it |
| **T-K** the rules | simple (lead) | AGENTS.md "Tests" gets §3's four lines; docs/design.md:536-572 rewritten to §2's table; `templates/task-spec.md` gets the `Tests:` report line and the "which tests go" question; the integrator's role text gets the accept check (rule 1) | AGENTS.md, docs/design.md, templates/task-spec.md, templates/agents/integrator.md, docs/agents/integrator.md | — |
| **T-L** the `affected_full` decision | simple | with the suite at phase-2 size, a change to `internal/testkit/**`, `internal/herdr/herdrtest/**` or `Makefile` runs the whole `-short` suite in the lane (today's behaviour, now 3-4 min) and the promotion runs the full tier; `.obor/config.json`'s `affected_full` globs stay. Decision for the owner: alternatively the lane runs only testkit's own tests and the window closes at the promotion. Default: keep the full `-short` run. | docs only, or `.obor/config.json` | no 20-min lane |

Order and parallelism, in one line: **T-A** (alone) → **T-B ∥ T-C ∥ T-D** → **T-E** (its own phase; T-G and T-H may
run beside it in other lanes) → **T-F** → **T-G ∥ T-H** if not already run → **T-J** → **T-K ∥ T-L**. Restore
`keep/T-957` (the `-short` tier, already the lane's behaviour through `check-affected`'s `-short`: check whether the
branch still adds anything), `keep/T-982` and `keep/T-988` after phase 0; they are unrelated to this plan and ordinary.

## 5. How each phase's time is verified

The same three commands before and after, by the lead, with the swarm paused (load under 10; `uptime` in the note):

1. `go test -short -count=1 -json ./internal/app ./internal/checks ./cmd/obor ./internal/launch ./internal/setup`, one
   package at a time, and for each: the package `Elapsed`, the count and sum of top-level tests ≥ 1 s (the script in
   the session scratchpad `short/` did this; 20 lines of Python or `jq`). Phase 0 changes nothing here; phase 1 must
   show internal/app ≤ 30 s; phase 2 internal/checks ≤ 45 s and cmd/obor ≤ 30 s.
2. The git process count per package: a `git` wrapper first on `PATH` that appends one line per call (the audit's
   `gitcount.sh`), one plain `go test -count=1 ./<pkg>`. Phase 1: internal/app < 300. Phase 2: internal/checks ≤ 6,000.
3. The production numbers after three days of merges: `test` step durations (the SQL in this plan's header: intent→result
   pairs for `step like 'test%'`), the promotion's `promote.*` durations, and the per-package `ok` lines in the newest
   `.obor/state/logs/T-*-check_affected.log` and `promote-check.log`. Phase 0: lane median under 400 s, no tests/release
   in `promote-check.log`. Phase 1: a one-package internal/app lane run under 90 s. Phase 2: promotion under 300 s at
   load 20-30.

A phase whose number does not land is not followed by the next: the lead posts the two tables (before, after) in
BACKLOG.md "Handoff" and the owner decides.

## 6. What this plan does not do

- It does not touch `slots.lane_cpus` (2), `slots.max` (4) or the promotion weight: the owner's load decisions stand;
  the suite is made to fit them.
- It does not prune by coverage. Every deletion names the mechanism that went (release/upgrade binaries, the third
  scenario layer, real git under the state machine) or the test that holds the behaviour elsewhere.
- It does not add a test framework, a generated fake or a dependency; `fakeScratch` is a struct with maps and a script,
  in the test package, like `concScratch` today.
- It does not fix internal/launch, internal/setup, internal/config or internal/guard beyond T-H's githooks change:
  each is under 60 s in the promotion once load drops; the budget (T-J) says if one needs a task later.
- Not verified: the estimates in §4's "Leaves" column are derived from the per-test times in §1 and the measured git
  floor, not from a prototype; §5 is how they become measurements.
