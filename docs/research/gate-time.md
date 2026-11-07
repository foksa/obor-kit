# Why the merge gate is slow, and what cuts it (researcher, 11-07)

## Recommendation

1. **Tier the gate (option C), narrowly and first.** Mark the tests that take 1 s or more in `cmd/obor`, `internal/checks`
   and `internal/setup` as `testing.Short()`-skipped, run `check-affected` with `-short`, and add one full `go test ./...`
   (no `-short`) to `merge.nightly_commands`. It cuts the gate's CPU the most per line changed. These three packages hold
   about 63% of the measured test CPU, and `internal/app` is the rest (§2). Estimate: ~60 lines, median big-set gate
   190 s → ~110 s, about 60–120 gate-min/day saved (§4). **Nightly runs no `go test` today** (`.obor/config.json`:
   `nightly_commands` = `conformance`, `trim_go_cache`), so tiering without that line would leave the slow tests running
   nowhere.
2. **Make a test-only change affect one package** (new, not one of A/B/C). `internal/affected/affected.go:55-60` treats
   `foo_test.go` like `foo.go`: it marks the package changed, and every package that imports it is then selected. A
   `_test.go` file is never imported by another package, so its change can only break its own package. 37 of 284 merges
   since 11-01 that touched cmd/internal Go (13%) were test-only. About 15 lines, saving ~9 gate-min/day (estimate).
   Risk: none that I can see. An xtest package (`foo_test`) is the same package directory.
3. **Do not start option A as an "event signal" project.** It would save about no wall time (§3). Do one small thing
   instead: `loopRig.has`/`steps` re-read and decode the whole event log on every 5 ms poll (`internal/app/loop_test.go:357-373`
   → `event.ReadLog`). Cache the read by file size. Whether polling CPU matters is **unmeasured** (§6).
4. **Do not start another pruning wave (B).** The wave is done and the suite has since grown faster than it shrank (§5).
5. Load is the multiplier, not test length: packages run alone take 20–61 s, but in the gate they take 95–150 s (§1).
   Fewer test-CPU-seconds per gate is the only thing that helps, which is why 1 beats A and B.

## 1. Where the gate time goes

**Gate wall time** (from `.obor/state/obor.db`, `events` where `step like 'gate%'`, intent → result):
- 1037 gate runs, 10-25..11-07. Median 144 s, p90 275 s.
- 11-01..11-07: 526 green/red gates, 1632 gate-min (233 min/day). The two busiest days: 11-04 126 gates, 403 min; 11-05
  143 gates, 552 min. The gate is serial (one merge worktree, `obor-kit-wt/merge`), so those minutes are queue time
  for merges.
- Medians since 11-01: affected, 4-5 packages 102 s; affected, 9+ packages 189 s; full suite (`33 pass`) 189 s; red 219 s.
- Per step, warm cache, load 22: `make build` 2 s, `go vet ./...` 1 s, `make deadcode` 2 s, `obor affected go` 1 s. `go test`
  is the whole gate; the other steps are noise (a cold cache costs more; not measured).

**Per package in the gate** (`.obor/state/logs/T-*-check_affected.log`, 301 logs, 11-04..11-07 `ok` lines):

| package | in last 40 gates | median in gate | alone (my run, 11-07 11:09) | tests / top-level CPU alone |
|---|---|---|---|---|
| cmd/obor | 100% | 94 s (max 393) | 37 s | 185 / 94 s |
| internal/app | 97% | 100 s (max 409) | 50 s | 623 / 183 s |
| internal/checks | 49% | 138 s (max 300) | 60 s | 73 / 114 s |
| internal/setup | 46% | 46 s | 18 s | 82 / 65 s |
| internal/launch | 51% | 38 s | – | – |
| tests/upgrade | 38% | 35 s | – | – |
| internal/view, web | 81% | 7 s, 10 s | – | – |
| event, wire, mcp, herdr | 27–38% | 0.2–2 s | – | – |

- The gate takes 2–2.5× as long as the same package alone. During my runs the load average was 22–57 on 10 cores
  (`sysctl hw.ncpu` = 10), with four lanes and the gates running at once.
