# Module map

obor is one app per project (`obor serve`): a **core** that owns task state and reacts to events, and **adapters**
that each talk to one outside system. Agents never message each other; the app reacts.

**Addressing.** The core addresses everything as (machine, workspace, pane). A project is a herdr workspace, and obor
never touches panes in other workspaces. Only the `herdr` and `checks` adapters know how to reach a machine;
multi-machine itself is later, but no interface may assume local.

## Layers and import rules

The layers are base, bridge, core, adapter, view, entry, test helpers, tools (probes under `tests/`, the kit's own
programs under `tools/`: `affected`, the Go packages a change can break, run by the Makefile's `check-affected`)
and the kit's kinds (`kinds/<name>`: each kind's packages, which implement the kind protocol; they import the
bridge and their own tree only). The enforced table
(which package is in which layer, and who may import whom) is `layerOf` in `internal/checks/boundary_test.go`; the merge
gate runs it. Adapters never import each other: one that needs another goes through an interface the core defines
(e.g. `launch` gets panes via `app.PaneControl`, not `herdr`). A package with no layer fails the test: add it to
`layerOf`.

## Packages

**base** — shared leaves, no obor state of their own.
- `project`: find the project, main checkout, lane dirs, session role/agent. `project.Find(dir)`, `Env`; the running
  project a hook stands inside (`project.Running`, `project.InProject`: the nearest repository's main checkout holds an
  obor config and no stop marker, a nested repository its own); `ShellQuote`, `EnvLine` and `RunEnvLine` (`shell.go`: one word of a shell command line, the env line, and the one handshake that types it at a pane's shell and awaits its mark, for launch and the hc adapter); the file lock (`lock.go`: `Lock`),
  `LabelOurs` (whether a labelled pane is this project's), the sid files and the opencode plugin marker (`plugin.go`), the background Bash calls the PreToolUse hook records
  (`background.go`) and the Bash calls its parser rejects (`unparsed.go`), the caller's own pane (`pane.go`: `Pane`, the one resolver: `OBOR_PANE`, else the
  terminal's `HERDR_PANE_ID` for a pane obor did not start, else `ErrNoPane`; here, not in `herdr`, because adapters
  never import each other), the identity names a terminal server must not pass to its panes (`identity.go`:
  `ForeignIdentity`, read by hc and rex),
  the machine's list of obor projects by name (`projects.go`: `Register`, `Projects`, `RootOf`); a stopped
  project (`stopped.go`: `Stopped`, `WriteStopMarker`, `BeginStart`/`EndStart`, `Unregister`: obor stop / obor start);
  `sweep.go`: the nightly sweep of `.obor` (daily backups past the newest 7 dates, gzip'd or the older
  uncompressed ones, and only after a successful backup; files of lanes and agents the config does not
  names, the retired state names, the cache files of a kit kind the config answers in process, old drafts, the logs
  nothing needs (the names no code writes, and
  `launch.log`/`restart-<agent>.log` 14 days unwritten; a task's step logs are serve's), `flakes.jsonl`'s
  entries older than the 7 days `obor flakes` reads, and stop-failure entries).
- `config`: effective config. `config.Load(root) (*Config, error)` (every command's, serve's: an unknown key is ignored
  with a warning); `LoadReader` (obor status, obor stop/start, web: the warning returned);
  `LoadForValidate` (obor config validate, the one strict place: a FAIL line per unknown key); `Validate` (removed keys
  WARN), `TaskIDOK` /
  `TaskFromBranch` / `TaskBranch`; `edit.go`: `Edit`, the one setup change obor web may make, applied to config.json's bytes
  in their own layout, and `LoadWith` (what it would load as); `models.go`: `Config.Model`, a name resolved from
  `agents.models` alone, and `UpgradeModels`, the entries `obor setup agents` adds and an unknown-model refusal quotes.
- `background`: the agents' live processes, the one check of them — the machine's process table
  (`ReadProcs`, `ParseProcs`, `Proc`), a recorded background Bash call matched to the process it started
  (`Match` by command and time; `Live`, the newest call still running in an agent's descendants; `Mine`, an agent's
  calls; `Text`), an agent's descendants (`ProcIndex.Descendants`) and a hook's agent (`AgentOf`). The view's
  background scan and the Stop hook's block both read it.
- `roles`: role names and defaults. `spec`: a spec text's title, sections and globs (the text is the store's).
- `progstatus`: what obor makes of a program's own status as a terminal relays it (OSC 7501): the root record's
  four fields (`Report`: state, kind, msg, app) and the rules on them (`Word`, `Note`, `Shown`, `TurnEnded`). An
  adapter decodes its terminal's wire into a `Report`; no obor imports.
