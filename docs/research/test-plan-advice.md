# Test plan after the architecture refactors (advisor, 2026-11-20)

For the lead. Owner's brief 11-20: after the architecture refactors, plan the tests. Read-only: I ran no test. The
numbers are from the event store (`.obor/state/obor.db`, `test` intent → result, which includes the wait for a check
slot), `.obor/state/flakes.jsonl`, `git diff --numstat`, and greps of the tree at `5a1cc702`.

## Recommendation

1. **Fix what the lane runs before touching any test.** The gate is cheap per run today: a Go lane test is 61 s on
   average, the slot's compile 7 s. Over a third of the lane test seconds since 11-10 went to two causes that are
   configuration, not tests: a file no class matches runs the full suite (55 runs), and a change to Go files plus a
   skill or template runs the big packages twice (26 runs at 140 s against 61 s). Two tasks (TP-1, TP-2).
2. **Keep importers and the integration tag in the lane. Do not move anything to the nightly.** `flakes.jsonl` has
   seven red lane tests for 11-20 that stayed red on the retry. All seven were real, and each failed on a test the
   worker's own run does not reach: three on a test behind the integration tag, two on `cmd/obor` tests reached only
   as an importer, two on cross-package checks. That is the lane check doing its job. The cheap change is on the
   worker's side: its one run before `task_done` passes `-tags integration` (TP-4).
3. **One decision for the owner on CPUs.** `checks.cpus_per_check: 5` does not reach a lane test: a lane test runs at
   `lanes.cpus`, which is unset, so 2 (`internal/checks/lanetest.go:78-81`). Since the 21:53 config change the machine
   runs two lane tests at once at 2 CPUs each. I recommend that a lane's heavy check runs at `cpus_per_check`, so the
   config means what its commit message says (TP-3).
