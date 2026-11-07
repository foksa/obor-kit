# Upgrading an obor project from v0.29 to v0.30

## v0.30.0 (the binary serve follows is built where the promotion runs)

Adopters without `branches.promote` see no change: each merge still runs `commands.after_merge` in the main checkout
and serve follows the `dist/obor` it leaves there.

Re-run the installer, then in every project:

```sh
obor config validate      # names a configuration problem you still have
obor serve restart
```

Then restart the running agents (an agent's MCP server is the binary it started with): `obor stop --force "v0.30"`
and then `obor start`; open tasks stay open and a working task's worker comes back with its job.

### Switching to `staging` and the promotion

`make build` now takes `OUT` (`OUT ?= dist/obor`), and the project's `commands.after_merge` is
`make build OUT=${OBOR_ROOT:-.}/dist/obor`. A promotion runs it in its own worktree with `OBOR_ROOT` set, so the build
happens there but the binary lands in the main checkout's `dist/obor`, where serve follows it. The owner's one-time
switch (the plan's steps, `docs/plan/merge-flow-rework.md` §6), at a quiet queue:

1. `obor pause "staging switch"`, and wait until `obor status` shows no task in `candidate`, `review`, `publish` or
   `testing` (dispatched and working tasks are fine: their lanes re-merge onto staging through the slot).
2. In the main checkout: `git checkout -b staging main` (staging is main at that moment, so every open candidate's
   target is still an ancestor and publish fast-forwards as before); optionally `git push -u origin staging`.
3. In `.obor/config.json`: `branches.main` = `"staging"` (the loop's target), `branches.promote` = `"main"`,
   `merge.promote_weight` = 6, `slots.max` = 4 (the pool bounds parallel lane tests), `slots.cpus` = 3 (→ `go test
   -p 3`); then `obor config validate` and `obor serve restart`.
4. `obor pause --clear`. Once staging has a merge (the idle trigger) the pass requests a review and runs the suite;
   `main` moves to the closed review's reviewed head R once the suite is GREEN at R (T-978), then `after_merge`
   builds `dist/obor` from `main` and restarts serve on it.

After the switch, commits in the main checkout land on `staging`, `main` moves only to a closed review's reviewed
head on a GREEN full suite there (commits after it, such as the close commit, wait for the next review), and a
release is cut from a separate worktree at `main`. Never `git checkout main` in the main checkout (the loop pauses,
T-425); `git push origin staging main` when pushing.
