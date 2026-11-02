# `internal/app`: where the real boundaries are (design deep dive)

Deep dive (ob-lead's design pass, owner-approved 2026-11-02), at main `18f275c1`. Question: should `internal/app` (39
prod files, 11,815 lines) be split into packages, and where are the boundaries? Gemini's list
(`docs/research/codebase-performance-and-test-review.md` §4.1) and Fable's verdict
(`docs/research/codebase-review-advice-fable.md`) were read and not taken as given. Rules applied: AGENTS.md (one owner
per helper, idiomatic Go, simple), "LOC is not a goal", "structure debt is worth fixing without a bug" (decisions.md
2026-10-31), behaviour-preserving steps with the existing tests as the oracle. No code was changed. (The brief asked
for `docs/research/app-split-deep-dive.md`; the guard's deep_dive write profile allows only `docs/reviews/`, so the
file is here. Move it as you like.)

## Answer first

**Split it at the edges, not by subsystem.** The expensive coupling is not between the subsystems inside
`internal/app`. It is that every adapter, hook, test helper and reader imports the whole 11.8k-line core to get roughly
1,000 lines of contract types. `herdr` imports `app` only for the port types (`Pane`, `Event`, `PaneControl` …), and
`testkit → herdrtest → herdr` carries that dependency into the tests of **13 packages**. Today an `internal/app`-only
change re-runs all of them in the affected tier. Three leaf packages (ports, the event log, the socket protocol) cut
that to `app` + `cmd/obor`, and they move declarations without changing logic.

Inside the package, the "subsystems" share one kernel: `App.mu`, `record`/`do`, the fold fan-out, `whereIs`, the
outbox and the project needs-you. A subsystem package would need a host interface over that kernel and would carry the
`App.mu` discipline across a package boundary. For most candidates that cost buys nothing a file does not already
give: their state is already in one file, and git shows they are seldom edited together with the core. Two in-package
refactors are worth doing: one owner for the project needs-you, and `mtask` grouped by lifetime. There is one
conditional inner package (the outbox's state and fold) and one optional one (the identity table's rules).
The merge-loop core stays together.

Ranked list: §5. What not to split: §6.

## 1. Method

- **Access map, type-checked.** A probe (scratchpad, not committed) loads the package with `go/types` over
  `go list -export` data and records every field selection on the package's structs (read/write, with its file and
  function) and every cross-file use of a package-level func, method, type or const. Writes are LHS, `++`, `&x`,
  `delete`/`clear` targets. The numbers below come from it.
- **Lines by receiver** (declaration lines): `*App` methods 4,741; `*proj` methods 1,204; pure functions 1,214;
  methods on other types 540; types/consts/vars 1,742.
- **Co-change.** `git log pre-public --since=2026-10-01` plus `v0.16.0..HEAD`, non-merge, grouped by task id. That is
  181 tasks touching `internal/app` prod files, and 153 with at least 15 changed lines in some group ("substantive";
  smaller edits are wiring: a step constant, a fold call, a status field).
- **Dependencies.** `go list -test -deps` for every package; `app.X` identifiers used by each importer, prod and tests
  separately.

## 2. What the package is: one kernel, many folds

Every subsystem in `internal/app` follows the same pattern. It **folds** records into state under `App.mu`
(`foldRecord` fans each record out to 15 folds), **decides** under `App.mu` and acts outside it, **records** facts and
effects through `App.record`/`App.do` (intent → effect → result), resolves agents through `whereIs`, sends lines by
recording a `message` fact (the outbox delivers it), and raises project-level needs-you facts on its own task key.
That shared surface is the kernel:

| Kernel piece | Used by (prod files) |
|---|---|
| `App.mu` | 26 |
| `App.logf` | 21 |
| `folded.loop` (the merge tasks, `mtask`) | 19 read it |
| `App.record` / `recordSeq` / `do` | 16 / 3 / 8 |
| `App.wake` | 13 |
| `whereIs` (+ `Identified`, `Where`, …) | 11 (19 with the types) |
| `folded.active` (watchdog and project needs-you facts) + `proj.has` | 10 + 6 |
| `folded.calls` (stored replies of socket calls) | written by 5 folds (`foldJob`, `foldMsg`, `foldPause`, `foldWatch`, `foldReview`) |
| the outbox (`out.lead()`, `out.brief()`, `Message` facts) | 7 |

