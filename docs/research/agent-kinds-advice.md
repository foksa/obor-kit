# Agent kinds: agy, the kind boundary, extracting claude and codex, non-Claude leads

Advisor answer, 2026-11-22. Question: add an agy (Antigravity CLI 1.5.14) kind like `kinds/opencode`, make the kind
boundary ready for more kinds, and let a lead run on an agent other than Claude Code.

Sources: the live probes in `docs/research/agy-probes.md` (P1 to P9; cited as "probes P<n>"), `agy --help`,
`agy models`, the docs agy ships (`~/.gemini/antigravity-cli/builtin/skills/agy-customizations/docs/*.md`), the public
docs at antigravity.google/docs (read through a fetch agent, paraphrased), and obor's code. Owner decisions of 11-22
(`docs/decisions.md:7`): agy is a plugin kind; a lead with no inbox may get its lines through its hooks (the lesser
mode); the first non-Claude lead is agy if P7 passes, else OpenCode; codex then claude are extracted after agy has run
in lanes for a few days.

## Recommendation

Build agy as a plugin kind in two tasks: start and wiring, then hooks. Wire it with two files committed in the
project, `.agents/mcp_config.json` and `.agents/hooks.json`, that name `obor` by `PATH`: no agy plugin, no
`--add-dir`, nothing user-level. Start workers in auto mode, set in the start's `--settings` file, and let a project
widen it in its override file. Make agy the first non-Claude lead on the lesser mode, and add push delivery through
`agentapi` as a later, separate task.

Tasks 1 (the probes, T-1458) and 2 (protocol prep, T-1459: the kind's context read, `headless.flags`, `caps.app`,
the hook contract) are merged; the numbers below continue theirs.

| # | Task | Size | Difficulty | Files |
|---|---|---|---|---|
| 3 | **kind-agy, start and wiring.** `kinds/agy` as a Go binary: describe, start (model, effort, the `--settings` file, resume, first prompt), install, turn (reset, context). obor's MCP server through the project's `.agents/mcp_config.json`, which `obor setup agents` writes: the install reply gains `where: project` and the request `root`. No hooks yet: the sandbox of auto mode is the lane's guard, as Codex's is. The context read goes to the kind of the recorded session and is bounded (`project.ContextOf`, `internal/project/files.go:55`, asks every transcript kind in name order). The live tier also settles P10 (section 1.4). | L | medium | `kinds/agy/main.go`, `kinds/agy/kind/*.go`, `internal/agents/agents.go` (the `Where` values, lines 400-401), `internal/agents/plugin/calls.go` (install), `internal/setup/mcp.go`, `internal/project/files.go`, `internal/config/models.go` (`KitKinds`, line 99; the catalogue, lines 34-41), `Makefile`, `bin/obor` and the release list, `tests/conformance/live_agy.go`, `docs/design.md`, `docs/agents/kind-protocol.md`, `AGENTS.md` |
| 4 | **agy hook transport.** The project's `.agents/hooks.json` runs `obor`, which runs the kind binary; it maps agy's events to `obor hook <name>` and the answers back (section 1.6). Caps `hooks: plugin`, the plugin marker, the SessionStart record. The protocol's Hook transport section gains three statements (section 2). | M | hard | `kinds/agy/kind/hook.go` and its test, `cmd/obor/main.go` (one subcommand that runs a kit kind's binary), `internal/setup/` (the hooks entry), `tests/conformance/live_agy.go`, `docs/design.md`, `docs/agents/kind-protocol.md` |
| 5 | **A role's start prompt for every kind.** A kind that takes no plugin slash command gets a plain first prompt naming the skill file; the SessionStart line for a lead names the file too; the lead skill's Claude-only wording goes. agy is such a kind (no plugin, so no `/obor:lead`). | S | simple | `internal/roles/roles.go:57,207-223`, `internal/guard/session.go:136-149`, `skills/lead/*.md`, `templates/agents/lead.md` |
| 6 | **Lead lines through hooks (the lesser mode).** `obor hook user-prompt-submit` and `obor hook stop` hand a lead with no inbox its queued lines: as context at a prompt, as a block of the stop at a turn's end. Kind-neutral. Then a lead on agy in a scratch project is the acceptance test. | M | hard | `internal/guard/{prompt,stop}.go`, `cmd/obor/main.go:318`, `internal/app/messages.go`, `internal/app/wire/`, `docs/design.md:834-849` |
| 6b | **agy push delivery.** Any `obor` command an agent runs records the kind's inbox variables; the protocol's delivery gains "the kind delivered it"; the agy kind sends with `agentapi send-message`. A refused send leaves the line to task 6's path. | M | hard | `internal/guard/session.go:193-203`, `cmd/obor/main.go`, `internal/agents/agents.go` (`Delivery`, line 441), `internal/agents/plugin/calls.go:408`, `cmd/obor/send.go:152`, `kinds/agy/kind/*.go` |
| 7 | **A kit kind with no cached manifest is described at a start** (`plugin.go:189` fails such a start today). Before the lead's kind is a plugin. | S | medium | `internal/agents/plugin/plugin.go`, `internal/config/models.go`, `docs/agents/kind-protocol.md` |
| 8 | **Extract codex** to `kinds/codex` (section 3). | L | hard | `kinds/codex/**`, `internal/agents/codex/**` (deleted), `internal/agents/agents.go`, `internal/agents/plugin/*.go`, `internal/launch/launch.go:132,931`, `internal/setup/mcp.go:147`, `internal/project/files.go:176`, `internal/config/models.go:74-82`, `internal/testkit/testkit.go`, `tests/conformance/live_codex.go`, release files |
| 9 | **Extract claude** to `kinds/claude`; then `Split`, `Preparer`, the two-pass `StartOf`, the registry and the closures in `agents.Start` go. | L | hard | `kinds/claude/**`, `internal/agents/claude/**` (deleted), `internal/agents/agents.go:569-743`, `internal/launch/launch.go:1145-1180`, `cmd/obor/main.go`, release files, `docs/module-map.md` |

