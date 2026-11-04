# Upgrading an obor project from v0.23 to v0.24

## v0.24.0 (no agents key, derived names)
Re-run the installer. Nothing else is required, unless the project still has `agents` entries (see below).

```sh
obor setup migrate
```

What changes:
- **The `agents.<name>` config key is gone.** Advisor, researcher and triage are roles
  (`roles.advisor`, `roles.researcher`, `roles.triage`). `obor config validate` refuses a config that still sets
  `agents`; `obor setup migrate` moves an entry named for one of those roles (or whose `role` field names it) to that
  role, drops the rest with a line each, and commits `.obor/config.json`. axis-alies has four entries.
- **Names are derived, never configured:** a worker is `<prefix>-<lane lower>`, every role is
  `<prefix>-<role suffix>`. The compact lane alias (`hk-o1`) and a pane label standing in for a worker's name are gone;
  a lane's row name and `<prefix>-<lane lower>` are its only names.
- **No role worktree is reserved under `lanes_dir`** (the reviewer and the deep dive run in the main checkout since
  v0.20): any `L-NN` worktree there is a lane. Remove a leftover `review` / `deep-dive` worktree with
  `git worktree remove` if one is still there.
