# OpenCode lead probes: resume, hook answers, an in-plugin inbox, context size (T-1465)

`docs/research/agent-kinds-advice.md` §4.3 lists what an OpenCode lead lacks: no resume (`kinds/opencode/kind/kind.go`
`start`), hook answers dropped by `opencode/obor.js`, no transcript and so no handoff reminder, no inbox. This run
started OpenCode live and records what it did, with the command or screen that showed it, so the tasks below can be
written against observed behaviour. Every gap is closable with what OpenCode 2.2.0 offers a plugin.

## Tasks it implies

| # | Task | Files | Size |
|---|---|---|---|
| 1 | **Resume.** `caps.resume`; a start with a session id runs `--auto --standalone --session <id>`, and `--prompt <p>` when the start has a prompt (O1: both reopen the session with its history; the prompt runs on top of it). The session id is the one `obor.js` reports today. | `kinds/opencode/kind/kind.go` (`describe`, `start`), `kind_test.go`; `docs/design.md` "opencode lanes" (protected: it says obor never resumes an OpenCode session and sends `session_id: ""`, while `obor.js` sends the session's id) | small |
| 2 | **Act on hook answers** (`caps.hook_answers`). `user-prompt-submit` runs in the session hook `prompt` (it blocks admission, O2) and its `additionalContext` is appended to the prompt's text (stored, shown in the TUI under the typed text, seen by the model). `stop`'s `{"decision":"block","reason"}` at `session.execution.succeeded` becomes `ctx.session.prompt({sessionID, text: reason, metadata: {obor: …}})`, which starts a new turn; the stop that ends that turn carries `stop_hook_active: true`. obor's own prompts carry `metadata`, which reaches the prompt hook, so the hook can tell them apart. | `opencode/obor.js`, `opencode/obor.test.js`, `kinds/opencode/kind/kind.go` (cap), `kind_test.go`; `docs/design.md` "Lines to the lead" (protected: it says OpenCode's plugin drops the answers) | medium |
| 3 | **An inbox in the plugin** (`caps.inbox`, `inbox_record`, the `deliver` call). The plugin binds a unix socket per session and puts its path in the env of the `session-start` hook it spawns (`OBOR_OPENCODE_INBOX`, say), which writes the inbox record from it. `deliver` names that socket and the lines. A line read on the socket becomes `ctx.session.prompt({sessionID, text, metadata})` with the default delivery `steer`. Idle: a turn starts. Busy: the line waits for the running model step or tool, nothing is interrupted. The owner's draft stays (O3). | `opencode/obor.js`, `opencode/obor.test.js`, `kinds/opencode/kind/kind.go`, `kind_test.go`; `docs/design.md` (protected) | medium |
| 4 | **Context size.** The plugin appends one line per root-session step (`session.step.ended`) to a per-session file: `tokens` = `input + cache.read + cache.write` of that step, the sum obor reads from Claude's transcript (O4). It reports that file as `transcript_path` (today: the SQLite database). The manifest's `transcript` glob names it, and `turn what: context` reads its tail. This gives the handoff reminder its number. | `opencode/obor.js`, `opencode/obor.test.js`, `kinds/opencode/kind/kind.go` (`describe` `transcript`/`env`, `turn`), `kind_test.go` | small |
| — | The lead's start: none. T-1461's plain prompt (`read <kit>/skills/lead/SKILL.md and follow it`) works; OpenCode has no slash form for a skill; the lead's `obor` calls dial their unix socket with no prompt under `--auto` (O5). | — | — |

Order: 1 and 4 stand alone. 2 is needed even with 3: the handoff reminder and a worker's wrong-tab guard are
`user-prompt-submit` answers, whatever carries the lines. 3 makes OpenCode a lead whose lines are posted, as Claude's
are, rather than the lesser mode.

## Method

- Date: 2026-11-22. OpenCode v2.2.0 (`~/.opencode/bin/opencode`, no update during the run: `autoupdate: false`,
  `OPENCODE_DISABLE_AUTOUPDATE=1`), herdr 0.9.5, macOS.
- A throwaway herdr session `probe-oc-t1465` (headless `herdr --session probe-oc-t1465 server` under `env -i` with
  `HOME USER LOGNAME SHELL TERM LANG PATH`), one workspace, one pane, at a scratch git repository `<kit>/repo` (`git
  init`, no commit). OpenCode was started in the pane as obor starts it, `opencode --auto --standalone [args]`, with
  `OPENCODE_CONFIG=<kit>/opencode.json` and scratch `XDG_{CONFIG,DATA,STATE,CACHE}_HOME` (`oc.sh`). It was driven with
  `herdr pane send-text` / `send-keys` and read with `herdr pane read`. Never `opencode run`.
- The model: a fake OpenAI-compatible provider on 127.0.0.1 (`fakeprov.py`), declared in the scratch config. It writes
  every request body to a file and answers with the `MARK-*` tokens it finds, tagged with where each one was (`sys`
  system prompt, `u<i>` the i-th user message, `t<i>` a tool result). So each reply shows on screen what reached the
  model, and the request file shows it exactly. Usage is fixed per request: `prompt_tokens = 1000·n + 7` (n the
  request number since the provider started), `cached_tokens = 500`, `completion_tokens = 11`. A queue file scripts
  replies: a delay (streamed, one chunk a second), a tool call, a text.
