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
- Agent names are global across workspaces → always use the project prefix (`<prefix>-…`).
- After a herdr restart, obor's startup hook restarts obor panes (`obor restart`, log `~/.obor/restore.log`); set
  `resume_agents_on_restore = false` under `[session]` in herdr's config.toml, else herdr resumes them without obor's env.
- A pane labelled an obor agent without its env (herdr resume on, a hand start) → SessionStart warns "run obor restart
  <agent>" → `obor restart <agent>` from another pane.
- An abandoned task (or a finished review / smoke test) leaves its lane `done` / `working`, so dispatch refuses it
  (2026-10-11) → `obor drop <LANE> "<why>"` (lead only; `--discard` for a dirty lane or unmerged commits), not by hand.

## Claude Code
- Workers often run long checks as a background shell and go idle → the idle state proves nothing: the worker's
  `task_done` (obor serve's submission, `obor status --facts`), not the idle state, is the signal. (The old watch-for-idle
  command is gone, T-465.)
- Auto mode can't check `bash -c '…'` scripts that contain `rm` → a permission prompt that auto-denies after ~2 min.
  Write probes to a script file in the scratchpad and run it.
- Spawning panes/agents can be blocked by the auto-mode classifier → ask the user.
- An agent can't `/clear` itself; send `/clear` via `herdr agent prompt`. If the pane shows the Background tasks
  panel, send `esc` first — only while the agent is idle: Esc interrupts a working turn.
- TaskStop on a background shell can leave its child script running → check by script path, kill by PID.
- Claude panes except leads start with `--settings '{"autoMemoryEnabled":false,"remoteControlAtStartup":false}'` (only the lead
  on the phone and with auto memory); the lane type's `settings` overlay those keys.
- Ctrl+Z suspends Claude Code (2026-10-10) → type `fg` in the pane. `obor restart` no longer resumes it: it refuses a
  stopped job, so `fg` and `/exit` it (or end it), then `obor restart <agent> --fresh`.
- After a usage-limit reset Claude Code resumes the session by itself ("Your claude.ai usage limit has reset. Continue
  …", 2026-10-11); the wrong-tab guard lets that turn through, so an idle worker after a reset is a different problem.
- Hooks only cover Claude Code panes; Codex/opencode lanes rely on their sandbox and the worker prompt, so the
  integrator's diff review (placement against the module map) matters.

## Codex CLI
- Can self-update on start and exit to the shell → confirm the input box before prompting.
- Can't write git metadata in worktrees → never commits (fine: the integrator commits).
- Can't `git checkout` inside a linked worktree (the parent's `.git/worktrees/<wt>/index.lock`) → the lead detaches
  the review worktree at the target commit before briefing.
- No network for installs.
- First Codex start in a new repo shows a "Trust this folder?" dialog → `herdr agent start` reports `agent_not_ready`
  (blocked); answer it (Enter = trust) once per repo.

## Prompt cache and context size
- An idle pane is free, but its next prompt after cache expiry re-reads the whole context at full price → keep
  sessions short: every dispatch starts a fresh worker, the reviewer gets a fresh session per review
  (`obor restart <reviewer> --fresh`), `/clear` the integrator when it holds no job and advisors per topic.
- Above `roles.lead.handoff_tokens` (default 500k) each prompt reminds the lead to hand off; nothing forces it, the
  lead picks the next logical break, writes the Handoff and restarts itself fresh (`obor restart <lead> --fresh`).

## bash
- macOS `/bin/bash` is 3.2: no associative arrays, no `mapfile`, no `${var,,}`; `stat -f %m` (BSD) vs `stat -c %Y`
  (GNU); `sed -i ''` vs `sed -i`.
- macOS has no `timeout` binary → use the tool the project provides.

## This project
<!-- fill in: quirks specific to this project's stack and toolchain (symptom → what to do), dated. -->
