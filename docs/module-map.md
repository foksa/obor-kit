# Module map

obor is one app per project (`obor serve`): a **core** that owns task state and reacts to events, and **adapters**
that each talk to one outside system. Agents never message each other; the app reacts.

**Addressing.** The core addresses everything as (machine, workspace, pane). A project is a herdr workspace, and obor
never touches panes in other workspaces. Only the `herdr` and `checks` adapters know how to reach a machine;
multi-machine itself is later, but no interface may assume local.

## Layers and import rules

The layers are base, bridge, core, adapter, view, entry, test helpers and tools (probes under `tests/`). The enforced table
(which package is in which layer, and who may import whom) is `layerOf` in `internal/checks/boundary_test.go`; the merge
gate runs it. Adapters never import each other: one that needs another goes through an interface the core defines
(e.g. `launch` gets panes via `app.PaneControl`, not `herdr`). A package with no layer fails the test: add it to
`layerOf`.

## Packages

**base** — shared leaves, no obor state of their own.
- `project`: find the project, main checkout, lane dirs, session role/agent. `project.Find(dir)`, `Env`; the file lock (`lock.go`: `Lock`),
  `LabelOurs` (whether a labelled pane is this project's), the sid files and the opencode plugin marker (`plugin.go`),
  the machine's list of obor projects by name (`projects.go`, T-646: `Register`, `Projects`, `RootOf`).
- `config`: effective config. `config.Load(root) (*Config, error)`; `Validate` (removed keys WARN), `TaskIDOK` /
  `TaskFromBranch`.
- `roles`: role names and defaults. `spec`: a spec text's title, sections and globs (the text is the store's).
- `run`: commands under a timeout as a process group; machine-wide check slots; receipts. `run.Group`, `run.Slots`.
- `version`: build version line.
- `store`: the project's SQLite database (`.obor/state/obor.db`) and the rules of one writer: `store.OpenRW` for
  serve (WAL, `synchronous=FULL`, one connection, schema version in `meta`), `store.OpenRO` for hooks and CLIs
  (read-only, short `busy_timeout`), `store.Backup`/`BackupDaily` (`VACUUM INTO`). Tables: `events` (T-435),
  `lanes` (T-439: each lane's dispatched task, serve's projection of the log; readers use `store.ReadLaneAt` /
  `ReadLanesAt`), `specs` (T-440: each task's spec and rev; `obor spec set` is a serve request, readers use
  `store.ReadSpecAt` / `ReadSpecsAt`; the gate puts the current rev into every merge's message, T-521), `prompts`
  (T-665, T-670: the text of every job, brief and start prompt serve sent, by the seq of the fact it belongs to and
  its own id, one row per distinct text a job's fetch returned; serve writes it, readers use `store.ReadPromptsAt` /
  `ReadPrompts`, pruned to 14 days at the nightly backup; `store.OpenAt` is the read-only handle a reader that runs
  several queries holds, as obor web's event poll does). The agents'
  sessions (T-577) are `events` facts on task key `session` (`sessions.go`: `store.Session`, readers
  `ReadSessionsAt` / `CurrentAt` / `TranscriptAt` for launch and the hooks).

**bridge** — `agents` and one package per agent kind (`agents/claude`, `agents/codex`, `agents/opencode`; phase 5,
T-495): **what** differs per agent CLI, as data and pure functions; standard library only,
imports no obor package (every other layer may import it). `agents.Kind`: `Name`, `Start(Start) []string`
(fresh/resume argv, MCP registration, first prompt; `Start.Read` is the adapter's file read, a `--mcp-config` path),
`NotReady(screen)`, `Stop() Stop` (quit recipe), `Headless() Headless` (argv forms the guard denies), `Install(kit, env)
Install` (MCP registration, plugin link), `Transcript(env, cwd, sid)`, `Session(first line)` (the session a
transcript begins with: Codex's `session_meta`, T-577), `Deliver(text, record) (Delivery, bool)` (typed
vs inbox post), `Caps()` (hooks transport — the plugin marker is `Caps().Hooks == HooksPlugin` —, inbox, resume,
transcript). `Install` and `Transcript` take the environment (config dirs: `XDG_CONFIG_HOME`, `CLAUDE_CONFIG_DIR`,
`CODEX_HOME`), `Transcript` a cwd (Claude's per-cwd check of a recorded sid). Registry: `agents.Lookup`, `Names`,
`Default`. All I/O stays in the adapters, which ask the kind what differs; non-lead default settings stay in
`config.LaneType.LaunchArgs`, which asks the kind to render them. No kind name outside the bridges (boundary test).
Adding a kind = one package + a config entry.

**core** — `app`: the only place with obor logic. Files by concern: `app.go` (types, options), `serve.go` (New, Serve,
  run, the serve flock), `project.go` (load, log, fold), `log.go`, `panes.go` (herdr events to projects),
  `watchdog.go`, `notify.go`, `status.go`; the merge loop: `loop.go` (wake, advance, record, do, drive, the picks),
  `mtask.go` (loop state fold), `states.go` (the state table), `steps.go` (scratch/after steps, submit, gate, publish,
  unpin, fix), `jobstart.go` (job start, texts), `settle.go`; `registry.go` (agent identity: `reconcile` derives the
  project's agent ↔ terminal table from proofs; `whereIs`/`whoIs` the only lookups, never herdr's name; T-504);
  `lanerows.go` (the `lanes` table projection), `specs.go`, `prompts.go` (the `prompts` table's writes and prune,
  T-665, T-670), `dispatch.go`, `messages.go`, `mergewatch.go`,
  `stopped.go`, `stopwake.go`, `rolestop.go` (serve stops the integrator and the reviewer once done, T-672), `nightly.go`,
  `pause.go`, `sessions.go` (the agents' recorded sessions, T-577),
  `answer.go`, `verdict.go`, `jobs.go`, `prompt.go`,
  `effects.go`: one concern each.
- Task state and the per-project event log (the `events` table of `.obor/state/obor.db`; intent before an effect,
  result after).
- Scheduler rules: readiness (`--after`), dispatch, accept/reject/sendback, moved target, the integrator slot (no cap
  yet).
- Watchdogs: idle with a task, working without activity, blocked, pane exited.
- Interfaces it drives adapters through: `Panes` (subscribe, busy, activity, notify), `Lanes` (a project's lanes), and,
  on its `Loop`, `Scratch` (submit/gate/resolved/publish/after_merge/landed/unpin), `Agents` (launch/stop), `Typer`
  (a job's wake, a line typed into a pane), `Poster` (posted to a session's inbox), `Specs`, `Nightly` and `Restart` (serve's own restart after a code merge, T-652).
  API: `app.New(Options, Panes, Lanes)`, `(*App).Serve(ctx)`.

**adapters** — one outside system each.
- `herdr`: herdr socket client (events.subscribe, session.snapshot, agent.prompt, pane.*). Implements `app.Panes`
  and `app.Typer`; `herdr/herdrtest` is its fake (test helper).
- `checks`: git and the merge gate: lanes (`checks.Lane`, `LaneSetup`), serve's worktree `merge.scratch_dir` (submit,
  gate, resolved, publish, after_merge over pins under `refs/obor/`), nightly/milestone checks in their own worktree,
  flakes, receipts, and the lane table (`LaneTable`: worktree facts for `obor status` and `app.Lanes`, merge evidence).
  Implements `app.Scratch`.
- `launch`: start, stop (the kill sequence) and restart an agent CLI in its pane (`Launcher`, panes via
  `app.PaneControl`; implements `app.Agents` and `app.Starter`); the per-lane restart lock (`restartlock.go`:
  `RestartLock`, T-500); the herdr plugin's restore (`Restorer`); `session.go`: a fresh start's session from the
  transcript its process holds open (a Codex rollout, T-588), reported to serve.
- `mcp`: stdio shim from an agent to the app's socket; typed tools per role: the worker's `job_next`/`task_done`, the
  integrator's jobs and verdicts. The lead gets no tools: its chat work goes through the CLI, one request path.
- `guard`: the Claude Code hooks: PreToolUse (Bash parse, edit checks, the headless ban), Stop, StopFailure,
  SessionStart, UserPromptSubmit, whoami.
- `setup`: make a project obor-ready (config, lanes, panes, roles, githooks, herdr/opencode plugin, MCP report, the
  monitor pane `monitor.go`, T-658; it starts agents through an `app.Starter` that `cmd/obor` injects); `obor init`
  (`oborinit.go`, P7-2: the steps chained with no questions; serve install through `Setup.Serve`, which `cmd/obor` wires
  to `service`).
- `affected`: affected Go packages for a Go project's per-merge check (`obor affected go`).
- `service`: the project's launchd agent (T-596; darwin only, elsewhere each verb says to run `obor serve` yourself):
  the plist `~/Library/LaunchAgents/obor.serve.<name>.plist` and `launchctl` bootstrap / bootout / kickstart / print
  for `obor serve install|uninstall|restart` and `obor status`' serve line.

**view** — `view` (T-664): the project's read model. `view.Build(view.Input{Status, Err, Lanes, DB, Now, Events})`
joins serve's status answer (none when serve is down, with its error), the git lane table (`checks.LaneTable`) and the
store (read-only: specs, events) into one `view.Project`: serve, running and pending, the lanes (state column, spec
rev, open job), serve's tasks and agents, the role agents, needs-you, the full check, the pause, the lines, the queue
(merge loop with stages and the next by id, waiting dispatches: after | lane | never dispatched, the review request,
the outbox, pause, nightly) and the newest worded events (seq, ts, task, line). `view.Load` is that build for one
project at a root (serve's answer, the lane table, the store), as `obor web` builds every project the machine knows.
`view.Detail` is the drill-down the API serves and nothing else: a task's state, spec, report, gate line and log,
needs-you, records and jobs; an agent's sessions (a transcript path and last write, never contents), briefs, lines and
jobs; a job's text (a `prompts` row). `EventsSince` pages the log by seq for the page's long poll. It may import base,
core and the adapters; only entry packages import it. **Every reader of project state goes through `view.Build`**:
`obor status` (`--json` prints `view.Project` as is), `obor monitor` and `obor web` render the same struct, so a field
added for one appears in all of them; no second builder.

**test helpers** — `testkit` (fixture projects, the store helpers tests use) and `herdr/herdrtest` (the fake herdr
socket: scripted panes and plugins, and every request a test's client sent).

**entry** — `cmd/obor`: flags and wiring only; `obor status` and `obor monitor` (`monitor.go`: the loop and the alternate-screen drawing; `monitor_render.go`: the look; T-658, T-668) rendering of
`view.Project` (rendering only: no state is joined there); the
thin `app.Specs`, `Lanes`, `Poster` adapters live here. The lead's chat commands are the same CLI (`status`, `dispatch`,
`answer`, `resume`, `sendback`, `drop`, `spec`, `watch`, `pause`, `send`, `projects`, `brief`), beside `serve`, `mcp`,
`setup`, `config` and the rest (`run`, `nightly`, `milestone-check`, `affected`, `flakes`, `restart`, `lead`, `plugin`,
`hook`, `whoami`).
- `web` (`internal/web`, T-667): `obor web`'s read-only HTTP server over the read model — the API (`/api/projects`,
  `/api/view`, `/api/task`, `/api/agent`, `/api/job`, `/api/events` with a long poll on the store's seq) and one
  embedded HTML page (`embed`, vanilla JS, no build step), standard library only. Localhost only, no verbs; its own
  process, never inside serve.
