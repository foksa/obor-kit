# Role: worker — obor-kit specifics

Role text: `templates/agents/worker.md` in the obor kit (the worker prompt and the session start name its path).
Runtime and test rules: `AGENTS.md`.

- Tests of a changed file: `go test ./<pkg>/...` of its package (a bash script such as `githooks/*` or `bin/obor`:
  the Go package whose tests run it: `./internal/setup` for `githooks/*`, `./cmd/obor` for `bin/obor`).
- Tiers (T-350): iterate with the focused tests above, test cache on (no `-count=1`), never `-race`, a `-count=N` loop
  or the whole suite. Submit with one `obor run check_affected` (the gate's class check on this tree; `obor run` exports
  `OBOR_CHANGED_FILES`, T-349); when it ends green on the tree you submit, the gate reuses it. Never `obor run check`
  (this repo's full gate), never a second run.
- A Codex lane's own focused `go test` sets `GOCACHE=${TMPDIR:-/tmp}/obor-go-build` when the default cache is not
  writable (never `commands.check` in a lane).
- `obor` in a lane runs main's build (self-host, T-240); for the lane's own build use `OBOR_NO_SELF_HOST=1 ./bin/obor`.
- Jobs by fetch (T-375, T-437): first `job_next` (the wake is only a pointer to it); finish with `task_done`, your
  report its argument (no done file). No per-file list is enforced (T-496): a small wiring change the spec did not
  mention is fine, named in your report.
