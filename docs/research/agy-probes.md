# agy probes: what Antigravity CLI does at run time (T-1458)

`docs/research/agent-kinds-advice.md` §1.1 lists nine questions (P1 to P9) the agy kind's design rests on, all
answered from documents so far. This run started agy live and records what it did, with the command or screen that
showed it, so tasks 2 to 6 of that advice can be written against observed behaviour.

**P2 passes.** A stdio MCP server, from a workspace `.agents/mcp_config.json` or from a plugin, inherits the CLI's whole
environment (`OBOR_ROLE`, `OBOR_LANE`, `OBOR_PANE`, `HERDR_PANE_ID`, …). A static `mcp_config.json` can serve
`obor mcp`.

**P1 fails for a plugin outside the repository.** A workspace `.agents/plugins.json` entry loads its plugin only when
the plugin's real path lies inside a workspace root: the repository agy was started in, or a directory given with
`--add-dir`. The kit's planned entry `~/.obor/kit/agy` (advice §1.5) does not load as written; a `~/` entry, an absolute
entry and a symlink pointing out of the repository were all ignored, with no message. The same entry loads when the
kind also passes `--add-dir` for the plugin's parent directory. Workspace-relative entries (obor-kit's own checkout)
load. In a trusted folder nothing asks for approval.

- Date: 2026-11-22. agy 1.5.14 (no self-update during the run), herdr 0.9.5, macOS. Model for the turns:
  `gemini-3.8-flash-low`, except P5.
- Method: a throwaway herdr session `probe-agy` (headless server under `env -i`, one workspace, one pane), a scratch git
  repository with a linked worktree under the session's scratch dir (outside `$HOME`), and a scratch dir under
  `~/.cache` for the `~/` plugin entry. agy was started interactively in the pane with `herdr pane run`, driven with
  `herdr agent prompt`, `pane send-text` and `pane send-keys`, and read with `pane read`, the hook and MCP logs, the
  conversation's transcript and `~/.gemini/antigravity-cli/cli.log`. Never `-p`, `--print`, `--prompt` or `agy acp`.
- Fixture (appendix): `.agents/hooks.json` with one named hook `probe-ws` that has a handler for each of the six events
  (logs stdin, environment and cwd, answers `{}` or a reply from a control file), `.agents/mcp_config.json` with a stdio
  server `probe-ws` that logs its environment and cwd and has one tool `echo`, and `.agents/plugins.json` naming plugins
  in several places (P1). The workspace plugin `wsplug` has the same hooks and server, tagged `wsplug`.
- In the quotes `<kit>` is the session's scratch dir, `<repo>` the scratch repository (`<kit>/repo`), `<account>` the
  signed-in account. Hook payloads are trimmed of the common fields where only the event's own fields matter.
- Footprint: the saved settings were not touched (`settings.json`, `config/config.json`, `config/hooks.json` and
  `config/mcp_config.json` had the same checksums after the run as before; trust and permissions went through
  `--settings` only, every trust dialog was answered "No, exit", every permission prompt "Yes" once or "No"). agy's
  own run-time data grew: conversations under `~/.gemini/antigravity-cli/brain/`, `presence/*.lock`,
  `cache/last_conversations.json` entries for the two scratch paths, `cli.log`. The session was stopped and deleted, the
  `~/.cache` dir removed, no agy or probe process left.

Summary:

