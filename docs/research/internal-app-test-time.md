# internal/app: where the test time goes

A measurement of `internal/app`'s suite (`go test -tags integration ./internal/app`, the run `tests/budget.json` caps
at 34.2 s) for plan `docs/plan/internal-app.md` tasks 13–15. No code changed. It answers how the time splits
between store open, rig start, waits and the test bodies, and what a faster store (one store per package, or an
in-memory one) or the rig work in tasks 14–15 would save.

## The answer in short

A run takes **17.45 s** (16.99–18.15 s over three runs). It has three phases:

| Phase | Wall | What happens |
|---|---|---|
| Binary start and the 21 other serial tests | 0.7 s | — |
| `TestWireRetryDeadlines`, run serially | **5.8 s (33 %)** | 8 parallel subtests wait in real time for the wire client's production deadlines (4 s, 3 s, 1.5 s, 1 s, …). They use 0.01 s of CPU, and no other test runs meanwhile. |
| The ~810 parallel tests | 10.9 s | Both slots are busy throughout. The work is mostly CPU, 85 % of it in file syscalls. |

The parallel phase is CPU-bound, so its wall time is about CPU ÷ 2. The CPU (17.3 s) goes to:

| Where the CPU goes | CPU | ≈ Wall |
|---|---|---|
| Re-reading `.obor/config.json` (and `local.json`) on every serve tick and merge pass | 4.76 s (27.5 %) | 2.4 s |
| Store: open, schema and close | 2.92 s (16.9 %) | 1.5 s |
| Globbing the **real** `~/.claude/projects/*/<session>.jsonl` for the rig's integrator pane | 2.83 s (16.4 %) | 1.4 s |
| Store: reads and writes (sqlite) | 2.33 s (13.5 %) | 1.2 s |
| Temp dirs, fixtures, cleanup `RemoveAll` | 1.35 s (7.8 %) | 0.7 s |
| Runtime (scheduler, synctest), GC, other syscalls | 2.90 s | 1.5 s |
| App and test logic outside these | ≈ 0.2 s | 0.1 s |

Git and exec cost nothing: the CPU profile has no sample in a process start. Fake-clock waits cost no wall time
directly. A bubble's clock jumps whenever every goroutine is blocked, but each jump runs the serve ticks that fall
inside it. The rigs tick every 2 ms, so a wait of T fake seconds costs T ÷ 2 ms ticks, and each tick reloads the
config (the first row above).

**What decides the store question.** An in-memory store for bubble tests would save about **2.1 s (12 %)** of a
run. These levers cost no store semantics:

- the wire deadline tests: −5.5 s;
- the per-tick config reload: up to −2.4 s;
- the real-home glob: −1.4 s;
- the real-time negative waits: −1.2 s.

The store is the third-largest lever, behind the first two. The glob and the negative waits together (2.6 s) save
more than it. It is the only lever that changes what a unit test proves about the store. Details are in section 6.

## 1. Setup and runs

- Head `bd6340a2`, go1.27.4 darwin/arm64, Apple M4 with 10 cores. The lane's env sets `GOMAXPROCS=2`, `GOFLAGS=-p=2`,
  so `-parallel` is 2, the same 2 cores the budget measures at. Load average was 4.5–5.1 while the other lanes
  worked.
- Three runs, one after another: `go test -tags integration -count=1 -json ./internal/app`. The package
  `Elapsed` is what `tests/budget` reads.

| Run | Package Elapsed | Result |
|---|---|---|
| 1 | 18.15 s | FAIL (`TestResumeWorkerSubmitsEndsTheNeedsYou`, see section 8) |
| 2 | 17.21 s | PASS |
| 3 | 16.99 s | FAIL (same test) |
| mean | 17.45 s, spread 1.16 s (±3.3 %) | |

