# obor-kit design (v2)

Lead-owned. Workers don't change this file; propose changes in `QUESTIONS.md`. The why and the flow are here; the
state, steps and crash classes are `docs/state-model.md`; packages and who imports whom are `docs/module-map.md`.
Hook facts (verified live: PreToolUse deny
works in auto mode; Stop fires with background tasks running and not on `/clear`; `OBOR_ROLE` reaches hooks; stdin
`cwd` follows the session; MultiEdit doesn't exist).

## What obor-kit is
A Claude Code plugin (`obor`) that lets any project run the lead / integrator / workers / reviewers swarm from
axis-alies in herdr panes: the lead skill, role texts, hooks (guards and the worker's `task_done`), commands (`serve`,
`dispatch`, `status`, `spec`, `drop`, `answer`, `send`, `brief`, …), templates (BACKLOG, decisions, task spec, AGENTS.md
section) and the role texts in `templates/agents/`,
read in place from the kit (T-204; projects keep only short `docs/agents/<role>.md` stubs with their specifics). Per-project specifics live in
`.obor/config.json` (committed) and `.obor/local.json` (per machine, gitignored).

## Constraints
- **Runtime deps: the obor Go binary (built, or fetched by `bin/obor`), git, jq, herdr.** The only bash is the
  shim `bin/obor` and the git hooks (`githooks/`): macOS `/bin/bash` 3.2 and GNU bash 5, no GNU coreutils.
- **Hooks:** no-op when the project has no `.obor/config.json`; **no herdr calls, no network**; fast (< 200 ms
  typical; a timed-out PreToolUse hook does *not* block, so speed is part of correctness). They find the project via
  stdin `cwd` → `git rev-parse --git-common-dir`, never `CLAUDE_PROJECT_DIR` (stays at the session-start dir).
- Hooks only cover Claude Code panes. Codex/opencode lanes rely on their sandbox + the worker prompt; the integrator's
  review at accept (placement against `docs/module-map.md`) is the real control for every lane. The Edit/Write guard is
  a backstop, not a sandbox.
- No `git push`, ever, from any obor script (agents: only leads listed in `guards.allow_push`, T-105).

## Layout (plugin root)
```
.claude-plugin/plugin.json      manifest (name: obor)
skills/<name>/SKILL.md          lead (+ its side file review-brief.md), setup, role → /obor:<name>
hooks/hooks.json                {"hooks": {...}} → `bin/obor hook <name>` per event
bin/obor                        CLI entry (on the Bash PATH of sessions with the plugin): obor <subcommand>; the one
                                bash shim: self-host, find the Go binary, exec it (see "Shipping the binary")
bin/go-commands                 the subcommands bin/obor hands to the binary (one per line)
cmd/obor/, internal/            the Go binary (`make build` → dist/obor)
githooks/                       pre-commit, pre-push (obor setup githooks)
templates/                      BACKLOG.md, decisions.md, task spec, agents/<role>.md (role texts, read in place), AGENTS.md section, config examples
tests/                          Go probes (`go/`); the Go tests sit in their packages
docs/                           this repo's own process — not shipped
```
Subcommands: `obor help` lists them (`bin/go-commands`). The integrator's verdicts (`accept`, `reject`, `resolved`,
`hold`, `sendback`) are MCP tools only (T-437); the lead's chat commands are `dispatch`, `sendback <LANE>`, `resume`,
`drop`, `answer`, `spec`, `watch`, `pause`, `send` and `brief`; `pause`, `send` and `brief` are requests to obor serve.

## Paths
- **Project root** = the main checkout = `dirname "$(git rev-parse --path-format=absolute --git-common-dir)"`, from
  any worktree. Scripts always read the **main checkout's** `.obor/config.json` (a lane's copy can be a stale snapshot).
- Lane dir = `<root>/<lanes_dir>/<LANE>` (lanes_dir relative to the root). Scratch merge worktree = `merge.scratch_dir`
  (default `<lanes_dir>/merge`).
- State dir = `<root>/.obor/state/` (gitignored).

