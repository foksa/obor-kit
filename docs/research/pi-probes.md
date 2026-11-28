# pi probes: can pi (pi 1.7.0) be an obor agent kind?

**Yes, as a third plugin kind built like OpenCode's: a Go kind (`kinds/pi/kind`) plus a pi extension (TypeScript)
that obor's start loads with `-e`.** The extension does the work OpenCode's `opencode/obor.js` does. It is the hook
transport: `tool_call` is a PreToolUse that blocks with a reason the model sees; `agent_before_settle` is a Stop that
can start one more run; `input` and `before_agent_start` cover a turn's start and its context. It registers obor's MCP
server for the session (`pi.registerMcpServer`, `exposure: "direct"`). Its per-session socket is the inbox
(`pi.sendUserMessage`, which starts a turn when idle and steers or follows up when busy). Everything a start needs is
flags: `--model <provider>/<id>`, `--thinking <level>`, `--session-id <id>` (obor picks the id),
`--session <id>` (resume), `--approve`, `-e <ext>`, and the first prompt as a positional message. pi reads AGENTS.md
and `.agents/skills`, never stops on a permission prompt (it has none), and herdr reads a pi pane's working and idle
states with no integration installed. The transcript is a JSONL file with each assistant message's `usage` and
`stopReason`, so `turn` `last`, `context` and `reset` read it directly.

The owner's first use works as asked. A pi worker ran on `anthropic/claude-sonnet-5-5 --thinking low`, and a fresh
start ran on `openai-codex/gpt-6-luna:low` through the Codex subscription. Both providers were already signed in, so
the kind configures neither.

What does not fit:
- pi has **no per-start settings file**. `--agent-dir` (`PI_CODING_AGENT_DIR`) moves the whole agent dir, and that
  includes `auth.json`, so a start would lose the owner's sign-ins.
- pi does not read the project's `.agents/mcp_config.json`. The extension has to register the project's servers
  itself.
- The user's own `~/.pi/agent/mcp.json` servers, user AGENTS.md and settings cannot be switched off for a lane without
  also losing obor's server or project context (`--no-mcp`, `-nc`), so the guard has to deny those servers per call.
- An uncaught throw in an extension handler crashes the whole TUI (observed).
- Codex's rate-limit reset reaches obor only as text in the error message.

## Summary

