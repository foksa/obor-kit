## Rules for coding agents in an obor swarm
<!-- Paste into the project's AGENTS.md / CLAUDE.md. -->

This repo runs an obor swarm (`{{project}}`). If you are an obor role (worker, integrator, reviewer …), your role text
is the obor kit's `templates/agents/<role>.md` plus `docs/agents/<role>.md` here: the session start or your prompt
names both; read them before acting. The lead runs `/obor:lead`.

- **Speak up.** Noticed something systemic (complexity creep, a fragile dependency, effort out of proportion, a rule
  that keeps causing friction, a bug pattern, your own mistake)? Say so with a recommendation, unprompted (workers: a
  `Concerns:` line in the `task_done` report).
- **Never `pkill`/`killall` by pattern**: stop only processes you started, by PID. Never `git push`.
- **Text says what is, never what was**: docs, comments and reports describe the current behaviour, with no previous
  states ("it used to…", "no longer…") and no task or review ids.

<!-- fill in: project-specific rules — runtime constraints, layout, how to run the checks, protected files. -->
