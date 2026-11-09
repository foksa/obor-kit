# Stored data audit: what obor writes, who reads it, what goes (advisor, 2026-11-09)

Question: owner 11-09, "obor keeps too much useless data... why do we log that tests pass when we cannot merge
unless tests pass". Audited on obor-kit's real data (15 days, 717 merges), with axis-alies as a second sample. Every
open was read-only; nothing was changed. Tree: `staging` at 43f2b988. Method at the end.

The owner's rule, used for every verdict: **a passing step leaves nothing behind but the fact it passed; output is
kept only when something failed and a person must read it.**

## Recommendation

Seven small tasks (table in §5). Four of them carry almost all the effect:

1. **A passing command writes no log file** (task A). 1,788 of the 1,809 task step logs here record a pass (98.8%).
   No code ever opens a step log; its path is only named in a RED or WARN line.
2. **The candidate's diff and stat are stored once, not three times** (task B). The gate result, the publish intent
   and the after_merge intent each hold a full copy. Only the review job reads them, from the gate result. The two
   extra copies are 4.48 MB of the log's 16.75 MB (27%), and the same share of serve's heap.
3. **Prompts are kept 3 days, not 14** (task C). The prompts table grows 1.4 MB a day, more than the event log's
   1.1 MB. It is a third copy of each diff and a second copy of each spec and report, read only by three web pages.
4. **The shim removes old release binaries** (task E). `~/.obor/bin` holds 27 binaries, 332 MB; the shim can run
   only the kit's current version (18.5 MB). This is more than the whole project state (201 MB).

Expected store size at steady state, at last week's pace: about 40 MB instead of about 70 MB (§3).

**The answer to "why do we log that tests pass":** there are two things. The log *file* of a passing run is useless
and goes (task A). The *row* in the event log saying a gate or test passed is the fact the merge loop decides on; it
is a few hundred bytes and stays. What was wrong with the rows is the payload copied around them (task B).

**T-1089 does not change** (§6). It is already merged (43f2b988, 11-09 09:49), not waiting.

**T-1090's step goes once task A is in** (§7). With no pass logs, it has about one file a day left to remove, and
the runner's 14 days ends those. Nothing replaces it.

**The sweep has never run on this project.** T-1085, T-1086 and T-1090 merged after the 02:00 nightly. Tonight's
run removes about 20 MB (the four `herd-*.db` backups, the oldest day, the retired files); the backups fall from
153 MB to about 45 MB by 11-16, as gzip'd days replace the uncompressed ones. Do not spec anything for the items
marked "goes tonight" below.

## 1. Totals on obor-kit, 2026-11-09

| where | size | files | of it useless |
|---|---|---|---|
| `~/.obor/bin` | 332 MB | 27 | 25 binaries, 314 MB: nothing can run them |
| `.obor/state/backup` | 153 MB | 11 | 4 `herd-*.db` (8.9 MB) and uncompressed days of 11 to 32 MB; about 45 MB with 7 gzip'd days |
| `.obor/state/obor.db` (+ wal) | 34.6 MB (+ 4.1) | 1 | 4.5 MB duplicate diffs, 8 of 10.8 MB prompts older than 3 days |
| `.obor/state/logs` | 3.1 MB of content, 10 MB on disk | 1,840 | 1,788 pass logs (0.6 MB of content, 7 MB of disk blocks) |
| `.obor/drafts` | 7.8 MB | 642 | one 5.4 MB `release-0.18.1-check.log`; goes 11-16 by the sweep's 14 days |
| `.obor/state/receipts`, `merges.log`, `merge-changed-files` | 0.4 MB | 87 | all, retired; goes tonight |
| everything else under `.obor/state` | 0.3 MB | about 45 | see §2 |

## 2. Files under `.obor/state`

No code reads a step log, `launch.log`, `restart-*.log`, `stop-failure.log`, `conformance.json`, `serve.log` or a
backup. Where a reader is named below for those, it is a person or an agent following a path in a line.

