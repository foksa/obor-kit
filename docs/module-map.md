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
  `LabelOurs` (whether a labelled pane is this project's), the sid files and the opencode plugin marker (`plugin.go`), the background Bash calls the PreToolUse hook records
  (`background.go`, T-714),
  the machine's list of obor projects by name (`projects.go`, T-646: `Register`, `Projects`, `RootOf`); a stopped
  project (`stopped.go`, T-709, T-711: `Stopped`, `WriteStopMarker` (the pause stop replaced), `BeginStart`/`EndStart`, `Unregister`: obor stop / obor start).
- `config`: effective config. `config.Load(root) (*Config, error)`; `LoadReader` (obor status, obor stop/start, web: an
  unknown key is a warning, T-709); `Validate` (removed keys WARN), `TaskIDOK` /
  `TaskFromBranch`; `edit.go` (T-753): `Edit`, the one setup change obor web may make, applied to config.json's bytes
  in their own layout, and `LoadWith` (what it would load as).
- `roles`: role names and defaults. `spec`: a spec text's title, sections and globs (the text is the store's).
- `run`: commands under a timeout as a process group; machine-wide check slots; receipts. `run.Group`, `run.Slots`.
- `version`: build version line.
- `help` (T-854): the one list of obor commands (`help.Commands`: name, usage, description, group), printed by
  `obor help` and shown by obor web's Help page, and README's "Install" and "Set up a project" sections (`Sections`).
