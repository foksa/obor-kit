# Upgrading an obor project from v0.28 to v0.29

## v0.29.0 (fix tasks link themselves, hard tasks reviewed on merge, close through serve, one automatic restart)
Upgrade from v0.28.x. Close an open review first (`obor review close`), and let a fix round's verify finish before
it (see below). Re-run the installer, then in every project:

```sh
obor config validate      # names a removed key you still set (restart_serve): delete it
obor serve restart
```

Then restart the running agents (an agent's MCP server is the binary it started with): `obor stop --force "v0.29"`
and then `obor start`; open tasks stay open and a working task's worker comes back with its job.

What changes:
- **Fix tasks link themselves:** `obor review fix` is gone. A fix spec carries the line `Fixes: RV-<n>[ <what to
  check>]` between its title and its first `##` heading; dispatching it links it to that open review (`obor dispatch`
  refuses one that names another review), and serve briefs the verify in the same reviewer session once the linked
  tasks merged. The free text is the reviewer's note for that task. The new serve ignores links made by
  `obor review fix`: if a review has fix tasks dispatched or merged but not verified when you upgrade, either let its
  verify finish and close it first, or after the upgrade add `Fixes: RV-<n>` to each of those specs (`obor spec set`);
  a task already verified is then verified once more.
- **Hard tasks are reviewed on merge** without a `Review: on merge` line (`Difficulty: hard` is enough); a spec with
  `Fixes:` never is (its verify is its review), and `obor spec set` refuses a spec with both lines.
- **`obor review close` runs in serve:** same command, output and refusals; serve commits the close and stops the
  reviewer at once. Only the lead may close. A failed commit leaves the review files and `BACKLOG.md` as they were.
- **`commands.after_merge.restart_serve` is gone.** A config that still sets it is refused naming the key: remove it.
  Binary following restarts serve on the new build an `after_merge` leaves behind, within a minute, and never while a
  `config_set` or review close is committing.
- **The guard's load-run rule is a config list:** `guards.deny_commands` (`match`, `alone`, `message`). The Go rule
  (`go test -count>1`, `-race`, two test commands at once) is no longer built in: a Go project that wants it adds it.
- **A drive is new work only:** `obor drive <T-xx>` on a worker's task is refused.
- **`obor projects --notify` is gone** (use `obor send <name> "<text>"`).
- **Gates:** a clean move that shares only packages re-runs the compile steps, not the tests; a resolved conflict is
  not resolved again when the target moves (`git rerere` in the merge scratch).
- **Status:** `obor status --json` task entries carry `ended` (was `open`) and the project carries `free` lanes (was
  `locked`): a missing field from an older serve reads as open/busy.