| # | Question | Answer | Probe |
|---|---|---|---|
| 1 | Start | `pi [flags] [--] "<prompt>"` runs the TUI with the prompt as the first turn. `--model provider/id` (and `:level`), `--thinking off\|minimal\|low\|medium\|high\|xhigh\|max`. `--session-id <id>` starts a new session under an id obor chooses; `--session <id>` resumes with history, takes a prompt, and the `--model` given at resume wins. No per-start config file or dir short of `--agent-dir`, which also moves `auth.json`. `--approve` trusts the project for this run only (nothing saved). | live: P1, P5, P6 |
| 2 | obor's tools | pi speaks MCP (stdio and streamable HTTP) built in. An extension loaded with `-e` registers a server for the session (`pi.registerMcpServer`); with `exposure: "direct"` the tools are declared to the model as `mcp__<server>__<tool>`. The server inherits the TUI's whole env (`OBOR_*`, `HERDR_PANE_ID`); its cwd is the session's. `-ne -e builtin:mcp -e <ext>` (no user extensions) keeps it working. | live: P2 |
| 3 | Guard | `tool_call` fires before every tool runs (built-ins, MCP tools, codemode's nested calls); `{block: true, reason}` stops it and the model gets the reason as the tool's error. Probed on `bash` and `write`: the file was not written. A handler that throws blocks the call (fail-safe, documented). | live: P3 |
| 4 | Turn end, turn start | `agent_before_settle` is the last point before idle and can append a message and `continue: true`, which starts one more run (the Stop block); `agent_settled` is final. A turn's start: `input` (raw text, its source: `interactive`, `rpc`, `extension`) and `before_agent_start` (can add a message or change the system prompt). `turn_start`/`turn_end` are per model request. | live: P4 |
| 5 | Context, limits | The session JSONL has every assistant message's `usage` (`input`, `cacheRead`, `cacheWrite`, `output`) and `stopReason` (`error` with `errorMessage`). `ctx.getContextUsage()` gives `{tokens, contextWindow, percent}`. Anthropic: `after_provider_response` gives the `anthropic-ratelimit-*-reset` headers on every response. Codex: no headers reach extensions (WebSocket); its limit error text says "Try again in ~N min." | live: P5; source |
| 6 | Delivery | An extension socket calling `pi.sendUserMessage(text)` starts a turn when idle; with `deliverAs: "steer"` a busy session takes it after the running tool calls, before the next model call; `"followUp"` after the run. A line typed into a busy pane with Enter is a steer as well. RPC mode replaces the TUI and does not apply. | live: P6 |
| 7 | AGENTS.md, skills | Context files from the agent dir and from the cwd up through its parents: **one per directory**, the first of `AGENTS.override.md`, `AGENTS.md`, `AGENTS.MD`, `CLAUDE.md`, `CLAUDE.MD` (a CLAUDE.md beside an AGENTS.md is not loaded). Skills: `~/.pi/agent/skills`, `~/.agents/skills`, `.pi/skills`, `.agents/skills` (cwd up to the repository root), `--skill <path>`; not `~/.claude/skills`. `/skill:<name> <args>` forces one. | live: P7; source |
| 8 | Prompts, herdr | No tool-permission prompts. The one startup dialog is project trust (a `.pi/` resource or `.agents/skills` present), and `--approve` skips it. herdr (0.9.5) detects a pi pane (`agent: pi`) and its `working`/`idle` with no integration installed, but reported the trust dialog as `idle`, not `blocked`. herdr's own pi integration is a user-global extension (`~/.pi/agent/extensions/herdr-agent-state.ts`, not installed here). `/quit` with one Enter exits and prints the resume command. | live: P8 |

## Method

- Date: 2026-11-28. pi 1.7.0 (managed install under `~/.pi/agent/install/releases/1.7.0`, Node from pi's own
  installer), herdr 0.9.5, macOS.
- A throwaway herdr session `probe-pi` (headless `herdr --session probe-pi server` under `env -i` with `HOME USER
  LOGNAME SHELL TERM LANG PATH`), one workspace, one pane, cwd a scratch dir `<kit>/repo`. The scratch dir is not a git
  repository: the guard denies `git init` to a researcher. pi started in the pane the way obor would start it: first
  `herdr agent start probe --kind pi --pane w1:p1 -- <args>`, then `herdr pane run`. It was driven with
  `herdr agent prompt`, `pane send-text` and `send-keys` and read with `pane read`. Never `pi -p` or `--mode json|rpc`.
- The pane's env had `OBOR_ROLE=probe-worker OBOR_LANE=L-probe OBOR_PANE=w1:p1`.
- Every start carried `--offline`, which only skips startup network work (version check, catalog refresh; model calls
  still ran), plus `--session-dir <kit>/sessions` and `-e <kit>/probe.ts`. No start used `--agent-dir`.
- `probe.ts` (appendix) logs every lifecycle event to `<kit>/log/events.jsonl`. It does five things:
  - registers a stdio MCP server `probe` (`mcp.py`, one tool `echo`, logs its env and cwd);
  - blocks any tool call whose input contains `PROBE-DENY`;
  - on `agent_before_settle`, while a control file exists, appends a message and asks for a continuation;
  - listens on a unix socket for lines it passes to `pi.sendUserMessage` / `pi.sendMessage`;
  - logs the rate-limit headers of each provider response.
- The fixture holds `AGENTS.md` (marker `MARK-AGENTS-7`), `CLAUDE.md` (`MARK-CLAUDE-3`), and
  `.agents/skills/probe-skill/SKILL.md` (`MARK-SKILL-5`).
- Models: `anthropic/claude-haiku-4-5 --thinking low` for most turns, `anthropic/claude-sonnet-5-5 --thinking low` for
  the resume, `openai-codex/gpt-6-luna:low` for the `--session-id` and trimmed starts. Total model spend about $0.08
  (pi's footer), Codex turns on the subscription.
- In the quotes `<kit>` is the probe dir, `<repo>` is `<kit>/repo`. Session ids are scratch ids.
- **Footprint:** `~/.pi/agent/settings.json`, `auth.json` and `models-store.json` had the same checksums after the
  run as before; no session was written under `~/.pi/agent/sessions`; no trust decision was saved (`trust.json` does
  not exist). One file appeared: `~/.pi/agent/crashes.json`, pi's crash record for the first start (P1: the probe
  extension threw at session start); it holds that crash's cwd, session file path and stack. It was left in place
  (pi's own runtime data; the owner may delete it). The herdr session was stopped and deleted; no pi, MCP or herdr
  process of the probe was left.

## P1: start, trust, flags

Question: an interactive start with a first prompt; model, provider and thinking flags; a per-start config.

Ran: `herdr agent start probe --kind pi --pane w1:p1 -- --offline -e <kit>/probe.ts --session-dir <kit>/sessions
--model anthropic/claude-haiku-4-5 --thinking low`, then (after the crash below) `pi --offline --approve -e … --model
anthropic/claude-haiku-4-5 --thinking low 'What are the AGENTS, CLAUDE and skill markers? …'`.

Saw:
- Without `--approve`, a project with `.agents/skills` opens a trust dialog before anything loads: "Trust project
  folder? … This allows pi to load .pi settings and resources, install missing project packages, and execute project
  extensions." The options are Trust, Trust parent folder, Trust (this session only), Do not trust, and Do not trust
  (this session only). `herdr agent start` returned `agent_status: idle, interactive_ready: true` while the dialog was
  on screen, and `agent get` still said `idle`.
- The probe's `project_trust` handler ran first. It answered `undecided`, so the dialog showed. A CLI `-e` extension
  may answer `yes` without `remember` and decide for that run only (documented, `extensions.md` "project_trust").
- Ctrl+C on the dialog went on to start a session, and the probe's socket `listen` threw `EINVAL`: the absolute path
  under the scratch dir is over macOS's 104-byte unix socket limit. The throw was not caught and pi exited: "pi exiting
  due to uncaughtException … A stack frame came from loaded extension `<kit>/probe.ts`". The next start showed
  "Warning: pi crashed on … Run /bug to report it" and pi had written `~/.pi/agent/crashes.json`.
- With `--approve` and a relative socket path, the start showed `[Context] AGENTS.md`, `[Skills] probe-skill`,
  `[Extensions] probe.ts` and ran the positional prompt as the first turn at once (`input` source `interactive`). The
  footer: `(anthropic) claude-haiku-4-5 • low`.
- `session_start` (`reason: startup`, `mode: tui`, `trusted: true`) and the env the extension sees: `OBOR_ROLE`,
  `OBOR_LANE`, `OBOR_PANE`, `HERDR_PANE_ID`, plus pi's own `PI_CODING_AGENT=true`, `PI_OFFLINE=1`,
  `PI_SKIP_VERSION_CHECK=1`, `PI_MANAGED_INSTALL_ROOT`. Active tools at start: `read`, `bash`, `edit`, `write`.

From `pi --help` and the docs:
- The prompt is positional (`pi [options] [--] [@files...] [messages...]`). One beginning with `-` needs `--` first.
  Several positional messages run one after another.
- `--model` takes `provider/id`, a pattern, and an optional `:<thinking>`. `--provider` narrows a bare pattern.
  `--thinking` takes `off`, `minimal`, `low`, `medium`, `high`, `xhigh`, `max`. A model that lacks a level is limited
  to the levels it supports (`models.md`: "Pi limits the choices to levels supported by the selected model").
- Per-start config: there is none. The only settings are `~/.pi/agent/settings.json` (or the dir
  `--agent-dir`/`PI_CODING_AGENT_DIR` names) and the project's `.pi/settings.json` (trust-gated). The agent dir also
  holds `auth.json` (`configuration.md`), so a per-start agent dir starts with no sign-ins. Everything else a start
  needs has a flag:
  - tools: `--tools`, `--exclude-tools`, `--no-builtin-tools`;
  - extensions: `-e`, `-ne`;
  - skills, prompt templates, themes: `--skill`, `--no-skills`, `-np`, `--no-themes`;
  - context files: `-nc`;
  - MCP: `--no-mcp`;
  - system prompt: `--system-prompt`, `--append-system-prompt`;
  - sessions: `--session-dir`;
  - trust and network: `--approve`/`--no-approve`, `--offline`.

Decides:
- The kind's `start` is argv only. Its `env` needs nothing beyond the extension's own variables (below).
- `--approve` on every start: a lane's worktree is a new path every time, and herdr would not show the dialog as
  blocked.
- The extension must catch every error in its handlers. An inbox socket path must stay short:
  `${HOME}/.obor/pi/<sid>.sock`, as OpenCode's `inboxPath` is.

## P2: MCP and the extension

Question: does pi speak MCP; how obor's server and an extension reach a start without global settings.

Ran: the start of P1, then the prompt "… 3) call the probe echo MCP tool with text hello". Then a trimmed start:
`pi --offline --approve -ne -e builtin:mcp -e <kit>/probe.ts --no-skills -np … --model openai-codex/gpt-6-luna:low
'Call the probe echo MCP tool with text trimmed …'`.

Saw:
- `mcp.py`, registered by `pi.registerMcpServer("probe", {command: "python3", args: [...], exposure: "direct"})` in
  the extension's factory, started at session start with the TUI's env (`OBOR_ROLE=probe-worker`, `OBOR_LANE=L-probe`,
  `OBOR_PANE=w1:p1`, `HERDR_PANE_ID=w1:p1`, the `PI_*` vars) and cwd `<repo>`.
- The model called `mcp__probe__echo {"text":"hello"}` and got `ECHO:hello`. The call went through `tool_call` first,
  with `toolName: "mcp__probe__echo"`. The tool was not in the active-tools list at `session_start`: servers connect in
  the background, and the first prompt waits up to 10 s for `direct` servers (`mcp.md`).
- The trimmed start (`-ne`: no discovered or built-in extensions; `-e builtin:mcp` back; `--no-skills`, `-np`) showed
  only `[Context] AGENTS.md` and `[Extensions] probe.ts`, and `mcp__probe__echo` worked (`ECHO:trimmed`).

From the docs (`mcp.md`, `extensions.md` "MCP Servers"):
- MCP is built in (`builtin:mcp`). User servers live in `~/.pi/agent/mcp.json` and project servers in `.pi/mcp.json`
  (read only after trust). pi does not read `.agents/mcp_config.json`.
- The default `exposure` is `codemode`: the tools are reachable only from `codemode` scripts. `direct` declares them
  like built-ins.
- A file-configured server with the same name wins over an extension's registration.
- `--no-mcp` disables MCP altogether, obor's server included.
- Tool names are `mcp__<server>__<tool>`, with every character other than letters, digits and `_` replaced by `_`.
  Server names that differ only in `-`/`_` count as one server.

Decides:
- No user-global registration. The kind's `install` answers `mcp: {where: "start"}` (OpenCode's), and the extension
  registers `obor` (`{kit}/bin/obor mcp`, `exposure: "direct"`) at every start.
- The extension is loaded by `-e {kit}/pi/obor.ts`. pi loads TypeScript itself (jiti), so no build step; like
  `opencode/obor.js` it runs in the CLI's own Node.
- The transport maps `mcp__<server>__<tool>` to the guard's `tool_name: mcp`, with `mcp_server` taken from the
  registered server names. A server name with `-` comes back as `_`, so the transport matches against the registered
  names, not the prefix.
- The project's other servers (the lanes' allowed set, `.agents/mcp_config.json`) are not loaded by pi. The extension
  can read that file and register them as well.

