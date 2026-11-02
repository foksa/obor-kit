# Self re-exec advice (ob-fable, 2026-11-02)

## Recommendation

Do it as one helper with three callers, and drop two wave items: web config reload (already per request,
`internal/view/load.go:19`) and "ship restarts everything" (owner's machine only, see 5).

Helper `internal/selfexec`: `Candidate(kit)` resolves the binary the shim would exec *now* (its rule,
`bin/obor:238-242`: `<kit>/dist/obor` if executable, else `~/.obor/bin/obor-<VERSION>-<os>-<arch>`); `Changed()`
is true when that file exists, is executable, is a different dev/inode from `os.Executable()`, its mtime is ≥5 s
old, and `<candidate> version` exits 0 within 2 s (the shim's stale-binary probe, `bin/obor:107-128`); `Exec(args)`
is `syscall.Exec` of `<kit>/bin/obor <args>`. Kit root: `project.KitRoot` (`internal/project/project.go:178-187`),
so a lane build never counts.

Serve polls `Changed()` on its 30 s tick (`internal/app/serve.go:156-158`) and takes the self-restart path that
exists (`internal/app/steps.go:79-106` → `cmd/obor/service.go:91-107`). Monitor and web poll, then `Exec` with
their own args. Nothing in a plist, nothing in ship.

## 1. Safe point for serve

Reuse the self-restart's: "any time, gracefully" (`docs/design.md:291-308`): `stop()` cancels the context, gates end
their process groups, effects are waited for (`internal/app/loop.go:243`), a finished step's result is in the log
and replays; a cut gate re-runs. That is safety. The watch adds only *cost*: a cut kit gate is up to 20 minutes.
So prefer idle: no running effect (`p.running` zero, `steps.go:30`), no nightly, no admitted verdict; never idle,
go after 10 minutes. Set `p.restarting` meanwhile so status and monitor show it (`status.go:156`,
`monitor_render.go:152`).

## 2. Detecting "newer"

Not the version string (`v0.18.0-dev+sha-dirty` has no order; a rollback must restart too). Not VERSION alone:
`install.sh:34-46` checks the kit out at the new tag *before* the binary is fetched. A different inode of an
executable, probed candidate is the test, and it is self-stabilising: after the re-exec the executable *is* the
candidate, so no loop. Flap during an install: both writers are atomic renames (`.tmp.$$` then `mv`,
`bin/obor:97-99`; `go build -o`), the 5 s quiet period and the probe cover a partial file, and a kit mid-checkout
names a binary that is missing: no trigger until `obor version` fetched it. A candidate that starts and dies is
launchd's respawn loop as today; the probe is the guard.

## 3. launchd, pane, dev checkout

launchd: nothing new. The plist runs `<kit>/bin/obor serve` (`internal/service/service.go:88-92`), the shim
resolves the binary at every spawn, so exit 0 + KeepAlive (T-656) already lands on the new build.

Pane, and Linux: `serveRestarter` refuses today (`service.go:101-103`). Instead `Exec` after `served` closes
(`cmd/obor/serve.go:128`): Go fds are CLOEXEC, so the flock is released; the socket file stays (no defers on exec)
and serve removes a leftover once it holds the lock (`serve.go:84`). Same pid, same pane. This also makes
`restart_serve` work off launchd.

Dev checkout: a launchd serve whose candidate is `dist/obor`; `after_merge` plus `restart_serve` cover merges. The
watch adds one case, an owner's hand `make build` in the main checkout, dirty or not. Accept it, logging the stamped
version.

## 4. Monitor and web: re-exec, not reload

The binary is what goes stale; config reload exists (`cmd/obor/monitor.go:171-186`; web per request). Both are
stateless readers. Monitor: `s.leave()`, exec, the new one re-enters the alternate screen (one flicker); poll on its
2 s redraw. Web: exec with `--listen` pinned to the bound address (`ln.Addr()`, `cmd/obor/web.go:47`), else an unset
port changes and the owner's URL dies; the long poll reconnects. Poll every 30 s.

## 5. Ship alone? No

Ship ends in a printed line (`scripts/ship:142-157`) and could run `obor serve restart` per project, but it runs only
on the owner's machine: adopters upgrade by `install.sh` (`docs/design.md:400-404`), and monitors sit in herdr panes
ship cannot address. The watch removes the class everywhere; ship's last line becomes "serves and monitors follow
within a minute".

## Tasks

1. `internal/selfexec`: Candidate, Changed, Exec; tests with temp kits (inode change, missing binary, failing probe).
2. serve: tick watch, idle rule, pane `Exec`; status shows the pending restart. Tests beside `selfrestart_test.go`.
3. monitor and web re-exec (web pins its port).
4. design.md paragraph under self-restart; ship's last line; drop the web-reload item.

Flag: a candidate speaking another herdr protocol restarts into today's warning (`serve.go:95-100`), nothing new.
