# AGENTS.md — rules for coding agents

obor-kit: a Claude Code plugin (`obor`) packaging a lead / integrator / worker / reviewer swarm that runs in herdr
panes. Contracts: `docs/design.md` (lead-owned — don't change it; propose changes in `QUESTIONS.md`).

## Prime directive: keep it simple
Small packages, one job each, readable top to bottom. Prefer: plain functions, explicit loops, early returns with a
clear one-line result (`MERGED …`, `FAIL …`). Avoid: frameworks, generated code, dependencies outside the policy below.

## Speak up (every role, unprompted)
If you notice something systemic while working — complexity growing faster than value, a fragile dependency (e.g. on
another tool's undocumented behaviour), effort out of proportion to the benefit, a rule or process that keeps causing
friction, a pattern of bugs, or a mistake of your own — say so without being asked. One or two sentences, with a
recommendation. Workers: a `Concerns:` line in their `task_done` report. Reviewers: a "Design
concerns" section. Leads: tell the user and keep `BACKLOG.md` "Concerns" current. Different agents see different things;
none of this replaces the task, and "nothing to flag" needs no line.

## Runtime constraints
- Go, standard library first. A dependency is allowed only when it implements a published spec or a database engine
  obor would otherwise write itself, is cgo-free and pinned in `go.mod`; adding or bumping one is its own task with
  `make check` as the gate (the owner approves new ones). `govulncheck` runs in `obor nightly` (`check_milestone`).
  Today the two dependencies are the shell parser (`mvdan.cc/sh`) and SQLite (`modernc.org/sqlite`, cgo-free, with its
  matching `modernc.org/libc` pin). One static binary
  (`make build` → `dist/obor`); `bin/obor` is the only bash (bash 3.2 compatible, macOS and Linux).
- Runtime deps: **git, herdr, jq** (jq only in the `bin/obor` shim) + the binary. No python, node or bun in shipped code; the mods are TypeScript run by
  Claude Code itself.
- Timeouts through `context`; processes that may leave children run as a process group (`run.Group`).
- Hooks: fast, no network, never block on herdr; no-op outside an obor project (no `.obor/config.json`).

## Layout
`.claude-plugin/plugin.json` · `skills/` · `hooks/hooks.json` · `bin/obor` (the one bash shim) · `cmd/obor/` +
`internal/` (Go) · `githooks/` · `mods/` · `templates/` · `tests/` · `docs/` (this repo's own process;
your spec comes in your first prompt (`obor spec show T-xx`) — edit only in your lane; no per-file list is enforced (T-496): the integrator
checks placement against `docs/module-map.md` at accept).

## Helpers
- One owner per helper: grep `internal/` for a twin before writing one. No line-count targets: simpler, not shorter.
- A name obor can derive is never stored; state is keyed by slot or task (DD-consolidation I3, T-786: an agent's name
  comes from `project.Self`, never an exported `OBOR_AGENT`).
- An empty derived value is an error, never a default: a name, role, path or id that obor derives and gets back empty
  (or unknown) fails closed with a message naming what is missing; no silent fallback to "no role", "no agent" or the
  main checkout (T-791: an opencode worker's MCP calls lost their name; T-814: a removed role kept write rights).

## Tests
- The merge gate is `make check` (`commands.check`, heavy): `make build`, gofmt, `go vet`, `go test ./...`,
  `make deadcode` (pinned; `herdrtest`/`testkit` excepted), on a `$TMPDIR` GOCACHE when the default is not writable.
- Two tiers (T-347): a merge of only `cmd/**`/`internal/**` (plus neutral docs) runs `make check-affected`
  (`commands.check_affected`): the same steps, `go test` only on the changed packages and their importers
  (`cmd/affected`); every other merge, and `obor nightly`, run the full `make check`.
- No load runs (T-543): one plain `go test <pkgs>` at a time — `-count` > 1, `-race` and two test commands at once
  (`&`, `xargs -P`, `parallel`) are denied by the guard for every role; the gate and nightly run the rest.
- Tests are Go only (`go test ./...`; no bash suite). Tests build fixture repos in a temp dir and put a stub `herdr`
  on `PATH`; they never touch this repo's git state or real herdr panes.
- Tests stub herdr. Never link a real herdr plugin from a test or probe: herdr's plugin registry is user-global (even
  with `HERDR_CONFIG_PATH`), so it would act on the user's real panes. A live probe uses only a throwaway named
  session (`herdr --session <probe-name>`) in a scratch dir and never addresses the default session (T-150).
- A task that touches agent identity, the MCP server or how an agent is launched runs `make conformance TIER=live
  TURN=1 KIND=<kind>` for each agent kind it affects (claude, codex, opencode) before `task_done`, and puts each
  summary line in its report: stubs do not catch a real CLI's behaviour (T-791 passed every unit test).

## Workflow
Workers: read your spec and the `docs/design.md` sections it cites; the rest (tests to run, no git writes) is the
worker role text `templates/agents/worker.md` + `docs/agents/worker.md`. Never run the full suite in a lane:
`obor serve`'s merge loop runs it once as the gate.
A job is fetched, not typed (T-375, T-437): the first prompt is the constant wake `[obor job <id>] call the obor
job_next tool`; call `job_next` first. A worker finishes only with `task_done`, its report as the argument (there is
no done file). `obor setup mcp` shows whether the `obor mcp` server is registered per agent kind (Claude: the plugin
manifest's `mcpServers`; Codex: launch args; OpenCode: each start's generated config).
