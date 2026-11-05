# Role: worker — obor-kit specifics

Role text: `templates/agents/worker.md` in the obor kit (the work job carries its text; the session start names its
path).

- Tests of a changed file: `go test ./<pkg>/...` of its package (a bash script such as `githooks/*`, `bin/obor` or the
  kit's `scripts/*`/`install.sh`: the Go package whose tests run it: `./internal/setup` for `githooks/*`, `./cmd/obor`
  for `bin/obor`, `./tests/release` for the kit's release tooling, T-902).
- Before `task_done` (owner 2026-11-05): one plain `go test` of the packages you changed (the line above), nothing
  more. Never `obor run check_affected` or `obor run check` in a lane: the gate runs the class check once (a break in
  an importer comes back as a sendback in the same attempt). This replaces the role text's "one run of the gate's
  check for your class".
- `obor` in a lane runs main's build (self-host, T-240); for the lane's own build use `OBOR_NO_SELF_HOST=1 ./bin/obor`.
- No per-file list is enforced (T-496): a small wiring change is fine.
