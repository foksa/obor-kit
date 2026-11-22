# herdr-core (`hc`) as an obor terminal: implementation plan

Cited here but no longer in the tree: `docs/research/herdr-core.md`, `docs/research/improve-architecture.md`. Git keeps
them: `git show v0.44.0:<path>`.

Advisor plan, 2026-11-20, for the owner's question of the same day. It builds on `docs/research/herdr-core.md`
(10-30), `docs/research/terminal-port-advice.md` and `docs/plan/terminal-adapters.md` (11-11; steps 1-5 done as
T-1245..T-1253). obor line numbers are at commit `d1fb30ef`.

**What was read.** `hc` 0.1.21 installed here: `--help`, every subcommand's `--help`, `--skill`, `hc schema`
(OpenRPC 1.3.2, 32 methods), `hc server status`, `hc ls --all`. The herdr-core repo at tag `v0.1.21`
(`1b0393f8`, 2026-11-12): `CHANGELOG.md`, `README.md`, `schema/README.md`, `docs/security.md`,
`crates/herdr-core/src/api/run.rs`, `client.rs`, `platform/macos_launchd.rs`, `paths.rs`; the schema at `v0.1.14`
(`29e6d375`) for a diff; master's 10 commits after the tag (head `c7517808`, 11-16). herdr discussions #5684, #5073,
#5106. obor: `internal/app/port`, `internal/herdr`, `internal/herdr/herdrtest`, `internal/launch/start.go`,
`launch.go`, `restore.go`, `internal/app/restore.go`, `internal/testkit/terminal.go`, `cmd/obor/terminal.go`,
`internal/project/pane.go`, `tests/conformance`.

**What was not done.** No terminal was started, killed or typed into, in hc or herdr; no core was started or
restarted. Every claim about hc's behaviour at run time is from its help, schema, docs or source; the ones that
need a live check are probes P1-P11 (step 0).

Words used: an hc **terminal** is hc's pane (one program, address `t7`). The **core** is hc's background server; a
**holder** is the process that owns terminals so they outlive a core restart. A **kept** terminal (`keep`) gets a
fresh shell in place when its program is lost. **OSC 7501** is the escape sequence a program writes to report
`idle`, `working`, `blocked`, `done` or `error`; hc reads status only from it.

**Probe facts (T-1351, 2026-11-20, `docs/research/hc-probes.md`).** None of step 0's no-go checks hit: all three
kinds report OSC 7501 inside hc, `blocked` included, and a send at `blocked` is refused before any byte. Where a fact
differs from this plan, the probe note's "What each fact changes in steps 2-5" is the spec input: `Snapshot.Instance`
is the core id plus the sorted holder instances (not the pid); "up" is foreground not the shell and status not null;
a turn ends when the status leaves `working` (OpenCode never says `done`); `Prompt`'s bound exceeds hc's 60 s
`draft_wait`; no ~1 KB env-line split on hc; `env: {"NAME": ""}` sets empty, never unsets. **Step 5 is dropped**
(only Codex's trust and update prompts report nothing: the start's screen read covers the first, the second is an
upstream ask). HD4 holds as decided. **HD8 changed (owner 11-20, `docs/decisions.md`):** P7 passed, and obor lays out herdr-app like herdr (project space, named lead, lane and role tabs) through herdr-app's commands, placing terminals by `herdr.space`/`herdr.tab` labels: part of step 3's `Place`, `--tab`/`--new-tab` no longer refused on hc.

## Recommendation

1. **Build hc as a second adapter behind the port, chosen per project by `project.terminal: "hc"`; herdr stays the
   default. Do not replace herdr now.** Replacement is a later owner decision with named criteria (Stop C). The
   adapter is experimental: its live tier does not gate, and nothing changes for a project that does not set the key.
2. **The 10-30 triggers are not all met** (§1): hc is still "preview", #5684 has no reply, and only Claude is known
   to report OSC 7501. What changed is that the first two steps below are worth doing whatever hc turns out to be,
   and that hc's API held its "only grows" promise across the seven releases since 0.1.14 (checked by diffing the
   two schemas: nothing removed). So: go, but in an order where the first money spent is a probe and a cleanup.
3. **Order: a probe run with no product code (step 0), then `terminal-adapters.md` step 6 (the start split), then
   the adapter in three task-sized pieces, then a live run on a real project.** Step 6 comes first because without
   it an hc adapter has to imitate herdr's `launch_pending` to satisfy `internal/launch`.
