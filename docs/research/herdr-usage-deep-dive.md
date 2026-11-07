# DD-herdr-usage: does obor use what herdr 0.9.5 gives it?

Deep dive (ob-deep-dive, role worktree at ebef4182), 2026-11-07, for the lead (the owner asked). Baselines:
`docs/research/agent-driver-deep-dive.md` (**DD**, at 9a5346cb, 11-04) and
`docs/research/agent-cli-and-herdr-capabilities.md` (**CAP**, herdr 0.9.3).

**Method.** I read herdr's live contract and changed nothing in it: `herdr api schema --json` (272 KB, protocol 22,
schema_version 1), `herdr --skill`, `herdr agent|pane|worktree|integration` help, `herdr status` (client and server
0.9.5, protocol 22, compatible) and `herdr integration status`. I also read the installed hook scripts
`~/.claude/hooks/herdr-agent-state.sh` (v11) and `~/.codex/herdr-agent-state.sh` (v10). I started no session and
sent no input to any pane. Source code was read only. Every finding has a **Fact** part (file:line, schema path, doc,
git) and a **Judgement** part (my opinion: likelihood, what it would delete, cost).

## 0. Answer (judgement)

**Only partly.** obor has not drifted much *since 9a5346cb*. The herdr-facing files barely grew: `internal/herdr`
+159/−122, `internal/launch` +279/−179 (non-test, `git diff --numstat 9a5346cb..HEAD`). Most of those lines are
re-shapes: one wire struct (T-888, T-890) and the session id obor now chooses (T-813). Two of the four DD exceptions
are fixed (T-798, T-800). The ~4 700 lines added to `internal/app` since then are merge-loop, review, rule and idle
logic, not derivations of herdr state.

What the owner senses is real, but it is mostly **older code that DD flagged and nobody removed**, plus three newer
things:

1. **Code for herdr features obor does not use.** The output-watch half of `Stream` cannot be reached in production
   (H1).
2. **herdr answers obor reads and then ignores.** `notification.show` answers `shown:false` with a reason; obor
   records "shown" anyway, in two durable paths (H2).
3. **herdr fields and waits obor never asks for.** `completion_seq`, `launch_pending`, `interactive_ready`,
   `agent.wait`, `pane.wait_for_output` and `events.wait` are all unused. obor polls or derives the same thing instead
   (H3–H6).
4. **One new reliance on labels as identity.** T-874's worker auto-start fires on "the only pane labelled with the
   agent, at its shell" (H9).

Ranked by obor code deleted per change (§5), the best changes are: delete the dead output watches (H1); give the
needs-you notification one path, a rule's `call` (H2); take Codex's session from herdr's `agent_session` and drop the
lsof search (H7); re-run the H-L1 probe on 0.9.5 and drop the start poll if herdr now waits (H6).

## 1. What obor polls or derives that herdr reports

### H1 The output watches are dead code (fact + judgement)

Fact: `Socket.Subscribe(ctx, watches ...Watch)` (`internal/herdr/socket.go:679`) opens one `pane.output_matched`
subscription per watch (`:695-699`, `Stream.Watch` `:723-731`). The code behind it is the `Watch` and `Match` types
(`:560-570`, `:625-631`), `EventOutput` (`:594`), the `Line/Text/Watch` fields of `Event` (`:616-618`),
`subLine.watch` (`:672`), `open`'s watch parameter (`:768`) and the decode branch (`:892-896`). The only production
caller, `Panes.Subscribe`, passes no watches (`internal/herdr/panes.go:46`) and drops any `EventOutput`
(`panes.go:55`). Its own comment says "output watches are not the core's yet" (`panes.go:37`). The conformance list
still declares the subscription (`contract.go:47`). Only `socket_test.go` exercises it (12 references). `make
deadcode` cannot see this because `Subscribe` is variadic.