## P3: the guard (PreToolUse)

Question: can an extension see and block a bash command or a file write before it runs, with a reason the model sees.

Ran: one delivered prompt: "1) run the bash command: echo PROBE-DENY 2) write a file named PROBE-DENY.txt containing
hi 3) call the probe echo MCP tool". The probe's `tool_call` handler returns `{block: true, reason: "obor: denied by
the probe guard (PROBE-DENY)"}` for any input containing the string.

Saw:
- `tool_call {tool: "bash", input: {command: "echo PROBE-DENY", timeout: 5}}`, then `tool_execution_end {isError:
  true}`. The screen showed the reason as the tool's result, and the model's reply named it: "1. bash: denied by the
  probe guard (PROBE-DENY)".
- `tool_call {tool: "write", input: {path: "PROBE-DENY.txt", content: "hi"}}` was blocked the same way. The file does
  not exist afterwards (`ls <repo>`: `AGENTS.md CLAUDE.md`).
- Inputs: `bash` `{command, timeout}`, `write` `{path, content}`, `read` `{path, offset, limit}`, and `edit` (per the
  docs) `{path, …}`. The path argument is `path`, not `file_path`, and pi's bash has no background flag.

From the docs: `tool_call` runs before execution for every tool, built-ins, extension tools and MCP tools alike
(`mcp.md` "Permissions"). Codemode scripts reach tools only through the same pipeline: the QuickJS sandbox has no
Node APIs, file system or network (`codemode.md`), and nested calls carry `parentToolCallId`. Handler errors block the
tool ("`tool_call` errors block the tool (fail-safe)"). `event.input` is mutable. Unknown tools and invalid arguments
skip `tool_call` and end as errors. `user_bash` (the user's own `!` commands) is a separate event that can intercept.

Decides: the guard maps cleanly. The transport runs `obor hook pre-tool-use` with:
- `bash` → `Bash` + `command`;
- `write` → `Write` + `file_path` from `path`;
- `edit` → `Edit`;
- `read` → allowed;
- `mcp__*` → `mcp`;
- `codemode` and `tool_search` → allowed, since their inner calls reach `tool_call` themselves;
- any other tool → `unguarded`.

Exit 2 becomes `{block: true, reason}`. A transport failure is a block, as the protocol asks.

## P4: turn end (Stop) and turn start

Question: a hook at turn end that can keep a worker going, and a turn's start.

Ran: with the control file `ctrl/settle-block` present, a delivered prompt "Run the bash command: sleep 8 && echo
SLEPT. Then reply DONE-1.", plus two lines while it ran (P6). On the first `agent_before_settle` the probe removes the
file and returns `{entries: [...e.entries, {type: "custom_message", customType: "obor-stop", content: "obor: you have
not called task_done. Reply with exactly the word BLOCKED-SEEN."}], continue: true}`.

Saw (events, times in seconds):

```
13:15 input (extension) → before_agent_start → agent_start → turn_start 0
13:18 tool_call bash "sleep 8 && echo SLEPT"      13:26 tool_execution_end
13:26 turn_end 0 (toolUse) → turn_start 1 → 13:28 turn_end 1 (stop) → turn_start 2 → 13:30 turn_end 2 (stop)
13:30 agent_end → agent_before_settle {block: true} → agent_start → turn_start 0 → 13:33 turn_end 0 (stop)
13:33 agent_end → agent_before_settle {block: false} → agent_settled {aborted: false}
```

The screen showed `[obor-stop]` with the text, then the model's reply `BLOCKED-SEEN`. The transcript holds the block
as a `custom_message` entry (`customType: obor-stop`) and the model got it as a user-role message.

From the docs (`extensions.md` "agent_start / agent_end / agent_before_settle / agent_settled"):
- `agent_end` is not idle: retries, compaction and queued messages can follow. `agent_before_settle` is "the final
  actionable boundary"; `agent_settled` is final.
- An aborted run (Escape) skips `agent_before_settle`.
- An unconditional `continue: true` loops, so the handler guards it.
- `before_agent_start` can add a message (`display: false` hides it) or change the system prompt.
- `input` can transform the text or answer it without a turn.

Decides:
- `stop` → `agent_before_settle`. The extension keeps `stop_hook_active` itself: true when this settle follows a
  continuation it asked for.
- `user-prompt-submit` → `input`, with the context added at `before_agent_start` as a hidden message. `session-start`
  → `session_start`.
- A worker's turn begun → `agent_start` (or `before_agent_start`); ended → `agent_settled`.
- `stop-failure` → a run whose last assistant message has `stopReason: "error"` at `agent_settled`, with its
  `errorMessage`.

## P5: context size, token use, rate limits

Question: where the context size and token use are, and when a rate limit resets.

Saw:
- Session file `<session-dir>/<timestamp>_<session-id>.jsonl`. Its first line is `{"type":"session","version":3,
  "id":"01a221da-…","timestamp":…,"cwd":"<repo>"}`. After it come `message` entries with `role` `user`, `assistant`,
  `toolResult` or `system`, plus `custom_message`, `model_change` and `thinking_level_change` entries. The default
  session dir is `~/.pi/agent/sessions/--<cwd with / as ->--/` (`session-format.md`).
- Each assistant message has `model`, `stopReason` (`stop`, `toolUse`, `error`, `aborted`, …, with `errorMessage` on
  error) and `usage` (`input`, `output`, `cacheRead`, `cacheWrite`, `totalTokens`, `cost`). Context =
  `input + cacheRead + cacheWrite` of the last one: the Sonnet resume's `{input: 4, cacheWrite: 4802}` was the whole
  prompt.
- `ctx.getContextUsage()` at `turn_end`: `{tokens: 2867, contextWindow: 200000, percent: 1.43}` for Haiku 4.5;
  `contextWindow: 272000` for `gpt-6-luna`.
- Anthropic: `after_provider_response` fired per request with status 200 and the headers
  `anthropic-ratelimit-{requests,tokens,input-tokens,output-tokens}-{limit,remaining,reset}` (reset as RFC 3339).
- Codex: no `after_provider_response` event fired. The docs say it does not fire on WebSocket transports. The limit
  error is built in `pi-ai/dist/api/openai-codex-responses.js:1249-1257` from the response's `resets_at`: "You have hit
  your ChatGPT usage limit (<plan> plan). Try again in ~N min." (or the server's own `message`). pi retries transient
  errors itself (`retry.*` settings, 3 tries by default).

Decides:
- The manifest's `transcript` glob: `${HOME}/.pi/agent/sessions/*/*_{sid}.jsonl`. With
  `PI_CODING_AGENT_DIR`/`PI_CODING_AGENT_SESSION_DIR` in `env`, the glob needs a form for those too; or every start
  passes `--session-dir` to a dir obor names.
- `turn` `last`, `context` and `session` read the file. `reset` parses Anthropic's 429 and Codex's "Try again in ~N
  min." from `errorMessage`. The extension could also log Anthropic's reset headers, but that is not needed for a
  first kind.
- No token file like OpenCode's is needed: pi's own transcript has the numbers.

## P6: delivering a line

Question: put a line into an idle or a busy session.

Ran:
- (a) idle: a line on the probe's socket → `pi.sendUserMessage(text)`;
- (b) busy: two lines 3 s into the `sleep 8` run of P4, one with `deliverAs: "steer"` ("Also say STEER-SEEN."), one with
  `"followUp"` ("Finally say FOLLOWUP-SEEN.");
- (c) typed: `herdr agent prompt w1:p1 "Run … sleep 6 …"`, then 4 s later `pane send-text "Also say TYPED-BUSY-SEEN."`
  + `send-keys enter`.

Saw:
- (a) `inbox {idle: true}` → `input {source: "extension"}` → a turn ran.
- (b) Both lines logged `input {streaming: "steer"}` and `{streaming: "followUp"}` at once. The steer reached the
  model after the running bash call finished (turn 1: "DONE-1 STEER-SEEN"). The follow-up came after that turn (turn
  2: "FOLLOWUP-SEEN"). Nothing was interrupted.
- (c) The typed line logged `input {source: "interactive", streaming: "steer"}` while bash ran and was answered in the
  same run: "DONE-2 TYPED-BUSY-SEEN". `herdr agent prompt` to an idle pi pane worked by pane id.
- `pi.sendUserMessage` while busy without `deliverAs` is rejected as an extension error (documented).
  `pi.sendMessage(…, {triggerTurn: true})` is the variant that adds a custom message (not probed live).

Decides:
- `deliver` works as OpenCode's does. The extension binds `${HOME}/.obor/pi/<sid>.sock` at `session_start` and puts
  its path in the session-start hook's env (`OBOR_PI_INBOX`, recorded through `inbox_record`).
- `deliver` returns `{socket, lines}`. The extension sends a line with `sendUserMessage(text)` when idle, else
  `deliverAs: "steer"`.
- Typing into the pane (`typed: true`) also works as a fallback.
- RPC mode (`--mode rpc`) replaces the TUI with stdin/stdout JSON, so it is not an option for an interactive pane.

## P7: AGENTS.md, CLAUDE.md, skills

Question: which instruction files and skills pi loads, from where.

Ran: the first prompt of P1 asked for the three markers.

Saw:
- `before_agent_start` listed `contextFiles: ["<repo>/AGENTS.md"]` and `skills: ["probe-skill
  <repo>/.agents/skills/probe-skill/SKILL.md"]`. CLAUDE.md was not in the context: the model said "For CLAUDE, I
  don't see it defined in my context" and read the file with its own tools.
- The model loaded the skill by reading its `SKILL.md` (`tool_call read …/SKILL.md`), as pi's skills work: only the
  name, description and path are in the system prompt.

Source: `loadContextFileFromDir` in pi's bundle (`dist/bundle/chunks/chunk-Q6QBNASS.js`) returns the first existing
file of `["AGENTS.override.md","AGENTS.md","AGENTS.MD","CLAUDE.md","CLAUDE.MD"]` per directory.

From the docs:
- Context files come from the agent dir, the cwd and its parents, and do not need trust. `-nc` turns all of them off,
  the project's included.
- Skills come from `~/.pi/agent/skills/`, `~/.agents/skills/`, `.pi/skills/`, and `.agents/skills/` (cwd up to the
  repository root), plus `--skill <path>` and the `skills` setting. Project skills need trust.
- `/skill:<name> <args>` forces a skill and appends the args. `--no-skills` turns off discovery; explicit `--skill`
  paths are documented for extensions (`-ne` vs `-e`), and for skills the same was not probed.
- `~/.claude/skills` is not a pi location.

Decides:
- obor-kit has both files at its root. pi reads AGENTS.md, which is where the rules are; CLAUDE.md only points to it.
- A trimmed start (`trim_user`) can pass `--no-skills --skill {kit}/skills/…` and `-ne -e builtin:mcp -e {kit}/pi/obor.ts`.
  There is no switch for the user's agent-dir AGENTS.md short of `-nc`, which also drops the project's.
- A lead's handoff prompt can be `/skill:obor-lead handoff` if the kit's skills are passed with `--skill`, or the plain
  "read <kit>/skills/lead/SKILL.md" that OpenCode uses.

## P8: permission prompts, quitting, herdr

Saw:
- No tool asked for permission in any probe (bash, write, read, MCP). pi's model is "it does not ask for approval
  before every tool call" (`security.md`). Dialogs come only from extensions (`ctx.ui.confirm`, which fires
  `ui_prompt_start`) and from the trust check.