Order: 3, 4, 5, 6, then 6b; 7 to 9 after agy has run in lanes for a few days. Not done: the review's `BuildStart`
and interface slimming (owner 11-22), a `check-effort` call, an agy plugin shipped by the kit.

## 1. kind-agy

### 1.1 What the probes settled

| Probe | Result | Consequence |
|---|---|---|
| P1 | Workspace `.agents/hooks.json`, `.agents/mcp_config.json` and skills load with no prompt in a trusted folder; a linked worktree loads its own copy. A `plugins.json` entry loads only a plugin whose real path is inside a workspace root. | wiring by committed workspace files (1.5) |
| P2 | An MCP server and a hook inherit the agent's whole environment, `PATH` and the `OBOR_*` variables included. | a static file can run `obor mcp` |
| P3 | Tool names `run_command`, `write_file`, `edit_file`; a PreToolUse answer of `{}`, a non-zero exit or a timeout **denies**; `ask` is the neutral answer; SessionStart does not fire on `--conversation`. | the transport's mapping (1.6) |
| P4 | `--settings` is laid over the saved settings by top-level key, a key it names replacing the saved value; trust is by exact path. | the start's settings file (1.4) |
| P5 | `--effort` works only with a model's base name; a wrong model or effort runs Gemini 4 Neon (High) with a notice, the start succeeding. | model and effort (1.3) |
| P6 | `-i` carries a first prompt, also on a resume; a plugin's skill is `/<plugin>:<skill>`. | first prompt in the argv |
| P7 | `agentapi send-message` wakes an idle session and reaches a busy one; its token is only in the environment of commands the agent runs. | lead lines (4.2) |
| P8 | `/exit` and one Enter quits; Ctrl+U clears the input; the trust dialog's default is "Yes, I trust this folder". | the manifest's stop |
| P9 | Auto mode's sandbox passes build, tests and git reads and refuses the network, unix sockets and writes outside the workspace, temp dirs and caches. A PreToolUse deny is final. | fixed args (1.4) |

### 1.2 Start, resume, first prompt, stop

- Start: `agy [flags]`, the first prompt as `-i <text>`. Resume: `--conversation <id>`; the wake of a resumed session
  is typed once it is up, as for every kind (`launch.go:775-812`).
- No caller-chosen session id: caps `named_session: false`. Until task 4 the id is herdr's `agent_session` (herdr's own
  hook reports the `conversationId`; launch adopts it for a kind that names none, `launch.go:733`).
