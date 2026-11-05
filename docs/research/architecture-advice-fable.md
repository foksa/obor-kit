# Architecture advice: why every change adds code, and what to change

Advisor: ob-fable (Claude Fable), 2026-11-05, at main b3300cf4 (v0.27.1). First pass, from my own reading of `cmd/`,
`internal/`, `docs/design.md`, `docs/state-model.md`, the top of `docs/decisions.md` and BACKLOG "Concerns". The deep
dive's inventory (`docs/reviews/DD-architecture.md`) is not read yet; a second pass follows when it arrives.

**Read "After the owner's correction" first.** The owner corrected the first pass on the same day: nothing may
depend on an agent remembering. That section restates the rule and the plan; where it and the first-pass text below
disagree, it wins. The superseded places are marked.

**To decide, read "Combined plan" at the end**: one ordered plan that reconciles this file with the deep dive
(`docs/reviews/DD-architecture.md`). It wins over everything before it.

Not verified: I ran no tests and no probes. Every "saves about N lines" below is an estimate from reading, to be
checked by the pilot in the plan. Line counts are `wc -l` over non-test `.go` files, or `git diff --stat`.

## Recommendation first

1. **The cause is scope, carried by one expensive mechanism.** obor has one way to add behaviour: a new durable state
   machine inside `obor serve`, on the event log. That way has a fixed price of about twelve obligations per unit of
   behaviour (list in §1). This week's "flow simplifications" made the lead's flow simpler by moving the lead's manual
   steps into serve. To the code they were features, and each paid the full price.
2. **Keep Go. Do not rewrite.** The bash to Go rewrite of 10-13 already tested the rewrite theory: its target was
   12k lines at v1.0 (`docs/decisions.md:168`), and three weeks later the tree is 46.9k. The language changed, the
   growth mechanism did not. Go costs perhaps 15 to 25% in verbosity here; the obligations cost a factor of 3 to 10.
3. **Change the shape in this order** (value for effort and risk, details in §2):
   1. A scope rule: serve reminds and refuses, the lead acts. No code, an owner decision. *(Superseded: serve acts
      or refuses; see the correction.)*
   2. Two durability tiers, written into the design, the spec template and the review brief.
   3. A pilot: the idle line rebuilt in the cheap tier, measured, before anything else is converted.
   4. A verb is one critical section; effects run only in the merge loop.
   5. One table row per verb, with its own typed parameters.
   6. A snapshot at upgrade, so readers of old record shapes can be deleted.
   7. The web types checked against the Go structs.
4. **Stop in the process**: crash and interleaving hunts on convenience code, reviewer rounds on fix tasks of such
   findings, coverage-ratchet findings, and keeping the transitions twice (prose and table).

None of this shrinks 46.9k by much on its own. My estimate is 1 to 1.5k production lines and 3 to 5k test lines if
the pilot confirms. *(After the correction: about 1k production lines.)* The real effect is on the slope: what the
next flow change costs.

## After the owner's correction (2026-11-05)

The owner's constraint, relayed by the lead: **nothing the lead or any agent must do may depend on the agent
remembering to do it.** Procedures were moved into serve because agents dropped them more than once, and a skill
cannot fix that. I take it as fixed. It withdraws four things from the first pass below:

- "Serve reminds, the lead acts" (§2 rule 1) and "procedures go to the lead's skill" (§4). A one-shot line that
  relies on the lead acting is not a mechanism.
- The fix round as "refusal plus reminder" (§2 rule 1, plan step 3). Serve must brief the verify itself.
- "No `obor idle` acknowledgement verb" in the pilot (§2 rule 3). The verb is how a judgment closes the obligation.
- The size estimate. Serve keeps doing the steps, so less goes. My estimate is now about 1k production lines, still
  unmeasured.

What stands: the diagnosis (§1), the tiers, the pilot, verbs as one critical section, the verb table, the snapshot,
the process list and the Go answer. The correction changes what the cheap tier is for: it acts, it does not remind.

### The rule, restated

Every step of a procedure gets exactly one of three guarantees. The test runs in this order.

1. **Can serve do the step?** It can when the step needs no judgment: its inputs are in the store, git or herdr.
   Then serve does it, in one of two tiers.
   - **(b) Rule tier** if all three hold: the condition can be recomputed from current state on every pass; a second
     run equals one run, or is addressed to something that no longer exists; a run one pass late harms nothing.
   - **(c) Kernel** if the step's order against another step is the guarantee, or a repeated or late run destroys
     work or moves main.
2. **Is there a verb every path to "done" goes through?** Put **(a) a refusal** there, naming what is missing. A
   refusal stops a wrong finish. It does not make the missing step happen, so it is never used alone for a step that
   must happen: it is paired with (b) or (c).
3. **The step needs judgment** (a text, a choice only an agent or the owner has). Serve holds a **standing
   obligation** until the verb that carries the judgment arrives: shown in `obor status`, lined to the lead,
   repeated, then escalated to the owner. This is rule tier too: the obligation is a condition recomputed each pass,
   never a line sent once. A needs-you is the existing example.

obor already has one working example of each, which is why I trust the split:

