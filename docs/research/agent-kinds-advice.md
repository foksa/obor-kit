# Agent kinds: agy, the kind boundary, extracting claude and codex, non-Claude leads

Advisor answer, 2026-11-22. Question: add an agy (Antigravity CLI 1.5.14) kind like `kinds/opencode`, make the kind
boundary ready for more kinds, and let a lead run on an agent other than Claude Code.

Sources: `agy --help` and its subcommands' help, `agy models`, `agy changelog`, the docs agy ships
(`~/.gemini/antigravity-cli/builtin/skills/agy-customizations/docs/*.md`, `conversations/SKILL.md`), the public docs at
antigravity.google/docs (read through a fetch agent, paraphrased), the files under `~/.gemini`, strings of the `agy` and
`agentapi` binaries, and obor's code. **No agy session was started**: every statement about agy's behaviour at run time
is from documents and is marked where a live probe has to confirm it (probes P1 to P9, section 1.1).

## Recommendation

Write agy as a plugin kind now, in two tasks (start and wiring first, hooks second), behind one short probe task. Change
the protocol only where agy shows a gap. Extract codex, then claude, after agy is live, and skip the review's
standalone `BuildStart` and interface clean-up: the extraction deletes that code. A non-Claude lead needs three
kind-neutral pieces (hooks through the kind, a start prompt that is not a Claude slash command, lines that reach the
session); the first two are small, the third needs a decision from the owner.

Order of steps. Each is one task; 1 and 2 run in parallel, 5 can run any time after 2.

| # | Task | Size | Difficulty | Files |
|---|---|---|---|---|
| 1 | **agy probes.** Run P1 to P9 in a throwaway herdr session and a scratch repository; write the answers to `docs/research/agy-probes.md`. No code. | S | simple | `docs/research/agy-probes.md` |
| 2 | **Protocol prep.** (a) Context size is the kind's: `agents.Kind` gains a context read of a transcript tail, the Claude and Codex parsers leave `internal/project/files.go:58-121` for their kind packages, the protocol's `turn` gains `what: context`. (b) The manifest's `headless` gains a list of long flags (`agy --prompt` is headless and `print_flag` covers only `-p` and `--print`, `internal/guard/shell.go:489-497`). (c) The manifest's `caps` gains `app` (`agents.Caps.App`, `agents.go:444`, has no manifest field, `plugin.go:60-70`). (d) `docs/agents/kind-protocol.md` gains a "Hook transport" section: the event fields `obor hook <name>` reads and what it answers. | M | medium | `internal/agents/agents.go`, `internal/agents/claude/claude.go`, `internal/agents/codex/codex.go`, `internal/agents/plugin/{plugin,calls}.go`, `internal/project/files.go`, `internal/guard/{prompt,shell}.go`, `internal/app/keeplane.go`, `docs/agents/kind-protocol.md` |
| 3 | **kind-agy, start and wiring.** `kinds/agy` as a Go binary like `kinds/opencode`: describe, start (model, effort, `--settings` file, resume, first prompt), install, turn (reset, context). obor's MCP server through a native agy plugin the kit ships. No hooks yet: an agy lane is guarded by its role text, as a Codex lane is today (`docs/design.md:23`). Live conformance tier. | L | medium | `kinds/agy/main.go`, `kinds/agy/kind/*.go`, `agy/obor/{plugin.json,mcp_config.json}`, `internal/config/models.go` (`KitKinds`, line 99; the model catalogue, lines 34-41), `internal/setup/` (the project's `.agents/plugins.json`), `Makefile`, `bin/obor` and the release list (one more kind binary), `tests/conformance/live_agy.go`, `docs/design.md`, `docs/agents/kind-protocol.md`, `AGENTS.md` (the conformance line names the kinds) |
| 4 | **agy hook transport.** `agy/obor/hooks.json` runs the kind binary, which maps agy's six events to `obor hook <name>` and the answers back (section 1.6). Caps `hooks: plugin`, the plugin marker, the session recorded by the SessionStart hook. | M | hard | `kinds/agy/kind/hook.go` and its test, `agy/obor/hooks.json`, `cmd/obor/main.go` (one subcommand that runs a kit kind's binary), `tests/conformance/live_agy.go`, `docs/design.md` |
| 5 | **A role's start prompt for every kind.** A kind that takes no plugin slash command gets a plain first prompt naming the skill file; the SessionStart line for a lead names the file too; the lead skill's Claude-only wording goes. | S | simple | `internal/roles/roles.go:57,207-223`, `internal/guard/session.go:136-149`, `skills/lead/*.md`, `templates/agents/lead.md` |
| 6 | **Lead lines for a kind without an inbox.** After the owner's decision D3: the protocol's delivery gains "the kind delivered it" beside typed and socket; the agy kind delivers through its own channel if P7 passes. Then a lead on agy in a scratch project is the acceptance test. | M | hard | `internal/agents/agents.go` (`Delivery`, line 428), `internal/agents/plugin/calls.go:391`, `cmd/obor/send.go:140-170`, `internal/app/messages.go:412-416`, `kinds/agy/kind/*.go`, `docs/design.md:834-849` |
| 7 | **A kit kind with no cached manifest is described at a start.** Today a start of an uncached plugin kind fails with "run obor setup agents" (`plugin.go:187-200`). Needed before the lead's kind is a plugin. Hooks and the guard still never run the command. | S | medium | `internal/agents/plugin/plugin.go`, `internal/config/models.go`, `docs/agents/kind-protocol.md` |
| 8 | **Extract codex** to `kinds/codex` (section 3). Deletes `internal/agents/codex`, the install-host plumbing and Codex's base-args row. | L | hard | `kinds/codex/**`, `internal/agents/codex/**` (deleted), `internal/agents/agents.go`, `internal/agents/plugin/*.go`, `internal/launch/launch.go:132,931`, `internal/setup/mcp.go:147`, `internal/project/files.go:220-230`, `internal/config/models.go:74-82`, `internal/testkit/testkit.go`, `tests/conformance/live_codex.go`, release files |
| 9 | **Extract claude** to `kinds/claude`. Deletes `internal/agents/claude`, then `Split`, `Preparer`, the two-pass `StartOf`, the registry and the closures in `agents.Start`. | L | hard | `kinds/claude/**`, `internal/agents/claude/**` (deleted), `internal/agents/agents.go:556-730`, `internal/launch/launch.go:1145-1180`, `cmd/obor/main.go` (the kind imports), release files, `docs/module-map.md` |

