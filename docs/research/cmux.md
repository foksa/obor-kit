# cmux as obor's next terminal (researcher, 2026-11-26)

Brief (lead, owner 11-26: "with osc 7501, we can add cmux support next... it supports the same protocol"): what cmux
is and how obor would drive it; what it does with OSC 7501 and whether obor can read an agent's status per pane; the
port mapped method by method; identity and environment; gaps, risks, the adapter's task list and what to ask upstream.

- Installed: `/Applications/cmux.app`, `cmux 0.65.2 (113) [12023d89f]` (`Resources/bin/cmux version`). Commit
  `12023d89f` is the tag **v0.65.2.2** (2026-10-27, the latest release per the GitHub API), although the bundle's
  `CFBundleShortVersionString` says 0.65.2. The source I read is the **v0.65.2** tarball (commit `9d01f956`, a
  codeload download into the session scratchpad); v0.65.2.2 is two point releases later, so small differences are
  possible. Line references below are `cmux@v0.65.2:<path>`, i.e.
  `https://github.com/manaflow-ai/cmux/blob/v0.65.2/<path>`.
- What was done: read the bundle (file list, `Info.plist`, `codesign`, `strings`), ran the bundled CLI's offline
  commands (`--help`, `help <topic>`, `<cmd> --help`, `version`, `docs api`, `--skill`, `socket-status --json`, none of
  which connects to the socket per `docs/cli-contract.md:15-17`), read the v0.65.2 source and docs, and listed the
  variable **names** (never values) of the running app's environment with `ps -E`.
- What was not done: **no call to the owner's cmux socket at all**, not even a read-only one. The socket is in
  `cmuxOnly` mode (below), so a call from this herdr pane would be refused anyway, and a call from inside a cmux pane
  would mean using the owner's windows. No cmux window, workspace or surface was created, focused or typed into. No
  agent ran under cmux. Every behaviour claim is from cmux's source, docs or help text unless marked **verified**.
  Main-branch docs could be compared only once (GitHub rate-limited this session with HTTP 429).

## Answer first

1. **cmux is a herdr-like terminal, not a Rex-like one: a multiplexer with its own agent layer on top.** It is a
   native macOS app (Swift/AppKit on libghostty, GPL-3.0-or-later, open source at `github.com/manaflow-ai/cmux`)
   with windows → workspaces (sidebar entries, optionally in workspace groups) → split panes → surfaces (tabs inside
   a pane). Its control surface is a **bundled CLI** (`cmux.app/Contents/Resources/bin/cmux`, put on `PATH` inside cmux
   panes only) over a **Unix socket** (`~/.local/state/cmux/cmux.sock`) speaking a newline-delimited **JSON v2**
   protocol (`{"id","method","params"}`), with a legacy v1 line protocol beside it. It has almost everything a
   multiplexer port needs: create workspaces/splits/surfaces without focus (`--focus false` is the default for
   splits), with a cwd, an initial command and a **per-surface startup environment**; send text and keys; read a
   surface's screen and scrollback; rename; per-surface tty and process trees; an **ordered, resumable event stream**
   (`events.stream`, `seq` + `boot_id`); native notifications. On top it runs its own agent layer: PATH wrappers for
   `claude`, `codex`, `agy` and others that inject hooks, an agent lifecycle per surface, an `input_state` dialog/draft
   check before typing, agent hibernation, auto-resume and session restore.
2. **OSC 7501: cmux consumes it (spec rev 0.3 per its docs) and answers the query, but obor cannot read back what it
   needs.** Each surface keeps its records (ids, `app` inheritance, progress) and the sidebar shows the most urgent
   one. A client gets them **only** as `program_status.changed` events, and those carry **only `id`, `state`, `kind`,
   `progress`, `updated_seq`: `app`, `msg` and `title` are left out on purpose**, and **there is no snapshot query**
   (`cmux@v0.65.2:docs/events.md:355-375`; same text on main on 11-26). So obor gets the state word and the blocked
   kind per surface, live, but not the agent's kind (`app`) or its note (`msg`), and after a reconnect it knows
   nothing until the next report. cmux also drops `done`/`error` when the user **focuses** the pane, and
   `working`/`blocked` at the next prompt or process exit.
3. **The port maps well except status.** Pane reads, placement, write, keys, screen, rename, close, notify and the
   stream all have a v2 method. The gaps are status (2 above), no `cwd`/process change events (poll), no free
   key=value labels (the surface title is the only label), and the socket's default trust mode (4 below).