- The largest log sample, 11-07 10:3x (T-947), had `cmd/obor` 351 s and `internal/app` 409 s. It went RED on
  `TestLoopRejectUnpins` and `TestLoopFinalOutcomes`, each a 60 s `waitFor` timeout at load 54 (`flakes.jsonl`).
- Affected sets: median 6 packages in the last 40 gates (median 12 over all 279 partial logs; the full suite is 33).
  `cmd/obor` and `internal/app` are in nearly every set, so "affected" removes the small packages and almost never the
  two big ones.
- **Reds are 15–19% of gate time.** Since 11-04: 50 reds, 212 of 1065 gate-min. 38 of them were followed by a green
  for the same task, which is my proxy for a flake: 158 min. Since 11-01: 75 reds, 305 min. `flakes.jsonl` has 211
  records in 14 days; the most frequent are `cmd/obor` as a package (19), `TestOpencodePlugin` (9),
  `TestStartBusyOnFirstPrompt` (6), `TestGithooks` (5), `TestWorkerStartRule` (5). Many are subprocess tests in
  `cmd/obor`, not `internal/app` rig waits.

## 2. Per-test times (plain `go test -json ./<pkg>`, one at a time, no -count/-race)

Load average at start: setup 57, checks 48, cmd/obor 35, app 24 (end 26). Files: `…/scratchpad/{internal_setup,internal_checks,cmd_obor,internal_app}.json`.

- **internal/app**: 623 tests, 49.5 s wall. 422 take under 0.1 s, 140 under 1 s, 61 take 1–5 s (Σ 102 s). Top:
  `TestStopFailureLandsNearItsDeadline` 4.0, `TestLoopMovedConflictResolved` 3.4, `TestWatchedSetPostsOneLineAfterTheLast`
  3.0, `TestVerdictLandsAfterRestartOnce` 3.0. The `TestLoop*`/`TestDrive*`/`TestWatch*`/`TestIdle*` families sit at 1.7–3.4 s.
- **internal/checks**: 73 tests, 60 s wall. `TestMovedGateCountFourInARow` 17.4, `TestResolvedPinsOwnMergeOnly` 6.4,
  `TestResolvedRefusesMismatchUntilEditRestored` 5.0, then ~20 scratch-repo tests at 3.3–4.6 s (real `git`
  clone/commit/merge/rerere in temp repos). 29 tests ≥1 s hold Σ 109 s of 114 s.
