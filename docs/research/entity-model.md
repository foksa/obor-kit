# Entity model: second pass after `data-model.md` (11-05)

Brief: lead 11-08 (owner decision 11-08: data and architecture passes are a standing part of the process). Survey only; no code changed. Baseline `9d2d8131` (staging). Companion: `docs/research/architecture-inventory.md` (axes, git-process measurement).

Conventions: **Fact** = read from code or measured. **Opinion** = marked. `file:line` at the baseline.

## Answer first

1. **The 11-05 data-model work was done almost completely** (T-883, T-885, T-887, T-888, T-889, T-891, T-896; re-checked one by one in §"Re-check"). The mirrors it named are gone: one `view.Task` (`view/task.go:15`), one `port.Pane` (`port/port.go:17`), one `store.Session` embedded by `guard.Began`, `view.AgentSession` and carried as `*store.Session` in `wire.Call` (`call.go:150`), `port.Dispatch` embedded by the dispatch fact and both lane intents (`effects.go:~402`), `view.Lane` embedding `port.Lane`, a Go test tying `types.ts` to the Go structs (`internal/web/types_test.go`).
2. **What remains is different in kind**: not copies of a struct, but **entities with no struct** (Job, Step, Review, Spec-as-parsed-text, the "current task of a lane"), **entities spread over stores of different kinds** (Review lives in the event log, a git commit message, a review file, `BACKLOG.md` and a spec line), and **union structs** (`wire.Call` 33 fields, `event.Message` 15, `event.FactData` 18) that act as several entities.
3. **Measured spread** (type-aware `go/types` pass over production + tests; method in §Method): the Task entity's data is `mtask` + `attemptVars` + `dispatchData` + `port.Dispatch` + `wire.TaskStatus` + `view.Task`: **110 fields, 227 production write sites in 11 files, 836 production read sites, 43 production files in 5 packages, and 1,290 test sites**. Its state field is written in **4 files** (`mtask.go` 22 sites, `states.go` 3, `dispatch.go` 2, `stopstep.go` 1): that part is already near one place. The Lane data (`LaneRow`, `port.Lane`, `view.Lane`): 28 fields, 92 write / 278 read sites, **40 production files in 11 packages**. Agent/Session/Pane data (`store.Session`, `port.Pane`, `wire.AgentStatus`, `roles.Role`, `config.AgentInfo`): 51 fields, 157 write / 528 read sites, **59 files in 12 packages**.
4. **Identity strings repeat across types**: 45 production struct types carry a task id field, 36 a lane, 49 an agent (go/ast count, §Method). Most are references by id (`Record.Task`, `Call.Task`), which is fine; the problem cases are where the type also *copies facts* about the entity (listed per entity below).
5. **Cost-ranked recommendation of where encapsulation pays** (opinion, from the measured sites in §Targets): (a) Lane: one "is this lane free / whose task is it" rule instead of 6 readers; (b) Review: make the log fact the source and the git commit an output; (c) Step/Job: one counter map and one `Job` value instead of 8 counters and ~10 loose `attemptVars` fields; (d) Spec: parse once. Agent/Session and Task need little more than the guard rail "writes go through the fold".

## Method and limits

- **Field references**: a program (scratchpad `refs/main.go`, stdlib `go/types` over `go list -export -deps -test -json` export data) type-checks every package with its in-package tests and records each selector and composite-literal key that resolves to a field of the listed struct types. A site is a *write* when it is the left side of `=`, `+=`, `++`, a keyed composite-literal field, or `&x.F`; everything else is a *read*. Counts include `internal/testkit` and `internal/herdr/herdrtest` as production (they are non-`_test.go` files). External `_test` packages are checked where `go list` reported them. Cross-package promoted fields (embedded structs) resolve to the embedded type's field, so `mt.gates` counts under `attemptVars`. Not covered: reflection/JSON (a field read only through `json.Marshal` shows as zero reads), `tests/conformance`, and `cmd/obor` calls through function values.
- **Type counts**: `go/ast` over non-test, non-`tests/` Go files, struct types with a field named (case-insensitively) `task`, `lane`, `agent`.
- A first attempt with a compile-and-rename probe undercounted cross-package fields (a failed package blocks its dependents) and was discarded.
- Not verified: JSON compatibility of any change proposed below; who outside this repo reads `obor status --json` (unknown on 11-05 and now); `docs/design.md` rules on the entities.

## Re-check of the 11-05 findings (`data-model.md`)

