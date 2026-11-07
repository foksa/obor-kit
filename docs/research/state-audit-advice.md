# State-machine audit (11-06): how it lands — advice

Advisor (ob-fable), 2026-11-07, for the lead. Question: the uncommitted audit in the main checkout (SM-01..04, schema 13,
+500 lines in `docs/state-model.md`). Line numbers below are the uncommitted working tree's. I ran `go test
./internal/app ./internal/checks ./internal/store` on it: all three green.

## Recommendation

1. **Land SM-01, SM-03 and SM-04 as written** (five production files, ≤ 12 lines each, each strictly more correct, none
   adds state). One lane task, Difficulty medium, `Review: on merge`.
2. **Do not land SM-02 as written.** The finding is real but needs three coincidences; the fix adds a table, a schema
   bump the day after v0.29.0 was tagged, and a second receipt mechanism beside the one every other verb uses
   (`p.folded.calls`). Replace it with a `spec.set` fact folded into `folded.calls`, and keep the audit's one good
   move: `submitMu` held from receipt lookup through the write. No schema change, rollback to v0.29.0 keeps working.
   One lane task, Difficulty medium, `Review: on merge`; it also settles the RV-129 comment in `app.go:87`.
3. **The 500 doc lines do not belong in `state-model.md`.** Keep ~12 contract lines there (one sentence per changed rule,
   at the section that owns it). Move the SM ledger, evidence, validation and the 15 inventory tables to
   `docs/research/state-audit-2026-11-06.md` as the audit's report. The lead commits that directly.
4. **Mechanics:** the lead commits the whole working tree to a side branch (`audit/state-2026-11-06`, not main) so the
   owner's work is preserved verbatim; each task's spec names the files to take from it. Nothing else needs a task now.

## 1. The four findings

| | Real and reachable? | Fix correct and minimal? | Regression risk |
|---|---|---|---|
| SM-01 lane.free | Yes, low likelihood | Yes, simplifying | None found |
| SM-02 spec receipts | Yes, very low likelihood | Correct, not minimal | Schema bump; lock widening is fine |
| SM-03 result fold | Yes, every merge; low impact | Yes, 5 lines | None found |
| SM-04 late task_done | Yes, low likelihood, medium impact | Yes, a moved guard | None found; one redundancy to fold |

### SM-01 — `Lane.Free` deleted a branch when `git log` failed

Real. Before the fix (`git show HEAD:internal/checks/lane.go`, lines 62–68) a failed `rev-parse` counted as "no
branch", any `merge-base` error counted as "unmerged", and `git log`'s error was discarded, so an empty `commits`
passed the refusal switch and `checkout --detach` + `branch -D` ran (now `internal/checks/lane.go:102-109`).
Reachability is narrow: `git log` must fail right after `merge-base` succeeded and `checkout` must then succeed
(a canceled context kills both, so cancellation is self-protecting). Think OOM-killed git or a transient fs error;
I would not expect to see it, but a drop that silently deletes unmerged work is the one failure that must never happen.
Fix (`lane.go:62-79`): the three reads propagate errors, `exit1` (`scratch.go:779`) separates the predicates' exit 1
from fatal errors; no new helper, no new state. Error → `freeStep` pending → counted retry → needs-you, as today.
The test (`lane_test.go:285-318`) covers calls 2–4 failing, with and without `--discard`, and a healthy retry.
`gitShim` grew an exit-code parameter; the one other caller was updated. Land as is.

### SM-02 — a retried `spec_set` after a restart overwrote a newer spec

Real: `p.replies` (`app.go:154`) is live only; HEAD's `specSet` looked it up, then `SetSpec` compared text and wrote.
To hit it: the lead's call loses its reply (the wire retries the same call id until its deadline), a *different* caller
writes the spec in between (the lead is sequential, so this is the owner's CLI), and serve restarts inside the retry
window. Three coincidences in a few seconds; I rate it very unlikely. The concurrent same-id variant needs two clients
sending one id.

The fix is correct: `store.SetSpecCall` (`internal/store/specs.go:76-101`) commits spec and receipt in one transaction,
`specSet` (`internal/app/specs.go:29-40`) holds `submitMu` from lookup to write, `TestSpecCallReceiptMigrationAndRollback`
proves the rollback. It is not minimal for this codebase:

- Every other mutation verb's receipt is a fact folded into `p.folded.calls` (dispatch `dispatch.go:125`, drive
  `drive.go:221`, land `drive.go:289`, config `configset.go:220`, send/brief/resume `messages.go:287-347`), and the
  spec domain already records a per-task, per-call fact: `StepSpecSeen` with `Call` and the refusal (`fetch.go:381`,
  T-821). A `spec.set` fact `{Call, Reply, Rev}` under `c.Task`, attempt 0, folded the same way, is ~15 lines, no
  schema change, and replays on restart. The only loss versus one transaction is a crash between spec commit and
  fact append: the retry then re-applies the same text and answers `UNCHANGED rev n` instead of `SET rev n`. Harmless.
- Schema 13 (`store.go:26`, `:155`) is routine mechanically (T-894 did 12 the same way: `CREATE TABLE IF NOT EXISTS`
  plus the version row; `initSchema` migrates 12→13 on first `OpenRW`; readers `OpenRO`/`OpenAt` never migrate). The
  cost is rollback: a v0.29.0 binary refuses the store (`store.go:199`, "newer than this obor"). obor-kit's own serve
  runs main builds, so the day this merges obor-kit cannot fall back to the release binary. Past bumps accepted that;
  this one buys protection against a very unlikely path, and the owner's rule is no compat surface without need.

Locks: holding `submitMu` across the receipt read, `config.LoadDaemon` and the write matches `taskDone`, which holds it
across `LoadDaemon` and the lanes git read (`fetch.go:287-336`). Order stays `submitMu` → `mu` (`specs.go:29-31`);
`verdicts` is never taken, so no inversion with resume/move (`resume.go:76`, `dispatch.go:141`). J1 (`app.go:83-88`)
already lists spec set among the verbs that keep `submitMu` across reads. Keep the widening in the replacement.
RV-129 (`BACKLOG.md:51`) asks that `app.go:87` say which lock each verb takes on the next touch: do it in this task.

### SM-03 — `App.do` folded a synthesized result (Seq 0, now) instead of the stored record

Real and reachable on every merge: `foldLoop` advances the idle key only for a newer seq (`mtask.go:213-214`), so live
`afterSeq` never moved and the idle key named the merge, while replay named the stop. Impact: after a restart the idle
key changes, so one acknowledged idle gets a second line or ping (T-866/T-868). Low, but it is exactly the live/replay
divergence the log exists to prevent. Fix (`loop.go:460-464`, `event/log.go:447-453`): fold `log.Result(key)`, which
`Log.Do` has already populated on append (`log.go:306`), so the `!ok` branch is a guard, not a path. Nothing else in a
live fold reads a result's `Seq` (only facts and intents do), so the behavior change is this key and `afterAt`. The
unwritten-result path already folded the appended record. Land as is; keep the BACKLOG Concern.

### SM-04 — a late `task_done` reopened a task the owner had merged outside obor

Real. `taskDone` checks `mt.may(vSubmit)` outside `mu`, then records (`fetch.go:387-395`); the merge loop can record
`merged` from `Scratch.Landed` in between (`loop.go:584`, `:743`). HEAD's `foldLoop` replaced the task with
`stSubmitted` unless it was dropped/dropping, so merged went back to submitted, live and on replay, and `foldLanes`
re-finished a lane that a later dispatch may hold. Window is one task_done's checks (config load, a git lanes read),
and the trigger is an owner merging by hand while the worker reports: low likelihood, medium impact (merge metadata
lost, after-merge steps rerun, lane row stolen).

Fix: the guard moved from `foldLoop` to `foldRecord` (`project.go:82-86`) and widened from dropped/dropping to every
`ended` state (`states.go:27`), ahead of `fold`/`foldJob`/`foldLanes`, so the late fact touches nothing. A dispatch
fact still reopens an ended task (it changes state first), and a rejected task was never resumable without a drop
(`resume.go:219`), so no legitimate path records `submitted` on an ended task. `taskDone` then reports superseded when
its fact left no receipt (`fetch.go:400-408`). Minimal and right. One simplification for the landing: the two
post-record checks (`dropped`, `recorded`) now test the same thing; one check reading `m.shown()` replaces both.
Old logs with the bad sequence replay as merged after this: the correct state, no compat branch.

## 2. What deserves a task now, what goes to BACKLOG

Nothing in "Accepted failure modes and audit limits" is new: the marker gaps, no-proof identity and the lost review
start are already in BACKLOG Open; the web save/refresh gap is a client risk behind the base-revision refusal.
Likelihoods of the four findings are above; none of the unverified risks named by the audit has a concrete path.

- **Task now:** the two landing tasks in §4. Fold into them: the `taskDone` post-record merge (SM-04), the `app.go:87`
  lock comment (RV-129), and moving the tests out of `stateaudit_test.go` into the existing test file of each subject
  (`specs`, `idle`/`loop`, `fetch`): a file named after an audit will not be found by the next person touching
  `taskDone`.
- **BACKLOG Concern (already written, keep):** result-injecting test helpers miss `App.do` differences.
- **Nothing:** the last volatile receipt, `p.replies` for job_next (`fetch.go:73`, `:122`): the delivered fact is
  durable and the response text is rebuilt; same class as SM-02 only in name. An UPGRADE note for schema 13 is moot
  if §1's replacement is taken; previous bumps got none.

## 3. The doc

Trim and split. `docs/state-model.md` is a contract ("what `obor serve` records, in what order, and what a restart does
with it"); some 30 production files cite its numbered rows. The addition puts a 90-line issue ledger with test names and a
validation timestamp *above* "The log", and appends 15 tables that restate code in a second vocabulary: "Task phase
and block product" overlaps the existing Transitions table row for row, and five tables (web client, release scripts,
setup/install, agent adapters, commands/slots) are outside serve's state entirely. Two descriptions of one machine
diverge; the doc's own closing rule ("keep this file current when adding a state, flag combination…") is a standing
tax the owner's preference argues against.

- **Keep in `state-model.md` (~12 lines, at the owning section, no SM prose):** Transitions: a `submitted` fact on an
  ended or dropping task is ignored (SM-04). The log: a result folds as the log wrote it, seq and time included
  (SM-03). Concurrency: spec set takes `submitMu` from its receipt lookup through its write, order `submitMu` then `mu`.
  Not recorded / The log: where spec call receipts live (the `spec.set` fact, or `spec_calls` if §1 is overruled).
  The Transitions row for lane.free: a failed git read is a retryable error, never a free (SM-01).
- **Move to `docs/research/state-audit-2026-11-06.md`:** the SM table, the four evidence sections, the validation
  paragraph, the inventory tables and the accepted-limits list. It is a good audit report and a map for the next
  audit; `docs/module-map.md` can point to it in one line. If the lead wants one inventory table promoted to the
  contract later, "Effect durability and retry machine" is the one with content Crash classes does not already state.

## 4. Landing plan

Two lane tasks, not one commit and not four tasks. One commit would carry the schema decision inside three trivial
fixes and skip the gate; four tasks spend three dispatches on 5–12-line changes. A lane task gets `make check-affected`,
the integrator's placement check and review on merge for free. The owner's diff is the implementation: the lead
commits the working tree as is to a side branch `audit/state-2026-11-06` (BACKLOG and the doc included, so nothing is
lost), restores main, and each spec says which files to take from that branch and what to change.

| Task | Finding → files | Spec content | Difficulty |
|---|---|---|---|
| T-A: three audit fixes | SM-01 → `internal/checks/lane.go`, `lane_test.go`. SM-03 → `internal/app/event/log.go`, `loop.go`. SM-04 → `fetch.go`, `mtask.go`, `project.go`. Tests → from `stateaudit_test.go` into the subjects' test files. Doc → the contract lines of §3 (not the audit section). | Take the files from the side branch verbatim; merge `taskDone`'s two post-record checks into one; move tests; `make check-affected`. | medium, `Review: on merge` |
| T-B: durable spec_set receipt | SM-02 → `internal/app/specs.go` (keep the `submitMu` widening from the branch), new `spec.set` fact + fold into `folded.calls`, the two spec tests from `stateaudit_test.go` adapted, `app.go:87` lock comment (RV-129), one contract line in the doc. Not taken: `internal/store/*`, schema 13. | Decision stated in the spec: fact receipt, no schema change. If the owner prefers the audit's table instead, take `store/specs.go`, `specs_test.go`, `store.go` from the branch unchanged and add the rollback note to the next UPGRADE doc. | medium, `Review: on merge` |
| lead, direct | `docs/research/state-audit-2026-11-06.md` (the moved 500 lines), `BACKLOG.md` Concern, a module-map pointer | Docs only, no gate needed | — |

Order: T-A first (independent, lowest risk); T-B after, since it touches `specs.go` only and conflicts with nothing in
T-A. Both can run in parallel if lanes are free; their file sets are disjoint except `docs/state-model.md`, where
each adds its own sentences.
