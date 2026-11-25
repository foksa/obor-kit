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
A default `obor init` makes L-01 and L-02, so its next lane is L-03: take the next name from the config as it is now
(`obor setup show` prints the lanes).
```
obor setup lane L-03
```
Takes the next lane name (`L-01` … `L-NN`, in order) or one already configured (a rerun). It raises the config's
`lanes.count` (a `lanes.list` names the lanes: add `{"name": "L-03"}` to the list by hand first, then run it), adds a
worktree detached at the main checkout's branch under `<lanes.dir>`, symlinks `lanes.links`, runs `lanes.setup`
there. It places no pane: serve places the lane's worker pane in a lanes tab at its first dispatch (`obor setup pane L-03`
places one now, if you want it there before). A `FAIL lane_setup …` shows the command's output: fix with the user. A lane has no model: the task spec's
`Model:` line tries those models first, its `Difficulty:` level picks the rest at dispatch, and
`obor dispatch <task> --model <model>[,<model>…]` overrides both (`obor setup show` prints the levels).

## Add a pane or a role agent
Spawning panes can be blocked by the auto-mode classifier: ask the user to approve, or to run the command.
```
obor setup pane L-03                          # a lane: into a lanes tab with room, else a new tab "lanes"
obor setup role integrator                    # lead | integrator | reviewer: into a roles tab, else a new tab "roles"
obor setup role reviewer                      # reviewer gets <lanes.dir>/reviewer
obor setup role triage                        # optional bug inbox: <project.agent_prefix>-triage in the main checkout, docs/BUGS.md
```
Placement goes by the pane's kind: the lead's tab holds the lead alone; a lane's pane goes to a tab of lane panes
with room (a tab holds 4), a role's pane to a tab of role panes, else a new tab `lanes` / `roles`. A tab's kind comes
from its panes' labels; a tab with a shell in it is no kind's. `--tab <id>` splits that tab (not the lead's: refused),
`--new-tab <label>` opens a tab of that label, `--pane <id>` uses an existing shell pane. `obor setup pane` labels the pane and types the env but starts no agent (dispatch starts a fresh worker anyway); add `--start` to start it now. `obor init` and `obor setup role` likewise label their panes and start no agent (each starts on first use: a job, a review, a brief); `--start` starts it now. Panes get
`--cwd` the lane and `OBOR_ROLE` / `OBOR_LANE`; agents are named `<project.agent_prefix>-<lane lower>` (`<project.agent_prefix>-int`,
`<project.agent_prefix>-rev`, `<project.agent_prefix>-lead`); Claude agents except leads start with Remote Control and auto memory off, on a settings file obor writes (`.obor/state/settings/<agent>.json`: the user's settings with the project's `.obor/claude/settings_override.json` merged over them; a lead keeps Remote Control, auto memory and the user's settings as they are).
- `FAIL tab … holds the lead's pane` → drop `--tab`, or use `--new-tab <label>`.
- `FAIL start failed in <pane> (… Trust this folder? …)` → ask the user to answer the dialog in that pane (Enter = trust,
  once per repo). `… updated itself and exited …` → run the command the hint prints (`obor restart <agent> [--fresh] --pane <id>`,
  or `obor drive <T-xx>`).
Rerunning `obor setup pane` for a lane whose agent runs is a no-op (`OK … already running`).
`obor setup agents` adds the `agents.models` entry of each model the config's levels and roles name without one, when
obor knows it (uncommitted: commit it), caches each plugin kind's manifest (`.obor/state/kinds/`) and checks every kind's install;
no kind installs anything outside the project. opencode lanes (`kind:
opencode`, no Claude Code hooks) get obor's MCP server and its plugin (the file guard, the session context, and the
hooks' answers: a stop's block nudges a missing `task_done`) from each start's generated config (`OPENCODE_CONFIG`, under
`.obor/state/`), Claude and Codex from the plugin manifest and launch args. If an obor before v0.25 put a global
opencode registration on the machine, remove it by hand: the `mcp.servers.obor` entry of
`~/.config/opencode/opencode.json` and the `plugins/obor.js` link there (`$XDG_CONFIG_HOME` respected); restart
opencode agents after it.
Optional: offer a bug-triage agent (`agents.roles.triage` = a model ref like `<model>[:<effort>]`, then `obor setup role triage --new-tab triage`): the person reports bugs to it, it keeps `docs/BUGS.md` and hands fixes to the project's lead (`agents/triage.md`).

## Terminals
On any terminal: `project.terminal` names it (herdr, the default, hc or rex), obor serve reads it at its start (restart
serve after changing it: `obor stop`, the key, `obor start`; serve then drops its records of the old terminal's panes
and identifies them afresh), placement goes by the pane's kind as above, and `obor setup check` runs the terminal's
own check (`OK check: …` or `FAIL …`).
- **herdr:** workspaces, tabs and splits are herdr's; serve restores obor's panes itself after a herdr restart,
  replacing an agent herdr resumed there; `resume_agents_on_restore = false` under `[session]` in herdr's config.toml
  (optional, user-global) saves that double start. `obor setup check` has nothing of its own to check.
- **hc** (herdr-core, experimental, hc ≥ 0.1.21): obor lays herdr-app out like herdr (a space for the project, tabs
  `lead`, `lanes`, `roles`) and places each terminal by its labels. `obor setup check` reads hc's core environment and
  fails when it holds another terminal's identity (`HERDR_`, `OBOR_`, `REX_`, a Claude session's markers: a core
  started from an obor pane, another terminal or a Claude session gives every terminal that one's identity): run
  `hc server restart --fresh` from a shell outside any pane. obor serve starts a core that is down from a scrubbed
  environment and never restarts or stops one that runs. Codex runs with `--no-daemon` and OpenCode with
  `--standalone` there (`obor config validate` refuses a model without them).
- **rex** (Rex, experimental): the project is a Rex session labelled with its name, a tab a window, a pane a terminal
  block; obor drives the server the `rex` CLI reaches by default. `obor setup check` (and `obor init`, serve's
  connect) fails when no `rex` is on `PATH`: put `/Applications/Rex Beta.app/Contents/Helpers/Rex Server.rex/Contents/MacOS`
  there (obor never runs it from the bundle itself). A block inherits the Rex server's whole environment, so every
  block obor makes drops the identity names of other terminals (`OBOR_`, `HERDR_`, `HC_TERMINAL_`, a Claude session's
  markers) before its own; Rex's own `REX_BLOCK` and `REX_SESSION` stay.