Judgement: certain. Deleting it removes ~50 code lines in `socket.go`, the filter in `panes.go`, one contract entry
and the tests that use it. No behaviour changes. Cost: tiny. One caveat: if the advisor's job-in-first-prompt design
(§4) decides to match the wake on screen, keep it until that answer lands.

### H2 Notifications: three paths, and herdr's `shown` is never read (fact + judgement)

Fact: herdr answers `notification.show` with `{type: notification_show, shown: bool, reason}`. The reason is one of
`shown | disabled | rate_limited | no_foreground_client | busy` (schema `success_response.$defs.ResponseResult`,
`NotificationShowReason`; CAP §5.5: best effort, through the foreground client only). `Socket.Notify` decodes no
result (`socket.go:587-589`). So every answer that is not an error counts as success. obor has three notification
paths:

- a needs-you fact: an event-log intent/result `notify.<seq>` through `p.log.Do`, a retry map with 1-2-4…30-minute
  backoff and its own scheduler (`internal/app/notify.go:22-58`, `nextNotify` `:62-82`, `notifyWait`/`retry`
  `app.go:60-74`, `:264-267`, reset `project.go:150`);
- a role agent still stopped after its wake: another intent `alarm.<seq>` (`stopwake.go:265-272`, `alarmKey`
  `:282-285`);
- the idle ping: a rule action's `call` (`idle.go:172-174`). This is the rule tier's idempotent-call form
  (`rules.go:25-30`).

Both durable paths record "shown" when herdr said `shown:false`. The retry machinery only runs on a socket error
while serve is connected (`nextNotify` returns nothing while `!a.connected`, `notify.go:65`), which is rare.

Judgement: high likelihood that the durable machinery buys nothing. herdr's toast is best effort by contract, and
the lead's needs-you line (the `needs` rule, `messages.go:959-1010`) is the channel that must arrive. Proposal: the
needs rule's action carries the notification as a `call` (first line of a key only, so repeats do not re-notify), as
the idle ping already does, and the stopwake alarm does the same. This deletes `notify.go` (~83 code lines), the
retry type, map and backoff, and the alarm intent. Optionally read `shown`: `busy`/`rate_limited` → the rule's call
fails and the next pass tries again; `disabled`/`no_foreground_client` → done. Cost: small to medium. Old
`notify.*`/`alarm.*` records must still replay; they would fold to nothing. About 3 test files change.

### H3 Brief finish is derived from status transitions; herdr has `completion_seq` (fact + judgement)

Fact: a brief finishes when obor *saw* its agent working after the line was delivered, and now sees it ready.
`sawWork` sets `b.worked` from a pane event or snapshot (`messages.go:443-454`). `logWorked` makes that durable as
fact `worked`, with a claim/logging handshake (`:464-483`, fold `:353`). `deliver` then emits "finished"
(`:506`). A turn that starts and ends between two observations is never seen as work. Examples: a short turn while
the stream is down, or `events_lost`, which CAP §5.6 documents. herdr's `AgentInfo` has `completion_seq`, described
as "The current idle transition completed work, independently of who has viewed it" (schema
`success_response.$defs.AgentInfo.properties.completion_seq`). CAP's P-cc probe saw `completion_seq: 6` in an
`agent prompt --wait` answer. obor's `Wire` decodes `state_change_seq` but not `completion_seq`
(`socket.go:1053-1066`).

Judgement: medium likelihood, because the semantics are unverified. Is it a per-agent monotonic counter? Is it set
for screen-detected Claude/Codex? Does it reset on a herdr restart? If it is a counter, "finished" is "completion_seq
grew since the line's delivery, and ready". That deletes `sawWork`, `logWorked`, the claim and the worked fold
(~35 code lines; the fact type stays for replay). It also closes the missed-short-turn gap. Cost: one probe in the
live conformance tier, then a small change. A herdr restart mid-brief needs a rule (the "gone" path,
`messages.go:505`, already covers it).

### H4 A stop polls `agent.get` every second for idle; herdr has `agent.wait` (fact + judgement)

