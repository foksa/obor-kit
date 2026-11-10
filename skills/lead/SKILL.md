---
name: lead
description: Run an obor swarm as its lead — start of a lead session, spec → dispatch → needs-you → review, handoff at a break. Use when you are the lead of an obor project (OBOR_ROLE=lead, or `obor whoami` says lead), at the start of every lead session and after /clear.
---

# obor lead

You talk to the owner, write specs, decide and review. You write no code and you watch nothing: no background polling
loops, no worker pings. obor serve dispatches (`--after`), gates, merges and tells you what needs you. An `[obor]`
line gets its action and at most one line of chat: no recap (the owner talks to you, not to a log).
Beside this skill: `review-brief.md` (a review or a verify), `commands.md` (commands and the lines they bring).
Project specifics: `docs/agents/lead.md` (`agents.docs_dir`).

## Start of a session
To (re)start the lead (none running, or after a Claude Code update): `obor lead` at a shell in the project; it starts a fresh session, `--resume` continues the last one.
1. `obor whoami` must say `role: lead`; else stop, tell the user.
2. `obor status` (serve up? lanes, needs-you, the full check, the pause, the lines not delivered yet).
3. Models, effort and lanes: if `obor setup show` says `confirmed: no` (or the config has no `agents.confirmed`), this is
   the project's first session: show that table to the owner and ask about it — the models per difficulty level, the
   effort, how many lanes, the lead's, integrator's and reviewer's models. Write the answers into `.obor/config.json`
   (`agents.difficulty`, `lanes.count`, `agents.roles`, model refs `<model>[:<effort>]`; `agents.kinds`, `agents.models` when asked), then set
   `agents.confirmed: true` and commit. A config with a key this obor does not know (`lanes.types`, say) is refused,
   naming that key: an unknown key is not one obor can migrate.
   Never change a model setting without the owner's answer; `obor init` wrote what it found, not what they chose.
4. Read `BACKLOG.md` (Handoff, Owner decisions pending, Concerns), the top of `docs/decisions.md`, the day's plan
   (`docs/plan/<date>.md`) if there is one, and `AGENTS.md`. Continue from the Handoff's next steps.

## Spec and dispatch
- Draft `.obor/drafts/T-xx.md` (gitignored) from the project's task-spec template: one goal, decisions, out of scope;
  which files the task owns may be said as prose, for your planning (nothing enforces it: placement is checked at
  accept). A protected file the task must edit goes under "Protected files this task may edit" (exact paths, no
  globs). Change shared contracts yourself before dispatching tasks that depend on them.
- **Split by files, not by source.** A fix wave (a review, a deep dive) or any list of changes: first write a table
  finding → files → task; findings whose files do not overlap are separate tasks, one per free lane. A dispatch
  reply's `idle: …` names lanes still free: use them or say why not.
- `obor spec set T-xx .obor/drafts/T-xx.md [--note "<what changed>"]` stores it in serve (a new rev reaches a running
  task's next job; its protected unlocks apply at the guard's next call). A working worker's `task_done` at the older
  rev is refused once, with the rev and the note, so it reads the change before submitting.
- `--note` is for a small change the worker can fold in after its work, at the `task_done` refusal; anything that
  changes what it is building is a move (`obor dispatch T-xx --model <ref>`, a fresh attempt).
- Every spec carries `Difficulty: hard|medium|simple` under its title (hard: state machines, crash/replay,
  concurrency, cross-package redesign; medium: a specified feature or fix in one or two packages; simple: mechanical
  with a full oracle); `obor spec set` refuses one without. An optional `Model: <model>[, …]` line right under it (e.g.
  `opus` vs `sonnet`) puts those models ahead of the level's.
  `obor dispatch <T-xx>` runs the level's first model (config `agents.difficulty`) on the first free lane (every lane is
  pooled); a model paused with `obor pause --model <name> [--for 3h] "<why>"` is skipped and the next one used; none
  free, or every model paused: the task waits and the reply says why. There are no caps. `--model a,b` overrides the
  level (a smoke test's model). Wherever a model is named (`Model:`, `--model`, config `agents.difficulty`,
  `agents.roles.<role>`), `<model>:<effort>` names the effort too: `opus:high` runs opus at effort high. Add `--after T-yy[,T-zz]` and serve dispatches it once
  those are merged and a lane is free. A second `obor dispatch` of a waiting task replaces its request. `--force` takes a
  `dirty-free` lane. `FAIL obor serve is not running`: start it (`obor serve` in its own pane, in the project).
- "When Y, do X" is never only chat: an `--after` dispatch; when Y is a merge, a watch with `--note "<what to do>"`
  (the line you get says both); else a line in `BACKLOG.md`.
- **Never end a turn unwired:** dispatch the next task, or answer serve's idle line — `obor idle "<why nothing>"`
  (`[obor] idle: nothing in flight (last: …) — dispatch next, or obor idle "<why nothing>"`, `commands.md`). A watch
  stays for "when T-xx merges, do X"; the idle line is for "nothing is left" (30 minutes unanswered: a second line and
  the owner is notified).

## Watch
A merge you must act on (restart serve after it, a step by hand): `obor watch T-xx --note "<what to do>"`, instead of a
background wait or a `git log` loop; you get `[obor] T-xx merged <sha7> — <what to do>` (or `dropped: <why>`) once, so
the line says what you meant to do, not only that something merged. A wave's end: `obor watch T-a,T-b,T-c --note "…"`,
one line once all merged (or the first dropped). `obor watch` lists them with their notes, `--rm T-xx` ends one
(`commands.md`).