- `selfexec`: a long-running obor process follows a new obor binary (T-719): `Candidate` (the binary the kit's shim
  would run now), `Watch.Changed`, `Exec` (the shim, in place). Callers: serve, web.
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
`NotReady(screen)`, `StartEnv` (a start's own env: OpenCode's `OBOR_OPENCODE_MODEL` for obor's plugin, T-703), `Stop() Stop` (quit recipe), `Headless() Headless` (argv forms the guard denies), `Install(kit, env)
Install` (MCP registration, plugin link), `Transcript(env, sid)` (a glob: the file of that session id, no cwd — a
kind's directory for it is the kind's own, T-813), `Session(first line)` (the session a
transcript begins with: Codex's `session_meta`, T-577), `Deliver(text, record) (Delivery, bool)` (typed
vs inbox post), `Caps()` (hooks transport — the plugin marker is `Caps().Hooks == HooksPlugin` —, inbox, resume,
transcript, a named session: `NamedSession`, obor chooses the id of a fresh start, T-813). `Install` and `Transcript`
take the environment (config dirs: `XDG_CONFIG_HOME`, `CLAUDE_CONFIG_DIR`, `CODEX_HOME`). Registry: `agents.Lookup`, `Names`,
`Default`. All I/O stays in the adapters, which ask the kind what differs; non-lead default settings stay in
`config.LaneType.LaunchArgs`, which asks the kind to render them. No kind name outside the bridges (boundary test).
Adding a kind = one package + a config entry.

**core** — `app`: the only place with obor logic. Files by concern: `app.go` (types, options), `serve.go` (New, Serve,
  run, the serve flock), `project.go` (load, log, fold), `log.go` (serve's side of the backup), `panes.go` (herdr events to projects),
  `watchdog.go`, `lanescan.go` (the lane table as a cached observation: scanned on the tick and by a refresher, never
  per pane event; T-879), `notify.go`, `status.go` (the status snapshot and `Listen`: the socket's server half); the merge loop: `loop.go` (wake, advance, record, do, drive, the picks),
  `mtask.go` (loop state fold), `states.go` (the state table), `steps.go` (scratch/after steps, submit, gate, publish,
  unpin, fix), `jobstart.go` (job start, texts), `settle.go`; `registry.go` (agent identity: `reconcile` derives the
  project's agent ↔ terminal table from proofs; `whereIs`/`whoIs` the only lookups, never herdr's name; T-504);
  `lanerows.go` (the `lanes` table projection), `specs.go`, `prompts.go` (the `prompts` table's writes and prune,
  T-665, T-670), `dispatch.go`, `drive.go` (the owner's drive and land: `obor drive`, `obor land`, the driving state, T-751), `messages.go`, `mergewatch.go`,
  `rules.go` (the rule pass: rule-tier steps, T-866), `idle.go` (the idle rules and `obor idle`), `stopped.go`, `stopwake.go`, `rolestop.go` (the role stop rules: serve stops the integrator and the reviewer once done, and a fresh brief's stop; T-672, T-870), `workerstart.go` (the worker start rule: a lane's worker started again while its job is outstanding; T-874), `nightly.go`,
  `configset.go` (the owner's setup edit from obor web, through the `ConfigFile` port, T-753),
  `pause.go`, `exit.go` (the socket's exit request: serve ends gracefully, obor stop, T-711), `sessions.go` (the agents' recorded sessions, T-577),
  `resume.go`, `verdict.go`, `jobs.go`, `prompt.go`,
  `effects.go` (`JobID`): one concern each.
- `app/port` (core, a leaf below `app`; T-690): the ports and the types they exchange, with no logic: herdr's `Pane`,
  `Snapshot`, `Event`, `Stream`, `Panes`; `Lane`, `Lanes`; and the effects (`Agents`, `Typer`, `PaneControl`, `Starter`,
  `Scratch`, `LaneSetup` and their inputs and outcomes). Adapters import it instead of `app`; `app` keeps type and const
  aliases of it until a later task removes them (`app/ports.go`).
- `app/event` (core, a leaf below `app`; T-691): the event log's vocabulary and its store: `Record`, `Key`, `Intent`,
  `FactData`, the `Kind*`/`Class*` constants, every exported `Step*` and the task keys (`MsgTask` …), the fact data
  types readers decode (`Pause`, `Message`, `MergedData`), `Log` (`OpenLog`, `Append`, `Do` …), the readers (`ReadLog`,
  `Dump`, `RecentEvents`, `DBFile`) and `Merge`/`ReadMerges`, and `StepN`. It imports `store` only. `app` keeps type, var and const
  aliases of it until a later task removes them (`app/events.go`).
- `app/wire` (core, a leaf above `app/event`; T-692): the app socket's client half, what a caller can ask serve: every
  `Method*`, `Call`, `Request`/`Answer`, `Ask`, `Verdict` (with its retries and `OBOR_CALL_DEADLINE`), `SocketPath`,
  `NewCallID`, `JobReply`, `ResumeArgs` and `Resume*`, the status types (`Status`, `ProjectStatus`, `TaskStatus` …,
  `Scratch*`) and the `Backup` client. It imports `app/event`, `store` and `project`, never `app`; `mcp`, `launch`,
  `checks`, `view` and `web` import it instead of `app`. `app` keeps type, var and const aliases of it until a later task
  removes them (`app/wire.go`). `Listen` and the handlers stay in `app`.
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
  flakes, receipts, and the lane table (`LaneTable`: worktree facts for `obor status` and `app.Lanes`, merge evidence; `Lanes` is serve's
  `app.Lanes` adapter, each lane's configured type, T-738).
  Implements `app.Scratch`.
- `launch`: start, stop (the kill sequence) and restart an agent CLI in its pane (`Launcher`, panes via
  `app.PaneControl`; implements `app.Agents` and `app.Starter`); the per-lane restart lock (`restartlock.go`:
  `RestartLock`, T-500); the herdr plugin's restore (`Restorer`); `session.go`: the id obor names a fresh Claude
  start and records before it runs (T-813), and a fresh Codex start's session from the transcript its process holds
  open (a Codex rollout, T-588), both reported to serve.
- `mcp`: stdio shim from an agent to the app's socket; typed tools per role: the worker's `job_next`/`task_done`, the
  integrator's jobs and verdicts. The lead gets no tools: its chat work goes through the CLI, one request path.
- `guard`: the Claude Code hooks: PreToolUse (Bash parse, edit checks, the headless ban), Stop, StopFailure,
  SessionStart, UserPromptSubmit, whoami.
- `setup`: make a project obor-ready (config, lanes, panes, roles, githooks, herdr/opencode plugin, MCP report; it
  starts agents through an `app.Starter` that `cmd/obor` injects); `obor init`
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
`background.go` (T-714) is the agents' long-running background processes (`Input.Background`, by agent name;
`ScanBackground`: one `ps` per build, each agent pane's foreground leader from herdr process-info, its descendants
running > 30 s through its shells, helpers left out), labelled from the PreToolUse hook's records
(`project.AppendBackground` / `BackgroundCalls`, `.obor/state/background.jsonl`).
`view.Detail` is the drill-down the API serves and nothing else: a task's state, spec, report, gate line and log,
needs-you, records and jobs; an agent's sessions (a transcript path and last write, never contents), briefs, lines and
jobs; a job's text (a `prompts` row). The pages read narrow (T-880): `view.LoadTask`, `view.LoadAgent` and
`view.LoadSetup` build only their page's facts (a task or agent page runs no git and no ps; setup reads the lane
states). `EventsSince` pages the log by seq for the page's long poll. It may import base,
core and the adapters; only entry packages import it. **Every reader of project state goes through `view`**:
`obor status` (`--json` prints `view.Project` as is) and `obor web` render the same struct, so a field added for one
appears in both; the narrow page reads share its mapping helpers, and there is no second Project builder.

**test helpers** — `testkit` (fixture projects, the store helpers tests use) and `herdr/herdrtest` (the fake herdr
socket: scripted panes and plugins, and every request a test's client sent).

**entry** — `cmd/obor`: flags and wiring only; `obor status`' rendering of
`view.Project` (rendering only: no state is joined there); the
thin `app.Specs`, `Lanes`, `Poster` adapters live here (and `configFile`, `app.ConfigFile` on the main checkout's git, T-753). The lead's chat commands are the same CLI (`status`, `dispatch`,
`resume`, `drop`, `drive`, `land`, `spec`, `watch`, `pause`, `send`, `projects`, `brief`), beside `serve`, `mcp`,
`setup`, `config` and the rest (`run`, `nightly`, `affected`, `flakes`, `restart`, `lead`, `plugin`,
`hook`, `whoami`).
- `web` (`internal/web`, T-667): `obor web`'s HTTP server over the read model — the API (`/api/projects`,
  `/api/view`, `/api/setup`, `/api/task`, `/api/agent`, `/api/job`, `/api/events` with a long poll on the store's seq,
  `/api/help`: `help.Commands` and the kit's README sections, read at the request)
  and the page (T-760): a Svelte app whose source is `web/` at the repo's root, built by `make web` into
  `internal/web/dist` (committed, `embed`; the Go build needs no Node, `TestDistFresh` fails on a stale build).
  Localhost only; its own process, never inside serve. Its two verbs (T-753), `POST /api/config` (a list of changes and
  the config revision they were made on, one commit, T-760) and `POST /api/pause`, are requests on the project's app
  socket (`wire`), taken only from the page's own origin: serve writes.