| Probe | Answer | Decides |
|---|---|---|
| P1 | Workspace `hooks.json`, `mcp_config.json`, skills and in-repo plugins load with no prompt in a trusted folder; a linked worktree loads its own copy. A `plugins.json` entry or symlink whose real path is outside every workspace root is ignored; `--add-dir` of the plugin's parent makes it load. A plugin's `skills` dir that is a symlink out of the workspace does not load. | 1.5: the kit's plugin needs `--add-dir` (or the fallback, D2) |
| P2 | Yes: the whole environment, plus the config's `env`; cwd the workspace root (workspace config) or the plugin dir (plugin, with `PLUGIN_ROOT`). Started at the conversation's first prompt, once per CLI process. | `obor mcp` from a static `mcp_config.json` works |
| P3 | Tool names `run_command`, `write_file`, `edit_file`, `read_file`, `call_mcp_tool`, and matchers match them. A PreToolUse `{}` **denies**; so do `"decision": ""`, a non-zero exit and a timeout. `"ask"` falls through to agy's own permission check. SessionStart fires at a new conversation's first prompt and after `/clear`, not on `--conversation`. After a Stop `continue`, the next Stop has `executionNum: 1` and `continuationActive: false`. Hooks see the identity variables and `ANTIGRAVITY_CONVERSATION_ID`, not the LS address or token. | 1.6: the neutral answer is `ask`; `stop_hook_active` from `executionNum`; the session record from PreInvocation |
| P4 | `--settings` is laid over the saved settings key by key; a key it names replaces the saved value (arrays are not merged). `trustedWorkspaces` and `permissions.allow` in it take effect. Trust is by exact path. A plugin's MCP server is `<plugin>_<server>` in rules (`mcp(wsplug_probe-wsplug/*)`). | the per-start settings file holds the lane's trust and obor's MCP rule |
| P5 | `--model <base> --effort <e>` runs `<base>-<e>`. A slug that names an effort wins over `--effort` (notice). An effort the model lacks, or an unknown model, runs Gemini 4 Neon (High) with a notice. A label works. | 1.3: pass the base name and `--effort`, or the full slug alone; check efforts before the start |
| P6 | `-i` runs its prompt in a fresh start and in a `--conversation` resume. `-i "/<skill> handoff"` invokes the skill with `handoff` as its argument. A plugin's skill is `/<plugin>:<skill>` (one of two in-repo plugins showed it without the prefix). | the first prompt and the lead's |
| P7 | `agentapi send-message <conversationId> <text>` starts a turn in an idle session and reaches a busy one at its next model call; a half-typed draft stays. But the CSRF token it needs is only in the environment of commands the agent runs: not in hooks, not in MCP servers, not on disk. | lead lines: delivery works, getting the token to obor is open (see P7) |
| P8 | `/exit` quits on one Enter (the popup's) and prints the resume command; Ctrl+D twice and Ctrl+C twice quit; Ctrl+U and Esc Esc clear the input. No dialog with background tasks running; they are stopped. Untrusted folder: "Do you trust the contents of this project?", default "Yes, I trust this folder". Self-update check at most every 15 min at start. A linked worktree is in `default-cli-project`, as the main checkout. | stop, not-ready, trust |
| P9 | Auto mode (also the default here) runs commands in a sandbox: `go build`, `go test` and git reads pass; network and unix sockets are refused; writes outside the workspace, temp dirs and caches are refused. A PreToolUse deny is final. MCP calls prompt unless a `permissions.allow` rule names them. | D1: `--mode auto` works for a lane except anything that talks to a socket or the network |

## P1: workspace config, plugins.json, worktrees, skills

Question: do a workspace `.agents/hooks.json`, `.agents/mcp_config.json` and a plugin named by `.agents/plugins.json`
(a `~/` path and a workspace-relative path) load in a trusted folder with no approval prompt; does a linked worktree
find them at its own root; does a plugin's `skills` dir load when it is a symlink.

Ran: `setup.sh`, then `agy --model gemini-3.8-flash-low --settings <kit>/set-trust.json` in `<repo>` (the settings
file names `<repo>` and `<repo>-wt` in `trustedWorkspaces`; P4), one prompt. Then `setup2.sh` and `setup3.sh` added
more plugins and entries, and `/skills` listed what loaded after each start (`skills.sh`). Then the same start in
`<repo>-wt`. Entries tried, with where each plugin's real path is:

| Entry or link | Real path | Loaded |
|---|---|---|
| `.agents/hooks.json`, `.agents/mcp_config.json`, `.agents/skills/probe-wsskill` | in the repo | yes |
| `{"path": "plugsrc"}` → `wsplug` (hooks, MCP, `skills` a symlink to `<kit>/skills-wsplug`) | in the repo; skills outside | plugin yes, its skill no |
| `{"path": "plugsrc"}` → `wsplug2` (real `skills` dir) | in the repo | yes |
| `{"path": ".hid/plugins"}` → `hidplug` | in the repo, hidden dir | yes |
| `plugsrc/linkplug`, a symlink to `<kit>/linktarget/linkplug` | outside | no |
| `{"path": "~/.cache/obor-agy-probe-T1458/plugins"}` → `homeplug` | under `$HOME` | no |
| `{"path": "/Users/…/.cache/obor-agy-probe-T1458/plugins-abs"}` → `absplug` | under `$HOME` | no |
| `{"path": "<kit>/extplugins"}` → `extplug` | outside `$HOME`, no hidden part | no |

Saw:

- First start in the repository: `cli.log` `hooks_manager.go:54] loaded 3 named hooks from 3 hooks.json file(s)` (the
  user's herdr hook, `probe-ws`, `probe-wsplug`; `homeplug` missing). At the first prompt the hooks of `ws` and
  `wsplug` fired and both MCP servers started. No prompt, no notice for the entries that did not load.
- Adding the outside dirs to `trustedWorkspaces` did not change it. Adding `--add-dir <kit>/extplugins --add-dir
  ~/.cache/obor-agy-probe-T1458/plugins-abs` loaded `extplug` and `absplug` (`/skills`: `absplug/skills · From
  ~/.cache/obor-agy-probe-T1458/plugins-abs/absplug`). `--add-dir <kit>/linktarget` loaded `linkplug` through the
  in-repo symlink. `--add-dir <kit>/addonly`, a dir with a plugin that no entry names, loaded nothing: the entry and the
  workspace root are both needed.
- `/skills` grouped them as `Workspace skills · Workspace config` (`probe-wsskill`), `plugsrc/wsplug2/skills · From
  <repo>/plugsrc/wsplug2` (`probe-wsplug2`) and `.hid/plugins/hidplug/skills · From …` (`hidplug:probe-hidplug`).
- Linked worktree (`<repo>-wt`, `.agents/` committed): hooks fired from `<repo>-wt/.agents` and
  `<repo>-wt/plugsrc/wsplug`, the MCP servers ran with cwd `<repo>-wt` and `<repo>-wt/plugsrc/wsplug`. It finds its own
  copy at its own root.
- In an untrusted folder (P8) the dialog came first and `cli.log` said `loaded 1 named hooks from 1 hooks.json
  file(s)`: nothing of the workspace loads before trust.

Decides (1.5): the committed `.agents/plugins.json` naming the kit's `agy` dir works for obor-kit's own checkout
(workspace-relative) and for no installed kit (`~/.obor/kit/agy`) unless the kind adds `--add-dir ~/.obor/kit/agy`
(the entry's directory) to every start. That makes the kit's plugin dir part of the session's workspace (it appears
in `workspacePaths`); whether auto mode then approves edits there was not probed. The other way is the fallback of
§1.5, `agy plugin install`, the owner's call (D2). Skills ship as a real directory, not a symlink out of the plugin.

## P2: the MCP server's environment and cwd

Question: does a stdio MCP server inherit `OBOR_ROLE`, `OBOR_LANE`, `OBOR_PANE`, `HERDR_PANE_ID`, and what is its
cwd.

Ran: agy started as `env OBOR_ROLE=probe-worker OBOR_LANE=L-probe OBOR_PANE=w1:p1 agy …` in the herdr pane; the
servers log their environment at start (`mcp.py`); later the agent called each server's `echo` tool, which returns
what the server sees.

Saw (`mcp-…-ws.env`, `mcp-…-wsplug.env`, trimmed):

```
HERDR_PANE_ID=w1:p1
HERDR_SESSION=probe-agy
HERDR_SOCKET_PATH=/Users/…/.config/herdr/sessions/probe-agy/herdr.sock
OBOR_LANE=L-probe
OBOR_PANE=w1:p1
OBOR_ROLE=probe-worker
PROBE_SET=ws                                   # the config's "env"
PWD=<repo>
# wsplug's server also:
PLUGIN_ROOT=<repo>/plugsrc/wsplug
PROBE_SET=wsplug
```

- cwd: `<repo>` for the workspace server, `<repo>/plugsrc/wsplug` for the plugin's (`PWD` stays the inherited
  `<repo>`). Parent: the `agy` process.
- No `ANTIGRAVITY_*` variable in the server's environment (the hooks have `ANTIGRAVITY_CONVERSATION_ID`; nothing else).
- The plugin server's tool answered, through a tool call: `{"OBOR_ROLE": "probe-worker", "OBOR_LANE": "L-probe",
  "OBOR_PANE": "w1:p1", "HERDR_PANE_ID": "w1:p1", …, "PLUGIN_ROOT": "<repo>/plugsrc/wsplug", "cwd":
  "<repo>/plugsrc/wsplug", "tag": "wsplug"}`.
- The servers start at the first prompt of the CLI's first conversation (the log's timestamp is the SessionStart
  hook's), not when the CLI starts; they live as long as the CLI process (the same pids after `/clear`) and stop on
  exit. A `--conversation` resume starts them at its first prompt.
- The protocol agy speaks: `server/discover` first (protocol `2026-07-28`; the stub answered "no method"), then
  `initialize` with `protocolVersion: "2025-11-25"`, `notifications/initialized`, `tools/list`.

Decides: a static `mcp_config.json` can run `obor mcp`: it gets the agent's role, lane and pane from the environment.

## P3: the hook payloads and answers

Question: `toolCall.name` and `args` of a shell command and each file tool; when SessionStart fires; PreInvocation's
`invocationNum`; Stop's fields; what a PreToolUse `{}`, a non-zero exit and a timeout do; the hooks' environment.

Ran: prompts that run a command, write, edit and read a file and call an MCP tool, with the PreToolUse answer set by
a control file between turns (`hook.py`); a `/clear`; a `--conversation` resume; a second named hook `probe-match`
with one PreToolUse group per matcher string.

Saw, the tool calls (PreToolUse, trimmed of the common fields):

```
{"stepIdx": 4, "toolCall": {"name": "run_command", "id": "call_1002688346176174214",
  "args": {"CommandLine": "env | sort > agent-env.txt", "WaitMsBeforeAsync": 5000, "toolSummary": "Dump sorted environment variables"}}}
{"stepIdx": 6, "toolCall": {"name": "write_file", "args": {"Content": "alpha\n", "Path": "<repo>/notes.txt"}}}
{"stepIdx": 9, "toolCall": {"name": "edit_file", "args": {"Path": "<repo>/notes.txt", "Replacement": "beta", "Target": "alpha"}}}
{"stepIdx": 11, "toolCall": {"name": "read_file", "args": {"Path": "<repo>/README.md"}}}
{"stepIdx": 14, "toolCall": {"name": "call_mcp_tool", "args": {"Arguments": {"text": "hi"}, "ServerName": "probe-ws", "ToolName": "echo"}}}
```

- Paths are absolute. A two-place edit was one `edit_file` with a multi-line `Target`. No other file tool was seen.
- Matchers `run_command`, `write_file`, `edit_file`, `read_file` and `call_mcp_tool` each fired for their tool;
  `mcp_tool`, `file_change` and `view_file` (step-type names in the binary) fired for none.
- PostToolUse: the same `toolCall` and `stepIdx`, `"error": ""`; it fires for a tool that ran, not for a denied one;
  for a command that went to the background it fired when the command ended.

The PreToolUse answers (one command per turn, both named hooks answering):

| Answer | Result | The error the model and the screen got |
|---|---|---|
| `{}` | denied | `tool call denied by pre-tool hook: ` |
| `{"decision": ""}` | denied | `tool call denied by pre-tool hook: ` |
| `{"decision": "deny", "reason": "obor: probe deny reason XYZ"}` | denied | `Encountered error in tool execution: tool call denied by pre-tool hook: obor: probe deny reason XYZ` |
| `{"decision": "ask"}` and exit 1 | failed | `JSON hook "jsonhook__probe-ws_PreToolUse_0_0" failed: command failed: exit status 1, stderr: ` |
| `{"decision": "ask"}` after 35 s (default timeout 30 s) | failed after 30 s | `JSON hook "jsonhook__probe-ws_PreToolUse_0_0" failed: command failed: signal: killed, stderr: ` |
| `{"decision": "ask"}` | agy's own check: auto mode ran `echo probeA > a.txt` with no prompt; default mode showed its permission prompt | |
| `{"decision": "allow"}` | in default mode agy still showed its permission prompt for `echo probeG > g.txt` | |

- The first deny or failure ends the chain: the other named hook's PreToolUse did not run. The order of the named
  hooks is not fixed: `ws` ran before `wsplug` in some calls and after it in others.
- Cancelling agy's permission prompt with Esc ended the turn (`Interrupted · What should Antigravity CLI do
  instead?`) with no PostInvocation and no Stop hook.

SessionStart, PreInvocation, PostInvocation:

```
SessionStart: {"conversationId": "a340acfb-…", "cwd": "<repo>", "hookEventName": "SessionStart",
  "lastUserInput": "Reply with exactly the single word OK.", "modelName": "gemini-3.8-flash-low",
  "transcriptPath": "~/.gemini/antigravity-cli/brain/a340acfb-…/.system_generated/logs/transcript_full.jsonl",
  "artifactDirectoryPath": "~/.gemini/antigravity-cli/brain/a340acfb-…", "workspacePaths": ["<repo>"]}
