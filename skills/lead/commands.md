# obor lead: commands and their lines

## obor lead (T-553)
`obor lead` starts a fresh lead session (`RESTARTED <agent> (pane <p>, session new)`); `obor lead --resume` continues
the last one (`session <sid>`). An already running lead: `OK <agent> runs (pane <p>)`, nothing touched.

## obor status (T-657)
Read the line under serve's to see what runs without an agent: `running: gate T-656 (make -s check, 2m14s) · after_merge
T-655 (make build, 12s)` (else `running: nothing`); a lane's state column is its task's phase, the same word the queue's
`merge` line and web show (T-788): `dispatching`, `working`, `submitted`, `gating`, `gate pending`, `review`, `reviewing`,
`publish`, `merging`, `publish pending`, `merge hold: <role>`, `needs-you`, or `held` (the integrator's hold alone);
an undelivered line names why: `undelivered: <why>`. `obor status --short` is just that line, serve's and needs-you (a status bar).

## obor web (T-667)
`obor web` serves a page for every obor project on the machine at `http://127.0.0.1:8787/` (`--listen
127.0.0.1:<port>` for another loopback port; a host that is not loopback is refused). Its own process, never inside
serve; Ctrl-C ends it. The fleet page shows every project with its lanes, needs-you, queue and last full check, and
their events together; a project's dashboard puts what needs you, the pipeline by stage, its lanes, watches, queue and
role agents beside its live events (T-760); a task, an agent or a job opens as its own page — the
task's state, spec rev, report, gate line and log, needs-you, records and the text of every job; the agent's sessions
(transcript path and last write, never contents), briefs, lines and jobs; a job's whole prompt text in monospace.
The only verbs are the Setup page's edits (below, T-753): answer a needs-you with the command the page shows, as before. Localhost
only: remote access is an SSH tunnel.

## obor watch (T-523, T-528, T-666)
Clean merges stay quiet for you; a merge you must act on (restart serve after it, dispatch by hand, tell the owner)
is a watch, never a shell loop over `git log` or a background wait. A "when Y, do X" whose Y is a merge is a watch
with `--note "<what to do>"`: the line that tells you Y happened also tells you X.

- `obor watch <T-xx> [<T-yy> …] [--note "<text>"]` — one request per argument. An argument is a task or a comma list
  (`T-a,T-b,T-c`, as dispatch's `--after`): one watch on the set, a single task being a set of one. `--note` gives
  those watches your note (one line; what you meant to do when the set ends), shown by `obor watch` and `obor status`
  and carried by the line. Replies: `WATCHING T-xx` / `WATCHING T-a,T-b,T-c`; its line at once when it has
  ended already (nothing is watched; tasks of a set merged already count); `FAIL watch: unknown task …` for a task with
  no dispatch and no spec.
- `obor watch` — the open watches, `WATCH T-xx since <age>` each, a set adding its open tasks
  (`WATCH T-a,T-b,T-c since <age>, waiting T-c`), a watch with a note adding ` — <note>`; `NO WATCHES`.
- `obor watch --rm <T-xx>` — ends one (a set by the same comma list): `UNWATCHED T-xx` (`NOT WATCHING T-xx`). A note
  goes with a watch, not with `--rm`.

Lines to you, one per watch (the watch ends with it; it survives a serve restart and a lead handoff, like every line
to you):
- `[obor] T-xx merged <sha7>` (`merged (outside obor serve)` for a merge by hand)
- `[obor] T-xx dropped: <why>` (`rejected: <note>` for a task the integrator rejected)
- a set, once every task merged: `[obor] T-a,T-b,T-c merged (<the last merge's sha7>)`; the first task dropped or
  rejected ends it at once: `[obor] T-b dropped: <why> (watch T-a,T-b,T-c ended)`
- a watch with a note adds ` — <note>` to its line, merged or dropped

A task's needs-you reaches you as before (`[obor] needs-you …`), watched or not.

## Setup edits from obor web (T-753)
The owner may change the setup on obor web's Setup page instead of editing `.obor/config.json`: a difficulty level's
model refs, a role's model ref, the lanes count, a kind's args, and the pause. The page stages the edits and
the owner commits them together (T-760): serve writes them as one commit on main itself and tells you in one line:
- `[obor] config: <key> <old> → <new>; <key> <old> → <new> (owner, web)` — e.g. `[obor] config: difficulty.hard ["opus:high"] → ["sonnet","opus:high"] (owner, web)`.

A save made on a config that changed since the owner's page read it (your hand edit, say) is refused, nothing written:
the owner reviews it against yours.

It counts from the next dispatch (a role's type at its next start); nothing to do unless it changes your plan. The
commit is on main: pull before you edit the config by hand. A pause set or cleared there is the same as `obor pause`.

## obor nightly on|off (T-781)
`merge.nightly_at` is the nightly's switch, and you or the owner throw it; nothing else decides whether it runs. While
on, serve runs `obor nightly` (check, check_milestone, baseline, the forced gate, e2e, the store backup) every night at
that time, whatever merged since; off, never.
- `obor nightly on HH:MM` — `NIGHTLY ON at HH:MM` (`… already` when it was; nothing committed). A time that is not
  `00:00`–`23:59` is refused, the config unchanged.
- `obor nightly off` — `NIGHTLY OFF` (`NIGHTLY OFF already`). A run in progress is not stopped.
- `obor nightly` — one run now, as before.

Serve writes it as a setup edit (one commit on main, your `[obor] config: merge.nightly_at …` line) and reads it from
the next pass: no serve restart. `obor status` shows `nightly check at HH:MM (in …)`, or `nightly: off (obor nightly
on HH:MM)`.

## obor send and obor projects (T-646)
Every obor project on this machine is known by its config name: `obor serve` (at its start) and `obor init` write the
list, and `obor projects` prints it, one project per line: `<name> <root> serve <up|down>` (a project whose directory
moved or whose name changed is left out).

- `obor send <name> "<text>"` — to another project's lead; `lead@<name>` is the same (only leads get messages), and a
  path into the project still works. An unknown name: FAIL, then the names it knows ("no obor project named X (known:
  a, b, c)").
- The line the target's lead reads names its sender, so the reply is one command: `[obor <name>] <text> (reply: obor
  send <name> "…")`.

## obor review (T-638)
`obor review [--after T-a,T-b] [--note "<text>"]` (`--rm` ends it) — `REVIEW REQUESTED (now | after T-a,T-b)`. A start
is no line to you (T-650): `obor status` shows the review.
- After the fixes merged (T-716): `obor review verify <T-x,…> [--findings 1,3] [--note "<text>"]` marks the head
  reviewed (`refs/obor/reviewed`; T-718: no review worktree) and briefs the reviewer's same session to re-run its probes (the `obor brief` reply).
- When a verify approves: `obor review close "<summary>" [--accept "<line>"]…` deletes the review's files, adds each
  accepted line to `BACKLOG.md`'s Open section naming the RV, commits `RV-xx closed · <sha> · approved · <summary>`
  (`CLOSED <subject>`) and stops the reviewer (its `STOPPED …` line last). Refused, nothing changed: no open review,
  a main checkout dirty outside `BACKLOG.md`. Steps and texts: review-brief.md §4–5.

## An agent stopped by an API error (T-532)
A Claude agent whose turn ends on an API error (a usage limit `rate_limit`, `overloaded`, `billing_error`, …) did not
finish: it waits for the limit to reset or for you. Serve says so:
- a role agent (reviewer, deep dive, advisor, integrator): `[obor] <agent> stopped: <error> (<details>)` instead of its
  brief's `finished` line. Brief it again (or `obor restart <agent>`) once the limit has reset.
- a worker with an open task: `[obor] needs-you T-xx (<LANE>): <agent> stopped: <error> (<details>) → …`. It ends by
  itself when the worker's next turn starts (the owner types `continue` in its pane once the limit resets), or
  `obor resume T-xx "<text>"` sends the worker a line (typed once it is idle) that starts that turn.
The raw payloads are in `.obor/state/stop-failure.log`.

## obor dispatch (T-538)
You give the difficulty; obor picks the model and the lane (T-765: every lane is pooled, no caps). The spec's
`Difficulty: hard|medium|simple` line (right under the title; `obor spec set` refuses a spec without it) maps through
the config's `difficulty` to model refs, in order. An optional `Model: <model>[, …]` line right under it (`obor spec
set` refuses a model the config lacks, and the old `Type:` line) puts those models first, then the level's; `--model`
overrides both.

- `obor dispatch <T-xx> [--after T-yy[,T-zz]] [--force]` — the first model not paused, on the first free lane.
  Replies: `DISPATCHED T-xx → <LANE> (queued) as <model>`, or, with no free lane, `WAITING T-xx (queued): waiting for
  a free lane (<level>: <models>)`, or, every model paused, `WAITING T-xx (queued): <model> paused: <why> (until …), …`
  (also in `obor status`): serve dispatches it when a lane frees or a pause ends. `--after` adds `WAITING T-xx after
  T-yy (queued)`.
- `obor dispatch <T-xx> --model a,b` — overrides the level (smoke tests, Haiku, which is in no level); no free lane is
  a refusal, not a wait.
- `obor pause --model <name> [--for 3h] "<why>"` — dispatch skips that model (a quota, an outage) and a level's next
  one runs; `obor pause --model <name> --clear` ends it. A redo keeps its own model even while it is paused.
- Effort: any model named here (`Model:`, `--model`, config `difficulty`, `roles.<role>`) may be `<model>:<effort>`
  (`opus:high`, `luna:xhigh`): the agent starts at that effort, shown in the reply (`DISPATCHED T-xx → L-01 (queued)
  as opus effort high`) and `obor status`; an effort the kind lacks is refused.
- A spec with no level and no `--model`: `FAIL`, naming the fix. The `<LANE> <T-xx>` form is gone.
- A second `obor dispatch` of a waiting task replaces its request (new `--model`, `--after`); its watches stay.
  `obor drop <T-xx> "<why>"` cancels it (`DROPPED T-xx`; a watch on it ends with `dropped: <why>`).
- Move a working task to another model (T-773): `obor dispatch <T-xx> --model <model>[:<effort>]` on a task its worker
  works on. Serve stops the worker (as `obor drive <T-xx>` does), keeps the lane, branch, worktree and spec, and starts
  the new model's agent there with the task's next attempt: `MOVED T-xx on <LANE>: <old> → <new> (attempt n)`. One
  command, nothing to stop, drop or redispatch by hand. Refused: the task gating, merging or under review; the model it
  runs (`already on <model>`); a stop that fails (nothing recorded, the same command retries).
- `obor drop <T-xx> "<why>" [--discard] [--force]` stops the task's worker as part of the drop (a working one is
  interrupted with Escape and quits once idle), then frees its lane (serve knows it; the lane form is gone). `--force` is only for a worker that keeps working
  after Escape (or has background tasks): without it that drop is refused, nothing dropped. A refused free (a dirty
  lane without `--discard`) still stopped the worker; the refusal says so. Any other stop that fails (a quit
  timeout, a forced stop failing, herdr unreachable, two panes claiming the worker) refuses the drop, nothing freed or
  recorded: fix what it names, then the same `obor drop` retries. `obor restart <agent> --stop` stops the
  same way: no more "left running".
- `obor restart <lane's worker>` while its lane has a task restarts it with the task's open job: a fresh session gets
  the job's wake as its first prompt, a resumed one has it typed (`job T-xx/n/work: its wake …`). It never leaves an
  idle worker with no job; with no task on the lane it is unchanged.

## obor drive and obor land (T-751)
The owner iterates by hand (the web page's design after a worker's first pass, say) in a plain interactive agent on its
own branch, then sends the result through the same gate, integrator and merge as any task. A drive is a task on a
lane: serve sends it no jobs, its watchdog does not watch it, and a merge watch on it has no line until it lands.

- `obor drive <name> [--model <model>[:<effort>]]` — a new task (the next T-xx, its spec `Owner drive: <name>` stored
  by serve) on the first free lane, on branch `task/T-xx`; the model defaults to the hard level's first. Serve prepares
  the lane; the command then starts the drive agent
  (`<lane's worker>-drive`, role driver) in a pane split in the tab it runs in, its first prompt one line saying what it
  is. Replies: `DRIVE T-xx → <LANE> (<agent>, <model>)`, the start's lines, then `OK T-xx: <agent> drives in lane
  <LANE> on task/T-xx; obor land T-xx when done`. No free lane is a refusal naming the lanes' states, never a wait.
- `obor drive <T-xx>` — takes over a task its worker works on: serve stops the worker (as `obor restart --stop` does),
  keeps its lane, branch and spec, and the command starts the drive agent there: `DRIVE T-xx → <LANE> (<agent>,
  <model>) taking over from <worker>; STOPPED <worker>`. A task gating, merging or under review is refused. The worker
  is stopped before the take-over is recorded: a stop that fails refuses it with the error and records nothing (the task
  stays the worker's; retry with `obor drive <T-xx>`).
- Retry (T-755): `obor drive <T-xx>` or `obor drive <name>` on a drive already starts its agent again on its recorded
  lane and type when it is not running (a failed start's retry: `DRIVE … again: its agent is not running`, nothing
  recorded); running: `DRIVING T-xx → <LANE>: <agent> runs already (pane <id>)`, then `OK …`, nothing started.
- `obor land <T-xx|name> ["<summary>"]` — commits nothing (a dirty lane worktree is refused with its paths); submits the
  branch head on the owner's behalf, the summary as its report (default: the branch's commit subjects): `LANDED T-xx:
  submitted (gate, review and merge follow)`. From there it is an ordinary task; after the merge the drive agent is
  stopped and the lane freed.
- Back to the drive, never to a worker: a red gate, the integrator's sendback or hold (any count) make the task
  `driving` again, with one line to you: `[obor] T-xx back to its owner's drive: <why>`.
- `obor status` and `obor web` show the task as `driving`; `obor drop` works on a drive as on any task.
- A drive and you message each other (T-761). The drive agent runs `obor tell-lead "<text>"` (`obor lead` starts you,
  so its way to you has this name; a worker, reviewer, the owner or you are refused): you get the line
  `[obor T-xx drive] <text> (reply: obor resume T-xx "…")`, posted like your other lines. That reply must reach the drive,
  so tell-lead is refused (naming the state) from a land on — submitted, gating, under review — until the task is
  `driving` again. Your `obor resume T-xx "<text>"` on a driving task needs no open question: the drive agent gets
  `[obor answer] <text>` typed into (or posted to) its pane once it is idle, held while it is busy (reply `ANSWERED T-xx:
  obor serve sends <agent> the answer`).
  Each answer is its own line; one still undelivered when the drive lands or is dropped closes unsent. Use it for
  direction: contracts, protected docs, queued work.
