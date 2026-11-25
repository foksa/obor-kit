# obor-kit design (v2)

Lead-owned. Workers don't change this file; propose changes in `QUESTIONS.md`. The why and the flow are here; the
state, steps and crash classes are `docs/state-model.md`; packages and who imports whom are `docs/module-map.md`.
Hook facts (verified live: PreToolUse deny
works in auto mode; Stop fires with background tasks running and not on `/clear`; `OBOR_ROLE` reaches hooks; stdin
`cwd` follows the session; MultiEdit doesn't exist).

## What obor-kit is
A Claude Code plugin (`obor`) that lets any project run the lead / integrator / workers / reviewers swarm from
axis-alies in herdr panes: the lead skill, role texts, hooks (guards and the worker's `task_done`), commands (`serve`,
`dispatch`, `status`, `spec`, `drop`, `resume`, `send`, `brief`, …), templates (BACKLOG, decisions, task spec, AGENTS.md
section) and the role texts in `templates/agents/`,
read in place from the kit (projects keep only short `docs/agents/<role>.md` stubs with their specifics). Per-project specifics live in
`.obor/config.json` (committed) and `.obor/local.json` (per machine, gitignored).

## Constraints
- **Runtime deps: the obor Go binary (built, or fetched by `bin/obor`), git, jq, herdr.** The only bash is the
  shim `bin/obor` and the git hooks (`githooks/`): macOS `/bin/bash` 3.2 and GNU bash 5, no GNU coreutils.
- **Hooks:** no-op when the project has no `.obor/config.json`; **no herdr calls, no network**; fast (< 200 ms
  typical; a timed-out PreToolUse hook does *not* block, so speed is part of correctness). They find the project via
  stdin `cwd` → `git rev-parse --git-common-dir`, never `CLAUDE_PROJECT_DIR` (stays at the session-start dir).
- Hooks cover Claude Code panes, OpenCode lanes (through its plugin, "opencode lanes" below) and agy panes (through
  the kind's transport, "agy lanes" below); Codex lanes have none and rely on their sandbox + the worker prompt; the integrator's
  review at accept (placement against `docs/module-map.md`) is the real control for every lane. The Edit/Write guard is
  a backstop, not a sandbox.
- No `git push`, ever, from any obor script (agents: only leads listed in `rules.may_push`).

## Change rules (owner 2026-11-05; docs/research/architecture-advice-fable.md, docs/reviews/DD-architecture.md)
1. **Nothing depends on an agent remembering.** Every step of a procedure has one guarantee:
   - **kernel**: exactly once, crash-safe (intent and result, replay, crash cuts): placing a task, submit, gate,
     publish, verdicts, drop and lane free, spec revs, job delivery, needs-you and its first notification, stops whose
     order against the lane's next use matters. A step is kernel when its order against another step is the
     guarantee, or a repeated or late run destroys work or moves main.
   - **rule**: serve recomputes the condition from current state on every pass and acts (a line, a brief, a stop, a
     prompt: one idempotent call); done when the action's effect shows in state; only a line records `did{key}`. No
     intent, crash class, replay case, claim or fold of its own. Its written failure mode: after a crash an action may
     run twice or one pass late.
   - **refusal**: the verb every path to "done" goes through refuses until the step is done, naming the command;
     always paired with a rule or the kernel, never alone.
   - A step that needs judgment is a **standing obligation** (shown, lined, repeated, escalated) until the verb that
     carries it arrives; never a line sent once.
2. **A replacement deletes what it replaced**, in the same task, or the spec says why both stay and until which
   release.
3. **A spec names what it adds**: each state, step, verb, fact, `Message` field and config key, or none.
4. **No by-hand twin of an automated path**, unless a store shows it used within one release.
5. **A compat fold branch names its release**; at each release a census of every registered store deletes the
   branches with no records. A command that changes state is a serve call; a CLI does no multi-step work.
6. **Reviews** ask "what did this make redundant? name it or confirm it is gone" and "does it add a field or type
   that copies data another type already holds? name the owner type" (owner 11-05: a task had five shapes) and "does a client decide something from a state
   string that serve knows? then serve ships the predicate" (owner 11-05). On rule-tier steps a crash or
   interleaving finding counts only when a kernel guarantee breaks, and its fix goes to the integrator, not a reviewer
   round. No coverage-ratchet findings; a rule's test is one table (condition → action); deleting a mechanism is how
   tests are pruned.
7. **Readers while serve is down read a projection.** A hook or reader that needs serve's state when serve may be
   down reads a column serve projects (as the lanes table: serve rewrites it on every change), never a query that
   re-folds the `events` table (owner 11-05).

## Layout (plugin root)
```
.claude-plugin/plugin.json      manifest (name: obor)
skills/<name>/SKILL.md          lead (+ its side files review-brief.md, commands.md, agent-setup.md), setup → /obor:<name>
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
`hold`, `sendback`) are MCP tools only, as are the worker's `job_next`, `task_done` and `merge_preview` (the lane's
work, uncommitted changes included, merged onto the target's head now by `git merge-tree`: clean or the conflicted
paths; read-only, the gate still decides); the lead's chat commands are `dispatch`, `resume`, `drop`, `spec`,
`watch`, `pause`, `send` and `brief`; `pause`, `send` and `brief` are requests to obor serve. Every task command
(`drop`, `resume`, `drive`, `land`, `watch`) takes a task id, never a lane: serve knows the lane. `obor resume
<T-xx> ["<text>"] [--regate|--sendback]` is the one verb that sends the lead's word to a task: serve picks the action
from the task's state (a question's answer, a stalled or stopped worker's line, a drive's line, a sendback of a green
or reviewed task, a merge-loop needs-you's regate, reprompt or retry; a flag only where two fit: red past K
`--regate` or `--sendback`, a Blocked report `--regate`). Which verbs a state takes is the `verbs` column of
`internal/app/states.go`; every verb also needs no live claim on the task (`proj.busy`).

## Paths
- **Project root** = the main checkout = `dirname "$(git rev-parse --path-format=absolute --git-common-dir)"`, from
  any worktree. Scripts always read the **main checkout's** `.obor/config.json` (a lane's copy can be a stale snapshot).
- Lane dir = `<root>/<lanes.dir>/<LANE>` (`lanes.dir` relative to the root). Scratch merge worktree =
  `<lanes.dir>/merge` (derived, not a key).
- State dir = `<root>/.obor/state/` (gitignored).

## Config
`.obor/config.json` (committed) deep-merged with `.obor/local.json` (gitignored; overrides) by the Go loader
(`config.Load`). Defaults live in the loader so most projects omit most fields. The reference for each key is the
field comments of `config.Data` (`internal/config/config.go`); `obor config validate` checks it (`validate.go`), and
a load warns on every key it does not know. Serve reads the file at each pass, so a change counts at its next pass with
no restart; it reads `project.name`, `project.agent_prefix`, `project.terminal`, whether `agents.roles.integrator` is
set, `agents.roles.lead` (the lead's name) and the kind its outbox routes to (`agents.models.<the lead's model>.kind`)
once, at its start, so a change to one needs `obor serve` restarted (`obor config validate`'s OK line says so).
The file is **shape 2**: ten top-level keys — nine groups by what they are about, and `version` — in this order.
```jsonc
{
  "version": 2,
  "project": {
    "name": "axis-alies",                       // display name
    "agent_prefix": "aa",                       // agent names: aa-l-01, aa-int; tab names
    "task_id_pattern": "T-[0-9]+[a-z]?"         // ERE
  },
  "lanes": {
    "count": 3,                                 // L-01 … L-03, every one pooled; "list" names them instead
    "dir": "../axis-alies-wt",                  // lane worktrees; the merge scratch is <dir>/merge
    "task_branch": "task/{id}",
    "links": ["reference", "public/assets"],    // symlinked from the root into new lanes and the scratch
    "setup": "bun install --frozen-lockfile"    // run in new lanes and the scratch worktree
  },
  "agents": {
    "roles": { "lead": "opus:high", "integrator": "opus", "reviewer": "sol:high" },  // model refs
    "difficulty": { "hard": ["opus:high"], "medium": ["sonnet:medium", "deepseek"], "simple": ["deepseek"] },
    "kinds": {                                  // per kind: args after the model, the work jobs' job_note
      "codex": { "args": ["-c", "sandbox_workspace_write.network_access=true"],
                 "job_note": "Network is unavailable; deps are installed." }
    },
    "models": {                                 // the project's models: a model ref names one of these
      "opus": { "kind": "claude", "id": "opus" }, "sonnet": { "kind": "claude", "id": "sonnet" },
      "opus-4.6": { "kind": "claude", "id": "claude-opus-4-6" }, "sol": { "kind": "codex", "id": "gpt-6.1-sol" },
      "deepseek": { "kind": "opencode", "id": "opencode-go/deepseek-v4.1-flash" }, "muse": { "kind": "opencode", "id": "" }
    },
    "handoff_tokens": 500000                    // the lead's handoff reminder threshold
  },
  "rules": {
    "protected_files": ["src/engine/types.ts"]  // lead-only; globs ("may_push", "deny_commands" sit here too)
  },
  "merge": {
    "target_branch": "main",
    "check": { "cmd": "bun run check", "timeout": 900 }  // what every merge must pass
  },
  "nightly": {
    "slow_check": { "cmd": "bun run check:slow", "timeout": 3600 }  // the nightly's long tier; "at" switches it on
  },
  "commands": {                                 // the project's own: `obor run <name>`, named in merge/nightly keys
    "lint": "bun run lint"                      // string = {"cmd": s, "timeout": 600}; null/absent = skipped
  },
  "checks": {
    "output": { "summary": "[0-9]+ pass|[0-9]+ fail",
                "failure": "error|FAIL|Expected|Received" }
  },
  "reviews": { "dir": "docs/reviews" }
}
```
`.obor/local.json` (per machine, never committed): any key of the config, `lanes.dir` included (an object merges key
by key; a list or scalar replaces). A tab holds four panes: a constant, not a key.

**The seven fixed commands** keep the names typed at a prompt and written in logs, steps and role texts
(`obor run check`, `lane_setup`, `after_merge`, `check_milestone`, `<task>-check.log`); each lives in the group that
runs it, and `Config.Command` (`internal/config/config.go`) maps the name to it: `lane_setup` → `lanes.setup`,
`lane_ready` → `lanes.ready`, `compile`, `check`, `retest` and `after_merge` → `merge`, `check_milestone` →
`nightly.slow_check`. Every other name is a `commands.<name>`.

**Models:** the config names models with their effort directly: a model ref is `<model>[:<effort>]`
(`opus:high`), in `agents.difficulty` levels, `agents.roles.<role>`, a spec's `Model:` line, `--model` and
`obor pause --model`. `agents.models.<name>` is the one place a name resolves (`Config.Model`): `kind` required, `id`
the model id the kind takes, verbatim (`""`: the kind's own choice), so a version or a variant is one more entry
(`"opus-4.6": {"kind": "claude", "id": "claude-opus-4-6"}`, `"opus-1m": {"kind": "claude", "id": "opus[1m]"}`). The
name is the project's label: no `:`, `,` or whitespace. A name with no entry is refused; for a name of the upgrade
list (`config.UpgradeModels`: opus, sonnet, haiku, fable → claude; deepseek → opencode; terra, sol, luna → codex) the
refusal carries its entry to paste, and `obor setup agents` adds the entry of each such name the levels or roles name
without one, writing nothing else. `obor init` writes the entries of each kind whose CLI is on PATH from its starter
list (`internal/setup/models.go`; it changes new projects only). `agents.difficulty` names all three levels (init writes them). A start is its
kind's base args (claude `--permission-mode auto`; codex `-s workspace-write -a never --no-daemon`; opencode
`--auto --standalone`; agy `--settings <the start's settings file>`), the model, then `agents.kinds.<kind>.args`; the
effort is the kind's own flag, refused when the kind does not take it.
`lanes.count` is a count: L-01 … L-NN, every lane pooled (it runs the model its dispatch picks); `lanes.list` names
the lanes instead (each `{name, keep_agent}`). There are no caps. A key obor does not know (`lane_types`, `lanes` as
an object of typed lanes) is dropped with a WARN by every command and serve (its setting does not apply);
`obor config validate` alone fails, naming the key: delete it from the file it names.

Per-kind defaults live in the plugin. A lead keeps the user's own settings untrimmed; every other agent's start
is trimmed (`agents.Start.TrimUser`: the kind's default below switches off the user's plugins and skills). Every start
carries the project's override files of its kind (`agents.Start.Overrides`, file name → document as written): files in
the CLI's own format in `.obor/<kind>/` at the project's root, each merged over the user's file of the same kind after
the default (for agy on every start, lead included; for Claude, Codex and OpenCode on a trimmed start only).

| File | Merged over |
|---|---|
| `.obor/claude/settings_override.json` | `~/.claude/settings.json` (`$CLAUDE_CONFIG_DIR`) |
| `.obor/claude/claude_override.json` | the `mcpServers` of `~/.claude.json` |
| `.obor/codex/config_override.toml` | `~/.codex/config.toml` (`$CODEX_HOME`) |
| `.obor/codex/hooks_override.json` | `~/.codex/hooks.json` |
| `.obor/opencode/opencode_override.json` | the user's `opencode.json[c]` (`$XDG_CONFIG_HOME/opencode`, `~/.config/opencode`) |
| `.obor/agy/settings_override.json` | `~/.gemini/antigravity-cli/settings.json` |

The merge is a JSON Merge Patch (RFC 7396; `agents.MergePatch`): objects merge key by key, `null` deletes a key,
anything else (a list too) replaces; TOML the same on its parsed tree (TOML has no `null`). A kind names its files
(`agents.Overridable`; a plugin kind in its manifest's `override_files`); obor core reads them at each start
(`config.Overrides`) and hands them to the kind, in the kind protocol's `start` request too. A file that does not
parse refuses the start, naming it. `obor config validate` fails on such a file and warns on a file of another name
in `.obor/<kind>/` and on the kind's own notes (`agents.OverrideChecker`). There are no per-role files.
Claude: `--setting-sources project,local --settings <file>`, the file `.obor/state/settings/<agent>.json`, written at
each start (a write that fails refuses the start, naming the file). It is built in this order: the user's
`settings.json` (`$CLAUDE_CONFIG_DIR` or `~/.claude`; none: empty); every plugin it enables set `false` (obor's own
stays); every entry the project's `.claude/settings.json` or `.claude/settings.local.json` sets dropped, object by
object (the file ranks above the project's settings, the user's own rank below them; a list both hold stays, Claude
adds lists up); `settings_override.json` merged in (what it sets stays, whatever the project's settings set: it is
the project's word for its agents); `{"autoMemoryEnabled":false,"remoteControlAtStartup":false}` merged in, without a
key the project's settings set (only the lead keeps Remote Control and auto memory). So env, hooks, the status line
and permissions apply, the user's plugins are off unless the project or the override enables one, and what is not in
`settings.json` does not load: the user's `CLAUDE.md`, rules, skills and the skills Claude syncs from claude.ai.
`claude_override.json`: only `mcpServers` has an effect (validate warns on another key). When it names `mcpServers`,
the start is `--mcp-config <file> --strict-mcp-config`, the file `.obor/state/mcp/<agent>.json`: for the lead and a
role agent, the user's servers (the top-level `mcpServers` of the user's `.claude.json`, then those under its `projects` entry of the project's
root) with the override's `mcpServers` merged over them, then the servers of the start dir's `.mcp.json` the
project's settings approve and do not reject (`enableAllProjectMcpServers`, or named in `enabledMcpjsonServers`; a
name `disabledMcpjsonServers` lists never joins), then obor's own; for a lane agent, those approved project servers
with the override's `mcpServers` merged over them, then obor's own (no user
server). The start is strict whatever its role, so the file's servers are all it loads (other plugins' MCP servers and claude.ai
connectors are hidden). Codex: `-c features.plugins=false -c features.apps=false -c skills.bundled.enabled=false` and
`-c skills.config=[{name="<skill>",enabled=false},…]` for each skill of the user's skills dir (`$CODEX_HOME` or
`~/.codex`, `skills/`: a dir holding a `SKILL.md`). `config_override.toml`, before those switches: `-c <key>=<value>`
per key it sets, the value as TOML and a table as dotted keys down to its leaves (`-c features.web_search=true`),
with an inline table only for the entries whose key needs quotes, passed first and not at all under a key the type's
args set (an inline table replaces what earlier `-c` options set under its key, a dotted key after it stays: so no
entry cancels a trim switch or an args key); the effort key gives way to the start's own effort; a trim key the file
sets is the file's alone. Codex lays each `-c` over the user's config key by key and has no option that deletes a
user key: an entry is switched off the CLI's own way (`enabled = false` under `[mcp_servers.<name>]`). After the
switches: `-c hooks.state={"<hooks.json path>:<event>:<group>:<index>"={enabled=false},…}` for each hook of the
user's `hooks.json` that the file merged with `hooks_override.json` lacks (a hook is the same when it is equal to one
of the same event; the key is the hook's position, read on every start; an inline table, since `-c` does not honor a
quoted part of a dotted key; never `features.hooks=false`, which would stop the project's hooks too). A hook the
override adds does not run (validate warns: it belongs in the project's own Codex hooks). A key the project's
`.codex/config.toml` sets is left to it: a trim key, a key of the override. OpenCode:
`OPENCODE_DISABLE_CLAUDE_CODE_SKILLS=1` hides `~/.claude/skills` and the project's `.claude/skills`. With
`opencode_override.json`, the start's generated config (`OPENCODE_CONFIG`, which OpenCode ranks above the user's
config and below the project's) is the user's `opencode.json` then `opencode.jsonc` with the override merged over
them, then obor's own entries (obor's plugin first in `plugins`, `mcp.servers.obor` obor's own); without the file it
is obor's entries alone. OpenCode still reads the user's own config below it, so removal is by OpenCode's own syntax:
`"plugins": ["-<id>"]`, and an MCP server with `"disabled": true` (the merge makes `{"disabled": true}` the user's
whole definition, which OpenCode needs). agy: the start's `--settings` file (`.obor/state/agy-settings-<lead|trimmed>-<dir
hash>.json`, one per kind of start and start dir), which agy lays over the user's saved settings by top-level key: each
key it names is the user's saved value with `settings_override.json` merged over it (whole on a trimmed start; without
its mode keys `toolPermission` and `agentMode` on a lead's start), then
obor's entries: `toolPermission: "always-proceed"` on a lane's start (a worker's or the drive agent's, unless the
override names that key: agy's allow rules cover no compound command's every part, so its dialog would ask for a lane's
checks; obor's hooks guard every call), `agentMode: "auto"` for every agent, a lead too (agy's sandbox runs its commands
with no prompt, and its reviewer approves the re-run of one the sandbox refused), `permissions.allow` gains
`mcp(obor/*)` and `command(obor)` (a
trimmed start also `read_file(<kit>/templates)`: obor's hook answers a read "ask", agy's own check then asks for a file
outside the workspace, and the session start names the role text there), and `trustedWorkspaces` gains the start dir
when it is a worktree obor made (a lane's or a role's: agy loads the workspace's `.agents/mcp_config.json` and hooks only
in a folder trusted when it starts, so a lane answered at the dialog would run without obor's server); a start in the
main checkout leaves trust to the owner's answer to agy's dialog. agy has no switch for
the user's own plugins, skills and hooks: they stay (MCP servers: below). The trim is best
effort, not a project-only view: Codex's `~/.agents/skills`, and OpenCode's user config, plugins,
`~/.config/opencode/skills` and `~/.agents/skills`, stay unless an override file switches them off. The project's own settings and skills stay for Claude and
Codex; OpenCode keeps the project's `.agents/skills` and `.opencode/skills`, not its `.claude/skills`. MCP servers of a lane's agent
(a worker, the owner's drive agent; owner 2026-11-22): the project's declared servers plus obor's, none of the user's,
each kind by its CLI's own switch. Claude starts strict: `--strict-mcp-config` with one `--mcp-config` holding the start
dir's `.mcp.json` servers the project's settings approve and do not reject (`enableAllProjectMcpServers`, or named in
`enabledMcpjsonServers`; a name `disabledMcpjsonServers` lists never joins), then obor's. Codex gets `-c mcp_servers.<name>.enabled=false` for each server of the user's
`config.toml` that the project's `.codex/config.toml` does not define (obor's and a name `config_override.toml` sets are
left alone; a name is matched as the config stores it, one with a space or a dot too). A name that needs quotes goes in
one inline table at `mcp_servers`, the override's quoted entries in the same one. A lane's Codex start whose args (the
type's or `agents.kinds.codex.args`) set a key under `mcp_servers` (`-c`/`--config`, dotted or the whole table; obor's own server included: the kit's
start passes it) is refused: "a lane's MCP servers come from the project's .codex/config.toml; move <key> there" (a role agent's or a
lead's start takes such args). Every Codex key is compared as TOML parses it, part by part: a quoted `"a.b"` is one
part, not the nested `a` → `b`, in the project's config, the override and the args alike. OpenCode's generated config makes `{"disabled": true}` each server of the
user's `opencode.json(c)` that the project's does not define (obor's and an override's entry are left alone). Claude,
Codex and OpenCode read the lane's own copy of the project file at its start; agy's rule reads the main
checkout's. agy has no switch, so the guard denies a lane
agent's `call_mcp_tool`, `list_resources` and `read_resource` on a server its project's `.agents/mcp_config.json` does not
list (a missing file: obor's alone). With `claude_override.json` naming `mcpServers`, a Claude lane's merge starts from the approved project servers
instead of the user's (above). The lead and the role agents keep every server.
Permission mode and posted lines: a Claude lane's job wakes and a Claude role agent's briefs are posted to its
session's inbox, not typed. A session in `--permission-mode bypassPermissions` holds a posted message for its 5-minute
dialog and drops it while serve records `sent` (phase-5 advice §8); the delivery timers re-wake a worker, a brief is
not retried. Claude starts run in `auto` (its base args); a `bypassPermissions` in `agents.kinds.claude.args` is at its own
risk.
Dispatch launches a fresh process per task.

**Identity (R-28):** the role (`OBOR_ROLE`, a lane's `OBOR_LANE`; a lane worktree cwd wins) and
the herdr **pane label** (the launch mark: the agent's name on the pane), both set by the one launch path (`obor
setup` / `obor restart`, which serve's start calls). An agent's **name is derived, never stored in env**: one function
(`project.Self` → `config.Self`: role + lane + the current config → the slot's name) answers "who am I" for the hooks,
the MCP server and every CLI call to serve; no `OBOR_AGENT` is exported, and one a shell still has is ignored (a value
obor derives needs no stored copy). The CLI finds an agent's pane by label (`launch.Launcher.PaneOf`); serve finds it by
proofs ("Finding the pane" below); `obor hook` under opencode has no env and derives the name from the cwd alone (see
"opencode lanes").
After a herdr restart serve restarts its project's labelled panes (with herdr's `resume_agents_on_restore = false`
they come back as shells; with it on, herdr resumes their agents bare, without obor's start, and serve treats such a
lost pane as a shell: `obor restart` for one it restores, herdr's agent stopped for one it skips; the line only
saves the double start)
(`launch.Restorer`): when its terminal connection comes back during its lifetime, and at its first connect
when the terminal restarted while serve was down — told by the adapter's instance (herdr: its socket file's mtime;
herdr exposes no instance id) against the instance serve last restored for (`.obor/state/terminal-instance`). A serve restart or re-exec with
the same herdr restores nothing. A role agent comes back only while serve's state has work open for it — a brief not
finished, the integrator's open job, the reviewer's open review or verify; the lead always, a worker by its lane. So a
role agent stopped on purpose stays stopped, and its next brief starts a fresh session. obor ships no herdr plugin. The
store's `session` facts record each agent's sessions
(`docs/state-model.md` row 51), for resuming after a kill.

## State (`.obor/state/obor.db`)
Task, lane and spec state is `obor serve`'s store, one SQLite database: the `events`, `lanes` and `specs` tables, serve their only
writer and every hook and CLI a read-only reader (the guard, whoami and the worker Stop hook read the `lanes` row and
the task's stored spec). What is recorded, in what
order, and what a restart does with it: `docs/state-model.md` ("The log"). `.obor/state/` also holds serve's own
bookkeeping (logs, backups, receipts). `obor pause [--for 3h] "<why>"` is a request to serve: fact `paused{why,
until}` (`docs/state-model.md` row 46); while it holds, a dispatch is refused with the reason and waiting tasks wait;
running gates, accepts and merges finish; `obor pause --clear` ends it; `obor status` shows it. `obor pause --model
<name> [--for 3h] "<why>"` pauses one model (fact `paused{model, …}`): dispatch skips it and a level's next
model is used; every model of a request paused, the task waits and its reply names each pause (`--model <name>
--clear` ends it).

## Roles
Resolution (`obor whoami`, `project.DetectRole`):
1. `OBOR_ROLE` if set (`lead | integrator | worker | reviewer | deep_dive | driver` or a custom role's name: every role
   has one, so the derived name, its session and inbox records and the caller identity cover it), set at pane creation with
   `herdr pane split --env OBOR_ROLE=… --env OBOR_LANE=…`.
2. Else cwd-derived: the toplevel is a linked worktree (`git-dir` ≠ `git-common-dir`) under `lanes.dir` → `worker`,
   `OBOR_LANE` = basename.
3. Else `none`.
**Worker restrictions apply if either env or cwd says worker** (most restrictive wins). **Universal guards don't
consult the role.** A `SessionStart` hook prints `obor whoami` (role, lane, task, the protected files the spec unlocks) into the
session context, plus `obor: role text: <kit>/templates/agents/<role>.md[, project: docs/agents/<role>.md] — read it
before acting` (lead: `run /obor:lead`).

**Custom roles** (owner 2026-11-04). The built-in table (`internal/roles`) keeps only the roles serve treats
specially: lead, integrator, worker, reviewer, deep_dive, driver. Any other role is a config row: `agents.roles.<name>`
is a model ref string (the role writes nothing) or `{"model": "<ref>", "write": ["<dir>/", "<file>"]}` (paths relative
to the root, a trailing `/` a directory), plus its doc `<agents.docs_dir>/<name>.md` (what it does, what it writes). The name
matches `[a-z][a-z0-9_]{0,23}`, is no table role's name or agent suffix (nor `none`, `leads`); the agent is
`<project.agent_prefix>-<name, _ → ->`, `OBOR_ROLE=<name>`. `obor config validate` refuses a bad name, a collision, a
write path outside the project or into `.obor/`, a missing doc (naming the file to write) and a `rules.may_push` that
lists a custom role; it warns of a role doc (`# Role:` first line) of a custom name whose row is missing (not a kit
default row's: `obor init` stubs those). A custom role works like the advisor:
main checkout, git reads only, never lane-bound or dispatchable, no push, no MCP tools; the lead places
it with `obor setup role <name>`, briefs it with `obor brief`, stops it with `obor restart --stop`. Its writes are
exactly its `write` list (the guard). `config.Role` is the one lookup of table and custom roles (identity, guard,
SessionStart role text, `obor status`, obor web's role list and Setup page); removing a row leaves a running agent of
it with no role agent name (no derived name, not listed), nothing else.
The kit ships three default rows: **advisor** (`write: ["docs/research/", "docs/plan/"]`), **researcher**
(`["docs/research/"]`) and **triage** (`["docs/BUGS.md"]`; `obor setup role triage` copies `templates/BUGS.md`). Each
counts only when the config names it: a string sets its model, an object replaces its `write`. A default row's doc is
the project's `docs/agents/<name>.md` if present, else the kit's `templates/agents/<name>.md`; the role-text line names
the kit's text and the project's doc as for a table role. Agent names stay
`<project.agent_prefix>-advisor|researcher|triage`.

**Role agents are stopped once done** (owner 2026-11-01: idle role agents hold memory). Nothing of a stopped
role agent is resumed: its next use starts fresh. A stop is `launch.Stop` (`obor restart <agent> --stop`): the agent
quits, its labelled pane stays at a shell, as for a worker after its merge. Serve stops the **integrator** once a job's
verdict is recorded and no other job is open or queued for it (each job starts a fresh session anyway: a stopped
integrator with no open job is the normal state, nothing is raised, `obor status` shows it `stopped`), and the
**reviewer** once its review cycle closes (the `RV-<n> closed` commit), never before: its verify runs in the same
session. The **advisor** and the **deep dive** are the lead's: once the advisor's answer needs no follow-up, or a deep
dive's verify approves, `obor restart <agent> --stop`; neither is kept for a later, unrelated question. A brief to a
stopped role agent but the integrator starts it fresh with the brief as its first prompt. State: `docs/state-model.md`
row 53.

## Setup (`obor init`, `obor setup`; Go `internal/setup`)
`obor init [--name] [--prefix] [--terminal herdr|hc|rex]` sets a project up with no questions and ends by starting
the lead. The terminal: an existing config's `project.terminal`; else the one the caller is typed in (`REX_BLOCK` → rex, `HC_TERMINAL_ID`
→ hc, `HERDR_PANE_ID` → herdr, both → herdr), else the one that answers (both → herdr, with a line); `--terminal`
overrides; the choice is written into the new config. On hc the core's env check is the first step (a leaked core:
FAIL before any write). Then the terminal reachable (else FAIL first), config with the detected commands, lanes for
the CLIs on PATH, the git hooks layer, the plugin (`claude plugin marketplace add <kit>` + `install obor@<kit
marketplace> --scope project`); in a directory with no commit, one "obor init" commit holding every file init
wrote or found at its own paths (any other file there: FAIL, no write), so the tree is clean after; then panes and
roles, `obor serve install`, a short summary (warnings, herdr's `resume_agents_on_restore = false` hint while its
config lacks it) and the lead's start: in the calling pane when it is the chosen terminal's, carries no
`OBOR_ROLE` and is alone in its tab, else a new `lead` tab; a lead already running is left as it is. `obor lead` with
no config runs `obor init`. The lead's first session asks one question (keep or change the models and lanes,
`agents.confirmed`). Every step is idempotent: a rerun is
the repair path. Placement: a workspace or tab is the project's when every pane in it with a
known cwd is in the project; none: init creates a workspace. Run init from a shell in the project's own workspace:
a workspace whose only pane is a shell `cd`'d into the project counts as the project's (accepted). `obor setup` keeps the single steps for changes later: `lane`, `pane`, `role`, `githooks`, `agents`, `mcp`, `show`, `rename`; each idempotent, ending in `OK …` / `FAIL …` (the
contract is the package doc). Role names, env values and cwd rules come from `internal/roles` (the reviewer, the deep
dive and the integrator run in a role worktree serve owns, `<lanes.dir>/<role>`; `obor setup role`, serve's starts and
`obor restart` add it when it is missing). Placement by kind (lead, lane, role): the lead's tab holds the lead's
pane alone (never split, `--tab` into it refused); with no `--tab`/`--new-tab`/`--pane` a lane's pane (a drive
agent's too) goes to the project's first tab whose panes are all lane panes and holds fewer than 4, else a new tab
`lanes`, and a role's pane likewise to a tab of role panes, else a new tab `roles`; a full tab, named or not, means a
new tab of the kind, never a refusal. A tab's kind is read from its panes' labels (else agent names); a tab holding
a pane that is no agent of the project (a shell, `obor web`) is no kind's. `obor init` places this way: its lanes in
a `lanes` tab, the integrator and the reviewer in a `roles` tab. `pane --tab`
splits the tab's largest pane (right when it is wider than tall on screen, else down), not the last one. `githooks`
points `core.hooksPath` at `<kit>/githooks`. Every herdr call
goes through `internal/herdr`, and `pane` / `role` start the agent through `launch.Launcher.Start`, the one
start `obor restart` uses too. That start is two parts. launch does the work any terminal needs: it waits for the
pane's shell, types the env and cwd line (its echoed mark read on the pane's screen), labels the pane and, after a
failed start, looks at the screen for a startup dialog. The terminal starts the agent (`port.AgentStarter`, asked of
its `PaneControl`; `AgentStart` carries the pane, the agent's name, kind, argv, the env and cwd, the first prompt and
the bound): it returns once the agent runs, or an error matching `PaneBusy` (launch waits for the shell and starts
again within the same bound), `NotReady`, `TimedOut`, `NameHeld` (a race lost: Locate's refusal) or `Accepted` (the
terminal took the start: a trust dialog then holds a fresh start's agent). herdr's start (`internal/herdr/start.go`)
is `agent.start`, a name herdr kept with no live agent behind it cleared and retried once, polled until a listing
read after herdr accepted the start is up and no longer `launch_pending`, or works on its first prompt.

## Lane status (`obor status [--json]`)
Reads the store's `lanes` table read-only (each lane's task and merge evidence) plus the worktree facts git answers
(`missing`, `free`, `dirty-free`, `working`, `done`; `checks.LaneTable`), joined to obor serve's answer (its tasks,
agents, needs-you, pause, the lines not typed yet) when serve is up. The row, how serve projects it from the log and
every reader: `docs/state-model.md` ("The log", the `lanes` bullet). Fields per lane (`type`: the model its open
dispatch runs, "" none): `lane, type, dir, branch, task,
state, done_age, dirty, warning, op, task_state, attempt, running, agent, agent_status`. Text output: serve's line with
the machine's web agent's line under it, one line per
lane, serve's tasks on no lane, needs-you with the commands that answer them, the full check, the pause.
A skew line names each agent whose calls come from another obor release than serve's, until it starts a new session
(serve has the caller's version from every request envelope; `wire.Status.Skew`). Agent CLI and terminal
versions are a rule: serve reads each kind's `<program> --version` (first line) and herdr's snapshot
version every ten minutes, records a change as fact `version`, and sends the lead one line per change ("watch its
next start"); it holds or refuses nothing.

## obor web's write path
`obor web` reads the read model like `obor status` and has two verbs, the Setup page's edits: `POST /api/config`
(`{changes: [{key, value}], base}`: a list of changes, each `agents.difficulty.<level>`, `agents.roles.<role>`,
`lanes.count` or `agents.kinds.<kind>.args`, and `base`, the `.obor/config.json` revision the page read, `config.Rev`: the
setup's `config_rev`) and `POST /api/pause` (set with a reason and an optional duration, or clear). Web writes nothing:
each is a request on the project's app socket (`config_set`, `pause`), so serve stays the one writer; serve down is
503. Every request's Host must be a loopback name (`localhost`, `127.0.0.1`, `[::1]`, any port; no DNS
rebinding of reads either), else 403. A POST is taken only on those two routes, only with the page's own Origin
and a JSON body (403 otherwise); every other route is GET/HEAD (405). Serve applies the changes in order to
`.obor/config.json`, each to the bytes the one before wrote (`config.Edit`), runs `config.Validate` once on the result (refused for a new FAIL), writes the file in its own layout and
commits that one file on main once (`config: <key> <old> → <new> (owner, web)`; several: the keys as the subject, one
`- <key> <old> → <new>` line each in the body), behind the publish admission (never while a publish moves main). All
or nothing: any change refused refuses them all and nothing is written. Refused: a config revision that is not the
base (another tab, the lead or a hand edit changed it since the page read it: the page never overwrites theirs),
uncommitted changes to the config (a hand edit is never committed under the owner's), a key `.obor/local.json`
overrides (named), a lower lanes count while a lane it drops has a task, a name the config lacks. Fact `config.set` records the changes (one record)
and the lead gets one line listing them (`[obor] config: <key> <old> → <new>; … (owner, web)`); serve reads the config
per call, so they count from the next dispatch or start. `agents.models`, a kind's `agents.kinds.<kind>.job_note` and
other keys stay `obor setup`'s or a hand edit's. The page is a Svelte app in `web/`, built by `make web` into
`internal/web/dist` and committed (the gate never builds it: `TestDistFresh` fails on a stale build); it stages the
edits and sends them as one POST.

## Hooks (`hooks/hooks.json`; one entry per event; the Go binary branches on role)
Each event's command is `OBOR_NO_SELF_HOST=1 bash "${CLAUDE_PLUGIN_ROOT}/bin/obor" hook <name>` (never
self-hosted into the cwd's checkout); `bin/obor` execs `obor hook <name>` on the first binary (dist/obor, then the
installed release; hooks never fetch) whose `obor has hook <name>` exits 0 within 2 s (a stale dist/obor breaks
every hook; a hanging one is killed with its process group). **No usable binary** (user decision: fail
visibly; the rule lives in `bin/obor`): in an obor project PreToolUse denies Bash / Edit / Write / MultiEdit /
NotebookEdit with `obor: no usable Go binary in <kit> — run: make -C <kit> build`, except exactly that command (or
`make build` with cwd = the kit); the other hooks allow with that line on stderr; outside an obor project every hook
allows silently.
1. **Stop — lead / integrator: never a block.** Nothing wakes the lead: the owner asks, and each needs-you reaches
   the owner as a herdr notification (serve's). The handoff reminder rides on each prompt (UserPromptSubmit, see
   "Wrong-tab guard").
2. **PreToolUse guards** (matcher `Bash|Edit|Write|MultiEdit|NotebookEdit`; deny = exit 2 + reason on stderr, or the
   `permissionDecision: deny` JSON):
   - universal (Bash): `git push` and pushing `gh` commands (`gh pr create`, `gh repo create … --push`, `gh repo sync
     <repo>`) — except for a role listed in `rules.may_push` that is not a worker (user 2026-10-10: a lead
     that cannot push cannot advance a milestone; entries are roles, `["lead"]`, a derived name `ob-lead` read as
     its slot; an accident guard, not a security boundary);
     `pkill`/`killall` by pattern (`pkill -f`, `pkill <name>`, `killall`); `git commit` with `-a`/`--all`/`-am` when cwd
     is the main checkout; an agent CLI run headless — `codex exec`, `claude -p`/`--print`, `opencode run`/`acp`/`serve`/`api`, `agy -p`/`--print`/`--prompt`/`acp` — for every
     role, the lead included (owner 2026-10-27: agents run in herdr panes); a command the project's
     `rules.deny_commands` matches (regex on the resolved command; `alone`: also two matching at once via `&`,
     `xargs -P`, `parallel`, background units) — for every role (owner 2026-11-05; obor-kit's config carries
     the Go load-run rule: `go test -count>1`/`-race`, `*.test -test.count>1`/`-test.race`, two test commands at once).
   - worker (Bash): git writes (`commit`, `merge`, `rebase`, `reset --hard`, `checkout`/`switch` to another branch,
     `stash`, `push`); redirections / `sed -i` / `mv` / `cp` / `tee` whose target is a protected file.
   - reviewer, deep dive (Bash): `checkout`/`switch` to a commit only in their own role worktree; no branch, no
     path checkout, no other git write, nothing in another worktree.
   - worker (Edit/Write/MultiEdit/NotebookEdit): target resolved relative to the lane toplevel. Inside the lane ->
     allow, any file (owner 2026-10-28: no "Files you may touch"; placement is checked at accept, only contracts
     are locked), unless a final symlink resolves out of it. Inside another worktree of this repo (main checkout, other
     lanes) or another obor project (a directory with `.obor/config.json`) -> deny. Elsewhere (temp dirs) -> allow.
   - every non-lead role: `rules.protected_files` → deny. Exception (user 2026-10-14): a worker in its own lane may
     write a protected file that its task's stored spec names as an exact path (no glob) under "Protected files this
     task may edit" — the spec is the lead's authorization for that task. The guard reads it from the store's `lanes`
     and `specs` tables (read-only, the current rev; `docs/state-model.md` "The log"); a store it cannot read unlocks
     nothing.
3. **Stop - worker: `task_done` required.** The hook reads the lane's row read-only: while the lane's task awaits its
   `task_done` it blocks once per stop loop with the hint (call `task_done` with the report, the sections being the four
   built-in ones — Summary, Self-check, Skipped, Open questions; or, to ask, a `Blocked:` report naming what you need:
   `docs/state-model.md` row 16a). No done file, no block count.
   The row and the finish: `docs/state-model.md` ("The log", the `lanes` bullet, and its last paragraph, the worker
   Stop hook).
4. **SessionStart** (`guard.SessionStart`): prints the `obor: role …, lane …, task …` line, the protected files the
   stored spec unlocks (or that the lane has no row), the hooks this role runs under and the role-text line. It
   tells serve the agent's session (the store records it; serve down: nothing) and, for an opencode lane, the
   plugin marker. The agent is the derived name, so a restored pane has no "lost identity"; the renames herdr
   needs are serve's.
5. **StopFailure** (`obor hook stop-failure`, Claude Code fires it instead of Stop when a turn ends on an API
   error: `error` `rate_limit`, `overloaded`, `billing_error`, … `unknown`, and `error_details`): appends the raw
   `error`/`error_details` with a timestamp to `.obor/state/stop-failure.log` and sends serve one `stopped` request
   (the agent, its role and lane, the error and details); serve down: exit 0, nothing else. Never a block. Serve's
   fact and lines: `docs/state-model.md` row 50 (a role agent's `[obor] <agent> stopped: <error> (<details>)` instead
   of its brief's finished line; a worker's needs-you, carried in the `stopped` fact itself). A worker's stop
   ends on its next turn: its `UserPromptSubmit` and `Stop` hooks send serve one `turn` request (serve down:
   nothing, the stop ends on a later turn).

## launchd (darwin)
Two LaunchAgents per user, both in `~/Library/LaunchAgents`, both `RunAtLoad` + `KeepAlive`, each with the caller's
PATH and the terminal's own variables as the installing caller had them (`internal/service`):
- **`obor.serve.<name>`**, one per project: `<kit>/bin/obor serve` in the project's main checkout, its output in
  `.obor/state/logs/serve.log`. `obor serve install` writes and bootstraps it (`obor init` runs that), `obor serve
  uninstall` boots it out and removes it, `obor serve restart` kickstarts it.
- **`obor.web`**, one per machine (the page covers every project): `<kit>/bin/obor web` on its default listen
  `127.0.0.1:8787`, its working directory the user's home, its output in `~/.obor/web.log` (a machine path, not a
  project's). `obor serve install` installs it or refreshes a stale plist; a current plist whose label is loaded is
  left alone. A serve launchd starts (`obor serve`) installs it at its start when its plist is missing, so an
  upgraded kit brings it with serve's next start and no extra step; serve never restarts or replaces a loaded
  `obor.web`. `obor serve uninstall` removes it only when no other project's `obor.serve.*` plist is left.
- A web agent whose port is held (a hand-started `obor web`) fails to bind, writes one line naming the port and the
  error and exits non-zero; launchd's throttle starts it again. It hunts no free port.
- `obor status` shows it under serve's: `web: launchd pid <n> · http://127.0.0.1:8787` when launchd runs it, `web:
  not running (obor serve install)` when its plist is missing or launchd does not have it loaded; no line on a host
  without launchd.

## Serve's work: merge and dispatch
Serve dispatches waiting tasks (`obor dispatch … --after`), gates, merges, runs the nightly full check
(`nightly.at`) and notifies each needs-you through herdr; `obor status` shows needs-you, serve up/down, the last
full check and the pause.
- **merge** (Go `internal/checks`, run by obor serve's gate; `obor nightly` and the after-merge commands share its runner):
  scratch worktree detached at the target; `--no-ff --no-commit`; `lanes.links` symlinked into the scratch;
  `lane_setup`, `check` each under its timeout. The outcome is one
  line: `MERGED … | CONFLICT in <scratch>: … | RED … | HOLD … | FAIL …`. Counts (`checks.output.summary`, each alternative
  summed over a chained command's parts) and seconds go in the MERGED line; a RED names the `checks.output.failure` matches and
  the log. Commit messages carry the worker label. Every command's full output is kept in
  `.obor/state/logs/<task|nightly|milestone>-<name>.log`, so a RED can be triaged without a re-run.
- **dispatch** (obor serve, `internal/app`): branch from the root's current branch; refuse unless the lane is `free`
  (or `dirty-free` with `--force`) and its agent is not working/blocked; `checkout -B`; the `lanes` row records the
  dispatch; **launch** a fresh worker (`Agents.Launch(Launch{Fresh: true})` with env, label and the dispatch's
  model's args; the one start below) whose
  first prompt is the work job's wake, `[obor job <id>] call the obor job_next tool now (connected; it returns your job)`. The worker's `job_next` is the
  delivery. No screen reading (R-28 §4). An undelivered wake is retried after 5 minutes, then becomes needs-you.
- **sendback** (`lane.sync`, `internal/checks/lane.go`): a fix attempt (after a red gate, the integrator's sendback or
  the lead's `obor resume --sendback`) first merges the target into the lane's branch, in the lane, at the target sha
  read then; the fix job goes out after it and says which: `merged` (serve committed `Merge <target> into task/T-xx
  (obor: fix attempt <n>)`, its sha), `current` (the branch holds the target already), `conflict` (the merge aborted,
  the lane as it was, the files named; the worker fixes the branch as it is and the integrator resolves
  the conflict at the gate) or `dirty` (uncommitted changes, nothing touched).
  obor's links come out of the merge's way and are put back once it is committed or aborted. A first dispatch, a
  drive and a move run no sync. Rows 24a and 24 of `docs/state-model.md`.
- **drive** (owner 2026-11-03): the owner iterates by hand on a task's branch in a plain interactive agent (role
  `driver`: a lane's write limits and push denial, commits on its branch, no job pull, Stop-hook demand or wrong-tab
  guard), then lands it through the same gate, integrator and merge. A drive is a task on a lane, so the dispatch's
  machinery does the work. `obor drive <name>` is a new task (spec `Owner drive: <name>`) dispatched as a drive onto
  the first free lane (owner 2026-11-05: a drive is for new work; no take-over of a worker's task). After lane.prepare the task is **driving**: serve sends it no
  jobs, no watchdog watches it (no idle or stall needs-you), a merge watch has no line for it; the CLI starts the drive
  agent in a pane placed as a lane's (a lanes tab). `obor land` (a clean worktree; it commits nothing) records the submission
  on the owner's behalf, the summary as its report; from there it is an ordinary submitted task, and after the merge
  agent.stop stops the drive agent. A red gate, the integrator's sendback or hold go back to driving with one line to
  the lead, never to a worker; `obor drop` works on it unchanged. The drive and the lead message each other:
  the drive agent's `obor tell-lead "<text>"` (role driver only; its lane from its cwd) is fact message{driver} to the
  lead, the line `[obor T-xx drive] <text> (reply: obor resume T-xx "…")`, delivered as every lead line; the lead's
  `obor resume T-xx "<text>"` on a driving task needs no question in force: fact answered{driver} to the drive agent,
  the line `[obor answer] <text>` delivered as an answer to a worker is (when its pane is idle). Each such answer is its
  own line (a newer one ends no older); a land or a drop closes an undelivered one unsent. The drive agent's first
  prompt names both.
- **Role worktrees** (owner 2026-11-05): the reviewer and the deep dive run in their
  own worktree `<lanes.dir>/<role>`, detached at the head serve marks reviewed (`refs/obor/reviewed`); serve moves it
  there at a review's start, at each verify and at a brief to an idle deep dive, so it is never stale, and merges go on
  while they work (no merge hold). They may check out any commit there; their review files are written to the main checkout's
  `docs/reviews/`. The integrator has its own worktree `<lanes.dir>/integrator` for its review jobs; its resolve jobs
  work in the scratch (`<lanes.dir>/merge`). Only the lead works in the main checkout among the merge-loop roles.
  A review's close commit names its reviewed sha, its covered tasks (`Covered:`) and its start sha (`Start:`);
  the scope, `review due` and the release check read those. A fix task's spec line `Fixes: RV-<n>` (rule
  tier) links it to that review: the link is read from the dispatched tasks' stored
  specs, not recorded. The verify is a rule: while the review is open, its linked tasks that were not dropped have all
  merged, a finished verify does not cover them, and the reviewer has no open brief and is idle, serve moves the
  reviewer's worktree to the integration head and briefs the verify in the same session. A verify that fails (the
  reviewer stopped, a newer brief replaced it, its git failed) leaves the condition true and the next pass briefs again
  (one line to the lead per failure). Only the reviewer's finished line ends a verify and covers its tasks. `obor
  review close` is refused while a linked task is open or merged and not covered (`--force`: the owner's word). A spec line `Review: on merge` (rule tier), or the spec's `Difficulty: hard`, makes serve
  record a review request once that task merged (never for a spec with `Fixes:`: its verify is its review) and no request is open or review started since; it waits for an open cycle's
  close like any request.

## Machine load (v2.1, 2026-10-08 — requests from the axis-alies lead)
Many lanes on one machine (load 20–50 on 10 cores; obor-kit and axis-alies share it) turn timing-based tests RED.
- **Merge window:** serve's gate runs the checks (`docs/state-model.md`, rows 18–33). Worker prompts/skills say: focused
  tests while iterating; before `task_done` the tests the project's worker doc names (obor-kit: its packages only,
  2026-11-05); the gate runs the class check. A moved target is merged and compiled again in the gate, never
  tested again: the accept binds to the task's own patch, which a move keeps. A project without `merge.compile` runs
  `merge.check` on the moved merge instead, as a lane test runs it (the lane env, one retest of a red's failing tests):
  a moved candidate never lands with nothing run.
- **Watchdogs:** a stalled or blocked worker and a pane that exits are serve's facts (`needs-you`), recorded and
  notified by the merge loop (`docs/state-model.md` "Timers").
- **Dispatch:** refuses a lane with uncommitted leftovers (`dirty-free` → FAIL unless `--force`).

## Wrong-tab guard (2026-10-08 — user request via the axis-alies lead)
When the user types into a worker's pane by mistake, the worker must not act on it: it first replies
`I am worker <agent> on <task>. Did you mean to type here?` and continues only on confirmation.
- Every line obor delivers (typed, or posted to a Claude session's inbox) starts with `[obor `: a job's wake
  `[obor job <id>]` (the only thing sent to a worker), and serve's lines ("Lines to the lead"). Nobody messages a worker; its question is answered by `obor resume`.
- `obor hook user-prompt-submit` (UserPromptSubmit): role worker only; prompt starts with `[obor ` or `/` (slash
  command), or is one of Claude Code's machine envelopes (opening tag at the start, closing tag at the very end: e.g.
  `<task-notification>`, and `<agent-message from=…>` (a subagent's hand-back) and `<cross-session-message
  from=…>` (another Claude session's SendMessage); Claude Code 2.1.312 gives the hook no origin field for these, only
  the prompt; the captured payloads in `internal/guard/testdata` are the contract, captured again when the pinned
  Claude Code version changes) → nothing. A raw post to the inbox socket arrives bare: obor's pass by their `[obor `
  tag; an untagged one cannot be told from typing and is guarded. Otherwise inject context instructing the agent to reply exactly with the question above (agent
  name from the store's `lanes` row or `OBOR_LANE`, task from that row) and not to run any tool until the user
  confirms in the next message. Accident guard, not a security boundary (the tag is not secret).
- Handoff reminder: the same hook, for the lead whose session context is above
  `agents.handoff_tokens` (`0` = off), adds a reminder to every prompt: hand off at the next logical break, not
  mid-work. It is a reminder, not a block: the agent picks the break, writes the Handoff section of `BACKLOG.md` and
  commits it (the lead; the integrator hands off after the merge in hand, with a plain `obor restart <you> --fresh`),
  then runs the handoff restart `obor restart <you> --handoff`, which from its own pane is scheduled: a detached run
  waits until the agent is idle and restarts it fresh. That restart's first prompt is the role's handoff form
  (`/obor:lead handoff`): the session runs its start checks and continues from the Handoff's next steps. `--handoff`
  implies `--fresh`, and only the lead takes it: a non-lead agent refuses it in one line. A session the owner starts
  (`obor lead`, a `/clear`, a typed `/obor:lead`) gets the role's plain prompt and presents the state and the options
  instead, and starts no work until the owner picks one.
- Worker skill/template/prompt: the same rule in words, as a fallback for non-Claude lanes (Codex has no hook).

## Git hooks layer (v2.3)
A second layer that doesn't depend on agent hooks (also covers Codex/opencode lanes and humans).
- Shipped in the kit as `githooks/` (`pre-commit`, `pre-push`, the runner `_hook`) and `githooks/chain/` (a file per
  client-side hook git runs, each sourcing `../_hook`). `obor setup githooks` (and `obor init`) sets `core.hooksPath`
  in the project's repo config (shared by all worktrees) to `chain/` only when there is something to chain (a recorded
  `obor.hooksChain`, or an executable `.git/hooks/<name>` other than those two), else to `githooks/`: a commit then
  runs two cheap hooks, not twelve. A project that already has its own `core.hooksPath` keeps
  its hooks: setup records it as `obor.hooksChain` (git config) and `--remove` puts it back.
- Every hook first runs the project's own `<name>`: `<obor.hooksChain>/<name>` when a chain is recorded (nothing when
  it lacks that hook), else `.git/hooks/<name>`; args and stdin pass through; its failure is the hook's result where
  git uses one (not for the post-* hooks). Then obor's rule, for `pre-commit` and `pre-push` only. Without
  `.obor/config.json` a hook only chains.
- serve's own commits (submit, the gate's merge commit, the amend) pass `--no-verify`: the project's checking step is
  `merge.check`, not its commit hooks; git still runs `prepare-commit-msg`.
- `pre-push`: a shell with no `OBOR_ROLE` (a person) pushes; the block is for agents (every supported agent
  start sets `OBOR_ROLE`). A caller with a role asks the binary (`obor whoami --can-push`, hidden, exit 0/1:
  `config.MayPush`, a role that is not lane-bound and is in `rules.may_push`, the rule the agent guard's
  `checkPush` uses). For a role it fails closed: no binary, a stale one or a hang past the watchdog refuses,
  naming `rules.may_push`.
- `pre-commit`: refuses in a lane worktree unless `OBOR_MERGE=1` (set by serve's gate for its
  commands and scratch commits); in the main checkout git can't tell a `commit -a` from a normal commit, so nothing is
  checked there (the agent guard covers `commit -a`).
- obor has no rule of its own in `post-merge` / `post-commit` (serve dispatches the waiting tasks, `--after`, and runs
  `merge.after` in the main checkout after a merge); a project's own still run through the chain.
- Self-restart (a graceful exit): `after_merge` restarts nothing itself, a build in `after_merge` reaches serve through binary following: serve ends itself the way a signal does — it cancels its context, running gates end their process groups as at any
  shutdown, and it exits 0 — and launchd's `KeepAlive` starts the new build, which replays a done step (no second merge
  or command; work in flight folds as at any restart). No `launchctl kickstart -k` on itself: that signal is not known
  to be a TERM, and a SIGKILL would orphan a gate's process groups; `obor serve restart` keeps kickstart for a restart
  by hand. launchd's serve exits for launchd to start it again; any other serve (in a pane, on Linux, launchd's agent
  not installed) execs the kit's shim itself once it has ended (below). Calls during a restart: serve
  binds its socket only once its log is replayed (it logs `replayed N records in X s`), so no call is
  admitted to wait; a caller retries a refused/missing socket or a lost reply every 500 ms with the same call id until
  its own deadline (`wire.Verdict`'s table: a CLI verdict or MCP tool 15 s, a hook's turn/SessionStart 8 s,
  stop-failure 5 s; the last try starts 200 ms before it): a restart that ends before that last try lands the call
  once, a longer one fails it at its deadline with nothing left waiting to be applied. A call already admitted (a
  verdict waiting for the scratch or the verdict lock) runs under serve's 60 s context, not the caller's: its timeout
  is an ambiguous outcome; check `obor status` before retrying by hand (accepted). `obor status` does not retry (`serve is not
  running` during a restart). 
- Following a new obor binary (`internal/selfexec`): serve and `obor web` each watch the binary
  the kit's shim would run now (`<kit>/dist/obor` when executable, else `~/.obor/bin/obor-<VERSION>-<os>-<arch>`; kit
  root `project.KitRoot`). It counts as new when it is another file (dev/inode) than the running one, executable, at
  least 5 s old, and its `version` exits 0 within 2 s; a process not started on the kit's candidate (a test binary, a
  lane's own build) never follows, and after the exec the running binary is the candidate, so there is no loop. A
  rollback counts as much as an upgrade (no version order); a kit checked out at a new tag before its binary is fetched
  names a missing file: no trigger until it is there. Serve asks on its own 30 s timer, herdr connected or not (the
  watchdogs' tick counts only while it is), and takes the self-restart above: at
  once when idle (no effect running, no nightly, no verdict call admitted), else at the first pass after the work
  ends, and at the latest 30 minutes after the binary was seen (`followWait`: the work still running is cut and
  replays); work of a task waiting on a person holds nothing back, and a pending restart holds nothing back; never while a main-checkout writer (`config_set`, `review close`) is admitted, and none is admitted
  once the restart is decided; serve logs the candidate binary (path, version) once, and what holds a pending
  restart at each change; `obor status` shows `restarting serve` meanwhile with the same holds, and a failed restart is a needs-you
  `serve restart on the new obor binary failed: <err>` (key `serve`), not tried again by that serve. A serve launchd
  does not run execs `<kit>/bin/obor serve` after it has ended: same pid, same pane; Go's files are close-on-exec, so
  the flock goes with the exec, and the new serve takes it and removes a leftover socket. Web asks every 30 s: a web
  launchd runs exits 0 for launchd to start the new build, as serve does there; any other web execs `obor web --listen
  <the address it bound>`, so its URL stays and the page's long poll reconnects. `scripts/ship` restarts nothing: its last line says they follow within a minute. A
  candidate speaking another herdr protocol restarts into today's warning.

## Machine-wide check slots (v2.4, 2026-10-08 — axis-alies lead + user; protocol from axis-alies)
Both workspaces share one machine, so heavy test runs take a slot from one **machine-wide** slot dir shared with
axis-alies: `checks.slot_dir` (kit default `/tmp/obor-check-slots`; env `CHECK_SLOT_DIR` overrides; machines shared with
axis-alies set `/tmp/axis-check-slots` in `.obor/local.json`), `checks.heavy_at_once` (default 2,
env `CHECK_SLOTS`). `obor run <heavy command>` acquires a slot before running (waits with a line every 30 s; stale
slots with a dead PID are reclaimed; released on exit incl. signals) and also waits while the `merge-running` marker
exists; serve's gate runs its checks with `CHECK_PRIORITY=merge` (reserved slot, never waits on worker slots,
creates/removes the `merge-running` marker). The slot file format must match axis-alies `scripts/checkSlots.ts`
(merged 2026-10-08) exactly: files `slot-0`…`slot-<N-1>` and `merge-running` in the dir, created exclusively
(O_EXCL — bash `set -C`), content `<pid> <epoch ms>\n`, a file whose PID is dead is reclaimed once (unlink + retry),
release only if the file still holds our PID. Workers don't start while a live `merge-running` exists; a merge claims
`merge-running` (waits only for another live merge marker, never on worker slots).
**Stale reclaim (agreed with axis-alies 2026-10-08):** reclaiming a slot or marker whose holder PID is dead
is serialised per path by an atomic `mkdir <path>.reclaim` guard: inside the guard re-read the holder, unlink only if
it is still the same dead PID, create the file, then `rmdir` the guard. A guard older than 10 s is stale and may be
removed. Normal acquisition (no stale file) is unchanged. axis-alies ports the identical rule to checkSlots.ts.
**Per-project merge marker + block cap (agreed with axis-alies lead-mp 2026-10-08):** the marker is per project, `merge-running-<project>` (`<project>` = the project's name: `project.name`, else the main checkout's base name; env `CHECK_PROJECT` overrides;
content unchanged). A worker check waits only for its **own** project's live marker; **every** live marker of any
project counts as one occupied slot toward `checks.heavy_at_once` for everyone (load stays machine-wide). A merge waits only for
its own project's live marker. Cap: a marker older than 900 s (its epoch ms field)
stops blocking its project's workers (it still counts as a slot). A legacy `merge-running` (no suffix) is read as a
marker of an unknown project. axis-alies has the same rule in checkSlots.ts with project `axis-alies`.
**Slot handed to the command (2026-10-10 lead-mp report):** `obor run` for a heavy command exports
`CHECK_SLOT_HELD=<the slot it claimed>` (and keeps `CHECK_PRIORITY`) to the command, so a slot-aware runner inside
(checkSlots.ts) reuses that slot instead of claiming a second one, and the check's timeout never includes a slot wait.
obor-kit uses its own slot dir.

## Skills: only the lead's is model-invoked (2026-10-13)
`lead` is the one self-chosen skill: a lead must find its way back after `/clear` and a handoff. Workers, the
integrator and reviewers are always started by obor, so their text comes from the kit (`templates/agents/<role>.md`)
through the dispatch prompt and the SessionStart role-text line — one source per role, and Codex lanes (which read no
Claude skills) get the same text. Rarely needed lead procedures are side files of the lead skill. `setup`
carries `disable-model-invocation: true`: only a person starts it (`/obor:setup`); a lead uses the CLI
(`obor setup …`) instead. A role runs only under obor (`obor setup role <name>`, or `obor restart <agent>`, then
`obor brief`): no skill puts a running session into one. User decision 2026-10-13.

## Change-based merge checks (v2.5, 2026-10-08 — user + axis-alies)
A UI-only task shouldn't fail its merge on an engine/AI soak test. Skipping is allowed only when a project guard
proves the skipped code can't depend on the changed code (e.g. an import-direction test: engine/AI never import UI).
- Config `merge.by_path.rules`: ordered list `[{ "name": "ui", "paths": ["src/ui/**", "e2e/**", "*.md"], "run":
  ["check_ui", "e2e_touched"] }, { "name": "core", "paths": ["src/engine/**", "src/ai/**"], "full": true }]`;
  `merge.by_path.guard`: a command name (e.g. `check_imports`). Each changed file of the candidate (the slot's merge of the task
  onto the target: `git diff --no-renames --name-only <target>..<candidate>`, listed by the lane's test) gets the first class whose glob matches. **Neutral** files (`merge.by_path.no_check` globs, default `["docs/**"]` (`*.md` would match nested product files; list root .md files explicitly);
  2026-10-09 user) never decide the class: they only need the guard. **Partial** = every non-neutral file matches a class
  that has `run` and is not `full`, whether one class or several (all files neutral → partial `neutral`, guard
  only) → run `guard` once, then the `run` of the distinct matched classes in config order, a command two classes
  name once (each a `commands.<name>`, with `OBOR_CHANGED_FILES` = path to a file listing the changed paths);
  Otherwise (any unmatched file, a matched `full` class or one without `run`, no
  `merge.by_path.guard` configured, or `OBOR_FULL=1`) → the full path: `check`, then the run of each matched class marked
  `also_on_full`. The result says `partial:<class>` or `full:<reason>` (the file that decided
  it). A failing guard → RED (never falls back). These checks run in the task's lane (see "Where tests run").
- `obor nightly`: `check` + `check_milestone`, each skipped when unset, in its own nightly worktree
  (`<lanes.dir>/merge-nightly`, not the merge scratch) at `merge.target_branch`'s head (whatever the main checkout has
  checked out), under the merge window; its scrub between the gating checks and the rest resets the worktree to that
  head and links the lane links again (the reset's `clean -fdq` takes them), so `nightly.also_run` runs with them; it then
  backs the store up, asks serve to prune the branches of tasks merged over 7 days ago (a non-gating line,
  `prune: N branches (oldest merge <date>)`; never `--unmerged`, never worktrees) and runs `nightly.also_run` in order, after the gates (each a `commands.<name>` that is no
  gating command; non-gating, one report line — its last output line — never RED, a nonzero exit a `WARN` line,
  and every `WARN`/`SKIP`/`FAIL` line it printed below it); writes `state/last-full-check`
  (`<sha> <epoch> GREEN|RED <summary>`); prints `NIGHTLY GREEN …` / `NIGHTLY RED …` last. `nightly.at` is its
  switch: set (`"HH:MM"`), serve runs it every night at that time, whatever merged since (it runs more than
  the merge gate); unset, never. The lead or the owner switches it with `obor nightly on HH:MM` / `obor nightly off`
  (serve's `config_set`: one commit on main; counts from serve's next pass, no restart; a run in progress is not
  stopped). Serve records the outcome (`docs/state-model.md` row 45); `obor status` prints the next run (`nightly
  check at HH:MM (in …)`, or `nightly: off (obor nightly on HH:MM)`) and the last full check and its age.

## Pruning task branches and worktrees (`obor prune [--dry-run] [--unmerged]`)
A call to serve, which owns the fold and the main repo's git writes. A branch goes only when serve's fold records its
task as merged (or landed outside the app), never by ancestry: the gate merges a candidate commit, so a merged task's
branch is not an ancestor of the target. `--unmerged` adds the branches of tasks dropped over 14 days ago. Never:
a branch outside `task/`, a task still open or waiting, a task the fold does not know, a branch checked out in any
worktree. A worktree goes only when it sits directly under `lanes.dir`, is a lane beyond `lanes.count` or the worktree
of a role not in `agents.roles`, holds no task and is clean (dirty, or holding commits no branch or tag has: listed and
kept); a registered worktree whose dir is gone is `git worktree prune`'s. Every deletion prints and logs its tip sha
(`deleted task/T-982 (was 1a2b3c4)`; `git branch task/T-982 1a2b3c4` brings it back while git has the commit); a run
that deleted something records one fact `pruned` listing them. `--dry-run` prints the same list and deletes nothing.

## Check tiers (v2.6, 2026-10-08 — user decision via axis-alies)
Long property gates and soak runs stay out of per-task merge checks: fixing what they find later is cheaper than
waiting on every merge.
- `merge.check` is the merge check: what every merge must pass, kept to a few minutes at a lane's CPU budget
  (an adopter without classes runs it in every lane; what is slower goes in `check_milestone`). A project with
  classes (obor-kit) runs it only on a full lane and at night; a class's `run` commands (v2.5) are its per-merge tier.
  `nightly.slow_check` is the long tier (property gates, soak; obor-kit: `make vulncheck`); a slow test that must run
  per merge is a class's `run` command.
- A task's lane test runs only per-merge commands, as "Change-based merge checks" lists them. `check_milestone` never
  runs there.
- `obor nightly` runs the milestone tier, as "Change-based merge checks" lists it; **non-blocking** (reports, never
  holds merges; serve's nightly run records `needs-you{full-check RED <sha7>}`).

## Where tests run: the lane, the slot, the nightly (v0.31, 2026-11-08)
A task's tests run in its own lane, not in serve's merge slot; nothing is reused from an earlier run,
and tests have one split, not tiers.
- **The slot compiles.** Serve's merge slot merges the task onto the target in its scratch worktree and runs
  `merge.compile` only (obor-kit: `make check-compile`: build, gofmt, vet, deadcode; no `go test`), after
  `lane_setup`. A red compile holds the merge; no check of the classes runs there. Without `merge.compile` a lane
  input runs nothing in the slot, and a moved input (its target moved after its lane test) runs `merge.check` there
  with the lane env and the lane test's retest: red holds the merge. `obor init` suggests `merge.compile` from a
  `typecheck` script, never a `build` (a script name is no proof the command type-checks what the check covers);
  `obor init` warns when it is empty and `merge.target_branch` is the repository's default branch.
- **The lane tests.** Serve then checks the candidate out, detached, in the task's lane worktree (the lane must be
  clean; its branch is checked out again afterwards, what the checks changed discarded). It lists the changed files
  (`<target>..<candidate>`) into `.obor/state/lane-changed-files-<lane>` as `OBOR_CHANGED_FILES`, classifies them (v2.5),
  runs `lane_setup`, then the checks "Change-based merge checks" names for the result. `lane_setup` runs first and
  holds no slot: the sequence's first check command acquires a worker-priority pool slot (never the merge marker),
  kept through the guard, the class checks and the retest and released after them, so `checks.heavy_at_once` bounds how many lanes
  are in their check sequence at once, not setup.
  Compile and the nightly run at merge priority (they claim the merge marker); each keeps its slot from its first check
  to its explicit release, its setup exempt.
  A red that names its failing tests and packages is rerun once at the same commit by `merge.retest`
  (`{tests}` joined by `merge.retest.join`, default `|`; the packages in `OBOR_FAILED_PKGS`); green then is the
  step's green and the flake is logged as retried. The nightly's `check` and `check_milestone` retest the same way:
  a retest that saves a red leaves the nightly GREEN, its line naming the rerun. The names come from `checks.output.failure` lines read by
  `checks.output.failed_test`/`failed_package` (unset: no flake parsing).
- **Lane env.** Every process in a lane gets `OBOR_LANE_CPUS` (`lanes.cpus`, default 2) and the project's
  `lanes.env` (values with `{cpus}` replaced by that budget). The lane test also exports
  `CHECK_CPUS` = the lane's budget. obor-kit's `lanes.env` sets `GOMAXPROCS` and `GOFLAGS=-p={cpus} -trimpath`.
- **`obor run <heavy command>`** takes a check slot, exports `CHECK_SLOT_HELD`, `CHECK_CPUS` (`checks.cpus_per_check`, else
  `max(1, cores / checks.heavy_at_once)`) and, in a lane, `OBOR_CHANGED_FILES` (state file `.obor/state/run-changed-<lane>`): the
  lane's files against its merge base with `merge.target_branch`, committed and uncommitted (untracked too), the run marker not,
  computed from the lane's content tree built with a temporary index (`GIT_INDEX_FILE`, `add -A`): no write to the
  lane's index or history. It writes no receipt.
- **A lane tests with importers**: the lane's class check covers the changed packages and the packages that import
  them; the slot's compile catches an importer's build break.
- **The nightly** runs what "Change-based merge checks" lists, on the target's head: it covers what a moved candidate
  publishes untested when `compile` alone ran (owner 11-09: `main` moves at release); without `compile` the moved
  candidate runs `merge.check` in the slot.
- **A worker runs unit tests; the lane test and the nightly run the rest** (owner 2026-11-08).
  A worker's `go test ./<pkg>/...` is plain: it runs the unit tests of its package. A test that needs real git,
  a real socket or a built binary sits in a file with `//go:build integration`; every check that gates or measures
  (the lane test, the nightly, `vet`, `deadcode`, the budget) passes `-tags integration`. The live
  conformance tier (`make conformance TIER=live`, one throwaway session per kind) is run by hand; the nightly runs
  the static tier, the budget (`make check-budget`, tests/budget: the size ratchet on wall time, git processes
  and slow tests) and one race-detector run (`make check-race`: the data race count, non-gating) from
  `nightly.also_run`: nothing skips a test in a lane.
- **The generic mechanism, and obor-kit's mapping.** A class's `run` names commands; a Go project narrows its test
  to the affected packages. obor-kit's `check_affected` is `make check-affected`, which runs the kit tool
  `tools/affected` (`go run ./tools/affected`) and then `go test`
  on that list with `$CHECK_CPUS` as `-p`. The tool reads `$OBOR_CHANGED_FILES` and prints the affected package import
  paths one per line (a changed file maps to the package of its nearest directory that holds one, and with the
  packages that import it, except a `_test.go` file, which selects its own package only), none when no package is affected, or one
  `full: <why>` line: a changed `go.mod`/`go.sum`, a file matching a `-full` glob the Makefile passes (build files
  and shared test support: obor-kit lists `Makefile`, `internal/testkit/**`, …), a removed package, or an unset or
  unreadable list.
  When it cannot make a list, the Makefile runs the full suite. The `go` class (`cmd/**`, `internal/**`, `kinds/**`,
  `tools/**`) has `run: ["check_affected"]`; the other classes map the non-Go files to the checks that read them
  (`check_web`, `check_release`, `check_readers`), and `merge.by_path.guard` covers the rest. The
  Go-specific residue is the project's: `lanes.env`, `checks.output.failed_test`/`failed_package`, `merge.retest`,
  the Makefile.

## Stable channel (owner 2026-10-30)
Other projects never run obor from this repo's main checkout (every merge here would change their live tooling).
They use one kit per machine, `~/.obor/kit`: a checkout at the newest release tag, made and moved by `install.sh`
(RELEASING.md), with `obor` linked into `~/.local/bin`. A release is tagged only after its merges are reviewed and
verified; adopting leads hear of it by `obor send`. A project's serve picks a new kit up at `obor serve restart`.

## Shipping the binary (bin/obor the only bash)
`bin/obor` is a small self-contained bash shim (no other kit file sourced):
- **Self-host**: inside an obor checkout (main or lane worktree: `bin/obor` plus a plugin.json named `obor`) it
  re-execs the main checkout's `bin/obor`, so a lane never runs its own stale build; `OBOR_NO_SELF_HOST=1 ./bin/obor`
  runs a lane's own; kit-owned callers (hooks, mods) pin `OBOR_NO_SELF_HOST=1`.
- **Binary:** the kit's dev build `dist/obor` when present (run as is: a stale one says `unknown subcommand`, `make
  build` fixes it), else `~/.obor/bin/obor-<version>-<os>-<arch>`, version = the kit's `VERSION`;
  missing → fetched from `bin/release-url` with curl and checked against `bin/checksums.txt` (`make
  release` keeps that catalog cumulative). It exports `OBOR_PLUGIN_ROOT` and `OBOR_VERSION_SOURCE` (dev build /
  kit default) and execs `<binary> <subcommand> …` for the subcommands in `bin/go-commands`; others → usage, exit 2.
- Hooks: the `has` check and the no-binary rule ("Hooks").

## herdr integration (2026-10-09 — user direction)
obor should be as simple and as integrated as possible: the terminal's own status is the source of agent state
(herdr's agent detection; on hc and Rex, the status each agent reports itself, OSC 7501, which the terminal relays and
`internal/progstatus` alone interprets for every adapter: its word decides, `error` as idle, and its blocked kind and msg or an error's msg are the pane's note, sanitized and shown on the agent's
`obor status` line, a blocked pane's needs-you and the web, never decided on); obor does no
Claude/Codex screen parsing of its own beyond what exists, and removes it as the terminal grows the API. obor ships no
herdr plugin: serve restores its panes itself ("After a herdr restart" above).
- **The terminal is the project's** (`project.terminal`, read at serve's start): herdr, the default, hc
  (herdr-core, experimental: hc ≥ 0.1.21, its live tier does not gate) or rex (Rex, experimental: the `rex` CLI on
  `PATH`, never guessed in the app bundle; a status counts only while its reporting process holds the block's
  foreground; its live tier does not gate). The pane variable is the kind's own (`HERDR_PANE_ID`, `HC_TERMINAL_ID`,
  `REX_BLOCK`), never another kind's; a block inherits the Rex server's environment, so `REX_BLOCK` wins over any
  inherited name; an empty `OBOR_`/`HERDR_` name reads as unset. A kind
  change drops serve's records of the old terminal (`terminal-instance`, `terminal-runs`; `terminal-kind` names the
  kind they belong to), so the new terminal's panes are identified afresh.
- **hc's core** gives every terminal its own environment, and any `hc` call starts a core with its caller's: serve
  starts a core that is down from a scrubbed environment (no `HERDR_`, `OBOR_`, `HC_TERMINAL_` name) and never restarts
  or stops one that runs; serve's connect and `obor setup check` refuse a core whose environment holds a `HERDR_` or
  `OBOR_` name ("run `hc server restart --fresh` from a shell outside any pane"). Codex `--no-daemon` and OpenCode
  `--standalone` are required there (`obor config validate`). In any agent's pane but the lead's the guard denies the
  `hc` and `herdr-app` commands that create, end, type into or relabel terminals; reads stay allowed.

## Lines to the lead (2026-10-27 — owner decision)
Serve posts two kinds of lines to the lead (`<project.agent_prefix>-lead`) into its session's inbox (Claude,
OpenCode, agy; a lead with no live inbox takes them through its hooks when they act on the answers), and delivers a
brief to a role agent (posted to a Claude, OpenCode or agy session, typed into another kind's pane); nothing is typed into the pane the owner
writes in, and nothing else goes to a chat agent but a job's wake. State in `docs/state-model.md` row 47.
- **`obor send <project> "<text>"`** (a lead or the owner): `<project>` is a path into an obor project. One `obor serve`
  per project (owner decision 2026-10-28): it runs in the project and listens on
  `<root>/.obor/state/serve.sock`, so the request goes to `<target root>/.obor/state/serve.sock`. That serve records
  fact `message` (task key `msg`) and posts `[obor <sender project>] <text>` to its lead's session.
  The own project is refused. A repeated call id gets the stored reply.
- **Briefs:** `obor brief <role-agent> "<text>"` records fact `briefed` with a line `[obor <caller>] <text>`
  delivered to the role agent once its pane is idle (a verify into the running session); a stopped one (its labelled
  pane at a shell) is started fresh in that pane with the line as its first prompt. A brief to the reviewer or
  the deep dive moves an idle deep dive's role worktree to the integration branch's head before the brief is
  recorded; nothing waits for a publish in flight. A role agent takes its first prompt this way only (`obor restart`
  takes no prompt): `obor brief <role-agent> --fresh "<text>"` is the same brief, admitted and recorded as any, and
  only then a running agent is stopped and started fresh with the line as its first prompt (a refused brief stops
  nothing; refused for the lead, the integrator and a worker, and with no text). A stopped one starts fresh with a plain
  `obor brief`.
  A role agent is any agent of the config but the lead, the integrator and the workers. Once that pane has
  worked after the brief (after its line was delivered) and is idle again, serve records and posts
  `[obor] <agent> finished (briefed <age> ago)` to the lead's session: one line per brief, nothing for task or merge
  flow. A turn that ends on an API error (the StopFailure hook) ends the brief with
  `[obor] <agent> stopped: <error> (<details>)` instead.
- **Delivery** is one function in serve (`deliver`, each merge-loop pass): the oldest line per agent, the lead's queued lines as one post per target, one per row; a post's result settles exactly the lines its intent names. The lines
  survive a restart (the log) and a crash may deliver one twice, never not at all; `obor status` lists the ones
  not delivered yet, and one waiting over 24 h is the project's needs-you. Mechanics: `docs/state-model.md` row 47.
- **A needs-you reaches the lead** (the herdr notification reaches the owner, the lead is the one who acts):
  each needs-you fact in force, once, queues the line `[obor] needs-you <task> (<lane>): <cause, one line> → <the
  commands that answer it>` to the lead (row 47).
- **Lines to the lead are posted, never typed** (owner 2026-10-28: nothing types into the pane the owner writes
  in, and an idle lead must still be woken). Claude Code binds an inbox socket per session
  (`CLAUDE_CODE_MESSAGING_SOCKET`, with `CLAUDE_CODE_MESSAGING_TOKEN`; needs Claude Code ≥ 2.1.224); every agent's
  SessionStart hook records it as `<root>/.obor/state/inbox/<slot>` in its project's main checkout (0600; a session
  without one removes it): keyed by the project and the slot (`lead`, `reviewer`, `L-03`, the
  drive agent's `L-03/drive` as `L-03.drive`), never by an agent name, so two projects with one prefix keep apart and
  a rename moves nothing. Serve and `obor send`/`brief` read their own project's records, the slot of an agent being
  the one whose derived name it is under the config as it is now (`project.AgentSlot`); there is no global record
  (no record outside the project: a live agent records again at its next SessionStart). Serve posts the line to the socket
  that record names (the auth line, then a
  `user` message; `cmd/obor`'s `poster`): no pane lookup, no idle rule. An idle lead starts a new turn with it, a busy
  one reads it between tool calls; it arrives as a message from another session, and the owner's half-typed draft
  stays in the box. **OpenCode's inbox** is its plugin's: `opencode/obor.js` binds a unix socket per session
  (`${HOME}/.obor/opencode/<session>.sock`, beside its token file; none over a unix socket's 103 bytes) before the
  session's `session-start` hook and puts its path in that hook's env (`OBOR_OPENCODE_INBOX`, the kind's
  `inbox_record`), so the hook records it as the session's inbox: the socket the kind's `deliver` names, the kind's own
  record kept beside it and handed back to `deliver`. `deliver` writes one JSON line `{"type":"user","text"}`; the
  plugin turns each line into `ctx.session.prompt({sessionID, text, metadata: {obor: "line"}})`, delivery `steer`
  (OpenCode's default): idle → a turn starts; busy → the line waits for the running model step or tool, nothing is
  interrupted; the owner's draft stays. The socket is removed when it closes and when the process exits. No record, or
  its socket gone → the lead's hooks take its lines when they act on the answers (next bullet), else the line waits,
  untried; a refused dial (a socket file nobody listens on) → fact `failed` with its reason, shown in `obor status`
  and the needs-you, and the record marked refused: the lead's hooks take its lines until the session records an inbox
  again (a lead line is never typed).
  There is no ack: `sent` means handed to the socket, as a typed line's means handed to herdr. **agy's inbox** is its
  language server's: `agentapi send-message <conversationId> <text>` (agy's own `agentapi`) starts a turn in an idle
  session and reaches a busy one at its next model call, the owner's draft kept; it arrives as a system message. Its
  address and token (`ANTIGRAVITY_LS_ADDRESS`, `ANTIGRAVITY_CSRF_TOKEN`) are only in the environment of the commands
  the agent runs, never its hooks', so **any `obor` command** (every one but `obor hook`, `obor kind` and `obor mcp`)
  run with a role and the kind's `inbox_record` variables in its env (the address, the token,
  `ANTIGRAVITY_CONVERSATION_ID`; never agy's `ANTIGRAVITY_AGENTAPI_EXE`: the record is the agent's to write, so it
  names no program) writes the session's inbox record from that env (`guard.CommandRecord`; the same file, 0600; the
  kind's own record kept whole, marked as one the kind sends with): a lead's start checks (`obor whoami`, `obor
  status`) create it in its first turn, with no instruction to the model, and every later command refreshes it (a new
  token after an agy restart, a refused mark cleared). A record that names its socket itself (Claude's) is its
  SessionStart hook's alone; a SessionStart removes a record its env does not carry (a new agy conversation's, until
  its next `obor` command). The kind's `deliver` sends the line itself (`agy agentapi send-message`, agy from serve's
  PATH, with the record's address and token, only to `localhost:<port>` or `127.0.0.1:<port>`: another address is no
  inbox): the kind delivered it, no socket, no ack. A refused send
  (agentapi's non-zero exit: a stale token, one per agy start; the token's lifetime is unprobed) delivers nothing:
  fact `failed` with its reason, and the record is marked refused, so it is not live and the lead's lines go through
  its hooks until a command of the session records it again; a line goes once across the two paths (next bullet). A
  lead of a kind with no inbox (Codex) takes its lines through its hooks when its transport acts on their answers (next
  bullet), else they wait.
- **A lead with no live inbox takes its lines through its hooks** (owner 2026-11-22: the lesser mode), when its
  kind's hook transport acts on the hooks' answers (`agents.Caps.HookAnswers`, the manifest's `caps.hook_answers`).
  One rule decides how a lead's lines go (`agents.LeadLines`): posted when its kind has an inbox (`agents.Caps.Inbox`)
  and its inbox record is live (`project.InboxLive`: it names a socket that is there, or the kind sends with it and no
  send with it was refused); else through its hooks when its kind has that cap; else they wait. Serve reads it with
  the lead's identified pane's kind (else its configured one), the lead's hooks and `obor whoami`'s `lines:` with the
  kind the session runs as; each reads the record. When it says hooks, the delivery pass
  holds the lead's lines (`<lead>'s kind has no inbox: its hooks hand it its lines at its next prompt or turn end`, or
  `no inbox recorded for <lead>: …` for a kind with an inbox, in `obor status` and the 24 h needs-you; `obor send`'s
  reply says it queued them so, with the `obor restart` hint when no session is recorded), and its hooks ask serve for
  them (`lead_lines`):
  `user-prompt-submit` adds them as the prompt's context, before the handoff reminder; `stop` blocks the stop with them,
  so a lead that just finished goes on with its lines. Within an active stop loop (`stop_hook_active`) the stop hook
  asks nothing and the stop goes through, and a background run of the lead's own blocks first (its lines wait). Serve
  hands every line queued for the lead, one per row, as one post (an open post's members first, as a post goes again)
  and records it as a post's delivery (intent `deliver.<first seq>{to, lines, via: hook}`, result `sent`, each other
  member's own result): a line handed out is delivered, never handed out or posted again, and `obor status`, the 24 h
  needs-you and a needs-you line's repeat read that delivery. A repeated call id gets the same lines; there is no ack:
  `sent` means handed to the hook. Any other lead is refused (a live inbox's lines are posted there). Once per line
  across the two paths: a line posted through the inbox is delivered and never handed out by the hooks, and a line is
  claimed by one path at a time (the pass posts no line a hook call is handing, a hook call hands no line a post is
  sending). An idle lead whose lines go through its hooks asks nothing: it is reached by `obor status` and the
  needs-you notice (the owner's herdr notification), its lines waiting for its next prompt or turn end. A lead with
  neither a live inbox nor the cap (Codex: no hooks) gets nothing through its hooks: its lines wait, untried and never
  recorded as delivered (`no inbox recorded for <lead>: …` in `obor status`), and the 24 h needs-you reaches the owner.
- **Role agents' lines go as the pane's kind takes them** (`agents.Kind.Deliver`), only while that agent's pane
  is idle or done (the rule of job delivery): a kind with an inbox (Claude, OpenCode, agy) gets the line posted to its
  session when its inbox record is the live session's in that pane, else typed into the pane as its job wakes are (a line
  to an idle identified agent never waits on a missing or mismatched record); a post refused before a byte went out
  (a dead socket: `os.Stat` passes, the dial refuses; agy's refused send) makes the next attempt typed, the pane
  re-checked idle first. Any other kind (Codex) is typed into its pane (`agent.prompt`, `herdr.Panes.Type`), no Escape,
  no screen read, no clear. A typed line herdr refuses because the pane is blocked (`blocked`, `agent_blocked`) is a
  refusal before typing: fact `failed`, retried at a later pass. For Claude the inbox ends the dialog class (a typed
  line landing in a Claude Code dialog): a post cannot reach a dialog; a session behind an open permission dialog
  reads it when its turn continues, and it cannot answer the dialog.
- **A queued line is in one state** (`queued.state`, its moves in one function): *waiting* (nothing
  went out; why: the pass's hold — busy, not identified, no record — else its last refusal's reason, both in `obor
  status` and the 24 h needs-you), *sending* (its delivery claimed or its intent in the log: a byte may have gone
  out), *sent* (a result, also `sent: <error>` after a byte) and *retired* (it ends undelivered). A refusal before a
  byte (fact `failed`) puts it back to waiting with its reason; replay folds the same records to the same state. An
  answer's line retires once its question is settled another way — the worker submits or asks again, takes its job again (`job_next`), the task is
  unblocked, dropped or rejected, a newer answer or resume comes: waiting, at once; sending, it is marked and
  stays until its outcome (it may have landed, as a brief's does): sent, or retired by a refusal before a byte,
  or by the next pass closing its open delivery unsent (result `retired: …`, a crash's cut delivery).
- **Job wakes go the same way** (owner 2026-10-28): a worker's or the integrator's wake to a Claude, OpenCode or
  agy session with an inbox record is posted, `[obor job <id>] call the obor job_next tool now (connected; it returns your job)` as typed (no `ctrl+u`, the same intent and
  result, `docs/state-model.md` "Prompts": a wake is harmless twice). Another kind, a pane serve does not see, or a
  session with no inbox record (a session its SessionStart hook did not register) is typed as before: a refused wake
  would strand the worker until a restart, a typed one reaches it as it always did.
- **Finding the pane**: one identity table per project, derived each pass
  from proofs (session record > serve's own start > label; herdr's agent name is never read by the table), answers
  which pane an agent runs in (Identified, Ambiguous: two panes with equal proof, or Unavailable) and which agent a
  pane runs; every configured agent is a candidate. Every line, brief and job wake is addressed to the identified
  pane's id right before it goes; ambiguous or unavailable, it waits (lead lines are posted by inbox record,
  pane-independent). **Every start asks it first** (owner 2026-11-11, `docs/research/start-identity-advice.md`):
  launch's `Locate` sends serve's `where` request, adds the terminal's live holder of the agent's name (read once,
  the weakest claim) and decides runs / ambiguous / free / cannot tell before any stop, label, env line or session
  record; restart, stop, serve's starts, setup, `obor lead` and `obor drive` all go through it. Serve down: a start
  that would add an agent is refused while a live agent pane of the project is not labelled for another configured
  agent. Only a label naming another configured agent reserves a pane. Mechanics: `docs/state-model.md` row 48.
- Only for projects with a merge loop (an integrator role): the loop's pass is the delivery's clock. `obor send` and
  `obor brief` to a project without one are refused (`merge loop off …`), as `obor dispatch` is.

## opencode lanes (2026-10-17 — user direction)
opencode runs no Claude Code hooks. The kit ships `opencode/obor.js`, an opencode **2.x** plugin (default export
`setup`; opencode 2.x's plugin runtime API is undocumented, found in the binary: a dispatch warning shows when it
stops loading). It holds no obor rules: each hook maps opencode's event to the Claude Code hook event JSON and runs
`obor hook <name>`, so the guard and the session context stay in the Go binary. `tool execute.before` →
`pre-tool-use` (a deny throws, which aborts the tool with obor's line; an unreadable patch is denied). An OpenCode
tool the plugin does not map goes to the guard as `unguarded` (`tool_input.tool` its name, `tool_input.why` "obor has
no rule for OpenCode tool <X>: update obor"), so an opencode update that adds a tool shows as a named deny for obor's
agents, never a silent bypass. `execute` (Code Mode, the carrier of every MCP tool) and the tools with no guard rule
(`read`, `glob`, `grep`, `list`, `skill`, `question`, `webfetch`, `websearch`, `subagent`; a child's calls reach the hook
under its own session) pass as they are; `execute`'s inner calls reach the hook as `<namespace>_<tool>`, and one passes
only when `ctx.tool.list()` gives it a namespace and the runtime reports that MCP server connected (`ctx.mcp.list()`;
obor's own always), never by prefix: a plugin's namespace is no
server's and goes to the guard as unguarded. OpenCode's own tools (`opencode_*`) are classified one by one:
`worktree_create` and `worktree_remove` go as the guard's `Bash` event for the git command they run (`git worktree
add`, `git worktree remove [--force]`), `session_move` goes as unguarded with the transport's own reason (an obor
agent stays where obor started it), `session_rename` and its read tools (`worktree_list`, `models`, the MCP resource
readers) pass, and any other is unguarded. OpenCode's tool catalog is re-probed on an upgrade
(`docs/research/opencode-tool-names.md`). The session context
→ `session-start` (with the session id the plugin's events name, so the session is recorded and a restart resumes it
with `--session <id>`; a resumed start runs it at plugin start, the id read from `OBOR_OPENCODE_RESUME` (the kind's start sets it on a resume and unsets it on a fresh start; the plugin's server process inherits the TUI's env, not its argv), so the session's
inbox exists before its first turn). Only the agent's own session runs it: the plugin asks `ctx.session.get` once
per session, and a session with a `parentID` (a subagent's) is a child, never started, recorded or bound. Every event
carries `agent_kind: "opencode"`. The plugin acts on the hooks' answers (`caps.hook_answers`): `user-prompt-submit` runs in the
session `prompt` hook, before admission, and its `additionalContext` is appended to the prompt's text (the model and
the owner see it, and the session keeps it); a stop's `{"decision":"block","reason"}` becomes
`ctx.session.prompt({sessionID, text: reason, metadata: {obor: …}})`, which starts the session's next turn, and the
stop ending that turn carries `stop_hook_active: true`. A prompt that carries `metadata.obor` `stop` is that reason:
the prompt hook runs no second `user-prompt-submit` for it (an inbox line, `metadata.obor` `line`, runs it as a typed
prompt does). The plugin is also the session's inbox ("Lines to the lead": a socket per session, recorded through
`session-start`'s env). A failing hook leaves the prompt as typed, except the guard: it allows a call only on exit 0,
and denies on exit 2 with obor's line and on every other outcome (another exit, a kill by a signal, a timeout, no
binary) with a repair hint. opencode lanes run `opencode --auto --standalone` (own server with the pane's env), so
`obor hook` gets the agent's whole env, `OBOR_ROLE`, `OBOR_LANE` and `HERDR_PANE_ID` included, as Claude's and Codex's
hooks do. Each start's generated config (`OPENCODE_CONFIG`) names the launching kit's plugin package dir
(`<kit>/opencode`), so the kit that started the agent runs its hooks, through that kit's `bin/obor` with
`OBOR_NO_SELF_HOST=1`; the plugin is a no-op outside an obor project. The guard fails closed on a store it cannot read: a store that exists but cannot be
opened or read (a newer schema, any open error) denies a non-lead role's writes inside the repo, naming the store and
"run this kit's binary"; no store yet, or no lane row, stays as before.
The session-start hook writes `.obor/state/lanes/<LANE>.plugin` in the main checkout (never in the lane worktree), a marker holding the time the hook ran. `obor status` shows `no-plugin` for a working opencode lane whose marker is missing or older than the lane row's dispatch, once 2 min (`view.pluginSettle`) have passed since the lane's last `agent.start` (the plugin checks in on the session's first event, so a restart's or a resume's start comes after the dispatch; `internal/view` `pluginMissing`), so a lane whose plugin never loaded for the current dispatch is flagged.
The model and effort of an opencode start: the type's `--model provider/model[#variant]`, the effort as its
variant, reach the plugin as `OBOR_OPENCODE_MODEL` on the env line (unset for a type without `--model`: opencode's own
choice stands; an effort then is an error). The plugin's session `prompt` hook switches each new root session of
its location to it, once (`ctx.session.switchModel`), before the first turn; a child session is never switched (it
inherits its parent's model), and without the variable no switch is made, so the owner's own opencode windows keep
their model. Gated on opencode 2 with `switchModel` (else one line, nothing). The TUI has no `--model` and, for a new
session, prefers the variant it remembers in its `model.json`; it switches the session to its pick just before it
submits, and the prompt hook runs after that switch, so the start's model wins from the first turn (a
switch on `session.created` loses). opencode runs on the owner's own state and config, which obor never writes.
The start's prompt: a fresh start runs it as `--prompt <p>`; a start with a session id runs `--session <id>` and, with
a prompt, that prompt as a new turn on top of the resumed history.
The context size of an opencode session: the plugin appends one line per root session's step (`session.step.ended`) to
`${HOME}/.obor/opencode/<session>.jsonl` — the step's `input + cache.read + cache.write`, and its time — and reports
that file as the session's `transcript_path`; the kind's manifest names the same glob as its `transcript`, and `turn
what: context` reads the file's last line, so the handoff reminder and a keep lane's handoff rule have the session's
number. A child session's steps and another directory's sessions write none.

## agy lanes
agy (Antigravity CLI) is a kit kind, `kinds/agy`, linked into obor (answered in process), wired by files the project
commits, never by anything user-level: `.agents/mcp_config.json` holds `mcpServers.obor` = `obor mcp`, the `obor` on
the agent's `PATH` (the server inherits the agent's role, lane and pane), and `.agents/hooks.json` the named hook
`obor`, whose handlers (SessionStart, PreInvocation, Stop, and PreToolUse matching every tool) run `obor kind agy hook`:
that obor runs agy's hook transport in process, which rewrites agy's event as Claude Code's, runs obor's hook in
process on the same obor (no second obor start) and rewrites the answer (`kinds/agy/kind/hook.go`; no rule lives there).
The transport asks `internal/project` whether the event is in a running project, the guard's rule, so a repository
nested in an obor project is its own. Its tool table maps `run_command`,
`write_file`, `edit_file` to the guard's shell and write events and `call_mcp_tool`/`list_resources`/`read_resource` to
`mcp`; the tools obor cannot guard (`run_workflow`: a script with no hook; `swarm`; `invoke_subagent`: a child's session
cannot be told from the agent's) and any tool it does not know go to the guard as `unguarded` with agy's reason, and the
guard denies them for an agent with a role (a role-less session in the main checkout: no opinion); the rest only read or
talk and get no opinion. A stopped project runs none of it. `obor kind agy hook` runs the first obor that has `kind`
(no kind binary beside it is needed) and never fetches; with none it answers by event: PreToolUse in a running project
is the deny naming the repair, PreToolUse anywhere else `ask` (agy's own check decides), and SessionStart,
PreInvocation and Stop nothing (agy ignores their answers). A start refuses (a lead: warns) a workspace whose
`.agents/hooks.json` obor entry is not this kit's, after launch has prepared that workspace. obor's
allow is agy's `ask` (agy's own check decides: in auto mode, none), a deny is final; agy denies a tool call whose hook
fails, so the transport always answers, and a failing `obor hook pre-tool-use` is a deny naming the failure.
`obor setup agents` writes both entries when the config has an agy model (each file's other entries kept) and adds
both files to `rules.protected_files`; the lead commits them; `obor setup mcp` checks the MCP entry and warns when
the `obor` on `PATH` is not the running kit's. A lane worktree has the files through git. Every agy agent, a lead
too, runs in auto mode: a command runs in agy's sandbox (no writes outside the workspace, temp dirs and caches, no
unix socket, no network), and one the sandbox refuses is re-run outside it once agy's reviewer approves (a socket, the
network, a write outside the workspace: no prompt; a destructive one asks); obor's guard checks each attempt. obor
allows its own MCP tools (`mcp(obor/*)`) and commands (`command(obor)`) through agy's `permissions.allow` rules on
every start (turning a reviewer ask on an unsandboxed re-run into a run; deny rules stay final). A project's own
command rules go in its override's `permissions.allow` (`.obor/agy/settings_override.json`, merged whole into a trimmed
start's settings file and without its mode keys `toolPermission` and `agentMode` into a lead's, so the lead takes the
project's rules and other keys, never its permission mode, while keeping the user's own settings untrimmed). Allow rules
do not lift the
sandboxed first attempt or skip the review: git in a linked worktree (whose git dir sits outside the workspace) and
tests that dial a unix socket still fail sandboxed, and a rule covers no compound command's every part, so agy's own
dialog would ask for a lane's checks and read-only shell (P12). A lane's start (a worker's or the drive agent's, told
by `strict_mcp`) names `toolPermission: "always-proceed"`, obor's hooks its guard; a project's override that names that
key wins. A lead's or a role's start sets none of obor's own (the lead stays in plain auto mode, where each `obor` call
runs sandboxed first and then goes through the reviewer's check, 11–36 s; a role's override may still name one, as
today). The
session record is the SessionStart hook's (a new conversation, again after `/clear`; a resume fires none and needs
none): a restart without `--fresh` resumes it after the agent exited or its pane is gone, and a recorded session of
another kind (the agent ran one before) is never resumed: herdr's live agy session of the pane, else a new one. A start: `agy --settings <file> [args] --model <id>`, with an effort `--model <the id
without its effort word> --effort <effort>`; the first prompt as `-i`, a resume as `--conversation <id>` (the
recorded session id). A fresh start checks its model and effort against `agy models` (agy's own list, live, up to 4 s,
in its own process group; the last good list is kept in `~/.obor/agy/models` and used when the live call fails) and
fails for an unknown model or an effort the model lacks before agy runs; a resume checks nothing; the effort check (`obor config validate`, a dispatch) reads the manifest only, no CLI. One limit: a
custom model from agy's settings (`customModels`) that `agy models` leaves out is refused at the start. The manifest's
`not_ready` matches the notice agy shows when it still runs another model. Stop: `/exit`. The context size is the conversation's `transcript.jsonl`
(`~/.gemini/antigravity-cli/brain/<id>/.system_generated/logs/`).
