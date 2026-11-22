# Questions for the lead

- T-1503: `docs/design.md` ("agy lanes") says the transport's PreToolUse runs for `run_command`, `write_file`,
  `edit_file`; it now also maps `call_mcp_tool`, `list_resources` and `read_resource` to the guard's `mcp` call (the
  server check). Should that paragraph name them? The transport still holds no rule.
