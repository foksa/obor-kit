# hc probes P1-P11 (T-1351)

Step 0 of `docs/plan/herdr-core.md`: one live run of the plan's probe table against throwaway hc cores, so that the
plan's guesses about hc's run-time behaviour are replaced by what hc did. No product code. Its no-go checks are
answered at the end, followed by what each fact changes in steps 2-5.

- Date: 2026-11-20, 10:30-11:20 CET. macOS (Darwin 27.2), one machine.
- Versions: hc 0.1.21 (every probe); Claude Code 2.1.329; Codex 0.169.0 for its first start, 0.171.0 after that (an
  update happened during P1, see P1 and "Side effects"); OpenCode 2.2.0.
- Method: each probe in its own session, `HC_SESSION=obor-probe-<n>`, with scratch `XDG_STATE_HOME`,
  `XDG_CONFIG_HOME` and `XDG_RUNTIME_DIR` under `/tmp/obp/<n>`, `HC_SOCKET` unset. The core was started by
  `hc server start` from `env -i` plus `HOME USER LOGNAME SHELL TERM LANG PATH` and those four names, and stopped with
  `hc server stop`. The probes used the CLI from the same scrubbed env and the socket directly (JSON-RPC 2.0, one
  message per line, `hello {generation: 1}` first), with the timings taken around one call on its own connection.
  Agents ran interactively at a kept shell terminal's prompt, typed in with `terminal.send`, never in a headless
  form. The default session was read only through its own socket and its state files (P11).
- Repeatable part: `make conformance TIER=live KIND=hc [TURN=1]` (`tests/conformance/live_hc.go`) runs P2-P6, P8,
  P9, P11 and Claude's P1 the same way and prints one line per fact. Codex and OpenCode in P1, P7 and P10 were run by
  hand (see each section). Timings are one machine's.

Summary:

| # | Question | Answer at 0.1.21 |
|---|---|---|
| P1 | Do the three kinds report OSC 7501 inside hc, and `blocked` at a permission prompt? | Yes, all three, with `status.app` naming the kind. Gaps: Codex's trust and update dialogs report nothing; OpenCode never says `done` and reports an API error as `idle` |
| P2 | `terminal.send`: timing, refusal, leftovers, size | 0.2-0.9 s to a shell, 0.2-1.6 s to an idle agent, 5.2 s when the text starts no turn, 59 s while a person's draft is open. At `blocked` refused in ≤1 ms with kind `blocked` and nothing typed. Never clears earlier input. 4 KB and 16 KB lines arrive whole |
| P3 | `terminal.keys` delay per call | 31-36 ms per call at a quiet shell, a busy shell and a working agent: no quiet wait |
| P4 | A terminal's env; the leak; unset; reading the core's env | The core's env plus hc's names. A core started from a shell with `HERDR_`/`OBOR_` names gives them to every terminal; restart keeps them, `restart --fresh` drops them. `env: {"NAME": ""}` sets empty, never unsets. `machine.exec env` reads the core's env |
| P5 | Holder loss, restart, stop/start; what names one run | Kept terminal: `lost` then `restarted`, same address, labels and cwd. Restart: programs live, the stream ends with EOF, `seq` restarts at 0, core pid changes, holder instance stays. Stop/start: kept terminals back as shells, holder instance new |
| P6 | Event order, `missed`, the label filter, label keys | Order below. A slow reader gets `missed` with a snapshot; a stalled one is disconnected. Newly matching terminal: `labels` with `record`; leaving the filter: one last `labels`, then silence. Keys refuse only `""` and a key with `=` |
| P7 | Does a socket-made terminal show in herdr-app? | Owner: steps below |
| P8 | Runs with no caller; an agent calling `hc run` | No caller: no parent, nothing typed anywhere. From inside a terminal: the child's parent is that terminal, it dies with the parent's program, and an agent parent gets a note typed into its input that starts a turn |
| P9 | `pid` and `foreground` | `pid` is the shell's always; `foreground` is the tty's group leader: the shell, a command it runs, or the agent. An agent's tool runs outside the tty's group and never shows |
| P10 | Core CPU with three idle agents | 0.064 s of CPU per minute; extrapolated from 15 minutes, about 3.8 s per hour (holder 2.5 s) |
| P11 | Scratch sessions leave the default alone; `hc schema` with no core | Default core, holder, terminals and state files unchanged first to last; herdr-app ended mid-run for a reason not found (P11). `hc schema` prints the schema but starts a core for the session when none runs |

## P11: the scratch sessions and the default session (run first and last)

What ran: before any probe and after the last one, a read of the default session that cannot start a core: the
hashes of `~/.local/state/herdr-core/default/{core.json,core.lock,docs.json}`, whether `~/.config/herdr-core` exists,
the default core's processes in `ps`, and over its socket (named by `core.lock`; a dial starts nothing)
`server.status` and `terminal.list {all: true}`.