- herdr's `agent list` showed `"agent":"pi"` for the pane, `working` during runs and `idle` after `agent_settled`,
  with herdr's own pi integration not installed (`herdr integration status`: "pi: not installed
  (~/.pi/agent/extensions/herdr-agent-state.ts)"). That integration is a pi extension that reports `session_start`,
  `agent_start` and `agent_settled` (strings in the herdr binary); installing it writes into the owner's `~/.pi`.
- The trust dialog showed as `idle`, not `blocked` (P1).
- An agent named at `herdr agent start` lost its name when pi crashed. `herdr agent prompt` by pane id still found the
  next pi.
- `/quit` typed opens the command popup (`→ quit  Quit pi`). One Enter runs it, and pi prints "To resume this session:
  pi --session-dir <kit>/sessions --session 01a221da-…" (`session_shutdown {reason: "quit"}`).
- Keys (`keybindings.md`): Ctrl+C clears the editor, and a second one exits; Ctrl+D exits when the editor is empty;
  Ctrl+U deletes to line start; Escape interrupts a run.

Decides:
- `stop`: `{quit: "/quit", second_enter: false, clear_input: ["ctrl+u"]}`. Not `ctrl+c`, which exits on its second
  press.
- `status: terminal` works without herdr's integration.
- `--approve` on every start keeps the trust dialog, the one screen herdr misreads, from appearing.

