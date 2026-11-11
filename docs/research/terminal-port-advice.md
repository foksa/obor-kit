# Terminal port: herdr, rex, or both (advisor, 2026-11-11)

Question (lead, for the owner): plan the terminal architecture so obor can run on herdr, on rex, or on both.
Investigation and plan only. The step plan is `docs/plan/terminal-adapters.md`.

**What was read.** Rex's docs (server API, CLI, Lua, events, program status, agent skill, remote access; 13 pages),
the Rex Beta bundle (0.1.0, build 1925: `Info.plist`, the `rex` binary's `--help` for 20 commands, and the API field
descriptions embedded in the binary), `hc --help` (0.1.13), the four research notes the lead named, `internal/app/port`,
`internal/herdr`, `internal/launch`, `internal/setup/agent.go`, `cmd/obor`, `docs/module-map.md`,
`docs/plan/architecture-target.md` §2b. **What was not done.** No Rex session was started or driven, nothing was
installed, no API call was sent to the running Rex server. Every Rex claim below is from its docs, its help text or
its binary's strings; the ones that need a live check are listed under "Not verified" and as probes R1-R13 in the plan.

Words used: a Rex **block** is Rex's pane (one program in a window); a Rex **window** is a tab; a Rex **session** is
a named group of windows. **OSC 7501** (the Program Status Protocol) is an escape sequence a program writes to its
own terminal to say `idle`, `working`, `blocked`, `done` or `error`; Rex and herdr-core read status only from it.

## Recommendation

1. **"Both" means per project: one serve talks to one terminal, chosen by `project.terminal`** (the key exists,
   `internal/config/config.go:66`). Two projects on one machine may use different terminals. Not per role, and not
   one serve on two terminals: if that is ever wanted it is a third adapter that wraps two (§3), never a core feature.
2. **The port is not ready for a second adapter, and the gap is not where the earlier notes looked.** The names are
   mostly clean (`HERDR_PANE_ID` has one reader, no pane id is parsed, tabs and workspaces are out of `port.Pane`).
   What is left is that **`internal/launch` runs herdr's start protocol through the port**: it compares herdr's error
   codes, waits on herdr's `launch_pending`, clears herdr's agent names and types an env line because herdr's start
   takes no env (§2 B). A rex adapter written today would have to imitate herdr to satisfy launch.
3. **Raise the port from herdr's verbs to obor's intents** (§3): observe panes, place a pane, start an agent in a pane
   (argv, env, cwd, label, in one call), type into it, send keys, look at it, label it, notify. How env reaches the
   agent, what "the agent is up" means, agent names, error codes, tabs, windows and sessions stay inside the adapter.
4. **Do the cleanup in five small steps that pay off on herdr alone, then one hard step, then Rex** (plan). The hard
   step (the start behind the port) is only worth its risk once a second terminal is decided; the rule "a method added
   to a port names the implementation that needs it" (`improve-architecture.md` A2) holds it back until then.
5. **Rex fits, with one real gap: status.** Rex gives an instance id, an ordered event stream with a snapshot, tabs
   and splits obor can place, process and cwd events, and a server that outlives its app. It detects no agent and has
   no "refuse typing while blocked": status is whatever the program reports by OSC 7501. Claude Code reports it
   (per `herdr-core.md` §3); Codex and OpenCode are not known to. So a rex project needs obor to own status for
   those kinds first (owner decision D3).
6. **Before any rex code, one live probe run on a throwaway Rex server** (plan step 7). Rex is a closed beta at 0.1.0
   and five facts that shape the adapter are undocumented.
7. On the needs list alone herdr-core scores higher than Rex (§1). Rex has what herdr-core lacks: a window the owner
   watches, with tabs and splits obor can lay out. The port in §3 serves both, so that choice can wait for the probes.

## 1. Rex and herdr-core against the needs list

The list is `improve-architecture.md` A2 "Idea, terminal" (lines 91-102).

