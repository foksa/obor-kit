# Terminal adapters: herdr cleaned up behind the port, then Rex

Advisor plan, 2026-11-11. Reasons, the Rex and herdr-core tables, the leak list and the port sketch are in
`docs/research/terminal-port-advice.md`; this file is the steps. Line numbers are at commit `96131f0c`.

Steps 1-5 are small, independent of Rex and useful on herdr alone. Step 6 is hard and waits for a decision that a
second terminal will be built. Steps 7-9 are Rex. No task ids or specs here: the owner picks, the lead writes them.

## Decisions needed first (owner)

| # | Decision | Recommended | Blocks |
|---|---|---|---|
| D1 | "Both" means one terminal per project | yes | step 1's selector shape |
| D2 | The choice lives in `project.terminal` (committed), with no per-machine override yet | yes | step 9 |
| D3 | Status on a terminal that detects nothing: hooks write OSC 7501, serve overlays hook reports, or Claude only | probe the first (R3) | step 8 |
| D4 | Rex transport: the `rex` CLI as a subprocess, or a WebSocket dependency | the CLI | step 9 |
| D5 | Rex pane model: agent typed at a shell block, or agent as the block's command | after R4, R8 | step 9 |
| D6 | Notifications where the terminal has none | an OS notifier behind `Notify` | step 9 |
| D7 | Rex as an experimental adapter whose live tier does not gate | yes | step 9 |

## Steps

### 1. One place builds the terminal
- **Does.** `cmd/obor/terminal.go`: one function from the project's `TerminalKind()` and the env to a
  `port.Terminal` (herdr is its only case). `port.Terminal` is declared as the union of today's `Panes`,
  `PaneControl`, `Typer` and `TerminalSetup`; nothing else in the port changes yet. `view.ScanBackground` takes the
  process-info function from its caller. The stream's "refresh" end becomes a port predicate (or the adapter
  resubscribes itself). `CheckSocketPath` moves to `internal/app/wire`, whose socket it checks.
- **Files.** `cmd/obor/serve.go:104,166,251,315-319,72`, `setup.go:23,32-34`, `lead.go:23`, `drive.go:132`,
  `sleep.go:83`, `restore.go:19,24`, `restart.go:46`; `internal/view/background.go:176-204`;
  `internal/app/port/effects.go`; `internal/checks/boundary_test.go` (only `cmd/obor/terminal.go` and
  `tests/conformance` may import `internal/herdr`).
- **Deletes.** Nine construction sites, `herdrSocket`, `terminalSetup`, `refreshed`'s herdr type, the read model's
  import of an adapter.
- **Tests.** Changed: the boundary test gains the rule. None added.

### 2. Neutral names where herdr shows through
- **Does.** `Snapshot.Started time.Time` becomes `Instance string` (herdr: its socket mtime, formatted);
  `Version`, `Protocol`, `Speaks` become `Version` and `Note` (the adapter's own sentence when the terminal is newer
  than it knows). The instance file is `.obor/state/terminal-instance`; the old name goes on the sweep's retired
  list. Status JSON `herdr`, `herdr_note` become `terminal`, `terminal_note`. The launchd plist takes the terminal's
  variables from the adapter. `versions.go` names the project's terminal kind. Messages and hints say "terminal",
  and a hint that printed a herdr command prints the obor command (`obor restart <agent>`).
- **Files.** `internal/app/port/port.go:54-63`; `internal/herdr/panes.go:98-119`; `internal/app/panes.go:43-47`,
  `restore.go:50-58,91`, `status.go:574-581`, `versions.go:24`; `internal/app/wire/status.go:15`;
  `internal/view/view.go:80-81,253`; `web/src/lib/types.ts:22-23`, `web/src/pages/Project.svelte:61`;
  `cmd/obor/status.go:477-480`; `internal/service/service.go:34,148-150`, `cmd/obor/service.go:24`;
  `internal/project/sweep.go:37`; the message sites in `internal/launch/start.go:60,217,331`, `launch.go:311,759`,
  `stop.go:121`, `internal/setup/agent.go:200-348`, `cmd/obor/lead.go:77`, `sleep.go:347,414`,
  `internal/guard/shell.go:481,485,498`; comments `port.go:70,73,90,93`, `internal/config/config.go:26-27`.
- **Deletes.** `herdrVer`, `herdrProt`, `herdrOwn` and `herdrNote` in the app; the protocol numbers from the port.
- **Leaves.** `"workspace"` in stored events (`internal/app/event/log.go:55`).
- **Tests.** Changed: the status and view tests' field names, `TestTSStringSetsMatchGo` if it lists them.

