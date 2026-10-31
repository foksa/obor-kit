# Upgrading an obor project from v0.16 to v0.17

## Before you upgrade
v0.17 deletes v0.16's one-release herd compatibility (T-625): `bin/herd`, `HERD_*` env, `[herd job` tags, the
`<agent>.sid` session files and `obor migrate`. A project still on `.herd/` now loads as no obor project: hooks do
nothing instead of failing closed. Migrate with v0.16's `obor migrate` first (`docs/UPGRADE-v0.16.md`). Every pane's
env must be `OBOR_*`: restart any pane started before the rename with `obor restart <agent> --fresh`.

## Upgrade (once per machine, then each project)
1. Re-run the installer: `curl -fsSL https://raw.githubusercontent.com/foksa/obor-kit/main/install.sh | sh`.
2. In each project: `obor setup githooks` (picks the small or the chain hook set, below).
3. Serve under launchd (new): `obor serve install` once per project (macOS); afterwards `obor serve restart` after an
   upgrade. A serve already running in a pane: stop it first (Ctrl-C), then install.
4. Restart every Claude pane (hooks are read at session start): `obor restart <agent> --fresh`, the lead last at a
   handoff.

## New
- **`obor init`** sets a project up with no questions (herdr check, config with detected commands, lanes for the CLIs
  on PATH, git hooks, panes and roles, plugin, obor.events, `obor serve install`); every step is idempotent, a rerun
  is the repair path (T-608, T-613, T-618). The old `obor setup init` question flow is gone (T-609).
- **Serve under launchd** (macOS): `obor serve install|uninstall|restart`; serve waits for herdr instead of exiting;
  `serve.log` rotates on install/restart past 5 MiB (T-596, T-607, T-614).
- **Git hooks keep the project's own**: a project with its own `core.hooksPath` keeps its hooks, chained before obor's
  rules (`obor.hooksChain` in git config; `--remove` restores it). The full hook set is installed only when there is
  something to chain; otherwise only `pre-commit` and `pre-push` (T-620, T-623). serve's own commits skip the
  project's commit hooks (T-622).
- **Open questions go to the lead** before the integrator's review; the lead's answer reaches the integrator and the
  worker; a blank `obor resume` of such a hold is refused; `obor sendback <LANE>` works on a held task and does not
  count toward the two-sendback limit (T-604, T-611, T-612, T-616, T-619).
- **Needs-you reminders**: a needs-you still unanswered 30 min after it was delivered is posted again, then hourly,
  never while the previous line waits undelivered (T-621, T-624).
- **Fewer tokens**: the integrator starts fresh per job with the diff in its review job; workers get their role text
  in the job and do not repeat their report (T-602, T-603, T-606).

## Fixes and internals
- Pane labels, reused-pane env and `obor setup rename` stay in sync with serve; an agent not in the config owns no
  pane; the env line's marker proves the env was set (T-595, T-601, T-605).
- The store is the only session record (T-599); a stop is recorded against the lane's current task (T-617).
- `internal/app`'s project state is split into `registry`, `outbox` and folded vs live state; choices over tasks are
  ordered, never Go map order (T-600, T-610, T-615).
- The `obor.version` kit pin is gone (T-597); `internal/setup` imports no adapter (T-598).