4. **Identity is clean; access is not.** Every cmux pane carries `CMUX_SURFACE_ID` (= `CMUX_PANEL_ID`),
   `CMUX_WORKSPACE_ID` (= `CMUX_TAB_ID`), `CMUX_TERMINAL_LIFECYCLE_ID` and `CMUX_SOCKET_PATH`, protected from
   override; `TERM_PROGRAM` is forced to `ghostty`, so detection must key on `CMUX_SURFACE_ID`. The app is launched
   by LaunchServices and its environment holds only launchd's variables (**verified** by name, below), so the Rex
   leak (a server started from an obor pane) does not happen in normal use. **But the socket's default mode,
   `cmuxOnly`, admits only processes descended from the cmux app** (**verified**: `socket-status` says
   `effective_mode: cmuxOnly`). obor serve runs as a LaunchAgent (`internal/service/service.go:1-3`), so it would be
   refused; the owner has to switch cmux to `automation` (any process of the same user) or `password` mode.
5. **Recommendation: not next as a plain "same protocol" adapter.** cmux's 7501 relay was built for its sidebar, not
   for clients, so on today's cmux obor would need its own status source again (its hooks, D3a's first choice) or the
   screen. Ask upstream first for (a) a per-surface program-status snapshot query and (b) `app`/`msg` in it for local
   authorised clients; both are small and in line with how cmux already guards the stream. With those two, an
   adapter is about the size of the Rex one. Also weigh cmux's own agent layer (hibernation, auto-resume, hook
   injection) acting on obor's agents: it has to be switched off for obor's panes, and how to do that per pane is
   not documented.

## 1. What cmux is, and its control surface

| | Fact | Source |
|---|---|---|
| Product | "cmux - control cmux via Unix socket"; a macOS terminal built on libghostty (`ghostty` binary and terminfo bundled, `Resources/ghostty`), with a browser surface, iOS/cloud features, agent integrations | `cmux --help`; bundle listing |
| Licence | GPL-3.0-or-later for the macOS app, CLI and cmux-tui; BUSL-1.1 only for server code (`web/`, some workers); by Manaflow, Inc. | `Resources/LICENSE` (bundle) |
| Source | `https://github.com/manaflow-ai/cmux`, tags `v0.65.0`…`v0.65.2.2`; main pushed daily (last push 2026-11-26) | `git ls-remote --tags`, GitHub API |
| Updates | Sparkle, feed `https://github.com/manaflow-ai/cmux/releases/latest/download/appcast.xml` | `Info.plist` `SUFeedURL` |
| Sandbox | **Not sandboxed**: no `com.apple.security.app-sandbox` entitlement; Developer ID, team `7WLXT3NR37` | `codesign -d --entitlements -` |
| CLI | **In the bundle**: `Contents/Resources/bin/cmux` (arm64 Mach-O, 24 MB). Not on `PATH` outside cmux; inside a cmux pane cmux prepends `Resources/bin` to `PATH` | `file`; `cmux@v0.65.2:Packages/macOS/CmuxTerminal/Sources/CmuxTerminal/Surface/TerminalSurface+RuntimeSurfaceCreation.swift:200-212` |
| Socket | `~/.local/state/cmux/cmux.sock` (override `CMUX_SOCKET_PATH`, `--socket`). Mode `0600`, owner-only. **Present here** | `cmux --help` "Environment"; `ls -la ~/.local/state/cmux` |
| Protocol | v2: one JSON request per line `{"id","method","params"}`, one JSON reply per line; v1: space-delimited line commands, kept for compatibility. `cmux rpc <method> [json]` sends any raw v2 call | `cmux@v0.65.2:docs/v2-api-migration.md:1-25`; `docs/events.md:44-52`; `cmux rpc --help` |
| Handles | UUIDs, short refs (`window:1`, `workspace:2`, `pane:3`, `surface:4`) or indexes; `--id-format uuids\|both` | `cmux --help` "Targets" |
| Method list | `system.capabilities` (`cmux capabilities`) lists the methods at runtime; there is no per-method parameter schema like Rex's `api describe` | `cmux@v0.65.2:Sources/TerminalController+Capabilities.swift`; `docs/cli-contract.md:324` |
| Admission | A command never blocks others: `overloaded` (retryable, `retry_after_ms`) or `timeout` (main thread hop > 10 s, `retryable` says whether it ran) | `cmux@v0.65.2:docs/cli-contract.md:851-866` |
| Docs | `https://cmux.com/docs/api`; `docs/cli-contract.md`, `skills/cmux/SKILL.md`, `docs/events.md`, `docs/program-status.md` in the repo | `cmux docs api` |

### The nouns and the API obor would use (v2 method names from `system.capabilities`' list in source)