## Config
`.obor/config.json` (committed) deep-merged with `.obor/local.json` (gitignored; overrides) by the Go loader
(`config.Load`). Defaults live in the loader so most projects omit most fields. The reference for each key is the
field comments of `config.Data` (`internal/config/config.go`); `obor config validate` checks it (`validate.go`), and
warns on the keys that no longer do anything.
```jsonc
{
  "version": 1,
  "name": "axis-alies",                       // display name
  "prefix": "aa",                             // agent names: aa-o1, aa-s1, aa-int; tab names
  "lanes_dir": "../axis-alies-wt",
  "branches": { "main": "main", "task": "task/{id}" },
  "task_id_pattern": "T-[0-9]+[a-z]?",        // ERE
  "paths": { "tasks": "docs/tasks", "reviews": "docs/reviews" },
  "done_file_sections": ["Summary", "Self-check"],
  "commands": {                               // string = {"cmd": s, "timeout": 600}; null/absent = skipped
    "lane_setup": "bun install --frozen-lockfile",          // reserved: run in new lanes and the scratch worktree
    "check": { "cmd": "bun run check", "timeout": 900 },     // reserved: merge check
    "baseline": { "cmd": "bun run baseline diff \"$OBOR_BASELINE\"", "dir": "../axis-alies-baseline/m8",
                  "timeout": 600 },                          // reserved: runs only if dir exists; dir → $OBOR_BASELINE
    "gate": { "cmd": "bun run gate", "timeout": 900, "paths": ["src/ai/**"] },  // reserved: runs if the merge touches paths, or OBOR_GATE=1
    "check_fast": "bun run check:fast", "e2e": "bun run e2e"  // any other key: `obor run <key>`, named in prompts
  },
  "lane_links": ["reference", "public/assets"],  // symlinked from the root into new lanes and the scratch
  "protected_files": ["src/engine/types.ts"],    // lead-only; globs
  "lane_types": {                                // how to start/reset an agent for a lane of this type
    "opus":   { "kind": "claude", "args": ["--model","opus","--effort","medium","--permission-mode","auto"], "max": 4 },
    "sonnet": { "kind": "claude", "args": ["--model","sonnet","--effort","medium","--permission-mode","auto"] },
    "haiku":  { "kind": "claude", "args": ["--model","haiku","--permission-mode","auto"] },
    "luna":   { "kind": "codex",  "args": ["-m","gpt-6-luna","-c","model_reasoning_effort=high","-s","workspace-write","-a","never"],
                "prompt_extra": "Network is unavailable; deps are installed." }
  },
  "lanes": { "O-01": "opus", "S-01": "sonnet", "T-01": "luna" },  // lane name → lane type; dir = lanes_dir/<name>
  "roles": { "integrator": { "type": "opus" }, "reviewer": { "type": "sol", "worktree": "review" } },
  "merge": { "scratch_dir": null, "trailer": "Co-Authored-By: …", "summary_regex": "[0-9]+ pass|[0-9]+ fail",
             "fail_grep": "error|FAIL|Expected|Received" }
}
```
`.obor/local.json` (per machine, never committed): `lanes_dir` override, `limits` (`max_panes_per_tab`,
per-type `max`), baseline `dir` overrides, `lane_types.*.args` overrides.

Per-kind defaults live in the plugin, overridable per lane type: Claude `--settings
'{"autoMemoryEnabled":false,"remoteControlAtStartup":false}'` for every agent except leads (only the lead keeps Remote
Control and auto memory, T-280/T-471); a lane type's `settings` overlay those keys.
Permission mode and posted lines (T-499): a Claude lane's job wakes and a Claude role agent's briefs are posted to its
session's inbox, not typed. A session in `--permission-mode bypassPermissions` holds a posted message for its 5-minute
dialog and drops it while serve records `sent` (phase-5 advice §8); the delivery timers re-wake a worker, a brief is
not retried. Run Claude lane types in `auto` (every obor-kit lane type does); a `bypassPermissions` lane type is at
its own risk.
Dispatch launches a fresh process per task (R-28, T-157).

**Identity (R-28, T-154/T-159):** the env inside the process (`OBOR_ROLE`/`OBOR_AGENT`/`OBOR_LANE`) and the herdr
**pane label** (the launch mark: the agent's name on the pane), both set by the one launch path (`obor setup` /
`obor restart`, which serve's start calls). The CLI finds an agent's pane by label (`launch.Launcher.PaneOf`); serve
finds it by proofs ("Finding the pane" below); hooks read identity from the env (`obor hook` under opencode: from the
cwd, see "opencode lanes").
After a herdr restart, obor's `[[startup]]` hook (`obor plugin restore`) runs `obor restart` for each labelled
obor pane (herdr `resume_agents_on_restore = false`). One sid file per agent is kept for resuming after a kill.

## State (`.obor/state/obor.db`)
Task, lane and spec state is `obor serve`'s store, one SQLite database: the `events`, `lanes` and `specs` tables, serve their only
writer and every hook and CLI a read-only reader (the guard, whoami and the worker Stop hook read the `lanes` row and
the task's stored spec). What is recorded, in what
order, and what a restart does with it: `docs/state-model.md` ("The log"). `.obor/state/` also holds serve's own
bookkeeping (logs, backups, receipts). `obor pause [--for 3h] "<why>"` is a request to serve: fact `paused{why,
until}` (`docs/state-model.md` row 46); while it holds, a dispatch is refused with the reason and waiting tasks wait;
running gates, accepts and merges finish; `obor pause --clear` ends it; `obor status` shows it.

## Roles
Resolution (`obor whoami`, `project.DetectRole`):
1. `OBOR_ROLE` if set (`lead | integrator | worker | reviewer | deep_dive | advisor`; triage has none), set at pane creation with
   `herdr pane split --env OBOR_ROLE=… --env OBOR_LANE=…`.
2. Else cwd-derived: the toplevel is a linked worktree (`git-dir` ≠ `git-common-dir`) under `lanes_dir` → `worker`,
   `OBOR_LANE` = basename.
3. Else `none`.
**Worker restrictions apply if either env or cwd says worker** (most restrictive wins). **Universal guards don't
consult the role.** A `SessionStart` hook prints `obor whoami` (role, lane, task, the protected files the spec unlocks) into the
session context, plus `obor: role text: <kit>/templates/agents/<role>.md[, project: docs/agents/<role>.md] — read it
before acting` (lead: `run /obor:lead`; T-204).

## Setup (`obor init`, `obor setup`; Go `internal/setup` since T-250)
`obor init [--name] [--prefix]` (T-608) sets a project up with no questions: herdr reachable (else FAIL first),
config with the detected commands, lanes for the CLIs on PATH, panes and roles, the plugin (`claude plugin
marketplace add <kit>` + `install obor@<kit marketplace> --scope project`), obor.events, `obor serve install`, then a
summary (panes waiting on a trust dialog, the file to change, next: `obor lead`). Every step is idempotent: a rerun is
the repair path. `obor setup` keeps the single steps for changes later: `lane`, `pane`, `role`, `githooks`, `herdr-plugin`, `agents`, `mcp`; each idempotent, ending in `OK …` / `FAIL …` (the
contract is the package doc). Role names, env values and worktree defaults come from `internal/roles`. `pane --tab`
splits the tab's largest pane (right when it is wider than tall on screen, else down), not the last one. `githooks`
points `core.hooksPath` at `<kit>/githooks`. `herdr-plugin` links obor.events in herdr from `<kit>/herdr-plugin`
directly, re-linking it when herdr runs another kit's copy: herdr stores the path resolved at link time (a symlink in
between buys nothing), so a kit at a new path needs a re-link; `init` only reports the link state. Every herdr call
goes through `internal/herdr` (T-256), and `pane` / `role` start the agent through `launch.Launcher.Start`, the one
start `obor restart` uses too.

## Lane status (`obor status [--json]`)
Reads the store's `lanes` table read-only (each lane's task and merge evidence) plus the worktree facts git answers
(`missing`, `free`, `dirty-free`, `working`, `done`; `checks.LaneTable`), joined to obor serve's answer (its tasks,
agents, needs-you, pause, the lines not typed yet) when serve is up. The row, how serve projects it from the log and
every reader: `docs/state-model.md` ("The log", the `lanes` bullet). Fields per lane: `lane, type, dir, branch, task,
state, done_age, dirty, warning, op, task_state, attempt, running, agent, agent_status`. Text output: serve's line, one line per
lane, serve's tasks on no lane, needs-you with the commands that answer them, the full check, the pause.

