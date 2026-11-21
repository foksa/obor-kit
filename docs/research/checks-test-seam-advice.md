# internal/checks tests: decisions on the fake, git in a few named tests

Advice for the lead, 2026-11-21, on `staging` (fd93107c). Read-only: no code changed, no test run.
Numbers are from the 11-21 census log (`docs/research/test-external-tools.md`), re-attributed by each git call's temp-dir name.

## Recommendation

**Do not build a new seam. Finish the one `Scratch` already has.** The question's premise ("no port") is out of date for the merge path:

- `gitIO` (`internal/checks/gitio.go:16-95`) is a typed port: one method per question or change (`Dirt`, `Ancestor`, `Reset`, `FF`, …). `realGit` (`gitio.go:108-316`) is the only code with git argv.
- `fakeGit` (`fakegit_test.go:20-45`) is a small model of the repository (refs, heads, status, open merges), not a script. It logs each call as `"<Method> <args>"`.
- `decide_test.go` already holds the decision tables for Submit, Gate, Resolved, Publish, AfterMerge and Landed, and a cut table that fails every write of every step and runs it again (`decide_test.go:182-228`). These start no git.
- The workflow contract (`internal/testkit/workflow.go:161-247`, run by `workflow_integration_test.go:22-28`) runs every flow on real git in 5 rows. It is the "one integration test per flow".

What is left on real git is there because three pieces of product code go around the port:

1. **The runner** reads git itself: `runner.go:339` (changed files) and `flakes.go:130` (the sha for the flake log). So `Scratch.Test` has no decision table (`decide_test.go:185-186` says so), and every lane-test, retest and flake-log row pays a real Submit + Gate + Test, 31 git, to check one line of text.
2. **`Lane`** (prepare, free) has its own exec and no port (`lane.go:152-183`). Its failure rows cut git by call number (`lane_integration_test.go:289`, `:335` "call 6 is the branch delete"), the most brittle form there is.
3. **The nightly session** calls git directly (`checks.go:108`, `:219`, `nightly.go:51`), so 25 rows that check report lines each build a worktree.

So: a **mix**. The port and model fake for code that decides over git's answers (Scratch with its lane test, Lane). A function split for the nightly, where git is plumbing around a body that needs none. About **1,000 of the package's 2,588 git processes go (40 %)**, with no behaviour change.

## 1. The seam, weighed

| | (a) recording fake, scripted outputs | (b) read / decide / act | what exists: typed port + model fake |
|---|---|---|---|
| Brittleness | Pins git argv; a flag change breaks rows | None | Pins port actions (`Reset <dir> main0`, `decide_test.go:351-352`), not argv |
| Scripting per row | 12 to 31 outputs, in order | Plain values | None for refs, heads, status, ancestry; `answers` only for diffs and logs |
| Fits the code | Any | Only where reads come first | Any |
| Crash and replay | Cut by call number | Needs an executor anyway | Cut table at every write, already there |

- **(b) does not fit Scratch.** Its steps read after they write: Submit reads HEAD after its commit (`scratch.go:91`), Gate asks for `MERGE_HEAD` after the merge (`scratch.go:272`), Publish reads the target again after a refused fast-forward (`scratch.go:640`). A split would rewrite the merge path for no test gain.
- **(b) fits the nightly**, and the small pure functions already pulled out (`gateBase`, `movedBody`, `replayDiffers`, `newestDate`, `backupOutcome`). Keep that habit.
- **Row rule:** assert the write list where order or absence is the decision ("a red gate commits nothing"), else the model's end state. Never assert reads, except "a repeat reads only the pin" (`decide_test.go:263`).
- **What still proves git does what we mean:** the contract's 5 rows reach every `realGit` method the flows use, plus the named git-behaviour tests in section 2. `fakeGit` is a hand model; when a real test and a fake row disagree, the fake is wrong.
- **Repeated reads** (`status` 4×, `rev-parse` main and HEAD 2×, `branch` 2×) are across steps or after a write, never twice inside one step. Measured: Submit 10 git, Gate 12, lane Test 9, no duplicate within any of them. They are what makes each step a function of its inputs. Leave them; on the fake they cost nothing.

## 2. Per flow

| Flow | On the fake today | Real-git rows (git) | Decisions, move | Git behaviour, keep | Saves |
|---|---|---|---|---|---|
| Submit | 7 rows + cut | locks 34, AddAll 8, commit hooks 30 | 0 | all | 0 |
| Gate | ~15 rows + 3 cut steps | `TestGateCompilesOnly` 74, `TestGateReportsItsCommands` 62, own diff 38, foreign state 53, rerere 141, links 57 | 6 rows | own diff, foreign state, rerere, links | ~135 |
| Lane test | none | 12 rows, 394; `TestFlakeLog` 3 rows, 95 | 12 rows | 3: runs in the lane, discards, cut by ctx | ~380 |
| Publish | table + cut | contract rows only | 0 | – | 0 |
| Lane prepare / free | none | 10 tests, 238 | 18 rows | locks 3 rows, links 2, cancel 1 | ~180 |
| Nightly | 2 pure tables | 25 rows, 247 | ~19 rows | green with worktree, reset to the branch, serve's worktree untouched, 3 lock rows | ~175 |
| Links and checkout | – | `TestCheckoutWorktree` 137, `TestPreexistingLinkKeepsTheLaneClean` 113 | 0 | all, but each runs twice for two link paths | ~110 |