| Guarantee | Existing example |
|---|---|
| (a) refusal | A dispatch needs a stored spec. `task_done` at an older spec rev is refused once (`fetch.go:368`). `obor review close` refuses while a linked task is pending (`reviewverify.go:317`). |
| (b) serve acts, level-triggered | An undelivered job is sent again, then nudged (`loop.go` `pickJob`, `watchJob`). The reviewer's "finished" line is serve's observation of its pane, not the reviewer's report (`messages.go:36-37`). |
| (c) kernel | Submit, gate, publish, verdicts (`loop.go:372-437`). |

### The rule tier, now that it acts

```
rule:   name, when(state) → action          (a line, a brief, a stop, a prompt: one idempotent call)
pass:   for each rule whose condition holds: run the action in the loop's goroutine
done:   the condition turns false because the action's effect is visible in state
        (a brief fact, a pane at its shell, a finished line); only a line, which leaves
        no other trace, records did{key}
fail:   the condition stays true, so the next pass tries again; after N failures in a row
        one generic needs-you names the rule
```

A rule has no intent and result, no crash class, no replay case, no claim, no fold of its own and no per-rule
needs-you cause. Its written failure mode: after a crash it may run twice or one pass late.

### Each current automaton, placed

| Automaton | Today | Guarantee | Why |
|---|---|---|---|
| Fix round and verify lifecycle (`reviewfix.go` 208, `reviewverify.go` 627) | request fact, five states, two admission paths, `vf`, `admits` | (a) at close, (b) for the verify brief | nothing is lost or merged by a repeat; every state is readable from brief facts and git |
| Stop step (`stopstep.go` 263, `move.go` 112) | a kernel step beside the state table (`halt`) | (c) | its order against the lane's next use is the guarantee |
| Idle line (`idle.go` 354) | epoch fact, ack with stored reply, settle, ping | standing obligation, (b) | a late or doubled line harms nothing |
| Merge watch (`mergewatch.go` 271) | watch fact, line, fold, claim | (b) for the line; prefer `--after` so serve acts | the line is a notice, not a guarantee |
| Role stop (`rolestop.go` 307) | intent and result `stop.<agent>.N`, fold | (b) | the pane shows whether it is done; a wrong stop heals through the job's own retry |
| Needs-you repeat (`messages.go:76-81`, `1019-1022`) | deadline folded from delivery records | (b); the needs-you fact itself stays (c) | a pure function of "in force" and "last delivered" |
| Stop and wake (`stopped.go` 438, `stopwake.go` 294) | stop fact, wake keys, alarm | (b); the alarm is a standing obligation | a second "continue" is noise; most of the size is per-kind detection |

**Fix round and verify lifecycle.** Three steps, three guarantees.

- *Close*: (a), as today. `obor review close` refuses while a linked fix task is unverified.
- *Verify*: (b). Serve briefs it. Condition: the review is open, its linked tasks are merged, a finished verify does
  not cover them, the reviewer has no open brief and is idle. Action: read the integration head, move the reviewer's
  worktree to it (`ReviewGit.Detach`, idempotent), brief with that head. The brief's own record makes the condition
  false. "Requested, briefed, done, superseded, failed" stop being stored states: they are readings of the brief's
  facts (briefed, the finished line with `Ends`, a stop with `Ends`). A failed or superseded verify simply leaves the
  condition true, and the next pass briefs again. The request fact, `verifyRec`'s state, the second admission path
  and the `vf` mutex can go.
- *Link*: today the lead must remember `obor review fix`. That breaks the constraint. Derive the link from the fix
  task's spec, a `Fixes: RV-n #k` line written while the finding is in front of the lead, and read it at dispatch.
  The stronger form, for the plan and not decided here: close refuses while a finding number has neither a verified
  fix nor an "accepted" disposition, which needs the reviewer's report to carry its finding count.

Not (c): no path here loses work or touches main. The cost of the rare double is one extra verify run. One point to
confirm before the task: `admits` blocks a publish while a verify is admitted (`roleworktree.go:104-127`). If the
scope is computed from the one head sha the worktree moves to, a publish in between changes nothing and `admits` can
go too. T-730 and T-733 should be re-read for a reason I have not seen.

**Stop step.** Kernel, and rightly built as a loop step this morning. A move starts a new worker in the same lane
under the same derived name; a drop frees the lane and may discard work. A stop that runs after the new start kills
the new worker. A free that runs after a redispatch discards the next task's work. Neither is idempotent in the
sense rule tier needs, so the +330 is the kernel's price and it is owed. What can still be cheaper: `halt` is a
seven-field machine beside the state table with its own busy clause (`stopstep.go:30-38`, `states.go:256-258`), and
T-855 was the interleaving that came from it sitting beside the table. Whether "stopping" should be a row with a
verbs column is a question for the deep dive; I have not verified it fits all three callers.

**Idle line.** The obligation is "nothing is in flight: dispatch, or say why". Nobody has to remember anything:
serve detects it, and it stays open until a dispatch or `obor idle "<why>"`. As rules: the line after the settle
window, the ping and owner notification 30 minutes later, both keyed by the merged or dropped record that emptied
the pipeline. The acknowledgement stays as one plain fact naming that record. The epoch fact and its fold go, since
"empty since record N" is derivable. Still the right pilot, with a new pass mark: about 150 production lines, the
verb kept.

**Merge watch.** The line is rule tier: condition "every task of the set merged, or one dropped", one `did` key per
watch. But the subscription is itself something the lead must remember, so the watch cannot carry a guarantee.
Where the follow-up is a verb, the guarantee is `--after` on that verb (`obor dispatch --after`, `obor review
--after`): serve acts. Where it is a judgment, the idle obligation catches the end state. Keep the watch as the
convenience it is and do not build on it.

