# Advice: planner / coordinator split (Fable, 2026-11-01)

On `docs/research/planner-coordinator-plan.md`. Read: that plan, `docs/design.md`, `docs/state-model.md` (rows 16a/16b,
31, 36, 40, 47, 49, 52; `start.<job>`), `skills/lead/SKILL.md` + `review-brief.md`, `templates/agents/integrator.md`,
`templates/agents/worker.md`, `internal/app/loop.go` (`pickGreen`), `internal/roles/roles.go`, `.obor/config.json`,
`docs/decisions.md`, and today's `.obor/state/logs/serve.log`.

## Recommendation

**Do not build the coordinator role yet. Three quarters of the lines in the plan's evidence table have role-free fixes
that already exist in the design; apply those first, then pilot the remaining judgment work (review triage) with the
brief mechanism and the dormant `triage` role before adding a job kind, an escalation queue and a fourth Opus agent.**

The plan's own target is "only escalations reach the planner" (plan §3.5). That target is reachable without a new role:

| plan's line kind | count | role-free fix | left |
|---|---|---|---|
| open questions (needs-you) | 7 | undo T-604's routing: the review job carries them and the **integrator** decides the in-goal ones, holds the rest (§Q3) | ~2, as holds |
| merged (watched) | 8 | the lead sets these watches because every code merge needs `make build` + `obor serve restart` by hand (`BACKLOG.md:29-30`); `after_merge` already runs `make build` (`.obor/config.json:16-26`): add the restart there | 1–2 wave ends |
| review started / finished | 6 | `[obor] review RV-xx started` is informational: `obor status` has it (row 52); stop posting it | 3 `finished` |
| failures needing the owner | 2 | none: these are the escalations | 2 |

About 6–8 lines a day, every one an action the lead must take. That is the state the plan wants, reached with two
small app changes and two template changes (task list at the end). Only the three `reviewer finished` lines carry real
judgment work (triage → fix specs → dispatch → verify → close); that is the one piece a second agent earns, and §Q5
shows how to get it with what exists.

Why not the role now: the coordinator of plan §2 is, event for event, a copy of the integrator since T-602: fresh per
job, Opus, started by serve with "the event + spec + relevant state", acts with verbs, ends (`integrator.md:11-14`,
state-model row 36, `start.<job>` at `docs/state-model.md:151`). The integrator already holds the decide / escalate rule
of plan §2.2 (`integrator.md:59-64`: "Decide yourself … Specs, contracts and protected files still escalate (`hold`)")
and already hands the owner's decision back into its next job (`integrator.md:41-42`, row 40). Adding a second agent
with the same shape, and routing the integrator's holds through it (plan §2 "every needs-you"), is a double hop for
the one class that is owner-only by definition (`integrator.md:39-42`).

## Section 4 answers

### Q1. Fresh per job vs long-lived
Fresh per job, with two conditions. The reviewer keeps its session only because its probes live nowhere else
(`templates/agents/reviewer.md:28-29`; `review-brief.md` §5: "the reviewer's evidence lives only in its session"). A
coordinator has no such hidden state: every decision it makes must leave as a verb or a committed line anyway (plan §2.4),
so a session adds nothing a job text cannot carry. Conditions:
1. **The cycle's state lives in serve, not in a session.** The review request fact (row 52, `docs/state-model.md:293`)
   is `requested → fired → none`; extend it to hold the fix tasks dispatched for it and the verify brief, so a
   `verify-finished` job can say "RV-xx's fixes were T-a (sha), T-b (sha)" without the agent grepping `git log` for the
   `RV-xx closed` convention (`review-brief.md` §1). A spec header `Fixes: RV-xx #n` (or `obor dispatch --for RV-xx`)
   is enough to bind a task to a review.
