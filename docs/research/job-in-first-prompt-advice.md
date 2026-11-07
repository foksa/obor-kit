# Job in the first prompt: advice

Advisor answer, 2026-11-07, to the lead's question (BACKLOG.md:32): can serve put the job itself into a fresh
session's first prompt, for every agent kind, and still have what the fetch gives today. Read only; cites
`file:line` at commit ebef4182, herdr 0.9.5 on this machine, the event log `.obor/state/obor.db` (read-only).

## Recommendation

**No. Keep jobs by fetch; do not put the job into the first prompt.** The first prompt is a command-line argument
that herdr types at the pane's shell, so it is size-capped and unretriable; no agent kind gives a delivery proof as
strong as the agent's own `job_next` call; and the change deletes nothing (the fetch stays for resumed sessions,
running agents and retries) while adding a second transport per kind. It also does not touch the stuck-fetched-job
gap, which sits after delivery.

**The census cost has a one-line fix that is not a global switch.** Claude Code defers MCP tools per server:
`"alwaysLoad": true` in a server's entry in `--mcp-config` or `.mcp.json` loads that server's tools upfront and
leaves every other server deferred (Claude Code docs `mcp.md`, "Configure tool search", v2.1.287+; verified by the
Claude Code guide today, section 9). obor already writes its own server entry for every lane worker
(`internal/agents/claude/claude.go:30-37`, `MCPConfig`, spliced in at `:62-66`) and for the integrator and role
agents in the plugin manifest (`.claude-plugin/plugin.json:8-18`). Add the field to both. The wake, `job_next`,
`delivered`, the timers and the state model do not change; the model's first call becomes `job_next` itself. That is
the whole ToolSearch saving (543 calls a week, `docs/research/chatter-census.md:17`) for the price of one JSON field,
and it loads two tools in a worker and six in the integrator, nothing of the user's. The owner's objection to a
global tool-search switch (it loads every MCP tool) does not apply to a per-server field. Whether a plugin
manifest's `mcpServers` entry honours it is not documented, so the integrator path needs the live probe; if it does
not, splice obor's `--mcp-config` into the integrator's start too (`needsObor`, `claude.go:172-183`, is strict-only
today).

Do, in this order:

1. **`alwaysLoad: true` on obor's MCP server entry** (`claude.go:30-37`, `plugin.json:8-18`), probed with
   `make conformance TIER=live TURN=1 KIND=claude` reading the first tool call of a worker and of an integrator
   session. Difficulty simple.
2. **Close the stuck-fetched gap on its own** (two small changes, no new mechanism): anchor the integrator's stall
   nudge to the recorded `delivered` fact's time instead of an in-memory timer that every serve restart and agent
   restart resets (`internal/app/jobs.go:10-24`, `:160-186`), and let `obor restart <integrator>` carry its open
   review or resolve job's wake the way it carries a lane worker's work or fix job (`cmd/obor/restart.go:116-134`,
   `internal/launch/launch.go:515-523`). Difficulty simple each.
3. Not this: herdr's events as the proof. Nothing in herdr's protocol says "a prompt was submitted"; its agent
   status for Claude and Codex is read off the screen and documented as best effort.
4. Not this either: the job through a hook's `additionalContext` (section 7). It looked like the one shape that keeps
   the fetch's semantics, but Claude caps hook context at 10,000 characters and spills the rest to a file with a
   2,000-character preview (`hooks.md`, "Output limits"); a work job with its role texts and a review job with its
   diff are larger.

## 1. What the fetch gives today (the things not to lose)

- The wake is one constant line, `[obor job <id>] call the obor job_next tool` (`internal/app/wire/call.go:212-220`;
  `internal/app/fetch.go:22-34`). A fresh start carries it as the first prompt (`internal/app/jobstart.go:139-176`,
  `startJob`: result `sent (start prompt)`); a running agent, a resumed session or a retry gets it typed or posted
  (`internal/app/jobstart.go:81-87`, `internal/app/prompt.go:42-72`, `internal/launch/launch.go:540-573`).
- The agent's first `job_next` is the only delivery record: fact `delivered{job, via: mcp, call, rev}`
  (`internal/app/fetch.go:104-117`). It folds a worker's task from dispatched to working and marks an integrator job
  delivered (`internal/app/mtask.go:99-108`, `internal/app/verdict.go:84-85`); `obor status` shows it as `fetched`
  (`cmd/obor/status.go:331`, `internal/app/status.go:88-91`).
