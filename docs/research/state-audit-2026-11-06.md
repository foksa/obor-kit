# State-machine audit (2026-11-06)

The owner's audit of serve's explicit and implicit state machines, moved here from `docs/state-model.md` (the
contract keeps one line per changed rule). Its code is preserved on branch `audit/state-2026-11-06`; it lands through
T-938 (SM-01, SM-03, SM-04) and T-939 (SM-02 as a fact receipt, not schema 13). Advice: `docs/research/state-audit-advice.md`.


This file is the combined inventory, transition reference and local issue ledger. The numbered transition rows below
remain stable because code cites them. The [additional machines](#additional-explicit-and-implicit-machines) cover
state carried by flags, counters, timers, files and UI requests as well as named enums. Sources are implementation
functions, not assumptions about a live project's database or herdr panes. Tests use temporary projects and stubs.

An invariant below is a required property, not a claim that every possible interleaving has been verified. The
issues below have concrete reachable paths; accepted limitations and unverified risks are listed separately. No
external tracker issues were created: SM-01–04 are the related fix groups in this file.

| Priority / issue | Confirmed invalid transition before the fix | Impact | Resolution |
|---|---|---|---|
| **P1 · SM-01: preserve a lane when Git cannot prove it safe** | `dropping → lane.free success → dropped` after the unmerged-history read fails | A plain drop can detach the lane and delete a branch containing unmerged work without `--discard` | Fixed: propagate branch/history errors; distinguish exit 1 from fatal predicate errors; regressions cover failures with and without discard and a healthy retry |
| **P1 · SM-02: durable, serialized spec call receipts** | `spec A/s1 → spec B/s2 → restart → retry s1 → spec A rev 3` | An old request overwrites newer worker instructions and file scope; concurrent retries can also disagree about the original reply | Fixed: schema 13 `spec_calls`, committed in the spec transaction; receipt lookup through commit serialized with task_done; restart, concurrent retry, migration and rollback regressions |
| **P2 · SM-04: terminal task beats a stale submission** | `working → outside merge detected → merged → late task_done → submitted` | A terminal task is reopened and loses its merge metadata; if its lane was reused, a late fact can also overwrite the new assignment | Fixed: ignore late submitted facts before any projection updates and report superseded; deterministic concurrency/replay regression |
| **P2 · SM-03: fold durable effect metadata** | `merged → after_merge/agent.stop results` leaves the live idle key at the merge, but replay advances it to the final result | Live and replay disagree on which idle epoch was acknowledged and when it began | Fixed: `App.do` folds the log's actual result record, including sequence and timestamp; live/replay regression |

### SM-01 evidence and fix boundary

In `internal/checks/lane.go`, `Lane.Free` originally ignored `rev-parse` errors, treated every ancestry error as
"unmerged", and discarded the error from `git log`. Reachable sequence: a clean lane with an unmerged commit;
`git status` succeeds; `rev-parse` succeeds; `merge-base --is-ancestor` exits 1; `git log` fails with empty stdout;
`commits == ""` bypasses the refusal; checkout detaches at the target and `branch -D` deletes the task branch.
The drop's kernel then records successful free and drops the lane projection. No concurrent worker is needed.
The commit may remain recoverable through reflogs, but its named branch and active lane are lost.

`TestLaneFreeHistoryFailurePreservesBranch` first failed with `history failure: <nil>; want a retryable error`.
It now injects fatal branch/ancestry errors and failed history reads, asserts the original branch and HEAD survive,
and retries with healthy Git (refusal without discard, successful cleanup with discard). The fix reuses `exit1`,
the existing predicate-status helper; it adds no second error classifier. Errors remain pending in `freeStep`,
use the normal counted retry/needs-you path, and cannot produce a successful free result.

### SM-02 evidence and fix boundary

`internal/app/specs.go`, `specSet`, used `proj.replies`, a live map initialized empty by `load`. Only text/revision
were durable. After s1 writes A and s2 writes B, a restarted serve receives s1 again (the wire retries the same call
after a reset): `SetSpec` sees different text and writes A at revision 3. The reproduction returned `SET T-1 rev 3`
instead of s1's `SET T-1 rev 1` and read back A. Before the fix, receipt lookup also preceded `submitMu`, and receipt
publication followed its release: simultaneous s1 calls could both miss it and return SET versus UNCHANGED.

`TestSpecSetRetryAfterRestartPreservesNewerSpec` proves the destructive restart path and now keeps B at rev 2.
`TestSpecSetConcurrentRetriesShareReply` checks simultaneous callers all receive the original SET reply.
`TestSpecCallReceiptMigrationAndRollback` upgrades a schema-12 fixture, fails the receipt insert, proves both spec
and receipt roll back, and checks an UNCHANGED receipt survives reopening the database.

`store.SetSpecCall` writes spec and receipt in one transaction. `specSet` takes `submitMu` before receipt lookup
and keeps it through validation and commit, so task_done and spec mutations have one order. The volatile spec
reply cache is removed; ordinary drive-spec creation still uses the common `setSpec` transaction helper through
`SetSpec`. Schema 13 adds one table and preserves existing specs. A schema-12 binary refuses that newer database:
deploy the matching binary when opening it. Calls completed by older code have no reconstructible receipts; the
new guarantee applies to calls committed with this fix.

### SM-03 evidence and fix boundary

`internal/app/loop.go`, `App.do`, previously constructed a result record from raw JSON, with Seq 0 and the current
clock, instead of folding the record the log wrote. `foldLoop` advances `afterSeq/afterAt` only for a newer sequence,
so successful production effects never advanced the live idle key. Tests that injected results through `record`
supplied real sequences and missed it. This is independent of a crash: a restart exposes the discrepancy by replay.

`TestEffectResultIdleEpochMatchesReplay` runs both after-merge effects through `do`. Before the fix it observed
live idle key 2 (merge) versus replay key 6 (stop), with a two-minute time difference; afterward both identify the
final effect. `event.Log.Result` supplies the stored record. The unwritten-result recovery path already folds the
actual record returned by Append and remains unchanged; effects still run outside the app lock.

### SM-04 evidence and fix boundary

`taskDone` in `fetch.go` read the lane and checked the task, then recorded submitted without revalidating its
decision against the merge loop. `drive` independently checks `Scratch.Landed` for a working task, recording merged
when the owner has merged it outside obor. Interleave those after task_done's checks and before its append:
`foldLoop` protected only dropped/dropping tasks, so submitted replaced the merged task, including on replay.
`TestTaskDoneCannotReopenConcurrentOutsideMerge` first failed with a successful late reply and `submitted` both
live and replayed. It uses the existing decision hook to run the actual outside-merge detection at that cut.

`foldRecord` ignores submitted facts after any ended phase or while dropping, before updating task, call or
lane projections, so an old log containing the bad sequence replays as terminal. The regression checks the real API
interleaving and a legacy late fact after the lane was reused: it must not steal the new task's assignment live or
on replay. task_done checks whether its submission produced a receipt and reports superseded if the terminal fact
won. A fresh dispatch still explicitly reopens an ended task. This adds no phase, flag or effect, and preserves the
existing drop/submission ordering rather than holding the app lock through their appends.

Validation (2026-11-06): `make check-affected` passed on the final code: build, gofmt, `go vet ./...`, all 21 affected
packages (0 failures), and the pinned dead-code check. This includes the new regressions and the existing
`TestDroppedEndsTheTask` drop/submission interleaving. `git diff --check` passed. The gate used a temporary GOCACHE
and permission to bind fixture Unix sockets; all repositories and herdr behavior in these tests are fixtures.

There is one `obor serve` per project (T-522): started in the project, it serves that project only. At its start it takes the flock
`<root>/.obor/state/serve.flock` (it names the serve's pid; the log's one writer), refused with `FAIL obor serve
already runs for <root> (pid …)` while another serve holds it, and listens on `<root>/.obor/state/serve.sock` (a path
over the unix socket limit, 104 bytes on macOS, fails the start naming it). Every CLI dials the socket of the project
it runs in (a lane worktree's is its main checkout's); `obor send` dials the target's. Both go when serve ends.

## Additional explicit and implicit machines

These tables complement the numbered task transitions, not replace them. State names in backticks are code names;
other names describe combinations of existing fields. "Unchanged" includes a refusal; it never licenses an effect.

### Task phase and block product

Owner: `states.go`, `mtask.go`, `block.go`, `stopstep.go`, `dispatch.go`, `drive.go`. The complete phase set is the
18 rows of `states`; UI phases such as held, gating and merging are projections with overlays, not additional
durable task phases. Every phase also carries attempt, candidate, jobs, counters and a separate block.

| From | Trigger / guard | To / invariant |
|---|---|---|
| absent or ended | valid dispatch; new attempt | waiting or dispatched; no second active placement of that task |
| waiting | prerequisites merged, suitable lane available, not paused | dispatched; same waiting attempt |
| dispatched | prepare succeeds, worker fetches work | working; dispatch base exists before start/submit |
| dispatched drive | prepare succeeds | driving; no worker job or watchdog |
| working / driving | task_done / land, permitted and not busy | submitted; drive flag and dispatch identity survive |
| submitted / resolved / moved | submit if needed, then gate | green, red, conflict, or due block; candidate bound to target and spec revision |
| green | patch still accepted, else new review intent | accepted, else awaiting-accept |
| conflict | resolve intent | resolving; scratch remains reserved |
| awaiting-accept | accept / sendback / reject on current job | accepted / next attempt working (driving for drive) / rejected |
| resolving | resolved / reject on current job | resolved / due block; a reject here does not terminally reject the task |
| red | sendbacks below K, or drive | next attempt working / driving; otherwise needs-you |
| accepted | publish | published, moved, or due block; target movement requires another gate |
| published | merged fact | merged; then ordered after_merge and agent.stop |
| eligible task holding a lane | drop decision, not busy | dropping; stop precedes free; refused steps keep lane ownership |
| dropping | stop done and free succeeds | dropped; no task_done allowed during dropping, including while blocked |
| merged / dropped / rejected | late submission | cannot reopen; only a new dispatch reopens an ended task (SM-04) |
| rejected | cleanup then needs-you | rejected; explicit redispatch or drop is required |

Candidate invariants: `accepted` matches `cand.Patch`; a changed gate spec revision clears the accept; the kept
green base remains the base actually checked; resolved gates use the pinned resolution; publish is idempotent by
ancestry. A final result is never interchangeable with a retryable I/O error. After-merge steps are ordered and a
failed stop prevents lane reuse. Permissions come from `mt.may` plus `p.busy`, not the display phase.

| Block state | Trigger | Next / job and scratch meaning |
|---|---|---|
| none | phase produces final failure/refusal | due (`why != ""`, Seq 0); phase's work ended, no open job |
| due | needs-you appended | held, closed; phase preserved |
| none | hold, failed pending step, watchdog intervention | held, open; eligible outstanding job remains answerable |
| held | permitted resume | none; retry/reprompt/regate target follows phase and cause |
| held open job | valid verdict | none; verdict moves the phase |
| any | merge, drop, lead sendback | none; phase transition decides what remains |

Due and held are mutually exclusive. Held alone must not imply that scratch is free: an open conflict/resolution
still reserves it. Terminal-phase needs-you may still require cleanup without reopening normal work.

### Effect durability and retry machine

Owner: `event/log.go` (`Log.Do`, Append), `loop.go` (`App.do`, failed), `settle.go`.

| From | Event | To / durable evidence |
|---|---|---|
| absent | intent append succeeds | pending; failed append leaves absent and runs nothing |
| pending | exclusive key claimed | running; concurrent same-key effect refused |
| running | transient error or daemon cancellation | pending; cancellation adds no counted failure |
| running | final outcome, result persisted | recorded; fold actual Seq/TS (SM-03) |
| running | outcome known, result write fails | unwritten in this process; keep raw result, never repeat the effect here |
| unwritten | next pass can append result | recorded, folding the persisted record |
| recorded | repeated key | stored reply; no outside effect |
| pending | restart | reconcile by crash class; state observation / ancestry / repeat prompt |
| pending of ended task | settleEnded, not a protected exception | settled-not-run; no outcome applied |
| pending error | third counted failure for step key | held needs-you; retry only after unblock |

Invariants: intent precedes effect; result means a final outcome; same `(task, attempt, step)` cannot run two effects
at once. Step counters provide new keys when the intended action changes. Results and their metadata must mean the
same thing live and in replay. `p.unwritten` is deliberately not durable: a restarted process must reconcile.
At-least-once notifications and prompts may duplicate across an effect/result crash cut; "recorded once" does not
make the outside transport exactly once.

### Scratch, admissions and task claims

Owner: `scheduler.go`, `steps.go` (`scratchStep`), `roleworktree.go` (`admit`), `states.go` (`busy`, `claim`). These
are live mutex/token machines rebuilt on restart; persistent open intents still prevent conflicting operations.

| Resource state | Event / guard | Next |
|---|---|---|
| scratch free | gate/publish obtains token; no conflicting reservation | running(task, key) |
| scratch running | effect and its fold end | free; wake loop; release publish waiters |
| conflict/resolving/resolved owns scratch | another task wants a resetting gate | unchanged; other task waits |
| no publish running | config/close admitted, no restart claim | admissions count increases |
| publish running | writer requests admission | writer waits on publish completion or context |
| admissions > 0 | publish or self-restart selected | unchanged; must recheck at actual start |
| admission held | writer ends | decrement, including error paths |
| unclaimed task | begin validates current attempt and no ending/claim | held until step completion |
| claimed / running / pending gate or publish | drop/move/submit/resume request | refuse busy; retry after it settles |
| free task | drop decides under lock | ending claim until fact records; append failure removes claim |

Invariants: one scratch effect; no publish while a main-checkout writer is admitted; no new writer after restart
claim; no ordinary step starts under a decided drop. Slow effects run outside `a.mu`; admission checks at selection
alone are insufficient. Selected activity samples release their claim and validate the sampled job before acting.

### Lane ownership, Git observations and store projection

Owner: `lanerows.go`, `lanescan.go`, `checks/lanetable.go`, `checks/lane.go`. Distinguish durable lane assignment
from observed Git state (`free`, `dirty-free`, working/blocked/finished/diverged etc.) and failed/old observation.

| From | Event | To / invariant |
|---|---|---|
| unassigned | dispatch decided | reserved for prepare; no other open dispatch takes it |
| reserved | successful lane.prepare | assigned; move same task off any old lane, save branch/base/type |
| assigned, unfinished | successful submission | finished projection; row may remain after merge |
| assigned | dropping intent / failed stop or free | still assigned; cannot advertise free early |
| assigned | dropped fact | row removed |
| projection changed | SQLite rewrite succeeds | synced; read-only hooks see the folded row |
| projection changed | SQLite rewrite fails | unsynced error; steps wait until sync succeeds |
| observation fresh | read error / stale scan | unknown; preserve last task and active facts, reset elapsed-time baselines |
| unknown | good scan | observed again; timing starts from new evidence |
| Git branch clean and proven merged | free | detach at target, delete branch; repeated free converges |
| dirty or proven unmerged | free without discard | refusal; no mutation |
| any read unknown | free, with or without discard | retryable error; no detachment/deletion (SM-01) |

Only configured lanes belong in the projection. An unknown observation is never free, finished, idle or proof that
an agent disappeared. Assignment has one task per lane and one lane per task. A missing row must not silently name
the main checkout or an empty agent. Readers consume projected columns rather than rebuilding event history.

### Jobs, fetched revisions and caller replies

Owner: `fetch.go`, `jobs.go`, `jobstart.go`, `prompt.go`, `verdict.go`, `specs.go`, `wire/client.go`.

| From | Event / guard | To |
|---|---|---|
| no worker job | prepared dispatch / new fix attempt | work/fix intent, wake queued |
| no integrator job | green / conflict with integrator slot available | review/resolve intent; job open before start carries wake |
| open, undelivered | job_next selects current authorized job | delivered fact and text/spec rev; working for dispatch |
| open, undelivered | deliveryWait | one resend of same job id; after another interval, needs-you |
| delivered integrator job | quiet Idle | one nudge, then needs-you; activity resets quiet baseline |
| integrator gone | Gone elapsed | lost; reissue under new job key once, then needs-you if gone again |
| delivered worker job at old spec rev | task_done, not Blocked | refuse once with spec.seen and receipt; fresh call can submit |
| current review/resolve job | valid authorized verdict, matching job/patch | job closed or held according to phase table |
| any job | stale explicit job id | superseded/refused; cannot answer a different job |
| request in flight | connection reset/EOF before reply | same method/payload/call id retried until deadline |
| persisted mutation reply | retry, including after restart | original reply, no mutation; spec_set uses spec_calls (SM-02) |

Invariants: herdr working status does not prove delivery; job IDs bind task/attempt/step; accept binds patch and
reviewed revision; one integrator job must not be cut off to start another. First fetch delivery is durable; later
fetch response text is cached live and can be rebuilt after restart. A client deadline is an unknown outcome, not
proof that the mutation failed. A refused old-revision call keeps its refusal when retried with the same id.

### Message delivery enum and batches

Owner: `messages.go` (`lineState.next`, foldMsg, leadPost, settleLine). States are waiting, sending,
sending-settled, sent and retired. Events below use the enum's suffixes; unlisted combinations retain their state.

| State | claimed | delivered | refused before bytes | question settled | closed unsent |
|---|---|---|---|---|---|
| waiting | sending | sent | waiting | retired | retired |
| sending | sending | sent | waiting | sending-settled | retired |
| sending-settled | sending-settled | sent | retired | sending-settled | retired |
| sent | sent | sent | sent | sent | sent |
| retired | retired | retired | retired | retired | retired |

Invariants: sent/retired are terminal and leave the outbox. A possible partial send is not a safe refusal; after
bytes, an error settles as delivered. A settled question retires an unsent answer but must reconcile a sending
one. A batch's durable intent names its members; its result settles exactly those members, never lines enqueued
later. A pending batch on replay uses its original membership. Lead lines post to inbox rather than type; routing
requires current identity and readiness, and unknown/mismatching session evidence holds delivery. Delivery held
for 24 hours creates a project needs-you; delivery/retirement clears the condition.

### Briefs, turns, stops and identity evidence

Owners: `messages.go`, `stopped.go`, `stopwake.go`, `registry.go`, `sessions.go`. A brief is the product of delivery,
worked/logged/followed flags, optional stop, and last observed pane/terminal; it is not a second task phase.

| From | Event / guard | To |
|---|---|---|
| no brief | briefed fact | waiting for delivery; newest brief owns the slot |
| delivered, no work observed | pane works / worked fact | worked; replay retains logged evidence |
| worked | same brief becomes ready | finished message; remove brief and save ending seq/time |
| open brief | non-limit stop or prolonged disappearance | stopped/ended; line to lead |
| open brief | usage/rate-limit stop | stopped, brief retained; prior worked flag disarmed |
| retained stop | reset reached / unknown-reset periodic wake | continue once per due key; alarm if still stopped |
| stopped turn | later turn starts | followed, stop cleared; start alone cannot finish the brief |
| followed | turn ends successfully | worked, then finish; failed end becomes a new stop |
| old brief | newer brief replaces it | old work/stop signals cannot finish the new brief |

Identity resolution is a separate derived enum: unavailable → identified when one pane has proof; identified →
ambiguous when equally strong proofs claim multiple panes; ambiguous → identified when only one remains;
any → unavailable when disconnected or no proof remains. Proof rank is session > recorded start > label. Unknown
is never permission to pick an arbitrary pane. A session ID belongs to one agent; reports may confirm the planned
session with its transcript, and a real reported ID can replace an unconfirmed planned ID. Names derive from
configured slots, not exported names. Existing no-proof/live-pane limitations are distinguished below.

### Watchdog and notification product

Owner: `watchdog.go`, `stopped.go`, `stopwake.go`, `notify.go`. Quiet sample transitions are in Timers above.

| State | Event | Next |
|---|---|---|
| observed, no fact | sustained quiet / blocked / exited | stalled or needs-you after its corresponding baseline |
| stalled | further quiet | escalated needs-you |
| stalled/escalated | activity | resumed; clear transient facts and pending retry schedule |
| asks question | ordinary activity | asks retained; only answer/unblock/submission/end settles it |
| stopped worker | later turn / explicit resume | stop ended; quiet timing starts anew |
| facts active | task merged/dropped/rejected | facts cleared; late watchdog fact discarded |
| needs-you unnotified | connected and retry due | notify intent/effect/result |
| notification error | fact still active | retry after 1, 2, 4 … 30 minutes |
| notification error | fact cleared while in flight | no retry |
| any elapsed baseline | missing observation / reconnect with new herdr session | reset baseline, preserve durable obligations |

No error reading a transcript/process/lane becomes quiet evidence. First sample establishes a baseline. Needs-you
and its notification are separate: a successful outside notification whose result cannot persist may repeat.

### Review request, cycle and fix verification

Owner: `review.go`, `reviewclose.go`, `reviewverify.go`, `reviewonmerge.go`.

| From | Trigger / guard | To |
|---|---|---|
| no request | explicit request or review-on-merge rule | pending, optionally waiting for after tasks |
| pending | prerequisite dropped/rejected | ended by failure line; does not suppress a later on-merge request |
| pending | rm | removed; clears request and records ending key |
| pending | after merged, prior cycle closed, reviewer idle, pass claims it | claimed live; no second firing in this process |
| claimed | fire fact, prepare scope/worktree and start brief | started cycle; failed start is reported |
| started/open cycle | linked Fixes task still open | verification waiting; close refused unless forced |
| all links ended, uncovered merges present | reviewer idle with no brief | verify brief at pinned reviewed head |
| verify brief | finished normally | listed task coverage recorded |
| verify brief | stopped/replaced/failed | uncovered remains; retry rule briefs again |
| open cycle | valid close commit and recorded close | closed; stop issuing verification for it |

Invariants: dropped/rejected fixes are not falsely called verified; a start or delivery alone is not a finished
verify; close needs every merged linked fix covered, unless the owner forces it. Review starts, close commits and
their covered/start SHAs determine later scope. Main-checkout commit uses the config-writer admission. A review
request and an open review cycle are distinct: a request can wait for the cycle's close. Rules may repeat or run
one pass late without violating kernel placement/publish guarantees.

### Rules, merge watches and idle epochs

Owner: `rules.go`, `mergewatch.go`, `idle.go`.

| Machine / from | Event | To / guard |
|---|---|---|
| rule absent | condition true at key | settling from now |
| rule settling | same key holds for settle | call, then optional line; successful no-line action waits for its effect to show |
| rule settling/failed | condition false / key changes | remove/reset live state |
| rule action | line persisted with Of(kind, key) | did; never line same key again |
| rule action | fails three passes | rule needs-you; ends when failures cease |
| merge watch open | all merged / first dropped or rejected | outcome line with watch seq; did closes watch |
| merge watch open | rm | removed; no later outcome line owed |
| pipeline busy | no tasks/after-steps/briefs/requests/owed verify/needs-you | idle candidate keyed by newest work-ending seq |
| idle candidate | empty for idleSettle, not acknowledged | idle line once for key |
| idle lined | unacknowledged for idlePingAfter | one ping and owner notification |
| idle | lead acknowledgement | acknowledged key; no more ping for it |
| idle | new work / newer work end | busy / new idle key; old acknowledgement cannot cover new end |

A rule is not a kernel transaction: no extra durable state machine should be invented for its action. Its failure
mode permits duplicates or delay after crash. The idle key includes real after-merge result sequences (SM-03),
brief ends and request ends; age is not inferred from an unrelated merge's time. Rule failure needs-you is excluded
from the idle condition so it does not defeat its own rule.

### Nightly and pause machines

Owner: `nightly.go`, `pause.go`.

| From | Event | To |
|---|---|---|
| nightly off | enable clock | scheduled; does not invent a past result |
| scheduled | clock due, no scratch run, disk sufficient, day's key not done/spent | running |
| running | GREEN / RED result | done for date; settle needs-you (GREEN clears, RED sets it) |
| running | retryable error | pending with persisted failure count; retry after nightlyRetry |
| pending | third failure | spent for that date, needs-you; later day's key can run |
| result unsettled | next pass or restart | settle missing needs-you, even when nightly is now off |
| RED needs-you | hand GREEN observed | resumed, even while off |
| nightly any | turn off | no new run; in-progress run and settlement finish |
| unpaused / paused | pause call with future expiry or none | paused/replaced; fact and reply persisted |
| paused | clear or expiry | unpaused; expiry is derived, not a new fact |

Project pause blocks dispatch/waiting placement and the worker-restart rule; it does not cancel gates or merges.
Model pause removes that model from dispatch choice rather than pausing the project. A final nightly result,
per-day exhaustion and its outstanding notification are different state components.

### Daemon, project sleep and binary following

Owner: `serve.go`, `panes.go`, `follow.go`, `project/stopped.go`, `cmd/obor/sleep.go`, `service/service.go`,
`selfexec/selfexec.go`.

| From | Event | To / invariant |
|---|---|---|
| daemon absent | start, no stopped marker, exclusive serve.flock | load/replay, then bind socket; one writer |
| loaded/disconnected | subscribe snapshot succeeds | connected; reset unknown/new-session baselines |
| connected | lost stream | disconnected/retry; socket remains available, no false absence alarms |
| disconnected | failed subscribe | retry with capped backoff; successful fresh snapshot reconnects |
| running | cancellation | stopping; cancel grouped effects, wait requests/effects, close socket/store/flock |
| active project | stop | pause admissions, stop agents/serve as requested, unregister, write stopped marker |
| stopped marker | BeginStart | starting marker; start serve and restore saved unexpired pause |
| starting | successful completion | remove starting marker; failed start can retry from marker |
| binary unchanged | valid quiet replacement detected | restart pending from timestamp |
| restart pending | idle or followWait elapsed, admissions zero | claim restart; refuse new admissions |
| claimed | restart succeeds / lifetime canceled | daemon ending; pending effects replay in new process |
| claimed | restart fails | failed, clear claim; needs-you until next serve starts |

Installed service vs loaded/running service are separate states, observed through launchctl; an installed plist
alone is not a running daemon. Marker files and service/process state are not one atomic transaction; retained
limitations are below. Binary replacement must be executable, quiet and pass its version probe; a rollback binary
is still a replacement. Follow timing runs even when herdr is disconnected.

### Commands, slots, receipts and schema lifecycle

Owner: `run/run.go`, `run/slots.go`, `run/receipts.go`, `checks/runner.go`, `store/store.go`, `config/load.go`.

| From | Event / guard | To |
|---|---|---|
| command requested | configured heavy command | waiting for load/slot; missing command fails before execution |
| waiting | inherited live slot / successful exclusive claim | held; merge marker protocol differs from worker slots |
| stale slot | holder dead and reclaim guard acquired | reclaim then claim; don't remove another live holder |
| held | execute under timeout/group context | running; signal/timeout cancels process group |
| running | finish | exit status; release only resources still owned by this pid |
| check on lane tree | success against fingerprint | receipt for exact inputs; changed tree/config invalidates reuse |
| store absent | OpenRW | create schema 13 transactionally |
| supported older store | OpenRW | migrate transactionally, preserving rows; upgrading schema 12 adds spec_calls |
| too-old / newer / malformed schema | OpenRW | refuse before changing it |
| config file edit | validate all proposed changes and base revision | commit all changes or none; failed commit restores prior bytes |

Invariants: a receipt cannot prove a different tree passed; failed/canceled work cannot become a green receipt;
read-only clients must not migrate. Slot files, background markers and stop markers have distinct owners and
lifetimes; don't infer task completion from a missing pid file. SQLite spec text and call receipt commit together
(SM-02); lane projection follows the event log and must catch up before starting dependent work.

### Agent start/stop adapters and hook loop

Owner: `launch/start.go`, `launch/stop.go`, `launch/self.go`, `launch/restartlock.go`, `guard/stop.go`,
`agents/*`, `app/port/port.go`. Agent-kind bridges provide recipes and capabilities; they do not own task state.
Observed pane status is one of idle, done, working, blocked or unknown; only idle/done is Ready.

| From | Event / guard | To |
|---|---|---|
| pane selected | successful label and validated bridge | start-ready; label failure starts nothing |
| start-ready | fresh kind with named-session capability | planned session recorded before launch |
| starting | start reply pending | keep polling; pending reply alone is not started |
| starting | matching agent observed running in pane | started; first-prompt activity can prove startup before readiness reply |
| starting | name taken, no live different-pane holder | clear stale name, retry once |
| starting | startup dialog / not-ready / bound expires | StartError; no successful-start claim |
| running, stop requested | working/blocked | Escape, bounded wait for idle; refuse if force is required |
| ready, stop requested | kind quit recipe | quit/dialog, wait for shell; terminate if required within total bound |
| terminating | foreground agent exits | stopped; do not mistake leftover MCP children for the agent |
| caller's own pane | stop/restart | detached scheduled operation; resolve identity/session again after waiting for idle |
| restart pending | per-lane lock acquired | serialized stop/start; release lock on every return |
| worker Stop hook, first loop | lane row unfinished, not blocked, no live nonstale background work | block once with task_done hint |
| worker Stop hook | active stop loop / finished / Blocked report | allow; hook writes no completion fact |
| hook | outside project / stopped project / internal Stop error | no-op or allow with error note; never invent completion |

Invariants: no restart of a different session/pane after a stale wait; no stolen live name; start success requires
observed evidence, not elapsed time. Identity uncertainty is handled separately by the core's identity enum.
The worker's second Stop is permission to stop its turn, not permission to mark its task submitted.

### Setup, install and release progress

Owner: `setup/oborinit.go`, `install.sh`, `bin/obor`, `scripts/release`, `scripts/ship`, `scripts/publish`.
These are filesystem/remote-evidence machines outside serve's task log. The audit reads them; it does not install,
publish, push, restart real panes or send release messages.

| From | Trigger / evidence | To |
|---|---|---|
| project absent/partial | init preconditions pass | apply idempotent setup steps; rerun reports done/already/skipped |
| unborn repository with unrelated files | init | refusal before writes; init-owned untouched files permit repair rerun |
| kit absent / existing kit | install | clone / update to newest release tag; link shim and probe version |
| shim without usable binary | release needed | fetch platform binary and verify catalog checksum before executing |
| release source clean, reviewed | release version bump/check | committed version bump, or undo uncommitted bump on check failure |
| bumped, untagged | upgrade/release checks pass | build in scratch, create immutable release tag; failure leaves no new tag |
| tagged | all four binaries match tag catalog | copy back catalog/artifacts and commit checksums on main; rerun checks evidence |
| local release ready | ship validation succeeds | push only missing tag/checksums-main evidence; never main's unrelated later tip |
| pushed | validated notes/catalog and missing remote release/assets | publish or upload missing assets; failure stops with repair command |
| published | installed version differs | install; recheck version |

Invariants: never move an existing tag to repair a release; never upload bytes that fail the tag's catalog;
partial progress is resumed by checking evidence. Local release preparation, remote publication and local install
are separate states. Existing shallow publication-completion checks are a retained limitation, not an exactly-once
transaction spanning Git, GitHub and the filesystem. Their Go fixture tests run in `tests/release`.

### Web read, navigation and setup edits

Owner: `web/src/lib/live.svelte.ts`, `router.svelte.ts`, `route.ts`, `setup.ts`, `pages/Setup.svelte`,
`internal/web/web.go`. These states are client-local; server task phases remain authoritative.

| From | Event | To / invariant |
|---|---|---|
| stopped Live | start | controller active, initial read, periodic refresh, optional event poll |
| read pending | successful response with ticket >= painted | data painted, fresh timestamp; older success cannot replace newer painted data |
| read pending | error | retain data, expose error/stale mark; retry refresh/poll |
| event poll | seq beyond cursor | read page; advance cursor only after successful read |
| active Live | stop/navigation | abort controller and remove from global live mark |
| route | valid hash change / same-route go | new keyed page / reread; routing does not mutate task state |
| clean setup | first staged edit | staged values plus original config revision |
| staged | incoming revision differs | moved/conflicted; explicit rebase/discard before saving |
| staged/reviewing | commit | busy; POST all changes and base revision |
| busy | accepted save | clear staged set, refresh setup |
| busy | refusal or transport failure | show error; retain staged changes when POST failed |
| pause form | submit/clear | immediate busy POST then refresh; not part of staged config commit |

The setup server checks base revision, dirty config and occupied dropped lanes, and serializes its writer with
publish/close. A stale client view must never be trusted as server authorization. Web failure marks and saved data
are distinct: an error need not discard the last readable page.

### Accepted failure modes and audit limits

- Prompts, notifications and rule actions deliberately permit repeat delivery or one-pass delay; those alone are
  not confirmed bugs. Kernel ordering and call receipts still must hold.
- Existing BACKLOG entries retain low-frequency stop/start marker gaps, no-proof live-agent ambiguity, and a review
  launch whose started fact was lost. They are not newly reproduced findings here and are not relabeled fixed.
- The web's save/refresh gap and last-painted/read-error behavior are client risks; the config base-revision
  refusal is the kernel protection. This audit did not reproduce browser interleavings or change web assets.
- Tests exercise injected Git errors, SQLite failure, concurrent spec callers, and live versus replayed folds.
  The outside-merge/task_done cut is deterministic and also checks replay of a legacy late submission.
  They do not prove every OS/process/network interleaving or behavior of a real agent CLI. No agent launch,
  identity policy or MCP registration behavior is changed by these fixes; no live herdr/agent probe was run.
- Keep this file current when adding a state, flag combination, transition, or receipt. Add evidence and a regression
  before promoting a suspected bad interleaving to a confirmed issue; preserve the numbered rows code cites.


