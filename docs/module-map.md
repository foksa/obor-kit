# Module map

obor is one app per project (`obor serve`): a **core** that owns task state and reacts to events, and **adapters**
that each talk to one outside system. Agents never message each other; the app reacts.

**Addressing.** The core addresses everything as (machine, workspace, pane). A project is a herdr workspace, and obor
never touches panes in other workspaces. Only the `herdr` and `checks` adapters know how to reach a machine;
multi-machine itself is later, but no interface may assume local.

## Layers and import rules

The layers are base, bridge, core, adapter, view, entry, test helpers, tools (probes under `tests/`, the kit's own
programs under `tools/`: `affected`, the Go packages a change can break, run by the Makefile's `check-affected`)
and kind binaries (`kinds/<name>`: a plugin kind the kit ships as its own binary; it imports the bridge
and its own tree only). The enforced table
(which package is in which layer, and who may import whom) is `layerOf` in `internal/checks/boundary_test.go`; the merge
gate runs it. Adapters never import each other: one that needs another goes through an interface the core defines
(e.g. `launch` gets panes via `app.PaneControl`, not `herdr`). A package with no layer fails the test: add it to
`layerOf`.

## Packages

**base** — shared leaves, no obor state of their own.
- `project`: find the project, main checkout, lane dirs, session role/agent. `project.Find(dir)`, `Env`; the file lock (`lock.go`: `Lock`),
  `LabelOurs` (whether a labelled pane is this project's), the sid files and the opencode plugin marker (`plugin.go`), the background Bash calls the PreToolUse hook records
  (`background.go`), the caller's own pane (`pane.go`: `Pane`, the one resolver: `OBOR_PANE`, else the
  terminal's `HERDR_PANE_ID` for a pane obor did not start, else `ErrNoPane`; here, not in `herdr`, because adapters
  never import each other),
  the machine's list of obor projects by name (`projects.go`: `Register`, `Projects`, `RootOf`); a stopped
  project (`stopped.go`: `Stopped`, `WriteStopMarker`, `BeginStart`/`EndStart`, `Unregister`: obor stop / obor start);
  `sweep.go`: the nightly sweep of `.obor` (daily backups past the newest 7 dates, gzip'd or the older
  uncompressed ones, and only after a successful backup; files of lanes and agents the config does not
  names, the retired state names, old drafts, the logs nothing needs (the names no code writes, and
  `launch.log`/`restart-<agent>.log` 14 days unwritten; a task's step logs are serve's), `flakes.jsonl`'s
  entries older than the 7 days `obor flakes` reads, and stop-failure entries).
- `config`: effective config. `config.Load(root) (*Config, error)` (every command's, serve's: an unknown key is ignored
  with a warning); `LoadReader` (obor status, obor stop/start, web: the warning returned);
  `LoadForValidate` (obor config validate, the one strict place: a FAIL line per unknown key); `Validate` (removed keys
  WARN), `TaskIDOK` /
  `TaskFromBranch` / `TaskBranch`; `edit.go`: `Edit`, the one setup change obor web may make, applied to config.json's bytes
  in their own layout, and `LoadWith` (what it would load as).
- `background`: the agents' live processes, the one check of them — the machine's process table
  (`ReadProcs`, `ParseProcs`, `Proc`), a recorded background Bash call matched to the process it started
  (`Match` by command and time; `Live`, the newest call still running in an agent's descendants; `Mine`, an agent's
  calls; `Text`), an agent's descendants (`ProcIndex.Descendants`) and a hook's agent (`AgentOf`). The view's
  background scan and the Stop hook's block both read it.
- `roles`: role names and defaults. `spec`: a spec text's title, sections and globs (the text is the store's).
- `run`: commands under a timeout as a process group; machine-wide check slots. `run.Group`, `run.Slots`.
- `version`: build version line.
- `agents/plugin`: a plugin kind, an `agents.Kind` whose CLI-specific part is an executable answering the kind
  protocol (`docs/agents/kind-protocol.md`), run as a process group under a per-call timeout. `config` registers the
  kinds its `kinds.<name>.command` name through `agents.Plugins` (it cannot import this package) on every load, each with
  the manifest obor serve, `obor init` or `obor setup agents` cached (`plugin.Ensure`, `plugin.Cache`,
  `.obor/state/kinds/<name>.json`): no process runs at load.
  Base, not bridge: it runs processes and writes the cache. `Local`: a kind answered in process by its own Go
  handler instead of its command (testkit's OpenCode; empty in obor).
- `help`: the one list of obor commands (`help.Commands`: name, usage, description, group), printed by
  `obor help` and shown by obor web's Help page, and README's "Install" and "Set up a project" sections (`Sections`).
- `selfexec`: a long-running obor process follows a new obor binary: `Candidate` (the binary the kit's shim
  would run now), `Watch.Changed`, `Exec` (the shim, in place). Callers: serve, web.
- `store`: the project's SQLite database (`.obor/state/obor.db`) and the rules of one writer: `store.OpenRW` for
  serve (WAL, `synchronous=FULL`, one connection, schema version in `meta`), `store.OpenRO` for hooks and CLIs
  (read-only, short `busy_timeout`), `store.Backup`/`BackupDaily` (`VACUUM INTO`; a daily backup is gzip'd,
  `obor-<YYYY-MM-DD>.db.gz`). Tables: `events`,
  `lanes` (each lane's dispatched task, serve's projection of the log; readers use `store.ReadLaneAt` /
  `ReadLanesAt`), `specs` (each task's spec and rev; `obor spec set` is a serve request, readers use
  `store.ReadSpecAt` / `ReadSpecsAt`; the gate puts the current rev into every merge's message), `prompts`
  (the text of every job, brief and start prompt serve sent, by the seq of the fact it belongs to and
  its own id, one row per distinct text a job's fetch returned; serve writes it, readers use `store.ReadPromptsAt` /
  `ReadPrompts`, pruned to 3 days at the nightly backup; `store.OpenAt` is the read-only handle a reader that runs
  several queries holds, as obor web's event poll does). The agents'
  sessions are `events` facts on task key `session` (`sessions.go`: `store.Session`, readers
  `ReadSessionsAt` / `CurrentAt` / `TranscriptAt` for launch and the hooks).

**bridge** — `agents` and one package per compiled agent kind (`agents/claude`, `agents/codex`; phase 5):
**what** differs per agent CLI, as data and pure functions; standard library only,
imports no obor package (every other layer may import it). `agents.Kind`: `Name`, `NotReady(screen)`, `Stop() Stop` (quit recipe), `Headless() Headless` (argv forms the guard denies), `Install(kit, env)
Install` (MCP registration), `Transcript(env, sid)` (a glob: the file of that session id, no cwd — a
kind's directory for it is the kind's own), `Session(first line)` (the session a
transcript begins with: Codex's `session_meta`), `Deliver(text, record) (Delivery, bool)` (typed
vs inbox post), `Caps()` (hooks transport — the plugin marker is `Caps().Hooks == HooksPlugin` —, inbox, resume,
transcript, a named session: `NamedSession`, obor chooses the id of a fresh start; `Status`, where the status and
turns come from: `terminal`, `hooks`, `transcript`). A kind starts as `agents.Split` (a compiled kind:
`Start(Start) []string`, the fresh/resume argv, MCP registration and first prompt, `Start.Read` the adapter's file read;
`StartEnv`, a start's own env) or `agents.Whole` (a plugin kind's one start call: its argv, env and files together;
a plugin kind has no `Start`/`StartEnv`). Registry: the compiled kinds register from their init; a
config load replaces the plugin kinds (`SetPlugins`; OpenCode's the kit's binary, `kinds/opencode`); a start is
built once at the launch boundary from the complete `agents.Start` (`agents.StartOf`), and a plugin kind's start and
install can fail (`StartOf`, `InstallOf`). `Install` and `Transcript`
take the environment (config dirs: `XDG_CONFIG_HOME`, `CLAUDE_CONFIG_DIR`, `CODEX_HOME`). Registry: `agents.Lookup`, `Names`,
`Default`. All I/O stays in the adapters, which ask the kind what differs; non-lead default settings stay in
`config.Model.Launch`, which asks the kind to render them. No kind name outside the bridges and the kind binaries (boundary test).
Adding a kind = a plugin kind's executable (`docs/agents/kind-protocol.md`) + `kinds.<name>.command`.

**kind binaries** — `kinds/opencode`: the OpenCode kind as the reference plugin kind, its own
binary `obor-kind-opencode` (`main.go`: stdin → `kind.Handle` → stdout) beside obor's. Package `kinds/opencode/kind` is
the kind: the manifest, `start` (the args without `--model`, `--prompt`; `OBOR_OPENCODE_MODEL` model#effort and
`OPENCODE_CONFIG`, the generated config with obor's MCP server and plugin `<kit>/opencode`), `turn` (a `rate_limit`'s
reset), `project-warnings`. It imports `agents` only (`Pin`, `ResetIn`); testkit registers its handler in
process (`plugin.Local`).

**core** — `app`: the only place with obor logic. Files by concern: `app.go` (types, options), `serve.go` (New, Serve,
  run, the serve flock), `restore.go` (the restore of the project's panes after a lost terminal connection came
  back, through the `Restorer` port, and at serve's first connect when herdr restarted while serve was down:
  the instance it last connected to in `.obor/state/herdr-instance`), `project.go` (load, log, fold), `log.go` (serve's side of the backup), `panes.go` (herdr events to projects),
  `watchdog.go`, `lanescan.go` (the lane table as a cached observation: scanned on the tick and by a refresher, never
  per pane event), `toast.go` (the owner's herdr toast, a rule action's best-effort after), `status.go` (the status snapshot and `Listen`: the socket's server half); the merge loop: `loop.go` (wake, advance, record, do, drive, the picks),
  `mtask.go` (loop state fold), `states.go` (the state table), `steps.go` (scratch/after steps, submit, gate, publish,
  unpin, fix), `jobstart.go` (job start, texts), `fetch.go` (jobs by fetch: the wake, the first fetch's delivered fact,
  the job a call names), `finish.go` (`awaitsTaskDone`: whose attempt is its worker's and no task_done yet;
  `inLoop`: what the merge loop holds), `worktext.go` (the work job's wake: its text, the send to the worker and the
  watch on its delivery), `settle.go`; `registry.go` (agent identity: `reconcile` collects the proofs
  the `app/identity` table accepts; `whereIs` and the table's `WhoIs` the only lookups, never herdr's name);
  `lanerows.go` (the `lanes` table projection), `specs.go`, `prompts.go` (the `prompts` table's writes and prune), `prune.go` (the nightly prune of ended tasks' records and old sessions, 30 days on: which tasks; the
  rows' SQL is `event.PruneTasks` and `store.PruneSessions`; `mtask.ended` is the end both it and the logs' removal
  read), `logs.go` (a task's step logs go when it ends, and at load for every task the replayed log
  folds as ended: merged with its after-merge steps done, or dropped), `dispatch.go`,
  `waiting.go` (a dispatch that waits for the tasks it names (`--after`) or for a lane: fold, pick, dispatch),
  `lanepick.go` (the lane a dispatch takes and the replies that say why none was free), `drive.go` (the owner's drive and land: `obor drive`, `obor land`, the driving state), `messages.go` (the lines' delivery over `app/outbox`'s state: `deliver`, `route`, `recordBrief`, `foldMsg` applying what `Fold` ended), `mergewatch.go`,
  `review.go` (a review request: `obor review` records it and the merge loop fires the reviewer's start),
  `reviewclose.go` (a review's close is a call to serve, which commits it and records `review.closed`),
  `reviewonmerge.go` (the Review: on merge rule: a hard or risky task's spec asks for a review at its merge),
  `reviewverify.go` (a review's fix round and its verify, briefed in the reviewer's session),
  `rules.go` (the rule pass: rule-tier steps), `idle.go` (the idle rules and `obor idle`), `stopped.go`, `stopwake.go`, `stopstep.go` (a task worker's stop as a step of the merge loop, for a move or a drop), `move.go` (`obor dispatch --model` on a working task: a redo dispatch on the same lane and worktree), `rolestop.go` (the role stop rules: serve stops the integrator and the reviewer once done, and a fresh brief's stop), `workerstart.go` (the worker start rule: a lane's worker started again while its job is outstanding), `keeplane.go` (a keep lane's agent between its tasks: its live agent takes the next job, the handoff rule stops one past handoff_tokens), `nightly.go`,
  `configset.go` (the owner's setup edit from obor web, through the `ConfigFile` port),
  `verbs.go` (`leadOr`: the admission of a lead's and the owner's method, a custom role's by its `roles.<name>.verbs`),
  `feed.go` (the socket's `events` request: the event feed after a seq, streamed with `--follow`),
  `pause.go`, `exit.go` (the socket's exit request: serve ends gracefully, obor stop), `sessions.go` (the agents' recorded sessions),
  `resume.go`, `verdict.go`, `jobs.go`, `prompt.go`,
  `roleworktree.go` (role worktrees: serve moves the reviewer's and the deep dive's to the head it hands them),
  `block.go` (the merge loop's needs-you as a block on a task's phase), `disk.go` (low disk: free space below
  diskLow is a needs-you that holds the gate and the nightly), `versions.go` (the versions of the tools obor
  drives: the periodic read, fact `version`, the rule that lines the lead on a change), `fold.go` (`foldStart`: a start's result into the
  identity table: agent, pane, kind, session, model), `follow.go` (serve follows a new obor binary and takes the
  self-restart path, preferring an idle moment), `scheduler.go` (the scheduler's rule for serve's worktree: pass
  order, who holds the scratch, what a step waits),
  `effects.go` (`JobID`), `describe.go` (`Describe`: a record as the readers word it, decoded with the types serve
  writes it with; the view's one reader of record data): one concern each.
- `app/port` (core, a leaf below `app`): the ports and the types they exchange, with no logic: the terminal's `Pane`
  (terminal-neutral: its `Group`, no tab or workspace), `Snapshot`, `Event`, `Stream`, `Panes`; `Lane`, `Lanes`;
  `TerminalSetup` (setup's terminal: `Agents`, `Place` a `Placement`, `Integrate`, `RenameProject`); and the
  effects (`Agents`, `Typer`, `PaneControl`, `Starter`,
  `Scratch`, `LaneSetup`, `Review` and their inputs and outcomes, and `Workflow`, the git workflow behind one
  interface: those three and the dispatch's `Target`). Adapters import it instead of `app`.
- `app/event` (core, a leaf below `app`): the event log's vocabulary and its store: `Record`, `Key`, `Intent`,
  `FactData`, the `Kind*`/`Class*` constants, every exported `Step*` and the task keys (`MsgTask` …), the fact data
  types readers decode (`Pause`, `Message`, `MergedData`), `Log` (`OpenLog`, `Append`, `Do` …), the readers (`ReadLog`,
  `Dump`, `RecentEvents`, `RecordsOf`, `MessagesTo`, `Since`, `Facts`, `FactTasks`, `DBFile`: the view's queries) and `Merge`/`ReadMerges`, and `StepN`. It imports `store` only.
- `app/identity` (core, a leaf below `app`): a project's identity table, `Registry`, and its rules: the start
  rows (`RecordStart`, `Start`), `Reconcile` (the tie rules over `Claims`, the strongest proof of each agent and
  terminal), `Proof`, `CrossCheck`, the lookups `WhoIs` and `Lookup` (`whereIs`'s one query), `TermOf`, `SameSession`.
  Its maps are unexported, so only it names them. No lock: under `App.mu`. It imports `app/port` only, never `app`;
  `whereIs`, `why`, `candidates` and the proof collection (`App.reconcile`) stay in `app`'s `registry.go`.
- `app/outbox` (core, a leaf below `app`): a project's lines to its agents, `Outbox`: the undelivered lines
  (`Line`, its delivery `State` and `Next`), the open briefs (`Brief`, a role agent's `RoleStop`), the needs-you lines'
  delivery times, the line and stop fact data (`Stopped`, `Turned`, `Worked`, `LineData`), and `Fold`: a record of the
  lines folded, returning what it ended (`Ended`: a did key, a stored reply, a drive's or review's line, a worker's
  stop, an answered question, a brief's end) for `app`'s `foldMsg` to apply in its fold order. Queries `Lead`, `Brief`,
  `Briefs`, `Line`, `Queued`, `Waits`, `Delivered`, `Post`, `BriefStatus`, `MessageStatus`. Its maps are unexported, so
  only it names them. No lock: under `App.mu`. It imports `app/event` and `app/wire` only, never `app`; delivery
  stays in `app`'s `messages.go`.
- `app/review` (core, a leaf below `app`): a project's review cycle, `State`: the reviews the log started (start,
  finished verifies, close snapshot, close), the kernel's merges and the open verify briefs, and `Fold`: a record
  folded, returning a review fact's data (`Data`) for `app`'s `foldReview` (the requests, a call's stored reply) to
  apply in its fold order. What a review covered and the next one's scope are decided here only: the queries `Cycle`
  (covered, reviewed head, `Pending` over the fix `Link`s) and `Next` (id, scope, the review it follows), with `Last`,
  `Prev`, `Closing`, `Failed`, `MergeOf`, `BriefFile`; `Round` is the fix round's status line, `ID`/`Number` a review's
  id. Its fields are unexported, so only it names them. No lock: under `App.mu`. It imports `app/event` and
  `app/outbox` (a stop's data) only, never `app`; the effects (the start and its brief, the verify, the close's commit
  and crash read, the reviewer's stop, the on-merge request, dispatch's `Fixes:` check) stay in `app`'s review files.
- `app/wire` (core, a leaf above `app/event`): the app socket's client half, what a caller can ask serve: every
  `Method*`, `Call`, `Request`/`Answer`, `Ask`, `Verdict` (with its retries and `OBOR_CALL_DEADLINE`), `SocketPath`,
  `NewCallID`, `JobReply`, `ResumeArgs` and `Resume*`, the status types (`Status`, `ProjectStatus`, `TaskStatus` …,
  `Scratch*`) and the `Backup` client. The event feed (`feed.go`): `FeedRecord` (a record and its task's spec `Ref`), `FeedSince` (the
  one read of it: serve's and `obor events`' while serve is down) and the `Feed` client. It imports `app/event`, `store` and `project`, never `app`; `mcp`, `launch`,
  `checks`, `view` and `web` import it instead of `app`. `Listen` and the handlers stay in `app`.
- Task state and the per-project event log (the `events` table of `.obor/state/obor.db`; intent before an effect,
  result after).
- Scheduler rules: readiness (`--after`), dispatch, accept/reject/sendback, moved target, the integrator slot (no cap
  yet).
- Watchdogs: idle with a task, working without activity, blocked, pane exited.
- Interfaces it drives adapters through: `Panes` (subscribe, busy, activity, notify), `Lanes` (a project's lanes), and,
  on its `Loop`, `Scratch` (submit/gate/resolved/publish/after_merge/landed/unpin), `Agents` (launch/stop), `Typer`
  (a job's wake, a line typed into a pane), `Poster` (posted to a session's inbox), `Specs`, `Nightly` and `Restart` (serve's own restart after a code merge).
  API: `app.New(Options, Panes, Lanes)`, `(*App).Serve(ctx)`. Its rig tests (`*_ext_test.go`) run in a
  `testing/synctest` bubble on in-memory connections (`bubble`, `rig_ext_test.go`), and the ones that need a unix
  socket are `*_integration_test.go` files.

**adapters** — one outside system each.
- `herdr`: herdr socket client (events.subscribe, session.snapshot, agent.prompt, pane.*). Implements `app.Panes`
  and `app.Typer`, and `port.TerminalSetup` as `Setup` (`setup.go`: the layout of setup's placements in tabs,
  workspaces and splits, herdr's `resume_agents_on_restore` hint, the workspace rename); herdr's own `Pane` (tab and workspace ids) stays
  here; `cmd/obor`'s `terminal` builds the project's `port.Terminal` over these; `herdr/herdrtest` is its fake (test
  helper) and holds the terminal contract table (`contract.go`, run by `TestTerminalContract` and the live tier's
  H-C rows).
- `checks`: git and the merge gate: lanes (`checks.Lane`, `LaneSetup`), serve's worktree `<lanes_dir>/merge` (submit,
  gate: merge and compile, resolved, publish, after_merge over pins under `refs/obor/`), the lane test of a candidate in
  the task's lane worktree (`lanetest.go`, `Scratch.Test`: the class's checks), nightly/milestone checks in their own worktree,
  flakes, and the lane table (`LaneTable`: worktree facts for `obor status` and `app.Lanes`, merge evidence; `Lanes` is serve's
  `app.Lanes` adapter, each lane's configured type).
  Implements `port.Workflow` as `ScratchFF` (`workflow.go`, workflow.kind `scratch-ff`: `Scratch`, `Lane`, the
  target, the review git cmd/obor gives it). `Scratch` decides over git's answers; its git goes through `gitReader`/`gitWriter`
  (`gitio.go`: `realGit`, the one adapter over real git), so its decision tests run on `fakegit_test.go`'s fake; the
  workflow contract table (`testkit.WorkflowContract`, run on internal/app's `FakeWorkflow` too) and the real-git cases
  are behind `//go:build integration` (`workflow_integration_test.go`, `scratch_integration_test.go`, `gitlock_integration_test.go`).
- `launch`: start, stop (the kill sequence) and restart an agent CLI in its pane (`Launcher`, panes via
  `app.PaneControl`; implements `app.Agents` and `app.Starter`); the per-lane restart lock (`restartlock.go`:
  `RestartLock`); the restore after the terminal restarts (`Restorer`: serve's run for its project, after a
  lost connection or at its first connect after a herdr restart, under its `RestoreLock`); `session.go`: the id obor names a fresh Claude
  start and records before it runs, and a fresh Codex start's session from the transcript its process holds
  open (a Codex rollout), both reported to serve; `CLIVersions`: the first line of each kind's
  `<kind> --version`, for serve's version read.
- `mcp`: stdio shim from an agent to the app's socket; typed tools per role: the worker's `job_next`/`task_done`, the
  integrator's jobs and verdicts. The lead gets no tools: its chat work goes through the CLI, one request path.
- `guard`: the Claude Code hooks: PreToolUse (Bash parse, edit checks, the headless ban), Stop (the worker's
  task_done, and every agent's live background run), StopFailure, SessionStart, UserPromptSubmit, whoami.
- `setup`: make a project obor-ready (config, lanes, panes, roles, githooks, herdr/opencode plugin, MCP report; it
  starts agents through an `app.Starter` that `cmd/obor` injects, and asks the terminal for panes through
  `port.TerminalSetup`, whose adapter lays them out); `obor init`
  (`oborinit.go`: the steps chained with no questions; serve install through `Setup.Serve`, which `cmd/obor` wires
  to `service`).
- `service`: the project's launchd agent (darwin only, elsewhere each verb says to run `obor serve` yourself):
  the plist `~/Library/LaunchAgents/obor.serve.<name>.plist` and `launchctl` bootstrap / bootout / kickstart / print
  for `obor serve install|uninstall|restart` and `obor status`' serve line.

**view** — `view`: the project's read model. `view.Build(view.Input{Status, Err, Lanes, DB, Now, Events})`
joins serve's status answer (none when serve is down, with its error), the git lane table (`checks.LaneTable`) and the
store (read-only: specs, events) into one `view.Project`: serve, running and pending, the lanes (state column, spec
rev, open job), serve's tasks and agents, the role agents, needs-you, the full check, the pause, the lines, the queue
(merge loop with stages and the next by id, waiting dispatches: after | lane | never dispatched, the review request,
the outbox, pause, nightly) and the newest worded events (seq, ts, task, line). `view.Load` is that build for one
project at a root (serve's answer, the lane table, the store), as `obor web` builds every project the machine knows.
`background.go` is the agents' long-running background processes (`Input.Background`, by agent name;
`ScanBackground`: one `ps` per build, each agent pane's foreground leader from the caller's process info, its descendants
running > 30 s through its shells, helpers left out), labelled from the PreToolUse hook's records
(`project.AppendBackground` / `BackgroundCalls`, `.obor/state/background.jsonl`); the process table and the call's
matching are `internal/background`'s.
`view.Detail` is the drill-down the API serves and nothing else: a task's state, spec, report, gate line and log,
needs-you, records and jobs; an agent's sessions (a transcript path and last write, never contents), briefs, lines and
jobs; a job's text (a `prompts` row). The pages read narrow: `view.LoadTask`, `view.LoadAgent` and
`view.LoadSetup` build only their page's facts (a task or agent page runs no git and no ps; setup reads the lane
states). `EventsSince` pages the log by seq for the page's long poll. It may import base,
core and the adapters; only entry packages import it. **Every reader of project state goes through `view`**:
`obor status` (`--json` prints `view.Project` as is) and `obor web` render the same struct, so a field added for one
appears in both; the narrow page reads share its mapping helpers, and there is no second Project builder.

**test helpers** — `testkit` (fixture projects, the store helpers tests use, the workflow contract table) and `herdr/herdrtest` (the fake herdr
socket: scripted panes and plugins, and every request a test's client sent; the terminal contract table).

**entry** — `cmd/obor`: flags and wiring only; `obor status`' rendering of
`view.Project` (rendering only: no state is joined there); the
thin `app.Specs`, `Lanes`, `Poster` adapters and the review git (`reviewGit`, `port.Review`) live here; `workflowOf`
picks the workflow by workflow.kind (and `configFile`, `app.ConfigFile` on the main checkout's git); `terminal.go`
builds the project's terminal (`port.Terminal`, herdr its one kind), and no other production file imports
`internal/herdr` (`TestOneTerminalBuilder`). The lead's chat commands are the same CLI (`status`, `dispatch`,
`resume`, `drop`, `drive`, `land`, `spec`, `watch`, `pause`, `send`, `projects`, `brief`), beside `serve`, `mcp`,
`setup`, `config` and the rest (`run`, `nightly`, `flakes`, `restart`, `lead`, `plugin`,
`hook`, `whoami`).
- `web` (`internal/web`): `obor web`'s HTTP server over the read model — the API (`/api/projects`,
  `/api/view`, `/api/setup`, `/api/task`, `/api/agent`, `/api/job`, `/api/events` with a long poll on the store's seq,
  `/api/help`: `help.Commands` and the kit's README sections, read at the request)
  and the page: a Svelte app whose source is `web/` at the repo's root, built by `make web` into
  `internal/web/dist` (committed, `embed`; the Go build needs no Node, `TestDistFresh` fails on a stale build).
  Localhost only; its own process, never inside serve. Its two verbs, `POST /api/config` (a list of changes and
  the config revision they were made on, one commit) and `POST /api/pause`, are requests on the project's app
  socket (`wire`), taken only from the page's own origin: serve writes.