4. **On hc, hc owns most of the agent layer and obor owns the rest.** hc's status (relayed OSC 7501), its guarded
   `terminal.send` and its events are the terminal's facts, like herdr's; obor supplies what hc has no notion of:
   which kind runs, the session, and the proof that a start is up. `terminal-adapters.md` step 7 (obor's own agent
   layer) is then not needed for hc, except a status fallback for a kind that reports nothing (sized by probe P1).
   herdr's side does not change: D3 stays at "two layers", and the week of logging obor's status beside herdr's is
   dropped (it was there to decide D3 for the Rex path).
5. **Pane model: one kept shell terminal per pane, the agent's command line written at its shell**, as on herdr.
   It leaves launch's stop, restart in place, home pane and restore as they are, and hc's `keep` gives exactly what
   the restore expects (a labelled pane back at a shell). The alternative, the agent as the terminal's own program,
   deletes more and is listed under HD5.
6. **Two things in hc need care before a project runs on it:** a core started from inside an obor pane still gets
   that pane's environment (verified in source at 0.1.21, §4 risk 2), and hc has no layout, no notifications and no
   agent kinds. The plan has a guard for the first and owner decisions for the rest.

## 1. Where hc stands (0.1.21, tag `1b0393f8`)

### Against `herdr-core.md` §5's triggers

