# Upgrading an obor project from v0.40 to v0.41

## v0.41.1 (fixes from the herdr-core trial)

**obor init on a config you wrote.** Init fills a missing `project.agent_prefix` (and name) in a config it finds; no
agent starts with an empty name.

**Links and checkouts.** Every checkout obor runs in a lane or role worktree removes obor's link where the target
commit tracks that path and puts the links back after, so committing a linked file (`.claude/settings.json`) no
longer stops a dispatch, and a worktree behind that commit still gets the plugin settings. `obor config validate`
warns about a `lanes.links` entry the repository tracks.

**Restore.** On hc a lost holder restores every pane it held, the lead included. A restored worker (or integrator)
gets its open job's wake, and `obor resume <task> "<line>"` types a line to an idle worker of a working task.

**Smaller.** The web page shows an agent waiting for your input. `obor review start` uses `docs/reviews` when
`reviews.dir` is unset. On hc, serve's notifications show as macOS notifications.

```sh
# nothing to do
```

## v0.41.0 (herdr-core as an experimental terminal; obor prune and obor uninstall; the web page as a launchd agent)

**herdr-core (experimental).** `"project": {"terminal": "hc"}` in `.obor/config.json` runs the project's agents in
herdr-core (`hc` 0.1.21 or newer) instead of herdr; herdr-app shows them in a space named after the project, with
`lead`, `lanes` and `roles` tabs as in herdr. Serve starts a core that is down from a clean environment and refuses
one started from an obor pane (`hc server restart --fresh` from a shell outside any pane). herdr stays the default.

**Restore only what was lost.** After a terminal reconnect serve restarts only the panes whose process the terminal
lost: on hc a core restart or update keeps every agent, a lost holder restarts only its panes. On herdr a restart
still takes every pane, so nothing changes there.

**obor prune.** `obor prune [--dry-run] [--unmerged]` deletes `task/*` branches of tasks serve records as merged
(`--unmerged`: also tasks dropped more than 14 days ago) and clean worktrees of lanes or roles the config no longer
names. Every deletion prints its tip sha. The nightly prunes branches of tasks merged more than 7 days ago.

**obor uninstall.** `obor uninstall [--dry-run] [--discard] [--purge] [--force]` undoes `obor init` in a project: its
agents, serve, panes, worktrees, task branches, git hooks and plugin links go; `.obor/config.json` and every committed
file stay, and `.obor/state` stays unless `--purge`. It refuses while tasks are open or worktrees are dirty.

**obor lead in a new project.** In a git repository with no `.obor/config.json`, `obor lead` runs `obor init` first,
then starts the lead. A config that does not load is refused with its load error.

**Workers: merge_preview.** A worker's `merge_preview` tool shows whether its uncommitted work merges cleanly onto
the merge target's current head, and lists the conflicts; nothing in the lane changes.

**Done role agents.** A role agent done for an hour with no open work gets one line to the lead with its stop
command, and `obor status` ends its line with `→ obor restart <agent> --stop`.

**Store.** obor opens only schema 14 stores. A store last opened by obor v0.31 or older is refused, unchanged: open it
once with v0.40.7 first, which migrates it.

**obor web.** The machine's page runs as its own launchd agent, `obor.web`, beside each project's serve agent:
`obor serve install` installs it, and the first serve launchd starts after the upgrade installs it when it is missing.
`obor status` shows it as `web: launchd pid <n> · http://127.0.0.1:8787`. A `obor web` started by hand on that port
holds it: the agent's run logs one line naming the port and exits, and launchd retries; stop the hand-started one.

```sh
# nothing to do: the agent appears at serve's first start after the upgrade
```