Fact: after Escape, `stop` polls `l.H.Agent` each `Poll` until the agent is no longer busy (`internal/launch/
stop.go:85-88`). herdr's `agent.wait {target, until, timeout_ms}` is server-owned and event-driven. It is pinned to
the pane's occupant and ends `agent_not_running` if the agent leaves (CAP §5.4; schema `AgentWaitParams`). obor
never sends `agent.wait` (`contract.go:7-31`).

Judgement: certain to work as documented; small. One `agent.wait --until idle,done` call with the remaining bound
replaces the loop. An `agent_not_running` answer is what the loop already treats as done (`err != nil`). It deletes
~5 lines and removes a poll. It adds a herdr-only verb to the `PaneControl` port, but that port is launch's, not
`internal/app`'s.

### H5 The env line still polls the screen; herdr has `pane.wait_for_output` (fact + judgement)

Fact: `setEnv` types the env line, then reads the screen every second until one of two marks shows
(`launch.go:667-686`). `pane.wait_for_output {pane_id, source, match{type: regex}, timeout_ms}` checks the current
snapshot first and then waits (CAP §5.4, verified on 0.9.3; schema `PaneWaitForOutputParams`). obor does not send it.
This is DD F3.4 row 1 / P7, still open.

Judgement: certain; small. One call with regex `okmark|badmark` replaces the closure (~12 lines). Cost: one method
in `Socket` and `contract.go`.

### H6 Agent start polls beside `agent.start`; herdr reports `launch_pending` (fact + judgement)

Fact: `started` runs `agent.start` and polls `agent.get` every second until the agent is up (`internal/launch/
start.go:116-163`). `up` and `busyStart` (`:171-195`) decide "up" from kind, status and name. The H-L1 probe on
0.9.3 saw herdr answer at once with `launch_pending` and the agent up 3.7–4.4 s later (DD §7; asserted by T-839,
`tests/conformance/live.go:704-718`). `AgentInfo` has `launch_pending` and `interactive_ready` (schema). obor's
`Wire` decodes neither, and `detected` guesses from kind and status (`start.go:165-168`). herdr 0.9.5's skill still
says "A successful `agent start` returns only after Herdr detects the expected agent … and considers it ready".

Judgement: unknown until H-L1 is re-run on 0.9.5 (`make conformance TIER=live KIND=herdr`; cheap). If 0.9.5 waits
until ready, the ticker, `pre`, `up` and most of `started` go (~40 of its 68 code lines). `busyStart` stays for
T-387, an agent busy on its first prompt that is never "ready". If it still answers at once, decode
`launch_pending`/`interactive_ready` and let them replace `detected`'s guess (no deletion, less guessing).

### H7 Codex's session comes from an lsof search; herdr's Codex hook already reports it (fact + judgement)

Fact: after a fresh Codex start obor pins the process and searches the rollout file it holds open: `lsof`, or
`/proc/<pid>/fd`, every poll for up to 10 s (`internal/launch/session.go:78-276`, ~166 code lines; DD F4.3/P6).
herdr's installed Codex integration (v10) sends `pane.report_agent_session` with `agent_session_id` on SessionStart
(`~/.codex/herdr-agent-state.sh:57-80`). obor already uses herdr's `agent_session` twice. First, as the start row's
session (`Started.Session = seen.Session`, `launch.go:578`). Second, as the strongest identity proof (`registry.go:
365-367`, `sameSession`). `Panes.transcript` globs the rollout from that id (`internal/herdr/panes.go:199-227`).

