# Upgrading an obor project from v0.18 to v0.19

## v0.19.1 (opencode model through obor's plugin)
Re-run the installer, then `obor serve restart` in each project. opencode panes already running keep their old start
until their next restart.

- **opencode's model and effort go through obor's plugin**: an opencode start no longer gets its own
  `XDG_STATE_HOME` or `OPENCODE_CONFIG_CONTENT`; it exports `OBOR_OPENCODE_MODEL=<provider>/<model>[#<effort>]` (from
  the type's `--model` and the effort), and `opencode/obor.js` switches each new root session to it before its first
  turn, over a variant your own opencode remembers. Your own opencode windows (no such variable) are untouched. Needs
  opencode 2.x; an older one logs a line and starts on its own model (T-703).
- `~/.obor/opencode-state/` is no longer used: delete it. A pane shell that still exports an `XDG_STATE_HOME` under it
  (started by v0.19.0) keeps it until the shell is restarted; harmless, opencode just keeps using that old dir.

## v0.19.1 (patch)
Re-run the installer, then `obor serve restart` in each project. No config change needed.
- **`obor stop` / `obor start`**: put a project to sleep (stops its agents except your session, stops its serve, drops
  it from `obor projects` and the web page; its hooks and git hooks stand down) and wake it again. Refuses with work
  in progress unless `--force` (T-709..T-711).
- **No idle agents after setup**: `obor init`, `obor setup pane` and `obor setup role` label panes and start no
  agent; agents start on first use (`--start` for the old behaviour) (T-705, T-708).
- **`obor init` in a new project**: an uncommitted `.claude/settings.json` reaches every lane (`lane_links`), and
  status/dispatch flag a Claude lane without the obor plugin; the default opencode type names its model (T-705).
- **opencode model and effort through obor's plugin**: obor's opencode plugin switches each new session to the
  model and effort obor asked for; the per-agent `XDG_STATE_HOME` dirs of v0.19.0 are gone (`~/.obor/opencode-state`
  can be deleted) (T-703, T-707).
- **Researcher role** (`roles.researcher.type`): a role pane in the main checkout that writes only `docs/research/`
  (T-704, T-706); every role's write profile now also covers shell writes and git commands (T-706).
- `obor web`, `obor monitor` and `obor status` show a project whose config has fields this binary does not know,
  with a warning (T-709).

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
