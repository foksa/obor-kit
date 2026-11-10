# What agents load, and where to change it

Every agent but the lead starts with the owner's own plugins and skills off; every other user setting (env, hooks,
statusLine, permissions) applies. Claude agents do not get the owner's `~/.claude/CLAUDE.md`, rules or skills.
What a change to this is: the owner's call (ask first), committed in the project, so every lane gets it through git.
Details, per CLI: the kit's `README.md`, "Customizing agents".

| The owner wants, for the project's agents | Where |
| --- | --- |
| a skill | `.agents/skills/<name>/SKILL.md`, with `.claude/skills` a symlink to `../.agents/skills`; the role text (`docs/agents/<role>.md`) names which roles use it |
| an MCP server (e.g. Blender) | the CLI's project file: Claude `.mcp.json` plus `enabledMcpjsonServers` in `.claude/settings.json` (lane agents load it then); Codex `mcp_servers` in `.codex/config.toml`; OpenCode `mcp` in `opencode.json` |
| a setting for everyone who opens the project, lead and owner included | the CLI's project file (`.claude/settings.json`, `.codex/config.toml`, `opencode.json`): it wins over the user's per key, but cannot remove a user hook or MCP server |
| one of the owner's plugins or skills back on | `.obor/claude/settings_override.json` (`enabledPlugins`), `.obor/codex/config_override.toml`, `.obor/opencode/opencode_override.json` |
| one of the owner's hooks, MCP servers or keys gone, or a setting for obor's agents only | the override file of that CLI, `.obor/<kind>/<file>_override.<ext>`, merged over the owner's file of the same name (JSON Merge Patch: `null` deletes, objects merge, the rest replaces): Claude `settings_override.json`, `claude_override.json` (`mcpServers` only); Codex `config_override.toml`, `hooks_override.json`; OpenCode `opencode_override.json` |
| model, effort, lanes, a kind's extra args | `.obor/config.json` (`agents.difficulty`, `agents.roles`, `lanes.count`, `agents.kinds.<kind>.args`) |

After a change: `obor config validate` (it warns on an override entry it cannot apply), commit, and the next start of
each agent picks it up (`obor restart <agent> --fresh` for a running role agent).
