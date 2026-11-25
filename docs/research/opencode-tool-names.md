# OpenCode tool names: what a plugin's `execute.before` sees (P2 of the v0.44.1 advice; it decides W8)

Question (`docs/research/v0.44.1-deep-dives-advice.md` §3(b) P2, "Missed by both"): `opencode/obor.js` maps built-in tools
only (`toClaudeEvents`), so a tool it does not know is allowed. W8 is to deny such a tool by name in an obor project, as
`kinds/agy/kind/hook.go` does for agy through the guard's `tool_name: "unguarded"` event, with obor's and the project's MCP
tools excepted. What name does OpenCode 2.2.0's `execute.before` hook (the plugin call `ctx.tool.hook("execute.before",
fn)`, `obor.js:569`) see for an MCP tool (obor's and another server's), a custom tool and a subagent's (`task`) call,
and what is its argument shape?

**Answer.** The hook's argument is `{tool, sessionID, agent, messageID, id, input}`: no server, no namespace, no parent
session. `input` is the tool's argument object as the model gave it. Three things differ from what the advice assumed:

1. **The model reaches MCP tools only through one tool, `execute`** (Code Mode): it writes JavaScript,
   `tools.obor.job_next({})`, and `execute` runs it. The hook fires twice for such a call: first `tool: "execute"` with
   `input: {code}`, then, for each tool the code calls, `tool: "<namespace>_<tool>"` with that tool's own arguments
   (`obor_job_next`, `probe-b_echo`). The same holds for OpenCode's own `opencode_*` tools and a plugin's code-mode tool.
   A deny-by-name rule that counts `execute` as an unknown tool denies obor's own MCP tools: the probe's deny of `execute`
   stopped `obor_merge_preview` before it was called. `execute` has to be allowed and its inner calls judged by id.
2. **A subagent's tool is named `subagent`, not `task`** (input `{agent, description, prompt, model?, sessionID?,
   background?}`). The child's own tool calls go through the same hook, under the child's `sessionID` and `agent:
   "general"`.
3. **The hook sees a name before the host checks it exists:** an invented tool name and a plugin tool the model was not
   offered both reached it, and the host rejected them afterwards (`No tool named "nosuchtool" is currently available`).

Built-in tools carry plain names (`shell`, `edit`, `write`, `read`, `glob`, `grep`, `skill`, `question`, `webfetch`,
`websearch`, `subagent`); a namespaced tool's name is its **id**, `<namespace>_<tool>`, and `ctx.tool.list()` gives each
tool's `id` and `options.namespace`, which the hook does not. A throw in the hook denies: an inner call's throw is an
exception in the model's code, an `execute`'s throw is the tool's error. Not verified: a file-defined custom tool
(`.opencode/tool[s]/`), see "Not verified".

## Method

- 2026-11-23. OpenCode v2.2.0 (`~/.opencode/bin/opencode`, no update: `OPENCODE_DISABLE_AUTOUPDATE=1`, `autoupdate: false`),
  herdr 0.9.5, macOS. The method is `docs/research/opencode-lead-probes.md`'s: a throwaway herdr session `probe-oc-p2`
  (headless server under `env -i`), one pane at the scratch dir `<kit>/repo` (`<kit>` = `/Users/Shared/obor-oc-probe-P2`,
  outside `$HOME`), `opencode --auto --standalone` started as obor starts it, with `OPENCODE_CONFIG=<kit>/opencode.json` and
  scratch `XDG_{CONFIG,DATA,STATE,CACHE}_HOME`. Driven with `herdr pane send-text` and read with `pane read`. Never
  `opencode run`, `serve` or `debug`.
- The model is a fake OpenAI-compatible provider on 127.0.0.1 (`fakeprov.py` of the lead probes). It answers each request
  that carries tools with the next scripted spec: a named tool call with arguments, or a text. So the probe chose the
  tool calls, and every request body is on disk (the tools the model was offered, the tool results it got). The config
  denies every provider but the fake (`policies`), so no model call left the machine.
- `<kit>/repo` is no git repository (the researcher guard allows no `git init`) but holds `.obor/config.json`. The
  config's two MCP servers: obor's own (`<obor-kit>/bin/obor mcp`, `OBOR_ROLE=worker`, `OBOR_NO_SELF_HOST=1`: a worker's
  tools `job_next`, `merge_preview`, `task_done`) and `probe-b` (agy-probes P2's `mcp.py`, one tool `echo`).
- The probe plugin (`<kit>/probeplug`, loaded from the config's `plugins` as obor's is) registers
  `ctx.tool.hook("execute.before", …)` and logs the whole call object to `log/plug.jsonl`. It also adds two tools with
  `ctx.tool.transform(t => t.add(…))`: `probe_direct` (no options) and `probe-plug_probe_code` (`options: {namespace:
  "probe-plug", codemode: true}`), and, when `ctrl/deny.txt` names the call's tool, throws.
- Footprint: the owner's `~/.config/opencode/{opencode,service,cli}.json` and `tui.jsonc` had the same checksums after the
  run; the owner's shared service (`opencode serve --service`) kept its pid (never attached to); the probe's data went to
  the scratch XDG dirs (the owner's own running OpenCode agents write `~/.local/share/opencode/opencode.db`; the probe
  did not). The TUI quit with `/exit`, the herdr session was stopped and deleted, the fake provider killed by its pid, `<kit>`
  removed, no probe process left.

## Saw

### The tools the model is offered

First model request (`req-001.json`): twelve tools,

```
edit glob grep question read shell skill subagent webfetch websearch write execute
```

(`patch` is in `ctx.tool.list()` but was not offered.) The system prompt's "Code Mode" section says: "Use the `execute`
tool to call the tools listed below. They cannot be called directly", "The catalog is complete", and lists three
namespaces: `obor` (3 tools), `opencode` (8), `probe-b` (1), each as `tools.<namespace>.<tool>(input)`. `execute`'s
schema is `{"code": string}`. A subagent's first request (a child session) offered ten: the same without `question` and
`subagent`, with `execute`.

### `ctx.tool.list()` (69 entries in this run)

Each entry has `name`, `id`, `options: {namespace?, codemode?}`, `description`, `input` (a JSON schema), `output`,
`execute`. Name | id | namespace, trimmed:

```
job_next | obor_job_next | obor | code           merge_preview | obor_merge_preview | obor | code
task_done | obor_task_done | obor | code         echo | probe-b_echo | probe-b | code
patch edit glob grep question read shell skill subagent webfetch websearch write   (id = name, no namespace)
session_rename | opencode_session_rename | opencode | code      … session_move, worktree_create, worktree_list,
worktree_remove, models, list_mcp_resources, read_mcp_resource  (the 8 `opencode_*`)
probe_direct | probe_direct | | (mine)                           probe_code | probe-plug_probe_code | probe-plug | code (mine)
list | browser_tabs_list | browser.tabs | code    navigate | browser_navigate | browser | code   upload | browser_files_upload | browser.files | code
… 43 `browser_*` tools in all (namespaces browser, browser.tabs, browser.files, browser.network, browser.trace, browser.cpu, browser.heap)
```

The id of a namespaced tool is the namespace with dots turned to underscores, an underscore, and the name. The `browser`
tools were in the list and not in the model's catalog; none was called.

### `opencode_*`: the eight own tools, what each does and how obor decides it

2026-11-25, the installed OpenCode v2.2.0 binary (`~/.opencode/bin/opencode`): the `OpenCodeTools.Plugin` chunk that
registers the namespace and, per tool, its `input` schema and the `execute` it runs. The live probe above listed the
eight ids and called one of them (`list_mcp_resources`, turn G), so an effect below is the code the tool runs, not a
scripted call.

| id | input (opencode 2.2) | what it does | how obor decides it |
|---|---|---|---|
| `opencode_session_rename` | `{sessionID?, title}`; `title` at least 1 character, trimmed, an empty one refused | `session.update({sessionID: sessionID ?? the call's session, title})`: the session's title only — no file, no directory | no event (`OPENCODE_PASS`): the session's own metadata |
| `opencode_session_move` | `{sessionID?, directory}`; `directory` at least 1 character, "relative to the target session's directory or absolute. Supports ~." | `session.move({sessionID: sessionID ?? the call's session, directory, delivery: "steer"})`: the session's directory — the cwd its tools run in — moves at the next safe boundary. It runs no git command; the TUI's Worktrees dialog moves a session the same way (`api.session.move`) | the guard's `unguarded` event with the row's own why (`OPENCODE_DENY`): "moves the session out of its directory; an obor agent stays where obor started it". The plugin's guard cwd is the start's directory and the transport does not follow a move, so the move is denied for every obor agent whatever the destination; the owner's own session (no role, the main checkout) is no opinion, as for any unguarded call. Its `directory` is not read |
| `opencode_worktree_create` | `{name}` | `worktree.create({projectID, name})`, which runs `git worktree add` | the guard's `Bash` event for `git worktree add '<name>'` |
| `opencode_worktree_remove` | `{directory, force?}` | `worktree.remove({projectID, directory, force})`, which runs `git worktree remove [--force]` | the guard's `Bash` event for that command |
| `opencode_worktree_list` | `{}` | `worktree.list({projectID})` | no event: it only reads |
| `opencode_models` | `{query?, provider?, all?, limit?, offset?}` | `provider.list` and `model.list`, filtered and paged | no event: it only reads |
| `opencode_list_mcp_resources`, `opencode_read_mcp_resource` | `{server?}` / `{server, uri}` | the runtime's `resourceCatalog` / `readResource` | no event: they only read |

`opencode_session_move` is denied whatever its arguments: its row reads no `directory` (the guard's decision is the
same for any), and opencode's own schema requires the argument, so a call that leaves it out gets the same answer.

### The hook calls (`log/plug.jsonl`; each turn one prompt, the scripted calls below)

| Turn | The model's call | What `execute.before` got (`tool` → `input`), in order |
|---|---|---|
| E | `shell {"command": "echo P2 > p2.txt"}` | `shell` → `{"command": "echo P2 > p2.txt"}` |
| A | `execute {"code": "return await tools.obor.job_next({});"}` | `execute` → `{"code": "…"}`; `obor_job_next` → `{}` |
| B | `execute` of `tools["probe-b"].echo({text: "hi"})` | `execute` → `{code}`; `probe-b_echo` → `{"text": "hi"}` |
| G | `execute` of `tools.opencode.list_mcp_resources({})` | `execute`; `opencode_list_mcp_resources` → `{}` |
| C | `execute` of `tools["probe-plug"].probe_code({text: "c"})` (a plugin-added code-mode tool) | `execute`; `probe-plug_probe_code` → `{"text": "c"}`; the tool's own `execute` then ran (it returned a bare string, which the host refused: `o is not an Object`) |
| H | one `execute` that awaits `tools.obor.merge_preview()` and `tools["probe-b"].echo(…)` in a `Promise.all` | `execute`; `obor_merge_preview` → `{}`; `probe-b_echo` → `{"text": "two"}` |
| F | `subagent {"agent": "general", "description": "P2 probe", "prompt": "MARK-SUB run the command"}` | `subagent` → `{"agent": "general", "description": "P2 probe", "prompt": "MARK-SUB run the command"}`; then the child's `shell` → `{"command": "echo child > p2-child.txt"}` |
| D | `probe_direct {"text": "d"}` (a plugin-added tool with no options) | `probe_direct` → `{"text": "d"}`, then the host's result: `{"error":{"type":"tool.execution","message":"No tool named \"probe_direct\" is currently available. Please use a tool from the available tool list."},"content":[]}`; the tool's `execute` never ran |
| I | `nosuchtool {"x": 1}` | `nosuchtool` → `{"x": 1}`, then the same host error |

The call objects, in full (`sessionID`s trimmed):

```
{"tool":"execute","sessionID":"ses_…5T5l","agent":"build","messageID":"msg_20717889d001ectGiZgKmJ3KNd","id":"call_3","input":{"code":"return await tools.obor.job_next({});"}}
{"tool":"obor_job_next","sessionID":"ses_…5T5l","agent":"build","messageID":"msg_20717889d001ectGiZgKmJ3KNd","id":"call_3","input":{}}
{"tool":"subagent","sessionID":"ses_…5T5l","agent":"build","messageID":"msg_207184718001gOgCM4eDa7p8cW","id":"call_14","input":{…}}
{"tool":"shell","sessionID":"ses_…RafhC4","agent":"general","messageID":"msg_207184726001WphFj44z2QLSti","id":"call_15","input":{"command":"echo child > p2-child.txt"}}
```

- `input` is an object (not a JSON string) and is the tool's argument object: `{}` for a tool with no arguments. The hook
  callback takes one argument (nothing after it).
- `id` is the model's tool-call id. An `execute` and the inner calls it makes carry the same `id` and `messageID`; the
  inner calls come in the order the code starts them.
- A child session (the subagent's) has its own `sessionID` and `agent: "general"` (the root's is `"build"`). No field of
  the call names the parent; `obor.js` already asks `ctx.session.get` for `parentID`.
- The shell tool's arguments are `command` (and optional `workdir`, `timeout`, `background`), as `toClaudeEvents`
  expects for 2.x.

### A throw in the hook

`ctrl/deny.txt` named a tool and the plugin threw `Error("P2 deny: <tool> is not allowed here")` for it:

| Denied | The call | What the model got |
|---|---|---|
| `probe-b_echo` | `execute` with the call in a `try`/`catch` (J) | `CAUGHT: P2 deny: probe-b_echo is not allowed here` (the screen: `✓ execute`, `✗ probe-b.echo [text=x]`) |
| `probe-b_echo` | `execute` with the bare call (K) | the result text `P2 deny: probe-b_echo is not allowed here` |
| `execute` | `execute` of `tools.obor.merge_preview()` (L) | `{"error":{"type":"unknown","message":"P2 deny: execute is not allowed here"},"content":[]}`; the hook saw no `obor_merge_preview`, so the code never ran |

## What it means for W8

Stated as what the facts allow, not as the design:

- The names a plugin has to place today (`opencode/obor.js:143-171` maps `bash`/`shell`, `edit`, `multiedit`, `write`,
  `patch`/`apply_patch`, `read`, `glob`, `grep`, `list` and returns `[]` for the rest). The rest seen here: `execute`,
  `subagent`, `skill`, `question`, `webfetch`, `websearch`, and every `<namespace>_<tool>` id.
- `execute` is the carrier of every MCP tool, obor's included. It is not a tool to deny by name; its `code` is a string of
  JavaScript, and what it does is decided by the inner calls the hook then sees.
- An inner call's id is `<namespace>_<tool>`. The hook does not give the namespace, but the plugin has `ctx.tool.list()`:
  look the id up there and read `options.namespace`. That is exact. Matching the id by prefix is not: `obor_foo_bar` is
  server `obor`'s tool `foo_bar` or a server `obor_foo`'s tool `bar`.
- obor's tools are the `obor` namespace (`obor_job_next`, `obor_merge_preview`, `obor_task_done` for a worker); another
  server's are `<server>_<tool>` (`probe-b_echo`); OpenCode's own are `opencode_*`.
- A subagent's child calls reach the same hook (with the child's `sessionID`), so the child's own tool calls are
  checked whether or not the `subagent` call is. Whether `subagent` itself is denied (agy's `invoke_subagent` is, for the
  reason `kinds/agy/kind/hook.go` gives: the child cannot be told from the agent's session) is a policy question this
  probe does not answer.
- A name the host does not know needs no deny to stay unrunnable (turns D and I), but it is what a table miss would name.
  A tool the host does run and the plugin does not map (`webfetch`, `websearch`, `skill`, `question`) does.

## Which namespaces are MCP servers: `ctx.mcp.list()`

2026-11-23, the installed OpenCode v2.2.0 binary (`~/.opencode/bin/opencode`: its bundled plugin API construction, the
`server.mcp` route schemas and the schema codec that shapes a plugin call's answer). A live `opencode serve` probe is
denied in a lane (the obor guard reads it as a headless agent run), so this reading is the source; the plugin context
page states its read methods use the client's inputs and responses.

- `setup(ctx)` has `ctx.mcp` beside `ctx.tool`, `ctx.session` and `ctx.event`: `{list, transform, reload}`.
- `ctx.mcp.list(input?)` takes an optional location and answers the `mcp.list` route's response, `{location, data}`:
  `data` is an array of `Mcp.Server` `{name, status, title?, integrationID?}`. `status` is one of
  `{status: "connected" | "pending" | "disabled" | "failed" | "needs_auth"}`, with `error` on the last three. A call
  with no location is the plugin instance's own (`opencode mcp list` itself reads this response's `.data`).
- A namespace is an MCP server when that server's `name` has `status.status === "connected"`: the runtime decides,
  whatever config layer named it (the inline `OPENCODE_CONFIG_CONTENT` obor preserves, the system config, an ancestor
  project, `OPENCODE_CONFIG_DIR`). A server a lane's generated config disables is `"disabled"`, so its tools are never
  reported connected.
- `ctx.tool.list()` still gives each id its namespace; its `options` carry no MCP marker, so the two lists together are
  the one source (`opencode/obor.js`'s `connectedServers`). A route whose success is only `{data}` (e.g.
  `session.get`) answers its `data` alone; the `location, data` pair above is why `mcp.list` keeps its envelope.

## Not verified

- A file-defined custom tool. `.opencode/tools/*.js` and `.opencode/tool/*.js` with a plain `{description, args, execute}`
  default export were not listed by `ctx.tool.list()` at a restart (the `tool()` helper of `@opencode-ai/plugin` was not
  tried: it needs an npm install, which the probe did not do). The custom tools here came from the plugin API
  (`ctx.tool.transform` with `add`). Why `probe_direct` (no `options`) was in `ctx.tool.list()` and not offered to the
  model is not known.
- `execute`'s description says its code may also call tools "returned by the `search` function". No `search` was
  scripted, so whether it reaches tools outside the catalog (and under which ids) was not seen.
- An MCP server named with a space, a dot, upper case or an underscore: `ctx.tool.list()` gives a namespaced tool's
  `options.namespace`, and whether it is the `Mcp.Server` name unchanged (the match `connectedServers` makes) or a
  sanitised one was not seen. A name that differs leaves the tool unguarded.
- A remote (`type: "remote"`) MCP server, and an MCP server's tool whose name has an underscore.
- A lead's obor tools: `OBOR_ROLE=lead` listed none in the scratch project (the probe used `worker`). The browser
  namespace's tools were listed and never offered or called.
- OpenCode 1.x names (`bash`, `filePath`) and the 2.x names of `patch`'s arguments: only `shell` was called here.

## Adjacent, not investigated

- obor's own MCP server answered the probe's `job_next` with `not in an obor project (no .obor/config.json)`, although the
  directory OpenCode ran in held a `.obor/config.json`; the hook fired regardless, so it does not touch the question.

## Appendix: the probe kit

Re-probe: put the files under one dir `<kit>` (`bin/`, `probeplug/`), make `log/`, `ctrl/`, `xdg/{config,data,state,cache}` and
`repo/` (a plain dir with `.obor/config.json` and a `README.md`: the researcher guard allows no `git init`, and OpenCode ran
without a repository). `bin/fakeprov.py` is `docs/research/opencode-lead-probes.md`'s appendix, unchanged; `bin/mcp.py` is
`docs/research/agy-probes.md` P2's, unchanged; `bin/scr.sh`, `bin/type.sh` and `bin/keys.sh` are the lead probes' too, and
`bin/env.sh` is theirs with `S=probe-oc-p2` and `K=<kit>`. Start `python3 -I bin/fakeprov.py <kit>` in the background (its
port is in `log/port`, its pid in `log/fakeprov.pid`) and write `opencode.json` with that port. Start the herdr session
(headless `herdr --session probe-oc-p2 server` under `env -i` with `HOME USER LOGNAME SHELL TERM LANG PATH`),
`herdr --session probe-oc-p2 workspace create --cwd <kit>/repo --label oc --no-focus`, write the root pane id to `log/pane`,
then `bin/oc.sh`. Each scripted turn is `python3 bin/turn.py <label> '<spec>'...`: one JSON spec per model request that
carries tools (a request without tools is the title request and takes none), `{"tool": {"name", "args"}}` for a call and
`{"text"}` for the reply. The turns of this run, in order, with the specs (`E` is the `execute` tool; `T` is
`{"text":"turn done"}`):

| Turn | Specs |
|---|---|
| A | `execute` `{"code": "return await tools.obor.job_next({});"}`, `T` |
| B | `execute` `{"code": "return await tools[\"probe-b\"].echo({text: \"hi\"});"}`, `T` |
| C | `execute` `{"code": "return await tools[\"probe-plug\"].probe_code({text: \"c\"});"}`, `T` |
| D | `probe_direct` `{"text": "d"}`, `T` |
| E | `shell` `{"command": "echo P2 > p2.txt"}`, `T` |
| F | `subagent` `{"agent": "general", "description": "P2 probe", "prompt": "MARK-SUB run the command"}`; the child's `shell` `{"command": "echo child > p2-child.txt"}`; the child's `{"text": "child done"}`; the parent's `{"text": "F parent done"}` |
| G | `execute` `{"code": "return await tools.opencode.list_mcp_resources({});"}`, `T` |
| H | `execute` `{"code": "const r = await Promise.all([tools.obor.merge_preview(), tools[\"probe-b\"].echo({text: \"two\"})]); return r.length;"}`, `T` |
| I | `nosuchtool` `{"x": 1}`, `T` |
| J, K | with `ctrl/deny.txt` = `probe-b_echo`: `execute` of `tools["probe-b"].echo` in a `try`/`catch` (J) and bare (K), `T` |
| L | with `ctrl/deny.txt` = `execute`: `execute` `{"code": "return await tools.obor.merge_preview();"}`, `T` |

Stop: `/exit` and a second Enter in the pane, `herdr --session probe-oc-p2 server stop`, `herdr session delete probe-oc-p2`,
`kill $(cat <kit>/log/fakeprov.pid)`, remove `<kit>`.

### `opencode.json`

`<port>` is the fake provider's. The v2 provider field is `package` (the lead probes' appendix).

```json
{
  "$schema": "https://opencode.ai/config.json",
  "model": "fake/m1",
  "small_model": "fake/m1",
  "providers": {
    "fake": {
      "name": "Fake",
      "package": "@opencode/ai/providers/openai-compatible",
      "settings": {
        "baseURL": "http://127.0.0.1:<port>/v1",
        "apiKey": "probe"
      },
      "options": {
        "baseURL": "http://127.0.0.1:<port>/v1",
        "apiKey": "probe"
      },
      "models": {
        "m1": {
          "name": "Fake m1",
          "limit": {
            "context": 200000,
            "output": 8192
          }
        }
      }
    }
  },
  "plugins": [
    "file://<kit>/probeplug"
  ],
  "autoupdate": false,
  "policies": [
    {
      "action": "provider.use",
      "resource": "*",
      "effect": "deny"
    },
    {
      "action": "provider.use",
      "resource": "fake",
      "effect": "allow"
    }
  ],
  "mcp": {
    "servers": {
      "obor": {
        "type": "local",
        "command": [
          "<obor-kit>/bin/obor",
          "mcp"
        ],
        "environment": {
          "OBOR_NO_SELF_HOST": "1",
          "OBOR_ROLE": "worker"
        }
      },
      "probe-b": {
        "type": "local",
        "command": [
          "python3",
          "<kit>/bin/mcp.py",
          "b"
        ]
      }
    }
  }
}
```

### `bin/oc.sh`

```sh
#!/bin/sh
# oc.sh [opencode args…]: opencode as obor starts it (--auto --standalone) in the probe pane, scratch config and XDG dirs.
. "$(dirname "$0")/env.sh"
P=$(cat $K/log/pane)
q=""; for a in "$@"; do q="$q '$(printf %s "$a" | sed "s/'/'\\\\''/g")'"; done
herdr --session "$S" pane run "$P" "clear; cd $REPO && env OPENCODE_CONFIG=$K/opencode.json XDG_CONFIG_HOME=$K/xdg/config XDG_DATA_HOME=$K/xdg/data XDG_STATE_HOME=$K/xdg/state XDG_CACHE_HOME=$K/xdg/cache OPENCODE_DISABLE_AUTOUPDATE=1 opencode --auto --standalone$q" >/dev/null
herdr --session "$S" pane wait-output "$P" --regex "Build|ctrl\+p|esc interrupt|Ask anything" --timeout 30000 >/dev/null 2>&1
sleep 1
"$K/bin/scr.sh" 30
```

### `bin/turn.py`

```python
#!/usr/bin/env python3
"""turn.py <label> <spec-json>...: write the fake provider's queue (one JSON spec per tool-bearing request), type
'MARK-<label> go' in the probe pane, wait until the model requests stop, then print the screen's tail and the plugin
log's new entries (execute.before calls, custom-tool runs, errors), one per line."""
import glob, json, os, subprocess, sys, time
K = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
label, specs = sys.argv[1], sys.argv[2:]
LOG = K + "/log/plug.jsonl"
n0 = sum(1 for _ in open(LOG))
open(K + "/ctrl/queue.jsonl", "w").write("".join(s + "\n" for s in specs))
nreq0 = len(glob.glob(K + "/log/req-*.json"))
subprocess.run(["sh", K + "/bin/type.sh", "MARK-%s go" % label, "enter"])
last, quiet = nreq0, 0
t0 = time.time()
while time.time() - t0 < 90:
    time.sleep(1.5)
    n = len(glob.glob(K + "/log/req-*.json"))
    quiet = quiet + 1 if n == last else 0
    last = n
    if quiet >= 3 and n > nreq0:
        break
time.sleep(1)
print("===== turn %s: %d model requests; queue left: %d" % (label, last - nreq0, len([l for l in open(K + "/ctrl/queue.jsonl") if l.strip()])))
print(subprocess.run(["sh", K + "/bin/scr.sh", os.environ.get("TAIL", "9")], capture_output=True, text=True).stdout)
for i, l in enumerate(open(LOG)):
    if i < n0:
        continue
    d = json.loads(l)
    if d["kind"] == "execute.before":
        x = d["data"]
        print("execute.before tool=%r sessionID=%s callKeys=%s extraArgs=%s\n    input=%s" % (x["tool"], (x["sessionID"] or "")[-8:], x["callKeys"], x["extraArgs"], json.dumps(x["input"])[:400]))
    elif d["kind"] in ("custom.execute", "transform.err", "custom.add.err"):
        print(d["kind"], json.dumps(d["data"])[:300])
```

### `probeplug/package.json`

```json
{ "name": "probe-opencode-tools", "version": "0.0.0", "private": true, "type": "module", "main": "index.js", "exports": { ".": "./index.js" } }
```

### `probeplug/index.js`

```js
// P2 probe plugin: logs the plugin context's shape and every tool.execute.before call (the whole call object) to
// <kit>/log/plug.jsonl. It answers nothing: no throw, so every tool runs.
import fs from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";

const KIT = path.dirname(path.dirname(fs.realpathSync(fileURLToPath(import.meta.url))));
const LOG = path.join(KIT, "log", "plug.jsonl");
function log(kind, data) {
  try {
    fs.appendFileSync(LOG, JSON.stringify({ t: Date.now(), pid: process.pid, kind, data }) + "\n");
  } catch {}
}
function shape(o, depth = 2) {
  if (o === null || typeof o !== "object") return typeof o;
  const out = {};
  for (const k of Object.keys(o)) {
    const v = o[k];
    out[k] = typeof v === "function" ? `fn/${v.length}` : depth > 0 && v && typeof v === "object" ? shape(v, depth - 1) : typeof v;
  }
  return out;
}
function safe(o) {
  try {
    return JSON.parse(JSON.stringify(o, (k, v) => (typeof v === "function" ? `fn/${v.length}` : typeof v === "bigint" ? String(v) : v)));
  } catch (e) {
    return String(e?.message ?? e);
  }
}

async function setup(ctx) {
  log("setup", { dir: ctx.location?.directory, ctx: shape(ctx, 3), toolKeys: Object.keys(ctx.tool ?? {}) });
  await ctx.tool.hook("execute.before", async (call, ...rest) => {
    log("execute.before", { callKeys: Object.keys(call ?? {}), tool: call?.tool, input: safe(call?.input), sessionID: call?.sessionID,
      full: safe(call), extraArgs: rest.length });
    let deny = [];
    try { deny = fs.readFileSync(path.join(KIT, "ctrl", "deny.txt"), "utf8").split("\n").map((x) => x.trim()).filter(Boolean); } catch {}
    if (deny.includes(call?.tool)) { log("denied", { tool: call.tool }); throw new Error("P2 deny: " + call.tool + " is not allowed here"); }
  });
  log("hook.registered", "execute.before");
  log("src", { transform: String(ctx.tool.transform).slice(0, 1500), list: String(ctx.tool.list).slice(0, 800), hook: String(ctx.tool.hook).slice(0, 800) });
  const dump = async (why) => {
    try {
      const l = await ctx.tool.list();
      log("tool.list." + why, { type: Array.isArray(l) ? "array" : typeof l, n: Array.isArray(l) ? l.length : undefined, sample: safe(Array.isArray(l) ? l.slice(0, 3) : l) });
      log("tool.names." + why, safe(Array.isArray(l) ? l.map((t) => [t?.name, t?.id, t?.options?.namespace, t?.options?.codemode ? "code" : ""].join("|")) : Object.keys(l ?? {})));
    } catch (e) { log("tool.list.err", String(e?.message ?? e)); }
  };
  const add = (t, spec) => {
    try { t.add(spec); log("custom.added", { id: spec.id, name: spec.name }); } catch (e) { log("custom.add.err", { id: spec.id, err: String(e?.message ?? e) }); }
  };
  try {
    await ctx.tool.transform((t) => {
      log("transform.arg", { keys: Object.keys(t), ns: typeof t.namespace, nsStr: String(t.namespace).slice(0, 300) });
      add(t, { id: "probe_direct", name: "probe_direct", description: "Probe custom tool (direct): returns CUSTOM-DIRECT-OK.",
        input: { type: "object", properties: { text: { type: "string" } } }, output: {}, options: {},
        execute: async (input) => { log("custom.execute", { id: "probe_direct", input: safe(input) }); return "CUSTOM-DIRECT-OK"; } });
      add(t, { id: "probe-plug_probe_code", name: "probe_code", description: "Probe custom tool (code mode): returns CUSTOM-CODE-OK.",
        input: { type: "object", properties: { text: { type: "string" } } }, output: {}, options: { namespace: "probe-plug", codemode: true },
        execute: async (input) => { log("custom.execute", { id: "probe_code", input: safe(input) }); return "CUSTOM-CODE-OK"; } });
    });
    log("transform.done", {});
  } catch (e) { log("transform.err", String(e?.message ?? e)); }
  setTimeout(() => dump("t5"), 5000);
  setTimeout(() => dump("t20"), 20000);
  return () => log("cleanup", {});
}
export default { id: "probe.opencode.tools", setup };
```
