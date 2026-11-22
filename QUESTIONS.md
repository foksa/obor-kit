# Questions for the lead

- T-1503: `docs/design.md` ("agy lanes") says the transport's PreToolUse runs for `run_command`, `write_file`,
  `edit_file`; it now also maps `call_mcp_tool`, `list_resources` and `read_resource` to the guard's `mcp` call (the
  server check). Should that paragraph name them? The transport still holds no rule.
- T-1512: `docs/design.md` (the `claude_override.json` paragraph, ~lines 208-216) and its override table row say the
  MCP file holds the user's servers with the override merged over them for every agent, the project's approved
  `.mcp.json` servers added after. A lane's start now builds it from the approved project servers with the override
  merged over them, and obor's own last; a role agent's start is unchanged. Should the design paragraph and the row
  say so? README.md carries the new behaviour.
