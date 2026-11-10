# Upgrading an obor project from v0.35 to v0.36

## v0.36.0 (role worktrees get the lane setup; a blocked worker is a needs-you at once)

Serve now runs `lanes.ready`, then `lanes.setup` when it fails, in a role worktree (reviewer, deep dive) each time it
moves that worktree to a head, as it does for a lane: a project whose tests need installed dependencies
(`node_modules`) no longer has to install them there by hand. A project with no `lanes.ready` sees no change.

A lane worker that herdr reports `blocked` (a question tool, a permission or approval dialog) is a needs-you as soon
as serve sees it, with the pane's last lines as the question; answer it in the worker's pane.

`obor review close "<summary>" --force` closes a review whose reviewer never wrote its file, when it covers no task.

Replay of a log's old record shapes changed: before upgrading, a project must have run the one-off record delete of
the v0.35 deep dive (obor-kit and axis-alies have).

```sh
# nothing to do
```
