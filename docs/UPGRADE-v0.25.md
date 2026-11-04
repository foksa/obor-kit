# Upgrading an obor project from v0.24 to v0.25

## v0.25.1 (opencode: obor's MCP server and plugin per start)
Run `obor setup agents` once per machine: it removes the global opencode entries (`mcp.servers.obor` in
`~/.config/opencode/opencode.json` and the `plugins/obor.js` link) when they are obor's, then restart opencode agents.
Each opencode start now names its own config (`OPENCODE_CONFIG`, `.obor/state/opencode.json`) with the launching kit's
`obor mcp` and plugin; while the global link is there opencode loads that copy instead and the per-start plugin never runs.

## v0.25.0 (derived identity, inbox per project, setup migrate gone)
Upgrade from v0.24.x only: a project whose config still has the pre-v0.23 shape or an `agents` key must run
`obor setup migrate` with v0.24 first (v0.25 has no migrate command and refuses such a config, naming the key).

Re-run the installer, then in each project (every lane free is not needed):

```sh
obor serve restart
rm -rf ~/.obor/agents ~/.obor/inbox
```

Then restart every role agent and the lead so each records its inbox in the project: `obor restart <agent>` for the
integrator, reviewer, deep dive, advisor, researcher and triage panes that run, and `obor lead --resume` for the lead.
A lead that is not restarted gets no lines from serve or other leads.

What changes:
- **Names are derived, never exported:** `OBOR_AGENT` is gone; an agent's name comes from its role (`OBOR_ROLE`), its
  lane (the cwd) and the config. A stale `OBOR_AGENT` in a shell is ignored. A caller with no derivable identity is
  refused with the reason. Triage now has a role (`OBOR_ROLE=triage`) like every other role.
- **Inbox records live in the project** (`.obor/state/inbox/<slot>`), not in `~/.obor/agents`: two projects with the
  same prefix no longer collide.
- **`guards.allow_push` lists roles** (`["lead"]`); an old derived name (`ob-lead`) still works and `obor setup
  rename` rewrites it to the role. **The git pre-push hook now honours it**: a listed non-worker role pushes without
  `OBOR_ALLOW_PUSH=1` (that still works by hand).
- **`obor brief <agent> [--fresh] "<text>"`** is the one way to brief a role agent; serve does the fresh stop and
  start once it has accepted the brief. `obor restart` keeps `--fresh` and `--stop`; `--prompt`, `--type`,
  `--effort` and `--sid` are gone (a model change is `roles.<role>` in config, or `obor dispatch T-xx --model`).
- **Spec titles** are read from `# T-xx: title` (or `—`); `obor spec set` refuses a spec without one. `obor dispatch`
  refuses a task that already merged.
- **One task phase**: `obor status`, obor web and the pipeline show the same phase; `held` is the integrator's verdict
  only (serve's own hold reads `merge hold: <role>`). A worker waiting on you is not in the merge queue.
- `setup rename --name` refuses while the old `obor.serve.<name>` service is installed (`obor serve uninstall` first).
- opencode lanes: the user-global opencode MCP registration runs `~/.obor/kit`; installing v0.25 brings it to the
  same version as the project's serve.

## v0.25.1 (custom roles, idle ping, per-launch opencode registration, nightly commands)
Re-run the installer, then `obor serve restart` in **every** project on the machine. Only once all of them run
v0.25.1, once per machine:

```sh
obor setup agents
```

It removes obor's old user-global opencode registration (`mcp.servers.obor` in `~/.config/opencode/opencode.json` and
the `plugins/obor.js` link); a v0.25.0 serve's opencode lanes still need it, so run it last. Then restart running
opencode lanes and role agents (`obor restart <agent>`), and the lead (`obor lead --resume`) to load the new skill.

What changes:
- **Custom roles:** a `roles.<name>` row (`"<model>"`, or `{"model": …, "write": ["dir/", "file"]}`) plus
  `docs/agents/<name>.md` adds a role: main checkout, git read-only, its write list only, briefed with `obor brief`,
  placed with `obor setup role <name>`. advisor, researcher and triage are such rows now (same names and config lines);
  **the advisor may write `docs/research/` and `docs/plan/`**. A role whose row is removed while it runs may write
  nothing.
- **Idle ping:** when nothing is in flight serve tells the lead once (`[obor] idle: …`); `obor idle "<why>"`
  acknowledges, a dispatch, review request or a reviewer/deep-dive brief ends it; unanswered after 30 min one more line
  and a herdr notification to the owner.
- **Spec changes reach a worker at task_done:** `obor spec set T-xx <file> --note "<what changed>"` records the note
  with the new rev; a worker's `task_done` against an older rev is refused once with the rev and the note.
- **opencode:** obor's MCP server and plugin are given per launch (`OPENCODE_CONFIG`, from the launching kit), so the
  kit and the project's build no longer skew; obor no longer appears in your own opencode windows after `obor setup
  agents`. A role agent on opencode now runs its guard as its role.
- **Agent env:** every start unsets every obor variable it does not set (one list), so a reused pane never keeps an
  old agent's env. Claude starts with a session id obor chooses, and restarts use the transcript path Claude recorded.
- **Codex:** starts with `--no-daemon`; efforts are `low medium high xhigh max ultra` (`minimal` is refused: change
  a `terra:minimal` in config or specs).
- **herdr:** any protocol from 22 up is accepted; `obor status` shows a newer herdr. `make conformance` (kit) checks
  the installed CLIs.
- **Nightly commands:** `merge.nightly_commands: ["<command>", …]` runs those `commands.<name>` after the nightly's
  checks, each non-gating (one line, a failure is a warning). obor-kit uses it for `conformance` and a Go cache trim.
- **Merge subjects** are `Merge T-xx: <spec title>`, conflict-resolved merges included; the body keeps the worker's
  whole summary.
- The git pre-push hook allows a push by a role in `guards.allow_push` (v0.25.0 notes).
- Review cycles record the tasks they covered, so a merge during a verify is never counted as reviewed.
