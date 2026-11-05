# Log snapshot at upgrade: design (advisor, 2026-11-05)

Question: owner decision 11-05 (log snapshot at upgrade). Read: `internal/store`, `internal/app/event`, the folds
(`internal/app/project.go:78-106` and the 17 folds it calls), every reader of `events` outside serve.

## Recommendation

**The snapshot is a selection of the log's own records, marked in place.** A new column `events.archived` (schema
12). Replay reads `archived = 0`; every other reader reads the table as today. An ended task keeps the few of its
own records that fold to its ended state; everything else of it is marked archived. No synthetic records, no state
table, no second table or file, no row moved or deleted, no seq changed.

Schema 12 is written by the snapshot's own transaction, and only when a fold of the live rows took no compat branch.
So "this store is at 12" means "its live log folds without the old readers", for every store, not only the three a
census can see. The follow-up raises `upgradeFrom` to 12 and deletes the branches.

**Say this to the owner before speccing:** the yield in lines is small today. T-869 (R3) already deleted the dead
branches; what is left is about 60 production and 150 test lines, and the snapshot itself costs about 150. Both
tasks together are about **+90 production lines**, not net-negative (numbers at the end). What it buys is the rule:
from here every record shape change costs one release of compat and then goes mechanically.

## 1. What the snapshot holds

Neither option as asked. Why:

- **Synthetic facts fail on three facts of this log.** Records name each other by seq (`event/data.go:49-82`:
  `Needs`, `Watch`, `Review`, `Stop`, `Supersedes`, `VerifyOf`, `Of.Seq`; step keys `deliver.<seq>`, `notify.<seq>`;
  `prompts.seq`; the web cursor). `Log.Do` answers a repeated key from its stored result record
  (`event/log.go:310-315`). And an open task's state (`mtask`, `loop.go:140-220`) has no faithful encoding in
  today's shapes other than its own records. A synthetic "task state" fact would be a state blob with a fold of its
  own: a second replay path.
- **A state table** is a second load path, and its structs become a stored format: the same compat problem, moved.
  Refused in the advice's §6 already.

**What stays live (folded):**
- Every record whose task is not a merge-loop task (`msg`, `review`, `session`, `idle`, `config`, `pause`, …),
  except a short list of dead steps the spec names (`idle` epoch facts; `stop.<agent>` under task `roles`).
- Every record of a task that is not ended, or that a lanes row names. Rejected tasks and merged tasks with an
  after-merge step still open count as not ended.
- For each other ended task (merged with its after steps done, or dropped): its last `dispatch` fact, its last
  `submitted` fact, its `merged` or `dropped` fact, its after-step results. Today's fold turns those into an ended
  `mtask` with state, attempt, head, `mergedSeq`/`droppedSeq`, `endedAt`, `after`, `disp`, report: what the readers
  of ended tasks use (`dispatch.go:285` T-792's refusal, `:636-638`, `:704-714`; `idle.go:105-114`;
  `mergewatch.go:101-130`; `reviewonmerge.go:34`; `status.go:61-70`; `configset.go:232-238`; `block.go:76`).

**Per-task check, fail-safe:** before marking, serve folds the candidate set a second time in memory. A task is
reduced only if its reduced `mtask` equals the full one on a named field list (the fields above plus `halt`,
`needs`, `drive`); otherwise it stays whole. The lanes rows of both folds must be equal, or nothing is marked.
(Example the check catches: a dropped task whose dispatch carried `Stop`, `dispatch.go:606`: reduced, its
`halt` would be pending again.)

One replay path: `load` folds the live rows with the same `foldRecord`. After the transaction it reopens the log
and folds again from the table, so the running state is exactly what any later restart folds.

## 2. When it runs, and crash safety

There is no `obor upgrade` step in the binary. An upgrade reaches a project when its serve starts or re-execs on the
new binary (`internal/selfexec`, `app/follow.go`); the store's migration is `store.OpenRW` → `initSchema`
(`store/store.go:170`), called from `App.openLog` (`project.go:63`).

