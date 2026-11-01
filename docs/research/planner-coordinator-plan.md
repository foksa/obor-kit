# Plan: split the lead into planner (the owner's) and coordinator (the swarm's)

Status: draft for review (Fable, Gemini), 2026-11-01. Owner's ask: "feedback from agents pushed what I was reading
and our discussion away … one agent just for discussion with me, he creates overall tasks, and another that is the
orchestrator and also replies to agents when needed." Same ask on 2026-10-24 ("it does not make sense for the agent I
communicate with to also be the orchestrator"); serve (v0.15–v0.17) took the mechanical half; the judgment half
still lands in the lead's chat.

## 1. Evidence (lead session, 2026-11-01)
About 20 `[obor]` lines reached the lead's chat today, interleaved with the owner's discussion:

| kind | count | examples | who should get it |
|---|---|---|---|
| open questions (needs-you) | 7 | T-639 git-error, T-640 ×2, T-642 rerun, T-643 fetch/draft, T-645 tag^1 | coordinator; 5 of 7 were "keep as built" |
| merged (watched) | 8 | T-638, T-639,T-640, T-641, T-643, T-644,T-645 | coordinator |
| review started / finished | 6 | RV-69..71 + verifies | coordinator (triage), planner gets the outcome |
| failures needing the owner | 2 | v0.17.2 release check failed (T-641); v0.17.2 catalog broke plugin loading | planner (owner must know) |

The owner's messages in the same window: ~25. Every `[obor]` line arrived as a turn of its own and the lead replied
to it in chat, pushing the discussion up the screen.

## 2. Proposal
| | **Planner** (role `lead`, kept) | **Coordinator** (new role `coordinator`) |
|---|---|---|
| Talks to | the owner only | serve (jobs), workers (through serve verbs), never the owner directly |
| Does | discussion, direction, specs, dispatch, which release when, `docs/decisions.md` | answers open questions within the spec, sendback/resume/drop, review triage → fix specs, verifies, closes reviews, `obor review` requests, `make release` once the planner says "release vX" |
| Lifetime | long-lived (the owner's conversation) | **fresh per job**, like the integrator since T-602: serve starts it with one job (the event + spec + relevant state), it acts with obor verbs, it ends |
| Gets from serve | nothing by default; escalations (below) | every needs-you, review finished, watched merges, release failures |
| Model | as today (opus; owner considering high effort) | opus, effort high (judgment calls) |

### 2.1 Coordinator jobs (serve → coordinator)
One job per event, like the integrator's review job: `open-questions T-xx` (spec rev, report, diff stat),
`review-finished RV-xx` (review file path, BACKLOG accepted lines, the triage rule), `verify-finished RV-xx`,
`merged T-a,T-b` for a watch the coordinator set, `release-failed vX` (output tail). The job text carries what it
needs; the coordinator reads files for the rest. Serve serializes coordinator jobs (one at a time) so decisions
don't race.

### 2.2 What the coordinator may decide (and what it must escalate)
- Decide: reversible calls inside the spec's goal: "keep as built", sendback with a concrete fix, accept a
  low-likelihood finding into BACKLOG, a fix spec for a review finding (dispatched under the same rules as the
  planner's), closing a verified review, a re-run of a failed release step.
- Escalate (never decide): anything that changes the spec's goal or scope; config/model/agent settings (owner rule);
  public or irreversible actions (push, publish, tags, data migrations, deleting); a second strike that calls for a
  rewrite; anything it is unsure of. Escalation = `obor escalate "<one line>" [--task T-xx]`.

### 2.3 Escalations reach the planner without interrupting
- Serve keeps escalations in a queue (facts, like needs-you). The planner sees them: (a) in `obor status`, (b) as **one**
  line `[obor] 2 escalations (obor escalations)` posted only when the planner's pane is idle and at most once per
  N minutes, never the text, (c) at its next turn through the UserPromptSubmit hook as a short header.
- Failures that block the owner (release failed after `make ship`, a broken install) are escalations marked urgent:
  posted at once.
- The planner answers with `obor escalation <id> "<decision>"`; serve hands the decision to a new coordinator job.

### 2.4 Shared memory between the two
- Specs live in serve (`obor spec`); `BACKLOG.md` (handoff, concerns, accepted findings) and `docs/decisions.md`
  are the shared written state; the coordinator commits its BACKLOG lines and decision lines itself.
- The coordinator's decisions are visible: `obor log --coordinator [--since 2h]` (one line each: event → action →
  why). The planner reads it when asked "what happened?" instead of being told live.

## 3. Rollout (tasks)
1. **Coordinator role + jobs** (hard): role config `roles.coordinator`, fresh-per-job start like the integrator,
   job texts for the five events, serialization, role text `templates/agents/coordinator.md`; needs-you and
   review lines route to the coordinator when one is configured, else to the lead (today's behaviour, so projects
   without one are unchanged).
2. **Escalations** (medium): `obor escalate`, the queue, `obor escalations`, the planner's idle-only digest line,
   `obor escalation <id> "<decision>"` → coordinator job; urgent ones posted at once.
3. **Coordinator log** (simple): `obor log --coordinator`.
4. **Lead skill split** (docs): `skills/lead` keeps discussion/spec/dispatch/release choice; new
   `skills/coordinator`; AGENTS.md and design.md updated.
5. Pilot on obor-kit for a day, measure lines into the planner's chat (target: only escalations), then axis-*.

## 4. Open questions for reviewers
1. Fresh-per-job vs a long-lived coordinator: fresh is cheap and chatter-free, but loses context across related
   events (a review's triage → its verify an hour later). Is the job text + BACKLOG + `obor log` enough, or should a
   review cycle keep one coordinator session (like the reviewer keeps its session for the verify)?
2. Should the coordinator dispatch at all, or only the planner? (Fix tasks from reviews are many and mechanical.)
3. Could serve's deterministic code take more of the coordinator's work (e.g. "keep as built" answers are 5 of 7 —
   is that a sign workers ask too much, which a spec/worker-prompt change would fix instead)?
4. Escalation delivery: is an idle-only digest line enough, or will the owner miss urgent ones?
5. Anything that makes this simpler: fewer roles, fewer verbs, reuse of the integrator instead of a new role.
