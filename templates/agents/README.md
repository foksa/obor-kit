# Agent swarm — how it works

The role texts of an obor swarm. They ship with the kit and are read in place: a project's own `docs/agents/<role>.md`
(`paths.agents`) holds only what differs there, under a pointer line. The session start prints both paths for obor
roles (`obor: role text: …`); the work job carries them; `/obor:role <name>` reads them for a role taken by hand.

| Text | Role | Typical model |
|---|---|---|
| the `obor:lead` skill ([lead.md](lead.md) points there) | Specs, dispatch, decisions, reviews, user contact | strongest Claude |
| [integrator.md](integrator.md) | Answers serve's review and resolve jobs: checks finished lanes against their specs, resolves conflicts (serve merges and publishes) | Claude pane |
| [worker.md](worker.md) | Implements one task in its own worktree (lane) | any lane type from the config |
| [triage.md](triage.md) | Bug inbox: records, dedupes, sorts, probes and hands bugs to the project's lead (optional) | Claude pane |
| [reviewer.md](reviewer.md) | Batch code review + verification in the same session; a role pane in its own worktree, never a lane | a different model family if possible |
| [advisor.md](advisor.md) | Read-only design advisor | strongest available, plan mode |
| [deep-dive.md](deep-dive.md) | Whole-codebase audit at milestone end; a role pane in its own worktree, never a lane | strongest Claude pane |
| [gotchas.md](gotchas.md) | Tool quirks (herdr, Claude Code, Codex, bash) | — |

Who runs now, and in which lane: `obor status` (or obor web); there is no hand-kept roster.

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
- Agent names are `<prefix>-<id>`: herdr agent names are global across workspaces.

## What the hooks enforce (expect the denials)
- Everyone: `git push`, `pkill`/`killall` by pattern, `git commit -a` in the main checkout.
- Workers: git writes, Edit/Write outside their own lane (another worktree, the main checkout, another obor project).
- Every non-lead role: protected files.
- Workers can't stop without `task_done` (once per stop: asking works); the lead and the integrator stop freely (no
  nudges). `obor pause "<why>"` holds obor serve's dispatches (`--clear` ends it).

## Changing a role
One place per rule: generic rules here, project rules in the project's doc. When something goes wrong, change the rule
itself (git log keeps the history; there are no tuning logs). The user approves changes to how a role works.