| # | Trigger | Now | Met |
|---|---|---|---|
| 1 | A stable generation (1.0 or a stated freeze), releases slowing from one a day | `schema/README.md` still says "Status: preview (protocol generation 1, version 0.x)". Cadence: 0.1.10-0.1.14 in the week to 10-29, then 0.1.15 (10-30), .16 (10-31), .17 (11-02), .18 (11-04), .19 (11-07), .20 (11-10), .21 (11-12); none in the 8 days since, master last touched 11-16. The released-API test exists (`released.openrpc.json`), and my diff of `hc.openrpc.json` 0.1.14 → 0.1.21 shows no method, param or field removed | no (slower: yes; stable: not stated) |
| 2 | Upstream answers #5684 (Q7 TUI and core, Q5 env, Q3/Q11 focus or input lock, Q9 restore) | 0 comments on 11-20. Releases answered three in code: Q5: `terminal.run` takes `env`, the core keeps its env across update and restart (0.1.19), `hc server restart --fresh` replaces it (0.1.20); Q3/Q11: sends and notes wait for a person's unfinished draft, up to `draft_wait` = 60 s (0.1.20); Q9: `keep` "never re-runs", kept terminals come back as shells after a reboot (0.1.15), so resuming an agent is the client's job. Q7 (will the TUI move onto core) is open: herdr's last release is v0.9.5 (10-14), its master is active (11-19) | no (three of four answered by behaviour) |
| 3 | Claude, Codex and OpenCode report OSC 7501 inside hc, or hc gets a fallback | Claude: yes since 2.1.295 (#5073; a field report on 2.1.312 lists `idle`, `working`, `blocked` with `kind` permission/question/auth, `done`, `error`; a 11-04 comment says herdr-core answers the query). Two gaps in that report: the "trust this folder" dialog sends no record, and an unreachable API stays `working`. Codex 0.169.0 and OpenCode 2.2.0 (installed here): no evidence either way; `hc --skill` says Codex needs `--no-daemon` or its commands run outside hc. No fallback: "hc never infers status from the screen" | unknown for two of three: probe P1 |

### Against the needs list (`improve-architecture.md` A2; the 11-11 table read 0.1.13)

| # | Need | hc 0.1.21 | Changed since 0.1.13/14 |
|---|---|---|---|
| 1 | Instance identity | Partly. `server.status`: `core.id` ("permanent random id, reset only when state is wiped"), `pid`, `uptime_ms`; each holder has an `instance`. No id for one run of the core. Less needed than on herdr: a core restart or update keeps programs running | field names now read; no run id (upstream ask) |
| 2 | Relabel event | Yes: a `labels` event carries all labels, and the whole record when the terminal newly matches the subscriber's label filter. No rename: `name` is fixed at `run` | same |
| 3 | Env on start | Yes: `terminal.run` `env` ("override the core's environment and terminal identity"), `cwd`, `labels`, `keep`, `size` | `size`, `display` added (0.1.18) |
| 4 | Blocked respected | Yes: `terminal.send` refuses a blocked program unless `force` (error kind `blocked`), `if: idle\|working`, and waits for a person's draft | the draft wait (0.1.20); status clears when the agent exits back to the shell (0.1.20) |
| 5 | Stable fields | Yes by policy and by test; `hello` returns `generation`, `version`, `methods`, `features`; errors carry a stable `data.kind` | held across 7 releases (diff) |
| 6 | Registry not user-global | No plugins. `HC_SESSION` plus scratch `XDG_STATE_HOME`, `XDG_CONFIG_HOME` and `XDG_RUNTIME_DIR` give a separate core, state and config (`docs/security.md`, "Sessions": config and templates are shared across sessions unless the config home is scratch too) | documented |
| 7 | Idle cost | Not measured (P10). 0.1.11 "busy terminals use less CPU", 0.1.20 fixed a spin on a kept terminal | — |

### Other changes since 0.1.14 that shape the adapter (`CHANGELOG.md` at the tag)

- **Lifetime.** On macOS the core is its own app (Herdr Core.app, 0.1.18) started by a one-shot launchd job
  (0.1.17, 0.1.20): quitting Herdr or the launching app no longer ends terminals. `hc update` and `hc server restart`
  keep programs running; `hc server stop` ends them and parks kept terminals for the next start.
- **Loss and recovery.** Events `lost` and `restarted` (kept terminal: same record, fresh shell), reasons
  `holder_lost`, `signaled`, `server_stop`, `stopped_by_restart`, `spawn_pending`. During recovery `send`, `keys` and
  `attach` answer `unavailable`.
- **Events.** `seq` rises by one per event and restarts with each core process; `heartbeat` every 15 s; `missed`
  with a fresh snapshot for a slow subscriber; `foreground` events (pid, name, argv, cwd) about twice a second.
- **Reads.** `terminal.read` of a full-screen agent returns the screen at once and never scrolls it while the
  status is `working`, `blocked` or `error` (0.1.17).
- **Master after the tag:** "a second core leaves a running core's socket and programs alone (#200)" and eight
  smaller fixes. Nothing that changes the API's shape.

Seen here, read-only: a default-session core (pid 52026, 0.1.21, up since about 10:01 today, the time the `hc` link
was last updated), one live terminal `t1` (a shell) in an older holder. The plan never uses the default session.

## 2. The shape

### Second adapter, not a replacement

`project.terminal` picks the adapter (D1, D2, decided 11-11; the selector is `cmd/obor/terminal.go:21-29`). An hc
project and a herdr project can run on one machine: sockets, state and env names are separate. A project switches by
`obor stop`, the key, `obor start`; panes do not move, sessions do (they are obor's records).

Replacing herdr would delete far more (§3, Stop C), but it needs three things nobody has yet: a stated API freeze,
proof that all three kinds report status in hc, and the owner's yes to watching lanes in herdr-app or `hc attach`
instead of herdr's tabs. A second adapter gets the facts for that decision at a tenth of the cost.

### Who owns what on hc (D3, D3a)

The rule from 11-11 holds: **a pane's agent facts are the terminal's when it reports them, else obor's.**

| Agent-layer part | herdr (unchanged) | hc | Whose on hc |
|---|---|---|---|
| Status | screen detection | the program's OSC 7501 word, relayed: `status.state`, `status.kind` for blocked | hc's |
| Blocked (D3a) | herdr's detection | `status.state == "blocked"`; never the screen | hc's |
| Prompt | `agent.prompt` | `terminal.send` (refused before any byte when blocked; waits for a person's draft) | hc's |
| Wait | `agent.wait` | the adapter's own wait on the event stream (not `terminal.wait`: its "caller" and "same turn" rules are for agents calling the CLI) | adapter, on hc's events |
| Start | `agent.start` and the evidence rule | the command line written at the pane's shell; up when the foreground is no longer the shell and the program has reported (`reported`, or a status) | obor's launch plus the adapter |
| Which kind runs | detection | none in hc: the adapter labels the terminal with the kind at its start and reads it back while the foreground is not the shell; for an agent a person started, `status.app` if it names one (P1) | obor's |
| Session | `agent_session` | none: Claude's is named by obor, Codex's found by launch (both exist) | obor's |

Status words: hc's `idle`, `working`, `blocked`, `done` are the port's. `error` (a turn that ended in an error) maps
to `idle` with its `msg` logged, since the agent takes a prompt again; a null status while a program holds the
foreground is `unknown`. Whether `error` deserves its own word in the port is a later question (flagged).

**What this means for herdr's side: nothing now.** herdr keeps `agent.start`, `agent.prompt`, `agent.wait` and its
names, all inside `internal/herdr` after step 1. D3 option (b), herdr driven as a plain multiplexer, is not needed
for hc and is not planned; if herdr is later replaced, its agent protocol is deleted with the adapter instead.

### The port after step 1, as hc fills it

| Port | hc |
|---|---|
| `Pane.ID` | the address (`t7`), "this terminal forever on this core" |
| `Pane.Group` | a label `obor.project=<name>`; the subscription filters on it (key spelling after P6) |
| `Pane.Label` | a label `obor.agent=<name>`, set and changed by `terminal.label` |
| `Pane.Cwd` | `foreground.cwd`, which follows a `cd` (herdr reports no event for one) |
| `Snapshot.Instance` | `core.id` and the core's `pid` (P5 says whether anything better exists) |
| `Subscribe` | one `events.subscribe {labels}`: snapshot, then events applied to the adapter's own copy of each record, each sent on as the whole pane. A `missed` or a `seq` gap ends the stream as a refresh, 45 s without a heartbeat as a lost connection; the core subscribes again either way. No reread per event, no subscription per pane |
| `Screen` | `terminal.read {lines: 0}`, its last lines |
| `ProcessInfo` | `pid` (the shell) and `foreground` (the group leader only) |
| `Write` | `terminal.send {force: true}`: paste and Enter. hc has no call that types without Enter; no product caller passes `submit: false` (`internal/launch/stop.go:122` is the only one, and passes true) |
| `SendKeys` | `terminal.keys` (obor's key names equal hc's) |
| `Place` | `terminal.run` with no argv (the login shell), `cwd`, `env`, labels, `keep: true`, `no_parent`, `no_notify`. No layout: `--tab` and `--new-tab` are refused on hc with one line |
| `RenameProject` | relabel the project's terminals |
| `ResumeHint` | "" (hc never re-runs a program) |
| `Notify` | `NotShown` (HD6) |
| `Named` | `NotFound`: hc holds no agent names |

Transport: the unix socket, JSON-RPC 2.0, one message per line (`schema/README.md`, "Connecting"), standard library
only; a second connection for the event stream. Not the CLI. The socket path follows `paths.rs`: `HC_SOCKET`, else
`$XDG_RUNTIME_DIR/herdr-core/<session>.sock`, else the session's state dir; a `socket` key in hc's `config.toml` is
reported, not parsed, in the first adapter. Version rule: `hello` must answer generation 1 and list every method the
adapter calls; a version below 0.1.21 is refused with a line, a newer one is fine (`Snapshot.Note` only when a
method is missing). No exact pin.

## 3. Steps

Each spec lists every message and state file the step touches and what each becomes (the DD-v0.38 rule,
`BACKLOG.md:33`). No task ids here: the owner picks, the lead writes them.

### 0. hc probe (no product code)

- **Does.** One live run against a throwaway core: `HC_SESSION=obor-probe-<n>`, scratch `XDG_STATE_HOME`,
  `XDG_CONFIG_HOME`, `XDG_RUNTIME_DIR`, `HC_SOCKET` unset, the core started from a scrubbed env, the session stopped
  at the end. Never the default session. One fact per probe in `docs/research/hc-probes.md`.

| # | Probe | Decides |
|---|---|---|
| P1 | Claude 2.1.329, Codex 0.169.0 (with and without `--no-daemon`) and OpenCode 2.2.0 in a terminal: `reported`, each status word over a turn, `blocked` and its `kind` at a permission prompt, `status.app`, what the trust dialog shows | trigger 3, HD4, whether step 5 exists |
| P2 | `terminal.send`: time to an idle agent and to a shell, with nobody attached and with a viewer who typed a second ago; the refusal at blocked (kind, nothing typed); a send into a shell; text an earlier send left unsent in the input; a multi-line text; a 4 KB command line at a shell (herdr cut one at about 1 KB, `internal/launch/launch.go:850-856`) | `Prompt`, its `clear`, timeouts, HD5 |
| P3 | `terminal.keys` esc, ctrl+c, enter in a row: the delay per call | the stop's bounds (`port.StopBound`) |
| P4 | a new terminal's env when the core was started scrubbed, and when started from a shell carrying made-up `HERDR_PANE_ID` and `OBOR_ROLE`; whether `env: {"NAME": ""}` unsets or sets empty; `machine.exec` of `env` as a read of the core's env | the env guard (step 4) |
| P5 | holder killed: `lost`, then `restarted` for a kept terminal (address, labels, cwd kept?); `hc server restart`: programs survive, how the stream ends, `seq`; `hc server stop` then start: kept terminals as shells; what names one run of the core | `Instance`, the restore trigger |
| P6 | the events of shell → agent start → turn → exit to shell, in order; `missed` under a slow reader; the label filter and its `record`; which characters a label key takes | the stream mapping, label spelling |
| P7 | a terminal made through the socket: does it show in the herdr-app window with nobody acting, where, under what title; does a viewer attaching resize it | HD8 |
| P8 | a run with no `caller`: no parent, no notes typed anywhere, ends with nothing; an agent inside calling `hc run` | `no_parent`, the guard flag below |
| P9 | `pid` and `foreground` for shell → agent → a tool the agent runs | `ProcessInfo`, `AtShell`, `Busy` |
| P10 | core CPU with three agents idle for an hour | need 7 |
| P11 | the scratch session leaves the default core, its state and config untouched; `hc schema` with no core running | the test rule, the static tier |

- **Files.** `tests/conformance/live_hc.go` (`KIND=hc`, prints facts as `live_herdr.go` does); the note.
- **Tests.** None in the suite.
- **Deletes.** Nothing.
- **No-go** (stop, keep watching): Claude does not report `blocked` reliably inside hc; a send cannot be refused
  before it types; the scratch session touches the default one. Codex or OpenCode not reporting is not a no-go: it
  sizes step 5.

### 1. Split the start (`terminal-adapters.md` step 6, as written there)

- **Does.** herdr's start protocol leaves `internal/launch` for `internal/herdr`: the terminal's start becomes
  `AgentStarter.Start(ctx, AgentStart{Pane, Label, Kind, Argv, Env, Cwd, Prompt, Bound}) (Pane, error)`, returning
  once the agent runs, or an error matching `PaneBusy`, `NotReady` or `TimedOut`. launch keeps the work any
  multiplexer needs (the shell wait, the env line, the label, the startup-dialog read). One addition to the 11-11
  text: `AgentStart` carries `Env` and `Cwd` from the start, so an adapter that can pass them by field (hc under
  HD5's second option) needs no later port change.
- **Files.** `internal/launch/start.go:140-283` (`attempt`, `counts`, `launching`, `started`, `busyStart`) and
  `evidence.go` to `internal/herdr/start.go` (new); `internal/app/port/port.go:39-44` (`Launched`),
  `effects.go:85-87,94-101` (`WaitForOutput`, `Start`); `internal/launch/launch.go:784` (the env line's mark read
  through `Screen`); `internal/setup/agent.go` (its start call).
- **Deletes.** From the port: `Pane.Launched`, `PaneControl.Start(name, kind, pane, …)`, `WaitForOutput`. From
  launch: the evidence rule and every reading of herdr's reset record, about 150 lines.
- **Tests.** launch's start tests run on a small in-memory terminal instead of herdr's wire fake (8 files use
  `herdrtest` today); the evidence cases move whole to `internal/herdr` against `herdrtest`. No new behaviour, so no
  new test beyond the in-memory terminal's own.
- **Live.** `make conformance TIER=live TURN=1 KIND=<kind>` for claude, codex and opencode before and after, the
  summary lines in the report (the `AGENTS.md` rule for a task that touches a launch).
- **Risk.** This code holds the fixes of a long run of live start bugs: move it, change no rule.

**Stop A.** Steps 0 and 1 pay off with no hc code: the probe note replaces guesses, and launch no longer speaks
herdr's protocol. Stop here on a no-go.

### 2. `internal/hc`: the client, its fake, the read side

- **Does.** The socket client (`hello`, the version rule, errors by `data.kind` mapped to the port's predicates:
  `not_found` → `NotFound`, a refused dial → `NotRunning`, `unavailable` → an error that is neither). `port.Panes`:
  `Snapshot`, `Subscribe`, `Notify`. The reads of `PaneControl`: `Pane`, `Panes`, `Screen`, `ProcessInfo`. Not
  selectable yet.
- **Files.** `internal/hc/` (new: `client.go`, `panes.go`, `stream.go`, `contract.go`), `internal/hc/hctest/`
  (new), `internal/checks/boundary_test.go:52,59,121-125` (the adapter and its test help get their rows; only
  `cmd/obor/terminal.go` and `tests/conformance` may import `internal/hc`), `tests/budget.json`,
  `docs/module-map.md`.
- **Tests.** Added. `hctest.Core`, herdrtest's counterpart: an in-memory core that holds terminal records, answers
  the methods the adapter calls and sends events with `seq`, `heartbeat` and `missed`, on an in-memory connection
  (a unix socket only in an `_integration_test.go`). It is small because hc's wire is plain lines and has no
  layout; if it passes about 400 lines, say so in the report. Unit tests: the record-to-pane mapping on JSON
  captured by the probe; the stream's four endings (`missed`, a `seq` gap, a lost heartbeat, a closed socket). The
  schema check, twin of `internal/herdr/contract.go:7-47`: the methods, params and fields the adapter uses, checked
  against a committed copy of 0.1.21's `hc schema`.
- **Deletes.** Nothing.

### 3. `internal/hc`: the write side and the start

- **Does.** `Prompt` (a read first: a terminal with no status runs no agent and is refused as `NotFound`; then
  `terminal.send`, its `blocked` answer returned as the refusal with `began` false), `Write`, `SendKeys`,
  `WaitStatus` on the stream, `RenamePane`, `AgentStarter.Start` (the quoted command line sent to the shell, the
  kind label set, up by the rule in §2; a program in the foreground that has not reported within the bound is
  `NotReady`, so launch reads the screen for the trust dialog as it does today, `internal/launch/start.go:122-137`),
  `Place`, `RenameProject`, `ResumeHint`. `shellQuote` (`internal/launch/launch.go:868`) gets one owner both can
  reach.
- **Files.** `internal/hc/control.go`, `start.go`, `setup.go` (new); `internal/hc/hctest`;
  `internal/hc/terminal_contract_test.go` (new).
- **Tests.** Added: the terminal contract table (`internal/testkit/terminal.go:42-241`, 15 rows) passes on
  `hctest`, as it does for herdr on `herdrtest`; the `notify` row already accepts `NotShown`. One test per refusal
  the adapter decides itself (no agent, blocked, `--tab`).
- **Known gap, stated in the spec.** The read-then-send in `Prompt` is a check-then-act: an agent that exits in
  between gets the text at its shell. herdr closes that inside `agent.prompt`. An upstream ask (below) would close
  it on hc.
- **Deletes.** Nothing.

### 4. Make it selectable: config, identity, the env guard

- **Does.** `config.TerminalKinds` gains `hc`; the selector and `terminalEnv` gain their case (`HC_SOCKET`,
  `HC_SESSION`, the three `XDG_` names). The pane variable is picked by the project's terminal kind, not by
  whichever is set: one table in `internal/project/pane.go` (`herdr: HERDR_PANE_ID`, `hc: HC_TERMINAL_ID`), so an hc
  terminal that inherited a herdr pane's variable is never taken for that pane. The env guard: on an hc project,
  `obor setup check` and serve's first connect read the core's env (P4 names the call) and refuse with one line
  when it carries any `HERDR_` or `OBOR_` name: "hc's core was started from an obor pane: run `hc server restart
  --fresh` from a shell outside any pane". Serve, whose own env is launchd's, starts a core that is down with a
  scrubbed env (HD9). A kind change is a terminal change: the spec says what becomes of
  `.obor/state/terminal-instance` and of the identity table's rows for the old terminal's panes.
- **Files.** `internal/config/config.go:241-251`, `validate.go`; `cmd/obor/terminal.go`;
  `internal/project/pane.go:9-29`; `internal/setup` (the check row); `internal/app/restore.go:50-58`;
  `templates/agents/gotchas.md`, `skills/setup/SKILL.md` (what holds on any terminal, and a short part per
  terminal); `docs/design.md` "herdr integration" (lead-owned: the direction line reads for herdr only today).
- **Tests.** Added: the pane-variable table (each kind reads its own variable and ignores the other's), the guard's
  refusal, the config's acceptance of `hc`. Changed: `pane.go`'s tests.
- **Live.** `make conformance TIER=live TURN=1 KIND=<kind>` for each kind on an hc scratch session (it touches
  identity and how an agent is launched). The tier needs a way to name the terminal; the spec adds it.
- **Deletes.** Nothing in herdr's path.

### 5. Status for a kind that reports none (only if P1 finds one)

- **Does.** For such a kind on hc, the kind reports OSC 7501 itself rather than obor keeping a second status path:
  its hook or wrapper writes the record to its terminal at a turn's start and end and at a permission prompt
  (`Caps.Status` names the source, as `terminal-adapters.md` step 7 sketched). If a kind has no hook for a
  permission prompt, obor's prompt guard cannot protect it on hc: that kind is then not offered on an hc project,
  said by `obor config validate`.
- **Files.** `internal/agents/<kind>`, `kinds/opencode`, `hooks/hooks.json` only for Claude and only if P1 shows a
  gap there.
- **Tests and size.** After P1. This is the one step whose size is unknown.

### 6. A real project on hc

- **Does.** A scratch project, then obor-kit itself for a few days, with `project.terminal: "hc"`: init, a
  dispatch, a merge, a review; then the three restores: after `hc update` or `hc server restart` (nothing to do),
  after one holder's loss (that pane's agent restarted with its session), after `hc server stop` and start (every
  labelled pane restarted, as after a herdr restart today). The findings go to `docs/research/hc-probes.md`.
- **Tests.** Nightly gains one non-gating line: the committed schema against the installed `hc schema` (the static
  tier), so drift after an hc update shows the next morning. The live tier stays by hand, run by a task that touches
  the adapter or a launch and when serve sees hc's version change.
- **Deletes.** Nothing.

**Stop B.** hc is a terminal a project can choose, marked experimental. Adopters are told by `obor send` and the
release notes; nobody is moved.

**Stop C: replace herdr (not planned; listed so the owner sees what it buys).** Not before: a stated freeze or 1.0
upstream; every kind's live tier green on hc across two hc releases; obor-kit and one adopter on hc for a release
cycle; the owner's yes to HD8. It would delete `internal/herdr` (1,911 lines) and `herdrtest` (1,472),
`tests/conformance/live_herdr.go`, the exact protocol check, the subscription per pane and the reread per event
(`internal/herdr/socket.go:888-1180`), the tab and split rules (`setup.go:55-300`) with `--tab` and `--new-tab`,
`ResumeHint`, the socket-time instance, the `HERDR_PANE_ID` fallback and herdr's start protocol whole. Its real
cost is not the adapter: 37 test files in `cmd/obor`, `internal/app`, `internal/launch`, `internal/setup` and
`internal/guard` use herdr's wire fake as "the terminal" and would have to move to a port-level fake first.

### Order

0 → 1 → 2 → 3 → 4 → 6; 5 between 4 and 6 if it exists. 0 and 1 are independent and can run together. 2 and 3
need 1's port and 0's captures. Nothing here blocks or needs the Rex steps (8, 9) of `terminal-adapters.md`.

## 4. Risks

1. **hc is a preview.** One vendor, 0.1.x, no roadmap, and the vendor also ships the terminal obor runs on today.
   What bounds it: herdr stays the default; the API's additive rule has a test upstream and held from 0.1.14 to
   0.1.21; `hello`'s method list is checked at every connect; the nightly schema line shows drift. What does not:
   behaviour changes that keep the schema (0.1.20 changed when a send is delivered; 0.1.19 changed what ends with a
   terminal). The live tier exists for those and should run on each hc version change. herdr-app updates the core
   by itself; for obor that is a socket drop and a reconnect.
2. **Env leaking from a herdr pane into hc terminals: still real at 0.1.21.** When the CLI starts a core, the
   launchd launcher captures the caller's whole environment for it (`platform/macos_launchd.rs:26-57`), and a new
   terminal gets the core's environment minus only the terminal-identity names and `HC_TERMINAL_ID`
   (`api/run.rs:228-250`). Update and restart keep that environment (0.1.19); only `hc server restart --fresh`
   from a clean shell, or a stop and start, replaces it. So one `hc` call from an obor pane while no core runs puts
   `HERDR_PANE_ID`, `OBOR_PANE` and `OBOR_ROLE` into every later terminal. The plan's answers: the pane variable by
   project kind and the guard (step 4), serve starting the core scrubbed (HD9), and obor's own starts, which set
   and unset every `OBOR_` name (`internal/launch/launch.go:826-847`). The core running here: `ps` shows only
   launchd's ten variables, but that is the block it was exec'd with, not the transferred one, so it proves
   nothing; P4's read does. The owner should check it before the first real use.
3. **Upgrade safety for adopters.** No change without the key. A kit older than step 4 refuses `terminal: "hc"`
   by name (`cmd/obor/terminal.go:25-26`), which is the right failure. The key is committed (D2), so a second
   person on the project needs hc installed; the per-machine override D2 deferred becomes real then. Switching a
   project leaves its old panes running under the old terminal until closed, and leaves records keyed by the old
   pane ids (step 4's spec). hc below 0.1.21 is refused with a line that names `hc update`.
4. **Serve restart and restore.** Better than herdr in the common case and different in the rare ones. A core
   restart or update keeps every agent: the stream ends, serve reconnects, the restore finds nothing to do (on
   herdr the same event takes every agent down, `BACKLOG.md:50`). A lost holder gives that one terminal a fresh
   shell (`restarted`): the adapter ends the stream as a lost connection so the existing restore runs once and
   restarts that pane, with no new rule in the core (the spec confirms `internal/app/restore.go:19-23` reads it so).
   A reboot or `hc server stop` brings kept terminals back as shells: the instance differs at serve's first connect
   and the restore runs as it does after a herdr restart. Weak points: the instance is a pid (a reused pid after a
   reboot would skip one restore; `obor restart` or the next dispatch still starts the agent); every obor pane must
   be kept (`Place` sets it, and labelling a pane a person made sets it too); a serve re-exec sees the same
   instance and restores nothing, as today.
5. **Delays built into hc's typing.** A send waits for a person's draft (up to 60 s) and for a quiet pause; keys
   wait for the pause too. obor's wake and stop bounds were tuned on herdr. P2 and P3 measure; the spec of step 3
   sets the timeouts from them.
6. **The view.** hc lays nothing out and shows nothing. Lanes are watched in herdr-app or by `hc attach`; obor's
   status page and CLI are unchanged. If P7 says a person must add each terminal to the window by hand, HD8 is the
   decision that stops the plan at Stop A.

## Owner decisions

| # | Decision | Recommended | Blocks |
|---|---|---|---|
| HD1 | Start now although the 10-30 triggers are not all met | Yes, bounded: steps 0 and 1 first, both useful without hc | everything |
| HD2 | Shape: a second adapter under `project.terminal: "hc"` with herdr the default, or a replacement | Second adapter; replacement only at Stop C's criteria | step 4 |
| HD3 | Agent layer on hc (D3 for hc): hc's status, send and events as the terminal's facts, obor's kind, session and start proof; herdr unchanged; the week-long comparison on herdr dropped | Yes | steps 3, 5 |
| HD4 | Blocked on hc (D3a): OSC 7501 as hc relays it, never the screen; a kind that reports nothing is made to report, or is not offered on hc | Yes, after P1 | step 5 |
| HD5 | Pane model (D5's twin): a kept shell terminal with the start written at its shell, or the agent as the terminal's own program (`terminal.run` argv, env and cwd by field: no env line, no shell wait, hc's own kill; but a new pane id per restart, no home pane, a new tile in the window each time) | The shell model first; the other only if P2 or use shows the typed start is the weak spot | step 3 |
| HD6 | Notifications (D6): hc has none | `NotShown` at first; an OS notifier behind `Notify` if toasts are missed | step 2 |
| HD7 | Experimental status (D7's twin): live tier does not gate, hc ≥ 0.1.21, no exact pin | Yes | steps 4, 6 |
| HD8 | The view: herdr-app or `hc attach`, no tabs or splits made by obor, `--tab` and `--new-tab` refused on hc | Yes, after P7 | Stop A |
| HD9 | Who starts the core: serve, from a scrubbed env, when it is down; obor never restarts or stops a running core | Yes | step 4 |

## Flagged, not expanded

- **The guard has no rule for `hc`.** Inside an hc terminal any process can run, kill, read and type into every
  other terminal of the session, and `hc --skill` teaches agents to delegate with `hc run`. On an hc project that
  is a way around dispatch and around the role's write limits. A guard rule is needed before a worker runs there.
- **`BACKLOG.md:42-45`** still says "spike behind `app.PaneControl` when…"; this plan supersedes it.
- **`docs/design.md:727-730`** ("herdr is the source of agent state") reads as a rule for every terminal; with
  step 4 it is herdr's and hc's own statuses that are the source. Lead-owned.
- **`port.Write`'s `submit`** has no caller that passes false; dropping it would remove a case hc cannot do.
- **Upstream asks** worth one discussion post: an id for one run of the core in `server.status`; a send that is
  refused unless a program reports a status (it closes step 3's gap); a send without Enter. #5684 is still open.
- **macOS privacy prompts** belong to Herdr Core.app (0.1.18): the first network or microphone use by a program in
  a terminal can raise a dialog nobody sees on a headless start.
- **hc's headless size is 200×120.** launch's startup-dialog patterns were written against herdr's pane sizes;
  step 6 should watch for a pattern that no longer matches.
- **herdr has had no release since v0.9.5 (10-14)** while hc had nine; its master is active. Worth watching with
  Q7.