### Rex Beta 0.1.0

| # | Need | Rex | A rex adapter does without |
|---|---|---|---|
| 1 | An instance identity | **Yes.** `server.status` returns `instance_id` ("A random ID for this run of the server. A new value means a new server process, even if pid is the same"), `started_at` and `pid` | the socket-file mtime (`internal/herdr/panes.go:110-119`) |
| 2 | An event when a pane is relabelled | **Probably, not verified.** No `block_label_changed` exists (windows and sessions have one). `session.set_block_label` returns a `revision`, and `session_view_changed {revision}` is sent when the view changes | the label merge on every reread (`internal/herdr/socket.go:1005-1028`), if probe R2 holds |
| 3 | An environment on an agent start | **By argv, not by a field.** A terminal block's options are `command`, `shell`, `cwd`, `interactive`, `initial_input`, `exit`, `path_prepend`: no `env`. But the agent can be the block's own `command`, so `env NAME=VALUE … claude …` carries the env and nothing is typed | the typed env line, its two marks and its output wait (`internal/launch/launch.go:742-768`), the shell wait (`internal/launch/stop.go:411-431`) |
| 4 | A blocked state that typing respects | **No.** `write` and the `paste` event type whatever the program's state. `blocked` (with `kind`: permission, question, auth) exists only as a status the program reported. The adapter can read `program_status` and then paste: a check-then-act gap, and only for programs that report | nothing; it adds a check |
| 5 | Stable optional fields | **Yes, by policy.** Each enum's description says how to treat an unknown value; the connection exchanges `api_revision` and `api_min_revision`; `rex api describe --json` prints each method's schema from the running server | the hand-kept list checked against a schema dump (`internal/herdr/contract.go`) has a native counterpart |
| 6 | A plugin or hook registry that is not user-global | **Not needed.** obor needs no plugin in Rex. A probe gets its own server: `rex server run --listen unix://<dir>/s.sock --state-dir <dir> --config <file> --restore=false` | the "never link a real plugin from a test" rule (`AGENTS.md`) has no rex counterpart |
| 7 | Idle cost | **Unknown.** The Rex server running here used 30 s of CPU in 71 h, with no obor load. herdr: about 35 % of a core on average (`BACKLOG.md:85`). Measure with agents running (R11) | — |

What herdr gives and Rex does not (outside the list):

- **Agent detection and status.** herdr reads the screen; Rex reads only OSC 7501. No agent kind, no session id
  (`agent_session`). obor already names Claude's sessions and finds Codex's itself, so the session half costs little.
- **Server-side waits.** No `agent.wait`, no `pane.wait_for_output`. The adapter waits on events
  (`terminal.program_status_changed`, `terminal.child_exited`) or polls the screen (`format`).
- **A notification call.** Rex shows notifications that programs emit; it has no method to show one.
- **Text and Enter in one call.** A `paste` event, then an `enter` key.
- **A documented wire.** The control channel is a WebSocket (`rex.control.v1`; the binary links `coder/websocket`)
  and its framing is not documented. The documented automation surface is the `rex` CLI: `rex api call METHOD ARGS`,
  `rex events --all --json` (one JSON object per line).
- **Openness.** Closed beta, preview update feed, licence not stated. herdr is Apache-2.0.

What Rex gives and herdr does not (outside the list):

- **Ordered events.** Each connection's events carry a gapless `seq`, and the snapshot (`session.bootstrap`) names the
  `seq` it is current to. herdr has no shared sequence, so obor treats every event as a hint and rereads
  (`internal/herdr/socket.go:954-1030`), and subscribes once per pane because herdr has no wildcard (`:744-820`).
- **Events herdr lacks:** `terminal.pwd_changed`, `terminal.process_changed` (foreground pid, name, cwd, ancestors),
  `terminal.child_exited` with the exit code.
- **A server apart from the app.** Observed here: the server process (pid 26711) has run since 11-08; the app was
  restarted on 11-11. Quitting or updating the app does not end the server. `rex server stop` ends every program.
