# Roster (current instances)

Update when an agent is started, renamed, re-modelled or retired. herdr workspace `obor-kit` (w5); find panes with
`herdr pane list --workspace w5` (or `herdr agent list`), lane states with `obor status`. Agent names are
global across herdr workspaces → prefix `ob-`. Tabs ([lead.md](lead.md)): `lead` (the lead pane alone), `opus lanes`
(O-01..O-04), `small lanes` (S/H/C/D), `roles` (integrator, reviewer, advisor; 2×2 grid), `system` (obor serve). Max 4 panes per tab (`limits.max_panes_per_tab`), max 4 Opus lane agents and 1 opencode agent
(`lane_types.*.max`); retire panes idle > 1 h.

| Name | Role | Kind / model | Tab (pane) | Lane (cwd) |
|---|---|---|---|---|
| ob-lead | [lead](lead.md) | Claude Code, Opus, effort medium (`roles.lead`, type `opus`) | lead (w5:p1R) | main checkout `~/Work/obor-kit` |
| ob-int | [integrator](integrator.md) | Claude Code, Opus, effort medium (`roles.integrator`, type `opus`; was Sonnet — user 2026-10-13: Opus for complex jobs) | roles (w5:pZ) | main checkout |
| ob-fable | [advisor](advisor.md) | Claude Code, Fable, auto mode — writes only its answer file (`agents.ob-fable`, type `fable`; `obor restart ob-fable` restores it) | roles (w5:p1G) | main checkout |
| ob-terra | [reviewer](reviewer.md) | Codex `gpt-6.1-terra` high (`terra`; owner 2026-10-31, was `gpt-6.1-sol`) | roles (w5:pA) | `~/Work/obor-kit-wt/review` (detached) |
| ob-o-01 | [worker](worker.md) | Claude Code, Opus, effort medium (`opus`) | opus lanes (w5:p2) | `~/Work/obor-kit-wt/O-01` |
| ob-o-02 | [worker](worker.md) | Claude Code, Opus, effort medium (`opus`) | opus lanes (w5:p5) | `~/Work/obor-kit-wt/O-02` |
| ob-o-03 | [worker](worker.md) | Claude Code, Opus, effort medium (`opus`) | opus lanes (w5:p8) | `~/Work/obor-kit-wt/O-03` |
| ob-o-04 | [worker](worker.md) | Claude Code, Opus, effort medium (`opus`) | opus lanes (w5:pB) | `~/Work/obor-kit-wt/O-04` |
| ob-s-01 | [worker](worker.md) | Claude Code, Sonnet, effort medium (`sonnet`) | small lanes (w5:p4) | `~/Work/obor-kit-wt/S-01` |
| ob-h-01 | [worker](worker.md) (simple/bulk) | Claude Code, Haiku (`haiku`) | small lanes (w5:p1M) | `~/Work/obor-kit-wt/H-01` |
| ob-c-01 | [worker](worker.md) | Codex `gpt-6-luna` high (`luna`) | small lanes (w5:p6) | `~/Work/obor-kit-wt/C-01` |
| ob-d-01 | [worker](worker.md) | opencode, DeepSeek V4.1 Flash (`deepseek`: `--auto --standalone`; max 1); guard + session context via obor's opencode plugin (`obor setup opencode-plugin` once per user, then restart opencode; opencode 2.x has no stop hook, so nothing blocks it before `task_done`) | small lanes (w5:p1N) | `~/Work/obor-kit-wt/D-01` |
| obor-kit-serve | dispatch + merge loop | `obor serve` | system (w5:p1F) | main checkout |

Role not running now: `deep_dive` (`roles.deep_dive`: type `opus`, worktree `~/Work/obor-kit-wt/deep-dive`).

## Start commands
Panes and agents come from `obor setup`; the lane type's `kind` and `args` in `.obor/config.json` are the launch line,
so a model change is a config edit, not a new command:
```
obor setup lane S-02 sonnet                       # worktree + lanes.S-02 (type from lane_types)
obor setup pane O-01 --new-tab "opus lanes"       # first lane of a tab; the next ones: --tab <tab-id>
obor setup role integrator --tab <tab-id>         # lead | integrator | reviewer | deep_dive | advisor | triage
obor setup opencode-plugin                        # once per user: D-01's guard + context; restart opencode
obor restart ob-fable                             # relaunch an agent from its configured type
```
`obor setup mcp` shows whether the `job_next` / `task_done` server is registered for each agent kind. A job is fetched,
not typed: `obor dispatch <LANE> <T-xx>` (a request to obor serve) sends a one-line wake that names the job id and
asks for the `job_next` tool; the agent calls `job_next` and finishes only with `task_done`, its report the argument (no done
file), which frees the lane and starts serve's gate → review → merge.
New lane: `obor setup lane <LANE> <type>`, or by hand `git worktree add ~/Work/obor-kit-wt/<LANE> -b task/<id> main`
(or detached) plus a `lanes` entry in the config.

## Cost
Model rules: [lead.md](lead.md) "Models and limits".