### 2.1 `logs/`

Writer of every step log: `runner.writeLog` (`internal/checks/runner.go:157`), called for every command run, green
or red, from `execCommand` (`:150`). The log name is the task (`scratch.go:173` gate, `:689` after_merge,
`lanetest.go:75` lane test) or `nightly` (`checks.go:93`). Readers: none in code. The path is put into a line by
`evidence` (`runner.go:313`, RED lines, which reach the worker's fix job at `app/steps.go:403` and the lead's
needs-you) and by `afterMergeWarn` (`scratch.go:712`). Both run only on a failure. Retention: 14 days
(`runner.go:154`), and 3 days after the task's merge (T-1090, `sweep.go:284`).

| log | files | bytes | what a pass leaves | verdict |
|---|---|---|---|---|
| `<task>-after_merge.log` | 614 | 78 KB | the `go build` command line, 126 bytes | SHRINK: failure only |
| `<task>-guard.log` | 424 | 34 KB | `OK` and one summary line | SHRINK: failure only |
| `<task>-check_affected.log` | 349 | 234 KB | the `ok <package>` list | SHRINK: failure only |
| `<task>-check.log` | 250 | 1.29 MB | the `ok <package>` list; one red run is 897 KB of it | SHRINK: failure only |
| `<task>-compile.log` | 124 | 5 KB | one 40-byte line, the same in all 124 | SHRINK: failure only |
| `<task>-check_release`, `-check_web`, `-check_readers.log` | 33 | 4 KB | one or two lines | SHRINK: failure only |
| `<task>-retest.log` | 15 | 25 KB | written only after a red run | KEEP when the retest failed |
| `nightly-<step>.log` | 6 | 9 KB | overwritten each night | SHRINK: failure only (same code) |
| `release-<version>.log` | 18 | 70 KB | the release script's whole run (`scripts/release:281`) | KEEP: about 4 KB a release, ends at 14 days |
| `milestone-*`, `promote-retest`, `conformance-turn*.log` | 6 | 23 KB | no writer in the tree | nothing to do: the 14 days end them by 11-21 |
| `serve.log` | 1 | 1.32 MB | serve's stdout, 20,003 lines in 8 days | SHRINK: see below |

About 21 of the 1,809 task step logs record a failure (grep for `FAIL`, `panic:`, a timeout; 1.07 MB, of which one
file is 897 KB). The same shape holds on axis-alies: 223 of its 227 logs are `lane_setup` and `check` of 110 merged
tasks.

`serve.log` (writer: launchd, `internal/service/service.go:99-102`; rotated at 5 MiB only by `obor serve install`
and `restart`, `:56-70`). No code reads it; it is where serve's `FAIL` lines land (342 here), so it stays. What is
in it:

| lines | bytes | what | verdict |
|---|---|---|---|
| 7,363 | 334 KB | one line per appended record, `<time> <project> <task> <step> (attempt n)` (`internal/app/loop.go:395`) | DROP: each line is a row of the events table, which `obor serve dump` and the task page already show |
| 3,369 | 227 KB | `agent … identified at pane …` | KEEP |
| 2,008 | 159 KB | `…'s start row retired: the config names no such agent` | flagged, §8: the same 4 to 8 rows at every start |
| 7,263 | 603 KB | the rest: waits, verdicts, restarts, `FAIL` lines | KEEP |

### 2.2 The other files