Judgement: medium. Recording herdr's `agent_session` for a fresh Codex start (one `agent.get`, waited for with a
bound) instead of the search deletes ~166 lines, the `lsof` exec among them. The risk is the dependency: herdr's
integration is user-global, needs `python3` (#5348) and can report to stale panes (#4814/#4649). obor already takes
that risk for its Codex identity proof, so the change adds no new dependency; it removes a second source. Probe
first: is `agent_session` filled for a `codex --no-daemon` pane (T-810), and how soon after start? If it is not
reliable, the other half of P6 is obor's own per-launch Codex SessionStart hook (CX-L5, deleted).

### H8 obor maintains herdr agent names it never reads (fact + judgement)

Fact: "herdr's agent name is never read (herdr drops it at times; nothing renames it back)" (`internal/app/
registry.go:1-4`). obor addresses every wake, line and stop by pane id (`prompt.go:37-39`, `panes.go:303-313`). Yet
launch keeps the name in step: `holds`/`release` clear it after a stop with a ≤10 s poll (`launch.go:826-855`), an
`agent_name_taken` retry (`start.go:70-87`), and `named`/`same` rename an agent herdr lost the name of (#5627,
`start.go:207-258`, ~37 code lines). herdr clears a name by itself "when that agent exits, is released, or is
replaced" (skill). `agent.start` requires a name (schema `AgentStartParams.required`).

Judgement: medium-low value, but it is a mechanism. `named`/`same` exist only for humans and herdr's UI. One caller
needs a name today: `templates/agents/gotchas.md:31` tells agents to `herdr agent prompt` a `/clear`, and the pane
id works there too. Dropping the rename-back deletes ~37 lines. Keep the `agent_name_taken` retry: herdr can lag in
noticing an exit. This is the owner's call, because a human typing `herdr agent prompt ob-l-01` then gets
`agent_not_found` after a long first turn.

### H9 Labels are still identity, and T-874 added a caller (fact + judgement)

Fact: `RenamePane` still says "Labels are obor's agent identity (R-28 §2)" (`socket.go:407`). A label is the third
proof (`srcLabel`, `registry.go:370-371`). It is rank 3 of 4 in `paneIn` (`dispatch.go:985`). The stream merges the
pane's label on every reread because "pane.rename emits no event" (`socket.go:969-983`). `waitPane` patches labels
from a fresh list (`dispatch.go:936-938`). The H-L3 probe asserted "no event on rename" on 0.9.3
(`tests/conformance/live_herdr.go:125-154`). New since 9a5346cb: T-874's `workerStartRule` (`workerstart.go:47`)
auto-launches a worker into `labelledShell`, "the only pane labelled agent, at its shell" (`rolestop.go:159-173`).
The start row already records the pane and terminal of serve's last start of that agent (`registry.go:93-100`), and
`why` already uses it to say "at its shell" (`:172-190`). In the 0.9.5 schema the `pane_updated` event carries a
full `PaneInfo`, with `label`, `cwd` and `foreground_cwd`.

Judgement: the label bug class (DD F3.2) is unchanged. T-874 made a label trigger an automatic start, so a stale or
hand-copied label now starts an agent, where before it only misnamed a pane. Small step: `labelledShell` prefers the
start row's terminal and falls back to the label only for a pane serve never started. The large deletion (label
proof, rank, stream merge, `waitPane` patch, RV-64 workaround) needs a decision that labels are for humans and
restore only, with `obor setup pane` recording a start-like row instead. That is medium-high cost; it is not for
now. Re-run H-L3 on 0.9.5 first: if a rename now emits `pane.updated`, the reread workarounds can go.

### H10 A cwd change "gives no event": unprobed (fact + judgement)

Fact: `rereadPanes` (`internal/app/panes.go:100-112`) and the `waitPane` read (`dispatch.go:904-946`) exist because
"a cd in the pane gives no event (T-745)". obor subscribes to `pane.updated` and rereads the pane on each one
(`socket.go:686`, `settle`). The 0.9.5 `pane_updated` payload has `cwd` and `foreground_cwd` (schema). No
conformance probe checks whether a `cd` emits `pane.updated`.

Judgement: low to medium likelihood. If herdr emits it, the reread and its RV-89 care go (~45 lines, plus
`port.Panes.Snapshot`, if nothing else uses it). Cost: one live probe.

