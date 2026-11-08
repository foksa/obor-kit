# Upgrading an obor project from v0.30 to v0.31

## v0.31.0 (a lane's env is the project's config, not obor's code)

A lane's environment is now what obor always exports as facts — `OBOR_LANE_CPUS` (the lane's `slots.lane_cpus`,
default 2) and, in a project with `branches.promote` set, `OBOR_PROMOTION=1` — plus the project's own `slots.lane_env`.
Obor no longer writes a Go policy into every lane: `GOMAXPROCS` and `GOFLAGS` (with `-p` and `-trimpath`) are gone from
the default env, and so are `CHECK_SHORT` and `CHECK_DIRECT`, which a lane test used to set.

**A Go project loses its lane CPU budget and `-trimpath` on upgrade unless it adds the one line below.** A project
whose lanes run Go commands wants it (obor-kit's own `.obor/config.json` has it):

```json
"slots": {
  "lane_env": {
    "GOMAXPROCS": "{cpus}",
    "GOFLAGS": "-p={cpus} -trimpath"
  }
}
```

`{cpus}` is the lane's `slots.lane_cpus` (the same value `OBOR_LANE_CPUS` carries); a `GOFLAGS` already in a shell is
replaced by the config's value, never merged: the config line is the policy.

A project that used the `CHECK_SHORT`/`CHECK_DIRECT` pair in its `Makefile` (obor-kit's own, and any fork of it) maps
`OBOR_PROMOTION=1` to the same behaviour instead: `-short` on `check`, `check-readers` and `check-web`, and `--direct`
on its affected-packages list. A project with `branches.promote` set gets `OBOR_PROMOTION=1` in a lane test's env; the
promotion's own run and the nightly never carry it.

Re-run the installer, then in every project:

```sh
obor config validate      # names a lane_env name or placeholder it refuses
obor serve restart
```

Then restart the running agents (`obor stop --force "v0.31"` and `obor start`): a lane's worker picks up its lane's env
at its next start.