## P9: resume and the session id

Ran:
- `/quit`, then `pi … --session 01a221da-… --model anthropic/claude-sonnet-5-5 --thinking low 'Which word did you last
  reply with? One line.'`;
- later, a fresh start with `--session-id 15a40d80-4b69-45c7-9dc5-ece05841c04b --model openai-codex/gpt-6-luna:low
  'Reply with exactly CODEX-OK.'`.

Saw:
- The resume reopened the session with its history and ran the prompt at once: "My last reply was 'DONE-2
  TYPED-BUSY-SEEN'". The footer read `(anthropic) claude-sonnet-5-5 • low`, and the transcript got `model_change` and
  `thinking_level_change` entries, so the resume's flags win over the session's recorded model.
- `session_start` said `reason: "startup"` for this CLI resume, not `"resume"`; `"resume"` is for `/resume` inside a
  running TUI.
- `--session-id <uuid>` created the session under exactly that id
  (`sessions/2026-11-28T11-15-50-368Z_15a40d80-….jsonl`), and the Codex turn ran on the subscription (footer
  `$0.000 (sub)`).

Decides:
- `caps.resume`: a resume is `--session <id> [--] <prompt>`. A fresh start can pass `--session-id`, so obor knows the
  session id before the first hook (none of the other kinds offers that).
