# Templates

`obor init` copies these into a project, replacing the placeholders below.
Files written once; after that the project owns them.

| Template | Becomes | For |
|---|---|---|
| `BACKLOG.md` | `BACKLOG.md` | The lead's working file, prose only (obor writes nothing there): Handoff, Owner decisions pending, Open, Concerns, Ideas. Running and waiting tasks come from `obor status`. |
| `decisions.md` | `docs/decisions.md` | Owner decisions on record, newest first. |
| `task-spec.md` | the spec skeleton (`obor spec set`) | What the lead fills for every task. A protected file the task may edit goes under `## Protected files this task may edit` (exact path). |
| `agents/*.md` | read in place from the kit | The role texts (lead → the `obor:lead` skill, integrator, worker, reviewer, advisor, deep-dive, triage) and `gotchas.md`. `obor init` writes a short project stub per role into the agents docs dir (title, pointer line, the text's **This project** prompts) and copies `roster.md` (who runs now); `README.md` stays in the kit. |
| `worker-prompt.txt` | the work job's intro (`obor serve` stores it, `job_next` hands it out; read in place from the kit) | `{{task}}` and `{{base}}`; the worker's role texts follow it in the job. |
| `AGENTS-section.md` | a block pasted into the project's `AGENTS.md` / `CLAUDE.md` | The rules every coding agent in the swarm obeys. |

`config.example.json` and `local.example.json` are separate.

## Placeholders
| Placeholder | Config source | Meaning |
|---|---|---|
| `{{project}}` | `name` | Display name of the project |
| `{{prefix}}` | `prefix` | Agent-name / tab prefix (`{{prefix}}-o1`) |
| `{{lanes_dir}}` | `lanes_dir` | Directory holding the lane worktrees |

Anything else is literal: `docs/reviews/`, `docs/research/` and `AGENTS.md` appear as the defaults (`paths.reviews`
can differ; edit the copied docs if so).