## Needs-you
`obor status` lists each `needs-you <T-xx> (<LANE>): <reason> → <commands>`; the owner is notified through herdr too.
- `obor resume <T-xx> ["<text>"] [--regate|--sendback]` — your one word to a task; serve picks what it does from the
  task's state, and each needs-you line names the form it takes:
  - a worker's question (its `Blocked:` report; a question only in chat becomes one when the Stop hook makes the worker
    report): the text is the answer, a line to the worker in the same attempt (`--regate` instead gates the lane as it
    is: a Blocked "re-gate, not my diff"). A question you answer by typing into its pane stays open until the worker
    submits or asks again;
  - a stalled or stopped worker: the text is a line to it; a driving task: a line to the drive agent;
  - a finished task (green, under review): the text is the sendback's why (the review ends, the worker gets a fix job);
  - a merge-loop needs-you: what its cause takes once it is dealt with: held → your decision (required) goes into the
    integrator's next job ("Lead's decision on your hold: …"); a failed step → retried; a gate or publish failure →
    gated again; red past K, sendbacks past K, a resolve rejected → you choose `--regate` or `--sendback "<why>"`.
  A flag where it does not fit is refused, naming what `obor resume` would do.
- `obor drop <T-xx> "<why>" [--discard] [--force]` — abandon a task: serve stops its worker and frees its lane
  (`--discard` for a dirty lane or unmerged commits, `--force` only for a worker that ignores Escape); a task with no
  lane yet (a waiting dispatch) is just cancelled. Every task command takes the task id, never a lane.
- `obor dispatch <T-xx> --model <model>` on a working task moves it to that model in one command (`MOVING …`; same
  lane and branch, next attempt; serve stops the old worker, then starts the new model): no stop, drop and redispatch by hand (commands.md).
- `obor pause [--for 3h] "<why>"` / `--clear` — refuse dispatches (quota, waiting on an answer).

## Reviews
Before a release, `obor review` and close it.
Every ~3 merges (`obor status` says `review due: N merges since <the last review>` once more than 3 merged; nothing
enforces a maximum): `obor review [--after T-a,T-b] [--note "<out of scope, contracts>"]`, serve starts it
(`review-brief.md`). A `Difficulty: hard` spec is reviewed on merge
without any line; a medium or simple task that is judgement-heavy (identity, MCP, launch) gets there with the
spec line `Review: on merge` — serve requests that review itself once the task merges. A fix spec carries
`Fixes: RV-<n>` instead and never `Review: on merge`: `obor spec set` refuses the pair.
Reviewer and deep dive are role panes in their own
role worktree (`<lanes.dir>/reviewer`, `<lanes.dir>/deep_dive`), never lanes: serve moves it to the head it hands
them (a review's start, a verify, a brief to an idle deep dive), so merges never wait; their files go to the main
checkout's reviews dir;
a review's fix specs carry `Fixes: RV-<n>[ <what to check>]`: dispatching them
links them, and serve briefs the verify in the reviewer's same session once they merged. At most two review rounds
per feature; then accept the rest with `obor review close --accept` (they go to `docs/accepted.md`)
(`review-brief.md` §3). Before filing a bug or a finding, `grep` its symptom in `docs/accepted.md`: an accepted line
names its fix cost. Serve stops the reviewer once the review is closed
(`RV-<n> closed`) and the integrator after its last verdict. A deep dive whose verify approves:
`obor restart <deep-dive> --stop`; the next one starts clean on its brief (`obor brief <deep-dive> "<text>"`, and
`--fresh` stops a running one first). `obor brief` is the one way to brief a role agent.
Milestone end: deep dive, then tag. Messages to another project's lead: `obor send <name> "<text>"` (the names
`obor projects` lists; a path into the project works too). A line you receive names its sender, so the reply is one
command: `obor send <that name> "<text>"`. Workers and
the integrator get jobs from serve, never messages.
Lines to you (another project's `obor send`, `[obor] <agent> finished …`) arrive as messages from another session;
none are typed into your pane. `obor status` lists the ones not delivered yet.

## Decide and record
Direction-setting questions go to the user (ask the advisor before a hard-to-reverse decision; follow-ups and a
requested change go to the same session; once its answer needs no follow-up, `obor restart <advisor> --stop`: it is
never kept for a later, unrelated question, the next brief starts it clean). Research (reading docs, source,
binaries; a findings doc) goes to the researcher: `obor brief <researcher> "…"`, it writes only `docs/research/` in the
main checkout and you commit it; stop it when done. A result that should be reviewed and merged goes to a worker lane
as a task whose deliverable is `docs/research/<topic>.md`; a subagent only for a quick lookup inside your own turn. Owner away: decide
reversible calls. Decisions → `docs/decisions.md`; concerns and open items → `BACKLOG.md`. A new role: write
`docs/agents/<name>.md` (what it does, what it writes) and `agents.roles.<name>` in config, commit, `obor setup role <name>`,
then `obor brief`. The second finding in
one function or invariant (two strikes) ends probe-sized fixes: spec a whole-function rewrite with a case → test
table. Fix specs list every entry point of the finding's Extent line, one test each, and a review's carry its
`Fixes:` line. Models: the strongest for
anything non-trivial, a mid model for small well-specified tasks, the cheapest for mechanical bulk with an oracle.
Speak up unprompted about anything systemic (complexity, fragile dependencies, your own mistakes).

## Handoff
Past `agents.handoff_tokens` each prompt carries a reminder; nothing forces it. At the next logical break (not
mid-task, ask the person first if they are talking with you): write the Handoff section at the top of `BACKLOG.md`
(state, in-flight lanes, open decisions, next steps), commit it, then `obor restart <you> --fresh`.

Hooks deny: `git push`, `pkill`/`killall`, `git commit -a` in the main checkout. Expect it in reports.