- The transport tells a resumed start from a fresh one by a variable the start sets, as OpenCode's `ResumeVar`
  does. It cannot use `session_start`'s reason, which says `startup` either way.

## What a pi kind needs

| Kind-protocol item | pi mechanism | Size |
|---|---|---|
| `describe`: caps, efforts, vars, stop, headless | `hooks: plugin`, `hook_answers: true`, `inbox: true`, `resume: true`, `model_arg: true`. Efforts: `off minimal low medium high xhigh max`. Stop: `/quit`, one Enter, `ctrl+u`. Headless: `print_flag` (`-p`/`--print`), `--mode` (`json`, `rpc`), and the config-writing subcommands (`install`, `remove`, `uninstall`, `update`, `config`, `auth`, `mcp`; their exact list is the guard's choice). `status: terminal`. | S |
| `start` | argv: `--approve`, `-e {kit}/pi/obor.ts`, `--model <provider/id>`, `--thinking <effort>`, `--session-id <id>` or `--session <id>`, `--` `<prompt>`. Trimmed: `-ne -e builtin:mcp`, `--no-skills` + `--skill` for the kit's, `-np`. env: the inbox and resume variables for the extension. No files. | S |
| `install` | `mcp: {where: "start"}` (the extension registers `obor mcp` at each start, `exposure: "direct"`); no hooks file | S |
| hook transport (`pre-tool-use`, `session-start`, `user-prompt-submit`, `stop`, `stop-failure`) | the extension (`pi/obor.ts`), on the pattern of `opencode/obor.js`: `tool_call` → pre-tool-use (tool map in P3; exit 2 → `block` + reason); `session_start` → session-start (session id, file as `transcript_path`, `agent_kind: pi`; on a resume too, inbox in process); `input` + `before_agent_start` → user-prompt-submit (context as a hidden message); `agent_before_settle` → stop (block → `custom_message` + `continue`, own `stop_hook_active`); `agent_settled` with an error → stop-failure. Every handler catches. | L |
| inbox + `deliver` | a unix socket per session at `${HOME}/.obor/pi/<sid>.sock`, recorded via `inbox_record`; `deliver` returns `{socket, lines}`; the extension calls `sendUserMessage` (idle) or `deliverAs: "steer"` (busy) | M (inside the extension; the Go side is S) |
| `turn` (`last`, `session`, `context`, `reset`) | the session JSONL: the last assistant message's `stopReason`/`errorMessage`/`usage`; the header line's `id`, `cwd`, `timestamp`; reset parsed from Anthropic 429 and Codex's "Try again in ~N min." | M |
| `transcript` glob | `${HOME}/.pi/agent/sessions/*/*_{sid}.jsonl` (or a `--session-dir` the start names) | S |
| MCP guard for lanes | the transport maps `mcp__<server>__<tool>` to `tool_name: mcp` against the registered server names; the extension registers the project's `.agents/mcp_config.json` servers too, since pi does not read that file | S–M |
| `not-ready` | none needed with `--approve`; otherwise a screen pattern "Trust project folder?" | S |
| conformance | `make conformance TIER=live KIND=pi` against the linked kind; static tier unchanged | M |

## What does not fit

- **No per-start settings.** The user's `~/.pi/agent/settings.json` (default provider and model, `defaultTools`,
  `extensions`, `packages`, `cacheWarming`, …) applies to every obor start. Flags override the model, thinking level,
  tools and resource discovery. `-ne` drops the user's extensions and packages. Anything else in the user's settings
  stays, and `--agent-dir` is no way out because it also moves `auth.json`. `override_files` (a project's override
  laid over the user's file) has nothing to write to. The kind would have no override file, or the extension would
  apply one in-process (for example `pi.setActiveTools`, `setThinkingLevel`), which is extra work and not probed.
- **The user's own MCP servers** (`~/.pi/agent/mcp.json`) cannot be hidden from a lane without `--no-mcp`, which also
  drops obor's. A project `.pi/mcp.json` entry `{enabled: false}` would, but that is a committed file per server. The
  guard's per-call deny of undeclared servers is the working answer. The owner has no `mcp.json` today.
- **The user's agent-dir AGENTS.md** cannot be switched off alone (`-nc` drops the project's too).
- **pi does not read `.agents/mcp_config.json` or `.agents/hooks.json`.** The extension has to carry both.
- **Extension crashes take pi down.** An uncaught throw in an async handler ended the TUI (P1), so the extension must
  wrap every handler. In `tool_call` a throw blocks the call, which is the wanted fail-safe.
