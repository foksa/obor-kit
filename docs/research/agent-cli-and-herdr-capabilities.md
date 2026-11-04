# What the agent CLIs and herdr offer an orchestrator

Research note, 2026-11-04. Versions on this machine: **Claude Code 2.1.318**, **codex-cli 0.166.0**, **opencode v2.2.0**,
**herdr 0.9.3** (protocol 22). Facts only, each with where it comes from; no judgement of obor's code. Read first and
not repeated here: `docs/research/herdr-core.md` (herdr-core / `hc`) and `docs/research/opencode-model-selection.md`
(model and variant selection in opencode v2).

## 1. How to read this

### 1.1 Marks

- **verified** = I saw it on the installed binary (`--help`, files on disk, `herdr api schema`), in the source at the
  installed tag, or in a probe (§1.3).
- **docs only** = taken from a doc page, release note, changelog or issue text and **not** reproduced here. Issue
  reports are "issue text only".
- Section 3 (Codex) uses its own key: `S` source tarball at tag `rust-v0.166.0`, `D` developers.openai.com doc, `R`
  release notes, `H` `--help`, `P` probe. Section 4 (opencode) marks `[V]` verified, `[D]` docs only, `[O]` stated in
  obor's own plugin header comments (`~/.obor/kit/opencode/obor.js`, earlier probes T-578/T-703 that were not re-run).
  Sections 2 and 5 use a Status column.

### 1.2 Doc and changelog sources

| Tool | Docs | Changelog used for "last ~3 months" (2026-08-04 → 2026-11-04) |
|---|---|---|
| Claude Code | https://code.claude.com/docs/en/<page>.md (index: https://code.claude.com/docs/llms.txt) | [CHANGELOG.md](https://github.com/anthropics/claude-code/blob/main/CHANGELOG.md), versions 2.1.221 (2026-08-03) → 2.1.318 (2026-11-03); dates from the npm registry `time` field |
| Codex | https://developers.openai.com/codex/<page>.md (tracks the latest build, not pinned); source https://github.com/openai/codex/tree/rust-v0.166.0 | GitHub releases `rust-v0.146.1` → `rust-v0.166.0` |
| opencode | https://opencode.ai/v2/docs/… | GitHub releases of `anomalyco/opencode`, v2.2.0 → v2.2.5 (v2.0/v2.1 are not published as releases) |
| herdr | docs at tag v0.9.3 (`raw.githubusercontent.com/herdrdev/herdr/v0.9.3/docs/next/website/src/content/docs/<page>.mdx`); `herdr --skill`; https://herdr.dev/llms.txt | releases [v0.9.4](https://github.com/herdrdev/herdr/releases/tag/v0.9.4), [v0.9.5](https://github.com/herdrdev/herdr/releases/tag/v0.9.5); the issue tracker |

### 1.3 Probes run (all on 2026-11-04)

AGENTS.md rule followed: a live probe uses only a throwaway named herdr session in a scratch dir and never addresses
the default session. No headless run (`claude -p`, `codex exec`, `opencode run`) was made.

| Probe | What | Cleanup |
|---|---|---|
| **P-cc / P-hd** (Claude + herdr) | Headless `herdr --session probe-claude server` started under `env -i`; a workspace with one pane in a scratch dir. Pane 1: `claude --model sonnet --effort low --session-id <uuid> --mcp-config <stub> --settings <hooks> --name probe-cc` (stub stdio MCP server that dumps its env/cwd/ppid; SessionStart/SessionEnd hooks that dump stdin/env/cwd), trust dialog answered, `/exit`. Pane 2 (same pane after exit): `herdr agent start cc1 --kind claude --pane … -- --model sonnet --effort low --settings <Stop/UserPromptSubmit hooks>`, one turn via `herdr agent prompt … --wait` ("Reply with exactly the single word OK"; the only model call), `agent prompt "/exit"`. Then `pane run`/`wait-output`/`rename`, `notification show`, raw `ping` and an unknown method on the socket, a stop/start of the session to see restore. | session stopped and deleted; `~/.claude.json` got a trust entry for the scratch dir; two small transcripts remain under `~/.claude/projects/-private-tmp-…probe-claude-work/` |
| **P** (Codex) | `herdr --session probe-codex`, scratch `CODEX_HOME` copy, stub stdio MCP via `-c mcp_servers.stub.*`, interactive `codex`, `/quit`. No model prompt. | session stopped; the copied `auth.json` removed; `~/.codex` untouched |
| **P1 / P2** (opencode) | `herdr --session probe-opencode`, scratch `XDG_*`, config via `OPENCODE_CONFIG_CONTENT`, stub stdio MCP. P1: `opencode --standalone`. P2: `opencode serve --port 49555` in one pane plus `opencode --server http://127.0.0.1:49555` in another, then `opencode api` / curl against the server (`POST /api/session`, `…/prompt` with `resume:false`, export). No model call. | session stopped |

Stub MCP server used by all three (about 15 lines, Python): on start write `{pid, ppid, cwd, argv, env}` to a file
named from `$STUB_OUT`; then answer `initialize` (echo `protocolVersion`, `capabilities:{tools:{}}`) and `tools/list`
(one `ping` tool) on stdin/stdout JSON lines. The hook probe was a shell script that wrote its argv, `$PPID`, `pwd`,
stdin and `env` to a file. Scripts were in the session scratchpad and are not committed.

### 1.4 Side-by-side (the facts the sections below support)