| file | here | writer | readers | verdict |
|---|---|---|---|---|
| `backup/obor-<date>.db(.gz)` | 153 MB, 11 | `store.BackupDaily` (`store/store.go:90`) from `app/log.go:21` | none in code; the owner on a restore (`skills/lead/commands.md:86`) | KEEP, 7 days (`sweep.go:21`). First gzip'd backup and first prune are tonight |
| `obor.db` | 34.6 MB | serve | everything | §3 |
| `flakes.jsonl` | 54 KB, 280 lines | `appendFlakes` (`checks/flakes.go:130`), red runs only; `scripts/release:134` | `obor flakes` (`cmd/obor/flakes.go:25`), which looks back 7 days (`flakes.go:32`) | SHRINK: never pruned; the sweep drops entries older than the 7 days the reader uses (task F) |
| `launch.log` | 54 KB, 39 entries since 10-17 | `logEvidence` (`launch/evidence.go:108`), on a give-up only | none in code. The CLI prints the same evidence (`cmd/obor/restart.go:71`, `sleep.go:432`); for a start serve ran, the file is the only copy | KEEP (failure output). SHRINK: never pruned; goes when not written for 14 days (task F) |
| `restart-<agent>.log` | 13 KB, 4 | the detached restart's output (`launch/self.go:46`, `:70`) | none in code; the only place a failed scheduled restart is seen | KEEP. Same 14-day rule (task F); two of the four are herd-era agents and go tonight |
| `stop-failure.log` | 1 KB, 5 lines | `guard.StopFailure` (`guard/stopfailure.go:62`) | none in code; named in `skills/lead/commands.md:151` | KEEP: failure output, 14 days (`sweep.go:335`), the only record when serve is down. The `stopped` fact holds the same error otherwise |
| `background.jsonl` | 39 KB, 86 lines | `AppendBackground` (`project/background.go:35`) from the Bash hook | `view.BackgroundOf` (`view/background.go:308`): `obor status` and the web's process list | KEEP: capped at 64 KiB, newest 100 lines (`background.go:29`) |
| `lane-changed-files-<lane>`, `run-changed-<lane>` | 4 + 12, under 1 KB each | `runner.changed` (`runner.go:341`); `run.exportChangedFiles` (`run/run.go:157`) | `tools/affected/main.go:39`, through `OBOR_CHANGED_FILES` | KEEP: one per lane, overwritten. 8 of gone lanes go tonight |
| `serve-changed-files` | 1.5 KB, from 11-07 | none: the gate and after_merge runners pass the name (`scratch.go:173`, `:689`) and never write it | none | DROP: the name argument and the leftover file (tasks A and F) |
| `conformance.json` | 1 KB | `tests/conformance/report.go:161`, every `make conformance` | only its own writer (`report.go:119`); the `obor status` line its comment promises (`:19`) was never written | DROP (task G): the nightly's result line already has the counts |
| `last-full-check` | 382 B | `checks/nightly.go:137` | serve's nightly needs-you (`cmd/obor/serve.go:263`), `obor status`, the web | KEEP |
| `inbox/<slot>` | 7, 130 B each | `project.WriteInbox` (`project/plugin.go:93`) | `cmd/obor/send.go:160`, `:213`: decides delivery | KEEP |
| `lanes/<lane>.plugin`, `.setup` | 7, 49 B each | `plugin.go:149`; `setup/lane.go:69` | `view/view.go:520`; `setup/lane.go:130` | KEEP |
| `kinds/<kind>.json`, `opencode.json` | 2 | `agents/plugin/plugin.go:251`; `calls.go:161` | every process that loads the kind; OpenCode itself | KEEP |
| `serve.flock`, `serve.sock`, `stopped`, `*.lock` | | | serve's liveness and locks | KEEP |
| `receipts/`, `merges.log`, `merge-changed-files`, `backup/herd-*.db` | 0.4 MB + 4 files | none | none | goes tonight (`sweep.go:71-77`, `:112`) |

## 3. The store

`obor.db`, schema 14: events 20.9 MB (32,758 rows, 16.75 MB of data), prompts 11.8 MB (1,332 rows), specs 1.5 MB (638
rows), the task index 0.6 MB, lanes and meta 4 KB each. Growth: 2,180 records and 1.12 MB of event data a day over
the 15 days (1.63 MB a day in the last 7 full days); prompts 1.4 MB a day.