| Need | v2 method / CLI | Notes |
|---|---|---|
| Layout tree | `system.tree` (`cmux tree --all`) | windows → workspaces → panes → surfaces; a surface node has `title`, `pane_id`, `tty`, focus/selection flags (`cmux@v0.65.2:Packages/macOS/CmuxControlSocket/Sources/CmuxControlSocket/Coordinator/System/ControlSystemTreeSurfaceNode.swift`) |
| List | `workspace.list`, `pane.list`, `pane.surfaces`, `surface.list`, `window.list`, `workspace.group.list` | `surface.list` gives `title`, `pane_id`, `requested_working_directory`, `initial_command`, `is_terminal`, `resume_binding` (`…/Surface/ControlSurfaceSummary.swift`): the **requested** cwd, not the live one |
| Processes | `system.top` (`cmux top --processes --json`) | per surface: `foreground_pgids` and a process tree (`cmux@v0.65.2:Sources/TerminalControllerTopSupport.swift:198-247`); the exact process fields (cwd, argv) not read |
| Live cwd | shell integration reports it (`surface.report_pwd`); exposed per workspace as `cwd`/`focused_cwd` in v1 `sidebar_state` | per surface: **not found** as a query (unverified that none exists) |
| Create | `workspace.create` (`cwd`, `workspace_env`, `initial_input`, `focus`, `group`), `surface.split` (`direction`, `working_directory`, `initial_command`, `initial_input`, `startup_environment`, `focus`), `pane.create`, `surface.create` (`pane_id`, same options) | `initial_command` **replaces** the shell with a one-shot process, `initial_input` types into the shell (`cmux@v0.65.2:docs/cli-contract.md:596-620`); env param names `startup_environment` / `initial_env` (`…/Surface/ControlCommandCoordinator+Surface.swift:348,516`, `…/Pane/ControlCommandCoordinator+Pane.swift:334`); split's CLI default `--focus false` |
| Type | `surface.send_text`, `surface.send_key`, `terminal.paste` (bracketed), `terminal.input` | errors: `input_queue_full`, `surface_unavailable`, `process_exited`, `unknown key` (`…/Surface/ControlSurfaceSendResolution.swift`). Key names: `enter`, `ctrl+c` in the help; the rest not read |
| Guard | `surface.input_state` | `agent` (cmux tracks an agent there), `lifecycle`, `waiting_on_human`, `state` = `empty`/`draft`/`dialog`/`unknown` (from the screen), `blocks_typing`. The CLI's `send`/`send-key` refuse unless `--force`; the **socket methods themselves do not check** (`cmux@v0.65.2:Sources/TerminalController+SurfaceInputState.swift`; `CLI/CMUXCLI+Paste.swift:325-370`) |
| Read | `surface.read_text` (`cmux read-screen --lines N --scrollback`) | plain text; whether long lines come unwrapped: unknown |
| Label | `surface.action` rename (`cmux rename-tab --surface … <title>`), `workspace.rename`, `workspace.group.rename` | no free key=value metadata on a surface |
| Close | `surface.close`, `workspace.close` | `surface.close` takes `force` |
| Focus | `surface.focus`, `pane.focus`, `workspace.select` | none needed by obor |
| Notify | `notification.create`, `notification.create_for_surface` | native; `cmux notify` |
| Identity | `system.identify` (`cmux identify --json`), `boot_id` in every event frame (changes per app process) | |
| Events | `events.stream` (`cmux events`) | see below |

### Events (`cmux@v0.65.2:docs/events.md`)

- One connection, request `{"method":"events.stream","params":{"after_seq":…,"categories":[…],"names":[…]}}`; the
  connection is then the stream. First frame `ack` with `boot_id`, `resume.{after_seq,oldest_seq,latest_seq,gap}`;
  then replay, then live events and heartbeats (15 s). Every event has a process-local, monotonically increasing
  `seq`, `boot_id`, `name`, `category`, `workspace_id`, `surface_id`, `pane_id`, `payload`. A gap (`resume.gap`) or
  `slow_consumer` means: refresh by snapshot queries. All events are also appended to `~/.cmuxterm/events.jsonl`.
