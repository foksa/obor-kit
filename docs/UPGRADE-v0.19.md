# Upgrading an obor project from v0.18 to v0.19

## v0.19.0 (effort, spec lane types, opencode isolation)
Re-run the installer, then `obor serve restart` in each project; restart the monitor; the lead picks up the new skill
text at its next session.

**Check before you restart: opencode lane types need a model.** obor now gives every opencode agent it starts its
own state dir (T-696, T-698), so opencode no longer falls back to the model last picked in your own opencode window.
An opencode lane type whose args have no `--model`, in a project with no `opencode.json` model and no global one,
starts on opencode's default model. Add the model to the type, e.g.
`"args": ["--auto", "--standalone", "--model", "opencode-go/deepseek-v4.1-flash"]`. A project whose lanes set the
model in their own `opencode.json` needs nothing.

- **Effort beside the type everywhere**: `<type>[:<effort>]` (`opus:high`, `sonnet:low`, `deepseek:high`) in a spec's
  `Type:` line, `obor dispatch --types`, config `difficulty` lists and `roles.*.type`. It applies when serve starts
  the agent: Claude `--effort`, Codex `model_reasoning_effort`, opencode `--model …#<effort>` (needs `--model` in the
  type). Types that exist only to set an effort (`opus-high`, `opus_high`) still work; `opus:high` can replace them
  (T-695).
- **A spec may name its lane type**: an optional `Type: <type>[, <type>…]` line under `Difficulty:`; dispatch takes a
  free lane of that type first, else the level's first free lane, and waits for either (T-689, T-695).
- **opencode lanes are isolated**: each agent's `XDG_STATE_HOME` is `~/.obor/opencode-state/<project>/<agent>`,
  emptied at a fresh start; your own opencode state is never written; another agent kind in the same pane drops it
  (T-696, T-697, T-698).
- `obor setup show` prints effort per level and role.
- `obor monitor` paints only failed events red (a passing gate's "0 fail" no longer does) (T-688).
- Kit maintainers: `internal/app` split into `port`, `event`, `wire` (T-690..T-694, T-699, T-700); `make check`
  about 115 s instead of 158 s (T-685, T-686); `OBOR_CALL_DEADLINE` shortens socket call deadlines in tests.