- `run`: commands under a timeout as a process group; machine-wide check slots. `run.Group`, `run.Slots`.
- `linestream`: a source of lines (a socket, a subprocess's stdout) as a channel of events: one goroutine drains the
  lines from the start, `Run` applies each with the caller's rules (`Apply`, `End`, an optional quiet timeout), the
  first error ends the stream once (`Err`, `Close`). The terminal adapters' event streams (`hc`, `rex`) run on it.
- `osnotify`: a desktop notification for a terminal that has none of its own (`Notifier.Show`: osascript on macOS,
  a reason elsewhere); `hc` and `rex` answer `port.Panes.Notify` with it.
- `version`: build version line.
- `agents/plugin`: a plugin kind, an `agents.Kind` whose CLI-specific part is an executable answering the kind
  protocol (`docs/agents/kind-protocol.md`), run as a process group under a per-call timeout. `config` builds the
  kinds its `agents.kinds.<name>.plugin` name through `agents.Plugins` (it cannot import this package) on every load,
  each with the manifest obor serve, `obor init` or `obor setup agents` cached (`plugin.Ensure`, `plugin.Cache`,
  `.obor/state/kinds/<name>.json`): no process runs at load; a kit kind answered in process has no cache file. The
  loaded config holds them (`cfg.Kinds()`, `cfg.Kind(name)`); a load changes no package state (boundary test).
  Base, not bridge: it runs processes and writes the cache. `Local`: the commands answered in process by a Go
  handler instead of a process, by the command a config names (the kit's kinds under their binary's name, which
  `cmd/obor`'s `kinds.go` registers; testkit's in a test binary); any other command runs.
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
transcript begins with: Codex's `session_meta`), `Context(tail)` (a session's context size from its own transcript
format; `project.ContextOf` reads a file's tail and gives it to the session's recorded kind alone), `Deliver(text, record) (Delivery,
bool)` (typed
vs inbox post), `Caps()` (hooks transport — the plugin marker is `Caps().Hooks == HooksPlugin` —, inbox, resume,
transcript, a named session: `NamedSession`, obor chooses the id of a fresh start; `Status`, where the status and
turns come from: `terminal`, `hooks`, `transcript`; `App`, the name its agents report in a terminal's status). A kind starts as `agents.Split` (a compiled kind:
`Start(Start) []string`, the fresh/resume argv, MCP registration and first prompt, `Start.Read` the adapter's file read;
`StartEnv`, a start's own env) or `agents.Whole` (a plugin kind's one start call: its argv, env and files together;
a plugin kind has no `Start`/`StartEnv`). Registry: the compiled kinds alone, each registered from its init; a
project's kinds are an `agents.Kinds` its config builds (the compiled ones and its plugin kinds; OpenCode and agy the
kit's, `kinds/opencode`, `kinds/agy`, answered in process), read as `cfg.Kind(name)` / `cfg.Kinds()`; a start is
built once at the launch boundary from the complete `agents.Start` (`agents.StartOf`), and a plugin kind's start and
install can fail (`StartOf`, `InstallOf`). `Install` and `Transcript`
take the environment (config dirs: `XDG_CONFIG_HOME`, `CLAUDE_CONFIG_DIR`, `CODEX_HOME`). A project's kinds:
`Kinds.Lookup`, `Names`, `IsPlugin`, `Vars`/`StartVars`; `Default`. All I/O stays in the adapters, which ask the kind what differs; non-lead default settings stay in
`config.Model.Launch`, which asks the kind to render them. No kind name outside the bridges and the kit's kinds (boundary test).
Adding a kind = a plugin kind's executable (`docs/agents/kind-protocol.md`) + `agents.kinds.<name>.plugin`.