- Profiles came from the test binary (`go test -c -tags integration`), run from the package dir:
  - with `-test.cpuprofile`: 17.52 s wall, 10.01 s user + 8.18 s sys, PASS;
  - with `-test.blockprofile -test.blockprofilerate=1`: 17.01 s, PASS.

  The process uses about one core of the two it has: 18.2 CPU-s over 17.5 s.
- Per-op costs come from a scratch benchmark module outside the repo, importing the lane's `store`, `event`,
  `config` and `testkit` (section 5).
- The top tests were each run alone with a CPU profile (section 7).
- The 34.2 s cap is about twice what this machine measures with the lanes busy. The cap is not changed here (no
  code change).

## 2. The shape of a run

Go runs the top-level serial tests first, in source order, and releases the parallel ones only after the last
serial test. `TestWireRetryDeadlines` (`wire_integration_test.go`) is serial: it calls no `t.Parallel()`. Its
subtests are parallel, so they hold both slots and every parallel test waits for them. The timestamps of the three
runs:

| | Run 1 | Run 2 | Run 3 |
|---|---|---|---|
| Start → `TestWireRetryDeadlines` | 0.76 s | 0.70 s | 0.70 s |
| `TestWireRetryDeadlines` | 5.79 s | 5.78 s | 5.78 s |
| Parallel phase | 11.61 s | 10.72 s | 10.51 s |

Slot time counts a test's own `Elapsed` plus its parallel subtests'. Go leaves out the time a test waits for a
slot. Slot time per run is 33.7 / 31.4 / 30.8 s:

- the wire subtests: 11.1 s;
- the parallel phase: 2 slots × 10.9 s ≈ 21.8 s, made of ≈ 17 s CPU plus ≈ 2.4 s of real-time waits (section 4)
  plus scheduling.

## 3. CPU attribution

The CPU profile has 17.28 s of samples. 85.7 % of them are in `syscall.rawsyscalln`, mostly file syscalls: `open`
5.6 s, `fcntl` (sqlite locks) 1.7 s, `stat` 1.1 s, `readdir` 0.8 s, `unlink` 0.7 s, `fsync` 0.7 s, `mkdir` 0.45 s.
Every sample was put in one class by its stack: the first class in this list that matches a frame wins.

| What | CPU | Main callers |
|---|---|---|
| Config re-read (`config.Load`) | 4.76 s | `reconcile` → `cfgNow`, once per serve tick: 3.23 s. `loopRig`'s own `Loop` factory, which calls `config.Load` per merge pass (test code): 0.99 s. `servedCfg` (status reads): 0.24 s. `App.load`: 0.15 s. |
| Store open, schema, close | 2.92 s | `App.load` → `openLog` → `store.OpenRW` (mkdir, `initSchema`, WAL) when serve starts: 1.36 s. `event.Log.Close` and `DB.Close`: about 0.5 s. testkit seeding (`Dispatched`, `event`, `setRow`), which opens per write: 0.6 s. Rigs reading the store on a second connection (`OpenRO`, `OpenAt`): 0.4 s. |
| Transcript scans (`agentFiles.Activity` → `Transcript`) | 2.83 s | `filepath.Glob` 2.67 s, from merge passes (`sampleJob`, `doneRoles`, `stopAgent`) and test polls of `Status()`. |
| Store reads and writes | 2.33 s | appends (`event.insert`, fsync per commit at `synchronous=FULL`), `ReadSpec`, the lanes table, `queryRecords`. |
| Temp dirs and fixtures | 1.35 s | `t.TempDir` and its cleanup `RemoveAll` (0.8 s), `testkit.NewDir`, `Config`, `Lane`. |
| Other syscalls | 1.11 s | `os.Getwd` in the rig's dispatch `loop` adapter (0.5 s), `Stat`, `MkdirAll`. |
| GC | 0.55 s | |
| Runtime, libraries | 1.24 s | scheduler wakeups, synctest's bubble bookkeeping, JSON. |
| App code alone | 0.17 s | the folds and rules themselves |
| Test code alone | 0.02 s | the fakes |