The folds also cross subsystems. The outbox's `foldMsg` ends merge watches (`watchEnded`), review requests
(`reviewEnded`), role-stop alarms (`roleAlarmed`) and role stops (`foldStop`), and writes stored replies.
`foldWake` writes into the outbox's `brief.stop`. A worker's stop lives in the watchdog's `folded.active`.

So a subsystem moved into its own package needs, at minimum: `Record`/`Key`/`FactData`/step constants from a package
below it, a host with `Record`, `Do`, `Wake`, `WhereIs`, project needs-you, stored reply, `Now`, `Logf`, and the
rule that its state is guarded by the host's `App.mu`, which it cannot see. The T-615 discipline already holds by
convention: the probe found writes to `folded.*` only inside fold functions (plus `watchEnded`/`reviewEnded`, called
from `foldMsg`). A package boundary would enforce what is already true. It does not fix a recurring defect.

## 3. Where the dependency cost is: the edges

What each importer uses from `app` (prod code; tests use the same groups):

| Importer | Ports (`app.go` types, `effects.go`) | Event log (`Record`, `Kind*`, `Step*`, `ReadMerges` …) | Socket protocol (`Call`, `Verdict`, `Status` …) | Core (`New`, `Options`, `Loop` …) |
|---|---|---|---|---|
| `herdr` | ✓ | | | |
| `setup` | ✓ | | | |
| `mcp` | | | ✓ | |
| `launch` | ✓ | (tests: `Record`, `OpenLog`) | ✓ | |
| `checks` | ✓ (`Scratch`, `GateOutcome` …) | ✓ (`Merge`, `ReadMerges`) | ✓ (`Backup`, `SocketPath`) | |
| `view` | | ✓ | ✓ | |
| `web` (tests) | | ✓ | ✓ | |
| `cmd/obor` | ✓ | ✓ | ✓ | ✓ |

The packages whose tests depend on `internal/app` today are `cmd/obor`, `app`, `checks`, `guard`, `herdr`,
`herdrtest`, `launch`, `mcp`, `run`, `setup`, `testkit`, `view` and `web`. Every one except `app` reaches it through
`herdr` (`testkit → herdrtest → herdr → app`); `guard` and `run` have no other link at all.

- Since 2026-10-15: 471 commits changed Go files. **94 changed only `internal/app`**, and each of those runs the
  tests of all 13 packages in the affected tier. Since v0.16: 39 of the 65 commits that touched `internal/app`.
- After ports, event log and protocol leave the core, `herdr`, `herdrtest`, `testkit`, `guard`, `run`, `setup`, `mcp`,
  `launch`, `checks`, `view` and `web` no longer depend on it. An app-only change then re-tests `app` and `cmd/obor`.
- Gate wall time today is bounded by `internal/app`'s own 156 s (Fable's measurement), so the gain now is CPU: about
  150 s per app-only merge (`checks` 80, `setup` 28, `launch` 26, `guard` 13, and the rest). After Fable's steps 1–2
  (`app` about 45 s, `checks` about 55 s), `checks` would be the critical path of an app-only merge. Removing it saves
  about 10 s of wall time there. Modest. The larger gain is structural, below.
- **Navigation.** Since 10-15, 156 commits changed adapter or view Go code without touching `app`. Today that work's
  contract is "grep `app.X`, land in one of 39 files". The exported step constants (44) are spread over 14 files, and
  the socket methods (21 `Method*`) over 11. After the move, each catalogue is one file: "what can the log say" and
  "what can a client ask serve".

The edges are clean leaves. `effects.go` uses nothing of the core except `Key`, in `JobID` (which stays in `app`).
`log.go` lines 1–417 use only `store`; its backup part (`backup`, `Backup`, `pruneStopFailure`) is serve and client
code and splits off. The client half of `status.go` (`Ask`, `Verdict`, `verdictAsk`, `askOnce`, `callDeadline`,
`SocketPath`, `NewCallID`) uses only `Call`, `Status` and the request/answer types. `ProjectStatus.Facts` is
`[]Record`, so the protocol sits above the event log.

## 4. The candidates, one by one

"Tasks" = substantive tasks since 10-01 that touched the candidate; "alone" = no substantive edit elsewhere except
wiring (`app`, `serve`, `status`, `project`); "with core" = also a substantive edit in the merge-loop core (§6).