What came back, first (10:30) and last (11:16): core id `0295b50c…`, core pid 52026 (up since 10:01), holder pid
51392 with the same instance, terminal `t1` live (zsh, same pid), `t2` finished, the three file hashes equal, no
`~/.config/herdr-core`. One thing differed: `terminals.json` (not in the hashed set, since hc rewrites it) changed at
11:02:55 in one field pair, `t1`'s size, 120×37 → 200×120. That is what a viewer leaving does (P2: the size returns to
the headless 200×120), and herdr-app (`/Applications/Herdr.app`, pid 51362) and its viewer process
(`herdr-core connect`, pid 52061) were running at 11:02:16 and gone at 11:16. Why herdr-app ended is not known: no
probe command addressed it or the default core other than the reads above (11:02:16 was one of them, a
`server.status` and a `terminal.list`), no process the probes signalled was it (each target's command line was checked
first), and there is no crash report. The owner should check whether they closed it. The scratch cores were separate
processes (`hc --hc-launchd …`) with sockets
under `/tmp/obp/<n>/run/herdr-core/`, holders under the same dir, and state under `/tmp/obp/<n>/state/herdr-core/`.

`hc schema` with no core running, in a fresh scratch session: it prints the same schema as with a core (byte for
byte) but leaves a core running for that session, started with the caller's environment (a second run from a shell
carrying `OBOR_ROLE` and `HERDR_PANE_ID` gave a core whose terminals have both). `hc --version`, `--help`, `--skill`
and `hc schema --help` start nothing. `hc server status` also starts a core when none runs.

## P4: environment

What ran: `terminal.run {argv: ["/usr/bin/env"], wait: true}` and `machine.exec {argv: ["/usr/bin/env"]}` on a
scrubbed core; the same after `hc server start` from the scrubbed env plus `HERDR_PANE_ID=w99:p99 OBOR_ROLE=worker
OBOR_PANE=w99:p99 HERDR_ENV=1 HERDR_SOCKET_PATH=…`; then `hc server restart`, then `hc server restart --fresh` from
the scrubbed shell.

- A terminal of a scrubbed core gets the core's env plus `HC_SCOPE`, `HC_SESSION`, `HC_TERMINAL_ID`,
  `HC_TERMINAL_SOCKET`, `TERM_PROGRAM=ghostty`, `COLORTERM=truecolor`, `PAGER=cat`, `GIT_PAGER=cat`, `GH_PAGER=cat`
  and `PWD`. The core's `XDG_*` names pass through to every terminal. No `HERDR_` or `OBOR_` name. (`machine.exec`'s
  env is the core's without `TERM`; a terminal has `TERM=xterm-256color`.)
- Core started with the made-up names: every new terminal has all five (`HERDR_ENV`, `HERDR_PANE_ID`,
  `HERDR_SOCKET_PATH`, `OBOR_PANE`, `OBOR_ROLE`). `machine.exec` of `env` shows the same five: it reads the core's
  env. `hc server restart` keeps them (5 after). `hc server restart --fresh` from the scrubbed shell drops them (0
  after). Risk 2 of the plan holds as written.
- `env: {"OBOR_ROLE": "", "HERDR_PANE_ID": ""}` on `terminal.run` gives `OBOR_ROLE=` and `HERDR_PANE_ID=`: set
  empty, not unset (the same for a name the core lacks). hc keeps its own value for some names whatever `env` says:
  `LANG: ""` left `en_US.UTF-8`, `PAGER: ""` left `cat`, and `TERM_PROGRAM: "herdr"` left `ghostty`, with no
  `TERM_PROGRAM_VERSION`. The terminal identity is not set through `env`.
- With no `cwd`, a terminal starts in the directory `hc server start` ran in.

## P2: `terminal.send`

What ran: sends at a kept zsh terminal and at Claude, Codex and OpenCode in kept shell terminals, timed around the
call; a viewer was `hc attach <t>` in a 100×40 pty driven by a script.

- To a shell that had been quiet for a second or more, with nobody attached: 0.20 s, every time. To a shell whose
  prompt had just drawn (the program's first send): 0.87 s, twice. To a shell that is printing (a loop writing every
  0.5 s): 0.20 s, and the text is typed into the running loop's output (shell typeahead).
- To an idle agent: 0.21-1.6 s (Claude 0.26-1.57, Codex 0.21-0.22, OpenCode 0.25-0.29), answer `started: true`,
  `status` the word before the text. A text that starts no turn (`/exit`, `/quit`): 5.2 s, `started: false`. So the
  call waits until the program says `working` or about 5 s pass.
- Viewer attached, not typing: 0.3 s. Viewer who typed `draft` one second before: at a shell 59.2 s, at Claude 59.3 s;
  then the text is pasted after the draft (`draftecho VIEWER-SEND` ran at the shell; Claude got
  `draft-textReply with exactly OK` as one message).
- At `blocked`: refused in 0-1 ms with `{code: -32000, data: {kind: "blocked", hint: "typing would answer its dialog;
  read its screen first (hc read t1), or --force"}, message: "t1 is blocked (permission)"}`. Checked for Claude (trust
  dialog and a Bash permission prompt), Codex and OpenCode (permission prompts). Nothing was typed: the status stayed
  the same prompt and the prompt's effect did not happen (a send of `4`, Claude's "No", left the prompt up and the file
  uncreated). `if: "idle"` at blocked gets the same refusal.