4. **Finding 5: yes, move the core's flow tests to a terminal-neutral fake, as the last step and in three tasks.**
   The move is smaller than the review said. The 292 tests reach the fake herdr almost only through the rig: 162
   lines in 22 test files touch it directly. So: split the rig by concern (a move), make the test files speak the rig
   only, then swap what is under the rig. It is the lowest-value part of this plan (of the last week's 36 commits to
   the package's tests, at most 15 changed a rig file and at most 6 the fake; the package takes 9.4 s), so it goes
   last, and TP-10 is the task to cut if the plan has to shrink.
5. **Delete by level, not by coverage.** Three concrete items (TP-5, TP-6, TP-7). Nothing to delete among the tests
   of fakes. The DD-tests-v0.33 delete list and the review's N3 are already done.
6. **Five rules** (below) so a refactor stops adding four test lines per production line: today's waves A to D added
   1,884 test lines and 459 production lines, 37 new tests against 13 deleted.

## 1. Finding 5: the core's tests on a neutral fake

**What is there, measured.**

- `internal/app/rig_ext_test.go` (3,791 lines, 300 functions) builds the app on `herdr.Panes{S: c}` over
  `herdrtest` (`:457-465`, again at `:2178-2194` and `:3042-3055`). The app takes five things from herdr there: the
  panes, `ProcessInfo`, `Screen`, the prompter, and a `Refresh` test on `*herdr.Error`.
- Outside the rig file, 22 of the 49 other external test files name the fake: 71 calls on the pane table (`r.f.Set` 25,
  `r.f.Pane` 18, `r.f.Agent` 16), 51 on the socket (`Requests` 7, `Push` 7, `DropConns` 6, `Die` 5), and herdr method
  names as strings (`"notification.show"` 18, `"session.snapshot"` 14). `loop_ext_test.go` (26 lines),
  `watchdog_ext_test.go` (25), `messages_ext_test.go` (20) and `prompt_ext_test.go` (19) hold more than half.
- The rig is not dead weight: of 571 helper names in the two rig files, 7 have one caller or none. It is a large
  vocabulary. `newLoopRig` has 162 call sites in 35 files and already takes options (`:1897-1925`); the small
  constructors are `refusedRig`, `verbsRig`, `restartRig` (3 calls each) and `newAdmitRig` (6).
- Churn since 11-13: 36 commits changed `internal/app`'s tests; 6 changed `rig_ext_test.go`, 9 `rig_test.go`, 3 each
  `herdrtest`'s two files. "Every new scenario extends the rig" holds for at most four commits in ten.
- `herdrtest` stays central whatever happens here: 14 test files of `cmd/obor`, 7 of `internal/herdr` and 4 of
  `internal/setup` import it.

**Why still do it.** The owner's trial runs on hc. The core's flow tests state how a pane appears and changes in
herdr's terms: the rig pushes a `workspace_created` event to make the app read a new snapshot (`:2021`, `:2067`).
A scenario stated as a `port.Event` is the contract both adapters have to meet; a scenario stated as herdr's wire is
not. That, and the layer rule (a core test imports no adapter), are the gain. Time is not.

**How.**

- A new fake in `internal/testkit`, not the in-package `fakePanes` grown. `fakePanes` is a struct of scripted answers
  for table tests that call `connect`, `event` and `check` themselves; its `Subscribe` returns an error on purpose
  (`internal/app/rig_test.go:3936-3938`). It stays as it is.
- The fake is a pane table, one stream and a call log, with no behaviour of its own:

  ```go
  // Term is an in-memory terminal for the core's tests.
  type Term struct{ /* panes, stream, calls, scripted errors */ }

  func (t *Term) Panes() port.Panes                               // Subscribe, Snapshot, Notify
  func (t *Term) ProcessInfo(ctx, pane) (port.ProcessInfo, error)
  func (t *Term) Screen(ctx, pane string, lines int) (string, error)
  func (t *Term) Write(ctx, pane, text string, submit bool) error // with SendKeys: the prompter the Loop takes

  func (t *Term) Add(p port.Pane)                    // a created event
  func (t *Term) Set(id string, fn func(*port.Pane)) // a status event carrying the new pane
  func (t *Term) Remove(id string, exited bool)      // closed or exited
  func (t *Term) End(err error)                      // the stream ends; Refresh() gives a port.Refreshed error
  func (t *Term) Fail(op string, err error)          // the next call of op fails
  func (t *Term) Calls(op string) []Call
  ```

  It gets no tests of its own. If it needs one, it has grown logic that belongs in an adapter.
- Three sets stay on a real adapter and its fake, as the boundary tests: `restore_integration_test.go` (herdr),
  `restorehc_ext_test.go` and `adopthc_ext_test.go` (hc).
- `herdrtest.Server.answer` (459 lines) stays one switch. It reads top to bottom and changed three times in a week.

**The rig split (TP-8), a move only.** One file per rig type with its methods:

| New file | Holds (today's lines) |
|---|---|
| `rig_bubble_ext_test.go` | `bubble`, `wallWatch`, `poll`, `clock`, `syncBuf` (`rig_ext_test.go:101-357`) |
| `rig_observe_ext_test.go` | `rig`, `newRig` (`:359-690`) |
| `rig_loop_ext_test.go` | `loopRig`, its options, start, stop, `Launch`, `Stop`, waits, store reads (`:1839-2400`) |
| `rig_dispatch_ext_test.go` | `dispatchFakes`, `cutPrompter`, `fetchRig` (`:691-1260`) |
| `rig_lines_ext_test.go` | `sent`, `rigInbox`, `lineRig`, `refusedRig` (`:2397-3000`) |
| `rig_roles_ext_test.go` | `codexRig`, `verbsRig` (`:3381-3791`) |
| `rig_harness_test.go` | `harness`, `fakePanes`, `fakeLanes`, `newTestApp`, the ident harness (`rig_test.go:1931-2245`, `:3904-4225`) |
| `rig_conc_test.go` | `admitRig`, `concFakes`, `concRig` (`:148-1100`) |
| `rig_workflow_test.go` | `FakeWorkflow`, `fakeLane`, `landedScratch`, `schedScratch` |
| `rig_review_test.go` | `reviewGitFake`, `reviewRig`, `fixRig` (`:2871-3480`) |
| `rig_prompt_test.go` | `boxPrompter`, `startAgents`, `promptRig`, the spies (`:2271-2700`) |
| `rig_serve_test.go` | `memListener`, `MemServe`, `startJobFakes` (`:3610-3900`) |

The worker settles the exact cut; the acceptance is `go test -list` equal before and after and no changed function
body.

## 2. What to delete or merge

| # | What | Evidence | Change |
|---|---|---|---|
| a | The lead-home rule's rows at the CLI level | `cmd/obor/lead_here_test.go:83-98` repeats "a sibling in its tab" and "a foreign workspace", and its hc tests repeat "runs a program" and "has a sibling", through the real adapters. The rule's table is `internal/app/layout/layout_test.go:233-263`; the adapters' facts are in `internal/herdr/setup_test.go` and `internal/hc/place_test.go`. These five tests are the five flake rows of T-1400 and T-1406: a rule change broke the CLI level. | Per terminal, one test that takes the pane and one that opens a new tab. Six tests become four, the three-row table one row. |
| b | A reply line used as the success check everywhere | `"RESTARTED pj-l-01 (pane w1:p3, session …"` is written out 53 times in `internal/launch`'s tests (32 in `launch_test.go`, 11 in `self_test.go`); `"DISPATCHED T-1 → L-01 (queued) as sonnet"` 19 times in `internal/app`'s, beside a rig that already has `mustDispatch`. | One test per line pins the whole text. The others call a helper that checks the outcome. |
| c | The same decision in an in-package test and a rig test | 30 of the 43 `_ext_test.go` files have an in-package file of the same name. Not read pair by pair; the names suggest some (`TestIdleNowFreshProject` and `TestIdleFreshProject`), and DD-tests-v0.33 found one such set in `restore`. | A researcher lists the pairs first (TP-7a). Then one delete task from that list. No deletion from names or coverage: the 11-04 pruning had 390 lines sent back by review. |

No task:

- **Tests of fakes.** `herdrtest` has 4 tests (137 lines), `testkit` 7 (191 lines). The contract tables
  (`testkit.Contract` run by herdr and hc, `TestWorkflowContractFake` beside `TestWorkflowContractScratchFF`) are the
  right way to prove a fake and stay.
- **N3.** T-1407 did it: `internal/setup/placement_test.go` is 249 lines, from 562.
- **DD-tests-v0.33's delete list.** All seven tests and `LinkPlugin` are gone.
- **`tests/release`.** 76 tests that run `make release` on fixture repos with a fake `go`
  (`tests/release/release_test.go:22`), so no real compile. It runs for release-class files, in the nightly and
  before a tag. That is the right place; TP-1 stops it riding along on full runs that should never have been full.
- **The structure tests in `internal/checks`** (`pkgstate_test.go`, 332 lines added today, `modulemap_test.go`).
  They add test lines for no production line and they replace a reviewer's check. Keep them, and read the ratio per
  package instead (section 5).

## 3. Gate cost and machine load

**What each check runs.**

| Check | When | Runs | Measured |
|---|---|---|---|
| `check-compile` | every merge, serve's worktree | build, gofmt, `go vet -tags integration`, deadcode | mean 7 s, max 26 s (105 runs on 11-20) |
| guard | every lane test | `bash -n` on changed scripts, `obor config validate`, `jq` | under 1 s |
| `check-affected` | class go | budget `-missing`, then `go test -tags integration` on the changed packages and their importers (`Makefile:128-143`) | mean 61 s, max 179 s (147 runs since 11-10) |
| `check-readers` | class readers | `go test` of ten packages, `cmd/obor`, `internal/app`, `internal/checks` and `internal/setup` among them (`Makefile:208-213`) | 59 s alone; with go, the step is 140 s on average (26 runs) |
| `check-web` | class web | `go test ./internal/web` | 5 s |
| `check-release` | class release, and a full run that holds a release-class file | compile tier, then `tests/release` | 75 s alone (2 runs); go + release 177 s |
| `check` | a file no class matches | compile tier, deadcode, `go test ./...` | mean about 100 s, max 245 s (55 runs since 11-10) |
| nightly | 02:00 | `check`, govulncheck, conformance static, `check-release`, `check-budget`, the race run, the cache trim | 290 s on 11-20 |

**Too heavy, in order of seconds.**

1. **Files that match no class** (`internal/checks/classify.go:66-73`). The 55 full runs since 11-10 by first
   unmatched file: `README.md` 20, `tests/budget.json` 13, `tests/conformance/**` 9, `tests/upgrade/**` 3, `bin/obor`
   3, `opencode/obor.js` 2, `.obor/config.json` 2, three others. A task that adds a package has to seed
   `tests/budget.json`, so every new package pays the whole suite.
2. **go + readers runs the big packages twice.** Both commands test `cmd/obor`, `internal/app`, `internal/checks` and
   `internal/setup`. The runner already runs a command named by two classes once (`internal/checks/runner.go:407-413`),
   so the fix is one command: `check-affected` adds the reader packages when a reader path changed.
3. **`cmd/obor` is in nearly every set and is the largest budget** (45.9 s at 2 cores, 248 tests, 22 of its 55 test
   files integration). It imports everything, so that is correct. The lever is item 2a: the CLI level proves wiring,
   not rules.

Together 1 and 2 are about 7,500 of the 19,900 lane test seconds since 11-10. The fix recovers the double run
(about 2,000 s) and most of what a full run costs over a partial one. Estimate, not a measurement: a fifth of the
lane test time.

**CPUs.** A lane test's environment is built in this order (`internal/checks/runner.go:120-127`): the check's
`CHECK_CPUS` from `checks.cpus_per_check` (`internal/run/slots.go:264-271`), then the lane's own entries, which
replace it: `CHECK_CPUS=<lanes.cpus>`, `GOMAXPROCS` and `GOFLAGS=-p` from `lanes.env`
(`internal/checks/lanetest.go:78-81`, the same in `scratch.go:219-220` for a moved gate). `lanes.cpus` is not in
`.obor/config.json`, so it is 2 (`internal/config/config.go:1104-1113`). So:

- Before 21:53 today: up to 4 lane tests at once, 2 CPUs each. Not the "4x4 = 16" of the commit message.
- Now: up to 2 at once, 2 CPUs each. Half the throughput, the same time per test.
- `cpus_per_check` reaches the nightly, the compile and a full check outside a lane.

Recommendation: a lane's heavy check gets `cpus_per_check` (for `CHECK_CPUS` and for `{cpus}` in `lanes.env`), and
the worker's own processes keep `lanes.cpus`. Two heavy checks at 5 CPUs is then the bound the owner set. The
alternative with no code change is `heavy_at_once: 4` again, which is what ran before. I prefer the first: the slot
is what bounds the load, so a check that holds one should use its share.

**What a lane runs and what the nightly runs.** No change in kind. The lane runs the guard and the changed packages
with their importers under the integration tag. The nightly runs everything, the release tests, the race run and the
budget. Earlier attempts to thin the lane (`-short`, gate-time.md option C; changed packages only, T-991) are
answered by 11-20's reds: two of the seven were caught only in an importer, three only under the tag.

## 4. Flakes

`flakes.jsonl` has 14 rows since 11-13. It is a log of retried reds: 12 did not pass on the retry and were real.

| Kind | Rows | Tests |
|---|---|---|
| Real, behind the integration tag | 3 | `TestInitEndToEnd` twice (T-1388, T-1389), `TestRoleIntegratorStartsInItsWorktreeAndNeedsAType` (T-1391) |
| Real, an importer's CLI-level copy of a rule | 5 | the `TestLead…` tests of `cmd/obor/lead_here_test.go` (T-1400, T-1406) |
| Real, a check across packages or languages | 4 | `TestTypesTSMatchesGoJSON` twice, `TestDistFresh`, `TestNoKindLiterals` |
| Passed on retry | 2 | `TestEveryEntryPointWaitsForTheLaunch` (`internal/launch`, nightly), `TestDropFreeFailsAfterTheRecord` (`internal/app`, in-package) |

- No flaky family. The two that passed on retry both wait on the real clock: `internal/launch`'s `shortWait` of 60 ms
  (`launch_test.go:157`) and `concRig.waitFor`'s 5 ms poll (`internal/app/rig_test.go:625-634`). The external rig's
  tests, which run in a bubble (330 `bubble` calls), have no row. Two rows in a week is no reason for a task; if
  either family reaches three in a week, that family moves to a bubble.
- The first kind goes with TP-4, the second with TP-5. For the third, the worker doc gets one line: a change to a
  type the page reads means `make web` before `task_done`.

## 5. Rules for new tests

For `AGENTS.md` "Tests" (the lead writes them). They add to the rules there; none replaces one.

1. **One level per behaviour.** A spec's Tests line names the level of each test: rule (a pure function's table),
   adapter (the adapter on its fake), flow (the rig), CLI (one row per verb, proving the wiring), integration (real
   I/O). A decision is tested at the level whose code makes it. The level above gets one row that it is wired, never
   the rule's rows again.
2. **A rig constructor or a new fake type needs its reason in the spec**: why no option of `newLoopRig` and no
   existing rig fits. A test file of `internal/app` names no terminal adapter and no adapter's fake; a boundary test
   holds the list of files that may (TP-9).
3. **A sentence is pinned once.** The full text of a reply, refusal or needs-you line is asserted by the test of the
   code that writes it. Other tests use the rig's helper or assert the step, state or outcome.
4. **A task with no intended behaviour change adds a test only for a pure function it extracts**, and deletes the
   rows that table replaces at the old level in the same diff. The spec's Tests line gives the counts (added,
   deleted), and the integrator compares them with the diff at accept (`grep -c '^+func Test'`), where it costs
   seconds, instead of the review, where it costs a round.