- The delivered mark switches the watchdogs: undelivered, the wake is sent again after 5 minutes and the owner told
  after 10 (`internal/app/jobs.go:13-14`, `:116-141`; `docs/state-model.md:291-292` rows 7-8); delivered, the
  integrator's quiet time counts toward one nudge and then needs-you (`internal/app/jobs.go:160-186`); the worker's
  delivered work job waits for `task_done` (`internal/app/dispatch.go:1223-1231`).
- A repeated wake returns the same job and records nothing; a refetch whose text changed (a new spec rev) keeps its
  text in the prompts table (`internal/app/fetch.go:57-61`, `:104-117`; `docs/state-model.md:189-199`).
- The MCP server learns the integrator's task and job id only from the `job_next` reply; `accept`, `sendback`,
  `reject` and `resolved` key on it (`internal/mcp/mcp.go:59-66`, `internal/mcp/tools.go:40-66`).
- The contract itself: "Every agent fetches its jobs (T-437): there is no typed job text and no status rule of
  delivery" (`docs/state-model.md:187`; row 5 at `:289`: "no screen read decides any of it"). T-437 replaced the
  earlier state where a start carried content and a typed wake was a second path (T-387, T-393, T-396 in row 5).

## 2. The first prompt is a typed command-line argument

How a fresh start reaches the agent: `internal/launch/start.go:63-67` builds the argv and `internal/herdr/socket.go:425-434`
sends `agent.start`, which "types its command line (kind and args) at the pane's shell". The prompt is positional:
Claude after `--` (`internal/agents/claude/claude.go:39-75`), Codex positional (`internal/agents/codex/codex.go:56-57`),
OpenCode `--prompt` (`internal/agents/opencode/opencode.go:31-32`).

Two consequences:

- **Size.** herdr 0.9.4 "refuses a command line longer than the terminal's canonical input limit" on macOS instead of
  cutting it (`docs/research/agent-cli-and-herdr-capabilities.md` §5.2, release notes #4950/#4988; docs only, not
  probed here). The typed line today already carries the settings JSON, the MCP config JSON and a session id. A
  work job is the worker prompt plus the spec plus two role texts inlined up to 12 KiB each
  (`internal/app/dispatch.go:1136-1166`, `roleTextMax`); a review job carries the diff up to 15 KiB
  (`internal/app/port/effects.go:230`, `internal/app/jobstart.go:221-230`) and the worker's report. Those cannot be
  typed as an argument. A shell indirection (`"$(cat file)"`) depends on herdr not quoting args, which is not
  documented either way.
- **No retry without a restart.** A lost first prompt (herdr #4537: a prompt never reached a pristine idle
  `--no-focus` Claude pane; #4529 on Windows; a Claude session in `bypassPermissions` holds a posted message for a
  5-minute dialog, `docs/design.md:146-149`) is today caught by the delivery timers typing the constant wake again
  (`internal/app/jobs.go:116-141`). A job in the prompt has no second delivery except another start, and
  `startJob`'s own rule is that a start that ran is the delivery and anything else types the wake
  (`internal/app/jobstart.go:139-146`). So the fetch path stays for the retry, and a job must then exist in two
  forms.