- `initSchema` adds the column (as the `note` migration, `store.go:211-222`) and **leaves the version at 11**.
- `App.load`, after the replay and before `syncLanes` (`project.go:44-49`), while the version is below 12: choose
  the set, check it, then one store call marks the rows and, when the live fold took no compat branch, sets the
  version to 12, in **one SQLite transaction** (WAL, `synchronous=FULL`, `store.go:47`).
- Compat branches count their hits during the fold (one line each, deleted with them). Hits left (an open task or an
  undelivered line of an older shape): rows are still marked, the version stays 11, `obor status` says which task
  holds it, and the next start tries again.
- **Kill mid-snapshot:** the transaction rolls back; no row is marked, version 11; the next load does it again from
  the same input. Marking a marked row changes nothing.
- **Before the first marking:** `store.Backup` to `.obor/state/backup/obor-pre-snapshot-12.db` (tmp + rename as
  `BackupDaily`, `store.go:94-99`; the name is outside its rotation glob). Rollback: stop serve, copy it back.
- Undo without the backup: clear the column and set the version to 11. Nothing was moved.

Store owns the column and the transaction; app owns which seqs. Store must not import the fold.

## 3. Where the old log goes; who reads it

Same table, `archived = 1`. No archive table or file: that would need a UNION in every history reader, a copy of
the indexes, and (for a file) a hand-made two-phase commit.

**Fold readers: live rows only (change):**
- `event.OpenLog` / `records` (`event/log.go:133`, `:195`) ← `App.openLog` (`project.go:68`).
- `event.ReadLog` (`log.go:151`) ← `app.ReadReviewCycle` (`reviewverify.go:297`, `obor review close|fix`). Its fold
  reads only `review` and `msg` records (`reviewverify.go:107`), which stay whole: same answer, same read as serve.

**History readers: all rows, unchanged code:**
- `event.Dump` (`log.go:165`) ← `obor serve dump` (`cmd/obor/serve.go:265`). It calls `ReadLog` today: it must
  read all rows (the one edit here).
- `event.RecentEvents` (`log.go:184`) ← `view/events.go:36` (`obor status --json`, web).
- `view.EventsSince` (`view/events.go:65`) ← `web/web.go:526-535` (long poll by seq).
- Task page: `view/detail.go:261` (`taskRecords`: timeline, report, gate). Agent page: `view/detail.go:285`.
- `view/setup.go:217` (dispatch counts), `view/view.go:428` (`lastStarts`), `view/queue.go:200`
  (`dispatchedTasks`).
- `event.ReadMerges` (`event/data.go:124`) ← `checks/flakes.go:308` (the `merged` facts stay live anyway).
- `store/sessions.go:236` (`sessionFacts` ← launch, hooks, `CurrentAt`, `TranscriptAt`): session records stay whole.
- `store/lanes.go:156` (`HasBlockedReportAt`, a hook; T-894 replaces it): an open task is whole.
- `prompts` rows point at `events.seq` (`store/prompts.go`): seqs do not change.
- Backups (`app/log.go:22`, `wire/backup.go:33`): the whole file.

Reviews' coverage of merged tasks: from the `review.started` and verify records (whole) and from git
(`review.go:347`); the on-merge rule needs `mergedSeq` only (`reviewonmerge.go:34`), which the kept `merged` fact
gives.

## 4. What the follow-up deletes

It sets `upgradeFrom = 12` and deletes (about 60 production, 150 test lines):

