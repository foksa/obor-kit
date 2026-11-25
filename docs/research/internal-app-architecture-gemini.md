# `internal/app` Architecture — Gemini Advisor Recommendation

*Date: 2026-11-25 · Audience: owner, lead, and Fable (advisor sync) · Scope: analysis and plan only; no code changed.*

---

## 1. Recommendation

**Keep the merge-loop core in `internal/app`, extract 4 narrow-seam subpackages, split 4 overloaded files while folding 8 micro-files, unify the 5 event-write paths into 1, and consolidate the 13 test rigs down to 2.**

Do **not** break `internal/app` into a constellation of feature packages (`dispatch/`, `verdict/`, `messages/`, `jobs/`, `loop/`). Across 74 production files (18,236 lines), `folded.loop` (`map[string]*mtask`) is touched by **38 files**. Splitting the merge-loop core across Go packages would force exporting ~55 fields of `mtask`, inventing glue interfaces between peers, and multiplying token cost for every agent tracing a task lifecycle.

Instead, target the four concrete sources of navigation friction, bugs, and agent token burn:

1. **Unify the 5 append-and-fold paths into one `commit` primitive first (bug + SM-03 fix).** Today `recordIn`, `do`/`doBegin`, `fact`, `turnStop`, `verdictRecord`, and `settleEnded` each call `p.log.Append` with different post-fold side effects. One live bug already falls out of this: `ask()` (`fetch.go:468`) calls `a.fact` (`watchdog.go:361-374`), which calls `p.fold(r)` instead of `p.foldRecord(r)`, skipping `syncLanes()` so `lanes.blocked` stays `0` in SQLite when `task_done` returns `askedReply` to the worker's Stop hook.
2. **Extract 4 genuinely narrow-seam subpackages** matching the existing `identity/`, `outbox/`, `review/` pattern:
   - `internal/app/describe` (or into `internal/app/event`): breaks `internal/view → internal/app` completely.
   - `internal/app/restore`: owns `restoreState`, instance/runs/kind files, and single-flight restore.
   - `internal/app/activity`: pure process/transcript/pane liveness (`activity.go`, zero `App`/`proj` coupling) + `lanescan.go` lane-health scan (or keep `lanescan` with watchdog).
   - `internal/app/watchdog`: owns `watch` + `quietTimer` + `judge` behind a `Watchdog` struct (like `outbox.Outbox`).
3. **Reshape files inside `internal/app` without new packages:** split `messages.go` (1,172L), `status.go` (628L), `fetch.go` (596L), and untangle `workerstart.go` (400L) into single-topic files; absorb 8 micro-files (`effects.go` 9L, `finish.go` 14L, `toast.go` 26L, `exit.go` 27L, `log.go` 29L, `openwork.go` 36L, `fold.go` 38L, `cfg.go` 41L) into their owners so agents stop hopping 6 files to read one flow.
4. **Tighten the state machine where it leaks:** finish the `rules.go` migration (move the 7 ad-hoc ticks out of `advance()` and `deliver()`), remove `verifyRule`'s `when()` side effects (`reviewverify.go:122,156`), and make `stDispatched`'s 5-phase sub-pipeline (`workerstart.go:81-105`) and `halt` (`stopstep.go:27-81`) explicit rather than boolean-flag puzzles.
5. **Shrink test maintenance and speed up tests:** replace `drive.go:93-109`'s 25 ms polling loop with `p.log.Changed()` (immediately cuts the 2 slowest tests, 1.29 s + 0.64 s), collapse the 13 rig constructors (`rig_*.go`, 8,656L) and 30 paired `foo_test.go` + `foo_ext_test.go` files (25,211L) into one black-box `loopRig` + pure unit/table tests, and add a small `App.do` boundary suite to close SM-03.

---

## 2. Reasoning

### Snapshot of `internal/app` Today

Measured across the workspace (2026-11-25):

| Layer | Files | Lines | Notes |
|---|---:|---:|---|
| `internal/app` prod (`*.go`) | 74 | 18,236 | Top 6: `messages.go` 1,172 · `loop.go` 897 · `review.go` 649 · `status.go` 628 · `fetch.go` 596 · `dispatch.go` 572 |
| `internal/app` tests (`*_test.go`) | 125 | 49,282 | 821 test funcs; 13 `rig_*` files (8,656L), 43 `*_ext_test.go` (14,568L), 63 in-pkg (24,653L), 6 `*_integration_test.go` (1,405L) |
| Existing 8 subpackages (`event/`, `identity/`, `layout/`, `outbox/`, `port/`, `review/`, `shellpane/`, `wire/`) | — | 7,865 | Clean leaves; zero adapters import `internal/app` itself |

External importers of `obor/internal/app` are **only** `cmd/obor` and `internal/view`. Every adapter already imports only `port/`, `wire/`, `event/`, `layout/`, or `shellpane/`.

---

### Q1. Real Seams: Subpackages vs File Splits vs Keep Together

#### A. Extract to Subpackages (Own State + Narrow Interface)

1. **`internal/app/describe` (or into `internal/app/event`) — highest leverage per line**
   - **Why it is a real seam:** `describe.go` (240L) and `NeedsText` (`messages.go:861-874`) are 100% pure record-to-display functions. They touch zero `App`, `proj`, or `mtask` state.
   - **What it unlocks:** `internal/view` imports `obor/internal/app` in 5 files (`internal/view/detail.go:8`, `events.go:8`, `setup.go:12`, `tasklist.go:9`, `view.go:21`) **exclusively** for `app.Describe`, `app.Described`, `app.Kind*`, `app.ReviewData` (already an alias to `review.Data` at `review.go:53`), and `app.NeedsText`.
   - **What moves with it:** Move the 7 small JSON record structs `describe.go` decodes (`gateOut`, `testOut` in `loop.go`, `verdictResult` in `verdict.go:36`, `dispatchData`, `submitData`, `noteData` in `loop.go`/`dispatch.go`, `versionData` in `versions.go:27`) into `event/data.go` (which already holds `Pause`, `ConfigSet`, `Message`, `MergedData`, `SpecSet`, `Pruned`). Move `describe.go` + `NeedsText` into `internal/app/event` (preferred — zero new package) or `internal/app/describe` (if `event` should stay schema-only without `wire.Method*` imports — note `describe.go:79,83` references `wire.MethodAccept`/`wire.MethodSendback`, so a small `internal/app/describe` importing `event`, `review`, `wire` keeps `event` dependency-free).
   - **Boundary win:** `internal/view` stops importing `obor/internal/app`. `internal/checks/boundary_test.go:115-116` can lock `view` to `internal/app/*` leaves only, cutting `internal/view` and `internal/web` out of `make check-affected` whenever `internal/app` changes.

2. **`internal/app/restore` (`restore.go`, 452L)**
   - **Why it is a real seam:** Owns its own persistent files (`InstanceFile`, `RunsFile`, `KindFile`, `legacyInstanceFile`, `restore.go:24-40`), its own state struct `restoreState` (`app.go:156`, `restore.go:56-63`), single-flight goroutine (`restore.go:113-163`), and startup/reload logic (`firstRestore`, `dropOtherKind`, `runsStep`).
   - **Measured coupling to `proj`/`App`:** Only 4 narrow touchpoints: `p.terminal` (`restore.go:119`), `p.lastBefore` (`restore.go:398`), `p.paused(now)` (`restore.go:195`), and `a.obs` (`restore.go:323`). Everything else is `port.Terminal`, `port.Restorer`, `config.Config`, and filesystem state.
   - **Interface:** `type Restorer struct` embedded or owned per-project on `proj`, taking a narrow callback/deps struct (`Root`, `Terminal`, `LastActivity`, `Paused`, `Agents`, `Obs`). Moves `restore_test.go`, `restorehc_ext_test.go`, and `restore_integration_test.go` out of `internal/app`'s monolithic test binary.