**kind packages** — `kinds/opencode`: the OpenCode kind as the reference plugin kind, linked into obor (its
`kind.Handle` in `plugin.Local` under the name `obor-kind-opencode`, its `kind.Main` as `obor kind opencode`:
`cmd/obor/kinds.go`); obor builds, ships and fetches no kind binary. Package `kinds/opencode/kind` is
the kind: the manifest, `start` (the args without `--model`, `--prompt`; `OBOR_OPENCODE_MODEL` model#effort and
`OPENCODE_CONFIG`, the generated config with obor's MCP server and plugin `<kit>/opencode`), `turn` (a `rate_limit`'s
reset; no turn, session or context read), `project-warnings`. It imports `agents` only (`Pin`, `ResetIn`); testkit
registers its handler in a test binary. `kinds/agy` (`obor-kind-agy`), linked the same way: package
`kinds/agy/kind` is the manifest, `start` (`--settings` with the start's settings file, `settings.go`: the user's saved
agy settings, the project's `settings_override.json` and obor's entries; the model and `--effort`; `-i` or
`--conversation`), `install` (`where: project`, `.agents/mcp_config.json` and `.agents/hooks.json`), `turn` (`context`
from `transcript.jsonl`, a limit's reset), `deliver` (`deliver.go`: agy's `agentapi send-message` with the record an
agent's `obor` command wrote), and the hook transport (`hook.go`: `RunHook`, which `obor kind agy hook` runs with
obor's hook in process; agy's event → `obor hook <name>` → agy's answer).

**core** — `app`: the only place with obor logic. One concern per file (a test, `TestModuleMapAppFiles`, fails
on a file missing from this table or a listed file that is gone); the entry is the function to read first:

| file | concern | entry function |
|---|---|---|
| `activity.go` | the agents' own activity, the watchdogs' evidence: transcripts' last write and last turn, child processes under a pane's agent | `agentFiles.Activity` |
| `app.go` | types, options | `Ready` |
| `block.go` | the merge loop's needs-you as a block on a task's phase | `mtask.block` |
| `cfg.go` | where the core reads the project's config: `Loop.Cfg` for a pass and the verbs that take a Loop, `proj.cfgNow` at the other entries | `proj.cfgNow` |
| `configset.go` | the owner's setup edit from obor web, through the `ConfigFile` port | `App.configCall` |
| `cutwait.go` | a wait that is cut (uncounted waits, `cutWaits`, `cutEnd`) | `App.uncounted` |
| `disk.go` | low disk: free space below diskLow is a needs-you that holds the gate and the nightly | `App.diskTick` |
| `dispatch.go` | `obor dispatch`: the lane and task a dispatch takes | `App.dispatchCall` |
| `drive.go` | the owner's drive and land: `obor drive`, `obor land`, the driving state | `App.driveCall` |
| `effects.go` | `JobID` | `JobID` |
| `exit.go` | the socket's exit request: serve ends gracefully, `obor stop` | `App.exitCall` |
| `feed.go` | the socket's `events` request: the event feed after a seq, streamed with `--follow` | `App.feed` |
| `fetch.go` | jobs by fetch: the wake, the first fetch's delivered fact, the job a call names | `App.jobNext` |
| `finish.go` | `awaitsTaskDone` (whose attempt is its worker's and no task_done yet), `inLoop` (what the merge loop holds) | `mtask.awaitsTaskDone` |
| `fold.go` | a start's result into the identity table: agent, pane, kind, session, model | `proj.foldStart` |
| `follow.go` | serve follows a new obor binary and takes the self-restart path, preferring an idle moment | `App.followTicks` |
| `gitprune.go` | `obor prune`: branches and worktrees of ended tasks | `App.pruneCall` |
| `idle.go` | the idle rules and `obor idle` | `proj.foldIdle` |
| `jobs.go` | the watch on an open job and its timer | `App.watchJob` |
| `jobstart.go` | job start, texts | `App.job` |
| `keeplane.go` | a keep lane's agent between its tasks: its live agent takes the next job, the handoff rule stops one past handoff_tokens | `App.keepLive` |
| `lanepick.go` | the lane a dispatch takes and the replies that say why none was free | `App.held` |
| `lanerows.go` | the `lanes` table projection | `proj.foldLanes` |
| `lanesync.go` | the target merged into a sent-back attempt's lane before its fix job (`lane.sync`, row 24a), and the fix job's line on it | `App.laneSync` |
| `lanescan.go` | the lane table as a cached observation: scanned on the tick and by a refresher, never per pane event | `App.scan` |
| `leadlines.go` | a lead whose kind has no inbox takes its lines through its hooks (`lead_lines` call), recorded as a post's delivery | `App.leadLinesCall` |
| `log.go` | serve's side of the backup | `App.backup` |
| `logs.go` | a task's step logs go when it ends, and at load for every task the replayed log folds as ended | `App.endTaskLogs` |
| `loop.go` | the merge loop: wake, advance, record, do, drive, the picks | `App.wake` |
| `mergewatch.go` | the merge watch: what a set of tasks' merge is waiting on | `proj.foldWatch` |
| `messages.go` | the lines' delivery over `app/outbox`'s state: `deliver`, `route`, `recordBrief`, `foldMsg` | `proj.foldMsg` |
| `move.go` | `obor dispatch --model` on a working task: a redo dispatch on the same lane and worktree | `App.moveTask` |
| `mtask.go` | the merge loop's state fold | `proj.foldLoop` |
| `nightly.go` | the nightly run | `App.nightly` |
| `nightlygreen.go` | the lead's line for a GREEN full check's new or changed non-gating failure | `nightlyGreenRule` |
| `openwork.go` | whether a role agent has work open, the restore's question before it restarts the agent's pane | `App.OpenWork` |
| `panes.go` | herdr events to projects | `App.connect` |
| `pause.go` | the pause of a model or of all work, the rate-limit evidence | `proj.foldPause` |
| `preview.go` | `obor merge preview`: whether a lane's work merges cleanly now | `App.mergePreview` |
| `project.go` | load, log, fold | `App.load` |
| `prompt.go` | the send of a prompt to an agent and its delivery | `App.send` |
| `prompts.go` | the `prompts` table's writes and prune | `App.prompt` |
| `prune.go` | the nightly prune of ended tasks' records and old sessions, 30 days on (`mtask.ended` is the end both it and the logs' removal read) | `App.pruneLog` |
| `registry.go` | agent identity: `reconcile` collects the proofs the `app/identity` table accepts; `whereIs` and the table's `WhoIs` the only lookups, never herdr's name | `App.whereIs` |
| `restore.go` | the restore of the project's panes after a lost terminal connection came back, through the `Restorer` port, and at serve's first connect when herdr restarted while serve was down | `App.dropOtherKind` |
| `resume.go` | `obor resume` | `App.resume` |
| `review.go` | a review request: `obor review start` records it and the merge loop fires the reviewer's start | `proj.foldReview` |
| `reviewclose.go` | a review's close is a call to serve, which commits it and records `review.closed` | `App.reviewCloseCall` |
| `reviewonmerge.go` | the Review: on merge rule: a hard or risky task's spec asks for a review at its merge | `reviewOnMergeRule` |
| `reviewverify.go` | a review's fix round and its verify, briefed in the reviewer's session | `verifyRule` |
| `roledone.go` | a role agent that is done: the done reminder's rule (`role.done`), its line and the stop hint | `roleDoneRule` |
| `rolestop.go` | the role stop rules: serve stops the integrator and the reviewer once done, and a fresh brief's stop | `integratorStopRule` |
| `roleworktree.go` | role worktrees: serve moves the reviewer's and the deep dive's to the head it hands them | `App.briefMove` |
| `rules.go` | the rule pass: rule-tier steps | `App.rulePass` |
| `scheduler.go` | the scheduler's rule for serve's worktree: pass order, who holds the scratch, what a step waits | `proj.passOrder` |
| `serve.go` | New, Serve, run, the serve flock | `Serve` |
| `sessions.go` | the agents' recorded sessions | `App.sessionCall` |
| `settle.go` | settling a task's open records when it ends | `App.settleEnded` |
| `specs.go` | the specs table: `obor spec set` and the readers | `App.specSet` |
| `states.go` | the state table | `init` |
| `status.go` | the status snapshot and `Listen`: the socket's server half | `App.Status` |
| `steps.go` | scratch/after steps, submit, gate, publish, unpin, fix | `App.scratchStep` |
| `stopped.go` | the agents' stop report (`stopped` call): a worker's and a role's stop | `App.stoppedCall` |
| `stopstep.go` | a task worker's stop as a step of the merge loop, for a move or a drop | `mtask.foldStop` |
| `stopwake.go` | the wake of a stopped agent at its rate limit's reset | `App.stopWakes` |
| `toast.go` | the owner's herdr toast, a rule action's best-effort after | `App.toast` |
| `trust.go` | a pane's trust prompt: the needs-you it raises and its answer | `trustHeld` |
| `verbs.go` | `leadOr`: the admission of a lead's and the owner's method, a custom role's by its `roles.<name>.verbs` | `leadOr` |
| `verdict.go` | a job's verdict call and its fold | `App.verdict` |
| `versions.go` | the versions of the tools obor drives: the periodic read, fact `version`, the rule that lines the lead on a change | `App.versionTick` |
| `waiting.go` | a dispatch that waits for the tasks it names (`--after`) or for a lane: fold, pick, dispatch | `App.waitTask` |
| `watchdog.go` | the watchdogs over panes and lanes | `App.check` |
| `workerstart.go` | the worker start rule: a lane's worker started again while its job is outstanding | `workerStartRule` |
| `worktext.go` | the work job's wake: its text, the send to the worker and the watch on its delivery | `App.sendWork` |

- `app/port` (core, a leaf below `app`): the ports and the types they exchange: the terminal's `Pane`
  (terminal-neutral: its `Group`, no tab or workspace), `Snapshot`, `Event`, `Stream`, `Panes`; `Lane`, `Lanes`;
  `TerminalSetup` (setup's terminal: `Agents`, `Place` a `Placement`, `Integrate`, `RenameProject`); and the
  effects (`Launcher`, the one way an agent is started, `Prompter`, `PaneControl`,
  `Scratch`, `LaneSetup`, `Review` and their inputs and outcomes, and `Workflow`, the git workflow behind one
  interface: those three and the dispatch's `Target`), with the one rule of the port's own: `GuardPrompts` in front of
  every terminal's `Prompt` (`cmd/obor` builds each kind behind it; `Blocked` reads its refusal). Adapters import it
  instead of `app`.
- `app/layout` (core, a leaf below `app`): the one placement decision of every terminal adapter, pure: `Place` (an
  `Ask`: the new pane's kind, `Group`/`In`, the group's `Tab`s with their `Pane`s' kinds → an `Answer`: which tab, a new
  tab of which name, or the refusal's text), `Label` (a new tab's name for a kind) and `Home` (whether the caller's pane
  qualifies as the lead's home, from a `Caller`'s facts, else why not). No terminal call: each adapter reads its facts
  into these types and does what the answer says. It imports `app/port` only (`PaneKind`).
- `app/shellpane` (core, a leaf below `app`): the shell pane model of the terminals that type an agent's command line
  at a pane's login shell (`hc`, `rex`): `Start` (port.AgentStarter's start: the quoted command line (`CommandLine`)
  typed after the pane is marked, then up by the status rule, with its refusals and failures as the port's
  predicates) over a `Terminal` each adapter gives (`Look`: a pane's facts; `Mark`; `Type`), and `WaitStatus` (a wait
  for an agent's status on the terminal's own `port.Panes` subscription, a refresh subscribed again). It imports
  `app/port`, `progstatus` and `project` only.
- `app/event` (core, a leaf below `app`): the event log's vocabulary and its store: `Record`, `Key`, `Intent`,
  `FactData`, the `Kind*`/`Class*` constants, every exported `Step*` and the task keys (`MsgTask` …), the data type of
  every record kind serve writes (`Pause`, `Message`, `MergedData`, `Submit`, `Stop`, `GateIn`, `GateOut`,
  `TestIn`, `TestOut`, `Note`, `Dispatch`, `PrepareOut`, `Start`, `StartOut`, `Job`, `Verdict`, `VerdictResult`,
  `Version`, `SpecSet`, `ConfigSet`, `Pruned`), `Log` (`OpenLog`, `Append`, `Do` …), the readers (`ReadLog`,
  `Dump`, `RecentEvents`, `RecordsOf`, `MessagesTo`, `Since`, `Facts`, `FactTasks`, `DBFile`: the view's queries) and `Merge`/`ReadMerges`, and `StepN`. It imports `store` and `app/port` (a dispatch's, a gate's and a start's data carry the port's types) only.
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
  `Prev`, `Closing`, `Failed`, `MergeOf`, `BriefFile`; `Round` is the fix round (its links and their
  states), `ID`/`Number` a review's id. Its fields are unexported, so only it names them. No lock: under `App.mu`. It imports `app/event` and
  `app/outbox` (a stop's data) only, never `app`; the effects (the start and its brief, the verify, the close's commit
  and crash read, the reviewer's stop, the on-merge request, dispatch's `Fixes:` check) stay in `app`'s review files.
- `app/wire` (core, a leaf above `app/event`): the app socket's client half, what a caller can ask serve: every
  `Method*`, `Call`, `Request`/`Answer`, `Ask`, `Verdict` (with its retries and `OBOR_CALL_DEADLINE`), `SocketPath`,
  `NewCallID`, `JobReply`, `ResumeArgs` and `Resume*`, the status types (`Status`, `ProjectStatus`, `TaskStatus` …,
  `Scratch*`) and the `Backup` client. The event feed (`feed.go`): `FeedRecord` (a record and its task's spec `Ref`), `FeedSince` (the
  one read of it: serve's and `obor events`' while serve is down) and the `Feed` client. It imports `app/event`, `store` and `project`, never `app`; `mcp`, `launch`,
  `checks`, `view` and `web` import it instead of `app`. `Listen` and the handlers stay in `app`.
- `app/describe` (core, a leaf below `app`): a record as the readers word it, decoded with the types serve writes it
  with: `Describe` (`Described`, the event list's line and the facts the pages read), the event kinds
  (`Kind*`: the web's icons and filters), `NeedsText` (a needs-you fact's text) and `Age` (an age as obor status
  writes it). It imports `app/event`, `app/review` and `app/wire` only, never `app`: the view, the web and `obor
  events` read records here, so a change in the core does not select them.
- Task state and the per-project event log (the `events` table of `.obor/state/obor.db`; intent before an effect,
  result after).
- Scheduler rules: readiness (`--after`), dispatch, accept/reject/sendback, moved target, the integrator slot (no cap
  yet).
- Watchdogs: idle with a task, working without activity, blocked, pane exited.
- Interfaces it drives adapters through: `Panes` (subscribe, snapshot, notify), `Lanes` (a project's lanes), and,
  on its `Loop`, `Scratch` (submit/gate/resolved/publish/after_merge/landed/unpin), `Agents` (launch/stop), `Prompter`
  (a job's wake, a line typed to the agent in a pane), `Poster` (posted to a session's inbox), `Specs`, `Nightly` and `Restart` (serve's own restart after a code merge).
  API: `app.New(Options, Panes, Lanes)`, `(*App).Serve(ctx)`. Its rig tests (`*_ext_test.go`) run in a
  `testing/synctest` bubble on in-memory connections (`bubble`, `rig_bubble_ext_test.go`), and the ones that need a unix
  socket are `*_integration_test.go` files.

**adapters** — one outside system each.
- `herdr`: herdr socket client (events.subscribe, session.snapshot, agent.prompt, pane.*). Implements `port.Panes`,
  `port.PaneControl` (a pane read by its id, the two ways to type: `Write` into a pane, `Prompt` to its agent) with
  `port.Prompter`, `port.AgentStarter` (`start.go`: herdr's agent start, its name-taken retry and the evidence rule on
  `launch_pending`), and `port.TerminalSetup` as `Setup` (`setup.go`: setup's placements carried out in tabs,
  workspaces and splits (`Place` reads herdr's tabs into `app/layout`'s types and does what its answer says), herdr's `resume_agents_on_restore` hint, the workspace rename); herdr's own `Pane` (tab and workspace ids, the name herdr runs an agent under) stays
  here; `cmd/obor`'s `terminal` builds the project's `port.Terminal` over these; `herdr/herdrtest` is its fake (test
  helper).
- `hc`: the herdr-core (`hc`) socket client, the second terminal adapter (`docs/plan/herdr-core.md`; a project selects
  it with `project.terminal: "hc"`, experimental): `hello` and the version rule (`client.go`), the core's environment
  (`env.go`: `CheckEnv` refuses a core holding another terminal's identity (`project.ForeignIdentity`), `Check` for
  `obor setup check`, `Core` for serve's subscribe: a core that is down started from a scrubbed environment), a terminal record as a pane (labels `obor.project`/`obor.agent`
  as its group and label, the kind from its status's app, else the `obor.kind` label) and `port.Panes` (`panes.go`),
  one unfiltered `events.subscribe` and its endings (`stream.go`), `port.PaneControl`: its reads (`client.go`) and
  `control.go` (`Prompt`: `terminal.send`, whose native `blocked` answer is `port.Blocked`; `Write`, `SendKeys`,
  `RenamePane`, `Named` by label, `WaitStatus`: `app/shellpane`'s on its own subscription), `port.AgentStarter`
  (`start.go`: `app/shellpane`'s start over the terminal record), `setup.go` (`RenameProject`, `ResumeHint`; `Place` is not built
  yet), and `contract.go` (what obor sends and reads, checked against a committed copy of hc's schema); `hc/hctest` is
  its fake (test helper).
- `rex`: the Rex client, the third terminal adapter (`docs/plan/terminal-adapters.md` step 9; `project.terminal`
  "rex"): the `rex` CLI on PATH, one `rex api call` per request and its errors (`client.go`: exit status 4 is no server,
  Rex's `code=` its answer; no `rex` on PATH the refusal naming the app bundle's directory, `Check` for `obor setup
  check`), a terminal block as a pane (its session's label the group, its label the label, the root
  `program_status` record as a `progstatus.Report` only while its `owner.pid` is the foreground) and `port.Panes`
  (`panes.go`; `Notify` by `osnotify`), one `rex events --all --json` with the client lines dropped, synced before
  the snapshot, its block events applied and its exit a refresh (`stream.go`), `port.PaneControl` and
  `port.AgentStarter` (`control.go`: a block found by its id (`target.resolve`), a paste and `rex send-key` for the
  typing behind `port.GuardPrompts`, `WaitStatus` and `Start` by `app/shellpane`), `port.TerminalSetup` (`place.go`:
  one session per project labelled with its name, its windows `app/layout`'s tabs, every window and block unfocused;
  `setup.go`: the agents, the rename, the close, the lead's home), a block's environment (`env.go`: every block obor
  makes runs its shell through `env -u` for each pane or session identity name the server's environment holds, read by
  a probe block), and `contract.go` (the methods, params, options and fields obor uses, checked against a committed
  copy of `rex api describe`; the stream's event names, which no schema has; the CLI commands; the stream's delivery
  guarantee). Only `cmd/obor/terminal.go` and `tests/conformance` import it; no fake Rex server (mapping functions are
  tested on recorded JSON, the live tier reads a scratch server).
- `checks`: git and the merge gate: lanes (`checks.Lane`, `LaneSetup`), serve's worktree `<lanes_dir>/merge` (submit,
  gate: merge and compile, resolved, publish, after_merge over pins under `refs/obor/`), the lane test of a candidate in
  the task's lane worktree (`lanetest.go`, `Scratch.Test`: the class's checks), nightly/milestone checks in their own worktree,
  flakes, and the lane table (`LaneTable`: worktree facts for `obor status` and `app.Lanes`, merge evidence; `Lanes` is serve's
  `app.Lanes` adapter, each lane's configured type), and the docs page's git (`docs.go`: `TrackedDocs`, `DocsHead`,
  `IndexFile`, `DocsIndex`, `GrepDocs`: a search's lines over the docs' files in the checkout).
  Implements `port.Workflow` as `ScratchFF` (`workflow.go`, workflow.kind `scratch-ff`: `Scratch`, `Lane`, the
  target, the review git cmd/obor gives it). `Scratch` decides over git's answers; its git goes through `gitReader`/`gitWriter`
  (`gitio.go`: `realGit`, the one adapter over real git), so its decision tests run on `fakegit_test.go`'s fake; the
  workflow contract table (`testkit.WorkflowContract`, run on internal/app's `FakeWorkflow` too) and the real-git cases
  are behind `//go:build integration` (`workflow_integration_test.go`, `scratch_integration_test.go`, `gitlock_integration_test.go`).
- `launch`: start, stop (the kill sequence) and restart an agent CLI in its pane (`Launcher`, panes via
  `app.PaneControl`, the agent's start itself the terminal's `port.AgentStarter`; implements `app.Agents` and `app.Starter`); the per-lane restart lock (`restartlock.go`:
  `RestartLock`); `locate.go`: `Locate`, the one decision before a start or stop touches a pane (serve's `where`
  answer, the pane list, the terminal's holder of the name), under that lock; the restore after the terminal restarts (`Restorer`: serve's run for its project, after a
  lost connection or at its first connect after a herdr restart, under its `RestoreLock`); `session.go`: the id obor names a fresh Claude
  start and records before it runs, and a fresh Codex start's session from the transcript its process holds
  open (a Codex rollout), both reported to serve; `CLIVersions`: the first line of each kind's
  `<kind> --version`, for serve's version read.
- `herdrapp`: the herdr-app window's CLI client (`herdr-app ls`, `do <command> '<json>'`, `commands`, `--version`), the
  surface that shows hc's terminals: `Spaces` (each space's id, name, folder, named flag and machine, its tabs) and the
  local layout it needs — `EnsureSpace` (a project's space by name and folder: `space.new`, or an unnamed space at that
  folder renamed), `NewTab` (`tab.new`), `RenameTab` and `Labels` (`herdr.space`/`herdr.tab`
  for a terminal to show in a tab); a typed no-window error (`port.NotRunning`: place by labels later, never start the
  app), the version rule (≥ 0.1.16) and `contract.go` (the commands and arguments obor sends, checked against a
  committed copy of `herdr-app commands`; `tests/conformance` compares the copy with the installed app). Only
  `internal/hc` imports it, for the hc layout; `herdrapptest` is its fake (test helper).
- `mcp`: stdio shim from an agent to the app's socket; typed tools per role: the worker's `job_next`/`task_done`, the
  integrator's jobs and verdicts. The lead gets no tools: its chat work goes through the CLI, one request path.
- `guard`: the Claude Code hooks: PreToolUse (Bash parse, edit checks, the headless ban, the `hc`/`herdr-app` terminal writes and every `rex` command but its reads outside the lead), Stop (the worker's
  task_done, and every agent's live background run), StopFailure, SessionStart, UserPromptSubmit, whoami.
- `setup`: make a project obor-ready (config, lanes, panes, roles, githooks, herdr/opencode plugin, MCP report; it
  starts agents through the launcher's one start (`port.Launcher.Launch`, given setup's placement and refusing rather
  than stopping a running agent; a pane with no `--start` is the same launch with its `NoAgent` option: label the pane and
  type its env, start no agent) that `cmd/obor` injects as a `port.Launcher`, and asks the terminal for panes through
  `port.TerminalSetup`, whose adapter lays them out; `obor setup check` runs the terminal's own check, `check.go`;
  `models.go`: the starter models and levels init writes, and setup agents' `agents.models` upgrade step); `obor init`
  (`oborinit.go`: the steps chained with no questions; serve install through `Setup.Serve`, which `cmd/obor` wires
  to `service`); `obor uninstall` (`uninstall.go`: those steps undone in reverse; serve, the agents and the main
  repo's git through `Setup.Down`, which `cmd/obor/uninstall.go` wires to the stop path, `service` and `checks.GitPruner`;
  the terminal closes the project's panes through `port.TerminalSetup.CloseProject`).
- `service`: the launchd agents (darwin only, elsewhere each verb says to run `obor serve` yourself): each project's
  `~/Library/LaunchAgents/obor.serve.<name>.plist` and the machine's `obor.web.plist` (the page), with `launchctl`
  bootstrap / bootout / kickstart / print for `obor serve install|uninstall|restart` and `obor status`' serve and web
  lines.

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
needs-you, records and jobs; every task the store knows (`TaskList`, the tasks page: one read each of the specs, the
dispatch, merge, drop and review facts and the log's activity per task); an agent's sessions (a transcript path and last write, never contents), briefs, lines and
jobs; a job's text (a `prompts` row). The pages read narrow: `view.LoadTask`, `view.LoadAgent` and
`view.LoadSetup` build only their page's facts (a task or agent page runs no git and no ps; setup reads the lane
states). `EventsSince` pages the log by seq for the page's long poll. `docs.go` is the docs page's list (`DocCache`: the
tracked `.md` files `checks.TrackedDocs` reads, each with its first level-1 heading (`DocTitle`), pinned and grouped by
`BuildDocs`, read again when HEAD, git's index or one of those files moves) and `ReadDoc`, the one read of a doc's text
(only a path git's index tracks at the read, as a regular file, never out of the root); `docsearch.go` is `Search`, the
docs search: the listed docs' lines holding every word of a query (`checks.GrepDocs`), each text cut around its first
hit, at most `MaxDocHits`. It may import base, core and the adapters; only entry packages import it. **Every reader of
project state goes through `view`**:
`obor status` (`--json` prints `view.Project` as is) and `obor web` render the same struct, so a field added for one
appears in both; the narrow page reads share its mapping helpers, and there is no second Project builder.

**test helpers** — `testkit` (fixture projects, the store helpers tests use, the workflow and terminal contract tables) and
`herdr/herdrtest` (the fake herdr socket: scripted panes and plugins, and every request a test's client sent) and
`hc/hctest` (an in-memory hc core: terminal records, the methods the hc client calls, events with seq, heartbeat and
missed, and a shell that runs the programs a test names, with each send and key it got).

**entry** — `cmd/obor`: flags and wiring only; `obor status`' rendering of
`view.Project` (rendering only: no state is joined there); the
thin `app.Specs`, `Lanes`, `Poster` adapters and the review git (`reviewGit`, `port.Review`) live here; `workflowOf`
picks the workflow by workflow.kind (and `configFile`, `app.ConfigFile` on the main checkout's git); `terminal.go`
builds the project's terminal (`port.Terminal`: herdr, or hc with serve's hold on its core), and no other production file imports
`internal/herdr`, `internal/hc` or `internal/rex` (`TestOneTerminalBuilder`). The lead's chat commands are the same CLI (`status`, `dispatch`,
`resume`, `drop`, `drive`, `land`, `spec`, `watch`, `pause`, `send`, `projects`, `brief`), beside `serve`, `mcp`,
`setup`, `config` and the rest (`run`, `nightly`, `flakes`, `restart`, `lead`, `plugin`,
`hook`, `whoami`).
- `web` (`internal/web`): `obor web`'s HTTP server over the read model — the API (`/api/projects`,
  `/api/view`, `/api/setup`, `/api/task`, `/api/tasks`, `/api/agent`, `/api/job`, `/api/cursor` (the store's newest seq, a page's starting cursor), `/api/events` with a long poll on the store's seq,
  `/api/help`: `help.Commands` and the kit's README sections, read at the request; `/api/docs` and `/api/doc`: the
  markdown files git's index tracks in the main checkout, `view.DocCache` per HEAD, index and each doc's file stat, a
  path outside that list or one the index no longer tracks refused; `/api/docsearch`: those files searched for the lines
  holding every word of `q` (`view.DocCache.Search`, at most `view.MaxDocHits`))
  and the page: a Svelte app whose source is `web/` at the repo's root, built by `make web` into
  `internal/web/dist` (committed, `embed`; the Go build needs no Node, `TestDistFresh` fails on a stale build).
  Localhost only; its own process, never inside serve. Its two verbs, `POST /api/config` (a list of changes and
  the config revision they were made on, one commit) and `POST /api/pause`, are requests on the project's app
  socket (`wire`), taken only from the page's own origin: serve writes.