5. **A new test that waits on time runs in a bubble.** No new wall-clock poll in `internal/app`.

Do not set a target for the ratio. It went up today in `internal/launch` (+485 test lines, +221 production),
`internal/checks` (+407, 0) and `internal/herdr` (+349, −38), and down in `internal/setup` and `internal/hc`. A
number per package per night shows where; one number for the tree does not (TP-11).

## Task split

Sizes: simple, medium, hard. "After" is a file overlap or a dependency.

| # | Task | Files | Size | Order |
|---|---|---|---|---|
| TP-1 | Classify the test paths: `tests/budget.json`, `tests/budget/**`, `tests/conformance/**`, `tests/upgrade/**`, `tests/go/**` join class go | `.obor/config.json` (`merge.by_path.rules`) | simple (the lead, `obor config set`) | first |
| TP-2 | One Go run per lane. `tools/affected` takes `-reads <glob>=<pkgs>` and adds those packages when a matching file changed; `check-affected` passes the reader globs, with `README.md` and `bin/obor` (`cmd/obor`), `githooks/**` (`internal/setup`, `cmd/obor`) and `opencode/**`; class readers runs `check_affected`; `check-readers` and `commands.check_readers` go. `TestOborKitReaders` keeps failing when a package reads a path the map lacks. This ends the Makefile's note that `README.md` and `bin/obor` stay unmatched (`Makefile:202-204`). | `tools/affected/main.go` and its test, `Makefile:128-143,179-213`, `.obor/config.json`, the readers survey test in `internal/checks`, `docs/agents/worker.md` | medium | after TP-1 (config) |
| TP-3 | A lane's heavy check runs at `checks.cpus_per_check`. Owner's decision first. | `internal/checks/lanetest.go:78-81`, `internal/checks/scratch.go:219-220`, `internal/config/config.go` (`LaneEnv` with a budget), their tests | simple | parallel with TP-2 |
| TP-4 | The worker's run before `task_done` passes `-tags integration` for the packages it changed; the `make web` line; the five rules; the Tests line's level and counts; the integrator's count check | `docs/agents/worker.md:6-16`, `AGENTS.md`, `templates/task-spec.md`, `docs/agents/integrator.md` | simple (the lead) | any time |
| TP-5 | `obor lead`'s CLI tests keep the wiring: one take and one new tab per terminal. The spec lists each removed row and the test that holds it. | `cmd/obor/lead_here_test.go` | simple | parallel |
| TP-6 | The `RESTARTED` line pinned by one test, a helper for the rest | `internal/launch/{launch,self,shell,trust,locate,session}_test.go` | simple | parallel |
| TP-7a | List the decisions tested both in-package and on the rig, for the 30 file pairs: both tests, the decision, the inputs, which one stays | `docs/research/app-test-levels.md` | simple (researcher) | parallel |
| TP-7b | Delete from TP-7a's list. A pair counts only when both tests assert the same decision from the same inputs. | `internal/app/*_test.go` as listed | medium, review on merge | after TP-7a, before TP-9 |
| TP-8 | Split the two rig files by concern, a move only | `internal/app/rig_test.go`, `rig_ext_test.go` into the twelve files of section 1 | simple; `internal/app` quiet | after TP-7b |
| TP-9 | The external test files speak the rig only: the 162 lines become rig methods in the port's words (set a pane, add an agent, end the stream, the calls of an operation); the 19 `DISPATCHED` pins go to `mustDispatch`; a boundary test lists the files that may import an adapter or its fake | the 22 external test files of section 1, the rig files, `internal/checks/boundary_test.go` | medium | after TP-8 |
| TP-10 | The neutral terminal under the rig: `testkit.Term`; the rig builds the app on it; `herdr` and `herdrtest` leave `internal/app`'s tests except the three boundary files. The spec lists each thing the flow tests proved of `herdr.Panes` (the snapshot after a workspace event, the refresh code, the prompt's reply) with the `internal/herdr` test that holds it, or adds it there. | `internal/testkit/term.go`, the rig files, `internal/herdr/*_test.go` for the listed rows | hard, review on merge | after TP-9; the one to cut |
| TP-11 | The nightly's budget line prints, per package, test lines and tests against the night before, as WARN lines with no cap | `tests/budget/main.go` and its test | simple | any time |