| 11-05 finding | State at 9d2d8131 | Evidence |
|---|---|---|
| 1. Task copied into five view types; `WaitItem` lost its title | **Fixed (T-883)**: `view.Task` built once for every task id (`tasks()`); `Lane`, `MergeItem`, `WaitItem`, `NeedsYou` name the task by id and keep only their own fields; `web.LaneDot` embeds `view.Task` | `view/task.go:15,38-86`; `view/queue.go:33-70`; `web/web.go:199-206` |
| 2. Title computed at four view sites + `steps.go` | **Fixed for the view** (one site, `tasks()`); `spec.Title` is still called at 6 other production sites: `app/steps.go:240` (gate), `app/specs.go:50,131`, `cmd/obor/spec.go:104`, `cmd/obor/drive.go:245`, `checks/flakes.go:352` | `view/task.go:83` |
| 3. TS drift (`Lane.hold`, `TaskStatus.stage/stopping`, `Serve.herdr_note`) | **Fixed (T-885)**: `internal/web/types_test.go` maps each TS interface to a Go struct (`goStructs`) and the string sets (`TestTSStringSetsMatchGo`) | `web/types_test.go:1-60`; `rg hold web/src/lib/types.ts` empty |
| 4. Cost of one field (Session ~14 files, Task 8-9, Lane 8-9) | **Re-measured, lower**: see §"Cost of one field now" | below |
| Pane: `herdr.Agent`/`PaneInfo`/`port.Pane` three shapes; `herdrtest` hand-copies the wire | **Fixed (T-888)**: one `port.Pane` (12 fields); `herdrtest.Pane` marshals `herdr.Wire` (`fake.go:102,153`) | `port/port.go:17-38` |
| Session: five shapes (`guard.Began`, `wire.Call` flat, `store.Session`, `view.AgentSession`, `launch.chosenSession`) | **Fixed (T-887)**: `Began{Root; store.Session}` (`guard/session.go:119`), `Call.Session *store.Session` (`wire/call.go:150`), `AgentSession{store.Session; LastWrite}` (`view/detail.go:81`) | |
| `wire.AgentStatus` copies Session/Transcript/Started from `store.Session` | **Remains**, 3 fields copied by hand (`app/status.go`, writer 1 file each per probe) | `wire/status.go:310` |
| Lane: `checks.LaneStatus`/`port.Lane` twins; `PrepareInput`/`FreeInput` re-list `dispatchData` | **Fixed (T-889, T-891)**: `checks.LaneStatus` is gone (`LaneTable.LaneState` returns `port.Lane`); `port.Dispatch` is embedded in `dispatchData`, `PrepareInput`, `FreeInput`; `view.Lane` embeds `port.Lane` | `checks/lanetable.go:78`; `port/effects.go:~395-420`; `view/view.go:100` |
| Small mirrors: `view.Idle`, `view.ReviewItem` | **Fixed**: `type Idle = wire.IdleStatus` (`view.go:181`), `ReviewItem{wire.ReviewRequestStatus; State}` (`queue.go:65`). **Remains**: `view.Running` vs `wire.RunningStep`, same four fields, `Command` meaning the name on the wire and the configured command line in the view (`view.go:70-76`, `wire/status.go:81-86`) | |
| `wire.Call` is a union | **Remains**: 33 json fields, 28 methods (`wire/call.go`); per-method parameters (advice item 5) not done. Session group now one pointer. Field spread (prod files): `Root` 27, `Note` 23, `CallID` 21, `Agent` 20, `Role` 20, `Task` 14 | |
| `Project` assembled by 11 hand assignments | Not re-checked | |
| Not read in 11-05: `config.Data`/`view.Setup`, `setup.agent`/`Drive`/`launch.Launcher`, `guard.Who`/`out`, `checks.Options`/`run.Options`, conformance records | **Still not read** except `guard.Who`/`out` (`guard/whoami.go:12-60`: `Who{Role, Agent, State *store.LaneRow, Rev, Protected}`, `out` a 5-field copy for JSON) | |

**Cost of one field now** (measured: production files that reference a field of the owner struct, from the same probe). A task field of `wire.TaskStatus`: **1 writer file** (`app/status.go:70`) and **1-5 reader files** (`Task` 6, `State` 5, `Lane` 5, `Ended` 5, most fields 1-2); plus `view/task.go`, `types.ts`, the renderer. A lane-row field: **4 writer files** (`app/lanerows.go`, `store/lanes.go`, `guard/whoami.go`, testkit) and 1-17 readers (`Task` 17, `Agent` 11, `Base`/`Branch` 6, others 4-5). A `store.Session` field: 2-4 writer files, 2-11 readers (`ID` 11, `Agent` 8, `Transcript` 8). Versus 11-05's hand counts: Task 8-9 → ~4-5; Session ~14 → ~4-6; Lane 8-9 → ~5-6.

## Shapes per entity

### Task