### H11 The watchdog's evidence: herdr has none of it (fact + judgement)

Fact: the stall timer samples three things per ready agent with an open task. It asks for child processes outside
the agent's group (`process_info` + `ps`, `internal/herdr/panes.go:232-262`). It reads the transcript's mtime
(`Activity`, `:117-133`). It reads the last turn (`Turn`, `:140-160`). herdr's `process_info` lists only the
foreground group, and its status says nothing about background children or transcript writes. `Activity` and `Turn`
each call `agent.get` again for a pane the stream already observed (`watchdog.go:72-76`, `:297-302`).

Judgement: keep the mechanism; herdr does not offer it. Small: pass the observed pane, which has
`Session`/`Transcript`, instead of re-asking herdr. That saves two `agent.get` per ready pane per tick. It deletes
~6 lines.

### H12 The herdr-core boundary (fact + judgement)

Fact: `HERDR_PANE_ID` is read in 9 places: `cmd/obor/sleep.go:185,333,416`, `cmd/obor/plugin.go:29` (unset),
`internal/setup/agent.go:476`, `internal/setup/oborinit.go:655`, `internal/launch/self.go:30`,
`internal/guard/guard.go:563`, `internal/guard/session.go:109`, `internal/mcp/mcp.go:114`. It is also in
`agents.EnvVars` (`internal/agents/agents.go:219`). None of these is new since 9a5346cb (the `mcp.go` read moved).
T-888 (11-05) put herdr's `Tab` into the core's `port.Pane` (`internal/app/port/port.go:18`). Only `internal/setup`
reads it.