The same samples by the goroutine that drives the work (the nearest driver frame):

| Driver | CPU |
|---|---|
| Merge-loop passes (`mergeLoop` → `advance`, `rulePass`) | 3.48 s |
| Serve ticks (`run` → `loadProject`, `check`) | 3.26 s |
| Test bodies | 2.53 s |
| Rig and harness construction (including `launch`, `start`) | 1.96 s |
| Runtime with no driver above it | 1.79 s |
| Test polls (`poll`, `waitFor`, `waitStep`, …) | 1.55 s |
| Test cleanup | 1.05 s |
| Serve's other goroutines, including the load | 1.05 s |
| Request handlers | 0.42 s |

## 4. Waits

**Real time outside a bubble.** The block profile is dominated by tests waiting for a parallel slot (3.35 h of
goroutine-wait, which says nothing here). Only the test goroutine's own blocking was counted: 12.63 s outside
bubbles and 2.0 s inside them.

- `wire.verdictAsk` in the `TestWireRetryDeadlines` subtests: 10.84 s. The subtests:

  | Subtest | Wait |
  |---|---|
  | `StopFailureLandsNearItsDeadline` | 4.0 s |
  | `VerdictLandsAfterRestartOnce` | 3.0 s |
  | `AskSurvivesServeRestart` | 1.5 s |
  | `AskVerdictWaitsForServe` (2 rows) | 1.0 s |
  | `AskRetriesLostReplyWithSameCallID` | 0.5 s |
  | `LostReplyAnsweredFromStore` | 0.5 s |
  | `AskRetriesStopAtCallerDeadline` | 0.3 s |
  | `TurnFailsAtItsDeadline` | 0.25 s |

  They assert the production constants in `wire/client.go` (`stopFailureDeadline` 5 s, `askRetryWait` 500 ms, …)
  against real unix sockets.
- Fixed negative waits of 200 ms ("it does not answer within 200 ms") outside bubbles: about 1.7 s.
  - `waiting()` in `rig_conc_test.go`: `TestConfigSetWaitsForPublish`, `TestConfigSetSameCallWhileInFlight`,
    `TestReviewCloseSameCallWhileInFlight`.
  - `time.After(200 ms)` in `jobstart_test.go`: `TestIntegratorStartsOneAtATime` 0.6 s,
    `TestDeliveryKeepsTheStartOwned` 0.2 s, and in `TestEventFeed`.
- Sleeps, which the block profile does not record:
  - `TestCallToARunningServeWaitsForIt`: 300 ms, by design;
  - `TestRigStoreLockDuringAServeWrite`: 200 ms, outside its bubble;
  - `TestAskDoesNotRetryTimeout`: a 200 ms context.

  About 0.7 s.

In the parallel phase these hold a slot for about 2.4 s, about 1.2 s of wall.

