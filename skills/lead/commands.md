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
`obor spec set` refuses a spec without it) maps through the config's `difficulty` to lane types, in order.

- `obor dispatch <T-xx> [--after T-yy[,T-zz]] [--force]` — the first free lane of the level's types. Replies:
  `DISPATCHED T-xx → <LANE> (queued)`, or, with no free lane of the level, `WAITING T-xx (queued): waiting for a
  <level> lane (<types>)` (also in `obor status`): serve dispatches it when one frees, never onto a higher level's lane.
  `--after` adds `WAITING T-xx after T-yy (queued)`.
- `obor dispatch <T-xx> --types a,b` — overrides the level (smoke tests, Haiku, which is in no level); no free lane of
  those types is a refusal, not a wait.
- A spec with no level and no `--types`: `FAIL`, naming the fix. The `<LANE> <T-xx>` form is gone.
- A second `obor dispatch` of a waiting task replaces its request (new `--types`, `--after`); its watches stay.
  `obor drop <T-xx> "<why>"` cancels it (`DROPPED T-xx`; a watch on it ends with `dropped: <why>`).
