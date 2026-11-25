# Terminal adapters: herdr cleaned up behind the port, then Rex

Advisor plan, 2026-11-11. Reasons, the Rex and herdr-core tables, the leak list and the port sketch are in
`docs/research/terminal-port-advice.md`; this file is the steps. Line numbers are at commit `96131f0c`.

The frame (owner 11-11): Rex is a multiplexer, herdr is a tool for agents. herdr is a multiplexer with an agent
layer on top (status, start, guarded prompt); Rex is the multiplexer alone. So the port splits into a multiplexer
port every terminal implements and an agent layer that herdr supplies natively and obor supplies over a plain
multiplexer.

Steps 1-5 are small, independent of Rex and useful on herdr alone. Steps 6 and 7 are the split and obor's own agent
layer: hard, and they wait for a decision that a second terminal will be built. Steps 8 and 9 are Rex. No task ids or
specs here: the owner picks, the lead writes them.

## Decisions needed first (owner)

| # | Decision | Recommended | Blocks |
|---|---|---|---|
| D1 | "Both" means one terminal per project | yes | step 1's selector shape |
| D2 | The choice lives in `project.terminal` (committed), with no per-machine override yet | yes | step 9 |
| D3 | Who owns the agent layer on herdr: herdr (two layers in obor) or obor (herdr driven as a multiplexer) | build obor's, compare on herdr, then decide | what step 7 deletes |
| D3a | obor's status and blocked signal | decided 11-25: OSC 7501 alone where the terminal relays it; never the screen (the start's one silent-dialog read aside) | step 7 |
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

### 5. Panes by id, two ways to type
- **Does.** `Agent(target)` becomes a pane read by id; `Wait(target, …)` becomes `WaitStatus(pane, …)`. Callers that
  find a running agent by herdr's name match the pane's label instead. `RenameAgent` and `Pane.Name` leave the port.
  Typing has two meanings and gets two names: `Write(pane, text, submit)` (the multiplexer's: a command line at a
  shell, a quit) and `Prompt(pane, text, clear)` (the agent layer's: refused when the agent is blocked).
  `PaneControl.Type` is the first; `Typer.Type` and `Wake` are the second. `PaneControl.Screen` and `Panes.Screen`
  become one. Keys are obor's names (`enter`, `esc`, `ctrl+c`, `ctrl+u`, a digit), which today equal herdr's; each
  adapter maps them.
- **Files.** `internal/app/port/effects.go:46-80`, `port.go:27-30`; `internal/herdr/socket.go:392-530`,
  `panes.go:305-328`; `internal/launch/start.go:93-110`, `self.go:138-148`, `launch.go:635`, `stop.go`;
  `cmd/obor/lead.go:75-86`; `internal/setup/agent.go:206`, `oborinit.go:85,370`, `rename.go:121-129`;
  `internal/app/prompt.go`.
- **Deletes.** `Pane.Name`, `RenameAgent`, `Wake`, one `Screen`, one of three ways to type.
- **Risk.** A pane whose label and herdr name differ (a person relabelled it): today the name wins in `obor lead`'s
  "already runs" check. The rest of obor never reads herdr's name (`docs/design.md:776`, `docs/module-map.md:113`).
- **Tests.** Changed: launch's name-taken cases move to `internal/herdr`.
- **Live.** `make conformance TIER=live TURN=1 KIND=<kind>` for claude, codex and opencode (it touches how an agent
  is launched).

### 6. Split the start: the multiplexer's part and herdr's part (hard; only once a second terminal is decided)
- **Does.** launch's start is two things today. One is work over any multiplexer: wait for the pane's shell, set the
  env and cwd, label the pane, look at the screen for a startup dialog. The other is herdr's agent protocol:
  `agent.start`, the evidence rule on `launch_pending`, the name-taken retry, `agent_pane_busy`. The second moves into
  `internal/herdr` as `AgentStarter.Start(ctx, AgentStart{Pane, Label, Kind, Argv, Prompt, Bound}) (Pane, error)`,
  which returns once the agent runs, or a `PaneBusy`, `NotReady` or `TimedOut` error. The first stays in launch and
  calls only `Mux` methods. launch asks the terminal for `AgentStarter`; step 7 adds the path for a terminal that
  has none. A start may come back with another pane id than it was given.
- **Files.** `internal/launch/start.go:83-111,134-268` (to `internal/herdr/start.go`, new); `start.go:39-82,112-132`
  and `launch.go:735-860`, `stop.go:411-445` (stay, on `Mux`); `internal/app/port/effects.go:54-80,207-255`;
  `port.go:39-44`; `internal/setup/agent.go:290-350`.
- **Deletes.** From the port: `Pane.Launched`, `PaneControl.Start(name, kind, pane, args)`, `WaitForOutput` (the env
  line reads its mark through `Screen`). From launch: every herdr code and the evidence rule, about 150 lines.
  launch's tests use a small in-memory `Mux` instead of herdr's wire fake (6 files); the evidence cases move to
  `internal/herdr`'s tests against `herdrtest`.
- **Risk.** This code holds the fixes for a long run of live start bugs. Move it without changing a rule; the oracle
  is the existing start tests moved whole, and the live tier for all three kinds before and after.
- **Live.** As step 5, for every kind.

### 7. OSC 7501's home and one prompt guard (owner 11-25, `docs/research/osc7501-architecture-advice.md`)
Status on a terminal that relays OSC 7501 (hc, Rex) comes from the program's root record alone, blocked included
(D3a); obor builds no status of its own from hooks or transcripts, adds no blocked hook and no `Caps.Status` source.
Hooks and Codex's transcript stay the turn facts of the stop watchdog, not pane status.
- **7a (T-1564).** `internal/progstatus`: the record (`State`, `Kind`, `Msg`, `App`), its word, note and shown text;
  `agents.ByApp` for the kind. Adapters decode their wire into it; `port.Pane` keeps `Status` and `Note`.
- **7b (T-1565).** `port.GuardPrompts` in front of every terminal's `Prompt` (`cmd/obor/terminal.go`): no agent →
  NotFound, blocked → `Blocked`, before any key; herdr's and hc's native refusals stay behind it.
- **The start.** hc's typed start (`internal/hc/start.go`) is the shell model; if D5 picks it for Rex, the start moves
  to one owner over `PaneControl` in the Rex task that needs it, never copied.

### 8. Rex probe (no product code)
One live run on a throwaway Rex server (`rex server run --listen unix://<scratch>/s.sock --state-dir <scratch>
--config <scratch>/init.lua --restore=false`, always `--autostart=false` on the client), never the owner's server.
Findings: `docs/research/rex-probes.md`. The field names of a status record and its events are read from the binary
(advice §1); R6, R11, R13 and R3's hook-written record are dropped.

| # | Still unknown | Decides |
|---|---|---|
| R10 | the scratch server leaves the owner's server, config and app untouched | whether any other probe runs |
| R12 | blocks made through the API show in the app with nobody attaching; `focus: false` | go or no-go |
| R1 | `rex api call` time per call; `rex events --all --json` shape and its end when the server stops; `rex api describe` | D4 |
| R3 | each kind in a block: a root record, the words over a turn, blocked with kind and msg at a permission prompt and how soon after the dialog; Codex, which sends no query; removal when the agent exits | the adapter's read, the guard's gap |
| R4, R8 | a new block's env (whether `OBOR_*`, `HERDR_*` leak from the server's); a block kept after its program exits | D5, identity |
| R7 | a multi-line paste and Enter into Claude's and Codex's input (`rex send`, `rex send-key`) | `Write` |
| R9 | `process` for shell, agent, tool | `AtShell`, "up" |
| R2, R5 | the event of a relabel; what a server restart brings back | label merge, restore |

