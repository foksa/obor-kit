# Upgrading an obor project from v0.19 to v0.20

## v0.20.1 (answer delivery)
Nothing to do beyond v0.20.0's steps below; v0.20.0 was not published on its own, so coming from v0.19 follow them.

## v0.20.0 (lanes are a pool)
Re-run the installer, then `obor serve restart` in each project and restart its `obor monitor` and `obor web`
panes once (v0.19 binaries cannot follow a new binary). From v0.20 on, monitor and web restart themselves on a new
binary within a minute; serve does too when idle, and while a gate, nightly or other work runs it waits for that,
at most 10 minutes after it noticed the binary, then restarts even if that work still runs (a failed restart is a
needs-you).

- **Untyped lanes pool the work**: `lanes.<LANE>` may be `""` (any type). A task's `Type:` and `Difficulty:` pick the
  model and effort per dispatch, and a free untyped lane takes it; the worktree, pane and label are reused. To pool a
  lane, blank its type with `"lanes": {"<LANE>": ""}` in `.obor/config.json`. Typed lanes are unchanged: they stay reservations for
  their type. `obor setup lane <LANE> [<type>]` takes the type as optional, `obor init` makes L-01 and L-02 untyped,
  and `obor setup pane --start` on an untyped lane refuses (a worker has no type without a dispatch) (T-724).

### Steps
There is no `obor config set`: edit `.obor/config.json` by hand.

1. **Finish in-flight tasks before blanking lanes.** A lane with a task in progress keeps its type until the task is
   merged, rejected or dropped.
2. **Blank the lanes you want pooled** in `.obor/config.json`: `"lanes": {"L-01": ""}`. A lane in object form
   (`{"type": "opus", "agent": ...}`) changes only its `type`; keep `agent`.
3. **Restart the reviewer (and the deep dive, if you have one).** Old sessions still run in their old worktrees, and
   `obor brief` to a running role does not restart it. Reviewer: `obor restart <reviewer> --stop` (its `STOPPED`
   line names the pane), then `obor setup role reviewer --pane <that pane>` (adds the reviews ignore to `.gitignore`
   and moves the pane's shell to the main checkout; without `--pane` an already labelled pane is left where it is).
   Only if `roles.deep_dive` is configured: the same with `obor restart <deep-dive> --stop` and
   `obor setup role deep_dive --pane <its pane>`. The next `obor brief` starts each fresh. Only then remove the old
   review (and deep-dive) worktrees.
4. **Dispatch caps changed.** `max` caps open worker dispatches of the type, enforced at dispatch only; role
   agents never count, and `obor setup` does not check it. Raise `max` if dispatch now starves.
5. **Merge hold.** While a reviewer or deep dive works, serve holds merges, for at most 30 minutes; then the publish
   goes ahead and the lead gets one line.

`obor init` writes `difficulty` only from the kinds it detected: Claude types always, and a non-Claude type
(deepseek on OpenCode) only if you list it yourself. An existing `difficulty` is kept.
