# Start identity: one decision before a start replaces or adds an agent

Advisor, 2026-11-11, for the lead. Read: `docs/reviews/RV-190.md`, `RV-190-verify.md`, `DD-v0.38.md` F1, BACKLOG
Concerns (lead 11-11), `docs/plan/terminal-adapters.md`, and the code cited below at `30995fc8`. Nothing was run:
this is a design, the probes it relies on are the reviews'.

## Recommendation

1. **The identity is serve's table, and nothing else is an identity.** Serve answers one new socket request,
   `where`: "which pane runs agent X, and who runs in pane P". It is `whereIs` and `WhoIs` as they are
   (`internal/app/registry.go:38-52`, `identity.go:132-135`), with their session, start-row and label proofs. No
   second implementation of the proofs is written for the CLI.
2. **The decision is one function in `internal/launch`, `Locate`,** because every start already passes through
   launch: `Launch` for a restart and for all of serve's starts, `Start` for setup's (`launch.go:470`,
   `start.go:41`). `Locate` asks serve, adds one fact from the terminal (the pane where the terminal itself holds a
   live agent under X's name), and returns where X runs, where a start of X belongs, or a refusal. It runs under the
   agent's restart lock, before the stop, the new pane, the label, the env line and the session record.
3. **Serve down is not "trust the label".** Without serve the two strongest proofs are missing, so a start that
   would add an agent is refused while any live agent in the project is not accounted for by a label.
4. **The adapter stops deciding.** `Socket.Start` keeps only the clear-and-retry of a name no live agent holds; a
   failed holder lookup is an error, never "nobody holds it" (V1).

This adds one request, one port method and one function. It deletes the stopgap's refusal plumbing and six
label-as-identity checks spread over three packages. The code size is about even; the gain is that the question is
asked once, in one place, before anything is touched.

## Why the seven strikes are one bug

Three parties answer "does X already run" and none asks the others:

| Party | Its answer | Where |
|---|---|---|
| CLI and launch | the pane labelled X | `cmd/obor/lead.go:87-96,102`, `internal/launch/launch.go:333-340`, `lane.go:27`, `internal/setup/agent.go:219-252` |
| serve | session > start row > label | `internal/app/identity/identity.go:272-283`, `registry.go:110-134` |
| herdr | its own agent name, at `agent.start` | `internal/herdr/socket.go:604-630` |

The third answer arrives last, inside `H.Start` (`start.go:195`), after the stop (`launch.go:602`), the env line
(`start.go:55`), the label (`start.go:61`) and the session record (`start.go:76`). So every fix so far chose
between refusing too late (DD F1, V2, V3) and not refusing (T-1254, RV-190 #1). A fix inside the adapter cannot do
better: by the time it runs, the destructive steps are done.

## The decision

### Inputs

| Input | Source | Proves |
|---|---|---|
| serve's `where` answer for X, and for the target pane | the socket; 3 s bound, as `restart_job` (`cmd/obor/restart.go:96`) | identified pane and proof, ambiguous panes, or none; who the target pane runs |
| the pane list, read now | `H.Panes`, which launch reads already (`launch.go:313`) | which panes run an agent, their labels and cwds; where a start belongs |
| the terminal's holder of X's name | new `Named(name)` on the port; herdr: `agent.get` by name | a live agent the terminal started as X, the weakest claim |
| the config | `AgentLookup` | which labels name another agent |

Serve's answer counts only for a pane that runs an agent in the list read now. That is `whereIs`'s own condition
(`registry.go:47`) applied to the fresh list, so a lead quit a second ago and started again with `obor lead` is not
refused on a stale observation.

herdr's name stays out of serve's table: herdr drops it after a long first turn (`docs/decisions.md:47`), so as a
table proof it would make an agent flap between identified and not. At a start it is read once and only ever adds a
refusal, so a dropped name costs nothing.

### Answers

| Answer | When | A start then |
|---|---|---|
| **runs** in pane Q (session, start, label or name) | serve identifies Q; or serve finds none and the list read now shows X's label on one live pane Q that serve gives to no other agent; or, failing both, the terminal holds X's name in a live pane Q | goes only if its target is Q (a restart in place); any other target is refused, naming Q and its proof |
| **ambiguous** | serve says so; or serve identifies Q and the terminal holds the name in another live pane | refused with `identity.NotIdentified`'s line (`identity.go:138`) |
| **free**, home H | nothing proves X; H is the pane labelled X at its shell, if any | goes into H; `--pane` elsewhere is refused while H exists; no H and no `--pane`: a new pane (lead, setup, drive) or `no pane labelled X` (restart) |
| **cannot tell** | serve absent, not connected to the terminal, or not serving the project; or `Named` failed | the serve-down rule below; a failed `Named` is a refusal |

In every case the target pane is checked by the same answer: a pane that serve identifies as another agent, or
that is labelled for another agent the config names, is no place for X. That is today's rule at `launch.go:333`
and `setup/agent.go:249`, asked once. The request therefore names the panes launch needs an owner for (the target,
and a live pane labelled X): a label change gives serve no event, so launch reads labels from its own list and
serve says whose those panes are.

### Serve-down rule

Decided from the pane list, the config and `Named` alone:

- X's label on exactly one live pane, or the terminal's name holder: **runs** there. Two: **ambiguous**.
- Otherwise, a start is about to add an agent. It goes only if every other live agent pane of the project (cwd
  inside it, `inProject`'s rule, `launch.go:404`) is labelled for another agent the config names. Any pane that is
  not (no label, a label like `notes`) refuses it: `cannot tell whether the claude in pane w1:p1 (label notes) is
  pj-lead: obor serve is not running. obor serve restart, then retry; or obor restart pj-lead --pane w1:p1 to
  replace it`.
- A restart in place of a live labelled pane always goes: it replaces one agent with one.

This refuses the relabelled lead and the name-dropped lead with no proof code in the CLI. Its cost is a false
refusal when a person's own unlabelled agent sits in the project while serve is down; the line names the pane and
both ways out.

### Who owns what

- `internal/app`: the identity (`where`). One table, one set of proofs, row 48 unchanged.
- `internal/launch`: the decision (`Locate`), its one refusal line, the lock. The pure part, answer plus panes
  plus holder plus target to a result, is a function with no I/O, so its tests are a table.
- `internal/herdr`: the terminal's fact (`Named`) and a `Start` that reports, never decides.
- Serve's own starts ask through the same request from inside serve, as a start's confirmed-start report does
  today (`start.go:313`). No second path for "serve asks itself".

A shared package that both sides compute with was the alternative. It fails on its inputs: start rows live in
serve's fold (`fold.go:18-38`) and the table resolves all agents jointly (`identity.go:168-216`), so the CLI would
fold the log and rebuild the candidate set itself: a second caller that can drift from the one that addresses
wakes and stops.

## What each start path does with the answer

| Path | Today | With `Locate` |
|---|---|---|
| `obor lead` (`lead.go:82-119`) | label loop, then `PaneOf`, then a new tab | runs: `OK pj-lead runs (pane Q)`, with `labelled notes` when the label differs, nothing touched. free with home: restart there. free without: new tab. ambiguous or cannot tell: FAIL, no tab made |
| `obor restart X` | the labelled pane, else FAIL | runs in Q: restart Q, also when Q's label differs; the start puts the label back and says so. free: the home pane |
| `obor restart X --pane P` | label checks in `resolve` | runs in Q ≠ P: refused before the stop, naming Q. P runs another agent: refused |
| `obor restart X --stop` | `resolve` | the same target as a restart: the pane X is proven in |
| `obor setup role` / `pane [--start]` (`agent.go:192`) | `Agents` loop, label checks, then `Place` | `Locate` before `Place` and before the label of a pane without `--start`; runs: `OK … already running`; refused: no pane made |
| `obor drive` (`agent.go:78`) | as setup | as setup; the "one agent in a lane" check (`agent.go:221-226`) stays, it is lane occupancy |
| dispatch, worker start rule (`workerstart.go:58,301`) | `placement`, then `Launch`; name held is a needs-you after the stop | runs outside the placed pane: `port.Refused` before the stop, one needs-you, no counted tries |
| integrator job, review, brief start (`verdict.go:459`, `review.go:438`, `messages.go:481`) | `Launch` with no pane: the label picks it | the identified pane, relabelled or not; never a second pane |
| restore (`restore.go:173-213`) | a labelled pane at its shell is restarted | X proven live elsewhere: refused, one line in `restore.log` |

`H.Start` has one production caller (`start.go:195`), so "other consumers of the terminal port" in the reviews'
Extent lines is covered by the launch rows.

## Case → test

Tests are decisions, so unit tests: one table over the pure decision (`internal/launch`, `TestLocate`), one row set
at serve's request (`internal/app`, `TestWhere`), and one "a refusal touches nothing" assertion per entry point.
Names are proposals.

| # | Case (source) | Today | With the decision | Test |
|---|---|---|---|---|
| 1 | Live lead, label `notes`, herdr name kept, session recorded; `obor lead` (RV-190 #1) | new tab placed and labelled, then refused | `OK pj-lead runs (pane w1:p1, labelled notes)`; no pane, rename, input or `agent.start` | `TestWhere`: session beats label (the fixture of `TestIdentSessionBeatsLabel`); `cmd/obor` `TestLeadRunsByItsPaneLabel` third case rewritten |
| 2 | The same, herdr name dropped (verify #1, `TestRV190LeadNameDropped`) | second lead starts | as 1: the session proves it | `TestLocate`: identified by session, no name holder |
| 3 | 2 with serve down | second lead starts | refused by the serve-down rule, naming `w1:p1` | `TestLocate`: cannot tell, unaccounted live pane |
| 4 | `obor restart pj-l-01 --pane w1:p8`, unlabelled `w1:p3` holds the name (DD F1 row 1) | env line typed, `w1:p8` labelled, then FAIL with the failing command | refused first, naming `w1:p3` and its proof (start row or session; name when neither) | `TestLocate`: runs elsewhere by start, by name; `internal/launch` `TestRestartRefusedTouchesNothing` |
| 5 | 4 with `--force`, `w1:p8` runs an unlabelled Claude (DD F1 row 2) | that Claude is stopped, then FAIL | nothing stopped | the same launch test, `--force` row |
| 6 | Every later `obor lead` restarts the labelled new pane and fails (DD F1) | loop until the label is put back | no new pane is ever labelled; 1 holds each time | covered by 1 |
| 7 | Dispatch into a pane while X is live elsewhere (DD F1, RV-190 Extent) | stop and start per counted try, then needs-you | one needs-you, no stop | `internal/app` `TestDispatchStartRefusedWhileALiveAgentHoldsTheName` changed to `port.Refused`, asserts no stop |
| 8 | `agent.get` fails after name-taken; the live holder's name is cleared, a second agent starts (V1, `TestRV190HolderLookupFailure`) | duplicate | at the decision: `Named` fails, refused before anything. In `Start`: the lookup error is returned, nothing cleared or retried | `internal/herdr` `TestSocketStartNameTaken`: lookup-error row, answer-without-pane row; `TestLocate`: holder lookup failed |
| 9 | A refused fresh start's new session becomes current (V2, `TestRV190RefusedFreshStartReportsSession`) | recorded before the refusal | the refusal precedes `recordSession` | `TestRestartRefusedTouchesNothing` asserts `Report` is not called, fresh row |
| 10 | Following the hint leaves two panes labelled X (V3, `TestRV190PutLabelBackHint`) | duplicate label, next restart refused | no label is assigned before a refusal; the line's command is `obor restart X --pane <holder>`, which works and puts the label back | `TestLocate`: runs in Q with another label, target Q goes; launch start test: the label is restored |
| 11 | `obor setup role` / `pane --start` while X is live in a relabelled pane (Extent) | a new pane, then refused | `OK … already running`; with `--pane` elsewhere: refused, no pane made | `internal/setup` `TestRoleStartRefusedWhenALiveAgentHoldsTheName` changed: asserts no `Place`, no rename |
| 12 | Integrator fresh job start, integrator live in a relabelled pane (Extent: role/job starts) | `no pane labelled`, or a second pane | fresh start in the identified pane | `internal/app` job-start test: `Launch` reaches the identified pane |
| 13 | Restore with X live elsewhere (Extent) | restarted in the labelled shell | refused, logged | `internal/launch` restore test, one row |
| 14 | Two panes prove X; or serve identifies Q and the terminal names another live pane | varies | ambiguous, refused | `TestLocate`: two rows |
| 15 | Serve's answer names a pane that runs no agent now | — | free | `TestLocate`: stale answer |
| 16 | Two starts of X at once (two `obor lead`; a restore and a restart) | both may pass a check | the second waits on the lock, then finds X running | `internal/launch`: `Locate` waits for a held restart lock (one test; the setup path takes the same lock) |
| 17 | X's label on a live pane whose cwd is outside the project | `obor lead` FAIL with the prefix hint (`lead.go:88-91`) | the same line, from `Locate` | `TestLocate`: foreign holder |

Deleted with the mechanism: `TestRestartAgentNameTaken`'s second subtest (`launch_test.go:1975`), the stopgap's
assertions in `TestLeadRunsByItsPaneLabel` (`lead_test.go:134-154`). Rows 4, 5, 9 share one test: a refusal that
leaves no call is one behaviour.

## Task split

A and B are independent; C needs both; D needs C. C and D land in one wave with no release between them: after C
alone, restart and serve ask `Locate` while lead and setup still trust labels, which is today's split moved.

| Task | Does | Files | Deletes | Tests |
|---|---|---|---|---|
| **A. serve answers `where`** | request `where{agent, panes}`; reply: state, pane, source, label, the ambiguous panes, why, and the agent each named pane runs. Under `mu`: `reconcile`, `whereIs`, `WhoIs`. "not connected" is its own state | `internal/app/wire/request.go`, `call.go`; `internal/app/registry.go`; `status.go:411-446` | nothing | added: `TestWhere` (synctest rig), 5 rows |
| **B. the terminal reports, never decides** | port: `Named(ctx, name) (Pane, error)`, `NotFound` when no live agent holds it. `Socket.Start`: a holder lookup error is returned; an answer with no pane id is an error; a live holder elsewhere stays `heldName` (a lost race now) | `internal/app/port/effects.go:90-95,116-126`; `internal/herdr/socket.go:582-640`; `internal/herdr/herdrtest`; `internal/testkit/terminal.go` | `heldName.Label` and its `paneAt` lookup (`socket.go:615-618`): the label is launch's to read | changed: `TestSocketStartNameTaken` (+2 rows); added: one contract row for `Named` |
| **C. `Locate` in launch; restart, stop and serve's starts** | `internal/launch/locate.go`: the pure decision, the I/O around it, the one refusal line. `Locate` takes the agent's restart lock and returns holding it; `Launch` and `Stop` call it first and release after their last step. A held name from `Start` becomes `port.Refused` with the same line | `internal/launch/locate.go` (new), `launch.go:288-372,470-498`, `start.go:99-143`, `stop.go:27-45`, `lane.go`; `internal/app/workerstart.go:367-376` | `nameHeldFail`, `nameHeldLine` (`start.go:124-143`); `resolve`'s two label checks (`launch.go:333-340`); `startRefused`'s name branch (`workerstart.go:374-375`); the name paragraphs of `Start`'s and the package's doc | added: `TestLocate`, `TestRestartRefusedTouchesNothing`; changed: row 7's; deleted: as listed above |
| **D. setup, lead, drive** | `port.Starter` gains `Locate`; `setup.start` calls it before `Place` and before a label, and releases its lock after the start; `obor lead` branches on its answer | `internal/app/port/effects.go:310-314`; `internal/setup/agent.go:214-256`; `cmd/obor/lead.go:82-134` | setup's `Agents` label match and its two label refusals (`agent.go:227-232,246-252`); lead's label loop and `leadOurs` (`lead.go:87-96,122-134`: a twin of `inProject`) | changed: rows 1 and 11's; none added |

B, C and D touch how an agent is launched: `make conformance TIER=live TURN=1 KIND=<kind>` for claude, codex and
opencode before `task_done` (AGENTS.md). Per DD-v0.38's note for step 6, C's and D's specs should list each
message they change and what it becomes: the refusal line, `OK … runs`, and the two `already labelled` lines.

The lead's own edits after D: `docs/design.md:775-780` ("the CLI finds a pane by its label, so it works while
serve is down" is no longer the rule), `docs/state-model.md` row 48 (`where` as a reader), the BACKLOG concern.

## Owner decisions

| # | Decision | Recommended | If not |
|---|---|---|---|
| O1 | Serve down: how strict | the rule above: an unaccounted live agent in the project blocks a start that adds one | label only, as today: cases 3 and the serve-down half of 4 stay open. Or refuse every start while serve is down: simplest, but `obor restart` then needs serve |
| O2 | herdr's agent name is read once, at the start decision, as the weakest claim | yes, while herdr's agent layer starts agents. `docs/design.md:776` then reads "never read by the table" | never read: `Start` clears any holder and retries. `Named` and case 8's first half go, and a live X that only herdr's name proves is duplicated |
| O3 | `obor restart X` restarts X in a pane a person relabelled, and puts X's label back | yes; `obor lead` on such a pane changes nothing | refuse with the pane named: the person relabels by hand first |
| O4 | `docs/design.md` drops "works while serve is down" for the CLI's pane lookup | yes, it follows from O1 | — |

## Risks

- **Serve's table is as fresh as its stream.** A label change gives no event; the session and start proofs do not
  depend on labels, and launch reads labels from its own list. An agent started by hand a moment ago can be
  missed for that moment, as it can today.
- **The residue.** A live X with no session match, no start row, no label and no herdr name is invisible to every
  party when serve is up. That needs a CLI-started agent whose kind reports no session, relabelled, after herdr
  dropped its name. Flagged, not expanded: CLI starts write no start row, so the lead's only strong proof is its
  session. The `started` request (`start.go:306-318`) already reports every confirmed start; carrying the pane in
  it would give CLI starts the start proof.
- **A fresh start that fails after the decision** still leaves the id obor chose as the current session
  (`start.go:74-77`); a later restart says "never prompted: starting fresh". Unchanged and outside this question:
  no live X exists at that point, so no session proof is broken.
- **The lock's order changes.** `Launch` takes the restart lock after `resolve` today (`launch.go:478,494`); the
  decision must sit inside it. The lock's key needs the lane, which the config gives before any pane is read.

## Bearing on D3

It bears on D3, and it does not wait for it.

`Named` and `Start`'s name handling are herdr's agent layer. Put `Named` on the agent-layer side of the port, so
the plan's step 6 moves it into `AgentStarter` with the rest. If D3 becomes "obor owns the agent layer", a start
is a command line through `Mux`, there is no terminal name to collide with, and `Named`, `heldName` and O2 are
deleted with `agent.start`: the decision is serve's table and the serve-down rule alone. If D3 stays "herdr owns
it", every terminal with an agent layer brings its own uniqueness rule, and `Named` is how the decision sees that
rule before acting instead of after.

Step 7 names "the start record obor wrote for the pane" as the source of which kind runs. That is the start row
for every start, the same record the residue above lacks. If the owner takes D3 toward obor's layer, that record
and this decision's start proof should be one thing, written once.