- **Go or no-go.** No-go if R12 says a person must attach by hand each time, or R1 fails D4.

### 9. The rex adapter
- **Does.** `internal/rex`: the client (D4: the `rex` CLI, required on `PATH`; `obor setup check` on a Rex project
  fails closed when it is missing and names the bundle path as the hint) and `port.Mux` over it. The status is a
  `progstatus.Report` from the block's root `program_status` record and the `program_status_changed` /
  `_removed` events; no agent code beyond the shared three lines. `Prompt` is clear, paste, Enter behind 7b's guard
  (Rex has no native refusal: a dialog that opens between the read and the write gets the text; R3 sizes the gap).
  Group ↔ session label, placement, `Instance` from `instance_id`, the stream from `session.bootstrap` and `seq`,
  `ProcessInfo` from `process`. Sub-steps as hc's (`docs/plan/herdr-core.md` steps 2-4, 6). `config.TerminalKinds`
  gains `rex`; `internal/project`'s pane-variable table gains `rex: REX_BLOCK`. Mapping functions are unit-tested on
  recorded JSON; the contract table runs live against a scratch server. No fake Rex server.
- **Before a worker runs on Rex:** a guard deny rule for `rex send`, `rex run`, `rex kill` and the like inside a
  block, as for `hc`.
- **Files.** `internal/rex/` (new); `cmd/obor/terminal.go`; `internal/config`; `internal/project/pane.go`;
  `internal/checks/boundary_test.go`; `tests/conformance`; `docs/design.md`, `docs/module-map.md`.
- **Texts.** `templates/agents/gotchas.md` and `skills/setup/SKILL.md` split into what holds on any terminal and a
  section per terminal.

## Order and stops

Steps 1-6 are done (6, the start split, with hc: `docs/plan/herdr-core.md`). 7a → 7b; 8 runs any time. 9 needs 7 and
a go from 8.

## Done when

`grep -ri herdr internal/app internal/launch internal/setup internal/view cmd/obor` finds only `cmd/obor/terminal.go`
and comments that name herdr as an example; the terminal contract table passes for herdr on the fake and live, and for
Rex live; a project with `project.terminal: rex` runs init, a dispatch, a merge and a restore after a Rex server
restart.