## Hooks (`hooks/hooks.json`; one entry per event; the Go binary branches on role)
Each event's command is `OBOR_NO_SELF_HOST=1 bash "${CLAUDE_PLUGIN_ROOT}/bin/obor" hook <name>` (T-221: never
self-hosted into the cwd's checkout); `bin/obor` execs `obor hook <name>` on the first binary (dist/obor, then the
installed release; hooks never fetch) whose `obor has hook <name>` exits 0 within 2 s (T-195: a stale dist/obor broke
every hook; RV-22 #1: a hanging one is killed with its process group). **No usable binary** (user decision T-201: fail
visibly; the rule lives in `bin/obor`, T-253): in an obor project PreToolUse denies Bash / Edit / Write / MultiEdit /
NotebookEdit with `obor: no usable Go binary in <kit> — run: make -C <kit> build`, except exactly that command (or
`make build` with cwd = the kit); the other hooks allow with that line on stderr; outside an obor project every hook
allows silently.
1. **Stop — lead / integrator: never a block.** Nothing wakes the lead: the owner asks, and each needs-you reaches
   the owner as a herdr notification (serve's). The handoff reminder rides on each prompt (UserPromptSubmit, see
   "Wrong-tab guard").
2. **PreToolUse guards** (matcher `Bash|Edit|Write|MultiEdit|NotebookEdit`; deny = exit 2 + reason on stderr, or the
   `permissionDecision: deny` JSON):
   - universal (Bash): `git push` and pushing `gh` commands (`gh pr create`, `gh repo create … --push`, `gh repo sync
     <repo>`) — except for an agent listed in `guards.allow_push` that is not a worker (T-105, user 2026-10-10: a lead
     that cannot push cannot advance a milestone; name matching only, an accident guard, not a security boundary);
     `pkill`/`killall` by pattern (`pkill -f`, `pkill <name>`, `killall`); `git commit` with `-a`/`--all`/`-am` when cwd
     is the main checkout; an agent CLI run headless — `codex exec`, `claude -p`/`--print`, `opencode run` — for every
     role, the lead included (T-470, owner 2026-10-27: agents run in herdr panes); a load run of tests — `go test`
     with `-count` > 1 or `-race`, a `*.test` binary with `-test.count` > 1 or `-test.race`, two or more test commands
     at once (`&`, `xargs -P`, `parallel`) — for every role (T-543, owner 2026-10-29: one plain `go test <pkgs>` run;
     the gate and nightly run the rest).
   - worker (Bash): git writes (`commit`, `merge`, `rebase`, `reset --hard`, `checkout`/`switch` to another branch,
     `stash`, `push`); redirections / `sed -i` / `mv` / `cp` / `tee` whose target is a protected file.
   - worker (Edit/Write/MultiEdit/NotebookEdit): target resolved relative to the lane toplevel. Inside the lane ->
     allow, any file (T-496, owner 2026-10-28: no "Files you may touch"; placement is checked at accept, only contracts
     are locked), unless a final symlink resolves out of it. Inside another worktree of this repo (main checkout, other
     lanes) or another obor project (a directory with `.obor/config.json`) -> deny. Elsewhere (temp dirs) -> allow.
   - every non-lead role: `protected_files` → deny. Exception (T-238, user 2026-10-14): a worker in its own lane may
     write a protected file that its task's stored spec names as an exact path (no glob) under "Protected files this
     task may edit" — the spec is the lead's authorization for that task. The guard reads it from the store's `lanes`
     and `specs` tables (read-only, the current rev; `docs/state-model.md` "The log"); a store it cannot read unlocks
     nothing.