| Candidate (files, lines) | Own state | What it calls in the kernel and in others | Who calls it | `mtask` fields read | Tasks / alone / with core | Verdict |
|---|---|---|---|---|---|---|
| stop/wake (`stopped`, `stopwake`; 725) | **none of its own**: `folded.active` (watchdog facts), `folded.woke`, `p.tasks`, the outbox's `brief.stop` | `do`, `record`, `wake`, `callProj`, `via`/`transmit`, `whereIs`, `shownAs`, `transcript`, `agentOf`, the watchdog's `fact`/`evidence`, `workerJob`, `foldRecord` | outbox (`roleTurns`, `roleWakes`, `foldStop`, `roleAlarmed`), watchdog (`turnStop`, `stops`), status, loop | agent, dispSeq, lane | 11 / 5 / 2 (5 with the outbox) | **No.** It is a feature across the watchdog and the outbox, not a subsystem |
| rolestop (296) | `folded.roles`, `roleFail`, `rvRead` (exclusive) | `do`, `whereIs`, `Agents.Stop`, outbox, review config/`reviewOpen` | loop, status, **outbox routes through it** (`shellPane`, `stoppedAt`, `startRoute`) | accepted, cand, state | 2 / 2 / 0 | **No** (small; coupled both ways with the outbox) |
| review (634) | `folded.review`, `reviewed`, `due`, `rv` (exclusive; ended by the outbox's fold) | `record`, `wake`, `whereIs`, `recordBrief`, `merges`, `callProj`, `Agents.Launch`, `specOf`, `waiting` (mergewatch) | loop, status, rolestop, outbox | state | 3 / 2 / 0 | **Not now.** Only its pure half (~250 lines: `reviewConfig`, `reviewOpen`, `previousReview`, `prepareReview`, `reviewBrief`) moves cheaply; revisit if it grows another claim or timer (Fable) |
| nightly + disk (371) | `p.night`, `diskAt` | `record`, `do`, `wake`, `effects`, `folded.active`/`has`, `p.running` (the scratch claim) | loop, status, project fold | none | 5 / 4 / 1 | **No.** After step 4 it has the smallest host surface of all, and still nothing to gain: two files that read top to bottom |
| outbox (`messages`; 973) | `p.out` (exclusive since T-610) | `record`, `recordSeq`, `do`, `wake`, `whereIs`, `via`/`transmit`, `inboxedAt`, `prompt`, `needsYou`, `answerPath`, `callProj`, rolestop, stopwake | **by recording a `message` fact** (inbound interface = the log), plus `lead()`/`brief()` | none directly | **27 / 12 / 11** | **Conditional yes, state and fold only** (step 6) |
| identity (`registry`, `sessions`; 524) | `p.reg` (its own type since T-600), `folded.sessions` | `a.obs`, `a.connected`, config names | 19 files through `whereIs` (stays in `app`) | agent | 10 / 5 / 5 | **Optional**, rules only (step 7) |
| mergewatch (281) | `folded.watches` | `record`, `wake`, `callProj`, `merges`, outbox, `specOf` | status, notify, review, **ended by the outbox's fold** | dropped, head, line, mergedSeq, state | 6 / 3 / 3 | **No** |
| herdr link (`panes`, `watchdog`, `notify`; 605) | `a.obs`, `a.spaces`, `a.connected`, `p.tasks`, `p.retry` | everything | serve | needs | 5 / 0 / 4 | **No:** this is the App |

Gemini's eleven groups are the right *topics*. As *boundaries* they fail in three ways. (1) Every group calls the same
kernel, so eleven packages means eleven host interfaces. (2) Its group 1 puts `log.go` (a true leaf) together with
`lanerows`/`specs`/`prompts`/`sessions` (folds and writes of `proj` under `App.mu`). Its group 10 joins stop/wake (no
state of its own) with rolestop (own state). (3) It misses the edges, which is where the dependency cost is. Fable's
advice did not map boundaries ("the proj split is already decided"). Its test-time levers are independent of this
work and should come first (see Ordering).

### `mtask`: split it?

44 fields, one per lifetime that the code already knows but does not show:

- **Task**: `task`, `sendbacks`, `specRev`, `dispSeq`. The submitted fold copies exactly these into the new `mtask`
  (`mtask.go:31-44`).
- **Dispatch** (until the submission): `lane`, `agent`, `disp`, `at`, `base`, `refused`, `prepares`, `starts`, `works`,
  `started`, `startAt`. Read by `dispatch.go`, plus `disp` by `lanerows`, `status` and `fetch`.
- **Attempt**: exactly the 21 fields `mtask.next` resets by hand (`mtask.go:216-222`): `line`, `fixed`, `fixes`,
  `fails`, `work`, `answer`, `decision`, `gates`, `reviews`, `resolves`, `pubs`, `submits`, `input`, `cand`, `gateRev`,
  `accepted`, `kind`, `job`, `patch`, `delivered`, `lost`.
- **Outcome**: `after`, `head`, `mergedSeq`, `dropped`, `resolved`. **State**: `state`, `left`, `needs`, `why`, `cause`.

The struct changed in 25 commits between 10-24 and 10-30, and `next()` in 3 since v0.16. A new attempt-scoped field
that `next()` forgets to reset is the error this shape invites. I found no merged bug traced to it, so the argument
is structural. **Answer: do not split `mtask` into separate types, maps or packages.** One `state` field drives one
machine from `waiting` to `merged`. Dispatch and merge are the same transition function, so a split would cut it in
two. Do group the attempt fields into an embedded struct, so `next()` resets them in one assignment (step 5). Field
promotion keeps every read (`mt.gates`) unchanged.

## 5. Ranked steps

Each step is one task, behaviour-preserving, with `make check` as the base oracle. A step that moves declarations
puts **type and const aliases** in `app` (one block), so code in `package app` and the 33 `app_test` files (969
`app.X` uses) compiles unchanged. Only importers outside `app` switch imports. Removing the aliases is a separate
mechanical task, scheduled when no lane is open in `internal/app` (see Concerns). Package names below are proposals.

| # | Step | Moves | Interface needed | Cost | Oracle | Buys |
|---|---|---|---|---|---|---|
| 1 | **Ports leaf** `internal/app/port` | `effects.go` except `JobID` (~330 lines); from `app.go`: `Pane`, `Snapshot`, `Event`+kinds, `Stream`, `Panes`, `Lane`, `Lanes` (~90) | none: declarations only | medium, mechanical: 379 `app.X` sites in ~25 files of `herdr`, `setup`, `launch`, `checks`, `cmd/obor` | `make check`, no test body changes beyond imports and qualifiers; `go list -test -deps ./internal/herdr ./internal/testkit ./internal/guard ./internal/run ./internal/setup` lists no `obor/internal/app`; module-map updated | `herdr`, `herdrtest`, `testkit`, `guard`, `run`, `setup` leave the affected set of every app-only change; the adapters' contract is one package |
| 2 | **Event-log leaf** `internal/app/event` | `log.go` 1–417 (`Record`, `Key`, `Intent`, `FactData`, `Kind*`, `Class*`, `Log` + methods, `OpenLog`, `ReadLog`, `Dump`, `RecentEvents`, `DBFile`); `Merge`/`ReadMerges` (from `nightly.go`); the 44 exported `Step*` and the `*Task` keys; the fact data types outside readers decode (`Pause`, `Message`, `CauseHeld`) | none | medium: 175 outside sites; `export_test.go`'s `Refuse` reaches `p.log.mu`/`refuse`, so `Log` needs an exported test hook. Make it a **field**, not a method: `make deadcode` would flag a test-only function | `make check`; `log_test`, `obor log`/`Dump` output unchanged on the existing fixtures; diff of moved declarations is the package line and qualifiers only | the log's vocabulary in one file (today 14); `checks`/`view`/`web` read the log without the core; prerequisite for 3, 6, 7 |
| 3 | **Protocol leaf** `internal/app/wire` | `Call`, `request`/`answer` (exported for `Listen`), `Ask`, `Verdict`, `verdictAsk`, `askOnce`, `callDeadline`, `lostReply`, `absent`, `SocketPath`, `NewCallID`; all 21 `Method*` (from 11 files); `JobReply` (`jobs_contract.go`); `Status`, `ProjectStatus` and the `*Status`/`RunningStep`/`PendingStep`/`Scratch*` types; `ResumeArgs` + `Resume*`; the `Backup` client and `BackupDir`. `Listen` and the handlers stay | none | medium: 310 outside sites in ~40 files (`cmd/obor`, `mcp`, `launch`, `checks`, `view`) | `make check`; moved structs byte-identical apart from the package line (JSON tags verbatim); MCP and CLI tests green | `mcp`, `launch`, `checks`, `view`, `web` leave the affected set (with 1–2): an app-only change re-tests `app` + `cmd/obor`; "what can a client ask serve" in one file; `status.go` splits into server and client |
| 4 | **One owner for a project key's needs-you** (in `app`) | five copies of "held? same reason? → resumed, then needs-you": `loopStatus` (`loop.go:496`), `msgNeedsYou` (`messages.go:669`), `nightlyNeedsYou`/`handGreen`/`nightlyVerdict` (`nightly.go`), `diskIsLow` (`disk.go:43`), the self-restart's (`steps.go:105-125`) → one `settleNeeds(p, task, reason)` ("" ends the one in force; returns whether the log took it). Callers keep their own decision: disk's "the reading decides", nightly's `unsettled`, `loopStatus`'s two sources and `loopMu` | the helper is the "project needs-you" a later host would offer | simple to medium: the five differ in small ways, each kept at its call site | `make check`; `disk_test`, `nightly_test`, `selfrestart_test`, the `loopStatus` and msg-wait tests unchanged | AGENTS "one owner per helper"; the smallest kernel API any split needs |
| 5 | **`mtask` by lifetime** (in `app`) | an embedded `attempt` struct holding the 21 fields `next()` resets; `next()` becomes `mt.attempt = attempt{line: note, answer: answer}` plus `state`; the task-lifetime survivors of a submission named in one place. `task`, `lane`, `agent`, `attempt` (N), `state`, `needs`, `left` stay top-level | none | medium-simple: reads unchanged (promotion); 4 prod and 49 test composite literals, of which ~13 field uses (`gates`, `job`, `work`, `line`, `input`, `reviews`) nest | `make check`; `states_test` and the loop and replay tests unchanged; review: the group holds exactly `next()`'s former list, nothing added | a new attempt field is reset by construction; the struct reads as its lifecycle |
| 6 | **Outbox state and fold** in its own package (conditional, after 2 and 4) | `outbox`, `queued`, `brief`, `roleStop`, the data types, `foldMsg`, `settleLine`, `queuedLines`, `needsLineWaits`, `members`, `briefStatus`/`messageStatus` (~400 lines). `deliver`/`route`/`deliverLine`/`deliverLead`/`leadPost`/`needsLines`/`msgNeedsYou`/`sendCall`/`briefCall`/`recordBrief`/`logWorked`/`sawWork` stay (~550). No lock in the package: under `App.mu` as today | queries (`Lead`, `Brief`, `Queued`, `Waits`) + `Fold(r)` returning what the record ended (a watch, a review, a role alarm, a stored reply). The coordinator applies those, in `foldRecord`'s current order | hard: untangling the cross-folds; 16 test files read `p.out.*` (48 sites) and switch to the queries | `make check`; replay tests (`endedreplay`, `messages`, `leadbatch`, `sendback`) unchanged apart from accessors; afterwards a fold-only unit test becomes possible | compiler-enforced ownership of the most-edited auxiliary state (27 substantive tasks since 10-01, 12 outbox-only), including its subtlest rules (T-575/T-580 supersede, T-654 batches). Do it if the outbox keeps leading the churn after 1–5 |
| 7 | **Identity rules** in their own package (optional, after 1) | the `registry` type and its pure methods (`recordStart`, `start`, `whoIs`, `reconcile`, `logOwners`, `crossCheck`, `proof`), `claim`, `owner`, `startRow`, `rank`, `termOf`, `sameSession` (~250). `whereIs`, `shownAs`, `terminal`, `why`, `App.reconcile`, `candidates`, `recordedSid`, `Where`/`State` stay, so the 19 user files do not change | one `Lookup(agent)` for `whereIs` (it reads `reg.ambiguous`/`agents`/`owners` today) | simple-medium | `make check`; the 46 `ident_test` tests (harness-driven) unchanged | "only this file names its maps" becomes a compiler rule; reconcile's tie rules (RV-53-verify V1) can get table tests without a harness |

**Ordering.** Fable's steps 1–2 (short deadline on a missing serve; `t.Parallel()` in `internal/app`) come before
all of these. They shorten the oracle every step here leans on, and they make the gate gain of steps 1–3 visible.
Steps 1 → 2 → 3 are sequential (each later leaf imports the earlier). Steps 4 and 5 are independent of them and of
each other. Step 6 needs 2 and 4; step 7 needs 1. Fable's §4.2 note (serve hands its rows to the `LaneTable` instead
of reading its own `lanes` table back) fits naturally into step 1, which touches `Lanes`.

