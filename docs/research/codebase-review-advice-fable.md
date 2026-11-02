# Advice on the Gemini codebase review (performance, tests, architecture)

Advisor: ob-fable (Claude Fable), 2026-11-02, at main 79cbc474. Judged: `docs/research/codebase-performance-and-test-review.md`
(Gemini, 2026-11-02). Rules applied: AGENTS.md (simple, one owner per helper, no line-count targets), memory rules
"LOC not a goal", "ignore unlikely edge cases", "structure debt counts", "delete with replacement", and the T-604
lesson (a Gemini saving that was not real once measured). Every cited line was read; one timed run of the full suite
was made (`go test -count=1 -json ./...`, the gate's `-p` is 10 here: 10 cores / `slots.max` 1).

## Recommendation first

The review's headline numbers (94,963 lines, 335 files, 1.53x test/prod, 74 and 39 test files, 7 rigs, 11,815 lines
in `internal/app`) are right. Its diagnosis of where the time goes is wrong, and its cure (fixture caching, sleep
trimming, a 24k-line test rewrite) would not move the gate. Measured:

| Package | Wall (s) | Note |
|---|---:|---|
| whole suite | 158 | = the longest package |
| `internal/app` | 156 | **the critical path**; 428 tests, 39 `t.Parallel()` calls: it runs serially |
| `cmd/obor` | 107 | one test is 75 s (`TestResumeAndNoVerdictVerbs`: 5 calls to a missing serve) |
| `internal/checks` | 80 | `TestNightlyBackup` 31 s (2 calls to a missing serve); the cut table 25 s in parallel |
| `internal/setup` / `launch` / `config` | 28 / 26 / 25 | not on the path |
| `internal/guard` | 13 | not on the path |