- **Headless and remote.** `rex server start`, Linux and FreeBSD servers, `-S <host>`, a tailnet listener, an iOS
  client.
- **The same nouns as herdr's layout:** session ↔ workspace, window ↔ tab, block ↔ pane, split ↔ split
  (`session.create`, `session.new_window`, `session.new_split` with `anchor_id`, `direction`, `side`, `ratio`,
  `focus: false`). herdr's placement rules (`internal/herdr/setup.go:52-278`) translate one to one.

### herdr-core (`hc` 0.1.13 here; `herdr-core.md` read 0.1.14)

| # | Need | herdr-core |
|---|---|---|
| 1 | Instance identity | Not stated in the help. Terminals survive a core restart (holders), so most of the restore has nothing to do |
| 2 | Relabel event | `labels` event. No rename: a name is fixed at `run`; labels are free `key=value` |
| 3 | Env on start | **Yes**: `terminal.run` takes argv, cwd, env, labels |
| 4 | Blocked respected | **Yes**: `send` refuses a blocked program unless `--force`, takes `--if idle\|working`, and waits for a person to stop typing |
| 5 | Stable fields | **Yes**: "the API only grows", an OpenRPC schema, a released-API compatibility test |
| 6 | Registry | No plugins; `HC_SESSION` isolates a probe |
| 7 | Idle cost | Not measured |

