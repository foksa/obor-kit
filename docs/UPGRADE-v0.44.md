# Upgrading an obor project from v0.43 to v0.44

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
  format, laid over yours). A lead takes the override's rules but never its `toolPermission` or `agentMode`.
- agy's auto mode runs commands in a sandbox first: git in a lane worktree and tests that open a unix socket fail
  there. A project whose lanes need either sets `{"toolPermission": "always-proceed"}` in the override; obor's hooks
  still guard every agy agent (a deny is final).
- A model or effort `agy models` does not list fails the start (about 1.5 s per start to read the list).
- Lines to an agy lead are posted through agy's `agentapi`; a refused send falls back to its hooks.

Also in v0.44.0, nothing to do for any of them:

- A restart records the session it resumes or adopts, with its kind, so a later restart after the agent exited
  resumes it.
- A review's fix task dispatched before the review started still counts for its verify once linked.
- The kind protocol (`docs/agents/kind-protocol.md`) gains `turn what: context`, `headless.flags`, `caps.app`,
  `caps.hook_answers`, an inbox for plugin kinds, `install where: project` with the request's `root`, the start
  request's `check`, and a "Hook transport" section.
- The release ships `obor-kind-agy` beside `obor-kind-opencode`; the installer fetches both.
