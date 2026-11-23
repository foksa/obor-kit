# Questions for the lead

- T-1503: `docs/design.md` ("agy lanes") says the transport's PreToolUse runs for `run_command`, `write_file`,
  `edit_file`; it now also maps `call_mcp_tool`, `list_resources` and `read_resource` to the guard's `mcp` call (the
  server check). Should that paragraph name them? The transport still holds no rule.
- T-1512: `docs/design.md`'s override table row (~186) and its `claude_override.json` paragraph (~208-216) still say
  the MCP file holds the user's servers with the override merged over them for every agent, the project's approved
  `.mcp.json` servers added after; only the lane rule was added later (~253). Should the row and the paragraph say a
  lane starts from the approved project servers with obor's own last, while a role agent's start is unchanged?
  README.md carries the split.
- T-1529: `docs/design.md`'s opencode paragraph (~975-981) says an `execute` inner call passes when `ctx.tool.list()`
  gives it the namespace of an MCP server the start's configs name; the plugin reads the runtime's connected servers
  now (`ctx.mcp.list()`), so no config layer is mirrored. Should that paragraph name the runtime source?