| Type | file:line | Is | Fields | Writers (prod files) / readers |
|---|---|---|---:|---|
| `mtask` | `app/loop.go:142` | serve's fold state (private) | ~30 own + `attemptVars` 24 + `disp` | state: 4 files; most others 1-2 |
| `attemptVars` | `app/loop.go:~205` | what a new attempt resets (embedded in `mtask`) | 24 | 1 writer file each except `line` 2, `fails` 2; readers 1-8 |
| `dispatchData` | `app/dispatch.go:55` | the dispatch fact's data (persisted); embeds `port.Dispatch` | 12 + 6 | `Type` 6 files, `Effort` 5, `Types` 4, `Call`/`Reply` 4 |
| `port.Dispatch` | `port/effects.go:~402` | lane, dir, agent, branch, target, redo | 6 | `Branch`/`Lane` 7 files each |
| `wire.TaskStatus` | `wire/status.go:274` | serve's answer to CLI/web | 19 | 1 writer (`app/status.go:70`, inline in a ~140-line `Status` function), 1-6 readers |
| `view.Task` | `view/task.go:15` | the view's one task | 13 | built in `tasks()` from 4 sources |
| `view.TaskDetail`, `MergeItem`, `WaitItem`, `NeedsYou`, `Lane`, `web.LaneDot` | `view/detail.go:27`, `queue.go:33,51`, `view.go:156,100`, `web.go:199` | sections that name the task by id | | |
| Event data of its steps | `submitData`, `stopData`, `gateIn/gateOut`, `testIn/testOut`, `dropData`, `promptData`, `startData/startOut` (`app/loop.go:~233-275`, `dispatch.go:77-110`, `stopstep.go`) | records | | |
| `event.Record.Task` | `event/log.go:22` | the key every record carries; **also names 13 project-level pseudo-tasks** (`disk, watch, nightly, pause, loop, review, msg, serve, roles, config, idle, rule, promote`, `event/steps.go:13-43`) | | `Task` write 68 sites in 28 files, read 98 sites in 27 |
| `wire.Call.Task`, `store.{LaneRow,Session,SpecRow,PromptRow}.Task`, `port.{Candidate,GateInput,TestInput,PrepareInput,Lane}.Task`, `guard.out.Task`, … | | references by id | 45 types in all | |
| TS | `web/src/lib/types.ts` (47 interfaces), checked by `web/types_test.go` | | | |

**Writes of the state (fact):** `mtask.state` is assigned at 28 sites: `mtask.go` 22 (the fold), `states.go` 3, `dispatch.go` 2, `stopstep.go` 1. 24 production files read it (82 sites). The legal transitions are one table, `stateRow` (`app/states.go:18-30`: pick, verdicts, verbs, resume, scratch, uses, landed, ended, failed, phase), mirrored by 58 numbered rows in `docs/state-model.md` (code cites "row 20", "row 50").

**Lifecycle (fact, `app/loop.go:120-140`):** 20 states: `waiting → dispatched → working → submitted → candidate → green → awaiting-accept → accepted → published → merged`, side states `red`, `conflict → resolving → resolved`, `moved`, `driving`, `dropping`, terminal `merged`, `rejected`, `dropped`. A needs-you is a *block on a phase*, not a state (`mtask.needs`, `block.go`, 84 lines). Who changes it: the fold of one record at a time (`foldLoop`, `mtask.go:13`; dispatch/waiting folds in `dispatch.go`; drop in `stopstep.go`), after `App.do` appended the record in the same `mu` section (T-944).

**Duplicates and scattered invariants:**
- *Which lane is the task's*: `mtask.lane`, `LaneRow.Task`, `port.Lane.Task` (derived from the branch name via `TaskFromBranch`, `config.go:1007`), `FactData.Lane`, `submitData.Lane`, `store.Session.Lane`; the view's rule is a precedence list in a comment and code (`view/task.go:19,51-79`: serve's lane, else the lane whose branch is its own, else its needs-you's). *Opinion:* this is the single most-repeated derived fact.
- *Is the task open*: `TaskStatus.Ended` read in 5 files; `view.Task.Open` (`!st.Ended`, `task.go:~58`); `states[mt.state].ended` (`status.go:71`). One place (T-896), three spellings.
- *Branch name*: one source (`config.TaskBranch`/`TaskFromBranch`, 5 production users); `stopstep.go:202` and `dispatch.go` use it, tests hardcode `task/T-xx` in 76 files.
- *Task id syntax*: `task_id_pattern` config; the drive's next id parses the number itself (`app/drive.go:310-327`).
- *Spec rev a worker saw*: `mtask.specRev`, `FactData.Rev`, `SpecRow.Rev`, the gate's `gateOut.SpecRev`, `gateRev` in `attemptVars`: five places for "which revision" (`dispatch.go`, `fetch.go`, `steps.go`).
- `mtask.disp` is copied into the next attempt's `mtask` by hand (`mtask.go:~44-52`: `next.sendbacks, next.specRev, next.drive = …; next.disp, next.at = …`): an invariant ("the dispatch's identity outlives the submission, T-778") enforced by one copy statement.