Inside `internal/app` (sum of test times 145 s, serial): five tests take 64 s. `TestBackup` 30 s and
`TestVerdictServeNotRunning` 15 s are calls to a socket that is not there, each waiting out `verdictDeadline` (15 s,
`internal/app/status.go:545`, T-659's retry loop). The other 375 tests have a median of 0.02 s and sum to ~35 s;
122 rig tests of 0.1 to 0.5 s sum to 32 s.

So the two levers are: (1) a short deadline where a test deliberately talks to a missing serve (about 240 s of test
time across packages, 45 s of it on the critical path); (2) `t.Parallel()` in `internal/app`, whose rigs are already
per-test (own temp dirs, own fake herdr socket, own clock). Together they should take the gate from ~150 s to ~60 s.
Nothing Gemini lists in §2 is on the critical path today.

Of the risky proposals: reject the guard emulator cut, reject the rate-limit "duplication" (the premise is false),
reject caching the lanes read, measure before a `Landed` cache, reject the LOC-driven test consolidation. Details
below, then the ranked list.

## Claim by claim

Format: claim → (1) true in the code? (2) does it matter? (3) verdict.

### §2.1 Fixture cost (`internal/testkit`)
1. Git counts are right: `New` 3 (`testkit.go:88-98`, not 71-106), `Lane` 2 (`146-159`), `Config` 3 (`164-203`);
   `Dispatched`/`SubmitAgo` open the store 4 to 5 times (`eventAt`, `rows`, `setRow`). Call counts are inflated:
   `setup(t)` is called 86 times in `launch_test.go` (not "220+"; 125 in the package), `fixture(t)` 37 times in
   `internal/guard` (not once per test: 93 tests), `newLoopRig` 56 times (not "250+").
2. launch 26 s, guard 13 s, both parallel, neither on the critical path. A `testkit.New` is ~30 ms; the app
   package's rig tests would gain maybe 10 to 20 s of serial time, which `t.Parallel()` gains ten times over.
3. Reject 1A (template-copy repos, a shared writer connection with `synchronous=OFF`: new test infrastructure
   with its own failure modes). Reject 1B (two fixture kinds in launch/guard: no wall-time gain).

### §2.2 `TestScratchCutTable` (`scratch_test.go:391-432`)
1. True in shape: 8 steps, for each N in 1..calls a fresh repo (`newSx`), the step armed to fail at git call N, run
   again. ~85 repos is a fair estimate.
2. Measured: 25 s wall (steps in parallel: publish 24.9 s, gate moved 23.7 s …) inside a package of 80 s whose
   biggest item is the 31-s backup test. It is the crash-safety oracle of the scratch (every git call may fail; the
   rerun must decide the same). Cutting only "mutating verbs" removes exactly the cases where a read fails mid-step
   (a `rev-parse` on a ref another process deleted): "ignore unlikely edge cases" applies to adding code, not to
   removing a test that already exists and costs nothing on the path.
3. Reject for now. Revisit only if, after the deadline fix, `internal/checks` becomes the longest package; then the
   cheaper cut is to copy the post-setup fixture per N (Gemini's second idea), not to drop cases.

### §2.3 "~30 s of hardcoded sleeps"
1. Each site exists, but the table sums serially what runs in parallel: the hanging-binary tests
   (`plugin_wiring_test.go:415-517`, `shim_test.go:369`) are all `t.Parallel()`, so the 2-s watchdog costs ~2 s of
   wall, not 12; the nightly second-boundary sleeps (`nightly_test.go:267-274`) and window waits are parallel
   subtests; `run_test.go` is a 5-s package; `OBOR_RESTART_WAIT=1` is 2 tests; `shortWait` has 32 uses in a 26-s
   package. The table misses the real sleepers: the 15-s `verdictDeadline` on a missing socket (above).
2. None of the listed sites is on the critical path.
3. Reject 1C as written (a `bin/obor` env knob for the watchdog, a duration form of `OBOR_RESTART_WAIT`, explicit
   timestamps in the nightly test): correct but worthless until `cmd/obor` is the longest package. The one item
   worth keeping from 1C is the idea of an env knob, applied to the deadline instead (task 1 below).

### §2.4 CLI subprocess tests and `make release` runs
1. `cmd/obor` runs the built binary 135 times via `run(t, …)` (not "250+"); the release tests call `make` 5 times
   (not "55+"); `TestMain` builds one binary.
2. The package is 107 s only because of one 75-s test; the subprocess runs are cheap and parallel.
3. Reject "run flag tests in-process": the CLI tests are the only tests of the binary's argv surface.

### §2.5 Fragmentation, prose tests, duplicate rigs
1. 74 / 39 test files: true. `TestStop*` in `launch_test.go` = 7 (not 14), `TestStart*` 7 (not 12), `TestRestart*`
   15 (not 10). The prose tests named (`TestSyncTemplates`, `TestAgentsTemplates`, `TestSkillsTemplates`,
   `TestRuleTemplates`, `TestSkillPrompts`) do not exist; `setup_test.go:195-650` holds the init and lane tests;
   `setup_test.go` has 17 `strings.Contains` in total. 7 rig builders in `internal/app`: true (12 / 55 / 31 / 25 /
   18 / 46 / 33 uses). "4 fake serve sockets": `serveStub` and `serveSocket` do not exist; `startFakeServe` plus a raw
   `net.Listen` in four test files is true in spirit.
2. Ticket-named tests and seven rigs are a navigation cost (structure-debt-counts), not a time cost.
3. See Wave 3 below.

### §3.1 Herdr events → git "storm"
1. True in structure: `App.event` (`panes.go:94-135`) → `check(ctx,false)` (`watchdog.go:18-39`) → `decideLanes` →
   `laneTable.Lanes` (`serve.go:255-271`) → `LaneTable.States()` (`lanetable.go:44-59`): 3 git per lane plus one
   `log --grep` when `MergeEvidence` is consulted, lanes in parallel goroutines. Then `wake()` → `advance`
   (`loop.go:295-331`) → `drive` (`534-563`) → `Scratch.Landed` (`scratch.go:467-479`): 2 git per task whose state has
   `landed` (`states.go`), which is most open states. For 8 lanes and T landed tasks: ~24 + 2T git processes per herdr
   status event, plus per 30-s tick.
2. Not measured by Gemini and not by me: herdr status events arrive a few times a minute; 30 short git processes in
   parallel is tens of ms of CPU. The owner has not reported serve CPU as a problem.
3. **Reject the cached-lanes half** (reuse `p.lanes` on non-full checks): the lanes read is how serve sees git truth
   that no event announces (a worker's commit, a branch moved by hand, a lane dir removed), and `decideLanes` feeds
   the stall/done watchdogs from it; a 30-s staleness window there is a behaviour change in a crash/replay machine,
   for an unmeasured gain. **Measure before the `Landed` cache**: the cache keyed by `refs/heads/<main>` SHA is sound
   as live state (reset at restart, never folded) because merge evidence on main changes only when main moves, but
   it is still a second source of truth in `drive`. Oracle: serve's CPU time over one working day
   (`ps -o cputime -p $(pgrep -f 'obor serve')` morning and evening) and the event count from its log; do the
   `Landed` cache only if serve's CPU is minutes per day.

### §3.2 `obor web` and `obor monitor`
1. `jobAPI` (`web.go:159-193`) builds the whole view (`s.build` → `view.Load` → lane table, 24 git, serve ask, store)
   and `Detail.Job` (`detail.go:244-258`) uses only `d.Root`. True. `/api/projects` builds every project in sequence:
   true. `obor monitor`: a frame is built on a new record **or** every `monitorEvery` (2 s, `monitor.go:32,117`) and a
   frame runs `checks.NewLaneTable` (24 git) plus `serveProcess` (`launchctl print`): true.
2. The job page is one click; the monitor is a process the owner leaves open, so its idle cost is real and
   measurable: `ps -o %cpu,cputime -p <monitor pid>` after 10 idle minutes.
3. **Do** the `jobAPI` fix (build no view: `view.Detail{Root: p.Root}.Job(id)`; three lines, no behaviour change).
   **Do after measuring** the monitor: if idle CPU is above ~2 %, keep the lane table and launchd answer of the last
   frame and rebuild them only when `seq` changed or every ~10 s; the 2-s redraw keeps elapsed times fresh. Reject
   the web view cache with TTL and the parallel `/api/projects` (a localhost read-only page; no measured latency).

### §3.3 `store.Owned` O(N³)
1. True in form (`sessions.go:31-81`): `Owned` → `Admits` → `Holder` → `Current`.
2. The real store has 261 session facts and 28 agents: ~2 million comparisons, under 10 ms, run by CLIs and hooks
   via `ReadSessionsAt` and once at replay. Not a cost.
3. Reject. The single-pass version is more code to reason about for a function that is correct and cheap.

### §3.4 Indexes, `syncLanes`, `Fact()`, config reloads, herdr dials
1. Indexes: the schema has none (`store.go:146-158`), true; the real store has 13,063 events and 53 prompts, so a
   full scan is milliseconds. `syncLanes` "unconditional": **false**, `lanerows.go:106` compares the rows with the
   last written table and writes only on a change (the write is under `a.mu`, true, but only then). `Fact()` is
   called at 34 sites, each a small unmarshal: microseconds per record. `config.LoadDaemon` is re-read in
   `roleAgent` (`rolestop.go:282`, per labelled shell pane per status) and `namedAgents` (`registry.go:238`, per
   reconcile): true, ~0.1 ms each, and live config is a feature (edits apply without a restart). Herdr dials:
   `reread` does two (`socket.go:1002-1016`), true; `Activity`/`Turn` run only for the agents whose idle timers are
   due (`watchdog.go:63-74`, `plan`), not on every event: overstated.
2. None measurable.
3. Reject all of 2B except nothing: indexes when a store passes ~1M rows, an mtime config cache never (complexity
   for 0.1 ms).

### §4.1 `internal/app` as a god package, mutexes, retained log
1. 11,815 lines, 39 files, 40-field `mtask`: true. Mutexes: 5 on `App` + `Log.mu` + `outbox.claim` +
   `reviewClaim.mu` = 8. "a.mu held across the log's fsync": **false**, `recordSeq` (`loop.go:340-360`) appends
   outside `a.mu` and folds under it; only the lanes-table write is under `a.mu`, and only when rows change. The
   `Log` keeps every intent and result record in maps: true (4,848 each in the real store, a few MB).
2. The owner already decided this (decisions.md 2026-10-31, structure-debt-counts): split `proj` in
   behaviour-preserving steps with the existing tests as the oracle. Gemini adds no new argument.
3. 4A (evict closed attempts' payloads): reject for now; eviction logic in a replayed log is complexity for a few
   MB. Note the growth once per quarter. 4B (lanes write off `a.mu`, single-row upsert): reject; the write is rare
   and ordering under `mu` is what makes "row written before the step goes on" (`laneRowsErr`) true.

### §4.2 "Quadruple duplication of lane state"
1. The four places are four different things: the folded projection (`lr.rows`), its persisted copy for hooks and
   CLIs (the `lanes` table), the merge-loop state (`folded.loop`), and the git read (`p.lanes`). One true oddity:
   serve's `laneTable.Lanes` reads back its own `lanes` table through `checks.NewLaneTable` → `Rows()` →
   `ReadLanesAt` (`serve.go:257-271`).
2. One read-only SQLite open per check; not a cost. The structure point is the proj split, already decided.
3. Do in passing when the proj split touches `laneTable`: hand `p.folded.lr.rows` to the `LaneTable` (a `Rows`
   field) instead of reading the table back. Not its own task.

### §4.3 Duplicated code
| Item | True? | Verdict |
|---|---|---|
| `stepN` (`loop.go:259-266`) vs `baseStep` (`view/events.go:175-182`) | Yes, same split (`stepN` keeps N) | Do: one owner (export from `app`, `view` imports `app` already) |
| `dataString`/`dataNumber` (`detail.go:346-369`) vs `eventData` vs `FactData` | Partly: the view reads untyped maps where typed structs exist | Do when touching `view`: read `app.FactData` and the result structs; not its own task |
| status vs monitor rendering | Different layouts (lines vs a fitted frame), written a day ago | Reject now; revisit after the monitor settles |
| worktree discovery (`guard.go:226-246` vs `project.go:73-115` vs `lanetable.go:75`) | **No**: list worktrees, find a dir's repo, `rev-parse` a lane: three jobs | Reject |
| `OpenRO` + `os.Stat` boilerplate "8 times" | 3 sites, all in `lanes.go` (70, 92, 119); `openROAt` exists | Do: three one-line replacements |

### §4.4 Over-engineered subsystems

**Shell emulator (`internal/guard/shell.go`, 1,304 lines): reject.** The file's own header is an inventory of what
each feature catches and a list of known bypasses; its history (`git log pre-public`, 19 commits) shows every feature
came from a reviewed bypass or false positive: T-193 (AST walk), T-198/T-230 (wrapper option tables), T-199/T-200
(unresolved command words), T-212, T-220 (**already cut to an accident guard, −639 lines**), T-474/T-476/T-486
(headless runs via `env -S`, `claude -cp`, `codex e`), T-543/T-551 (load runs). The `cd` and variable emulation is
what resolves a worker's `cd ../lane && git …` and protected-file paths; `sh -c`/`eval`/`xargs` nesting is how agents
actually invoke things. "Walk CallExpr and redirects without emulation" reopens those cases and the guard tests (163
cases exercising them in `guard_test.go`) would say so on day one. The rule that fits is "delete with replacement":
there is no replacement. If the owner wants it smaller, the only honest route is evidence: log which rule denied for
a month and remove rules with zero hits, one review per removal.

**Rate-limit parser and wake engine (`stopped.go` 434 + `stopwake.go` 291): reject, false premise.** The English
parsing is one regex (`agents.go:249`, "resets 6pm", "try again at 6:30 PM") plus per-kind `Reset` readers (Claude
epoch in the hook's details, Codex `resets_at` in its rollout). The two files are the stop/wake state machine the
owner ordered on 2026-10-30 (T-568 workers, T-569 role agents: wake after the reset, alarm 30 min later), covering
Claude (StopFailure hook), Codex (no hook: transcript turns) and OpenCode. Claude Code's auto-continue is not
duplicated: `autoContinue` in `guard/prompt.go:136` is the text of Claude Code's own message, recognised so it is not
counted as a person's prompt; Claude Code continues only its own Claude session and tells nobody. Obor's engine is
what makes "stopped agents continue by themselves" true for the other kinds and what raises the alarm.

**`obor review` as a "second dispatcher" (`review.go`, 634 lines): reject.** A review is not a task: no lane, no
gate, no merge; modelling it as one would push it through the merge-loop state table it does not fit (T-638 chose
a fold of its own). Flag if it grows another claim or timer.

### Wave 3 (test consolidation, 188 → ~90 files, ~24k lines removed): reject as specified
"LOC not a goal" is explicit: a file or line target is exactly what pushed workers to compress instead of simplify
(T-233/T-234). The rewrite of 23k lines of `internal/app` tests is also the single riskiest item in the document: those
tests are the oracle the owner's proj split relies on; rewriting the oracle and the code in the same season leaves
nothing fixed. The navigation cost of ticket-named tests is real (structure-debt-counts), so: fold a `TestRVxx`/`TestTxxx`
into the table it belongs to when a task touches that table; after the proj split is done, one task may merge the
seven rigs into two (Gemini's `newUnitRig`/`newServeRig` is a reasonable shape) with the oracle "same tests pass,
no test deleted". The prose-template tests Gemini names do not exist; the 17 `Contains` checks that do are cheap and
catch real template regressions.

## The ranked list

1. **Short deadline for tests that talk to a missing serve (simple).** In-process tests pass a context with a
   200-ms deadline (`verdictAsk` honours the parent context: `TestBackup`, `TestVerdictServeNotRunning`); nightly's
   backup call uses the session's context instead of `context.Background()` (`nightly.go:102`, also right in
   production: an interrupted nightly should not wait 15 s on a backup); the CLI tests get an env knob read by
   `callDeadline` (`status.go:551`), seconds like `OBOR_RESTART_WAIT`, set in `TestResumeAndNoVerdictVerbs` and the
   five other `cmd/obor` tests, `mcp_test.go` and `session_test.go`. Oracle: `go test -json ./...`: no test over 10 s
   except the cut table and the hanging-binary ones; `internal/app` under ~115 s, `cmd/obor` under ~40 s,
   `internal/checks` under ~55 s; every "serve is not running" assertion still present.
2. **`t.Parallel()` across `internal/app` (medium, after 1).** Start with the `newHarness` tests (fake panes and
   lanes, no git, no socket), then the rig tests; rigs are per-test already. Oracle: the package three times in a row
   (one `go test ./internal/app` at a time, as the guard requires) and one full `make check`, all green; package wall
   under ~45 s; the whole gate ~60 s. Watch the known 60-ms waits in `internal/launch` (BACKLOG: `TestStopEveryKind`,
   `TestStopForceTerminates`) and the T-684 patience cap: more load makes those flake first; widen them in the same
   task if they do.
3. **`jobAPI` builds no view; three `openROAt` replacements in `lanes.go`; one owner for `stepN`/`baseStep` (simple,
   one task).** Oracle: `make check`; `/api/job` returns the same JSON as before (web tests); `obor status`
   unchanged.
4. **Measure `obor monitor` and `obor serve` before any caching.** Monitor: `%cpu` after 10 idle minutes; above ~2 %
   → keep the last frame's lane table and launchd line and rebuild them on a new record or every ~10 s. Serve:
   `cputime` morning vs evening over a working day; minutes per day → the `Landed` cache keyed by main's SHA as live
   state, nothing else. Oracle for either: the same tests green, and the number measured drops.
5. **The proj split the owner already approved (decisions.md 2026-10-31)**, deletions first, tests as the oracle,
   with §4.2's "serve hands its rows to the LaneTable" folded into the step that touches it. Not new; listed so the
   test-rewrite idea is not scheduled ahead of it.

Not scheduled: fixture caching (1A/1B), sleep trimming (1C), cut-table trimming (1D), indexes and the O(N) `Owned`
(2B), the web cache, the emulator cut, the stop/wake removal, the review-as-task change, Wave 3 as written.
