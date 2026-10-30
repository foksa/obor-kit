# Upgrading a herd project from v0.15 to obor v0.16

v0.16 renames herd to **obor** (command, plugin, `.obor/`, `~/.obor`, `OBOR_*`, herdr plugin `obor.events`, line tags
`[obor …]`) and ships from a public repo, `github.com/foksa/obor-kit`, with GitHub release binaries.

## Install (once per machine)
```sh
curl -fsSL https://raw.githubusercontent.com/foksa/obor-kit/main/install.sh | sh
```
It clones the kit into `~/.obor/kit` at the newest tag, links `~/.local/bin/obor` (prints the PATH line when needed)
and fetches the checksum-checked binary. Re-running it is the upgrade.

## Migrate each project (once, at a lead handoff)
Stop the project's serve and agents (panes may stay), then in the project: `obor migrate`. Each step prints `OK …` or
`FAIL …`; rerun after a fix. It moves `.herd/` → `.obor/` (git mv, never rm), swaps the Claude plugin
(`herd@<marketplace>` → `obor@obor-kit`, project scope), re-points the git hooks, copies `~/.herd` → `~/.obor` (copy:
other projects may still run herd), links `obor.events` beside `herd.events`, lists your own prose files that still say
`herd` (it never edits them), and prints the finish: start serve, `obor restart <agent> --fresh` for each pane, the
lead at a handoff. Project name, prefix, lanes and agent names do not change.

Move a project's role worktrees (review, deep-dive) to the new tag before restarting their panes: a detached worktree
keeps its old commit's `.claude/settings.json` (the herd plugin).

## Also in v0.16 (no action beyond the restarts)
- obor records every agent's sessions in serve's store (Claude via its SessionStart hook, Codex from the rollout its own
  process has open, OpenCode through obor's plugin); herdr's `agent_session` is only a cross-check (T-577..T-589).
- Workers and role agents stopped on a usage limit are seen and continue after the reset (T-563..T-572, T-584).
- A newer brief replaces a role agent's undelivered one (T-575, T-580); low disk is a needs-you (T-564, T-566).
- `obor setup rename --name N --prefix P` renames a project and its agents (T-587).

## One release only
For v0.16 only: `bin/herd` forwards to `obor`, `HERD_*` env is read after `OBOR_*`, the guard accepts `[herd job` tags,
and `<agent>.sid` files back up the store when serve is down. v0.17 deletes them: migrate before upgrading to v0.17.