- Text an earlier input left unsent: `terminal.keys` `e c h o space L E F T`, then a send of `echo X`: the shell ran
  `echo leftecho X`. A send never clears what is already typed. (The keys also show that key names are read without
  case: `L` typed `l`.)
- Multi-line text `echo LINE-A\necho LINE-B`: at a zsh prompt both lines ran, in order. At Claude a two-line text is
  one message.
- Long lines at a shell: `echo <4000 x> | wc -c` printed 4001 and `echo <16000 x> | wc -c` printed 16001: nothing
  cut (herdr cuts at about 1 KB).
- A viewer's attach resizes the terminal to the viewer (`stty size` 40 100 while attached) and the size goes back to
  the headless 200×120 after the viewer detaches.

## P3: `terminal.keys`

What ran: `esc`, `ctrl+c`, `enter` as three calls in a row, timed, at a quiet shell, at a shell printing every 0.2 s,
and at Claude while `working`; one call with all three keys.

Each call took 31-36 ms in every case; the one call with three keys 95 ms. The schema's "after ~3 s of quiet" did not
show for a shell or for a working agent. At Claude, `esc` while working interrupted the turn (`idle` within 2 s, the
running tool ended).

## P5: holder loss, restart, stop and start

What ran: a kept shell terminal with labels `obor.project=probe obor.agent=w1` that had run `cd sub`, and a plain
`sleep 3600` terminal; an `events.subscribe {}` stream open throughout; then `kill -9` of the holder (pid from
`server.status`), `hc server restart`, and `hc server stop` followed by `hc server start`.

- Holder killed: the stream stays open and sends `lost` (reason `holder_lost`) for the kept terminal and `finished`
  (reason `holder_lost`) for the plain one at once, then `restarted` for the kept one 1.9 s later and a `foreground`
  event for its new shell. The kept terminal keeps its address, labels and its last cwd (`…/work/sub`: the record's
  `cwd` now holds it), with a new shell pid. The plain terminal's program is gone. `server.status` lists one new holder.
- `hc server restart`: every program keeps running (same pids), core id unchanged, core pid changed
  (41066 → 43124), holder pid and instance unchanged. The stream ends with EOF, after no closing message. A new
  subscription starts at `seq` 0.
- `hc server stop`: the stream gets `lost` (reason `server_stop`) for the kept terminal (twice for the same terminal in
  both program runs, once by hand), then EOF. Programs end.
  `hc server start`: the kept terminal is live again as a fresh shell, same address, labels and cwd. Plain terminals
  stay finished with reason `server_stop` (or `holder_lost` from before). Core id unchanged; holder pid and instance
  new.
- What names one run: nothing directly. The core pid changes on a restart that keeps every program; the core id
  never changes. The set of holder `instance` values changes exactly when programs were lost: a holder's loss or a
  stop. It does not change on a restart.
- `heartbeat` notifications every 15 s (`{seq}`: the last event's seq).

## P6: the event stream and labels

Claude's start, turn and exit in one terminal, as `events.subscribe {}` delivered them (seconds since the
subscription; repeated `foreground` events left out):

```
  0.3  foreground  zsh 71102
  7.8  foreground  "2.1.329" 71307                      (claude --model haiku typed at the shell)
  7.8  status      blocked/permission "trust this folder", reported true
 49.0  status      null                                  (trust accepted)
 49.4  status      idle;  title "Claude Code"
 66.5  status      working                               (a send)
 67.2  title       "Exact OK reply"
 67.4  status      done;  turn {state: done, turn: 1}
 68.6  status      done "replied OK as requested"        (Claude's own summary, 1.2 s later)
 99.8  status      blocked/permission "approve Bash: touch probe-file.txt";  turn {state: blocked, turn: 2}
136.7  status      working;  137.4 done;  turn {state: done, turn: 3}
188.7  status      idle;  turn {state: idle, turn: 6}    (esc during a turn)
196.9  status      null;  title ""                       (/exit)
198.1  foreground  zsh 71102
```

A `status` event carries `{status, reported, records}`; a `turn` event the word that ended the turn and the new turn
number. The turn number rises at `done`, at `blocked` and at an interrupt. OpenCode's events also carry `title` and
fill `records` keyed by its session id (`ses_…`). Status comes only from the program; hc adds nothing.