Three of `TestGateCompilesOnly`'s four rows already have a twin on the fake (`decide_test.go:371-417`): delete them, move "lane_setup fails".

## 3. Tasks

Three lanes in wave 1, three in wave 2. No two open tasks share a file.

| Task | Files | Change | Difficulty | Saves | Keeps on real git |
|---|---|---|---|---|---|
| **A. Runner reads through `gitReader`** (wave 1) | `runner.go`, `flakes.go`, `lanetest.go`, `scratch.go`, `checks.go`, `runner_test.go`, `decide_test.go`; one call-site line in `flakes_test.go` | The runner takes the reader its caller has: `Changed(target, sha)` and `Rev(dir, "HEAD")`. Add the lane test to the cut table. | middle | 0; unblocks B, C, F | all |
| **D. One link path per case** (wave 1) | `checkout_integration_test.go`, `links_integration_test.go` | Every case on one path, one case on the second | easy | ~110 | all cases, once |
| **E. Lane on a port** (wave 1) | `lane.go`, `lane_test.go`, `lane_integration_test.go`, new `fakelane_test.go` | A narrow `laneGit` interface over Lane's own calls, a `fake` field as Scratch has. `fakeGit` gains the methods (new branch checkout, branch delete, commits ahead). Free and prepare rows move; failures by method, not call number. | middle to hard: the drop path | ~180 | lock rows, link rows, cancel; contract row 5 |
| **B. Lane test rows** (after A) | `lanetest_integration_test.go`, new `lanetest_test.go` | 12 rows to the fake; the check's `git rev-parse HEAD` probe becomes the write list `Detach, Reset, Clean, Switch` | middle, largest | ~355 | runs in the lane (asserts `OBOR_CHANGED_FILES`), discards, cut by ctx, own diff |
| **C. Flake log and command reports** (after A) | `flakes_integration_test.go`, `flakes_test.go`, `scratch_integration_test.go`, new `gatecommands_test.go` | `TestFlakeLog`, `TestFlakeLogNightly`, `TestGateReportsItsCommands` become runner-level or fake rows | easy | ~190 | none of these; the rest of `scratch_integration_test.go` stays |
| **F. Nightly steps as a function** (after A) | `nightly.go`, `nightly_integration_test.go`, `nightly_test.go` | Split `session.nightly` (`nightly.go:41`) into prepare (lock, worktree, reset, sha) and the steps and report. Rows about lines call the second on a plain dir. | middle | ~145 | the 6 rows in section 2 |
| **G. Lower the cap** (last) | `tests/budget.json` | Measure, lower `git_procs` for checks from 3,020 | easy | – | – |

- B's "cut by ctx" row stays real because the fake has no ctx: only real git shows the restore runs under its own live ctx (`lanetest.go:111-117`).
- F's test helper builds a `session` without `open`. AGENTS.md asks for that reason in the spec.
- Each moved row deletes its real-git twin in the same diff (AGENTS.md "Test rules").

## 4. What not to do

- No string-level fake, no scripted PATH shim, no assertion on git argv. No new cut by call number.
- Do not restructure Scratch into gather / decide / act, and do not cache reads across steps: a stale read decides a merge.
- Do not send the session's `reset` through `gitIO.Clean`. It keeps obor's links (`gitio.go:283-290`); the session's `clean -fdq` (`checks.go:219`) does not. That is a behaviour change.
- Do not merge Lane's exec into `realGit` here. Lane's git has no `OBOR_MERGE=1` and no `Options.Environ` (`lane.go:167` against `checks.go:152`), and a different error line rule (`lane.go:187`). Hooks and messages would change.
- Do not delete the git-behaviour tests: rerere (3), commit hooks, `TestGitEndsWithCtx`, the lock tests, links and checkout, AddAll, foreign state, own diff, preview, prune.
- Do not edit the contract rows: `internal/app`'s `FakeWorkflow` runs the same table.
- No second fake type, and no `fakeGit` method the port lacks.
- Do not fake the check commands (bash) in this work.

## Flagged, not expanded

- **Census correction.** Its per-test numbers split parallel tests by time. By temp dir, `TestWorkflowContractScratchFF` is **330** (census: 62), the largest row in the package, and the six `*Decisions` tests are 0 (census: 38.6 each). The contract is the price of the real test per flow; I do not advise cutting it.
- `ScratchFF.Target` (`workflow.go:50-58`) is a fourth direct git seam, 3 git a call, covered only by the contract.
- `LaneTable` tests (90 git) are mostly `port.FreeState` decisions and the table already takes an injected `Git` (`lanetable.go:41-46`). Docs, prune and preview are 115. Not in the flows asked.
- Template builds are 257 (3 git per distinct config). They shrink as rows move to `newFk`, by up to ~100; not counted above.

## Not verified

- The savings are estimates from the census log, not a new measurement.
- D rests on the test's own comment that both link paths give the same results (`checkout_integration_test.go:135-136`); I did not read `project.LinksIn`'s tests.
- A relies on `git diff A..B` and `git diff A B` naming the same files for two commits. B's kept real row asserts `OBOR_CHANGED_FILES`, which covers it.
