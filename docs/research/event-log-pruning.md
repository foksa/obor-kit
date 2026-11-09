# Event log pruning: measurements and design (advisor, 2026-11-09)

Question: owner 11-09, "if the log's growth can affect obor performance, design pruning now". Measured on a copy of
`.obor/state/backup/obor-2026-11-09.db` (31,939 records, 15 days) and on copies scaled to 3, 6 and 12 months. The
live store was not touched. Method at the end.

## Recommendation

**Yes, it matters, and two tasks fix it. Do them in this order.**

1. **Fix the session fold first (small, no pruning).** Serve start is quadratic in the number of session facts:
   0.48 s today, 13 s at 3 months, 52 s at 6 months, 3.6 minutes at 12 months. Serve starts about 56 times a day on
   this project (430 starts in `serve.log` since 11-01). One fold keeps its index instead of rebuilding it per
   record: 0.10 s today, 4.4 s at 12 months. Pruning ended tasks would not fix this: session facts are the
   project's, not a task's.
2. **Prune in serve's nightly backup request.** Delete every record of a task that ended more than 30 days ago,
   except its last terminal fact (`merged` or `dropped`). Delete session facts older than 30 days that are not
   their agent's current session. Two small fold changes make a task's terminal fact alone fold to the ended task.
   No new table, column, record or config key.

With both, a 12-month store measures: serve start 0.40 s (4.4 s unpruned), heap 76 MB (684 MB), file 114 MB
(556 MB), the prompt hook's session read 8 ms (108 ms).

**Say this to the owner before speccing:** this does not bound the log completely. The project's own records
(lines, reviews, watches) stay: 20% of today's rows, 6.5% of its bytes, about 330 rows and 70 KB a day. Each needs
its own rule (they name each other by seq), and at 12 months they cost 11 MB. I would not prune them now.

## 1. What was measured

Growth: 2,030 records a day over the 15 days, 2,720 a day in the last week. The scaled copies use the 15-day
average, so at last week's pace each column arrives about a quarter sooner. Bytes: records of tasks are 93.5%
(gate, submitted, publish and after_merge data lead); `msg` 2.8%, `session` 2.6%.

### Inside serve

| | today, 32k rows | 3 months, 192k | 6 months, 383k | 12 months, 767k | 12 months, pruned, 178k |
|---|---|---|---|---|---|
| serve start (`App.load`) | 0.48 s | 13.1 s | 51.9 s | 215.9 s | 1.77 s |
| serve start, session fold fixed | 0.10 s | 0.78 s | 1.65 s | 4.43 s | 0.40 s |
| of it: reading the table | 0.03 s | 0.19 s | 0.43 s | 1.0 s | 0.14 s |
| heap after load | 29 MB | 171 MB | 339 MB | 684 MB | 76 MB |
| `Status()` (each status request) | 1.1 ms | 5.7 ms | 12 ms | 33 ms | 3.8 ms |
| `loopTasks()` (several per pass) | 0.05 ms | 0.39 ms | 0.85 ms | 1.7 ms | 0.09 ms |
| store file | 34 MB | 147 MB | 283 MB | 556 MB | 114 MB |

- **Serve start is quadratic.** `foldSession` (`internal/app/sessions.go:79`) calls `store.Admits`, which builds
  the whole session index again for every session fact (`internal/store/sessions.go:64-67`, `sessionsOf` at
  `:95-110`). 1,760 session facts today; a CPU profile puts 240 of 360 ms there. `serve.log` shows the same curve
  on the real store: best start 0.04 s at 12k records, 0.46 s at 32k.
- **Heap is linear**, about 1.8 MB a day: the log's `Results` and `intents` maps hold every intent and result with
  its data (`internal/app/event/log.go:125-127`), and each `mtask` its report.
- **Per-pass and per-request walks** over every task ever (`loopTasks`, `Status`) are linear and small. They do not
  matter on their own.
- **Appends** do not grow with the log (one insert by primary key).

