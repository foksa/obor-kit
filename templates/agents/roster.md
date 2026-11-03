# Roster (current instances)

Update when an agent is started, renamed, re-modelled or retired. Find panes with `herdr agent list`; lane states with
`obor status`. Agent names are global across herdr workspaces → prefix `{{prefix}}-`. Pane and lane limits are in
`.obor/local.json`; retire panes idle > 1 h.

| Name | Role | Kind / model | Tab | Lane (cwd) |
|---|---|---|---|---|
| lead | [lead](lead.md) + integrator | Claude Code, <model> | 1 | main checkout |
| {{prefix}}-l-01 | [worker](worker.md) | the model its dispatch picks | workers | `{{lanes_dir}}/L-01` |
| {{prefix}}-triage | [triage](triage.md) (optional) | <kind / model / effort> | main | main checkout |

## Start commands
Split a pane with role env and cwd, then start the agent in it (`obor setup pane` / `obor restart` do both; a model
ref in `.obor/config.json`, `opus:high`, gives the kind, the model and the effort; `obor dispatch` starts a lane's
worker fresh with the model its spec's level picks):
```
herdr pane split <pane> --direction right|down --cwd <lane> --env OBOR_ROLE=worker --env OBOR_LANE=<LANE>
herdr agent start {{prefix}}-l-01 --kind claude --pane <new-pane> -- --permission-mode auto --model opus --effort medium \
  --settings '{"autoMemoryEnabled":false,"remoteControlAtStartup":false}'
```
`OBOR_ROLE` (`lead | integrator | worker | reviewer | advisor`) is what the hooks use to pick the rules; triage runs with `OBOR_AGENT` only.
opencode lanes get obor's hooks only through obor's opencode plugin: `obor setup agents` once per user, then
restart their opencode.
New lane: `obor setup lane L-NN` (the next one: it raises `lanes` in the config and adds the worktree).

## Cost
<!-- fill in: the user's rules for which model gets which work, usage limits, which agents are expensive. -->

## This project
<!-- fill in: the actual agents, panes and lanes running now. -->