**Role stop.** Rule tier. Condition for the integrator: its agent runs and is idle, no job is open or queued, a
verdict came since its start. For the reviewer: its review is closed. The action is a stop; "stopped" is the pane at
its shell, which serve already reads (`rolestop.go:27-28`), so no result record and no `.N` keys are needed. If a
stop ever hit an integrator that had just taken a job, the job's delivery timers start it again. The stop inside
`obor brief --fresh` is different: it is ordered before that brief's start and stays part of that delivery.

**Needs-you repeat.** Rule tier, and nearly so already: repeat when the needs-you is in force, its last line was
delivered 30 (then 60) minutes ago and none of its lines waits. The needs-you fact and its first notification stay
kernel: they are how the kernel hands a decision out, and one must never be lost.

**Stop and wake.** Rule tier. Condition: the stop is in force (no turn since), the reset time plus two minutes has
passed, no `did` key for that wake time. Action: the "continue" prompt. The alarm 30 minutes later is a standing
obligation. Prompts already have no crash class (`docs/state-model.md:85-87`), so this is close to rule tier today,
and most of its 730 lines are per-kind stop detection (Claude's hook, Codex's rollout, OpenCode) that no tier
removes. Convert it last, if at all.

### The plan, in these terms

| Step | Task | Guarantee | Done when |
|---|---|---|---|
| 0 | Owner confirms the three guarantees and the written failure mode of rule tier | | an entry in `docs/decisions.md` |
| 1 | `docs/design.md`: the test above; spec template: `Guarantee: refusal \| rule \| kernel` per step and `Adds:`; review brief: no crash or interleaving findings on rule-tier steps unless a kernel guarantee breaks | | merged |
| 2 | Pilot: the generic rule pass, and the idle line on it (ack verb kept) | (b) | about 150 production lines, one review round; else stop here |
| 3 | Role stop, then the needs-you repeat, then merge watch | (b) | each task's net is negative |
| 4 | Verify as a rule; the link from the spec's `Fixes:` line; close's refusal unchanged | (a) + (b) | request fact, `vf` and the second admission path gone |
| 5 | Stop step stays kernel; decide with the deep dive whether `halt` becomes a table row | (c) | a decision, then one task |
| 6 | Verbs as one critical section; verb table; snapshot; `types.ts` test | | as in §5 below |

Order by risk: step 3's three are small and self-contained, so they prove the rule pass before step 4 touches the
review cycle that was fixed three times today.

### What this costs in guarantees

Two failure modes become accepted in writing, and only for rule-tier steps: an action may run twice after a crash
(a duplicate line, a second verify brief, a second "continue"), and it may run one pass late. No step can be
forgotten, because every condition is recomputed on every pass from state, and no step waits on an agent's memory.
That is a stronger guarantee against forgetting than a stored lifecycle gives, since a lifecycle stuck in a state
needs someone to notice it.

## 1. What makes every change add code

### The measurement

Since v0.20.1 (11-03) production Go grew by 4,992 lines. `internal/app` took 3,253 of them (65%). Every other
package moved by a few hundred lines at most:

| Package | v0.20.1 | v0.27.1 | Change |
|---|---:|---:|---:|
| `internal/app` | 13,109 | 16,362 | +3,253 |
| `internal/config` | 1,744 | 2,298 | +554 |
| `internal/view` | 1,738 | 2,059 | +321 |
| `internal/agents` | 1,101 | 1,299 | +198 |
| `internal/guard` | 2,690 | 2,807 | +117 |
| `cmd/obor` | 4,718 | 4,600 | −118 |
| `internal/setup` | 3,713 | 3,434 | −279 |
| all others | | | under ±100 each |

Inside `internal/app` the growth is new automata, not edits to old ones:

| What | Net lines | Kind |
|---|---:|---|
| `reviewverify.go`, `reviewfix.go` (review fix round, verify lifecycle) | +685 | a lead procedure moved into serve |
| `drive.go` (the owner drives a task) | +500 | new feature |
| `stopstep.go`, `move.go` (stop as a loop step) | +375 | a synchronous action made a durable step |
| `idle.go` (the idle line, with epoch and acknowledgement) | +354 | new reminder |
| `configset.go` (obor web's write path) | +249 | new feature |
| `states.go`, `block.go` (verbs, phase, block) | +239 | table columns |
| `resume.go` +354, `verdict.go` −258, `answer.go` −69 | +27 | a real consolidation: three verbs into one |
| `roleworktree.go` +130, `mergehold.go` −178 | −48 | a replacement |

The two rows at the bottom are the important ones. The one true consolidation came out even. The one deletion was a
replacement, and it was +110 over the whole tree because the guard, config, setup and launch had to follow.

### The price list of one unit of behaviour

A new state, step, verb or reminder in serve owes most of these. Each is a place a diff must touch and a reviewer
must check.

1. **Records.** A step constant and a data struct (`internal/app/event/steps.go`: 48 step constants;
   `event/data.go`).
2. **A fold, and live state beside it.** `foldRecord` runs 16 folds on every record
   (`internal/app/project.go:76-106`). Each fold owns a slice of `folded` (`app.go:200-227`), and most features keep
   live state next to it in `proj` (`app.go:122-198`, about 30 live fields).
3. **Intent, result, crash class, failure rule** for each outside effect: `App.do` (`loop.go:372-437`), `failed`
   (`loop.go:455-482`), and a row in two tables of `docs/state-model.md` (lines 118-165). That file is 91 KB in 408
   lines; its longest table row is 6,608 characters (line 323).
4. **A needs-you path**: a cause constant, a resume default, a resume rule and a resume target (`loop.go:75-105`,
   `states.go` `resume` column, `block.go:72-84`).
5. **Claims against concurrent callers.** Every socket request runs in its own goroutine beside the loop
   (`status.go:310`). The app has eight mutexes (`app.go:83`, `106-111`, `136`), one more in the outbox
   (`messages.go:92-97`), a scratch token, and the maps `held`, `ended`, `exec`, `unwritten`, `admits`. `busy` has to
   read five sources to answer "may a verb act on this task" (`states.go:245-270`).
6. **The wire.** A method constant, fields on the one shared `Call` struct (36 fields for 28 methods,
   `wire/call.go:84-140`; `Note` alone has six meanings, `call.go:78-81`), a handler named in two hand-kept lists
   and a map (`status.go:330-358`), a CLI parser in `cmd/obor`, a row in the help table kept in step by a test
   (`internal/help/help.go:24-26`), an MCP tool when an agent calls it. Replies are prose (`"SET <task> rev <n>"`,
   `call.go:32`) that clients and tests match as text.
7. **The view.** A task has five shapes: `mtask` (`loop.go:137-195`, about 50 fields), `wire.TaskStatus`
   (`wire/status.go:180-201`), `view.Lane` / `MergeItem` / `WaitItem` (`view/view.go:89`, `view/queue.go:34-68`), text
   lines (`cmd/obor/status.go:188`, `309`, `334`) and a hand-kept TypeScript interface (`web/src/lib/types.ts:108`;
   433 lines, 43 exported types). T-852's one new fact for the reader, `Stopping`, starts at `wire/status.go:200`.
8. **A line to the lead**, with its own delivery states (`messages.go:23-70`; the file is 1,308 lines, a reliable
   message queue).
9. **The guard and the role rows**, when the change touches a role, a worktree or a file rule (T-853 changed
   `guard.go`, `shell.go` and 134 lines of `guard_test.go`).
10. **Readers of the old shape, forever.** Replay starts at record 1, so every shape ever written must still fold.
    There are 64 such sites in production code (`mtask.go:98`, `mtask.go:323`, `loop.go:88`, `loop.go:107`,
    `reviewverify.go:132`, `reviewverify.go:175`, `messages.go:379`, `event/log.go:29`).
11. **Contract text**: state-model rows, `docs/design.md`, `skills/lead/commands.md`, role templates, the UPGRADE
    guide. Today: 141 insertions and 101 deletions across 11 such files.
12. **Tests**: a row test per state (`states.go:17`), the spec's case table, crash cuts, CLI tests. Today:
    +3,367 / −1,007 test lines against +2,324 / −908 production lines.

T-852 shows the fan-out: 40 files, of them `event/steps.go`, `event/data.go`, `wire/status.go`, `wire/client.go`,
`states.go`, `mtask.go`, `loop.go`, `steps.go`, `status.go`, `messages.go`, three `cmd/obor` files and two skill
files, for one idea ("answer first, stop later").

The idle line is the clearest example of the price. The behaviour is "tell the lead once when nothing is in flight".
It is 354 production lines and 762 test lines (`idle.go`, `idle_test.go`), an epoch fact, an acknowledgement verb
with a stored reply, a settle window, a ping, a status field, and three fix tasks in one day (T-858, T-859, T-860).

### Why a simplification adds code

Four mechanisms, each visible in today's five tasks.

- **A simpler flow means more automation.** T-850 and T-852 took steps the lead ran by hand (verify after the fixes
  merge; wait for a stop) and gave them to serve. T-852's spec asked for it in so many words: "an intent step of the
  merge loop, replayed after a restart". The stop went from one inline call to `halt` (seven fields,
  `stopstep.go:30-38`), a `.N` counter, a refusal, a needs-you cause, a resume rule and a busy rule. Complexity moved
  from the skill text into Go, and Go charges items 1 to 12 for it.
- **A deletion is a replacement.** The merge hold went (−178) and role worktrees came (+130), with new guard rows,
  config keys and setup steps.
- **A deleted concept leaves its reader.** Item 10.
- **Review rounds only add.** Each P1 on a new automaton is an interleaving or a crash window. Its fix is a new
  claim, record or guard clause. T-855 ("a drop never frees a lane while a stop is pending") is the interleaving that
  T-852 created. T-856 and T-857 are the verify lifecycle that T-850 needed to be correct: five states and two
  admission paths (`reviewverify.go:21-52`; "the request's own call, or a merge-loop pass", line 29).

The guard and the view are part of the price but not part of the slope. The guard is flat (2,690 to 2,807). Its
shell emulator (`shell.go`, 1,489 lines) is expensive to own and cheap to leave alone.

## 2. What to change, ranked

| # | Change | Value | Effort | Risk |
|---|---|---|---|---|
| 1 | Scope rule: serve reminds and refuses, the lead acts | high | none (a decision) | low |
| 2 | Two durability tiers in design, spec template, review brief | high | docs only | low |
| 3 | Pilot: idle as a cheap-tier rule; then the other candidates | high if it confirms | small, then medium | low, then medium |
| 4 | A verb is one critical section; effects only in the loop | high (review rounds) | medium to high | medium |
| 5 | One row per verb, typed parameters per method | medium | medium, mechanical | low |
| 6 | Snapshot at upgrade; delete old-shape readers | medium | medium | medium |
| 7 | TypeScript types checked against the Go structs | low to medium | small | low |
| 8 | Guard: log rule hits, probe the CLIs' own sandboxes | low now | small | low |