Judgement: the BACKLOG "Later" rule ("`HERDR_PANE_ID` read in one place", "no new herdr-only shapes in
`internal/app`") was not met before 9a5346cb and is not met now. `Tab` is a small new herdr layout shape in the
core's port. None of my proposals adds one. H4–H6 add herdr verbs only to `internal/herdr` and launch's
`PaneControl`.

### Not proposed (judgement)

- **herdr worktrees** (`worktree.create/open/remove`): each creates a workspace per worktree. obor's model is one
  workspace per project, found by its label (`projectOf`, `internal/app/panes.go:161`). Adopting them would change
  identity, not delete code.
- **The needs-you on `blocked`** after `Idle` (`watchdog.go:200-203`) is not a copy of herdr's own toast on blocked.
  It is delayed, scoped to the project and durable.
- **Pipeline idle** (`idle.go`) is obor's state, not an agent's.

## 2. What changed since 9a5346cb (fact)

| Commit | herdr relevance |
|---|---|
| 7cc34f13 T-798 | protocol `>= 22`, plus a schema check (`contract.go`, H-S1): DD F3.1 fixed |
| c937345c T-800 | the wake after a resume goes through `agent.prompt` (`launch.go:566`): DD F3.3 fixed |
| ec6bb61f T-813 | obor chooses Claude's session id (`--session-id`); herdr's `agent_session` is a cross-check for Claude (P6, Claude half) |
| da914bb5 T-810 | Codex starts `--no-daemon`, so herdr's Codex hook reports to the right pane (H7) |
| 9b7a3104 T-888, 3e92d160 T-890 | one `port.Pane`, one `Wire` for herdr's agent and pane JSON: less code; also puts `Tab` into the port (H12) |
| ab7e8b76 T-839 | H-L1 (start answers at once) and H-L3 (rename emits no event) asserted on 0.9.3 |
| cd2d83bd T-874 | worker auto-start keyed on a labelled shell pane: a new reliance on labels (H9) |
| bbaf2ffa T-870, d2537289 T-853 | role stops and role worktree moves as rules, on herdr's `Ready` status through `whereIs`: uses herdr as intended |
| 108c5fe1 T-879 | pane events only mark the lane table dirty; git scans moved off the stream path |
| T-866 (idle.go) | a third notification path (rule `call`), beside the two durable ones (H2) |

`internal/herdr` and `internal/launch` changed little in size. No new herdr state derivation was added, apart from
T-874's label use.

## 3. The four DD exceptions (fact + judgement)

| DD | Then | Now | Status |
|---|---|---|---|
| F3.1 exact protocol pin | `Protocol = 22`, refuse others | `checkProtocol` accepts `>= 22` (`socket.go:115,165-171`); `contract.go` is checked against the schema (H-S1). herdr 0.9.5 still speaks 22 (`herdr status`) | **fixed** (T-798) |
| F3.3 resume wake via `pane.send_input` | `l.H.Type` | `l.H.Wake` = `agent.prompt`, refused when blocked (`launch.go:566`, `socket.go:369`). `pane.send_input` is left only for the shell's env line and the quit (`launch.go:667`, `stop.go:123`) | **fixed** (T-800) |
| F3.2 labels as identity | label proof, caches | unchanged; H-L3 asserted on 0.9.3; one new caller (T-874) | **still there**, slightly worse (H9) |
| F3.4 two poll loops | env-line screen poll; start poll | both still poll (`launch.go:671-686`; `start.go:116-163`). The start poll is justified by H-L1 on 0.9.3 but not re-checked on 0.9.5. A third poll of the same kind sits in stop (`stop.go:85-88`). The Codex lsof poll (row 3) is still there | **still there** (H4–H7) |

## 4. Could herdr events prove a job's delivery? (facts only)

The advisor answers the design question in `docs/research/job-in-first-prompt-advice.md`. These are the facts it can
use.

- **`agent.prompt`** answers `agent_prompted` with the agent after "both [text and Enter] have been written; that
  alone does not prove the agent started a turn" (skill). It refuses a recognized blocked UI before any byte. #5263:
  it typed into a Claude permission dialog while the status read `working` (CAP §5.8).
- **`agent.prompt` with `wait`**: from a non-working state herdr needs to see `working` or `blocked` within 5 s,
  else `agent_prompt_stalled`. "A timeout or stalled response does not prove the prompt was never delivered" (skill).
  #5386: stalled although delivered. The wait "tracks lifecycle state, not an individual turn" (skill). obor never
  sends `wait` (`socket.go:364-366`).
- **Status source**: Claude and Codex are screen-detected. Their integrations report the session only: both hooks
  send only `pane.report_agent_session` on SessionStart (`~/.claude/hooks/herdr-agent-state.sh:54,82`,
  `~/.codex/herdr-agent-state.sh:52,80`). 0.9.5 moved Codex back to screen detection (CAP §5.9). OpenCode's state is
  reported by its plugin (v13, `herdr integration status`). Detection is "deliberately strict"; unknown prompts read
  `idle` (CAP §5.3).
- **Events**: `pane.agent_status_changed` needs one subscription per `pane_id` (schema `Subscription`). Payload:
  `agent, agent_status, display_agent, state_labels, title` (schema `EventData`). No replay, history is not durable,
  a slow reader gets `events_lost` and is disconnected, and there is no shared sequence with `session.snapshot` (CAP
  §5.6). obor's `Stream` therefore treats every event as a hint and rereads (`socket.go:646-651`).
- **Counters on the agent**: `state_change_seq` grows with every status change, and obor decodes it as `Seq`
  (`socket.go:1065`). `completion_seq` marks an idle transition that "completed work" (schema; semantics unprobed,
  H3). Neither says *which* text the turn answered.
- **Text on screen**: a `pane.output_matched` subscription or `pane.wait_for_output` can match `[obor job <id>]` in
  the pane (schema). That shows the text was drawn: in the input box, in the transcript, or behind a dialog. It does
  not show the text was submitted or read. obor's code for it is dead today (H1). #5750: rows can drop during
  streaming; #2805: `pane.read` revision is always 0 (CAP §5.8).
- **Paths herdr cannot see**: a Claude wake posted to the session inbox involves no typing (`prompt.go:16-21`).
  herdr sees at most the status change that follows. A fresh start's wake is the CLI's first-prompt argument
  (`launch.go:540-545`), passed in `agent.start`'s `args`. herdr types the command line but never delivers the prompt
  as input, so no herdr event marks that delivery. obor's `busyStart` already reads "working on its first prompt" from
  status (`start.go:187-195`).
- **obor's proof today** is the agent's `job_next` call (fact `delivered`, `fetch.go:22-27`), with the delivery
  timers (`jobs.go:14`, `:120-141`). BACKLOG 11-07 "a fetched job is never re-woken" is a failure *after* that proof:
  the agent's later call failed. No herdr signal would have shown it either. Status `idle` after a fetch is the
  closest one.

## 5. Proposals, ranked by obor code deleted per change (judgement)

| # | Change | Deletes (approx., code lines) | Likelihood it works | Cost | Needs first |
|---|---|---|---|---|---|
| 1 | Delete the output watches (H1) | ~50 + tests + contract entry | certain | tiny | the advisor's §4 answer does not want output matching |
| 2 | Needs-you and alarm notifications as rule `call`s; read `shown` (H2) | ~100 (`notify.go`, retry/backoff, alarm intent) | high | small–medium (replay of old intents) | — |
| 3 | Codex session from herdr's `agent_session`, drop the lsof search (H7) | ~166 | medium | medium | probe: `agent_session` filled for `--no-daemon`, and how soon |
| 4 | Drop the start poll if 0.9.5's `agent.start` waits (H6) | ~40 | unknown | small | re-run H-L1 on 0.9.5 |
| 5 | Brief finish by `completion_seq` (H3) | ~35, and a missed-turn gap closed | medium | small | probe `completion_seq` semantics |
| 6 | Stop renaming lost herdr names back (H8) | ~37 | high | small | owner: humans address by pane, not name |
| 7 | cwd from `pane.updated` (H10) | ~45 | low–medium | small | probe: does `cd` emit `pane.updated` |
| 8 | Env line by `pane.wait_for_output` (H5) | ~12 | certain | small | — |
| 9 | Watchdog uses the observed pane (H11) | ~6, and 2 calls per pane per tick | certain | tiny | — |
| 10 | Stop's idle wait by `agent.wait` (H4) | ~5, and one poll | certain | tiny | — |
| 11 | Labels for humans and restore only (H9) | largest (proof, rank, merges, workarounds) | medium | medium–high | owner decision; re-run H-L3 on 0.9.5; small step first: `labelledShell` prefers the start row |

Rows 3–5, 7 and 11 need probes. All of them fit in one live conformance run on a throwaway session
(`make conformance TIER=live KIND=herdr`, plus `KIND=codex` for row 3), which adds H-L3, H-L1 and a cwd case. That
run is the cheapest next step, and it tells which of these proposals are real.

## 6. Design concerns (judgement)

- **Probes settle, then go stale.** H-L1 and H-L3 were asserted on 0.9.3. Since then herdr moved to 0.9.5, and the
  integrations moved from claude v10 and codex v8 to v11 and v10. Nothing re-ran them. An assertion that obor's
  polls rest on should run on each herdr upgrade (DD §6 says "on a version change"). Today that depends on someone
  remembering.
- **Three notification paths for one best-effort toast** is the clearest case of mechanism growing faster than
  value in this area. The rule tier (T-866) already holds the simple form.

## 7. Not verified

Whether 0.9.5's `agent.start` still answers at once (H6). `completion_seq` semantics (H3). Whether `pane.rename` or a
`cd` emits `pane.updated` on 0.9.5 (H9, H10). Whether and when `agent_session` is filled for an obor Codex pane (H7).
Exactly which schema entries are new in 0.9.5: I had no 0.9.3 schema copy to diff, so I describe 0.9.5 as it is, not
what changed. The line counts are approximate (code lines, without comments and blanks).
