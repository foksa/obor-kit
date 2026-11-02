# Roster (current instances)

Update when an agent is started, renamed, re-modelled or retired. Find panes with `herdr agent list`; lane states with
`obor status`. Agent names are global across herdr workspaces → prefix `{{prefix}}-`. Pane and lane limits are in
`.obor/local.json`; retire panes idle > 1 h.

| Name | Role | Kind / model | Tab | Lane (cwd) |
|---|---|---|---|---|
| lead | [lead](lead.md) + integrator | Claude Code, <model> | 1 | main checkout |
| {{prefix}}-o1 | [worker](worker.md) | <kind / model / effort> | workers | `{{lanes_dir}}/O-01` |
| {{prefix}}-triage | [triage](triage.md) (optional) | <kind / model / effort> | main | main checkout |

## Start commands
Split a pane with role env and cwd, then start the agent in it (the lane types in `.obor/config.json` give the args;
a role's `type` may name an effort, `opus:high`, in place of the args' own; `obor dispatch` resets and prompts an
already running lane agent):
```
herdr pane split <pane> --direction right|down --cwd <lane> --env OBOR_ROLE=worker --env OBOR_LANE=<LANE>
herdr agent start {{prefix}}-o1 --kind claude --pane <new-pane> -- --model opus --effort medium \
  --permission-mode auto --settings '{"autoMemoryEnabled":false,"remoteControlAtStartup":false}'
```
`OBOR_ROLE` (`lead | integrator | worker | reviewer | advisor`) is what the hooks use to pick the rules; triage runs with `OBOR_AGENT` only.
opencode lanes get obor's hooks only through obor's opencode plugin: `obor setup agents` once per user, then
restart their opencode.
New lane: `git worktree add {{lanes_dir}}/<LANE> -b task/<id> <main-branch>` (or detached), add it to `lanes` in the config.

## Cost
<!-- fill in: the user's rules for which model gets which work, usage limits, which agents are expensive. -->

## This project
<!-- fill in: the actual agents, panes and lanes running now. -->
