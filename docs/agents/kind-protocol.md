# The kind protocol

An agent kind is the part of obor that differs per agent CLI (`agents.Kind`, `internal/agents/agents.go`). Two are
compiled in (claude, codex). A **plugin kind** is an executable, in any language, that answers this
protocol; obor's `internal/agents/plugin` package implements `agents.Kind` by running it, so nothing above the bridge
changes (`docs/plan/architecture-target.md` §2a).

**OpenCode is the reference plugin kind**: `kinds/opencode/` in the kit (the kind itself is package
`kinds/opencode/kind`, standard library and obor's `agents` only). obor links it and answers it in process
(`plugin.Local`, registered by `cmd/obor` under its binary's name `obor-kind-opencode`; a test binary's by testkit):
the same requests and replies, no process. `make build` also writes it as its own binary,
`dist/obor-kind-opencode` (`kinds/opencode/main.go`), and a release still ships it beside obor's
(`obor-kind-opencode-<v>-<os>-<arch>`, fetched by `bin/obor`); obor runs neither. `opencode/obor.js` stays its hook
transport.
The kit's second plugin kind is agy (Antigravity CLI), `kinds/agy`, linked and built the same way
(`obor-kind-agy`): obor's MCP server and hooks through the project's `.agents/mcp_config.json` and `.agents/hooks.json`
(`install`'s `where: project`); its `hook` call is agy's hook transport (`obor kind agy hook`); its inbox is the
agentapi variables of the commands the agent runs (`inbox_record`, recorded by the agent's `obor` commands), which its
`deliver` sends with (`sent`).

## Register one

```json
"agents": { "kinds": { "mykind": { "plugin": "tools/obor-kind-mykind", "args": ["--auto"] } } }
```

- `agents.kinds.<name>.plugin`: the executable (relative: to the project root). `<name>` is the kind's name everywhere
  (`agents.models.<model>.kind`, herdr's `--kind`); it may not be a compiled kind's.
- A kit kind (`opencode`, `agy`: `config.KitKinds`) needs no line: with none, or one naming its kit binary
  (`"plugin": "obor-kind-opencode"`, `"plugin": "obor-kind-agy"`), obor answers it in process (`config.KindCommand`
  keeps that name, `plugin.Local` has its handler): no process runs, no manifest is cached, `obor setup agents` and
  serve skip it. Any other path for a kit kind's name replaces it and runs as any plugin kind. `obor init` writes no
  line for them.
- `obor config validate` checks the file is executable and that its `describe` answers with a manifest of `<name>`.
- obor serve runs `describe` and caches the reply (the **manifest**) as `.obor/state/kinds/<name>.json` at its start
  and again once the command's mtime (or its path) differs from the cached one; `obor init` and
  `obor setup agents` cache it too (setup agents then runs `install`, whose answer `obor setup mcp` checks). A call that runs
  the command describes it again by itself, too, once the command changed.
- What the manifest answers is read from the cache, never by running the command: hooks and the guard load the config
  and stay as fast as for a compiled kind. A kind with no cache has no answers: every call that would run it fails
  with `kind <name>: no manifest cached (…): run obor setup agents`, and the guard denies every command that runs its
  program (its headless forms are unknown).

## One call

obor runs `<command> <call>` with one JSON object on stdin and reads one JSON object from stdout; the process exits.

- The request always has `"version": 1` and `"call": "<call>"`, then the call's fields below.
- The reply must have `"version": 1`. `"error": "<why>"` in a reply makes the call fail with that text.
- The command runs in its own process group with obor's environment, under a timeout: `describe` 10 s, `install`
  30 s, `start` and `turn` 5 s, `turn` `what: context` 1 s (it runs inside a lead's hook and under serve's lock),
  `deliver` 3 s, `not-ready` 2 s. A timeout, a non-zero exit (its stderr's first line is
  quoted), a reply that does not parse or names another version is an error naming the kind and the call:
  `kind mykind: start: no reply within 5s`, `kind mykind: turn: exit 3: boom`, `kind mykind: deliver: malformed reply: …`.
- Bytes (a transcript's tail, a file's contents) travel as JSON strings (text).
- `env` in a request is a snapshot of the variables the manifest's `env` names (from the start's environment where
  obor has one, else obor's own).

## The calls

| Call | Request fields | Reply | Replaces (`agents.Kind`) | When |
|---|---|---|---|---|
| `describe` | — | the manifest (below) | `Name`, `Caps`, `Efforts`, `Vars`, `Stop`, `Headless`, `Transcript`, `InboxRecord`, `MCPRegistration.Lane` | `obor setup agents`, `obor config validate`, a changed command |
| `start` | `env`, `kit`, `args`, `settings`, `resume`, `prompt`, `effort`, `strict_mcp`, `trim_user`, `overrides`, `session`, `write_dirs`, `dir`, `root`, `check` | `args` (argv after the binary), `env` (variable → value, "" unsets), `files: [{name, bytes}]`, `warnings` (lines obor logs, a start's notes on what it could not check: no failure) | `StartWhole` (a compiled kind's `Start` and `StartEnv`, `agents.Split`) | once per agent start, with every field of the start: its argv, env and files applied together; also the effort check (`check: true` with `args` and `effort` alone: `agents.ArgsChecker`) and `obor setup mcp --model`'s start check (`check: true` with the whole start: `agents.Start.Check`), each writing no file and answered without a process by a kind that reads its CLI's live data |
| `not-ready` | `screen` | `why` ("" ready) | `NotReady` | per poll during a start, when the manifest says `not_ready: {call: true}` |
| `install` | `kit`, `root`, `env` | `mcp: {where, path, key, entry, unpinned}`; optional `hooks: {where, path, key, entry}` | `Install` | `obor setup agents`, `obor setup mcp` |
| `turn` | `what: last` + `tail`; `what: session` + `first`; `what: context` + `tail`; `what: reset` + `error`, `details`, `at` | last: `ok`, `turn: {id, error, details, ended, reset}`; session: `ok`, `session: {id, cwd, started}`; context: `ok`, `context: {tokens, at, ttl}`; reset: `limited`, `reset` | `LastTurn`, `Session`, `Context`, `Reset` | per transcript poll (status `transcript`), a launch finding a session, the handoff reminder and a keep lane's handoff rule, a stop on an error |
| `deliver` | `text`, `record` | `ok`, `why` (ok false: why the record is no inbox), `delivery: {typed, socket, lines, sent}` | `Deliver` | per line delivered, unless the manifest says `typed: true`; `sent`: the kind delivered the text itself (agy's `agentapi send-message`), no socket; a send it tried and was refused answers `error` (the reason: nothing delivered); an empty `text` checks the record and sends nothing |
| `project-warnings` | `dir` | `warnings: ["<file>: <why>", …]` | `agents.ProjectChecker` | `obor config validate`, when the manifest says `project_warnings: true` (the kind reads the project's CLI files in `dir` itself) |

An install reply registers nothing user-global: `mcp.where` is
`manifest`, `start` or `project`, never `config`, and no `plugin` link is in it. A reply that carries either is refused
with an error naming the field; a kind registers per launch or through the project's own files.

`where: project` is a file the project commits: `path` relative to the project's root (the request's `root`; an
absolute path or one with `..` is refused), `key` the obor entry's place in the file's JSON (outermost first), `entry`
the entry. `obor setup agents` writes the entry into the file, keeping its other entries, when the config has a model
of the kind, and adds the file's path to `rules.protected_files`; the lead commits both. `obor setup mcp` checks the
entry and, since such an entry names `obor` by `PATH`, warns when the `obor` on `PATH` is not the running kit's.
agy's: `{"where": "project", "path": ".agents/mcp_config.json", "key": ["mcpServers", "obor"], "entry": {"command":
"obor", "args": ["mcp"]}}`. With no `root` (outside a project) the path stays as the reply names it.
`hooks` is a kind's hooks entry in a file of the project, `where: project` only (any other `where` is refused), written
and protected by `obor setup agents` as `mcp`'s is; agy's: `.agents/hooks.json`, key `["obor"]`, its named hook.

Times are RFC 3339 strings ("" none).

`turn` `what: context` is the kind's own transcript format: the tail's last main-chain line with a token count, its
time and the cache TTL the line names (`"1h"`, `"5m"`, `"30m"`; `""` none). obor reads a session's context this way
(`project.ContextOf`): the file's last 400 lines go to the kind the session is recorded as, that kind alone (a kind
that keeps no transcript is not asked); a kind whose format the tail is not answers `ok: false`.

### start: the closures as data

The compiled kinds get three Go functions; a process gets data instead.

1. **Read a file** (`Start.Read`): the plugin reads it itself. `dir` is the start dir (the agent's cwd), where a project's
   committed CLI config lives; the plugin decides what of it outranks obor's flags.
2. **Write a state file** (`Start.WriteState`): each of `files` is written as `<name>` in the project's state dir
   (`.obor/state/<name>`, a name, no `..`), and every `{file:<name>}` in the reply's `args` and `env` values is replaced
   with its path.
3. **Whether a lane's start gets obor's MCP server** (`MCPRegistration.Lane`): the manifest's `mcp_lane`.

A kind's `start` owns its own fixed flags: it puts them in the `args` it returns; obor's tables hold none for a plugin kind.

Every variable in `env` that gives a value must be among the manifest's `vars` (`agents.Kinds.Vars`: a start unsets
every registry var it does not set). A variable `env` names empty that `vars` does not is one this start clears from
the pane's shell (OpenCode: a lane's `OPENCODE_CONFIG_CONTENT`, the user's inline config layer); every other kind's or
role's start leaves it as the pane has it.

`strict_mcp` (`agents.Start.StrictMCP`) is true for a lane's agent (a worker, the owner's drive agent): the kind hides
the user's own MCP servers where its CLI has a way (Claude's `--strict-mcp-config`, OpenCode's generated config); a
lead's start and a role agent's keep them.

`trim_user` (`agents.Start.TrimUser`) is true for every agent but a lead: the kind switches off its default part of
the user's own settings (the user's plugins and skills, as far as its CLI has a switch); every other user setting
applies. A lead's start has `trim_user` false and keeps the user's own settings untrimmed.

`overrides` (`agents.Start.Overrides`) are the project's override files of the kind: an object, file name → the
document as written (text), holding each file the manifest's `override_files` names that the project has in
`.obor/<name>/` at its root (none: `null`), handed to every start (lead included). obor reads the files and checks that
each parses (`.toml` as TOML, any other name as a JSON object); the kind merges each over the user's file of the same
kind, which it reads itself from the config dir the request's `env` places, as a JSON Merge Patch (RFC 7396: objects
merge key by key, `null` deletes a key, anything else replaces), on every start (agy) or on a trimmed start only
(OpenCode). `root` is the project's root.

OpenCode: `OPENCODE_DISABLE_CLAUDE_CODE_SKILLS=1` in `env` on a trimmed start ("" for a lead) hides the Claude skill
dirs only; `opencode_override.json` merged over the user's `opencode.json[c]` is the base of its generated config
file, and a lane's start disables the user's own MCP servers the project's `opencode.json[c]` does not define. A lane's
start also names `OPENCODE_CONFIG_CONTENT` empty, so no server of the user's inline config reaches it; a lead's or
role's start names it not at all, so the pane's own value stands. What the CLI has no switch for stays visible.

## The manifest

```json
{
  "version": 1,
  "name": "mykind",
  "status": "terminal",
  "caps": {"hooks": "none", "inbox": false, "hook_answers": false, "resume": true, "skills": false, "sessions": false,
           "named_session": false, "project_plugin": false, "model_arg": false, "version_var": "", "app": ""},
  "efforts": ["low", "high"],
  "vars": ["MYKIND_CONFIG"],
  "env": ["HOME", "MYKIND_HOME"],
  "stop": {"quit": "/exit", "second_enter": false, "clear_input": ["ctrl+u"], "after_escape": [], "program": "",
           "dialog": {"option": "Quit anyway", "key": "(\\d)\\. Quit anyway"}},
  "headless": {"programs": [], "subcommands": ["run"], "anywhere": [], "value_opts": ["--model"], "print_flag": false,
               "flags": ["--prompt"]},
  "transcript": "${HOME}/.mykind/sessions/{sid}.jsonl",
  "inbox_record": ["MYKIND_INBOX"],
  "not_ready": {"pattern": "Updating", "why": "is updating itself"},
  "typed": true,
  "mcp_lane": {"how": "MYKIND_CONFIG (the start's generated config: {kit}/bin/obor mcp)", "registered": true, "static": false},
  "project_warnings": false,
  "override_files": ["mykind_override.json"]
}
```

| Field | Meaning (the `agents` field it fills) |
|---|---|
| `version`, `name` | 1, and the config's `<name>`: anything else is refused |
| `status` | where the agents' status and turns come from (`Caps.Status`): `terminal` (herdr's agent detection), `hooks` (the kind's hooks report turns), `transcript` (`turn` reads them from the session's transcript) |
| `caps` | `Caps`; `hooks` is `none`, `native` or `plugin` (`HookTransport`); `hook_answers` (absent: false) says its hook transport acts on the answers of `user-prompt-submit` (the context) and `stop` (the block), so a lead of it with no live inbox (none, none recorded, or its socket gone) takes its lines through them (design
"Lines to the lead"); `app` is the name its agents report in a terminal's status (hc's OSC 7501 `status.app`; `""`: the kind's name). `Caps.Transcript` is `transcript` being set |
| `efforts` | the values a model ref's `:<effort>` may take (`Efforts`; none: no effort) |
| `vars` | every variable `start`'s `env` may set (`Vars`) |
| `env` | the variables obor snapshots into each request, and the only ones `transcript` may name |
| `stop`, `headless` | `Stop` (the quit recipe; `dialog.key` a regular expression) and `Headless` (the argv forms the guard denies; `programs` empty: the name; `flags` are long flags that run it headless, as `print_flag` is `-p`/`--print`) |
| `transcript` | the glob of a session's transcript: `{sid}` the session id, `${VAR}` a variable of `env` (one empty: none) |
| `inbox_record` | the variables whose values, as a JSON object, are a session's inbox record (written by the SessionStart hook from its env, and by any `obor` command run with them in its env but `obor hook`, `obor kind` and `obor mcp`; given back to `deliver` as `record`); one empty: no record. The writer asks `deliver` (empty `text`) for the record's socket, or `sent` (the kind sends with it), and keeps that beside the record: `obor serve` posts only while that socket is there, or while no send with a `sent` record was refused (a refusal marks it; the next `obor` command that records it clears the mark); else the lead's hooks take its lines |
| `not_ready` | `{pattern, why}`: a regular expression on the start's screen, `why` the reason; `{call: true}`: run `not-ready` per poll; absent: ready once started |
| `typed` | every line is typed into the pane: `deliver` is never called |
| `mcp_lane` | `obor setup mcp --model`'s answer for a lane's start: how (`{kit}` the kit install names), registered, static (served by an installed file setup checks) |
| `project_warnings` | the kind answers `project-warnings` |
| `override_files` | the names of the kind's override files in the project's `.obor/<name>/` (`agents.Overridable`): obor hands those the project has to `start` as `overrides`, and `obor config validate` checks them |

## Where the core branches on status

`Caps.Status` says where a kind's agent status and turns come from. The core reads it through `agents.TurnsRead` (status `transcript`) in
`internal/herdr/panes.go` (`Panes.Turn`: a pane's last turn is read from its transcript) and `internal/app/stopped.go`
(`turnsRead`: a worker's or role agent's stop and its end are read from the transcript). `terminal` and `hooks` change
nothing yet: herdr's agent detection still gives every pane its status. The compiled kinds: claude `terminal`, codex
`transcript`; the OpenCode plugin kind `terminal`.

## Hook transport

A kind's hooks reach obor as `obor hook <name>`: `hooks: native` is the CLI's own hooks file running it (Claude
Code's plugin `hooks/hooks.json`), `hooks: plugin` is obor's plugin for the CLI, which maps the CLI's events to the
same calls (`opencode/obor.js`; agy's `hook` call, which the project's `.agents/hooks.json` runs as `obor kind agy
hook`: `obor kind <name>` runs that kit kind's call in process, and agy's transport runs obor's hook in process too, so
a tool call starts obor once after the shim). The kit kind's own binary (`obor-kind-agy`, which obor does not run) has
no obor in process: its hook call execs the executable `OBOR_EXE` names. A transport reads its CLI's event, runs
`obor hook <name>` with the event's JSON on stdin and reads the answer; a transport that only rewrites events holds
none of obor's rules. The hooks read the agent from its environment (`OBOR_ROLE`, `OBOR_LANE`, `OBOR_PANE`, and the
pane's `HERDR_PANE_ID`), so a transport passes it through.

`bin/obor` runs both of those on its hook path: it never fetches a binary there, runs the first one that has the
command (`hook <name>`, or `kind` for `obor kind <name> hook`), and
with none answers for the hook — a deny with the repair hint for `pre-tool-use`, nothing for the other hooks, and for
`obor kind <name> hook` by event: an event named PreToolUse or carrying a tool call in a running project is denied
with the repair, one elsewhere `ask`, nothing for the other events.

Every hook exits 0 but `pre-tool-use`'s denial, and a hook never fails: an unreadable event allows, and a kind's hook
answers one by the rule above.

- **An allow is no opinion**, never an approval: a transport maps it to its CLI's fall-through, which leaves the call
  to the CLI's own permission check (agy's `{"decision": "ask"}`, never `allow`).
- **When `obor hook` fails or is too slow** (not found, a non-zero exit but `pre-tool-use`'s 2, no answer within the
  transport's bound), the transport still answers, per hook: `pre-tool-use` a deny naming the failure (obor cannot
  guard the call); every other hook nothing (no context, no block). Outside an obor project a transport runs no hook
  and answers nothing (a tool call: the fall-through).
- **`session-start` runs once per conversation the CLI starts**, again after a clear (a new conversation), and not
  for a resume: the session record comes from it, and a resumed conversation already has its context. A transport
  whose inbox lives in its own process runs it at a resumed session's start too (OpenCode's plugin, which reads the
  session id from `OBOR_OPENCODE_RESUME`, the variable the kind's start sets on a resume): the session's inbox
  exists from that start, not from its first turn. It runs it only for the agent's own session: a transport that can
  see child sessions (a subagent's) asks its CLI for the session's parent first, and a child never runs `session-start`, is never recorded and never
  gets an inbox. Its events name the kind in `agent_kind`.

| `obor hook` | The event fields it reads | The answer |
|---|---|---|
| `pre-tool-use` | `tool_name` (`Bash`, `Edit`, `Write`, `MultiEdit`, `NotebookEdit`, `mcp`, `unguarded`; any other allows), `cwd`, `session_id`, `tool_input.command`, `tool_input.file_path` (or `notebook_path`), `tool_input.run_in_background`, `tool_input.description`, `tool_input.mcp_server`, `tool_input.tool`, `tool_input.why` | exit 0: allow, no output (one line on stderr after a recovered panic, which also allows), an allowed background Bash call recorded for the `stop` hook; exit 2: deny, one line `obor: <reason>` on stderr |
| `session-start` | `cwd`, `session_id`, `transcript_path`, `agent_kind` (a kind's transport names its kind: the session's kind when the config knows it, else the kind the agent runs as) | obor's context lines on stdout (role, lane, task, the spec's protected files, the hooks line, the role text; a `WARN` line for unknown config keys, or the one line of a config that does not load), nothing else; with a session id, the session is recorded and reported to serve with its transcript, its inbox record written and a plugin kind's marker |
| `user-prompt-submit` | `cwd`, `session_id`, `transcript_path`, `prompt`, the origin fields `origin` (a string or `{"kind": …}`), `turn_origin`/`turnOrigin`, `prompt_source`/`promptSource` | one UserPromptSubmit context object on stdout, `{"hookSpecificOutput":{"hookEventName":"UserPromptSubmit","additionalContext":…}}`: the lead's queued lines when no live inbox takes them and `hook_answers` (asked from serve, which records them as delivered) and its handoff reminder, or a worker's wrong-tab guard; nothing when neither applies; a worker's or role agent's turn is reported to serve as begun |
| `stop` | `cwd`, `session_id`, `transcript_path`, `stop_hook_active` (only `true` counts), `background_tasks`, each with its `status` (`running` counts) | `{"decision":"block","reason":…}` on stdout (a background run this session started still running, a worker before its `task_done`, or the lead's queued lines when no live inbox takes them and `hook_answers`, not within an active stop loop: asked from serve, which records them as delivered), or nothing; a worker's or role agent's turn is reported to serve as ended |
| `stop-failure` | `cwd`, `error`, `error_details` (both raw JSON, logged as written) | nothing: the event is logged under the project's state dir and, with an agent's name, its error reported to serve |
| `noop` | — | nothing |

A CLI's MCP tool call is `tool_name` `mcp` with the server's name in `tool_input.mcp_server` (the name its CLI's MCP
config registers the server under; `obor` is obor's own, `mcpServers.obor`). The guard denies a lane-bound role — a
worker, `obor drive` — any server the project does not declare, and a call that names none: allowed are the servers of
the project's committed `.agents/mcp_config.json` (read from the main checkout; `obor`'s always among them), so obor
lanes use the project's MCP servers only (`.agents/mcp_config.json`; owner 11-22); a missing or unreadable file allows
only `obor`. A lead's and a role agent's call: no opinion.

A CLI's tool call that obor cannot guard is `tool_name` `unguarded`, with the CLI's tool in `tool_input.tool` and why
obor cannot guard it in `tool_input.why`: a transport sends it for a tool whose effects no hook sees (agy's
`run_workflow` and `swarm`, a Python script run with no hook) or whose session cannot be told from the agent's (agy's
`invoke_subagent`), and for a tool it has no row for ("obor has no rule for agy tool <X>: update obor"). OpenCode's
plugin sends it for a name it knows nothing about ("obor has no rule for OpenCode tool <X>: update obor"), and allows
`execute` (Code Mode, the carrier of every MCP tool), whose inner calls reach the hook as `<namespace>_<tool>` ids: one
passes only when `ctx.tool.list()` gives it the namespace of an MCP server the runtime reports connected
(`ctx.mcp.list()`; obor's own always), and never by prefix.
OpenCode's own tools (`opencode_*`) are classified one by one: its worktree create and remove go as the guard's `Bash`
event for the git command they run, its read tools pass, and any other is unguarded until obor has a row for it (the
catalog is re-probed on an OpenCode upgrade). Which tools those are and why are the CLI's facts, so they stay in the
transport; the decision is the guard's: a deny with
`tool_input.why` for every session but a role-less one in the main checkout (the owner's own agent), which has no
opinion. In a stopped project (`obor stop`) no hook loads: a transport runs none (agy's reads `.obor/state/stopped`).

A write naming no file (an empty `tool_input.file_path`, or `notebook_path` for `NotebookEdit`) and a `Bash` call with
an empty `tool_input.command` are denied for every session, naming the missing argument: a renamed CLI argument
arrives as an empty one.

## Not covered

- A model name in the args (`agents.ModelEditor`): a plugin kind's model id goes on as `--model <id>` (appended to the
  kind's base args, `config.Model`).
- Live conformance of a plugin kind of a project's own: the static tier, `make conformance TIER=static KIND=plugin`,
  runs against `tests/conformance/kindplugin/obor-kind-shell`, a shell-script kind answering every call, and against
  `kinds/agy` built as a binary and named by its path (P-S8: describe and start), so the external-command path stays
  tested for a real kind. The kit's OpenCode keeps its live tier (`make conformance TIER=live KIND=opencode`), run
  against `kinds/opencode` built into the scratch dir and called through obor's plugin package.
