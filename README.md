# obor

obor runs a small team of coding agents on one repository: a **lead** you talk to, **workers** that each implement
one task in their own git worktree, an **integrator** that reviews and merges, and a **reviewer** from another model
family. Every agent runs in a visible [herdr](https://github.com/herdrdev/herdr) terminal pane. A background process, `obor serve`,
dispatches tasks, runs the merge checks, merges and tells the lead what needs a human.

It ships as a Claude Code plugin (`obor`: the lead skill, role texts, guard hooks, an MCP server) plus one Go binary.
Workers can be Claude Code, Codex or OpenCode.

> **Status:** early: built and used daily by one person on two projects. Expect rough edges and breaking changes
> between minor versions; each release has an upgrade guide in `docs/UPGRADE-v<x.y>.md`. macOS: the Linux binaries are
> built but untested.

## How it works

```
you ─── lead (Claude Code) ── writes a spec ── obor spec set T-12 .obor/drafts/T-12.md / obor dispatch T-12
                                                        │
        obor serve ── picks the first free model of the spec's difficulty on the first free lane,
            │         cuts task/T-12 from the target branch
            │
            ├─ worker (lane L-01, its own worktree) ── implements, runs the task's checks, task_done
            ├─ merge slot compiles onto the target ── lane test ── integrator reviews ── merge
            └─ "needs-you" lines to the lead: a question, a hold, a failed check
```

- **Lanes** are a pool of identical worktrees (`../<repo>-wt/L-01`, …; `lanes.count` of them). A spec says
  `Difficulty: hard | medium | simple`; each level has a list of models (`agents.difficulty`), and dispatch runs the
  first free model of that list on the first free lane. An optional `Model: <model>[, …]` line puts its models ahead
  of the level's list (the level's models stay the fallback); a model that is paused (`obor pause --model`) is
  skipped. `obor dispatch --model a,b` names the request's own list instead of the level's.
- **The target branch** is `merge.target_branch` (`main` unless you set another; obor-kit itself uses `staging`). Tasks
  are cut from it and merged into it.
- **Tests run in three places.** The worker runs the task's checks in its lane before it submits. After that the
  merge slot compiles the merge onto the target branch (`merge.compile`), then the lane test runs in the task's own
  lane: `merge.check`, or the smaller checks your `merge.by_path` rules pick for the changed files. The nightly
  (`nightly.at`, a local time such as `02:00`) runs `lanes.setup`, `merge.check` and `nightly.slow_check`; a failure
  of either check is red and becomes a needs-you line. The commands in `nightly.also_run` come after those, and a
  failure there is only a warning.
- **Reviews.** When a few merges have piled up (`obor status` says "review due") the lead asks for one; a hard task
  gets one when it merges. The reviewer, from another model family, reviews the merged work; its findings become fix
  tasks, and it verifies them.
- **Guards** (Claude Code hooks, an OpenCode plugin) keep a worker inside its lane: no pushes, no edits outside its
  worktree, no edits to protected files unless its spec allows them.
- **The lead never merges and never polls.** serve sends it one line when something needs it, and the lead answers
  with `obor resume` or `obor drop`.
- **State** lives in serve's store (`.obor/state/obor.db`, an append-only log), not in chat. A crashed serve replays
  it and carries on.

## Requirements

