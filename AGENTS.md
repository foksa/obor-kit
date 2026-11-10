# AGENTS.md — rules for coding agents

obor-kit: a Claude Code plugin (`obor`) packaging a lead / integrator / worker / reviewer swarm that runs in herdr
panes. Contracts: `docs/design.md` (lead-owned — don't change it; propose changes in `QUESTIONS.md`).

## Prime directive: keep it simple
Small packages, one job each, readable top to bottom. Prefer: plain functions, explicit loops, early returns with a
clear one-line result (`MERGED …`, `FAIL …`). Avoid: frameworks, generated code, dependencies outside the policy below.

**Text says what is, never what was** (owner 2026-11-10). Skills, role texts, templates, `docs/design.md`, help text
and code comments describe the current behaviour: no previous states ("it was `X`", "`Y` is gone", "used to",
"no longer", "replaces the old …") and no task or review ids: they mean nothing to a reader.
History and ids live in git (commit subjects), `docs/decisions.md`, reviews, BACKLOG, CHANGELOG and the UPGRADE guides.

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
  Today the dependencies are the shell parser (`mvdan.cc/sh`), the TOML parser (`github.com/BurntSushi/toml`, for
  Codex's override file), SQLite (`modernc.org/sqlite`, cgo-free, with its
  matching `modernc.org/libc` pin) and the official MCP Go SDK (`github.com/modelcontextprotocol/go-sdk`, adopted
  2026-11-05, with `github.com/google/jsonschema-go` for the tools' schemas). One static binary
  (`make build` → `dist/obor`), and beside it each plugin kind the kit ships as its own (`dist/obor-kind-opencode`,
  `kinds/opencode`); `bin/obor` is the only bash (bash 3.2 compatible, macOS).
- Runtime deps: **git, herdr, jq** (jq only in the `bin/obor` shim) + the binary. No python, node or bun in shipped
  code.
- Timeouts through `context`; processes that may leave children run as a process group (`run.Group`).
- Hooks: fast, no network, never block on herdr; no-op outside an obor project (no `.obor/config.json`).

## Layout
`.claude-plugin/plugin.json` · `skills/` · `hooks/hooks.json` · `bin/obor` (the one bash shim) · `cmd/obor/` +
`internal/` (Go) · `kinds/opencode/` (the kind's own binary) · `githooks/` · `templates/` · `tests/` · `docs/`
(this repo's own process; your spec comes in your first prompt (`obor spec show T-xx`) — edit only in your lane;
no per-file list is enforced: the integrator checks placement against `docs/module-map.md` at accept).

## Helpers
- One owner per helper: grep `internal/` for a twin before writing one. No line-count targets: simpler, not shorter.
- A name obor can derive is never stored; state is keyed by slot or task (an agent's name
  comes from `project.Self`, never an exported `OBOR_AGENT`).
- An empty derived value is an error, never a default: a name, role, path or id that obor derives and gets back empty
  (or unknown) fails closed with a message naming what is missing; no silent fallback to "no role", "no agent" or the
  main checkout.
- A task that deletes a writer adds its file to the nightly sweep's retired list (`internal/project/sweep.go`):
  the sweep lists the retired names, never an allowlist of the live ones.

## Tests
- Where tests run: the merge slot runs `make check-compile` (build, gofmt, vet, deadcode; no `go test`);
  the task's lane runs its class's check, for Go `make check-affected` (`make -s build` for the `dist/obor` that picks
  the packages, then `go test` on the changed packages and their importers); the full `make check` (every test) runs at
  `obor nightly` and before a release; `tests/release` only under `-tags release`
  (`make check-release`).
- One split, not two tiers: a test that needs real I/O (real git, herdr's socket, a built binary) goes
  in a file with `//go:build integration`, a plain `go test` runs the unit tests, and every gating or measuring check
  passes `-tags integration` so the tagged tests still gate. No other split exists: nothing skips a test in a lane.
  `internal/app`'s rig tests run in a `testing/synctest` bubble on in-memory connections (`bubble`,
  `internal/app/rig_ext_test.go`): no wait in them is wall-clock time; a test that needs a unix socket is in an
  `_integration_test.go` file.
- The suite has a budget: `tests/budget.json` caps each package's full wall time at 2 cores and its
  git processes; the nightly's `make check-budget` prints `FAIL budget: …` per breach. A budget is raised only by an
  owner line in `docs/decisions.md`; a task that gets under it lowers it.
- Test rules (test rework, `docs/plan/test-suite-rework.md`):
  - Real git only in `internal/checks`, the CLI tests (`cmd/obor`) and `internal/setup`'s githook tests (hooks that
    git itself runs): the code that runs git. Anywhere else a test uses the package's fake (a `FakeScratch`, a stub
    runner), not a fixture repo.
  - Even there, a new test starts git or a built binary only for a git/exec behaviour no existing test covers (owner
    11-09). A decision, a refusal or a message (what to write, when to refuse, which text) is a unit test with the
    package's fake or a function call; the one existing integration test of that write path keeps covering git.
  - A task that replaces a mechanism deletes the old mechanism's tests with it; a new test per behaviour, not per
    call path already covered.
  - A spec names its expected test change (added, changed, deleted; `templates/task-spec.md`): a task that adds
    tests without a line for them is a review finding.
- No load runs: one plain `go test <pkgs>` at a time — `-count` > 1, `-race` and two test commands at once
  (`&`, `xargs -P`, `parallel`) are denied by the guard for every role; the gate and nightly run the rest (the nightly
  runs the suite under `-race` as a non-gating line, `make check-race`).
- Tests are Go only (`go test ./...`; no bash suite). Tests build fixture repos in a temp dir and put a stub `herdr`
  on `PATH`; they never touch this repo's git state or real herdr panes.
- Tests stub herdr. Never link a real herdr plugin from a test or probe: herdr's plugin registry is user-global (even
  with `HERDR_CONFIG_PATH`), so it would act on the user's real panes. A live probe uses only a throwaway named
  session (`herdr --session <probe-name>`) in a scratch dir and never addresses the default session.
- A task that touches agent identity, the MCP server or how an agent is launched runs `make conformance TIER=live
  TURN=1 KIND=<kind>` for each agent kind it affects (claude, codex, opencode) before `task_done`, and puts each
  summary line in its report: stubs do not catch a real CLI's behaviour.

## Workflow
Workers: read your spec and the `docs/design.md` sections it cites; the rest (tests to run, no git writes) is the
worker role text `templates/agents/worker.md` + `docs/agents/worker.md`. Never run the full suite in a lane:
`obor serve`'s merge loop runs it once as the gate.
A job is fetched, not typed: the first prompt is the constant wake `[obor job <id>] call the obor
job_next tool`; call `job_next` first. A worker finishes only with `task_done`, its report as the argument (there is
no done file). `obor setup mcp` shows whether the `obor mcp` server is registered per agent kind (Claude: the plugin
manifest's `mcpServers`; Codex: launch args; OpenCode: each start's generated config).