2. **Tokens.** Fresh-per-job was adopted as a *token cut* (`docs/decisions.md:8-10`) because the integrator's job carries
   a ≤ 15 KB diff and the role text, and the integrator reads nothing else. A coordinator job that opens with "read
   `BACKLOG.md`, `docs/decisions.md`, the review file" costs more per event than a long session's increment, 20 times a
   day. Rule for its role text: the job carries everything (the review file inline under a size cap, the triage rule, the
   accepted-findings lines, the spec); no `BACKLOG.md` or design doc read whole. Measure Opus tokens per job in the pilot;
   if a review cycle costs more than the lead's triage did, the split bought quiet, not savings, and the owner should
   know which.

### Q2. Should the coordinator dispatch?
Yes, bounded to fix tasks of a review serve started: spec names `Fixes: RV-xx #n`, Difficulty medium or simple, no
"Protected files this task may edit" (a fix that touches `docs/design.md` is the planner's), no `--types` override.
Everything else (features, contracts, release choice) stays with the planner. Lane contention between the two
dispatchers needs no new rule yet: the difficulty map and `obor pause` arbitrate (`SKILL.md:32-37, 56`).

The real hazard the plan misses is **two agents committing in one working tree.** The coordinator's `BACKLOG.md` lines
and the `RV-xx closed` empty commit (plan §2.4, `review-brief.md` §5) interleave with the planner's edits in the main
checkout; a dirty main checkout refuses the publish (row 35) and two `git commit`s race on the index. Mitigation, cheapest
first: (a) the coordinator commits with explicit paths only (`git commit -m … -- BACKLOG.md`; `-a` is already denied,
`docs/design.md:174`); (b) better, the close becomes serve's commit through a verb (`obor review close RV-xx "approved ·
T-a,T-b"`), which turns the cycle's end into a fact (row 52) instead of a grep over `git log`, and serve already commits
with `--no-verify` (`docs/design.md:272-273`).

### Q3. Could serve's deterministic code take more of it?
Yes, and it is the biggest lever. The "5 of 7 keep as built" is not workers asking too much by nature; it is T-604, one
day old. `pickGreen` holds every task whose report has a non-empty `Open questions:` section before the review job
(`internal/app/loop.go:614-624`), so the lead now sees what the integrator used to settle: its role text still says
"Hold instead of accepting on open questions … Otherwise acceptance is yours" (`integrator.md:26-28`). The log shows
the kind of question that now stops the lead: "Should ship run `git fetch origin main` …", "Publish compares the tag's
marketplace with …" (serve.log, T-643, T-645), and two whose section starts with "none new." / "none (the lead
decided …)" and still raised a needs-you (check that T-616's `noOpenQuestionRe`, `internal/app/fetch.go:466`, covers
"none" followed by a word).

Fix, in order:
1. **Reverse the routing.** The review job carries the `Open questions:` section; the integrator decides a question
   whose answer is inside the spec's goal or decisions (`decided: <what> — <why>` in the accept note, the form it already
   uses, `integrator.md:59`) and `hold`s one that changes scope, a contract or a protected file. The owner's decision then
   flows back as today (`Lead's decision on your hold`, `integrator.md:41-42`, row 40). Serve's job text gains one
   section; `loop.go:618-624` and the `heldQuestions` / `openDecision` state (`loop.go:172-174`) go away or stay as the
   hold path. T-604's token saving (one integrator job less per question) was never real: the integrator job runs
   anyway after the lead answers.
2. **Worker template wording** (`templates/agents/worker.md:55`): "Open questions: only what blocks acceptance. A choice
   you made inside the goal is a Self-check deviation with its reason, not a question."
3. **Specs answer the predictable ones.** The spec template already has a decisions section (`SKILL.md:21`); the lead
   should fill it for the questions a worker will meet (release scripts: what to do when origin moved, etc.).

Also checked: the T-621 repeat line `(again, <age>)` appears zero times in today's log, so repeats are not part of the 20.

