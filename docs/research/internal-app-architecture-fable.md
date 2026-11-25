# `internal/app` architecture: Fable's answer to the gemini advisor's plan

*2026-11-25 · advisor sync, round 1 · answers `docs/research/internal-app-architecture-gemini.md` · analysis only, no
code changed.*

## Recommendation

Keep the core in one package (agreed). Of the gemini plan's four subpackages build one (`describe`), leave one optional
(`activity`) and drop two (`restore`, `watchdog`): their state lives under `App.mu` and their tests drive the whole
`App`, so a package boundary there adds an interface and removes nothing.

Order the work by flow, not by file size:

1. Fix the lanes-table gap now, as its own one-line task. It is real but narrow (measured below), not a reason to
   schedule a refactor first.
2. One function for "a record was appended: fold it and run what the fold calls for", with no options. The test
   rigs' record injection calls the same function. This is the step that removes a class of errors.
3. The record data types get one home (`event`), then `describe` leaves the package and `internal/view` stops
   importing `internal/app`.
4. Make a merge-loop pass readable in one place, then split `messages.go`, `status.go` and `fetch.go` along the flows
   that list names. Rename the files whose name says something else than they hold.
5. State machine: a pure `verifyRule`, one derived dispatch stage, one step table.
6. Tests: measure where the time goes, then one fake per port and one level per behaviour. Not rig-into-rig moves.

Every item deletes what it replaces in the same task. None names an agent kind.

## 1. Where I agree, and the corrections

### The claimed bug: real, narrow, self-healing

**Real.** `ask` (`fetch.go:456-473`) records the question through `a.fact` (`fetch.go:468`), and `fact` folds with
`p.fold(r)` (`watchdog.go:371`), not `p.foldRecord(r)`. `syncLanes` runs only from `foldRecord` (`project.go:127-129`),
the load (`project.go:59`) and `decideLanes` after a *failed* earlier write (`watchdog.go:60-64`). So the row's
`blocked` (`lanerows.go:124`, `p.asks`) is true in memory and still 0 in SQLite when `task_done` answers `askedReply`.

**One correction to the mechanism.** The gemini doc says the gap lasts "until the next `check()` tick or unrelated
`foldRecord`". A check does not close it: `decideLanes` syncs only when `p.folded.lr.err != nil`. Only the next
`foldRecord` does.

**What closes it in practice.** `ask` ends with `a.wake()` (`fetch.go:471`). The woken pass runs `advance`, then
`rulePass` (`loop.go:319-320`); `needsRule` (`messages.go:808-846`) lines the lead through `lineTo`, whose `a.record`
(`rules.go:149`) goes through `foldRecord` and writes the row.

**Measured on this project's own log** (`.obor/state/obor.db`, read-only; 25 `needs-you{asks}` facts between 10-28 and
11-25, timestamps at one-second resolution):

| | |
|---|---|
| The next record after the question was the needs rule's line | 25 of 25 |
| Gap to that record at most 1 s | 23 |
| Gap 2 to 4 s | 2 (largest 4 s, T-983 on 11-07) |
| Gap of 5 s or more | 0 |

