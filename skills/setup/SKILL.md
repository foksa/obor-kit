---
name: setup
description: Add a lane, a pane or a role agent to a project obor already set up (`obor init` sets a project up).
disable-model-invocation: true
---

# /obor:setup — add a lane / pane / role

`obor init` sets a project up: `.obor/config.json`, the lane worktrees, the panes and agents, the plugin and
`obor serve`. Use this skill to change what it made. `obor setup …` subcommands are idempotent and end with `OK …` or
`FAIL …`; read the last line, fix, rerun. Nothing is committed for you: show the user the diff and commit by explicit
path when they agree.

## Add a lane
```
obor setup lane L-02
```
Takes the next lane name (`L-01` … `L-NN`, in order) or one already configured (a rerun). It raises the config's
`lanes` count (a `lanes` list names the lanes: add `{"name": "L-02"}` by hand), adds a worktree detached at the main
checkout's branch under `<lanes_dir>`, symlinks `lane_links`, runs `commands.lane_setup` there. A `FAIL lane_setup …`
shows the command's output: fix with the user. A lane has no model: the task's `Difficulty:` level picks it at dispatch
(`obor setup show` prints the levels).

## Add a pane or a role agent
Spawning panes can be blocked by the auto-mode classifier: ask the user to approve, or to run the command.
```
obor setup pane O-01 --new-tab workers        # first lane: a new tab
obor setup pane O-02 --tab <tab-id>           # next lanes: split that tab (herdr tab list)
obor setup role integrator --tab <tab-id>     # lead | integrator | reviewer
obor setup role reviewer --new-tab review     # reviewer gets <lanes_dir>/<roles.reviewer.worktree>
obor setup role triage --tab <tab-id>         # optional bug inbox: <prefix>-triage in the main checkout, docs/BUGS.md
```
Without `--tab` / `--new-tab` the current pane's tab is split; `--pane <id>` uses an existing shell pane. `obor setup pane` labels the pane and types the env but starts no agent (dispatch starts a fresh worker anyway); add `--start` to start it now. `obor init` and `obor setup role` likewise label their panes and start no agent (each starts on first use: a job, a review, a brief); `--start` starts it now. Panes get
`--cwd` the lane and `OBOR_ROLE` / `OBOR_LANE`; agents are named `<prefix>-<lane lower>` (`<prefix>-int`,
`<prefix>-rev`, `<prefix>-lead`); Claude agents except leads start with `--settings '{"autoMemoryEnabled":false,"remoteControlAtStartup":false}'`
(a lead keeps Remote Control and auto memory; lane-type settings overlay those keys).
- `FAIL tab … is full` → use `--new-tab` (a tab holds 4 panes).
- `FAIL lane type … is at its max` → ask the user: retire an agent or raise `max`.
- `FAIL agent not ready: … Trust this folder? …` → ask the user to answer the dialog in that pane (Enter = trust,
  once per repo). `… updated itself and exited …` → rerun the `herdr agent start` the hint prints.
Rerunning `obor setup pane` for a lane whose agent runs is a no-op (`OK … already running`).
`obor setup agents` (once per user; ask first, it is user-global)
installs what each agent kind needs outside the project. Today no kind needs anything: opencode lanes (`kind:
opencode`, no Claude Code hooks) get obor's MCP server and its plugin (file guard and session context; there is no
Stop hook, so nothing nudges a missing `task_done`) from each start's generated config (`OPENCODE_CONFIG`, under
`.obor/state/`), Claude and Codex from the plugin manifest and launch args. If an obor before v0.25 put a global
opencode registration on the machine, remove it by hand: the `mcp.servers.obor` entry of
`~/.config/opencode/opencode.json` and the `plugins/obor.js` link there (`$XDG_CONFIG_HOME` respected); restart
opencode agents after it.
Optional: offer a bug-triage agent (`roles.triage` = a model ref, e.g. `opus:high`, then `obor setup role triage --new-tab triage`): the person reports bugs to it, it keeps `docs/BUGS.md` and hands fixes to the project's lead (`agents/triage.md`).
