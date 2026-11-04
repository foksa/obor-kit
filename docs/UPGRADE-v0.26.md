# Upgrading an obor project from v0.25 to v0.26

## v0.26.0 (old config keys and old stores refused, main checkout guarded)
Upgrade from v0.25.x. Before restarting serve, in each project:

```sh
obor config validate
```

It names every key it no longer knows. Keys removed in v0.11–v0.17 (`heartbeat`, `notify`, `inbox`, `stop_hook`,
`done_file`, `obor`, `roles.leads`, `leads.cross_ttl`, `merge.full_every`, `cache.warn_tokens`, `spec.files_heading`,
`spec.enforce_files`, `paths.status`, `paths.queue`, …) used to warn; now they are unknown keys: delete them from
`.obor/config.json` and `.obor/local.json`. A config with `agents` or `lane_types` (the pre-v0.23 shape) is
refused by every command, serve and hooks included: migrate it with v0.24 first (`obor setup migrate`), then upgrade.

Then re-run the installer and `obor serve restart` in every project.

What changes:
- **Stores older than v0.22 are refused**: serve opens schema 9 and newer. A project last run with v0.21 or earlier
  upgrades through v0.24 first.
- **The global opencode registration is no longer removed by `obor setup agents`.** If an obor before v0.25 put one on
  the machine and you never ran `obor setup agents` with v0.25.1, remove it by hand: the `mcp.servers.obor` entry of
  `~/.config/opencode/opencode.json` (or `opencode.jsonc`) and the `plugins/obor.js` link there.
- **`obor monitor` is gone** (unknown command): use `obor web` or `obor status`.
- **Only the lead moves HEAD in the main checkout**: the guard refuses `git checkout <ref>`, `--detach`, `switch`,
  `reset`, `rebase`, `merge`, `stash` and the like there for every other role (serve publishes to main from it).
  Review a commit with `git show` / `git diff`.
- **Every wire call carries the caller's obor version**: a refusal names both versions when the caller (an old
  `obor mcp`, a hook) runs another obor than serve.
- **CLI calls that stop an agent wait for the stop** (`obor dispatch --model`, `obor drop`, `obor drive`), and a call
  that outlasts its deadline says so instead of `use of closed network connection`.
