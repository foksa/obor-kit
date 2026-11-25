# Tool gotchas

Each line: symptom → what to do. Add new ones with a date; delete when fixed. Inherited from axis-alies unless dated
otherwise.

## herdr
- Prompt text in a double-quoted shell variable must not contain backticks (command substitution runs them). Use plain
  words or single quotes.
- A prompt sent right after `/new` / `/clear` is sometimes swallowed → retry until the agent is `working`
  (`obor dispatch` does this).
- `herdr agent wait` watchers fire falsely after panes are moved → re-check `herdr agent get`.
- Never poll in the foreground or in a background loop; obor serve tells you what needs you.
- A pane only shows the tail of long answers → Codex: read the last message from
  `~/.codex/sessions/**/rollout-*.jsonl`; Claude: ask for the full answer in one message or a file.
- A split pane inherits the neighbour's cwd → pass `--cwd` (and `--env OBOR_ROLE=…`) to `herdr pane split`.
- Agent names are global across workspaces → always use the project prefix (`<project.agent_prefix>-…`).
- After a herdr restart, obor serve restores obor panes (`obor restart`, log `~/.obor/restore.log`), replacing an
  agent herdr resumed there without obor's env; `resume_agents_on_restore = false` under `[session]` in herdr's
  config.toml (optional, user-global) saves that double start.
- A pane labelled an obor agent without its env (herdr resume on, a hand start) → SessionStart warns "run obor restart
  <agent>" → `obor restart <agent>` from another pane.
- An abandoned task (or a finished review / smoke test) leaves its lane `done` / `working`, so dispatch refuses it
  (2026-10-11) → `obor drop <T-xx> "<why>"` (lead only; `--discard` for a dirty lane or unmerged commits), not by hand.

## hc (herdr-core, `project.terminal: "hc"`)
- Any `hc` call that needs a core starts one with the caller's environment, and every terminal gets the core's: one
  call from an obor pane while no core runs gives every later terminal that pane's `HERDR_`/`OBOR_` names (2026-11-20)
  → serve and `obor setup check` refuse such a core; run `hc server restart --fresh` from a shell outside any pane.
  Serve starts a core that is down itself, from a scrubbed environment.
- `hc run`, `kill`, `send`, `keys`, `label`, `attach`, `server …`, `update` and `herdr-app do` are denied in every
  agent's pane but the lead's (2026-11-20) → the lead dispatches; reads (`hc ls`, `get`, `read`, `--help`, `herdr-app
  ls`) are fine.
- A send waits for a person's unfinished draft in that terminal, up to hc's `draft_wait` (60 s) → don't leave text
  typed in an agent's terminal obor is about to prompt.
- Codex's trust and update prompts report no status in hc (2026-11-20) → the start reads the screen for the trust
  dialog as on herdr; answer an update prompt in the pane.

## Rex (`project.terminal: "rex"`)
- `rex` inside any block reaches the default server, the one obor drives (no address variable is set; 2026-11-25) →
  `rex` commands that make, end, type into or relabel sessions, windows and blocks, a write method's `api call` and
  `server …` changes are denied in every agent's pane but the lead's; reads (`rex ls`, `capture`, `events`, `wait`,
  `session`/`window`/`block ls` and `inspect`, `api describe`, `--help`) are fine.
- No `rex` on `PATH` (the CLI ships inside the app bundle) → `obor setup check` and serve fail naming the directory to
  put on `PATH`: `/Applications/Rex Beta.app/Contents/Helpers/Rex Server.rex/Contents/MacOS`.
- A block inherits the Rex server's whole environment, a server started from an obor pane included (2026-11-25) →
  obor's own blocks drop those names; a block you open by hand keeps them: start agents through obor.

## Claude Code
- Workers often run long checks as a background shell and go idle → the idle state proves nothing: the worker's
  `task_done` (obor serve's submission, `obor status --facts`), not the idle state, is the signal.
- Auto mode can't check `bash -c '…'` scripts that contain `rm` → a permission prompt that auto-denies after ~2 min.
  Write probes to a script file in the scratchpad and run it.
- Spawning panes/agents can be blocked by the auto-mode classifier → ask the user.
- An agent can't `/clear` itself; send `/clear` via `herdr agent prompt <pane id> "/clear"` — the pane id, not the
  agent name (herdr drops a name at times, and obor never reads it). If the pane shows the Background tasks
  panel, send `esc` first — only while the agent is idle: Esc interrupts a working turn.
- TaskStop on a background shell can leave its child script running → check by script path, kill by PID.
- Claude panes except leads start with Remote Control and auto memory off (only the lead on the phone and with auto
  memory), on their own settings file (`--setting-sources project,local --settings .obor/state/settings/<agent>.json`:
  the user's settings with the project's `.obor/claude/settings_override.json` merged over them);
  `agents.kinds.claude.args` adds args to every Claude start.
- Ctrl+Z suspends Claude Code (2026-10-10) → type `fg` in the pane. `obor restart` refuses a stopped job, so `fg`
  and `/exit` it (or end it), then `obor restart <agent> --fresh`.
- After a usage-limit reset Claude Code resumes the session by itself ("Your claude.ai usage limit has reset. Continue
  …", 2026-10-11); the wrong-tab guard lets that turn through, so an idle worker after a reset is a different problem.
- Hooks only cover Claude Code panes; Codex/opencode lanes rely on their sandbox and the worker prompt, so the
  integrator's diff review (placement against the module map) matters.

## Codex CLI
- Can self-update on start and exit to the shell → confirm the input box before prompting.
- Can't write git metadata in worktrees → never commits (fine: the integrator commits).
- Can't `git checkout` inside a linked worktree (the parent's `.git/worktrees/<wt>/index.lock`) → a Codex reviewer or
  deep dive reads another commit with `git show <sha>:<path>` / `git diff`; serve moves its role worktree to the head
  it hands it. Its sandbox writes the cwd only: its start adds the main checkout's reviews dir (`--add-dir`).
- No network for installs.
- First Codex start in a new repo shows a "Trust this folder?" dialog → `herdr agent start` reports `agent_not_ready`
  (blocked); answer it (Enter = trust) once per repo.

## Prompt cache and context size
- An idle pane is free, but its next prompt after cache expiry re-reads the whole context at full price → keep
  sessions short: every dispatch starts a fresh worker, the reviewer gets a fresh session per review
  (`obor brief <reviewer> --fresh "…"`), `/clear` the integrator when it holds no job and advisors per topic.
- Above `agents.handoff_tokens` (default 500k) each prompt reminds the lead to hand off; nothing forces it, the
  lead picks the next logical break, writes the Handoff and runs the handoff restart (`obor restart <lead> --handoff`).

## bash
- macOS `/bin/bash` is 3.2: no associative arrays, no `mapfile`, no `${var,,}`; `stat -f %m` (BSD) vs `stat -c %Y`
  (GNU); `sed -i ''` vs `sed -i`.
- macOS has no `timeout` binary → use the tool the project provides.

## This project
<!-- fill in: quirks specific to this project's stack and toolchain (symptom → what to do), dated. -->
