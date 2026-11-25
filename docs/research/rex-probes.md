# Rex probes (researcher, 2026-11-25)

Brief (lead, owner go 11-25): run the step 8 probes of `docs/research/osc7501-architecture-advice.md` §3 on a throwaway
Rex server, in the order R10, R12, R1, R3, R4/R8, R7, R9, R2/R5. Method: `docs/plan/terminal-adapters.md` step 8.
Rex: the bundle's CLI, `rex_version 0.1.0-preview.21`, `api_revision 18`, commit `202b1b3f`, built 2026-11-17.
Agents: Claude Code 2.1.332, codex-cli 0.172.0, opencode 2.2.0, agy 1.5.16.

## Answer

- **R3: all four kinds report a root record in a Rex block, and each reports `blocked` with `kind=permission` at
  its permission dialog.** In every case the record arrived inside the same ~0.1-0.25 s screen-poll window in which
  the dialog first rendered; in no case was the dialog on screen visibly before the record. So the guard's
  read-then-write gap is the dialog's own render time, not a delay in the report.
- **The words over a turn differ by kind**: Claude, Codex and agy go `working` → `done`; OpenCode goes `working` →
  `idle` and never says `done` (as on hc). `msg`: Claude and agy fill it (working and done, and the blocked line);
  Codex sends none at all, not even when blocked; OpenCode sends `title` and, when blocked, the fixed text
  "Permission needs input".
- **Removal is not reliable.** Claude and Codex `clear` their record on a clean exit. **OpenCode and agy leave a stale
  `idle` record after a clean exit**, and **after Claude was SIGKILLed its `done` record stayed** with zsh back in
  the foreground, through later shell commands. Rex itself removed a record (`reason: foreground`) only once: an
  OpenCode record that was `working` when it quit. So the adapter cannot take "a record exists" as "the agent
  runs": it must check the record's `owner.pid` against `process.foreground.pid` (the field exists for that).
- **A new block inherits the server's whole environment.** `OBOR_*`, `HERDR_*` and `CLAUDE_CODE_*` leak from
  whoever started the server (here: this herdr pane, so a block had `OBOR_ROLE=researcher` and this pane's
  `HERDR_PANE_ID`). Rex adds `REX_BLOCK`, `REX_SESSION`, `REX_BIN_DIR`, `TERM_PROGRAM=rex`, and appends the bundle's
  `rex` directory to `PATH`, but sets no server address.
- **Go/no-go: no blocker found, R12 not answered.** Sessions and blocks made through the API run with no client
  attached and `--focus=false` keeps the active window; whether they show in the app was not probed, because that
  needs the owner's app. D4: `rex api call` costs ~30 ms per call (process start included); `rex events` exits 4 with
  "server went away" when the server stops.
