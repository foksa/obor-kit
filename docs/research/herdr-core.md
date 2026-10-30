# herdr-core (`hc`): what it is and whether obor should move to it

Research note, 2026-10-30, deep-dive pane. Sources: the herdr-core, herdr-app and herdr repos on GitHub (read with `gh`
on 2026-10-30) and obor's `internal/herdr`. Nothing was installed or run. herdr, its plugins and hc's state were only
read.

## TL;DR

- herdr-core is **not a new `herdr` CLI**. It is a separate product with its own binary, `herdr-core`, plus an `hc`
  symlink. It is a headless terminal daemon (spawn, read, send, wait, events), and the new desktop app `herdr-app`
  (also its own command) is built on top of it. The herdr we run (CLI/TUI/server 0.9.3, protocol 22) is a different
  codebase that is still shipping (v0.9.5, 2026-10-14).
- **Both fit on one machine.** Their binary names, sockets, config and state dirs and env vars are all separate.
  Installing hc does nothing to running herdr panes. One real hazard is env leaking from a herdr pane into hc
  terminals (§2).
- **The API is a different protocol, not a new version of ours.** It has terminals instead of
  workspaces, tabs and panes, no layout, no agent detection, no plugins, no notifications and no output-match events.
  Status comes only from OSC 7501, which is reported by the program itself.
- **Moving obor to it means rewriting the adapter layer.** `internal/herdr` and `herdrtest`, plus the herdr-facing
  parts of `internal/setup`, `internal/launch` and `cmd/obor`. The `internal/app` core can mostly stay. In return obor
  gets a headless core, terminals that survive core restarts and updates, an API that only grows, and the end of
  several herdr workarounds.
- **Recommendation: don't switch now.** hc is 14 days old, at 0.1.x, and marked "preview". Do a time-boxed spike when
  the triggers in §5 are met. Until then, keep `internal/app.PaneControl` free of herdr-only shapes.

## 1. What it is