What I would not do: the review's proposals 1 and 2 (`BuildStart`, slimming the interfaces) as tasks of their own,
a `check-effort` call, per-start generated agy config before P1 says the committed file does not work, and a codex or
opencode lead before one non-Claude lead has run.

## 1. kind-agy

### 1.1 Probes (task 1)

AGENTS.md allows a live probe in a throwaway named herdr session in a scratch dir. agy is started interactively there,
never with `-p`, `--print`, `--prompt` or `agy acp`. The scratch repository gets `.agents/hooks.json` with one named
hook that, for each of the six events, writes its stdin and its environment to a file and prints `{}`, and
`.agents/mcp_config.json` with a stdio server that writes its environment and cwd to a file.

| Probe | Question | Decides |
|---|---|---|
| P1 | Do a workspace `.agents/hooks.json`, `.agents/mcp_config.json` and a plugin named by `.agents/plugins.json` (a `~/` path and a workspace-relative path) load in a trusted folder, with no approval prompt? Does a linked worktree find them at its own root? Does a plugin's `skills` dir load when it is a symlink? `cli.log` says `loaded N named hooks from M hooks.json file(s)` at each start. | the wiring (1.5) |
| P2 | **Does a stdio MCP server inherit the CLI's environment** (`OBOR_ROLE`, `OBOR_LANE`, `OBOR_PANE`, `HERDR_PANE_ID`), and what is its cwd? | whether a static `mcp_config.json` can serve `obor mcp` at all |
| P3 | The hook payloads: `toolCall.name` and `args` of a shell command and of each file edit tool; whether `SessionStart` fires on `--conversation <id>` and on `/clear`; `PreInvocation`'s `invocationNum` across a turn; `Stop`'s fields at a normal end, an error and a usage limit. What a `PreToolUse` reply of `{}` does (falls through to agy's own permission check, or is refused as missing `decision`). What a hook that exits non-zero or times out does. The hooks' environment (the identity variables, `ANTIGRAVITY_LS_ADDRESS`, `ANTIGRAVITY_CSRF_TOKEN`). | the hook transport (1.6) |
| P4 | `--settings`: is the object laid over the saved settings key by key or does it replace them; do `permissions.allow: ["mcp(<server>/*)"]` and `trustedWorkspaces` in it take effect; the server name a plugin's MCP server gets in a permission rule. | the settings file and the override file |
| P5 | `--model gemini-4-neon-high --effort low`: which variant runs; an effort the model lacks; `--model` with a label. | effort (1.3) |
| P6 | `-i "<text>"` in a fresh start and with `--conversation`; `-i "/<skill> handoff"`; how a plugin's skill is named as a slash command. | the first prompt and the lead's |
| P7 | From a second shell, with the address and token a hook saw: `agentapi send-message <conversationId> "<text>"`. Does an idle TUI session start a turn with it, does a busy one read it, is the owner's half-typed draft kept? | lead lines (section 4) |
| P8 | The quit: `/exit` with one Enter or a popup's second; the keys that clear the input; a dialog while background tasks run. The start screen in an untrusted folder (the dialog's text and default option), a self-update at start, the project agy assigns a linked worktree. | stop, not-ready, trust |
| P9 | In `--mode auto` with a PreToolUse deny: is the deny final; which lane commands (go build, go test, git reads) the sandbox stops. | the fixed args (D1) |

