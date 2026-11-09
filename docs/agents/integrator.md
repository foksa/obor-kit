# Role: integrator — obor-kit specifics

Role text: `templates/agents/integrator.md` in the obor kit (the session start names its path).

- ob-int (`roles.integrator`) reviews every lane; the lead never merges by hand. A resolve job's conflict is in
  `../obor-kit-wt/merge` (`<lanes_dir>/merge`, the merge scratch). Merge checks: the merge classes in
  `.obor/config.json` (the gate picks them).
- A job that does not arrive: `obor setup mcp` shows whether the server is registered per agent kind.
- The layout doc is `docs/module-map.md`.
