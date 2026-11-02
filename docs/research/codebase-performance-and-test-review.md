# Deep Codebase Review: Performance, Test Suite Bloat, Duplication & Architecture

*Date: 2026-11-02*
*Scope: Full repository audit (`cmd/obor`, `internal/*`, `bin/obor`, `Makefile`, `scripts/*`)*

---

## 1. Executive Summary & Codebase Metrics

`obor-kit` has grown to **94,963 lines of Go** across **335 `.go` files**, where test code outweighs production code by **1.53x** (and up to **2.8x** in `cmd/obor` and **2.0x** in `internal/app`).

| Package | Prod LOC | Test LOC | Prod Files | Test Files | `Test*` Funcs | Test/Prod Ratio |
| :--- | ---: | ---: | ---: | ---: | ---: | ---: |
| [`internal/app`](file:///Users/djordjeradakovic/Work/obor-kit/internal/app/app.go) | 11,815 | 23,297 | 39 | **74** | 428 | **2.0x** |
| [`cmd/obor`](file:///Users/djordjeradakovic/Work/obor-kit/cmd/obor/main.go) | 3,579 | 9,860 | 27 | **39** | 188 | **2.8x** |
| [`internal/setup`](file:///Users/djordjeradakovic/Work/obor-kit/internal/setup/setup.go) | 3,510 | 4,368 | 10 | 8 | 97 | 1.2x |
| [`internal/launch`](file:///Users/djordjeradakovic/Work/obor-kit/internal/launch/launch.go) | 2,360 | 5,186 | 10 | 10 | 72 | **2.2x** |
| [`internal/checks`](file:///Users/djordjeradakovic/Work/obor-kit/internal/checks/checks.go) | 2,648 | 3,652 | 10 | 13 | 63 | 1.4x |
| [`internal/guard`](file:///Users/djordjeradakovic/Work/obor-kit/internal/guard/guard.go) | 2,512 | 2,976 | 8 | 7 | 93 | 1.2x |
| [`internal/herdr`](file:///Users/djordjeradakovic/Work/obor-kit/internal/herdr/socket.go) | 1,503 | 1,564 | 2 | 3 | 36 | 1.0x |
| [`internal/config`](file:///Users/djordjeradakovic/Work/obor-kit/internal/config/config.go) | 1,584 | 1,252 | 4 | 4 | 35 | 0.8x |
| [`internal/view`](file:///Users/djordjeradakovic/Work/obor-kit/internal/view/view.go) | 1,293 | 197 | 5 | 3 | 6 | 0.2x |
| [`internal/project`](file:///Users/djordjeradakovic/Work/obor-kit/internal/project/project.go) | 959 | 749 | 7 | 4 | 19 | 0.8x |
| [`internal/store`](file:///Users/djordjeradakovic/Work/obor-kit/internal/store/store.go) | 754 | 881 | 5 | 5 | 20 | 1.2x |
| [`internal/run`](file:///Users/djordjeradakovic/Work/obor-kit/internal/run/run.go) | 680 | 635 | 3 | 3 | 15 | 0.9x |
| [`internal/herdr/herdrtest`](file:///Users/djordjeradakovic/Work/obor-kit/internal/herdr/herdrtest/socket.go) | 1,207 | 99 | 2 | 2 | 2 | *(test helper)* |
| [`internal/mcp`](file:///Users/djordjeradakovic/Work/obor-kit/internal/mcp/mcp.go) | 532 | 644 | 3 | 1 | 10 | 1.2x |
| [`internal/web`](file:///Users/djordjeradakovic/Work/obor-kit/internal/web/web.go) | 343 | 599 | 1 | 2 | 13 | 1.7x |
| [`internal/testkit`](file:///Users/djordjeradakovic/Work/obor-kit/internal/testkit/testkit.go) | 507 | 37 | 1 | 1 | 2 | *(test helper)* |
| [`internal/service`](file:///Users/djordjeradakovic/Work/obor-kit/internal/service/service.go) | 292 | 387 | 1 | 1 | 8 | 1.3x |
| Other (`agents/*`, `spec`, `affected`, `roles`, `version`) | 1,476 | 1,026 | 9 | 8 | 29 | 0.7x |
| **Total** | **37,554** | **57,409** | **147** | **188** | **1,136** | **1.53x** |

### Core Findings at a Glance
1. **Why tests are bloated and slow:**
   - **~12,000+ subprocesses spawned per test run:** Every [`testkit.New`](file:///Users/djordjeradakovic/Work/obor-kit/internal/testkit/testkit.go#L71-L106) + `p.Lane` + `p.Config` spawns **7–12 real `git` processes** and **3–5 SQLite `WAL` + `synchronous=FULL` open/fsync/close cycles**—even for unit tests that only check CLI argv strings (`launch_test.go`) or shell AST nodes (`guard_test.go`).
   - **Combinatorial fault injection:** A single test ([`TestScratchCutTable`](file:///Users/djordjeradakovic/Work/obor-kit/internal/checks/scratch_test.go#L391-L432)) builds **~85 full multi-worktree Git repos** and spawns **~2,000+ `/bin/sh` + `git` subprocesses**.
   - **~30+ seconds of hardcoded wall-clock `sleep`s:** 6 subtests wait for `bin/obor`'s hardcoded `sleep 2` watchdog (**12s**), `nightly_test.go` sleeps across Unix 1-second boundaries (**5s**) and `window_wait` (**4.5s**), `run_test.go` sleeps **4.5s**, and `launch_test.go` has 35+ subtests timing out against real timers (**6–8s**).
   - **Append-only ticket fragmentation:** Workers add a new `*_test.go` file or `TestTxxx` / `TestRVxx` function per ticket instead of extending table-driven tests, resulting in **74 test files in `internal/app`**, **39 test files in `cmd/obor`**, **14 separate `TestStop*` functions in `launch_test.go`**, and **7 duplicate test rig builders**.
2. **Why runtime performance is inefficient:**
   - **Herdr event `git` subprocess storm:** Every single pane status event in [`App.event`](file:///Users/djordjeradakovic/Work/obor-kit/internal/app/panes.go#L94-L135) triggers [`decideLanes`](file:///Users/djordjeradakovic/Work/obor-kit/internal/app/watchdog.go#L43) (**$3L$–$4L$ `git` processes** across all lanes) AND wakes [`advance()`](file:///Users/djordjeradakovic/Work/obor-kit/internal/app/loop.go#L295-L331), which calls [`Scratch.Landed`](file:///Users/djordjeradakovic/Work/obor-kit/internal/checks/scratch.go#L468-L480) (**2 `git` processes per task**, including a full `git log -F --grep=<task>: --name-only` history walk) for every task in `stWorking`, `stNeedsYou`, `stAwaiting`, etc.
   - **`obor web` & `obor monitor` I/O amplification:** Fetching `/api/job` in [`internal/web/web.go:159-193`](file:///Users/djordjeradakovic/Work/obor-kit/internal/web/web.go#L159-L193) calls `s.detail` → `view.Load` → `LaneTable.States()`, spawning **24 `git` processes** just to discard `d.Project` and read 1 row from SQLite `prompts`. Meanwhile, `obor monitor` spawns 24 `git` processes + `launchctl print` every 2 seconds.
   - **$O(N^3)$ session ownership scan & missing SQLite indexes:** [`store.Owned`](file:///Users/djordjeradakovic/Work/obor-kit/internal/store/sessions.go#L74-L81) runs nested $O(N^3)$ loops on every session lookup and replay; [`store.schema`](file:///Users/djordjeradakovic/Work/obor-kit/internal/store/store.go#L146-L158) has **zero secondary indexes**, forcing full table scans on `events` and `prompts`; and [`config.LoadDaemon`](file:///Users/djordjeradakovic/Work/obor-kit/internal/config/load.go) is re-read and re-parsed from disk inside per-pane loops on every status call and herdr event.
3. **Why the architecture in `internal/app` is fragile:**
   - **11,815-LOC God-Package with 7 Mutexes:** `internal/app` combines 11 subsystems sharing private fields on `App`, `proj`, and a 41-field `mtask` struct, holding `a.mu` across synchronous SQLite disk `fsync`s (`p.syncLanes()`).
   - **Quadruple duplication of lane/task state:** Lane state is kept in `p.folded.lr.rows`, the SQLite `lanes` table, `p.folded.loop[task]`, and `p.lanes`/`p.tasks` (which re-reads the SQLite `lanes` table that `serve` itself wrote).

---

## 2. Deep Dive: Why There Are Too Many Tests & Why They Take So Long

### 2.1 Heavy Subprocess Git + SQLite Fixture Setup (`internal/testkit/testkit.go`)
Every test or subtest that builds a standard project fixture executes **8 to 12 `git` subprocesses** and **3 to 5 SQLite `OpenRW` + `fsync` + `Close` cycles** before the test body starts:

1. [`testkit.New(t)`](file:///Users/djordjeradakovic/Work/obor-kit/internal/testkit/testkit.go#L71-L106) — **3 `git` processes**:
   - Line 94: `p.Git(root, "init", "-q", "-b", "main")`
   - Line 99: `p.Git(root, "add", "-A")`
   - Line 100: `p.Git(root, "commit", "-q", "-m", "initial")`
2. [`p.Lane(name, branch)`](file:///Users/djordjeradakovic/Work/obor-kit/internal/testkit/testkit.go#L147-L164) — **2 `git` processes per lane**:
   - Line 154: `exec.Command("git", "-C", p.Root, "show-ref", "--verify", "--quiet", "refs/heads/"+branch).Run()`
   - Line 157/159: `p.Git(p.Root, "worktree", "add", "-q", "-b", branch, dir, "main")`
3. [`p.Config(opts...)`](file:///Users/djordjeradakovic/Work/obor-kit/internal/testkit/testkit.go#L169-L204) — **3 `git` processes**:
   - Line 199: `p.Git(p.Root, "add", ".obor/config.json")`
   - Line 200: `exec.Command("git", "-C", p.Root, "diff", "--cached", "--quiet").Run()`
   - Line 201: `p.Git(p.Root, "commit", "-q", "-m", "obor config")`
4. [`p.Dispatched` / `p.SubmitAgo`](file:///Users/djordjeradakovic/Work/obor-kit/internal/testkit/testkit.go#L284-L322) — **1 `git` process + 3–5 SQLite `OpenRW` calls**:
   - Line 290/316: `p.branch(dir)` → `git symbolic-ref --short HEAD`
   - Lines 291, 314, 318: [`p.eventAt`](file:///Users/djordjeradakovic/Work/obor-kit/internal/testkit/testkit.go#L338-L352) calls [`store.OpenRW`](file:///Users/djordjeradakovic/Work/obor-kit/internal/store/store.go#L42-L62) (`PRAGMA journal_mode=WAL`, `PRAGMA synchronous=FULL`, schema migration transaction) and closes it on *every single event*.
   - Lines 292, 310, 319: [`p.rows`](file:///Users/djordjeradakovic/Work/obor-kit/internal/testkit/testkit.go#L398-L405) and [`p.setRow`](file:///Users/djordjeradakovic/Work/obor-kit/internal/testkit/testkit.go#L421-L444) open and close `store.OpenRW` again.

**How package test helpers multiply this overhead:**
- [`internal/launch/launch_test.go:105-115`](file:///Users/djordjeradakovic/Work/obor-kit/internal/launch/launch_test.go#L105-L115) (`setup(t)`): Runs `testkit.New` + `p.Lane` + `p.Config` (**7 `git` processes**) across **220+ calls** (~1,540 `git` processes), even though >90% of `launch_test.go` only tests CLI argv construction or fake herdr RPCs and never uses Git.
- [`internal/guard/guard_test.go:22-33`](file:///Users/djordjeradakovic/Work/obor-kit/internal/guard/guard_test.go#L22-L33) (`fixture(t)`): Runs `testkit.New` + 2 `p.Lane` + `p.Config` + 2 SQLite `OpenRW` (**8 `git` processes**) per test, even though 90% of `guard_test.go` tests pure `mvdan.cc/sh` AST/path-prefix logic.
- [`internal/app/loop_test.go:69-100`](file:///Users/djordjeradakovic/Work/obor-kit/internal/app/loop_test.go#L69-L100) (`newLoopRig`): Runs **10 `git` processes** + `herdrtest.Serve` + background `app.Serve` polling goroutines across **250+ tests** in `internal/app/`.

### 2.2 Combinatorial Fault Injection in `internal/checks/scratch_test.go`
[`TestScratchCutTable`](file:///Users/djordjeradakovic/Work/obor-kit/internal/checks/scratch_test.go#L391-L432) iterates over 8 `cutSteps` (`submit`, `gate lane`, `gate resolved`, `gate moved`, `publish`, `after_merge`, `resolved verdict`, `unpin`). For each step, it counts the total number of `git` invocations `calls` (8–18 per step) and then loops `for n := 1; n <= calls; n++`:
- Inside the inner loop, it calls `newSx(t, nil)` ([`scratch_test.go:35-94`](file:///Users/djordjeradakovic/Work/obor-kit/internal/checks/scratch_test.go#L35-L94), spawning 10+ `git` commands to build `main`, `worker-1`, `worker-2`), runs `st.setup(y)` (running full `Submit` + `MergeGate`), arms a `/bin/sh` wrapper to fail at `git` call `n`, runs `st.call(y)`, disarms, and runs `st.call(y)` again.
- **Cost:** **~85 full multi-worktree Git repos** and **~2,000+ `/bin/sh` + `git` subprocesses** in one test function—including cutting read-only `git rev-parse` and `git status` calls that cannot mutate state.

### 2.3 Hardcoded Multi-Second Sleeps Across the Test Suite (~30+ Seconds)
| File & Lines | Test Function | Root Cause | Wall-Clock Sleep |
| :--- | :--- | :--- | ---: |
| [`cmd/obor/plugin_wiring_test.go:445-517`](file:///Users/djordjeradakovic/Work/obor-kit/cmd/obor/plugin_wiring_test.go#L445-L517) | `TestOtherHooksNoBinaryAllow` | Loops over 4 hooks (`stop`, `user-prompt-submit`, `stop-failure`, `session-start`), replacing `dist/obor` with `exec sleep 30` to trigger `bin/obor`'s hardcoded `( sleep 2; kill -KILL ... )` watchdog 4 times | **8.0s** |
| [`cmd/obor/plugin_wiring_test.go:415-439`](file:///Users/djordjeradakovic/Work/obor-kit/cmd/obor/plugin_wiring_test.go#L415-L439) | `TestPreToolUseHangingBinaryDenies` | Same `exec sleep 30` against `bin/obor`'s `sleep 2` watchdog + `reaped()` polling | **2.0s+** |
| [`cmd/obor/shim_test.go:369-399`](file:///Users/djordjeradakovic/Work/obor-kit/cmd/obor/shim_test.go#L369-L399) | `TestShimHookSkipsHangingDevBuild` | Same `exec sleep 30` against `bin/obor`'s `sleep 2` watchdog | **2.0s** |
| [`internal/checks/nightly_test.go:267-274`](file:///Users/djordjeradakovic/Work/obor-kit/internal/checks/nightly_test.go#L267-L274) | `TestNightlyKeepsConcurrentMergeDue` | 3 subtests calling `time.Sleep(time.Until(time.Unix(time.Now().Unix()+1, 0)))` twice to cross Unix 1-second timestamp boundaries | **3.0s – 5.0s** |
| [`internal/checks/nightly_test.go:81-124`](file:///Users/djordjeradakovic/Work/obor-kit/internal/checks/nightly_test.go#L81-L124) | `TestNightlyTimeoutFails`, `TestNightlyWaitsForQuietMergeWindow`, `TestNightlyCancelDuringQuietWait` | Runs `sh -c "sleep 8"` with `timeout: 1` (1s), `merge.window_wait = 1` polling `time.After(time.Second)` in [`checks.go:206`](file:///Users/djordjeradakovic/Work/obor-kit/internal/checks/checks.go#L206) across 2 subtests (2s), and `1500ms` cancel timer (1.5s) | **4.5s** |
| [`internal/checks/flakes_test.go:211`](file:///Users/djordjeradakovic/Work/obor-kit/internal/checks/flakes_test.go#L211) | `TestSetupLaneTimeout` | Runs `sleep 5` with `timeout: 1` | **1.0s** |
| [`internal/run/run_test.go:84,300`](file:///Users/djordjeradakovic/Work/obor-kit/internal/run/run_test.go#L84-L300) | `TestRun`, `TestExecuteLeavesNoGroup` | Runs `sleep 5` with `timeout: 1` (1s) + bash `TERM` trap with `Timeout: 3` (3.5s) | **4.5s** |
| [`cmd/obor/main_test.go:262`](file:///Users/djordjeradakovic/Work/obor-kit/cmd/obor/main_test.go#L262) & [`send_test.go:151`](file:///Users/djordjeradakovic/Work/obor-kit/cmd/obor/send_test.go#L151) | `TestRestartCommand`, `TestSendRestartFlag` | Sets `OBOR_RESTART_WAIT=1` (parsed as integer seconds in [`launch.go:173`](file:///Users/djordjeradakovic/Work/obor-kit/internal/launch/launch.go#L173)) with `OnStart = "never"` | **2.0s** |
| [`internal/launch/launch_test.go:1895-3030`](file:///Users/djordjeradakovic/Work/obor-kit/internal/launch/launch_test.go#L1895-L3030) | `TestStopForceTerminates`, `TestStopEveryKind`, `TestStopPerKind`, etc. | 35+ subtests intentionally timing out against `shortWait = 60ms` / `200ms` / `5*shortWait` (also causing the CI flakes logged in `BACKLOG.md:90`) | **6.0s – 8.0s** |

### 2.4 CLI Subprocess Spawns & E2E `make release` Runs in `cmd/obor`
- [`cmd/obor/main_test.go:23-60`](file:///Users/djordjeradakovic/Work/obor-kit/cmd/obor/main_test.go#L23-L60) compiles `oborBin` in `TestMain` and spawns `exec.Command(oborBin, ...)` **250+ times** to test CLI flag validation and text output (`TestHas`: 24 processes; `TestPauseRequest`: 17 processes; `TestWatchRequest`: 18 processes; `TestConfigSet`: 35+ processes; `TestStatus`: 35+ processes).
- [`cmd/obor/release_test.go`](file:///Users/djordjeradakovic/Work/obor-kit/cmd/obor/release_test.go) (821 LOC), [`release_steps_test.go`](file:///Users/djordjeradakovic/Work/obor-kit/cmd/obor/release_steps_test.go) (491 LOC), and [`makefile_test.go`](file:///Users/djordjeradakovic/Work/obor-kit/cmd/obor/makefile_test.go) (532 LOC) execute `make release`, `make ship`, `make publish`, and `make build` **55+ times** across fresh multi-worktree Git repos (~1,400 subprocesses).

### 2.5 Ticket-Per-File Fragmentation, Prose Change-Detectors & Duplicate Test Rigs
1. **74 Test Files in `internal/app` (23,297 LOC) & 39 Test Files in `cmd/obor` (9,860 LOC):**
   - Instead of adding rows to table-driven tests, workers created new files or `TestTxxx` / `TestRVxx` functions per ticket:
     - **Pause / Watch / Hold / Resume:** split across `pause_test.go`, `pause_watch_test.go`, `hold_watch_test.go`, `resume_clear_test.go`, `watch_test.go`, `watch_clear_test.go`, `stopped_test.go`, `stopwake_test.go`.
     - **Dispatch / Job / Prompt / Spec:** split across 15+ files (`dispatch_test.go`, `dispatch_order_test.go`, `dispatch_guard_test.go`, `jobs_test.go`, `job_next_test.go`, `fetch_test.go`, `prompts_test.go`, `work_prompt_test.go`, `review_prompt_test.go`, `integrator_prompt_test.go`, `specs_test.go`, `spec_show_test.go`, etc.).
     - **Ticket-Named Functions:** [`internal/app/concurrency_test.go:240-760`](file:///Users/djordjeradakovic/Work/obor-kit/internal/app/concurrency_test.go#L240-L760) consists of functions named `TestRV28...`, `TestRV32...`, `TestRV33...`, `TestRV34...`, `TestT504...`, `TestT526...`, `TestT555...`, `TestT556...`, `TestT580...`, each spinning up a full `newLoopRig` / `newConcRig`.
2. **14 Separate `TestStop*` Functions in [`internal/launch/launch_test.go:1800-3133`](file:///Users/djordjeradakovic/Work/obor-kit/internal/launch/launch_test.go#L1800-L3133):**
   - `TestStop`, `TestStopForceTerminates`, `TestStopRefusesForeignPane`, `TestStopDeadPane`, `TestStopOpenCode`, `TestStopInTurnInterruptsBeforeExit`, `TestStopUnprovenPaneDoesNotSignal`, `TestStopUnprovenLeftovers`, `TestStopCleansProvenDescendants`, `TestStopSlowExitAfterQuit`, `TestStopEveryKind`, `TestStopPerKind`, `TestStopVerify`, `TestStopKillsAdoptedChild` all test overlapping combinations of `{claude, codex, opencode} × {idle, in-turn, hung, dead, unproven}`.
3. **Markdown Prose "Change-Detector" Tests:**
   - [`internal/setup/setup_test.go:195-650`](file:///Users/djordjeradakovic/Work/obor-kit/internal/setup/setup_test.go#L195-L650) (`TestSyncTemplates`, `TestAgentsTemplates`, `TestSkillsTemplates`, `TestRuleTemplates`) and [`cmd/obor/plugin_wiring_test.go:216-299`](file:///Users/djordjeradakovic/Work/obor-kit/cmd/obor/plugin_wiring_test.go#L216-L299) (`TestSkillPrompts`) spend ~550 lines asserting exact English phrases inside `templates/agents/*.md` and `skills/*/SKILL.md`.
4. **Duplicated Test Rigs & Socket Stubs:**
   - **7 rig builders in `internal/app`:** `newRig` (`app_test.go:35`), `newLoopRig` (`loop_test.go:69`), `newDispatchRig` (`dispatch_test.go:32`), `fetchRig` (`fetch_test.go:25`), `lineRig` (`messages_test.go:28`), `newConcRig` (`concurrency_test.go:30`), `lineApp` (`rolestop_test.go:24`).
   - **4 fake `serve.sock` servers:** `serveStub` ([`cmd/obor/status_test.go:47`](file:///Users/djordjeradakovic/Work/obor-kit/cmd/obor/status_test.go#L47)), `fakeServe` ([`cmd/obor/monitor_test.go:31`](file:///Users/djordjeradakovic/Work/obor-kit/cmd/obor/monitor_test.go#L31)), `serveSocket` ([`internal/mcp/mcp_test.go:86`](file:///Users/djordjeradakovic/Work/obor-kit/internal/mcp/mcp_test.go#L86)), and inline `net.Listen` ([`internal/app/verdict_test.go:37`](file:///Users/djordjeradakovic/Work/obor-kit/internal/app/verdict_test.go#L37)).

---

## 3. Deep Dive: Runtime Performance Bottlenecks & How to Fix Them

### 3.1 Herdr Pane Events Trigger a `git` Subprocess Storm in `obor serve`
Trace what happens inside `obor serve` on **every single herdr pane status event** (e.g., whenever any agent flips between `working` and `idle`):

1. [`App.event`](file:///Users/djordjeradakovic/Work/obor-kit/internal/app/panes.go#L94-L135) calls [`a.check(ctx, false)`](file:///Users/djordjeradakovic/Work/obor-kit/internal/app/panes.go#L134).
2. [`App.check`](file:///Users/djordjeradakovic/Work/obor-kit/internal/app/watchdog.go#L18-L39) calls [`a.decideLanes(ctx, p)`](file:///Users/djordjeradakovic/Work/obor-kit/internal/app/watchdog.go#L30) **and** [`a.wake()`](file:///Users/djordjeradakovic/Work/obor-kit/internal/app/watchdog.go#L38).
3. `decideLanes` calls [`laneTable.Lanes(p.root)`](file:///Users/djordjeradakovic/Work/obor-kit/cmd/obor/serve.go#L256-L272) → [`LaneTable.States()`](file:///Users/djordjeradakovic/Work/obor-kit/internal/checks/lanetable.go#L44-L59), which spawns **3 to 4 `git` subprocesses per lane** (24–32 `git` processes for an 8-lane project):
   - `git -C <laneDir> rev-parse --show-toplevel HEAD --symbolic-full-name HEAD`
   - `git -C <root> merge-base --is-ancestor <head> HEAD`
   - `git -C <laneDir> status --porcelain`
   - `git -C <root> log -F --grep=<id>: --format=\x1e%s --name-only HEAD` (when checking `MergeEvidence`)
4. Then `a.wake()` wakes [`advance()`](file:///Users/djordjeradakovic/Work/obor-kit/internal/app/loop.go#L295-L331), which iterates all non-merged tasks and calls [`a.drive`](file:///Users/djordjeradakovic/Work/obor-kit/internal/app/loop.go#L534-L563).
5. At the top of `drive` ([`loop.go:542-554`](file:///Users/djordjeradakovic/Work/obor-kit/internal/app/loop.go#L542-L554)), for **every task** with `states[mt.state].landed == true` (which in [`states.go:55-114`](file:///Users/djordjeradakovic/Work/obor-kit/internal/app/states.go#L55-L114) includes `stWorking`, `stNeedsYou`, `stSubmitted`, `stGreen`, `stAwaiting`, `stRed`, `stConflict`, `stResolving`, `stFail`!), it calls [`fx.Scratch.Landed(ctx, task)`](file:///Users/djordjeradakovic/Work/obor-kit/internal/checks/scratch.go#L468-L480), spawning:
   - `git -C <root> rev-parse refs/heads/main`
   - `git -C <root> log -F --grep=<task>: --format=\x1e%s --name-only refs/heads/main` (walking the **entire repo commit history and diffing trees**!)

**How It Should Be Done:**
- **Decouple Herdr status events from full Git worktree scans:** In [`App.check(ctx, full)`](file:///Users/djordjeradakovic/Work/obor-kit/internal/app/watchdog.go#L18-L39), only call `a.lanes.Lanes(p.root)` when `full == true` (on the 30s tick / initial connect) or when no cached `p.lanes` exists; on `full == false` (a single pane status event), use the existing `p.lanes` slice (which `dispatch`, `task_done`, and `drop` already update in memory).
- **Cache `Scratch.Landed` by `refs/heads/<main>` SHA:** Store `lastTargetSHA string` and `landedChecked map[string]string` (`task -> targetSHA`) on `proj`. If `refs/heads/<main>` has not moved since `task` was last checked, `Scratch.Landed(ctx, task)` is an $O(1)$ map lookup instead of a full `git log --name-only` scan!

### 3.2 `obor web` Spawns 24 `git` Subprocesses to Read 1 SQLite Row (`/api/job`, `/api/task`, `/api/agent`)
Look at [`Server.detail`](file:///Users/djordjeradakovic/Work/obor-kit/internal/web/web.go#L175-L193) in `internal/web/web.go`:
```go
func (s *Server) jobAPI(w http.ResponseWriter, r *http.Request) {
    d, id, ok := s.detail(w, r, "job") // <-- calls s.build(ctx, p.Root) -> view.Load()!
    if !ok {
        return
    }
    p, ok := d.Job(id)                 // <-- ignores d.Project completely, queries 1 SQLite row!
```
- Calling `/api/job?project=obor-kit&job=T-1/1/work` calls `s.detail`, which calls [`view.Load`](file:///Users/djordjeradakovic/Work/obor-kit/internal/view/load.go#L17-L28), spawning **24 `git` subprocesses** (`LaneTable.States()`), dialing `serve.sock`, querying all specs and events, and building `view.Project`—only for [`d.Job(id)`](file:///Users/djordjeradakovic/Work/obor-kit/internal/view/detail.go#L244-L258) to ignore `d.Project`, open another SQLite connection, and run `SELECT ... FROM prompts WHERE job = ? LIMIT 1`!
- `/api/projects` ([`web.go:90-113`](file:///Users/djordjeradakovic/Work/obor-kit/internal/web/web.go#L90-L113)) runs `view.Load` sequentially across every project on the machine (72+ `git` processes for 3 projects).
- [`obor monitor`](file:///Users/djordjeradakovic/Work/obor-kit/cmd/obor/monitor.go#L143-L148) runs `checks.NewLaneTable(m.cfg)` (24 `git` processes) + `serveProcess` (`launchctl print` on macOS) **every 2 seconds**!

**How It Should Be Done:**
- In `jobAPI`, open SQLite directly via `view.Detail{Root: p.Root}.Job(id)` without calling `s.build`.
- Cache `view.Load` in `obor web` with a short TTL (e.g., 1–2s invalidated when `events.seq` advances) and parallelize `/api/projects` across projects.
- In `obor monitor`, only re-run `LaneTable.States()` and `launchctl print` when `seq != m.seq` or every 10–15 seconds, using cached lane/launchd state for the 2-second elapsed-time redraws.

### 3.3 $O(N^3)$ Session Ownership Scan in `internal/store/sessions.go`
In [`internal/store/sessions.go:31-81`](file:///Users/djordjeradakovic/Work/obor-kit/internal/store/sessions.go#L31-L81):
- `Current(ss, agent)` scans `ss` ($O(N)$).
- `Holder(ss, agent, id)` loops over `ss` and calls `Current(ss, s.Agent)` inside the loop ($O(N^2)$).
- `Admits(ss, s)` calls `Holder`, and `Owned(ss)` calls `Admits(out, s)` for each session in `ss` (**$O(N^3)$**).
- Furthermore, [`p.foldSession`](file:///Users/djordjeradakovic/Work/obor-kit/internal/app/sessions.go#L71-L79) calls `store.Admits(p.folded.sessions, s)` on every session record during replay, and [`view.Detail.Agent`](file:///Users/djordjeradakovic/Work/obor-kit/internal/view/detail.go#L210-L222) calls `store.ReadSessionsAt(d.Root)` (opening SQLite #1 and running $O(N^3)$ `Owned`) before immediately calling `d.open()` (opening SQLite #2).

**How It Should Be Done ($O(N)$ Single-Pass `Owned`):**
```go
func Owned(ss []Session) []Session {
	out := make([]Session, 0, len(ss))
	seen := make(map[[2]string]bool, len(ss))
	current := make(map[string]Session) // agent -> newest Session
	holder := make(map[string]string)   // sessionID -> current owner agent

	for _, s := range ss {
		if s.Agent == "" || s.ID == "" || seen[[2]string{s.Agent, s.ID}] {
			continue
		}
		if h := holder[s.ID]; h != "" && h != s.Agent {
			continue
		}
		seen[[2]string{s.Agent, s.ID}] = true
		if prev, ok := current[s.Agent]; !ok || s.Started >= prev.Started {
			if ok && holder[prev.ID] == s.Agent {
				delete(holder, prev.ID)
			}
			current[s.Agent] = s
			holder[s.ID] = s.Agent
		}
		out = append(out, s)
	}
	return out
}
```

### 3.4 Missing SQLite Indexes, Unconditional `syncLanes`, Duplicate `json.Unmarshal`, and Uncached Config Reads
1. **Zero Secondary Indexes in SQLite Schema ([`internal/store/store.go:146-158`](file:///Users/djordjeradakovic/Work/obor-kit/internal/store/store.go#L146-L158)):**
   - Every query filtering by `task`, `kind`, `step`, `job`, or `agent` performs a **full table scan** on `events` or `prompts`:
     - [`store.HasBlockedReportAt`](file:///Users/djordjeradakovic/Work/obor-kit/internal/store/lanes.go#L117-L141) (runs on every worker `Stop` hook)
     - [`view.dispatchedTasks`](file:///Users/djordjeradakovic/Work/obor-kit/internal/view/queue.go#L253) (runs on every `obor status`, every 2s `obor monitor` frame, and every `obor web` view)
     - [`view.taskRecords`](file:///Users/djordjeradakovic/Work/obor-kit/internal/view/detail.go#L279-L300) & [`view.agentLines`](file:///Users/djordjeradakovic/Work/obor-kit/internal/view/detail.go#L304)
     - [`store.ReadPrompts`](file:///Users/djordjeradakovic/Work/obor-kit/internal/store/prompts.go#L63-L88) & [`app.prompt`](file:///Users/djordjeradakovic/Work/obor-kit/internal/app/prompts.go#L26) (scans `prompts` with full prompt text on every `job_next`!)
   - **Fix:** Add secondary indexes to `schema` in `internal/store/store.go`:
     ```sql
     CREATE INDEX IF NOT EXISTS idx_events_task_seq ON events(task, seq DESC);
     CREATE INDEX IF NOT EXISTS idx_events_kind_step ON events(kind, step);
     CREATE INDEX IF NOT EXISTS idx_prompts_job ON prompts(job, id DESC);
     CREATE INDEX IF NOT EXISTS idx_prompts_task ON prompts(task, id DESC);
     CREATE INDEX IF NOT EXISTS idx_prompts_agent ON prompts(agent, id DESC);
     ```
2. **Unconditional `p.syncLanes()` on Every Event Fold ([`internal/app/project.go:97-99`](file:///Users/djordjeradakovic/Work/obor-kit/internal/app/project.go#L97-L99)):**
   - `foldRecord(r)` calls `p.syncLanes()` on **every single record** (even `msg`, `watch`, `session`, `nightly`, `review`), allocating and sorting a `[]store.LaneRow` slice every time. And when a row changes, [`store.WriteLanes`](file:///Users/djordjeradakovic/Work/obor-kit/internal/store/lanes.go#L43-L63) runs `DELETE FROM lanes` + re-inserts all rows while `a.mu` is held.
   - **Fix:** Have `foldLanes` mark a `dirty bool` flag when `p.folded.lr.rows` or `submitted` actually changes, and only run `syncLanes()` when `dirty` is true.
3. **5–7x Repeated `json.Unmarshal` Per Event Fold ([`internal/app/project.go:74-100`](file:///Users/djordjeradakovic/Work/obor-kit/internal/app/project.go#L74-L100)):**
   - [`r.Fact()`](file:///Users/djordjeradakovic/Work/obor-kit/internal/app/log.go#L53-L59) runs `json.Unmarshal(r.Data, &f)` on every call and is invoked up to 5–7 times for the same `Record` across `fold`, `foldLoop`, `foldLanes`, `foldMsg`, `foldWatch`, `foldReview`, `foldWake`.
4. **Uncached `config.LoadDaemon(p.root)` in Hot Loops:**
   - [`p.roleAgent(agent)`](file:///Users/djordjeradakovic/Work/obor-kit/internal/app/rolestop.go#L278-L296) calls `config.LoadDaemon(p.root)` inside `for _, o := range a.obs` in [`a.stoppedRoles(p)`](file:///Users/djordjeradakovic/Work/obor-kit/internal/app/rolestop.go#L92-L105)—reading and parsing `.obor/config.json` + `.obor/local.json` from disk once per idle shell pane on every `obor status` call!
   - [`namedAgents(p.root)`](file:///Users/djordjeradakovic/Work/obor-kit/internal/app/registry.go#L237-L246) reloads config from disk on every `reconcile(p)` (every herdr event).
   - **Fix:** Add an mtime-cached config loader on `proj` (checking `os.Stat` on `.obor/config.json` and `.obor/local.json`).
5. **Redundant Herdr Socket Calls ([`internal/herdr/socket.go:1002-1016`](file:///Users/djordjeradakovic/Work/obor-kit/internal/herdr/socket.go#L1002-L1016) & [`panes.go:106-146`](file:///Users/djordjeradakovic/Work/obor-kit/internal/herdr/panes.go#L106-L146)):**
   - On every status event, `st.reread(pane)` dials two separate Unix socket connections (`agent.get` + `pane.get`), and then `watchdog.go` immediately calls `panes.Activity` and `panes.Turn`, which dial `agent.get` **again** to fetch the transcript path that `reread` already placed on `Pane.Transcript`.

---

## 4. Deep Dive: Wrong Architecture, Duplication & Unnecessary Code

### 4.1 `internal/app` God-Package (39 Files, 11,815 LOC) & 7 Mutexes
`internal/app` violates `AGENTS.md`'s prime directive (*"Small packages, one job each, readable top to bottom"*) by combining 11 distinct subsystems into one package sharing private struct fields on [`App`](file:///Users/djordjeradakovic/Work/obor-kit/internal/app/app.go#L142-L172), [`proj`](file:///Users/djordjeradakovic/Work/obor-kit/internal/app/app.go#L176-L235), and [`mtask`](file:///Users/djordjeradakovic/Work/obor-kit/internal/app/loop.go#L156-L201) (41 fields):
1. Event log & SQLite projection writer (`log.go`, `lanerows.go`, `specs.go`, `prompts.go`, `sessions.go`)
2. Daemon lifecycle & Unix socket RPC server (`app.go`, `serve.go`, `status.go`)
3. Herdr stream consumer & terminal identity reconciler (`panes.go`, `registry.go`)
4. Watchdogs & turn/activity sampler (`watchdog.go`, `jobstart.go`, `settle.go`)
5. Merge loop state machine & scheduler (`loop.go`, `mtask.go`, `states.go`, `steps.go`, `scheduler.go`, `effects.go`)
6. Task dispatcher (`dispatch.go`, `fetch.go`)
7. Verdict & human intervention handler (`verdict.go`, `answer.go`, `pause.go`)
8. Message & brief outbox (`messages.go`)
9. Threshold code review orchestrator (`review.go`)
10. Rate-limit stop/wake engine & idle role auto-stopper (`stopped.go`, `stopwake.go`, `rolestop.go`)
11. Nightly runner, low-disk monitor & merge watch notifier (`nightly.go`, `disk.go`, `mergewatch.go`, `notify.go`)

**Why the 7 Mutexes Cause Races and Hangs:**
- `App` holds `mu`, `verdicts`, `loopMu`, `submitMu`, `sessions`, `notifying`, plus `outbox.claim` and `reviewClaim.mu`.
- Because `a.mu` guards both in-memory reads (`App.Status()`) and synchronous SQLite `WriteLanes` transactions (`p.syncLanes()`), disk I/O stalls all readers and event handlers.
- Furthermore, `Log` ([`internal/app/log.go:112-125, 242-252`](file:///Users/djordjeradakovic/Work/obor-kit/internal/app/log.go#L112-L125)) retains every `KindIntent` and `KindResult` record (including full JSON payloads in `Record.Data`) in `l.intents` and `l.results` forever, and `p.folded.loop` never evicts old tasks.

### 4.2 Quadruple Duplication of Lane & Task State
A single task's lane assignment and state are stored in **4 separate places**:
1. `p.folded.lr.rows` (`map[string]store.LaneRow` in [`internal/app/lanerows.go:18`](file:///Users/djordjeradakovic/Work/obor-kit/internal/app/lanerows.go#L18))
2. SQLite `lanes` table ([`internal/store/lanes.go`](file:///Users/djordjeradakovic/Work/obor-kit/internal/store/lanes.go))
3. `p.folded.loop[task]` (`*mtask` in [`internal/app/loop.go:156-201`](file:///Users/djordjeradakovic/Work/obor-kit/internal/app/loop.go#L156-L201))
4. `p.lanes` (`[]Lane`) & `p.tasks` (`map[string]*watch`) ([`internal/app/project.go:209-234`](file:///Users/djordjeradakovic/Work/obor-kit/internal/app/project.go#L209-L234)), rebuilt by calling [`laneTable.Lanes(root)`](file:///Users/djordjeradakovic/Work/obor-kit/cmd/obor/serve.go#L256-L272), which opens SQLite read-only to read back the `lanes` table that `serve` itself just wrote!

### 4.3 Duplicated Code Across Packages
| Duplicated Logic | Locations | How to Consolidate |
| :--- | :--- | :--- |
| **Step-number stripping (`step.N` → `step, N`)** | [`internal/app/loop.go:259-266`](file:///Users/djordjeradakovic/Work/obor-kit/internal/app/loop.go#L259-L266) (`stepN`) vs [`internal/view/events.go:175-182`](file:///Users/djordjeradakovic/Work/obor-kit/internal/view/events.go#L175-L182) (`baseStep`) | Export `app.StepN(step)` and delete `view.baseStep`. |
| **Ad-hoc JSON field extraction on `Record.Data`** | [`internal/view/detail.go:346-369`](file:///Users/djordjeradakovic/Work/obor-kit/internal/view/detail.go#L346-L369) (`dataString`, `dataNumber`) vs [`internal/view/events.go:89-109`](file:///Users/djordjeradakovic/Work/obor-kit/internal/view/events.go#L89-L109) (`eventData`) vs [`internal/app/log.go:27-77`](file:///Users/djordjeradakovic/Work/obor-kit/internal/app/log.go#L27-L77) | Use typed `app.FactData` / `gateOut` / `submitData` structs instead of unmarshaling `map[string]json.RawMessage` on every field access. |
| **Status vs Monitor text rendering** | [`cmd/obor/status.go:127-350`](file:///Users/djordjeradakovic/Work/obor-kit/cmd/obor/status.go#L127-L350) vs [`cmd/obor/monitor_render.go:103-319`](file:///Users/djordjeradakovic/Work/obor-kit/cmd/obor/monitor_render.go#L103-L319) | Share table column/row formatters for lanes, roles, needs-you, and queue items. |
| **Git worktree discovery** | [`internal/guard/guard.go:226-246`](file:///Users/djordjeradakovic/Work/obor-kit/internal/guard/guard.go#L226-L246) (`loadWorktrees`) vs [`internal/project/project.go:73-115`](file:///Users/djordjeradakovic/Work/obor-kit/internal/project/project.go#L73-L115) (`findFromGitFiles`) vs [`internal/checks/lanetable.go:75`](file:///Users/djordjeradakovic/Work/obor-kit/internal/checks/lanetable.go#L75) | Centralize worktree inspection in `internal/project`. |
| **`store.OpenRO` + `os.Stat` boilerplate** | Repeated 8 times across [`store/lanes.go`](file:///Users/djordjeradakovic/Work/obor-kit/internal/store/lanes.go#L68-L125), [`store/specs.go`](file:///Users/djordjeradakovic/Work/obor-kit/internal/store/specs.go#L67-L110), [`store/prompts.go`](file:///Users/djordjeradakovic/Work/obor-kit/internal/store/prompts.go#L92-L114), [`store/sessions.go`](file:///Users/djordjeradakovic/Work/obor-kit/internal/store/sessions.go#L86-L92) | Use `openROAt(root)` (`store/specs.go:112`) in all `Read*At` functions. |

### 4.4 Over-Engineered Subsystems to Simplify
1. **Static Bash Emulator in [`internal/guard/shell.go`](file:///Users/djordjeradakovic/Work/obor-kit/internal/guard/shell.go) (1,304 LOC):**
   - Emulates `cd` directory stacks, variable assignments (`X=`, `+=`, `export`), parameter expansion, field splitting, command wrappers (`env`, `sudo`, `xargs`, `nohup`, `timeout`, `stdbuf`), `find -exec`, nested `sh -c` / `eval` up to depth 8, `sed -i` / `cp` / `mv` target resolution, and `go test` flag parsing. Simplifying `shell.go` to walk AST `CallExpr` and redirect nodes without full variable/subshell emulation would cut ~700 LOC.
2. **English Rate-Limit Regex Parser & Wake Engine ([`internal/app/stopped.go`](file:///Users/djordjeradakovic/Work/obor-kit/internal/app/stopped.go) 434 LOC + [`stopwake.go`](file:///Users/djordjeradakovic/Work/obor-kit/internal/app/stopwake.go) 291 LOC = 725 LOC):**
   - Parses human-readable English error strings (`"resets 2pm (Europe/Belgrade)"`, `"try again at 3:30 AM"`, `"in 2 hours"`) from transcripts and hooks to schedule wake timers, duplicating Claude Code's native usage-limit auto-continuation (`autoContinue` in `guard/prompt.go:136`).
3. **Second Mini-Dispatcher for `obor review` ([`internal/app/review.go`](file:///Users/djordjeradakovic/Work/obor-kit/internal/app/review.go), 634 LOC):**
   - Implements a parallel scheduling subsystem inside `internal/app` with its own mutex (`reviewClaim`), `--after` wait list, held-task check, git diffstat counter, and prompt builder instead of modeling a review as a standard job/task in the existing scheduler.

---

## 5. Next Steps: Actionable Roadmap for Lead & Workers

Each step below is scoped so the lead can dispatch it as an isolated task (`make check` as the gate).

### Wave 1: Make the Test Suite 4–5x Faster (Zero Coverage Loss)
1. **Task 1A — Template-Copy Git Fixtures & Cached SQLite in `internal/testkit` (`Difficulty: medium`)**:
   - In [`internal/testkit/testkit.go`](file:///Users/djordjeradakovic/Work/obor-kit/internal/testkit/testkit.go):
     - Build one base git repo (`main` branch + initial commit + default `.obor/config.json` commit) once per test binary using `sync.Once` in a temp dir, and have `testkit.New(t)` copy that directory (`os.CopyFS`) into `t.TempDir()`.
     - Keep a single cached `*sql.DB` writer connection on `*testkit.Project` (with `PRAGMA synchronous=OFF`, closed in `t.Cleanup`) so `p.Dispatched`, `p.SubmitAgo`, `p.eventAt`, `p.rows`, and `p.setRow` reuse one connection instead of opening/migrating/closing SQLite 5 times per call.
2. **Task 1B — Split Light vs Git Fixtures in `launch_test.go` and `guard_test.go` (`Difficulty: simple`)**:
   - In [`internal/launch/launch_test.go`](file:///Users/djordjeradakovic/Work/obor-kit/internal/launch/launch_test.go#L105-L115), make `setup(t)` write `.obor/config.json` and `mkdir -p` the lane directory without spawning `git init` / `git worktree add` / `git commit`, keeping a `setupGit(t)` helper only for tests that run real `git` commands.
   - In [`internal/guard/guard_test.go`](file:///Users/djordjeradakovic/Work/obor-kit/internal/guard/guard_test.go#L22-L33), share one read-only git worktree layout across pure AST/path-prefix tests.
3. **Task 1C — Eliminate the ~30s of Hardcoded Test Sleeps (`Difficulty: simple`)**:
   - In [`bin/obor`](file:///Users/djordjeradakovic/Work/obor-kit/bin/obor), change `( sleep 2; kill -KILL ... )` to `( sleep "${OBOR_HOOK_TIMEOUT:-2}"; kill -KILL ... )` and set `OBOR_HOOK_TIMEOUT=0.05` in [`cmd/obor/shim_test.go:369`](file:///Users/djordjeradakovic/Work/obor-kit/cmd/obor/shim_test.go#L369) and [`cmd/obor/plugin_wiring_test.go:415,445`](file:///Users/djordjeradakovic/Work/obor-kit/cmd/obor/plugin_wiring_test.go#L415-L517) (and collapse the 4 duplicate hanging-binary loops in `TestOtherHooksNoBinaryAllow` into 1).
   - In [`internal/launch/launch.go:173`](file:///Users/djordjeradakovic/Work/obor-kit/internal/launch/launch.go#L173), allow `OBOR_RESTART_WAIT` to parse `time.ParseDuration` (e.g. `20ms`) in addition to integer seconds, and change `OBOR_RESTART_WAIT=1` to `20ms` in `cmd/obor/main_test.go:262` and `send_test.go:151`.
   - In [`internal/checks/nightly_test.go:267-274`](file:///Users/djordjeradakovic/Work/obor-kit/internal/checks/nightly_test.go#L267-L274), pass explicit timestamps instead of `time.Sleep(time.Until(time.Unix(time.Now().Unix()+1, 0)))`.
4. **Task 1D — Trim `TestScratchCutTable` & Run `cmd/obor` Flag Tests In-Process (`Difficulty: medium`)**:
   - In [`internal/checks/scratch_test.go:391-432`](file:///Users/djordjeradakovic/Work/obor-kit/internal/checks/scratch_test.go#L391-L432), only cut at mutating git verbs (`worktree`, `merge`, `reset`, `update-ref`, `commit`, `branch`, `checkout`) instead of read-only queries (`rev-parse`, `status`, `diff`, `show-ref`, `log`), and copy the pre-setup fixture directory for each cut index `n`.
   - In `cmd/obor`, call `runCLI` in-process for flag/validation/status tests instead of spawning `exec.Command(oborBin)` 250+ times.

### Wave 2: Fix Runtime Performance Bottlenecks in `serve`, `store`, `web`, and `monitor`
5. **Task 2A — Stop the Herdr-Event `git` Subprocess Storm in `internal/app` (`Difficulty: medium`)**:
   - In [`internal/app/watchdog.go:18-39`](file:///Users/djordjeradakovic/Work/obor-kit/internal/app/watchdog.go#L18-L39), only call `a.lanes.Lanes(p.root)` (`LaneTable.States()`) when `full == true` (the 30s tick / initial connect) or when `p.lanes == nil`; on `full == false` (single pane status events), reuse `p.lanes`.
   - In [`internal/app/loop.go:542-554`](file:///Users/djordjeradakovic/Work/obor-kit/internal/app/loop.go#L542-L554), cache the last checked `refs/heads/<main>` SHA per task so `fx.Scratch.Landed(ctx, task)` only runs `git log -F --grep=<task>: --name-only` when `<main>` has actually advanced.
   - In [`cmd/obor/serve.go:262-271`](file:///Users/djordjeradakovic/Work/obor-kit/cmd/obor/serve.go#L262-L271), pass `p.folded.lr.rows` from `serve` into `LaneTable` instead of opening SQLite read-only to read back `serve`'s own `lanes` table.
6. **Task 2B — Add SQLite Indexes, Fix $O(N^3)$ `store.Owned`, and Gate `syncLanes` (`Difficulty: simple`)**:
   - Add the 5 secondary indexes (`idx_events_task_seq`, `idx_events_kind_step`, `idx_prompts_job`, `idx_prompts_task`, `idx_prompts_agent`) to [`internal/store/store.go:146-158`](file:///Users/djordjeradakovic/Work/obor-kit/internal/store/store.go#L146-L158).
   - Replace the $O(N^3)$ `Owned` / `Admits` / `Holder` implementation in [`internal/store/sessions.go:31-81`](file:///Users/djordjeradakovic/Work/obor-kit/internal/store/sessions.go#L31-L81) with the single-pass $O(N)$ map implementation.
   - In [`internal/app/project.go:74-100`](file:///Users/djordjeradakovic/Work/obor-kit/internal/app/project.go#L74-L100), only call `p.syncLanes()` when `foldLanes` actually modified a lane row, and unmarshal `r.Fact()` once per `foldRecord` call.
   - Cache `config.LoadDaemon(p.root)` on `proj` by file mtime so `stoppedRoles` (`rolestop.go:282`) and `reconcile` (`registry.go:238`) do not re-read `.obor/config.json` from disk on every pane loop and herdr event.
7. **Task 2C — Fix `obor web` `/api/job`, `/api/task`, `/api/agent` & `obor monitor` Git Overhead (`Difficulty: simple`)**:
   - In [`internal/web/web.go:159-193`](file:///Users/djordjeradakovic/Work/obor-kit/internal/web/web.go#L159-L193), have `jobAPI` query `view.Detail{Root: p.Root}.Job(id)` directly without calling `s.build(ctx, p.Root)` (saving 24 `git` subprocesses per job click), and reuse a single SQLite connection in `view.Detail.Agent`.

### Wave 3: Consolidate the Test Suite (~188 → ~90 Files, ~24k LOC Removed)
8. **Task 3A — Consolidate `internal/launch/launch_test.go` (3,133 → ~1,200 LOC)**:
   - Merge the 14 `TestStop*` functions (`launch_test.go:1800-3133`) into two table-driven tests (`TestStopLifecycleTable`, `TestStopProcessCleanupTable`) with a step-advancing fake poll timer instead of 60ms/200ms wall-clock waits.
   - Merge the 12 `TestStart*` and 10 `TestRestart*` functions into table-driven `TestStartTable` and `TestRestartTable`.
9. **Task 3B — Unify the 7 Test Rigs & Consolidate 74 Test Files in `internal/app` (23.3k → ~12k LOC)**:
   - Replace `newRig`, `newLoopRig`, `newDispatchRig`, `fetchRig`, `lineRig`, `newConcRig`, and `lineApp` with 2 shared helpers in `internal/app/rig_test.go`: a synchronous state-machine rig (`newUnitRig`, driving steps directly without background `app.Serve` polling goroutines) and a full loop rig (`newServeRig`).
   - Fold the 74 ticket-fragmented `*_test.go` files into ~14 domain test files matching the production subsystems.
10. **Task 3C — Consolidate 39 Test Files in `cmd/obor` & Trim Prose Change-Detectors in `internal/setup`**:
    - Move `serveStub` / `fakeServe` / `serveSocket` into `internal/testkit`.
    - Merge `release_test.go` + `release_steps_test.go` using a `sync.Once` cloned release fixture.
    - Replace the ~550 lines of English prose substring checks in `internal/setup/setup_test.go:195-650` and `cmd/obor/plugin_wiring_test.go:216-299` with a structural template check (no unexpanded `{{...}}` placeholders, valid YAML frontmatter, required headings).

### Wave 4: Structural Cleanup in `internal/app`
11. **Task 4A — Bound In-Memory Event Log & Task Maps (`internal/app/log.go`, `project.go`)**:
    - Evict full `Record.Data` payloads from `Log.intents` and `Log.results` once an attempt closes (`stMerged`, `stDropped`, `stRejected`), keeping only open intents in memory.
12. **Task 4B — Unify Lane State & Move `syncLanes` Off the Critical `a.mu` Lock**:
    - Eliminate the redundant SQLite read-back in `cmd/obor/serve.go:265` and perform SQLite `WriteLanes` outside `a.mu` (or via `INSERT OR REPLACE` on the single changed lane).
