# obor lead: commands and their lines

## obor lead (T-553)
`obor lead` starts a fresh lead session (`RESTARTED <agent> (pane <p>, session new)`); `obor lead --resume` continues
the last one (`session <sid>`). An already running lead: `OK <agent> runs (pane <p>)`, nothing touched.

## obor status (T-657)
Read the line under serve's to see what runs without an agent: `running: gate T-656 (make -s check, 2m14s) · after_merge
T-655 (make build, 12s)` (else `running: nothing`); a lane's state column reads `gating`, `merging`, `reviewing` or
`held` while serve works on it. `obor status --short` is just that line, serve's and needs-you (a status bar).

## obor monitor (T-658)
The project's live view, in the `system` tab of its herdr workspace (a pane labelled `<prefix>-monitor`; `obor init`
makes it, `obor setup monitor` adds it to a project set up before). It is `obor status` redrawn in place, plus the
store's recent events:

- serve's line and the `running:` line; needs-you, in full, with the commands that answer them;
- one line per lane: its state column, task, how long it has been in that state, and its agent idle or working;
- the open review request (or `review due`), the watches and the dispatches queued on `--after` tasks;
- the recent events, newest first, with their local time: dispatched, submitted, gate pass/fail with its line,
  accepted, sendback, held, merged with its sha, a review requested or started, a line posted, serve restarted.

It redraws on every new record and at least every 2 s (elapsed times), says `serve: restarting…` while serve is between
restarts and reconnects by itself, and ends on Ctrl-C. Read-only, any role: the lane's states are serve's and git's,
never the monitor's own. `obor setup monitor` (idempotent: an existing pane is reused) is how a project gets one later.

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
  those watches your note (one line; what you meant to do when the set ends), shown by `obor watch`, `obor status` and
  the monitor and carried by the line. Replies: `WATCHING T-xx` / `WATCHING T-a,T-b,T-c`; its line at once when it has
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
types, a lane type's model, effort or max, a lane's type, a role's type, and the pause. The page stages the edits and
the owner commits them together (T-760): serve writes them as one commit on main itself and tells you in one line:
- `[obor] config: <key> <old> → <new>; <key> <old> → <new> (owner, web)` — e.g. `[obor] config: difficulty.hard ["opus:high"] → ["sonnet","opus:high"] (owner, web)`.

A save made on a config that changed since the owner's page read it (your hand edit, say) is refused, nothing written:
the owner reviews it against yours.

It counts from the next dispatch (a role's type at its next start); nothing to do unless it changes your plan. The
commit is on main: pull before you edit the config by hand. A pause set or cleared there is the same as `obor pause`.

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
  itself when the worker's next turn starts (the owner types `continue` in its pane once the limit resets); nothing
  to resume.
The raw payloads are in `.obor/state/stop-failure.log`.

## obor dispatch (T-538)
You give the difficulty; obor picks the lane. The spec's `Difficulty: hard|medium|simple` line (right under the title;
`obor spec set` refuses a spec without it) maps through the config's `difficulty` to lane types, in order. An optional `Type: <lane type>[, …]` line right under it (`obor spec set` refuses a type the config lacks) puts those types first, then the level's; `--types` overrides both.

- `obor dispatch <T-xx> [--after T-yy[,T-zz]] [--force]` — the first free lane of the level's types. Replies:
  `DISPATCHED T-xx → <LANE> (queued)`, or, with no free lane of the level, `WAITING T-xx (queued): waiting for a
  <level> lane (<types>)` (also in `obor status`): serve dispatches it when one frees, never onto a higher level's lane.
  `--after` adds `WAITING T-xx after T-yy (queued)`.
- `obor dispatch <T-xx> --types a,b` — overrides the level (smoke tests, Haiku, which is in no level); no free lane of
  those types is a refusal, not a wait.
- Effort: any type named here (`Type:`, `--types`, config `difficulty`, `roles.*.type`) may be `<type>:<effort>`
  (`opus:high`, `luna:xhigh`): it matches lanes of the type and starts the agent at that effort, shown in the reply
  (`DISPATCHED T-xx → O-01 (queued) effort high`) and `obor status`; an effort the kind lacks is refused.
- A spec with no level and no `--types`: `FAIL`, naming the fix. The `<LANE> <T-xx>` form is gone.
- A second `obor dispatch` of a waiting task replaces its request (new `--types`, `--after`); its watches stay.
  `obor drop <T-xx> "<why>"` cancels it (`DROPPED T-xx`; a watch on it ends with `dropped: <why>`).

## obor drive and obor land (T-751)
The owner iterates by hand (the web page's design after a worker's first pass, say) in a plain interactive agent on its
own branch, then sends the result through the same gate, integrator and merge as any task. A drive is a task on a
lane: serve sends it no jobs, its watchdog does not watch it, and a merge watch on it has no line until it lands.

- `obor drive <name> [--type <type>[:<effort>]]` — a new task (the next T-xx, its spec `Owner drive: <name>` stored by
  serve) on the first free untyped lane, on branch `task/T-xx`; the type defaults to the hard level's first, and a drive
  counts toward no `lane_types.<type>.max`. Serve prepares the lane; the command then starts the drive agent
  (`<lane's worker>-drive`, role driver) in a pane split in the tab it runs in, its first prompt one line saying what it
  is. Replies: `DRIVE T-xx → <LANE> (<agent>, <type>)`, the start's lines, then `OK T-xx: <agent> drives in lane
  <LANE> on task/T-xx; obor land T-xx when done`. No free untyped lane is a refusal naming the lanes' states, never a
  wait.
- `obor drive <T-xx>` — takes over a task its worker works on: serve stops the worker (as `obor restart --stop` does),
  keeps its lane, branch and spec, and the command starts the drive agent there: `DRIVE T-xx → <LANE> (<agent>,
  <type>) taking over from <worker>; STOPPED <worker>`. A task gating, merging or under review is refused. The worker
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
- `obor status`, the monitor and `obor web` show the task as `driving`; `obor drop` works on a drive as on any task.
- A drive and you message each other (T-761). The drive agent runs `obor tell-lead "<text>"` (`obor lead` starts you,
  so its way to you has this name; a worker, reviewer, the owner or you are refused): you get the line
  `[obor T-xx drive] <text> (reply: obor answer T-xx "…")`, posted like your other lines. That reply must reach the drive,
  so tell-lead is refused (naming the state) from a land on — submitted, gating, under review — until the task is
  `driving` again. Your `obor answer T-xx "<text>"` on a driving task needs no open question: the drive agent gets
  `[obor answer] <text>` typed into (or posted to) its pane once it is idle, held while it is busy (reply `ANSWERED T-xx:
  obor serve sends <agent> the answer`).
  Each answer is its own line; one still undelivered when the drive lands or is dropped closes unsent. Use it for
  direction: contracts, protected docs, queued work.