It lacks: any layout (tabs and splits are herdr-app's), agent kind and session, notifications, output-match events;
status is OSC 7501 only; 0.1.x preview. Natively it meets needs 3, 4, 5 and 6; Rex meets 1, 5 and 6, and 3 by argv.

## 2. Where herdr leaks past the port today

Already clean, and worth saying: `HERDR_PANE_ID` has one reader (`internal/project/pane.go:13-29`; it was nine);
no code outside the adapter parses a pane id (`w1:p1` appears only in comments and as the contract's missing id,
`internal/herdr/herdrtest/contract.go:149`); `port.Pane` has no tab or workspace; the label rule
(`internal/project/labelours.go:10-33`) is terminal-neutral.

**A. No selector: the adapter is built in nine places.**
- `cmd/obor/serve.go:104` (`herdr.Panes`), `:166` (a parameter typed `herdr.Panes`), `:251` (`herdrSocket`, used by
  `sleep.go:83`, `restore.go:19,24`, `restart.go:46`); `cmd/obor/setup.go:23,32-34`; `lead.go:23`; `drive.go:132`.
- `internal/view/background.go:201`: the read model dials herdr itself.
- `cmd/obor/serve.go:315-319` reads `*herdr.Error` for the code `refresh`; `:72` uses `herdr.CheckSocketPath` for
  obor's own socket (a general helper owned by the adapter).
- `internal/config/config.go:241-251` knows one kind; `cmd/obor/serve.go:188-189` refuses any other;
  `internal/app/versions.go:24` takes the terminal's tool name from the default, not from the project.

**B. `internal/launch` runs herdr's start protocol (the largest leak).**
- herdr's error codes compared as strings: `internal/launch/start.go:83,93,117,127,205,207`, `launch.go:624`,
  `stop.go:224`; `internal/setup/agent.go:322`; `internal/app/workerstart.go:371`, `loop.go:530`.
  `port.ErrorCode` (`internal/app/port/effects.go:257-264`) is the pipe they come through.
- The "is it up" rule is built on herdr's `launch_pending`: `start.go:138-161` (`attempt`, `counts`, `launching`),
  `:171-234` (`started`: herdr's call raced against a poll), `:236-268`; `port.Pane.Launched` (`port.go:39-44`).
- herdr's agent names: cleared and retried at `start.go:93-110`, compared at `self.go:147`; agents found by name in
  `cmd/obor/lead.go:80-84`, `internal/setup/agent.go:206`, `oborinit.go:85,370`, `rename.go:121-129`;
  `port.Pane.Name` (`port.go:27-30`); `config.AgentNameRE` documented as herdr's rule (`config.go:26-27`).
- The typed env line, because herdr's start takes no env: `launch.go:742-768`, `envLine` `:805`, `envMark` `:839`;
  the wait for a shell `stop.go:411,431`; `port.Start.Shell`, `Await`, `Env` (`effects.go:219-229`);
  `PaneControl.WaitForOutput` exists for this one caller (`launch.go:763`).
- Hints that print a herdr command, and messages that name herdr: `start.go:60,217,331`; `launch.go:311,759`;
  `stop.go:121`; `internal/setup/agent.go:200-348`; `cmd/obor/lead.go:77`, `sleep.go:347,414`.

**C. herdr shapes in the port's own types.**
- `Snapshot.Version`, `Protocol`, `Speaks`, and `Started` as a file time (`port.go:54-63`), read by
  `internal/app/panes.go:43-47` and `restore.go:55-58`.
- `PaneControl`: `Agent(target)` and `Wait(target)` take "an agent name or a pane", `RenameAgent`, `Wake` documented
  as `agent.prompt` (`effects.go:57,62,69,72`). Two `Screen`s and three ways to type (`Typer.Type`,
  `PaneControl.Type`, `Wake`).
- `ProcessInfo` carries herdr's JSON names (`effects.go:286-291`); `NotShown` lists herdr's reasons (`:266-272`).
- One word, two levels: `Pane.Group` is the workspace (`port.go:20-23`), `Placement.Group` and `In` are a tab
  (`effects.go:155-156`).
- Comments that say herdr where they mean the terminal: `port.go:70,73,90,93`.
- The status words (idle, working, blocked, done, unknown) are herdr's, and equal OSC 7501's minus `error`: keep them.

**D. Names in env, files and JSON.**
- `HERDR_SOCKET_PATH` in serve's launchd plist: `internal/service/service.go:34,148-150`, `cmd/obor/service.go:24`.
- `.obor/state/herdr-instance`: `internal/app/restore.go:50-51`.
- Status JSON `herdr`, `herdr_note`: `internal/app/wire/status.go:15`, `internal/view/view.go:80-81`, read by
  `web/src/lib/types.ts:22-23`, `web/src/pages/Project.svelte:61`, `cmd/obor/status.go:477-480`; the app's
  `herdrVer`, `herdrProt`, `herdrOwn` (`internal/app/status.go:574-581`).
- `"workspace"` as the group's JSON name: `internal/app/wire/status.go:279`, and in stored events
  (`internal/app/event/log.go:55`). Leave the stored one: renaming it costs a compat read for no gain.

**E. Texts for agents and people.** `templates/agents/gotchas.md:11,15,30,52`, `templates/agents/README.md:41`,
`skills/setup/SKILL.md:31,41`, `skills/lead/` tell agents herdr commands. `internal/guard/shell.go:481,485,498` says
"agents run in herdr panes". The flags `--tab` and `--new-tab` (`internal/setup/agent.go:36-38`) can stay: Rex's own
docs call its windows tabs.

**F. In the adapter, but not the terminal's.** `internal/herdr/panes.go:131-235` (`Activity`, `Turn`, `lastTurn`,
`transcript`) reads agent transcripts by session id and makes no herdr call; `:242-298` (`Busy`) walks `ps`, a twin of
`internal/view/background.go:89`. Both are on `port.Panes`, so a second adapter would copy about 170 lines. The
terminal contract table lives in the herdr fake's package (`internal/herdr/herdrtest/contract.go`).

**G. Tests.** herdr's wire fake is the terminal in the tests of `cmd/obor` (16 files), `internal/app` (11),
`internal/launch` (6), `internal/setup` (4), `internal/guard` (1) and `tests/upgrade` (1): those tests exercise
herdr's protocol to test obor's decisions.

## 3. The port shape

**Rule.** The port says what obor wants done; the adapter says how its terminal does it. A second terminal is then
one package, one `case` in the selector, one row in the pane-variable table and one live tier.

### What the port names

| Name | Meaning | herdr | Rex | herdr-core |
|---|---|---|---|---|
| **Pane** | one place where one program runs; `ID` is opaque, never parsed, unique in an instance | pane | block | terminal |
| **Group** | the named set of panes a person sees together; a project's panes are the group named after it | workspace label | session label | a label `project=<name>` |
| **Label** | the pane's visible name: obor's agent name, the mark for people and for the restore | pane label | block label | name, or a label `agent=` |
| **Agent** | what runs in a pane: obor's kind name, a `Status` of obor's five words, a `Session` when the terminal reports one | detection | OSC 7501 | OSC 7501 |
| **Instance** | an opaque string for this run of the terminal's server; a new value means it restarted | socket mtime | `instance_id` | to be probed |
| **Event** | created, closed, exited, status; each carries the whole pane. A stream is a snapshot and the events after it; one that cannot stay complete ends, and the core subscribes again | reread per event | `seq` | `seq` and `missed` |
| **Placement** | whose pane, its cwd, and near what (`Own`, a new named place, in a place the user named, beside a pane). Never a tab, window or split | tabs, splits | windows, splits | nothing to lay out |
| **Start** | run this agent in this pane: label, kind, argv, env, cwd. Returns the pane it runs in once it runs | env line, `agent.start`, the evidence poll | to be chosen (D5) | `terminal.run` |
| **Type** | text into a pane, submitted; with `Guard`, refused before any byte when the agent is blocked | `agent.prompt` | status check, `paste`, `enter` | `send` |

Identity in a process: `OBOR_PANE`, which every obor start sets, else the terminal's own variable. That variable is
picked **by the project's terminal kind, not by whichever is set**: one table in `internal/project`
(`herdr: HERDR_PANE_ID`, `rex: REX_BLOCK`), because hooks cannot import an adapter and because a terminal started from
inside another terminal's pane inherits the other's variable (`herdr-core.md` §2 describes this leak for hc; a Rex
server autostarted from a herdr pane would carry `HERDR_PANE_ID` and `OBOR_*` into every block).

### Sketch

```go
// Terminal is all obor asks of a terminal. One adapter implements it; serve, launch and setup each use a part.
type Terminal interface {
	// observe
	Subscribe(ctx context.Context) (*Stream, error)
	Snapshot(ctx context.Context) (Snapshot, error) // Instance, Version, Note, Panes
	Pane(ctx context.Context, id string) (Pane, error)
	Screen(ctx context.Context, pane string, lines int) (string, error)
	ProcessInfo(ctx context.Context, pane string) (ProcessInfo, error)
	// act on a pane
	Type(ctx context.Context, pane, text string, o TypeOpts) (began bool, err error) // Guard, Clear
	Keys(ctx context.Context, pane string, keys ...string) error                    // obor's key names
	Label(ctx context.Context, pane, label string) error
	WaitStatus(ctx context.Context, pane string, until []string, timeout time.Duration) error
	Start(ctx context.Context, s AgentStart) (Pane, error)
	// setup
	Place(ctx context.Context, p Placement) (pane string, err error)
	RenameProject(ctx context.Context, old, name string, relabel func(string) string) (Renamed, error)
	ResumeHint() string
	Notify(ctx context.Context, title, body string) error
}
```

Fourteen methods where `Panes`, `PaneControl`, `Typer` and `TerminalSetup` have twenty-four between them today
(a pane list is `Snapshot`'s). Errors
are port predicates (`NotFound`, `NotRunning`, and new: `PaneBusy`, `NotReady`, `Blocked`, `TimedOut`), not code
strings. Stopping an agent stays in `launch`: the quit recipe is the kind's, and it needs only `Keys`, `Type`,
`Screen`, `ProcessInfo` and a signal to a pid.

### What stays private to an adapter

Tabs, workspaces, windows, sessions, splits and sizes; agent names and their upkeep; `launch_pending`; wire error
codes; protocol numbers and schema checks; the socket path and its variable; how the env reaches the agent; how
"blocked" and "up" are known; one subscription per pane; the terminal's config file and what its hint says.

### What leaves the terminal port altogether

`Activity`, `Turn` and the transcript lookup (they are about agent kinds), and `Busy`'s `ps` walk (it needs only
`ProcessInfo`). `WaitForOutput` goes once the env line is inside the herdr adapter.

### What "both" means

- **Per project (recommended, and free with the selector).** `project.terminal` picks the adapter; one serve, one
  terminal, one event stream, one instance to restore against. A project switches by `obor stop`, changing the key,
  `obor start`: panes do not move between terminals, but sessions do, because the session ids are obor's records.
- **Per role, or one serve on two terminals (not recommended).** It needs pane ids from two namespaces, two streams
  merged in order, two instances to restore against and a choice in every placement, in the package where ordering
  bugs are already the main cost (`improve-architecture.md` A1). If the owner later wants the lead in Rex and workers
  headless, it is built as an adapter that wraps two adapters and prefixes their pane ids; the port above allows it
  because ids and the instance are opaque. Nobody has asked for it, so it gets no selector now.

## Owner decisions

- **D1.** "Both" is per project; per role is not built. (Recommended: yes.)
- **D2.** Where the choice lives. `project.terminal` is in the committed project config, so it holds for everyone
  on the project; a terminal is closer to a fact about a machine. Recommended: keep the project key, and add a
  per-machine override only when a second person needs a different terminal.
- **D3.** Status on a terminal that detects nothing (Rex, herdr-core). Options: (a) obor's hooks write OSC 7501 to
  the pane's tty, so the terminal reports status natively and the port does not change; (b) hooks report to serve
  and serve overlays the status; (c) Rex only for kinds that report by themselves (Claude). Recommended: probe (a)
  first (R3); it is the terminal's native format and works for herdr-core unchanged.
- **D4.** Rex transport: the `rex` CLI as a subprocess (no dependency, the documented surface; a process per call
  and one long `rex events` child) or a WebSocket client (a new dependency needing approval, on an undocumented
  framing). Recommended: the CLI for the probe and the first adapter.
- **D5.** Rex pane model: the agent typed at a shell block (as on herdr: the pane id is stable, the typed env line
  stays) or the agent as the block's own command (nothing typed; a restart makes a new block, so the pane id
  changes). Decide after R4 and R8.