### 3.1 Events: the big fields

Four fields are 61% of the log's data:

| field | where it is stored | rows | bytes | readers | verdict |
|---|---|---|---|---|---|
| `candidate.Diff`, `candidate.Stat` | every green gate result | 1,048 | 2.74 MB | `diffText` (`app/jobstart.go:250-259`), the review job's text; set by the fold at `app/mtask.go:178` | KEEP: the one copy the job is built from |
| `Diff`, `Stat` | every publish intent (`app/steps.go:337`, `Data: mt.cand`) | 943 | 2.55 MB | none | DROP (task B) |
| `Diff`, `Stat` | every after_merge intent (`app/steps.go:69`) | 717 | 1.93 MB | none | DROP (task B) |
| `report` | every submitted fact | 834 | 3.00 MB | the merge commit's message (`app/steps.go:189`, `:232`), the review job (`jobstart.go:203`), the task page (`view/detail.go:32`) | KEEP until the 30-day prune: it is the worker's statement, not a log of success |

Why the two intents' copies are dead: `Publish` and `AfterMerge` run on `mt.cand` from memory and use only its `SHA`,
`Target` and `Task` (`checks/scratch.go:575-630`, `:638-693`). An intent's data is read back only for start steps
(`p.log.Intent` at `jobstart.go:41`, `workerstart.go:133`, `:275`); `Log.Begin` only stores it
(`event/log.go:467`), and the timeline has no entry for either intent (`describe.go:52-202`). So the change needs no
migration and no schema step: older rows keep their copies until T-1089 prunes them.

axis-alies shows the same share: diff and stat are 0.90 MB of its 2.24 MB (40%), reports 0.54 MB.

### 3.2 Events: everything else

All of it is read by the fold and is small. Nothing here records only a success that something else proves.

