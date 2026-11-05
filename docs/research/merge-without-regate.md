# Merge without a re-gate after a clean move? (ob-fable, 2026-11-05)

Question (owner): may a green, integrator-accepted candidate merge without a re-gate when the target moved and the
merge onto the new target is clean?

## Answer
Yes for the tests, no for the compile steps. On a clean move, stop re-running `go test`; keep build, gofmt, vet and
deadcode on the merged tree (under a minute). Keep the full re-gate when the two sides changed the same file or
either side is `full` for `obor affected go` (go.mod, `merge.affected_full`, a removed package). Do not build the
post-merge check of main yet.

Why: in 12 days the moved re-gate ran 165 times and never caught a real break. It went red 15 times, all timing
tests under load, and each red sent a worker back with nothing to fix. The semantic conflicts our history does show
are compile errors (caught in seconds) or non-Go files read by tests (already outside T-900's contract).

## 1. What can break, and how often
Source: serve's event log (`.obor/state/obor.db`, 2026-10-25 to 11-05: 540 merges, 736 first gates, 165 moved gates).

**Moved re-gates: 0 real reds in 165** (131 green, 15 red, 19 conflict).
- 11-05, six reds: T-876, T-877, T-887, T-883 (twice), T-900. Every one went green later on the same sha with no
  code change. The failing tests were `TestWorkerStartRule` (4 of the 6), `TestOpencodePlugin`, `TestInitEndToEnd`:
  timeouts at load 25–40.
- Earlier, nine reds: T-574, T-579, T-810 passed unchanged. T-605 and T-721 fixed a wait in a test. T-612 fixed a
  race that was already on main. T-734 dropped its doc edits to avoid the full run. T-373 (twice) named two different
  timing tests on consecutive runs; no report text survives from that day.

**First gates do show semantic conflicts on clean merges: 4 in 489 pairs** (a pair = a candidate and one merge that
landed between its dispatch and its first gate). Found by a keyword search of the fix reports, so "at least 4".

| Class | Case | Caught by |
|---|---|---|
| Renamed symbol, new use on the other side | T-696: T-690 renamed `app.Started`, the candidate added a use | `make build` |
| Both sides add the same declaration | T-536: `case "agent.start"` twice in one switch | `go vet` |
| Candidate moves a non-Go file, a new test on main reads it | T-623: `githooks/commit-msg` moved, T-622's test copied it | full check |
| Candidate edits a non-Go file, a new test on main asserts it | T-627: `plugin.json` version against T-630's test | full check |

No case of the fifth class, Go behaviour (one side changes what a function does, the other relies on the old
behaviour, both compile): 0 in 489 pairs, so under 0.6% per pair at 95%.

Rate: 4/489 = 0.8% per pair. The moved windows held 108 pairs in 12 days, so about one conflict was expected there
and none came. At 11-05's volume (about 40 pairs) that is one slipped conflict every 3 days if nothing is checked;
half of them compile errors.

## 2. What catches a slipped break, how fast, at what cost
- **Compile error on main.** `after_merge` (`make build`) prints a WARN seconds after the publish, for non-test code.
  Then every gate goes red: build and vet are the first steps of every class check (`Makefile:68-73`). Each red is a
  sendback to a worker with nothing to fix; after two, a needs-you. Main stays red until the lead dispatches a fix or
  a revert. This is the case worth paying to prevent.
- **Behaviour break on main.** The next gate that runs the failing package: `cmd/obor` imports 27 of the 29 internal
  packages, so its tests run in almost every Go gate. Found in 5–15 minutes, but blamed on the wrong task. There is
  no `baseline` command in obor-kit's config, so a gate cannot tell main's red from the candidate's, and nothing
  pauses merges.
- **Measured example of that cost, 11-05:** `TestWorkerStartRule` arrived with T-874 (merged 13:16Z) and timed out
  under load. 5 red gates on 3 tasks, 4 sendbacks answered "no code change", 2 needs-you, 33 gate minutes, fixed by
  T-907 (merged 17:57Z), over 4 hours later. Nobody owns a red main until the lead reads the pattern.
- **Nightly (02:00).** Full check, non-blocking, `needs-you{full-check RED}` (`docs/design.md:529-531`). Up to 24 h
  late, and 53 merges to search on a day like 11-05. Of 9 nightly runs, 2 were red; neither was a merge interaction.
- **A post-merge check of main** does not exist.

## 3. Options (11-05: 36 clean moved gates, 140 gate minutes; my count includes T-903's last gate)

| Option | Saves per day at 11-05 volume | Risk left | Lines (production) |
|---|---|---|---|
| A. Today: T-900 | ~39 min: the 10 moves whose new commits were docs only. It kept 2 because it landed at 17:03Z | as decided | 0 |
| B. Looser overlap (changed packages, not affected sets) | +29 min (10 more gates) | a signature change with a new caller in another package compiles on neither check: needs C anyway | about +5 |
| C. Compile steps only on a clean move | +65 min, and the load reds go: 6 sendbacks and at least 27 min of repeat first gates | Go behaviour (0 seen) | about +15 |
| D. Never re-gate, post-merge check of main pauses merges on red | +100 min | compile errors land on main and block every gate until fixed | about +40 |
| E. Batch N candidates in one gate | first gates too | first gates were red 10 of 80 times, mostly load: a batch of 3 is red a third of the time, then bisect | large |

Why A cannot do more: T-900 compares affected sets (`internal/checks/scratch.go:253-265`), and every Go change's
affected set holds `cmd/obor`. So it keeps a Go candidate only against docs or non-Go commits.

One more option outside this question: accept first, run the checks last, publish at once (a merge queue's order).
It leaves no window and no risk, but it reorders rows 18–33 of the state model. Not for v0.28.

## 4. Recommendation: option C on top of T-900
- `keeps` (`internal/checks/scratch.go:228`) gives three answers instead of two. Unchanged: keep everything when
  nothing overlaps. New: when only the affected packages overlap (no common file, neither side full, last green on
  an ancestor), the gate runs the class's checks with `OBOR_MOVED=1`. Otherwise the full re-gate.
- obor-kit's `check-affected` skips `go test` when `OBOR_MOVED=1` and still runs build, gofmt, vet and deadcode.
  A project that does not read the variable re-runs everything, which is today's behaviour. The variable is exported
  as `OBOR_CHANGED_FILES` is (`internal/checks/runner.go:355`).
- The result line says so (`check_affected rc=0, moved`), and the merge is `partial`, so the log can count them.
- Measured here at load 30 with a warm cache: build 3.5 s, vet 3.5 s. Deadcode not measured.

Expected on a day like 11-05: moved gates fall from 140 to about 35 minutes. T-900 already gives 39 of that; this
adds 65, and about 27 more minutes of repeat first gates go with the load reds. So this change saves about 90 of
493 serial gate minutes, 130 with T-900. Resolved gates (41 min) are T-913's.

Net production lines: about +15 in `internal/checks/scratch.go`, +2 in the Makefile, one paragraph in
`docs/design.md`. Not net-negative; the reason is the gate minutes and six fewer false sendbacks a day.

Risks:
- A Go behaviour conflict lands on main. None seen in 489 pairs; the 95% bound is one every 4 days at this volume.
  The next gate finds it and blames the wrong task. Lead's rule until then: a red in a package the candidate did not change
  points at the last `partial` merges first.
- Non-Go readers: T-900 already leaves these to the nightly (`docs/design.md:551-553`), and they are 2 of the 4
  conflicts above. Flagged, not changed here.

Option D later, on a trigger: build the idle check of main after the first break that slips through, not before.
It would reuse the nightly's runner and worktree and the `full-check RED` needs-you.
