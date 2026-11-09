# Role: worker — obor-kit specifics

Role text: `templates/agents/worker.md` in the obor kit (the work job carries its text; the session start names its
path).

- Tests of a changed file: `go test ./<pkg>/...` of its package (run a test by name with `-run '^TestX$'` only when
  you changed it) (a bash script such as `githooks/*`, `bin/obor` or the
  kit's `scripts/*`/`install.sh`: the Go package whose tests run it: `./internal/setup` for `githooks/*`, `./cmd/obor`
  for `bin/obor`, `go test -tags release ./tests/release` for the kit's release tooling, T-902: behind the release tag,
  T-1001).
- Unit and integration tests (owner 11-08): a plain `go test` runs a package's unit tests; tests that run real git, a
  real socket or a built binary are behind `//go:build integration` and the lane test runs them. That tag is the only
  split: `-short` and `testkit.Slow` are gone (T-1056), so the lane's check runs a package's whole suite. You changed
  an adapter's git or I/O code (a file with an `_integration_test.go` beside it): also run that package's integration
  tests once, `go test -tags integration -run '<the tests of your change>' ./<pkg>/`. A new test that needs real git
  goes in an `_integration_test.go` file.
- Before `task_done` (owner 2026-11-05): one `go test` of the packages you changed (the line above), nothing
  more. Never `obor run check_affected` or `obor run check` in a lane: the gate runs the class check once (a break in
  an importer comes back as a sendback in the same attempt). This replaces the role text's "one run of the gate's
  check for your class".
- Your tests run on the lane's CPU budget (`lanes.cpus`, exported as `OBOR_LANE_CPUS` and, through this
  project's `lanes.env`, as `GOMAXPROCS` and `GOFLAGS=-p=<n>`): no `-p` or `GOMAXPROCS` overrides.
- `obor` in a lane runs main's build (self-host, T-240); for the lane's own build use `OBOR_NO_SELF_HOST=1 ./bin/obor`.
- No per-file list is enforced (T-496): a small wiring change is fine.