**Fake time in a bubble.** A wait in a bubble costs no real time, but the serve's tickers run during it: loopRig's
`Tick` and `Follow` are 2 ms. Every tick runs `check` → `reconcile` → `config.Load` (about 128 µs alone, more under
contention). Merge passes add `config.Load` (the rig's `Loop` factory), the transcript glob and store reads. The
config's 4.76 s of CPU is about 35,000 loads, an estimate from the benchmark cost and not a count, over about
1,800 test rows. The drive tests show it most clearly: `TestDriveModelHonoured` spends 0.24 s of its 0.38 s CPU in
`config.Load` from `reconcile`, ticking while the drive waits. So the lever for fake-time waits is the work per
tick, not the clock.

**Polls.** Test polls (`poll`, `waitFor`) cost 1.55 s of CPU. They are mostly the `Status()` they call, which runs
the transcript glob (0.95 s) and store reads (0.39 s), not the polling itself.

## 5. Per-op costs (benchmark, 2 procs, this machine)

| Op | Cost |
|---|---|
| New store: `OpenRW` (mkdir, schema, WAL) + `event.OpenLog` + close | 1.11 ms |
| Reopen an existing store (`OpenRW` + close) | 0.35 ms |
| Read-only open (`OpenAt` + one query + close) | 0.22 ms |
| In-memory store, same schema, `OpenLog` + close | 0.17 ms |
| `Log.Append`, file store (WAL, `synchronous=FULL`) | 38 µs |
| `Log.Append`, in-memory | 7 µs |
| `config.Load` of loopRig's config | 128 µs |
| Glob `~/.claude/projects/*/int-1.jsonl`, real home (254 entries) | 5.8 ms |
| The same glob, empty home | 1.6 µs |
| A test project dir: `NewDir` + `Config` + `RemoveAll` | 0.52 ms |

Store open and close cost 2.92 s of CPU, about 2,600 opens counted at the new-store price. That is about 1.4 per
test row: a rig's serve load plus the testkit seeding and second connections.

## 6. The numbers that decide

The wall savings below are for this run. The parallel phase counts at CPU ÷ 2. The categories don't overlap, so
the savings add up.

| Lever | Saves (wall) | What a test stops proving |
|---|---|---|
| **A.** The wire deadline subtests: inject the deadlines and retry wait (ms in tests) or run them on a fake clock. | ≈ 5.5 s | The production constants in real time. One real-time row can stay in `internal/app/wire`, where the client lives. |
| A, alternative: only make `TestWireRetryDeadlines` parallel. | ≈ 1.2 s | Nothing, but the subtests still hold 11 s of slot time. |
| A, alternative: move the subtests to `internal/app/wire`'s tests. | 5.8 s off `internal/app`'s budget | Nothing; the suite's total is unchanged. |
| **B.** Per-tick config reload: a longer rig tick inside bubbles (bubble time is free, ticks are not), the rig's `Loop` factory reusing the config, or serve reloading only when the file changes (a production change). | up to ≈ 2.4 s | Nothing about the store. A longer tick changes how many passes a test sees per fake second, so tests that count passes or ticks need a look. |
| **C.** Real-home glob: set `Options.Home` to the test's dir in the rigs (test-only). | ≈ 1.4 s here (it scales with the size of the user's `~/.claude/projects`) | Nothing. It also stops the tests reading the user's real home (section 8). |
| **D.** In-memory store for bubble tests. | ≈ 2.1 s: open −85 % (1.11 → 0.17 ms), append −81 % (38 → 7 µs), on 5.25 s of store CPU | See below. |
| D, alternative: one store opened once per package. | ≤ 1.5 s (the opens only) | Parallel tests would share one database: one writer connection, one event log, `seq`, lanes and specs. The store has no per-test isolation, so this needs code under test to change. Not viable. |
| **E.** Real-time negative waits into bubbles (`waiting()`, the `jobstart_test.go` selects, the 200 ms sleeps). | ≈ 1.2 s | Nothing; the bubble's clock proves the same "not within 200 ms". |
| **F.** Temp dirs and fixtures. | ≤ 0.7 s | — (already small per test: 0.52 ms) |

**What an in-memory store stops proving in a unit test** (it stays proved by `internal/store`'s tests and the
`_integration_test.go` files):

- WAL behaviour: a reader on its own connection never waits on serve's writer. Covered by
  `TestRigStoreLockDuringAServeWrite` and the rigs' `readStore`, `OpenRO`, `OpenAt` reads with their
  `busy_timeout`.
- `synchronous=FULL`: an effect is on disk before its result is recorded. A memory store has no disk, so crash
  tests become replay-from-memory tests.
- Replay from the file when serve stops and starts again on the same root: about 90 `r.stop()` calls, most
  followed by `r.start()`. A memory database must outlive its connection for this (a named shared cache kept open
  by a holder), so a restart still replays but no longer reads a file.
- `OpenRW`'s schema-version check on reopen, the path `.obor/state/obor.db`, and the testkit seeding writes that
  open the file themselves.