### 1. Scope rule: serve reminds and refuses, the lead acts

*Superseded by "After the owner's correction": a reminder that relies on the lead acting is not a mechanism. Kept
for the record.*

A lead is an LLM. It is bad at remembering to do something later and good at running an exact command it is handed
at the right moment. So serve needs two cheap tools for process, not a state machine per procedure:

- **A reminder**: one line to the lead when a condition becomes true, naming the exact next command.
- **A refusal**: the verb at the point of no return checks the condition and refuses with the command to run first.

Applied to the fix round: `obor review close` refuses while a fix task is unmerged or unverified (it already can:
`ReviewCycle.Pending`, `reviewverify.go:317`), and one line goes out when the last fix task merges ("run: obor
review verify T-a,T-b").
That gives the owner's rule of 11-05 ("close refused until verified") without `fixRound`, `verifyPass`, admission or
supersede.

What serve must still own, because a mistake there loses work or damages main: placing a task on a lane, submit,
gate and publish, verdicts, drop and lane free, spec revs, job delivery, needs-you and its notification.

The honest cost: a reminder is weaker than automation, because a lead can ignore a line. The refusal covers the
cases that matter. The owner decided T-850 this morning and it works. The choice is the owner's: accept roughly 300
to 700 lines and two or three review rounds per automated procedure, or adopt this rule for the next ones.

### 2. Two durability tiers

Today everything gets the merge pipeline's guarantees, the idle line included. Write two tiers into
`docs/design.md`:

- **Kernel tier**: the list above. Event log, intent and result, crash classes, call-id replies, crash-cut tests.
  As now.
- **Rule tier**: reminders, the idle line, merge watches, review due and scheduling, role stops, the needs-you
  repeat, stop and wake after a usage limit, the nightly trigger, the binary follow. One generic mechanism:

  ```
  rule:  name, when(state) → key and action (a line, or one idempotent call)
  pass:  for each rule whose condition holds and has no fact did{key}: act, then record did{key}
  ```

  A rule has no intent, no crash class, no replay case, no claim (it runs in the loop's goroutine), no verb and no
  fold of its own: `did` keys are one generic set. Its allowed failure is written down once: after a crash a line
  may go out twice, or one pass late. `messages.go:63-64` already accepts that for lines ("a line may reach its agent
  twice, never not at all").

The tier is a line in every spec and in the review brief, so workers and reviewers price the change the same way.

### 3. Pilot before converting anything

*Pass mark and order superseded by the correction: the acknowledgement verb stays, about 150 lines, and the fix
round becomes a rule that briefs the verify, not a reminder.*

Rebuild the idle line as a rule. Today: 354 + 762 lines. Pass mark: under about 120 production lines, one review
round, and no `obor idle` acknowledgement verb (status shows the state; the line goes once per emptying). If it
passes, convert in this order: the fix round and verify lifecycle (to refusal plus reminder), `mergewatch.go` (271),
`rolestop.go` (307), the needs-you repeat in `messages.go`. If it fails, stop and keep rule 1 only.

### 4. A verb is one critical section; effects only in the loop

T-852 started this: the verb records its decision and answers; the loop does the work. Finish it. A verb handler
does exactly: check the state table, append one record, fold it, reply, all under `mu`. It never starts an effect.
Then verbs are atomic against each other and against the loop's picks, and the only live claim left is "this task
has an effect in flight".

What can go once every verb is converted: the mutexes `verdicts`, `submitMu`, `loopMu`, `sessions`, `configMu`,
`vf`, the outbox `claim`, the maps `ended` and `admits`, and most of `busy`. Start with the verify admission (two
paths today), then `brief --fresh`, then the `resolved` verdict's check, then `dispatch` and `drive`. One verb per
task, each a deletion. This is the change that removes the class of finding behind RV-42 #1, RV-61 #3, T-730, T-733
and T-855.

### 5. One row per verb

One table: name, usage, group, roles allowed, handler, parameter type. `Listen`'s lists, the help table, the role
check and the CLI's usage read it. Each method gets its own small parameter struct in place of the shared `Call`.
Replies become a struct with a `text` field for people and typed fields for the web and tests. Mechanical, and it
ends the drift tests.

### 6. Snapshot at upgrade

When the store's schema moves, serve writes each open task's current state as records in today's shapes, marks the
older rows as archive (shown by obor web, never folded), and replays from the snapshot. Each release can then delete
the readers of shapes older than its snapshot (item 10). It also makes "delete a concept" delete code.

I considered the larger form, state rows in place of the fold, and do not recommend it. `internal/app` has 34k lines
of tests written against records. Tiering stops event sourcing from spreading, which is most of the benefit, at a
fraction of the risk.

### 7 and 8. View and guard

- **View**: a test that fails when `types.ts` and the Go structs disagree, or a 60-line generator in `make web`.
  Fold `phase`, `stage` and `shown` (`states.go:170-203`, `block.go:58-67`) into one value.
- **Guard**: I rejected a cut on 11-02 because nothing replaced the emulator. That holds. The replacement to probe is
  enforcement the CLIs already have, generated per role at launch: Claude Code permission and sandbox settings through
  `--settings`, Codex `-s workspace-write` (the advisor already runs under it), OpenCode `permissions[]`. Until a probe
  passes, only log which rule denied, and drop rules with no hits after a month. Low priority: the guard is not
  growing.

## 3. What to stop doing in the process

1. **Stop calling flow automation a simplification in the plan.** Every spec gets two lines: `Tier: kernel|rule`
   and `Adds: <each new state, step, verb, fact>` or `Adds: none`. The owner sees the price at approval. This reports
   size, it sets no target (`docs/decisions.md:166`).
2. **Stop prescribing the mechanism in a rule-tier spec.** State the outcome and the allowed failure. No restart
   rows in its case table.
3. **Stop crash-window and interleaving findings on rule-tier code.** The brief already says "only findings a real
   config or workflow hits" (`skills/lead/review-brief.md:46`). Add: on rule-tier diffs such findings go in one
   "accepted by tier" line unless they break a kernel guarantee.
4. **Stop reviewer rounds on fix tasks of rule-tier findings.** Today's chain was RV-113, T-855 to T-857, RV-114,
   T-859, RV-115, T-860. The integrator already reads every merge and sends back about 8% (`docs/decisions.md:13`);
   that is enough for those.
5. **Stop coverage-ratchet findings.** RV-108 #2 ("checks coverage 84.0<86.1") produced a task whose only output is
   tests. Carry on with the pruning plan (`docs/research/test-pruning.md`: about 12k redundant lines).
6. **Stop keeping the transitions twice.** `states.go:10` says the code mirrors `docs/state-model.md` row by row.
   Keep the concepts in the doc (the log, crash classes, the failure rule) and generate or drop the row tables.
7. **Keep**: the integrator on every merge, two review rounds at most, `make check` as the gate, the advisor before
   decision tables.

## 4. Is Go the right tool?

Yes. The honest comparison, against the constraints (one static binary, no runtime dependency beyond git and herdr,
macOS and Linux, agents editing it all day):

| Option | What it would fix | What it costs | Verdict |
|---|---|---|---|
| Go, new shape (§2) | The slope: obligations per change | Weeks of ordinary tasks, no flag day | **Do this** |
| Rust | Sum types and exhaustive matches: illegal state combinations stop compiling, fewer "state not handled" findings | Slower builds on the gate, an async runtime against the dependency rule, agents slower in concurrent code, no fewer lines | No |
| TypeScript, compiled with Bun or Deno | Shared types with the web page, perhaps 25 to 35% fewer lines of glue | A 60 to 90 MB binary, 30 to 50 ms start on every hook call, npm supply chain in a tool with authority over the repo, the "no node or bun" rule | No |
| Elixir / OTP | The best conceptual fit: supervised state machines with timers and mailboxes | Not a static binary in practice, slow start for hooks, agents less fluent | No |
| A durable-execution engine (Temporal, Restate, DBOS) | It is `App.do` and replay as a product | A server or Postgres at runtime; an embedded library breaks the dependency rule and brings its own workflow-versioning problem (item 10 again) | No; take only the lesson: durability for the workflows that need it |
| Python | Fewer lines | A runtime | Out by constraint |

**What a rewrite would cost.** 46.9k production lines and 76.8k test lines, whose behaviour lives in those tests and
in 91 KB of state model. At today's measured rate (2.3k production lines added over 13 reviewed merges) a port is
four to eight weeks of the whole swarm, run by the tool being rewritten, with a double implementation for the
duration. The owner stopped exactly that on 10-13 ("after phase 1 Go was 7.7k lines and bash 10.6k unchanged",
`docs/decisions.md:173`).

**What a rewrite would fix.** Verbosity (item 6's boilerplate, JSON structs, error plumbing) and, in a language with
sum types, some state bugs. It would not touch items 2, 3, 5, 8, 10, 11 or 12, which are design, and they are where
the lines go.

**What Go gives here and should not be traded**: builds in seconds (gate time), a cgo-free static binary, a standard
library that covers sockets, process groups, HTTP and embed, and agents that write it reliably. Its one real gap for
this job is sum types. A `stateRow` table plus row tests is the Go answer and it is in place.

**Moving parts out of code.**

- **Into the agent CLIs, as text**: no. *(First pass said yes; withdrawn after the owner's correction: agents drop
  procedures, and a skill cannot fix that.)*
- **Into the agent CLIs, as enforcement**: the guard's replacement, after a probe (§2, 8). Also worth one probe:
  Claude Code now reports its own quota auto-resume (`docs/research/agent-cli-and-herdr-capabilities.md:108`), which
  may cover part of `stopped.go` (438) and `stopwake.go` (294) for Claude agents.
- **Into herdr**: little. Lane worktrees could use herdr's `worktree.*` calls (docs only so far); small gain.
- **Into config**: no. Behaviour in config needs an interpreter, and `internal/config` is already the second
  fastest-growing package (+554).

## 5. Plan

*Steps 0 to 3 are superseded by "The plan, in these terms" in the correction. Steps 4 to 7 stand.*

| Step | Task | Who | Done when |
|---|---|---|---|
| 0 | Owner decides the scope rule and the kernel list | owner | an entry in `docs/decisions.md` |
| 1 | Tiers in `docs/design.md`; `Tier:` and `Adds:` in the spec template; two lines in the review brief | lead | merged |
| 2 | Pilot: idle as a rule | one worker, hard | under ~120 production lines, one review round; else stop |
| 3 | Fix round and verify to refusal plus reminder; then merge watch, role stop, needs-you repeat | one task each | each task's net is negative |
| 4 | Verbs as one critical section: verify admission, `brief --fresh`, `resolved`, `dispatch`, `drive` | one task per verb | a mutex or claim deleted per task |
| 5 | Verb table, typed parameters and replies | 2 to 3 tasks | `Listen`'s lists and the help drift test gone |
| 6 | Snapshot at the next schema bump; delete old-shape readers | 2 tasks | the 64 sites reduced, replay starts at the snapshot |
| 7 | `types.ts` drift test | 1 task, easy | merged |

Steps 0 to 2 are a day. Steps 3 and 4 are the ones that change what a flow change costs. Report each task's net
production lines in its `task_done` report, as today's were, so the slope is visible.

## Risks

- **Reminders get ignored.** *(Superseded: the corrected plan has no step that relies on a reminder.)*
- **Rule-tier conversions remove behaviour a review once asked for.** A duplicated line after a crash will happen
  and must be accepted in writing first (step 0).
- **Step 4 edits concurrency code that works.** One verb per task, no behaviour change, the existing concurrency
  tests as the oracle.
- **My size estimates are from reading.** The pilot is there to replace them with a measurement.

## Flagged, not expanded

- `internal/config` (+554) and `internal/view` (+321) are the only other packages that grew since v0.20.1. Worth a
  look in the deep dive.
- Production code carries 1,620 ticket references (T-, RV-, DD-) in comments; comments are 14% of the lines. Not a
  growth driver, but it is history that `git blame` already holds.
- `docs/state-model.md` at 91 KB is past what a worker reads before a task. If workers stop reading it, it stops
  being a contract.

## Combined plan

Second pass, 2026-11-05: `docs/reviews/DD-architecture.md` (all of it) reconciled with the corrected plan above. This
section is the one to decide from; where it differs from anything earlier in this file, it wins. DD tasks 2 (config
keys, flags) and 3 (lane rows) are already dispatched and are left out. Line figures marked DD are the deep dive's
measured deletions; the others are my estimates from reading.

The two documents agree on the cause from two sides. The deep dive shows that each replacement kept what it replaced
(DD §6.1). This file shows that each new automaton pays about twelve obligations (§1). The plan below removes the
leftovers first, then lowers the price.

### 1. The deep dive's items

**Stand as written** (direct deletions, each with a test oracle):

| DD item | What | Note |
|---|---|---|
| R1 | `obor review verify` by hand goes | Do it now. The later "verify as a rule" then has less to preserve. Until then a verify whose git failed still waits for the lead, as today |
| R2 | `refs/obor/reviewed` and close's fallback go | The head comes from the brief's fact |
| R3 | Five dead fold branches go | Its replay probe (fold each live store before and after, state equal) becomes the oracle for every later task here |
| U2 | T-858's idle input goes | |
| D1-A, D4 | The CLI's second reviewer stop; one fake `ReviewGit` | |
| U1 | Publish admission only for main-checkout writers | This settles the point I left open: the scope is a function of the one head sha. Route `Detach` through `gitLocked`, as DD says |
| D2, D3 | One usage source, one git runner | D2 is all I keep of my "verb table" for now |
| M2 | `obor review close` becomes a serve call | Kernel: it commits on main, under `config_set`'s path. It gives serve the `review.closed` fact, so the reviewer's stop needs no git poll (D1-B) |

**Changed or subsumed by the rule tier:**

| DD item | Change |
|---|---|
| U3, U3b (idle ping, ack) | Not deleted: rebuilt as rules in the pilot. See decision 4 |
| M1 (one back-reference on `event.Message`) | Not one high-risk task. The pilot defines the single field (`Of {kind, seq}`); it is also the rule pass's "done" key. Each conversion moves its feature onto it: idle, watch, stop and wake, verify (`VerifyFail`, `FixDrop`). What is left afterwards (`Needs`, `Drive`, `Stopped`, `Ends`) is decided then |
| DD §6.5 (one decide-and-append lock) | Same as my "a verb is one critical section". Last, after `vf` and the admissions are gone, when no lock is held across git |
| DD rule 2 (a compat branch expires by census) | Replaces my "snapshot at upgrade", which I drop: the census is cheaper and needs no new mechanism |

**Dropped or parked:**

- DD task 5 as written (drop the ping) and DD task 8 as a standalone task: replaced as above.
- From my first pass: the snapshot, typed per-method parameters, the `types.ts` generator, any guard work. None is
  where the growth is.
- S2 and S1 (a): no task until the owner decides (below).

### 2. Process rules, one list

For `docs/design.md` (rules 1 to 5), the spec template (the three spec lines) and the review brief (rule 6).

1. **Nothing depends on an agent remembering.** Every step of a procedure has one guarantee: serve does it as a
   rule, serve does it in the kernel, or a verb refuses until it is done. A judgment is a standing obligation, never
   a line sent once. Spec line: `Guarantee: refusal | rule | kernel`, per step.
2. **A replacement deletes what it replaced.** Spec line: `Replaces: <mechanism>`, with its deletion as an
   acceptance item, or why both stay and until which release.
3. **A spec names what it adds.** Spec line: `Adds: <each state, step, verb, fact, Message field, config key>` or
   `Adds: none`.
4. **No by-hand twin of an automated path**, unless a store shows it used within one release.
5. **A compat fold branch names its release.** A census of every registered store at each release deletes the
   branches with no records. A command that changes state is a serve call; a CLI does no multi-step work.
6. **Review brief.** "What did this make redundant? Name it or confirm it is gone." On rule-tier steps, a crash or
   interleaving finding counts only when a kernel guarantee breaks, and its fix task gets the integrator, not a
   reviewer round. No coverage-ratchet findings. A rule's test is one table (condition, action); deleting a
   mechanism is how tests are pruned.

### 3. Owner's decisions

| # | Decision | Recommendation | Why |
|---|---|---|---|
| 1 | The three guarantees, and the rule tier's failure mode (after a crash an action may run twice or one pass late) | **Yes** | It is the only way a flow change gets cheaper without relying on memory |
| 2 | S2: drive and land (about 1,000 production and 1,500 test lines, one use in two days) | **Keep one release, frozen.** Decide at the next minor by census: delete unless the stores show regular use | Two days is no evidence either way, and it is the largest single feature added since v0.20.1 |
| 3 | S1 (a): serve starts the drive agent after the stop. S1 (b): `obor stop --force` drops nothing | **(a) waits for decision 2**; yes if drive stays. **(b) yes now** | A CLI that polls and then acts is a second copy of the loop, and it fails if the caller's process ends. (b) removes a destructive step: tasks stay open and continue at `obor start` |
| 4 | U3 and U3b: the idle ping and the acknowledgement | **Keep both**, rebuilt as rules. This differs from the deep dive | The ping is what makes idle a standing obligation instead of a line sent once, which the owner's constraint rules out. The acknowledgement is how "empty on purpose" stops the ping. They go or stay together. Zero pings in one day shows the lead answered that day, nothing more. Count again after a release |
| 5 | M3: the red-under-load re-run (`merge.timeout_grep`) | **The axis-alies lead measures first**; delete if no gate was saved by a re-run in one release | Only that project sets it |
| 6 | R1: the lead loses `obor review verify` and `--findings` | **Yes** | Never used by hand in any store; the fix round asks for every verify |

### 4. Task order

A wave's tasks have disjoint files and run at once. `messages.go`, `event/data.go` and the review files are the
hotspots that force the sequence: `messages.go` is touched by A, C, E, H and J in that order, `event/data.go` by A,
C, G and H. Net lines are production / tests.

| Wave | Task | Items | Files (exclusive) | Net lines | Needs |
|---|---|---|---|---|---|
| 1 | **A. Pilot: the rule pass, idle on it** (ping and ack kept) | pilot, U3, U3b, first use of M1's field | new `internal/app/rules.go`, `idle.go`, `messages.go` (idle branch), `event/data.go`, `loop.go` (one call), `idle_test.go` | about −140 / −400 | decisions 1, 4 |
| 1 | **B. `obor stop --force` drops nothing** | S1 (b) | `cmd/obor/sleep.go`, `sleep_test.go` | about −45 / test edits | decision 3 |
| 2 | **C. Review cycle leftovers**, and the replay probe | R1, R2, R3, U2, D1-A, D4 | DD task 1's list | −210 / −400 (DD) | decision 6 |
| 2 | **D. Role stop as a rule** (integrator; the reviewer's half keeps its git poll until I) | rule | `rolestop.go`, its tests | about −120 / −200 | A passed |
| 3 | **E. Admission only for main-checkout writers** | U1 | DD task 4's list | −35 / −240 (DD) | C |
| 3 | **F. One usage source, one git runner** | D2, D3 | DD task 6's list | −50 / 0 (DD) | C, DD task 2 |
| 4 | **G. Verify as a rule; the link from the spec's `Fixes:` line** | (a) + (b); `VerifyFail`, `FixDrop` onto the one field | `reviewverify.go`, `reviewfix.go`, `review.go`, `event/data.go`, `internal/spec`, `dispatch.go` (read the line), their tests | about −300 / −500 | A passed, C, E |
| 5 | **H. Needs-you repeat, then merge watch, as rules** | rules | `messages.go`, `mergewatch.go`, `event/data.go`, their tests | about −120 / −250 | A passed, E, G |
| 5 | **I. Close through serve** | M2, D1-B | DD task 7's list; in `event/` only one step constant in `steps.go` | −105 / smaller (DD) | G |
| 6 | **J. One decide-and-append lock** | DD §6.5 | `app.go`, `status.go`, `verdict.go`, `fetch.go`, `sessions.go`, `messages.go` | about −100 / interleaving tests | G, I |

Pass mark for A: net production lines negative, one review round. If it fails, D, G and H are off and the rest
stands: C, E, F, I and B are deletions that need no rule tier.

Not in the order: stop and wake (convert last, if at all: most of it is per-kind detection); the stop step (kernel;
whether `halt` becomes a table row is still open, and the deep dive did not cover it); the rest of M1; S2 and S1 (a).

**Expected total** if every wave lands: about 1.2k production and 2.5k test lines removed, of which about 450 / 750
are the deep dive's measured deletions and the rest my estimates. A decision to delete drive adds about 1,000 /
1,500. The larger effect is the one neither figure shows: after waves 1 to 5 a new flow step is a rule of a few
dozen lines, with one table test, and a replacement arrives with its deletion.