- **Codex limits** are text in an error message, not headers.
- **No `stop_hook_active` from pi.** The extension tracks its own continuation.
- **Node runtime.** The extension runs in pi's own Node (pi's installer ships one), as `opencode/obor.js` does in
  OpenCode's. obor ships TypeScript source, no build.

## Suggested task split

1. **The Go kind** (`kinds/pi/kind`, linked like agy and OpenCode, `config.KitKinds`): `describe`, `start` (argv
   above, effort check against the seven levels), `install` (`where: start`), `turn` (`last`, `session`, `context`,
   `reset` from the JSONL), the `transcript` glob, and `deliver` returning `{socket, lines}`. Unit tests on the
   protocol's fakes. Size M.
2. **The extension, hooks and MCP** (`pi/obor.ts`, its tests under the pattern of `opencode/obor.test.js`): register
   `obor mcp` and the project's `.agents/mcp_config.json` servers; the five hooks (P3, P4) through `bin/obor hook
   <name>`, every handler caught; the tool map; `stop_hook_active`; the resume variable. Size L.
3. **The inbox** in the extension: the per-session socket, its path in the session-start hook's env, idle versus busy
   delivery. Size M. It can come after 2: until then the kind says `typed: true`, and typed lines work (P6c).
4. **The guard's pi rows**: headless forms (`pi -p`, `--print`, `--mode json|rpc`), `pi install/remove/update/config/
   auth/mcp` (they write user config), and `mcp__` server matching. Size S.
5. **Live conformance and docs**: `make conformance TIER=live KIND=pi`, `docs/agents/kind-protocol.md` (a third plugin
   kind), `docs/design.md` (protected: the lead's), a pi line in `obor setup mcp`. Size M.

The owner's first use, a worker on Sonnet, needs tasks 1, 2 and 4. Task 3 makes the lead's lines arrive without
typing.

## Not verified

- Escape (abort) during a run and what `agent_settled {aborted: true}` does to a worker's stop: documented to skip
  `agent_before_settle`.
- `--no-skills` together with an explicit `--skill <path>`: does the explicit path still load?
- A real rate-limit error on either provider (the error texts are from source, not observed).
- Compaction mid-session and how the transcript's last usage reads after it.
- `pi.sendMessage(…, {triggerTurn: true})` as the inbox's delivery (only `sendUserMessage` was run).
- A lane in a linked git worktree (the scratch dir was no repository): `.agents/skills` discovery stops at the
  repository root, which in a worktree is the worktree's.
- herdr's `blocked` state for an extension dialog, and whether herdr's own pi integration changes detection.
- Latency of `tool_call` spawning `obor hook pre-tool-use` per call (OpenCode's plugin does the same).

## Appendix: the probe files

### `probe.ts`

```ts
// pi probe extension: logs events, guards tool calls, a settle-time block, an inbox socket, an MCP server.
import { appendFileSync, existsSync, unlinkSync } from "node:fs";
import { createServer } from "node:net";
import { join } from "node:path";

const KIT = process.env.PROBE_KIT!;
const log = (ev: string, data: unknown) =>
  appendFileSync(join(KIT, "log", "events.jsonl"), JSON.stringify({ t: new Date().toISOString(), ev, data }) + "\n");

