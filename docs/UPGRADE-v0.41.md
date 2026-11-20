# Upgrading an obor project from v0.40 to v0.41

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