- **One correction to the advice §1:** in the `program_status_changed` event the root record's `id` is `""`, not
  left out (it is left out only in the `program_status` method's list); `program_status_removed` names the root as
  `""` too.

## Hard rules: what was done to keep off the owner's Rex

- **Never connected to the owner's server.** Every client call went through a wrapper that always passes
  `-S unix:///private/tmp/claude-501/rx/s.sock --autostart=false` (and `t.sock` for the R5 server). The owner's server
  (pid 2720, `~/Library/Application Support/rex/server.sock`) logs every control connection; its log
  (`servers/ab2fc…e1e.log`) kept size 161054 and mtime Nov 23 10:37 through the run.
- **Server**: `rex server run --listen unix:///private/tmp/claude-501/rx/s.sock --state-dir /private/tmp/claude-501/rx/state
  --config /private/tmp/claude-501/rx/init.lua --restore=false` (empty `init.lua`), with `XDG_RUNTIME_DIR` set to scratch
  (see R10). Agents ran with a scratch cwd under `/private/tmp/claude-501/rx/work/<kind>`.
- **Stopped at the end**: `rex server stop` on both scratch servers (11:04:46, 11:05:19); every agent was exited
  (`/exit`, `/quit`) or killed by PID (Claude 42621 with SIGKILL as a probe; its orphaned `caffeinate` 43085), and
  stopping the server ended the block shells. Afterwards no process of the block trees, no `rex` client, watcher or
  `sleep` probe was left (`ps -p <saved pids>`, `pgrep`). The owner's server was still pid 2720, started Nov 13 18:10.
- No herdr or hc terminal was touched. obor's guard denied one command of mine (`pkill`, below); no `rex` call was
  denied.

## R10: the scratch server leaves the owner's server, config and app untouched

**Holds, after one fix.** Method: a `stat` listing (mtime, size, inode) of `~/Library/Application Support/rex`,
`~/Library/Application Support/com.superlogical.rex`, `~/Library/Preferences/com.superlogical*` before, during
and after, diffed.

1. **First start wrote into the owner's Rex data directory.** Despite `--state-dir`, the server registered itself at
   `~/Library/Application Support/rex/servers/<sha256 of the endpoint>/runtime.json` and `start.lock` (the same
   place as the owner's `servers/ab2fc…/runtime.json`):
   ```
   > …/rex/servers/d3d72f9f…941/runtime.json
   {"endpoint":"unix:///private/tmp/claude-501/rx/s.sock","pid":27209,"instance_id":"bd8fcaa6-…","start_id":"1795600186.848961"}
   ```
   The hash is `sha256("unix:///private/tmp/claude-501/rx/s.sock")`, so it does not collide with the owner's entry. On
   stop the server removed `runtime.json` but left the directory and `start.lock`. I stopped it (`rex server stop`,
   0.09 s), deleted that empty directory and `start.lock` (mine), and restarted.
2. **`XDG_RUNTIME_DIR` moves the registry.** With `XDG_RUNTIME_DIR=/private/tmp/claude-501/rx/xrun` for the server and
   every client, the entry went to `xrun/rex/servers/<hash>/`. A client must have the same value, or it looks
   in the default place: `rex server stop` without it answered `Error: rex server is not running` (I then stopped
   that test server by PID).
3. **Diff at the end** (`before` vs `final`): no file added or removed. Changed: the mtime of
   `…/rex/servers` (from step 1's add and delete), and the owner's own snapshot files
   (`servers/ab2fc…/snapshot/blocks/…/1195` → `1196`, `snapshot.db-wal`) at 10:40:11 and 11:00:11: the owner's
   server saves every 20 minutes (10:20:11, 10:40:11, 11:00:11), so this is its own schedule.
4. **Second leak, harmless**: `rex server stop --discard` without `--state-dir` looked in the owner's directory
   (`there were no saved sessions in /Users/…/Library/Application Support/rex/servers/dd3a2aff…/snapshot`). It
   created and deleted nothing there (diff above). The command's help says to pass `--state-dir`.
5. Rex Beta.app was not running and was not opened; `~/.config/rex` does not exist and was not created.

**For obor**: a scratch or test server needs `XDG_RUNTIME_DIR` set for the server and every client, besides the
flags in the plan. Without it, the registry entry lands in the owner's data directory.

## R12: blocks made through the API, with nobody attaching

**Not answered for the app.** Showing in the app needs the owner's app connected to the scratch server, which the
rules exclude. What the server says:

- Every session, window and block in this run was made by `rex new` / `rex run` / API calls, and the agents ran
  with no interactive client: `session.list_clients` listed only CLI clients (`"info":{"kind":"cli"}`), and the
  only long-lived one was the `rex events` stream (no `session_ids`).
- `rex run --focus=false` six times: `session.view` kept `active_window_id` at the session's first window
  (`window:01a2121d-a8dc-…`).

To find out: on a machine or account where the owner allows it, open the app on a scratch server and run
`rex run --focus=false` while it shows another block.

## R1: call cost, events, method names

- **Per call** (fresh process each, 20 calls, median): `rex api call server.status` 34 ms (min 29, max 95);
  `rex api call session.list` 31 ms (min 27, max 40). `rex new probe --json`: 57 ms wall.
- **Methods** (`rex api list`, 74 server methods plus block methods). The ones the adapter needs:
  `session.create`, `session.list`, `session.view`, `session.list_blocks`, `session.new_window`, `session.new_split`,
  `session.set_block_label`, `session.set_label`, `block.close`, `block.event` (key, paste), `session.bootstrap`,
  `session.attach_all`, and the terminal block's `com.superlogical.terminal.program_status`, `.process`, `.format`,
  `.write`, `.clear`. A block method is called as
  `rex api call com.superlogical.terminal.program_status '{"session_id":…,"block_id":…,"args":{}}'`.
- **`program_status` schema** (`rex api describe com.superlogical.terminal.program_status`): output
  `{"records":[…]}`, each with `state` and `updated_at` required, and optional `id` ("Left out for the root
  record"), `kind` (`permission`, `question`, `auth`), `msg`, `title`, `progress` (0-100), `app`, `effective_app`,
  `owner` (`{pid, name}`, "The terminal's foreground process when the report arrived"). Root first, "each parent
  before its children". Confirms the advice's field list.
- **Events**: `rex events --all --json`, one object per line. Block events arrive wrapped:
  ```json
  {"type":"session_event","payload":{"session_id":"session:…","event":{"type":"block_event","payload":{
    "block_id":"block:…","creator_name":"com.superlogical.terminal","name":"program_status_changed",
    "payload":{"id":"","record":{"state":"blocked","kind":"permission","app":"claude-code","effective_app":"claude-code",
    "msg":"approve Write: /private/tmp/claude-501/rx/work/claude/hello.txt","updated_at":"2026-11-25T10:53:27.384487+01:00",
    "owner":{"pid":42621,"name":"2.1.332"}},"reason":"report"}}}}}
  {"type":"session_event","payload":{…,"event":{"type":"block_event","payload":{"block_id":"block:…",
    "creator_name":"com.superlogical.terminal","name":"program_status_removed",
    "payload":{"ids":["","ses_dedd9752bffeg5xJAUEuu4nEXZ"],"reason":"foreground"}}}}}
  ```
  Changed `reason` seen: only `report`. Removed `reason` seen: `clear` and `foreground`. Other block events seen:
  `process_changed`, `title_changed`, `ports_changed`. Top-level types seen: `session_created`, `session_event`,
  `block_created`, `window_created`, `block_owner_changed`, `session_view_changed`, `session_label_changed`,
  `client_joined`/`client_left`, `client_connected`/`client_disconnected`. With `--all`, **every CLI call adds a
  `client_joined` and `client_left` line** (13.7k of 28k lines in this run, from my pollers): a consumer filters them.
- **No events in `help.all`**: `rex api call help.all` (320 KB) describes methods and block types only; the event
  names above are from the stream, not from a schema.
- **Server stop ends the stream**: `rex events --all --json` printed
  `Error: server went away: reading API control: reading control message: failed to get reader: failed to read frame header: EOF`
  and exited with status 4, at the same second as `rex server stop` (seen on both servers).

## R3: each kind in a block

Setup per kind: `rex run -s probe --focus=false --label <kind> -c <scratch>/<kind> -- /bin/zsh -l`, then
`rex send` of `unset OBOR_ROLE OBOR_PANE HERDR_… CLAUDECODE CLAUDE_CODE_…; <agent>` and `\r` (the leaked env, R4).
A poller called `program_status` and `rex capture` in a loop (~100 ms per round, both calls) and logged changes
with timestamps; the `rex events --all --json` stream was timestamped on arrival. Prompts: "list the files here",
then "create a file hello.txt containing hi" (or an outside path, or "run the shell command: touch hello2.txt")
for the permission dialog, answered "yes".

### Is there a root record, and `app`

Yes, for all four, from the agent's start, with no query from anyone: `app` = `claude-code`, `codex`, `opencode`,
`antigravity` (agy, as the advice expected). `owner.name` is the process name, which is not the command: Claude's
is its version (`"2.1.332"`, path `~/.local/share/claude/versions/2.1.332`); agy's changed mid-session from `agy` to
`agy.1795600910734317000.old` (it replaced its binary while running). Compare `owner.pid`, not the name.

### The words over one turn ("list the files here")

| Kind | Start | Turn (record changes, local time) | msg / title |
|---|---|---|---|
| Claude | `idle` | 10:52:58.374 Enter → `working` .496 → `working` + msg 59.696 → `done` 00.381 → `done` + msg 02.118 | working: "Listing files in current directory"; done: "listed directory: one file, a.txt (11 bytes)" |
| Codex | `idle` (a `working`/`idle` blip at start) | Enter 10:55:46.8 → `working` 46.938 → `done` 53.558 | none, ever |
| OpenCode | `idle` | `working` 10:58:40.485 (+ child record `id=ses_…`) → `working` 43.099 → `idle` 47.283 (child removed, `clear`) | `title` "New session - …", then "List files here"; no msg |
| agy | `idle` (after trust) | `working` 11:02:09.380 → `done` 11:02:15.850 | done: "The workspace \`/private/…/agy\` contains:" |

The first `working` arrived 0.09-0.25 s after Enter for all four. `done` stays until the next prompt (Claude,
Codex, agy); OpenCode returns to `idle`.

Also seen: Claude writes **child records** for a background shell (`id: "b2b2uaf3g"`, `title: "Pause for 15
seconds"`) and clears them; when that shell finished, Claude started a turn by itself (`working` → `done`, "sleep
15 finished, exit code 0") with no prompt. OpenCode mirrors the root into a child per session (`id: ses_…`).

### Blocked at a permission prompt, and how soon after the dialog

| Kind | Record | Server `updated_at` | Last capture without the dialog → first with it |
|---|---|---|---|
| Claude (Write) | `blocked`, `permission`, "approve Write: /private/tmp/claude-501/rx/work/claude/hello.txt" | 10:53:27.384 | 27.361 → 27.473 |
| Codex (shell, `-a on-request -s read-only`) | `blocked`, `permission`, **no msg** | 10:57:41.857 | 41.057 → 41.908 (a slow poll round) |
| OpenCode (edit, project `opencode.json` `"edit":"ask"`) | `blocked`, `permission`, "Permission needs input", on root and child | 11:00:53.655 | 53.449 → 53.700 |
| agy (shell, `--mode accept-edits`) | `blocked`, `permission`, "Approve Bash: touch hello2.txt" | 11:03:27.353 | 27.264 → 27.366 |

In each row the record's time lies inside the window in which the dialog first appeared; the resolution is the
poll's (~0.1 s, 0.85 s for Codex). No case showed the dialog on screen while the record still said `working`.
After "yes" each went back to `working` within 0.1-0.15 s of Enter, then `done` (OpenCode: `idle`).

Startup dialogs also report: Claude's trust dialog `blocked`/`permission` "trust this folder" (then `clear`, then
`idle`); agy's `blocked`/`auth` "Sign in required" for 1.2 s at start (it was signed in; it passed by itself), then
`blocked`/`permission` "Trust this folder?". Codex showed no trust dialog in the scratch dir.

How the prompts were reached, without touching the owner's config: the owner's Codex config has
`approvals_reviewer = "auto_review"` and asked nothing; `codex -a on-request -s read-only -c approvals_reviewer="user"`
did (`-a untrusted` is refused by 0.172: "possible values: on-request, never"). OpenCode asked nothing by default, and
`OPENCODE_CONFIG_CONTENT` with a `permission` object did not change that; an `opencode.json` in the scratch cwd did.
agy's default mode is `auto` (no prompt for an edit); `--mode accept-edits` prompted for a shell command.

### Codex, which sends no query

Codex reports without any query and without a reply: root record `idle` at start, `working`, `done`, `blocked`. It
never sends `msg` or `title`, so obor's `Note` for a blocked Codex is only "permission".

### Removal when the agent exits

| Kind, how it ended | What Rex did |
|---|---|
| Claude `/exit` | `program_status_removed {"ids":[""],"reason":"clear"}`: the agent cleared it |
| Claude SIGKILL (as a crash) | **nothing**: `done` record kept; `process.foreground` = `zsh`; still there after `echo x` and `sleep 2` at the shell (checked 13 s+) |
| Codex `/quit` | `idle`, then `removed … "clear"` |
| OpenCode `/exit` while idle (twice) | child `clear`ed; **root stays `idle`** with the dead `owner.pid` until the next opencode reports |
| OpenCode `/exit` while `working` | `removed {"ids":["","ses_…"],"reason":"foreground"}`: removed by Rex |
| agy `/exit` | **root stays `idle`** (two `idle` reports, no clear) |

So Rex removed a record itself only when it was `working`; `idle` and `done` records outlived their program.

## R4, R8: a new block's environment; a block kept after its program exits

- **Env** (`rex run --keep-open -- sh -c 'env | sort > …'`, and `--shell=none -- /usr/bin/env K=V sh -c …`): the
  block has the server's whole environment: `OBOR_ROLE=researcher`, `OBOR_PANE=w5:p29`, `HERDR_ENV=1`,
  `HERDR_PANE_ID=w5:p29`, `HERDR_SOCKET_PATH`, `HERDR_TAB_ID`, `HERDR_WORKSPACE_ID`, `HERDR_BIN_PATH`, 13
  `CLAUDE*` variables of the session that started it (`CLAUDECODE`, `CLAUDE_CODE_SESSION_ID`, …), and my
  `XDG_RUNTIME_DIR`. A Claude started there came up in **auto** mode with "Transcript saving is off — inherited
  CLAUDE_CODE_CHILD_SESSION marker"; I restarted it after unsetting them.
- Rex adds: `REX_BLOCK=block:…`, `REX_SESSION=session:…`, `REX_BIN_DIR=/Applications/Rex Beta.app/…/MacOS`,
  `TERM=xterm-256color`, `TERM_PROGRAM=rex`, `TERM_PROGRAM_VERSION=0.1.0-preview.21`, and `REX_BIN_DIR` appended to
  `PATH`. No variable names the server's address. The only difference with `--shell=none` was the added `K=V`.
- `env K=V` as the command works as a way to set a block's variables.
- **R8**: a block made with `--keep-open` whose program exited has no child: `process` returns only
  `{"last_exit":{"exit_code":0},"home":…}`. `rex send -b <it> 'echo hi'` exits 0 and nothing runs; the screen stays
  the old output. No method restarts a program in a block (none in `rex api list`). A new program needs a new block.

## R7: multi-line paste and Enter

Into Claude only (not tried into Codex): `rex api call block.event '{"session_id":…,"block_id":…,"event":{"type":"paste","payload":{"text":"list the files here.\nthen say how many there are.\nkeep it short."}}}'`
returned `{}` and the three lines stood in Claude's input box, not submitted. `rex send-key enter` then sent them as
one prompt (one turn, answer "2 files."). Single-line prompts to all four kinds went in by `rex send '<text>'` then
`rex send-key enter`; `rex send $'…\r'` typed and ran a command at zsh.

Unknown: the same paste into Codex, agy and OpenCode.

## R9: `process` for shell, agent, tool

`rex api call com.superlogical.terminal.process` returns `last_exit`, `child`, `foreground`, `ancestors`, `home`;
each process has `pid`, `user_id`, `user`, `name`, `path`, and for the foreground `invoked_path`, `argv0`, `cwd`.

| Moment | child | foreground |
|---|---|---|
| `rex new` (default) | `login` (`/usr/bin/login`, root) | `zsh`, `argv0` `-zsh` |
| `rex run -- /bin/zsh -l` at its prompt | `zsh` | `zsh` |
| Claude running | `zsh` | `2.1.332`, `invoked_path` `~/.local/bin/claude`, `argv0` `claude` |
| Claude running a tool (`sleep 15`) | `zsh` | still Claude: the tool is not the foreground process |
| Codex running | `zsh` | `codex`, path `/opt/homebrew/Caskroom/codex/0.172.0/bin/codex` |
| after the agent exits or is killed | `zsh` | `zsh` |

So "at the shell" is `foreground.pid == child.pid` when the block's command is the shell, and "agent up" is a
foreground other than the shell. A `process_changed` block event carries `generation`, `foreground` and `ancestors`
when the foreground changes.

## R2, R5: a relabel's event; what a server restart brings back

- **R2**: `session.set_block_label` returned `{"revision":14}`; the only event was
  `session_view_changed {"revision":14}`: no block-label event. A reader re-reads `session.view` or
  `session.list_blocks`. `session.set_label` sends `session_label_changed {"label":"probe2"}` and
  `session_view_changed`.
- **R5** (second scratch server, `--restore=true`, state dir `/private/tmp/claude-501/rx/state2`): a session `keep` with
  a block labelled `sleeper` running `sh -c 'echo started-$$; sleep 600'`; `rex server stop`; start again.
  - `rex server stop` ended the program (`sleep 600` gone).
  - After the start: the same session id and label, the same two block ids, labels kept (`shell`, `sleeper`).
  - The program was **not** re-run: the sleeper block held a fresh login shell (child `login`, foreground `zsh`)
    in `~` ("Last login: … on ttys084"), not its command or its cwd.
  - `instance_id` changed (`4e65fee2-…` → `f0b40cd2-…`); `host_id` stayed.
  - Then `rex server stop --discard` (the R10 point 4 above).

## Still unknown, and what it would take

| # | Unknown | To find out |
|---|---|---|
| R12 | blocks made through the API show in the app; `focus:false` steals no focus there | the app on a scratch server, with the owner's leave |
| R7 | multi-line paste into Codex, OpenCode, agy | three paste calls in a later run |
| R3 | Codex's update dialog, `question` kind (any agent asking the user), an API error | needs a case that triggers each |
| R3 | whether Rex would remove an `idle`/`done` record whose owner left the foreground after a longer wait, or with Rex's shell integration (prompt marks) installed | a wait of minutes; `rex skill` or Rex's shell setup was not run (it installs into agent and shell config) |
| R4 | what `rex` run inside a block connects to: no address variable is set, so likely the default socket, i.e. the owner's server | not tried, by the rules; read `rex whoami` inside a block of a server started from a clean env |
| — | dialog-to-record gap at finer than ~0.1 s | a `data.connect` stream of the screen instead of polling |

## Flagged, not expanded

- **Stale records are the adapter's problem.** On Rex an exited OpenCode or agy, or a crashed Claude, leaves a
  record that reads `idle` or `done`. Reading `Status` from the record alone would show a dead pane as ready. The
  rule `owner.pid == process.foreground.pid`, else no agent, is cheap (one more call, or `process_changed`).
- **obor must start the Rex server (or each block) with a clean environment.** Otherwise every agent in a block
  inherits the starter's `OBOR_ROLE`, `OBOR_PANE`, `HERDR_*` and `CLAUDE_CODE_*`: obor's hooks would attribute the
  agent to the wrong role and pane, and Claude starts in the parent's permission mode.
- **`rex` is on `PATH` inside every Rex block** (the bundle directory is appended). With no address variable, an
  agent's `rex send` or `rex kill` would reach the default server, which is the owner's. This sharpens the advice's
  "the guard hook has no rule for `rex`".
- **A test server needs `XDG_RUNTIME_DIR`** (R10), and `rex server stop --discard` needs `--state-dir`; the plan's
  command line lacks both.
- obor's guard denied `pkill -f "rx events -s probe"`: "obor: pkill kills by pattern and can hit other lanes'
  processes — stop only processes you started, by PID (kill <pid>)". I used `kill <pid>`.

Raw logs (not kept by the repo): `/private/tmp/claude-501/rx/` (`ev.jsonl` the timestamped event stream, `psev.txt`
its program-status lines, `w-<kind>.log` the screen and record pollers, `before.txt`/`final.txt` the R10 listings).
