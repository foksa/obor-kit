# Upgrading an obor project from v0.23 to v0.24

## v0.24.0 (obor monitor removed, no agents key, derived names)
Re-run the installer, then `obor serve restart` in each project. Nothing else is required, unless the project still
has `agents` entries (see below).

Close the monitor pane (labelled `<prefix>-monitor`) in each project's herdr workspace: `obor monitor` is gone, and
`obor init` no longer makes the "system" tab. obor web and `obor status` show the same view.

```sh
obor setup migrate
```

What changes:
- `obor monitor`, `obor setup monitor` and the monitor pane are gone. `obor monitor` prints one line naming
  `obor web` or `obor status` for one minor release, then it is deleted.
- A pane still labelled `<prefix>-monitor` is no agent: the restore hook types nothing in it, and nothing restarts it.
- **The `agents.<name>` config key is gone.** Advisor, researcher and triage are roles
  (`roles.advisor`, `roles.researcher`, `roles.triage`). `obor config validate` refuses a config that still sets
  `agents`; `obor setup migrate` moves an entry named for one of those roles (or whose `role` field names it) to that
  role, drops the rest with a line each, and commits `.obor/config.json`. axis-alies has four entries.
- **Names are derived, never configured:** a worker is `<prefix>-<lane lower>`, every role is
  `<prefix>-<role suffix>`. The compact lane alias (`hk-o1`) and a pane label standing in for a worker's name are gone;
  a lane's row name and `<prefix>-<lane lower>` are its only names.
- **Lead commands:** `obor answer` and `obor sendback` are gone; `obor resume <T-xx> ["<text>"]` does both (the
  needs-you line names the form). `obor drop` takes a task id, never a lane. A lead session started before the upgrade
  still has the old skill text: restart it (`obor lead --resume`) to load the new one.
- **No role worktree is reserved under `lanes_dir`** (the reviewer and the deep dive run in the main checkout since
  v0.20): any `L-NN` worktree there is a lane. Remove a leftover `review` / `deep-dive` worktree with
  `git worktree remove` if one is still there.
