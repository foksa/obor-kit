# The kind protocol

An agent kind is the part of obor that differs per agent CLI (`agents.Kind`, `internal/agents/agents.go`). Three are
compiled in (claude, codex, opencode). A **plugin kind** (T-1036) is an executable, in any language, that answers this
protocol; obor's `internal/agents/plugin` package implements `agents.Kind` by running it, so nothing above the bridge
changes (`docs/plan/architecture-target.md` §2a).

## Register one

```json
"kinds": { "mykind": { "command": "tools/obor-kind-mykind", "args": ["--auto"] } }
```

- `kinds.<name>.command`: the executable (relative: to the project root). `<name>` is the kind's name everywhere
  (`models.<model>.kind`, herdr's `--kind`); it may not be a compiled kind's.
- `obor config validate` checks the file is executable and that its `describe` answers with a manifest of `<name>`.
- `obor setup agents` runs `describe` and caches the reply (the **manifest**) as `.obor/state/kinds/<name>.json`, then
  runs `install` and applies what it returns. Run it again after changing the command; obor also describes it again
  by itself at the next call that runs it once the command's mtime (or its path) differs from the cached one.
- What the manifest answers is read from the cache, never by running the command: hooks and the guard load the config
  and stay as fast as for a compiled kind. A kind with no cache has no answers, and every call that would run it fails
  with `kind <name>: no manifest cached (…): run obor setup agents`.

## One call

obor runs `<command> <call>` with one JSON object on stdin and reads one JSON object from stdout; the process exits.

- The request always has `"version": 1` and `"call": "<call>"`, then the call's fields below.
- The reply must have `"version": 1`. `"error": "<why>"` in a reply makes the call fail with that text.
- The command runs in its own process group with obor's environment, under a timeout: `describe` 10 s, `install`
  30 s, `start` and `turn` 5 s, `deliver` 3 s, `not-ready` 2 s. A timeout, a non-zero exit (its stderr's first line is
  quoted), a reply that does not parse or names another version is an error naming the kind and the call:
  `kind mykind: start: no reply within 5s`, `kind mykind: turn: exit 3: boom`, `kind mykind: deliver: malformed reply: …`.
- Bytes (a transcript's tail, a file's contents) travel as JSON strings (text).
- `env` in a request is a snapshot of the variables the manifest's `env` names (from the start's environment where
  obor has one, else obor's own).

## The calls

| Call | Request fields | Reply | Replaces (`agents.Kind`) | When |
|---|---|---|---|---|
| `describe` | — | the manifest (below) | `Name`, `Caps`, `Efforts`, `Vars`, `Stop`, `Headless`, `Transcript`, `InboxRecord`, `MCPRegistration.Lane` | `obor setup agents`, `obor config validate`, a changed command |
| `start` | `env`, `kit`, `args`, `settings`, `resume`, `prompt`, `effort`, `strict_mcp`, `session`, `write_dirs`, `dir` | `args` (argv after the binary), `env` (variable → value, "" unsets), `files: [{name, bytes}]` | `Start`, `StartEnv` | each agent start (several times: env, then argv) |
| `not-ready` | `screen` | `why` ("" ready) | `NotReady` | per poll during a start, when the manifest says `not_ready: {call: true}` |
| `install` | `kit`, `env` | `mcp: {where, path, alt_path, key, entry, unpinned}`, `plugin: {from, to}` or null | `Install` | `obor setup agents`, `obor setup mcp` |
| `turn` | `what: last` + `tail`; `what: session` + `first`; `what: reset` + `error`, `details`, `at` | last: `ok`, `turn: {id, error, details, ended, reset}`; session: `ok`, `session: {id, cwd, started}`; reset: `limited`, `reset` | `LastTurn`, `Session`, `Reset` | per transcript poll (status `transcript`), a launch finding a session, a stop on an error |
| `deliver` | `text`, `record` | `ok`, `delivery: {typed, socket, lines}` | `Deliver` | per line delivered, unless the manifest says `typed: true` |

Times are RFC 3339 strings ("" none).

### start: the closures as data

The compiled kinds get three Go functions; a process gets data instead.

1. **Read a file** (`Start.Read`): the plugin reads it itself. `dir` is the start dir (the agent's cwd), where a project's
   committed CLI config lives; the plugin decides what of it outranks obor's flags (T-1033).
2. **Write a state file** (`Start.WriteState`): each of `files` is written as `<name>` in the project's state dir
   (`.obor/state/<name>`, a name, no `..`), and every `{file:<name>}` in the reply's `args` and `env` values is replaced
   with its path.
3. **Whether a lane's start gets obor's MCP server** (`MCPRegistration.Lane`): the manifest's `mcp_lane`.

Every variable in `env` must be among the manifest's `vars` (`agents.Vars`, T-812: a start unsets every registry var
it does not set).

## The manifest

```json
{
  "version": 1,
  "name": "mykind",
  "status": "terminal",
  "caps": {"hooks": "none", "inbox": false, "resume": true, "skills": false, "sessions": false,
           "named_session": false, "project_plugin": false, "model_arg": false, "version_var": ""},
  "efforts": ["low", "high"],
  "vars": ["MYKIND_CONFIG"],
  "env": ["HOME", "MYKIND_HOME"],
  "stop": {"quit": "/exit", "second_enter": false, "clear_input": ["ctrl+u"], "after_escape": [], "program": "",
           "dialog": {"option": "Quit anyway", "key": "(\\d)\\. Quit anyway"}},
  "headless": {"programs": [], "subcommands": ["run"], "anywhere": [], "value_opts": ["--model"], "print_flag": false},
  "transcript": "${HOME}/.mykind/sessions/{sid}.jsonl",
  "inbox_record": ["MYKIND_INBOX"],
  "not_ready": {"pattern": "Updating", "why": "is updating itself"},
  "typed": true,
  "mcp_lane": {"how": "MYKIND_CONFIG (the start's generated config)", "registered": true, "static": false}
}
```

| Field | Meaning (the `agents` field it fills) |
|---|---|
| `version`, `name` | 1, and the config's `<name>`: anything else is refused |
| `status` | where the agents' status and turns come from (`Caps.Status`): `terminal` (herdr's agent detection), `hooks` (the kind's hooks report turns), `transcript` (`turn` reads them from the session's transcript) |
| `caps` | `Caps`; `hooks` is `none`, `native` or `plugin` (`HookTransport`). `Caps.Transcript` is `transcript` being set |
| `efforts` | the values a model ref's `:<effort>` may take (`Efforts`; none: no effort) |
| `vars` | every variable `start`'s `env` may set (`Vars`) |
| `env` | the variables obor snapshots into each request, and the only ones `transcript` may name |
| `stop`, `headless` | `Stop` (the quit recipe; `dialog.key` a regular expression) and `Headless` (the argv forms the guard denies; `programs` empty: the name) |
| `transcript` | the glob of a session's transcript: `{sid}` the session id, `${VAR}` a variable of `env` (one empty: none) |
| `inbox_record` | the variables whose values, as a JSON object, are a session's inbox record (written by the SessionStart hook, given back to `deliver` as `record`); one empty: no record |
| `not_ready` | `{pattern, why}`: a regular expression on the start's screen, `why` the reason; `{call: true}`: run `not-ready` per poll; absent: ready once started |
| `typed` | every line is typed into the pane: `deliver` is never called |
| `mcp_lane` | `obor setup mcp --model`'s answer for a lane's start: how, registered, static (served by an installed file setup checks) |

## Where the core branches on status

`Caps.Status` replaced `Caps.Turns` (T-1036). The core reads it through `agents.TurnsRead` (status `transcript`) in
`internal/herdr/panes.go` (`Panes.Turn`: a pane's last turn is read from its transcript) and `internal/app/stopped.go`
(`turnsRead`: a worker's or role agent's stop and its end are read from the transcript). `terminal` and `hooks` change
nothing yet: herdr's agent detection still gives every pane its status. The compiled kinds: claude `terminal`, codex
`transcript`, opencode `terminal`.

## Not covered

- A model name in the args (`agents.ModelEditor`): a plugin kind's model id goes on as `--model <id>`.
- Project-file warnings (`agents.ProjectChecker`) and an effort that depends on the args (`agents.ArgsChecker`).
- Live conformance: a plugin kind has the static tier, `make conformance TIER=static KIND=plugin`, run against
  `tests/conformance/kindplugin/obor-kind-shell`, a shell-script kind answering every call.