| | Claude Code 2.1.318 | Codex 0.166.0 | opencode 2.2.0 | herdr 0.9.3 |
|---|---|---|---|---|
| **First prompt + model + effort at launch** | `claude [prompt] --model X --effort L` (flags verified) | `codex [PROMPT] -m X -c model_reasoning_effort="L"` | TUI has `--prompt` only; **no `--model`/`--variant` on the TUI command** (`mini` and `run` have) | `agent start <name> --kind K --pane P -- <args>` passes args through |
| **MCP server env** | **whole env of the Claude process** + entry `env` + `CLAUDE_PROJECT_DIR`, `CLAUDE_CODE_SESSION_ID`, `CLAUDE_CODE_MESSAGING_SOCKET/TOKEN` (verified) | **allowlist only**: `HOME LOGNAME PATH SHELL USER LANG LC_ALL TERM TMPDIR TZ …` + `env_vars` names + `env` map; no `HERDR_*`/`OBOR_*` (verified) | **whole env of the process that runs the opencode server** (shared service ≠ pane; `--standalone` = the pane) + `environment` map (verified) | pane process env = the **server's** env + injected `HERDR_*` (verified) |
| **MCP server cwd** | launch cwd (verified) | `cwd` key, else session cwd (verified) | location (project) directory (verified, ambiguous vs server cwd) | pane cwd |
| **MCP process lifetime** | one per Claude session, child of Claude, started at launch (verified) | one per codex process, child of codex, started at launch (verified) | **one per location/server, shared by sessions**, started when the client opens the directory (verified) | n/a |
| **Hooks / plugins run in** | child process of Claude, cwd = session cwd, Claude's env (verified) | `shell -lc`, cwd = session cwd (source only) | server plugin: inside the server process; TUI plugin: inside the TUI (= pane) process | herdr plugins: processes spawned by herdr with `HERDR_PLUGIN_*` |
| **Turn-done signal** | `Stop` hook (`last_assistant_message`) / `StopFailure` (verified Stop) | `Stop` hook; legacy `notify` program | events `session.execution.succeeded/failed/interrupted` (no `session.idle` in v2) | `agent wait`, `pane.agent_status_changed` (screen-based for Claude/Codex) |
| **Session id source** | hook `session_id`, `CLAUDE_CODE_SESSION_ID`, `--session-id`, `claude agents --json` (verified) | hook `session_id`; printed `Session ID:` at exit; `state_5.sqlite` (verified) | `POST /api/session` `data.id`; `Continue  opencode -s <id>` at exit (verified) | `agent_session` (reported by the agent's integration) |
| **Transcript** | `~/.claude/projects/<cwd-encoded>/<id>.jsonl`, created at first prompt (verified) | `~/.codex/sessions/YYYY/MM/DD/rollout-<ts>-<id>.jsonl`, none if no turn (verified) | one SQLite db `~/.local/share/opencode/opencode.db`; `GET …/export` (verified) | none |
| **Deliver text without typing** | inbox socket (format undocumented), channels (preview), `SendMessage`; herdr paste (verified) | `codex queue --thread`, app-server `turn/steer`; herdr paste | HTTP `POST /api/session/{id}/prompt` with Basic auth (verified), `resume:false` enqueues | `agent prompt`, `pane run`, `send-text`, `send-keys` |
| **Clean quit** | `/exit` → status 0, SessionEnd (`prompt_input_exit`), resume hint after a turn (verified) | `/quit` → status 0, prints `Session ID` (verified) | `/exit`; one Ctrl+C → status 0, prints `Continue  opencode -s <id>` (verified) | `pane close`, `session stop`, `server stop` |
| **Rate-limit signal** | `StopFailure` hook `error: rate_limit`; auto-continue; retry watchdog | rollout `token_count.rate_limits`, `CodexErrorInfo` enum; no StopFailure event | `session.retry.scheduled`, `session.execution.failed {error.type}` | none (screen only) |


## 2. Claude Code 2.1.318

Sources: `docs` = https://code.claude.com/docs/en/<page>.md (pages named in each row); `CL x.y.z` = [CHANGELOG.md](https://github.com/anthropics/claude-code/blob/main/CHANGELOG.md) entry for that version; `P-cc` = the Claude probe (§1.3; the same throwaway herdr session also gave the `P-hd` herdr facts). Version dates (npm registry `time`): 2.1.221 = 2026-08-03, 2.1.247 = 2026-08-26, 2.1.258 = 2026-09-01, 2.1.299 = 2026-10-14, 2.1.318 = 2026-11-03.

### 2.1 Interactive start with model, effort and first prompt

| Fact | Status | Source |
|---|---|---|
| `claude [options] [prompt]` starts an **interactive** session; `-p/--print` is the non-interactive mode. A positional `prompt` is the first user prompt. | verified (`claude --help`: "starts an interactive session by default", `Arguments: prompt`); that the positional prompt is submitted as turn 1 in interactive mode is docs only | `claude --help`; docs `cli-reference` |
| `--model <alias or full name>`: aliases `fable`, `opus`, `sonnet` (help text). `--model sonnet` produced the banner "Sonnet 5.5 with low effort" and SessionStart `model: "claude-sonnet-5-5"`. | verified | P-cc |
| `--effort low\|medium\|high\|xhigh\|max`. Env `CLAUDE_CODE_EFFORT_LEVEL` (same values, plus `auto`/`unset`) **takes precedence over** `--effort`, `/effort` and settings; a `maxEffortLevel` setting caps it. Hooks get `effort.level` and `$CLAUDE_EFFORT`. | flag verified (help + banner); precedence docs only | docs `env-vars`, `model-config`, `hooks` (common fields); CL 2.1.267 (`maxEffortLevel`) |
| Effort is saved **per model** (`/effort`): CL 2.1.251. An effort saved before that change does not apply to new models (CL 2.1.280). `--effort` lifts a new model's default-effort hold for that session only (CL 2.1.257). | docs only | changelog |
| `--session-id <uuid>` makes the session use that id. With it the SessionStart hook got `session_id` = the id and the transcript path ended `<id>.jsonl`. | verified | P-cc |
| `-n/--name <name>` sets the display name; SessionStart then carries `session_title`. | verified | P-cc |
| Other launch flags an orchestrator can pass: `--settings <file-or-json>` (extra hooks/settings, verified), `--mcp-config <files-or-json...>` (verified), `--strict-mcp-config`, `--plugin-dir`, `--add-dir`, `--permission-mode acceptEdits\|auto\|bypassPermissions\|manual\|dontAsk\|plan`, `--agent`, `-w/--worktree`, `--append-system-prompt`, `--fallback-model a,b`, `--bg`, `--safe-mode`, `--bare`, `--restricted`. | listed in help (verified); semantics docs only | `claude --help` |
| **First launch in a directory shows a workspace-trust dialog** ("Is this a project you created or one you trust?") that blocks everything until answered (`down`+`enter` = "Yes, I trust this folder"). `--settings` / `--mcp-config` do not skip it. Accepting records the directory in `~/.claude.json`. `claude --bg` asks too and exits when not interactive (CL 2.1.281). `CI` set + `claude agents` or `ultrareview` refuse an untrusted folder at once (CL 2.1.248, 2.1.314). | verified (interactive); changelog for `--bg`/CI | P-cc; CL 2.1.281 |
| `herdr agent start <name> --kind claude --pane <id> -- <claude args>` passes the args through (`--model sonnet --effort low --settings …` worked) and returned when Claude was idle, in about 3.6 s. | verified | P-cc |
| Resume does **not** restore `--mcp-config`, `--settings`, `--plugin-dir`, `--fallback-model`, `--add-dir`: pass them again. | docs only | docs `sessions` |

### 2.2 MCP servers: registration, environment, working directory

| Fact | Status | Source |
|---|---|---|
| Registration: `claude mcp add [--scope local\|project\|user] <name> -- <cmd> [args]`, `claude mcp add-json`, a project `.mcp.json`, `~/.claude.json` (local and user scope), **per launch** `--mcp-config` (+`--strict-mcp-config` to ignore every other source), a plugin's `mcpServers` (inline in `plugin.json`, a `.json` file, or the plugin's own `.mcp.json`; a later name replaces an earlier one), enterprise `managed-mcp.json`. `${VAR}` / `${VAR:-default}` expand in `command`, `args`, `env`, `url`, `headers`. | docs only (`--mcp-config` verified) | docs `mcp`, `plugins-reference` |
| A project `.mcp.json` server needs approval (trust dialog, `enabledMcpjsonServers`); a cloned repo cannot approve itself. | docs only | docs `mcp` (Server status) |
| A stdio server is **spawned by the Claude process at session start, without any prompt being sent**, and torn down when Claude exits (the stub was gone after `/exit`). **One process per Claude session**, never shared across sessions: its parent pid was the Claude pid. | verified | P-cc |
| **cwd** = the directory Claude was launched in (the pane's cwd). | verified | P-cc |
| **env** = the whole environment of the Claude process (not stripped): every `HERDR_*` var, `AI_AGENT=claude-code_2-1-318_harness`, `CLAUDECODE=1`, plus the entry's own `env`, plus Claude's own additions: `CLAUDE_PROJECT_DIR` (project root), `CLAUDE_CODE_SESSION_ID` (the id the server was spawned with; on `--resume` the resumed id), `CLAUDE_CODE_MESSAGING_SOCKET`, `CLAUDE_CODE_MESSAGING_TOKEN`, `CLAUDE_CODE_ENTRYPOINT`. Anything the launching pane exported (e.g. `OBOR_ROLE`) reaches it, because nothing is filtered. | verified | P-cc; docs `mcp` ("Claude Code sets `CLAUDE_PROJECT_DIR`…"), `env-vars` (`CLAUDE_CODE_SESSION_ID`) |
| Opt-in scrub: `CLAUDE_CODE_SUBPROCESS_ENV_SCRUB=1` strips credential-looking vars from Bash, hooks and stdio MCP servers; `OTEL_*` exporter vars are always removed from every subprocess. | docs only | docs `env-vars`, `hooks` (common fields) |
| Roots: Claude answers MCP `roots/list` with the launch dir plus `--add-dir` dirs. Timeouts: `MCP_TIMEOUT` (startup), `MCP_TOOL_TIMEOUT` or a per-server `timeout` (ms), `MAX_MCP_OUTPUT_TOKENS` (default 25 000). `CLAUDE_CODE_MCP_STARTUP_WAIT_MS` bounds how long a **non-interactive** first turn waits for connecting servers (CL 2.1.274). | docs only | docs `mcp`, `env-vars` |
| Tool names are `mcp__<server>__<tool>` (hook matchers); a hook can itself be `type: "mcp_tool"`. | docs only | docs `hooks` |

### 2.3 Hooks

| Fact | Status | Source |
|---|---|---|
| Events (docs table): `SessionStart` (matchers `startup`, `resume`, `clear`, `compact`, `fork`), `Setup`, `InstructionsLoaded`, `UserPromptSubmit`, `UserPromptExpansion`, `PreToolUse`, `PermissionRequest`, `PermissionDenied`, `PostToolUse`, `PostToolUseFailure`, `PostToolBatch`, `Notification`, `MessageDisplay`, `SubagentStart`, `SubagentStop`, `TaskCreated`, `TaskCompleted`, `Stop`, `StopFailure`, `TeammateIdle`, `ConfigChange`, `CwdChanged`, `DirectoryAdded`, `FileChanged`, `WorktreeCreate`, `WorktreeRemove`, `PreCompact`, `PostCompact`, `PreModelSwitch`, `PostModelSwitch`, `Elicitation`, `ElicitationResult`, `SessionEnd`. | docs only (5 of them fired in P-cc) | docs `hooks` |
| Handler types: `command`, `http`, `mcp_tool`, `prompt`, `agent`. Configured in settings files (user, project, local, managed), `--settings`, plugin `hooks/hooks.json`, skill/agent frontmatter. `SessionStart` supports only `command` and `mcp_tool`. | docs only (`--settings` verified) | docs `hooks` |
| A command hook is a **child of the Claude process** (parent pid = Claude pid), runs with **cwd = the session's current directory**, and gets Claude's full environment plus `CLAUDE_PROJECT_DIR`, `CLAUDE_PLUGIN_ROOT`, `CLAUDE_PLUGIN_DATA` (plugin hooks), `CLAUDE_CODE_SESSION_ID`, `CLAUDE_CODE_MESSAGING_SOCKET/TOKEN`, `CLAUDE_ENV_FILE` (only SessionStart, Setup, CwdChanged, FileChanged). If the cwd was deleted Claude falls back to the start dir, the project root, `$HOME`, then temp. | verified (parent, cwd, env); fallback docs only | P-cc; docs `hooks` ("Handlers run in the current directory…") |
| stdin JSON, common fields: `session_id`, `prompt_id`, `transcript_path`, `cwd`, `scratchpad_dir` (v2.1.257+), `permission_mode`, `effort`, `hook_event_name`, and `agent_id`/`agent_type` inside a subagent. **No model env var** (model only in SessionStart `model` and in Pre/PostModelSwitch). | verified for the events below | P-cc; docs `hooks` |
| SessionStart payload seen: `session_id, transcript_path, cwd, scratchpad_dir, hook_event_name, source:"startup", model, session_title`. It fires at launch **before any prompt**. On `resume`/`fork` it also carries `seconds_since_last_response`, `context_tokens`, `prompt_cache_likely_expired`, `estimated_cache_write_usd` (v2.1.251+). | verified (startup); resume fields docs only | P-cc; docs `hooks` |
| UserPromptSubmit payload seen: `session_id, transcript_path, cwd, prompt_id, permission_mode, hook_event_name, prompt` (full prompt text). It also fires on turns Claude starts itself. | verified | P-cc; docs |
| **Stop** payload seen: `session_id, cwd, prompt_id, permission_mode, effort:{level}, hook_event_name:"Stop", stop_hook_active, last_assistant_message:"OK", background_tasks:[], session_crons:[]` (+`transcript_path`). The transcript may lag the turn: use `last_assistant_message`. `background_tasks`/`session_crons` tell "done" from "paused for background work". A Stop hook that blocks is capped at 8 consecutive continuations (`CLAUDE_CODE_STOP_HOOK_BLOCK_CAP`). | verified (fields); cap docs only | P-cc; docs `hooks`, `env-vars` |
| **StopFailure** (runs *instead of* Stop when the turn ends on an API error): `error` ∈ `rate_limit, overloaded, authentication_failed, oauth_org_not_allowed, account_on_hold, billing_error, invalid_request, model_not_found, server_error, max_output_tokens, cloud_credential_error, unknown`, optional `error_details` (e.g. "429 Too Many Requests"), `last_assistant_message` = the API error text. Output and exit code are ignored. | docs only | docs `hooks` (StopFailure) |
| **SessionEnd**: `reason` ∈ `clear, resume, logout, prompt_input_exit, other`. `/exit` gave `prompt_input_exit`. The hook budget is **1.5 s** by default (raised by a per-hook `timeout`, up to 60 s, or `CLAUDE_CODE_SESSIONEND_HOOKS_TIMEOUT_MS`). It cannot block and its JSON output is discarded. | `/exit` reason verified; budget docs only | P-cc; docs `hooks` |
| **Notification** `notification_type`: `permission_prompt` (after ~6 s unanswered), `idle_prompt` (~60 s after a turn ended), `auth_success`, `elicitation_*`, `agent_needs_input`, `agent_completed`, `quota_auto_resume_fired/stale/disabled` (v2.1.234+), `plugin_notification`. Timed gates mean it is **not** an immediate "needs input" signal; use `PermissionRequest` for that. | docs only | docs `hooks` (Notification) |
| Exit codes: 0 ok (stdout JSON parsed), **2 = block** (stderr to Claude), anything else non-blocking. `onFailure: "block"` (v2.1.295+) makes a hook that cannot start, times out or exits oddly block the action. Timeouts: command 600 s default, 30 s on UserPromptSubmit/Pre/PostModelSwitch, 10 s MessageDisplay; a timed-out PreToolUse hook does **not** block unless `onFailure:"block"`. `async: true` hooks run in the background (`asyncRewake` wakes Claude on exit 2). | docs only | docs `hooks`; CL 2.1.295 |
| SessionStart hooks run in the background at launch/`--resume`/`/clear`: typing works at once, but Claude's first response waits for them. | docs only | docs `hooks` |
| `claude --bare` / `--safe-mode` / `--restricted` skip or limit hooks, plugins and settings. | docs only (flags verified in help) | `claude --help` |

### 2.4 Session ids, resume, transcripts

| Fact | Status | Source |
|---|---|---|
| Session id = UUID. Visible to hooks (`session_id`), Bash/hook/stdio-MCP subprocesses (`CLAUDE_CODE_SESSION_ID`, updated on `/clear` for Bash and hooks, fixed at spawn for MCP), `claude agents --json` (`sessionId`), and herdr (`agent_session` via herdr's own Claude SessionStart hook). | verified | P-cc; this session's env |
| Transcript: `~/.claude/projects/<cwd with every non-alphanumeric char → "-">/<session-id>.jsonl` (truncated + hashed past 200 chars). The cwd is **realpath-resolved** (`/tmp/x` became `-private-tmp-…`). The file/dir is **created lazily at the first prompt**: before it, `transcript_path` pointed at a path that did not exist; after one "OK" turn the file had 40 lines (354 KB, mostly attachments). Line types seen: `mode, permission-mode, session-owner, atis-latch, bridge-session, file-history-snapshot, user, attachment, …`; `user` lines carry `cwd, gitBranch, sessionId, timestamp, promptId, permissionMode, version`. | verified | P-cc |
| Retention 30 days (`cleanupPeriodDays`); `CLAUDE_CODE_SKIP_PROMPT_HISTORY=1` writes no transcript (and the session can't be resumed); `CLAUDE_CODE_PROJECT_DIR_NAME` + `CLAUDE_CONFIG_DIR` choose the directory. | docs only | docs `sessions` |
| Resume: `claude -c`, `claude -r <id\|name>`, `claude --resume <transcript-path>`, `--fork-session`, `--from-pr`. Resuming the same session in two terminals interleaves one transcript. `--resume <id> --bg` continues under its own id when nothing runs it (CL 2.1.257). `/exit` after a turn prints `Resume this session with:` + `claude --resume <id>` (id, not name, since CL 2.1.299). With zero turns nothing was printed. | `/exit` hint verified; the rest docs only | P-cc; docs `sessions`; CL 2.1.299 |
| `~/.claude/sessions/<claude-pid>.json` exists for each live session: `pid, sessionId, cwd, startedAt, procStart, version, peerProtocol, peerFeatures, kind, entrypoint, messagingSocketPath, name, nameSource, status, updatedAt`. **Not documented**; the documented interface is `claude agents --json`. | file verified; stability unknown | local file (this session's own) |
| `claude agents --json [--all] [--cwd <p>]`: JSON array of live sessions: `pid, cwd, kind (interactive\|background), startedAt, sessionId, name, status (busy\|waiting\|idle)`, plus `waitingFor` and, for background sessions, `id` and `state` (`working\|blocked\|done\|failed\|stopped`). Documented as "the supported way to read session state from outside Claude Code"; `~/.claude/jobs/<id>/` files are **not** a stable interface. Interactive pane sessions appear with `status` only. | verified (shape, interactive entries); semantics docs | `claude agents --json`; docs `agent-view` |
| Background sessions: `claude --bg`, `attach/logs/stop/respawn/rm <id\|name>`, a supervisor process (`claude daemon status/stop`), idle ones are stopped after ~1 h unattached and resume on attach/reply. | docs only | docs `agent-view` |

### 2.5 Delivering text to a running session other than typing

| Channel | What it is | Status | Source |
|---|---|---|---|
| **herdr `agent prompt`** | paste + Enter into the pane. Worked: `agent prompt cc1 "Reply with exactly the single word OK…" --wait` returned idle with `completion_seq: 6`; `agent prompt cc1 "/exit"` ran the slash command. | verified | P-cc; see §5 for its limits |
| **Inbox socket** | each session binds a Unix socket `/tmp/cc-socks/<claude-pid>.sock` (mode `srw-------`), exported as `CLAUDE_CODE_MESSAGING_SOCKET` with `CLAUDE_CODE_MESSAGING_TOKEN` to hooks, Bash and MCP children. Documented as the place "a script or hook can post into a session". Optional first line `{"type":"auth","token":"…"}` (required on Windows). Connection closed after 30 s without a complete line. Messages are plain text, ≤ ~1 M chars, queue ≤ 50, held ≤ 100. Arrival: read between tool calls in a turn, or starts a new turn when idle. **The line format of a message is not documented**, and I did not post to a socket. | path/mode/env verified; semantics docs only | `ls /tmp/cc-socks`; docs `cross-session-messaging` ("The session's inbox socket") |
| Inbound policy | `crossSessionInbound` = `accept`/`hold`/`refuse`. When unset, per message from permission-mode classes: a **prompting** receiver accepts, but **holds** a message from a sender that bypasses permissions; a **bypassing** receiver holds everything unless the sender also bypasses. A hook or Bash child posting to **its own session's** socket is delivered when no value applies (verified by pid evidence on Linux; on macOS only while the child is alive, else by the token). Held messages open an approval dialog (5 min `dialogExpiry`) in an interactive session. A message can never approve a permission prompt, change config, or run a slash command. | docs only | docs `cross-session-messaging` |
| `SendMessage` / `ListAgents` tools | what a Claude session itself uses to message another (name or `@mention`); `notify_when_idle` subscribes to one notice at the next idle/exit (v2.1.236+, 12 h expiry). Requires v2.1.224+ (macOS/Linux), 2.1.234 (Windows). `--restricted` and `--bare` sessions bind no socket. | docs only | docs; CL 2.1.224, 2.1.239, 2.1.290 |
| **Channels** | an MCP server declaring capability `experimental['claude/channel']` pushes `notifications/claude/channel` events that arrive as messages in the running session; two-way with reply tools. **Research preview**: needs claude.ai or Console auth (not Bedrock/Vertex/Foundry), org opt-in for Team/Enterprise, an allowlist; a custom one needs `--dangerously-load-development-channels server:<name>` (confirmation prompt) or `--channels`. | docs only | docs `channels`, `channels-reference` |
| `claude --resume <id> "prompt"` | against a **running background** session: sends the prompt as its next turn and attaches. | docs only | docs `sessions` |
| Hooks as injectors | `SessionStart`/`UserPromptSubmit` can return `additionalContext` or a first user message; `asyncRewake` shows hook stderr to Claude as a system reminder. | docs only | docs `hooks` |

### 2.6 Quitting cleanly

| Fact | Status | Source |
|---|---|---|
| `/exit` (typed, or sent with `herdr agent prompt <name> "/exit"`) ends the process: shell `$?` = 0, the `SessionEnd` hook ran (reason `prompt_input_exit`), the stdio MCP child exited, the pane returned to the shell. After at least one turn Claude prints `Resume this session with:` / `claude --resume <id>`. | verified | P-cc |
| Ctrl+C / Ctrl+D semantics (interrupt a turn vs. exit) and `Esc`: not probed. `herdr agent send-keys … ctrl+c` was accepted and the agent stayed idle. | docs only / partial | docs `interactive-mode` |
| An attached background session: Ctrl+C at the idle prompt leaves a pending `/loop` alone; press twice to detach (CL 2.1.295). `claude stop <id>` stops a background session. | docs only | CL 2.1.295; docs `agent-view` |

### 2.7 Rate-limit and error reporting

| Fact | Status | Source |
|---|---|---|
| In-session: the TUI shows the API error text; the `Stop` hook is replaced by **`StopFailure`** with the `error` enum (§2.3). Nothing is written to a file for an outside process except what that hook writes. | docs only | docs `hooks` |
| Usage limit: Claude can wait and continue by itself (`autoContinueAtUsageLimit` setting; at most 3 automatic continuations without a user prompt, CL 2.1.314); `Notification` types `quota_auto_resume_fired/stale/disabled` report it. Messages say "5-hour limit" (CL 2.1.312). | docs only | docs `hooks`, `settings`; CL 2.1.312, 2.1.314 |
| Retries: `CLAUDE_CODE_MAX_RETRIES` bounds 429/529 retries; **`CLAUDE_CODE_RETRY_WATCHDOG=1`** retries 429/529 forever for unattended sessions, but fails at once on a spend-limit or exhausted-credits 429; bounded by `CLAUDE_CODE_RETRY_WATCHDOG_MAX_WAIT_MS` (2.1.295), `…_MAX_USAGE_LIMIT_WAIT_MS` (2.1.301), `…_USAGE_LIMIT_RECHECK_MS` (2.1.305); 529 backoff via `CLAUDE_CODE_OVERLOADED_RETRY_BASE_DELAY_MS` / `_MAX_DELAY_MS` (2.1.292, 2.1.296). | docs only | docs `env-vars`; changelog |
| `--fallback-model a,b` switches model when the default is overloaded/unavailable (retries the primary each turn). Refusal fallbacks can be turned off with `CLAUDE_CODE_DISABLE_REFUSAL_FALLBACK=1` (CL 2.1.312). | flag verified in help | help; CL 2.1.312 |
| `claude agents --json` `state: blocked` includes "an error only you can clear such as an expired login"; background agent notifications say when an agent "was stopped by a usage limit" (CL 2.1.299). | docs only | docs `agent-view` |
| A pane is a plain terminal to herdr; herdr does not see `StopFailure`. herdr issue #3467 (open): a stale busy OSC title can outrank a blocked rule (Claude). | issue text only | §5 |

### 2.8 Changelog items from the last ~3 months (2.1.221 → 2.1.318) that matter to an orchestrator

All entries are `docs only` (changelog text), except where marked.

- **Messaging**: cross-session messaging, `SendMessage`/`ListAgents`, inbox socket (2.1.224); `crossSessionInbound` and `@mention` targets (2.1.232); socket dir hardening to `/tmp/cc-socks-<uid>` fallback (2.1.232/2.1.248); `notify_when_idle` (2.1.236); inbox socket closes a connection that sends no complete line in 30 s (2.1.243); `--restricted` sessions open no socket (2.1.290); held-message transcript view (2.1.309); messages to a running background agent arrive earlier (2.1.317). *The socket dir seen here is `/tmp/cc-socks/` (verified), not the `cc-socks-<uid>` fallback.*
- **Hooks**: `PreModelSwitch`/`PostModelSwitch` events and resume staleness fields (2.1.251); `scratchpad_dir` in hook input (2.1.257); `onFailure: "block"` for command/HTTP hooks (2.1.295) and SDK callbacks (2.1.317); `CLAUDE_ENV_FILE` for PowerShell (2.1.296); prompt/agent hooks capped at 24 concurrent (2.1.312); `updatedInput` validated (2.1.300); hook output escapes `<system-reminder>` (2.1.292); SessionEnd timeout variable fixed (2.1.268); `SessionEnd` reason `bypass_permissions_disabled` removed (2.1.234).
- **Sessions/background**: `claude attach/logs/stop/respawn/rm` in `--help` (2.1.251), by name (2.1.290); `--resume <id> --bg` keeps the id (2.1.257); `--bg` asks workspace trust first (2.1.281); resume command printed at exit uses the id (2.1.299); concurrent sessions no longer overwrite each other's `~/.claude.json` (trust, MCP state) (2.1.259).
- **MCP**: `CLAUDE_CODE_MCP_STARTUP_WAIT_MS` (2.1.274); `claude mcp get` hides plugin stdio command/env (2.1.285); an unreadable `managed-mcp.json` keeps exclusive control (2.1.271); stdio servers no longer get an empty `AWS_BEARER_TOKEN_BEDROCK` (2.1.317).
- **Limits**: retry-watchdog variables (2.1.292–2.1.305, §2.7); quota notification types (2.1.234); usage-limit auto-continue capped at 3 (2.1.314).
- **Models/effort**: Claude Fable 5.1 (`claude-fable-5-1`) default Fable (2.1.257); `maxEffortLevel` (2.1.267); `CLAUDE_CODE_SUBAGENT_EFFORT` (2.1.316); subagents on another model use that model's effort (2.1.317).
- **Deprecated / removed**: fast mode for Opus 4.8 deprecated 2026-10-30, **removed 2026-11-30**, then returns an error with no fallback (2.1.316); `TaskOutput` tool removed (2.1.277); 200-subagent cap removed (2.1.224); `--sandbox/--no-sandbox` removed from `claude remote-control` (2.1.300); "mod" API changes: `bold` deprecated for `weight` (2.1.299), mod hook error handling (2.1.301, 2.1.304).

## 3. Codex CLI 0.166.0 (installed `/opt/homebrew/bin/codex`, Caskroom 0.166.0)

Sources: `S` = source tarball of tag `rust-v0.166.0` (https://github.com/openai/codex/tree/rust-v0.166.0, paths under `codex-rs/`);
`D` = https://developers.openai.com/codex/<page>.md (fetched 2026-11-04; docs track the latest build, not pinned to 0.166.0);
`R` = GitHub release notes https://github.com/openai/codex/releases/tag/rust-vX; `H` = `codex ... --help` on the installed binary;
`P` = my probe: throwaway herdr session `probe-codex`, scratch `CODEX_HOME` copy (no touch of `~/.codex`), stub stdio MCP server,
interactive `codex` in a pane, `/quit`, then `herdr --session probe-codex server stop`. No `codex exec`, no model prompt sent.
`verified` = seen in installed binary/help/files/probe/source at the tag; `docs only` = only in a doc or release note.

### 3.1 Interactive start with model, effort, first prompt
- `codex [OPTIONS] [PROMPT]`: the positional PROMPT is the first user prompt of the interactive TUI. verified (H)
- Model: `-m/--model <MODEL>` or `-c model="..."`; reasoning effort has no dedicated flag: `-c model_reasoning_effort="high"` (levels
  advertised per model, e.g. low/medium/high/xhigh/max/ultra). verified for `-m` (H); effort key docs only (D config-reference, `model_reasoning_effort`).
  Local config uses `model = "gpt-6.1-terra"`, `model_reasoning_effort = "xhigh"`, and a recorded turn showed `effort:"high"` in `turn_context`
  (so the effective effort is per-turn in the rollout). verified (`~/.codex/config.toml`, rollout `turn_context.payload.effort`).
- `-c key=value` takes dotted paths, value parsed as TOML, falls back to raw string; repeatable. verified (H). `--enable/--disable <feature>` = `-c features.<f>=true/false`. verified (H)
- `-p/--profile <name>` layers `$CODEX_HOME/<name>.config.toml` over the base config (profile v2 = separate files). verified (H "CONFIG_PROFILE_V2"); D config-reference.
- Other start flags seen: `-C/--cd <DIR>`, `--add-dir`, `-s/--sandbox`, `-a/--ask-for-approval` (`on-request|never` only listed), `--approve-for-me`,
  `--dangerously-bypass-approvals-and-sandbox`, `--dangerously-bypass-hook-trust`, `--worktree`, `--no-alt-screen`, `--no-daemon`, `--remote <ADDR>`,
  `--strict-config`, `-i <FILE>`, `--search`, `--oss`. verified (H)
- In-session: `/model` and `/permissions` accept inline args to set model, effort, speed, profile (0.164.0, R rust-v0.164.0). docs only
- `-c` overrides given at several subcommand levels are preserved (0.164.0, R). docs only
- Startup draft: the composer accepts typing while the TUI initialises (0.148.0, R). docs only
- Interactive `codex` may attach to a shared local background app-server ("daemon"): auto-start for eligible sessions since 0.157.0 (R rust-v0.157.0),
  bypass with `--no-daemon` (0.156.0). Feature `daemon_auto_start` is `stable true` in `codex features list`. verified (features list). In my probe (custom
  `CODEX_HOME`) the MCP child's parent pid was the foreground `codex` process, i.e. no daemon was involved (P). A user daemon `codex app-server --listen unix://`
  (pid from `~/.codex/packages/app-server-daemon/releases/0.166.0-.../bin/codex`) was running at the same time. verified (ps)

### 3.2 MCP servers: registration, env, cwd
- Registration: `[mcp_servers.<name>]` in config.toml; `codex mcp add <name> [--env K=V] -- <cmd...>` / `--url`; `codex mcp list|get|remove|login|logout`;
  per launch with `-c mcp_servers.<name>.command="..."`, `.args=[...]`, `.env={K="v"}`, `.env_vars=[...]`. verified (H for add/list; P for `-c` form, it started the server).
- Stdio keys: `command`, `args`, `env` (set literally), `env_vars` (names to forward; `{name=..., source="remote"}` objects exist), `cwd`, `startup_timeout_sec`
  (default 10), `tool_timeout_sec` (default 60), `required`, `enabled_tools`, `disabled_tools`. Source `config/src/mcp_types.rs` (lines ~251-450); D mcp.md L262-325. verified (S)
- **Env: allowlist, not inherited.** The launcher starts from `DEFAULT_ENV_VARS` = `HOME LOGNAME PATH SHELL USER __CF_USER_TEXT_ENCODING LANG LC_ALL TERM TMPDIR TZ`
  (unix) plus names in `env_vars`, plus custom-CA vars, then the config `env` map; vars in `NON_INHERITABLE_ENV_VARS` (CODEX_EXEC_SERVER_NOISE_AUTH_TOKEN,
  NODE_REPL_AUTH_TOKEN, ...) are dropped. S `rmcp-client/src/utils.rs:16-58,163-176`, `protocol/src/shell_environment.rs:14-27`. verified (S)
  - P result: the stub saw `HOME LANG LOGNAME PATH SHELL TERM TMPDIR USER __CF_USER_TEXT_ENCODING`, `EXTRA` (from `env`), `STUB_OUT` (from `env`), `PROBE_MARK`
    (only because named in `env_vars`). It did **not** see `HERDR_*`, `OBOR_*`, `CODEX_THREAD_ID`, or other pane variables. verified (P)
  - P oddity: it also saw `CPATH`, `LIBRARY_PATH`, `MANPATH`, `SDKROOT` (Xcode values different from the codex process's own `MANPATH`). Not in the source allowlist;
    origin unexplained (possibly the shell-snapshot/profile path in `exec-server`). verified that they appear; cause unverified.
  - `PATH` seen by the server is prefixed with `$CODEX_HOME/tmp/arg0-macos/codex-arg0XXXX` and the Caskroom `codex-path` dir (codex adds its own helper dirs). verified (P)
- **cwd:** `cwd` key if set, else a fallback cwd. S `rmcp-client/src/stdio_server_launcher.rs:459-472` (`command.current_dir(&cwd)`). In P the fallback was the
  session cwd (the pane dir `.../cdxprobe/work`). verified (P, S)
- Process: spawned as a child of the interactive `codex` process (ppid = that pid), new process group, only stdio fds kept (`DescriptorPolicy::Explicit` on unix).
  One server process per codex process/session (the stub wrote its file once, at TUI start, before any prompt, so servers start eagerly at launch). verified (P, S lines 470-480).
  The child exited when codex quit. verified (P: `ps` showed it gone after `/quit`).
- Shared vs per session: feature `mcp_connection_sharing` exists, `under development false`. verified (features list). MCP tool calls "stay bound to their thread" (0.147.0 R). docs only.
- 0.147.0: optional MCP servers start without blocking turns; `features.mcp_2026_07_28` opt-in protocol; 0.151.0 grace period for optional server tool discovery;
  0.152.0 server names may contain `: @ / .`; 0.161.0 `/mcp login <name>`. R. docs only
- `codex mcp-server` entry point removed in 0.154.0 (R). docs only

### 3.3 Hooks
- Enabled by default; `[features] hooks = false` turns off (`codex_hooks` deprecated alias). D hooks.md L313-324; `codex features list` shows `hooks stable true`. verified
- Locations (all loaded, merged, concurrent): `~/.codex/hooks.json`, `~/.codex/config.toml` `[hooks]`, `<repo>/.codex/hooks.json`, `<repo>/.codex/config.toml`;
  project-local ones only when the project layer is trusted; plugin-bundled `hooks/hooks.json`; managed `requirements.toml` hooks. D hooks.md L31-60, 326-411. S `hooks/src/engine/discovery.rs:390-394`.
- **Trust review:** a non-managed hook does not run until its hash is trusted (new/changed hooks are skipped until reviewed); `--dangerously-bypass-hook-trust` skips it for one run. D L63-79; H. verified (flag in H)
- Events (schemas in S `hooks/schema/generated/*.json`): SessionStart, SessionEnd, SubagentStart, SubagentStop, UserPromptSubmit, PreToolUse, PermissionRequest,
  PostToolUse, PreCompact, PostCompact, PreModelRequest/PostModelRequest, Stop, Interrupt (0.150.0). verified (S file list); D L650-1140.
- Stdin: one JSON object. Common: `session_id`, `transcript_path` (nullable), `cwd`, `hook_event_name`, `model`; with `permission_mode` on most; turn events add `turn_id`.
  `SessionStart.source` in `startup|resume|clear|compact|fork`. `Stop`: `stop_hook_active`, `last_assistant_message`. `SessionEnd.reason` is always `other`.
  `Interrupt`: `turn_id`. verified (schema property lists via `jq`, S). Hook transcripts "not a stable interface" (D L492).
- Process: `shell -lc <command>` (default shell program), `current_dir(session cwd)`; env = the codex process environment plus per-event additions (not probed). S
  `hooks/src/engine/command_runner.rs:281,444-470`; D L196 "Commands run with the session cwd". Exact hook env verified only by source reading, not probed.
- Timeouts: default 600 s (SessionEnd has its own default), `Interrupt` 1 s (1-3 s allowed). S `discovery.rs:965-968`; D L184, L1131. 0.148.0: timed-out hook process trees are terminated, async hooks (`"async": true`) and `mcp_tool` handlers supported. R. D L572-640.
- Stop: exit 0 with JSON `{"decision":"block","reason":...}` (or exit 2 + stderr) makes codex continue with `reason` as a new prompt. Plain text stdout invalid. D L1088-1120.
- `prompt` and `agent` handler types are parsed but skipped. D L195.
- Legacy `notify = [argv...]` in config.toml still works: the program is run with a JSON string appended as the last argument, type `agent-turn-complete`
  with `thread_id, turn_id, cwd, client, input_messages, last_assistant_message`; marked TODO-remove. S `hooks/src/legacy_notify.rs:11-60`. verified (S). This machine's config sets `notify` to the Computer Use client (`turn-ended`). verified (config.toml)
- Plugin API: `plugin_hooks` flag is `removed`; plugins now bundle hooks via manifest. verified (features list).

### 3.4 Session ids, resume, transcripts
- Session/thread id is a UUIDv7-style string (e.g. `01a1a605-1578-7153-87dd-65fa9e5da747`); `session_id` == thread id. verified (rollout session_meta `id`==`session_id`)
- Where they live (CODEX_HOME = `~/.codex`):
  - Rollouts: `sessions/YYYY/MM/DD/rollout-<local-timestamp>-<id>.jsonl`; archived ones in `archived_sessions/`. verified (ls)
  - Line shape: `{timestamp, ordinal, type, payload}`; types seen: `session_meta` (id, cwd, cli_version, originator `codex-tui`, source `cli`, git, base_instructions),
    `turn_context` (model, effort, cwd, approval_policy, sandbox_policy, turn_id), `event_msg` (payload.type: `task_started`, `task_complete`, `item_completed`,
    `token_count`, `thread_settings_applied`, `turn_aborted`, ...), `response_item`, `token_usage_record`, `world_state`. verified (jq on a 2026-11-04 rollout)
  - `session_index.jsonl`: `{id, thread_name, updated_at}` per named thread. verified (head)
  - `state_5.sqlite` table `threads` (id, rollout_path, cwd, title, model, reasoning_effort, tokens_used, archived, name, first_user_message, source, ...). verified (`.schema`)
  - Without any turn, a TUI start+`/quit` wrote **no** rollout file (my scratch home had no `sessions/` dir) even though `Session ID: <uuid>` was printed. verified (P)
  - Legacy rollouts are migrated to paginated history (`codex migrate-rollouts`, 0.147-0.148 R); format not stable.
- Resume/fork: `codex resume [SESSION_ID|name] [PROMPT]` (`--last`, `--all`; `--last` scoped to cwd unless `--all`), `codex fork [SESSION_ID]` (`--last`), `codex exec fork` (0.148.0).
  `SESSION_ID` is a UUID or exact session name (UUID wins). Resume restores the saved cwd, permission profile and approval policy (0.148-0.163 R). verified (H) / R docs only for restoration.
- Also: `codex archive|unarchive|delete <id|name>`; threads can be archived before first turn (0.159.0 R).
- Finding the id of a running session: (a) SessionStart/other hook stdin `session_id` + `transcript_path`; (b) `CODEX_THREAD_ID` (and `CODEX_SESSION_ID`) set for
  model-spawned commands (const in S `protocol/src/shell_environment.rs:6-7`; herdr's script reads `CODEX_THREAD_ID`); NOT present in MCP server env (P);
  (c) printed as `Session ID: <uuid>` on exit (P); (d) `state_5.sqlite` `threads` by cwd/updated_at; (e) `session_index.jsonl`. (a),(c),(d),(e) verified; (b) verified in source + herdr script.

### 3.5 Delivering text to a running session without typing
- `codex queue --thread <UUID|exact name> --message <TEXT> [--resume] [--remote ADDR] [-p profile]`: queue a text-only message for an existing session (added 0.149.0; `--resume` added 0.166.0).
  verified (H, S `cli/src/queue_cmd.rs`). Messages persist in `$CODEX_HOME/queue_1.sqlite` (`queued_items(thread_id, payload_json, queue_order, ...)` + revision trigger). verified (`.schema`).
  "Queued messages now wake idle sessions reliably" (0.149.0 R). D cli reference L52-66 says `--resume` needs a running local daemon or `--remote` server.
  **Not verified:** whether a TUI running in its own in-process server (as in my probe) consumes the queue; docs frame it around the daemon/remote server. I did not run it.
- App-server JSON-RPC (`codex app-server --listen stdio:// | ws://IP:PORT | unix://[PATH]`; WS experimental/unsupported per D): `turn/start`, `turn/steer`
  (needs `expectedTurnId`), `turn/interrupt`, `thread/inject_items`, `thread/queue/add|list|update|delete|reorder` (marked experimental in S `app-server-protocol/src/protocol/common.rs:646-670`). D app-server.md L1064-1175; S verified for queue method names.
- `codex --remote ws://|wss://|unix://` attaches the TUI to such a server (`--remote-auth-token-env <VAR>` for bearer tokens). verified (H); D app-server.md L10-37.
- `codex remote-control [start|stop|pair]` = managed daemon with remote control (experimental). verified (H); D L81-96. `codex agents` = command center over the shared daemon (no `--no-daemon`). D L45-50.
- In the TUI, Tab while working queues a follow-up; a follow-up typed during a turn steers it (0.163.0; `instant_interrupt` opt-in 0.159.0). D L388; R. docs only
- `codex exec ...` also exists but is out of scope (headless).

### 3.6 Quitting cleanly
- `/quit` or `/exit` (+Enter) exits; Ctrl+C also "closes the session". D cli reference L391, L1077-1085. `/quit` verified (P): process exited, exit status `0`, MCP child exited, and the shell printed `Session ID: <uuid>` as the last line.
  Whether the exit summary adds a `codex resume <id>` line is described by 0.153.0 "Clarify resume guidance in exit summaries" (R); in my idle probe only the Session ID line appeared (no turn run). verified for the idle case.
- Ctrl+C semantics (interrupt turn vs exit, double press): not verified.
- `SessionEnd` hook runs on normal close (and after 30 min idle with no connected client when on a daemon). D hooks.md L688-700.
- Offline `/quit` handling fixed in 0.166.0 (R). docs only

### 3.7 Rate limits and errors
- Rollout `event_msg.payload.type=="token_count"` carries `rate_limits`: `{limit_id, primary:{used_percent, window_minutes:300, resets_at}, secondary:{used_percent, window_minutes:10080, resets_at}, credits:{...}, plan_type, rate_limit_reached_type}`. verified (rollout 2026-11-04). This is machine-readable remaining quota per turn.
- Error enum `CodexErrorInfo`: ContextWindowExceeded, SessionBudgetExceeded, UsageLimitExceeded, RateLimitExceeded, FlexUnavailable, ServerOverloaded, ModelNotFound, PolicyCapacityUnavailable, CyberPolicy, BioPolicy, TooManyDenials, HttpConnectionFailed{status}, ResponseStreamConnectionFailed, InternalServerError, Unauthorized, BadRequest, InvalidPrompt, SandboxError, ResponseStreamDisconnected, ResponseTooManyFailedAttempts, ActiveTurnNotSteerable. S `protocol/src/protocol.rs:2052-2092`. verified (S)
- Over app-server these appear in the `error` notification (`codexErrorInfo`, `willRetry`); a failed turn ends `status:"failed"`. D app-server.md L1464-1475. docs only
- TUI: rate-limit banners with actions for usage/credits/reset/plans (0.152.0 R); earlier warning at <50% of the ~5h window for Plus/Team (0.153.0 R). docs only
- Retry: `Retry-After` honoured for Responses/WebSocket failures (0.161-0.162 R); turns reconnect through temporary provider outages (0.148.0 R). docs only
- Hooks: there is no StopFailure-style event; `Stop` runs at normal turn end. Interrupted turns write `event_msg turn_aborted {reason:"interrupted", duration_ms}` (2 seen) and fire the `Interrupt` hook. verified (rollout), D.
- In this machine's rollouts the literal `usage_limit_exceeded` string occurred only inside tool output text (my first grep was a false positive); no real error events were found in sessions since 2026-08. verified (jq over event_msg types).

### 3.8 Changelog items (2026-08-04 to 2026-11-04; 0.146.1 to 0.166.0, plus 0.167/0.168 alphas in flight)
Use or watch:
- 0.149.0 `codex queue`; `codex agents` dashboard; 0.166.0 `queue --resume`.
- 0.148.0 async command hooks + MCP-tool hooks; 0.150.0 `Interrupt` hook; 0.148.0 `codex exec fork`; TUI resume picker archive/restore.
- 0.153.0 TUI reconnects automatically after an external app-server drop and keeps uncertain/queued submissions paused (0.160.0: resumes unsent queued messages after reconnect).
- 0.154.0/0.155.0 daemon on Windows, `codex app-server daemon update`; 0.156.0 `/daemon`, `--no-daemon`; 0.157.0 automatic background-server startup for eligible interactive sessions.
- 0.154.0 `--worktree`; 0.156.0 worktrees enabled by default; 0.162.0 worktree tools.
- 0.153.0 app-server thread metadata exposes `model` and `reasoningEffort`.
- 0.164.0 `/model` and `/permissions` inline args; 0.163.0 steer by typing a follow-up while responding.
- 0.160.0/0.162.0 sessions outside a project use workspace defaults; resumed sessions restore saved permissions (0.163.0 also network approvals).
- 0.166.0 `codex doctor --network-only`; `codex doctor` exists. verified (H lists `doctor`).
Deprecated/removed:
- 0.147.0 `codex exec --full-auto` removed (use `--sandbox workspace-write`); 0.154.0 `codex mcp-server` removed; 0.159.0 `tui.prompt_suggestions` removed;
  0.163.0 bundled patched `zsh` removed (migration notice for retired shell flags); `codex_hooks` flag deprecated alias of `hooks`; `plugin_hooks`, `remote_control`, `collaboration_modes` features show `removed`. R / verified (features list).
- Legacy `notify` marked for removal in source (TODO comment). verified (S).
Sources: R rust-v0.147.0 ... rust-v0.166.0 (bodies fetched with `gh release view`).

### 3.9 herdr 0.9.3 integration with Codex on this machine
- `~/.codex/hooks.json` contains only one hook: `SessionStart` -> `bash ~/.codex/herdr-agent-state.sh session`, timeout 10, `type: command`. verified (read)
- `~/.codex/herdr-agent-state.sh` (header: `HERDR_INTEGRATION_ID=codex`, `HERDR_INTEGRATION_VERSION=8`): reads hook stdin to a temp file; exits unless `HERDR_ENV=1`, `HERDR_SOCKET_PATH`, `HERDR_PANE_ID` set and `python3` exists;
  ignores events other than SessionStart; skips if `transcript_path` empty or if `CODEX_THREAD_ID` is set and differs from the hook's `session_id` (nested codex); sends one `pane.report_agent_session`
  (`source "herdr:codex"`, `agent "codex"`, `seq` ns timestamp, `agent_session_id`, `session_start_source`) over the Unix socket with a 0.5 s timeout, errors swallowed. verified (read)
- So herdr 0.9.3 gets Codex session identity from the hook, and status (working/idle/blocked) from screen detection; `[features] hooks = true` is not set in `~/.codex/config.toml` (hooks default on). verified (config.toml has no `[features]`)
- herdr docs v0.9.3 integrations.mdx "Codex" says install writes `herdr-agent-state.sh`, updates `hooks.json`, ensures `[features] hooks = true`. verified differs locally (no `[features]` table present). herdr v0.9.5 release notes: "Herdr no longer installs Codex turn hooks, and Codex status comes from screen detection again" (https://github.com/herdrdev/herdr/releases/tag/v0.9.5). docs only
- herdr 0.9.4 notes: Codex finished turns reported through its integration under `--no-daemon`; startup update screen shows blocked. docs only. Open upstream issue: "#5775 Codex model picker ending 'esc to go back' is reported idle" (https://github.com/herdrdev/herdr/issues/5775). docs only
- herdr docs say Codex falls back to `unknown` (not `idle`) when no screen rule matches and may stay `unknown` after a response. v0.9.3 agents.mdx "Blocked state". docs only

### 3.10 Not verified
- Hook process env contents (only source-read); whether `codex queue` reaches a TUI that owns an in-process server; Ctrl+C single/double semantics; the origin of `CPATH/LIBRARY_PATH/MANPATH/SDKROOT` in the MCP env; a real `StopFailure`-like path (none found).

## 4. opencode (v2.2.0 installed; v2.2.5 is the newest release, 2026-10-30)

Marks: **[V]** verified here (installed binary, `--help`, config files, a probe, the live API of a probe server);
**[D]** docs only; **[O]** stated in obor's own `~/.obor/kit/opencode/obor.js` header comments, which record earlier
probes (T-578, T-703: opencode 2.2.0) that I did not re-run. Docs base: `https://opencode.ai/v2/docs/…` (fetched
2026-11-04). `model/variant selection` is in `docs/research/opencode-model-selection.md` and is not repeated.

**Probes run (2026-11-04).** Throwaway herdr session `probe-opencode` (stopped at the end; default session not touched),
cwd = a scratch dir, scratch `XDG_{CONFIG,DATA,STATE,CACHE}_HOME`, config injected with `OPENCODE_CONFIG_CONTENT`, a
python stub stdio MCP server that dumps `env`/cwd/ppid and answers `initialize`/`tools/list`. No model prompt, no
`opencode run`.
- **P1**: `opencode --standalone` in a pane (`PROBE_MARK=pane_env` exported in the pane).
- **P2**: `opencode serve --port 49555` in pane A (`SERVER_MARK=server_env`) plus `opencode --server http://127.0.0.1:49555`
  in pane B (`PANE_MARK=pane2_env`), then `opencode api` calls against the P2 server.

### 4.0 Process model
- `opencode` (main command) is a TUI client. By default it discovers or starts one shared background server per user
  (`opencode serve --service`), which owns sessions, config, MCP, permissions and tool execution. `--standalone` runs a
  private server instead; `--server <url>` connects to a given one. [D] cli (`/v2/docs/cli/`: "Background service"). [V]
  `opencode --help` (flags `--standalone`, `--server`).
- [V] The shared service here is `opencode serve --service`, pid 2883, ppid 1, started 2026-10-28 09:42; its url, pid and a
  password are in `~/.local/state/opencode/service.json` (and `~/.config/opencode/service.json` holds a `password`).
  Its environment has `TERM_PROGRAM=ghostty` and **no** `HERDR_*`, i.e. it carries the env of whichever process started
  it first, not of the pane that later attaches.
- [V] P1: `--standalone` spawns `opencode serve --stdio --port 0` as a **child of the TUI process** (ps: TUI pid 47069 →
  server pid 47072 → MCP stub pid 47093).
- `opencode2` is `~/.opencode/bin/opencode2`, a 47-byte `sh` script `exec "$(dirname "$0")/opencode" "$@"` [V]. "OpenCode 1
  and OpenCode 2 both use the `opencode` command and are no longer installed side by side by default; the V2 curl
  installer replaces the V1 binary" [D] `/v2/docs/migrate-v1/`. v1 is still released in parallel (v1.19.5 is GitHub
  "Latest" on 2026-10-28) [V] `gh release list -R anomalyco/opencode`.
- `opencode service start|restart|status|stop|get|set|unset`; `opencode service set disabled true` makes private servers
  the default and stops the service; `opencode reload` reloads config [V help, D troubleshooting].

### 4.1 Starting interactively
- [V] `opencode --help` for the main (TUI) command lists only: `[<directory>]`, `--standalone`, `--server`, `--auto`
  ("auto-approve permissions that are not explicitly denied"), `--continue/-c`, `--session/-s <id>` ("continue, or
  create if it does not exist"), `--prompt <text>`. **No `--model`, `--agent` or `--variant` on the TUI command.**
- [V] `opencode mini --help` (the minimal interactive UI) has `--model/-m provider/model`, `--agent`, `--prompt`,
  `--continue`, `--session`, `--fork`, `--replay`, `--replay-limit`. `run` takes `#variant` per the earlier doc
  (`--help` of `run` is denied by the obor guard, so not re-read here).
- [D] Config `model` = `provider/model`; "the root default currently does not retain a `#variant`; agent and command model
  references can select one" (`/v2/docs/config/`). `default_agent` picks the primary agent. Agent/command model
  refs use `provider/model#variant` (`/v2/docs/migrate-v1/`).
- [V] `opencode --session <existing-id> --server …` opens that session (P2: the TUI showed the session's queued
  text). `--session` with a non-existent id creates it per help text [D].
- [V] A fresh TUI shows a home screen with the composer, `Build · <model>` footer and an MCP count (`1 MCP /mcps`) at
  the bottom; it also shows `/update to install v2.2.5`.
- `--auto` and `OPENCODE_CLI_CONFIG_CONTENT` / `cli.json` (TUI prefs: theme, sidebar, thinking, `plugin`) [V strings,
  `~/.config/opencode/cli.json`]. TUI preferences moved from `tui.json(c)` to one global `cli.json` (auto-migrated) [D migrate-v1].
- Env names present in the binary [V `strings`]: `OPENCODE_CONFIG`, `OPENCODE_CONFIG_CONTENT`, `OPENCODE_CONFIG_DIR`,
  `OPENCODE_CLI_CONFIG_CONTENT`, `OPENCODE_CONFIG_PROJECT_DISABLE`, `OPENCODE_DISABLE_PROJECT_CONFIG`, `OPENCODE_DB`,
  `OPENCODE_PASSWORD`, `OPENCODE_SERVER_PASSWORD`, `OPENCODE_SESSION_ID`, `OPENCODE_DISABLE_AUTOUPDATE`,
  `OPENCODE_LOG_LEVEL`, `OPENCODE_PRINT_LOGS`, `OPENCODE_CLAUDE_PATH` (v2.2.3 release note). Meaning of each beyond the
  ones named in docs/probes is not verified; `OPENCODE_CONFIG_CONTENT` and `OPENCODE_CONFIG_DIR`/XDG vars work as
  expected in P1/P2 [V].
- Config discovery: system dir (`/Library/Application Support/opencode` on macOS) > project `opencode.json(c)` and
  `.opencode/opencode.json(c)` (walking up from cwd) > `~/.config/opencode/opencode.json(c)` [D config].

### 4.2 MCP
- **Registration** [D `/v2/docs/mcp-servers/`]: `opencode.json(c)` → `mcp.servers.<name>` (v2 shape; v1's flat `mcp.<name>`
  and `enabled` are migrated: `disabled` is the inverse). Local: `{type:"local", command:[exe,…args], cwd?, environment?{}, disabled?, timeout?, codemode?, protocol?}`.
  Remote: `{type:"remote", url, headers?, oauth?|false}`. `opencode mcp add <name> [--global] [--env K=V]… -- <cmd…>` or
  `--url … [--header …]`; project config by default, `--global` for `~/.config/opencode`. `opencode mcp list|auth|logout`.
  A higher-precedence config replaces the **whole** server object of the same name. `{env:NAME}` substitution in config strings.
- [V] The user's `~/.config/opencode/opencode.json` registers `herd` and `obor` this way (v2 shape, `environment` with
  `HERD_NO_SELF_HOST` / `OBOR_NO_SELF_HOST`, `type: "local"`).
- **Env** [D: "`environment`: String variables added to OpenCode's inherited process environment"]; **[V] P1 and P2
  confirm**: the stub got the *entire* environment of the process that runs the opencode server (including `HERDR_*`,
  `OBOR_ROLE`, `CLAUDE_*` of the launching shell, `OPENCODE_CONFIG_CONTENT` itself) plus the `environment` map. Not stripped.
  No `OPENCODE_SESSION_ID` was set on the MCP process (not set at startup; a session id is not known then).
- **Whose env** [V P2]: with `--server`, the stub's env was the **server's** (`SERVER_MARK=server_env`,
  `HERDR_PANE_ID=w1:p1` = the server's pane; `PANE_MARK` of the attaching TUI absent). With `--standalone` (P1) the server
  is the TUI's child, so it is the pane's env (`PROBE_MARK=pane_env`, `HERDR_PANE_ID=w1:p1`). With the shared background
  service it is the service's env (see §0: no pane identity).
- **cwd** [V]: the stub's cwd was the project directory of the location (P1/P2 both: the directory the TUI/server was
  started in; they coincided, so "location dir" vs "server cwd" is not separated by the probe). [D] `cwd` option: "Relative
  paths resolve from the workspace, which is also the default".
- **Per session or shared** [V]: not per session. One stub process (one `initialize` + `tools/list`, no `tools/call`) per
  location/server, started when the client opened the directory (the TUI was already up with "1 MCP" before any session
  existed); P2 `GET /api/mcp` → `{"location":{"directory":…},"data":[{"name":"stub","status":{"status":"connected"}}]}`.
  The service runs one instance per location (directory) [O]; v2.2.5 fixes "Idle locations no longer restart their MCP servers
  right after shutting down" (#60668) [D release]. The stub was killed when the TUI exited (P1; no stragglers in `ps`) [V].
- **Identity of the calling session** [D `/v2/docs/mcp-servers/` "Context"]: for session-driven calls opencode sends the
  session id in `tools/call` `params._meta["ai.opencode/sessionID"]` (stdio and Streamable HTTP, direct tools and Code
  Mode); can be absent; identifies the opencode session, not the MCP transport session; "do not use it by itself for
  authentication". Not exercised (no model call was made).
- **MCP tool exposure** [D]: default is **Code Mode** (tools grouped as `tools.<server>.<tool>(...)`); `codemode:false`
  exposes native tools named `<server>_<tool>` (non-alphanumerics → `_`). Permission actions match the normalized name.
  MCP prompts become slash commands `<server>:<prompt>`. `mcp.timeout` {startup 30 s, catalog 30 s, execution 12 h}.
  `protocol`: `legacy` default (client sent `protocolVersion 2025-11-25`, `clientInfo opencode 2.2.0`, `capabilities:
  {elicitation:{form,url}, roots:{}}` [V P1 rpc log]), `auto`, `2026-07-28`.
- Runtime control [V openapi list]: `GET /api/mcp`, `PUT|DELETE /api/experimental/mcp/{server}`, `POST …/connect|disconnect`,
  `GET /api/mcp/resource`. Managed policy has `mcp.use` since v2.2.3 (old `mcp:` prefixes auto-converted) [D release v2.2.3].

### 4.3 Plugins and hooks
- **Where plugins load** [D `/v2/docs/build/plugins/`]: `.opencode/plugins/` (auto), `plugins` array in `opencode.json(c)`
  (npm names, `./path`, `file://`, `{package, options}`); one entry loads every part (server `.`/`./server`, TUI `./tui`,
  GUI, `./rpc`); a TUI-only package goes in `cli.json` `plugin`. [V] here: `~/.config/opencode/plugins/{herd.js (symlink),
  herdr-agent-state.js, obor.js (symlink)}` are auto-loaded files; `cli.json`/`tui.jsonc` list `./herdr-tui-session.js`.
- **Process** [D `/v2/docs/build/plugins/server`]: the server part "runs in the OpenCode server" (so: the shared service, or
  the `serve --stdio` child under `--standalone`); `setup(ctx)` once per **location**, `ctx.location.directory` is that
  location's cwd; `ctx.app` `{name, version, channel}`; may return a cleanup function. A shared service runs one plugin
  instance per location in one process, and `ctx.event.subscribe` delivers **all locations'** events [O; docs: "This is
  the plugin instance's location, not the location of every session it can access or event it receives"]. Env = that
  server process's env (§2 "Whose env").
- **Server hooks** [D]: `ctx.session.hook(name, cb)`: `prompt` (admission draft: text/files/skills, runs once at admission,
  not before each model call, retry-safe), `context`, `compaction`, `generate`, `title`, `model.request`, `http.request`,
  `http.response`, `experimental.ws.{handshake,send,receive}`, `retry` (set `event.decision = {retry, delay}`; `attempt`
  1 = initial request); `ctx.permission.hook("evaluate")` (set `effect`/`message`; explicit config deny is final and skips it);
  `ctx.shell.hook("create.before")` (`command, cwd, timeout, shell, env` mutable); `ctx.tool.hook("execute.before"|"execute.after")`
  (inspect/replace input; result/error). Hooks run in plugin order. Model/variant rewriting hooks: see the earlier doc.
  Other ctx: `ctx.session.{create,prompt,switchModel,context,instructions.entry.put|list|remove}`, `ctx.agent.transform`,
  `ctx.model.transform`, `ctx.tool.transform|list`, `ctx.storage`, `ctx.options`, `ctx.generate.text`.
- **Events** [D + V]: `ctx.event.subscribe({types, signal})` (async iterable; no `server.connected`) and the HTTP SSE
  `GET /api/event?types=…` carry the same names. List from the installed API doc: `session.created`, `session.renamed`,
  `session.deleted`, `session.forked`, `session.model.selected`, `session.agent.selected`, `session.inbox.enqueued|delivered|
  cancelled|delivery.changed`, **`session.execution.started|succeeded|failed|interrupted`**, `session.step.started|ended|failed`,
  `session.text.*`, `session.reasoning.*`, `session.tool.called|progress|success|failed`, `session.retry.scheduled`,
  `session.compaction.*`, `session.shell.started|ended`, `session.usage.updated`, `session.permissions`, `session.viewed`,
  `location.shutdown`, … plus `permission.asked|replied` and `form.created|replied|cancelled` (used by herdr's TUI plugin [V]).
  No `session.idle` in v2 [O]. `session.execution.*` events carry `data.sessionID` and (failed) `data.error`; they carry no
  `location` [O]; `session.created`, `session.inbox.enqueued`, `session.retry.scheduled` do [O]. A `session.created` with
  `data.parentID` is a child (subagent) session [O]. Event stream is "volatile by contract": a slow consumer overflows and the
  stream fails; events during a disconnect are missed [D `/api/event`].
- **TUI plugins** [D `/v2/docs/build/plugins/cli`]: run in the TUI process (= the pane process); `api.data.listen`,
  `api.client`, `api.ui.*`, `api.lifecycle.onDispose`. [V] herdr's TUI plugin (`~/.config/opencode/herdr-tui-session.js`,
  integration v13) uses `api.data.listen` for `permission.asked/replied`, `form.created/replied/cancelled`,
  `session.execution.started` (→working), `succeeded|interrupted` (→idle), `failed` (→blocked), and the v1 names
  (`session.status`, `session.idle`, `session.error`, `question.*`) in the same file; it reports to herdr's socket via `HERDR_PANE_ID`
  / `HERDR_SOCKET_PATH` from the pane env. V2 Mini and headless clients do not run the TUI plugin [D herdr integrations.mdx].
- **obor's own plugin** [V file exists, symlink `plugins/obor.js` → `~/.obor/kit/opencode/obor.js`; claims [O]]: maps
  `tool.execute.before` → pre-tool-use, a session's first event → session-start, `session.execution.started|succeeded|failed`
  → user-prompt-submit|stop|stop-failure, and a `prompt` hook for `OBOR_OPENCODE_MODEL`. Facts it records about the platform:
  no session-start event reaches a plugin set up after the session was made; transcript = the SQLite db (below); a 429 is retried
  (`session.retry.scheduled {attempt, at, error}`, up to 10 times, each wait ≤ min(retryAfterMs, 15 min)) before
  `session.execution.failed`.

### 4.4 Session ids, resume, storage
- [V] Ids look like `ses_e59f8d9beffeQaaDykZ6aMgaV6`; messages `msg_…` (P2 `POST /api/session` → `data.id`). `POST /api/session` accepts
  `{id?, parentID?, title?, agent?, model?{providerID,id,variant?}, location?, metadata?, permissions?}`; the server default
  location is its cwd; returns `data.projectID`, `location.directory`.
- [V] CLI: `--continue/-c` (last session), `--session/-s <id>` (continue or create), `mini --fork`. On exit the TUI prints
  `Session   <title>` and `Continue  opencode -s <id>` (P2, after ctrl+c in a session with content). A session opened through
  `--session` in a `--server` TUI re-shows its queued input [V].
- [V] `opencode session list|delete|export|import` (help). `list` = "top-level sessions in the current project, newest first".
  API: `GET /api/experimental/session/{id}/export` → `{info, messages}` [V P2]; import accepts transcripts from earlier releases [D].
- **Where** [V]: one SQLite database for all sessions, `opencode debug paths db` → `~/.local/share/opencode/opencode.db`
  (WAL files beside it; a scratch `XDG_DATA_HOME` gave its own `…/opencode/opencode.db`, created empty-ish at 11 MB on first
  start). `OPENCODE_DB` overrides; off the release channels the file is `opencode-<channel>.db` [O]. `opencode debug paths`
  prints data/config/cache/state/log/db (log: `~/.local/share/opencode/log/opencode.log`). Per-user state: `~/.local/state/opencode/{model.json,kv.json,prompt-history.jsonl,frecency.jsonl}` [V].
  There is no per-session transcript file [O].
- Session "active" listing: `GET /api/session/active` = foreground drains owned by this process [D; V returned `{"data":{}}`].

### 4.5 Delivering text to a running session
- [V] HTTP API on the server (the shared service, `serve`, or ...). Auth: **HTTP Basic, user `opencode`, password = the server
  password** (curl `-u opencode:$PW` → 200; empty user, Bearer, or no auth → 401) [V P2]; `opencode api` reads `OPENCODE_PASSWORD`,
  the TUI reads `OPENCODE_SERVER_PASSWORD` [V]. `opencode serve` prints `server listening on …` and `server password …` at start [V]. The shared
  service's url/password are in `~/.local/state/opencode/service.json` [V]. A `--standalone` server (`serve --stdio --port 0`) has
  no reachable port from outside [O; consistent with P1].
- [V P2] `POST /api/session/{id}/prompt {text, files?, agents?, skills?, metadata?, delivery?, resume?}` → `{"data":{id:"msg_…",
  type:"user", payload:{text}, delivery:"steer"}}`; with `"resume": false` the input is enqueued without starting a turn
  (no model call). `GET /api/session/{id}/inbox` lists durable pending inputs; `PATCH/DELETE …/inbox/{inboxID}` changes delivery /
  cancels [D]. Default `delivery` was `"steer"` [V]; the TUI showed the queued text in the session [V].
- [D openapi] Also: `POST /api/session/{id}/synthetic` (synthetic message), `/command` (run a slash command), `/shell`,
  `/interrupt?resume=` (interrupts only an execution **owned by this process**), `/compact`, `/model`, `/agent`, `/fork`, `/background`,
  `PUT /api/session/{id}/environment` (env for local shell commands), `POST /api/experimental/session/{id}/wait` (blocks
  until the agent loop is idle; 204), `GET …/context`, `…/message`, `…/diff`, `…/permission`, `…/form`;
  pending permissions: `GET /api/permission/request`, reply `POST /api/session/{id}/permission/{requestID}/reply`; forms likewise.
  PTY API `/api/pty*` and `/api/experimental/persistent-pty*` (WebSocket) with `terminal/read` ("last physical rows of the session's
  most recently controlled terminal"). 169 operations in the docs' API reference (`/v2/docs/api`, "Experimental HttpApi
  surface"); the live probe server's `GET /api/mcp`, `/api/location`, `/api/info`, `/api/session*` matched.
- Plugin-side equivalent: `ctx.session.prompt/create` [D]. `session.inbox.*` events announce enqueue/delivery [D].
- `opencode attach` no longer exists as a subcommand in v2; use `opencode --server <url> [--session id]` [V help list]. herdr's
  `integration` still handles `opencode attach` args for v1 [V herdr-agent-state.js `ownsLocalLifecycle`].
- Typing-based delivery goes through herdr (`agent.prompt`) and is covered in the herdr section.

### 4.6 Quitting
- [V P1] `/exit` typed + Enter at the home screen: TUI exits to the shell, stub MCP process gone, no server leftovers (standalone). Exit
  status not captured in P1 (my `$?` check read `clear`'s). [V P2] one **Ctrl+C** in a session view: TUI exits at once with no
  confirmation, prints `Session <title>` / `Continue  opencode -s <id>`, shell `$?` = **0**. A Ctrl+C at the shell afterwards gives 130 (shell's own).
- With `--server`, quitting the TUI leaves the server and its MCP processes running [V P2: server pane kept running until stopped].
- Shared service keeps running after the TUI exits; `opencode service stop` [V help].
- Esc/`/interrupt` stops a running turn; the TUI keybind for exit is `/exit`, Ctrl+C [D keybinds not re-read].

### 4.7 Rate limits and errors
- [D+V strings] Turn-level: `session.execution.failed {sessionID, error: Session.StructuredError}`; `StructuredError {type, message,
  status?(100-599), response?{body}, retryAfterMs?}`; the binary contains `provider.rate-limit` [V strings]; obor lists the
  types as `provider.rate-limit, quota, free-limit, budget-limit, model-budget-limit, auth, content-filter, invalid-request,
  transport, timeout, internal` [O]. HTTP-level `ProviderRequestError {reason: authentication|rate_limit|content_filter|
  invalid_request|overloaded|provider, status, retryAfterMs}` [D openapi].
- Retries: `session.retry.scheduled {attempt, at, error}` precedes each retry; the plugin `retry` hook can veto or change
  delay/decision [D]; assistant messages carry `retry {attempt, at, error}` [D openapi]. A 429 does not end the turn until
  retries are exhausted [O]; v2.2.1 "Retry provider throttling correctly instead of reporting it as context overflow" [D release];
  v2.2.2 "Correctly typed and displayed Go and ChatGPT usage-limit errors; usage-limit messages with reset time" [D release].
- Context overflow: separate path (compaction), not a retry [D]. Failures of `session.step` emit `session.step.failed` [D].
- TUI: herdr maps `execution.failed` → blocked, and `permission.asked`/`form.created` → blocked [V herdr-tui-session.js].
- Logs: `~/.local/share/opencode/log/opencode.log` (`--print-logs` prints to stderr; server logs only with `--standalone` or `serve`) [V help, D troubleshooting].

### 4.8 Last ~3 months: what an orchestrator should know
(GitHub releases of `anomalyco/opencode`; 2026-08-04 → 2026-11-04. v2.0/v2.1 are not published as GitHub releases; v2 arrived
on top of the v1.x line, v1.18.x → v1.19.5 in this window [V gh release list]. Only v2.2.0–v2.2.5 notes were read in full.)
- **v2.2.0 (2026-10-27, installed)**: plugins gain keyed session instructions `ctx.session.instructions.entry.put/list/remove`
  (updates are appended at the next step instead of rewriting the system prompt, survive compaction); experimental image and
  decision generation APIs; per-provider prompt-cache TTLs; GitHub Action removed; TUI plugins need OpenTUI 0.7 (v2.2.3: 0.8).
- **v2.2.1**: `/goal <objective>` (session continues until a reviewer says done; `/goal pause|resume|clear`); opt-in
  `experimental.schedules` (`/schedule`, agent tools; `POST /api/experimental/schedule`); provider-throttling retry fix.
- **v2.2.3**: built-in **Claude Agent** provider (uses `claude auth login`; `OPENCODE_CLAUDE_PATH`); managed-policy split
  (`integration.use` vs new `mcp.use|skill.use|plugin.use`) — **breaking** for bare `integration.use` rules.
- **v2.2.4**: opt-in `experimental.artifacts`; schedules run under `opencode serve` (one fire across several server processes);
  persistent session PTYs; local service requests bypass env proxies.
- **v2.2.5**: MCP stdio messages > 10 MiB no longer disconnect; idle locations no longer restart MCP servers right after
  shutdown; `compaction.buffer` as a percentage; calls to a tool named `bash` run as `shell`.
- **Deprecations / renames** [D `/v2/docs/migrate-v1/`]: v1 plugins do **not** run in v2 (new `@opencode/plugin` API); v1
  `tui.json(c)` → `cli.json`; permissions `bash→shell`, `task→subagent`, `write/patch→edit`, ordered `permissions[]`;
  `external_directory` is **allowed by default** in v2 (v1 asked); todos off by default (`experimental.todo`); `mcp.<name>` →
  `mcp.servers.<name>` with `disabled`; singular config maps → plural (`agent→agents`, `command→commands`, `provider→providers`,
  `snapshot→snapshots`); agent `variant` joins model as `model#variant`; root `model` does not keep a `#variant`.
  `POST /api/experimental/generate` is a deprecated alias of `/generate/text` [D openapi].
- **Usable by an orchestrator**: HTTP API with Basic auth (§5) incl. `prompt resume:false`, inbox, `session wait`, `interrupt`,
  `export`, permission/form reply; `ai.opencode/sessionID` in MCP `_meta`; `cwd`/`environment` per MCP server; `OPENCODE_CONFIG_CONTENT`
  for per-process config; `opencode --server … --session` to open a pre-created session; `session.execution.*` events instead of
  `session.idle`; `opencode mcp list`; `opencode service`.
- Herdr (0.9.3 here) tested its opencode integration with "V1 1.18.29 or later and OpenCode V2 (beta 19242)" [D integrations.mdx];
  v2.2.0 behaviour matches what its v13 files expect [V files present, version 13 in all three].

### 4.9 herdr's opencode integration on this machine [V]
- `~/.config/opencode/plugins/herdr-agent-state.js` (server plugin, integration v13, "managed by herdr"): only acts when
  `HERDR_ENV=1`, `HERDR_SOCKET_PATH`, `HERDR_PANE_ID` are set **and** the process is `run` or `--mini`
  (`ownsLocalLifecycle`); reports `pane.report_agent` / `pane.report_agent_session` with `source=herdr:opencode`, `agent=opencode`,
  `agent_session_id`. For the TUI it is a no-op by design.
- `~/.config/opencode/herdr-tui-session.js` + `herdr-opencode/tui.js` (re-export): the V2 TUI plugin, registered in `cli.json`?
  No: `cli.json` here has no `plugin` key; `tui.jsonc` has `"plugin": ["./herdr-tui-session.js"]` (the v1 registration); herdr's doc says
  it registers v2 in `cli.json` — the file here does not show it, so whether v2.2.0 loads this plugin from `tui.jsonc` is **not verified**
  (open item in the earlier doc too). Report path: pane env → `HERDR_SOCKET_PATH` socket, session id = the pane's selected root session.
- herdr resume command for an opencode pane: `opencode --session <id>` (integration ≥ 5) [D session-state.mdx].

## 5. herdr 0.9.3 (protocol 22)

Scope: the herdr we run. `docs/research/herdr-core.md` already covers herdr-core (`hc`) and is not repeated.

Sources: `HD` = herdr docs at tag **v0.9.3** (`https://raw.githubusercontent.com/herdrdev/herdr/v0.9.3/docs/next/website/src/content/docs/<page>.mdx`; pages `socket-api`, `agents`, `agent-automation`, `session-state`, `cli-reference`, `integrations`, `plugins`, `add-herdr-support`, `troubleshooting`; the same set exists at v0.9.5 and was diffed). `RN` = GitHub release notes ([v0.9.4](https://github.com/herdrdev/herdr/releases/tag/v0.9.4), [v0.9.5](https://github.com/herdrdev/herdr/releases/tag/v0.9.5)). `#n` = an open or closed issue in herdrdev/herdr, **read as a report, not reproduced** (marked `issue text only`). `P-hd` = probes on a throwaway session (§1.3). `herdr api schema --json` (verified locally, 272 KB) lists every request and event name. Installed herdr is 0.9.3; the latest stable is 0.9.5 (2026-10-14) — fixes after 0.9.3 are listed in §5.9.

### 5.1 Model and transport

| Fact | Status | Source |
|---|---|---|
| Objects: workspace (`w1`) → tab (`w1:t1`) → pane (`w1:p1`); public pane ids are **workspace-qualified** and change when a pane moves to another workspace (the old `HERDR_PANE_ID` stays an alias for `--current`). A pane exists with or without an agent; an agent is the recognized process in a pane. `agent start` needs an existing shell pane and never creates layout. Creating a workspace creates its first tab and root pane; creation commands print JSON (`.result.workspace`, `.tab`, `.root_pane.pane_id`, `.result.pane.pane_id`). | verified (workspace create/list/pane list); rest docs | HD `agent-automation`, `cli-reference`; P-hd |
| Socket: newline-delimited JSON, one request per line `{"id","method","params"}` → `{"id","result"}` or `{"id","error":{"code","message"}}`. Default `~/.config/herdr/herdr.sock`; named session `~/.config/herdr/sessions/<name>/herdr.sock`. Resolution order: `--session` > `HERDR_SOCKET_PATH` > `HERDR_SESSION` > default. `ping` returned `{"type":"pong","version":"0.9.3","protocol":22,"capabilities":{live_handoff, endpoint_protocol_generation:1, surface_interest, health_check, ssh_agent_registration,…}}`. An unknown method returns `invalid_request` listing every valid method. | verified | P-hd; HD `socket-api` |
| `herdr api schema [--json]` prints the protocol schema bundled with the binary (protocol 22, schema_version 1; request, success/error response, event and subscription-event schemas); `herdr api snapshot` prints `session.snapshot`. The raw method list: server, notification, client.window_title, session.snapshot, workspace.*, worktree.*, tab.*, pane.* (incl. `pane.report_agent`, `pane.report_agent_session`, `pane.report_metadata`, `pane.release_agent`, `pane.wait_for_output`), layout.*, agent.* (`list,get,read,explain,send_keys,prompt,wait,rename,focus,start,view.set/clear`), events.subscribe/wait, integration.*, plugin.*. | verified (schema, names) | `herdr api schema --json`; HD `socket-api` |
| Compatibility: JSON clients "should ignore unknown fields and handle unsupported methods as normal errors"; the numbered binary protocol is for same-install operations. Servers keep running across a compatible client update; `herdr update --handoff` (experimental) keeps pane processes across a server replacement, but in-flight requests, waits and subscriptions may be interrupted. | docs only | HD `socket-api` (Protocol stability), `session-state` |
| Pane process env: herdr injects `HERDR_SOCKET_PATH`, `HERDR_ENV=1`, `HERDR_WORKSPACE_ID`, `HERDR_TAB_ID`, `HERDR_PANE_ID` (**authoritative** over caller `env`); `env` on `workspace create / tab create / pane split / layout.apply` adds vars to **that new root shell only**. In a named session the pane also got `HERDR_SESSION=<name>` and `TERM_PROGRAM=herdr`, `TERM_PROGRAM_VERSION=0.9.3` (not in the docs). A pane inherits the **server's** environment: the probe server was started under `env -i` and the panes had none of the starting shell's vars. | verified | P-hd (`env` inside the pane and in a stub MCP server) |

### 5.2 Panes, labels, workspaces and tabs

| Fact | Status | Source |
|---|---|---|
| `pane rename <id> <label>` (or `--clear`) sets `label` (visible in `pane get` as `label`; `name` stays `null`). **Pane label ≠ agent name**: `agent start <name>` / `agent rename` set the agent alias, which must match `[a-z][a-z0-9_-]{0,31}`, be unique among live agents, follows the terminal if the pane moves, and is cleared when the agent exits/is released/replaced. | verified (label); agent name rules docs | P-hd; HD `agent-automation` |
| `pane split` / `workspace create` / `tab create` do **not** steal focus unless `--focus`; without `--cwd` a new terminal follows `terminal.new_cwd` (source pane/workspace by default). `pane close` is immediate for CLI/API (the TUI confirms). `tab close` of the last tab closes the workspace; `workspace close` of a primary workspace with open linked worktrees needs `--group`. | docs only | HD `cli-reference` |
| `pane get/list` fields: `pane_id, terminal_id, workspace_id, tab_id, focused, label, agent, agent_status, agent_session{source,agent,kind,value}, cwd, foreground_cwd, terminal_title(_stripped), scroll{offset_from_bottom,…}, revision, state_change_seq`. `pane process-info` returns shell pid, foreground pgid and foreground processes (pid, name, argv, cwd). | verified (get/list) | P-hd; HD `socket-api` |
| Worktrees as workspaces: `worktree.create/open/remove/list` (checks out or creates the branch, returns workspace, tab, root pane); `worktree.remove` never deletes the branch. | docs only | HD `socket-api` |
| Layout API: `layout.export` / `layout.apply` (restores structure, labels, cwd, env, optional argv; **not** live PTYs), `pane.swap`, `pane.move`, `pane.zoom`, `pane.resize`. | docs only | HD `socket-api` |
| Process-launch commands on macOS refuse a command line longer than the terminal's canonical input limit (v0.9.4 fix; documented in v0.9.5 `socket-api`). | docs only | RN v0.9.4 (#4950, #4988) |

### 5.3 Agent detection and status

| Fact | Status | Source |
|---|---|---|
| Statuses: `idle`, `working`, `blocked`, `done` (= idle and not yet seen by `focus`; reads do not mark seen), `unknown`. The server finds the agent process in the pane and applies **screen manifest rules** to the live bottom of the screen (`agent read --source detection` shows what is matched). Where an integration reports state, those reports replace screen reading. For Claude Code and Codex at 0.9.3 the integration reports **session identity only**; status comes from the screen. For OpenCode the plugin reports state too. | docs + verified (Claude pane showed `agent_status: idle`, `agent_session` filled) | HD `agents`, `integrations`; P-hd |
| `agent explain <target> [--json] [--verbose]` returns the matched rule and evidence (manifest source/version, `osc_title_working` rule, region previews). Manifests update remotely from herdr.dev (`[update] manifest_check`), local overrides `~/.config/herdr/agent-detection/<agent>.toml` win; `herdr server update-agent-manifests` forces a check. The cached remote manifest for claude was `2026.10.11.1`. | verified (explain output) | P-hd; HD `agents` |
| **Blocked detection is deliberately strict**: only known visible approval/question UI counts. For a known agent with no matching rule the fallback is `idle` (`default_known_agent_idle_fallback`). **At 0.9.3 Codex falls back to `unknown`** and "may stay `unknown` after a response, and waits for `idle` or completion may time out" (v0.9.5 docs drop this and say Codex falls back to idle). Unusual prompts can read `idle`; "idle status is not authorization to approve" — automation that trusts it can type into a dialog. | docs only | HD `agents` (v0.9.3) vs v0.9.5 `agents` |
| Agent kinds for `agent start --kind`: `pi claude codex gemini cursor devin agy cline omp mastracode opencode copilot kimi kiro droid amp grok hermes kilo qodercli qwen letta maki muse`. `HERDR_AGENT=<kind>` on a wrapper command tells herdr which manifest to use when a sandbox wrapper hides the process. Panes where the shell auto-enters tmux show `tmux` as the process. | docs only | HD `agent-automation`, `agents` |
| Here (`herdr integration status`, 0.9.3): **claude current (v10)** (`~/.claude/hooks/herdr-agent-state.sh`), **codex current (v8)** (`~/.codex/herdr-agent-state.sh`), **opencode "needs repair (v13)"** (`~/.config/opencode/plugins/herdr-agent-state.js`); others not installed. | verified | `herdr integration status` |
| Claude integration: a `SessionStart` hook (matchers `startup, resume, clear, compact, fork`) reports the session id through the socket; herdr stores it as `agent_session` and uses it for native restore `claude --resume <id>`. In P-cc `agent_session.value` equalled the id given by `--session-id`, and after a **second** session started in the same pane it changed to the new id. | verified | P-hd, P-cc; HD `integrations` |
| `pane report-agent / report-agent-session / release-agent / report-metadata` let **any program** become the reporter for a pane (state `idle\|working\|blocked\|unknown`, `--source`, `--agent`, `--seq`, session id and path, resume argv). Reporting authority is per `source`+`agent`; metadata (title, display name, state labels, tokens ≤ 16 keys/report, 32/pane) is display-only and never affects waits. | docs only | HD `socket-api`, `add-herdr-support`, `cli-reference` |

### 5.4 Sending text, reading the screen, waiting

| Fact | Status | Source |
|---|---|---|
| `agent prompt <target> <text>` writes text + delayed Enter as one ordered submission, honouring live bracketed-paste mode, **even while the agent is working** (it queues in the agent's input). If the target is already `blocked` it returns `agent_blocked` and sends nothing. `--wait [--until STATUS…] [--timeout MS]`: if the prompt started from a non-working state herdr allows 5 s after submission to see `working` or `blocked`, else `agent_prompt_stalled`; the timeout includes submission time; "does not track individual turns", and an already-working agent's current turn can satisfy the wait. A timeout or stall **does not prove the prompt was not delivered** (read before retrying). Default `--until` set: idle, done, blocked. Target = unique live agent name or pane id; terminal ids and bare kinds are rejected. | docs; behaviour verified for Claude (`--wait` returned idle, `completion_seq: 6`, 2.8 s) | HD `agent-automation`, `cli-reference`; P-cc |
| Errors seen: `agent_not_found` (`agent target w1:p1 not found` for a plain shell pane, and for an unknown name), `pane_not_found`, `invalid_request`. Others documented: `agent_blocked`, `agent_not_ready`, `agent_not_idle`, `agent_prompt_stalled`, `agent_pane_busy`, `events_lost`, `workspace_not_found`, `confirmation_required`, `invalid_params`, `resume_not_accepted`, `invalid_resume_argv`, `platform_unsupported`, `plugin_disabled`. CLI exit code: 1 on a server error or timeout (JSON on stderr), 2 on a usage error.  Exit codes were **not** independently verified (the probe piped the output). | partly verified | P-hd; HD `cli-reference` |
| `pane run <pane> <command>` (text + Enter atomically, bracketed-paste aware), `pane send-text` (no Enter), `pane send-keys` / `agent send-keys` (validated before any byte is written: `enter esc tab backspace up down left right ctrl+c alt+x f1 …`; `escape` = `esc`; `C-c` accepted). `pane run MARK_$((6*7))` + `pane wait-output --match MARK_42` matched. `pane` input addresses the terminal whatever is in it; `agent` input rejects if the agent no longer owns the pane. | verified (run, wait-output, send-keys) | P-hd; HD `cli-reference` |
| `agent start <name> --kind K --pane P [--timeout MS] -- <args>`: needs the pane's **interactive shell at its prompt** (no foreground command); returns only after herdr detects the agent and it is ready for input (default timeout 30 s, valid 3 001–300 000 ms); `blocked` during startup returns `agent_not_ready` at once (the name stays usable for `read`/`send-keys`). | verified (Claude: returned idle in ~3.6 s with the name set) | HD; P-cc |
| `pane read <pane> --source visible\|recent\|recent-unwrapped\|detection [--lines N] [--format ansi]`; default `recent` returns the last 80 rendered rows; `visible`/`detection` return the full snapshot unless `--lines`. Socket: text at `.result.read.text`. For an **idle** recognized full-screen agent (Claude Code, OpenCode) reads asking for more than the screen scroll the agent's alternate-screen history with mouse-wheel input and put the viewport back; `agent read --lines N` that needs this returns `agent_not_idle` while the agent is working/blocked/unknown. A finished terminal's screen is readable only while the pane exists. | docs; `agent read --lines 5` and `--source visible` verified | HD `agent-automation`; P-hd |
| `pane wait-output <pane> (--match T\|--regex R) [--source …] [--lines N] [--timeout MS]`: checks the current snapshot immediately (already-present text matches), then polls; one line at a time; no default timeout. `agent wait <target> [--until …] [--timeout MS]` is **server-owned and event-driven** and pins the resolved pane occupant so a replacement cannot satisfy it (ends with `agent_not_running` if the agent leaves). | verified (`wait-output`); `agent wait` docs | P-hd; HD |
| Direct attach: `herdr agent attach <name> [--takeover]`, `herdr terminal attach <terminal_id>`, `herdr terminal session control\|observe <target>` (newline-delimited JSON `terminal.frame` records with base64 ANSI; one controller per terminal; observers disconnected after 30 s without write progress). | docs only | HD `cli-reference` |

### 5.5 Notifications

| Fact | Status | Source |
|---|---|---|
| `herdr notification show <title> [--body] [--position] [--sound none\|done\|request]` / `notification.show`: title ≤ 80 chars, body ≤ 240, control chars collapsed; result `{"type":"notification_show","shown":bool,"reason":…}` with reason `shown`, `disabled`, `rate_limited`, `no_foreground_client`, `busy` (an in-app toast is already up). Terminal and system delivery are **best-effort through the current foreground attached client**; `ui.toast.delivery = "off"` gives `disabled`. In the throwaway session the call returned `shown:false, reason:"disabled"` (no config, no client). | verified (shape, `disabled`); other reasons docs | P-hd; HD `socket-api` |
| Herdr itself notifies on agent state (done, needs attention) according to `[ui.toast]`/sound settings; `HERDR_DISABLE_SOUND` silences sound. | docs only | HD `cli-reference` |

### 5.6 Events, plugins, session restore

| Fact | Status | Source |
|---|---|---|
| `events.subscribe` with a list of `{type, pane_id?, agent_status?}`: first line acknowledges (`subscription_started`), later lines are pushed events. **Any invalid entry rejects the whole request and closes the connection.** Subscriptions start at acceptance with **no replay**. History is shared across event types and **not durable**: a slow reader gets `events_lost` and the connection is closed — then resubscribe, wait for `subscription_started`, and reconcile through `session.snapshot` on another connection ("Snapshots and events have no shared sequence boundary"). Event names: `workspace.{created,updated,metadata_updated,renamed,moved,reordered,closed,focused}`, `tab.{created,closed,focused,renamed,moved}`, `pane.{created,updated,closed,focused,moved,exited,agent_detected,output_matched,agent_status_changed,scroll_changed}`, `layout.updated`, `worktree.{created,opened,removed}`. | docs; names verified in the schema | HD `socket-api`; `herdr api schema --json` |
| Plugins (manifest `herdr-plugin.toml`, `min_herdr_version` required): `[[startup]]`, `[[actions]]`, `[[events]] on = "<event>"`, `[[panes]]`, `[[link_handlers]]`, `[[build]]`. Plugin commands are **processes spawned by herdr** (not by the agent) with `HERDR_SOCKET_PATH`, `HERDR_BIN_PATH`, `HERDR_ENV=1`, `HERDR_PLUGIN_ID/ROOT/CONFIG_DIR/STATE_DIR`, `HERDR_PLUGIN_CONTEXT_JSON`, and (when known) `HERDR_WORKSPACE_ID/TAB_ID/PANE_ID`; actions get `HERDR_PLUGIN_ACTION_ID`; events get `HERDR_PLUGIN_EVENT` and `HERDR_PLUGIN_EVENT_JSON`; startup hooks run once after restore with `HERDR_PLUGIN_EVENT=startup`. The registry (`plugins.json` beside `session.json`) is **user-global even with `HERDR_CONFIG_PATH`** (AGENTS.md rule). `plugin.log.list` returns recent command logs. `workspace.metadata_updated` does not invoke plugin hooks. There is no herdr-managed plugin storage in v1. | docs only | HD `socket-api`, `plugins`; AGENTS.md |
| What survives (HD `session-state`): **detach/reattach** keeps every process; **server restart** kills panes but restores layout, cwd, focus; a pane comes back as a **new shell in its saved directory** unless a stronger path applies. **Native agent restore** (`[session] resume_agents_on_restore`, default true) re-runs the agent's resume command for panes with an integration-reported session id (Claude `claude --resume <id>` from integration v6, Codex `codex resume <id>` v5, OpenCode `opencode --session <id>` v5) or a custom reporter's `resume_argv`. Pane screen history is **off by default** (`[experimental] pane_history`). Layout snapshots: 48 kept in `session-snapshots/`. Probe: stopping a named session and starting it again brought back workspace `cc` and its pane as a fresh shell. | docs; restart behaviour verified | HD `session-state`; P-hd |
| Updating the binary does not replace a running compatible server; `herdr server stop` ends all pane processes. Closing the shell that launched herdr no longer stops the server (v0.9.4: use `herdr server stop` or SIGTERM, not SIGHUP). A server started over SSH or from a background job can lack Keychain / user-service context on macOS (#966, #4100). | docs only | HD `troubleshooting`; RN v0.9.4 |

### 5.7 What herdr guarantees and what is best effort

Guaranteed by the documented contract: ordered, atomic submission of `agent prompt` / `pane run` text plus Enter (bracketed-paste aware); `agent_blocked` refusal when the screen is a **recognized** blocked UI; `agent wait` pinned to the pane occupant; key validation before any byte is written; server restart restores layout; `agent start` returns only when the agent is detected and ready; JSON API is additive ("ignore unknown fields").

Best effort, per the docs themselves: agent state for screen-manifest agents ("deliberately strict"; unknown prompts read `idle`; Codex `unknown`); notifications (foreground client only); `agent prompt --wait` (no turn tracking; may stall or finish on a previous turn); live handoff (experimental); event delivery (`events_lost`); `done` vs `idle` (per-client seen state); remote manifests (rules can change between runs).

### 5.8 Known issues and limits that touch an orchestrator (`issue text only` unless noted; all open on 2026-11-04 unless stated)

- Delivery and prompts: **#5263** `agent prompt` types text + Enter into a Claude Code permission dialog while a `PermissionRequest` hook runs (status still `working`; the reporter says it approved a merge). **#4537** (0.9.1) `agent prompt`, `send-text` and `pane run` never reached a pristine idle `--no-focus` Claude pane. **#5386** `agent.prompt --wait` can return `agent_prompt_stalled` although the prompt was delivered. **#4529** (closed) first prompt to a fresh Windows Claude often not submitted. **#4718** (closed) custom submit key. **#3813** OpenCode "ready" right after start but `agent_prompt_stalled`. **#5365** OpenCode "working" never detected (status line text changed to `esc interrupt`). **#5333**/**#4511**/**#4454**/**#4214** OpenCode v2 state problems (lost registration, subagent running while idle, background session flips the pane, latch on `blocked`).
- Start: **#5627** an agent started with `agent start` loses its name when its first turn outlasts the start timeout; **#5744** brush shell → `agent_pane_busy`; **#4918**/**#5598** start waits the full timeout when the agent exits or the pane closes; **#3208** zsh start still rejected on 0.8.2; **#5724** agents started from the tab menu skip the user's shell PATH.
- Detection: **#3467** stale busy OSC title outranks Claude's blocked rules; **#5741** AskUserQuestion with extra footer hints not blocked; **#5358** Codex spinner left in the OSC title keeps `working` for hours; **#5490**/**#5775** Codex pickers read idle; **#4322** Codex reported blocked during async questions.
- Integrations: **#5733** the Claude SessionStart hook reads stdin before its guards and can time out instead of reporting the session; **#5348** no `agent_session` on hosts without `python3` (Claude/Codex); **#4814**/**#4649** Codex integration reports to inherited/stale panes (shared Codex daemon, app-server); **#5544** OpenCode 2 shell commands get no `HERDR_PANE_ID`; **#5767** a restored Claude session starts in the pane's pwd, not the session's; **#5443** live handoff clears `agent_session` when two panes share a session id.
- Reads: **#5787** `agent read` default source returns only the visible frame while working; **#5750** a pane can drop middle rows while streaming long TUI output; **#2805** `pane.read` always returns `revision: 0`.
- Scale/server: **#5746** `pane list` gets slow with many panes and polling it makes typing lag; **#5791** `terminal attach` drops plain input when the queue is full; **#5765** repeated SIGSEGV/SIGBUS reported on 0.9.5; **#5778** `server reload-config` exits 0 on failure; **#5703** closing a Claude pane takes ~550 ms; **#5729** a server without an agent adopts a stale `herdr.sock.agent`.
- Documented limits: wait commands have **no default timeout**; names `[a-z][a-z0-9_-]{0,31}`; `agent start` timeout 3 001–300 000 ms; live handoff transfers >64 panes only from servers that already support batching; `terminal session observe` drops after 30 s of no write progress; token metadata is not restored after restart.

### 5.9 What changed after 0.9.3 (v0.9.4, v0.9.5)

- v0.9.4 (2026-10-13), `RN`: `herdr plugin update`; **closing the launching shell no longer stops the server (SIGHUP no longer works to stop it)**; saves are atomic (crash can't leave an unreadable `session.json`); out-of-range workspace number returns `workspace_not_found`; Ctrl+Z/`fg` keeps an agent's name and session; `pane run`/`agent start` on macOS refuse an over-long command instead of cutting it; Codex reports finished turns through its integration when started `--no-daemon`, and its startup update screen shows as blocked; Antigravity/Grok count idle at their prompt.
- v0.9.5 (2026-10-14), `RN`: **herdr no longer installs Codex turn hooks and Codex status comes from screen detection again** ("run `herdr integration install codex` to update"); Claude Code questions show as blocked in named sessions (#4573, #5192); "clamping Interrupt hook timeout" warning removed.
- The installed Codex integration (v8) has **no turn hooks**: `~/.codex/hooks.json` holds one `SessionStart` entry only (§3.9), so the 0.9.5 removal does not change this machine's behaviour; status is screen-detected either way. The OpenCode integration reads "needs repair (v13)" (§4.9: `cli.json` has no `plugin` key; the V2 TUI plugin is registered only in `tui.jsonc`).

## 6. Not verified (open items collected from the sections)

- **Claude**: the wire format of a message posted to the inbox socket (undocumented; nothing was sent); whether a
  positional `prompt` is submitted as turn 1 in interactive mode (help says so; not run); Ctrl+C / Ctrl+D semantics; the
  `StopFailure`, `Notification`, `PermissionRequest` payloads (docs only); `~/.claude/sessions/<pid>.json` stability;
  channels (research preview, not run).
- **Codex**: the hook process env (source only); whether `codex queue` reaches a TUI with an in-process server (it is
  documented for the daemon or `--remote`); Ctrl+C single/double press; the origin of `CPATH`, `LIBRARY_PATH`,
  `MANPATH`, `SDKROOT` in the MCP server env (not in the source allowlist).
- **opencode**: whether the MCP cwd is the location directory or the server's cwd (they coincided); the
  `ai.opencode/sessionID` `_meta` (no model call); whether v2.2.0 loads herdr's TUI plugin from `tui.jsonc` (herdr
  reports "needs repair"); the `type` strings of `Session.StructuredError` beyond `provider.rate-limit`; v2.0/v2.1
  notes (not published as releases).
- **herdr**: CLI exit codes (probe piped the output); `agent wait`, `events.subscribe`, `events_lost` (docs only);
  every issue in §5.8 is issue text, not reproduced; behaviour of 0.9.5 vs the installed 0.9.3 beyond the release
  notes.
- **Cross-tool**: nothing was run against the user's default herdr session or a real obor lane; the obor-specific
  mapping (what obor does with each fact) is left to the deep dive.