3. **Stop - worker: `task_done` required.** The hook reads the lane's row read-only: while the lane's task awaits its
   `task_done` it blocks once per stop loop with the hint (call `task_done` with the report, the sections being
   `done_file_sections`; or, to ask, a `Blocked:` report naming what you need: `docs/state-model.md` row 16a). No done file, no block count.
   The row and the finish: `docs/state-model.md` ("The log", the `lanes` bullet, and its last paragraph, the worker
   Stop hook).
4. **SessionStart** (`guard.SessionStart`): prints the `obor: role …, lane …, task …` line, the protected files the
   stored spec unlocks (or that the lane has no row), the hooks this role runs under and the role-text line (T-204). It
   tells serve the agent's session (the store records it; serve down: nothing, T-599) and, for an opencode lane, the plugin marker. When the pane carries a
   obor agent's label but `OBOR_AGENT` is unset, it prints a warning (herdr restore?) and renames nothing; the renames
   herdr needs are serve's (T-416).
5. **StopFailure** (`obor hook stop-failure`, T-532; Claude Code fires it instead of Stop when a turn ends on an API
   error: `error` `rate_limit`, `overloaded`, `billing_error`, … `unknown`, and `error_details`): appends the raw
   `error`/`error_details` with a timestamp to `.obor/state/stop-failure.log` and sends serve one `stopped` request
   (the agent, its role and lane, the error and details); serve down: exit 0, nothing else. Never a block. Serve's
   fact and lines: `docs/state-model.md` row 50 (a role agent's `[obor] <agent> stopped: <error> (<details>)` instead
   of its brief's finished line; a worker's needs-you, carried in the `stopped` fact itself, T-540). A worker's stop
   ends on its next turn: its `UserPromptSubmit` and `Stop` hooks send serve one `turn` request (T-540; serve down:
   nothing, the stop ends on a later turn).