- `missed`: a subscriber reading one line per 20 ms while 6,000 label events arrived (5 ms apart) got 1,784 events,
  4 `missed` notifications, each with `seq` and a fresh `terminals` snapshot, and 4 heartbeats, and stayed connected
  (the program's smaller run, 3,000 events: 757 events and 2 `missed`). A
  subscriber that read nothing for 45 s during the same flood got 48 events and then EOF: no `missed`, the core
  closed it. The writer was not slowed (5 ms per call either way).
- The label filter (`events.subscribe {labels: {"obor.project": "probe"}}` while labels on a terminal changed):
  setting `obor.project=probe` sent `labels` with the whole `record`; `obor.agent=w1` sent `labels` with both keys;
  removing `obor.project` sent one `labels` without it; `obor.agent=w2` while not matching sent nothing; setting
  `obor.project=probe` again sent `labels` with the `record`. A `labels` event carries every label of the terminal.
- Label keys: `obor.project`, `obor/project`, `obor-project`, `obor_project`, `obor project`, `Obor.Project` and `ü`
  are taken; `""` and `a=b` are refused (`invalid_params`, `bad label key`). Values are free text (`v=1 x/y` taken).

## P1: agents in hc terminals

Each kind was typed at a kept zsh terminal's prompt in a fresh git directory (`/tmp/obp/1/work/proj`), with the
owner's own config and auth (real `HOME`).

**Claude 2.1.329** (`claude --model haiku`):

- `reported: true` as soon as it drew; `status.app` is `claude-code`; `foreground.name` is `2.1.329` (its process
  title), `argv` `["claude", "--model", "haiku"]`.
- The trust dialog reports `blocked`, kind `permission`, msg `trust this folder`. (The field report the plan cites,
  on 2.1.312, said the dialog sent nothing; on 2.1.329 it does.) After `down`, `enter`: `null` for 0.4 s, then `idle`.
- A turn: `idle` → `working` → `done`, then `done` again with a `msg` summary 1.2 s later. It stays `done` until the
  next prompt: it does not go back to `idle`.
- The owner's default permission mode is auto: a harmless command runs with no prompt. In manual mode (shift+tab
  once), `touch probe-file.txt` gave `blocked`, kind `permission`, msg `approve Bash: touch probe-file.txt` within a
  second; `enter` approved it (`working` → `done`). A tool running (a `ping`) leaves the status `working`.
- `esc` during a turn: `idle`. `/exit`: status `null`, `reported` stays `true`, foreground the shell.
- No `error` status was seen.

**Codex** (Codex's own setting `approvals_reviewer = "auto_review"` approves commands without a prompt, so the
permission prompt was taken with `-c approvals_reviewer="user" -a on-request -s workspace-write`):

- 0.169.0, plain `codex`: reports `app: codex`: `idle` once up; a turn `idle` → `working` → `done`, and it stays
  `done`. Its trust dialog ("Trust this folder?") reported nothing: `status: null`, `reported: false` until it was
  answered. `foreground.name` `codex`.
- Its update prompt ("Update available! 0.169.0 → 0.171.0", shown at the second start) also reported nothing, so a
  send was not refused: its Enter took the default answer and Codex ran `brew upgrade --cask codex` in the terminal.
  This is the gap's practical form: hc's guard covers only dialogs the program reports.
- Without `--no-daemon`, the first `codex` started an app-server daemon as its child
  (`~/.codex/packages/app-server-daemon/…/codex app-server`), which stayed running when Codex quit (re-parented to
  launchd) with the starting terminal's env. A second `codex` in another terminal used it: a command it ran printed
  `HC_TERMINAL_ID=t2` (the first terminal's id) and ran as the daemon's child. With `--no-daemon` (0.171.0) a command
  ran as Codex's own child and printed `HC_TERMINAL_ID=t3`, the right one. `hc --skill`'s "Codex must run with
  `--no-daemon`" is this. obor already passes it (`internal/config/models.go:42`).
- 0.171.0 `--no-daemon`: same words. The permission prompt (a `touch` outside the workspace): `blocked`, kind
  `permission`, no `msg`; a send refused with kind `blocked`; `esc` → `idle`.

**OpenCode 2.2.0**:

- Plain `opencode` failed in the probe terminal: it starts or joins a background service, and the scratch `XDG_*`
  names it got from the probe core gave it a different state dir, so it tried a second service on the port the
  owner's service (`opencode serve --service`) holds. `opencode --standalone` (a private server, the flag obor's
  opencode kind passes) works. With the scratch `XDG_STATE_HOME`/`XDG_CONFIG_HOME` it also lost the owner's model
  choice and config; the terminal was started with `env: {XDG_CONFIG_HOME, XDG_STATE_HOME}` set back to the owner's
  for the turn below.
- Reports `app: opencode`: `idle` when up (`reported: true`); a turn `idle` → `working` (with `title`) → `idle`. It
  never says `done`. It also fills `records` keyed by its session id.
- A model that was out of credits ("Default is out of credits") gave `working` → `idle`: an error reported as `idle`,
  not `error`.
- The owner's config lets bash run without asking. With a project `opencode.json` `{"permission": {"bash": "ask"}}`:
  `blocked`, kind `permission`, msg `Permission needs input`, title `Bash command request`; a send refused; `esc` →
  `idle`. With `--standalone` a command ran as a child of its `serve --stdio` process and saw `HC_TERMINAL_ID=t6`.

## P9: `pid` and `foreground`

- At a shell: `pid` = `foreground.pid` = the shell (`/bin/zsh -l`, `name` `zsh`). The shell running `sleep 30`:
  `foreground` `sleep`, `pid` still the shell.
- Claude: `pid` the shell (71102), `foreground` Claude (`name` `2.1.329`, pid 71307). While Claude ran `ping` through
  its Bash tool, `ps` showed `ping` in its own process group (76433, under a zsh Claude started) with no controlling
  terminal (tpgid 0): `foreground` stayed Claude. Claude's helpers (a `uv`/python MCP server, `caffeinate`) sit in
  Claude's group. Codex: `foreground.name` `codex`; its commands run under the daemon or (with `--no-daemon`) under
  Codex. OpenCode: `foreground.name` `opencode`; commands under its server child.