export default function (pi: any) {
  let ctxRef: any;
  let server: any;
  pi.registerMcpServer("probe", {
    command: "python3", args: [join(KIT, "mcp.py")], exposure: "direct", description: "probe echo server",
  });
  pi.on("project_trust", async (e: any) => { log("project_trust", { cwd: e.cwd }); return { trusted: "undecided" }; });
  pi.on("session_start", async (e: any, ctx: any) => {
    ctxRef = ctx;
    const sm = ctx.sessionManager;
    log("session_start", { reason: e.reason, id: sm.getSessionId(), file: sm.getSessionFile(), cwd: ctx.cwd, mode: ctx.mode,
      trusted: ctx.isProjectTrusted(), model: ctx.model?.provider + "/" + ctx.model?.id, thinking: ctx.thinkingLevel,
      env: Object.fromEntries(Object.entries(process.env).filter(([k]) => /^(OBOR_|HERDR_PANE|PI_)/.test(k))),
      tools: pi.getActiveTools() });
    const sock = ".probe-in.sock"; // relative to cwd: the absolute path is over the unix limit
    if (existsSync(sock)) unlinkSync(sock);
    server = createServer((c) => {
      let buf = "";
      c.on("data", (d) => { buf += d; });
      c.on("end", () => {
        const m = JSON.parse(buf);
        const idle = ctxRef.isIdle();
        log("inbox", { text: m.text, as: m.as, kind: m.kind, idle });
        try {
          if (m.kind === "message") pi.sendMessage({ customType: "obor", content: m.text, display: true }, { triggerTurn: true, deliverAs: m.as ?? "steer" });
          else if (idle) pi.sendUserMessage(m.text); else pi.sendUserMessage(m.text, { deliverAs: m.as ?? "steer" });
        } catch (err: any) { log("inbox_error", String(err)); }
      });
    }).listen(sock);
  });
  pi.on("session_shutdown", async (e: any) => { log("session_shutdown", { reason: e.reason }); server?.close(); });
  pi.on("input", async (e: any) => { log("input", { text: e.text, source: e.source, streaming: e.streamingBehavior }); });
  pi.on("before_agent_start", async (e: any) => { log("before_agent_start", { type: e.messageType, prompt: e.prompt,
    contextFiles: e.systemPromptOptions?.contextFiles?.map((f: any) => f.path ?? f), skills: e.systemPromptOptions?.skills?.map((s: any) => s.name + " " + (s.filePath ?? s.path ?? "")) }); });
  pi.on("agent_start", async () => log("agent_start", {}));
  pi.on("turn_start", async (e: any) => log("turn_start", { i: e.turnIndex }));
  pi.on("turn_end", async (e: any, ctx: any) => log("turn_end", { i: e.turnIndex, outcome: e.outcome, stop: e.message?.stopReason,
    usage: e.message?.usage, context: ctx.getContextUsage() }));
  pi.on("agent_end", async () => log("agent_end", {}));
  pi.on("agent_before_settle", async (e: any) => {
    const f = join(KIT, "ctrl", "settle-block");
    if (existsSync(f)) {
      unlinkSync(f);
      log("agent_before_settle", { block: true });
      return { entries: [...e.entries, { type: "custom_message", customType: "obor-stop", display: true,
        content: "obor: you have not called task_done. Reply with exactly the word BLOCKED-SEEN." }], continue: true };
    }
    log("agent_before_settle", { block: false });
  });
  pi.on("agent_settled", async (e: any) => log("agent_settled", { aborted: e.aborted }));
  pi.on("tool_call", async (e: any) => {
    log("tool_call", { tool: e.toolName, id: e.toolCallId, parent: e.parentToolCallId, input: e.input });
    const s = JSON.stringify(e.input ?? {});
    if (s.includes("PROBE-DENY")) return { block: true, reason: "obor: denied by the probe guard (PROBE-DENY)" };
  });
  pi.on("tool_execution_end", async (e: any) => log("tool_execution_end", { tool: e.toolName, isError: e.isError }));
  pi.on("after_provider_response", async (e: any) => {
    const h = e.headers ?? {};
    log("provider_response", { status: e.status, rate: Object.fromEntries(Object.entries(h).filter(([k]) => /ratelimit|retry-after|reset|x-codex/i.test(k))) });
  });
}
```

The first run had `const sock = join(KIT, "in.sock")`, which crashed pi (P1).

### `mcp.py`

```python
#!/usr/bin/env python3
# Minimal stdio MCP server: one tool `echo`, logs its env and cwd.
import json, os, sys
kit = os.environ.get("PROBE_KIT", ".")
with open(os.path.join(kit, "log", "mcp.env"), "w") as f:
    f.write("cwd=" + os.getcwd() + "\n")
    for k in sorted(os.environ):
        if k.startswith(("OBOR_", "HERDR_PANE", "PI_")): f.write(f"{k}={os.environ[k]}\n")
for line in sys.stdin:
    m = json.loads(line); mid = m.get("id"); meth = m.get("method")
    if mid is None: continue
    if meth == "initialize":
        r = {"protocolVersion": m["params"].get("protocolVersion", "2025-06-18"), "capabilities": {"tools": {}}, "serverInfo": {"name": "probe", "version": "0"}}
    elif meth == "tools/list":
        r = {"tools": [{"name": "echo", "description": "Echo text back", "inputSchema": {"type": "object", "properties": {"text": {"type": "string"}}, "required": ["text"]}}]}
    elif meth == "tools/call":
        r = {"content": [{"type": "text", "text": "ECHO:" + m["params"]["arguments"].get("text", "")}]}
    else:
        r = {}
    sys.stdout.write(json.dumps({"jsonrpc": "2.0", "id": mid, "result": r}) + "\n"); sys.stdout.flush()
```

### `send.py`

```python
#!/usr/bin/env python3
# send.py <repo> <json>: one line to the probe extension's inbox socket
import os, socket, sys
os.chdir(sys.argv[1]); s = socket.socket(socket.AF_UNIX); s.connect(".probe-in.sock"); s.sendall(sys.argv[2].encode()); s.close()
```

Lines sent: `{"text": …}` (idle), `{"text": …, "as": "steer"}`, `{"text": …, "as": "followUp"}`.