- Names obor would use: `surface.created/closed/moved/action`, `pane.created/closed/…`,
  `workspace.created/closed/renamed/moved`, `program_status.changed`, plus `agent.*` (cmux's own agent layer).
- Missing for obor: no event for a cwd change, a foreground process change or a child exit (Rex has all three), and a
  rename arrives only as a generic `surface.action`.

### Socket access (`cmux socket-status --json`, **verified**; `cmux@v0.65.2:docs/configuration.md:7-13`)

`{"configured_mode":"cmuxOnly","effective_mode":"cmuxOnly","socket_path":"/Users/…/.local/state/cmux/cmux.sock"}`.
Modes: `off`, `cmuxOnly` (default: the peer's pid must descend from the cmux app; "programs started from cmux
terminals"), `automation` and `password` (same UID; password from `--password`, `CMUX_SOCKET_PASSWORD` or Settings),
`allowAll` (unsafe). A managed profile key can force `cmuxOnly`/`off`
(`cmux@v0.65.2:Packages/macOS/CmuxControlSocket/Sources/CmuxControlSocket/Transport/SocketClientAuthorization.swift:60-120`).
A `CMUX_SOCKET_CAPABILITY` token envelope exists as a "seam for terminals whose process trees are later reparented",
but cmux does not put it in a normal pane's env (not in the managed key list below).

## 2. OSC 7501 in cmux

| Question | Answer | Source |
|---|---|---|
| Revision | Its doc says **rev 0.3**. Added in 0.65.1 (2026-10-12), PRs #18742, #19091 | `cmux@v0.65.2:docs/program-status.md:3-4`; `CHANGELOG.md:70` |
| Parser | libghostty's (`terminal.osc.parsers.program_status.Report`, "invalid OSC 7501 report", "OSC 7501 sequence too long" in the app binary) | `strings MacOS/cmux` (**verified**) |
| Query | Answered: "cmux answers with the same bytes" (`]7501;?` in the binary, no `states`/`kinds` lists, which the spec reads as the original five and three). So Claude and agy, which wait for the reply, will report | `docs/program-status.md:15-16`; `strings` |
| Records | kept per surface (`programStatusStoresByPanelId`), with `id`s, `app` inherited from the nearest parent, `progress`, `title`, `msg` | `cmux@v0.65.2:Sources/Workspace+ProgramStatus.swift:122-175` |
| Shown | the most urgent non-idle record per surface (blocked > error > working > done) as a sidebar row "app · msg-or-title-or-label · N%"; the workspace row shows the most urgent pane's. `idle` is kept, not shown | same |
| Clearing | `working`/`blocked` dropped at the next prompt (OSC 133 A or cmux's shell integration) and when the process exits (`transient_drop`); **`done`/`error` dropped when the user focuses the pane (`dismissed`)**; RIS clears all; a closed or moved surface `removed`/`adopted` | `docs/program-status.md:24-30`; `docs/events.md:360-370` |
| Relayed to clients | `program_status.changed` event: `event` (`report`, `clear`, `prompt_start`, `transient_drop`, `dismissed`, `removed`, `adopted`), `id`, `record`, `records` (chunks of ≤ 32, `snapshot_id`/`snapshot_index`/`snapshot_count`), `records_total`. **Each record: `id`, `state`, `kind`, `progress`, `updated_seq` only.** "free-form OSC text and provider names are intentionally omitted from the user-facing stream and persistent event log" | `docs/events.md:341-375`; `cmux@v0.65.2:Sources/CmuxEventPublishing.swift:40-110` |
| Snapshot | "cmux does not currently provide a dedicated program-status snapshot query" (v0.65.2 and main on 11-26) | `docs/events.md:358-359` |
| Other readers | the v1 `sidebar_state` dumps a **workspace's** status entries, which include the winning `program_status` row text (app · msg); per surface it is not exposed | `cmux@v0.65.2:Packages/macOS/CmuxControlSocket/…/Sidebar/ControlCommandCoordinator+SidebarReportsV1.swift:426-480` |
| Effect on cmux's agent layer | none, except: a `claude-code` record ending while cmux's Claude row says Running, with no hook for 3 s, settles the row to Idle | `docs/program-status.md:32-42` |

**What obor can read back per pane** (compare hc: `state`, `kind`, `msg`, `app`, `internal/hc/panes.go:32-38`; Rex:
the same and `owner.pid`, `docs/research/rex-probes.md`):

| Field | hc | Rex | cmux |
|---|---|---|---|
| state word | yes | yes | **yes, by event only** |
| blocked kind | yes | yes | **yes, by event only** |
| `app` (the agent kind) | yes | yes | **no** |
| `msg` (the port's `Note`) | yes | yes | **no** (workspace-level text only, via v1 `sidebar_state`) |
| a snapshot after (re)connect | `terminal.list` | `program_status` method | **none**; replay of retained events, or `~/.cmuxterm/events.jsonl` since the last `boot_id` |
| owner pid of the record | no | yes | no |

Consequences for obor: the guard (`port.GuardPrompts`) can refuse `blocked` from events; `Pane.Agent` cannot come
from 7501 (obor must name the kind itself, as hc's `obor.kind` label fallback does, `internal/hc/panes.go:70`); a
done that the owner looks at turns into "no record", which obor must read as "idle"; and serve's restart or a stream
gap leaves every pane `unknown` until its agent next reports. Not verified live: whether the retained replay covers
a serve restart in practice (retention size not read), and the reply bytes to the query.

## 3. obor's terminal port mapped to cmux

The port is `internal/app/port/terminal.go` (`PaneControl`, `AgentStarter`, `TerminalSetup`, `Adopter`) and
`internal/app/port/port.go` (`Pane`, `Snapshot`, `Event`, `Stream`, `Panes`). Proposed mapping of nouns, following
herdr's: obor's **group** (herdr workspace, Rex session) = a cmux **workspace group** or a **workspace**; herdr's
**tab** (Rex window) = a cmux **workspace**; obor's **pane** = a cmux **surface** (one terminal surface per cmux pane).
`app/layout` would ask per workspace for room as it does per tab.

| Port method | cmux | Have / missing / workaround |
|---|---|---|
| `Panes.Snapshot` | `system.tree --all` + `system.top --processes` + `surface.list` | **have** panes, titles, ttys, processes; **workaround** for live cwd (foreground process cwd from `top`, if its tree carries cwd: unread) and status (last `program_status.changed` per surface, kept by the stream) |
| `Snapshot.Instance` | `boot_id` / `system.identify` | **have** (a new app process = new `boot_id`) |
| `Snapshot.PaneRuns`, `Pane.Run` | surface UUIDs survive a restore (`TerminalSurface+StartupEnvironment.swift:14-27`), the shell does not | **workaround**: Run = the surface's tty or shell pid (`system.tree` `tty`, `top` root pid); `CMUX_TERMINAL_LIFECYCLE_ID` looks like the native answer but no query returns it (unverified) |
| `Panes.Subscribe` | `events.stream` with `after_seq`, `categories: [workspace, pane, surface, terminal]` | **have**, ordered, resumable; gap → `Refreshed` |
| `Panes.Notify` | `notification.create_for_surface` | **have** (native) |
| `Pane` / `Panes` (PaneControl) | as Snapshot, one surface | **have**; Agent/Status/Note see §2 |
| `Pane.Agent` | 7501 `app` withheld | **missing**; workaround: the program in the foreground (argv0 from `top`) or the kind obor launched |
| `Pane.Status` | `program_status.changed` | **have, event-only**; done→dismissed on focus; no snapshot |
| `Pane.Note` | `msg` withheld | **missing** |
| `ProcessInfo` | `system.top --processes` (`foreground_pgids`, tree) | **have** in substance; field names unread |
| `Screen` | `surface.read_text` (lines) | **have** |
| `Recent` | `surface.read_text` with scrollback | **have**; unwrapping unknown (the port allows Screen's text) |
| `WaitStatus` | events | **workaround**: wait on the stream (or poll `read_text`), as Rex does |
| `RenamePane` | `surface.action` rename | **have**; whether a program's own OSC title later overrides a custom title: unverified |
| `Write` | `surface.send_text` / `terminal.paste` + `send_key enter` | **have** |
| `SendKeys` | `surface.send_key` | **have**; key-name table to write (`enter`, `ctrl+c` documented; `esc`, `ctrl+u`, digits unread) |
| `Prompt` | none (cmux's guard lives in its CLI) | **workaround**: `GuardPrompts` + `surface.input_state` (`blocks_typing`, `dialog`) as an extra check |
| `Named` | none | **workaround**: by title (obor's label), as on Rex |
| `AgentStarter.Start` | `surface.split`/`surface.create` with `startup_environment`, `working_directory`, then type at the shell (`initial_input` or `send_text`) | **have**; the shellpane starter Rex uses fits. Env goes in at spawn, so no typed env line is needed |
| `TerminalSetup.Agents` | snapshot filtered | **have** with the Agent caveat |
| `Place` | `workspace.group.*`, `workspace.create` (`group`, `cwd`, `workspace_env`, `focus:false`), `surface.split` (`direction`, `focus:false`) | **have**; no-focus creation documented, not observed |
| `ResumeHint` | cmux restores sessions and can auto-resume agents (`surface.resume.*`, "Auto-Resume Agents") | **have** a counterpart: a line telling the owner to switch auto-resume off for obor's panes, if it can be scoped |
| `RenameProject` | `workspace.group.rename` / `workspace.rename` + `surface.action` rename | **have** |
| `CloseProject` | `surface.close`, `workspace.close` | **have** |
| `TakeLead`, `LeadSpace` | `system.identify` with the caller's `CMUX_SURFACE_ID`, `workspace.group.create/add` | **have** |
| `Adopter.Adopt` | n/a: every cmux surface is in a window (the app owns the ptys) | not needed |
| `Terminal.Check` | `system.ping` + `socket-status --json` (offline, says the mode) | **have**; Check must refuse `cmuxOnly`/`off` with the fix in the line |

Transport: Rex's adapter shells out to the `rex` CLI (D4). For cmux the v2 socket is plain newline JSON over a Unix
socket, so a stdlib `net` client is simpler than spawning `cmux rpc` (~24 MB binary per call) and needs no
dependency; `events.stream` takes over its connection, so the stream uses a second one. Password authentication on
the v2 wire was not read (unknown).

## 4. Identity and environment

**What a cmux pane carries** (`cmux@v0.65.2:Packages/macOS/CmuxTerminal/Sources/CmuxTerminal/Spawn/TerminalSurface+StartupEnvironment.swift:60-100`,
`…/Surface/TerminalSurface+RuntimeSurfaceCreation.swift:200-265`), all protected from workspace or per-surface
overrides:

| Variable | Value |
|---|---|
| `CMUX_SURFACE_ID`, `CMUX_PANEL_ID` | the surface UUID (obor's pane id) |
| `CMUX_WORKSPACE_ID`, `CMUX_TAB_ID` | the workspace UUID |
| `CMUX_TERMINAL_LIFECYCLE_ID` | a UUID per terminal lifecycle |
| `CMUX_SOCKET_PATH` | the socket (`CMUX_SOCKET` is set empty) |
| `CMUX_HISTORY_FILE`, `CMUX_SHELL_INTEGRATION(_DIR)`, `CMUX_NO_GIT_WATCH`, `CMUX_NO_PR_WATCH`, `CMUX_CLAUDE_INTEGRATION_DISABLED`, `CMUX_AGENT_COMMAND_SHIM_ROOT`, `GHOSTTY_BIN` | cmux's integrations |
| `TERM=xterm-256color`, `COLORTERM=truecolor`, **`TERM_PROGRAM=ghostty`** | forced |
| `PATH` | `Resources/bin` and the agent shim directory **prepended** |

**How `obor init` would detect it:** `CMUX_SURFACE_ID` set (and `CMUX_SOCKET_PATH`), the way `REX_BLOCK` and
`HC_TERMINAL_ID` are read (`internal/config/config.go:255-256`). Never `TERM_PROGRAM`: it says `ghostty`, the same as
plain Ghostty. The pane variable is `CMUX_SURFACE_ID`; it would join `project.identityNames`
(`internal/project/identity.go:19-31`) as the cmux kind's own, with `CMUX_WORKSPACE_ID`, `CMUX_PANEL_ID`,
`CMUX_TAB_ID` and `CMUX_TERMINAL_LIFECYCLE_ID` (a herdr or hc server started from a cmux pane would otherwise hand
them to its panes; cmux's agent wrappers key on `CMUX_SURFACE_ID`).

**Do panes inherit the server's env (the Rex problem)?** Panes inherit the **app process's** environment plus the
managed keys above (libghostty spawns through `/usr/bin/login`; **verified**: the app's children are `login`
processes). The running app (pid 1240) holds only launchd's variables (**verified**, names only: `COMMAND_MODE HOME
LOGNAME LaunchInstanceID OSLogRateLimit PATH SECURITYSESSIONID SHELL SSH_AUTH_SOCK TMPDIR USER XPC_FLAGS
XPC_SERVICE_NAME __CFBundleIdentifier __CF_USER_TEXT_ENCODING`). So in normal use (Dock, Finder, `open`, `cmux
<path>` which goes "through the app's file-open path") no `OBOR_*`/`HERDR_*` leaks. It would leak only if someone ran
`Contents/MacOS/cmux` directly from a shell: unverified, but the adapter can refuse that the way hc refuses a core
holding a foreign identity, by checking one probe surface's env, or simply pass `startup_environment` with the foreign
names emptied. A pane's own environment could not be read with `ps -E` (the shells under `login` show none), so the
table above is from source, not observed.

**Another inheritance that matters: cmux's agent wrappers.** obor starts an agent by typing its program's bare name
(`agents.Programs`, `internal/agents/agents.go:982-988`). In a cmux pane `claude`, `codex`, `agy` (and amp, pi,
hermes) resolve to cmux's wrappers first. The Claude wrapper (bash, `Resources/bin/cmux-claude-wrapper`) adds
`--settings <cmux hooks>` and, unless the command already has one, `--session-id` (lines 4-6, 1815, 2444-2446);
the hooks report to cmux (`cmux hooks enqueue claude …`) and are skipped with `CMUX_CLAUDE_HOOKS_DISABLED=1`. Those
hooks are what make cmux's agent layer track the pane: hibernation (reaps idle agents; under critical memory pressure
"even when routine Agent Hibernation is off", `CHANGELOG.md:1201`), auto-resume after retryable errors (types a
continue into the agent, `CHANGELOG.md:353`), and restore. Not verified: whether obor's explicit `--session-id`
fully avoids the wrapper's session binding, and whether disabling the hooks per pane also keeps hibernation off.

## 5. Gaps, risks, the adapter's tasks, and what to ask upstream

### Gaps

1. **Status read-back** (§2): no `app`, no `msg`, no snapshot; done dropped on focus. The adapter would hold the last
   event per surface, start `unknown` after a reconnect, and could rebuild from `~/.cmuxterm/events.jsonl` (an
   undocumented-for-clients file read: fragile).
2. **Socket trust**: default `cmuxOnly` refuses a LaunchAgent serve. The owner must set `automation` (or `password`,
   with the password in serve's environment: `service.EnvVar`, `internal/service/service.go:24-30`).
3. **No cwd/process/exit events**: poll `top`/`tree` on a timer, as obor once did on herdr.
4. **Labels**: only the surface title; a program's own title updates may compete (unverified).
5. **cmux's agent layer on obor's agents** (§4): hook injection, hibernation, auto-resume, restore. Needs a per-pane
   opt-out obor can set at spawn (`startup_environment: {CMUX_CLAUDE_HOOKS_DISABLED: "1"}` is the candidate for
   Claude; for Codex/agy/opencode: unknown).
6. **Panes die with the app**: the default terminal profile is the app's own PTY; quitting or updating cmux ends every
   agent (only the opt-in `cmux local-tmux` profile survives, `cmux@v0.65.2:docs/local-tmux.md:62-75`). Like herdr
   (`PaneRuns` false after a restart), unlike hc and Rex.

### Risks

- **Version churn: high.** 0.64.17 → 0.65.2.2 in four months, main pushed several times a day, the CLI contract is
  mid-migration to ArgumentParser (`docs/cli-contract.md:1-12`), v1 and v2 protocols side by side. A contract test
  needs a committed method list (from `system.capabilities`) since there is no parameter schema.
- **Not closed source**: GPL-3.0-or-later, readable, PRs accepted (CLA: `CLA.md`). A plus over Rex.
- **Sandbox: none**, so no sandbox limits on obor's side; the trust boundary is the socket mode.
- **Scope**: cmux is a large product (cloud VMs, browser, iOS, billing, PostHog/Sentry telemetry bundled) and its own
  opinionated agent manager. obor and cmux would both be the agent layer; that is the herdr D3 question again, with
  less control.
- **Owner's single instance**: probing needs an isolated cmux, and I found no documented way to run a second app
  instance with its own socket and state (a "tagged/debug socket" mechanism and `CMUX_TAG` exist in source; unread).

### Task list for an adapter (sizes: S ≤ ½ day, M ≈ 1-2 days, L ≈ 3+ days)

Only after the two upstream asks below land, or the owner accepts event-only status.

| # | Package | Task | Size |
|---|---|---|---|
| 0 | `docs/research` | **Probes on an isolated cmux** (below); go/no-go | M |
| 1 | `internal/config`, `internal/project` | `TerminalCmux = "cmux"` in `TerminalKinds`, pane var `CMUX_SURFACE_ID`; cmux names in `identityNames` | S |
| 2 | `internal/cmux` (new) `client.go`, `contract.go` | v2 newline-JSON client over the socket (stdlib), error predicates (`not_found`, `overloaded` retry, `timeout` + `retryable`), `Sent` list checked against a committed `system.capabilities` dump | M |
| 3 | `internal/cmux` `panes.go` | Snapshot from `system.tree` + `system.top --processes` + `surface.list`; Run from tty/shell pid; Instance from `boot_id` | M |
| 4 | `internal/cmux` `stream.go` | `events.stream` with `after_seq`; lifecycle events → port events; `program_status.changed` chunk assembly, gap → `Refreshed`; per-surface last status | M |
| 5 | `internal/cmux` `control.go` | Screen/Recent (`read_text`), Write, SendKeys (key table), RenamePane, Named, WaitStatus on the stream, `input_state` beside `GuardPrompts` | M |
| 6 | `internal/cmux` `place.go`, `setup.go` | Placement over workspace groups / workspaces / splits with `focus:false`, `startup_environment` (obor's env + the cmux opt-outs), TakeLead/LeadSpace, RenameProject, CloseProject, ResumeHint line | L |
| 7 | `internal/cmux` `env.go` | refuse or empty foreign identity names at spawn; Check refuses `cmuxOnly`/`off` naming the fix | S |
| 8 | `internal/app` status | an Agent source other than 7501's `app` (the kind obor started, or argv0) and "no record = idle" for a dismissed done | M |
| 9 | `cmd/obor`, `internal/service` | wire the kind; serve's env carries `CMUX_SOCKET_PATH` (and the password in `password` mode) | S |
| 10 | `internal/cmux/cmuxtest` | a fake socket server for adapter tests (as `hctest`) | M |
| 11 | `make conformance` | live tier per agent kind on cmux, non-gating (as Rex, D7) | M |

Total about 3 weeks of lanes, close to Rex's; tasks 3, 4 and 8 shrink to S each if upstream adds the snapshot query
with `app`/`msg`.

### Probes (not run: each needs a cmux of its own; what I would run)

The owner's app must not be used. A probe needs either the owner's go to run probes in a dedicated workspace of
their app, or a second cmux with its own socket and state (to find out first: does `CMUX_TAG` or the nightly bundle
`com.cmuxterm.app.nightly` give one?). With that, from a pane of the probe instance (or in `automation` mode):

| Probe | Command (against the probe socket only, `--socket <probe>`) | Answers |
|---|---|---|
| C1 trust | `socket-status --json`; a call from a launchd-started process in each mode | can serve connect, and how password auth looks on the v2 wire |
| C2 7501 | Claude, Codex, OpenCode, agy each in a surface; `cmux events --name program_status.changed` through a turn and a permission dialog | words, kinds, timing vs the dialog's render (Rex's R3) |
| C3 dismiss | focus a pane with a `done` record | the `dismissed` event; what obor would see |
| C4 env | `surface.split` with `startup_environment`, then `env` in the pane | per-surface env works; inherited names |
| C5 wrappers | start `claude --session-id X` with and without `CMUX_CLAUDE_HOOKS_DISABLED=1`; `ps -o args` | what the wrapper adds; whether cmux's agent layer still tracks the pane |
| C6 focus | `workspace.create`/`surface.split` with `focus:false` | the owner's focus is kept |
| C7 title | rename a surface, then run a program that sets its OSC title | which title `surface.list` reports |
| C8 restart | quit and reopen the probe app | `boot_id` changes, surface ids kept, shells new, what restore does to agents |
| C9 cost | idle CPU of the app with 8 agent panes | the herdr comparison (`BACKLOG.md:85`) |

### What to ask cmux upstream

1. **A per-surface program-status query** (`surface.program_status` or records in `surface.list`/`system.tree`):
   the docs already say there is none (`docs/events.md:358-359`).
2. **`app`, `msg` and `title` for authorised local clients**, at least in the query (opt-in parameter), since the
   socket is already owner-only; the sidebar shows them anyway.
3. **An option to keep `done`/`error` on focus**, or a field saying a record was dismissed by focus rather than
   cleared by the program.
4. **Events for cwd, foreground process and child exit** per surface (Rex has `terminal.pwd_changed`,
   `process_changed`, `child_exited`).
5. **A per-surface opt-out from cmux's agent layer** set at spawn (hooks, hibernation, auto-resume, restore), for an
   external orchestrator's panes.
6. **Free key=value metadata on a surface** (like hc's labels), returned by `surface.list` and evented.
7. **A parameter schema** for v2 methods (like Rex's `api describe --json`), and the spec revision it implements
   (0.3 vs 0.4) stated in the query reply.

## Still unknown

| Unknown | How to find out |
|---|---|
| v0.65.2.2 vs v0.65.2 differences in the parts read | diff the two tags' `Sources/Workspace+ProgramStatus.swift`, `docs/events.md`, `Packages/macOS/CmuxControlSocket` |
| Live cwd per surface as a query; `top` process fields (cwd, argv) | read `TerminalControllerTopSupport.swift` `processTreePayload`, or C-probe |
| Key names `esc`, `ctrl+u`, digits | read the `send_key` key table in source |
| Password auth on the v2 wire | read `SocketPasswordAuthorization.swift` |
| Event retention (how much replay covers a serve restart) | read `CmuxEventStream.swift` |
| An isolated second cmux instance | read `CMUX_TAG`/tagged-socket handling; try the nightly bundle |
| What `v1.x` tags in the repo are (`v1.38.1` exists; the latest release is `v0.65.2.2`) | look at one tag's tree; probably another product of the monorepo |

## Concern

cmux, like herdr, is a terminal with its own agent manager, and its 7501 relay is built for its sidebar. On today's
cmux, "it supports the same protocol" holds for the agents' side (they will report) but not for obor's side (obor
cannot read `app`/`msg` or a snapshot). Building the adapter before upstream adds a status query would bring back a
second status path in obor. Recommended: send asks 1, 2 and 5 upstream first and run probe C1 (trust) and C2 (7501)
on an isolated instance before writing a spec.
