# Agent swarm — how it works

The role texts of an obor swarm. They ship with the kit and are read in place: a project's own `docs/agents/<role>.md`
(`agents.docs_dir`) holds only what differs there, under a pointer line. The session start prints both paths for obor
roles (`obor: role text: …`); the work job carries them. A role runs only under obor: `obor setup role <name>` (or
`obor restart <agent>`), then `obor brief`.

| Text | Role | Typical model |
|---|---|---|
| the `obor:lead` skill ([lead.md](lead.md) points there) | Specs, dispatch, decisions, reviews, user contact | strongest Claude |
| [integrator.md](integrator.md) | Answers serve's review and resolve jobs: checks finished lanes against their specs, resolves conflicts (serve merges and publishes) | Claude pane |
| [worker.md](worker.md) | Implements one task in its own worktree (lane) | a model from `agents.difficulty` |
| [triage.md](triage.md) | Bug inbox: records, dedupes, sorts, probes and hands bugs to the project's lead (optional; writes `docs/BUGS.md`) | Claude pane |
| [reviewer.md](reviewer.md) | Batch code review + verification in the same session; a role pane in its own worktree, never a lane | a different model family if possible |
| [advisor.md](advisor.md) | Design advisor: writes its advice or plan under `docs/research/` or `docs/plan/` (optional) | strongest available |
| [researcher.md](researcher.md) | Research for the lead: a findings doc under `docs/research/` (optional) | a mid model |
| [deep-dive.md](deep-dive.md) | Whole-codebase audit at milestone end; a role pane in its own worktree, never a lane | strongest Claude pane |
| [gotchas.md](gotchas.md) | Tool quirks (herdr, Claude Code, Codex, bash) | — |

Who runs now, and in which lane: `obor status` (or obor web); there is no hand-kept roster.

Advisor, researcher and triage are the kit's default custom roles (T-801): a project adds its own role the same way,
with one `agents.roles.<name>` row in `.obor/config.json` (a model ref, or `{"model": …, "write": ["<dir>/", "<file>"], "verbs": […]}`) and
its doc `docs/agents/<name>.md` (what it does, what it writes). A custom role runs in the main checkout, reads git
only, writes only its `write` list, never pushes; the lead places it with `obor setup role <name>` and briefs it with
`obor brief`. Its `verbs` (T-1039) are the lead's calls serve admits it to (`spec_set`, `dispatch`, `watch`, `send`,
`brief`): an integration's role (the kit's `docs/agents/integrations.md`).

## Communication
```
user ──► lead ──spec──► worker (lane) ──task_done──►   obor serve ──review job──► integrator ──accept──► main
          ▲                                              gate/publish               │ open questions / deviations
          └─────────────────────────────────────────────────────────────────────────┘
          │  ──brief (obor brief; --fresh stops the running one first)──► reviewer / deep dive ──RV-xx.md / DD-xx.md──► lead ──fix tasks──► workers
          └─ ──question─► advisor ──answer (saved under the research dir)
```
- Only obor serve merges: it publishes what the integrator accepted (`docs/state-model.md`). The lead commits its own
  docs by explicit path.
- Shared state lives in files: `BACKLOG.md`, `docs/decisions.md`, the reviews and research dirs. Machine state (the
  lane rows, the open tasks' specs, the pause) is in `.obor/state/obor.db`; read it with `obor status`.
- Agent names are `<project.agent_prefix>-<id>`: herdr agent names are global across workspaces.

## What the hooks enforce (expect the denials)
- Everyone: `git push`, `pkill`/`killall` by pattern, `git commit -a` in the main checkout.
- Workers: git writes, Edit/Write outside their own lane (another worktree, the main checkout, another obor project).
- Every non-lead role: protected files.
- A custom role (advisor, researcher, triage, the project's own): writes only its `agents.roles.<name>.write` list, git reads
  only.
- Workers can't stop without `task_done` (once per stop: asking works); the lead and the integrator stop freely (no
  nudges). `obor pause "<why>"` holds obor serve's dispatches (`--clear` ends it).

## Changing a role
One place per rule: generic rules here, project rules in the project's doc. When something goes wrong, change the rule
itself (git log keeps the history; there are no tuning logs). The user approves changes to how a role works.