- So `foreground` tells shell from agent, never whether an agent's tool is running: that is `status: working`.

## P8: runs with no caller, and an agent calling `hc run`

- `terminal.run` with no `caller` and no `no_parent`, from outside any terminal: `parent: null` (`notify: true`, with
  nobody to tell). `hc run --bg …` from the scrubbed shell: `parent: null`. Nothing was typed anywhere; the run ended
  with nothing left but its finished record.
- `hc run --bg -- sh -c 'echo child-c; sleep 1'` typed at a shell terminal t3: the child's `parent` is `t3`. When it
  ended nothing was typed into the shell.
- `hc run --bg -- sleep 300` from t3, then `exit` at t3's (kept) shell: t3 is `finished` (reason `exited`, code 0)
  and stays finished: `keep` replaces a lost program, not one that exits. Its child was `killed` with it.
- Claude in a terminal asked to run `hc run --bg -- sh -c 'echo child-x; sleep 3'`: the child's parent is Claude's
  terminal. When it ended, hc typed into Claude's input and submitted
  `<hc>t7 exited (exit 0). Last lines: child-x Read: hc read t7</hc>`, and Claude started a turn on it (turn 1 → 2)
  that nobody had sent.

## P7: does a socket-made terminal show in herdr-app? (owner)

Not run: it needs a person at herdr-app. One fact read without touching it: herdr-app keeps its layout in the default
core's doc store (`docs.json`, doc `herdr-app`: spaces, tabs, blocks naming terminals by address), and the terminal it
made (`t1`) carries labels `herdr.space` and `herdr.tab`. A terminal made through the socket has neither.

Steps for the owner (about 5 minutes; they make one terminal in the default session and remove it):

1. Open herdr-app and note its spaces and tabs.
2. In a terminal outside herdr and outside any obor pane (Terminal.app), run
   `hc run --bg --name p7probe -- zsh` and note the address it prints.
3. Without touching herdr-app: does a new tile or tab appear? If so, where (which space and tab) and under what
   title? If not, how does herdr-app let a person add `p7probe` to the window?
4. `hc get p7probe --json | grep -E '"(cols|rows)"'`: the size before any viewer (200 and 120 expected).
5. If it shows, click into it, then run step 4 again: did the size change to the tile's?
6. Remove it: `hc kill p7probe; hc rm p7probe`.

The answers decide HD8: if each terminal must be added by hand, the plan stops at Stop A.