### 3. What is not the terminal's leaves the adapter
- **Does.** `Activity`, `Turn`, `lastTurn` and `transcript` move to one owner that knows agent kinds and files
  (they make no terminal call). `Busy` becomes a function over `port.ProcessInfo` and a `ps` table, sharing its walk
  with `view.BackgroundOf`. `port.Panes` keeps `Subscribe`, `Snapshot`, `Screen`, `Notify`. The terminal contract
  table moves from `internal/herdr/herdrtest/contract.go` to `internal/testkit`, beside the workflow contract table,
  and gains rows: instance (non-empty, equal across two snapshots), place (a new pane in the project's group with
  its cwd), type with guard.
- **Files.** `internal/herdr/panes.go:131-298`; `internal/view/background.go:76-175`; `internal/app/watchdog.go`
  (the callers); `internal/app/port/port.go:90-110`; `internal/herdr/herdrtest/contract.go`,
  `internal/herdr/terminal_contract_test.go`, `tests/conformance/live_herdr.go:70-71`.
- **Deletes.** About 170 lines from `internal/herdr`, three methods from `port.Panes`, one of two `ps` walks.
- **Tests.** Moved with the code (`panes_test.go`'s transcript and busy cases); three contract rows added.

### 4. Port errors, not herdr codes
- **Does.** New predicates beside `NotFound` and `NotRunning`: `PaneBusy`, `NotReady`, `Blocked`, `TimedOut`.
  `internal/herdr` maps its codes to them (`agent_pane_busy` and `pane_not_available`, `agent_not_ready`,
  `agent_blocked` and `blocked`, `timeout`). `agent_name_taken` and `agent_not_found` stop crossing the port: the
  adapter handles the first inside its start and reports the second as `NotFound`.
- **Files.** `internal/app/port/effects.go:82-117,257-264`; `internal/herdr/socket.go:55-110`;
  `internal/launch/start.go:83,93,117,127,202-209`, `launch.go:624`, `stop.go:224`; `internal/setup/agent.go:322`;
  `internal/app/workerstart.go:371`, `loop.go:530`, `messages.go:62`.
- **Deletes.** `port.ErrorCode` and twelve string comparisons.
- **Tests.** Changed: the fakes return the predicates. A contract row per predicate that a shell pane can show
  (`TimedOut`, `NotFound` exist; `PaneBusy` added).

### 5. Panes by id, one way to type
- **Does.** `Agent(target)` becomes `Pane(id)`; `Wait(target, …)` becomes `WaitStatus(pane, …)`. Callers that find a
  running agent by herdr's name match the pane's label instead. `RenameAgent` and `Pane.Name` leave the port.
  `Typer.Type`, `PaneControl.Type` and `Wake` become one `Type(pane, text, TypeOpts{Guard, Clear})`;
  `PaneControl.Screen` and `Panes.Screen` become one. Keys are obor's names (`enter`, `esc`, `ctrl+c`, `ctrl+u`, a
  digit), which today equal herdr's; each adapter maps them.
- **Files.** `internal/app/port/effects.go:46-80`, `port.go:27-30`; `internal/herdr/socket.go:392-530`,
  `panes.go:305-328`; `internal/launch/start.go:93-110`, `self.go:138-148`, `launch.go:635`, `stop.go`;
  `cmd/obor/lead.go:75-86`; `internal/setup/agent.go:206`, `oborinit.go:85,370`, `rename.go:121-129`;
  `internal/app/prompt.go`.
- **Deletes.** `Pane.Name`, `RenameAgent`, `Wake`, one `Screen`, two `Type`s.
- **Risk.** A pane whose label and herdr name differ (a person relabelled it): today the name wins in `obor lead`'s
  "already runs" check. The rest of obor never reads herdr's name (`docs/design.md:776`, `docs/module-map.md:113`).
- **Tests.** Changed: launch's name-taken cases move to `internal/herdr`.
- **Live.** `make conformance TIER=live TURN=1 KIND=<kind>` for claude, codex and opencode (it touches how an agent
  is launched).

### 6. The start behind the port (hard; only once a second terminal is decided)
- **Does.** `Start(ctx, AgentStart{Pane, Label, Kind, Argv, Env, Cwd, Prompt, Bound}) (Pane, error)` returns once the
  agent runs, or a `PaneBusy`, `NotReady` (with its hint) or `TimedOut` error. The herdr adapter does inside it what
  launch does today: wait for the shell, type the env line and read its mark, label the pane, `agent.start`, the
  evidence rule on `launch_pending`, the name-taken retry. launch keeps who, where, which session, the kind's
  build, the session record and the report to serve. It may come back with another pane id than it was given (a
  terminal that replaces the pane on a restart).
  Two tasks: (a) the env line and shell wait, (b) the evidence rule.
- **Files.** `internal/launch/start.go:39-268`, `launch.go:735-860`, `stop.go:411-445`; new
  `internal/herdr/start.go`; `internal/app/port/effects.go:181-255` (`AgentEnv`, `Start`, `Starter.SetEnv`);
  `internal/setup/agent.go:290-350`; `port.go:39-44`.
