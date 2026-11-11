# Upgrading an obor project from v0.36 to v0.37

## v0.37.0 (obor init sets up every configured lane; no tight retry loop on a cut step)

`obor init` sets up every lane the config names (`lanes.count` or `lanes.list`), not only L-01 and L-02, and places
the first two lanes' panes from that same list: a project re-inited with four lanes no longer shows L-03 and L-04
`missing`, and a `lanes.list` without L-01 no longer stops init before serve.

A step whose effect is cut while serve keeps running (a canceled stop) is tried again after a growing delay, never
as fast as a lane scan.

`obor review close` commits the project's `BACKLOG.md` wherever it lives, and only when git tracks it.

Serve's log lines no longer interleave when serve and its app write at once.

```sh
# nothing to do
```