### Q4. Escalation delivery: idle-only digest?
No. It conflicts with two standing decisions:
- **Lead noise (c)**, owner 2026-10-14: "**No** to (c) holding messages while the person talks" (`docs/decisions.md:77`).
- **T-485**: lead lines are posted to the session's inbox with "no pane lookup, no idle rule", because "an idle lead must
  still be woken" (`docs/design.md:421-427`).
"Posted only when the planner's pane is idle and at most once per N minutes" (plan §2.3) is (c) plus a timer: a decision
can sit N minutes while the owner reads the screen, the "when Y do X only in chat" trap `SKILL.md:38` forbids. If the
owner now wants (c), it must be an explicit reversal in `docs/decisions.md`, not a side effect of this plan.

What actually pushes the discussion up is (1) one turn per line and (2) the lead answering each line in prose. Cheaper,
compatible with both decisions:
- **Batch per pass.** Row 47 delivers "the oldest line per agent" per merge-loop pass (`docs/state-model.md:288`). For
  the lead, post every queued line of the pass as one message: one turn for a wave's three lines.
- **Lead skill rule:** an `[obor]` line gets the action and at most one line of chat, no recap of the discussion.
- **Fewer lines** (the table above).
Urgent-at-once needs no new class: a release failure is a needs-you today. The UserPromptSubmit header (plan §2.3 c)
is sound and cheap (hooks already read the store read-only, `docs/design.md:186-190`); it only covers Claude leads
(`docs/design.md:22-23`), which is every lead today.

### Q5. Simpler
1. **Reuse the integrator** for in-flow judgment (open questions, in-goal deviations): §Q3.1. It is fresh per job, Opus,
   has spec + report + diff, and the decide/escalate rule. No new role for this event class.