So "job in the first prompt" is not one change but one per kind: Claude could take it only through a hook (section 3),
Codex only through argv (no hooks in obor's Codex setup: `internal/agents/codex/codex.go:318`, `HooksNone`), OpenCode
through the plugin's system-prompt context (`opencode/obor.js:1-8`) or `--prompt`.

## 3. What could prove delivery

| Candidate | Kinds | What it proves | What it does not | Source |
|---|---|---|---|---|
| herdr `pane.agent_status_changed` → `working` after the start | all | a turn began in that pane | that the turn is the job: a prompt typed into a permission dialog still shows `working` (#5263); Claude and Codex status is screen-read, "deliberately strict", unknown prompts read `idle` (#3467, #5741, #5358); events have no replay and a slow reader gets `events_lost` | capabilities §5.3, §5.6, §5.8; `herdr api schema --json` 0.9.5: `pane_agent_status_changed{agent, agent_status, title, state_labels}` |
| herdr `pane.agent_detected` / `pane.updated` (`agent_session`) | all | the agent process is up and, for Claude, which session id it reports | nothing about the prompt; the name is lost when the first turn outlasts the start timeout (#5627) | schema: `pane_agent_detected{agent, final_status, released}`; serve already subscribes to both (`internal/herdr/contract.go:38-49`) |
| herdr `pane.output_changed{revision}` (new in the 0.9.5 schema) | all | the screen changed | which text; a screen read, which row 5 rules out | schema |
| obor's `UserPromptSubmit` hook on the wake | Claude native; OpenCode via plugin (`opencode/obor.js:8`, `:214`); Codex none | the CLI accepted the wake as a user turn, with its text (`[obor job <id>]`) | that the model acted; hook output capped at 10 s (`hooks/hooks.json`), 30 s by Claude | `internal/guard/prompt.go:24-35`; capabilities §2.3 |
| the agent's `job_next` (today) | all | the model read the wake and asked for the job over the socket; the reply is the job, the call id dedupes | nothing: it is the strongest available | `internal/app/fetch.go:62-125` |
| `task_done` / a verdict carrying the job id | all | the job was done | comes after the work: the stall and undelivered watchdogs need a mark before it, else every job longer than 10 minutes gets a re-wake and a needs-you (`internal/app/jobs.go:116-141`) | `internal/app/fetch.go:285-408` |

Conclusion: herdr has no prompt-level event, and its status is a screen read that obor's state model excludes on
purpose. The only kind-neutral proof is the agent calling serve, which is what `job_next` is. The hook-based proof is
real for Claude (and plausibly OpenCode), but the job cannot travel with it: hook context is capped at 10,000
characters (section 7).

Resumed sessions and running agents under a job-in-prompt design: a resume carries no first prompt by design
(`internal/launch/launch.go:540-547`, "the wake is typed once it is up"); a running integrator gets the wake typed
(`internal/app/jobstart.go:24-26`, `:81-87`); a worker started again by the T-874 rule gets the wake as its prompt or
typed (`internal/app/workerstart.go:23-32`, `:54`). All of them keep `job_next`. A repeated wake would either re-send
the whole job text (size again) or fall back to the fetch. Both halves of the system stay.

## 4. Deletes versus adds, if the job went into the first prompt

| | Deleted | Added |
|---|---|---|
| `job_next` | nothing: resumes, running agents, retries, Codex | a second way to get a job; the verdict tools' keying (`internal/mcp/tools.go:58-66`) needs the job id from somewhere else |
| `delivered` fact | nothing it drives can go: the watchdog split, `fetched` in status, dispatched → working | a `via` per kind with weaker proofs, or `delivered` only for the fallback path |
| re-wake logic | nothing: a lost first prompt still needs the typed wake | a rule telling "started with the job" from "started, job lost" without the fetch |
| the wake line | the constant line for fresh starts | a job text per kind in argv, a hook, a plugin |
| state model | row 5's "nothing typed" simplification | rows 5-6 split by kind |

Net: more mechanisms, not fewer. That is the owner's own test (`BACKLOG.md:32`).

## 5. The stuck-fetched-job gap is after delivery, so no transport fixes it

The 11-07 case in the event log (UTC; `sqlite3 -readonly .obor/state/obor.db`):

| seq | time | record |
|---|---|---|
| 25364 | 08:53:09 | `T-938/4/review` result `sent (start prompt)` |
| 25369 | 08:53:11 | `delivered{job: T-938/4/review, via: mcp}` |
| (none) | 08:53-09:06 | serve down 08:53-08:57 (BACKLOG.md:220); no retry, nudge, lost or needs-you for T-938 |
| 25404 | 09:06:55 | session fact for ob-int, source launch: the lead's `obor restart ob-int` |
| 25406 | 09:08:48 | `accept` for `T-938/4/review` after the lead typed the wake |

Fifteen minutes, three of them serve's outage. Why serve woke nobody:

- The only re-wake of a delivered integrator job is the stall nudge: quiet for `Idle` (10 minutes,
  `internal/app/app.go:33`) with the pane ready, then one reprompt, then needs-you (`internal/app/jobs.go:160-186`).
  Its timer is not recorded, "a replay starts them afresh" (`internal/app/jobs.go:10-24`), so serve's restart at
  08:57 started the ten minutes over, and the pane's agent change at the lead's restart reset it again
  (`internal/app/panes.go:91-97`).
- `obor restart` carries an open job's wake only for a lane worker (`internal/launch/launch.go:515-523`,
  `t.info.Role == "worker"`; `cmd/obor/restart.go:116-134`, `laneJob` takes work and fix jobs only). The
  integrator's fresh session had no wake and nothing to fetch by itself.
- A verdict call retries through a serve restart only inside its 15 s deadline (`internal/app/wire/client.go:69-78`,
  T-659), so the integrator's `accept` failed against a 4-minute outage and its turn ended with the job still open
  and `delivered`.

A job in the first prompt would have changed none of this: the job had been delivered.

Fixes, independent of the transport:

1. **Anchor the nudge to the fact.** `timerOf` starts the quiet time at "now"; start it at the `delivered` fact's
   recorded time when that is older (`internal/app/jobs.go:97-107`, `:164-179`; the fact's seq is in
   `p.folded.delivered`, `internal/app/verdict.go:84-85`, and `event.RecordTime` gives its time). The nudge then
   comes at most `Idle` after delivery whatever restarts happen. On 11-07 it would have fired at 09:03, before the
   lead acted. No new state, one fewer unrecorded timer in effect. Difficulty simple.
2. **A role agent's restart carries its open job.** Extend `laneJob` to the integrator's open review or resolve job
   (status already carries `Job` per task, `internal/app/status.go:88-91`) and drop the worker-only condition at
   `internal/launch/launch.go:519` for the integrator (its job id is the task's `Job` whose step is review or
   resolve). The wake is harmless twice (`internal/app/fetch.go:22-25`). Difficulty simple.
3. Optional: `obor mcp` keeps retrying a verdict through an outage longer than its 15 s deadline, bounded (say 2
   minutes), since the call id makes a repeat idempotent (`internal/app/fetch.go:73-80`). Check Claude Code's MCP
   tool timeout first; if the tool call times out earlier, this buys nothing. Difficulty simple, verify first.

## 6. Would herdr events help anywhere?

Serve already subscribes to the pane events that exist (`internal/herdr/contract.go:38-49`) and folds them into its
pane table (`internal/app/panes.go:91-97`) for identification and the watchdogs' "ready" and "gone" reads. The 0.9.5
schema adds nothing prompt-shaped: the event kinds are pane created/updated/closed/exited/moved/focused,
`pane_agent_detected`, `pane_agent_status_changed`, `pane_output_changed` and workspace, tab, layout and worktree
events. `agent_session` on `pane_updated` is herdr's own Claude hook reporting a session id, which obor records
through its own hook already (`internal/guard/session.go:107-113`, `internal/app/sessions.go:14-26`). Nothing here
can replace `delivered`.

## 7. The hook shape, and why it is out

For Claude, the wake's `UserPromptSubmit` hook is the one place a job could travel without the argv cap and with a
proof equal to today's: the hook sees the prompt text (`internal/guard/prompt.go:29`), already answers with
`additionalContext` that the wrong-tab guard and the handoff reminder rely on in production
(`internal/guard/prompt.go:93-97`; 8 injections in the census week, `docs/research/chatter-census.md:96`), already
calls serve (`cmd/obor/main.go:302-310`), and fires for a posted cross-session message as well as a typed prompt
(`hooks.md`, `cross-session-messaging.md`; section 9), so one path would serve a fresh, a resumed and a running
session. The hook would send the same `job_next` socket request `obor mcp` sends (`internal/mcp/mcp.go:131`), serve
would record `delivered{via: hook}` exactly as `via: mcp` (`internal/app/fetch.go:104-117`), and `job_next` would stay
as the fallback and as Codex's and OpenCode's path.

It fails on size: `additionalContext` is capped at 10,000 characters, the rest goes to a file with a 2,000-character
preview (`hooks.md`, "Output limits"). A work job inlines two role texts up to 12 KiB each
(`internal/app/dispatch.go:1136-1166`) and a review job up to 15 KiB of diff (`internal/app/port/effects.go:230`).
Reading the spill file back is the Bash call the change was meant to remove. It would also need the verdict tools to
learn the task and job without a `job_next` reply: a verdict requires the task (`internal/app/verdict.go:139-141`) and
checks the job (`:178-179`), both from the server's last reply (`internal/mcp/tools.go:58-66`). With `alwaysLoad`
removing the ToolSearch call, the remaining `job_next` call is the transport and the proof in one; it stays.

## 8. Plan

| # | Task | Files | Difficulty | Gate |
|---|---|---|---|---|
| 1 | `alwaysLoad: true` on obor's MCP server entry, for the lane start and the plugin manifest; `obor setup mcp` shows it | `internal/agents/claude/claude.go:30-37` (`MCPConfig`), `.claude-plugin/plugin.json:8-18`, `claude_test.go`; if the manifest path ignores the field, `claude.go:172-183` (`needsObor`) splices the config for the integrator too | simple | `make conformance TIER=live TURN=1 KIND=claude`: the first tool call of a worker and of an integrator session is `job_next`, not ToolSearch (read the transcript as the census did, `chatter-census.md:128`) |
| 2 | Stall nudge counts from the `delivered` fact's time (serve and agent restarts no longer reset it) | `internal/app/jobs.go` (`timerOf`, `sampleJob`), a test in `internal/app` that restarts the loop after a delivery and expects the nudge at `Idle` from the fact | simple | `make check-affected`; `docs/state-model.md` rows 10-14 note the anchor |
| 3 | `obor restart <integrator>` carries its open review/resolve job's wake | `cmd/obor/restart.go:116-134` (`laneJob` → the agent's open job, review and resolve included), `internal/launch/launch.go:515-523` (role check), `restart_test.go` | simple | `make conformance TIER=live TURN=1 KIND=claude` (it touches how an agent is launched, AGENTS.md) |
| 4 | Optional: `obor mcp` retries a verdict through an outage up to a bound (the call id makes a repeat idempotent, `internal/app/fetch.go:73-80`) | `internal/app/wire/client.go:69-78` (a longer deadline for the MCP caller only), `internal/mcp/mcp.go:131` | simple | verify Claude Code's MCP tool timeout first; if the tool call times out earlier, drop it |

Order: 1 now (one field, one probe); 2 and 3 next (they close the gap the owner hit); 4 only if the timeout check
says it pays. Do not start a job-in-argv or job-in-hook task for any kind.

Adjacent, not expanded here: census proposal 3 (inline the integrator's role text in its job, as T-603 did for
workers) removes the other removable integrator call and is kind-neutral; and the OpenCode start that "lands ~30 s
after start even when the job was fetched in 4 s" (BACKLOG.md:209-210) could end on the `delivered` fact, which task 2's
anchor makes easy to read.

## 9. Verification notes

Checked by the Claude Code guide against the live docs at code.claude.com/docs/en/ on 2026-11-07 (the 2.1.318
changelog says nothing on these):

- Tool search is on by default for Opus 4, Sonnet 4, Haiku 4.5 and later; `ENABLE_TOOL_SEARCH=false` turns it off
  globally, `auto:N` sets a context-fraction threshold (default 10%). Per server, `"alwaysLoad": true` loads that
  server upfront and `false` keeps it deferred (v2.1.287+), set in the server's entry in `.mcp.json` or
  `--mcp-config`. Not documented: the field in a plugin manifest's `mcpServers`, a per-server environment variable,
  or deferral through `--settings`. `--strict-mcp-config` only limits which servers load (`mcp.md`,
  `cli-reference.md`).
- `UserPromptSubmit` fires for a message another session sends (an idle session starts a new turn with it); the
  docs do not say in so many words that it fires for a positional startup prompt (`hooks.md`,
  `cross-session-messaging.md`).
- Hook output limits: `additionalContext`, `systemMessage`, `initialUserMessage` and plain stdout are capped at
  10,000 characters; over the cap the text goes to a file with a 2,000-character preview (`hooks.md`).
- `SessionStart` `additionalContext` reaches Claude before the first prompt, and the first response waits for the
  hooks (`hooks.md`).

Not verified here, left to the probe in task 1: that a plugin manifest's `mcpServers` entry honours `alwaysLoad`;
that herdr 0.9.5 on this machine refuses an over-long `agent start` line (docs and release notes only).
