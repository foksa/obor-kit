# obor

obor runs a small team of coding agents on one repository: a **lead** you talk to, **workers** that each implement
one task in their own git worktree, an **integrator** that reviews and merges, and a **reviewer** from another model
family. Every agent runs in a visible [herdr](https://github.com/herdrdev/herdr) terminal pane. A background process, `obor serve`,
dispatches tasks, runs the merge checks, merges and tells the lead what needs a human.

It ships as a Claude Code plugin (`obor`: the lead skill, role texts, guard hooks, an MCP server) plus one Go binary.
Workers can be Claude Code, Codex or OpenCode.

> **Status:** early (v0.16), built and used daily by one person on two projects. Expect rough edges and breaking
> changes between minor versions; each release has an upgrade guide in `docs/UPGRADE-v<x.y>.md`. macOS and Linux.

## How it works

```
you ─── lead (Claude Code) ── writes a spec ── obor spec set T-12 / obor dispatch T-12
                                                        │
              obor serve ── picks a free lane of the right model tier, cuts task/T-12 from main
                  │
                  ├─ worker (lane O-01, its own worktree) ── implements, runs the checks, task_done
                  ├─ merge checks (your project's commands) ── integrator reviews ── merge into main
                  └─ "needs-you" lines to the lead: a question, a hold, a failed check
```

- **Lanes** are fixed worktrees (`../<repo>-wt/O-01`, `S-01`, …), each with a model type. A spec says
  `Difficulty: hard | medium | simple`, and serve picks a lane of the matching tier.
- **Guards** (Claude Code hooks, an OpenCode plugin) keep a worker inside its lane: no pushes, no edits outside its
  worktree, no edits to protected files unless its spec allows them.
- **The lead never merges and never polls.** serve sends it one line when something needs it, and the lead answers
  with `obor answer`, `obor resume`, `obor sendback` or `obor drop`.
- **State** lives in serve's store (`.obor/state/obor.db`, an append-only log), not in chat. A crashed serve replays
  it and carries on.

## Requirements

- [herdr](https://github.com/herdrdev/herdr) (0.9 or later), the terminal workspace the agents run in
- [Claude Code](https://claude.com/claude-code) for the lead and the integrator; optionally Codex and OpenCode for workers
  and the reviewer
- git, jq, curl; macOS `/bin/bash` 3.2 is enough (no Go needed: the binary is downloaded)

## Install

```sh
curl -fsSL https://raw.githubusercontent.com/foksa/obor-kit/main/install.sh | sh
```

This clones the kit into `~/.obor/kit` at the newest release, links `~/.local/bin/obor` (it prints the `PATH` line if
needed), then downloads the release binary for your platform and checks its checksum. To upgrade, run the same line
again.

## Set up a project

In the project's directory (an empty one works: init runs `git init` and makes the first commit), inside herdr,
from a shell in a herdr workspace of its own or one where none of another project's panes run:

```sh
obor init       # the plugin, the git hooks, .obor/config.json, lane worktrees, agent panes, obor serve
```

`obor init` detects the check commands, picks the lanes and role types, installs the plugin and the git hooks and its
`obor serve` launchd agent, then prints each step and the file to change (`.obor/config.json`). Run it once on a
machine with `claude` on PATH. With no pane of the project open yet, init creates a herdr workspace for it.

After that:

```sh
obor lead       # starts the lead in its own tab; run /obor:lead in it
```

## Everyday commands

| Command | What it does |
|---|---|
| `obor status` | serve, lanes, agents, what needs you, the last full check |
| `obor web` | a read-only page for every project on this machine: lanes, queue, needs-you, events, task reports and agent prompts (localhost, `--listen 127.0.0.1:<port>`) |
| `obor spec set T-xx <file>` | store a task spec (it needs a `Difficulty:` line) |
| `obor dispatch T-xx [--after T-yy]` | run it on the first free lane of its tier, now or after T-yy merges |
| `obor watch T-xx[,T-yy]` | one line to the lead when they merge (or one is dropped) |
| `obor answer` · `resume` · `sendback` · `drop` | act on a needs-you |
| `obor restart <agent> [--fresh]` | restart an agent in its pane |
| `obor pause [--for 3h] "<why>"` | stop dispatching (quota, waiting on you) |
| `obor stop ["<why>"] [--force]` · `obor start` | put the project to sleep (its agents, serve and its entry in `obor projects` go; files and config stay; hooks stand down) and wake it. Stop pauses serve first and says STOPPED only once serve is confirmed down; it refuses (clearing its pause) from a worker lane, with serve down (start it first), with a task in progress (`--force` drops each through serve; a failed drop refuses), when herdr does not answer, or when serve is not launchd's (stop that one in its pane). Start also clears the stop's pause |

The lead's full playbook is `skills/lead/SKILL.md`; the role texts are in `templates/agents/`.

## Repository layout

```
bin/obor           the CLI shim: finds or downloads the Go binary and runs it
cmd/obor, internal Go source (make build → dist/obor; make check → the merge gate)
skills/            Claude Code skills: lead, setup, role
hooks/             Claude Code hook wiring (every hook is `obor hook <name>`)
templates/         role texts, task spec, BACKLOG/decisions templates, config examples
herdr-plugin/      restores obor's panes after a herdr restart
docs/              design (design.md, state-model.md, module-map.md), plans, decisions, upgrade guides
```

obor is developed by its own swarm: `BACKLOG.md`, `docs/decisions.md` and `docs/plan/` are that project's working
files, kept in the open.

## Releasing

See [`RELEASING.md`](RELEASING.md).

## License

MIT, see [LICENSE](LICENSE).