| | herdr (what obor runs) | herdr-core (`hc`) | herdr-app |
|---|---|---|---|
| Repo | [herdrdev/herdr](https://github.com/herdrdev/herdr), since 2026-03, ~47k★ | [herdrdev/herdr-core](https://github.com/herdrdev/herdr-core), since 2026-10-16, ~110★ | [herdrdev/herdr-app](https://github.com/herdrdev/herdr-app), since 2026-10-16 |
| What | CLI + TUI + background server; workspaces/tabs/panes, agent detection, plugins | "terminals for coding agents… Core draws nothing" ([README](https://github.com/herdrdev/herdr-core#readme)) | desktop window; "The window arranges terminals. The core runs them." ([README](https://github.com/herdrdev/herdr-app#readme)) |
| Version | 0.9.3 here; latest [v0.9.5](https://github.com/herdrdev/herdr/releases/tag/v0.9.5) | 0.1.14 ([Cargo.toml](https://github.com/herdrdev/herdr-core/blob/master/crates/herdr-core/Cargo.toml)), first commit 2026-10-23, 7 releases in a week | 0.1.9 |
| API | socket, protocol 22 (obor pins it exactly) | JSON-RPC 2.0 / OpenRPC, "Status: preview (protocol generation 1, version 0.x)" ([schema/README](https://github.com/herdrdev/herdr-core/blob/master/schema/README.md)) | `herdr-app` CLI drives the window; spaces/tabs stored in hc doc `herdr-app` |
| Language/license | Rust, Apache-2.0 | Rust (ghostty-vt), Apache-2.0 | Rust + QuickJS mods |

Maturity: preview, but the project takes it seriously. It has CI on Linux, macOS and Windows, a released-API
compatibility test ("The API only grows… A real break needs a new protocol generation"), and documented security and
state. There is no published roadmap. Whether the herdr TUI will move onto core is an open question that someone building on
hc 0.1.13 asked upstream in
[herdr discussion #5684](https://github.com/herdrdev/herdr/discussions/5684) (Q7), with no answer as of today. herdr
itself is still maintained: there were fixes on 2026-10-26, and the `/Applications/Herdr.app` here is
`so.pen.herdr-gpui`, herdr's GPUI client, built 2026-10-28.

Seen on this machine (read-only): hc is **already installed**. `~/.local/bin/herdr-core` and `~/.local/bin/hc` date
from 2026-10-29 20:47, and `~/.local/state/herdr-core/default/` holds terminals and a `herdr-app` doc. No core process
is running now. I did not touch any of it.

## 2. The command clash: there is none, apart from one env leak

- **Binaries.** The [core installer](https://github.com/herdrdev/herdr-core/blob/master/distribution/install.sh)
  writes `~/.local/bin/herdr-core` and symlinks `hc` to it, but only if no `hc` exists yet. The
  [app installer](https://github.com/herdrdev/herdr-app/blob/master/distribution/install.sh) writes `herdr-app` and
  `~/Applications/Herdr.app`. Neither installs or overwrites `herdr`, and our `~/.local/bin/herdr` (0.9.3) is
  untouched. One cosmetic point: there are now two bundles named "Herdr.app", `/Applications` (herdr GPUI) and
  `~/Applications` (herdr-app), which can confuse Spotlight and the Dock but not obor.
- **Sockets, config, state.** These are fully separate
  ([docs/security.md](https://github.com/herdrdev/herdr-core/blob/master/docs/security.md#state-and-endpoints)):

  | | herdr | hc |
  |---|---|---|
  | Socket | `~/.config/herdr/herdr.sock`, `$HERDR_SOCKET_PATH` | `$XDG_RUNTIME_DIR/herdr-core/<session>.sock`, else the state dir; `$HC_SOCKET` |
  | Config | `~/.config/herdr/` | `~/.config/herdr-core/config.toml` (shared by all sessions) |
  | State | herdr's session save | `~/.local/state/herdr-core/<session>/` |
  | Session | `herdr --session` | `HC_SESSION` |
  | Pane id env | `HERDR_PANE_ID` | `HC_TERMINAL_ID` |

- **Running panes.** Installing or running hc does not affect herdr panes. They are different processes and holders,
  and hc speaks nothing on herdr's socket. "Replacing" herdr would mean obor launching lanes as hc terminals instead.
  Existing herdr panes keep running under herdr until they are closed. Nothing migrates: a pane cannot become an hc
  terminal (asked in #5684, Q7).
- **Env leak (real).** A hc terminal gets the *core's* environment, not the caller's
  ([run.rs `child_env`](https://github.com/herdrdev/herdr-core/blob/master/crates/herdr-core/src/api/run.rs)). Only
  `TERM*`/`COLORTERM` and hc's own vars are scrubbed. The CLI starts the core on first use, so if the first `hc` call
  comes from inside an obor herdr pane, the core and **every later hc terminal inherit that pane's `HERDR_PANE_ID`,
  `HERDR_SOCKET_PATH` and `OBOR_ROLE`/`OBOR_LANE`**. obor's hooks and `internal/launch/self.go` would then take an hc
  terminal for that herdr pane. Mitigation if we try hc: start the core from a clean env (`env -i … hc server
  start`) and, in obor, ignore `HERDR_PANE_ID` when `HC_TERMINAL_ID` is set.

## 3. API compatibility with what obor uses

obor's herdr client (`internal/herdr/socket.go`, protocol 22) calls these methods, mapped to the
[hc OpenRPC](https://github.com/herdrdev/herdr-core/blob/master/schema/hc.openrpc.json):

| obor uses (herdr) | hc | Verdict |
|---|---|---|
| `ping` + exact protocol 22 | `hello` (generation 1) + `rpc.discover` | different; better policy: additive-only within a generation |
| `session.snapshot`, `agent.list`, `pane.list`, `pane.get`, `agent.get` | `terminal.list`, `terminal.get`, `events.subscribe` snapshot | different shape; **no workspaces, tabs or panes** |
| `agent.start` (name, kind, pane, args) | `terminal.run` (argv, name, cwd, env, labels, parent, keep, …), templates | similar; no "kind", and it starts in no pane |
| `agent.prompt` (refuses blocked, queues if working) | `terminal.send` (paste + Enter after ~3 s of no typing; refuses blocked unless `force`) | similar; the typing guard is built in |
| `agent.send_keys`, `pane.send_keys`, `pane.send_input` | `terminal.keys`, `terminal.send` | same idea |
| `pane.read` | `terminal.read` (screen + `lines` history, works for finished terminals too) | same |
| `pane.rename`, `agent.rename` | name at `run` only; `terminal.label` (free `key=value`) | rename **missing**; labels can carry lane/task |
| `pane.split`, `tab.create`, `pane.layout`, `pane.close` | `terminal.kill`; layout belongs to herdr-app | **missing** in core by design |
| `pane.process_info` (shell + fg group) | `Terminal.foreground` {pid, name, argv, cwd}, about 2 Hz, plus `pid` | partial; `Busy` would need its own `ps` |
| `agent_status` (herdr's screen/hook detection) | `status` = OSC 7501 report only ("never infers status from the screen") | **different**: Claude Code ≥ 2.1.295 and Pi 1.1.0 report it ([herdr #5073](https://github.com/herdrdev/herdr/discussions/5073)); anything else has no status |
| `agent` kind, `agent_session` / transcript path | none ("does not recognize agent brands") | **missing**; obor must record kind and session itself |
| `notification.show` | none (only child-to-parent "notes"; desktop notices are the app's job) | **missing** |
| `plugin.list/link/unlink` (obor.events: restore on startup) | none; `keep` gives a fresh shell after loss, never re-runs; agent resume is "a client's job?" (#5684 Q9) | **missing**, though largely not needed |
| events: `agent_status_changed`, `created`, `closed`, `exited`, `updated`, `moved`, `workspace.*` | `status`, `turn`, `created`, `finished`, `lost`, `restarted`, `removed`, `labels`, `foreground`, `title`, `doc`; `seq` + `heartbeat` + `missed` resync | richer; ordered with resync, which is better than obor's re-read loop |
| `pane.output_matched` | none | **missing**: poll `terminal.read` or use the framed attach stream |

Net: reading, typing and keys carry over. Pane layout, agent detection, session or transcript tracking, plugins and
notifications do not. Status semantics change from "herdr guesses" to "the agent reports".

## 4. Cost and gain of moving obor

**Cost (packages).** The `app.PaneControl` port (`internal/app/effects.go`, about 21 methods) keeps the core mostly
safe:
- `internal/herdr` (about 1,450 lines) plus `herdrtest`: a new adapter (`internal/hc`) and a new fake. Plain
  JSON-RPC is simpler than today's client.
- `internal/app`: the port loses `Split`/`NewTab`/`Layout`/plugins and gains labels. `Turn`/`Transcript` come from
  obor's own record instead of `agent_session`, and status vocabulary maps onto 7501 (`done`, `error`).
- `internal/setup` (agent.go: lanes as panes or tabs; setup.go/plugin.go: plugin link),
  `internal/launch` (`self.go` uses `HERDR_PANE_ID`; `restore.go` becomes event-driven on `lost`/`restarted`),
  `cmd/obor` (serve, lead, setup, plugin), `internal/guard`, `internal/config`, `testkit`.
- `herdr-plugin/` and `obor setup herdr-plugin` go away. A notification path has to be added (osascript or a
  herdr-app mod). Viewing lanes moves to herdr-app or `hc attach`.
- Docs: the `docs/design.md` herdr-integration sections, skills and templates (7 files mention herdr).

**Gain.**
- **Headless serve**: the core starts on demand with no app or TUI, so obor could run on a box over SSH (`hc machine
  add`, `--on`).
- Terminals **survive core restarts and updates** (holders), whereas herdr loses processes on a server restart. That
  removes the obor.events restore plugin and `resume_agents_on_restore = false`.
- An API that grows without breaking replaces the exact-protocol pin that breaks obor on every herdr protocol bump.
- Status reported by the agent replaces screen detection quirks. The `turn` counter fits obor's turn tracking, and
  `parent`/`children_of`/`hc wait --children` match the lead/worker shape.
- Ordered events with `seq`, `missed` and a snapshot replace obor's settle and re-read logic.

## 5. Recommendation

**Stay on herdr 0.9.x now.** Spike hc (a throwaway `HC_SESSION`, scratch `XDG_*`, binary not on PATH, a stub adapter
behind `PaneControl`) when **all** of these hold:
1. hc reaches a stable generation (1.0 or a stated freeze), with releases slowing from about one a day.
2. Upstream answers #5684: TUI and core plans (Q7), env passing (Q5), focus or input lock (Q3/Q11), and session
   restore (Q9).
3. Every agent kind obor runs (Claude, Codex, OpenCode) reports OSC 7501 inside hc, or hc gets a fallback (Q8).

**Watch:** herdr-core releases and `released.openrpc.json` diffs; whether herdr (TUI) answers OSC 7501
([#5106](https://github.com/herdrdev/herdr/discussions/5106)), which would give obor 7501 status without moving;
any herdr announcement of maintenance mode or of the TUI moving onto core.

**Now, at no cost:** keep new herdr-only shapes (tabs, layout, plugins) out of `internal/app`, and keep reading
`HERDR_PANE_ID` in one place.

**Questions for the owner**
1. Who installed hc here on 2026-10-29, and did you start its core from inside an obor pane? (See the env leak in §2.)
   Should obor guard against it now, for example by ignoring `HERDR_PANE_ID` when `HC_TERMINAL_ID` is set?
2. Is headless or remote serve, with no app, a goal worth an adapter rewrite, or is the TUI view of lanes essential?
3. Would you accept herdr-app as the way to see lanes (no tmux-style split of the lead's tab)?
4. Should obor own agent session ids and transcripts (via its hooks) now, so it no longer depends on herdr's
   `agent_session`? That would help in either world.
5. Shall I open the "herdr answers OSC 7501" request upstream as a vote, or wait for #5684's answers?
