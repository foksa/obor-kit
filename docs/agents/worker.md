# Role: worker — obor-kit specifics

Role text: `templates/agents/worker.md` in the obor kit (the work job carries its text; the session start names its
path).

- Tests of a changed file: `go test ./<pkg>/...` of its package (a bash script such as `githooks/*` or `bin/obor`:
  the Go package whose tests run it: `./internal/setup` for `githooks/*`, `./cmd/obor` for `bin/obor`).
- The gate's class check here is `obor run check_affected` (it exports `OBOR_CHANGED_FILES`, T-349).
- `obor` in a lane runs main's build (self-host, T-240); for the lane's own build use `OBOR_NO_SELF_HOST=1 ./bin/obor`.
- No per-file list is enforced (T-496): a small wiring change is fine.