3. **`internal/app/activity` (`activity.go`, 189L)**
   - **Why it is a real seam:** `activity.go` has **zero references** to `App`, `proj`, `mtask`, or `event`. It only inspects transcript mtimes, process tables (`port.Proc`), and pane metadata (`port.Pane`) for an `agents.Kind`. It is already behind an interface (`AgentActivity`, `app.go:75`).
   - **Payoff:** Pure leaf package; drops `activity_test.go` out of `internal/app` and makes the adapter boundary explicit.

4. **`internal/app/watchdog` (`watchdog.go` 374L + `lanescan.go` 123L + `watch`/`quietTimer` at `app.go:322-345`)**
   - **Why it is a real seam:** `lanescan.go` (`laneScan`, `p.scan`, `p.badLanes`) is self-contained lane-health caching. `watchdog.go`'s `quietTimer.sample` (`watchdog.go:200-237`), `judge` (`watchdog.go:247-325`), and `watch` map (`app.go:322-345`) form the stall/blocked/exit detector — conceptually parallel to `outbox.Outbox` and `identity.Registry`.
   - **Caveat (why this is Wave 3, not Wave 1):** `p.tasks` (`map[string]*watch`) is currently read/mutated by 8 files (`watchdog.go`, `stopped.go`, `stopwake.go`, `resume.go`, `fetch.go:463`, `project.go:33`, `status.go`, `loop.go`). Extracting `watchdog` requires putting `watch` behind methods (`SetLane`, `NoteResumed`, `ResetQuiet`, `TaskWatch`) — clean and worth doing once the single `commit` path and `stopped`/`stopwake` cleanup are done, not before.

#### B. File Splits & Consolidations Inside `internal/app` (No New Packages)

These files have high navigation/token cost because they bundle unrelated concerns, yet they share `proj`/`mtask`/`a.mu`:

1. **Split `messages.go` (1,172L — largest file in `internal/app`) into 4 focused files:**
   - `briefs.go` (~260L): brief lifecycle (`sawWork`, `logWorked`, `noteSeen`, `gone`, `briefDone`, `briefStatus`, `briefStage`, `recordBrief`, `briefCall` — `messages.go:148-187, 319-393, 1014-1133`).
   - `deliver.go` (~360L): delivery loop & routing (`deliver`, `closeSettled`, `route`, `routeOf`, `freshRoute`, `deliverLine`, `freshStop`, `deliverLead`, `leadPost`, `claimLines`, `membersSent` — `messages.go:195-311, 403-752`).
   - `needs.go` (~170L): needs-you rule & repeat logic (`msgNeedsYou`, `needsRule`, `needsToast`, `needsYou`, `needsLine` — `messages.go:756-913`; `NeedsText` moves to `describe`).
   - `messages.go` (~180L): outbox fold glue (`foldMsg`, `settles`, `unsettledAnswer`, `oldLineOf` — `messages.go:83-143, 918-931`) + `sendCall` (`messages.go:955-1004`).
2. **Split `status.go` (628L) into 2 files:**
   - `status.go` (~330L): `App.Status()` snapshot assembly (`status.go:22-328, 606-628`).
   - `socket.go` (~280L): `App.Listen`, `handlers()`, `noteSkew`, `skewNote`, `startedCall` (`status.go:330-604`). Today an agent looking for the socket RPC router has to know it lives halfway down `status.go`.
3. **Split `fetch.go` (596L) into 2 files:**
   - `fetch.go` (~475L): `job_next`, `restartJob`, `fetchText`, `task_done`, `ask` (`fetch.go:39-476`).
   - `report.go` (~120L): pure worker-report markdown parser (`blockedAsk`, `reportRefusal`, `MissingSections`, `reportSection`, `openQuestions`, `clip` — `fetch.go:480-596`).
4. **Untangle `workerstart.go` (400L):**
   - Lines `19-75` are `workerStartRule` (rule tier); lines `77-400` are the merge-loop's `stDispatched` step executors (`nextDispatch`, `prepare`, `nextStart`, `waitPane`, `placeLane`, `paneIn`, `startWorker`, `startSeen`). Moving `workerStartRule` into `rules.go` (or keeping `dispatch_steps.go` clearly named beside `dispatch.go`) separates rule-tier delivery from kernel step execution.
5. **Fold 8 micro-files (<45L each) into their single callers/owners:**
   - `effects.go` (9L, just `JobID`) → `jobs.go`
   - `finish.go` (14L, 2 methods on `mtask`) → `mtask.go`
   - `fold.go` (38L, `proj.fold` for watchdog facts) → `project.go` (right next to `foldRecord`)
   - `openwork.go` (36L, `proj.openWork`) → `status.go` or `project.go`
   - `toast.go` (26L) → `needs.go`
   - `exit.go` (27L) → `serve.go`
   - `log.go` (29L) → `app.go`
   - `cfg.go` (41L) → `project.go`
   - *(Keep `verbs.go` (30L) only if expanded into the shared handler preamble in Q2; otherwise fold into `socket.go`.)*

#### C. Keep Together in `internal/app`

- **The merge-loop core (`app.go`, `project.go`, `serve.go`, `states.go`, `block.go`, `loop.go`, `mtask.go`, `steps.go`, `scheduler.go`, `stopstep.go`, `dispatch.go`, `verdict.go`, `resume.go`, `drive.go`, `fetch.go`, `jobs.go`, `rules.go`):** 38 files share `folded.loop`, `mtask`, `a.mu`, `a.record`, and `p.foldRecord`. Splitting any slice of this into a separate package adds an exported surface larger than the code it hides.

---

### Q2. Duplications and Double Owners Today

#### 1. Five separate append-and-fold paths with divergent side effects *(highest bug risk)*
Every log write should obey one invariant: append → fold → sync `lanes` table → close finished task logs → clear cut-wait → settle ended intents → notify hooks. Instead, `internal/app` has **5 distinct implementations**:

| Path | Location | Calls `foldRecord`? | Calls `syncLanes`? | Calls `endTaskLogs` / `cutEnd`? | Calls `settleEnded`? | Fires `hookAppended`? |
|---|---|---:|---:|---:|---:|---:|
| `recordIn` | `loop.go:395-418` | Yes | Yes (via `foldRecord`) | Yes (`cutEndFact`) | Yes | Yes |
| `do` / `doBegin` | `loop.go:450-519` | Yes | Yes (via `foldRecord`) | Yes (`cutEnd`) | No | No |
| `fact` | `watchdog.go:361-374` | **No (`p.fold` only)** | **No** | No | No | No |
| `turnStop` | `stopped.go:263-270` | Yes | Yes (via `foldRecord`) | No | No | No |
| `verdictRecord` | `verdict.go:233-246` | Yes | Yes (via `foldRecord`) | No | Yes | Yes |
| `settleEnded` | `settle.go:47-52` | Yes | Yes (via `foldRecord`) | No | N/A | No |

