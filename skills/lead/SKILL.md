---
name: lead
description: Run an obor swarm as its lead — start of a lead session, spec → dispatch → needs-you → review, handoff at a break. Use when you are the lead of an obor project (OBOR_ROLE=lead, or `obor whoami` says lead), at the start of every lead session and after /clear.
---

# obor lead

You talk to the owner, write specs, decide and review. You write no code and you watch nothing: no background polling
loops, no worker pings. obor serve dispatches (`--after`), gates, merges and tells you what needs you. An `[obor]`
line gets its action and at most one line of chat: no recap (the owner talks to you, not to a log).
Beside this skill: `review-brief.md` (a review or a verify), `commands.md` (commands and the lines they bring).
Project specifics: `docs/agents/lead.md` (`paths.agents`).

## Start of a session
To (re)start the lead (none running, or after a Claude Code update): `obor lead` at a shell in the project; it starts a fresh session, `--resume` continues the last one.
1. `obor whoami` must say `role: lead`; else stop, tell the user.
2. `obor status` (serve up? lanes, needs-you, the full check, the pause, the lines not delivered yet).
3. Models, effort and lanes: if `obor setup show` says `confirmed: no` (or the config has no `setup.confirmed`), this is
   the project's first session: show that table to the owner and ask about it — the models per difficulty level, the
   effort, how many lanes of each type, the lead's, integrator's and reviewer's models. Write the answers into
   `.obor/config.json` (`difficulty`, `lane_types`, `lanes`, `roles`), then set `setup.confirmed: true` and commit.
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
- `obor spec set T-xx .obor/drafts/T-xx.md` stores it in serve (a new rev reaches a running task's next job; its
  protected unlocks apply at the guard's next call).
- Every spec carries `Difficulty: hard|medium|simple` under its title (hard: state machines, crash/replay,
  concurrency, cross-package redesign; medium: a specified feature or fix in one or two packages; simple: mechanical
  with a full oracle); `obor spec set` refuses one without. An optional `Type: <lane type>[, …]` line right under it (e.g. `opus` vs `sonnet`) puts those types ahead of the level's: a free lane of the named type wins, else the level's first free lane; `--types` overrides it. `obor dispatch <T-xx>` takes the first free lane of the
  level's types that have a lane (config `difficulty`; a level with no lane at all uses medium's, then hard's, and the
  reply says so); none free: the task waits for one, never for a higher level, and the reply says so.
  `--types a,b` overrides the level (smoke tests, Haiku). Wherever a type is named (`Type:`, `--types`, config
  `difficulty`, `roles.*.type`), `<type>:<effort>` names the effort too: `opus:high` runs on an opus lane at effort high. Add `--after T-yy[,T-zz]` and serve dispatches it once
  those are merged and a lane is free. A second `obor dispatch` of a waiting task replaces its request. `--force` takes a
  `dirty-free` lane. `FAIL obor serve is not running`: start it (`obor serve` in its own pane, in the project).
- "When Y, do X" is never only chat: an `--after` dispatch; when Y is a merge, a watch with `--note "<what to do>"`
  (the line you get says both); else a line in `BACKLOG.md`.

## Watch
A merge you must act on (restart serve after it, a step by hand): `obor watch T-xx --note "<what to do>"`, instead of a
background wait or a `git log` loop; you get `[obor] T-xx merged <sha7> — <what to do>` (or `dropped: <why>`) once, so
the line says what you meant to do, not only that something merged. A wave's end: `obor watch T-a,T-b,T-c --note "…"`,
one line once all merged (or the first dropped). `obor watch` lists them with their notes, `--rm T-xx` ends one
(`commands.md`).

## Needs-you
`obor status` lists each `needs-you <T-xx> (<LANE>): <reason> → <commands>`; the owner is notified through herdr too.
- `obor answer <T-xx> "<text>"` — a worker's question (its `Blocked:` report; a question only in chat becomes one when the Stop hook makes the worker report); the answer
  reaches the worker as a line in the same attempt. A question you answer by typing into the worker's pane stays
  open until the worker submits or asks again.
- `obor resume <T-xx> [regate|reprompt] ["<decision>"]` — a step that stopped, once its cause is dealt with; for a
  held review, the decision text goes into the integrator's next job ("Lead's decision on your hold: …"); for open
  questions the decision is required and reaches the worker too.
- `obor sendback <LANE> "<why>"` — fixes to a finished task (its review ends, the worker gets a fix job).
- `obor drop <LANE> "<why>" [--discard] [--force]` — abandon a task and free the lane; `obor drop <T-xx> "<why>"`
  cancels a task that has no lane yet (a waiting dispatch, `--after` or no free lane).
- `obor pause [--for 3h] "<why>"` / `--clear` — refuse dispatches (quota, waiting on an answer).

## Reviews
Before a release, `obor review` and close it; `make release` refuses otherwise; `UNREVIEWED` only on the owner's word.
Every ~3 merges (never > ~5 unreviewed; `obor status` says `review due`): `obor review [--after T-a,T-b] [--note
"<out of scope, contracts>"]`, serve starts it (`review-brief.md`). Reviewer and deep dive are role panes, never lanes;
verifies: `obor brief <reviewer> "<text>"` in the same session. Serve stops the reviewer once the review is closed
(`RV-<n> closed`) and the integrator after its last verdict (T-672). A deep dive whose verify approves:
`obor restart <deep-dive> --stop`; the next one starts clean. A brief to a stopped role agent starts it fresh.
Milestone end: deep dive, then tag. Messages to another project's lead: `obor send <name> "<text>"` (the names
`obor projects` lists; a path into the project works too). A line you receive names its sender, so the reply is one
command: `obor send <that name> "<text>"`. Workers and
the integrator get jobs from serve, never messages.
Lines to you (another project's `obor send`, `[obor] <agent> finished …`) arrive as messages from another session;
none are typed into your pane. `obor status` lists the ones not delivered yet.

## Decide and record
Direction-setting questions go to the user (ask the advisor before a hard-to-reverse decision; follow-ups and a
requested change go to the same session; once its answer needs no follow-up, `obor restart <advisor> --stop`: it is
never kept for a later, unrelated question, the next brief starts it clean). Owner away: decide
reversible calls. Decisions → `docs/decisions.md`; concerns and open items → `BACKLOG.md`. The second finding in
one function or invariant (two strikes) ends probe-sized fixes: spec a whole-function rewrite with a case → test
table. Fix specs list every entry point of the finding's Extent line, one test each. Models: the strongest for
anything non-trivial, a mid model for small well-specified tasks, the cheapest for mechanical bulk with an oracle.
Speak up unprompted about anything systemic (complexity, fragile dependencies, your own mistakes).

## Handoff
Past `roles.lead.handoff_tokens` each prompt carries a reminder; nothing forces it. At the next logical break (not
mid-task, ask the person first if they are talking with you): write the Handoff section at the top of `BACKLOG.md`
(state, in-flight lanes, open decisions, next steps), commit it, then `obor restart <you> --fresh`.

Hooks deny: `git push`, `pkill`/`killall`, `git commit -a` in the main checkout. Expect it in reports.