**Result (owner and lead, 2026-11-20 ~11:45, hc 0.1.21, herdr-app open on space `~`, tab `zsh`).** `hc run --bg
--name p7probe -- zsh` (typed in herdr-app's own terminal t1, so `parent: t1`, `die_with_parent: true`, and t1's
`where` label copied) made t3: nothing appeared in herdr-app; size 200×120. herdr-app's terminal t1 carries
`herdr.space=spa…` and `herdr.tab=tb…` (ids from its `docs.json` layout doc); t3 had neither. After
`hc label t3 herdr.space=<t1's> herdr.tab=<t1's>`, herdr-app showed t3 at once as a new pane in that tab, and t3's
size became the tile's (58×37, t1 57×37). So: a socket-made terminal shows in herdr-app when it carries an existing
space's and tab's ids as labels; with none it stays hidden. HD8 holds: obor can place its terminals into a herdr-app
tab by labels (ids read from herdr-app's layout doc or from a terminal already in that tab); making new spaces or
tabs would mean writing herdr-app's doc and is not planned. t3 removed afterwards.

## P10: core CPU with three idle agents

What ran: Claude (`done`), Codex `--no-daemon` and OpenCode `--standalone` (both `idle`) in one core, nobody
attached, `ps -o time` of the core and its holder once a minute for 15 minutes (10:56-11:11).

Core: 0:01.66 → 0:02.62 of CPU in 15 minutes, 0.064 s per minute, every one-minute step between 0.06 and 0.07 s
(`%cpu` 0.0-0.1); resident memory 11 → 7 MB. Holder: 0:00.74 → 0:01.36, 0.041 s per minute; 18 → 17 MB.
Extrapolated from the 15 minutes (not measured over an hour): about 3.8 s of CPU per hour for the core and 2.5 s for
the holder.
For scale, the agents themselves over 8.7 minutes of the same window: Claude 2.72 s (about 19 s per hour), OpenCode
1.49 s (about 10 s), Codex 0.63 s (about 4 s). A core with no terminals was not sampled, so the cost per agent inside
the core is not separated. Need 7 ("idle cost") is met: hc costs less than the idle agents it holds.

## No-go checks

| Check | Answer |
|---|---|
| Claude reports `blocked` reliably inside hc | **Yes.** Its trust dialog and every permission prompt reported `blocked`/`permission` within a second, with a `msg` naming the prompt, every time: 7 of 7 (3 by hand, 2 in each of the two program runs). Codex and OpenCode did too at their permission prompts |
| A send at `blocked` is refused before any byte is typed | **Yes.** Refused in ≤1 ms with kind `blocked`, the prompt unchanged, for all three kinds. Only for dialogs the program reports: Codex's trust and update prompts report nothing and are typed into |
| The scratch session leaves the default one alone | **Yes, as far as hc is concerned.** Core, holder, terminals and state files equal first and last. The one difference, `t1`'s size after herdr-app ended at 11:02:55 (P11), has no link found to the probes, but its cause is unknown. Any `hc` command that needs a core (`schema`, `server status`, `ls` …) starts one for its session when none runs, with the caller's env: the scratch session must be set before the first call |

None of the three is a no-go. Steps 2-5 can follow; HD8 waits for P7.

## What each fact changes in steps 2-5

Step 2 (client, fake, read side):

- `Snapshot.Instance`: core id plus the sorted holder `instance` values, not the core pid (P5). The pid changes on a
  restart that loses nothing; the holder set changes exactly when programs were lost, which is what should trigger the
  restore.
- The stream's endings (P5, P6): a restart is a bare EOF, a stop is `lost` (`server_stop`) then EOF, a stalled
  reader is EOF with no `missed`, a slow reader gets `missed` with a snapshot and stays connected, `seq` restarts at 0
  with each core process, `heartbeat` every 15 s. The four endings in the plan stand; EOF covers three causes, all a
  reconnect.
- The label filter (P6): a terminal that newly matches arrives as `labels` with `record`; one that stops matching
  sends one `labels` event without the filter's key and then nothing. The adapter must read that event as the pane
  leaving the group.
- Label spelling: `obor.project` and `obor.agent` work as written.
- Kind from a record (P1, P9): `status.app` (`claude-code`, `codex`, `opencode`) names the kind of a person-started
  agent; `foreground.name` does not (Claude's is its version, `2.1.329`).
- `hctest.Core`'s records and events can be the JSON captured here: `status` with `records` and `title`, `turn`
  events, `foreground` events.

Step 3 (write side and start):

- `Prompt` timeouts (P2): a send that starts a turn returns in under 1.6 s; one that does not, in about 5.2 s; with a
  person's draft open, up to 60 s. The bound must exceed 65 s, or the owner sets `draft_wait` lower in hc's config
  (an owner decision: the config is the person's too).
- A send appends to unsent input (P2): the plan's `clear` has to be done by obor before the send (keys), as on herdr.
- `Write` and the start (P2): 16 KB command lines arrive whole, so the env line needs no ~1 KB split on hc.
- `SendKeys` and `port.StopBound` (P3): 31-36 ms per call; herdr's bounds are enough. Key names are read without
  case, so obor's names must not rely on case.
- The "up" rule (P1): `reported` stays `true` after the agent exits, so up is "foreground is not the shell and status
  is not null", not `reported`.
- Trust dialogs (P1): Claude's reports `blocked`/`permission` "trust this folder", so the start can see it in the
  status; Codex's reports nothing, so the screen read of `internal/launch/start.go:122-137` stays for it.
- `WaitStatus` (P1, P6): a turn ends at `done` for Claude and Codex (which then stay `done`), but at `idle` for
  OpenCode, which never says `done`. Wait for the `turn` event or for the status to leave `working`, not for `done`.
  Treat `error` as the plan says; OpenCode reports an API error as `idle`.
- `ProcessInfo`, `Busy` (P9): `foreground` never shows an agent's tool; `Busy` is `status: working`.
- Restore (P8): a labelled kept terminal whose shell exits with a code is `finished` for good, so a pane can end by a
  person typing `exit`. The restore must place a new terminal for it rather than wait for a `restarted`.

Step 4 (selectable, identity, the env guard):

- The guard's read is `machine.exec {argv: ["/usr/bin/env"]}` (P4): it returns the core's env.
- Risk 2 confirmed (P4), and widened: every `hc` CLI call that needs a core starts one with the caller's env, `hc
  schema` included (P11). The guard and HD9 stand; the nightly schema line (step 6) must run `hc schema` from a
  scrubbed env in a scratch session, or read a running core.
- `env: {"NAME": ""}` sets an empty value, never unsets (P4): obor cannot remove an inherited name per terminal through
  `terminal.run`. Its typed env line (`unset …`) still can, and an empty `OBOR_`/`HERDR_` name must read as unset.
- The pane variable is `HC_TERMINAL_ID` (P4, P1: commands an agent runs see it when the kind runs without a shared
  server).
- The core's `XDG_*` names reach every terminal (P4, P1): on a scratch-session core (the live tier's), OpenCode loses
  its config, model and service. The live tier has to set `XDG_CONFIG_HOME` and `XDG_STATE_HOME` back per terminal,
  as P1 did.
- Codex `--no-daemon` and OpenCode `--standalone` are required on hc, not only advised (P1). obor passes both today;
  `obor config validate` could refuse a codex or opencode model without them on an hc project.

Step 5 (status for a kind that reports none):

- Not needed for status words: all three kinds report OSC 7501, `blocked` included (P1).
- What is left is two dialogs that report nothing, both Codex's: its trust dialog (handled by the start's screen read,
  above) and its update prompt (a start with a first prompt or a send right after the start answers it). The second
  is a launch concern on herdr too; on hc it is worth one upstream ask to Codex, or a start that reads the screen
  until `idle` before the first send. Step 5 as written can be dropped.
- Upstream asks to add to the plan's list: Codex reporting its trust and update prompts; OpenCode reporting `done`
  and `error`.

## Side effects of this run

- Codex was upgraded 0.169.0 → 0.171.0 by Homebrew (see P1: a send answered Codex's unreported update prompt). Not
  reverted.
- Claude, Codex and OpenCode each recorded the scratch folder `/private/tmp/obp/1/work/proj` (trust entries, a
  session) in the owner's own state; OpenCode's sessions there are in the owner's `~/.local/state`.
- Files the agents created outside the scratch dir (`~/obor-probe-outside.txt`, `~/obor-probe-oc.txt`) were removed.
- The Codex app-server daemon the first `codex` started was stopped (`codex app-server daemon stop`). Every probe
  core was stopped; `/tmp/obp` and the program's `/tmp/obp-*` dirs removed.
- herdr-app (Herdr.app) ended at about 11:02:55, cause unknown (P11).

## The program run

`make conformance TIER=live KIND=hc TURN=1`, 11:13-11:15, the run on the tree submitted (the scratch dir and the
core ids are the run's own):

```
note: hc 0.1.21
WARN HC-P11 hc schema with no core running prints the schema but starts a core for the session (with the caller's environment): the static tier must run it from a scrubbed env and a scratch session (docs/plan/herdr-core.md P11)
PASS HC-P4 a terminal of a scrubbed core gets the core's env plus COLORTERM GH_PAGER GIT_PAGER HC_TERMINAL_ID HC_TERMINAL_SOCKET PAGER TERM TERM_PROGRAM; HERDR_/OBOR_ names: [] (docs/plan/herdr-core.md P4)
PASS HC-P4 env {"NAME": ""} sets it empty, never unsets: OBOR_PROBE set true "", LOGNAME ""; TERM_PROGRAM=herdr is overridden by the terminal identity: "ghostty" (docs/plan/herdr-core.md P4)
WARN HC-P4 a core started from a shell carrying HERDR_PANE_ID, OBOR_ROLE, OBOR_PANE gives every new terminal [HERDR_PANE_ID OBOR_PANE OBOR_ROLE]; machine.exec of env reads the core's env: [HERDR_PANE_ID OBOR_PANE OBOR_ROLE] (docs/plan/herdr-core.md P4, risk 2)
PASS HC-P4 hc server restart --fresh from a scrubbed shell replaces it: HERDR_/OBOR_ names after: [] (docs/plan/herdr-core.md P4)
PASS HC-P2 a send to a quiet shell with nobody attached returns in 873ms and runs: true (docs/plan/herdr-core.md P2)
WARN HC-P2 a send after unsent input appends to it (no clear): keys `echo LEFT` then send `echo X` ran `echo leftecho X`; keys take uppercase letters as lowercase key names (docs/plan/herdr-core.md P2)
PASS HC-P2 a two-line send at a zsh prompt runs both lines: true (docs/plan/herdr-core.md P2)
PASS HC-P2 a 4000-byte command line at a shell arrives whole: true (docs/plan/herdr-core.md P2, internal/launch/launch.go:868)
PASS HC-P2 a 16000-byte command line at a shell arrives whole: true (docs/plan/herdr-core.md P2, internal/launch/launch.go:868)
PASS HC-P3 terminal.keys at a quiet shell, one call each: esc 32ms, ctrl+c 32ms, enter 31ms (docs/plan/herdr-core.md P3)
PASS HC-P3 terminal.keys at a shell printing every 200 ms: esc 32ms, ctrl+c 32ms, enter 32ms (no 3 s quiet wait for a program that reports no status) (docs/plan/herdr-core.md P3)
PASS HC-P9 at the shell: pid 66651, foreground zsh 66651 (docs/plan/herdr-core.md P9)
PASS HC-P9 running `sleep 30`: pid 66651 (the shell), foreground sleep 66933 (docs/plan/herdr-core.md P9)
PASS HC-P5 holder killed: kept t1 events [lost restarted] (stream open: true), now pid 67050 cwd /private/tmp/obp-2782595009/5/work/sub labels map[obor.agent:w1 obor.project:probe]; plain t2 events [finished], reason holder_lost (docs/plan/herdr-core.md P5)
PASS HC-P5 hc server restart: programs keep running true; the stream ends (EOF) true; a new subscribe starts at seq 0; core.id 89662116a0916ac1e33cb73e2339acc5→89662116a0916ac1e33cb73e2339acc5, core pid 66950→67151, holder instances [485446de6b621a5678cb7ce3b359f186]→[485446de6b621a5678cb7ce3b359f186] (docs/plan/herdr-core.md P5)
PASS HC-P5 hc server stop then start: the stream got [lost t1 lost t1] and ended true; the kept terminal is back live as a shell (pid 67289, labels map[obor.agent:w1 obor.project:probe], cwd /private/tmp/obp-2782595009/5/work/sub); core.id 89662116a0916ac1e33cb73e2339acc5, holder instances [485446de6b621a5678cb7ce3b359f186]→[6b0f43aea512fe9a53adff3783852d0b] (docs/plan/herdr-core.md P5)
PASS HC-P6 label keys taken: "obor.project" "obor/project" "obor_project" "obor project" "Obor.Project"; refused: "a=b" (invalid_params) "" (invalid_params) (values are free text) (docs/plan/herdr-core.md P6)
PASS HC-P6 a stream filtered on obor.project=probe, labels set in turn: map[obor.project:probe]→[labels+record]; map[obor.agent:w1]→[labels]; map[obor.project:]→[labels]; map[obor.agent:w2]→[]; map[obor.project:probe]→[labels+record] (docs/plan/herdr-core.md P6)
PASS HC-P6 a subscriber reading one line per 20 ms through a 4 KB buffer while 3000 label events arrive: map[event:757 missed:2] (an EOF entry: the core closed it) (docs/plan/herdr-core.md P6)
PASS HC-P8 terminal.run with no caller and no no_parent: parent null (docs/plan/herdr-core.md P8)
PASS HC-P8 `hc run --bg` typed in shell t2: child t3 parent t2; typed into the shell after the child ended: false (docs/plan/herdr-core.md P8)
WARN HC-P8 `exit` typed at a kept shell: the terminal is finished (exited), keep does not bring it back; its running child t4 is finished (killed) (docs/plan/herdr-core.md P8)
PASS HC-P1 Claude's trust dialog: blocked/permission "trust this folder", app "claude-code", reported true (docs/plan/herdr-core.md P1)
PASS HC-P2 a send at blocked: refused with kind "blocked", the prompt still up after it: true (docs/plan/herdr-core.md P1, no-go)
PASS HC-P1 one turn: the send returned in 1.567s; status words [working done] (docs/plan/herdr-core.md P1)
PASS HC-P1 Claude's permission prompt: blocked/permission "approve Bash: touch probe-file.txt" (docs/plan/herdr-core.md P1)
PASS HC-P2 a send at blocked: refused with kind "blocked", the prompt still up after it: true (docs/plan/herdr-core.md P1, no-go)
PASS HC-P1 Claude exited to the shell: status null, reported true (docs/plan/herdr-core.md P1)
SKIP HC-P1 codex and opencode run by hand (dialogs and their own background servers): docs/research/hc-probes.md P1 (docs/plan/herdr-core.md P1)
SKIP HC-P7 needs a person at herdr-app: the owner's steps are in docs/research/hc-probes.md P7 (docs/plan/herdr-core.md P7)
SKIP HC-P10 an idle-CPU sample takes 15 minutes or more: run by hand, docs/research/hc-probes.md P10 (docs/plan/herdr-core.md P10)
PASS HC-P11 the default session is unchanged after the probes: core 0295b50c7057fa780e539fb313a24b5a pid 52026, live [t1:51393]; files map[core.json:7289b65b5a65b268 core.lock:c991234a064d1877 docs.json:ef996abd9208ddfd]; config dir false (docs/plan/herdr-core.md P11)
conformance: 26 pass, 0 fail, 3 skip, 4 warn
```

## Trial (docs/plan/hc-trial.md, owner + lead, 11-20)

- Before: outside the obor-kit checkout `obor` runs the pinned release; v0.40.7 had no hc (`FAIL terminal.kind "hc":
  obor knows herdr`). v0.41.0 shipped for the trial.
- Step 1: init refuses a repo with no commit holding a config it did not write; the config is committed first.
- Step 2: `obor init` on the hand-written config added agents, lanes, BACKLOG and docs but no
  `project.agent_prefix`; `obor lead` then derived an empty lead name and matched an unlabelled hc terminal `t1`
  (`pane t1 (from label)`, `FAIL bad agent name ""`), nothing touched. T-1374. Workaround: set the prefix by hand.
- Step 2 (after the prefix by hand): serve under launchd on hc, lead `hct-lead` in herdr-app's `hc-trial` space with
  `lead`, `lanes` and `roles` tabs; the space's first shell tab stays open beside them, as herdr's does at a first
  `obor lead`. The lead at its first question shows `blocked` (herdr-app "Needs you"); the web page showed the
  project idle, as it does on herdr (needs-you there is serve's records only): T-1375.
- Step 3: models confirmed and committed by the trial lead (integrator sonnet, reviewer codex terra:xhigh, 2 lanes).
  `obor send hc-trial` from obor-kit reached the trial lead's hc terminal ("posted to its session").
- Step 4: `obor setup check` OK (core env holds no HERDR_/OBOR_ name); `hc ls`: one terminal per lane and role pane,
  each in its worktree, under `hc-trial / lanes` and `hc-trial / roles`.
- Step 5: lane.prepare failed once: `.claude/settings.json` was obor's link in every worktree, then committed by the
  trial lead, and `checkout -B` refused to overwrite the untracked symlink. The links removed by hand left the
  integrator's worktree (serve never moves it; still at the first commit) with no plugin settings, so its Claude had
  no obor MCP and no `job_next`; serve retried the job at +5 min and would raise needs-you at +10 (as designed).
  Both in T-1376. After the repair: worker start, `task_done`, gate GREEN, integrator ACCEPT, `MERGED T-01 be1793b`,
  worker and integrator stopped. The "runs session X, not Y (the id obor chose)" line also shows on herdr: not hc.
  Small: lane.prepare's failed fact recorded three times for one attempt; the publish result's candidate fields empty.