- [herdr](https://github.com/herdrdev/herdr) (0.9.5 or later), the terminal workspace the agents run in
- [Claude Code](https://claude.com/claude-code) for the lead and the integrator; optionally Codex and OpenCode for workers
  and the reviewer
- git, jq, curl; macOS `/bin/bash` 3.2 is enough (no Go needed: the binary is downloaded)

## Install

```sh
curl -fsSL https://raw.githubusercontent.com/foksa/obor-kit/main/install.sh | sh
```

This clones the kit into `~/.obor/kit` at the newest release, links `~/.local/bin/obor` (it prints the `PATH` line if
needed), then downloads the release binary for your platform and the OpenCode kind binary (`obor-kind-opencode`) beside
it, and checks their checksums. To upgrade, run the same line again.

## Set up a project

In the project's directory (an empty one works: init runs `git init` and makes the first commit), inside herdr,
from a shell in a herdr workspace of its own or one where none of another project's panes run:

```sh
obor init       # the plugin, the git hooks, .obor/config.json, lane worktrees, agent panes, obor serve
```

`obor init` detects the check commands and the agent CLIs on your machine, writes the lane count and the models of each
difficulty level it found, installs the plugin and the git hooks and its `obor serve` launchd agent, then prints each
step and the file to change (`.obor/config.json`). Run it once on a machine with `claude` on PATH. With no pane of the
project open yet, init creates a herdr workspace for it. The lead's first session shows the lane count and the models
and asks you to confirm them (`agents.confirmed`).

obor commits with `--no-verify`, so your own pre-commit and commit-msg hooks never run on its lane and merge commits:
put formatting and secret scans in `merge.check` (the merge check) instead.

After that:

```sh
obor lead       # starts the lead in its own tab; run /obor:lead in it
```

## Customizing agents

Agents read the project's own committed CLI config in their start dir (a lane worktree, or the main checkout for a
role), as they do outside obor. obor starts each one with a few policy flags; a key the project file sets wins over
the flag for it:

| CLI | Project file | Key it sets | obor then leaves out |
| --- | --- | --- | --- |
| Claude | `.claude/settings.json` | `enableAllProjectMcpServers: true` or a non-empty `enabledMcpjsonServers` | `--strict-mcp-config` on a lane's agent: the project's `.mcp.json` servers load (the list also approves them, so no prompt waits) |
| Claude | `.claude/settings.json` | `remoteControlAtStartup`, `autoMemoryEnabled` | that key of `--settings` (non-lead roles) |
| Codex | `.codex/config.toml`, top-level (before any `[table]`) | `sandbox_mode` | `-s workspace-write` |
| Codex | same | `check_for_update_on_startup` | `-c check_for_update_on_startup=false` |

obor's MCP server, the session id, the prompt, the model ref each role and difficulty level names (`<model>[:<effort>]`,
e.g. `opus:high`), Claude's `--permission-mode auto` and Codex's `-a never` stay (change those in `.obor/config.json`:
`agents.roles`, `agents.difficulty`, `agents.kinds.<kind>.args`). Codex reads a project's `.codex/` only when the
project is trusted in `~/.codex/config.toml`. A file that does not parse counts as
absent. `obor config validate` warns on such a file, on `disableAllHooks: true` in `.claude/settings.json` (it turns
off obor's guard hooks) and on an OpenCode project config (`opencode.json[c]`, `.opencode/opencode.json[c]`) that
defines `mcp.servers.obor` (it replaces obor's server).

## Everyday commands

`obor help` (or obor web's Help page) lists every command, its usage and what it does. The ones you meet first:

- `obor lead`: start the lead in its own tab
- `obor status`: serve, lanes, agents, what needs you, the last full check
- `obor web`: a localhost page for every project on this machine
- `obor dispatch T-12`: run a task on the first free lane of its difficulty
- `obor resume T-12 "<text>"`: answer a task's needs-you, send it back or re-run its gate
- `obor drop T-12 "<why>"`: stop a task's worker and free its lane

The lead's full playbook is `skills/lead/SKILL.md`; the role texts are in `templates/agents/`.

## Repository layout

```
bin/obor           the CLI shim: finds or downloads the Go binary and runs it
cmd/obor, internal Go source (make build → dist/obor; make check → the full suite: nightly, before a release)
kinds/             the OpenCode kind plugin, its own Go binary (obor-kind-opencode)
opencode/          the OpenCode guard plugin
skills/            Claude Code skills: lead, setup
hooks/             Claude Code hook wiring (every hook is `obor hook <name>`)
templates/         role texts, task spec, BACKLOG/decisions templates, config examples
docs/              design (design.md, state-model.md, module-map.md), plans, decisions, upgrade guides
```

obor is developed by its own swarm: `BACKLOG.md`, `docs/decisions.md` and `docs/plan/` are that project's working
files, kept in the open.

## Releasing

See [`RELEASING.md`](RELEASING.md).

## License

MIT, see [LICENSE](LICENSE).