| records | rows | bytes | verdict |
|---|---|---|---|
| gate and test results without the diff and stat (the line, the outcome, the candidate's shas) | 1,432 | 0.67 MB | KEEP: this is "the fact it passed", about 470 bytes each. Gates: 1,048 green, 143 red, 87 conflict, 3 fail |
| after_merge results | 717 | 4 KB | KEEP: 6 bytes each |
| `session` facts | 1,834 | 444 KB | KEEP: T-1089 prunes them at 30 days |
| `delivered`, `dispatch`, `lane.prepare`, `agent.start`, `start.review`, `submit`, `merged`, `waiting` and the other task steps | about 21,500 | 3.9 MB | KEEP: the merge loop's state, about 180 bytes each |
| `msg` (`message`, `briefed`, `worked`, `answered`) | 3,235 | 475 KB | KEEP |
| `review`, `watch`, `idle`, `pause`, `nightly`, `loop`, `disk`, `agents`, `config` | 868 | 156 KB | KEEP |
| `roles` (no writer since 11-05), `promote` (deleted in T-1076) | 578 | 33 KB | flagged, §8: no task for 33 KB |

### 3.3 The prompts table

Writer: `App.prompt` (`app/prompts.go:21`) from `fetch.go:117` (each job's text) and `messages.go:946`, `:948` (briefs
and start prompts). Readers: the web's task, agent and job pages only (`view/detail.go:158`, `:211`, `:238`). Nothing
decides on it. Retention: 14 days (`prompts.go:15`).

| kind | rows | bytes | average | what it copies |
|---|---|---|---|---|
| review jobs | 502 | 6.28 MB | 12.5 KB | the spec, the report and the diff, all in the log or the specs table already |
| work jobs | 432 | 3.82 MB | 8.9 KB | the spec (1.8 KB on average) and constant role text |
| fix and resolve jobs | 185 | 0.35 MB | 1.9 KB | the sendback, in the log |
| start prompts | 87 | 0.24 MB | 2.8 KB | |
| briefs | 126 | 0.12 MB | 1.0 KB | the `briefed` fact's `line` |

10.8 MB in the 8 days since T-665 landed: 1.4 MB a day, so 19 MB at the 14 days it is heading for. Verdict: SHRINK to
3 days (task C), about 4 MB. A prompt is read to see what an agent was told while its task is open or just merged;
tasks here go from dispatch to merge within hours, and 3 days is the retention T-1090 chose for the same reader.

### 3.4 The specs table

638 rows, 1.12 MB; 616 belong to merged tasks. No retention. Readers: `obor spec show` and `list`
(`cmd/obor/spec.go:96`), the queue (`view/queue.go:227`), the task page, `obor flakes` (`checks/flakes.go:347`).
Verdict: KEEP. A spec is the only copy of what a task asked for (drafts go after 14 days), and deleting a dropped
task's spec would change what a re-dispatch and the queue compare (`droppedRev`). At 27 MB a year it is not worth
that risk now.

### 3.5 Store size after tasks B and C

At last week's pace, with T-1089's 30 days: events about 49 MB plus prompts 20 MB, about 70 MB. With B and C: events
about 36 MB plus prompts 4 MB, about 40 MB. Serve's heap falls with B in the same proportion, since the log keeps
every intent's data in memory (`event/log.go:125-127`).

## 4. The repo and the home directory

| what | here | writer | readers | verdict |
|---|---|---|---|---|
| `~/.obor/bin/obor-<version>-…` | 332 MB, 27 files (15.8 MB a release, 2.7 MB a kind binary) | the shim's fetch (`bin/obor:96-126`) | the shim runs only the kit's `VERSION` (`bin/obor:67-70`, `:231`) | DROP the old ones (task E): 295 MB go, two versions (37 MB) stay |
| `~/.obor/kit` | 32 MB | `install.sh` | the installed kit | KEEP |
| `~/.obor/restore.log` | 20 KB | `launch/restore.go:250` | none in code; `templates/agents/gotchas.md:17` | KEEP: capped at 200 lines (`restore.go:39`) |
| `~/.obor/projects.json` | 120 B | `project/projects.go:129` | `obor send`, `obor projects`, the web | KEEP |
| `~/.obor/serve/`, `~/.obor/opencode-state/` | 0.3 MB | none in the tree (10-30 and 11-02 leftovers) | none | the owner removes them by hand; no task |
| `.obor/drafts/` | 7.8 MB, 642 | the lead (`skills/lead/SKILL.md:29`) | `obor spec set` | KEEP: 14 days (`sweep.go:312`) |
| `.obor/cache/` | 8 KB, 2 files from 10-15 | none in the tree | none | DROP: the sweep's retired list (task F) |
| `docs/reviews/` | 292 KB, 13 | the reviewer agent | `app/reviewverify.go:296`, `:417`; serve deletes a review's files at its close (`cmd/obor/reviewcycle.go:26`) | KEEP |
| `refs/obor/*` | 12 refs | the gate | publish, the review job | KEEP: removed at publish or reject |
| `BACKLOG.md`, `.obor/config.json` | | serve's review close and `config_set` | the project | KEEP |

## 5. Tasks

Letters stand for the task ids the lead assigns. No two tasks share a file, except that F runs `--after` A and G
(it owns the sweep, and it edits a second test in the nightly test file A touches).

| | change | files | tests |
|---|---|---|---|
| **A** | A command that passes writes no log and removes an earlier log of the same name; a failed one writes it as today. The 14-day prune runs at a run's first command, not at its first write. The unused changed-files name of the gate and after_merge runners goes. | `internal/checks/runner.go`, `scratch.go`, `checks.go` | changed: `decide_test.go:742` `TestAfterMergeDecisions` (a green run leaves no log), `flakes_integration_test.go:351` `TestRunPrunesOldLogs` (`:382` expects a green gate's log), `nightly_integration_test.go:30` `TestNightly` (`:66-76`, a log only for its red case). Added: one case, a red run then a green one leaves no log |
| **A2** | The task page's `gate_log` goes. It always names `<task>-check.log`, which a lane-tested task never has (349 of the 599 tasks with a test log here); the RED line carries the real path. | `internal/view/detail.go` (`:149`, `:302-305`), `web/src/lib/types.ts:354`, `web/src/pages/Task.svelte:125` | changed: `internal/web/web_test.go:267` `TestTaskPage` (`:287`), the view's golden file |
| **B** | The publish and after_merge intents carry the candidate's `Task`, `Lane`, `SHA` and `Target` only. | `internal/app/steps.go` (`:69`, `:337`) | added: one, both intents' data hold no `Diff` or `Stat`. None found that pins the old payload |
| **C** | `promptKeep` is 3 days. | `internal/app/prompts.go:15`, the comment at `internal/store/prompts.go:15`, `docs/state-model.md:215` | none change: `prune_test.go:698` (`eventKeep` the larger) still holds |
| **D** | Serve no longer prints one line per appended record. | `internal/app/loop.go:395` | deleted: any assertion on that line (none found by grep) |
| **E** | After a fetch, the shim removes the release and kind binaries of every version but the kit's and the one fetched before it. | `bin/obor` | added in `cmd/obor/shim_integration_test.go`: a fetch with three older versions present leaves two |
| **G** | `make conformance` no longer writes `conformance.json`. | `tests/conformance/report.go`, `main.go` | deleted: the `TestSave*` tests of `tests/conformance/report_test.go` (`:59`, `:83`, `:120`, `:136`, `:162`, `:183`) |
| **F** (after A, G) | The sweep: `flakes.jsonl` loses entries older than 7 days; `launch.log` and `restart-<agent>.log` go when not written for 14 days; the retired list gains `serve-changed-files`, `conformance.json` and `.obor/cache`; the merged-task log step (T-1090) goes with its merge reader (§7). | `internal/project/sweep.go`, `internal/checks/nightly.go` (`:103`, `:149-163`), `docs/module-map.md:33` | changed: `sweep_test.go` (`:158-178` never-touched list; every `Sweep` call loses its fourth argument), `nightly_integration_test.go:107-108`, `:396-397` (no `sweep merged logs` line). Deleted: `sweep_test.go:197-260` (five merged-log cases), `internal/checks/nightly_test.go:14-56` (`sweepMerges`). Added: one case per new rule |

