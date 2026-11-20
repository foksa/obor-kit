# Role: integrator — obor-kit specifics

Role text: `templates/agents/integrator.md` in the obor kit (the session start names its path).

- ob-int (`agents.roles.integrator`) reviews every lane; the lead never merges by hand. A resolve job's conflict is in
  `../obor-kit-wt/merge` (`<lanes.dir>/merge`, the merge scratch). Merge checks: the merge.by_path rules in
  `.obor/config.json` (the gate picks them).
- A job that does not arrive: `obor setup mcp` shows whether the server is registered per agent kind.
- The layout doc is `docs/module-map.md`.
- Tests at accept: the spec's `Counts:` line against the diff (`git diff <base>.. | grep -c '^+func Test'`, and `^-func
  Test` for deleted); a task with no intended behaviour change that adds tests beyond a pure function it extracts, or
  a test that repeats a rule's rows at a higher level (AGENTS.md "One level per behaviour"), is a sendback.
