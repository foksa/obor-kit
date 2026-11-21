# Census: external processes in the Go tests

Question (owner): tests used to start git (and other tools) far too often, which made the suite slow and heavy. Count it now.
Measured 2026-11-21 on `staging` (19ea0cc2), macOS arm64, `-tags integration`, one plain `go test` per package, in sequence, `GOMAXPROCS=2`.
No code changed; nothing committed.

## Answer first

- **The suite starts 5,351 git processes and about 410 other tracked processes per full run** (41 packages with tests, 2,013 top-level tests and 2,400 subtests). The
  git count is stable: two full runs differed by 2 in `cmd/obor` and by 0 everywhere else.
- **Git is concentrated.** `internal/checks` 2,588 (48 %), `internal/setup` 926 (17 %), `cmd/obor` 813 (15 %), then
  `internal/web` 277, `internal/config` 253, `internal/run` 188, `internal/view` 170, `internal/launch` 83. Those eight are
  99 % of it. 29 of the 41 packages start no git.
- **The test side is no longer the main cost; 60 % of the git is the product's own.** 3,197 of the 5,351 git processes carry
  `GIT_OPTIONAL_LOCKS=0` (the mark of `project.GitEnv`, so the product's calls: gate, submit, lane state, init, validate). 2,154 are
  the tests' own setup (`testkit` helpers and direct `exec.Command`).
- **Fixtures are already templates.** `testkit.New` copies one template (0 git per call, 3 once per binary), and `checks`/`cmd/obor`
  copy built fixtures. What is left per test is the helper tail on top of the copy: `Config()` 3 git, `Lane()` 2, `Commit()` 2.
  Those three helpers are 785 test-side processes, 15 % of the suite.
- **Five packages use real git although AGENTS.md "Test rules" allow it only in `checks`, `cmd/obor`, `setup` (githooks):**
  `web` 277, `config` 253, `run` 188, `view` 170, `launch` 83 = 971 processes (18 %).
- Not git, but heavy: `internal/app` takes 94 s under the shim, caused by the shim (see Method, "Shim artefact"); on its own the test takes 0.02 s.

## 1. Where tests start an external process (static)

About 100 `exec.Command` / `CommandContext` / `LookPath` sites in `*_test.go` and test-only helpers outside `tests/release`, and
about 30 more in `tests/release` (51 test files are `//go:build integration`, 12 are `release`). No `os.StartProcess` anywhere in tests. By tool:

| Tool | Where (file:line) | Notes |
|---|---|---|
| git | `internal/testkit/testkit.go:120` (template), `:356` (`Project.Git`), `:385` (`Lane`'s `show-ref`), `:433` (`Config`'s `diff --cached`) | the helper every package calls |
| git | `internal/checks/docs_integration_test.go:21,79,178`, `lanetable_integration_test.go:27`, `scratch_integration_test.go:190`, `gitlock_integration_test.go:114` | direct, `checks` |
| git | `internal/setup/setup_test.go:155`, `githooks_integration_test.go:164`; `cmd/obor/githooks_integration_test.go:49`; `internal/project/project_test.go:42-45` (`run(t, …, "git", …)` init + commit + `worktree add` per lane); `internal/config/validate_test.go` (through `testkit.New`) | |
| git | `tests/release/*` (`release_test.go:59,176,456,716`, `release_steps_test.go:358,378`, `release_notes_test.go:269`, `release_staging_test.go:15`, via `f.Git`), `tests/upgrade/check_test.go:222,232,682,768` | `release` tag / `OBOR_UPGRADE_CHECK=1` only: not in the gate |
| git (the counting wrapper itself) | `tests/budget/main_test.go:285`; `tests/budget/main.go` (`writeGitWrapper`) | the nightly budget tool |
| the `obor` binary (built in `TestMain`) | `cmd/obor/main_test.go:85,108,129,145,180,214,240,411`, `mcp_test.go:56`, `serve_test.go:31`, `events_integration_test.go:96`, `main_integration_test.go:42`, `plugin_wiring_test.go:83,298` | absolute path: not caught by a PATH shim |
| `go` | `cmd/obor/main_test.go:44,50` (2× `go build` per test binary), `internal/launch/opencode_binary_integration_test.go:40` (`go build`), `internal/checks/boundary_test.go:282` (`go list -deps`), `tests/release/makefile_test.go:520,1091` | `go test` puts `$GOROOT/bin` first on the tests' PATH: not caught by a PATH shim |
| bash / sh | `cmd/obor/shim_integration_test.go:164,218,708` (`bash bin/obor …`, `sh -c "obor help"`), `shim_hashook_integration_test.go:75`, `internal/launch/startenv_test.go:74`, `launch_test.go:1952`, `internal/checks/lane_test.go:21`, `tests/release/*` | plus every `bash -c` the product runs for a configured command (`obor-run-*`), 157 in `checks` |
| jq | not started by a test: `bin/obor` (the one bash shim) runs it, 82× in `cmd/obor`'s shim tests | |
| make | `cmd/obor/shim_integration_test.go:661`; `tests/release/makefile_test.go` (17 sites), `buildout_test.go:43`, `release_test.go:163` | `release` tag |
| herdr | none: `internal/herdr/herdrtest` is an in-process fake on a unix socket (`herdrtest.Serve`) | `tests/conformance/live.go:56,327` runs the real herdr, in `go run ./tests/conformance` (nightly), not a test |
| hc | `internal/hc/client_integration_test.go:53` (real `hc`, `server start` + calls), `tests/conformance/live_hc.go:45,114` | the integration test is 4 `hc` starts (it starts and stops a core) |
| tmux, node, npm | none | |
| a built fake binary | `cmd/obor/plugin_wiring_test.go:83,298`, `shim_integration_test.go:59` (`path warm`), `internal/checks/fixture_test.go` (`stub`), `testkit.Script` (hard-linked scripts, a script per body) | absolute paths |
| the test binary re-run as a child | `internal/app/messages_test.go:770`, `loop_test.go:123`, `internal/testkit/testkit_test.go:118`, `internal/project/project_test.go:219` | `os.Args[0] -test.run=…` |
| other | `cp -R` (`internal/checks/fixture_test.go:189`, once per `newFx` / `copySx`; `cmd/obor/reviewcycle_integration_test.go:55,66`), `sleep`/`true` (`testkit.go:724,737`), `ps` (`launch_test.go:1976`), `curl`, `tar` (`tests/upgrade/check_test.go:747,769`) | not shimmed (see Method) |

## 2. The git helpers and what one call costs

Measured against the log (a call's git commands are in the run's per-test sequences) and the code:

| Helper | Git processes per call | Source |
|---|---|---|
| `testkit.New` / `NewAt` / `NewDir` / `NewPlain` / `Adopt` | **0** (template copied in process; plain projects are files) | `testkit.go:288-300`, `plain.go:21` |
| the project template | **3 once per test binary** (`init`, `add -A`, `commit`) | `testkit.go:105-127` |
| `Project.Git` / `GitEnv` | **1** | `testkit.go:345-370` |
| `Project.Lane(name, branch)` | **2** for a branch (`show-ref --verify` + `worktree add`), **1** detached | `testkit.go:374-392` |
| `Project.Config(edits…)` | **3** (`add`, `diff --cached --quiet`, `commit`), 2 when nothing changed | `testkit.go:432-440` |
| `Project.Commit(dir)` | **2** (`add work.txt`, `commit`) | `testkit.go:511-520` |
| `Project.Submit` / `Dispatched` (via `laneTask`) | **1** (`symbolic-ref --short HEAD`) | `testkit.go:558-565` |
| `Project.Landed` | **1** (`rev-parse HEAD`) | `testkit.go` (`Landed`) |
| `checks.newFx` / `copySx` | **0 git** (+1 `cp -R`, + `repairWorktrees` in process); the template is **~10 once per binary** and **3 once per distinct config-edits key** (`editedTemplate`, `fixture_test.go:196-207`) | `fixture_test.go:236-282` |
| `reviewFixture` (cmd/obor) | **~14 once per binary**, then `cp -Rp` per test | `reviewcycle_integration_test.go:30-70` |
| `setup` test helper `git(dir, args…)` | **1** | `setup_test.go:155` |
| `project_test.go` fixture | **2 + 1 per lane** (init, commit, `worktree add`) | `project_test.go:42-45` |
| `tests/budget` wrapper | 1 per git call (this is the counter) | |

Test-side git by helper over the whole run (fractions split where parallel tests overlapped):

| Bucket | Processes | Mostly in |
|---|---|---|
| direct `Project.Git` / `exec.Command("git")` (assertions, `rev-parse HEAD`, `status`, `reset`, …) | 1,031 | checks 408, cmd/obor 330, setup 258 |
| `Project.Config()` | 448 | setup 105, cmd/obor 90, web 69, view 54, run 51, checks 165 (built once per distinct edits key, 3 each) |
| `Project.Lane()` | 255 | cmd/obor 77, setup 46, web 36, view 31, run 22 |
| template builds (`hk-tmpl*`) | 241 | checks 214 (one per config-edits key), 3 each elsewhere |
| `laneTask` / repo init | 97 | web 51, cmd/obor 24, view 12 |
| `Project.Commit()` | 82 | web 36, view 23, cmd/obor 14 |

## 3. Measured per package

`go test -json -tags integration <pkg>`, the 41 packages with tests, one after another. A counting shim (below) sat first on PATH.
"tests" is top-level test functions, "sub" their subtests. "wall" is go test's package elapsed **under the shim and a loaded machine**
(load average about 5 on 10 cores; other agents run): use `tests/budget.json` for quiet-machine wall times. "git P/T" is
product (`GIT_OPTIONAL_LOCKS=0`) / test side.

| Package | tests | sub | git | git P / T | other (tracked) | wall s | `tests/budget.json` git cap |
|---|---|---|---|---|---|---|---|
| `internal/checks` | 92 | 203 | **2,588** | 1,946 / 642 | 159 (bash 157, sh 2) | 37.0 | 3,020 |
| `internal/setup` | 132 | 103 | **926** | 514 / 412 | 30 (bash 30) | 23.2 | 875 (over) |
| `cmd/obor` | 246 | 59 | **813** | 275 / 538 | 189 (bash 100, jq 82, sh 6, make 1) | 40.7 | 1,119 |
| `internal/web` | 36 | 0 | 277 | 82 / 195 | 0 | 5.8 | 362 |
| `internal/config` | 66 | 133 | 253 | 198 / 55 | 0 | 4.8 | 321 |
| `internal/run` | 13 | 3 | 188 | 101 / 87 | 25 (bash 25) | 6.6 | 282 |
| `internal/view` | 45 | 8 | 170 | 38 / 132 | 0 | 3.3 | 245 |
| `internal/launch` | 131 | 389 | 83 | 30 / 53 | 5 (sh 5) | 11.8 | 45 (over) |
| `internal/guard` | 88 | 67 | 31 | 10 / 21 | 0 | 3.3 | 47 |
| `internal/project` | 38 | 27 | 15 | 1 / 14 | 0 | 1.7 | 23 |
| `internal/testkit` | 6 | 0 | 5 | 0 / 5 | 0 | 0.4 | 6 |
| `internal/mcp` | 10 | 0 | 2 | 2 / 0 | 0 | 0.9 | 2 |
| `internal/hc` | 49 | 63 | 0 | – | 4 (hc 4) | 1.0 | 0 |
| `internal/app` | 778 | 893 | 0 | – | 1 (bash 1) | 94.8 | 0 |
| every other package | 4–60 each | | 0 | – | 0 | < 2 | 0 |
| **Total** | | | **5,351** | **3,197 / 2,154** | **413** | | |

The 0-process packages: `agents`, `agents/{claude,codex,plugin}`, `app/{event,identity,layout,outbox,port,review,wire}`,
`background`, `herdr`, `herdr/herdrtest`, `herdrapp`, `kinds/opencode/kind`, `roles`, `selfexec`, `service`, `spec`, `store`,
`version`, `tests/{budget,conformance,conformance/stubmcp,upgrade}`, `tools/affected`. `agents`, `agents/claude`, `agents/codex`,
`app/identity|layout|outbox|port|review`, `background`, `roles`, `spec`, `version` were served from go's test cache, so their wall
time is not a measurement; they call no `exec` (grep above), so 0 stands.

Two caps in `tests/budget.json` are below the measurement: `internal/launch` 83 > 45 and `internal/setup` 926 > 875. I did not
run the budget tool (a second whole-suite run); the cause is unknown. Candidates: the caps were seeded earlier than T-1413's
launcher work, or this shim counts something the budget wrapper does not (it should not: both wrap `git` on PATH).

`tests/release` (`-tags release`, ~4,400 lines, 76 tests, make/bash/git/sh heavy) and `tests/upgrade`'s `TestUpgradeCheck`
(`OBOR_UPGRADE_CHECK=1`) are outside `-tags integration` and were not measured.

## 4. Top tests and helpers by git processes

Top 10 tests (git, product/test split). Where several parallel tests overlapped a process, it is split evenly among them; rows
marked ≈ share a table or a parallel group, so read them as the group's cost.

| # | Test | Package | git | P / T | What repeats |
|---|---|---|---|---|---|
| 1 | `TestLaneTestRedRestores` | checks | 293 | 263 / 30 | table of lane-test cases, each a full Submit + lane test (see the gate sequence below) |
| 2 | `TestGithooks` ≈ | setup | 209 | 47 / 162 | a table; its rows call `NoCompileWarning`'s 2 `symbolic-ref` and the githooks config reads (section 5) |
| 3 | `TestGithooksPicksTheHooksDir` ≈ | setup | 208 | 46 / 162 | same shape, runs in parallel with #2 |
| 4 | `TestGateCompilesOnly` | checks | 113 | 100 / 13 | product Submit/Gate calls (100 of 113) |
| 5 | `TestLaneTestGreenRestores` | checks | 109 | 93 / 16 | as #1 |
| 6 | `TestCheckoutWorktree` | checks | 106 | 71 / 35 | table; not inspected further |
| 7 | `TestFlakeLog` | checks | 101 | 89 / 12 | mostly product Gate calls |
| 8 | `TestPreexistingLinkKeepsTheLaneClean` | checks | 93 | 78 / 15 | not inspected |
| 9 | `TestOtherHooksHangingBinaryAllow` | cmd/obor | 92 | 32 / 60 | runs the built binary and the bash shim; +22 bash/jq |
| 10 | `TestLaneFreeHistoryFailurePreservesBranch` | checks | 89 | 56 / 34 | not inspected |

Next: `TestValidate` (config) 84, 0 test-side: every row calls `Validate`, which runs `rev-parse --git-dir` once per call
(`internal/config/validate.go:608`); the three `TestReviewGitClose*` cases (cmd/obor) 71 to 84 each with 22 bash+jq each.

Top 5 helpers (processes over the run, from section 2's buckets):

1. direct test-side `git` (`Project.Git` and `exec.Command("git")`): **1,031**.
2. `Project.Config()`: **448** (3 per call; ~200 calls, of which checks' are shared per edits key).
3. `Project.Lane()`: **255** (2 per call, ~110 calls).
4. template builds: **241** (214 of them are `checks` building one fixture per distinct config-edits key).
5. `laneTask` + `Project.Commit()`: **179** (97 + 82).

Which repeat the same setup per test (candidates for one shared template copied per test, or a pure function):

- `Config()` default: 46 `cmd/obor` tests, 26 `setup`, 23 `web`, 16 `view`, 11 `run`, 7 `config`, 5 `guard` each commit the same default
  `.obor/config.json` on a fresh `testkit.New` copy, 3 git per test (about 200 tests, 450 processes). The template could already hold it.
- `Lane("L-01", "task/T-01")` + `Commit` + `laneTask`, repeated the same way in `web` (20 tests), `view` (11), `run` (10),
  `cmd/obor` (46), `setup` (18): 4 to 7 git per test, identical across tests. `checks` already does this with
  `repairWorktrees` on a copied template; `testkit` does not.
- `internal/config` `TestValidate` rows: the same `rev-parse --git-dir` per row, a pure function would do (the real probe is
  not what the row tests).
- `web`, `view`, `run` render or read lane state: they build real repos to feed `checks.LaneTable`, which already has an injectable
  `Git` (`internal/checks/lanetable.go:41-46`).

## 5. Product code that shells out to git per call, which tests hit many times

All product git goes through `exec.CommandContext(…, "git", …)` at four seams. Each call is one process.

| Seam | File:line | Called per | Tests hit it |
|---|---|---|---|
| `project.gitRun` (`Git`, `GitCtx`) | `internal/project/project.go:164` | every lane read, init step, worktree add | all packages |
| `Options.gitEnv` (the merge scratch) | `internal/checks/checks.go:142` | every git step of Submit, Gate, publish, nightly | checks 1,946 |
| `Lane.gitRaw` (lane prepare, free, test) | `internal/checks/lane.go:160` | every lane step | checks |
| `config.gitCmd` | `internal/config/validate.go:619` | `Validate` (`:608` `rev-parse --git-dir`, `:613` `rev-parse --verify`) and `NoCompileWarning` (`:662`, 2 `symbolic-ref`) | config 253, setup 202 |
| `realGit.Commit` | `internal/setup/git.go:42` | each setup commit | setup |

Measured per product operation (from the sequences):

- **One `Submit` + `Gate` is about 36 git processes** (`TestGateReportsItsCommands`: 77 calls for 2 flows, 2 × (11 submit + 25 gate), 0
  test-side after the shared template): `rev-parse --verify refs/obor/submitted/…`, `branch --show-current`, `status`, `add -A`,
  `commit`, `rev-parse refs/heads/main`, `rev-parse HEAD`, `merge-base --is-ancestor`, `update-ref`, `worktree add -f`, `reset --hard`,
  `clean -fdq`, rerere, `show -s`, `commit`, `update-ref`, `merge-base`, `diff`, `diff --stat`, `status` ×3, `checkout` ×2,
  `diff --name-only`, `reset`, `clean`. The same reads repeat inside one flow: `rev-parse refs/heads/main` 2×, `rev-parse HEAD` 2×,
  `branch --show-current` 2×, `status` 4×. Across the suite: `rev-parse` 932, `status` 345, `diff` 333, `symbolic-ref` 233, `branch` 157,
  `reset` 140, `clean` 124, `commit` 127, `merge-base` 139, `checkout` 99, `update-ref` 94.
- **`Config.Validate` runs `rev-parse --git-dir` on every call** (`validate.go:608`, `trunk()`), once per `Validate`, plus
  `rev-parse --verify` (`:613`). `TestValidate` calls it per row: 175 + 19 processes for tests that check strings. The real callers
  are `cmd/obor/config.go:53` and `internal/app/configset.go:126-127` (twice per config set).
- **`NoCompileWarning` runs 2 `symbolic-ref` per `obor init`** (`validate.go:662`, reached from `internal/setup/oborinit.go:562`):
  101 × 2 = 202 in `setup`'s tests, when `init.go:242` already read the branch.
- **`LaneTable.States` runs `rev-parse --show-toplevel HEAD --symbolic-full-name HEAD` and `status --porcelain --untracked-files=all` per
  lane** (`internal/checks/lanetable.go:83`, `status` via `LaneDirt`) and `merge-base --is-ancestor` per lane (`:122`): web 41+41, view 18+18,
  cmd/obor 23+29. Every test that loads a project's state pays 2 to 3 per lane.
- **`setup`'s githooks step runs `config --local --get core.hooksPath` and `… obor.hooksChain`** (`internal/setup/plugin.go:57-59`): 47 + 47
  in `TestGithooks` and `TestGithooksPicksTheHooksDir`.
- **`project.AddWorktree`** (`internal/project/worktree.go:30`) plus `TrackedIn`'s `ls-files --error-unmatch` (`:84`) per link: 45 + 46 in setup/launch.
- **`run.Execute`** (`internal/run/run.go:68,168`): `rev-parse --show-toplevel`, `branch --show-current`, plus `check-ignore`, `read-tree`,
  `write-tree`, `add -A`, `reset` for the task-done file: 101 product processes in `internal/run`'s 13 tests, ~8 per test.

## 6. What would cut the most (ranked, rough; estimates from the counts above)

1. **Put the default config, one lane and one commit into the `testkit` template** (and give `Config(edits…)` the checks approach:
   build once per edits key, copy). Removes most of `Config()` 448, `Lane()` 255, `Commit()` 82, `laneTask` 97: **about 600 to 750 processes
   (11 to 14 %)**, across cmd/obor, setup, web, view, run, config. Needs `repairWorktrees` (already in `checks/fixture_test.go:132`) lifted into `testkit`.
2. **Move `web`, `view`, `run`, `config`, `launch` off real git** (the AGENTS rule already says so) with the injectable `LaneTable.Git`, a
   `config.ValidateOptions` probe for the trunk check, and the package fakes: **about 700 to 900 processes** (971 minus a few integration tests).
   Overlaps #1 for web/view/run (count them once: together #1 + #2 are about 1,100 to 1,300, ~22 %).
3. **`internal/config` `TestValidate` and the `Validate` trunk probe**: a pure branch-exists hook in `ValidateOptions` makes the 253
   processes in `config` plain function calls: **~200 to 250** (4 to 5 %). The smallest change for a clear number; part of #2.
4. **`setup`: `TestGithooks` + `TestGithooksPicksTheHooksDir` (≈ 410, 8 %) and `NoCompileWarning`'s 2 `symbolic-ref` per init (202, 4 %)**:
   fewer full installs per table (rows that only differ in the hooks dir check one function, not a repo), and a branch read shared with
   `init.go:242`: **~250 to 400**.
5. **`checks`' 1,946 product processes (36 % of the suite)**: the product repeats the same reads inside one Submit/Gate (`rev-parse` ×2
   for main and HEAD, `branch --show-current` ×2, `status` ×4 of ~36). Caching them per flow would save **~6 to 8 of every 36 (about 20 %), ~350
   to 400** — a product change with correctness risk (a stale read decides a merge), so last, and only with an owner decision. Test-side,
   fewer rows that run a whole Submit + Gate where one decision function would do (`TestLaneTestRedRestores` 293, `TestGateCompilesOnly` 113,
   `TestFlakeLog` 101) saves the same order without touching the product.
6. **Non-git**: `cmd/obor`'s bash-shim tests start `bash` 100× and `jq` 82× (a hook call is one bash and 2 to 3 jq), `checks` starts
   `bash -c` 157× for configured commands, plus `cp -R` once per `checks` fixture copy. About 180 + 160 processes; cheap ones, little to gain beyond
   batching the shim's cases.

## Method

- **Shim**: a Go binary (`shim`, 50 lines), copied as `git`, `herdr`, `hc`, `jq`, `make`, `go`, `node`, `npm`, `tmux`, `sh`, `bash` into a
  directory first on PATH (`sh` and `bash` resolve to `/bin/sh` and `/bin/bash`; `herdr`, `hc`, `jq`, `make`, `go`, `git`, `node` and `npm` to the first
  one on PATH; `tmux` is not installed, so not shimmed; `node` and `npm` saw no call). Per call it appends one line to the current package's log file (time in ns, tool,
  pid, ppid, parent command, cwd, args, `GIT_OPTIONAL_LOCKS`/`OBOR_MERGE`) and `exec`s the real tool. Its configuration is
  files next to the binary, not environment, because several tests clear the environment (a first attempt through environment variables
  failed `TestGitEndsWithCtx`, `TestLaneCheckoutsPutTheLinkBack` and `TestRealCore` with exit 127; the numbers here come from the file-based runs).
- **Run**: for each of the 41 packages with tests, `GOMAXPROCS=2 go test -json -tags integration <pkg>` with PATH = shim dir first and a
  private short `TMPDIR`. No `-count`, no `-race`, one package at a time. The census ran four times: a sh-script shim (stopped by PID
  mid-way, discarded), the Go shim configured by environment (discarded: the three failures above), then two complete runs with the
  file-configured Go shim; the numbers are the last run's, the one before gave the same git counts within 2 (`cmd/obor` 815 vs 813).
  12 packages were served from go's test cache on the repeat runs (all zero-exec).
- **Test attribution**: `go test -json` gives each top-level test's run/pause/cont/pass times; each log line is attributed to the test
  whose temp dir name is in its cwd or args (`t.TempDir`), else to the test(s) active at that nanosecond, split evenly when parallel
  tests overlapped. `testkit.New` uses `os.MkdirTemp("", "hk")` with no test name, so most attribution is by time. Per-test numbers
  for parallel groups (`setup`'s githooks table, `cmd/obor`'s parallel tests) are therefore approximate; per-package numbers are exact.
- **Product vs test**: `project.GitEnv` exports `GIT_OPTIONAL_LOCKS=0` to every git the product runs (`internal/project/project.go:127-140`);
  the tests' own `exec.Command("git")` inherit an environment without it. A product call made with another environment would count
  as test-side; I saw none (`checks` runs `Options.gitEnv` through `project.GitEnv`).
- **What the shim cannot catch** (absolute path, or a PATH it does not control): the `obor` binary and `obor-kind-opencode` started
  by absolute path; built fakes and `testkit.Script` scripts; the `go` calls (`go test` prepends `$GOROOT/bin` to the tests' PATH: the
  2 `go build` in `cmd/obor`'s `TestMain`, 1 in `launch`, 1 `go list` in `checks`); `cp`, `ps`, `sleep`, `true`, `curl`, `tar`, `osascript`;
  and any test that sets its own PATH (`internal/hc/client_integration_test.go` sets `/usr/bin:/bin` for the `hc` it starts; its 4 `hc`
  calls were still counted, so its binary was reached through the shim). A child that calls git by absolute path would be missed;
  `grep '/usr/bin/git'` in non-test Go code finds none.
- **Shim artefact**: under the shim `internal/app` took 94 s because of `TestSelfRestartEndsServeAndItsGateGroup`
  (its log line is `bash -c 'sleep 60 & …; wait'`, from the shimmed `bash`); run alone and unshimmed it takes 0.02 s (one `go test -run`, no
  shim). Why the shim changes it is not established. `tests/budget.json` has `internal/app` at 15 s; treat 94 s as the shim's. Other packages' wall times carry a
  smaller shim cost (one extra exec per tracked call, about 5 ms, plus one `ps` per new parent pid) and the machine's load.
- The raw logs, JSON and analysis scripts are in the session scratchpad (`…/scratchpad/census/`), not in the repo.

## Unknown, and what it would take to find out

- The obor binary's start count and the `cp`/`go`/fake counts: need a shim that is not PATH-bound (a `dtrace`/`eslogger` run, root) or
  a counter in `testkit` for `oborBin` runs. Static call sites above, no counts.
- Why `launch` and `setup` are above their `tests/budget.json` caps: run `go run ./tests/budget` on a quiet machine and diff its per-package
  counts with this table.
- `tests/release` and `OBOR_UPGRADE_CHECK=1` costs: a `-tags release` run of that package under the same shim.
- Exact per-test numbers for parallel groups: a `-parallel 1` run (not done; it changes the suite's timing).