Order: B, C and E first (the bytes). A and A2 next. D and G any time. F last.

One exception to state in the specs of A and G: AGENTS.md says a task that deletes a writer adds its file to the
sweep's retired list. Here F adds those lines, so that only F edits `sweep.go`.

## 6. T-1089

It is merged, and its rule stays: 30 days after a task's end, all but the terminal fact, in the nightly backup.

- Nothing found here is a record the prune should treat differently. The duplicate diffs are removed at the writer
  (task B), not by a second prune rule; rows written before B go with their task at 30 days.
- 30 days stays. With B the window holds about 36 MB instead of 49 MB, so there is no size reason to shorten it, and
  the report and the timeline are what a deep dive of last month's work reads.
- Its test that `eventKeep` is larger than `promptKeep` holds with C (30 days and 3).
- Do not add the specs table to it (§3.4).
- Its first prune here is about 11-25; nothing waits on these tasks.

## 7. T-1090

T-1090 (merged 11-09, 9d4e608c) makes the nightly sweep remove `logs/<task>-<step>.log` 3 days after the task's last
merge. **Delete it in task F, after task A. Nothing replaces it.**

What it would still do after A: a passing command leaves no log, and a pass removes an earlier red log of the same
step. A merged task then has a log only where a step went red and never ran green under that name again: a red
check that a retest saved, a failed retest, a failed after_merge. That is about 21 files per 15 days here, about
one a day. The runner's own prune (`run.PruneOlder`, 14 days, `checks/runner.go:154`, `:165`) ends them. Without
T-1090's step about 20 such files stand at any time instead of about 4.

