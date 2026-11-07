# Test audit: what the suite runs that obor does not own

Brief (owner 11-07): "there is just too many tests; see if we test stuff out of our control, like running outside
commands (kit, herdr) and testing their result." Read-only survey; no test was changed, nothing committed.

## Recommendation

**The suspicion is mostly wrong, and the real cost is somewhere else.** Almost no test runs a tool obor does not own
and asserts on its output. The suite's weight is in tests of *our own* code that use real `git` as a fixture and in
tests of *our own* scripts (`make release`, `bin/obor`). Only two tests call a third-party binary and judge its
answer (`claude plugin validate`, `node --test`), and both are small. herdr, codex, opencode and `gh` are never run
for real by a Go test (`herdrtest` fake, a fake `go` and `gh` in `tests/release`). So deleting "tests of other
people's tools" would remove about 6 s and 11 flake rows. The cuts that pay are these, ranked by test-seconds
removed per change (all seconds measured at load 20–30 on 10 cores; see Method for what they mean):

| # | Change | Test-s removed | Flake rows removed | Effort |
|---|---|---|---|---|
| 1 | **Promotion's full suite skips `tests/release` unless a release-class path changed since the last green promotion** (`scripts/release` already runs `make check-release` before every tag, so nothing ships unchecked) | **544** (28% of the suite's 1955) | 1 | small: `make check` package list or a class check |
| 2 | **Delete the frozen-release-binary tests in `tests/upgrade`** (`TestUpgradeV0230ToV0231`, `TestUpgradeBrokenBuild`) — `make upgrade-check` runs the same harness on the real previous release at each release | **74** | 0 | small |
| 3 | **Give the `internal/app` loop rigs an in-memory `Scratch` instead of real git** (66 tests use `newLoopRig` → `gitScratch` → real git) | up to **242** (88 in the `-short` lane tier); estimate, see §3.3 | 16 | medium; the one real-adapter-over-git suite stays in `internal/checks` |
| 4 | **Mark `cmd/obor` `TestServeLoop*` (6 tests) `testkit.Slow`** (real adapter + git, a third copy of scenarios `internal/checks` and `internal/app` already run) | 16.7 per lane run | **18** (the most of any family) | one line each |
| 5 | **`TestOpencodePlugin`: drop the `node --test` run from Go** (conformance `OC-S2` runs the same `node --check` + `node --test` nightly); run it when `opencode/**` changes | 5.1 per cmd/obor run | **9** | small |
| 6 | **`TestGithooks`: stop `go build`-ing obor inside the test**; use the stub binary `hx.binary` already provides, keep the one real-binary hook case in `cmd/obor` | 4.6 + a compile | **5** | small |
| 7 | **Make the shim's 2 s hang watchdog settable by env** so the 5 hang/stale-binary hook tests stop waiting it out | ~15 per run | up to **15** | small |
| 8 | `TestClaudePlugin` → conformance `CC-S3` (`claude plugin validate` is the tool's behaviour (b); it also silently skips on a machine without `claude`) | 0.7 | 2 | trivial |

Rows 4–8 together are 34–49 of the 248 flake rows (14–20%) for under 30 s of test time: the point of them is flakes,
not CPU. Rows 1–3 are the CPU: about 860 of 1955 test-seconds (44%). Row 3 is an estimate; rows 1, 2, 4, 5 are
measured.

Keep as is: the `cmd/obor` tests that run the built binary (75 tests, 91 s, 7 flakes: obor's own CLI, kind (a)),
`internal/checks` (the real adapter over real git is the one place git belongs), the `herdrtest` users, and the
`tests/release` tests themselves when a release-class path changes.

## Method

- **Timing**: one plain `go test -json <pkg>` per package, in sequence, no `-count`, no `-race`, no `-short`, on tree
  `4783a522` (clean `staging`), 21:00–21:12 on 11-07, load 20–30 throughout (`uptime`: 22.6/26.0/27.5 at the end).
  The 11-07 11:09 runs were not reused: ten hours and T-987 old. Files: `…/476cd480-…/scratchpad/runs/*.json`,
  `runs.txt`, joined in `joined.json`. A test's seconds are its `Elapsed` as the top-level test; parallel tests overlap,
  so per-package sums exceed the package's wall time (internal/app: 556 test-s in 151 s wall; tests/release: 544 in
  85 s). "Test-s" ranks; it is not CPU time. Total: 1955 test-s over 1,513 tests.
- **Tags**: a script split each `*_test.go` into functions and tagged each `Test*` by tokens in its own body
  (`exec.Command`, `testkit.New`/`.Git(`, `run(t,`, `"make"`, `herdrtest`, `time.Sleep`, …). Tags are direct only:
  a test that reaches git through a helper is found by the helper's name (§3.3), not by the tag. Source:
  `…/scratchpad/tag.py`, `tags.json`.
- **git process count**: a `git` wrapper first on PATH logged one line per invocation; one `go test -count=1` per
  package. Counts below are `git` processes started by the package's tests and by the code under test.
- **Flakes**: `.obor/state/flakes.jsonl`, 248 rows, 10-25 → 11-07 (134 `check_affected`, 113 `check`, 1 `compile`).
  A row is a test that failed in a gate run and passed or was retried; I did not verify the cause of any row. 74
  rows name a test that no longer exists (old `herd` names, or the package-level rows `obor/cmd/obor` ×23, `obor` ×7).
- **Slow**: 119 tests call `testkit.Slow` (603 of 1955 test-s); `-short` lanes skip them, the promotion and release
  run them.

## 1. Which external things the tests actually run

| External thing | Run for real by | Everywhere else |
|---|---|---|
| `git` | ~33,500 processes across 9 packages (§3.3); fixtures in `testkit`, `checks`, `setup`, `tests/release` | — |
| `claude` | `cmd/obor` `TestClaudePlugin` (`plugin_wiring_test.go:212`), if on PATH | — |
| `node` | `cmd/obor` `TestOpencodePlugin` (`opencode_test.go:18`), if on PATH | — |
| `go` | `go build` in `cmd/obor` `TestMain` (`main_test.go:29`) and `TestGithooks` (`githooks_test.go:226`); `go list -deps` in `checks` `TestImportBoundaries` (`boundary_test.go:199`); `go env` in one release test | fake `go` script in `tests/release`, `shim_test` |
| `make`, `sh`, `bash` | `tests/release` (60 tests), `cmd/obor` shim/`bin/obor` tests — our own Makefile and scripts | — |
| `ps`, `sh`, `sleep` | `launch` `TestRestartSuspended` (`launch_test.go:1699`): a real stopped child read by real `ps` | fake `ps` elsewhere |
| `curl`, `tar` | `tests/upgrade` `release()`/`download()` (`check_test.go:721`), only in `TestUpgradeCheck` (env-gated) — the two frozen-binary tests use the cache | stub `curl`, `file://` URLs in shim/install tests |
| built `obor` binary | `cmd/obor` (75 tests call `run(t,…)`), `internal/run` (7), `tests/upgrade`, one in `setup` | — |
| herdr | **never** (`internal/herdr/herdrtest` fake + socket server) | |
| codex, opencode CLI | **never** | |
| `gh` | **never** (fake `gh` in `tests/release`) | |
| network | **never** in Go tests (only `net.Listen` on local sockets) | |

The conformance tier (`tests/conformance`, behind `-tags conformance`, nightly `TIER=static`) is where real tools are
checked: `H-S1/S2`, `H-L1…L11` (herdr), `CC-S1/S2` (claude `--help`, hook events), `CX-S1…S3` (codex), `OC-S1/S2`
(opencode `--help`; `node --check` + `node --test opencode/obor.test.js`, `static.go:462-474`). Only `OC-S2`
duplicates a Go test.

## 2. Families

Kind: (a) our logic, tool only a fixture; (b) the assertion is the tool's behaviour; (c) duplicates the conformance
tier. "Lane s" = seconds a `-short` lane still runs. Flake rows from §Method.

| Family | Package, where | External | Kind | Tests | Test-s | Lane s | Flake rows |
|---|---|---|---|---|---|---|---|
| Release tooling | `tests/release/*_test.go` (60) | real `make`, `sh`, `git` (bare origins, tags), fake `go`/`gh` | (a), our scripts; rarely changing | 60 | 544 | 544 if run | 1 |
| Frozen-release upgrade | `tests/upgrade/upgrade_test.go:140,151` | two cached release binaries `~/.obor/bin/obor-0.23.{0,1}-*`, real `serve` children | (c) of `make upgrade-check`; host-dependent (skips without the cache) | 2 (+3 subtests) | 74 | 74 | 0 |
| Loop rigs on real git | `internal/app/loop_test.go:81` `newLoopRig` → `scratch_test.go` `gitScratch` (66 tests, 24 files) | real `git`, `sh -c` of configured commands | (a); git is incidental to what they assert (states, steps, jobs) | 66 | 242 | 88 | 16 |
| Real adapter over git | `internal/checks` (`scratch_test.go`, `specgate`, `mismatch`, `replay`, `lanetest`…) | real `git`, git shims for cut/ctx cases | (a); the right place for git | 40 | 222 | — | 4 |
| serve end to end | `cmd/obor/serve_loop_test.go:322-469` | real adapter, real git, `herdrtest` | (a); scenarios repeated in `checks` and `app` | 6 | 16.7 | 16.7 | 18 |
| obor CLI as a subprocess | `cmd/obor` via `run(t,…)` (`main_test.go:50`) | the freshly built binary | (a) | 75 | 91.4 | 70.0 | 7 |
| Shim and plugin wiring | `cmd/obor/shim_test.go`, `shim_hashook_test.go`, `plugin_wiring_test.go` | real `bash bin/obor`, stub binaries that hang, real timeouts | (a); 2 s watchdog waited out in real time | 21 | 38 | 14 | 15 |
| opencode plugin | `cmd/obor/opencode_test.go:18` | `node --test opencode/obor.test.js` | (a) for our JS, (c) of `OC-S2` | 1 | 5.1 | — (Slow) | **9** |
| claude plugin validate | `cmd/obor/plugin_wiring_test.go:212` | `claude plugin validate` | (b) | 1 | 0.7 | 0.7 | 2 |
| Git hooks | `internal/setup/githooks_test.go:222` | `go build` of obor, `git push/commit` with the hooks | (a) | 1 | 4.6 | — (Slow) | **5** |
| Suspended pane | `internal/launch/launch_test.go:1699` | real `sh`, `sleep`, `ps`, `SIGSTOP` | (b) for `ps` output; not Slow | 1 | 6.2 | 6.2 | 0 |
| Import boundaries | `internal/checks/boundary_test.go:155` | `go list -deps -json ./...` | (a) | 1 | 0.7 | 0.7 | 1 |
| Script syntax | `cmd/obor/plugin_wiring_test.go:134` `TestScriptsParse` | `bash -n` | (c) of the `guard` command (`bash -n` on changed scripts) | 1 | 0.2 | 0.2 | 0 |

Families add to more than their parents because the shim and plugin_wiring rows overlap `cmd/obor`'s run.

## 3. Facts behind the recommendation

### 3.1 `tests/release` — rank 1

- 60 tests, 544 test-s, 85 s wall, 5,177 git processes (1,115 `-C`, 1,080 `show`, 944 `rev-parse`, 475 `status`,
  400 `tag`, 395 `describe`), one flake row in 13 days. None is `testkit.Slow`.
- Each test builds a kit copy, a real git repo with a bare `origin`, a fake `go`/`gh`, then runs `make release`/`ship`/
  `publish` (`release_test.go:68-130` `newReleaseFixture`). The slowest: `TestMakeShip` 32.7 s,
  `TestShipPublishEvidence` 32.3, `TestShipPushDoneWhenOriginContainsMain` 27.4, `TestMakeReleaseResumes` 25.5.
- They run: in a lane when a release-class path changes (`check_release`, `.obor/config.json` `merge.classes`
  "release"); in `make check` because `go test ./...` includes the package (Makefile `check` target, `$pkgs=./...`);
  and in `scripts/release` before the tag (Makefile comment at `check-release`: "scripts/release runs this target
  once more before the tag"). So at the promotion they re-test files that have not changed.
- Risk of the change: a release-class path changing on `staging` between two promotions must still run them. The
  lane already does (class `release`), so the promotion needs only to run `check_release` when the promoted range
  touches those paths, or rely on the lane run plus the release-time run.

### 3.2 `tests/upgrade` — rank 2

- `TestUpgradeV0230ToV0231` (34.65 s) and the two `TestUpgradeBrokenBuild` subtests (4.41 s, 35.23 s) each run the
  whole upgrade harness between v0.23.0 and v0.23.1, two released binaries read from `~/.obor/bin` (present on this
  machine: `ls ~/.obor/bin`). `offline()` (`upgrade_test.go:103`) skips when they are not cached, so the result depends
  on the host.
- The harness is tested for real at each release by `make upgrade-check` (`TestUpgradeCheck`, env-gated, previous
  release from the catalog). The three offline runs pin a v0.23 pair, two minors behind v0.25.
- Keep `TestGuideSteps`, `TestGuideV023`, `TestPrevious`, `TestUpgradeCheckRefusesOldPrev` (0 s, pure logic). If the
  "no steps in the guide: step 3 fails" assertion is wanted, it needs a pair of stub binaries, not releases (not
  checked whether the harness can take stubs).

### 3.3 `internal/app` and git — rank 3

- git processes started per package (plain `go test`, wrapper log): `internal/app` 9,969, `internal/checks` 9,341,
  `tests/release` 5,177, `cmd/obor` 3,295, `internal/launch` 2,041, `internal/setup` 2,031, `internal/guard` 1,127,
  `internal/web` 327, `internal/view` 203: **33,511**. One `git -C . rev-parse HEAD` costs 15 ms wall at this load
  (100 in 1.50 s); commits and worktree adds cost more, so ~8 min of process time is a floor.
- `internal/app`: `newLoopRig` (`loop_test.go:81`) makes a `testkit.Project` (3 git calls), commits a 10-line file,
  `Lane` (worktree add), and wires a `gitScratch` — "the fake app.Scratch the loop tests drive: the real adapter's steps
  over the fixture's git in a few lines" (`scratch_test.go:20-24`). 66 tests in 24 files use it: 242 test-s of the
  package's 556, 88 of them in the `-short` tier (29 are Slow-marked), 16 flake rows. What they assert is states, steps,
  jobs and prompts of the loop (`TestLoop*`, `TestDrive*`, `TestStop*`), not git.
- The change: a `fakeScratch` with scripted outcomes (green, red, conflict, moved, fail) behind `port.Scratch`, and
  `testkit.New` only where a test needs a real tree. **I did not prototype it**: the 242 s is the whole of these tests,
  not what git accounts for in them. 9,969 git calls × ≥15 ms ≈ 150 s of process time bounds the saving from below, but
  tests that wait on timers would not shrink.
- `internal/checks` (40 tests, 222 test-s, 9,341 git calls) is where the real adapter is checked; leave it. Within it,
  `specgate` (4 tests, 33.5 s), `mismatch` (4, 26.1), `replay` (3, 16.7) cost 8–15 s each for a conflict fixture each
  time; sharing the fixture would cut them. Not measured beyond their times.

### 3.4 Flake families (rows 4–8)

- `cmd/obor` `TestServeLoop*`: `CrashAfterGateCommit` 5, `MovedTarget` 4, `LaneLockNotCounted` 3, `HappyPath` 2,
  `MergeBodyKeepsSummary` 2, `ConflictResolved` 2 = 18 rows. All six are in the `-short` lane tier. Each scenario has an
  `internal/app` loop test (`TestLoopMovedConflictResolved`, …) and an `internal/checks` adapter test
  (`TestScratchMovedGateConflict`, `TestRV39MovedSubject`); I compared names, not coverage — a coverage diff should
  confirm before deleting rather than marking Slow.
- `TestOpencodePlugin` (9 rows, the top single external test): its own comment (`opencode_test.go:46-48`) says the
  suite "takes ~5 s idle (one case waits out obor.js's 5 s hook timeout) and its fixtures run git" and that the old
  20 s limit timed out under gate load. Its Go-side asserts (file, package.json, plugin path in `opencode.go`) are
  cheap and (a); the `node` run is `OC-S2`.
- `TestGithooks` (5 rows): `exec.Command("go","build",…,"../../cmd/obor")` at `githooks_test.go:226`, a second compile
  of obor next to `cmd/obor`'s `TestMain` build, then 9 git/hook cases. `hx.binary(script)` (`githooks_test.go:213`)
  already stubs the binary for the cold-start cases.
- Shim/plugin_wiring hang cases: `TestPreToolUseHangingBinaryDenies` 6.3 s (2 rows), `MissingBinaryDenies` 4.9,
  `StaleBinaryDenies` 4.6 (1), `TestShimSelfHost` 4.8, `TestShimHookSkipsHangingDevBuild` 2.9 (2),
  `TestShimPicksBinary` (3), `TestShimHookSkipsStaleDevBuild` (3), `TestPluginManifests` (2),
  `TestClaudePlugin` (2). `bin/obor` has no env knob for the watchdog (`grep -n timeout bin/obor` finds nothing
  I could use; the 2 s is stated in the test comments, `shim_test.go` "the shim's own 2 s watchdog").
- Not external, for completeness: the biggest flake sources are `internal/app` tests with the fake herdr and timers
  (76 rows in the package: `TestWorkerStartRule` 5, `TestNeedsYouAnswerPathsAreAccepted` 4, `TestWakePostRefusedThenReplayed`
  4, `TestHoldAndResume` 4, …) and `internal/launch` (`TestStartBusyOnFirstPrompt` 6). These are timing, not tools.

## 4. Not found / not verified

- Cause of any flake row: I only attributed rows to the test's name and what it runs.
- The saving of §3.3 (an in-memory scratch): estimate only.
- `tests/release` and `tests/upgrade` ran with `~/.obor/bin` populated; on a machine without it
  `TestUpgradeV0230ToV0231` skips (the 74 s would not exist there).
- Whether the lane tier (`check_affected`, `-short`) runs `tests/release`: only through the `release` class; I did not
  run `obor affected go` to see which packages a Go change pulls in.
- The old 11-07 11:09 runs differ (cmd/obor 39 s vs 68 s now, internal/app 53 s vs 151 s): load 20–57 then and now;
  absolute seconds are not comparable between the two sessions.
- Per-test CPU (user+sys) was not measured; `go test -json` has only elapsed.

## 5. Where the data is

`/private/tmp/claude-501/-Users-djordjeradakovic-Work-obor-kit/476cd480-4685-476c-b337-07dde3093cc7/scratchpad/`:
`runs/<pkg>.json` (timings), `runs/runs.txt` (wall and load per package), `joined.json` (test × time × tags ×
flakes), `tags.json`, `gitcount.txt`, `gitlog_<pkg>.txt`, scripts `join.py`, `tag.py`, `run.sh`, `gitcount.sh`.