## Serve's work: merge and dispatch
Serve dispatches waiting tasks (`obor dispatch … --after`), gates, merges, runs the nightly full check
(`merge.nightly_at`) and notifies each needs-you through herdr; `obor status` shows needs-you, serve up/down, the last
full check and the pause.
- **merge** (Go `internal/checks`, run by obor serve's gate; `obor nightly` and the after-merge commands share its runner):
  scratch worktree detached at the target; `--no-ff --no-commit`; `lane_links` symlinked into the scratch;
  `lane_setup`, `check`, `baseline` (dir exists), `gate` (paths/`OBOR_GATE=1`) each under its timeout. The outcome is one
  line: `MERGED … | CONFLICT in <scratch>: … | RED … | HOLD … | FAIL …`. Counts (`merge.summary_regex`, each alternative
  summed over a chained command's parts) and seconds go in the MERGED line; a RED names the `merge.fail_grep` matches and
  the log. Commit messages carry the worker label and `merge.trailer`. Every command's full output is kept in
  `.obor/state/logs/<task|nightly|milestone>-<name>.log`, so a RED can be triaged without a re-run.
- **dispatch** (obor serve, `internal/app`): branch from the root's current branch; refuse unless the lane is `free`
  (or `dirty-free` with `--force`) and its agent is not working/blocked; `checkout -B`; the `lanes` row records the
  dispatch; **launch** a fresh worker (`agent.start`: `Agents.Launch(Launch{Fresh: true})` with env, label and type args) whose
  first prompt is the work job's wake, `[obor job <id>] call the obor job_next tool`. The worker's `job_next` is the
  delivery. No screen reading (R-28 §4, T-157). An undelivered wake is retried after 5 minutes, then becomes needs-you.

## Machine load (v2.1, 2026-10-08 — requests from the axis-alies lead)
Many lanes on one machine (load 20–50 on 10 cores; obor-kit and axis-alies share it) turn timing-based tests RED.
- **Merge window:** serve's gate runs the checks (`docs/state-model.md`, rows 18–33). Worker prompts/skills say: full
  checks and e2e only via `obor run`, focused tests while iterating, the class check once before `task_done`.
- **Load-aware retry:** after a RED, if **every** failing line matches `merge.timeout_grep` (e.g. `timed out|Timeout|
  exceeded`) and the 1-min load average > `merge.max_load` (default: number of CPUs), serve's gate waits until load
  < `merge.max_load` (max `merge.load_wait`, default 600 s), re-runs the failed command once, and reports
  `MERGED … (retried after load <x>)` or the second RED. Never more than one retry.
- **Watchdogs:** a stalled or blocked worker and a pane that exits are serve's facts (`needs-you`), recorded and
  notified by the merge loop (`docs/state-model.md` "Timers").
- **Dispatch:** refuses a lane with uncommitted leftovers (`dirty-free` → FAIL unless `--force`).

## Wrong-tab guard (2026-10-08 — user request via the axis-alies lead)
When the user types into a worker's pane by mistake, the worker must not act on it: it first replies
`I am worker <agent> on <task>. Did you mean to type here?` and continues only on confirmation.
- Every line obor delivers (typed, or posted to a Claude session's inbox, T-499) starts with `[obor `: a job's wake
  `[obor job <id>]` (the only thing sent to a worker), and serve's lines ("Lines to the lead"). Nobody messages a worker; its question is answered by `obor answer`.
- `obor hook user-prompt-submit` (UserPromptSubmit): role worker only; prompt starts with `[obor ` or `/` (slash
  command), or is one of Claude Code's machine envelopes (opening tag at the start, closing tag at the very end: e.g.
  `<task-notification>`, and, T-502, `<agent-message from=…>` (a subagent's hand-back) and `<cross-session-message
  from=…>` (another Claude session's SendMessage); Claude Code 2.1.312 gives the hook no origin field for these, only
  the prompt; the captured payloads in `internal/guard/testdata` are the contract, captured again when the pinned
  Claude Code version changes) → nothing. A raw post to the inbox socket arrives bare: obor's pass by their `[obor `
  tag; an untagged one cannot be told from typing and is guarded. Otherwise inject context instructing the agent to reply exactly with the question above (agent
  name from the store's `lanes` row or `OBOR_LANE`, task from that row) and not to run any tool until the user
  confirms in the next message. Accident guard, not a security boundary (the tag is not secret).
- Handoff reminder (T-232): the same hook, for a lead or integrator whose session context is above its
  `roles.*.handoff_tokens` (`0` = off), adds a reminder to every prompt: hand off at the next logical break, not
  mid-work. It is a reminder, not a block: the agent picks the break, writes the Handoff section of `BACKLOG.md` and
  commits it (the lead; the integrator hands off after the merge in hand), then runs `obor restart <you> --fresh`,
  which from its own pane is scheduled: a detached run waits until the agent is idle and restarts it fresh.
- Worker skill/template/prompt: the same rule in words, as a fallback for non-Claude lanes (Codex has no hook).

## Git hooks layer (v2.3)
A second layer that doesn't depend on agent hooks (also covers Codex/opencode lanes and humans).
- Shipped in the kit as `githooks/`: a file per client-side hook git runs, each sourcing one runner (`_hook`).
  `obor setup githooks` sets `core.hooksPath` in the project's repo config (shared by all worktrees) to
  `<kit>/githooks`. A project that already has its own `core.hooksPath` keeps its hooks: setup records it as
  `obor.hooksChain` (git config) and `--remove` puts it back (T-620; a herd or obor kit's path is re-pointed).
- Every hook first runs the project's own `<name>`: `<obor.hooksChain>/<name>`, else `.git/hooks/<name>`; args and
  stdin pass through; its failure is the hook's result where git uses one (not for the post-* hooks). Then obor's
  rule, for `pre-commit` and `pre-push` only. Without `.obor/config.json` a hook only chains.
- `pre-push`: always refuses (`obor: the user pushes; set OBOR_ALLOW_PUSH=1 to push by hand`).
- `pre-commit`: refuses in a lane worktree unless `OBOR_MERGE=1` (set by serve's gate for its
  commands and scratch commits); in the main checkout git can't tell a `commit -a` from a normal commit, so nothing is
  checked there (the agent guard covers `commit -a`).
- obor has no rule of its own in `post-merge` / `post-commit` (serve dispatches the waiting tasks, `--after`, and runs
  `commands.after_merge` in the main checkout after a merge); a project's own still run through the chain.

## Machine-wide check slots (v2.4, 2026-10-08 — axis-alies lead + user; protocol from axis-alies T-373)
Both workspaces share one machine, so heavy test runs take a slot from one **machine-wide** slot dir shared with
axis-alies: `slots.dir` (kit default `/tmp/obor-check-slots`; env `CHECK_SLOT_DIR` overrides; machines shared with
axis-alies set `/tmp/axis-check-slots` in `.obor/local.json`), `slots.max` (default 2,
env `CHECK_SLOTS`). `obor run <heavy command>` acquires a slot before running (waits with a line every 30 s; stale
slots with a dead PID are reclaimed; released on exit incl. signals) and also waits while the `merge-running` marker
exists; serve's gate runs its checks with `CHECK_PRIORITY=merge` (reserved slot, never waits on worker slots,
creates/removes the `merge-running` marker). The slot file format must match axis-alies `scripts/checkSlots.ts`
(T-373, merged 2026-10-08) exactly: files `slot-0`…`slot-<N-1>` and `merge-running` in the dir, created exclusively
(O_EXCL — bash `set -C`), content `<pid> <epoch ms>\n`, a file whose PID is dead is reclaimed once (unlink + retry),
release only if the file still holds our PID. Workers don't start while a live `merge-running` exists; a merge claims
`merge-running` (waits only for another live merge marker, never on worker slots).
**Stale reclaim (agreed with axis-alies 2026-10-08, RV-03 #6):** reclaiming a slot or marker whose holder PID is dead
is serialised per path by an atomic `mkdir <path>.reclaim` guard: inside the guard re-read the holder, unlink only if
it is still the same dead PID, create the file, then `rmdir` the guard. A guard older than 10 s is stale and may be
removed. Normal acquisition (no stale file) is unchanged. axis-alies ports the identical rule to checkSlots.ts.
**Per-project merge marker + block cap (agreed with axis-alies lead-mp 2026-10-08):** the marker is per project, `merge-running-<project>` (`<project>` = config `name`, env `CHECK_PROJECT` overrides;
content unchanged). A worker check waits only for its **own** project's live marker; **every** live marker of any
project counts as one occupied slot toward `slots.max` for everyone (load stays machine-wide). A merge waits only for
its own project's live marker. Cap: a marker older than `slots.merge_block_max` (default 900 s; its epoch ms field)
stops blocking its project's workers (it still counts as a slot). A legacy `merge-running` (no suffix) is read as a
marker of an unknown project. axis-alies has the same rule in checkSlots.ts with project `axis-alies`.
**Slot handed to the command (T-121, 2026-10-10 lead-mp report):** `obor run` for a heavy command exports
`CHECK_SLOT_HELD=<the slot it claimed>` (and keeps `CHECK_PRIORITY`) to the command, so a slot-aware runner inside
(checkSlots.ts) reuses that slot instead of claiming a second one, and the check's timeout never includes a slot wait.
obor-kit uses its own slot dir.

## Skills: only the lead's is model-invoked (T-204, 2026-10-13)
`lead` is the one self-chosen skill: a lead must find its way back after `/clear` and a handoff. Workers, the
integrator and reviewers are always started by obor, so their text comes from the kit (`templates/agents/<role>.md`)
through the dispatch prompt and the SessionStart role-text line — one source per role, and Codex lanes (which read no
Claude skills) get the same text. Rarely needed lead procedures are side files of the lead skill. `setup` and `role`
carry `disable-model-invocation: true`: only a person starts them (`/obor:setup`, `/obor:role`); a lead uses the CLI
(`obor setup …`) instead. User decision 2026-10-13.

## Change-based merge checks (v2.5, 2026-10-08 — user + axis-alies T-375)
A UI-only task shouldn't fail its merge on an engine/AI soak test. Skipping is allowed only when a project guard
proves the skipped code can't depend on the changed code (e.g. an import-direction test: engine/AI never import UI).
- Config `merge.classes`: ordered list `[{ "name": "ui", "paths": ["src/ui/**", "e2e/**", "*.md"], "checks":
  ["check_ui", "e2e_touched"] }, { "name": "core", "paths": ["src/engine/**", "src/ai/**"], "full": true }]`;
  `merge.guard`: a command name (e.g. `check_imports`). Each changed file (`git diff --name-only <target>...<branch>`)
  gets the first class whose glob matches. **Neutral** files (`merge.neutral` globs, default `["docs/**"]` (RV-07 #2: `*.md` would match nested product files; list root .md files explicitly);
  2026-10-09 user) never decide the class: they only need the guard. **Partial** = every non-neutral file in one class
  that has `checks` and is not `full` (all files neutral → partial `neutral`, guard only)
  → run `guard`, then that class's `checks` (each a `commands.<name>`, with `OBOR_CHANGED_FILES` = path to a file
  listing the changed paths); baseline/gate skipped. Otherwise (**mixed**, any unmatched file, a `full` class, no
  `merge.guard` configured, or `OBOR_FULL=1`) → today's full path (check + baseline + gate rules). The MERGED line
  says `partial:<class>` or `full:<reason>` (the file that decided it). A failing guard → RED (never falls back).
- `obor nightly`: full check + baseline + gate (forced) + `commands.e2e` if set, in its own nightly worktree
  (`<merge.scratch_dir>-nightly`, not the merge scratch) at the current branch head, under the merge window; it then
  backs the store up; writes `state/last-full-check` (`<sha> <epoch> GREEN|RED <summary>`); prints `NIGHTLY GREEN …`
  / `NIGHTLY RED …` last. Serve runs it at `merge.nightly_at` (unset = off) when due and records the outcome
  (`docs/state-model.md` row 45); `obor status` prints the last full check and its age.

## Check tiers (v2.6, 2026-10-08 — user decision via axis-alies)
Slow tests and long property gates run only at milestone end (+ an optional non-blocking nightly), never in per-task
merge checks: fixing what they find later is cheaper than waiting on every merge.
- `commands.check` = the per-merge check (fast tier). `commands.check_milestone` = the slow tier (slow suites,
  property gates, soak). `commands.gate` and `commands.baseline` get `"at": "milestone"` by default; `"at": "merge"`
  keeps today's per-merge behaviour (opt-in).
- Serve's gate runs only per-merge commands (check, or the change-based partial set of v2.5; plus gate/baseline with
  `at: merge`).
- `obor nightly` (v2.5) runs the milestone tier: check + check_milestone + baseline + gate + e2e; **non-blocking**
  (reports, never holds merges; serve's nightly run records `needs-you{full-check RED <sha7>}`, T-464).
- `obor milestone-check` = the same tier, run by the integrator before the milestone merge into `main` (blocking:
  the milestone merge waits for GREEN, or for a lead decision logged in `docs/decisions.md`).

## Test tiers and receipts (T-349, 2026-10-25)
- `obor run <command>` of a **heavy** command in a lane exports `OBOR_CHANGED_FILES` (state file
  `.obor/state/run-changed-<lane>`): the lane's files against its merge base with the target, committed and uncommitted
  (untracked too), the run marker not. It is computed from the lane's content tree, which is built
  with a temporary index (`GIT_INDEX_FILE`, `add -A`): no write to the lane's index or history.
- **Receipt.** A heavy run (no extra args) that ends green with the lane's tree unchanged writes
  `.obor/state/receipts/<tree>-<check>`: `<tree> <check> <sha256 of the command text> <epoch> <lane> <secs>s`. A red,
  timed-out or signalled run writes nothing; only `obor run` writes one.
- **Reuse.** Serve's gate, per check they would run (`check`, a class's checks, `gate`; never the
  guard, `baseline` or `nightly`): a receipt for (merge tree, check, command hash) younger than `merge.receipt_max_age`
  (s, default 86400; 0: never) is not run again; its place in the result and `Candidate.Checks` reads `reused <lane>
  <tree7>`. The merge tree equals the lane's only when the lane is based on the current target; a moved target, a
  changed command or a later edit runs the check as before. A merge that reused a check is `partial` (fact `merged` without `full`, T-525).
- **`CHECK_CPUS`** (`obor run` of a heavy command and the gates' commands): `slots.cpus` when set, else
  `max(1, cores / slots.max)`. The project maps it (obor-kit: `go test -p`).
- **Go projects: `obor affected go`** (T-351; research §2, §5). `obor affected go [--changed <file>]` (default
  `$OBOR_CHANGED_FILES`, run from the module root) prints the affected package import paths one per line, none when no
  package is affected, or one `full: <why>` line: a changed `go.mod`/`go.sum`, a file matching `merge.affected_full`
  (globs, default none: build files and shared test support, e.g. `Makefile`, `internal/testkit/**`), a removed
  package, an unreadable list or a failing `go list`. A project wires it in three steps: a `check_affected` command
  whose `go test` takes that list (obor-kit's `make check-affected`), a `merge.classes` entry for its Go paths with
  `checks: ["check_affected"]`, and `merge.guard` for the rest; `CHECK_CPUS` maps to `go test -p`.

## Stable channel (owner 2026-10-30, phase 7)
Other projects never run obor from this repo's main checkout (every merge here would change their live tooling).
They use one kit per machine, `~/.obor/kit`: a checkout at the newest release tag, made and moved by `install.sh`
(RELEASING.md), with `obor` linked into `~/.local/bin`. A release is tagged only after its merges are reviewed and
verified; adopting leads hear of it by `obor send`. A project's serve picks a new kit up at `obor serve restart`.

## Shipping the binary (T-187, T-195, T-240; bin/obor the only bash since T-253)
`bin/obor` is a small self-contained bash shim (no other kit file sourced):
- **Self-host** (T-240): inside an obor checkout (main or lane worktree: `bin/obor` plus a plugin.json named `obor`) it
  re-execs the main checkout's `bin/obor`, so a lane never runs its own stale build; `OBOR_NO_SELF_HOST=1 ./bin/obor`
  runs a lane's own; kit-owned callers (hooks, mods) pin `OBOR_NO_SELF_HOST=1`.
- **Binary:** the kit's dev build `dist/obor` when present (run as is: a stale one says `unknown subcommand`, `make
  build` fixes it), else `~/.obor/bin/obor-<version>-<os>-<arch>`, version = the kit's `VERSION` (no
  per-project pin since T-597); missing → fetched from `bin/release-url` with curl and checked against `bin/checksums.txt` (`make
  release` keeps that catalog cumulative). It exports `OBOR_PLUGIN_ROOT` and `OBOR_VERSION_SOURCE` (dev build /
  kit default) and execs `<binary> <subcommand> …` for the subcommands in `bin/go-commands`; others → usage, exit 2.
- Hooks: the `has` check and the no-binary rule ("Hooks").

## herdr integration (2026-10-09 — user direction)
obor should be as simple and as integrated as possible: herdr (which maintains agent detection) is the source of
agent state; obor does no Claude/Codex screen parsing of its own beyond what exists, and removes it as herdr grows the
API. The herdr plugin (`herdr-plugin/` in this repo, linked with `herdr plugin link`) hooks only `[[startup]]`:
after a herdr session restore, `obor plugin restore` restarts obor's labelled panes. The manifest's command runs the
Go binary through the kit's `bin/obor` (`launch.Restorer`); it starts itself detached and returns at once.
Everything file- or git-based stays in the Go binary: plugins have no timers or storage, so the herdr plugin only
starts `bin/obor`.

## Lines to the lead (T-466, 2026-10-27 — owner decision, phase 4)
Serve posts two kinds of lines to the lead (`roles.lead.agent`) into its Claude session, and delivers a brief to a role
agent (posted to a Claude session, typed into another kind's pane, T-499); nothing is typed into the pane the owner
writes in, and nothing else goes to a chat agent but a job's wake. State in `docs/state-model.md` row 47.
- **`obor send <project> "<text>"`** (a lead or the owner): `<project>` is a path into an obor project. One `obor serve`
  per project (T-522, owner decision 2026-10-28): it runs in the project and listens on
  `<root>/.obor/state/serve.sock`, so the request goes to `<target root>/.obor/state/serve.sock` (v0.14 or later in
  both projects; the v0.14 transition socket is gone, `docs/UPGRADE-v0.15.md`). That serve records
  fact `message` (task key `msg`) and posts `[obor <sender project>] <text>` to its lead's session.
  The own project is refused. A repeated call id gets the stored reply.
- **Briefs:** `obor restart <role-agent> --fresh --prompt "<brief>"` (the start carries the brief) records fact
  `briefed` once the restart succeeded; `obor brief <role-agent> "<text>"` records it with a line `[obor <caller>]
  <text>` delivered to the role agent once its pane is idle (a verify into the running session). A role agent is any
  agent of the config but the lead, the integrator and the workers. Once that pane has worked after the brief (after
  its line was delivered) and is idle again, serve records and posts `[obor] <agent> finished (briefed <age> ago)` to the lead's
  session: one line per brief, nothing for task or merge flow. A turn that ends on an API error (the StopFailure
  hook, T-532) ends the brief with `[obor] <agent> stopped: <error> (<details>)` instead.
- **Delivery** is one function in serve (`deliver`, each merge-loop pass): the oldest line per agent. The lines
  survive a restart (the log) and a crash may deliver one twice, never not at all (T-524); `obor status` lists the ones
  not delivered yet, and one waiting over 24 h is the project's needs-you. Mechanics: `docs/state-model.md` row 47.
- **A needs-you reaches the lead** (T-497; the herdr notification reaches the owner, the lead is the one who acts):
  each needs-you fact in force, once, queues the line `[obor] needs-you <task> (<lane>): <cause, one line> → <the
  commands that answer it>` to the lead (row 47).
- **Lines to the lead are posted, never typed** (T-485, owner 2026-10-28: nothing types into the pane the owner writes
  in, and an idle lead must still be woken). Claude Code binds an inbox socket per session
  (`CLAUDE_CODE_MESSAGING_SOCKET`, with `CLAUDE_CODE_MESSAGING_TOKEN`; needs Claude Code ≥ 2.1.224); every agent's
  SessionStart hook records it as `${OBOR_STATE_HOME:-$HOME/.obor}/agents/<agent>.inbox` (0600;
  a session without one removes it). Serve posts the line to the socket that record names (the auth line, then a
  `user` message; `cmd/obor`'s `poster`): no pane lookup, no idle rule. An idle lead starts a new turn with it, a busy
  one reads it between tool calls; it arrives as a message from another session, and the owner's half-typed draft
  stays in the box. No record (or its socket gone) → the line waits, untried; a refused dial → fact `failed`, the
  next pass. There is no ack: `sent` means handed to the socket, as a typed line's means handed to herdr. A lead of
  another kind (Codex, OpenCode) has no inbox: `obor status` and the 24 h needs-you.
- **Role agents' lines go as the pane's kind takes them** (T-499, `agents.Kind.Deliver`), only while that agent's pane
  is idle or done (the rule of job delivery): a kind with an inbox (Claude) gets the line posted to its session as
  the lead's is (no record → it waits, untried), any other kind (Codex, OpenCode) typed into its pane (`agent.prompt`,
  `herdr.Panes.Type`), no Escape, no screen read, no clear. A typed line herdr refuses because the pane is blocked
  (`blocked`, `agent_blocked`) is a refusal before typing: fact `failed`, retried at a later pass. For Claude the
  inbox ends the dialog class (a typed line landing in a Claude Code dialog): a post cannot reach a dialog; a
  session behind an open permission dialog reads it when its turn continues, and it cannot answer the dialog.
- **Job wakes go the same way** (T-499, owner 2026-10-28): a worker's or the integrator's wake to a Claude session with
  an inbox record is posted, `[obor job <id>] call the obor job_next tool` as typed (no `ctrl+u`, the same intent and
  result, `docs/state-model.md` "Prompts": a wake is harmless twice, T-524). Another kind, a pane serve does not see, or a Claude
  session with no inbox record (a session its SessionStart hook did not register) is typed as before: a refused wake
  would strand the worker until a restart, a typed one reaches it as it always did.
- **Finding the pane** (T-504): one identity table per project, derived each pass
  from proofs (session record > serve's own start > label; herdr's agent name is never read), answers which pane an
  agent runs in (Identified, Ambiguous: two panes with equal proof, or Unavailable) and which agent a pane runs. Every
  line, brief and job wake is addressed to the identified pane's id right before it goes; ambiguous or unavailable,
  it waits (lead lines are posted by inbox record, pane-independent). The CLI (`obor restart`) finds a pane by
  its label, so it works while serve is down. Mechanics: `docs/state-model.md` row 48.
- Only for projects with a merge loop (an integrator role): the loop's pass is the delivery's clock. `obor send` and
  `obor brief` to a project without one are refused (`merge loop off …`), as `obor dispatch` is (T-480).

## opencode lanes (T-306, T-308, 2026-10-17 — user direction)
opencode runs no Claude Code hooks. The kit ships `opencode/obor.js`, an opencode **2.x** plugin (default export
`setup`; opencode 2.x's plugin runtime API is undocumented, found in the binary: T-307's dispatch warning shows when it
stops loading). It holds no obor rules: each hook maps opencode's event to the Claude Code hook event JSON and runs
`obor hook <name>`, so the guard and the session context stay in the Go binary. `tool execute.before` →
`pre-tool-use` (a deny throws, which aborts the tool with obor's line; an unreadable patch is denied); session context
→ `session-start` (with `session_id: ""`: obor never resumes an opencode session, so no session record). There is no
`task_done` re-prompt on 2.x (it delivered no idle event).
`obor hook` runs without `OBOR_AGENT`, `OBOR_ROLE`, `OBOR_LANE`, `HERDR_PANE_ID`: identity comes from the cwd and
the lane state, because a shared `opencode serve --service` carries whichever pane's env started it. opencode lanes
run `opencode --auto --standalone` (own server with the pane's env). Installed user-global by `obor setup
agents` (alias `opencode-plugin`; a link in `~/.config/opencode/plugins/`, beside herdr's own plugin); a no-op outside an obor project.
The `obor` it runs is the linked kit's, except inside an obor checkout (T-478, `bin/obor`'s self-hosting rule): there the
main checkout's `bin/obor` (with `OBOR_NO_SELF_HOST=1`, never a lane's own build), so obor-kit's own lanes run its
guard, not an older global kit's. The guard fails closed on a store it cannot read: a store that exists but cannot be
opened or read (a newer schema, any open error) denies a non-lead role's writes inside the repo, naming the store and
"run this kit's binary"; no store yet, or no lane row, stays as before.
The session-start hook writes `.obor/state/lanes/<LANE>.plugin` in the main checkout (T-310: never in the lane worktree), a marker holding the time the hook ran (T-307). `obor status` shows `no-plugin` for a working opencode lane whose marker is missing or older than the lane row's dispatch (`cmd/obor/status.go` `pluginMissing`), so a lane whose plugin never loaded for the current dispatch is flagged.
The model of an opencode lane is a per-worktree `opencode.json` (git-excluded).