- Prompts are typed (`typed: true`); agy queues text submitted during a turn. A typed line that starts with `/` can
  be changed by the completion popup's Enter (probes P6): obor's typed lines start with `[obor`, and a first prompt is
  in the argv.
- Stop: `quit: "/exit"`, no second Enter, `clear_input: ["ctrl+u"]`, no dialog. A quit kills the session's background
  tasks without asking (probes P8).
- Headless forms: `print_flag: true`, `flags: ["--prompt"]`, `subcommands: ["acp"]`, `value_opts` from `--help`.
- Status `terminal` (herdr detects agy). Every start creates a conversation, so obor resumes by id, never `-c`.

### 1.3 Models and effort

`agy models` lists slugs with the effort as their last word (`gemini-4-neon-high`, `gemini-3.8-flash-low`, …).
`agents.models.<name>.id` is a slug, caps `model_arg: true`. With no effort in the ref the kind passes the slug alone.
With one it drops the slug's effort word and passes `--model <base> --effort <effort>`; a slug with `--effort` is the
conflict probes P5 shows.

agy accepts a wrong model or effort and runs another model, so the start must catch it. The kind keeps no table of
models (agy's list changes with its releases): the manifest's `not_ready` pattern matches the notice on the start
screen (`Using "…" instead.`, `is not recognized as a known model`), which makes the start fail with that reason. The
manifest's `efforts` are `low, medium, high, xhigh, max`; `obor config validate` checks a ref against them and no
further.

### 1.4 The start's settings file: mode, permissions, trust (D1)

`--settings <file>` is the per-start channel and agy's own format. The kind writes one file per kind of start to the
state dir (trimmed and not, as OpenCode's `kind.go:41-46`). Because a key in it replaces the user's saved value (probes
P4), each key the kind writes is the user's saved value with obor's addition: the kind reads
`~/.gemini/antigravity-cli/settings.json` itself. The project's `.obor/agy/settings_override.json` is merged between
the two (JSON Merge Patch), and obor's own entries are put back last, as OpenCode's `override.go:35-66` does.

- **Mode: `agentMode: "auto"` for every agent but a lead, no mode flag.** Auto mode asks nothing and runs commands
  in the sandbox; the other modes ask before every command (probes P9), which stops an unattended lane.
- **`permissions.allow` gets `mcp(obor/*)`**, or every obor tool call asks (probes P4, P9). The server is the workspace
  one, so its name is `obor` with no plugin prefix.
- **A project widens the sandbox in its override file, in agy's permission rules.** The public docs say a
  `write_file(<path>)` or `read_file(<path>)` allow rule mounts that path in the sandbox, `read_url(<domain>)` adds it
  to the sandbox's outbound allowlist, and a `command(<prefix>)` rule lets the command run "without prompting, even
  when it needs to run outside the sandbox". No key for unix sockets is documented. That last rule is what obor-kit
  needs: its worker doc has a worker run `go test -tags integration` of its packages before `task_done`
  (`docs/agents/worker.md:16`), which uses unix sockets, and `obor run` and `obor spec show` dial serve's socket. None
  of this was probed. **P10**, in task 3's live tier: with `permissions.allow: ["command(obor)", "command(go test)"]`
  in the `--settings` file, do `obor status` and a test that opens a unix socket run in auto mode with no prompt; does
  `read_url(proxy.golang.org)` let a module download through.
- **`--dangerously-skip-permissions` (the setting `toolPermission: "always-proceed"`) is not the default.** It removes
  the prompts and, as the docs read, the sandbox. Before task 4 nothing would stand between such a worker and the
  machine; after it, obor's guard does, and its deny is final (probes P9). A project whose lanes cannot work in the
  sandbox sets it in its override file once task 4 is live. It is the fallback for obor-kit if P10 fails.
- **Usable before task 4: yes, in auto mode.** The sandbox does the guard's main jobs for a worker: no write outside
  its lane (a linked worktree's git dir is in the main checkout, so git writes fail too) and no network. It has the
  standing of a Codex lane (`docs/design.md:23`): no guard on what the role text forbids inside the lane, no context
  line, no session record but herdr's. In obor-kit such a lane takes tasks whose tests need no socket until P10 is
  answered.
- **Trust.** Trust is by exact path and each lane worktree is its own path, so each shows the dialog once (agy saves a
  "Yes"). Recommendation: the kind adds the start dir to the file's `trustedWorkspaces` when the project's root is in
  the user's saved list, and only then; the owner trusts a repository once, in its main checkout, and its lanes
  follow, which is what obor's trust hint already says ("once per repo", `agents.go:359-365`). A start in a project
  the owner has not trusted shows the dialog and raises the trust needs-you as for every kind. Decision D7.
- **No trim of the user's plugins, skills, hooks and MCP servers.** They live in `~/.gemini/config`, outside
  `settings.json`, and no per-session switch is known. On this machine every agy agent loads `chrome-devtools-plugin`,
  `modern-web-guidance-plugin` and the `lit-site-manager` MCP server. The user's own hooks can also deny a call before
  obor's run (probes P3: the first deny wins, the order is not fixed).

### 1.5 Wiring: obor's MCP server and hooks (D2)

| Way | For | Against |
|---|---|---|
| **Committed `.agents/mcp_config.json` and `.agents/hooks.json` naming `obor` by `PATH`** (recommended) | agy's native workspace files, loaded with no prompt (probes P1); every lane has them through git, as it has `.claude/settings.json`; the kit stays outside the agent's workspace and the sandbox's writable paths; nothing user-level; the server's name is plain `obor` | the command is the `obor` on the agent's `PATH`, not the kit pinned by path; the owner's own agy sessions in the project load them too |
| `--add-dir <kit>/agy` and a `plugins.json` entry | a kit-shipped plugin, the skill as `/obor:lead` | the kit dir joins the session's workspace, where auto mode approves edits and the sandbox allows writes (not probed, but that is what a workspace root is): an agent could rewrite its own guard's wiring; the entry's path differs per machine; a plugin's skills must be a real dir inside it (probes P1), which the kit's `skills/` is not |
| A copy of the plugin written into the worktree | no flag | dirties every lane; collides with a project's own committed `.agents/plugins.json` |
| `agy plugin install` at user level | loads everywhere | user-global (`agents.go:45-46`); a copy that goes stale at each kit upgrade; `PLUGIN_ROOT` is the copy, so its commands would have to name the kit's path |

How the recommended way works:

- `.agents/mcp_config.json` holds `{"mcpServers": {"obor": {"command": "obor", "args": ["mcp"]}}}`. The server inherits
  the agent's `PATH` and identity variables (probes P2). In obor-kit's lanes the unpinned shim runs the main checkout's
  binary (`bin/obor`'s self-hosting); elsewhere `obor` is the installed kit (`~/.local/bin/obor`). `obor setup mcp`
  warns when the `obor` on `PATH` is not the kit that runs it.
- The protocol's install reply gains a third `where`, `project`: a file of the project's root, the entry's key and
  the entry. `obor setup agents` writes the entry into the file (keeping the project's other entries), the lead
  commits it, `obor setup mcp` checks it. The request gains `root`. This is the "through the project's own files" the
  protocol's install paragraph already allows; the reply still names nothing user-global.
- Task 4 adds `.agents/hooks.json` the same way (one named hook, `obor`).
- `.agents/` goes into the project's `rules.protected_files`, so no worker edits its own wiring; the integrator's
  accept sees any diff there in the meantime.
- If the `obor` on `PATH` is missing or broken, every tool call of every agy session in the project is denied once
  task 4 is in (a failed PreToolUse hook denies, probes P3). That is the guard's own rule for a missing binary; the
  setup check names it.

### 1.6 Hooks (task 4)

`obor hook <name>` reads Claude Code's event JSON and answers in Claude's forms. The agy kind binary is the transport,
as `opencode/obor.js` is OpenCode's. The hook command is `obor` with one subcommand that execs the kit's kind binary
(`config.KitKind`, `models.go:121`), which rewrites the event, runs `obor hook <name>` and rewrites the answer. That is
two more process starts per tool call than Claude's hook; the task measures it against the budget
(`docs/design.md:20`).

| agy event | obor hook | In | Out |
|---|---|---|---|
| `SessionStart` (a new conversation's first prompt, and after `/clear`) | `session-start` | `conversationId` → `session_id`, `transcriptPath` → `transcript_path`, `cwd` | the context lines → `injectSteps: [{systemMessage: {systemMessage: …}}]`, which stays in the conversation |
| `PreToolUse`, matcher `run_command\|write_file\|edit_file` | `pre-tool-use` | `run_command` → `Bash`, `args.CommandLine` → `command`; `write_file` → `Write`, `edit_file` → `Edit`, `args.Path` → `file_path` | a deny → `{"decision": "deny", "reason": <obor's line>}`; an allow → `{"decision": "ask"}`, which leaves the call to agy's own check |
| `PreInvocation` with `invocationNum: 0` | `user-prompt-submit` | `lastUserInput` → `prompt` | the context → `injectSteps` |
| `Stop` | `stop`; with `terminationReason: ERROR`, `stop-failure` | `executionNum > 0` → `stop_hook_active`; `fullyIdle: false` → one running entry of `background_tasks`; `error` → `error` | a block → `{"decision": "continue", "reason": …}` |

- **The transport always answers.** An empty answer, a non-zero exit or 30 s without one denies the tool (probes P3).
  For `pre-tool-use` a failure of `obor hook` is a deny with its reason; outside an obor project the answer is `ask`.
- **A resume needs no SessionStart.** Launch chose the id it resumes, the context line is already in the conversation
  (an injected message persists, probes P3), and the plugin marker is the first session's. So the session record
  comes from `SessionStart` alone.
- A turn that a message or a finished background task starts also has `invocationNum: 0`, with the previous prompt in
  `lastUserInput`. The handoff reminder does not read the prompt, and the wrong-tab guard gives the same verdict it
  gave that prompt the first time.
- Esc on agy's permission prompt ends a turn with no Stop: nothing in obor may wait for a stop that has to come.
- The transcript the hooks name is `transcript_full.jsonl`; `transcript.jsonl` beside it has the same token fields and
  shorter lines. The context read works on either; the manifest's glob names `transcript.jsonl`.
- There is no StopFailure event, and a Stop with an error or a usage limit has not been seen. `turn` `reset` answers
  "limited, no reset time" for an error text that names a limit, until one is seen.

### 1.7 Transcript and context size

`~/.gemini/antigravity-cli/brain/<conversationId>/.system_generated/logs/transcript.jsonl`; a `PLANNER_RESPONSE` line
has `input_tokens` and `cache_read_tokens`, whose sum is the context, and `created_at`. The first line names no session
or cwd (caps `sessions: false`). The kind answers `turn` `what: context` with it.

## 2. Hardening for more kinds

The review's findings in §4.1 are true, and a new kind is served by `plugin.Kind` without them. The protocol has
what agy's transcript, print alias and app name need: the kind's context read (`turn` `what: context`),
`headless.flags`, `caps.app`, and the hook contract ("Hook transport" in `docs/agents/kind-protocol.md`). What agy
still needs:

| Change | Task | Why |
|---|---|---|
| The context read routed by the recorded session's kind, and bounded | 3 | `project.ContextOf` asks every transcript kind in turn; agy is the first plugin kind with a transcript, and a plugin's `turn` may take 5 s (`calls.go:26`) inside a lead's hook |
| `install`: `where: project`, the request's `root` | 3 | wiring through the project's own files |
| Three statements in the hook contract | 4 | the probes: an allow is "no opinion", never an approval, and a transport maps it to its CLI's fall-through (agy's `ask`); what a transport answers when `obor hook` fails or is too slow, per hook (`pre-tool-use`: deny; the others: nothing); `session-start` runs once per conversation the CLI starts, again after a clear, and not for a resume |
| The inbox record written by any `obor` command; a delivery the kind performs | 6b | agy's token is not in a hook's environment |

Worth doing for every kind, with the extraction: a kit kind described at a start with no cache (task 7); a transcript
glob with a fallback; `install` with the lane's args; warnings and a refusal in replies; errors returned from `turn`
and `deliver` in place of the package's `Log` writer (`calls.go:32`).

Not worth doing: `BuildStart` and interface slimming as their own tasks (tasks 8 and 9 delete `Split`, `Preparer`
and the two passes, `agents.go:684-735`); a `check-effort` call (the manifest's list, and for agy the start screen);
an agy plugin or per-start generated agy config.

## 3. Extracting kind-codex and kind-claude

Owner 11-22: codex, then claude, after agy has run in lanes for a few days; each one task that moves the kind and
deletes the compiled package.

### 3.1 Size

| Kind | Code | Tests | Shape |
|---|---|---|---|
| opencode (plugin today) | 404 lines (`kind.go`, `override.go`) | 486 | the reference |
| codex | 789 (`codex.go`) | 787 | start from `-c` options, the user's `config.toml` and `hooks.json`, turns and sessions from the rollout file, the quarantine check |
| claude | 652 (`claude.go`) | 615 | two state files per agent (`Prepare`, `claude.go:183`), strict MCP, the manifest registration, the inbox |

The code moves almost unchanged: a kind method becomes a call's handler, and the closures (`Start.Read`, `Dirs`,
`WriteState`, `Env`) become the binary's own file reads. The work is in the protocol gaps and the upgrade.

### 3.2 What the protocol lacks

| Gap | Where it shows | Fix |
|---|---|---|
| The start request has no agent name | Claude writes `settings/<agent>.json` and `mcp/<agent>.json` (`claude.go:166-167`) | add `agent`, or name the files by content |
| `mcp_lane` is static | Claude's answer depends on the lane's args and on a file an arg names (`claude.go:500-533`) | `install` takes `args` |
| No warnings or refusal from the kind | Codex's quarantine (`InstallChecker`, `StartChecker`, `agents.go:326-344`), both kinds' override warnings | `install` replies `warnings`; `start`'s `error` is the refusal; `project-warnings` takes the overrides |
| The transcript glob has no fallback | `$CLAUDE_CONFIG_DIR` else `~/.claude` (`claude.go:577-583`), `$CODEX_HOME` else `~/.codex` | a list of globs, the first with every variable set |
| The inbox record's shape | Claude's is `{"socket", "token"}` (`claude.go:688-701`); a manifest's is variable name → value | the Claude kind's `deliver` reads both, so a lead running across the upgrade keeps its record |
| A turn read costs a process | Codex's status is `transcript`: `turn` `what: last` per transcript poll, per Codex pane (`calls.go:26,343`) | measure first; if it shows, the manifest names the rollout's event types as data |
| The kind's name in the bridge | `agents.Default = "claude"` (`agents.go:496`), `TrustHint`'s branch on it (`agents.go:359-365`), the base args and hc rows (`config/models.go:74-82`) | the trust hint's wording and the base args move into the kinds; `Default` stays a name |

### 3.3 What it deletes

- `internal/agents/claude` and `internal/agents/codex` (they become `kinds/claude/kind`, `kinds/codex/kind`).
- In `internal/agents/agents.go`: `Split`, `Preparer`, `Whole` as a separate interface, `StartOf`'s two passes
  (712-735), the registry and the compiled-first lookup (569-630), `Fallible` and `InstallOf` (671-673, 738-743),
  `InstallHost`, `InstallChecker`, `StartChecker` (318-344), and of `Start` the fields only a compiled kind reads
  (`Read`, `Dirs`, `WriteState`, `Env`, `SettingsFile`, `MCPFile`, `Agent`): about fifteen fields remain, the request's.
  The optional interfaces fall from fifteen to about five.
- In `internal/launch`: the install-host plumbing (`launch.go:132,931`), the state-file writer and the closures of the
  start (`launch.go:1145-1180`); in `internal/setup`, `mcp.go:147`; in `internal/project`, `files.go:176`; in
  `internal/config`, the base-args and hc tables (`models.go:74-82`).
- The helpers the kinds share (`MergePatch`, `ParseOverride`, `UserDir`, `UserSkills`, `SetFlag`, `ResetIn`) stay in
  `internal/agents`: `kinds/opencode` already imports it.

### 3.4 Risks

- **The lead runs on claude.** After task 9 every lead start, and the SessionStart hook's inbox record, depend on the
  cached manifest of a plugin kind. Task 7 removes the failed start; the cache has to be written before the first
  session of the new version, which serve's start and `obor setup agents` do. The upgrade guide gets one ordered step.
- **Hooks.** Claude's hooks stay as they are: the kit root is the Claude plugin, and `obor hook` never runs a kind's
  command. The guard reads the kinds' headless forms from the cache; with none it denies the kind's program outright.
- **Upgrade of obor-kit and axis-alies.** Both configs name claude and codex only through `agents.models.<m>.kind`;
  with `KitKinds` holding every kit kind no config line changes. The release ships four binaries per platform.
- **Speed.** One process per transcript poll for Codex has to be measured in task 8 before the compiled path goes.
- **Both paths at once.** Each extraction moves the kind and deletes the package in one task, gated by the kind's
  live conformance tier. No flag chooses between a compiled and a plugin claude.

## 4. Non-Claude leads

### 4.1 What ties the lead to Claude Code today

| Tie | Where | Neutral? |
|---|---|---|
| The first prompt is a plugin slash command | the lead role's `Prompt: "/obor:lead"` (`roles.go:57`); only a kind with caps `skills` gets it (`roles.go:207-223`, `launch.go:800-810`) | no: task 5 |
| The skill ships as the Claude plugin's | `skills/lead/`; the SessionStart line says "run /obor:lead" (`session.go:149`) | the file is a plain `SKILL.md`: task 5 |
| The skill's text | "after a Claude Code update" (`skills/lead/SKILL.md:16`), `/clear` | nearly: task 5 |
| Hooks: guard, context, handoff reminder, the background-run stop | `hooks/hooks.json`; `stop.go:46-48` reads Claude's `background_tasks` | the transport is per kind; the rules are neutral |
| Lines to the lead are posted to the session's inbox, never typed | `docs/design.md:834-849`; only Claude has an inbox (`claude.go:682`) | no: task 6, then 6b |
| Session id and resume | `launch.sessionOf` (`launch.go:720`) is by capability | yes |
| `obor lead`, `obor restart <lead> --handoff` | `cmd/obor/lead.go`, `restart.go:23-72` | yes |
| The handoff reminder | `project.ContextOf` (`internal/guard/prompt.go:116`) | yes: the kind reads its transcript |

`AskUserQuestion` is not a tie: the skill asks through whatever the client has.

### 4.2 Lines to an agy lead (P7)

`agentapi send-message <conversationId> <text>` does what a lead line needs: an idle session starts a turn, a busy
one reads it at its next model call, the owner's draft stays (probes P7). Its address and token
(`ANTIGRAVITY_LS_ADDRESS`, `ANTIGRAVITY_CSRF_TOKEN`) are only in the environment of commands the agent runs.

**Recommendation: read P7 as passed and make agy the first non-Claude lead, on the lesser mode first.**

- Task 6, the lesser mode, is the base and is kind-neutral: at a prompt the hook adds the queued lines as context, at
  a turn's end it blocks the stop with them, so a lead that just finished goes on with its lines. An idle lead has
  `obor status` and the needs-you notice (owner 11-22). With tasks 3 to 6 an agy lead has the start, the guard, the
  handoff reminder, resume and its lines.
- Task 6b adds the push. The sound way to get the token is that obor takes it itself: any `obor` command run by an
  agent, with a role and the kind's `inbox_record` variables in its environment, writes the session's inbox record
  (0600 in the state dir, as Claude's messaging token is, `docs/design.md:837`). The lead's start checks are
  `obor whoami` and `obor status`, so the record exists within the session's first turn with no instruction to the
  model, and every later `obor` command refreshes it. The kind then sends (`agentapi send-message`), which needs the
  protocol's delivery to say "the kind delivered it".
- The token's lifetime was not probed. The language server lives in the `agy` process (`cli.log`: "CLI program
  exited … Language server shutting down"), so expect a new address and token per start. A stale record gives a
  refused send, which obor already records and retries; the line then goes by task 6's path, and a line is delivered
  by whichever path takes it first.
- The weak point: the docs describe the two variables for commands the agent runs, and agy keeps the token out of
  hooks and off disk. A send from serve with a saved token is outside what they describe, and a release can end it.
  That is why the push is an addition to the lesser mode and never the only path.
- **An agy lead cannot work in auto mode's sandbox as it is**: `obor status`, `obor dispatch` and every command that
  dials serve use a unix socket (`internal/app/wire/client.go:205`). The lead's settings file needs
  `permissions.allow: ["command(obor)"]` (if P10 passes) or `toolPermission: "always-proceed"`. A lead is not trimmed,
  so the kind writes only this and the MCP rule.

If the owner reads the rule strictly (P7 passes only when obor can send unaided), OpenCode goes first. I would still
start with agy: on the lesser mode alone it is the more complete lead. An OpenCode lead has no resume (`kind.go:133`),
no transcript and so no handoff reminder, and a stop verdict that is not acted on, so it gets lines only at a prompt.

### 4.3 What each kind would need

| | codex | opencode | agy |
|---|---|---|---|
| Start prompt | task 5 | task 5 | task 5 |
| Hooks | none today (`codex.go:814`); Codex's own hooks need a trusted project and a trust review per hook, and no `-c` option runs one (`codex.go:279-288`): a task of its own | through `opencode/obor.js`; its stop verdict is not acted on | task 4 |
| Lines to the lead | lesser mode needs its hooks first; `codex queue --thread` against a `--no-daemon` TUI is not verified (`docs/research/agent-cli-and-herdr-capabilities.md` §3.5) | lesser mode at a prompt; a socket in its plugin (`ctx.session.prompt`, docs only) could be an inbox with today's `Delivery` | task 6, then 6b |
| Resume | yes | no | yes |
| Context size | has it | none | task 3 (the kind's `turn` `what: context`) |

### 4.4 What belongs in the kind protocol

The hook transport's contract (in the protocol document); how a kind takes a role's start prompt (task 5: the role names the skill,
a kind with caps `skills` gets its slash form, any other the plain prompt naming the file); delivery as typed, a socket
and lines, or done by the kind (task 6b); context size (the `turn` call). Not in the protocol: the lead skill's text,
`obor lead`'s flow, question tools.

## Owner decisions

- **D1. agy's mode.** Recommended: auto mode for every agent but a lead, set as `agentMode` in the start's settings
  file; a project widens the sandbox or sets `toolPermission: "always-proceed"` in `.obor/agy/settings_override.json`,
  the second only once task 4 is live. No mode in the fixed args.
- **D2. agy's wiring.** Recommended: `.agents/mcp_config.json` and `.agents/hooks.json` committed in the project,
  written by `obor setup agents`, naming `obor` by `PATH`; `.agents/` protected. And: agy agents load your user-level
  agy plugins and MCP server until a switch is found, unless you switch those off yourself.
- **P7 and the first lead (D4).** Recommended: count P7 as passed, agy first, on the lesser mode (task 6); the push
  (task 6b) after it.
- **D7. Trust of lane worktrees.** Recommended: the kind trusts a start dir for the session when the project's root
  is in your saved `trustedWorkspaces`. The other choice: every lane shows the dialog once and you press Enter.
- **D6. Codex hooks**: only if you want a Codex lead. Not in this plan.

## Concerns

- `docs/design.md:23` says hooks cover only Claude Code panes; OpenCode lanes run obor's hooks through its plugin.
  The line should say which kinds have a guard, and task 4 changes the answer again.
- agy fails closed where Claude fails open: a slow or broken hook denies the call. A fault in obor's transport stops
  every agy agent of the project at once. Task 4's tests should cover each failing path of the transport.
- agy's hook payloads and settings keys changed in most of its last seven releases, and it updates itself. The kind
  needs its live conformance tier at the nightly, not only at a release.

## Not verified

- **P10**: that a `command(…)` allow rule lets a command leave the sandbox (a unix socket) with no prompt, and that
  `read_url(…)` opens the network for a sandboxed command. From the public docs as a fetch agent paraphrased them; I
  did not read the pages.
- What `toolPermission: "always-proceed"` in a `--settings` file does to the sandbox; `--dangerously-skip-permissions`
  and `--sandbox` (probes: not run).
- That the start screen still shows the model notice when the start's not-ready poll reads it, and what launch does
  with the running agent after such a failed start.
- Whether a failing SessionStart, PreInvocation or Stop hook blocks the turn (the probes cover PreToolUse).
- The lifetime of the agentapi token; whether `agentapi` takes a message of several lines.
- A Stop with `ERROR`, and a usage limit's text.
- Whether auto mode approves an edit of `.agents/mcp_config.json` in the workspace (agy's changelog calls it a
  protected configuration file).
- That agy's context is `input_tokens + cache_read_tokens`: inferred from the numbers in the transcripts here.
- The time of the hook chain with two more process starts (task 4).
- The cost of a `turn` process per Codex transcript poll; Codex's `queue` and OpenCode's `ctx.session.prompt`.