2. **Reuse `brief` + the `triage` role for review triage.** `internal/roles/roles.go:46` has an optional `triage` role
   with no env value and no role text: unused. The planner runs `obor brief <triage> "Triage RV-xx: <review worktree
   path>"`; the triage agent (its own pane in the `roles` tab, Opus) writes fix specs, `obor spec set`, `obor dispatch`,
   the `BACKLOG.md` accepted lines, and later the verify brief to the reviewer; serve posts one `[obor] <triage> finished
   (briefed <age> ago)` to the planner (row 47, `docs/design.md:408-414`). That is plan §2.1's `review-finished` job
   done with verbs that exist. Needed: a role text (`templates/agents/triage.md`), and the CLI's caller gating widened
   from "the lead or the owner" to include triage for `spec set`, `dispatch`, `watch`, `brief <reviewer>`, `resume`
   (rows 16b, 40, 49, 52). If after a week the planner's inbox is quiet and the triage agent's work holds up, automating
   the brief (serve sends it on `reviewer finished`, a template like `review-brief.md` §2's) is a small step; the
   escalation queue, `obor escalate` / `obor escalation <id>` and `obor log --coordinator` were never needed.
3. **`obor log --coordinator` is `obor status --facts --since 2h`.** Every decision the coordinator would log is already
   a fact or verdict record: `answered`, `sendback.<call id>`, `accepted{note}` with its `decided:` lines, `briefed`,
   `watch`. A `--since` filter on the facts is the whole feature.
4. **No second queue.** An escalation is a needs-you with an addressee. Route by cause: `held`, `rejected`, `publish`,
   gate `fail`, release failed → the lead; `open questions` → the integrator's job (no needs-you at all); `stalled` /
   `blocked` / `pane exited` stay with the lead (rare, operational). One table in serve, no new verbs.

## What the plan gets wrong or misses
- **§2 vs §2.2 contradiction.** §2 has the coordinator run `make release`; §2.2 forbids it tags. `make release` commits
  `bin/checksums.txt` and creates the tag locally (`Makefile:107`, `RELEASING.md:14`); push and publish are `make
  ship`, the owner's. A local tag is reversible, so either is defensible, but the plan must pick one.
- **Holds routed to the coordinator** are a double hop (`integrator.md:39-42`: "for what only the owner can decide").
- **Watches.** The lead sets a watch for a merge *it* must act on (`SKILL.md:40-43`). Routing all watched merges to the
  coordinator breaks that; route by who set it (row 49 records `by`, `docs/state-model.md:290`).
- **Unassigned lines:** `obor send` from another project's lead and `[obor] <advisor|deep-dive> finished` must stay with
  the planner (`docs/design.md:400-406`, `408-414`); the plan does not say.
- **Caller gating.** Rows 16b, 40, 46, 49, 52 say "the lead or the owner, not a worker or the integrator"; a new role
  must be added to each. The guard needs nothing: only `WriteReviewsOnly` is enforced beyond protected files
  (`internal/guard/guard.go:393`), and only `docs/design.md` is protected (`.obor/config.json:56-58`).
- **A coordinator job must never wait.** A job that dispatches fix tasks then waits for their merge is the shell loop
  the lead skill forbids (`SKILL.md:8-9`). Its text must say: set `obor watch` (recorded `by` it) and end; the merge
  arrives as the next job.
- **The decision bus is unnecessary.** "Serve hands the decision to a new coordinator job" (plan §2.3) duplicates what
  `obor resume <T> "<decision>"` already does per cause (row 40, `docs/state-model.md:284`).
- **Pilot metric.** "Lines into the planner's chat" alone will look good by construction. Add: Opus tokens per day
  across lead + coordinator + integrator (limits ran out on 10-30, `BACKLOG.md:19`), and the median time from
  `reviewer finished` to the first fix dispatch (the lead does triage in minutes while chatting; a fresh agent that
  reads a review file and writes specs may be slower, not faster).
- **Honest reading of the evidence.** Of the 20 lines, 8 watches and 3 `review started` lines were requested by the lead
  itself, and 7 came from a routing change the day before. The owner's ask is real, but the symptom this week is
  mostly configuration, not architecture.
- **Restart-after-merge needs a check.** `after_merge` running `obor serve restart` kills the serve that runs the step;
  the step's result record may be lost and replayed. `make build` is idempotent, and the restart should be the detached
  form the design already has for self-restarts (`docs/design.md:256-257`). If this is why the lead does it by hand,
  that reason belongs in `docs/agents/lead.md`.

## Concrete alternative (tasks, in order)
1. **T-a (medium, `internal/app`, `templates/agents/integrator.md`): open questions go to the integrator, not the lead.**
   The review job text carries the report's `Open questions:`; the integrator decides in-goal questions (`decided:` in
   the note) and holds the rest; `pickGreen`'s hold path (`loop.go:618-624`) is removed; the `Lead's decision on your
   hold` flow is unchanged. Tests: the T-604 app tests flip to "review job carries the questions"; one test for a hold.
   Also check `noOpenQuestionRe` against "none new." and "none (…)".
2. **T-b (simple, templates/docs):** `worker.md:55` wording; `SKILL.md` adds "an `[obor]` line: act, at most one line of
   chat"; `review RV-xx started` dropped from the lead's lines (status only; `commands.md:29`, row 52).
3. **T-c (medium, `internal/app/messages.go`):** lead lines batched per pass into one post (row 47).
4. **T-d (medium, obor-kit config + a probe):** `after_merge` restarts serve detached after `make build`; verify the
   replay of the step; then the lead stops watching routine merges.
5. **T-e (medium, roles + CLI gating + a role text): the `triage` role.** `templates/agents/triage.md` (the triage rule
   of `review-brief.md` §3–5, the fix-spec rules of `SKILL.md:70-72`, "watch, never wait", explicit-path commits);
   caller gating for `spec set`, `dispatch`, `watch`, `brief`, `resume`; `obor setup role triage`. The planner briefs it per
   review for a week. Measure lines, tokens, time-to-dispatch.
6. **Then decide** whether serve starts the triage brief itself on `reviewer finished`, and whether anything of the
   plan's §2.3 escalation queue is still wanted once the lead's inbox is 6 lines a day. My expectation: no.
