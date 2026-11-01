# Upgrading an obor project from v0.16 to v0.17

## v0.17.5 (patch; v0.17.3 and v0.17.4 were tagged but never published: their notes below are part of this one)
Same steps as v0.17.4 below.
- **`obor monitor`**: a live view (running work, needs-you, lanes, review, recent events) in each project's `system`
  tab; `obor init` creates it, `obor setup monitor` adds it to an existing project (T-658).
- `obor status` shows `running:` (gates, merges, after_merge, nightly, restart) and lane states `gating`, `merging`,
  `reviewing`, `held`; `obor status --short` (T-657). Known: work waiting for a retry also shows as running (T-662).
- A serve shutting down records no failure caused by its own cancellation (T-660).
- Kit maintainers: `make ship` pushes main only up to the release's checksums commit (T-661).

## v0.17.4 (patch; v0.17.3 was tagged but never published, its notes below are part of this one)
Re-run the installer, then `obor serve restart` in each project, then restart every Claude pane started since you
installed v0.17.2 (`obor restart <agent> --fresh`, the lead at a break).
- **Open questions go to the integrator again** (T-604 reversed): it decides questions inside the spec and holds the
  rest for the lead; a decision is carried in a sendback's why so the next attempt does not ask again (T-649, T-655).
- **Fewer lead lines**: no `review started` line; every line queued for the lead in one pass arrives as one message
  (one per target); a sent batch settles exactly the lines it carried (T-650, T-651, T-654).
- **`obor send <project name>`** (or `lead@<name>`, or a path); `obor projects` lists the projects this machine knows
  (`~/.obor/projects.json`, written by serve and init); a posted line ends with its reply command (T-646).
- **Model setup**: `obor setup show` prints models and effort per difficulty and role; `obor init` ends with it and the
  lead's first session goes through it with you (`setup.confirmed`); the example config has `opus-high` and
  `deepseek` types (T-647, T-653).
- **Serve restarts itself after a code merge** when `commands.after_merge.restart_serve` is true (kit development;
  off by default): it exits gracefully and launchd starts it again; it binds its socket only after replay, and calls
  retry with the same call id until their deadline (T-652, T-656, T-659).
- Release scripts: the scratch worktree is the repo's own and cleanup never deletes what it did not create (T-648).

## v0.17.3 (patch, fixes v0.17.2's plugin)
Re-run the installer, then `obor serve restart` in each project, then restart every Claude pane started since you
installed v0.17.2 (`obor restart <agent> --fresh`, the lead at a break).
- **v0.17.2's catalog renamed the plugin** (`obor-kit` instead of `obor`), so `obor@obor-kit` did not load: new
  sessions had no obor skills (`Unknown command: /obor:lead`) and no obor hooks. Fixed; release and publish now refuse
  a tag whose plugin name differs from `plugin.json` (T-645).
- `obor init` sets the lead's type (`roles.lead.type`); before, `obor lead` failed after a fresh init (T-644). A
  project initialized with v0.17.0–v0.17.2: add `"type": "opus"` under `roles.lead` in `.obor/config.json`, or re-run
  `obor init`.
- Kit maintainers: `make release` refuses while merged code is unreviewed (`UNREVIEWED="<why>"` overrides, owner's
  call), and the release/ship steps resume safely after any failure (T-642, T-643).

## v0.17.2 (patch)
Re-run the installer, then `obor serve restart` in each project. No config change.
- **`obor review [--after T-a,T-b] [--note "…"]`**: serve starts the next review itself once the listed tasks merged
  and the reviewer is idle: next RV id, the merges and files since the previous `RV-n closed` commit, the review
  worktree checked out at head, the reviewer restarted fresh with the brief. One review cycle at a time: a new
  request waits until the open one's `RV-n closed` commit exists. `obor status` says `review due: N merges since
  RV-n` past 3 (T-638, T-639). Close every review with its own `RV-n closed · <sha> · …` commit: that is what serve
  counts from.
- `make release V=x.y.z` and `make ship` replace the manual release steps (T-640; kit maintainers only).
- A closed task gets no new watchdog fact and a reject settles its pending notification (T-636); the needs-you line
  tests no longer time out under load (T-637).

## v0.17.1 (patch)
Re-run the installer, then `obor serve restart` in each project. No config change.
- `obor init` works in an empty directory (runs `git init` and makes the first commit, only when the directory held
  nothing but `.git`; with your own files present it asks you to commit first). On a Claude-only machine a difficulty
  level whose lane types have no lane falls back (simple → sonnet) instead of waiting forever (T-629, T-631).
- `obor init` puts a project's panes in its own herdr workspace (creating one when none has the project's panes) and
  waits for a new pane's shell before starting its agent (T-628, T-632, T-634). Run it from a shell in the project's
  own workspace.
- After a herdr restart, a session that never got a prompt starts fresh instead of leaving a shell; a failed search
  for a session's transcript refuses instead of starting fresh (T-627, T-633).
- A merged, dropped or rejected task's old needs-you facts end, so the hourly reminder no longer reposts them; an old
  log's stale ones end at the next serve start (T-635).
- The plugin version is checked against `VERSION` (T-630).
- Known, low (fixed in v0.17.2, T-636): a worker stop recorded just after its task closed can leave one stale
  needs-you; a rejected task can leave one notification pending.

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
