# Advice: the monitor, its read model, and a web UI later (Fable, 2026-11-01)

For ob-lead's question (design only, no code). Read: `cmd/obor/monitor.go`, `cmd/obor/status.go`,
`internal/app/status.go`, `sessions.go`, `fetch.go`, `prompt.go`, `jobs.go`, `jobstart.go`, `log.go`, `messages.go`,
`docs/design.md` (herdr integration), `docs/state-model.md` (rows 47, 51; "Not recorded"), `docs/module-map.md`,
`docs/decisions.md` (UI deferrals), `go.mod`, `AGENTS.md`, `herdr-plugin.toml`, and herdr's socket API docs
(herdr 0.9.5: `pane.report_metadata`, `agent.view.set`, `plugin.pane.open`, `notification.show`; plugins are terminal
only: "native non-terminal plugin UI are not part of plugin v1").

## Recommendation

1. **One read model, in one new package `internal/view`, built by one function, consumed by three renderers.** Today
   the model already exists in the wrong place: `statusModel` and `buildStatus` join serve's `ProjectStatus` with git's
   lane table in `cmd/obor/status.go:22-40, 186-248`, and the monitor adds the event list on its own from the store
   (`monitor.go:148-171, 300-315`). Move that join, with the event list and a per-task detail, into `internal/view`;
   `obor status --json` prints `view.Project` as is, the status text and the monitor render it, the web UI later serves
   the same JSON over HTTP. Nothing new in serve's socket: `status` stays the one method (`internal/app/status.go:270-281,
   321-323`); the view's extra inputs are the store (read-only) and git, which the CLI has already.
2. **Record what obor sends an agent, now.** A job's text is built at `job_next` and kept in memory only
   (`internal/app/fetch.go:92-112`; `docs/state-model.md:361`); a `--prompt` brief is recorded "without a text" (row 47,
   `docs/state-model.md:289`). "Inspect their prompts" is impossible for anything sent before the day it is recorded, so
   a `prompts` table in the store (serve the one writer, as `specs`) comes first, whatever UI reads it.
3. **Plain ANSI, no Bubble Tea, no Cobra.** Keep the monitor a frame renderer (`monitor.go:3-7`) and make it nicer with
   layout, emphasis and a flicker-free redraw. Drill-down is a command, `obor show <T-xx|LANE|agent|job id|RV-xx>`,
   printing the same view's detail as text or `--json`, not a keyboard mode inside the monitor.
4. **Lean on herdr for the "agents" part**: `pane.report_metadata` tokens (`$task`, `$state`, `$since`) on every obor
   pane so herdr's own sidebar shows them, and the monitor as a `[[panes]]` entry of the kit's plugin
   (`herdr-plugin.toml:14-15` has only `[[startup]]` today) so it opens as a popup or split from inside herdr. That is
   the owner's "UI inside herdr first" (`docs/decisions.md:46-49, 105`) at near zero cost.
5. **Build `obor web` in the same wave, right after the view and the prompts table** (the owner lifted the
   "until stable" deferral, addendum below): a separate stdlib process (`net/http` + `embed`) over the same `view`
   package and the store, read-only, localhost, one landing page for every project on the machine; never inside serve.

## The read model

**Where.** `internal/view`, a new layer row in `layerOf` (`docs/module-map.md:10-16`): it may import `app` (the
status types), `checks` (`LaneTable`), `store`, `config`; only `cmd/obor` (and later `obor web`'s package) imports it;
`app` never imports it (no cycle). It replaces `statusModel`, `buildStatus`, `laneState`, `laneSince`, `eventsOf`,
`eventLine` in `cmd/obor` (deleted there; `docs/module-map.md:99` then says "rendering only").

**Why not serve's socket as the one source.** Serve could answer the whole view, but then `obor status` with serve
down would need a second builder from the store and git (today's degraded path, `status.go:186-192`). One function
`view.Build(ps *app.ProjectStatus, lanes *checks.LaneTable, db)` with `ps == nil` for "serve down" keeps one builder.
Serve answers facts; the view joins them. A web server asks serve for `status` and the store for the rest exactly as
the CLI does.

**What `view.Project` holds** (the `--json` shape; every field has a reason to be read by a person or a UI):
- `serve`: as `serveJSON` today (`status.go:52-65`) plus `herdr` connected and `log_error`.
- `running`: steps without an agent, elapsed (`status.go:44-49`).
- `needs_you`: task, lane, reason, the answer commands, since (`status.go:91-97`), sorted oldest first.
- `lanes`: as `laneJSON` (`status.go:69-89`), plus `spec_rev` (the store's current rev), `job` (the open job id and
  whether its `delivered` fact exists: a wake out but not fetched is a visible state), `session`, `transcript`,
  `last_write` (the stall sampler's `seenWrite`, which exists only in memory today: serve exposes it on `AgentStatus`).
- `roles`: one row per configured role agent not on a lane (integrator, reviewer, deep dive, advisor): status, age, open
  brief and since, its session. Today the monitor shows none of them (`monitor.go:238-257` draws lanes only); the owner
  asked to "see the agents", and these are the ones the lanes table misses.
- `tasks`: serve's open tasks with no lane (waiting, `--after`), as today.
- `attention`: review (open or due), watches with `by`, queued dispatches, pause (`monitor.go:259-283`).
- `messages`: lines not delivered.
- `full_check`.
- `events`: the newest N records worded as the monitor words them (`monitor.go:331-385`), each with `seq`, `ts`, `task`,
  `line`. The seq is the UI's cursor: "changed since seq X" is one query (`log.go:176-190`).

**`view.Task(id)`**, the drill-down, from the store alone (serve adds the live bits when up): state, attempt, lane,
agent; spec title, current rev, gated rev (`jobstart.go:188-198` already distinguishes them); dispatched at; the open
job id, delivered at, and its prompt text (the `prompts` table); the report (fact `submitted`); candidate sha, target,
patch hash, gate line and the gate log path (`.obor/state/logs/<task>-<name>.log`, `docs/design.md:219-220`); the
needs-you and its answer commands; the task's records (seq, ts, step, kind) as a timeline; session id, transcript path,
last write age. `view.Agent(name)`: its sessions (the facts of row 51, "the UI's list", `sessions.go:11-13`), briefs
sent (`messages.go:871-874` keeps the brief line), lines sent, jobs fetched. `view.Review(RV-xx)`: the request fact,
its start, the fix tasks (once bound, see the planner/coordinator advice), the close commit.

**What stays out of the view:** transcript contents (a path and a last-write age only; see risks), git history beyond
the candidate, the full event log (the web UI pages it by seq), config.

**The `prompts` table.** Columns: `seq` (the fact it belongs to: `delivered` for a job, `briefed` for a brief), `agent`,
`kind` (`job | brief | line`), `job` (id, for jobs), `ts`, `text`. Written by serve inside the same step that records
the fact: `jobNext` after `fetchText` (`fetch.go:92-104`), `recordBrief` and the `--prompt` start (row 47). Lines
(`message` facts) already carry their text, so they need no row; the table joins them for the reader. Retention: prune
with the daily backup, keep 14 days (a review job is up to 15 KB, `jobstart.go:224-226`; 30 jobs a day is well under
1 MB a day). Not in `events`: the log is the state machine's record; prompts are payloads, and replay must not slow
down for them.

## The monitor

**Layout** (fixed order, top to bottom; the needs-you block never scrolls away; events fill the rest):
```
obor-kit · serve launchd 4821 since 09:12 (6h) · merging · herdr ok    running: gate T-660 (make -s check, 1m12s)
NEEDS YOU
  T-640 (O-02) 48m  held: open questions: … → obor resume T-640 "<decision>" | obor drop O-02 "<why>"