### Lane

| Type | file:line | Is | Fields | Spread |
|---|---|---|---:|---|
| `store.LaneRow` | `store/lanes.go:18` | the lanes table row (serve's projection of its folded state) | 10 | writers: `app/lanerows.go`, `store/lanes.go`, `guard/whoami.go`, testkit; readers 21 prod files |
| `port.Lane` | `port/port.go:103` | git + row observation (the lane table); JSON-tagged | 11 | `Name` 15 files, `Agent` 16, `Task` 12, `State` 9 |
| `view.Lane` | `view/view.go:100` | embeds `port.Lane`; adds `Op, AgentStatus, Free, Column, Since, Background` | 6 + 11 | 4 files |
| `config.Lane` | `config/config.go:624` | `{Name, Dir}` derived from a count | 2 | |
| `web.LaneDot`, `view.SetupLane`, TS `Lane`/`LaneDot` | | views | | |
| `checks.Lane` | `checks/lane.go:21` | the lane-step adapter (`Prepare`, `Free`) | | |
| `port.PrepareInput`, `FreeInput` | `port/effects.go:~410-420` | intents; embed `port.Dispatch` | | |
| `guard.Who.State *store.LaneRow` | `guard/whoami.go:12` | hook reads the row while serve may be down | | |

**Writers (fact, probe):** the table is written by one function (`WriteLanes`, `store/lanes.go:81`) from one fold (`app/lanerows.go`, 156 lines; 2 literals). `Finished` is read by `guard/stop.go`, `checks/lanetable.go`, `app/lanerows.go`; `Blocked` by `guard/stop.go`: the hook decides from the projection (design "Change rules" 7).

**Lifecycle (fact):** states are `free | dirty-free | working | missing | unknown` (`port/port.go:~115-125`), *computed from git on each read* (`checks/lanetable.go:78-135`), not stored: free = HEAD is an ancestor of the main checkout's HEAD and nothing uncommitted and no in-flight evidence; the row only says which task/branch/agent it was last given and when it finished. A lane changes state by: dispatch (`lane.prepare`, `checks/lane.go:29`), merge (ancestry changes), `lane.free` for a drop (`lane.go:70`), a lane test's temporary detach/restore (`lanetest.go:48-70,104`).

**Duplicates and scattered invariants:**
- *Free* is decided in 5 places: the git rule (`lanetable.go:100-125`), `pickLane` (`app/dispatch.go:492`), `view.Lane.Free` ("no task holds the lane", `view.go:~112`), `Lane.Column`, and `guard/stop.go` (via `Finished`/`Blocked`).
- *Two lane shapes with overlapping fields*: `LaneRow` (Lane, Task, Branch, Agent, Type) vs `port.Lane` (Name, Task, Branch, Agent, Type): the row's `Branch` is a recorded fact; `port.Lane.Branch` is git's current branch. They can differ and both are read (`LaneRow.Branch` read in `view/view.go` ×2, `cmd/obor/config.go`).
- *Agent of a lane*: `LaneRow.Agent` (stored), `app.laneAgent` prefers the row's then the derived name (`dispatch.go:410`), `view.laneAgent` prefers serve's live agent then the row's (`view/task.go:93`): same name, two precedence rules in two packages.
- *Lane names*: `config.LaneName` (`L-%02d`) is the one derivation; `setup/oborinit.go:188,643-651` hardcodes `L-01`/`L-02`; 168 test files use `"L-0N"`.

### Step (the merge/lane steps)

| Type | file:line | Is |
|---|---|---|
| `event.Record{Task, Attempt, Step, Kind, Class, Data}` | `event/log.go:22` | one log line: intent, result or fact |
| `event.Key{Task, Attempt, Step}`, `event.Intent`, `event.StepN`, `app.nth` | `event/log.go:~96`, `event/steps.go:159`, `app/loop.go:269` | idempotency key; `StepN` parses `name.N`, `nth` builds it (two packages, inverse functions) |
| 57 distinct `event.Step…` identifiers referenced in production (53 constants declared in `event/steps.go`, the rest helpers and constants elsewhere) | | the vocabulary; top users `mtask.go` 30, `messages.go` 24, `loop.go` 23, `describe.go` 17 |
| per-attempt counters in `attemptVars`: `gates, reviews, resolves, pubs, submits, tests, fixes` + `prepares, starts, works` in `mtask` | `loop.go:205-232`, `:~175` | "the last N used" per step type: 10 integer fields |
| `fails map[string]failure` | `loop.go:~233` | counted failures per step key; `tries = 3` (`loop.go:117`) |
| `wire.PendingStep`, `wire.RunningStep`, `view.Running` | `wire/status.go:81,91`, `view/view.go:72` | status projections of open intents |
| `stateRow` + `docs/state-model.md` 58 rows | `states.go:18` | the transition table |
| Effect inputs/outputs | `port.GateInput`, `TestInput`, `PrepareInput`, `FreeInput`, `Candidate`, `PromoteOut`, `FFOut` | `port/effects.go` |

**Lifecycle (fact):** an effect step is *intent → result*, with a crash class (`F` fact, `S` start/stop checked by snapshot, `G` git checked by ancestry, `prompt`); a failure is counted per step key and the third makes a needs-you (`App.do`, `loop.go:117`). One writer for records: `App.do`/`record` (append and fold in one `mu` section since T-944). Readers fold them (`foldLoop`).

**Duplicates / scattered:** step *names* are used in 57 constants and ~12 files with 10+ references; the counter-per-type fields (10) repeat one concept ("next N for step X") ten times, plus the same `nth/StepN` pair spelled in two packages. The state→next-step mapping is the table (good); the step→crash-class→recovery mapping is partly in docs (rows) and partly in `ClassFact/ClassStart/ClassGit` use (`event/log.go:38-41`).

### Agent (with Session, Pane, Role)

| Type | file:line | Is | Fields | Spread |
|---|---|---|---:|---|
| `roles.Role` | `roles/roles.go:30` | the role definition (name, cwd, write list, git read-only, prompt, suffix…) | 13 | `Name` 15 files |
| `config.RoleConfig`, `config.AgentInfo` | `config/config.go:~640-655` | the role + model resolved for a configured agent | 5 | 14 files |
| `project.Role` | `project/project.go:238` | a session's role and lane and where they came from | 3 | `DetectRole` |
| `guard.Who`, `guard.out` | `guard/whoami.go:12,55` | whoami | | |
| `store.Session` | `store/sessions.go:19` | one recorded session (agent, kind, id, transcript, pane, lane, task, started, source) | 9 | `ID` 11 files, `Agent` 8, `Transcript` 8 |
| `port.Pane` | `port/port.go:17` | herdr's pane + agent as observed | 12 | `ID` 24 files, `Agent` 17, `Status` 17, `Cwd` 12, `Label` 10 |
| `wire.AgentStatus` | `wire/status.go:310` | serve's answer per observed agent | 12 | 7 files |
| `registry`, `Where`, `owner`, `startRow`, `claim` | `app/registry.go:29-110,194` | identity table: which pane proves which agent | | "only this file names its maps" |
| `launch.target`, `port.Launch`, `port.Start`, `port.Started`, `port.AgentEnv` | `launch/launch.go:223`, `port/effects.go:31,138,27,131` | start/stop/restore parameters | | adjacent copies of kind/pane/session/terminal, not traced in 11-05 or here |
| `app.startData`, `startOut`, `stopData`, `stoppedData`, `halt` | `app/dispatch.go:86-110`, `stopstep.go`, `stopped.go` | records of starts and stops | | |
| `view.Role`, `view.SetupRole`, `wire.BriefStatus` | | views | | |

**Name derivation (fact):** one derivation, `roles.Role.AgentName(prefix, lane)` (`roles/roles.go:170`), reached by `config.AgentName` (`config.go:770`), `config.Self` (`:~780`) and `project.SelfAgent`/`Self` (`project.go:285-299`; "every who-am-I is this one call"). But `LaneRow.Agent` *stores* a name (writes at `app/lanerows.go`, the table scan in `store/lanes.go`, testkit; read by `project/laneagent.go` and 9 other files) and `app.laneAgent` prefers it over the derived one (`dispatch.go:410`), against AGENTS.md "a name obor can derive is never stored" (T-786). `port.Pane.Name` is herdr's name, never used for identity (`port.go:22-25`).

**Lifecycle (fact):** an agent is *not* a stored entity. Its states are: pane status from herdr (`idle, working, blocked, done, unknown`, `port.Pane.Ready/Busy`, `port.go:41-45`); identity state from the registry (`Identified | Ambiguous | Unavailable`, `registry.go:22-26`, proofs `session > start > label`); role-agent stop state (stopped/limit/wake: `stopwake.go`, `stopped.go`); worker presence via the lane. Changed by: `reconcile` of the registry on each snapshot (serve), `launch.Launcher` start/stop, `App.do` start/stop steps, hooks (`session` method) that append `store.Session`.

**Duplicates / scattered:** `Session` identity facts (`ID`, `Transcript`, `Pane`, `Kind`) are held by `store.Session`, `port.Pane.{Session,Transcript,Terminal}` (herdr's), `startRow.{Pane,Terminal,Kind,Session}` (registry), `startOut` (record), `launch.target`: five holders of "which session is this agent's" with a priority scheme (`srcSession > srcStart > srcLabel`, T-813 `NamedSession`) that is the actual rule. `wire.AgentStatus` copies 3 session fields by hand.

### Job

**No struct holds a job.** Fact: a job is a step key `(task, attempt, step)` with `JobID(key) = task/attempt/step` (`app/effects.go:9`; the id is built in one place and I found no `strings.Split`/`Cut` on it in `internal/app`, `internal/mcp`, `cmd`). Its data is spread over:
- `attemptVars.job` (step name), `.work`, `.patch`, `.delivered`, `.lost`, `.fixed`, `.fixes`, `.decision` (`loop.go:205-232`; `job` read in 5 files/11 sites, `work` 7 files/17 sites); `mtask.workerJob()`/`integratorJob()`/`jobKey()`/`openJob()`/`jobState()` (`fetch.go:42-55`, `mtask.go:261-280`).
- the prompt record (`promptData`, `app/prompt.go`; `store.PromptRow` `store/prompts.go:27`, `prompts_job` index) and its text (`jobstart.go`, 337 lines).
- timers not recorded (`jobTimer`, `jobs.go:17-25`; "a replay starts them afresh").
- delivery: `p.folded.delivered[jobID]` (`status.go:~92`), `FactData.Via`, `wire.JobReply{State, Job, Task, Attempt, Step, Text, Current}` (`wire/call.go:81`; 7 fields, 2 production files: `app/fetch.go`, `mcp`), `view.Job{ID, Delivered}`, `TaskStatus.Job/Delivered`.

**Lifecycle (fact):** to be sent (`stateRow.pick` → `a.job`, `jobstart.go:28`) → prompted (intent/result `prompt` class) → delivered (the agent's `job_next` fetch; "a job is fetched, not typed", AGENTS.md) → answered by a verdict or `task_done` / lost (`job.lost`, `mtask.lost`) → superseded by a newer job (`JobReply.State = "superseded"`). Changed by: `startJob` (`jobstart.go:148`), `fetch.go jobNext`, `verdict.go`, `jobs.go` timers.

### Review

| Piece | Where | Is |
|---|---|---|
| request | `reviewReq` (`app/review.go:105`; 6 fields), folded by `foldReview` (`:~150`); `ReviewData` fact (`:53`), `wire.ReviewRequestStatus` (`wire/status.go:102`), `view.ReviewItem` | the open request: tasks it waits for, note, asker, held |
| started review | `lastReview{seq, id, head, closed}` (`review.go:126`) | id, head at start, closed seq |
| verify cycle | `reviewLog`, `verifyBrief`, `fixLink`, `fixState`, `reviewCycle`, `openCycle` (`reviewverify.go:43,54,126,184,351`; `reviewclose.go:27`) | what a close needs: covered tasks, reviewed head, pending fixes |
| the close | `ReviewCommit`/`ReviewClose` (`review.go:69,98`), `ReviewGit.Close` (`cmd/obor/reviewcycle.go:63`) | a commit on the integration branch + `BACKLOG.md` edit + file deletes |
| the file | `docs/reviews/RV-n.md` (+ `-verify.md`) in the main checkout | the reviewer's output, read by `openReview` (`reviewverify.go:~360`) |
| fix link | the fix task's spec line `Fixes: RV-n …` (`spec.Fixes`, `internal/spec/spec.go:243`; `linkFixes`) | which task fixes which review |

**Four stores**: the event log (request/start/closed facts), git (close commit subject and `Covered:` body; "No state: git alone", `promote.go:40`), the filesystem (review file), and spec text (`Fixes:`). Twelve types carry its data, but only 3 production files touch `reviewReq`'s fields (`app/review.go`, `app/status.go`; 13 write sites), so the in-memory part is encapsulated; the *cross-store* invariants are not: "main moves only to a commit covered by a closed review" (`promote.go:394-397` reads git), "close refused until fixes verified" (`reviewverify.go`), "the reviewer's stop is a rule keyed by the closed fact" (`rules.go ruleReviewerStop`).

**Lifecycle (fact):** requested (waiting on tasks/held/ready) → fired → started (id, head) → verify briefs → closed (commit + `review.closed` fact). Changed by: `reviewCall`/`reviewClose` (serve, admitted), the `verify` and `review-on-merge` rules, `cmd/obor/reviewcycle.go` (git side).

### Spec

`store.SpecRow{Task, Text, Rev, SetBy, SetAt, Note}` (`store/specs.go:19`; 6 fields). The entity is the **text**; its fields are parsed from it by free functions in `internal/spec` (`Title`, `Difficulty`, `Models`, `OldTypeLine`, `ReviewValue`, `ReviewOnMerge`, `Fixes`, `Protected`, `Overlap`, `Match`; `spec/spec.go`, 308 lines) called at **24 production sites in 13 files** (`app/specs.go` 9, `dispatch.go` 3, `reviewverify.go` 2, then one each in `view/task.go`, `guard`, `checks/classify.go`, `checks/flakes.go`, `app/steps.go`, `status.go`, `promote.go`, `affected`, `cmd/obor/spec.go`, `drive.go`). `SpecRow.Text` is read at 19 sites in 14 files; `Rev` in 8 files. Validation happens once at set time (`app/specs.go:50-70`: title, difficulty, no old `Type:` line, `Review:` value, `Fixes:` shape) and readers re-parse leniently. A spec is written by one function (`specSet`, `app/specs.go:25`, lead/owner only); the store row is written by `store/specs.go` (1 file, 2 sites). Lifecycle: set (rev n+1) → read by dispatch/work job/gate (rev copied into the merge commit message and `gateOut.SpecRev`) → `task_done` at an older rev refused once (`mtask.specRev`, T-819/T-824).

### Candidate

`port.Candidate{Task, Lane, SHA, Target, Base, Patch, Checks, Stat, Diff}` (`port/effects.go:233`). **Best-encapsulated entity**: written in 1 production file (`checks/scratch.go`, 12 write sites), read in 6 files (47 sites); persisted inside `gateOut.Candidate` (`loop.go:~265`) and held as `attemptVars.cand` (18 reads, 5 files), so its JSON is part of the log's shape. Plus its git half: pins `refs/obor/candidate/<task>`, `submitted/<task>/<id>`, `resolved/<task>` (`checks/scratch.go:34-36`). Lifecycle: submitted pin → gate merge → candidate pin (+ compile) → lane test (`stCandidate`, T-961) → accepted → published (ff) or moved (re-merge onto a new target, one merge commit per task) → unpinned (`Unpin`, `AfterMerge`). `Diff`/`Stat` carry up to `InlineDiff` = 15 KiB of text per record (`effects.go:~244`). Test sites: 198 write / 135 read (tests construct candidates directly, 24 files).

### Others found

| Entity | Shape(s) | Note |
|---|---|---|
| Message / brief / lead line | `event.Message` (`event/data.go:44`, **15 fields**, one struct for lines, briefs, answers, stop alarms, verifies, driver lines); `outbox` (`app/messages.go:89`), `brief` (`:213`), `wire.MessageStatus`, `BriefStatus`, `view.OutboxItem` | union struct: 66 write sites in 7 files, 99 read in 10, all in `app`; `Fresh`, `Stop`, `Supersedes` each referenced in one production file |
| Needs-you | `block` (`app/block.go`), `event.FactData{Cause, Job, Call, Step, Reason…}` (18 fields, `event/log.go:~43-68`: a union of watchdog, merge-loop and role facts), `view.NeedsYou` | `FactData` is the generic fact payload for stalled/needs-you/resumed and the merge-loop facts |
| Watch, Pause, Idle, Promotion, Drive/Drop (`dropData`, `halt`) | `WatchData` (`mergewatch.go`), `event.Pause`, `wire.IdleStatus`, `PromoteStatus`, `dropData`/`halt` (`stopstep.go`) | each is its own fold with its own pseudo-task key |
| Role (project-level) | `roles.Role` + `config.RoleConfig` + `project.Role` | three types for "a role": definition, resolved config, detected session role |
| Config | `config.Data` (27 top-level keys, `config.go`), `view.Setup`, `Command`, `Merge`, `Branches`, `Slots` | removed keys still carried as `any` fields that WARN for two minors (`Merge.TimeoutGrep`, `MaxLoad`, `LoadWait`, `ReceiptMaxAge`, `Slots.MergeBlockMax`, `Checks.LoadLimit`) |

## Targets: what each entity would own if encapsulated, and the measured cost

Opinion throughout this section; the **cost is the measured number of production sites/files to move** (probe above) plus the test sites that construct or assert the internals. None of this was prototyped; JSON/log compatibility is the main unverified risk because several shapes are persisted (`dispatchData`, `gateOut`, `Candidate`, `Message`, `FactData` are event data; `LaneRow`, `SpecRow`, `Session` are table rows).

| Entity | Owns (fields) | Methods | The one place its state changes | Cost (measured) |
|---|---|---|---|---|
| **Task** | `mtask` + `attemptVars` + `disp` as one `task` value; `Lane`, `Agent`, `Attempt`, `State`, `Needs`, current `Job`, `Candidate`, `SpecRev`, counters | `Apply(record)` (the fold), `Status()` (builds `wire.TaskStatus`), `Open()`, `Lane()` | `foldLoop` (`mtask.go:13`) | Small: move 6 `state` writes out of `dispatch.go` (2), `states.go` (3), `stopstep.go` (1) into the fold, plus `disp`/`drive` writes in `dispatch.go` (3) and `halt` (1): **~12 write sites in 3 files**. `Status()` replaces the inline 19-field literal at `status.go:70`. Tests: 96 test writes of `mt.state` in 12 files build states directly (they would use the fold or a constructor): the main cost |
| **Lane** | `LaneRow` facts (task, branch, agent, base, head, dispatched, finished, blocked, type) + observed git state; one `Free()` / `Holds(task)` | `State(row, obs)`, `Free()`, `Agent()` | `app/lanerows.go` (row) and `LaneTable.LaneState` (observation) | Medium: collapse the 5 deciders of "free" (git rule, `pickLane`, `view.Lane.Free`, `Column`, `guard/stop.go`) onto one function over (row, obs); reconcile the two `laneAgent` functions; keep one `Branch`. LaneRow: 44 write sites in 4 files, 82 read sites in ~17 files; 256 test write sites (testkit builds rows). Table migration (`laneCols`, `store/lanes.go:~38-50`) is the existing price of a new column |
| **Step / Job** | `Step{Key, Class, Intent, Result, Tries}`; `Job{Key, Text, Sent, Delivered, Lost, Patch}` | `Next(base) key` (replaces `nth` + 10 counters), `Fail(key, why)` | `App.do` / `record` | Small: 10 counters → one `map[base]int` (27 read sites, ~8 write sites in 4-5 files); `StepN`/`nth` to one package; `attemptVars.job*` (8 fields: ~45 read sites, 7 files) into a `Job` value; `wire.JobReply` unchanged (2 files). Tests: 116 read sites of `JobReply` fields (they assert the wire reply, not internals) |
| **Agent (identity + session + pane)** | `Name` (derived), `Kind`, `Role`, `Lane`, current `Session`, pane binding | `Name(role, lane)`, `Proof()` | `registry` + `store.Session` append (two places, both single-file) | Small structurally (already one derivation and one registry), medium to remove stored names: `LaneRow.Agent` (5 write sites, 3 files; 13 read sites, 10 files) and `laneAgent` precedence; `wire.AgentStatus` 3-field copy (1 file). The `session > start > label` proof is a genuine rule worth keeping in `registry.go`. Tests: 768 write sites over 5 types (mostly `port.Pane` literals in 24 fake-pane scripts, `herdrtest`): high and mechanical |
| **Review** | request, started `{id, head}`, `covered`, `fixes`, `state` (requested/started/closed) — as **one value in the fold**; the close commit and `BACKLOG.md` line are *rendered from it* | `Covered()`, `Pending()`, `Closeable()`, `Render() ReviewCommit` | `foldReview` + `reviewClose` | The in-memory side is cheap (13 write sites, 3 files). The cost is the cross-store part: `unreviewed` (`promote.go:394`) and `openReview`/`Last` (`reviewverify.go:~351`) re-derive state from git and the review file; making the log authoritative means writing `Covered` into the `review.closed` fact (`ReviewData.Tasks` already exists, `review.go:53`) and reading git only to reconcile. 4 production files + `cmd/obor/reviewcycle.go`; tests with real git in `cmd/obor` (reviewcycle_test.go) |
| **Spec** | parsed fields (title, difficulty, models, review, fixes, protected files) next to `Text`, `Rev` | `Parse(text) (Spec, error)` once at set; stored parse cached in the row or re-parsed by one accessor | `specSet` | Small and mechanical: 24 call sites in 13 files become field reads; stored text unchanged (no migration); error behavior: readers today ignore parse failures, a `Spec` would carry them |
| **Candidate** | as today | `Pin`, `Bind(patch)` | `checks/scratch.go` | Nothing to do structurally (1 writer file). Only watch the persisted `Diff`/`Stat` size |
| **Record / FactData / Message / Call** | union structs; each acts as 3-6 entities | per-kind payload types | writers `App.record` | High: `Record.Task`/`Step`/`Kind` have 68 write sites in 28 files and 953 test write sites; `Message` 66 write sites in 7 files; `Call` has 27 `Root`-readers. These are persisted or wire shapes: splitting them needs the compat window the Change rules require (`docs/design.md` rule 5). *Opinion:* only worth it per pseudo-task kind (the 13 project-level keys) if a new feature would otherwise add a 14th |

## What the standing pass could check each time (opinion)

1. The count of production files that reference an owner struct's fields (this doc's probe: `scratchpad/refs/main.go`, ~190 lines of stdlib Go, 2 s to run) — a per-pass number to track.
2. New fields on a union struct (`Call`, `Message`, `FactData`): count since the last pass.
3. Types carrying task/lane/agent id that also copy facts about it (the 45/36/49 lists, `go/ast` pass).
4. Derived values stored (`LaneRow.Agent`, `mtask.specRev`/`FactData.Rev`/`gateOut.SpecRev`): "a name obor can derive is never stored" (AGENTS.md).
5. Entities with no struct: any `attemptVars`-style bag that grows fields named after one concept (job*, step counters).