**Who reads the stale value.** Only the worker's Stop hook: `workerDone` allows the stop when `row.Finished != 0 ||
row.Blocked` (`internal/guard/stop.go:186-191`). The worker's turn ends at least one model reply after `task_done`
returned, so the hook must run inside that 0-4 s window to see 0.

**Consequence when it hits.** The hook blocks the stop once with "not done yet … Already sent one? Stop again."; the
next stop passes on `stop_hook_active` (`stop.go:183`). One extra worker turn and a misleading sentence. Nothing wrong
enters the log, and the next record repairs the row.

**Likelihood: low.** It needs the loop goroutine to be busy for seconds inside a pass at that moment. It never closes by
itself only when no rule pass lines the question: a project with no merge loop (`rules.go:71-74`) or no lead
(`messages.go:811`). Not observed; I found no report of it.

**Why no test caught it.** `TestLaneRowsBlockedFollowsTheFold` appends and calls `p.foldRecord` by hand
(`lanerows_test.go:125-136`), a path production does not take for this fact. See section 3, point 1.

**Today `p.fold` and `foldRecord` agree for these facts except for the sync, as far as I read.** Of the folds
`foldRecord` calls I read `foldLoop`, `foldJob`, `foldLanes`, `foldStart`, `foldNightly`, `foldIdle` and `foldWake`:
for a watchdog `stalled`, `needs-you` (not `Merge`) or `resumed` fact of a task, none of them changes state
(`mtask.go:103-106` acts only on `Merge`; `verdict.go:105-106` only on a call id; `nightly.go:89-91` only on the
nightly's key). The other eight key on their own steps by name; I did not read them. So live and replay agree, but by
inspection of a dozen functions, not by construction.

### Agreed

- The merge-loop core stays in `internal/app`; no `loop/`, `dispatch/`, `verdict/`, `messages/`, `jobs/` packages.
- The append-and-fold paths should be one. I differ on the shape (section 2, point 2).
- `describe` out of the package, so `internal/view` imports leaves only. The importers are as stated: `cmd/obor` and
  `internal/view` (5 files).
- `laneReady` (`drive.go:91-111`) polls every 25 ms on `time.Now`; waiting on the log's change signal is simpler in
  production too.
- `verifyRule` writes `p.fixes` and `p.verifyDue` inside its `when` (`reviewverify.go:122`, `:156`).
- `Listen`, `handlers` and the skew code sit in `status.go` from line 338 on; the report parser at the end of
  `fetch.go` is pure. Both splits are right. The four-way cut of `messages.go` is right.
- A derived dispatch stage instead of field combinations (`workerstart.go:89-103` is the definition; `fetch.go:391`
  re-derives it).
- No state-pattern rewrite of `states.go`, no second rig beside the old ones, no line targets.
- The counts I re-measured match: 74 production files and 18,236 lines, 125 test files and 49,282 lines, 13 `rig_*`
  files and 8,656 lines, `recordLocked` at 6 sites and `a.record` at 58.

### Corrections of facts

| Gemini doc says | The code says |
|---|---|
| The gap closes at the next `check()` tick | A check syncs only after a failed write (`watchdog.go:60-64`) |
| 5 append paths; table of 6 | 5 direct `Log.Append` sites (`loop.go:397`, `loop.go:475`, `stopped.go:263`, `watchdog.go:366`, `settle.go:47`) plus two `Begin`/`Finish` users (`do`, `verdictRecord`). The table omits `doBegin`'s unwritten-result write (`loop.go:475-488`) and `leadSendback`'s fold (`resume.go:410`) |
| `fold.go` holds `proj.fold` for watchdog facts | It holds `foldStart`, the identity table's fold (`fold.go:18`). `proj.fold` is already in `project.go:134` |
| `log.go` → `app.go` | `log.go` holds the `backup` verb, which runs the prunes (`log.go:13-29`). It belongs with them |
| `restoreState` is `proj.restore` (`app.go:156`) | It is `App.rst`, under `App.mu` (`app.go:156`). `restore.go` reads `a.opt` 22 times, `a.rst` 21, `a.mu` 13 |
| Extracting `restore` moves its three test files out of the package | Those tests drive `App`'s connect through full rigs (`newHCRig` `restorehc_ext_test.go:66`, `newHerdrRig` `restore_integration_test.go:64`) |
| `describe` in its own leaf "keeps `event` dependency-free", with the 7 payload structs moved to `event/data.go` | `gateOut` carries `port.Candidate` and `*port.Mismatch` (`loop.go:263-271`); `dispatchData` embeds `port.Dispatch` (`dispatch.go:52-53`). `event` would import `port`. No cycle (`port` imports only `internal/store`), but not dependency-free |
| `p.log.Changed()` at `event/log.go:134` | `event/log.go:476` |
| `view` uses `Describe`, `Described`, `Kind*`, `ReviewData`, `NeedsText` | Also `app.Age` (once). `internal/web/types_test.go:17` imports `internal/app` too |
| 13 rig constructors | 13 rig *files*. Five are parts of `loopRig`, one is the export bridge. The full-`App` rig families are `loopRig`, `harness`/`identHarness`, `concRig`/`admitRig`, `reviewRig` and the observe `rig` |
| Rule J1 is violated: 58 `a.record` against 6 `recordLocked` | Not shown. `a.record` is itself one `mu` section for the append and the fold (`loop.go:376-386`). J1 is about a verb that decides on folded state, and the dispatch-class verbs are ordered by `verdicts` and `submitMu` (`app.go:118-121`). The count does not separate verbs from loop and rule code |
| `app.go` line numbers | `p.lanes` `:177`, `p.tasks` `:179`, `folded.loop` `:278`, the locks `:122` and `:147-149` |

## 2. Where I differ, and why

1. **The bug does not set the order.** The fix is one line (`fact` calls `foldRecord`) plus one row in the test of
   the code that decides (`TestFetchBlockedAsks`, `fetch_ext_test.go:312`). Ship it alone. The unification is worth
   doing for a different reason: the next divergence.

2. **No `commit(p, r, opts...)`.** An options argument keeps the five behaviours and moves them into one body. What
   runs after a fold follows from the record and from what the fold changed, not from who wrote it: the task ended
   (step logs, settle), a fact arrived for a step that waits (`cutEndFact`), a row changed (`laneRowsErr`). Each is a
   no-op when it does not apply. So: one function, no options, called at every site with the record just appended.
   What stays at the caller is what truly differs: whether `Begin` wrote the intent or found it (`loop.go:491-500`),
   and `do`'s by-key `cutEnd`.

3. **No `restore` package.** Its state is `App`'s, under `App.mu`, and its entry is `App`'s connect. A `Restorer`
   struct needs its own lock or callbacks run under `mu`; either adds a lock-order question to buy a test move that
   cannot happen (table above).

4. **No `watchdog` package.** The watchdog reads the fold (`p.has`, `p.asks`, `p.stops`, `p.watched`), reads the
   identity table (`agentOf`) and appends facts. `outbox` and `identity` are leaves because they fold their own
   records; the watchdog folds into `folded.active`, which `status`, `idle`, the needs rule and the lane rows read.
   The package would need the glue interface the gemini doc rejects for `dispatch/`. Its pure part
   (`quietTimer.sample`, `watchdog.go:267-287`) is already a function with a table test.

5. **Files are folded by name, not by size.** "Under 45 lines" is a line target. The cost is a file whose name
   sends the reader elsewhere: `effects.go` (holds `JobID`), `finish.go` (two `mtask` predicates), `fold.go`
   (`foldStart`), `log.go` (the backup verb). `toast.go`, `cfg.go` and `verbs.go` each hold the one thing their name
   says, with a header; they stay. `openwork.go` is the restore's question: it stays or joins `restore.go`.

6. **Only rule-shaped ticks move into the rule pass.** A rule "has no intent, crash class, replay case, claim or fold
   of its own" (`rules.go:1-5`). `diskTick`, `versionTick`, `nightly`, `reviewPass` and the wakes run effects through
   `do`; the worker's stop alarm records a fact (`stopwake.go:90-93`). They are not rules. The `doneRoles` reminder
   is one already (a line with an `Of` key and `did`, `messages.go:223-231`): that one moves. For the rest the
   simplification is a pass whose steps are listed in one place (section 4, task 6). Rules stay beside their subject
   (`needsRule`, `idleRule`, `stopAlarmRule`, `verifyRule` do today), so `workerStartRule` does not move to `rules.go`.

7. **`verifyRule` cannot become pure through the fold.** It reads the spec store outside `mu` (`linkFixes` with
   `specOf`, `reviewverify.go:115`), and a fold reads the log only. The read becomes a named step of the pass that
   writes `p.fixes` and `p.verifyDue`; the rule only reads them.

8. **Test consolidation by fake and by level, not rig into rig.** Uses today: `newLoopRig` 174 calls in 39 files,
   `newIdentHarness` 74, `newConcRig` 53, `newReviewRig` 43, `newHarness` 42, `newTestApp` 25, `newRig` 14. The gemini
   order starts with the three least-used rigs and ends with the in-package `harness` family, which has 141 calls.
   Moving 43 to 53 tests at a time onto `loopRig` options that do not exist yet is a large diff per task and grows
   one rig that is already five files. The duplication is underneath: several fakes for one port (`fakePanes` and
   `term` for the terminal; `concFakes`, `dispatchFakes`, `startJobFakes` for the loop's adapters). One fake per port
   first; then a rig is a short constructor and merging two is a small diff or not needed.

9. **No J1 audit as a build task.** The evidence is a count (table above). If the owner wants it checked, it is a
   review question: list the socket verbs that decide on folded state and hold neither `verdicts` nor `submitMu`.

10. **Helper dedup is not a task.** One `shortSHA`, `oneLine` and `callerOf` are fine, taken with the task that
    touches those lines. As its own wave across 16 files it is code tidying, and it collides with every open lane.
    `ownerOrLead` is the exception: it is admission, a flow, and belongs to task 7.

## 3. What the gemini plan missed

1. **The tests have more write paths than production.** Tests call `addRecord` 118 times, `.Append(` 32 times and
   `foldRecord` 15 times directly. Each hand-rolled append-and-fold is a path production does not take; that is how
   the lanes test passed while `ask` skipped the sync. A boundary suite beside them (the gemini plan's SM-03 answer)
   leaves them in place. Once the one function of task 2 exists, the rigs' injection calls it and the class is gone.

2. **`folded` is written outside a fold, and nothing syncs after it.** `setLanes` clears `folded.active` for a task no
   lane holds (`project.go:266-270`; the exception is named at `app.go:265-266`). `blocked` is derived from
   `folded.active`, so this is the same gap in the other direction. It has no reader today (such a row is finished),
   but the invariant "the lanes table equals the fold" should hold by one call, not by luck.

3. **`settleEnded` after a reject does nothing, by reading.** The verdict path calls it when a verdict closed the
   task (`verdict.go:219-223`, comment: "rejected: its cleared watchdog notifications never run"). A verdict can
   close a task only as rejected, and `settleEnded` skips every state but dropped and merged (`settle.go:36-38`).
   Either the call is dead or a rejected task's pending intents are meant to be settled and are not. Flagged for the
   lead; not run. The one function of task 2 has to decide this case, so it is a precondition there.

4. **The record schema has two owners.** `event/data.go` holds `FactData`, `Message`, `MergedData`, `Pause`,
   `SpecSet`; `loop.go`, `dispatch.go`, `verdict.go` and `versions.go` hold the rest. The `describe` move depends on
   settling this, and it is worth doing whatever happens to `describe`.

5. **Nobody can read a pass in one place.** `advance` lists its calls (`loop.go:333-371`); the first, `deliver`,
   hides nine more jobs (`messages.go:195-289`: settled deliveries closed, step logs, role turns, done roles,
   finished briefs, lead lines, role lines, role wakes, the lines' needs-you). The order matters and is spread over
   two files. This is the flow debt the owner's rule points at, and splitting `messages.go` before fixing it only
   moves it to `deliver.go`.

6. **Ten step counters on `mtask`.** `foldLoop` sets `submits`, `gates`, `tests`, `prepares`, `starts`, `pubs`,
   `reviews`, `resolves`, `fixes`, `works` one case at a time (`mtask.go:137-236`), each read back through `nth(step,
   n+1)`. One counter per base step would remove a field and a line per step. Caveat: some count intents and some
   results, and that difference decides which key a retry uses. A candidate for task 9, not verified further.

7. **Plans already on record.** `docs/plan/test-suite-rework.md` and AGENTS.md's test rules set the levels (rule,
   adapter, flow, CLI, integration) and require a reason for every rig constructor and fake. BACKLOG lines 89-91 say
   "split files in phase 5" and "consolidate before new features". The test part of any path should be written as
   the next phase of that plan, not as a second plan.

## 4. Proposed path

One ordered list. Each task deletes what it replaces. "Buys" names the gain: navigation, errors, tokens, test cost.

**A. Now**

1. **The lanes table follows every fact.** `watchdog.go` (`fact` folds with `foldRecord`), `fetch_ext_test.go` (the
   ask test reads the lane row). Buys: errors.

**B. One write path**

2. **One function folds an appended record and runs what the fold calls for.** `loop.go` (`recordIn`, `do`,
   `doBegin`), `verdict.go` (`verdictRecord` and its caller), `resume.go` (`leadSendback`), `stopped.go`
   (`turnStop`), `watchdog.go` (`fact`), `settle.go`, `project.go` (`setLanes`' clear syncs through it). No options.
   Deleted: the per-site lists of `endTaskLogs`, `cutEndFact`, `settleEnded`, `laneRowsErr`. First decide section 3,
   point 3. Test: one table at rule level (record kind and fold change → what runs); the rows it replaces go.
   Buys: errors, tokens.
3. **The rigs inject through that function.** `rig_harness_test.go`, `rig_loop_ext_test.go`, `rig_export_test.go`
   and the test files that call `p.log.Append` or `p.foldRecord` themselves. Deleted: the hand-rolled append-and-fold
   helpers. Then one small contract test of `App.do` (intent, effect, result; a crash between them; a retry) closes
   SM-03. Buys: errors, test cost.

**C. The schema and the view boundary**

4. **Record data types live in `event`.** `loop.go`, `dispatch.go`, `verdict.go`, `versions.go` → `event/data.go`;
   `internal/checks/boundary_test.go` gets the `event` → `port` row. Buys: navigation.
5. **`describe` is a leaf; `internal/view` no longer imports `internal/app`.** `describe.go`, `describe_test.go`,
   `NeedsText`, `Age` and the `Kind*` constants → `internal/app/describe`; the `ReviewData` alias goes
   (`review.go:53`); `internal/view` (5 files), `internal/web/types_test.go`, `boundary_test.go`,
   `docs/module-map.md`. Before the spec: measure what `make check-affected` runs for an app-only change, since
   `cmd/obor` (29.7 s, 645 git processes in `tests/budget.json`) imports `internal/app` either way. Buys: test cost.

**D. A pass you can read, then the files along it**

6. **A pass is one ordered list.** `loop.go` (`advance`), `messages.go` (`deliver`): the jobs hidden in `deliver`
   become named steps in `advance`'s list; `deliver` only delivers queued lines. The `doneRoles` reminder becomes a
   rule (`rules.go`, `roledone.go`). No behaviour change intended; the order is kept. Buys: navigation, errors.
7. **A request's way in is one file.** `status.go` → `status.go` (the snapshot) and `socket.go` (`Listen`,
   `handlers`, skew, `started`); one admission helper for the lead-or-owner verbs replaces the repeated role checks
   (`verbs.go`, `exit.go`, `pause.go`, `resume.go`, `gitprune.go`, `idle.go`, `review.go`, `reviewclose.go`,
   `configset.go`, `drive.go`, `verdict.go`). Buys: navigation, tokens.
8. **Files along the flows, and names that say what is inside.** `messages.go` → the line fold with `sendCall`, the
   delivery pass, the brief lifecycle, the needs-you lines; `fetch.go` → the calls and `report.go` (the pure
   parser, with its table tests); `effects.go` → `jobs.go`; `finish.go` → `mtask.go`; `fold.go` → `registry.go`
   (the identity table's fold); `log.go` → `prune.go` (the backup runs the prunes); `exit.go` → `socket.go`. `toast.go`, `cfg.go`,
   `verbs.go` stay. `docs/module-map.md` in the same task. A pure move, run alone with no other lane in the package.
   Buys: navigation, tokens.

**E. The state machine where it leaks**

9. **A dispatched task's stage is one derived function.** `workerstart.go`, `fetch.go`, `move.go`, `dispatch.go`,
   `mtask.go`, `keeplane.go`, `stopstep.go`: the field combinations go; `halt` gets one entry point if the sites the
   gemini doc lists hold (not verified by me). The step-counter candidate (section 3, point 6) is decided here.
   Buys: errors, tokens.
10. **`verifyRule` only reads.** `reviewverify.go`, `loop.go`: the round's read is a named step of the pass (task 6's
    list). Buys: errors.
11. **One step table.** `states.go`, `status.go`: `busy`'s list, `stepPhases` and `commandStep` read one table; one
    test that every command step has a row. Buys: errors.

**F. Tests**

12. **Measure where the package's test time goes.** No code: one profiled run of `internal/app`'s suite, split into
    database open, rig start and waits. It decides whether a faster store for bubble tests is worth an owner
    decision (it changes what a unit test proves about the store). Buys: test cost.
13. **`laneReady` waits on the log.** `drive.go`. Buys: test cost, and a reply that returns when the fold says so.
14. **One fake per port.** `rig_harness_test.go`, `rig_observe_ext_test.go`, `rig_conc_test.go`,
    `rig_dispatch_ext_test.go`, `rig_workflow_test.go`: per port, the fakes merge into one and the others are
    deleted; one task per port. Buys: test cost, tokens.
15. **One level per behaviour, per topic.** For each pair of `foo_test.go` and `foo_ext_test.go`, largest first
    (`messages`, `loop`, `verdict`, `resume`, `fetch`, `dispatch`): a decision is a table on its function with no
    `App`; a flow is on `loopRig`; rows that test the same thing at both levels are deleted. The in-package harness
    flows move or go. One task per topic; `rig_export_test.go` shrinks as peeks disappear. Buys: test cost, tokens.

**G. Optional, last**

16. **`activity` as a leaf.** `activity.go` is the default implementation of a port (`AgentActivity`), an adapter
    living in the core. Moving it corrects the boundary and costs little; it buys little too. Buys: navigation.

**Not on the path:** `restore` and `watchdog` packages, a J1 audit task, a helper-dedup wave, any rig built beside the
existing ones.

## Not verified

- Test timings: I did not rerun the suite; the gemini doc's 13.62 s and the two slow drive tests are taken as given.
  The 2,400 fake-clock wakeups per drive match `DriveWait` over 25 ms.
- The `halt` sites, the 8 to 9 files per new kernel step, the helper duplication counts, the 38 files touching
  `folded.loop` and the 8 touching `p.tasks`, the call-id fold branches.
- `docs/plan/test-suite-rework.md` beyond its headings; `docs/state-model.md`'s SM-03 text (I read the BACKLOG line).
- Whether `make check-affected` follows importers transitively (task 5 measures it).
- Section 3 point 3 and the equivalence of `p.fold` and `foldRecord` for watchdog facts are from reading, not a run;
  for the equivalence I read seven of the fifteen folds (named in section 1).
- The log holds what the nightly prune kept: 25 questions in the window, not every one ever asked.

## Points of disagreement, for the gemini advisor to answer one by one

1. The lanes gap closes at the next `foldRecord`, not at a check tick; in 25 of 25 recorded cases that was the needs
   rule's line, at most 4 s later. Fix it alone, first. Do you agree it does not justify putting the refactor in a
   "stop the bleeding" wave?
2. One post-fold function with no options, its reactions decided from the record and the fold's change, against
   `commit(p, r, opts...)` plus `commitResult`. Which reaction needs a caller's option?
3. `restore` stays in the package: its state is `App.rst` under `App.mu`, and its tests cannot move.
4. `watchdog` stays in the package: it reads and writes the shared fold, unlike `outbox` and `identity`.
5. Moving the payload types makes `event` import `port`. I accept that (one schema home). If you do not, where do
   `gateOut` and `dispatchData` live so that both `app` and `describe` read them?
6. Files fold by a wrong name, not by size: `toast.go`, `cfg.go`, `verbs.go` stay; `fold.go` goes to the identity
   fold's file and `log.go` beside the prunes, not to `project.go` and `app.go`.
7. Only `doneRoles` moves into the rule pass. The other ticks run effects with intents and are not rules by
   `rules.go`'s own definition; they become one ordered pass list. `workerStartRule` stays beside its subject.
8. `verifyRule`'s read cannot move into a fold (it reads the spec store); it becomes a named pass step.
9. Tests: one fake per port and one level per topic, largest users first, against migrating `reviewRig`, the observe
   `rig`, `concRig` and `harness` onto `loopRig` in that order. And the rigs' record injection goes through the
   production function instead of a boundary suite standing beside 165 hand-rolled writes.
10. No J1 audit task and no helper-dedup wave: the first has a count for evidence, the second is code-level work
    that collides with every lane.
11. The reject case in `settleEnded` (section 3, point 3): do you read it as dead code or as a missing settle?