LANES    state      task    since   agent               spec  job
  O-01   working    T-659   12m     ob-o-01 working     r2    work (fetched)
  O-02   held       T-640   48m     ob-o-02 idle        r1    —
  S-01   free
ROLES    ob-int idle 2h · ob-terra working, briefed RV-74 12m ago · ob-fable idle
QUEUE    merge loop   T-660 gating (next) · T-661 submitted, waits for the scratch · T-659 awaiting-accept (job out, fetched 2m)
         dispatch     T-664 after T-662 · T-665 waits for a medium lane (opus, sonnet) · T-666 spec r1 stored, not dispatched
         review       RV-75 requested, waits for T-664 to merge · RV-74 open (verify pending)
         lines        1 to ob-terra (brief, 3m, pane working) · watch T-662,T-663 (ob-lead): "restart serve after"
         paused: no · nightly at 03:00 (in 9h)
EVENTS   14:02:22  gate T-660 pass (ok 212 tests, 71s)
         14:01:50  submitted T-660
         …
```
Emphasis: the NEEDS YOU block bold or inverse when non-empty (and absent, not an empty heading, when empty); a `held`
state and a red gate in red; ages in dim; the header one line. Honour `NO_COLOR` and a non-tty (`obor monitor | cat`
prints plain frames; a test compares plain text). Width: clip, never wrap (T-663). Height: the section order of
`fitSections` (`monitor.go:419-439`) stays; events take what is left, minimum three rows.

**Redraw without flicker.** `drawFrame` clears the whole screen each frame (`monitor.go:200-203`), which flashes at
every 2 s tick on most terminals. Write cursor-home, each line followed by clear-to-end-of-line (`\033[K`), then
clear-to-end-of-screen (`\033[J`) once; redraw only when the frame text changed (the seq test is already there,
`monitor.go:102-110`; add a text compare so the elapsed-time tick redraws only lines that changed, or accept a full
redraw with the per-line clear, which does not flash).

**Interactions: one key.** `q` or Ctrl-C ends it. Everything else is a command in another pane, where the lead and
the owner already type: `obor show T-660` (the task detail above), `obor show O-02` (its lane and task), `obor show
ob-terra` (sessions, briefs, lines), `obor show job T-660/1/review` (the prompt text), `obor show RV-74`. Reasons: (a)
the monitor pane in herdr's `system` tab is looked at, not typed into (the wrong-tab guard exists because typing into
the wrong pane is the common accident, `docs/design.md:238-250`); (b) a keyboard mode needs raw terminal input, which in
the standard library is a platform-specific `ioctl` under build tags, or a dependency; (c) the lead agent reads text,
not screens, so the drill-down must be a command anyway; (d) the web UI's detail page is the same `view.Task` as JSON.
If a selection key is wanted later, it is one `j/k/Enter` loop over the lanes list in raw mode; nothing in the model
changes.

## Bubble Tea, lipgloss, cobra, plain ANSI

Plain ANSI. The dependency rule allows a module only for a published spec or a database engine (`AGENTS.md:19-22`);
neither applies. `bubbletea` + `lipgloss` pull in a dozen modules (termenv, reflow, go-runewidth, go-colorful,
x/term, x/sys, …), tens of thousands of lines, for one screen that redraws every two seconds. Their value is the
update/view loop for multi-screen interactive apps with keyboard focus; the monitor has one screen and one key. What
the owner called "a wall of text" is the lack of sections, alignment and emphasis, which an 80-line ANSI helper in
`internal/view/text` (bold, dim, red, inverse, home, clear-line, clear-below; all off when not a tty) fixes. Cobra is
argument parsing: the hand-written `switch` over `bin/go-commands` is the smaller thing and already there. If the kit
ever needs real terminal control (raw mode for selection), `golang.org/x/term` is one small module from the Go team,
but it still needs the owner's exception to the rule; ask then, not now.

## herdr's hooks vs a web app

Use herdr for the agents view; build nothing that herdr already draws:
- **`pane.report_metadata`** on each obor pane (lanes and roles) with tokens `task`, `state` (obor's state column:
  working, gating, reviewing, held, free), `since`, and `display_agent`; serve already identifies every pane (row 48)
  and already renames panes (`agent.rename`, `pane.rename` are in the adapter). herdr's sidebar then shows `$task
  $state` per agent across workspaces, and `agent.view.set` can sort by attention. Tokens are display-only, TTL-bound
  and not restored after a herdr restart, so serve re-reports them on every snapshot (it reconciles on snapshot already).
  All calls stay in `internal/herdr` (`docs/module-map.md:73`; "obor should be as simple and as integrated as possible:
  herdr is the source of agent state", `docs/design.md:407-410`).
- **The monitor as a plugin pane**: a `[[panes]]` entry in `herdr-plugin.toml` with `command = ["/bin/bash",
  "../bin/obor", "monitor"]`, placement `popup` or `split`; `plugin.pane.open` from a key binding. herdr runs it as a
  terminal pane, so this is the same binary, no second implementation.
- **`notification.show`** is already used (the adapter lists it); a needs-you is a toast with `sound: request`.
- **What herdr cannot do**: draw obor content in its sidebar (plugins are terminal only), show a prompt text, or
  serve anything to a browser (no web API found). So the "inspect prompts" wish is either `obor show job …` in a pane,
  or the web UI.

## Web UI: now, in this shape

The deferral (`docs/decisions.md:46-49, 105`) was a premise the owner lifted on 2026-11-01 ("the kit is in great part
feature complete, but I need monitoring: not only what is going on but also what is queued"). Weighed on value: the
monitor pane gives a glance; a browser page gives room (queue and detail side by side), reading long texts (prompts,
reports, gate logs) and, the one thing the TUI cannot do well, **every project on the machine on one page** (three serves
run here; `project.Projects`, T-646, already lists them). Cost: a few hundred lines of Go and one HTML file, no
dependency, once `view` exists. So: yes, now, after the `view` package (its JSON is the API) and the `prompts` table
(its content), and before any monitor work beyond the queue section. Shape:
- `obor web [--listen 127.0.0.1:PORT]`, its own process, standard library only: `net/http`, `embed` for one static
  HTML page with vanilla JS, no framework, no build step. Routes: `/api/view` (= `obor status --json`), `/api/task/T-xx`,
  `/api/agent/<name>`, `/api/job/<id>` (the prompt text), `/api/events?since=<seq>` (long-poll on the store's seq:
  the store is the stream; no WebSocket protocol of obor's own), `/api/transcript/<agent>?tail=N` (see risks).
- Read-only. No verb goes through the browser in the first version: the owner's verbs are the CLI and the chat with
  the lead; a button that drops a lane is an accident surface. Localhost only; remote access is an SSH tunnel, as
  herdr's own remote story is SSH.
- Never inside serve: serve is the one writer with a replayed state machine; a web handler in its process shares its
  lifetime, its restarts after a merge (T-652) and its failure modes.

## Now vs later

Now (one wave, five tasks; the first two unblock everything else):
1. **`internal/view`** (medium): move the join out of `cmd/obor` (delete it there), add `roles`, `spec_rev`, `job`,
   `events`; `obor status --json` prints it; text status and monitor render it; boundary table row; tests: today's
   `status`/`monitor` tests move with the code and compare JSON and plain frames.
2. **`prompts` table** (medium, store + app): written at `jobNext`, `recordBrief`, the `--prompt` start; pruned with the
   backup; `view.Task` and `view.Agent` read it.
3. **`obor show`** (simple once 1 and 2 exist): the detail renderers, text and `--json`.
4. **Monitor polish** (simple): sections, emphasis, `NO_COLOR`/non-tty, home + clear-line redraw, `q`.
5. **herdr metadata + plugin pane** (medium, adapter): tokens per pane on identify and on snapshot; the `[[panes]]`
   entry.
6. **`obor web`** (medium): the landing page (projects, each with its needs-you count and queue length), the project
   page (the layout above in two columns: lanes and queue left, needs-you and events right), the detail pages (task,
   agent, job text, review), all from `view` and the `prompts` table; `/api/events?since=` long-polls the store seq.
Later: transcript tail through the bridge; selection keys in the monitor only if `obor show` and the web page turn out
not to be enough.

## Riskiest parts

- **Two builders creeping back.** The web server will be tempted to ask serve directly and shape its own JSON. Rule in
  `docs/module-map.md`: every reader of project state goes through `view.Build`; `--json`, the monitor and the web
  serve the same struct; a field added for one appears in all three.
- **Transcripts.** Claude's and Codex's transcript files are their private formats; the kit already reads them for
  session ids and write times (`internal/agents/claude/claude.go:197`, the stall sampler). A "tail" that parses turns
  is a third parser and breaks on every CLI update. If a tail is shown at all, it is the last N raw lines through one
  `agents.Kind` method in the bridge, labelled as raw, or only the path and the last write age.
- **Store growth and readers.** The `prompts` table makes the store the monitor's hot read path bigger; readers are
  `OpenRO` with a short `busy_timeout` (`docs/module-map.md`, `store`); a reader that polls four times a second must
  not hold a transaction. Retention from day one.
- **"Serve down" honesty.** The degraded view must say what it does not know (no agents, no running line) instead of
  showing stale zeros; `serve.error` is the field, and every renderer shows it first.
- **herdr churn.** 0.9.x; metadata tokens are display-only, so a breaking change degrades the sidebar, not the swarm.
  Keep the token names in one place and treat a refused `pane.report_metadata` as a logged warning, never a fact.
- **Boundary and deadcode.** `view` must land with its three consumers in one task or with `deadcode` exclusions marked
  "until T-xx wires it" (`docs/agents/lead.md`, the Makefile rule), or the gate goes RED.

## What I would not build

- Bubble Tea, lipgloss, cobra, or any TUI framework; a multi-screen interactive monitor with modes and focus.
- A web server inside serve; a WebSocket or event protocol of obor's own (the store's seq is the cursor).
- A second status struct for the web, or a `--json` that differs from the view.
- Transcript parsing beyond a raw tail; a chat-like rendering of an agent's turns.
- Verbs in the web UI (drop, resume, answer) in the first version.
- History views, charts, token accounting, or metrics pages: they are a different product and the owner has not asked.
- Authentication: localhost and an SSH tunnel until there is a remote story, which is herdr's to have first.

## Addendum (owner, 2026-11-01): "not only what is going on but also what is queued"

**What the store knows, what status shows, what the monitor shows.** The view's `queue` section is a join the store can
already answer for every item but one:

| queued item | the store knows it | `obor status` shows it | `obor monitor` shows it | gap |
|---|---|---|---|---|
| dispatch waiting on `--after` | yes: the loop task `waiting` with `After` (`internal/app/status.go:76-86`, row 2a) | yes (`status.go:304-311`, `waitingOn` 651-659) | yes (`monitor.go:274-278`) | none |
| dispatch waiting for a free lane of its level | yes: `waiting` with `Level`, `Types`, `Why` (the last refusal, in memory only, `docs/state-model.md:362`) | yes ("waiting for a medium lane (opus, sonnet)") | **no**: `attentionSection` keeps only `len(After) > 0` | show it; `Why` is lost on a restart, say "waiting for a lane" then |
| spec stored, never dispatched | yes: a `specs` row with no loop record (`store.ReadSpecsAt`, `internal/store/specs.go:104`; `obor spec list`, `cmd/obor/spec.go:76`) | **no** (only `obor spec list`) | no | one join in `view.Build`: specs minus loop tasks, with rev, age and title |
| review request waiting on `--after` | yes: the `review` fact with `After` (`internal/app/review.go:64`; row 52) | yes, as one string (`reviewStatus`: "review request …, waiting T-b") | yes (the same string) | make it structured (`after`, `note`, `since`) so the UI can link the tasks; the text stays for the lead |
| review open, verify pending | yes (row 52: fired, not closed) | yes | yes | none |
| lines not delivered | yes (`MessageStatus`, `messages.go:933-940`) | yes ("undelivered to …") | **no** | add to the queue block |
| briefs delivered, agent still working on them | yes (`outbox.briefs`, `messages.go:159-171`) but **not exported** by `Status()` | no | no | export `Briefs []BriefStatus{to, from, since, delivered, pane status}`: the owner's "what are the role agents doing" |
| the merge loop's order (which task is gated next) | yes, derivable: `advance` walks tasks sorted by id (`loop.go:296-330`) and `pick` takes the scratch for the first `submitted` task when no gate or publish holds it (`loop.go:646`); then green → review job (oldest intent first, `fetch.go:128-143`) → accept → publish | partly: each task's state and `running` step, no order | partly: the lane state column | compute `next` in the view: the lowest-id submitted task when the scratch is free, the one gating or publishing when not; show each task's stage as a short pipeline (`submitted → gating → review → publish`) with the stage reached |
| the integrator's queue | yes: open integrator jobs, delivered or not (`folded.delivered`, the job intents) | partly (`awaiting-accept`, `resolving`) | partly (`reviewing`) | show "job out, fetched 2m ago" vs "job out, not fetched (wake retried once)" |
| pause, nightly due | yes (`Paused`; `merge.nightly_at` in config, the day's `nightly.<date>` key in the log) | pause yes, nightly only as the last run | pause yes | add "nightly at HH:MM (in Nh)" when configured |
| the lead's "when Y, do X" | **no**: a line in `BACKLOG.md` (`SKILL.md:38`: an `--after` dispatch, else BACKLOG) | no | no | see below |

Note on order: the scratch goes to the lowest task id among submitted tasks, not to the one submitted first. That is
fine for a view (deterministic, explainable) but worth one line in the queue block ("next: T-660, by id"), or a one-line
change to pick by submission seq if the owner expects first-in-first-out.

**"When Y, do X" belongs in the store, as a watch with a note.** A watch is already "when these merge, tell me" (row 49,
`docs/state-model.md:290`, with `by`); add an optional `note` to it: `obor watch T-662,T-663 --note "restart serve, then
release v0.17.4"`. The queue shows `watch T-662,T-663 (ob-lead): restart serve, then release v0.17.4`, and the line
delivered when it ends carries the note (`[obor] T-662,T-663 merged (sha) — restart serve, then release v0.17.4`), so the
lead is told what it meant to do, not only that something merged. This is one field on an existing fact and one word in
the lead skill ("When Y, do X" is a watch with a note, else a BACKLOG line only when Y is not a merge). What stays in
`BACKLOG.md`: items with no task trigger (owner decisions pending, concerns, later candidates); the view does not parse
BACKLOG text, it links to the file.

**Queue view in the layout** (the block in "The monitor" above, after LANES and ROLES, before EVENTS; the web page's
project view puts it beside the lanes): four labelled rows, each clipped to the width, in this order because it is the
order of urgency for the owner: the merge loop (what lands next and what is stuck), dispatches waiting (why: after, a
lane, never dispatched), the review cycle, then lines, briefs and watches, then pause and nightly. Each entry names its
cause in words a person can act on ("waits for a medium lane (opus, sonnet)", "after T-662", "waits for the scratch",
"job out, not fetched"). The JSON (`view.Project.queue`) has the same four arrays with typed fields (`task`, `stage`,
`waits_on: {after|lane|scratch|integrator|close}`, `since`, `why`, `note`); `--json` and the web read those, the text
renderer words them.

**Tasks this adds to the wave** (in "Now vs later"): the queue section is part of task 1 (`internal/view`): the specs
join, the structured review request, the exported briefs, the pipeline stage and `next`, the lane-wait rows; the watch
note is one small app task (fact field, CLI flag, the delivered line, lead skill wording).

Written only this file: `docs/research/monitor-ui-advice-fable.md`.