- **cmd/obor**: 185 tests, 37 s wall (incl. `TestMain`'s `go build -o`, `cmd/obor/main_test.go:29`, once per run).
  `TestOpencodePlugin` 5.2, `TestPreToolUseHangingBinaryDenies` 4.7, `TestLeadWakesAStoppedProjectFirst` 4.7,
  shim tests 2.2–2.4. These run the real `obor` binary as a process; the "hanging binary" ones wait out a real timeout.
  16 tests ≥1 s hold Σ 39 s of 94 s.
- **internal/setup**: 82 tests, 18 s wall. Pane/placement tests `TestSetupPaneTypesTheEnvAtAReusedPane` 5.8,
  `TestPaneRefusesUnknownLaneAndAmbiguousPlacement` 4.2, `TestPaneLabelSetAndRefused` 3.9 …; 18 tests ≥1 s hold Σ 46 s of 65 s.
- **tests/upgrade**: 35 s in the gate (7 tests; `TestUpgradeCheck` skips without `OBOR_UPGRADE_CHECK=1`; the fixture tests and
  `TestGuideSteps` run). Not re-measured alone.
- Top-level CPU sum over the four packages: 456 s. Tests ≥1 s across them: 296 s (65%).

What the slow tests wait on: real git in temp repos (`checks`), real subprocess spawns and a built binary (`cmd/obor`),
fixed sleeps/real timeouts (`TestPreToolUseHangingBinaryDenies`), and in `internal/app` the 20 ms merge-loop tick
(`loopRig.tick`, `loop_test.go:239-244`): one test needs N loop passes × 20 ms real time.

## 3. Option A: event signals instead of polling waits

Sites (greps over `*_test.go`):
- `internal/app`: `r.waitFor` 325 call sites, `waitStep` 142, `waitJob` 68, `waitAfter` 21, `waitFacts` 17, `waitPrefix` 14, `waitCall` 8,
  `waitBriefDelivered` 5, `waitWake` 5, `waitQuiet` 4, `waitTimer` 4, `awaitStop` 4. About 615 call sites over ~12 helpers, in
  four copies of `waitFor` (`app_test.go:204`, `loop_test.go:308`, `concurrency_test.go:342`, `lanescan_test.go:315`).
  `time.Sleep` 20, `time.After` 33.
- `cmd/obor`: `waitUntil`, `waitFor`, `serveRig.waitFor` (3 + 3 sites), `time.Sleep` 17. `internal/launch`: `awaitReports` 3,
  `time.Sleep` 6. `internal/checks`: `time.Sleep` 5.
- `herdrtest.Server` already has `Hold/reached`, `WaitSubscribed` and `Before` hooks (`socket.go:231,314,288`); an App-side signal does not exist.

Why it saves little time:
- The polls are 1 ms (`rig.waitFor`) or 5 ms (`loopRig.waitFor`). A poll adds at most that much latency per wait, against
  tests that take 1–4 s. The wait is for the app's own work, which runs on a 20 ms tick.
- A signal still needs a deadline (`waitCap` 60 s, `loop_test.go:291-306`). The failing waits in the 11-07 logs are those
  deadlines expiring at load 54. A signal wakes sooner but does not remove "the machine did not run the app in 60 s".
  It would only turn a lost wake (a real bug) into an immediate failure.
- It can save CPU: each `loopRig` poll decodes the whole JSONL log. **I did not measure how much CPU that is.**
- Change size to do it properly: App exposes a record-appended channel (production code, new surface), 4 helper copies → 1,
  ~300 lines. The cheap variant (cache `records()`) is ~40 lines.
- Estimate: gate minutes saved 0–5%; flakes removed: only lost-wake ones, count unknown (the `TestLoop*` 60 s timeouts at
  load 54 would still fail); risk moved: none.

## 4. Option C: tier the gate

What moves: tests ≥1 s in `checks`, `setup`, `cmd/obor` (§2: 194 s CPU of 273 s in those packages); `app` stays whole in the
gate (its 1–5 s tests are the loop-ordering coverage, §5) or goes second if more is wanted.

- Mechanism: `if testing.Short() { t.Skip(…) }` at the top of ~60 tests; `-short` in `check-affected`'s `go test`; a nightly
  command `go test -p N ./...` without `-short`. Change ~60–80 lines in tests, 2 in Makefile, 1 in config. `make check` (full) keeps no `-short`.
- Gate minutes: contended `checks` 138 s, `cmd/obor` 94 s, `setup` 46 s would fall to roughly a third. `internal/app` (100 s)
  is then the wall: median big-set gate ~190 s → ~110 s. That is ~40% of 233 min/day = **~90 min/day** (7-day average) or
  ~190 min/day on a 11-04/05-style day. Tiering `app`'s ≥1 s tests as well takes the median under ~60 s, at a risk cost (§5).
  These are estimates from alone-times and the gate/alone ratio, not a measurement.
- Flakes removed: the `cmd/obor` subprocess and `checks` git tests are about half of `flakes.jsonl` names, so maybe a third
  to a half of reds from flakes (a guess from the names; ~79 min since 11-04). `internal/app`'s remain.
- Risk that moves after merge: a regression in git conflict/rerere/moved-gate handling (`checks`), the hook/shim/process
  paths (`cmd/obor`), and setup pane placement is found at 02:00 instead of at merge, with up to a day's merges to bisect
  (~100 on a busy day). `merge-without-regate.md` already accepts a similar trade for clean moves (T-914).
  Mitigation: the lead picks the `-short` set by area; tests of a file changed in the merge could still run in the gate (not designed here).

## 5. Option B: finish the pruning plan

`docs/research/test-pruning.md` §6 P1–P12, merged 11-04:

| plan | task | test lines −/+ (git numstat, merge vs first parent) |
|---|---|---|
| P1 config keys | T-830 | −209/+69 |
| P2 cmd/obor | T-831 | −307/+144 |
| P3 checks | T-832 | −325/+219 |
| P4 app (a) | T-833 | −206/+176 |
| P5 app (b) | T-834 | −523/+301 |
| P6 setup | T-835 | −725/+147 |
| P7 launch | T-836 | −793/+140 |
| P8 guard | T-837 | −247/+235 |
| P9 small pkgs | T-838 | −528/+295 |
| P10 conformance | T-839 | −86/+18 |
| P11 store migrations | T-840 | −158/+69 |
| P12 v0.26 removals | T-846 | not measured |
| review restores | T-842, 843, 844, 845, 849 | −55/+390 |

- Done: all twelve are merged. Test lines removed −4,162, added +2,203: **net −1,959 lines against ~12,000 planned (~16%)**.
  The review rounds RV-108/109/111 sent back 390 lines of pruned scenarios.
- Total test lines (`cmd`, `internal`, `tests`, from git): 78,995 before T-830, 78,019 after T-849, **85,809 at HEAD**. The
  suite regrew +7.8k lines in three days (about 2.6k/day), four times the pruning's net.
- Time effect (median package time in gate logs, mixed load): `internal/app` 158 s (10-25..11-03) → 107 s (11-04) → 93 s (11-06..07);
  `cmd/obor` 95 → 88; `checks` 128 → 138; `setup` 32 → 57 (later days, so a load effect too); `launch` 44 → 36. Only `app`
  improved clearly, and load differs by day, so this is not a clean attribution.
- What is left: the survey's pool is not re-derived. The reviews showed statement coverage over-counts redundancy
  (390 lines restored), so a second pass costs review rounds for a smaller yield. Estimate for another 1–2k lines of cuts: ≤3% gate
  minutes, ~0 flakes, ~2 tasks plus reviews; the risk moved is the lost-scenario kind RV-109 found.

## 6. Affected-set computation: where it pulls more than needed

`internal/affected/affected.go` (135 lines), probed with `OBOR_CHANGED_FILES` on dist/obor:
- **A `_test.go` file selects its package's reverse dependencies** (lines 55-60; no special case). This is the one clear over-selection (§Recommendation 2).
- A one-file change in `internal/app/event/data.go` selects 11 packages including `tests/upgrade`, `mcp`, `setup`, `launch`, `checks`
  (T-943 ran 293 s). That is import-graph-correct (`go list -test` Deps, transitively; `tests/upgrade` imports `app/wire`, which imports `event`), but it is package granularity:
  a type added to a leaf package re-runs everything above it. `internal/view/view.go` → 4 packages; `internal/guard/guard.go` → 2; `internal/checks/checks.go` → 6;
  `internal/store/store.go` → 21; `internal/config/config.go` → 19.
- `cmd/obor` is in every set because it is `main` and its tests build and run the binary. That is correct, and it is why the set never goes below ~60-100 s of work.
- The `affected_full` globs (`Makefile`, `internal/testkit/**`, `internal/herdr/herdrtest/**`) force the full suite (the `33 pass` gates, median 189 s,
  185 of 526 gates since 11-01 were full-suite or full-class). I did not check how many of those were forced by a glob rather than by a mixed change; unknown.
- Not examined: whether `tests/upgrade` (35 s, 38% of gates) needs to be in the gate at all. Its fixture tests drive the real herdr stub and a built binary; it is a release check (`make upgrade-check` runs it before a tag).

## 7. Not verified / next measurements

- CPU spent in rig polling (A): profile `internal/app` with `-cpuprofile` once, outside the guard's limits, or ask the lead to run it.
- The 60-s waits at load 54: whether they are starvation or a lost wake. Re-running `TestLoopRejectUnpins` at load <10 would split the two.
- The per-test CPU numbers are one run each at load 24–57; the ratios hold, the absolute seconds drift.
- Gate-minute savings are estimates from alone-times and the gate/alone ratio. A real number needs one gate run with the proposed `-short` set.
- A manual loop-step hook for `internal/app` (drive passes instead of a 20 ms tick) is a fourth option I did not size. It targets the 1–4 s floor of the `TestLoop*` family directly.