- **D6.** Notifications where the terminal has none: an OS notifier behind `Notify`, or none.
- **D7.** Rex is a closed beta whose API may change: accept it as an experimental adapter whose live tier does not gate.

## Design concerns

- **launch is half a herdr adapter.** That is the cost of a port made of herdr's verbs. It is also where most start
  bugs were fixed, so moving it is the riskiest step in the plan; it is last before Rex for that reason.
- **Core tests speak herdr's wire.** Thirty-nine test files outside the adapter depend on the herdr fake. A port-level
  fake would make them cheaper and terminal-neutral, but it is a large test change; the plan takes it only for
  `launch`, where the start moves anyway.
- **Probes go stale** (`herdr-usage-deep-dive.md` §6). A second terminal doubles that. The live tier per terminal
  should run when serve sees the terminal's version change (`improve-architecture.md` A8).

## Not verified

Everything about Rex's behaviour at run time: which event a block relabel sends; whether Claude, Codex, OpenCode or
an obor hook's write reaches `program_status`; what env a block gets and whether `env …` as the command works as
expected; what `--restore` brings back after a server stop and whether ids survive; whether blocks created through the
API show in the app without a person attaching; paste into a TUI; idle CPU under load; the CLI's cost per call.
For herdr-core: its instance identity, and anything that changed between 0.1.13 and today. The herdr line numbers are
at commit `96131f0c`.