### 1.2 Start, resume, first prompt

- Interactive start: `agy [flags]`; `-i`/`--prompt-interactive <text>` runs a first prompt and stays in the session.
  The print forms are `-p`, `--print` and `--prompt` (an alias of `--print`); `agy acp` is a stdio server.
- Resume: `--conversation <id>`; `-c`/`--continue` takes the workspace's most recent one
  (`~/.gemini/antigravity-cli/cache/last_conversations.json` maps a directory to an id). The changelog (1.5.9) says
  `-i` with `--conversation` sends the prompt into the resumed conversation, so a resumed agy session could carry its
  wake in the argv. obor types a resumed session's wake once it is up (`launch.go:775-812`); keep that, it is one path
  for every kind.
- No caller-chosen session id: caps `named_session: false`. The id is the hooks' `conversationId`. herdr's own hook
  (`~/.gemini/config/hooks/herdr-agent-state.sh`, a `PreInvocation` handler) reports it to herdr as the pane's
  `agent_session`, and launch adopts herdr's session for a kind that names none (`launch.go:733`). So task 3 resumes
  through herdr's report and task 4 adds obor's own record.
- Headless forms for the guard: `print_flag: true`, the new `flags: ["--prompt"]`, `subcommands: ["acp"]`, and
  `value_opts` from `--help` (`--add-dir`, `--agent`, `--conversation`, `--effort`, `-i`, `--prompt-interactive`,
  `--input-format`, `--json-schema`, `--log-file`, `--mode`, `--model`, `--output-format`, `--print-timeout`,
  `--project`, `--settings`).
