# Upgrading an obor project from v0.44 to v0.45

## v0.45.0 (the kit's kinds part of obor; lane MCP rules per kind; OpenCode's tools classified)

From v0.44.1. Nothing committed changes, and most projects have nothing to do but restart their OpenCode agents.

```sh
# after updating the kit, in the project
obor version                     # any obor command: it fetches this release's binary (hooks never fetch)
grep -n 'mcp_servers' .obor/config.json .obor/local.json 2> /dev/null || true   # Codex lanes: see below; no output is fine
jq '.agents.kinds // {} | map_values(.plugin // empty)' .obor/config.json   # see "The kit's kinds"
```

Then `obor restart <agent>` each running OpenCode agent (or let the next dispatch start it): a session keeps the
plugin it started with.

**The kit's kinds (OpenCode, agy) are part of obor.** obor answers them itself and runs no `obor-kind-*` binary.

- `agents.kinds.opencode.plugin: "obor-kind-opencode"` and `agents.kinds.agy.plugin: "obor-kind-agy"` (written by an
  older `obor init`) mean the same as no line. Keep or delete them.
- A `plugin` that names one of those binaries by a path (`dist/obor-kind-opencode`, an absolute path) still runs that
  file, which is now an old build. Delete the line.
- A kind of another name whose `plugin` is the bare `obor-kind-opencode` or `obor-kind-agy` no longer finds the
  binary beside obor. Name the kind `opencode` or `agy`.
- `.obor/state/kinds/agy.json` and `.obor/state/kinds/opencode.json` are no longer read; delete them or leave them.
- This is the last release that ships the `obor-kind-*` binaries (they are fetched beside obor's, for a rollback).

**Codex: a lane's start refuses args that set a key under `mcp_servers`.** If `agents.kinds.codex.args` has such a
`-c` (the `grep` above), every Codex worker and drive start fails with "a lane's MCP servers come from the project's
.codex/config.toml; move … there". Move the key to the committed `.codex/config.toml`, or to
`.obor/codex/config_override.toml`. `obor config validate` does not report it; `obor setup mcp --model <a codex
model>` does. The lead and the role agents are not affected. A server whose name needs quotes (a space, a dot) is
now handled in a lane.

**Claude: a lane leaves out a server `disabledMcpjsonServers` names**, also when `enableAllProjectMcpServers` or
`enabledMcpjsonServers` approves it (committed `.claude/settings.json`).

**OpenCode, from the restart:**

- A tool obor has no rule for is denied for every obor agent, the lead included: "obor has no rule for OpenCode tool
  X: update obor". That covers a user plugin's tools, custom tools, `opencode_session_rename`,
  `opencode_session_move` and the `browser_*` tools. Your own OpenCode sessions in the project (no obor role, the
  main checkout) are not denied them.
- An MCP server's tools pass when OpenCode reports the server connected. A server that connects or signs in after
  the agent's first such call is seen at the agent's next restart.
- `opencode_worktree_create` and `opencode_worktree_remove` are checked as `git worktree add` and `git worktree
  remove`. `opencode api` is denied as a headless run, for every role.
- A lane: a `null` for one of your servers in `.obor/opencode/opencode_override.json` leaves that server off (it
  loaded from your own config before).
- obor's plugin needs OpenCode 2.x.

**agy:**

- agy 1.5.15 needs nothing: its tools are the ones obor's table has, and the nightly's live run records the version
  once it is green. A tool a later agy adds is denied by name for obor's agents until obor has a rule for it.
- With no usable obor binary (a kit just updated, before any `obor` command), obor's agy hook denies tool calls in a
  running obor project, with the command that repairs it, and leaves every other repository and a stopped project
  to agy's own check. Running `obor status` once after an update avoids it.
- A start whose live `agy models` call answers no model uses the last good list (`~/.obor/agy/models`).

Also in v0.45, nothing to do for any of them:

- `obor init` writes no `plugin` line for the kit's kinds; `obor setup agents` and serve cache no manifest for them.
- `obor setup mcp --model <name>` only checks: it writes nothing, runs no `agy models`, and reports a stale
  `.agents/hooks.json` as a finding.
- A lead's lines are not handed to a hook that is within 250 ms of its own timeout; they wait for the next hook or
  the inbox.
- A refused agy delivery says why in the line the lead sees.
- An OpenCode root session's turn end creates its token file.
- The nightly records agy's version only after a run with passing rows and no failed or skipped one.
- The kind protocol: `obor kind <name>` runs a kit kind in process, with this obor's executable in `OBOR_EXE`; a
  third-party kind is an external command as before.