PreInvocation: … "initialNumSteps": 1, "invocationNum": 0 …
```

- SessionStart fires with the first prompt of a new conversation (not at CLI start), after `/clear` with the next
  prompt (a new `conversationId`; herdr's `agent_session` followed it), and **not** on a `--conversation` resume (with
  or without `-i`). `transcriptPath` names `transcript_full.jsonl`; `transcript.jsonl` sits beside it.
- PreInvocation fires before every model call, `invocationNum` 0 at a turn's first and counting up (0 to 5 in a turn
  of five tool calls); PostInvocation after each, same number.
- `lastUserInput` is the last prompt the user submitted. A turn that an `agentapi` message (P7) or a finished
  background task starts has `invocationNum: 0` and the previous prompt in `lastUserInput`.

Stop:

```
{"terminationReason": "NO_TOOL_CALL", "error": "", "executionNum": 0, "fullyIdle": true, "continuationActive": false, …}
{"terminationReason": "NO_TOOL_CALL", "fullyIdle": false, …}          # the turn ended with a background command running
{"terminationReason": "TERMINAL_CUSTOM_HOOK", "executionNum": 0, …}   # a PreInvocation hook answered "terminate"
```

- A Stop answer `{"decision": "continue", "reason": "PROBE-STOP: now reply with exactly: CONTINUED"}` kept the turn
  going: the transcript got `Stop hook blocked termination: PROBE-STOP: …` as a system message, the model answered, and
  the next Stop had `"executionNum": 1, "continuationActive": false`. The documents say `continuationActive` is true
  then; here it was not. After one hook answered `continue` the other's Stop did not run.
- `ERROR` and a usage limit were not seen (nothing here forces them).

The injecting answers:

- PreInvocation `{"injectSteps": [{"systemMessage": {"systemMessage": "PROBE-INJECT: end every reply with the word
  KIWI."}}]}`: the transcript got `The following system message was injected by a PreInvocation hook: PROBE-INJECT: …`
  (source `SYSTEM_SDK`) and the reply was `HELLO KIWI`. It stays in the conversation: the next turn's reply ended in
  KIWI too.
- SessionStart with the same shape after `/clear`: `The following system message was injected by a SessionStart hook:
  PROBE-SESSION: …`, after the user's prompt and before the model call; reply `NEWCONV MANGO`.
- PreInvocation `{"injectSteps": [{"errorMessage": {"userMessage": "PROBE-BLOCK: obor refused this prompt"}}],
  "terminationBehavior": "terminate"}`: the screen showed `⚠ PROBE-BLOCK: obor refused this prompt`, no model call,
  Stop `TERMINAL_CUSTOM_HOOK`.

The hooks' environment and cwd: the agent's environment (`OBOR_*`, `HERDR_PANE_ID`, `HERDR_SOCKET_PATH`, …) plus
`ANTIGRAVITY_CONVERSATION_ID`; no `ANTIGRAVITY_LS_ADDRESS`, no `ANTIGRAVITY_CSRF_TOKEN`. cwd is the directory holding
the `hooks.json` (`<repo>/.agents`, or the plugin dir); a plugin's hooks get `PLUGIN_ROOT` (the plugin dir).

Decides (1.6):

- The neutral PreToolUse answer is `{"decision": "ask"}`; `{}` blocks every tool. obor's deny maps to `deny` with the
  reason, which reaches the model verbatim. A transport that crashes, exits non-zero or takes 30 s blocks the call
  (fails closed). The matcher is `run_command|write_file|edit_file` (the names in `toolCall.name`).
- `stop_hook_active` comes from `executionNum > 0`, not `continuationActive`. `fullyIdle: false` is there for the
  background-run rule. A Stop `continue` and its reason work as the advice's table says.
- `user-prompt-submit` keyed on PreInvocation `invocationNum == 0` also fires for turns that no user prompt started
  (agentapi, background completion), with a stale `lastUserInput`.
- obor's session record cannot rely on SessionStart (no resume); PreInvocation carries `conversationId` and
  `transcriptPath` on every call, which is what herdr's own hook uses. Esc on a permission prompt ends a turn with no
  Stop: a Stop-driven state has to tolerate a missing Stop.
- Hook order across named hooks is not fixed, and the first deny wins: the user's own hooks can deny before obor's run.

## P4: --settings, trust, permission rules

Question: is the `--settings` object laid over the saved settings key by key or does it replace them; do
`permissions.allow: ["mcp(<server>/*)"]` and `trustedWorkspaces` in it take effect; what name a plugin's MCP server has
in a rule.

Ran (`trust.sh`, the trust dialog as the signal, answered "No, exit"): starts in `$HOME` (the saved
`trustedWorkspaces` holds `$HOME` exactly) with no `--settings`, with `--settings '{"colorScheme": "dark"}'` (a file),
and with `--settings '{"trustedWorkspaces": ["/nonexistent-probe"]}'`; starts in a subdir of a trusted dir; MCP calls
with and without `permissions.allow` rules (`set-perm.json`, `set-perm2.json`).

Saw:

| Start | Trust dialog |
|---|---|
| `$HOME`, no `--settings` | no |
| `$HOME`, `--settings` without `trustedWorkspaces` | no |
| `$HOME`, `--settings` with `trustedWorkspaces: ["/nonexistent-probe"]` | yes |
| `<repo>` with `--settings` naming `<repo>` | no |
| `<repo>/plugsrc` with the same file; `<kit>/plain/sub` with `<kit>/plain` trusted | yes |
| `~/.cache/obor-agy-probe-T1458` (under the trusted `$HOME`), with or without `--settings` | yes |

- `cli.log` shows the parsed rules: `CLI settings initialized: permissions=&{Allow:[mcp(probe-ws/echo)
  mcp(probe-wsplug/*)] Deny:[] Ask:[]}, toolPermission=request-review`.
- With no rule, an MCP call in auto mode showed `probe-ws/echo` `Allow calling this tool?` with options `1. Yes, allow
  tool call` … `3. Yes, and always allow tool 'probe-ws/echo' (Persist to settings.json)` … `6. No, and always deny
  tool 'probe-ws/echo' (Persist to settings.json)`. Answering "No" ended the turn.
- `mcp(probe-ws/echo)` ran the workspace server's tool with no prompt. The plugin's server is named
  `wsplug_probe-wsplug` (the model listed its tools as `wsplug_probe-wsplug / echo`; the call
  `ServerName: probe-wsplug` failed with `tool echo is not enabled for server probe-wsplug`).
  `mcp(wsplug_probe-wsplug/*)` ran it with no prompt.
- After all of it, the saved `settings.json` had its old checksum: nothing from `--settings` is saved.

Decides: `--settings` is the per-start channel and is merged over the user's file by top-level key, so the kind writes
only obor's keys; a key it writes replaces the user's value for the session. Trust is by exact path, so every lane
worktree shows the trust dialog unless the start file's `trustedWorkspaces` names the lane's own directory (and, since
the key replaces, any other folder the session needs trusted). obor's rule is `mcp(<plugin>_obor/*)`, the prefix being
the plugin's name (directory and `plugin.json` name were the same here, so which of the two it is was not separated).

## P5: model and effort

Question: what `--model gemini-4-neon-high --effort low` runs; an effort the model lacks; `--model` with a label.

Ran: `p5.sh`, a start and one turn each; the footer, `cli.log`'s resolver lines, the hooks' `modelName` and the
transcript's `model`.

Saw:

| Arguments | Ran | Screen notice |
|---|---|---|
| `--model gemini-4-neon-high --effort low` | `gemini-4-neon-high` | `--model gemini-4-neon-high conflicts with --effort=low. Using "Gemini 4 Neon (High)" instead.` |
| `--model gemini-3.8-flash --effort medium` | `gemini-3.8-flash-medium` | none (`model alias "gemini-3.8-flash" resolved to "gemini-3.8-flash-medium"`) |
| `--model gemini-4-neon --effort low` | `gemini-4-neon-low` | none |
| `--model gemini-3.8-flash --effort xhigh` | `gemini-4-neon-high` | `gemini-3.8-flash has no "xhigh" effort (available: low, medium, high). Using "Gemini 4 Neon (High)" instead.` |
| `--model "Gemini 3.8 Flash (Low)"` | `gemini-3.8-flash-low` | none |
| `--model gemini-9-bogus` | `gemini-4-neon-high` | `model gemini-9-bogus is not recognized as a known model or custom model in settings. Using "Gemini 4 Neon (High)"` |

The footer shows the result (`auto · Gemini 3.8 Flash · medium`). Every start logs `failed to apply model override`
once before sign-in completes; the second resolve is the one that counts.

Decides (1.3): `--effort` selects a variant only with the base name (the slug minus its effort word). So the kind
either passes the slug alone, or strips the effort word and passes `--model <base> --effort <effort>`; never a slug
with `--effort`. A wrong effort or model does not fail the start: it silently runs Gemini 4 Neon (High), so the kind
(or `obor config validate`, with the manifest's efforts per model) has to refuse it before the start.

## P6: the first prompt and slash commands

Question: `-i "<text>"` in a fresh start and with `--conversation`; `-i "/<skill> handoff"`; how a plugin's skill is
named as a slash command.

Ran: `agy … --conversation <id> -i 'Reply with exactly: RESUMED-I'`; `agy … -i '/probe-wsskill handoff'`; typed
prompts `/hidplug:probe-hidplug handoff` and `/probe-hidplug lead`.

Saw:

- The resumed conversation got the prompt as its next user message and answered `RESUMED-I`; no SessionStart; a
  system notice `[Notice] All your subagents and background tasks have been stopped due to server restart. …` came
  with it.
- `-i '/probe-wsskill handoff'`: the user message carried the skill, `/probe-wsskill is a [Slash Command]: <SKILL>The
  user has explicitly invoked the (probe-wsskill) skill. … Reply with exactly: SKILL probe-wsskill LOADED, then the
  arguments you were given.</SKILL>`; reply `SKILL probe-wsskill LOADED handoff`. `lastUserInput` was
  `/probe-wsskill handoff`.
- `/hidplug:probe-hidplug handoff` → `SKILL probe-hidplug LOADED handoff`. Typing `/probe-hidplug lead` and Enter
  submitted `/hidplug:probe-hidplug lead`: the completion popup's Enter takes the highlighted entry, so a typed `/…`
  line can turn into a different command.
- `/skills` listed `hidplug:probe-hidplug` with its plugin prefix and `probe-wsplug2` (also an in-repo plugin with a
  real skills dir) without one; the difference between the two was not found.

Decides: a first prompt and a resumed wake can go in the argv (`-i`), a skill slash command included. A plugin's
skill is invoked by its prefixed name; the kind should type the full `/<plugin>:<skill>` so the popup has nothing to
complete.

## P7: agentapi send-message to the TUI

Question: from a second shell, with the address and token a hook saw, `agentapi send-message <conversationId>
"<text>"`: does an idle session start a turn, does a busy one read it, is a half-typed draft kept.

Ran: the agent ran `env | sort > agent-env.txt` (hooks do not get the address and token, P3); from the worker's shell
with `ANTIGRAVITY_LS_ADDRESS` and `ANTIGRAVITY_CSRF_TOKEN` from that file, agy's own
`~/.gemini/antigravity-cli/bin/agentapi` (a wrapper for `agy agentapi`): once to the idle session, once during a turn
whose `sleep 25` had gone to the background with `half typed draft` typed into the input (`p7busy.sh`), once during a
foreground `sleep 7`.

Saw:

```
$ agentapi send-message a340acfb-… "Reply with exactly: AGENTAPI-IDLE-1"
{"response": {"sendMessage": {"recipientId": "a340acfb-…", "content": "Reply with exactly: AGENTAPI-IDLE-1"}}}
```

- Idle: a turn started at once and the screen showed the reply `AGENTAPI-IDLE-1` (no `> …` line for the message).
  The transcript holds it as a system message, not a user input: `[Message] timestamp=2026-11-22T11:50:38Z
  sender=system priority=MESSAGE_PRIORITY_HIGH content=Reply with exactly: AGENTAPI-IDLE-1`. Hooks: PreInvocation
  `invocationNum: 0` with the previous prompt as `lastUserInput`, PostInvocation, Stop.
- Busy, background command: sent at 11:51:16, read at the next model call (11:51:21) in the same turn; reply
  `AGENTAPI-BUSY-2`; that turn's Stop had `fullyIdle: false`. When the `sleep 25` ended, its task sent its own message
  (`sender=<conversationId>/task-57`) and the model answered `SLEPT` in a new turn. `> half typed draft` stayed in the
  input through all of it.
- Busy, foreground command: sent 3 s into `sleep 7`; read after the command ended, at the next model call.
- The address is `localhost:<port>`; the LS listens on two loopback ports. Without the token: `rpc error: code =
  Unauthenticated desc = unauthenticated: missing CSRF token` (the other port: `connection reset by peer`); without the
  address: `ANTIGRAVITY_LS_ADDRESS is not set`.
- Where the token is: in the environment of every command the agent runs (with `ANTIGRAVITY_LS_ADDRESS`,
  `ANTIGRAVITY_PROJECT_ID`, `ANTIGRAVITY_TRAJECTORY_ID`, `ANTIGRAVITY_AGENTAPI_EXE`, …). Not in the hooks' environment,
  not in the MCP servers', not in the `agy` process's own environment (`ps -E`), and not in any file under
  `~/.gemini/antigravity-cli` or `~/.gemini/config` (searched for the live token's value).

Decides (lead lines, D4): the delivery itself passes: an idle agy session wakes on a message, a busy one reads it at
its next model call, the owner's draft is untouched. The channel as the advice wrote it does not exist: no hook or
MCP server sees the token, so obor cannot send without the agent first handing it over from one of its own commands
(which run sandboxed in auto mode, P9: a file in a temp dir or `~/.cache` is writable, a socket is not). Whether that
counts as P7 passing is the owner's call (owner 11-22: agy if P7 passes, else OpenCode). The message also arrives as
a system message, so the lead's PreInvocation sees no new user input for it (P3).

## P8: quit, input keys, trust screen, self-update, worktree project

Ran: `/exit`, Ctrl+D, Ctrl+C in an idle session; `keys.sh` (a draft, then a key); `/exit` with a background `sleep 90`
running; the first start in the untrusted `<repo>`.

Saw:

- `/exit` opens the popup `/exit  Exit the CLI`; one Enter selects and quits. On quitting agy prints `Resume with -c
  (or command below):` and `agy --conversation=<id>`. `/clear` behaves the same way (one Enter).
- Ctrl+D: `press ctrl+d again to exit`; the second quits. Ctrl+C with a draft: the draft stays, `press ctrl+c again to
  exit`; the second quits.
- Clear the input: Ctrl+U clears it; one Esc does not; Esc Esc clears it.
- `/exit` with `● [13:03:27] sleep 90 running` in the task bar: no dialog, agy quit at once, and no `sleep 90` process
  was left. The resumed conversation is told `All your subagents and background tasks have been stopped due to server
  restart`.
- Untrusted folder (outside `$HOME`, not in `trustedWorkspaces`):

```
Accessing workspace:

<repo>

Do you trust the contents of this project?

Antigravity CLI requires permission to read, edit, and execute files here.

> Yes, I trust this folder
  No, exit

  ↑/↓ Navigate · enter Confirm
```

  The default is "Yes, I trust this folder" (choosing it saves the folder to `settings.json`; not done). "No, exit"
  quits with no output. herdr reported the pane `blocked` while the dialog was up.
- Self-update: every start logs `auto_updater.go:319] Last check was less than 15 minutes ago, skipping update (fast
  path)`; `updater/update_status.json` says `Already on the latest version.` No update happened in the run.
- Linked worktree: `ANTIGRAVITY_PROJECT_ID=default-cli-project` in both `<repo>` and `<repo>-wt`;
  `cache/last_conversations.json` keys each directory to its own conversation. Every start creates a conversation (a
  `brain/<id>` dir) even with no prompt, and that empty one becomes the directory's last conversation.
- Not ready: the start screen shows `Introducing Gemini 4 Neon … esc to dismiss` above the input; prompts went through
  without dismissing it. herdr recognised the agent about 2 s after `? for shortcuts` was on screen; an
  `agent prompt` before that answered `agent_not_found`.

Decides: stop with `/exit` and one Enter (Ctrl+D twice as a fallback); clear a line with Ctrl+U. The quit does not
wait for background tasks and kills them. The trust dialog text matches `agents.TrustDialog`; obor answers none, so
the start file must trust the lane (P4). `-c` would resume an empty conversation after a start without a prompt;
obor resumes by id.

## P9: auto mode, a PreToolUse deny, the sandbox

Question: in `--mode auto` with a PreToolUse deny, is the deny final; which lane commands (go build, go test, git
reads) the sandbox stops.

Ran: `agy --mode auto` (no `--sandbox`) in `<repo>` with a small Go module (`go.mod`, `main.go`, `main_test.go`),
PreToolUse answering `ask`, `p9.sh` with `p9prompt*.txt`; the PreToolUse deny turns of P3 ran in auto mode too.

Saw (exit code and first output line, from the transcript):

| Command | Result |
|---|---|
| `go build ./...` | 0 (went to the background after `WaitMsBeforeAsync`, finished later) |
| `go test ./...` | 0, `ok  probe 0.277s` |
| `git status --short`, `git log --oneline -1` | 0, the right output after two stderr lines `git: error: couldn't create cache file '/var/folders/…/T/xcrun_db-…' (errno=Operation not permitted)` |
| `curl -sS … https://example.com` | 6, `Could not resolve host`, and agy's note `There were sandbox errors that may or may not be related to the failure. If you think the failure is because of running in the sandbox, you can run the command again with BypassSandbox set to true to request explicit user permission.` |
| `herdr --session probe-agy pane list` (a unix socket) | 1, `Error: Os { code: 1, kind: PermissionDenied, message: "Operation not permitted" }` |
| `touch` in `<kit>` (a temp dir) and in `~/.cache/…` | 0 |
| `touch ~/obor-agy-probe-T1458-touch.txt` | 1, `Operation not permitted` |
| `cat` of a file in `<kit>` and in `~/.cache/…` | 0 |
| `rm -rf` of a missing path in `<kit>` | 0, no prompt |

- None of these asked for permission. agy's own default mode here is auto (the footer says `auto` with no `--mode`);
  Shift+Tab cycles auto → plan → default (no label) → accept-edits. Default and accept-edits asked before every
  command (`Requesting permission for: echo probeF > f.txt … 1. Yes, run command … 3. Yes, and always allow for
  commands that start with 'echo probeF > f.txt' (Persist to settings.json) 4. No, cancel`).
- A deny is final in auto mode: five tools denied in a row (`run_command`, `write_file`, `edit_file`, `read_file`,
  `call_mcp_tool`); the model reported them and did not get around them (in the worktree it retried the same command
  once, denied again).
- MCP calls asked in auto mode (P4) unless a `permissions.allow` rule named them.

Decides (D1): `--mode auto` runs a worker's build, tests and git reads with no prompt. The sandbox refuses the network
(module downloads, `gh`), unix sockets and writes outside the workspace, temp dirs and caches. Every `obor` or `herdr`
command a lane runs in a shell that talks to a socket fails there; obor's MCP server is started by agy outside the
sandbox and is not affected. The fixed args also need obor's `permissions.allow` rule, or every obor MCP call asks.

## Not verified

- A Stop with `ERROR` and its `error` text, and what a usage limit looks like (nothing here forces them).
- Whether auto mode approves edits in an `--add-dir` directory (P1's way to load the kit plugin) and whether the
  sandbox lets commands write there.
- Whether a plugin's MCP server prefix is the directory name or `plugin.json`'s `name` (they were equal), and why one
  in-repo plugin's skill showed no prefix.
- `-c` / `--continue`, `--sandbox` (as opposed to auto mode's own sandbox), `--dangerously-skip-permissions`.
- A self-update at start (none was due).

## Appendix: the probe scripts

They live in one kit dir (`KIT`); each derives its paths from its own location or from the variables named in its
header. Re-probe: copy them to `<kit>/bin`, run `setup.sh` (and `setup2.sh`, `setup3.sh` for P1), start a headless
`herdr --session <name> server` under `env -i`, create a workspace at `<kit>/repo`, start agy in its pane with
`herdr pane run`, and drive it with the helpers. `set-trust.json` is `{"trustedWorkspaces": ["<kit>/repo",
"<kit>/repo-wt"]}`; `set-perm.json` adds `"permissions": {"allow": ["mcp(probe-ws/echo)", "mcp(probe-wsplug/*)"]}`.
A PreToolUse control file of `{"decision": "ask"}` (`ctrl/all-PreToolUse.reply`) keeps tools running; without one
the logging hook's `{}` denies every tool (P3).

### `hook.py`

```python
#!/usr/bin/env python3
"""agy probe hook: logs stdin, env, cwd for one event, answers from a control file or {}.

usage (hooks.json command): python3 <kit>/bin/hook.py <tag>
log:  <kit>/log/hook-<ns>-<tag>-<event>.{json,env,meta}
ctrl: <kit>/ctrl/<tag>-<event>.{reply,reply1,exit,sleep} (falls back to all-<event>.*); reply1 is used once
"""
import json
import os
import sys
import time

KIT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
tag = sys.argv[1] if len(sys.argv) > 1 else "x"
raw = sys.stdin.read()
try:
    ev = json.loads(raw).get("hookEventName", "unknown")
except Exception:
    ev = "unparsed"
ns = time.time_ns()
base = os.path.join(KIT, "log", "hook-%d-%s-%s" % (ns, tag, ev))
with open(base + ".json", "w") as f:
    f.write(raw)
with open(base + ".env", "w") as f:
    for k in sorted(os.environ):
        f.write("%s=%s\n" % (k, os.environ[k]))
with open(base + ".meta", "w") as f:
    f.write("cwd=%s\npid=%d\nppid=%d\nargv=%r\n" % (os.getcwd(), os.getpid(), os.getppid(), sys.argv))


def ctrl(ext):
    for name in ("%s-%s" % (tag, ev), "all-%s" % ev):
        p = os.path.join(KIT, "ctrl", name + "." + ext)
        if os.path.exists(p):
            with open(p) as f:
                return f.read()
    return None


s = ctrl("sleep")
if s:
    time.sleep(float(s))
r = None
for name in ("%s-%s" % (tag, ev), "all-%s" % ev):  # <name>.reply1: used once, then removed
    p = os.path.join(KIT, "ctrl", name + ".reply1")
    if os.path.exists(p):
        with open(p) as f:
            r = f.read()
        os.remove(p)
        break
if r is None:
    r = ctrl("reply")
sys.stdout.write(r if r is not None else "{}\n")
sys.stdout.flush()
e = ctrl("exit")
sys.exit(int(e) if e else 0)
```

### `mcp.py`

```python
#!/usr/bin/env python3
"""agy probe stdio MCP server: logs env, cwd, argv at start; one tool `echo` that returns
the identity variables it sees.

usage (mcp_config.json): command python3, args [<kit>/bin/mcp.py, <tag>]
log: <kit>/log/mcp-<ns>-<tag>.{env,meta,rpc}
"""
import json
import os
import sys
import time

KIT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
tag = sys.argv[1] if len(sys.argv) > 1 else "x"
ns = time.time_ns()
base = os.path.join(KIT, "log", "mcp-%d-%s" % (ns, tag))
with open(base + ".env", "w") as f:
    for k in sorted(os.environ):
        f.write("%s=%s\n" % (k, os.environ[k]))
with open(base + ".meta", "w") as f:
    f.write("cwd=%s\npid=%d\nppid=%d\nargv=%r\n" % (os.getcwd(), os.getpid(), os.getppid(), sys.argv))
rpc = open(base + ".rpc", "a")

IDS = ("OBOR_ROLE", "OBOR_LANE", "OBOR_PANE", "HERDR_PANE_ID", "HERDR_SOCKET_PATH", "PLUGIN_ROOT", "PWD")


def send(obj):
    sys.stdout.write(json.dumps(obj) + "\n")
    sys.stdout.flush()


for line in sys.stdin:
    line = line.strip()
    if not line:
        continue
    rpc.write(line + "\n")
    rpc.flush()
    try:
        msg = json.loads(line)
    except Exception:
        continue
    mid = msg.get("id")
    method = msg.get("method")
    if mid is None:
        continue
    if method == "initialize":
        pv = msg.get("params", {}).get("protocolVersion", "2025-03-26")
        send({"jsonrpc": "2.0", "id": mid, "result": {
            "protocolVersion": pv,
            "capabilities": {"tools": {}},
            "serverInfo": {"name": "probe-" + tag, "version": "0.0.1"}}})
    elif method == "tools/list":
        send({"jsonrpc": "2.0", "id": mid, "result": {"tools": [{
            "name": "echo",
            "description": "Probe tool: returns the identity environment the MCP server sees.",
            "inputSchema": {"type": "object", "properties": {"text": {"type": "string"}}}}]}})
    elif method == "tools/call":
        seen = {k: os.environ.get(k) for k in IDS}
        seen["cwd"] = os.getcwd()
        seen["tag"] = tag
        send({"jsonrpc": "2.0", "id": mid, "result": {
            "content": [{"type": "text", "text": json.dumps(seen)}]}})
    elif method == "ping":
        send({"jsonrpc": "2.0", "id": mid, "result": {}})
    else:
        send({"jsonrpc": "2.0", "id": mid, "error": {"code": -32601, "message": "no method " + str(method)}})
```

### `setup.sh`

```sh
#!/bin/sh
# Builds the agy probe fixture.
#   KIT   the probe kit dir (holds bin/hook.py, bin/mcp.py); log/ and ctrl/ are made here
#   REPO  the scratch repository to create (outside $HOME: untrusted by default)
#   HPLUG a dir under $HOME for the `~/` plugins.json entry (removed at cleanup)
set -eu
KIT=${KIT:?}; REPO=${REPO:?}; HPLUG=${HPLUG:?}
mkdir -p "$KIT/log" "$KIT/ctrl"
H="python3 $KIT/bin/hook.py"
M="$KIT/bin/mcp.py"

hooks() { # $1 tag: one named hook, a handler per event
  cat <<EOF
{
  "probe-$1": {
    "SessionStart":   [{"type": "command", "command": "$H $1"}],
    "PreInvocation":  [{"type": "command", "command": "$H $1"}],
    "PostInvocation": [{"type": "command", "command": "$H $1"}],
    "Stop":           [{"type": "command", "command": "$H $1"}],
    "PreToolUse":     [{"matcher": "*", "hooks": [{"type": "command", "command": "$H $1"}]}],
    "PostToolUse":    [{"matcher": "*", "hooks": [{"type": "command", "command": "$H $1"}]}]
  }
}
EOF
}
mcp() { # $1 tag
  printf '{"mcpServers": {"probe-%s": {"command": "python3", "args": ["%s", "%s"], "env": {"PROBE_SET": "%s"}}}}\n' "$1" "$M" "$1" "$1"
}
skill() { # $1 dir $2 name
  mkdir -p "$1/$2"
  printf -- '---\nname: %s\ndescription: Probe skill %s. Use only when asked for %s.\n---\n\nReply with exactly: SKILL %s LOADED, then the arguments you were given.\n' "$2" "$2" "$2" "$2" > "$1/$2/SKILL.md"
}

# the repository: workspace hooks and MCP, plugins.json with a workspace-relative and a ~/ entry
mkdir -p "$REPO/.agents" "$REPO/plugsrc/wsplug"
cd "$REPO"
git init -q -b main
hooks ws > .agents/hooks.json
mcp ws > .agents/mcp_config.json
printf '{"entries": [{"path": "plugsrc"}, {"path": "%s"}]}\n' "~/${HPLUG#"$HOME"/}/plugins" > .agents/plugins.json
printf '{"name": "wsplug"}\n' > plugsrc/wsplug/plugin.json
hooks wsplug > plugsrc/wsplug/hooks.json
mcp wsplug > plugsrc/wsplug/mcp_config.json
skill "$KIT/skills-wsplug" probe-wsplug
ln -s "$KIT/skills-wsplug" plugsrc/wsplug/skills   # the plugin's skills dir is a symlink
printf 'hello\n' > README.md
git add -A && git -c user.name=probe -c user.email=probe@example.invalid commit -q -m fixture

# the ~/ plugin: real skills dir
mkdir -p "$HPLUG/plugins/homeplug"
printf '{"name": "homeplug"}\n' > "$HPLUG/plugins/homeplug/plugin.json"
hooks homeplug > "$HPLUG/plugins/homeplug/hooks.json"
mcp homeplug > "$HPLUG/plugins/homeplug/mcp_config.json"
skill "$HPLUG/plugins/homeplug/skills" probe-homeplug

# a linked worktree of the repository
git worktree add -q "$REPO-wt" -b wt
echo "fixture ready: $REPO $REPO-wt $HPLUG"
```

### `setup2.sh`

```sh
#!/bin/sh
# Second fixture round (P1): which plugins.json entries and skills dirs load.
#   adds, uncommitted, to REPO:
#     plugsrc/wsplug2      plugin with a real skills dir (control for the symlinked wsplug/skills)
#     .agents/skills/probe-wsskill  a workspace skill (control: workspace skills load at all)
#   and under HPLUG: plugins-abs/absplug, named by an absolute path in plugins.json
#   (plugins/homeplug stays named by its ~/ path)
set -eu
KIT=${KIT:?}; REPO=${REPO:?}; HPLUG=${HPLUG:?}
skill() {
  mkdir -p "$1/$2"
  printf -- '---\nname: %s\ndescription: Probe skill %s. Use only when asked for %s.\n---\n\nReply with exactly: SKILL %s LOADED, then the arguments you were given.\n' "$2" "$2" "$2" "$2" > "$1/$2/SKILL.md"
}
cd "$REPO"
mkdir -p plugsrc/wsplug2
printf '{"name": "wsplug2"}\n' > plugsrc/wsplug2/plugin.json
skill plugsrc/wsplug2/skills probe-wsplug2
skill .agents/skills probe-wsskill
mkdir -p "$HPLUG/plugins-abs/absplug"
printf '{"name": "absplug"}\n' > "$HPLUG/plugins-abs/absplug/plugin.json"
skill "$HPLUG/plugins-abs/absplug/skills" probe-absplug
printf '{"mcpServers": {"probe-absplug": {"command": "python3", "args": ["%s", "absplug"]}}}\n' "$KIT/bin/mcp.py" > "$HPLUG/plugins-abs/absplug/mcp_config.json"
printf '{"entries": [{"path": "plugsrc"}, {"path": "~/%s/plugins"}, {"path": "%s/plugins-abs"}]}\n' "${HPLUG#"$HOME"/}" "$HPLUG" > .agents/plugins.json
cat .agents/plugins.json
```

### `setup3.sh`

```sh
#!/bin/sh
# Third fixture round (P1): why the plugins outside the repository did not load.
#   extplug   absolute path outside the repo and outside $HOME, no hidden path component
#   hidplug   workspace-relative path with a hidden component (.hid/plugins)
#   linkplug  plugsrc/linkplug is a symlink to a plugin dir outside the repo
set -eu
KIT=${KIT:?}; REPO=${REPO:?}; HPLUG=${HPLUG:?}
skill() {
  mkdir -p "$1/$2"
  printf -- '---\nname: %s\ndescription: Probe skill %s. Use only when asked for %s.\n---\n\nReply with exactly: SKILL %s LOADED, then the arguments you were given.\n' "$2" "$2" "$2" "$2" > "$1/$2/SKILL.md"
}
plug() { # $1 dir $2 name
  mkdir -p "$1"
  printf '{"name": "%s"}\n' "$2" > "$1/plugin.json"
  skill "$1/skills" "probe-$2"
}
plug "$KIT/extplugins/extplug" extplug
plug "$REPO/.hid/plugins/hidplug" hidplug
plug "$KIT/linktarget/linkplug" linkplug
ln -sfn "$KIT/linktarget/linkplug" "$REPO/plugsrc/linkplug"
printf '{"entries": [{"path": "plugsrc"}, {"path": "~/%s/plugins"}, {"path": "%s/plugins-abs"}, {"path": "%s/extplugins"}, {"path": ".hid/plugins"}]}\n' \
  "${HPLUG#"$HOME"/}" "$HPLUG" "$KIT" > "$REPO/.agents/plugins.json"
cat "$REPO/.agents/plugins.json"
```

### `turn.sh`

```sh
#!/bin/sh
# One probe turn: prompt the agy pane, wait for it to settle, print the status and the screen's tail.
#   turn.sh <pane> <prompt> [timeout-ms]
# env: SESSION (herdr session, default probe-agy), LINES_TAIL (default 14)
S=${SESSION:-probe-agy}
herdr --session "$S" agent prompt "$1" "$2" --wait --timeout "${3:-120000}" 2>&1 |
  python3 -c 'import json,sys
raw=sys.stdin.read()
try:
    d=json.loads(raw); a=d.get("result",{}).get("agent",{})
    print("status:", a.get("agent_status"), "session:", (a.get("agent_session") or {}).get("value"), "error:", d.get("error"))
except Exception:
    print("raw:", raw[:400])'
herdr --session "$S" pane read "$1" --source visible 2>&1 | grep -v '^[[:space:]]*$' | tail -"${LINES_TAIL:-14}"
```

### `state.sh`

```sh
#!/bin/sh
# Prints the pane's screen tail, the conversation transcript's last steps and the last hook events.
#   state.sh <pane> <conversationId> [screen-lines] [steps] [hooks]
S=${SESSION:-probe-agy}
KIT=$(cd "$(dirname "$0")/.." && pwd)
herdr --session "$S" pane read "$1" --source visible 2>&1 | grep -v '^[[:space:]]*$' | tail -"${3:-10}"
T=$HOME/.gemini/antigravity-cli/brain/$2/.system_generated/logs/transcript_full.jsonl
echo "--- transcript"
tail -"${4:-6}" "$T" | python3 -c 'import json,sys
for l in sys.stdin:
    d=json.loads(l); print(d["step_index"], d["created_at"], d["source"], d["type"], d["status"], repr(d.get("content") or d.get("tool_calls") or d.get("error"))[:260])'
echo "--- hooks (newest first)"
for f in $(ls -t "$KIT"/log/hook-*.json | head -"${5:-8}"); do
  python3 -c 'import json,sys,os; d=json.load(open(sys.argv[1])); b=os.path.basename(sys.argv[1]).split("-"); print(b[1], b[2], d["hookEventName"], "inv=%s" % d.get("invocationNum"), d.get("terminationReason") or "", "idle=%s" % d.get("fullyIdle"), (d.get("toolCall") or {}).get("name") or "", repr(d.get("lastUserInput"))[:50])' "$f"
done
```

### `skills.sh`

```sh
#!/bin/sh
# Opens /skills in an idle agy pane, pages through the list, prints the probe skills and their source rows, closes it.
#   skills.sh <pane>
S=${SESSION:-probe-agy}
herdr --session "$S" pane send-text "$1" "/skills" >/dev/null
sleep 0.5
herdr --session "$S" pane send-keys "$1" enter >/dev/null
sleep 2
out=""
for i in 1 2 3; do
  out="$out
$(herdr --session "$S" pane read "$1" --source visible 2>&1)"
  for j in $(seq 1 19); do herdr --session "$S" pane send-keys "$1" down >/dev/null; done
  sleep 0.5
done
printf '%s\n' "$out" | grep -E "skills ·|probe-|^[0-9]+ skills" | sed 's/^[›> ]*//' | sort -u
herdr --session "$S" pane send-keys "$1" esc >/dev/null
```

### `trust.sh`

```sh
#!/bin/sh
# P4/P8: does a start in <dir> show the trust dialog? Answers "No, exit" if it does (nothing is saved).
#   trust.sh <pane> <dir> [agy args...]
S=${SESSION:-probe-agy}
P=$1; D=$2; shift 2
herdr --session "$S" pane run "$P" "clear; cd $D && agy --model gemini-3.8-flash-low $*" >/dev/null
herdr --session "$S" pane wait-output "$P" --regex "for shortcuts|trust" --timeout 30000 >/dev/null 2>&1
sleep 1
scr=$(herdr --session "$S" pane read "$P" --source visible 2>&1)
if printf '%s' "$scr" | grep -q "Do you trust"; then
  echo "== $D [$*]: TRUST DIALOG"
  herdr --session "$S" pane send-keys "$P" down >/dev/null; sleep 0.3
  herdr --session "$S" pane send-keys "$P" enter >/dev/null; sleep 1
else
  echo "== $D [$*]: no dialog"
  herdr --session "$S" pane send-keys "$P" ctrl+d >/dev/null; sleep 0.3
  herdr --session "$S" pane send-keys "$P" ctrl+d >/dev/null; sleep 1
fi
herdr --session "$S" pane read "$P" --source visible 2>&1 | grep -v '^[[:space:]]*$' | tail -1
```

### `keys.sh`

```sh
#!/bin/sh
# P8: which keys clear a half-typed input in an idle agy pane.
#   keys.sh <pane> <key>...   each key: type a draft, press the key, print the input line(s) after it
S=${SESSION:-probe-agy}
P=$1; shift
inputline() { herdr --session "$S" pane read "$P" --source visible 2>&1 | grep -v '^[[:space:]]*$' | tail -4 | sed 's/^/    /'; }
for k in "$@"; do
  herdr --session "$S" pane send-text "$P" "draft words here" >/dev/null
  sleep 0.5
  herdr --session "$S" pane send-keys "$P" "$k" >/dev/null
  sleep 1
  echo "== after $k:"
  inputline
  # reset: clear whatever is left
  herdr --session "$S" pane send-keys "$P" ctrl+u >/dev/null
  sleep 0.5
done
```

### `p5.sh`

```sh
#!/bin/sh
# P5: start agy with the given model/effort args, report what runs.
#   p5.sh <pane> <repo> <settings-file> <agy args...>
# Exits a running agy in the pane first (/exit). Prints the footer, cli.log's model lines, then one
# turn's hook modelName and the transcript's model field.
S=${SESSION:-probe-agy}
P=$1; R=$2; SET=$3; shift 3
KIT=$(cd "$(dirname "$0")/.." && pwd)
if herdr --session "$S" pane get "$P" 2>/dev/null | grep -q '"agent":"agy"'; then
  herdr --session "$S" pane send-text "$P" "/exit" >/dev/null; sleep 0.5
  herdr --session "$S" pane send-keys "$P" enter >/dev/null
  herdr --session "$S" pane wait-output "$P" --match "Resume with" --timeout 10000 >/dev/null 2>&1
fi
herdr --session "$S" pane run "$P" "clear; cd $R && agy --settings $SET $*" >/dev/null
herdr --session "$S" pane wait-output "$P" --match "for shortcuts" --timeout 30000 >/dev/null 2>&1
sleep 2
echo "== agy $*"
herdr --session "$S" pane read "$P" --source visible 2>&1 | grep -E "Gemini|Claude|GPT|·" | grep -v "^ *┃" | sed 's/^ *//'
grep -E "model_resolver|failed to apply model|effort|Effort" "$HOME/.gemini/antigravity-cli/cli.log" | cut -c1-220 | sed 's/^/log: /'
herdr --session "$S" agent prompt "$P" "Reply with exactly: OK" --wait --timeout 60000 >/dev/null 2>&1
f=$(ls -t "$KIT"/log/hook-*-ws-Stop.json | head -1)
python3 - "$f" <<'EOF'
import json, sys
d = json.load(open(sys.argv[1]))
print("hook modelName:", d.get("modelName"))
t = d["transcriptPath"]
for l in open(t):
    j = json.loads(l)
    if j.get("type") == "PLANNER_RESPONSE":
        last = j
print("transcript model:", last.get("model"))
EOF
herdr --session "$S" pane read "$P" --source visible 2>&1 | tail -1 | sed 's/^ *//; s/^/footer after turn: /'
```

### `p7busy.sh`

```sh
#!/bin/sh
# P7 busy case: a long turn, a half-typed draft in the input, then agentapi send-message.
#   p7busy.sh <pane> <conversationId> <env-file with ANTIGRAVITY_LS_ADDRESS/CSRF_TOKEN lines>
S=${SESSION:-probe-agy}
P=$1; CID=$2
eval "$(grep -E '^ANTIGRAVITY_(LS_ADDRESS|CSRF_TOKEN)=' "$3" | sed 's/^/export /')"
snap() { echo "--- $(date +%T) $1"; herdr --session "$S" pane read "$P" --source visible 2>&1 | grep -v '^[[:space:]]*$' | tail -"${2:-8}"; }
herdr --session "$S" agent prompt "$P" "Run exactly this shell command and nothing else: sleep 25  Then reply with exactly: SLEPT" >/dev/null 2>&1
sleep 6
snap "turn running"
herdr --session "$S" pane send-text "$P" "half typed draft" >/dev/null 2>&1
sleep 1
snap "draft typed" 4
~/.gemini/antigravity-cli/bin/agentapi send-message "$CID" "Reply with exactly: AGENTAPI-BUSY-2"
echo "agentapi exit=$?"
sleep 4
snap "after send-message (turn still running)" 10
herdr --session "$S" agent wait "$P" --timeout 90000 >/dev/null 2>&1
sleep 8
snap "settled" 16
```

### `p9.sh`

```sh
#!/bin/sh
# P9: one turn running lane-like commands; every permission prompt is recorded, then approved once
# (option 1, "Yes, run command": nothing persisted) so the turn goes on.
#   p9.sh <pane> <prompt-file|-> <outside-touch-path> <outside-read-path>   (- : only wait on a turn already sent)
S=${SESSION:-probe-agy}
P=$1
prompt=$(sed -e "s#OUTSIDE_TOUCH_TARGET#$3#" -e "s#OUTSIDE_READ_TARGET#$4#" "$2")
[ "$2" = "-" ] || herdr --session "$S" agent prompt "$P" "$prompt" >/dev/null 2>&1
sleep 5
n=0
while [ $n -lt 12 ]; do
  out=$(herdr --session "$S" agent wait "$P" --timeout 120000 2>&1)
  st=$(printf '%s' "$out" | grep -o '"agent_status":"[a-z]*"' | head -1)
  if [ "$st" = '"agent_status":"blocked"' ]; then
    n=$((n + 1))
    echo "--- permission prompt $n"
    herdr --session "$S" pane read "$P" --source visible 2>&1 | grep -v '^[[:space:]]*$' | grep -A8 -E "Requesting permission|Run this command|permission" | head -12
    herdr --session "$S" pane send-keys "$P" enter >/dev/null
    sleep 2
  else
    break
  fi
done
echo "--- settled ($st), prompts approved: $n"
herdr --session "$S" pane read "$P" --source visible 2>&1 | grep -v '^[[:space:]]*$' | tail -"${LINES_TAIL:-16}"
```

### `p9prompt.txt`

```text
Run each of these shell commands as its own separate tool call, one after another, in this order, without changing them. After all of them, reply with one line per command: the command, then its exit code and its first output line. Commands: (1) go build ./...  (2) go test ./...  (3) git status --short  (4) git log --oneline -1  (5) curl -sS -o /dev/null -w %{http_code} https://example.com  (6) touch OUTSIDE_TOUCH_TARGET  (7) cat OUTSIDE_READ_TARGET
```

`p9prompt2.txt` to `p9prompt4.txt` are one- and two-command variants of the same text (`touch OUTSIDE_TOUCH_TARGET`,
`cat OUTSIDE_READ_TARGET`, and a bare `OUTSIDE_TOUCH_TARGET` that `p9.sh` fills with a whole command line).