- Stop: `/exit` (alias `/quit`), or Ctrl+D twice (agy's own CLI reference). Whether a popup takes the first Enter is P8.
- Prompts are typed (`typed: true`). agy queues text submitted during a turn until the turn ends (setting
  `queuedMessages`, default `queue`), which is what a job wake needs.
- Status: `terminal`. herdr lists `agy` among its kinds (`docs/research/agent-cli-and-herdr-capabilities.md:584`) and
  its integration says lifecycle comes from screen detection.

### 1.3 Models and effort

`agy models` on this machine lists slugs with the effort in them: `gemini-4-neon-{low,medium,high,xhigh}`,
`gemini-4-argon-{low,medium,high}`, `gemini-4-hydrogen-{low,medium,high,xhigh}`, `gemini-3.8-flash-{low,medium,high}`,
`gemini-pro-orchestrator-neon`, `gemini-pro-orchestrator-g1-pro`, `claude-opus-5-5-{low,medium,high}`,
`claude-sonnet-5-5-{low,medium,high}`, `gpt-oss-120b-medium`. `--effort` takes `low|medium|high|xhigh|max`; the docs say
an effort "switches to that model variant".

Recommendation: caps `model_arg: true`; `agents.models.<name>.id` is a slug; a model ref's `:<effort>` goes on as
`--effort <effort>` after `--model <slug>`. Whether the flag overrides the slug's own effort is P5. If it does not, the
kind rewrites the slug's last word, which it can do because the manifest's efforts are the list above. The manifest
already carries `efforts` (`plugin.go:38`) and `obor config validate` checks a ref against it, so no protocol change.

### 1.4 Settings, the override file, trust, sandbox

- agy's saved settings are `~/.gemini/antigravity-cli/settings.json`. `--settings` takes "a JSON object of
  settings.json keys, inline or as a file path", for the session only. That is the native per-start channel: the kind
  writes one file to the state dir (two names, trimmed and not, as OpenCode's `kind.go:40-46`) and passes its path.
- The file holds what obor needs to work: a `permissions.allow` rule for obor's MCP tools (the docs: an MCP call with no
  rule asks), as Codex's start sets `default_tools_approval_mode` (`codex.go:96`).
- Override file: `.obor/agy/settings_override.json`, the project's keys of `settings.json`. If `--settings` is laid
  over the saved file (P4) the kind passes the override's keys with obor's own; if it replaces, the kind merges over
  the user's file as OpenCode's does (`override.go:27-66`). Nothing invented: the file is agy's settings format.
- **No trim of the user's plugins, skills, hooks and MCP servers in task 3.** They live in `~/.gemini/config`
  (`config.json` `plugins.<name>.enabled`, `hooks.json`, `mcp_config.json`), not in `settings.json`, and no flag or
  per-session key switches them. On this machine every agy agent would load `chrome-devtools-plugin`,
  `modern-web-guidance-plugin` and the `lit-site-manager` MCP server (a bun process each). The protocol allows it
  (`trim_user`: "as far as its CLI has a switch"). Two candidates for later, both unverified: a custom agent file
  (`agents/<name>.md` in the plugin, frontmatter `mcpServers`, `skills`, `plugins`, started with `--agent`), and the
  undocumented `ANTIGRAVITY_CONFIG_REL_DIR`. Decision D2.
- Trust: the dialog's texts in the binary are "Trust this folder?" and "Do you trust the contents of this project?";
  `agents.TrustDialog`'s pattern (`agents.go:342`) matches both. Trusted folders are `trustedWorkspaces` in
  `settings.json`. This machine's list holds the home directory, so if trust is by prefix no folder here shows the
  dialog; P8 has to run somewhere outside it. obor answers no trust dialog for any kind; keep that.
- Sandbox and modes: `--mode accept-edits|auto|plan`, `--sandbox`, `--dangerously-skip-permissions`. Auto mode approves
  workspace edits and sandboxed commands, reviews commands that leave the sandbox and still prompts for risky ones; the
  sandbox (macOS `sandbox-exec`) writes the workspace, temp dirs and build caches, has no network by default and does
  not see other paths. `--dangerously-skip-permissions` approves everything; deny rules and a PreToolUse deny still
  apply. The fixed args are decision D1; `--mode auto` is the counterpart of Claude's `--permission-mode auto`
  (`config/models.go:74`).

### 1.5 obor's MCP server, and where hooks and skills load from

agy reads customizations from three places: `~/.gemini/config/` (user), the workspace's `.agents/` (cwd up to the
repository root), and plugins (a dir with `plugin.json`, holding `mcp_config.json`, `hooks.json`, `skills/`, `agents/`).
A plugin's hooks and MCP servers get `PLUGIN_ROOT`. A plugin is found under a customization root's `plugins/` or
through a `plugins.json` there (`{"entries": [{"path": …}]}`, each path scanned one level deep; `~/` and
workspace-relative paths resolve). There is no flag or variable that names a config for one start, as OpenCode's
`OPENCODE_CONFIG` does.

Recommendation: **the kit ships one native agy plugin, `agy/obor/`, and the project commits `.agents/plugins.json`
naming the kit's `agy` dir.**

- It is the shape Claude already has: the kit is a plugin, the project's committed `.claude/settings.json` enables it,
  every lane gets the file with its worktree (`internal/app/worktext.go:162-167` warns when a lane lacks it). agy's
  equivalent of `${CLAUDE_PLUGIN_ROOT}` is `PLUGIN_ROOT`, so the plugin's files never name the kit's path.
- `agy/obor/mcp_config.json` is agy's own format (`{"mcpServers": {"obor": {command, args, env}}}`), the entry running
  the kit's `bin/obor mcp` with the pin. The install reply is `where: manifest` with that path and key, which
  `obor setup mcp` already checks.
- The path in `.agents/plugins.json` is `~/.obor/kit/agy` for an installed kit (`install.sh` clones the kit to
  `~/.obor/kit`); obor-kit, which runs its own checkout, names `agy` relative to the repository. `obor setup agents`
  writes the file when the config has a model of kind agy.
- Later the same plugin carries `hooks.json` (task 4) and `skills` (a lead on agy).

This works only if P1 and P2 pass. P2 is the larger risk: Codex starts MCP servers with a stripped environment
(`codex.go:36-41`), and a static `mcp_config.json` cannot carry an agent's role, lane and pane. If agy strips it too,
no documented per-start channel is left, and the kind cannot be built as described; say so to the owner before task 3
starts. If only P1's `plugins.json` fails, the fallback is `agy plugin install <kit>/agy/obor` from
`obor setup agents`, a user-level copy that has to be refreshed at each kit upgrade and breaks the rule that a kind
installs nothing user-global (`agents.go:45-46`, the protocol's install paragraph): the owner's call (D2).

### 1.6 Hooks (task 4)

agy's hooks are close to Claude Code's: `SessionStart`, `PreToolUse`, `PostToolUse`, `PreInvocation`,
`PostInvocation`, `Stop`, command handlers only, JSON on stdin (camelCase) and stdout, a 30 s default timeout, run
synchronously. `obor hook <name>` reads Claude Code's event JSON (`internal/guard/guard.go:114-124`, `session.go:28-30`,
`prompt.go:26-34`, `stop.go:41-49`) and answers in Claude's forms. The agy kind binary is the transport, as
`opencode/obor.js` is OpenCode's: `agy/obor/hooks.json` runs it, it rewrites the event, runs `obor hook <name>` and
rewrites the answer. No rule lives in the kind.