## 6. What not to split, and why

- **The merge-loop core**: `loop`, `mtask`, `states`, `steps`, `scheduler`, `settle`, `finish`, `fold`, `jobstart`,
  `jobs`, `verdict`, `answer`, `fetch`, `dispatch`, `pause`, `specs`, `prompts`, `prompt`, `lanerows`, `project`.
  That is 20 files and 5,398 lines. They are one state machine over `mtask.state`: `dispatch` writes the same `mtask`
  (`foldDispatch`, `foldWaiting`); `verdict` and `fetch` are its socket entry points; `states` (the table), `mtask`
  (the fold), `loop` (the picks) and `steps` (the effects) are one transition function in four files. 77 of the 93
  substantive core tasks stayed inside it. A split would turn `mtask`'s 44 fields into an exported API between
  packages. Its replay (`settle`, `foldRecord`, `do`'s unwritten results) is crash-safety logic that must see all of
  it.
- **`mtask` as separate types or packages**: one machine (see §4). Group by lifetime inside it (step 5).
- **stop/wake**: it has no state of its own. A worker's stop is a watchdog fact; a role's stop is the outbox's brief.
  Split it and both of those would follow it.
- **rolestop, mergewatch, review (whole), nightly/disk**: each has exclusive state in one file, is small and is seldom
  co-edited with the core (2/0, 6/3, 3/0, 5/1 tasks / with core). A package would cost a host interface (at least five
  kernel calls each) and lock discipline across a boundary, and would buy nothing a file does not give.
- **The herdr link, watchdog, notify, serve, the status server**: they *are* the App (`obs`, `spaces`, `connected`,
  the event entry point, `Listen`).
- **`proj`'s live fields regrouped per subsystem**: already single-file each (`rv`/`due` only in `review.go`,
  `roleFail`/`rvRead` only in `rolestop.go`, `diskAt` only in `disk.go`). Nothing to gain after T-600/T-610/T-615.