- The probe plugin (`probeplug/index.js`, loaded through the config's `plugins`, as obor's is) logs the plugin
  context, every event and the session hooks' payloads, and listens on `<repo>/.probe/in.sock`. A line there drives
  `ctx.session.prompt` / `synthetic` / `instructions.entry.put`, queues text for the next `context` or `prompt` hook,
  or arms a prompt at the next turn end.
- In the quotes `<kit>` is the scratch dir and `<repo>` is `<kit>/repo`.
- Footprint: the owner's shared service (`opencode serve --service`) was never attached to and kept running throughout,
  same pid. `~/.config/opencode` and `~/.local/state/opencode` had no file newer than the probe kit after the run, and
  `~/.local/share/opencode/opencode.db` was last written before the first probe start. `opencode debug config` from a
  scratch env tries to start a *managed service* of its own on the fixed service port: it failed ("port 49374 … already
  in use by PID …") and left nothing running; probes must not run `opencode debug …`. At the end the TUI quit with
  `/exit`, the herdr session was stopped and deleted, and the fake provider was killed by pid. No probe process was
  left.
- **One unplanned outside call (O5).** A start with `--prompt "/obor-lead handoff"` (a config command) ran on
  OpenCode's hosted free model `opencode/tidepool-alpha-free`, not the configured fake one: two agent steps went to
  OpenCode's service. They carried the system prompt, including the names and descriptions of the skills in
  `~/.claude/skills` (an untrimmed start reads them), the scratch paths (which hold the local account name), and the
  fake skill file's text. Escape came too late to stop it. From then on the config denied every provider but the fake
  (`policies`, appendix). A probe config needs that policy from the start.

## O1: resume

Question: does `opencode --auto --standalone --session <id>` (the id obor's plugin reports) reopen that session with its
history; with `--prompt` too; after the TUI quit with `/exit`.

Ran: a session with twelve user turns (O2, O3), quit with `/exit` and a second Enter (the kind's stop). Then
`oc.sh --session ses_dfca00914ffeH0xO8a6dnbbTfq`, one typed turn (`turn7 MARK-U7`), `/exit` again, then
`oc.sh --session ses_dfca00914ffeH0xO8a6dnbbTfq --prompt "MARK-RESUMEPROMPT resumed with a prompt"`.

Saw:
- `/exit` prints `Continue  opencode -s ses_dfca00914ffeH0xO8a6dnbbTfq`. That id is `data.sessionID` of every session
  event the plugin got, which is the id `obor.js` reports as `session_id`.
- `--session <id>` opens the TUI on the session with its whole history on screen: the earlier turns, tool calls and
  replies. The footer shows the same context figure as before the quit (`6.0K (3%)`).
- The next turn's request held the whole history: `FAKE-7 saw: MARK-U2@u1 … MARK-IDLE2@u12 MARK-U7@u13`.
- `--session <id> --prompt <text>` runs the prompt as a new turn of that session, at once, with the history:
  `FAKE-8 saw: … MARK-U7@u13 MARK-PRM3@u13 MARK-RESUMEPROMPT@u14`.
- In a resumed TUI no event names the session until its first turn (the plugin's current session was `""` until then):
  `obor.js`'s session-start runs at that turn's first event or `context` hook. That is its rule for any session made
  before setup, and it sends `source: "startup"`, not `resume`.

Decides: OpenCode can resume (task 1). The kind's `start` drops the prompt when it resumes (`in.Prompt != "" &&
in.Resume == ""`). With `--session` the prompt can go along, as `obor restart <lead> --handoff` needs.

## O2: hook answers

Question: which plugin hooks can add context to the next model call at a user prompt, and which can continue a session
at a turn's end; does a line added that way show to the model and in the TUI. Decides the `hook_answers` cap.

Ran, with the probe plugin: text queued for the session hook `context` (pushed onto `system`), for the session hook
`prompt` (appended to `prompt.text`), `ctx.session.instructions.entry.put`, `ctx.session.synthetic`, and a prompt armed
for the next `session.execution.succeeded`. Typed turns, one of them with a tool call, so a turn made two model calls.
`prmdelay 3000` made the prompt hook take 3 s.

Saw:

| Way | The model | The TUI | Kept |
|---|---|---|---|
| session hook `context`, `c.system.push({type: "text", text})` | yes, in the system prompt (`MARK-CTX1@sys`) | nothing | **that model call only**: the hook runs before every model call (three calls, three runs), and text added on the first call of a turn was gone from the second (`req-006` had `MARK-CTX3`, `req-007` did not) |
| session hook `prompt`, `p.prompt.text += …` | yes, in the user message (`MARK-U2@u1 MARK-PRM2@u1`) | yes, under the typed text, as part of the user's message | stored in the user message, sent again on every later call |
| `instructions.entry.put({sessionID, key, value})` | yes, as a user message before the next prompt: `<system-update>\n&lt;context key="obor-lines"&gt;\nMARK-INSTR1 a lead line\n&lt;/context&gt;\n</system-update>` (the inner tags HTML-escaped) | `◈ Instructions updated: api/obor-lines`, not the text | stored; a put starts no turn; **two puts to one key before the next step deliver only the last** (`MARK-INSTR3` reached the model, `MARK-INSTR2` never did) |
| `synthetic({sessionID, text, description})` | yes, a plain user message (`MARK-SYN1@u6`) | `◈ obor` (the description), not the text | stored; idle: starts a turn |
| `prompt({sessionID, text})` | yes, a user message | yes, as a user message | stored; idle: starts a turn |

- The `prompt` hook's payload: `{sessionID, messageID, prompt: {text, files, agents}, metadata, delivery}`. It runs once
  per admitted input: typed prompts, a `--prompt`, a command's expanded template, and the plugin's own
  `ctx.session.prompt` calls (a synthetic message does not pass through it). `metadata` given to `ctx.session.prompt`
  arrives in it (`"metadata":{"obor":"lead-line"}`).
- The `prompt` hook **blocks admission**: with a 3 s hook the prompt was enqueued 3 s after Enter (`hook.prompt` at
  +0.35 s, `session.inbox.enqueued` at +3.36 s), and the TUI showed neither the message nor a spinner meanwhile. A
  hook that takes as long as `obor hook` (well under 1 s) is unnoticed.
- The `context` hook's payload: `{sessionID, model, system[], messages[], options, agent, tools}`.
- Turn end: a prompt from the plugin at `session.execution.succeeded` started a new turn at once: `MARK-IDLE1 lead line
  at turn end` showed as a user message, `FAKE-9 … MARK-IDLE1@u5`. The turn had already ended: the event comes after
  the fact, so a terminal watcher may see the pane idle for a moment.
- `session.execution.started` comes after admission (`inbox.enqueued` → `execution.started`). An answer to
  `user-prompt-submit` run there, as `obor.js` runs it today, is too late for the prompt's own message.

Decides: `hook_answers` can be true for OpenCode, with `obor.js` changed (task 2):
- `user-prompt-submit` in the `prompt` hook, with its `additionalContext` appended to `prompt.text`. That is the one way
  that is stored, reaches the model on every later call, and lets the owner see what was added. The `context` hook is
  wrong for lines: they would vanish after the turn's first model call. Instructions entries are a second choice: a key
  per answer (one key collapses), and the owner sees only a notice.
- `stop`'s block reason through `ctx.session.prompt` at `session.execution.succeeded`, the plugin keeping
  `stop_hook_active` for the stop that ends the turn it started. `obor.js` sends `false` today.
- The turn begin that `user-prompt-submit` reports to serve stays at `session.execution.started`. A prompt admitted
  while busy starts no execution (O3), so the prompt hook is not a turn begin.

## O3: an inbox in the plugin

Question: a plugin that listens on a unix socket and, on a line, prompts the session (SDK client from the plugin
context). Idle: does a turn start? Busy: is the line queued or does it interrupt? Is the owner's half-typed draft kept?

Ran: the probe plugin's socket (`<repo>/.probe/in.sock`, bound with a relative path: the absolute one is over macOS's
104-byte `sun_path`) and `send.py <repo> "<cmd> <text>"`.
- Idle, a draft in the composer: `half typed owner draft` typed, no Enter, then `prompt MARK-IDLE2 …`.
- Busy, streaming: a reply streamed for 12 s; at 3 s `owner draft words` typed (no Enter), then `prompt MARK-BUSY1 …`.
- Busy, in a tool: a tool call `sleep 8; echo slept`; at 3 s `promptq MARK-QUEUE1 …` (delivery `queue`), then
  `prompt MARK-STEER2 …` (the default delivery, `steer`).

Saw:
- `ctx.session.prompt` answers at once (2–4 ms) with the admitted item `{id, type: "user", payload: {text}, delivery:
  "steer"}`.
- Idle: a turn started (`execution.started` 10 ms after the line) and the line showed as a user message. The draft
  stayed in the composer, untouched.
- Busy, streaming: no interrupt. The stream ran its full 12 s and no request was cut. The TUI showed the line at once
  as a pending user message below the working reply. At the step's end the inbox delivered it and a second model call
  of **the same execution** took it (`step.ended stop` → `inbox.delivered` → `step.started`, one
  `execution.succeeded` at the end; `FAKE-2 … MARK-BUSY1@u8`). The draft stayed.
- Busy, in a tool: both lines waited for the tool (8 s). The TUI showed `1 queued · MARK-QUEUE1 queued line` for the
  queued one. After the tool step, the `steer` line went into the next model call together with the tool result
  (`FAKE-4 … MARK-STEER2@u10`). The `queue` line came after that step ended with `stop`, as one more step, still in
  the same execution (`FAKE-5 … MARK-QUEUE1@u11`).
- `ctx.session.interrupt` exists in the plugin context; not used.

Decides: an inbox like Claude's (`docs/design.md` "Lines to the lead") works (task 3). An idle lead starts a turn, a
busy one reads the line at its next model call, nothing is typed, and the owner's draft stays. Today's `Delivery` fits a
plugin kind: the manifest's `inbox_record` names variables of the hook's env (`internal/agents/plugin`), and `obor.js`
sets the env of the `obor hook session-start` it spawns, so the plugin's socket path can reach the record that way.
Where the socket lives must fit in 104 bytes (a project path may not: a short dir, or a relative bind).

## O4: context size

Question: the token counts per message in the plugin's events and in `opencode.db`; which sum is the context window in
use; can the plugin write it where the kind's `turn what: context` reads it.

Ran: the turns above against fixed usage, then `sqlite3` read-only on `<kit>/xdg/data/opencode/opencode.db`, the
plugin's `context` command (`ctx.session.context`), and a writer in the probe plugin (`writeContext`).

Saw (one request: `prompt_tokens 8007`, `cached_tokens 500`, `completion_tokens 11`):
- `session.step.ended` `data.tokens`: `{"input":7507,"output":11,"reasoning":0,"cache":{"read":500,"write":0}}`.
  `input` is the uncached part (prompt − cached). The assistant message's row in `session_message` (`type
  'assistant'`, `data.tokens`) holds the same object.
- `session.usage.updated` `data.tokens` and `session_v2.tokens_{input,output,reasoning,cache_read,cache_write}` are the
  session's **running total** (`77605|165|0|7500|0` after 14 assistant steps), title calls included (the first
  `usage.updated` of a session counted only the title request). It is no context size.
- The TUI footer (`8.0K (4%)` with a 200 000 limit) is `input + cache.read + output` of the last step (8018).
- `ctx.session.context({sessionID})` returns the session's messages since the last compaction, each assistant one with
  its `tokens`.
- The probe plugin appended, per root-session `step.ended`, `{"type":"context","session":…,"tokens":input+cache.read+
  cache.write,"output":…,"at":…}` to `<repo>/.probe/ctx/<session>.jsonl`: `tokens` 10007 then 11007 for the two steps
  of one turn, each the request's `prompt_tokens`.

Decides: the context in use is the last root-session step's `input + cache.read + cache.write`, the same sum obor reads
from Claude's transcript (input, cache read, cache creation; no output). The plugin can write it where `turn` reads it
(task 4): a per-session file, one line per step, named by the manifest's `transcript` glob and passed as
`transcript_path`. That gives the handoff reminder (`project.ContextOf`) its number. There is no cache TTL in the
events (`ttl` "").

## O5: the lead's start

Question: does a plain first prompt naming `skills/lead/SKILL.md` work (T-1461's form); does OpenCode's own skill
loading offer a slash form; the lead's `obor` CLI calls (unix socket) under `--auto` with no prompt.

Ran: a fake `<kit>/fakekit/skills/lead/SKILL.md` outside the repository, and a project skill
`<repo>/.opencode/skills/probe-skill/SKILL.md`. A fresh start with `--prompt "read <kit>/fakekit/skills/lead/SKILL.md
and follow it"`, the fake model answering with a `read` of that path. `/probe` and `/lea` typed in the composer. A
config command `obor-lead` (`commands`, template `read <file> and follow it $ARGUMENTS`), started with `--prompt
"/obor-lead handoff"` and later typed. A shell tool call that dials two unix sockets: `herdr --session probe-oc-t1465
pane list` and `send.py` to the probe plugin's socket.

Saw:
- The plain prompt arrives as the session's first user message. The `read` of a file outside the repository ran with
  no permission event (`--auto`; OpenCode 2 allows external directories by default) and its text reached the model.
  The unplanned hosted-model turn (Method) is the one real-model data point: given that first prompt, it read the
  file and answered as the file said (`LEAD SKILL READ`).
- Skills are listed in the system prompt (`<skill><id>probe-skill</id>…`, together with `~/.claude/skills` on an
  untrimmed start) and loaded by the model through the `skill` tool (`{id}`). The `/` popup lists commands only:
  `/probe` gave `No matching commands`. OpenCode 2.2.0 has no slash form for a skill. The experimental HTTP API has
  `POST /api/experimental/session/{id}/skill` ("Activate a skill … by appending a skill message"); the plugin's
  `ctx.session` has no `skill`.
- A config command is a slash form: `/obor-lead handoff` expands to `read <file> and follow it handoff MARK-CMD`. It
  ran on the TUI's model when typed. Given as the start's `--prompt`, it ran on the server's default model
  (`opencode/tidepool-alpha-free`) and ignored the config's `model`. The `prompt` hook does run for a command (it got
  the expanded text), so obor's model switch would apply; that was not run.
- The socket dial from the shell tool ran with no prompt and no sandbox: `send.py` got its answer
  (`{"ok":true,…,"result":42}`). The `herdr` call failed inside herdr's own client (`local socket name length exceeds
  capacity of sun_path`), which says nothing of OpenCode; not followed up.

Decides: no task. T-1461's plain prompt is the right form for OpenCode. A command (config or `ctx.command.transform`)
could give `/obor-lead`, but it adds a moving part, and its `--prompt` start ran on another model. The lead's `obor`
calls need no permission rule under `--auto`.

## Not verified

- What a compaction does to the per-step count (the next step after it should carry the smaller prompt), and child
  sessions' `step.ended` (the writer filtered on `session.created` without `parentID`).
- Resume after a crash or a Ctrl+C rather than `/exit`; `--session` with an id of another directory.
- That obor's model switch applies to a `--prompt "/<command>"` start.
- A real model's reading of a line delivered by `prompt` or appended in the `prompt` hook (the fake model shows only
  that the text reached it).
- Whether the config's `settings` or `options` is what the v2 provider read (both were given).
- Why `herdr` failed from OpenCode's shell.

## Concerns

- `obor.js` maps a prompt's text at `session.execution.started`. A line admitted while busy (`steer`, O3) starts no
  execution, so its text is reported with the *next* turn's `user-prompt-submit`. Task 2 should fix that mapping too.
- Probing OpenCode with a fake provider needs `policies` denying every other provider. Without it, a path that skips
  the TUI's model pick (a `--prompt` command) reaches OpenCode's hosted free model. obor's own starts have the same
  exposure when no `--model` is set: worth a line in the kind's docs.
- The instructions entry's inner tags reach the model HTML-escaped (`&lt;context …&gt;`), which looks like an OpenCode
  bug. Another reason not to build lines on it.

## Appendix: the probe kit

Re-probe: put the files under one dir `<kit>` (`bin/`, `probeplug/`), make `log/`, `ctrl/`,
`xdg/{config,data,state,cache}` and `repo/` (`git init`). Start `python3 -I bin/fakeprov.py <kit>` in the background
and write `opencode.json` with its port. Start the herdr session (headless server under `env -i`, as in Method),
`herdr --session <name> workspace create --cwd <kit>/repo --label oc --no-focus`, write the root pane id to
`log/pane`, then `bin/oc.sh`. Drive with `type.sh`, `keys.sh`, `scr.sh` and `send.py`. Script replies with
`ctrl/queue.jsonl`, one JSON spec per agent request. Stop: `/exit` + Enter in the pane, `herdr --session <name> server
stop`, `herdr session delete <name>`, kill the provider by the pid in `log/fakeprov.pid`.

### `opencode.json`

`<port>` is the fake provider's (`log/port`). `commands` is only for O5.

```json
{
  "$schema": "https://opencode.ai/config.json",
  "model": "fake/m1",
  "small_model": "fake/m1",
  "providers": {
    "fake": {
      "name": "Fake",
      "package": "@opencode/ai/providers/openai-compatible",
      "settings": { "baseURL": "http://127.0.0.1:<port>/v1", "apiKey": "probe" },
      "options": { "baseURL": "http://127.0.0.1:<port>/v1", "apiKey": "probe" },
      "models": { "m1": { "name": "Fake m1", "limit": { "context": 200000, "output": 8192 } } }
    }
  },
  "plugins": ["file://<kit>/probeplug"],
  "autoupdate": false,
  "policies": [
    { "action": "provider.use", "resource": "*", "effect": "deny" },
    { "action": "provider.use", "resource": "fake", "effect": "allow" }
  ],
  "commands": {
    "obor-lead": { "template": "read <kit>/fakekit/skills/lead/SKILL.md and follow it $ARGUMENTS MARK-CMD", "description": "obor lead (probe)" }
  }
}
```

v1's `npm: "@ai-sdk/openai-compatible"` gives `Unsupported package for fake/m1:` in 2.2.0. The v2 field is `package`,
with the bundled name.

### `bin/env.sh`

```sh
# sourced by the probe scripts
K=<kit>
S=probe-oc-t1465
REPO=$K/repo
export PATH=/usr/bin:/bin:/usr/sbin:/sbin:$HOME/.local/bin:$HOME/.opencode/bin
```

### `bin/oc.sh`

```sh
#!/bin/sh
# oc.sh [opencode args…]: start opencode as obor starts it (--auto --standalone) in the probe pane, with the scratch
# config (OPENCODE_CONFIG) and XDG dirs; waits for the TUI.
. "$(dirname "$0")/env.sh"
P=$(cat $K/log/pane)
q=""; for a in "$@"; do q="$q '$(printf %s "$a" | sed "s/'/'\\\\''/g")'"; done
herdr --session "$S" pane run "$P" "clear; cd $REPO && env OPENCODE_CONFIG=$K/opencode.json XDG_CONFIG_HOME=$K/xdg/config XDG_DATA_HOME=$K/xdg/data XDG_STATE_HOME=$K/xdg/state XDG_CACHE_HOME=$K/xdg/cache OPENCODE_DISABLE_AUTOUPDATE=1 PROBE_MARK=pane opencode --auto --standalone$q" >/dev/null
herdr --session "$S" pane wait-output "$P" --regex "Build|ctrl\+p|esc interrupt|Ask anything" --timeout 30000 >/dev/null 2>&1
sleep 1
"$K/bin/scr.sh" 30
```

### `bin/scr.sh`, `bin/type.sh`, `bin/keys.sh`

```sh
#!/bin/sh
# scr.sh [lines]: the probe pane's visible screen, blank lines dropped
. "$(dirname "$0")/env.sh"
herdr --session "$S" pane read "$(cat $K/log/pane)" --source visible 2>&1 | grep -v '^[[:space:]]*$' | tail -"${1:-40}"
```

```sh
#!/bin/sh
# type.sh <text> [enter]: type into the probe pane (send-text), Enter when the second arg is "enter"
. "$(dirname "$0")/env.sh"
P=$(cat $K/log/pane)
herdr --session "$S" pane send-text "$P" "$1" >/dev/null
[ "$2" = enter ] && { sleep 0.3; herdr --session "$S" pane send-keys "$P" enter >/dev/null; }
exit 0
```

```sh
#!/bin/sh
# keys.sh <key>…: send keys to the probe pane
. "$(dirname "$0")/env.sh"
herdr --session "$S" pane send-keys "$(cat $K/log/pane)" "$@" >/dev/null
```

### `bin/send.py`

```python
#!/usr/bin/env python3
"""send.py <repo> <line>: one command line to the probe plugin's socket (<repo>/.probe/in.sock), print its reply."""
import os
import socket
import sys

os.chdir(os.path.join(sys.argv[1], ".probe"))
s = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
s.settimeout(60)
s.connect("in.sock")
s.sendall((sys.argv[2] + "\n").encode())
buf = b""
while not buf.endswith(b"\n"):
    b = s.recv(65536)
    if not b:
        break
    buf += b
print(buf.decode().strip())
```

### `bin/fakeprov.py`

```python
#!/usr/bin/env python3
"""Fake OpenAI-compatible chat provider for the OpenCode probes.

usage: python3 -I fakeprov.py <kit> [port]      (port written to <kit>/log/port)
Every request body: <kit>/log/req-NNN.json. A request with tools (an agent turn) pops the first line of
<kit>/ctrl/queue.jsonl as its reply spec: {"text": str, "delay": seconds (streamed, one chunk a second),
"tool": {"name": str, "args": {...}}, "usage": {"prompt": int, "cached": int, "completion": int}}.
No spec: the text is "FAKE-<n> saw: <markers>", every MARK-* token of the system and user/tool messages with where
it was (sys = system, u<i>/t<i> = the i-th user/tool message; assistant messages are skipped). A request without tools (a title) gets
"Probe title". Usage defaults: prompt = 1000*n + 7, cached = 500, completion = 11.
"""
import json
import os
import re
import sys
import threading
import time
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

KIT = sys.argv[1]
LOG = os.path.join(KIT, "log")
QUEUE = os.path.join(KIT, "ctrl", "queue.jsonl")
lock = threading.Lock()
count = [0]


def pop_spec():
    with lock:
        if not os.path.exists(QUEUE):
            return None
        with open(QUEUE) as f:
            lines = [l for l in f.read().splitlines() if l.strip()]
        if not lines:
            return None
        with open(QUEUE, "w") as f:
            f.write("".join(l + "\n" for l in lines[1:]))
        return json.loads(lines[0])


def text_of(content):
    if isinstance(content, str):
        return content
    if isinstance(content, list):
        return " ".join(p.get("text", "") for p in content if isinstance(p, dict))
    return ""


def markers(body):
    seen = []
    idx = {"user": 0, "assistant": 0, "tool": 0}
    for m in body.get("messages", []):
        role = m.get("role", "?")
        if role == "assistant":
            idx[role] += 1
            continue
        if role == "system":
            where = "sys"
        else:
            idx[role] = idx.get(role, 0) + 1
            where = "%s%d" % (role[0], idx[role])
        txt = text_of(m.get("content"))
        for tc in m.get("tool_calls") or []:
            txt += " " + json.dumps(tc)
        for mk in re.findall(r"MARK-[A-Za-z0-9-]+", txt):
            tag = "%s@%s" % (mk, where)
            if tag not in seen:
                seen.append(tag)
    return seen


class H(BaseHTTPRequestHandler):
    def log_message(self, *a):
        pass

    def chunk(self, obj):
        self.wfile.write(("data: " + json.dumps(obj) + "\n\n").encode())
        self.wfile.flush()

    def do_GET(self):
        self.send_response(200)
        self.send_header("Content-Type", "application/json")
        self.end_headers()
        self.wfile.write(json.dumps({"object": "list", "data": [{"id": "m1", "object": "model"}]}).encode())

    def do_POST(self):
        raw = self.rfile.read(int(self.headers.get("Content-Length", "0")))
        with lock:
            count[0] += 1
            n = count[0]
        with open(os.path.join(LOG, "req-%03d.json" % n), "wb") as f:
            f.write(raw)
        try:
            body = json.loads(raw)
        except Exception:
            body = {}
        agent = bool(body.get("tools"))
        spec = (pop_spec() if agent else None) or {}
        if not agent:
            spec = {"text": "Probe title"}
        text = spec.get("text")
        if text is None and not spec.get("tool"):
            text = "FAKE-%d saw: %s" % (n, " ".join(markers(body)) or "none")
        u = spec.get("usage") or {}
        prompt = u.get("prompt", 1000 * n + 7)
        cached = u.get("cached", 500)
        completion = u.get("completion", 11)
        with open(os.path.join(LOG, "req-%03d.reply" % n), "w") as f:
            f.write(json.dumps({"spec": spec, "text": text, "prompt": prompt, "cached": cached, "completion": completion,
                                "stream": body.get("stream"), "t": time.time()}))
        self.send_response(200)
        self.send_header("Content-Type", "text/event-stream")
        self.send_header("Cache-Control", "no-cache")
        self.end_headers()
        base = {"id": "chatcmpl-%d" % n, "object": "chat.completion.chunk", "created": int(time.time()), "model": "m1"}
        try:
            self.chunk(dict(base, choices=[{"index": 0, "delta": {"role": "assistant", "content": ""}, "finish_reason": None}]))
            delay = int(spec.get("delay", 0))
            for i in range(delay):
                time.sleep(1)
                self.chunk(dict(base, choices=[{"index": 0, "delta": {"content": "." if i else "working"}, "finish_reason": None}]))
            finish = "stop"
            if text:
                self.chunk(dict(base, choices=[{"index": 0, "delta": {"content": (" " if delay else "") + text}, "finish_reason": None}]))
            if spec.get("tool"):
                t = spec["tool"]
                self.chunk(dict(base, choices=[{"index": 0, "delta": {"tool_calls": [{"index": 0, "id": "call_%d" % n, "type": "function",
                    "function": {"name": t["name"], "arguments": json.dumps(t.get("args", {}))}}]}, "finish_reason": None}]))
                finish = "tool_calls"
            self.chunk(dict(base, choices=[{"index": 0, "delta": {}, "finish_reason": finish}]))
            self.chunk(dict(base, choices=[], usage={"prompt_tokens": prompt, "completion_tokens": completion,
                "total_tokens": prompt + completion, "prompt_tokens_details": {"cached_tokens": cached}}))
            self.wfile.write(b"data: [DONE]\n\n")
            self.wfile.flush()
        except (BrokenPipeError, ConnectionResetError):
            with open(os.path.join(LOG, "req-%03d.cut" % n), "w") as f:
                f.write("client closed at %f\n" % time.time())


srv = ThreadingHTTPServer(("127.0.0.1", int(sys.argv[2]) if len(sys.argv) > 2 else 0), H)
with open(os.path.join(LOG, "port"), "w") as f:
    f.write(str(srv.server_address[1]))
with open(os.path.join(LOG, "fakeprov.pid"), "w") as f:
    f.write(str(os.getpid()))
srv.serve_forever()
```

### `probeplug/package.json`

```json
{
  "name": "probe-opencode",
  "version": "0.0.0",
  "private": true,
  "type": "module",
  "main": "index.js",
  "exports": { ".": "./index.js" }
}
```

### `probeplug/index.js`

```js
// OpenCode probe plugin (T-1465): logs the plugin context, every event and the session hooks' payloads to
// <kit>/log/plug.jsonl, and listens on <location>/.probe/in.sock (a relative path: macOS caps socket paths at 104
// bytes) for one command per line, answering one JSON line:
//   prompt <text> | promptq <text> | promptnr <text>   ctx.session.prompt (delivery steer | queue | resume false)
//   synth <text>                                       ctx.session.synthetic (description "obor")
//   instr <key> <text>                                 ctx.session.instructions.entry.put
//   ctx <text> | prm <text>                            add <text> in the next context hook (system) | prompt hook
//   prmdelay <ms>                                      the prompt hook waits <ms> before it returns (0: off)
//   onidle <text> | onidlesynth <text>                 at the next session.execution.succeeded: prompt | synthetic
//   context                                            ctx.session.context of the current session
//   eval <js>                                          an async function body with ctx, cur, log in scope
// "The current session" is the last root session an event named.
import fs from "node:fs";
import net from "node:net";
import path from "node:path";
import { fileURLToPath } from "node:url";

const KIT = path.dirname(path.dirname(fs.realpathSync(fileURLToPath(import.meta.url))));
const LOG = path.join(KIT, "log", "plug.jsonl");
const t0 = Date.now();

function log(kind, data) {
  try {
    fs.appendFileSync(LOG, JSON.stringify({ t: Date.now(), dt: Date.now() - t0, pid: process.pid, kind, data }) + "\n");
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

async function setup(ctx) {
  const dir = ctx.location?.directory || process.cwd();
  log("setup", { dir, cwd: process.cwd(), ppid: process.ppid, argv: process.argv, app: ctx.app, ctx: shape(ctx, 2),
    env: Object.fromEntries(Object.entries(process.env).filter(([k]) => /^(OBOR|HERDR|OPENCODE|XDG|PROBE)/.test(k))) });
  let cur = "";
  const roots = new Set();
  const pending = { ctx: [], prm: [] };
  const onidle = [];

  const sessionHook = async (name, mut) => {
    try {
      await ctx.session.hook(name, async (p) => {
        log("hook." + name, { shape: shape(p, 1), sessionID: p?.sessionID, sample: JSON.stringify(p)?.slice(0, 1500) });
        if (mut) await mut(p);
      });
      log("hook.registered", name);
    } catch (e) {
      log("hook.refused", { name, err: String(e?.message ?? e) });
    }
  };
  await sessionHook("context", (c) => {
    if (pending.ctx.length && Array.isArray(c.system)) {
      for (const text of pending.ctx.splice(0)) c.system.push({ type: "text", text });
      log("ctx.injected", { system: c.system.length });
    }
  });
  await sessionHook("prompt", async (p) => {
    if (pending.delay) {
      const ms = pending.delay;
      await new Promise((r) => setTimeout(r, ms));
      log("prm.delayed", { ms });
    }
    if (pending.prm.length) {
      const add = pending.prm.splice(0).join("\n");
      const before = JSON.stringify(p).slice(0, 300);
      if (p.prompt && typeof p.prompt.text === "string") p.prompt.text += "\n" + add;
      log("prm.injected", { before, after: JSON.stringify(p).slice(0, 400) });
    }
  });
  for (const n of ["generate", "compaction", "title"]) await sessionHook(n);

  // writeContext appends a root session's step tokens to <location>/.probe/ctx/<session>.jsonl: the prompt side
  // (input + cache read + cache write, the sum obor reads from Claude's transcript) and the output.
  const writeContext = (id, tk, created) => {
    const file = path.join(dir, ".probe", "ctx", id + ".jsonl");
    fs.mkdirSync(path.dirname(file), { recursive: true });
    const tokens = (tk.input ?? 0) + (tk.cache?.read ?? 0) + (tk.cache?.write ?? 0);
    fs.appendFileSync(file, JSON.stringify({ type: "context", session: id, tokens, output: tk.output ?? 0, at: new Date(created || Date.now()).toISOString() }) + "\n");
  };

  const doIdle = async (id) => {
    for (const a of onidle.splice(0)) {
      try {
        const r = a.synth
          ? await ctx.session.synthetic({ sessionID: id, text: a.text, description: "obor" })
          : await ctx.session.prompt({ sessionID: id, text: a.text });
        log("onidle.done", { a, r });
      } catch (e) {
        log("onidle.err", { a, err: String(e?.message ?? e) });
      }
    }
  };

  if (typeof ctx.event?.subscribe === "function") {
    (async () => {
      for await (const e of ctx.event.subscribe({})) {
        const d = e?.data ?? {};
        if (e.type === "session.created" && !d.parentID && d.sessionID) roots.add(d.sessionID);
        if (d.sessionID && (roots.has(d.sessionID) || !cur)) cur = d.sessionID;
        if (!/\.delta$/.test(e.type)) log("event", e);
        if (e.type === "session.execution.succeeded" && onidle.length) doIdle(d.sessionID);
        if (e.type === "session.step.ended" && roots.has(d.sessionID) && d.tokens) writeContext(d.sessionID, d.tokens, e.created);
      }
    })().catch((err) => log("subscribe.err", String(err?.message ?? err)));
  }

  const cmds = {
    prompt: (t) => ctx.session.prompt({ sessionID: cur, text: t }),
    promptq: (t) => ctx.session.prompt({ sessionID: cur, text: t, delivery: "queue" }),
    promptnr: (t) => ctx.session.prompt({ sessionID: cur, text: t, resume: false }),
    synth: (t) => ctx.session.synthetic({ sessionID: cur, text: t, description: "obor" }),
    instr: (t) => {
      const [key, ...rest] = t.split(" ");
      return ctx.session.instructions.entry.put({ sessionID: cur, key, value: rest.join(" ") });
    },
    ctx: (t) => pending.ctx.push(t),
    prm: (t) => pending.prm.push(t),
    prmdelay: (t) => (pending.delay = Number(t) || 0),
    onidle: (t) => onidle.push({ text: t }),
    onidlesynth: (t) => onidle.push({ text: t, synth: true }),
    context: () => ctx.session.context({ sessionID: cur }),
    eval: (js) => new Function("ctx", "cur", "log", "return (async () => {" + js + "})()")(ctx, cur, log),
  };
  const sockDir = path.join(dir, ".probe");
  fs.mkdirSync(sockDir, { recursive: true });
  const rel = path.relative(process.cwd(), path.join(sockDir, "in.sock")) || "in.sock";
  try {
    fs.unlinkSync(rel);
  } catch {}
  const srv = net.createServer((c) => {
    let buf = "";
    c.on("data", async (b) => {
      buf += b;
      let i;
      while ((i = buf.indexOf("\n")) >= 0) {
        const line = buf.slice(0, i);
        buf = buf.slice(i + 1);
        const [cmd, ...rest] = line.split(" ");
        const arg = rest.join(" ");
        const at = Date.now();
        let reply;
        try {
          if (!cmds[cmd]) throw new Error("no command " + cmd);
          reply = { ok: true, cur, ms: 0, result: await cmds[cmd](arg) };
        } catch (e) {
          reply = { ok: false, cur, err: String(e?.message ?? e), stack: String(e?.stack ?? "").slice(0, 600) };
        }
        reply.ms = Date.now() - at;
        log("sock", { line, reply });
        c.write(JSON.stringify(reply) + "\n");
      }
    });
  });
  srv.on("error", (e) => log("sock.err", String(e?.message ?? e)));
  srv.listen(rel, () => log("sock.listen", { rel, cwd: process.cwd() }));
  return () => {
    log("cleanup", {});
    srv.close();
  };
}

export default { id: "probe.opencode", setup };
```