| agy event | obor hook | In | Out |
|---|---|---|---|
| `SessionStart` | `session-start` | `conversationId` → `session_id`, `transcriptPath` → `transcript_path`, `cwd` | obor's context lines → `injectSteps: [{systemMessage: {systemMessage: …}}]` |
| `PreToolUse`, matcher the command and file-edit tools | `pre-tool-use` | `toolCall.name` `run_command` with `args.CommandLine` → `Bash` and `command`; the edit tools → `Edit` or `Write` and `file_path` (names and keys: P3) | a deny → `{"decision": "deny", "reason": <obor's line>}`; an allow must not become `"allow"`, which approves the call past agy's own permission check: the neutral answer is P3 |
| `PreInvocation`, the first of a turn | `user-prompt-submit` | `lastUserInput` → `prompt`; it fires before every model call, so the transport passes only a turn's first (`invocationNum`: P3) | the handoff reminder and wrong-tab text → `injectSteps` |
| `Stop` | `stop`; with `terminationReason: ERROR`, `stop-failure` | `continuationActive` → `stop_hook_active`; `fullyIdle: false` → one running entry of `background_tasks` (the lead's background-run rule, `stop.go:119`); `error` → `error` | a block → `{"decision": "continue", "reason": …}` |

- The hooks run with the agent's environment: herdr's own hook reads `HERDR_PANE_ID` there
  (`herdr-agent-state.sh:21-23`). P3 confirms it for the `OBOR_*` variables.
- The hook command has to find the kind binary, whose file name carries the version in a release
  (`~/.obor/bin/obor-kind-opencode-0.43.0-darwin-arm64`). Sketch: `hooks.json` runs `bin/obor` from `PLUGIN_ROOT` with
  one new subcommand that execs the kit's kind binary (`config.KitKind`, `models.go:118`). That is two more process
  starts per tool call than Claude's hook; the task measures it against the hooks' budget (`docs/design.md:20`).
- There is no StopFailure event. A usage limit reaches obor as a `Stop` with an error; its text is unknown until one
  is seen (P3 cannot force it). The kind's `turn` `reset` answers "limited, no reset time" until then.
- Until task 4 an agy session has no guard, no context line and no SessionStart record. That is Codex's state today
  (`codex.go:787-789`, `docs/design.md:23`), and the reason task 3 alone is enough for a worker lane and not for a lead.

### 1.7 Transcript and context size

- Transcript: `~/.gemini/antigravity-cli/brain/<conversationId>/.system_generated/logs/transcript.jsonl` (on disk here;
  the hooks' `transcriptPath` names it). The manifest's glob needs no variable but `HOME`.
- A line is `{step_index, source, type, status, created_at, …}`; a `PLANNER_RESPONSE` line has `input_tokens`,
  `output_tokens`, `cache_read_tokens`. In the five transcripts here a late line reads like
  `input_tokens 2365, cache_read_tokens 225979`, so the context is their sum; `created_at` is the time. No cache TTL.
- The first line names no session id and no cwd, so caps `sessions: false`.
- `project.ContextTokens` knows two formats by their field names (`files.go:58-121`); agy's is a third. That is task
  2(a). Without it an agy session never gets the handoff reminder (`guard/prompt.go:101-116`) and a kept agy lane is
  never stopped past `handoff_tokens` (`app/keeplane.go:122`): it degrades, it does not break.

## 2. Hardening for more kinds

The review's findings in §4.1 are true and the triage's reading holds: a new kind speaks the protocol and
`plugin.Kind` implements the optional interfaces once. Writing agy down changes the list in three places.

**agy needs (tasks 2 and 6):**

| Change | Why agy needs it | Review item |
|---|---|---|
| Context size read by the kind (`turn` `what: context`) | a third transcript format; the triage put it "with the first kind whose transcript obor reads", which is agy | §4.1 proposal 4 |
| `headless.flags` | `--prompt` is a print alias the guard would let through | new |
| `caps.app` | only if agy's OSC 7501 app name is not `agy` and an hc terminal runs it; one field | new |
| The hook contract written down | two transports (OpenCode's, agy's) will target `obor hook`; today the contract is the guard's structs and `opencode/obor.js`'s header | new |
| A delivery the kind performs itself | only for a lead on agy, and only if P7 passes | new |

Not needed, though the triage expected them: an efforts list (the manifest has one) and a fallible install
(`plugin.Kind.InstallErr`, `calls.go:277`, already is).

**Worth doing for every kind, with the extraction (tasks 7 to 9):** a kit kind described at a start when it has no
cache; a transcript glob with a fallback (`${CLAUDE_CONFIG_DIR}` or `~/.claude`: the manifest's rule is "one variable
empty: none"); `install` with the lane's args (Claude's answer depends on them, `claude.go:554-566`); warnings and a
refusal in replies (Codex's quarantine check, `codex.go:447-500`); errors returned from `turn` and `deliver` in place of
the package's `Log` writer (`calls.go:32`).

**Not worth doing:**

- **`BuildStart` and slimming the interfaces as their own tasks** (review proposals 1 and 2, Wave 2 item 1). `Split`,
  `Preparer`, `StartEnv` and the two passes of `Start` (`agents.go:671-722`) exist only for the two compiled kinds.
  Tasks 8 and 9 delete them. Unifying them first is a refactor of code with a deletion date.
- **A `check-effort` call.** The manifest's list answers most refs; a kind whose efforts depend on its args answers
  through `start` (`calls.go:211-217`), one process at validate time. agy needs neither.
- **Per-start generated agy config** (an `--add-dir` to a generated dir, a relocated config dir). Unverified,
  undocumented, and the committed file is the native form.

The TOML scanner and the per-agent OpenCode file are the triage's tasks G and E and are not part of this plan.

## 3. Extracting kind-codex and kind-claude

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
| No warnings or refusal from the kind | Codex's quarantine (`InstallChecker`, `StartChecker`, `agents.go:322-340`), both kinds' override warnings (`OverrideChecker`) | `install` replies `warnings`; `start`'s `error` is the refusal; `project-warnings` takes the overrides |
| The transcript glob has no fallback | `$CLAUDE_CONFIG_DIR` else `~/.claude` (`claude.go:577-583`), `$CODEX_HOME` else `~/.codex` | a list of globs, the first with every variable set |
| `caps.app` | Claude's `claude-code` (`claude.go:634`) | task 2(c) |
| The inbox record's shape | Claude's is `{"socket", "token"}` (`claude.go:638-651`); a manifest's is variable name → value | the Claude kind's `deliver` reads both, so a lead running across the upgrade keeps its record |
| A turn read costs a process | Codex's status is `transcript`: `turn` `what: last` per transcript poll, per Codex pane, under a 5 s timeout (`calls.go:26,343`) | measure first; if it shows, the manifest names the rollout's event types as data |
| The kind's name in the bridge | `agents.Default = "claude"` (`agents.go:483`), `TrustHint`'s branch on it (`agents.go:355-361`), the base args and hc rows (`config/models.go:74-82`) | the trust hint's wording and the base args move into the kinds; `Default` stays a name |

A model id is not a gap: both take `--model`, which the protocol appends.

### 3.3 What it deletes

- `internal/agents/claude` and `internal/agents/codex` (they become `kinds/claude/kind`, `kinds/codex/kind`).
- In `internal/agents/agents.go`: `Split`, `Preparer`, `Whole` as a separate interface, `StartOf`'s two passes
  (699-722), the registry and the compiled-first lookup (556-617), `Fallible` and `InstallOf` (658-660, 725-730),
  `InstallHost`, `InstallChecker`, `StartChecker` (314-340), and of `Start` the fields only a compiled kind reads
  (`Read`, `Dirs`, `WriteState`, `Env`, `SettingsFile`, `MCPFile`, `Agent`): about fifteen fields remain, the request's.
  The optional interfaces fall from fifteen to about five.
- In `internal/launch`: the install-host plumbing (`launch.go:132,931`), the state-file writer and the closures of the
  start (`launch.go:1145-1180`); in `internal/setup`, `mcp.go:147`; in `internal/project`, `files.go:220-230`; in
  `internal/config`, the base-args and hc tables (`models.go:74-82`).
- The helpers the kinds share (`MergePatch`, `ParseOverride`, `UserDir`, `UserSkills`, `SetFlag`, `ResetIn`) stay in
  `internal/agents`: `kinds/opencode` already imports it.

### 3.4 Risks

- **The lead runs on claude.** After task 9 every lead start, and the SessionStart hook's inbox record, depend on the
  cached manifest of a plugin kind. A missing or stale cache means no start ("run obor setup agents") and a lead whose
  lines wait. Task 7 removes the first; the second needs the cache written before the first session of the new
  version, which serve's start and `obor setup agents` do. The upgrade guide gets one ordered step.
- **Hooks.** Claude's hooks stay as they are: the kit root is the Claude plugin (`hooks/hooks.json`,
  `.claude-plugin/plugin.json`, `skills/`), and `obor hook` never runs a kind's command. The guard reads the kinds'
  headless forms from the cache; with none it denies the kind's program outright, which for claude is safe.
- **Upgrade of obor-kit and axis-alies** (the only two kept compatible, decisions 11-10). Both configs name claude and
  codex only through `agents.models.<m>.kind`; with `KitKinds` holding all kit kinds no config line changes. The
  release ships four binaries per platform, fetched together by `bin/obor`. Running sessions: a Claude lead's inbox
  record (above); a Codex reviewer's session is read from its rollout as before.
- **Speed.** One process per start is nothing. One per transcript poll for Codex has to be measured in task 8 before
  the compiled path is deleted.
- **Both paths at once.** The rule is to delete the old code the day its replacement is live, so each extraction is
  one task that moves the kind and deletes the package, gated by the kind's live conformance tier. No flag chooses
  between a compiled and a plugin claude.

### 3.5 When

**After agy, codex before claude.** agy needs nothing from the extraction. Written first, it is the second plugin kind
and the first with resume, hooks and a transcript, so it shows the protocol's gaps on a kind no role depends on;
several of them are the ones Claude needs (hooks, context size, delivery). Codex goes next because only obor-kit's
reviewer runs it and it has no hooks and no inbox. Claude goes last, with the protocol already carrying a kind of each
shape. Doing the extraction with agy would put three kinds' live conformance and the lead's own kind into one wave.

## 4. Non-Claude leads

### 4.1 What ties the lead to Claude Code today

| Tie | Where | Neutral? |
|---|---|---|
| The first prompt is a plugin slash command | the lead role's `Prompt: "/obor:lead"` (`roles.go:57`); only a kind with caps `skills` gets it, any other starts with none (`roles.go:207-223`, `launch.go:800-810`); handoff appends ` handoff` | no: the role holds Claude's spelling |
| The skill ships as the Claude plugin's | `skills/lead/` under the kit root; the SessionStart line says "run /obor:lead" (`session.go:149`) | the file is a plain `SKILL.md`; the way to it is Claude's |
| The skill's text | "after a Claude Code update" (`skills/lead/SKILL.md:16`), `/clear`; `AskUserQuestion` is already "where the client has it" (line 26) | nearly |
| Hooks: guard, context, handoff reminder, the background-run stop | `hooks/hooks.json`; `stop.go:46-48` reads Claude's `background_tasks`, `guard.go:122` its `run_in_background` | the transport is per kind; the rules are neutral |
| **Lines to the lead are posted to the session's inbox, never typed** | `docs/design.md:834-849`: Claude's messaging socket, recorded by the SessionStart hook; "A lead of another kind (Codex, OpenCode) has no inbox: `obor status` and the 24 h needs-you" | no: only Claude has an inbox (`claude.go:632-635`) |
| Session id and resume | Claude takes obor's id (`--session-id`); other kinds report theirs | yes: `launch.sessionOf` (`launch.go:720`) is by capability |
| `obor lead`, `obor restart <lead> --handoff` | `cmd/obor/lead.go`, `restart.go:23-72`; the stop recipe is the kind's | yes |
| The handoff reminder | `project.ContextTokens` | after task 2(a) |

`AskUserQuestion` is not a tie: the skill asks through whatever the client has, and agy, Codex and OpenCode each have a
question tool or form. Nothing of it belongs in the protocol.

### 4.2 What each kind would need

| | codex | opencode | agy |
|---|---|---|---|
| Start prompt | task 5 (Codex has no key for a skill root outside the project, `agent-skills-scope.md:170`, so the prompt names the file) | task 5 | task 5; a slash form if the plugin's skill has one (P6) |
| Hooks | none today (`codex.go:787`). Codex has Claude-shaped hooks, but a project's hooks file needs a trusted project and a trust review per hook, and no `-c` option runs one (`codex.go:279-288`): a task of its own | has them through `opencode/obor.js`; its stop verdict is not acted on, so a background run does not hold the turn | task 4, all four, with `fullyIdle` for the background rule |
| Lines to the lead | `codex queue --thread <id> --message <text>` exists; whether a TUI started with `--no-daemon` reads the queue is not verified (`docs/research/agent-cli-and-herdr-capabilities.md` §3.5) | the plugin runs inside the agent's process and has `ctx.session.prompt` (docs only): it could listen on a socket and be an inbox with today's `Delivery` | `agentapi send-message` or nothing (P7); hooks alone can hand lines to a busy lead (`PreInvocation` `injectSteps`, `Stop` `continue`) and cannot wake an idle one |
| Resume | yes | no (`kind.go:133`): `obor lead --resume` starts fresh | yes |
| Context size | has it | no transcript: no reminder | task 2(a) |

### 4.3 What belongs in the kind protocol

- The hook transport's contract (task 2(d)): the events, their fields, the answers, including the background-task
  field, so a kind's transport is written against a document.
- How a kind takes a role's start prompt: `caps.skills` today means "takes `/obor:lead`". Task 5 makes the role name
  the skill and the kind's manifest say how it is invoked, with the plain "read `<kit>/skills/lead/SKILL.md`" prompt
  for a kind that says nothing.
- Delivery: typed, a socket and lines, and (task 6) "the kind delivered it", for a channel that is an HTTP call or a
  command of the CLI's own.
- Context size (task 2(a)).

Not in the protocol: the lead skill's text, `obor lead`'s flow, question tools.

### 4.4 The order for a first non-Claude lead

agy, if P7 passes: tasks 3, 4 and 5 give it the start, the hooks and the prompt, and task 6 the lines. If P7 fails,
OpenCode is the better first candidate (the plugin socket, no protocol change), at the cost of no resume and no
handoff reminder. Codex is last: it needs hooks and the queue probe. Whichever goes first runs in a scratch project
before obor-kit's own lead changes.

## Owner decisions

- **D1. agy's fixed args.** `--mode auto` (recommended: the counterpart of Claude's `--permission-mode auto`; a risky
  action prompts and the lane shows blocked) or `--dangerously-skip-permissions`, with or without `--sandbox`. P9
  informs it.
- **D2. agy's wiring and trim.** The committed `.agents/plugins.json` naming the kit's plugin (recommended), or a
  user-level `agy plugin install`. And: accept that agy agents load your user-level agy plugins and MCP server until a
  switch is found, or switch those off yourself.
- **D3. Lines to a lead without an inbox.** Keep "posted, never typed, an idle lead is woken" as the rule for every
  kind (then a kind leads only with a push channel, and agy waits on P7), or allow lines that arrive at the lead's
  next prompt or turn end through its hooks, with `obor status` and the 24 h needs-you for an idle lead, as a stated
  lesser mode.
- **D4. The first non-Claude lead**: agy or OpenCode, and in which project.
- **D5. The extraction's go-ahead**: codex once agy has run in lanes for some days; claude once codex has. The
  direction is decided (decisions 11-08); the dates are yours.
- **D6. Codex hooks**: only if you want a Codex lead. Not in this plan.

## Concerns

- `docs/design.md:23` says hooks cover only Claude Code panes; OpenCode lanes have run obor's hooks through its plugin
  for some time. The line should say which kinds have a guard, and task 4 changes the answer again.
- agy updated itself today (the binary's mtime is 2026-11-22 12:15) and its hook payloads and settings keys changed in
  most of the last seven releases. A kind for it will need the live conformance tier at the nightly, not only at
  release, or drift shows first in a lane.

## Not verified

- Anything agy does at run time: P1 to P9. In particular that MCP servers inherit the environment (P2), that a
  workspace `plugins.json` loads a plugin outside the repository without a prompt (P1), the tool names and argument
  keys in hook payloads (P3), `--settings` merge behaviour (P4), `--effort` against a slug (P5), slash commands in `-i`
  (P6), `agentapi send-message` to a TUI session (P7), the quit and trust screens (P8).
- That herdr's agy hook loads: `cli.log`'s last start (12:15) says `loaded 0 named hooks from 1 hooks.json file(s)`
  and `hooks.json` was last written at 12:21, so that start predates it.
- That agy's context is `input_tokens + cache_read_tokens`: inferred from the numbers in five transcripts.
- The public docs: read by a fetch agent that paraphrased them; I did not read the pages myself. `--settings` and
  `agentapi` are in `--help` and the shipped skills but not in the pages it read.
- The cost of a `turn` process per Codex transcript poll, and how often the poll runs.
- Whether the triage's tasks E and G have merged (`codex.go:504` still has `configKeys`).
- Codex's `queue` against a `--no-daemon` TUI and OpenCode's `ctx.session.prompt`: both from the 11-04 capability
  research, marked unverified or docs-only there.
- `obor`'s hook time with two more process starts (task 4's sketch).