What it costs to keep: about 70 lines of code, five test cases and one test file; a read of every merged fact each
night (`event.ReadMerges`: 6 ms today, 37 ms at 12 months with T-1089's prune, per `event-log-pruning.md` §1); and
a `sweep merged logs: no store` line in every nightly of a project serve never made a store for.

What goes:

- `internal/project/sweep.go`: `mergedLogKeep` (`:27-29`), `Merge` and `Merges` (`:31-41`), the `merged logs` step
  (`:79-88`), `sweepMergedLogs` and `mergedLog` (`:281-309`), and `Sweep`'s `merges` parameter (`:55`).
- `internal/checks/nightly.go`: `sweepMerges` (`:149-163`) and its argument at `:103`.
- Tests: `sweep_test.go:197-260`, `internal/checks/nightly_test.go:14-56`, and the two `sweep merged logs: no store`
  assertions (`nightly_integration_test.go:107-108`, `:396-397`).
- `docs/module-map.md:33` loses its clause.

Two conditions:

1. **F lands after A, never before.** Without A every merge still writes 3 to 5 pass logs, and without T-1090 they
   would stand for 14 days again (about 1,700 files).
2. **Task A must run the 14-day prune at a run's first command.** Today it runs at a run's first log write
   (`runner.go:163-166`); after A a green run writes none, so a project whose runs are all green would never prune.
   This is in A's row in §5 and is the one thing the replacement depends on.

The pass logs written before A lands need no migration: T-1090's step removes those of merged tasks while it is
still there, and the 14 days end the rest.

## 8. Flagged, not expanded

- **`start row retired` repeats at every serve start**: 2,008 lines for the same 4 to 8 agents the config no longer
  names (`ob-l-05`, `ob-l-06`, `ob-d-02`, `ob-c-01` this morning). The retirement is not durable, or the line should
  print once. Not looked into.
- **`serve.log` rotates only on `obor serve install` or `restart`**, not when serve re-execs on a new build (419
  times here). At 165 KB a day it passes 5 MiB in about a month and keeps growing until a manual restart.
- **Briefs are stored twice**: the `briefed` fact's `line` (152 KB) and a prompts row (124 KB). `store/prompts.go:13`
  says a line has no prompts row; briefs do. Task C bounds it.
- **`roles` records** (558 rows, 30 KB) have no writer since 11-05 (`event/steps.go:30`: compat until v0.28), and
  `promote` records (20 rows) lost theirs in T-1076. They are project records, so T-1089 keeps them for ever.
- **The gate result still holds the diff after the review ends** (2.74 MB per 15 days). Removing it needs a git call
  when the review job is built. Not worth it while the 30-day prune takes it.

## Method

- Files: `du`, `stat` and `find` on `.obor/` and `~/.obor`; step logs grouped by the step in their name; a failing
  log is one that matches `FAIL`, `panic:`, `timed out`, `Error <n>` or `[build failed]` (an estimate: the exit code
  is not in the file).
- Store: `sqlite3` on `file:obor.db?mode=ro`; table sizes from `dbstat`; field sizes from `json_each` and
  `json_extract` over `events.data`. axis-alies the same way.
- Writers and readers: grep over `cmd/`, `internal/`, `kinds/`, `tools/`, `tests/conformance`, `bin/obor`,
  `scripts/`, `web/src`, `skills/`, `templates/` and `docs/agents`, non-test code first. "None in code" means no
  non-test code opens the file. Whether a person opens a file cannot be measured; where a role text or a result line
  names it, that is cited.
- Not verified: that no test pins the publish or after_merge intent's payload (grep found none), and what
  `TestNightly` asserts at `:76` on a green run. The worker of B and A confirms each.