**Live bug caused by this split:** `ask()` (`fetch.go:456-473`) records `needs-you{asks: …}` via `a.fact()` (`fetch.go:468` → `watchdog.go:361-374`), then returns `askedReply` ("stop and wait for the answer") so the worker immediately exits into its Stop hook. Because `a.fact()` calls `p.fold(r)` instead of `p.foldRecord(r)`, `p.syncLanes()` is **not** called (`a.wake()` at `fetch.go:471` triggers `advance()`, not `watchdog.go:61`'s `check()`). Until the next `check()` tick or unrelated `foldRecord`, the SQLite `lanes` table still has `blocked = 0` (`lanerows.go:124`) even though `p.asks(task)` is already true in memory.

**Fix:** One `func (a *App) commit(p *proj, r event.Record, opts ...)` (plus `commitResult` for `Begin`/`Finish`) that always runs `foldRecord` (and therefore `syncLanes`), `endTaskLogs`, `cutEnd`, `settleEnded`, and `hookAppended` in one place under `a.mu`.

#### 2. Duplicated RPC handler preamble, call-id idempotency, and inconsistent locking
- **Call-id idempotency has 3 mechanisms across 18+ handlers:**
  - `p.folded.calls` populated by **10 separate fold branches** (`verdict.go:63-120`, `messages.go:95`, `review.go:100`, `idle.go:76`, `mergewatch.go:53`, `pause.go:29`, `configset.go:216`, `specs.go:134`) and checked via `callProj` (`verdict.go:462`, 7 callers) or inline `p.folded.calls[verdictStep(...)]` (8 callers).
  - In-memory `p.replies` map used by 2 handlers (`fetch.go:75,135`, `leadlines.go:91,129`).
- **Rule J1 violation (`app.go:113-116`):** The comment at `app.go:113` states *"a handler checks, appends, folds and replies in one mu section (`recordLocked`)"*, yet `recordLocked` is called at only **6 sites** while `a.record` (which releases `a.mu` between check and append) is called at **58 sites**. To compensate, `App` has accumulated **4 coarse locks** (`mu`, `verdicts`, `submitMu`, `configMu`, `app.go:118-137`) plus **4 live claim maps** (`held`, `ended`, `rv.what`, `Line.Sending`) plus `admitOps` (`app.go:125`) plus a `scratch` channel (`app.go:220`).
- **Role admission boilerplate:** `leadOr` (`verbs.go:14-30`) handles the 5 `roles.Verbs` methods, while 10 other lead/owner-only handlers repeat the same `c.Role != "" && c.Role != "lead"` check and error formatting (`configset.go:57`, `drive.go:139`, `exit.go`, `gitprune.go:40`, `idle.go:286`, `pause.go:118`, `resume.go:127,178`, `review.go:175`, `reviewclose.go:63`, `verdict.go:406`). A small `ownerOrLead(verb, c)` helper removes 10 hand-rolled copies without conflating custom `roles.Verbs`.

#### 3. Overlapping "active task / lane" and "role agent" resolvers
- **Task ↔ lane ownership has 5 overlapping lookups:** `p.lanes` (cached scan, `app.go:175`), `p.folded.lr.rows` (DB projection, `lanerows.go:22`), `p.folded.loop` (merge loop, `app.go:275`), `p.tasks` (watchdog open tasks, `app.go:174`), plus `a.held` (`lanepick.go:17`). Callers switch between `p.laneHolds` (`configset.go:222`), `p.laneTask` (`lanerows.go:175`), `p.workerTask` (`stopped.go:382`), and `p.keptTask` (`project.go:277`) with subtle differences on ended/kept/driving tasks.
- **Role-agent enumeration has 4 overlapping sets:** `p.briefAgents` (`idle.go:203-221`), `p.wtAgents` (`roleworktree.go:31-59`), `p.roleAgent`/`roleAgentOf`/`roleAgents`/`roleOf` (`rolestop.go:186-231`), and `namedAgents` (`registry.go:147-155`).
- **Worker vs role-agent stop/wake split:** `stopped.go` and `stopwake.go` maintain parallel tracks (`turnStop` vs `roleTurns`, `stopWakes` vs `roleWakes`), and alarms fire differently: worker alarms call `a.fact` directly inside `stopWakes` (`stopwake.go:92`), whereas role-agent alarms go through `stopAlarmRule` in `rules.go:146`.

#### 4. Double owner of `reviewverify` state and `roledone` reminders
- `reviewverify.go:122,156` mutates live `p.fixes` and `p.verifyDue` **inside `verifyRule`'s `when()` predicate** — violating `rules.go`'s separation between pure check and action, and making `status.go` (`reviewOpen`), `idle.go`, and `openwork.go` depend on when `rulesPass` last ran.
- `messages.go:203,224-231` runs `logWorked`, `roleTurns`, `doneRoles`, `roleWakes`, and `msgNeedsYou` inside `deliver()` instead of letting `rules.go` own reminder/alarm firing.

#### 5. Small duplicated utilities scattered across files
- **Short SHA (8 copies):** `short` (`configset.go:206`), `sha7` (`nightly.go:254`), plus 6 inline `[:min(7, len(...))]` (`describe.go:140`, `idle.go:264`, `jobstart.go:276`, `mergewatch.go:129,131`, `review.go:621`).
- **Whitespace normalization (9 copies):** `oneLine` (`idle.go:268`) vs 8 inline `strings.Join(strings.Fields(...), " ")` (`drive.go:175,271,416`, `mergewatch.go:105,107,213`, `review.go:259`, `reviewclose.go:59`).
- **Caller fallback (7 copies):** `callerOf` (`dispatch.go:389`) vs 6 inline `by := c.Agent; if by == "" { by = "owner" }` (`configset.go:131`, `idle.go:301`, `mergewatch.go:209`, `messages.go:1043`, `review.go:199`, `specs.go:66`).

---

### Q3. The State Machine: Does `states.go` + Fold Hold Up?

**Verdict:** The core architecture (`states.go` table + `block.go` + `rules.go` + append-only replay) **holds up well and should be kept**. The 2–4 review findings per new feature do not come from `states.go` being the wrong pattern; they come from **four places where new features bypassed or overloaded the table**:

1. **Hidden sub-machines packed into one `states.go` row (`stDispatched` and `stDropping`):**
   - `stDispatched` (`states.go:93`) actually encodes a **5-stage sequential pipeline** inside `workerstart.go:81-105` via combinations of boolean/string fields on `mtask` (`halt`, `refused`, `base`, `kept`, `started`, `work`). Those flag combinations leak into 6 files: `fetch.go:391`, `move.go:61`, `dispatch.go:539`, `mtask.go:372-377`, `keeplane.go:194`, `workerstart.go:52`.
   - `halt` (`stopstep.go:27-81`) is a 5-field nested sub-machine checked by hand across `workerstart.go:85`, `stopstep.go:155`, `states.go:264`, `mtask.go:366`, `verdict.go:360`, and `status.go:99`.
   - `stDropping` similarly packs 3 sub-phases (`halt`, `refused`, `freed`) into `pickDropping` (`stopstep.go:154-170`).
   - **What makes adding a step safer:** Keep `states.go` as the single transition table, and either split `stDispatched` into explicit sub-phase helpers with a single `dispatchStage()` enum (derived, never stored) instead of scattered `base == "" && started == ""` checks, or give `halt` a single `mt.halting()` / `mt.pickHalt()` entry point called from `pick()` (`states.go`) so `nextDispatch` and `pickDropping` stop re-implementing halt checks.

2. **Adding a kernel step currently edits 8–9 files:**
   - `event/steps.go` (step constant)
   - `loop.go` (JSON payload struct + `mtask` fields)
   - `mtask.go` (`foldLoop` 240-line giant switch + `resumed()` ad-hoc resets)
   - `states.go` (`states` table + hardcoded step list in `busy()` at `:270` + `stepPhases` at `:225`)
   - `status.go` (`commandStep` at `:190` + `Status` switch at `:81-118`)
   - `describe.go` (`worded` table)
   - `settle.go` (`afterSteps`)
   - `follow.go` (`restartWork`/`workName`)
   - **High-ROI improvement:** Put step metadata (`stepMeta`: `phase`, `command`, `settlesOnEnd`, `busyWhileRunning`, `resetsOnResume`) in **one table** beside `states` in `states.go` (or a `stepSpec` map keyed by `step`). Then `busy()`, `stepPhases`, `commandStep`, and `settle.afterSteps` all read that single table, and a single unit test asserts every `event.Step*` kernel step has a row. That turns "forgot to add the new step to `commandStep` or `afterSteps`" from a review finding into a compile/test failure.

3. **`advance()` (`loop.go:333-371`) and `deliver()` (`messages.go:195-289`) still run 7+ ad-hoc ticks outside `rules.go`:**
   - `advance()` runs `rulesPass`, then manually calls `deliver`, `stopWakes`, `diskTick`, `versionTick`, `nightly`, `reviewCloseRead`, `reviewPass`, `reviewDueTick`.
   - `deliver()` embeds `logWorked`, `roleTurns`, `doneRoles`, `roleWakes`, `msgNeedsYou`.
   - Moving the periodic/reminder checks (`doneRoles`, worker `stopWakes` alarm, `reviewDueTick`) into `rules.go` (which already handles `stopAlarmRule`, `needsRule`, `verifyRule`, `workerStartRule`) restores one mental model: *kernel steps run via `states.go`, non-kernel reactions run via `rules.go`, and `deliver()` only delivers queued outbox lines*.

4. **SM-03 (test record injection bypassing `App.do`)** — addressed in Q4 below and directly reduced by unifying the 5 prod append paths.

---

### Q4. Tests: Cutting the Cost of 49,282 Lines Without Losing Coverage

#### Measured Facts (`go test -tags integration -json ./internal/app`, 2026-11-25)
- **801 non-socket tests passed in 13.62 s wall** (`25.52 s` cumulative across cores; `tests/budget.json` cap at 2 cores is `34.2 s`, `0` git procs). *(Note: 17 unix-socket tests in `*_integration_test.go` failed immediately in the sandbox due to `net.Listen("unix", ...)` restriction; see §3.)*
- **Top runtime outliers:**
  - `TestDriveModelHonoured` (`drive_ext_test.go:117`): **1.29 s**
  - `TestTellLeadRefusedWhileTheDriveIsDispatched` (`drive_ext_test.go:542`): **0.64 s**
  - Next 59 tests (`0.10–0.45 s`): **8.84 s** combined
  - Remaining 757 tests (`<0.10 s`): **14.75 s** combined
- **Why the top 2 tests are slow (verified):** `drive.go:91-111` (`laneReady`) uses `time.Now()` and `time.After(25 * time.Millisecond)` with default `DriveWait = time.Minute`. When `holdPrepare()` holds prepare in `synctest`, each `r.drive()` burns **2,400 fake-clock wakeups** (4,800 in `TestDriveModelHonoured`, which calls `r.drive` twice). Replacing `25 ms` polling with `p.log.Changed()` (`event/log.go:134`) + `a.opt.Now()` removes ~1.9 s of CPU immediately and eliminates the only `time.Now()`/`time.After` polling bypass in `internal/app` (`drive.go:93,102,108`).
- **Why the other 799 tests cost ~23.6 s cumulative:** Every single test calls `store.OpenRW(event.DBFile(root))` (`project.go:80`, `rig_prompt_test.go:170`), which opens a real temp-dir SQLite file with `PRAGMA synchronous=FULL` + `PRAGMA journal_mode=WAL` (`internal/store/store.go:44,54`). That runs real disk `fsync` on schema creation + every event append + every `syncLanes` transaction across ~800 tests.
  - **Hypothesis to measure (not yet benchmarked):** Allowing `Options.OpenDB` (or a test-only `store.OpenMemory` / `synchronous=OFF` pragma used only by non-integration unit/bubble tests, while `internal/store` + `*_integration_test.go` keep `OpenRW` with `synchronous=FULL` + WAL) could cut `internal/app` test time substantially without weakening crash/WAL coverage. Measure first in a 10-line spike before adopting.

#### Where the 49,282 Lines of Maintenance & Token Cost Come From

| Source | Count / Lines | Concrete Evidence |
|---|---:|---|
| Rig scaffolding (`rig_*.go`) | 13 files · **8,656 L** | 13 rig constructors & 54 fake/rig/spy structs: `loopRig` (3,282L across 4 ext files), `harness` (`rig_harness_test.go` 1,103L), `concRig`/`admitRig` (`rig_conc_test.go` 784L), `rig` (`rig_observe_ext_test.go` 763L), `reviewRig` (`rig_review_test.go` 703L), plus `rig_export_test.go` (218L exposing ~20 internals to `package app_test`) |
| Paired `foo_test.go` + `foo_ext_test.go` | **30 pairs · 25,211 L** | Half of all test code is split across both `package app` and `package app_test` for the exact same feature: `messages` (2,068+740=2,808L), `loop` (429+1,425=1,854L), `verdict` (1,258+437=1,695L), `resume` (1,013+402=1,415L), `fetch` (856+452=1,308L), `dispatch` (36+1,042=1,078L) |
| Direct record injections (SM-03) | **48 files · 252 sites** | `addRecord(`: 118 · `.record(`: 99 · `.Append(`: 32 · `recordLocked(`: 3 — bypassing `App.do` |

#### What Shape Cuts Cost Without Losing Coverage

1. **Collapse the 5 parallel full-`App` rigs down to 2:**
   - **One `loopRig` (external `package app_test`)** for end-to-end flow tests (merge loop, dispatch, verdicts, messages, review, concurrency). Absorb `rig_review_test.go` (703L), `rig_conc_test.go` (784L), `rig_observe_ext_test.go` (763L), and the full-app half of `rig_harness_test.go` (1,103L) into options on `loopRig` — deleting those rigs as their tests migrate (no double rigs).
   - **Zero-rig pure table tests (in-package `package app`)** for pure functions, fold rules, `states.go` transitions, `block.go`, report parsing, and rule predicates — constructing a `&proj{folded: newFolded()}` directly without SQLite, sockets, or goroutines.
   - **Result:** For any feature, tests live in **one** predictable place (pure rule table in `foo_test.go` OR flow test in `foo_ext_test.go`), not duplicated across both with two different rigs.
2. **Close SM-03 cleanly (two moves, not rewriting 252 call sites):**
   - First, unifying the 5 prod append paths into `commit` means `a.record` and `App.do` share the exact same fold/side-effect pipeline.
   - Second, add a dedicated `do_contract_test.go` (~150L) that runs every kernel step through the real `App.do` → `doBegin` → `effect` → `Finish` → `commit` path (including crash between `Begin` and `Finish`, retry, and `settleEnded`), and ban new `addRecord` injections for effect results in `*_ext_test.go` (keep `addRecord` only for *precondition setup* of fold/replay unit tests).
3. **Subpackage extraction shrinks `check-affected` blast radius:**
   - Moving `describe`, `restore`, `activity`, and `watchdog` out of `internal/app` moves their ~4,000+ lines of tests into their own packages, so editing `restore` or `view` no longer recompiles and runs all 800 `internal/app` tests.

---

### Q5. Order: What First, and What NOT to Do

#### Priority Order (Weighted by Navigation, Errors, and Agent Token Spend)

1. **Wave 1 — Stop the bleeding (low risk, immediate bug + speed + boundary wins):**
   - Unify the 5 append-and-fold paths into one `commit` helper (fixes the live `ask()` → `syncLanes()` race immediately, shrinks SM-03 surface).
   - Fix `drive.go:91-111` (`laneReady` → `p.log.Changed()` + `a.opt.Now()`) — cuts 1.9 s from `go test ./internal/app`.
   - Extract `internal/app/describe` (move 7 payload structs to `event/data.go`, move `describe.go` + `NeedsText`) — severs `internal/view → internal/app` and enforces it in `boundary_test.go`.
2. **Wave 2 — Cut navigation & token cost inside `internal/app` (pure file moves, zero behaviour change):**
   - Split `messages.go` → `messages.go`, `briefs.go`, `deliver.go`, `needs.go`; split `status.go` → `status.go`, `socket.go`; split `fetch.go` → `fetch.go`, `report.go`; move `workerStartRule` out of `workerstart.go`.
   - Fold the 8 micro-files (`effects.go`, `finish.go`, `fold.go`, `openwork.go`, `toast.go`, `exit.go`, `log.go`, `cfg.go`) into their owners.
   - Deduplicate `short`/`sha7`, `oneLine`, `callerOf`, and `ownerOrLead` (delete all inline copies in the same task).
3. **Wave 3 — Extract the 3 remaining true subpackages:**
   - `internal/app/activity` (`activity.go`, trivial zero-coupling leaf).
   - `internal/app/restore` (`restore.go` + tests).
   - `internal/app/watchdog` (`watchdog.go` + `lanescan.go` + `watch`/`quietTimer`, after encapsulating `p.tasks` accessors).
4. **Wave 4 — Tighten the state machine & `rules.go`:**
   - Single `stepMeta` table in `states.go` driving `busy()`, `stepPhases`, `commandStep`, `afterSteps` + exhaustiveness test.
   - Make `verifyRule` pure (move `p.fixes`/`p.verifyDue` update out of `when()`), move `doneRoles` / worker stop alarm / `reviewDueTick` into `rules.go`.
   - Give `stDispatched` a derived `dispatchStage()` enum and unify `halt` handling in `states.go` (`pick()`).
   - Consolidate RPC call-id idempotency preamble & audit the 58 `a.record` vs `recordLocked` sites against rule J1.
5. **Wave 5 — Test rig consolidation & SM-03 closure (opportunistic or dedicated follow-up):**
   - Add `do_contract_test.go` to close SM-03.
   - Benchmark SQLite `synchronous=OFF` / `:memory:` for unit tests; adopt only if measured savings are clear (e.g. >40% wall-time cut).
   - Retire `rig_review_test.go`, `rig_observe_ext_test.go`, `rig_conc_test.go`, and `rig_harness_test.go` one at a time by migrating their suites onto `loopRig` options (deleting each rig in the same PR as its last caller).

#### What NOT to Do At All

- **Do NOT extract `loop/`, `dispatch/`, `verdict/`, `messages/`, or `jobs/` into subpackages.** They all mutate `folded.loop` (`mtask`) and `a.mu`; turning them into packages creates an exported `mtask` God-API or an artificial event bus that costs more tokens to read than today's flat package.
- **Do NOT rewrite `states.go` into an OOP state pattern (one struct/interface per state).** The flat table in `states.go` is already the easiest file in `internal/app` to audit top-to-bottom; adding `stepMeta` and extracting `dispatchStage()` fixes its leaks without losing table readability.
- **Do NOT do a big-bang rewrite of the 49k lines of tests.** Every test cleanup task must delete an entire obsolete rig or test file in the same diff; never build a "new v2 rig" alongside the existing 13.
- **Do NOT introduce a second implementation during any move.** Every task in §4 moves and deletes in one atomic task so there are never two ways to append, two `Describe`s, or two rigs for the same suite.
- **Do NOT chase line-count targets.** `loop.go` (897L) shrinks naturally once `commit` is extracted and data structs move to `event/data.go`, and `review.go` (649L) is cohesive once read as one topic; splitting a cohesive file just to hit an arbitrary line limit hurts navigation.

---

## 3. Risks & What Was Not Verified

### Risks & Mitigations

| Risk | Why it matters | Mitigation |
|---|---|---|
| **Unifying `a.fact` into `commit` triggers unexpected `syncLanes()` or `hookAppended` in tests** | `watchdog.go:361` currently skips `foldRecord`/`syncLanes`/`hookAppended`; some tests may assert exact hook counts or `lanes` writes. | Audit all 4 callers of `a.fact` (`fetch.go:468`, `stopped.go:255,309`, `stopwake.go:92`, `watchdog.go:155`) in Task 1.1; make `commit` always run `foldRecord` (fixing `ask()`) and gate `hookAppended` explicitly only where `recordIn`/`verdictRecord` need it. |
| **Circular imports if `describe` lands in `internal/app/event`** | `describe.go:79,83` imports `wire.MethodAccept` / `wire.MethodSendback` and `review.Data` (`review` imports `event`). | Put `describe` in its own leaf `internal/app/describe` (imports `event`, `review`, `wire`) rather than inside `event`, keeping `event` dependency-free. |
| **Lock-order regressions when auditing `recordLocked` vs `a.record`** | `App` has 4 locks (`verdicts`, `submitMu`, `configMu`, `mu`) with documented order at `app.go:118-137`. | Keep the lock audit in Wave 4 as a narrow, dedicated task with `-race` verification (`make check-race` / nightly) and `rig_conc_test.go` coverage intact. |
| **Token churn during file splits if done alongside feature work** | Moving 4 large files while other lanes edit `messages.go` or `status.go` causes merge conflicts. | Run Wave 2 as a clean, solo consolidation batch with zero parallel feature lanes touching `internal/app` (matches BACKLOG: *"consolidate before new features"*). |
| **Weakening test durability guarantees if SQLite pragmas change** | Switching unit tests to `:memory:` or `synchronous=OFF` could mask SQLite lock/WAL bugs. | Keep `store.OpenRW` (`synchronous=FULL`, WAL) unchanged in production, `internal/store`, and all `*_integration_test.go` (including `TestRigStoreLockDuringAServeWrite`). Only consider a test-mode pragma after a quick benchmark proves >40% wall-time gain. |

### Explicitly Not Verified

- **17 unix-socket integration tests (`*_integration_test.go`):** Sandbox blocks `net.Listen("unix", ...)`, so `wire_integration_test.go`, `restore_integration_test.go`, `status_integration_test.go`, `loop_integration_test.go`, `messages_integration_test.go`, and `verbs_integration_test.go` failed immediately on bind. All 801 unit/bubble tests ran and passed; timings above are from those 801 tests.
- **SQLite test-speedup magnitude:** We verified every unit test calls `store.OpenRW` with `synchronous=FULL` + WAL (`project.go:80`, `internal/store/store.go:44`), but did **not** benchmark `synchronous=OFF` or `:memory:` — treat the speedup as a hypothesis to measure in Wave 5 before changing anything.
- **Full `make check` / `-race` runtime:** Only `go test -tags integration -json ./internal/app` was run (per role rules: read-only, no load runs).

---

## 4. Concrete Plan (Ordered Task Breakdown)

Each task is atomic, leaves zero double implementations, updates `docs/module-map.md` and `internal/checks/boundary_test.go` in the same diff where packages/files change, and gates on `make check-affected`.

### Wave 1 — Bug Fix, Fast Win & View Boundary Break

1. **T-W1a: Unify event append-and-fold (`commit`)**
   - **Files:** `internal/app/loop.go` (`recordIn`, `do`, `doBegin`), `internal/app/watchdog.go` (`fact`), `internal/app/stopped.go` (`turnStop`), `internal/app/verdict.go` (`verdictRecord`), `internal/app/settle.go` (`settleEnded`), `internal/app/fetch.go` (`ask`).
   - **Change:** Introduce one `a.commit` (and `a.commitLocked`) path that appends, calls `p.foldRecord(r)` (always keeping `lanes` table synced), runs `endTaskLogs`, `cutEnd`, and `settleEnded` consistently. Delete the divergent fold/append bodies in `fact`, `turnStop`, `verdictRecord`, and `settleEnded`.
   - **Test:** Add a unit/flow test proving `task_done` with `Blocked:` (`ask`) immediately updates `lanes.blocked = 1` in SQLite before any `check()` tick runs.

2. **T-W1b: Replace `laneReady` 25 ms polling with `p.log.Changed()`**
   - **Files:** `internal/app/drive.go:91-111`.
   - **Change:** Wait on `p.log.Changed()` + a single deadline timer using `a.opt.Now()` instead of `time.Now()` + `time.After(25 * time.Millisecond)`.
   - **Done when:** `TestDriveModelHonoured` and `TestTellLeadRefusedWhileTheDriveIsDispatched` drop from 1.29 s / 0.64 s to <0.15 s; zero `time.Now()`/`time.After(25ms)` polling remains in `drive.go`.

3. **T-W1c: Extract `internal/app/describe` and sever `internal/view → internal/app`**
   - **Files:**
     - Move JSON payload structs (`gateOut`, `testOut`, `verdictResult`, `dispatchData`, `submitData`, `noteData`, `versionData`) → `internal/app/event/data.go` (export them, delete private copies in `loop.go`, `verdict.go`, `dispatch.go`, `versions.go`).
     - Move `internal/app/describe.go` + `describe_test.go` + `NeedsText` (`messages.go:861-874`) → `internal/app/describe/`.
     - Delete `type ReviewData = review.Data` alias in `review.go:53`.
     - Update 5 callers in `internal/view/` (`detail.go`, `events.go`, `setup.go`, `tasklist.go`, `view.go`) to import `internal/app/describe` and `internal/app/review`.
     - Update `internal/checks/boundary_test.go` to forbid `internal/view` from importing `internal/app` directly (allowing `internal/app/*` leaves), and update `docs/module-map.md`.

### Wave 2 — File Reshaping & Helper Deduplication Inside `internal/app`

4. **T-W2a: Split `messages.go` (1,172L) and fold `toast.go`**
   - **Files:** `internal/app/messages.go` → `messages.go` (fold + `sendCall`), `briefs.go`, `deliver.go`, `needs.go` (absorbing `toast.go`, deleting `toast.go`). Update `docs/module-map.md`.
   - **Behaviour change:** None (pure move).

5. **T-W2b: Split `status.go` and `fetch.go`; untangle `workerstart.go`; fold 7 micro-files**
   - **Files:**
     - `status.go` → `status.go` (snapshot) + `socket.go` (`Listen`, `handlers`, skew, `startedCall`).
     - `fetch.go` → `fetch.go` (MCP calls) + `report.go` (pure report markdown parser).
     - Move `workerStartRule` (`workerstart.go:19-75`) → `rules.go`.
     - Fold and delete: `effects.go` → `jobs.go`; `finish.go` → `mtask.go`; `fold.go`, `openwork.go`, `cfg.go` → `project.go`; `exit.go` → `serve.go`; `log.go` → `app.go`.
     - Update `docs/module-map.md`.
   - **Behaviour change:** None (pure move).

6. **T-W2c: Deduplicate small helpers and lead/owner admission check**
   - **Files:** `configset.go`, `nightly.go`, `idle.go`, `jobstart.go`, `mergewatch.go`, `review.go`, `reviewclose.go`, `drive.go`, `dispatch.go`, `specs.go`, `messages.go`/`briefs.go`, `verbs.go`, `gitprune.go`, `pause.go`, `resume.go`, `verdict.go`.
   - **Change:** One `shortSHA` (replaces `short`, `sha7`, 6 inline slices), one `oneLine` (replaces 8 inline `strings.Join(strings.Fields(...))`), one `callerOf` (replaces 6 inline fallbacks), one `ownerOrLead` (replaces 10 repeated `c.Role != "" && c.Role != "lead"` checks).

### Wave 3 — Extract Remaining Narrow-Seam Subpackages

7. **T-W3a: Extract `internal/app/activity`**
   - **Files:** Move `internal/app/activity.go` + `activity_test.go` → `internal/app/activity/`; update `app.go` (`AgentActivity` wiring) and `docs/module-map.md`. Delete old files.

8. **T-W3b: Extract `internal/app/restore`**
   - **Files:** Move `internal/app/restore.go` + `restore_test.go` + `restorehc_ext_test.go` + `restore_integration_test.go` → `internal/app/restore/`; replace `proj.restore` (`app.go:156`) with `*restore.Restorer` constructed with a small deps struct (`Root`, `Terminal`, `LastActivity`, `Paused`, `Agents`, `Obs`). Update `docs/module-map.md` and `cmd/obor` if any exported constants (`InstanceFile`, `RunsFile`, `KindFile`) are referenced there.

9. **T-W3c: Extract `internal/app/watchdog`**
   - **Files:** Move `internal/app/watchdog.go`, `internal/app/lanescan.go`, and `watch`/`quietTimer` (`app.go:322-345`) + their unit tests → `internal/app/watchdog/`. Expose narrow methods on `*watchdog.Watchdog` for the 6 external callers (`stopped.go`, `stopwake.go`, `resume.go`, `fetch.go`, `status.go`, `project.go`). Update `docs/module-map.md`.

### Wave 4 — State Machine & Rule-Tier Tightening

10. **T-W4a: Single `stepMeta` table in `states.go`**
    - **Files:** `internal/app/states.go`, `internal/app/status.go`, `internal/app/settle.go`, `internal/app/follow.go`.
    - **Change:** Consolidate `stepPhases` (`states.go:225`), `busy()` step list (`states.go:270`), `commandStep` (`status.go:190`), and `afterSteps` (`settle.go`) into one `stepMeta` table in `states.go`, with one exhaustiveness unit test covering every kernel `event.Step*`.

11. **T-W4b: Finish the `rules.go` cleanup**
    - **Files:** `internal/app/rules.go`, `internal/app/reviewverify.go`, `internal/app/deliver.go`, `internal/app/loop.go` (`advance`), `internal/app/stopwake.go`, `internal/app/roledone.go`.
    - **Change:** Make `verifyRule.when` pure (compute `p.fixes`/`p.verifyDue` in fold or explicit pre-pass, never inside `when()`). Move `doneRoles`, worker stop-alarm firing, and `reviewDueTick` into `rules.go` so `deliver()` only delivers outbox lines.

12. **T-W4c: Explicit `dispatchStage()` and unified `halt` dispatch in `states.go`**
    - **Files:** `internal/app/states.go`, `internal/app/workerstart.go`, `internal/app/stopstep.go`, `internal/app/mtask.go`, `internal/app/fetch.go`, `internal/app/move.go`, `internal/app/keeplane.go`.
    - **Change:** Replace scattered `base == "" && started == ""` checks with a derived `mt.dispatchStage()` helper, and route `halt` picking once in `states.go`'s `pick()` instead of duplicating it in `nextDispatch` and `pickDropping`.

13. **T-W4d: Audit `a.record` vs `recordLocked` (Rule J1) and call-id preamble**
    - **Files:** `internal/app/app.go`, `internal/app/verdict.go`, `internal/app/socket.go`, handlers using `callProj` / `p.folded.calls`.
    - **Change:** Unify `callProj` + `verdictStep` lookup across all call-id handlers, fix any genuine check-then-act race where `a.record` releases `a.mu` prematurely, and document the true invariant at `app.go:113`.

### Wave 5 — Test Consolidation & SM-03 Closure

14. **T-W5a: Close SM-03 with `do_contract_test.go`**
    - **Files:** New `internal/app/do_contract_test.go`.
    - **Change:** Table-driven contract test exercising real `App.do` → `doBegin` → `effect` → `Finish` → `commit` (including crash-between-intent-and-result, retry, and `settleEnded`) for each kernel effect class (`ClassStart`, `ClassGit`, `ClassPrompt`).

15. **T-W5b: Benchmark SQLite test pragma (spike, decide before changing)**
    - **Files:** None committed until measured; benchmark `synchronous=OFF` vs current `OpenRW` on `go test ./internal/app` at 2 cores. Only pursue if it cuts wall time significantly without touching `*_integration_test.go` or `internal/store`'s production `OpenRW`.

16. **T-W5c: Retire redundant rigs one at a time (3–4 small tasks)**
    - **Order:**
      1. Migrate `rig_review_test.go` (703L) tests onto `loopRig` options; delete `rig_review_test.go`.
      2. Migrate `rig_observe_ext_test.go` (763L) tests onto `loopRig` options; delete `rig_observe_ext_test.go`.
      3. Migrate `rig_conc_test.go` (784L) tests onto `loopRig` options; delete `rig_conc_test.go`.
      4. Split `rig_harness_test.go` (1,103L) into pure no-rig fold tests + `loopRig` flow tests; delete `rig_harness_test.go`.
    - **Rule:** Each task deletes its retired rig in the same commit; trim `rig_export_test.go` as internal peeks disappear.

---

## Sync round 1: reply to Fable

*2026-11-25 · replies to `docs/research/internal-app-architecture-fable.md` · factual corrections verified against code.*

First, thank you for the rigorous fact-check — every item in your "Corrections of facts" table checks out against the code (`watchdog.go:60-64`, `fold.go:18`, `log.go:13-29`, `app.go:156`, `loop.go:263-271`, `dispatch.go:52-53`, `event/log.go:476`, `internal/web/types_test.go:17`), and your section 3 catches (especially `setLanes` clearing `folded.active` at `project.go:266-270`, `settleEnded` after reject at `verdict.go:221-223`, and `deliver` hiding nine pass jobs at `messages.go:195-289`) materially improve the plan.

### Answers to Fable's points of disagreement (1–11)

1. **Lanes gap fix alone first — Agree.**
   Verified `watchdog.go:60-64`: `decideLanes` only calls `syncLanes()` when `p.folded.lr.err != nil`, so `check()` does not close the gap — the woken pass's `needsRule` → `a.record` → `foldRecord` closes it (within 4 s in 25/25 logged cases). Fixing `fact` (`watchdog.go:371`) to call `foldRecord` plus adding the lane-row check in `TestFetchBlockedAsks` (`fetch_ext_test.go:312`) is a one-line standalone fix and should ship immediately, independent of the refactor.

2. **One optionless post-fold function vs `commit(p, r, opts...)` — Agree.**
   Re-reading `loop.go:395-503`, `verdict.go:219-245`, `resume.go:410`, `stopped.go:263-270`, and `settle.go:45-54`, no reaction genuinely needs a caller option:
   - `endTaskLogs(p, r.Task, was)` follows from `was` vs `p.taskEnded(r.Task)` (`mtask.go:291`).
   - `cutEndFact(r)` applies to `r.Kind == event.KindFact` (`loop.go:406-408`); `p.cutEnd(r.Key())` applies to `r.Kind == event.KindResult` (`loop.go:458`, a no-op when not waiting).
   - `settleEnded` follows from the state transition (`stMerged`/`stDropped`, subject to point 11), and its own appended `notRun` results (`settle.go:45-52`) do not re-trigger it.
   - `laneRowsErr()` always applies.
   - One subtle detail to verify during implementation: `a.hook(hookAppended)` (`drive.go:48`, `project_test.go:389`). In `verdictRecord` (`verdict.go:239,244`), the intent folds first without `hookAppended` and the hook fires on the result; in `TestRecordFoldsInLogOrder` (`project_test.go:231,389`), `fired.CompareAndSwap` fires once and checks `h.a.mu.TryLock()`. Firing `hookAppended` only on non-intent records (or verifying `TestRecordFoldsInLogOrder` passes when it fires on every append under `mu`) is still a pure predicate on `r.Kind`, needing zero caller options.

3. **`restore` stays in `internal/app` — Agree.**
   Verified `app.go:156` (`App.rst`, not `proj.restore`) and `restore.go` coupling (`a.opt` 22×, `a.rst` 21×, `a.mu` 13×), plus `restorehc_ext_test.go:66` and `restore_integration_test.go:64` driving full `App` rigs. Moving `restore` adds a lock-order seam without moving its tests out of `internal/app`. Dropped.

4. **`watchdog` stays in `internal/app` — Agree.**
   Unlike `outbox` and `identity` (which own self-contained folds), watchdog reads and writes `folded.active` (`p.has`, `p.asks`, `p.stops`, `p.watched`) shared with `status`, `idle`, `needsRule`, and `lanerows`, and calls `agentOf`. Extracting it would recreate the wide glue interface we both reject for `dispatch/`. Dropped.

5. **`event` imports `port` so `event/data.go` is the one schema home — Agree.**
   Verified `internal/app/port` imports only `internal/store` (`internal/app/port/lanefree.go:3`), so `event → port` introduces no cycle and fits `core → core` in `internal/checks/boundary_test.go:113-114` (just add a `TestBoundaryRules` row). Keeping all record payload structs in `event/data.go` beats splitting schema across two packages, and `describe` moving to `internal/app/describe` severs both `internal/view` (5 files) and `internal/web/types_test.go:17` from `internal/app`.

6. **Fold files by wrong name, not by size — Agree.**
   Verified `fold.go:18` is `foldStart` (belongs in `registry.go`) and `log.go:13-29` is `backup` (belongs in `prune.go`), and `<45L` was an implicit size target. Keep `toast.go`, `cfg.go`, `verbs.go` (names match contents); fold `effects.go` → `jobs.go`, `finish.go` → `mtask.go`, `fold.go` → `registry.go`, `log.go` → `prune.go`, `exit.go` → `socket.go`; keep `openwork.go` as is (or fold into `restore.go`).

7. **Only `doneRoles` moves to `rules.go`; other ticks become one ordered pass list in `advance()`; `workerStartRule` stays beside its subject — Agree.**
   Verified `rules.go:1-5`: rules have no intent, crash class, replay case, claim, or fold of their own. `diskTick`, `versionTick`, `nightly`, `reviewPass`, and `stopWakes` run effects through `do` or record facts (`stopwake.go:92`). Lifting the 9 hidden jobs out of `deliver()` (`messages.go:195-289`) into an explicit ordered list in `advance()` (`loop.go:333-371`) + moving `doneRoles` (`messages.go:223-231`) into `rules.go` fixes the flow-readability debt without distorting `rules.go`. Keeping `workerStartRule` in `workerstart.go` matches `needsRule`/`idleRule`/`stopAlarmRule`/`verifyRule`.

8. **`verifyRule`'s read becomes a named pass step, not a fold — Agree.**
   Verified `reviewverify.go:116`: `linkFixes` reads `specOf(p, task)` from SQLite outside `mu`, which a log-only fold cannot do. Making the round's read a named step in `advance()`'s ordered pass list that updates `p.fixes` and `p.verifyDue`, so `verifyRule.when` only reads them, is the right design.

9. **Tests: one fake per port + one level per topic (largest first) + rigs inject through the production post-fold function — Agree on design; minor differ on `laneReady` ordering.**
   - **Agree on routing rig injections through the production post-fold function (Task 3) + small `App.do` contract test:** That kills the root cause of `TestLaneRowsBlockedFollowsTheFold` (`lanerows_test.go:125-136`) missing the `ask()` bug, instead of leaving hand-rolled test writes beside a boundary suite.
   - **Agree on one fake per port → one level per topic (largest first):** Consolidating duplicate fakes (`fakePanes`/`term`; `concFakes`/`dispatchFakes`/`startJobFakes`) before shrinking the paired `foo_test.go`/`foo_ext_test.go` files aligns with `docs/plan/test-suite-rework.md` and avoids bloating `loopRig` with large rig-to-rig migration diffs.
   - **Minor differ on where `laneReady` (`drive.go:91-111`) sits in the order:** Fable puts `laneReady` at task 13, *after* task 12 ("Measure where the package's test time goes"). I would put `laneReady` right after Task 1 (or at least **before** Task 12's profiling): it is a ~10-line, zero-risk production fix (reply returns immediately on fold instead of polling every 25 ms on `time.Now()`), and removing its ~1.9 s of 2,400-tick noise *before* profiling makes Task 12's breakdown of DB open vs rig start vs waits cleaner.

10. **No J1 audit task and no standalone helper-dedup wave — Agree.**
    Your point on the raw `58` vs `6` count is right: `a.record` locks `a.mu` around append+fold (`loop.go:382-386`), dispatch-class verbs hold `verdicts`/`submitMu` (`app.go:118-121`), and a raw grep mixes loop/rule calls with socket verbs. If J1 ever needs checking, a read-only review question is the right tool. Dropping the 16-file helper-dedup wave avoids lane collisions; moving `ownerOrLead` into the `socket.go` admission task keeps the real flow cleanup.

11. **`settleEnded` after reject (`verdict.go:221-223`, `settle.go:25,36-40`) — Dead code today, with a trap if "fixed" naively.**
    Verified from `verdict.go:219-223`, `settle.go:25-41`, `mtask.go:270-274`, `loop.go:890-896`, and `steps.go:374`:
    - Today `verdict.go:221-223` (`if !was && p.closed(c.Task) { a.settleEnded(p, c.Task) }`) is **100% dead at runtime**: a verdict can close a task only as `stRejected`, and `settle.go:36-38` skips every state except `stDropped` and `stMerged`.
    - The comment at `verdict.go:222` (`"rejected: its cleared watchdog notifications never run"`) is historical: watchdog notifications are facts/rules now, not intents.
    - **Trap if you blindly add `case stRejected:` to `settleEnded`:** `stRejected` is explicitly *not* `ended()` (`mtask.go:270-274`: the owner still owes `obor drop`), and `pickRejected` (`loop.go:890-896`) still executes `event.StepUnpin` (`steps.go:374`) as a `ClassGit` intent! Without exempting `r.Attempt == mt.attempt && r.Step == event.StepUnpin` (mirroring `afterSteps` for `stMerged` at `settle.go:39`), a replay after crashing mid-unpin (`project.go:66`) would settle `unpin` as `not run` and leak git pins.
    - **My read:** Delete the dead call at `verdict.go:221-223` and remove `"or a reject (verdict)"` from `settle.go:25` so `settleEnded` strictly matches `stDropped` and `stMerged` (`obor drop` settles any leftover intents when the owner closes the rejected task). Only if the owner/lead wants stale pre-reject pending intents cleared from `p.log.Pending()` immediately on reject should we add `case stRejected:` **with** `event.StepUnpin` exempted. Flagged for the lead before Task 2; default recommendation is delete dead code.

---

### Path as I now see it

We have converged on nearly the entire plan. Below is the ordered task list I would sign (16 tasks, each deleting what it replaces), with the two small remaining open items explicitly marked:

**A. Now (tiny standalone fixes)**
1. **The lanes table follows every fact.** `watchdog.go:371` (`fact` calls `foldRecord`), `fetch_ext_test.go:312` (`TestFetchBlockedAsks` asserts `lanes.blocked`). — **`agreed-with-Fable`**
2. **`laneReady` waits on `p.log.Changed()` instead of 25 ms polling.** `drive.go:91-111` (uses `a.opt.Now()` + `p.log.Changed()`). Removes 2,400 fake-clock wakeups per drive and ~1.9 s of test noise before profiling. — **`still-open` (ordering only: agreed on the change; I prefer here in A so it lands clean and before Task 13's timing profile, Fable had it after Task 13)**

**B. One write path**
3. **One optionless post-fold function for every appended record.** `loop.go` (`recordIn`, `do`, `doBegin`), `verdict.go` (`verdictRecord` and caller), `resume.go` (`leadSendback`), `stopped.go` (`turnStop`), `watchdog.go` (`fact`), `settle.go`, `project.go` (`setLanes` active-clear sync). No options; resolves `settleEnded` on reject (my default: delete dead call at `verdict.go:221-223` + stale comment at `settle.go:25`). — **`agreed-with-Fable` (with point 11 lead decision `still-open`: delete dead call vs add `stRejected` exempting `StepUnpin`)**
4. **Test rigs inject through that production post-fold function + small `App.do` contract test.** `rig_harness_test.go`, `rig_loop_ext_test.go`, `rig_export_test.go`, and direct `Append`/`foldRecord` test helpers; delete hand-rolled append-and-fold helpers; add small `App.do` contract test (intent, effect, result, crash-between, retry) to close SM-03. — **`agreed-with-Fable`**

**C. Schema and view boundary**
5. **Record data types live in `event`.** `loop.go`, `dispatch.go`, `verdict.go`, `versions.go` → `event/data.go`; add `event → port` row in `internal/checks/boundary_test.go`. — **`agreed-with-Fable`**
6. **`describe` becomes a leaf; `internal/view` and `internal/web` stop importing `internal/app`.** `describe.go`, `describe_test.go`, `NeedsText`, `Age`, `Kind*` → `internal/app/describe`; delete `ReviewData` alias (`review.go:53`); update `internal/view` (5 files), `internal/web/types_test.go:17`, `boundary_test.go`, `docs/module-map.md`. Measure `make check-affected` impact before spec. — **`agreed-with-Fable`**

**D. Readable pass, then files along flows**
7. **One explicit ordered pass list in `advance()`.** `loop.go` (`advance`), `messages.go` (`deliver`): lift hidden jobs out of `deliver()` into named steps in `advance()`; `deliver()` only delivers queued lines; `doneRoles` moves into `rules.go` (`roledone.go`). — **`agreed-with-Fable`**
8. **Request entry in one file + `ownerOrLead` admission helper.** `status.go` → `status.go` (snapshot) + `socket.go` (`Listen`, `handlers`, skew, `started`); replace repeated `c.Role != "" && c.Role != "lead"` checks (`verbs.go`, `exit.go`, `pause.go`, `resume.go`, `gitprune.go`, `idle.go`, `review.go`, `reviewclose.go`, `configset.go`, `drive.go`, `verdict.go`). — **`agreed-with-Fable`**
9. **Split overloaded files along flows & fold misnamed files (pure move, solo lane).** `messages.go` → `messages.go` (fold + `sendCall`), `deliver.go`, `briefs.go`, `needs.go`; `fetch.go` → `fetch.go` + `report.go` (pure parser + table tests); fold `effects.go` → `jobs.go`, `finish.go` → `mtask.go`, `fold.go` → `registry.go`, `log.go` → `prune.go`, `exit.go` → `socket.go` (keep `toast.go`, `cfg.go`, `verbs.go`, `openwork.go`). Update `docs/module-map.md`. — **`agreed-with-Fable`**

**E. State machine leaks**
10. **One derived dispatch stage + unified `halt` entry point.** `workerstart.go`, `fetch.go`, `move.go`, `dispatch.go`, `mtask.go`, `keeplane.go`, `stopstep.go` (plus decide the 10 step-counter candidate on `mtask.go:137-236` here). — **`agreed-with-Fable`**
11. **`verifyRule` only reads.** `reviewverify.go`, `loop.go`: move `linkFixes`/`specOf` read into a named step in `advance()`'s pass list that writes `p.fixes` and `p.verifyDue`. — **`agreed-with-Fable`**
12. **One step table.** `states.go`, `status.go`: unify `busy`'s step list, `stepPhases`, and `commandStep` into one table with an exhaustiveness test covering every command step. — **`agreed-with-Fable`**

**F. Tests (next phase of `docs/plan/test-suite-rework.md`)**
13. **Profile where `internal/app` test time goes (no code).** Split runtime into SQLite open/fsync, rig startup, and waits to decide whether a faster store option for bubble tests warrants an owner decision. — **`agreed-with-Fable`**
14. **One fake per port (one task per port).** Merge duplicate port fakes across `rig_harness_test.go`, `rig_observe_ext_test.go`, `rig_conc_test.go`, `rig_dispatch_ext_test.go`, `rig_workflow_test.go` and delete replaced fakes in the same task. — **`agreed-with-Fable`**
15. **One level per behaviour, per topic (largest pairs first).** Work through `messages`, `loop`, `verdict`, `resume`, `fetch`, `dispatch`: pure decisions as zero-`App` table tests, flows on `loopRig`, delete duplicates and shrink `rig_export_test.go`. — **`agreed-with-Fable`**

**G. Optional, last**
16. **Extract `internal/app/activity` leaf.** Moves default `AgentActivity` implementation out of core; clean boundary fix, low urgency. — **`agreed-with-Fable`**

**Agreed NOT on the path:** `restore` and `watchdog` subpackages, splitting the merge-loop core into feature packages, a J1 audit build task, a standalone helper-dedup wave, and any new v2 rig beside existing ones.