**Combined.** A, B, C and E together (5.5 + 2.4 + 1.4 + 1.2 s) would bring the run from about 17.5 s to about 7 s
with the file store. Adding D would take it to about 4.9 s. So the store question is about 2.1 s of a 7 s suite,
about 30 % of what would remain, decided after the levers that cost no proof.

**Rigs (plan tasks 14–15).**

- Rig construction and the serve's load cost about 2.9 s of CPU, about 1.5 s of wall. That is roughly 2.5 ms of CPU
  per rig, about half of it the store open.
- A rig-level test row costs a mean 15 ms of slot time, about 7.5 ms of wall: 742 rows between 5 and 50 ms, 11.2 s
  in all. A pure function-table row stays under 5 ms; 972 rows round to 0.00 s.
- So task 15 saves about 7.5 ms of wall per duplicate rig row it deletes, about 0.75 s per 100 rows.
- Task 14 (one fake per port) saves little directly: the fakes themselves use 0.02 s of CPU. A merged rig is the
  natural place to land B and C, though, and both reach every rig test at once.

## 7. Top 20 tests by slot time

Mean of the three runs. Slot time is the test's `Elapsed` plus its parallel subtests'. "Alone" is the test run
alone from the binary: process wall time (with about 0.1 s of start) and CPU.

| # | Test | Slot s (runs) | Kind | Alone: wall / CPU | Spends it on |
|---|---|---|---|---|---|
| 1 | `TestWireRetryDeadlines` | 11.08 (11.09 11.08 11.08) | serial; 8 parallel subtests, real sockets | 5.82 / 0.01 | real-time waits for the wire deadlines (section 4); 5.8 s with nothing else running |
| 2 | `TestLoopRecovery` | 1.16 (1.20 1.13 1.16) | bubble | 0.73 / 1.04 | glob 0.34, config 0.21, sqlite 0.15 |
| 3 | `TestLoopFailureRule` | 0.70 (0.95 0.56 0.60) | bubble | 0.42 / 0.53 | glob 0.22, config 0.12, sqlite 0.07 |
| 4 | `TestIntegratorStartsOneAtATime` | 0.64 | harness, no bubble | 0.51 / 0.03 | three real 200 ms negative waits |
| 5 | `TestDeliveryKeepsTheStartOwned` | 0.41 | harness, no bubble | 0.51 / 0.02 | real 200 ms sleep + 200 ms negative wait |
| 6 | `TestDriveModelHonoured` | 0.41 | bubble | 0.51 / 0.38 | per-tick config reload while the drive waits (0.24) |
| 7 | `TestRV37V2ResumeRetryKeepsTheJob` | 0.38 | bubble | 0.31 / 0.36 | glob 0.19, sqlite 0.07 |
| 8 | `TestKeepLaneReusesItsAgentOnlyForTheSameModel` | 0.32 | serial top level, bubble | 0.31 / 0.30 | config 0.09, glob 0.09 |
| 9 | `TestCallToARunningServeWaitsForIt` | 0.30 | real socket | 0.31 / 0.00 | a 300 ms sleep it asserts on (by design) |
| 10 | `TestJobNudgeFromDelivery` | 0.28 | 3 bubbles | 0.30 / 0.27 | glob 0.19 |
| 11 | `TestNeedsYouBlockGoldenFolds` | 0.27 | serial, no bubble | 0.41 / 0.23 | sqlite 0.07, store open 0.06 |
| 12 | `TestDropSelectedStep` | 0.25 | harness, no bubble | 0.31 / 0.23 | config 0.07, store open 0.04, sqlite 0.04 |
| 13 | `TestRV37VerifyStopFailure` | 0.24 | bubble | 0.31 / 0.13 | glob, runtime, GC |
| 14 | `TestAnswerPathEveryCause` | 0.22 | harness per subtest | 0.31 / 0.20 | store open 0.05, config 0.04, temp dirs 0.03 |
| 15 | `TestConfigSetWaitsForPublish` | 0.21 | admit rig, no bubble | 0.31 / 0.00 | `waiting()`: real 200 ms |
| 16 | `TestReviewCloseSameCallWhileInFlight` | 0.21 | review rig, no bubble | 0.31 / 0.00 | `waiting()`: real 200 ms |
| 17 | `TestEventFeed` | 0.21 | integration (real socket) | 0.42 / 0.11 | real 200 ms negative wait |
| 18 | `TestRigStoreLockDuringAServeWrite` | 0.21 | bubble + real goroutine | 0.41 / 0.01 | a real 200 ms sleep outside the bubble (by design) |
| 19 | `TestTellLeadRefusedWhileTheDriveIsDispatched` | 0.21 | bubble | 0.30 / 0.18 | config 0.06, timer churn 0.10 |
| 20 | `TestConfigSetSameCallWhileInFlight` | 0.21 | admit rig, no bubble | 0.31 / 0.00 | `waiting()`: real 200 ms |