- **Deletes.** From the port: `Pane.Launched`, `Start.Shell`, `Await`, `Env`, `Starter.SetEnv`, `WaitForOutput`.
  From launch: about 250 lines that know herdr. launch's tests use a small in-memory `port.Terminal` instead of
  herdr's wire fake (6 files); the start cases move to `internal/herdr`'s tests against `herdrtest`.
- **Risk.** This code holds the fixes for a long run of live start bugs. Move it without changing a rule; the oracle
  is the existing start tests moved whole, and the live tier for all three kinds before and after.
- **Live.** As step 5, for every kind.

### 7. Rex probe (no product code)
- **Does.** One live run on a throwaway Rex server (`rex server run --listen unix://<scratch>/s.sock --state-dir
  <scratch> --config <scratch>/init.lua --restore=false`, always `--autostart=false` on the client), never the
  owner's server. A research note with one fact per probe:

| # | Probe | Decides |
|---|---|---|
| R1 | `rex api call` and `rex events --all --json`: time per call, event shape, behaviour when the server stops | D4 |
| R2 | which event a `session.set_block_label` sends | the label merge |
| R3 | `program_status` for Claude, Codex, OpenCode; and for an OSC 7501 written to `/dev/tty` by a hook | D3 |
| R4 | env of a new block; `command: ["env", "K=V", …]` with `shell` default and `none`; whether `OBOR_*` or `HERDR_*` leak from the server's env | D5, identity |
| R5 | after a server stop and start with `--restore`: which blocks return, whether programs re-run, whether ids and `instance_id` change | restore |
| R6 | app quit and relaunch: blocks keep running | — |
| R7 | a multi-line `paste` and an `enter` key into Claude's and Codex's input | `Type` |
| R8 | a block kept open after its program exits (`exit.on_completion: false`): can a new program start in it | D5 |
| R9 | `process`: child, foreground and ancestors for shell → agent → tool | `ProcessInfo`, `AtShell` |
| R10 | the scratch server leaves the owner's server, config and app untouched | the test rule |
| R11 | server CPU with three agents idle for an hour | need 7 |
| R12 | a session and blocks created through the API: visible in the app without a person attaching; `focus: false` steals no focus | flow |
| R13 | a way to show a notification | D6 |

- **Files.** `tests/conformance` (a `KIND=rex` live tier that prints facts, as `live_herdr.go` does);
  `docs/research/rex-probes.md`.
- **Go or no-go.** No-go if R3 leaves no status path for the kinds the owner runs, or R12 says a person must attach
  by hand each time.

### 8. Status without detection (if D3 chose hooks)
- **Does.** For a kind whose `Caps.Status` is not the terminal's, obor's hooks report the turn's start, its stop and
  a permission prompt in the form D3 chose. With OSC 7501 the terminal reports it and the port is unchanged.
- **Files.** `internal/guard` (the hook entry points), `internal/agents/*/` (`Caps.Status`), `kinds/opencode`,
  `opencode/obor.js`.
- **Deletes.** Nothing on herdr. It is the step that removes the need for herdr's screen detection.
- **Live.** Every kind.

### 9. The rex adapter
- **Does.** `internal/rex`: the client (D4), `port.Terminal` over it. Group ↔ session label, placement in windows
  and splits (herdr's rules translated), `Instance` from `instance_id`, the stream from `session.bootstrap` and
  `seq`, `Start` per D5, `Type` as a status check, `paste`, `enter`. `config.TerminalKinds` gains `rex`; the selector
  gains its case; `internal/project`'s pane-variable table gains `rex: REX_BLOCK`, picked by the project's kind.
  Mapping functions are unit-tested on recorded JSON; the contract table runs live against a scratch server
  (`make conformance TIER=live KIND=rex`). No fake Rex server.
- **Files.** `internal/rex/` (new); `cmd/obor/terminal.go`; `internal/config/config.go:241-251`, `validate.go:391`;
  `internal/project/pane.go:9-29`; `internal/checks/boundary_test.go`; `tests/conformance`; `docs/design.md`,
  `docs/module-map.md`.
- **Deletes.** `cmd/obor/serve.go:188-189`'s refusal of a second kind.
- **Texts.** `templates/agents/gotchas.md:11,15,30,52` and `skills/setup/SKILL.md:31,41` split into what holds on any
  terminal and a section per terminal; where an agent is told a herdr command for something obor can do, the text
  names the obor command.

## Order and stops

1 → 2 → 3 in any order after 1; 4 → 5 → 6 in that order. 7 can run any time after D1 and needs no earlier step.
Stop after 5 if no second terminal is decided: herdr's words are then out of the core, the port has one way to do
each thing, and nothing was built for an implementation nobody named. 8 and 9 need 6 and a go from 7.

## Done when

`grep -ri herdr internal/app internal/launch internal/setup internal/view cmd/obor` finds only `cmd/obor/terminal.go`
and comments that name herdr as an example; the terminal contract table passes for herdr on the fake and live, and for
Rex live; a project with `project.terminal: rex` runs init, a dispatch, a merge and a restore after a Rex server
restart.