### Readers outside serve (read-only opens; warm, best of 5)

| reader | who calls it | today | 3 months | 6 months | 12 months | 12 months, pruned |
|---|---|---|---|---|---|---|
| `dispatchedTasks` (`view/queue.go:236`) | every overview: `obor status`, the web page | 12 ms | 79 ms | 157 ms | 314 ms | 74 ms |
| `store.ReadSpecs` (`view/view.go:219`) | every overview | 4 ms | 24 ms | 45 ms | 90 ms | 84 ms |
| `distribution` (`view/setup.go:241`) | setup page | 7 ms | 42 ms | 82 ms | 163 ms | 25 ms |
| `store.TranscriptAt` (`guard/prompt.go:114`) | the prompt hook | 4 ms | 27 ms | 56 ms | 108 ms | 8 ms |
| `store.CurrentAt` (`launch/start.go:284`, `restore.go:206`) | every agent start | 5 ms | 27 ms | 56 ms | 119 ms | 8 ms |
| `store.ReadSessionsAt` (`view/detail.go:195`) | agent page | 4 ms | 26 ms | 55 ms | 113 ms | 8 ms |
| `event.MessagesTo`, an agent with few lines | agent page | 6 ms | 35 ms | 68 ms | 154 ms | 32 ms |
| `event.ReadMerges` (`checks/flakes.go:339`) | `obor flakes` | 6 ms | 38 ms | 74 ms | 155 ms | 37 ms |

Flat at every size (under 2 ms): `RecentEvents`, `Since` (the web cursor and `wire/feed.go:40`), `RecordsOf` (task
page, by the index), `MessagesTo` for a busy agent, `ModelPauses`.

