# Upgrading an obor project from v0.43 to v0.44

## v0.44.1 (agy's whole tool surface; lane MCP servers)

A project with an agy model has one step. Every other project has none.

```sh
# only a project with a model of kind agy, after everyone on it has updated the kit; the lead, on the integration branch
if jq -e '[.agents.models[]? | select(.kind == "agy")] | length > 0' .obor/config.json > /dev/null 2>&1; then
  obor setup agents   # rewrites .agents/hooks.json: one PreToolUse group that matches every tool
  git add .agents/hooks.json && git commit -m "obor: agy hooks for v0.44.1"
fi
```

Then `obor restart <agent>` each running agy agent: a session keeps the hooks it started with.

**agy: a start refuses a hooks file that is not this kit's.** Until the commit above, every agy worker, drive and
role agent start is refused ("agy's hooks: … run obor setup agents in the project, then commit .agents/hooks.json");
an agy lead starts with a `WARN` line. A missing file counts as stale. agy reads the hooks file of the worktree it
starts in, so after the commit:

- A lane branched before it is refused until its branch is on the integration branch's head: in the lane,
  `git rebase --autostash <merge.target_branch>`, or redispatch the task (`obor drop <task> "<why>" --discard`, then
  `obor dispatch <task>`). A new dispatch is fine.
- A role worktree (reviewer, deep dive, integrator) is refused until it is moved:
  `git -C <worktree> checkout --detach <merge.target_branch>`, then `obor restart <agent> --fresh`. serve moves the
  deep dive's at a brief and the reviewer's at a review's start, never the integrator's.
- A teammate still on v0.44.0 who pulls the new file runs agy without the denies below: update the kit first.

**agy, from the next start:**