- **Fewer mutexes**: no step here should change the lock set. `App.mu` stays the only lock over state, and a moved
  type documents "under `App.mu`" as `registry` does today. Gemini's mutex concerns are Fable's to judge, not a reason
  for a split.

## 7. What it buys, honestly

- **Edges (1–3)**: measurable. 13 → 2 packages re-tested per app-only change (94 such commits since 10-15). Adapter,
  hook and view work reads about 1,000 lines of contract in three packages instead of searching the core. Two
  catalogues (log vocabulary, socket methods) each become one file.
- **In-package refactors (4–5)**: fewer error-prone edits. One owner for a rule copied five times; an attempt's reset
  that cannot miss a field.
- **Inner packages (6–7)**: compiler enforcement of ownership the code already keeps by convention. An agent working
  on the outbox still reads the outbox, `record`/`do` and `whereIs`, so the token saving per task is small. The gain
  is in what it no longer has to check (who else writes this state), and what a reviewer can trust.
- **Not bought by any of this**: wall time of the gate today (bounded by `internal/app`'s own tests until Fable's
  steps land), and fewer locks.

## Concerns

- **Aliases are two names for one type.** They are what lets steps 1–3 land without touching the 1,300 to 2,500
  in-package uses (and without conflicting with every open lane in `internal/app`). Keep them only until a quiet
  window, then remove them in one mechanical task, or decide explicitly to keep them (Go's own `os.FileInfo =
  fs.FileInfo` is precedent). The lead's call; I recommend removing them.
- **Each move conflicts with any lane open in `internal/app` or the importers.** Dispatch them when no such lane is
  open.
- **Test-only hooks and `make deadcode`.** Moving `Log` out needs its crash-test hook (`Refuse`) exported. A
  test-only exported function would be dead code to the tool, so use a field.