| What | Where | Lines |
|---|---|---|
| `answer.<call id>` verdict (before T-511) and the `answer` flag it alone sets | `resume.go:31-32`, `:42-44`; `verdict.go:55`, `:114-116`; `mtask.go:245-253` (parameter), `:331-336`; `loop.go:196`; `steps.go:381-384`; `move.go:103-105`; `dispatch.go:73-75` | ~28 |
| `causeOpen` hold (T-604) | `loop.go:89-91`, `mtask.go:98-99` | ~5 |
| `idle` epoch fact, keyless `idle.ack` | `idle.go:79-80`, `:86`; `event/steps.go:131` | ~4 |
| `stop.<agent>` role stop, task `roles` | `rolestop.go:33-34`, `event/steps.go:30-32` | ~5 |
| Schema 10 migration | `store/store.go:27-30` (comment), `:209-222` | ~16 |
| The hit counter from task 1 | each branch above | ~8 |
| Tests | `answer_test.go:297-315` (19), `questions_test.go:211-257` (47), `rolestop_test.go:174-201` (28), `store_test.go:360-409` and `:413` on (~60, replaced by one "refuses 11" test) | ~150 |

**Check with a hit counter first, delete only at zero hits in live rows** (they may also fire on today's records):
`registry.go:73-78` and `:368` (a start row without a terminal, T-504), `messages.go:1015-1018` (a needs-you with no
delivery recorded).

**Not deleted by this** (not log shapes, or still read from archived rows): `view/setup.go:93`, `:211` (a dispatch
without a type: archived dispatches still count); `store/specs.go:26` and `store/lanes.go:24` (`specCols`,
`laneCols`: a newer CLI reading a store whose serve has not restarted); `checks/lanetable.go:251-280`
(`legacySpecDir`, git); `review.go:69`, `:565` (close commits, git); `config/config.go:757`. About 15 comment lines
on absent optional fields (`loop.go:108-109`, `:218-219`; `dispatch.go:67`, `:85`, `:89-91`; `event/data.go:24`,
`:104`; `event/log.go:29`, `:40`, `:69`) can go with it. The advice's "64 sites" were mostly these.

## 5. Risks, and what I would refuse

Risks:
1. **Folds that remember across tasks.** `folded.verdicts` (`app.go:210`, a count the start rows compare with,
   `rolestop.go` T-870) shrinks when verdict results are archived: the integrator's stop rule may miss one stop
   until its next verdict. `folded.calls` / `refusals` / `delivered`: a call retried across that one restart for a
   reduced task gets a refusal, not its stored reply (dispatch, task_done and drop keep theirs: their facts stay).
   The spec must walk the 17 folds of `foldRecord` and say for each what a reduced task changes.
2. **An open item of an older shape holds version 11.** A waiting task or a week-old question can hold it for days;
   the release after then refuses that store ("upgrade through <the snapshot's release> first"). It must be visible in `obor status`,
   and the UPGRADE note must say how to clear it (finish or drop the named task, restart serve).
3. **A redispatch of a reduced dropped task** must take the next attempt after the kept fact's (keys never collide
   with archived ones): one test.
4. **The oracle.** T-869's probe (`replayprobe_test.go`) on a dump of each registered store, full fold against
   live fold, equal outside the reduced tasks' unlisted fields, before the release. This store: 23,550 records, 561
   loop tasks, replay 0.2 s (so replay time is no argument today).
5. **Change rule 5 needs new text** (lead-owned): "a record shape change bumps the store schema and its compat
   branch names that schema; the next release raises `upgradeFrom` and deletes it". The census goes.

Refuse:
- A state table, synthetic records, renumbered seqs, or a separate archive table or file.
- Deleting or moving old rows.
- Raising `upgradeFrom` in the same release as the snapshot (a store must pass through one release that does it).
- Marking anything when the lanes rows of the two folds differ, or reducing a task the per-task check does not pass.
- Running it outside `load` (a CLI step, or in `store`), or on every start once the store is at 12.
- Tests that hand-write the reduced form: the selection is tested by folding real histories both ways.

## Net production lines

| Task | Production | Tests |
|---|---|---|
| 1. Snapshot (hard): column and migration ~15, mark-and-version transaction ~30, live read and `Dump` ~8, selection ~45, per-task and lanes check ~35, `load` wiring, backup, status line ~30, hit counter ~8 | **+150 to +170** | +300 |
| 2. Delete the pre-snapshot readers, `upgradeFrom = 12` | **−60 to −70** | −150 |
| Both | **about +90** | about +150 |