Nightly backup: `VACUUM INTO` plus gzip copy the whole file, so its time and the 7 kept days grow with it (today
6.5 MB a day gzip'd; about 110 MB a day at 12 months unpruned).

### What matters

1. Serve start, from about 2 months of log on (several seconds, 56 times a day, every MCP call waiting meanwhile).
2. Serve's heap: 340 MB at 6 months, 680 MB at 12.
3. The hook and launch reads of the session facts: over 100 ms per prompt at 12 months (AGENTS.md: hooks are fast).
4. `obor status` and the web overview: 0.4 s of store reads at 12 months.
5. Disk: 0.5 GB a year plus the backups.

Task 1 fixes most of the first. Pruning fixes the rest, and keeps the first small.

## 2. Task 1: the session fold keeps its index

`folded.sessions` becomes the store's fold (the `sessions` struct of `internal/store/sessions.go:82-87`, exported),
and `foldSession` calls its `add`. `Admits` and `sessionsOf` go; `Current` and `Holder` on a slice go if nothing
else calls them (`app/sessions.go:103`, `:107` then ask the fold). The rule and its order do not change, so the
existing session tests pass as they are; the tests of `Admits` move to the fold's `add`. No store change.

## 3. Task 2: the prune

### What is deleted

**Rule 1, tasks.** A merge-loop task is pruned when all of these hold, read from serve's fold under `mu`:

- its state is dropped, or merged with its after-merge steps done and no needs-you held or due. A rejected task is
  not ended for this (the owner still owes it a drop);
- its terminal fact is older than 30 days (`mtask.endedAt`);
- no lanes row names it, and no agent's current start row was recorded by it (below);
- with a review open, it was not dispatched after that review started (`fixCandidates`, `reviewverify.go:62-80`);
- the per-task check passes: folding only the fact that would stay gives an `mtask` equal to the live one on
  state, attempt, head, `mergedSeq`, `droppedSeq`, `droppedRev`, `endedAt` and the drop's reason, and the task has
  no `merged` fact other than the one that stays. A task that fails stays whole. On this store 1 of 718 fails:
  T-789, merged at attempt 1, dispatched again (before T-792) and dropped at attempt 2. Its merge would leave the
  reviews' merge list.

Deleted: every row of the task below the horizon, except its last terminal fact. The horizon is the log's last seq
at selection.

**Rule 2, sessions.** Session facts older than 30 days whose (agent, id) is not that agent's current session
(`store.Current`'s rule). A current session keeps both its records (the start and its T-813 confirmation).

Kept whole: every record whose task key is the project's (`msg`, `review`, `watch`, `nightly`, `pause`, `config`,
`idle`, `loop`, `disk`, `serve`, `rule`).

### Why the terminal fact stays

Measured by folding the real log before and after the delete. With nothing kept, these break:

- `--after T-old` never starts (`afterMerged`, `dispatch.go:221-228`: an unknown task is not merged).
- A merged task id can be dispatched again (T-792's refusal, `dispatch.go:371-379`).
- The queue lists a pruned dropped task's spec as never dispatched (`view/queue.go:146`: serve's status no longer
  names it).
- The reviews' merge list and `obor flakes` lose merges (`reviewfold.go`, `event.ReadMerges`).

27 of 722 tasks here have more than one terminal fact (dropped, dispatched again, merged): the last one is the
task's outcome, and the earlier drops have no reader. The kept facts are small: about 720 rows and 45 KB per 15
days.

### The two fold changes

1. `foldLoop` (`internal/app/mtask.go`): a `merged` fact of a task the fold does not know makes that task: merged,
   with its head, seq, time and attempt, its after-merge steps done (as an outside merge sets them, `:87`). Today
   it is ignored (`:63`). A `dropped` fact of an unknown task makes it dropped whatever its rev (`:29` asks
   `d.Rev > 0` today). On this store's full log neither changes anything: 722 tasks before and after.
2. `settles` (`internal/app/messages.go:118-128`): a `merged` fact settles its task's question, as `dropped` does.
   Without it the lead's answer to a question of a pruned task is an open line again after the next start, and
   serve types it into that lane's agent. Two such lines reopened on this store in the probe (seq 23995 and 24526).

With these, the before and after folds of this store are equal on: every unpruned task, the pruned tasks' named
fields, the lanes rows, the watchdog facts, watches, reviews and their merges, the rules' `did` keys, the open
lines, the idle state, the pending intents and `obor status` (a pruned dropped task loses its lane and start time
there). They differ, as intended, on per-task leftovers of pruned tasks: stored call replies, deliveries, start
times.

**The start rows are the one guard to add.** An agent's start row is folded from the start result of the task that
started it (`fold.go:25-37`). Pruning that task removes the row: 4 of 11 rows here with a 5-day cutoff, of lanes
no longer in the config (ob-c-01, ob-d-01, ob-d-02 among them). For a live agent it would drop its identity proof.
`identity.StartRow` (`identity.go:92-95`) gets the task that recorded it, and a task that holds a current start row
is not pruned. It pins at most one task per agent name.

### The five things the snapshot design had to answer

- **Seq references.** Kept records point at deleted ones only as keys already used (a rule's `did` key, a line's
  `review` or `stop`): nothing follows them. The other direction does not exist: no deleted record is the only
  end of a kept one, once `merged` settles answers (above).
- **`Log.Do`'s stored results.** A key is asked again only within its attempt. A merged task takes no new attempt;
  a dropped one's next dispatch takes the kept fact's attempt plus one (`waiting.go:81-86`), so no pruned key is
  ever asked again. A verdict call retried after 30 days is answered against the ended task, not from a stored
  reply.
- **`prompts.seq`.** Prompt rows go after 14 days by their own time (`prompts.go:15`), so a prompt whose fact is
  deleted went at least 16 days earlier. The retention is a constant beside `promptKeep`, and a test holds
  `eventKeep > promptKeep`.
- **The web cursor and seq reuse.** `seq` is the row id without AUTOINCREMENT, and the log's next seq is the
  table's maximum plus one (`event/log.go:323-324`, `:363`). If a project is idle for 30 days, the newest rows are
  a pruned task's, and deleting them would hand their seqs to new records: a cursor would miss them and a rule's
  `did` key would match a record it never saw. The horizon rule (delete only below the last seq) keeps the newest
  row whatever it is; it goes on a later night.
- **Upgrade compat.** No migration: the first night after the upgrade prunes whatever is old enough. Schema 14,
  with no DDL, so an older binary refuses a store that may be pruned (it would fold a lone `merged` fact to
  nothing). Rollback across this release is restoring a nightly backup, as for 12 and 13: one line in UPGRADE.

### Where it runs

In serve, in `App.backup` (`internal/app/log.go:12-27`), after `store.BackupDaily` returned without error and after
`prunePrompts`. So every deleted row is in that night's backup and the six before it. Not in `project.Sweep`
(T-1085): it runs in the CLI on files, and the store has one writer. Not on `wire.Backup`'s read-only path
(`wire/backup.go:33-43`), as the prompts.

- Selection under `mu`; the deletes outside it, at most 500 tasks per transaction. A task is deleted whole or not
  at all; a kill between batches leaves fewer tasks pruned, and the next night goes on. The delete is idempotent.
- Measured on the 12-month copy: 15,933 tasks and 549,941 rows in 1.0 s; 38,720 session rows in 0.24 s. A normal
  night (50 tasks, 1,812 rows): 4 ms.
- The SQL of task rows lives in package `event` (it owns the table's columns, `log.go:194-195`), the session rows'
  in `store/sessions.go`. `app` owns which tasks.
- One line in serve's log per night that deleted something; a failure is one `FAIL` line, as `prunePrompts`.
- **Serve's memory is not touched.** The running state stays a superset whose extra parts belong to tasks ended 30
  days ago, and the next start folds the smaller table. Here that is within the hour. A serve that never restarts
  keeps its heap until it does (1.8 MB a day at this project's pace). If a project shows that this matters, take
  the self-restart path after a prune; do not prune the in-memory maps by hand.
- The file stops growing and does not shrink: SQLite reuses the freed pages. The backups are compact already.

This store's log is 15 days old, so its first prune deletes nothing until about 11-25.

Retention: **30 days after the task ended**, a constant, no config key. The store then holds about twice today's
task records, so every measured cost stays near twice today's. 30 and not 14: the task page's timeline, a merged
task's report and gate history are what a deep dive or a review of last month's work reads.

### Visible changes to word in the spec

- A pruned task's page shows its outcome and its spec, no timeline (`view/detail.go:134`).
- The setup page's dispatch counts "for every record" become "since the retention" (`view/setup.go:241`).
- `obor serve dump` writes the pruned log.

## 4. Test table

Unit tests in `internal/app` with the package's fakes, on built histories. The oracle is a helper that folds a
history, prunes it in a temp store, folds again and compares; no test hand-writes a pruned log.

| case | expect |
|---|---|
| merged 31 days ago; merged 29 days ago; dropped 31 days ago; open; rejected 40 days ago; merged 31 days ago with after_merge still to run | only the first and third lose rows; each keeps its terminal fact |
| that history, folded before and after | unpruned tasks equal; pruned ones equal on the named fields; lanes rows, open lines, `did`, reviews, watches, idle and status equal |
| dropped at attempt 1, dispatched again, merged at attempt 2, 31 days ago | only the merged fact stays; folds to merged at attempt 2; the reviews' merges name it |
| a task with a `merged` fact and a later `dropped` one (T-789's shape) | the per-task check fails: the task stays whole |
| the lead's answer to a question of a task since merged and pruned | no open line after the fold |
| a waiting dispatch `--after` a pruned merged task | it starts |
| dispatch of a pruned merged task id | refused as today |
| dispatch of a pruned dropped task | its attempt is the dropped fact's plus one |
| a watch on a pruned merged task, and on a pruned dropped one | its line as today |
| ended 31 days ago and a lanes row names it; ended 31 days ago and it recorded an agent's current start row | both stay whole |
| ended 31 days ago, dispatched after the open review started | stays whole |
| the pruned task's rows are the table's newest | the newest row stays; the next record's seq is the old maximum plus one |
| a record of the task appended between selection and delete | it stays |
| prune twice | the second deletes nothing |
| a failed transaction in the second batch | the first batch's tasks are pruned, the others whole |
| an agent with three sessions older than 30 days, the newest confirmed | the newest keeps both its records, the others go; `CurrentAt` answers the same |
| the backup failed | nothing pruned |
| nightly without serve (the read-only path) | nothing pruned |
| a store at 13 opened by this binary; a binary at 13 on a store at 14 | 14; refused |
| `eventKeep` and `promptKeep` | `eventKeep` is the larger |
| the task page of a pruned task; the setup page's counts | outcome and spec, no error; the label says what it counts |

Before `task_done` the worker runs the oracle on a copy of this project's store and of axis-alies', with the
retention set to 0 and to 5 days, and reports each difference. That is the run that found the reopened lines, the
start rows and T-789. With the retention at 0 it deleted 24,738 of the 31,939 records here (717 tasks); with the
final rule the only differences left are the per-task leftovers named in §3.

Goes: nothing. `prunePrompts` stays where it is.

## 5. Risks, and what I would refuse

Risks:
1. **A fold I did not see.** The comparison ran on one store. An older store may hold a task whose kept facts fold
   differently; the per-task check keeps such a task whole, and the oracle run on axis-alies is the second sample.
2. **The verdict count** (`folded.verdicts`, compared with each start row's, `fold.go:33`) shrinks by the pruned
   verdicts. The difference between a start row and the count held in the probe (ob-int: 1 before and after). An
   integrator started over 30 days ago with no verdict since could miss one stop rule until its next verdict.
3. **A future fold that ends a kept record by a task's record** (as `settles` did) reopens it after a prune. The
   oracle test is what catches it: a new fold of this kind adds its row there.
4. **The first prune of an old store** deletes most of it in one night. Batches keep each transaction short; the
   night's backup holds what went.

Refuse:
- Deleting anything before the night's backup is written, or from any process but serve.
- Synthetic summary records, a state table, renumbered seqs or an archive table (as in `log-snapshot.md`).
- Pruning `msg`, `review` or `watch` records by age alone.
- Pruning serve's in-memory maps by hand, and a `VACUUM` step.
- A config key for the retention.
- Raising `upgradeFrom` because of this: the project's own records keep their old shapes, so this is not the
  snapshot's rule ("a store at N folds without the old readers").

## 6. Adjacent, flagged only

- Every overview reads every spec's text and scans the log for dispatched tasks (`view/view.go:219`, `:231`): 158
  ms at 12 months even when pruned. The specs table is never pruned (620 rows and 1.1 MB per 15 days).
- `Status()` ships every dropped task for ever (`status.go:74-75`): 576 at 12 months.
- The `roles` records (558 rows) are written by nothing now; they stay with the project's records.

## Method

- Copies: `scale.py` wrote N copies of the log into one store, each a distinct 16-day period: seqs offset, task
  ids renumbered, seq references and call ids shifted, session ids made distinct, times shifted back. The 1× copy
  folds to exactly the original's state (722 tasks, 11,237 results, 1,296 sessions, same load time).
- Timing: a test added with `go test -overlay` (no file of the repo changed) calls `App.load` on a temp root
  holding the copy and this project's config, then `Status`, `loopTasks` and the heap after a GC. A second one
  times each reader on a read-only open. The session fix and the two fold changes were overlays too.
- Pruned column: the rules above applied in SQL to the 12-month copy with the cutoff 30 days before the log's end.
  It folds without the tombstone rule, so it leaves out about 16,000 small `mtask` values.
- The copies carry 12 months of `roles` records, which a real store would not have. Machine: the owner's laptop.
