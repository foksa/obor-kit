# Role: worker — obor-kit specifics

Role text: `templates/agents/worker.md` in the obor kit (the work job carries its text; the session start names its
path).

- Tests of a changed file: `go test -short ./<pkg>/...` of its package (the slow tests, `testkit.Slow`, run in the
  promotion's full suite; run one by name, `-run '^TestX$'` without `-short`, only when you changed it) (a bash script such as `githooks/*`, `bin/obor` or the
  kit's `scripts/*`/`install.sh`: the Go package whose tests run it: `./internal/setup` for `githooks/*`, `./cmd/obor`
  for `bin/obor`, `go test -tags release ./tests/release` for the kit's release tooling, T-902: behind the release tag,
  T-1001).
- Before `task_done` (owner 2026-11-05): one `go test -short` of the packages you changed (the line above), nothing
  more. Never `obor run check_affected` or `obor run check` in a lane: the gate runs the class check once (a break in
  an importer comes back as a sendback in the same attempt). This replaces the role text's "one run of the gate's
  check for your class".
- Your tests run on the lane's CPU budget (`slots.lane_cpus`, T-963: the env sets `GOMAXPROCS` and `GOFLAGS=-p=<n>`):
  no `-p` or `GOMAXPROCS` overrides.
- `obor` in a lane runs main's build (self-host, T-240); for the lane's own build use `OBOR_NO_SELF_HOST=1 ./bin/obor`.
- No per-file list is enforced (T-496): a small wiring change is fine.
