# Upgrading an obor project from v0.34 to v0.35

## v0.35.0 (non-lead agents start without the user's plugins and skills)

Every agent but the lead now starts with the user's own plugins and skills switched off; every other user setting
(env, hooks, statusLine, permissions, …) still applies. Claude agents load the user's `settings.json` as a merged copy
(`.obor/state/settings/<agent>.json`); the user's `CLAUDE.md`, `rules/` and `~/.claude/skills` do not reach them.
Codex agents get plugins, apps, bundled and user skills switched off; OpenCode agents do not see the Claude skill
dirs (put project skills in `.agents/skills`).

To keep a user plugin or skill for a project's agents, or to remove a user hook or MCP server, write an override
file in the CLI's own format (README, "Customizing agents"):

```
.obor/claude/settings_override.json    .obor/claude/claude_override.json
.obor/codex/config_override.toml       .obor/codex/hooks_override.json
.obor/opencode/opencode_override.json
```

Role worktrees (integrator, reviewer, deep dive) now get `lanes.links` like the lanes, at their agent's next start.
Config shape 1 is no longer recognized at all (v0.34 refused it): a config without `"version": 2` loads as defaults
with unknown-key warnings.

```sh
# nothing to do
```