The top 20 are 56 % of slot time; without row 1 they are 21 %. Beyond them the time is spread thin: 1,811 leaf
rows, 97 of them at 50 ms or more (9.2 s), 742 between 5 and 50 ms (11.2 s), and 972 under 5 ms.

## 8. Side findings

- **The rigs read the user's real home.** `agentFiles.Transcript` falls back to `os.UserHomeDir()` when
  `Options.Home` is empty (`activity.go`), and no rig sets `Home`. loopRig's integrator pane has `Session: "int-1"`
  and no transcript path, so every activity check globs `~/.claude/projects/*/int-1.jsonl` in the real home. That
  costs 16 % of the suite's CPU on this machine. It also means a test's outcome could depend on the files in the
  user's home.
- **A flaky test.** `TestResumeWorkerSubmitsEndsTheNeedsYou` (`worktext_ext_test.go:91`) failed in 2 of the 5 full
  runs here (runs 1 and 3; the two profiled runs passed): `needs-you in force after the submission:
  [needs-you{submit: FAIL task/T-1 is already in the target}]`. Not investigated (no code change in this task).
- macOS file syscalls are where the CPU goes: sys is 8.2 s of 18.2 s CPU. On another OS the per-op costs above
  differ; the shape (per-tick reloads, the glob, store opens) does not.

## 9. Method, to repeat it

The profiles, raw dumps and tools are in the lane's scratchpad, not the repo. To repeat:

1. Run `go test -tags integration -count=1 -json ./internal/app > runN.json` three times.
2. Build the binary with `go test -c -tags integration -o <tmp>/app.test ./internal/app`. Then, from
   `internal/app`, run `<tmp>/app.test -test.count=1 -test.cpuprofile=…` and, separately,
   `-test.blockprofile=… -test.blockprofilerate=1`.
3. Slot time per test: sum `Elapsed` over the test and its paused (parallel) subtests. A non-parallel subtest is
   already inside its parent's `Elapsed`.
4. CPU classes: dump the samples with `go tool pprof -raw`, then classify each stack. "What" takes the first class
   in the section 3 order that matches any frame; "who" takes the nearest driver frame, and runtime only when no
   driver frame is above it.
5. Block time: keep only test goroutines (`testing.tRunner` at the root). Drop the slot and subtest waits
   (`T.Parallel`, `tRunner.func1`, `T.Run`), then split by whether a synctest frame is on the stack.
6. Benchmarks: a scratch module `obor/zbench` with `replace obor => <lane>`, `-bench . -benchtime=1s`.

Not measured:

- an exact count of store opens and config loads; they are estimated from CPU ÷ per-op cost;
- a Linux run;
- the nightly's load;
- a trace. Per-goroutine syscall wait time comes from the CPU profile, which on darwin counts kernel CPU, not time
  blocked on disk.
