# obor's data model: how many shapes one thing has, and what each copy costs

Brief: owner 11-05 asked why one task is copied into five view types, and how much code like that exists. Survey only; no code changed.
Sources: the lead's scan (`structscan/pairs.txt`, 154 struct pairs sharing >=4 field names out of 268 structs), `docs/research/architecture-advice-fable.md` items 6-7, and the code read at `0ba5dd5d`.

## Answer

1. **The task copies are mostly not boundaries.** Of the shapes a task has, two are real: `dispatchData` (the persisted dispatch record) and `wire.TaskStatus` (serve's answer). `mtask` is serve's private fold state. The other six are same-process mirrors in `internal/view` and `internal/web` (`view.Lane`, `MergeItem`, `WaitItem`, `NeedsYou`, `TaskDetail`, `web.LaneDot`), plus seven hand-typed TS interfaces for them. Each mirror is filled by hand from `wire.TaskStatus` and the spec store, so each can forget a field.
2. **The title bug is exactly that.** `queue()` builds a `WaitItem` three ways (`internal/view/queue.go:120-122`, `:141`). The two branches that wait on a queue (`WaitsLane`, `WaitsAfter`) never set `Title`; only the never-dispatched one does (`queue.go:141`). `MergeItem` (`:129`), `Lane` (`view.go:297`) and `TaskDetail` (`detail.go:140`) each call `spec.Title(...)` themselves. Four view call sites (plus `steps.go:275` in the app) compute one value; one branch of one of them is missing.
3. **The TS side has already drifted** (checked by script, see "TS side"): `Lane.hold` is in `types.ts` and used (`web/src/pages/Project.svelte:204`) but Go's `view.Lane` has no `hold` field. That `{#if l.hold}` branch can never be true. `TaskStatus` in TS lacks `stage` and `stopping`; `Serve` lacks `herdr_note`. Nothing fails when they disagree.
4. **Cost of one field today**, by cluster (non-test files that must change, counted by tracing the field through the code):

| Cluster | Files today | Files after the owner-type fix |
|---|---:|---:|
| Session (`Transcript`-like) | ~14 | 4-5 |
| Task (`Effort`-like, shown on lane and queue) | 8-9 | 3-4 |
| Lane (`Branch`/`Type`-like, store row to view) | 8-9 | 4-5 |
| Small wire-to-view mirrors (`Idle`, `ReviewItem`) | 4 | 2 |

5. **Ranked by cost x how often a field is added**, the order is: TS contract test (cheapest, already catching live bugs), Task view cluster, Session chain, Lane chain, pane types, small mirrors, then `wire.Call`. Task list at the end.

## Method and its limits

- `copies.py` counts same-name copy expressions in non-test Go (`Field: x.Field` and `a.F = b.F`, including `a.F, a.G = b.F, b.G`). It is a loose regex: it also counts copies unrelated to this survey (e.g. `herdr/socket.go` JSON decoding), so treat its totals as an upper bound. Total: **650 in 81 files**. In the survey's area: `internal/view` 47 + 38 + 33 + 14 (setup) + 8 + 3 = 143, `web.go` 28, `herdr/socket.go` 46, `herdr/panes.go` 21, `herdrtest` 25.
- Where I give a hand count I read the mapping site and counted fields. Hand counts are marked "hand".
- Scripts (scratch, not committed): `/private/tmp/claude-501/-Users-djordjeradakovic-Work-obor-kit/b378e4eb-8ab1-4cc5-a2db-083a65499c23/scratchpad/copies.py` and `.../drift.py`.
- "Files today" is by reading the chain, not by replaying a past commit: the commits I checked (`8eff3863`, `7178bcde`, `0c81d166`) each carry a whole feature, so their file counts say nothing about one field.
- Not read in depth (pairs.txt only): `config.Data`/`view.Setup`, `setup.agent`/`setup.Drive`/`launch.Launcher`, `guard.Who`/`guard.out`, `checks.Options`/`run.Options`, `tests/conformance` records. Setup (`view/setup.go`, 14 copies) is the next place to look if this survey is extended.
- Which consumers pin the exact JSON of `obor status --json` I did not establish (`grep "status --json"` in `skills/` and `docs/agents/` finds none, but `cmd/obor` renderers and the lead's habits read it). The fixes below say where they change a JSON shape.

## Cluster 1: Task

| Shape | File | Boundary or mirror | Notes |
|---|---|---|---|
| `mtask` | `app/loop.go:137` | serve's private fold state | owner of truth inside serve; embeds `dispatchData` as `disp` |
| `dispatchData` | `app/dispatch.go:54` | **real**: the event log's record | persisted; also `PrepareInput` is built from it field by field (`dispatch.go:829`) |
| `wire.TaskStatus` | `app/wire/status.go` | **real**: serve's answer to the CLI and web | built from `mtask` in `app/status.go:62-75` (hand: 9 fields) |
| `wire.JobReply` | `wire/call.go` | **real**: the MCP reply | shares `State, Job, Task, Attempt` with TaskStatus by meaning only, built at `fetch.go:88-98`; not a mirror |
| `view.Lane` | `view/view.go:89` | mirror | takes 9 fields from `checks.LaneStatus`, 5 from `TaskStatus`, 2 from `AgentStatus`, `Job`, spec rev and title (hand: ~20 copies in `lane()`) |
| `view.MergeItem` | `view/queue.go:34` | mirror | hand: 8 + `Job` (2) |
| `view.WaitItem` | `view/queue.go:56` | mirror | hand: 4 + 3 + 3 over three constructions |
| `view.TaskDetail` | `view/detail.go:26` | mirror | hand: ~12 across `Task()` |
| `web.LaneDot` | `web/web.go:190` | mirror (fleet page) | hand: 8, copied from `view.Lane` at `web.go:233` |
| `NeedsYou` | `view/view.go:144` | mirror of `event.Record` + `Answers` | hand: 5 |
| `port.GateInput`, `port.PrepareInput` | `port/effects.go` | **real**: the intent's data, persisted and replayed | `gate()` calls `spec.Title` again (`steps.go:275`), a fifth site |
| TS: `Lane`, `TaskStatus`, `MergeItem`, `WaitItem`, `NeedsYou`, `TaskDetail`, `LaneDot` | `web/src/lib/types.ts` | hand-typed copy of the JSON | 7 of the file's 43 interfaces |

**Real boundaries (keep): 4** (`dispatchData`, `TaskStatus`, the two intent inputs). **Mirrors (can go): 6 Go + 7 TS.**

**Owner type.** `wire.TaskStatus` for the task's live facts and a `view.TaskInfo` (new) for what the view adds to it: `Title`, `SpecRev`, `Job{ID, Delivered}`. The view builds one `TaskInfo` per task, once, in one function that does the `spec.Title` and `specs[...]` lookups; `Lane`, `MergeItem`, `WaitItem` and `TaskDetail` take it instead of reading the spec map.

**Constraint on the fix (Go JSON embedding).** Embedding `wire.TaskStatus` in `Lane` does not work: both have `lane`, `task`, `state` at the same depth, and `encoding/json` drops fields that collide at one depth (both disappear, silently). `Lane` names the task's state `task_state` for exactly this reason. So: embed a struct whose JSON names are already `Lane`'s (`TaskInfo{TaskState, Attempt, Running, Phase, Effort, Job, SpecRev, Title}` has none that collide; JSON stays identical). For `MergeItem` and `WaitItem`, `state` and `title` are the same in both, so they can embed a shared `TaskHead{Task, Title, Lane, State, Phase, Stage}`; also no shape change.

**Copy code removed (estimate from the hand counts):** about 45-55 `Field: x.Field` / `a.F = b.F` lines in `view.go`, `queue.go`, `detail.go` and `web.go`, the four `spec.Title` call sites become one, and the `LaneDot` 8-line copy becomes an embed. TS side: 7 interfaces shrink to the head plus extensions, or are generated (see TS side).

**Cost of one new task field, e.g. one like `Stopping` (T-852):**
- Today (hand): `mtask` or `dispatchData` (`app/*.go`) 1, `app/status.go` literal 2, `wire/status.go` struct 3, `view/view.go` `Lane` field and `lane()` 4, `view/queue.go` if it shows in the queue 5, `view/detail.go` 6, `web/web.go` `LaneDot` if on the fleet page 7, `types.ts` once per interface 8, `cmd/obor/*render*.go` 9, plus `internal/web/dist` (rebuilt, committed). The T-883 bug is a case where step 5 was done for one of three `WaitItem` branches.
- After: `mtask` -> `status.go` -> `wire/status.go` -> the one `TaskInfo` constructor -> a renderer. 4-5 files; `types.ts` regenerated or checked, 0 hand edits.

## Cluster 2: Agent, pane, session

The chain for one pane/agent fact (here `Transcript`, which appears in 21 non-test Go files):

```
herdr JSON  ->  herdr.wire (socket.go:1059)            real: herdr's protocol
            ->  herdr.Agent = port.AgentInfo (alias)   w.agent()  (socket.go:1104, 8 field copies, hand)
            ->  herdr.Pane  = port.PaneInfo (alias)    w.pane()   (socket.go:1108, 9 copies, hand)
            ->  port.Pane                              appPane    (herdr/panes.go:85, 7 copies, hand)
            ->  wire.AgentStatus                       app/status.go:37, 10 copies, hand; + Session/Transcript/Started from store.Session
test:       herdrtest.Pane (fake.go:16) + paneJSON      real: a fake of herdr's server, duplicates herdr.wire
```

and for a session (the id, transcript, pane, lane, task, kind, started, source of an agent run):

```
guard.Began (guard/session.go:117)  ->  wire.Call (flat: Session, Transcript, Kind, Started, Source, Pane, Lane, Task)  [cmd/obor/main.go:317, 9 copies]
wire.Call -> store.Session (app/sessions.go:64, 9 copies)          real: persisted as a fact
launch.chosenSession (launch/session.go:64) and launch/session.go:161 build store.Session directly (2 x ~8 copies)
store.Session -> view.AgentSession (view/detail.go:330, 8 copies, + LastWrite)    mirror
store.Session -> wire.AgentStatus (app/status.go:46: Session, Transcript, Started)  mirror, 3 copies
TS: AgentSession, AgentStatus
```

| Copy | Verdict |
|---|---|
| `herdr.wire` | boundary, keep |
| `herdr.Agent`, `herdr.Pane` | already aliases of `port.AgentInfo`, `port.PaneInfo`: no copy |
| `port.AgentInfo`, `port.PaneInfo`, `port.Pane` | same-process mirror x2: three structs for one pane. `AgentInfo` and `PaneInfo` share `Cwd, Kind, Label, Session, Status, Transcript, Workspace`; `port.Pane` shares `ID, Label, Cwd, Status, Session, Transcript, Workspace, Terminal` with `PaneInfo` (pairs.txt: 7-8 shared names) |
| `herdrtest.Pane` | test fake of a real boundary; legit, but it re-declares herdr's wire fields by hand (10 shared names with `PaneInfo`) |
| `wire.AgentStatus` | boundary (serve -> CLI/web) |
| `store.Session` | boundary (persisted fact) and the natural owner |
| `guard.Began` | mirror of `store.Session` + `Root` (7 shared names) |
| `wire.Call` session fields | a transport of `store.Session`; flat because `Call` is one struct for 27 methods |
| `view.AgentSession` | mirror of `store.Session` + `LastWrite` |
| `launch.target`, `port.Start`, `port.Started`, `app.startRow`, `app.startOut`, `app.startData` | adjacent copies of kind/pane/session/terminal (pairs.txt 4-5 shared names); not traced |

**Owner type.** `store.Session` for a session (already persisted, and `store` imports no `obor/...` package, so `wire`, `guard`, `launch`, `view` can all use it; checked with `go list`). For the pane: one `port.Pane` with `Tab` added, `PaneInfo` and `AgentInfo` removed (`Name`, `Seq` ride as optional fields; `AgentInfo` is used in 25 non-config files).
- `view.AgentSession` = `store.Session` embedded + `LastWrite`. JSON is identical except an extra `"agent"` field.
- `guard.Began` = `{Root string; store.Session}`. `wire.Call` carries `Session *store.Session` instead of 8 flat fields (changes the wire shape of `MethodSession` only; serve and the hook are one binary, but a serve older than its hook would not understand it, so `wire/version` bumps).
- `herdrtest` marshals `herdr`'s exported wire struct instead of `paneJSON`'s hand map; `herdr/contract_test.go` then tests the same struct on both sides.

**Copy code removed (hand, from the sites above):** `main.go` 9 + `sessions.go` 9 + `launch/session.go` 2 x ~8 + `detail.go` 8 + the three pane structs' conversions (`w.agent` 8 + `w.pane` 9 + `appPane` 7) ~ **58 lines**, plus three of the five session shapes and two of the three pane shapes; TS `AgentSession` and `AgentStatus` keep one definition each.

**Cost of one new field:**
- Session field today (hand, ~14 files): `store/sessions.go`, `wire/call.go`, `guard/session.go`, `cmd/obor/main.go`, `app/sessions.go`, `launch/session.go` (two sites), `app/status.go`, `wire/status.go` (if on `AgentStatus`), `view/detail.go` (struct and `sessionOf`), `types.ts` (two interfaces), `testkit/testkit.go:274`, `agents/*` (if the CLI reports it).
- After: `store/sessions.go`, the producer (`guard/session.go` or `launch/session.go`), `types.ts` check = 3-4.
- Pane field today (hand, ~9): `herdr/socket.go` `wire` + two converters, `port/effects.go` x2, `port/port.go`, `herdr/panes.go`, `herdrtest/fake.go` (struct + `paneJSON`), `app/status.go`. After: `herdr/socket.go` wire + converter, `port.Pane`, `herdrtest` (wire struct shared) = 3-4.

## Cluster 3: Lane

| Shape | File | Verdict |
|---|---|---|
| `store.LaneRow` | `store/lanes.go:17` | **real**: the lanes table; folded in `app/lanerows.go:55,79` (two literals, 3 + 7 copies, hand) and read by `checks`, `view`, `guard`, `launch` |
| `dispatchData` | `app/dispatch.go:54` | **real** (event log). Shares `Lane, Dir, Agent, Branch, Type, Effort` with the lane shapes (pairs.txt: 6 with `view.Lane`, 5 with `PrepareInput`) |
| `checks.LaneStatus` | `checks/lanetable.go:24` | mirror: git state of a worktree + row fields (`Lane, Type, Dir, Branch, Task, State, DoneAge, Dirty, Warning`); the git facts are its own, the others re-read `LaneRow` |
| `port.Lane` | `port/port.go:86` | mirror of `LaneStatus` (`lanetable.go:152`, 5 copies, hand); exists so `app` need not import `checks` |
| `port.PrepareInput`, `port.FreeInput` | `port/effects.go` | **real** (intent data, persisted), but both are built by copying `dispatchData` (`dispatch.go:829` 8 copies; `stopstep.go:231` 5 copies, hand) |
| `view.Lane` | `view/view.go:89` | mirror; 9 fields from `LaneStatus`, `Agent` from the row |
| `guard.out` | `guard/whoami.go:55` | boundary (the `whoami` JSON), 5 copies from `Who`/`w.State` |
| TS `Lane`, `LaneDot` | `types.ts` | mirror |

**Owner type.** `store.LaneRow` (+ the git facts, which `LaneStatus` owns) for what a lane is; `dispatchData` for what a dispatch chose. `PrepareInput` should hold `dispatchData` (or a pointer to the dispatch's own struct) in place of re-listing 8 of its fields. `view.Lane` embeds `checks.LaneStatus` (JSON flat; its names are `Lane`'s own except `DoneAge` and `Warning`, which `Lane` re-types as pointers, so keep that conversion in one function). `port.Lane` is an alias of, or replaced by, `LaneStatus` if the import rule allows (`internal/checks/boundary_test.go` holds an import boundary; read it before changing).

**Copy code removed (estimate):** 3 + 7 (lane row literals use `dispatchData` once) + 8 + 5 (`PrepareInput`, `FreeInput`) + 5 (`port.Lane`) + 9 (`lane()`) = **~35 lines** and one shape (`port.Lane`).

**Cost of one new lane field** (e.g. what `Type` cost in T-721, when the lanes table also needed a migration: `store/lanes.go:26-35` still carries that column check): `dispatchData`, `PrepareInput`, `lanerows.go`, `store/lanes.go` (cols, scan, insert, schema), `checks/lanetable.go` (`LaneStatus`, `port.Lane`), `view/view.go`, `types.ts`, plus `guard/whoami.go` if the hook needs it = 8-9. After: `dispatchData` (the one place the dispatch decides it), `store/lanes.go`, `types.ts` check = 3-4 (the store migration stays; item 6 of the architecture advice, a snapshot at upgrade, is the way to delete those).

## Cluster 4: wire to view mirrors

| Pair | Same fields | Verdict | Fix | Lines removed (hand) |
|---|---|---|---|---:|
| `wire.IdleStatus` -> `view.Idle` | 4 of 4, same tags | pure mirror | `type Idle = wire.IdleStatus`; delete the literal at `view.go:234` | 1 + struct + TS dup |
| `wire.ReviewRequestStatus` -> `view.ReviewItem` | 6 of 7 (`State` added) | mirror | `ReviewItem{wire.ReviewRequestStatus; State}` | 6 (`queue.go:153`) |
| `wire.BriefStatus` / `MessageStatus` -> `view.OutboxItem` | 4 / 4 | mirror that merges two shapes into one with a `Kind`; reasonable as a view, but `Since` for lines is computed (`now - Age`) | keep, build with one constructor each | 0-4 |
| `wire.RunningStep` -> `view.Running` | 4 of 4 | **not a pure mirror**: `Command` means the command *name* on the wire and the configured *command line* in the view (`view.go:206`, `runningCommand`). Same field name, two meanings | rename one (`wire.RunningStep.Gate` or `view.Running.Cmd`) before merging, else leave as is | 0 |
| `wire.ProjectStatus` -> `view.Project` | 11 | `Build` copies `Running, Pending, Tasks, Agents, Messages, Review, Watches, Idle, Facts, ModelPauses, Paused` (hand: 11 assignments, `view.go:226-240`); `Project` is a superset by design | embed `wire.ProjectStatus` as `Serve` payload? changes JSON; leave, count it as the cost of an honest join | 11 if embedded |
| `wire.WatchStatus`, `PendingStep`, `MessageStatus` | used by the view directly (no copy) | already the right pattern | none | 0 |

`view.Project` already uses `wire.PendingStep`, `wire.TaskStatus`, `wire.AgentStatus`, `wire.MessageStatus` and `wire.WatchStatus` directly in its own fields: the codebase already does this; the mirrors above are where it did not.

## TS side

`web/src/lib/types.ts`: 433 lines, 43 interfaces, 296 fields, hand-kept. No test or script ties it to Go. `drift.py` compares each TS interface's keys to the same-named Go struct's `json` tags in `internal/view` and `internal/app/wire`:

```
Lane        | Go not TS: []                | TS not Go: ['hold']
Serve       | Go not TS: ['herdr_note']    | TS not Go: []
TaskStatus  | Go not TS: ['stage','stopping'] | TS not Go: ['hold']
TS interfaces with no same-name Go struct: Help, HelpCommand, HelpSection, LaneDot, ProjectView, ProjectsEntry
```

- `Lane.hold` is read at `web/src/pages/Project.svelte:204` and never set by Go: a dead UI branch (`hold` came from before T-788 moved the hold into `phase`; I did not check the commit that dropped it).
- `TaskStatus.stage`: `Project.svelte:43` reads `m.stage` from `MergeItem`, which has it; `TaskStatus` is typed without it, so any use on a `TaskStatus` would not type-check. `herdr_note` is unused on the web side.
- The 6 TS-only names are mostly wire shapes defined in `internal/web` (`LaneDot`, `ProjectsEntry`) and `internal/help`; the check should cover those packages too.
- Two ways to close the gap, as the advice says (item 7): a Go test that parses `types.ts` and fails on a missing or extra field (cheapest; `drift.py` is ~30 lines), or a generator run by `make web`. A test first, because it needs no new build step and would have caught the `hold` branch.
- `Phase*`/`Stage*` constants in `wire/status.go` are retyped as strings in `web/src/lib/stages.ts` (test: `stages.test.ts`); not checked here. Flagged, not expanded.

## `wire.Call` (adjacent; the advice already covers it as item 5)

I count 35 `json` tags on `Call` and 27 `Method*` constants (the brief said 36 fields, 28 methods; my counts are from a regex over `call.go` and may differ by one from yours). `Call` is a union: each method reads 2-9 of the fields. The session group is the one that overlaps another shape (`store.Session`, 8 shared names, above); `after/level/types/lane/force` overlap `dispatchData` (6) and `TaskStatus` (6); `agent/pane/lane/task` overlap everything. A per-method parameter struct (advice item 5) ends this; the session group above can be done first as one embedded struct without waiting for that.

## Ranked list

Rank = (files touched per added field) x (how often that kind of field is added: `Lane`/`TaskStatus` took `Effort` T-695, `Phase` T-788, `Stage` T-797, `Stopping` T-852, `Background` T-714, `Job` T-664 in about three weeks; session fields T-577, T-813) plus whether it is wrong now.

| # | Task | Files today -> after | Lines removed | Why this rank |
|--:|---|---|---:|---|
| 1 | TS contract test | 0 tests -> 1 | 0 (adds ~60) | already finds `hold`, `stage`, `stopping`, `herdr_note`; no dependencies; protects every later change |
| 2 | Task cluster in `view` (after T-883) | 8-9 -> 4-5 | ~50 | most-changed fields; the title bug lives here |
| 3 | Session chain | ~14 -> 4 | ~58 | deepest chain, 5 shapes, 2 TS |
| 4 | Lane chain | 8-9 -> 4 | ~35 | the lane row's migration cost is separate |
| 5 | Pane types | ~9 -> 4 | ~24 | three structs and a fake for one pane |
| 6 | Small mirrors (`Idle`, `ReviewItem`) | 4 -> 2 | ~8 | trivial, low yield |
| 7 | `wire.Call` split | all 27 methods | ~0 net | advice item 5; not started here |

## Tasks, split by files (for parallel lanes)

Each task is one lane's file set. Tasks in different groups touch disjoint files and can run at once; tasks within a group overlap and must go in order.

**Group A (TS side; independent of everything)**
- **A1 TS contract test.** New `internal/view/tsdrift_test.go` (or `internal/web/types_test.go`): parse `web/src/lib/types.ts` interface keys, compare to `json` tags of the Go structs in `internal/view`, `internal/app/wire`, `internal/web`; allow-list for the 6 TS-only names. Files: that one new test. Then fix what it finds: `web/src/lib/types.ts` (drop `hold` on `Lane` and `TaskStatus`; add `stage`, `stopping`, `herdr_note`), `web/src/pages/Project.svelte:204` (remove the dead `l.hold` branch), `internal/web/dist/*` (rebuilt by `make web`). Note the dist rebuild: only one lane may rebuild `dist` at a time (`TestDistFresh`), so A1 goes before any other task that edits `web/`.

**Group B (the view; one lane at a time, T-883 first)**
- **B1 (T-883, already planned)**: `view.TaskInfo` / one constructor, `Title` set everywhere. Files: `internal/view/view.go`, `queue.go`, `detail.go`, `*_test.go`.
- **B2 `LaneDot` and wire-view mirrors**, after B1: `internal/web/web.go` (`LaneDot` embeds, 8 lines gone), `internal/view/view.go` (`Idle` alias, `Running` rename decision), `internal/view/queue.go` (`ReviewItem` embeds `ReviewRequestStatus`), `web/src/lib/types.ts` if A1 is not yet merged (otherwise the test fails on its own diff). Same files as B1, so after it.

**Group C (session; touches no view file except one function)**
- **C1 `store.Session` as the one session type.** `internal/app/wire/call.go` (`Session *store.Session` replaces 8 flat fields), `internal/guard/session.go` (`Began{Root; store.Session}`), `cmd/obor/main.go` (`sessionBegan`), `internal/app/sessions.go`, `internal/launch/session.go`, `internal/testkit/testkit.go:274`, `internal/app/wire/version` bump. Tests: `guard/session_test.go`, `app/*session*_test.go`, `launch/session_test.go`.
- **C2 `view.AgentSession` embeds `store.Session`.** `internal/view/detail.go` (`sessionOf`, `AgentSession`), `web/src/lib/types.ts` (`AgentSession`). Overlaps B1/B2 in `detail.go` and A1 in `types.ts`: sequence after them.
- **C3 `wire.AgentStatus` fills its session fields from one `store.Session`.** `internal/app/status.go:46`, `internal/app/wire/status.go` (3 fields become `Session *store.Session`? this changes `obor status --json`; otherwise keep the 3 flat fields and only drop the copy). Needs an owner's call on the JSON shape; split off so C1 and C2 are not blocked on it.

**Group D (pane; touches `herdr`, `port`, `launch`)**
- **D1 one `port.Pane`.** `internal/app/port/effects.go` (`AgentInfo`, `PaneInfo` removed), `internal/app/port/port.go` (`Pane` + `Tab`), `internal/herdr/socket.go` (aliases, `w.agent`, `w.pane`), `internal/herdr/panes.go`, `internal/app/panes.go`, `internal/app/registry.go`, `internal/launch/launch.go`, `launch/start.go`, `setup/*` (25 non-config files use `AgentInfo`; list them with `grep -rln "port\.AgentInfo\|port\.PaneInfo\|herdr\.Agent\b"` before splitting), tests for each. Big and mechanical; run alone.
- **D2 `herdrtest` marshals the real wire struct.** `internal/herdr/herdrtest/fake.go`, `herdrtest/socket.go`, `internal/herdr/socket.go` (export the wire struct), `internal/herdr/contract_test.go`. Overlaps D1 in `herdr/socket.go`: after D1.

**Group E (lane; touches `app`, `store`, `checks`, `port`)**
- **E1 `PrepareInput` and `FreeInput` hold `dispatchData`.** `internal/app/dispatch.go:829`, `internal/app/stopstep.go:231`, `internal/app/port/effects.go` (both structs), `internal/launch/lane.go` (the adapter that reads them), `app/*prepare*_test.go`. These are persisted intent data: the stored shape of an intent changes, so `event` replays of an older log must still read the old shape (decide: keep the old field names via the same `json` tags, or a snapshot, advice item 6). Needs the owner's call.
- **E2 `view.Lane` embeds `checks.LaneStatus`; `port.Lane` removed.** `internal/checks/lanetable.go`, `internal/app/port/port.go`, `internal/app/project.go:221`, `internal/app/fetch.go:341`, `internal/app/stopped.go:378`, `internal/view/view.go` (overlaps B). Read `internal/checks/boundary_test.go` first.

**Order for a single stream of work:** A1, B1 (T-883), B2, then C1 and D1 in parallel, then C2, C3, D2, E1, E2.

## Not verified, and what it would take

- That each "files today" count is complete: I traced by reading and grep. The fastest check is to add one dummy field (e.g. to `store.Session`) on a scratch branch and let `go build ./... && go vet ./...` list what stops compiling; that finds the struct literals but not copies built with `a.F = b.F` on a zero-value struct, so it undercounts.
- The JSON compatibility of the embed fixes: `Lane`'s flat names with an embedded struct (shown to be fine where names do not collide) need a golden test of `obor status --json` before and after; `view_test.go` has none.
- Who reads `obor status --json` outside the repo (the lead's skills, the owner's scripts): not established.
- That `hold` was removed from `Lane` by T-788: inferred from the phase constants (`PhaseHeld`), not from the diff.
- Item 7's other half (fold `phase`/`stage`/`shown`, `states.go:170-203`) and the guard (item 8) are outside this survey.