- obor's hook sees every agy tool. `run_workflow`, `swarm` and `invoke_subagent` are denied for every obor agent
  (a workflow's script runs with no hook; a subagent's session cannot be told from the agent's), and a tool a later
  agy adds is denied by name until obor has a rule for it.
- Your own agy sessions in the project (no obor role, the main checkout) are not denied these, and a stopped project
  (`obor stop`) runs none of obor's agy checks.
- A resume runs no `agy models`. A fresh start asks it live (up to 4 s) and uses the last good list
  (`~/.obor/agy/models`) when the live call fails.
- An override's `agentMode` applies to every agy agent but a lead.
- After updating the kit, run any `obor` command in a shell before an agy agent's next tool call: hooks never fetch
  a binary, and with none obor's agy hook denies every tool.

**Lane workers of every kind use the project's MCP servers and obor's, none of yours.** This is new for Codex,
OpenCode and agy lanes, and for Claude lanes of a project that approves its `.mcp.json` servers (those lanes loaded
your servers before). The lead and the role agents keep every server. A server the lanes need is declared in the
CLI's own project file, committed:

| Kind | Declare it in | Note |
|---|---|---|
| Claude | `.mcp.json`, approved in `.claude/settings.json` (`enableAllProjectMcpServers`, or named in `enabledMcpjsonServers`) | an approval in `.claude/settings.local.json` or `~/.claude.json` does not count |
| Codex | `.codex/config.toml` (`[mcp_servers.<name>]`), or `.obor/codex/config_override.toml` | |
| OpenCode | `opencode.json` / `opencode.jsonc`, or `.obor/opencode/opencode_override.json` | |
| agy | `.agents/mcp_config.json` | obor's guard denies a lane's call to any other server |

A `.obor/claude/claude_override.json` that names `mcpServers` is laid, in a lane, over the approved project servers,
not over yours: an entry that only patched one of your servers is incomplete there. Write whole entries.

**OpenCode agents restart onto the new plugin** (`obor restart <agent>`, or the next dispatch): the token file now
dates a turn's end. `opencode acp` and `opencode serve` are denied as headless runs, for every role.

Also in v0.44.1, nothing to do for any of them:

- A Claude `pre-tool-use` hook whose binary ends with any exit but 0 or 2 (a crash, a kill) denies the call.
- A write with no file path or a shell call with no command is denied (a renamed CLI argument cannot turn the guard
  off).
- `obor status` exits 1 with the socket's error when the dial of serve's socket is refused (a sandbox); it printed
  `serve: down` and exited 0.
- A lead's inbox record whose socket is dead is marked refused, and its lines go through its hooks until the session
  records an inbox again.
- `obor whoami --json` has `lines` for a lead. A lead of a kind with no slash command starts with "…follow it, with
  the argument handoff".
- `obor setup mcp` checks every kind a model names, used by a role or not.
- The kind protocol: `start` may reply `warnings`; `pre-tool-use` takes `tool_name: "mcp"` with
  `tool_input.mcp_server`, and `tool_name: "unguarded"` with `tool_input.tool` and `why`; a kind that ignores
  `strict_mcp` keeps the user's servers in its lanes.
- The research and plan files this release deleted are in the v0.44.0 tag (`git show v0.44.0:docs/research/<file>`).

## v0.44.0 (agy, and leads of every kind)

Nothing in an existing project has to change. The step below is empty; the notes say what behaves differently.

```sh
# nothing to do
```

**OpenCode agents restart onto the new plugin.** `opencode/obor.js` changed (resume, hook answers, an inbox), and
OpenCode loads it at start: an OpenCode agent running across the upgrade keeps the old plugin until its next start
(`obor restart <agent>`, or its next dispatch).

**OpenCode agents, from their next start:**

- A restart without `--fresh` resumes the agent's session (`opencode --session <id>`) instead of starting fresh.
- obor reads an OpenCode session's context size (a per-session token file the plugin writes), so the handoff reminder
  and the kept-lane stop past `agents.handoff_tokens` apply to OpenCode agents.
- The plugin acts on obor's hook answers: the handoff reminder and the wrong-tab text reach the prompt, and a stop the
  Stop hook blocks continues the session.
- Each OpenCode session has an inbox (a socket the plugin binds): a worker's job wake, a role agent's brief and a lead's
  lines are posted to the session instead of typed, and an idle OpenCode lead is woken by a line.

**Leads of any kind.**

- A kind without a plugin slash command starts a lead with "read `<kit>/skills/lead/SKILL.md` and follow it" (a
  Claude lead still starts with `/obor:lead`).
- `obor whoami` prints `lines: inbox|hooks|status` for a lead: how its lines reach it.
- A lead whose kind acts on hook answers and has no live inbox gets its lines at its next prompt or turn end through
  its hooks; before, they waited in `obor status`. This includes a Claude lead with cross-session messaging off.

**agy (Antigravity CLI) is a kit kind.** To use it, add a model of kind `agy` and run `obor setup agents`:

```json
"gemini": { "kind": "agy", "id": "gemini-4-neon" }
```

- `obor setup agents` writes `.agents/mcp_config.json` and `.agents/hooks.json` (agy's own workspace files, naming
  `obor` by PATH) and adds both to `rules.protected_files`: commit them. Your own agy sessions in the project load
  them too. `obor setup mcp` warns when the `obor` on PATH is not the kit you run.
- Each start gets a `--settings` file: auto mode, `mcp(obor/*)` and `command(obor)` allowed, the lane trusted when you
  trusted the project's main checkout. A project's own agy settings go in `.obor/agy/settings_override.json` (agy's
  format, laid over yours as a JSON Merge Patch: a list it sets replaces your saved list, it is not added to it). A
  lead takes the override's rules but never its `toolPermission` or `agentMode`.
- agy's auto mode runs commands in a sandbox first: git in a lane worktree and tests that open a unix socket fail
  there. A project whose lanes need either sets `{"toolPermission": "always-proceed"}` in the override; obor's hooks
  check every agy tool (a deny is final; since v0.44.1, below).
- A model or effort `agy models` does not list fails a fresh start (since v0.44.1 a resume reads no list, and a failed
  live call uses the last good one).
- Lines to an agy lead are posted through agy's `agentapi`; a refused send falls back to its hooks.

Also in v0.44.0, nothing to do for any of them:

- A restart records the session it resumes or adopts, with its kind, so a later restart after the agent exited
  resumes it.
- A review's fix task dispatched before the review started still counts for its verify once linked.
- The kind protocol (`docs/agents/kind-protocol.md`) gains `turn what: context`, `headless.flags`, `caps.app`,
  `caps.hook_answers`, an inbox for plugin kinds, `install where: project` with the request's `root`, the start
  request's `check`, and a "Hook transport" section.
- The release ships `obor-kind-agy` beside `obor-kind-opencode`; the installer fetches both.
