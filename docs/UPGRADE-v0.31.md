# Upgrading an obor project from v0.30 to v0.31

## v0.31.0 (OpenCode leaves the obor binary: a plugin kind of its own)

OpenCode is now a plugin kind (`docs/agents/kind-protocol.md`): the kit ships it as its own binary,
`obor-kind-opencode`, beside obor's (`make build` writes `dist/obor-kind-opencode`; the installer fetches
`~/.obor/bin/obor-kind-opencode-<v>-<os>-<arch>` with obor's binary). An OpenCode agent starts as before: the same argv,
env, generated config (`OPENCODE_CONFIG`), model switch and quit; `opencode/obor.js` is still its hook transport.

What obor knows about OpenCode without running it (its stop recipe, the `opencode run` the guard denies, its efforts,
its env vars) is the binary's manifest, cached per project. **Until a project caches it, OpenCode agents cannot start
there** (`kind opencode: no manifest cached (…): run obor setup agents`) and the guard does not know `opencode run`.

A config needs `kinds.opencode.command`; one without it still runs the kit's binary until v0.32.0, and `obor config
validate` warns while it is missing. Re-run the installer, then in every project:

```sh
obor setup agents        # caches the manifest and writes kinds.opencode.command = "obor-kind-opencode" (uncommitted)
obor config validate     # no WARN kinds.opencode.command
git commit -m "kinds.opencode.command" .obor/config.json
```

The config line, if you write it by hand (`obor-kind-opencode` names the kit's binary beside obor, not a file in the
project):

```json
"kinds": { "opencode": { "command": "obor-kind-opencode" } }
```

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

## v0.31.0 (the flake parser and the retest are the project's config, not obor's code)

Obor no longer reads `go test`'s output itself. The `--- FAIL: <test>` / `FAIL <pkg>` parsing that wrote
`.obor/state/flakes.jsonl` and that a red lane test's one rerun used, and the `OBOR_FAILED_TESTS` env var that rerun
read, are gone: the parser is `merge.flake_regex` and the retest is `commands.retest`'s `{tests}` placeholder.

**A Go project notices nothing on upgrade: no FAIL and no WARN.** With no `merge.flake_regex` the flake log stays
empty (so `obor flakes` never reports one) and a red lane test is never retested, so a real flake sends the worker back
once more. And a `commands.retest` that still runs `$OBOR_FAILED_TESTS` gets an empty value now, so its `-run ""` reruns
every test of the packages, not the failing ones. obor-kit's own `.obor/config.json` has the lines below.

`merge.flake_regex` is the pair obor reads a `merge.fail_grep` line with: the failed-test line (its name in the named
group `test`), then the failed-package line (the group `package`; a package is what a module path looks like, with a
slash or a dot, which is why a check's own `FAIL <what>` line is no name), and `merge.retest_join` separates the tests
in the template:

```json
"merge": {
  "flake_regex": [
    "^--- FAIL: (?P<test>\\S+)",
    "^FAIL\\s+(?P<package>[^\\s./][^\\s]*[./][^\\s]*)"
  ],
  "retest_join": "|"
}
```

`commands.retest` then says where the failing tests go: `{tests}` is their list, in the order the run named them,
joined by `merge.retest_join`, and the template carries the anchors (obor-kit's retest). `merge.fail_grep` still picks
the lines the pair reads; obor-kit's is `^--- FAIL|^FAIL\\s`:

```json
"commands": {
  "retest": {
    "cmd": "make -s check-retest TESTS='^({tests})$'",
    "timeout": 600
  }
}
```

and its `Makefile` target reads `$TESTS` where it read `$OBOR_FAILED_TESTS` (`go test -run "$$TESTS" $$OBOR_FAILED_PKGS`);
`OBOR_FAILED_PKGS` still carries the failing packages. A retest that is not `go test` passes `{tests}` to its runner
the same way.

A project whose checks print neither line, or whose retest needs no names, leaves these keys out: obor then parses
nothing and errors nothing. `obor config validate` refuses a `merge.flake_regex` that is not a pair of regexps, one
that does not compile, and one without its named group; a `merge.fail_grep` line matching neither regex is ignored.
