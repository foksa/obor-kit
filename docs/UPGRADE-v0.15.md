# Upgrading a herd project from v0.14 to v0.15

v0.15 deletes the v0.14 transition that let a v0.12/v0.13 project and a v0.14 one `herd send` to each other:
- `herd serve` no longer listens on the kit's old socket (`~/.herd/serve/<kit name>-<hash>/serve.sock`), and its
  `serve.flock` names just the serve's pid.
- `herd send` goes only to `<target root>/.herd/state/serve.sock`: a target without one has no serve there and the
  send fails.

Every project that sends to another must run v0.14 or later (one `herd serve` per project).
The old kit state `~/.herd/serve/` can be deleted.
`herd lead` starts fresh; `--resume` to continue (T-553; `--fresh` still parses).

Also in v0.15 (no action beyond the pane restart):
- The worker Stop hook lets a worker stop right after a `Blocked:` report, and an old report no longer lets a later
  attempt stop (T-558, T-560): restart each worker pane once (hooks are read at session start).
- `herd version` and `herd whoami` print what they mean; main's build names the newest release (T-554, T-559); a
  dirty checkout at a tag says so (T-561).
- `herd setup … --tab` splits the largest pane (T-557); the `paths.tasks` WARN is accurate (T-555).
- No store schema change: serves restart one by one.
