# Roster (current instances)

Update when an agent is started, renamed, re-modelled or retired. herdr workspace `obor-kit` (w5); find panes with
`herdr pane list --workspace w5` (or `herdr agent list`), lane states with `obor status`. Agent names are
global across herdr workspaces → prefix `ob-`. Tabs ([lead.md](lead.md)): `lead` (the lead pane alone), `opus lanes`
(L-01..L-04: every lane runs the model its dispatch picks, T-765), `roles` (integrator, reviewer, advisor; 2×2 grid),
`system` (obor serve). Max 4 panes per tab (`limits.max_panes_per_tab`); no per-model caps (a model out of quota:
`obor pause --model <name>`); retire panes idle > 1 h.

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
| ob-d-02 | [worker](worker.md) | opencode, DeepSeek V4.1 Flash (`deepseek`, as ob-d-01; max 2) | small lanes (w5:p1V) | `~/Work/obor-kit-wt/D-02` |
| ob-c-01 | [worker](worker.md) | Codex `gpt-6-luna` high (`luna`) | small lanes (w5:p6) | `~/Work/obor-kit-wt/C-01` |
| ob-d-01 | [worker](worker.md) | opencode, DeepSeek V4.1 Flash (`deepseek`: `--auto --standalone`; max 2); guard + session context via obor's opencode plugin (`obor setup opencode-plugin` once per user, then restart opencode; opencode 2.x has no stop hook, so nothing blocks it before `task_done`) | small lanes (w5:p1N) | `~/Work/obor-kit-wt/D-01` |
| obor-kit-serve | dispatch + merge loop | `obor serve` | system (w5:p1F) | main checkout |

Role not running now: `deep_dive` (`roles.deep_dive`: type `opus`, worktree `~/Work/obor-kit-wt/deep-dive`).

## Start commands
Panes and agents come from `obor setup`; a model ref in `.obor/config.json` (`roles.<role>`, `difficulty`,
`<model>[:<effort>]`) gives the kind, the model and the effort, so a model change is a config edit, not a new command:
```
obor setup lane L-05                              # the next lane: worktree + lanes 5
obor setup pane L-01 --new-tab workers            # first lane of a tab; the next ones: --tab <tab-id>
obor setup role integrator --tab <tab-id>         # lead | integrator | reviewer | deep_dive | advisor | triage
obor setup opencode-plugin                        # once per user: D-01's guard + context; restart opencode
obor restart ob-advisor                           # relaunch an agent from its configured model
```
`obor setup mcp` shows whether the `job_next` / `task_done` server is registered for each agent kind. A job is fetched,
not typed: `obor dispatch <LANE> <T-xx>` (a request to obor serve) sends a one-line wake that names the job id and
asks for the `job_next` tool; the agent calls `job_next` and finishes only with `task_done`, its report the argument (no done
file), which frees the lane and starts serve's gate → review → merge.
New lane: `obor setup lane L-NN` (the next one; it raises `lanes`).

## Cost
Model rules: [lead.md](lead.md) "Models and limits".