In one line: **TP-1 → TP-2 ∥ TP-3**, with **TP-4, TP-5, TP-6, TP-7a** beside them; then **TP-7b → TP-8 → TP-9 →
TP-10**; TP-11 whenever a lane is free.

## Risks

1. **TP-10 drops coverage the flow tests gave the herdr adapter for free.** This is the pattern of the verify
   review's N1: a deleted twin took a decision with it. The spec's list of what moves to `internal/herdr` is the
   guard, and the review compares that list, not a coverage number.
2. **TP-7b deletes a scenario that only looked like a copy.** The rule "same decision, same inputs" and the
   researcher's table are the guard; the in-package table test is the one that stays.
3. **TP-2 moves "which packages read this path" into a flag.** A wrong map means a reader's tests do not run in the
   lane. `TestOborKitReaders` already fails on a missing package; it has to read the new map.
4. **TP-3 changes how many tests run at once in a lane check.** The two wall-clock families (section 4) may flake
   more or less. Watch `flakes.jsonl` for a week after it.
5. **TP-1 puts `tests/conformance/**` in class go.** Its tagged files are type-checked only by deadcode's tags in
   `check-compile` (`Makefile:268-269`). That is the same coverage a full run gave them: `go test ./...` does not
   build them either.

## Not verified

- The machine's load by source. The event store has the lane tests; it has nothing on the workers' own `go test`
  runs or the role agents'. A role agent's start gets no CPU cap (`internal/launch/launch.go:581-583` adds the lane
  env only for a lane; this session's environment has no `GOMAXPROCS`), so a reviewer's or a deep dive's test run
  uses all 10 cores outside the check slots. Capping it changes a start's env, which means a live conformance run
  per kind, so it is flagged here and not scheduled.
- A lane test's time at 5 CPUs. The suite takes about 32 s alone on 10 cores (DD-tests-v0.33) and 135 s as the
  budget's sum at 2; where 5 lands is an estimate.
- The overlap inside the 30 file pairs: TP-7a reads them.
- The seconds TP-1 and TP-2 save: derived from the averages above, not from a run with the fix.
- Whether `opencode/**` and `githooks/**` are read by more packages than named in TP-2: the readers survey test
  decides.
