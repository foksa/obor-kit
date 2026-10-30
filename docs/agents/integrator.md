# Role: integrator — obor-kit specifics

Role text: `templates/agents/integrator.md` in the obor kit (the session start names its path).

- ob-int (`roles.integrator`) reviews every lane; the lead never merges by hand. `obor serve` (the `system` tab, one
  per kit install — each kit has its own registry, lock and socket, T-415) is the only merger: it dispatches, gates
  and publishes, and ob-int only answers its review and resolve jobs with the `accept` / `sendback` / `reject` /
  `resolved` / `hold` tools (`hold` when the owner must decide, not `reject`; T-470: no CLI forms, a refusal comes
  back as the tool's text); a resolve job's conflict is in `../obor-kit-wt/merge` (`merge.scratch_dir`). Merge checks:
  the merge classes in `.obor/config.json` (the gate picks them).
- Tiers (T-350): no test runs by hand; the gate is the test run (never `obor run check` before answering a job).
- A job arrives as a constant wake naming the job id: call `job_next`, answer with the `accept` / `sendback` /
  `reject` / `resolved` / `hold` tools (T-375; T-437: every job is fetched, no `mcp.jobs` switch). `obor setup mcp`
  shows whether the server is registered per agent kind.
