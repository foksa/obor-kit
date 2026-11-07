# Chatter census: which obor messages cost agent turns without needing action

Window 2026-11-01 .. 11-07 ~10:00 (UTC timestamps in the event log). Researcher run for the lead; read-only, no git writes.

## Answer

**The wake is the cost, not the bytes.** A lead wake re-reads the lead's whole cached context: 227k tokens on the first API call, on average. The whole chain after a line is ~4.7 calls and ~1.0M cache-read tokens. The line itself plus Claude's wrapper (39 + 543 chars, ~140 tokens) is under 0.1% of that. Shortening lines (needs-you "short", the 67-char command menu) saves about 6k tokens a week and no turns. Only fewer wakes, or fewer calls per wake, save anything.

Lead sessions in the window made 2,917 API calls and read 616M cache tokens. 318 wake chains started by a cross-session line made 1,488 of those calls (51%) and read 326M (53%). That is an upper bound: each chain includes whatever real work the line triggered. The lead received ~354 lines in all. 318 arrived as turn-starting messages. About 36 arrived mid-turn as `queued_command` attachments, which cost no extra wake.

**The big finding is outside the lead.** 543 of ~555 fresh job sessions (integrator review/resolve 386 of 396, worker 157 of 158) spend their first API call on `ToolSearch` to load the deferred `job_next` tool. 368 of 396 integrator sessions then spend a second call reading their own role text with Bash. That is 2 of the integrator's median 7 calls per job, and both are removable.

**Most of the lead's census is real work or already dying:**
- Merge-watch lines are 116 of 130 through 11-04 and 14 from 11-05 on. They were 12 on 11-05, 2 on 11-06 and 0 on 11-07. They are the lead's own `obor watch --note` reminders, and `Review: on merge` and the verify rule (T-878, T-924, T-932) made them obsolete. No code needed.
- Reviewer "finished" lines are 1:1 with the lead's own briefs (147 briefed, 142 finished lines). 70% of the chains were genuine follow-ups (spec, dispatch, verify, brief).
- 35 of 36 needs-you wakes were acted on, 30 of them with resume/sendback/drop.

**"57 of 63 plain approved" does not mean "clean".** It counts the close subject's status word. 39 of the 63 close subjects name findings or fix tasks. Only 7 say "no findings". The upper bound for "the lead only pressed close" is 32 of 115 reviewer wakes (28%).

**Ranked proposals** (detail in "Proposals", ranked there by turns saved per line of change):

| # | Change | Saves per week | Lines | Removes |
|---|---|---|---|---|
| 1 | Idle line stops asking for an ack | ~16 lead calls | 1 | step |
| 2 | Don't defer `job_next` (no ToolSearch first call) | ~543 calls, ~16M cache-read | ~3-10 | step |
| 3 | Inline the integrator role text in its review/resolve job (like T-603 did for workers) | ~368 calls, ~13M | ~15 | step |
| 4 | Reviewer "finished" line names the RV file and its result line | ~51 discovery calls, ~12M | ~25 | step |
| 5 | Delete the `[obor] T-n: stopped <agent>` echo of the lead's own stop | 5 wakes, ~2M | -10 | mechanism |
| 6 | Needs-you repeat: cap at one, or drop | 8 wakes (one night), 1.4-8M | -35 | mechanism |
| 7 | Auto-close a zero-finding review | 7 (proven) to 32 (bound) wakes, 7-34M | ~100 plus a prose parser | adds a mechanism |
| - | Merge-watch lines, "needs-you once and short", wrapper footer | nothing more to gain (already gone / bytes only / harness text) | 0 | - |

Do 1-4 now. They are small and each removes a step. Do 7 only after 4 has run for a week: 4 puts the result on the line and shows how many closes are really zero-finding. Sandbox-limited probes ("the lead ran the socket tests outside the sandbox", RV-112/115/116/117/122/128) show the lead's close sometimes does real checking.

## Method and what could not be measured

- **Transcripts.**
  - Claude: `~/.claude/projects/-Users-djordjeradakovic-Work-obor-kit*` (main = lead, integrator and reviewers started in the checkout; `-wt-L-*`, `-wt-O-*`, `-wt-S-*` lanes; `-wt-integrator`; `-wt-deep-dive`; `-wt-review`).
  - 1,364 user-turn rows in the window. Per row I took the next assistant calls until the next real user turn, deduplicated by message id.
  - "Turn cost" is `usage.cache_read_input_tokens` of the first call (the minimum a wake costs) and the sum over the chain (the upper bound).
  - "Acted" is judged from the chain's tool calls: `obor review close`, spec/dispatch/brief/verify/resume/sendback/drop/pause/move, release, edits, commits.
- **Sender side.** `serve.log` has only HH:MM:SS with no date, and mostly no message bodies. I used the event log `.obor/state/obor.db` (opened `-readonly`): `events` (388 `message` facts, 54 `notify.*` intents, `idle.ack`, `briefed`/`worked`, prompt intents) and `prompts` (857 rows). Counts from it match the transcripts: 22 idle lines in the log are 4 wakes plus 18 queued mid-turn.
- **Codex (reviewer terra).**
  - 16 reviewer rollouts survive: 15 in `~/.codex/archived_sessions` (RV-119..RV-130 and RV-9, 11-05..11-06) plus 1 live (RV-131, 11-07).
  - RV-68..RV-118 (11-01..11-05 morning) had no rollout on disk.
  - The other 11-0x rollouts in `~/.codex/sessions/2026/11` are owner-typed (composer) or guardian-review sessions, not obor messages.
- **opencode:** `opencode.db` has 20 sessions, all Aug-Sep, none in this project. Nothing to count.
- **Not measured.** Review files under `docs/reviews/RV-*.md` are pruned after close, so "zero findings" is only known from close subjects and 15 reviewer rollouts. The weekly numbers are one week and include heavy refactor days (11-02, 11-05). The "~35k per early call" figures in the proposals are estimates from `first_cr` (28-30k for fresh job sessions).

## Census by message kind (to the lead)

Sender counts are from the event log (388 lines). Wakes, cost and "acted" are from lead transcripts (10 lead sessions, 318 turn-starting lines).

| Kind | Lines/wk | Per day 11-01..07 | Wakes (+queued) | First call / chain / calls | Receiver acted? | Sent by |
|---|---|---|---|---|---|---|
| Reviewer finished (ob-terra 55, ob-rev 66) | 121 | 17,32,13,23,22,13,1 | 115 (+5) | 234k / 1.25M / 5.4 | follow-up 81 (70%), close-only 32 (28%), other 2 | `messages.go:507-508` (deliver) |
| Other role finished (deep-dive 7, advisor 6, fable 5, researcher 3) | 21 | - | 18 | 255k / 1.35M / 5.9 | acted 15 of 18 (83%) | `messages.go:507-508` |
| Merge-watch ("T-n merged sha - next step") | 130 | 22,44,20,30,12,2,0 | 118 | 233k / 0.84M / 3.8 | mutating action 102 (86%) | `mergewatch.go:251-265` (`watchRule`), set by `obor watch --note` |
| Needs-you (first lines 55) | 55 | 14,7,7,6,13,6,2 | 36 (+13) | 175k / 0.87M / 4.5 | acted 35 (97%); resume/sendback/drop 30 (83%) | `messages.go:987-1015` (`needsRule`), text `messages.go:1022-1037` |
| Needs-you repeat ("again, 1h") | 8 | 8 on 11-01 | - | - | one fact (T-635 open questions), 8 hourly repeats overnight | `messages.go:76-80`, `:987-1015` |
| Idle ("nothing in flight") | 22 | 0,0,0,8,9,5,0 | 4 (+18) | 346k / 1.13M / 3.2 | 16 of 19 keys answered with `obor idle "<why>"` (a status note); 3 ended by dispatch; 0 pings | `idle.go:155-179` |
| Stopped echo (`T-n: stopped ob-l-0x`) | 5 | 0,0,0,0,2,1,2 | 5 | 141k / 0.43M / 2.8 | no mutation 3 of 5 | `stopped.go:149`, `steps.go:143` (the lead's own move/drop) |
| Cross-project (axis-alies/maps/...) | 15 | - | 15 | 237k / 1.0M / 5.5 | acted 9 of 15 | `messages.go:1099` (`obor send`) |
| Other `[obor]` (review started, dropped, config) | 11 | - | ~7 | - | - | `review.go:468`, `resume.go:277` |

Duplicates and repeats:
- Needs-you: 8 hourly repeats of one fact (T-635, night of 11-01).
- Five task/reason pairs were sent as two separate needs-you facts: T-852, T-877 (gate red after 2 sendbacks), T-921 (rate_limit), two `loop:` and two `RV-n: no pane labelled ob-rev`. They are distinct attempts, not re-sends.
- Re-sent jobs: 1 in 725 (`T-864/1/work`). `deliver.*` results: 457 sent, 20 sent as a start prompt, 0 failed. Delivery is clean.
- Lines that arrived within 120 s of the previous one: 99 of 388. 50 of those were watch+finished pairs, which is moot now that watch lines have stopped.

Needs-you text: 63 lines, mean 389 chars (max 2,348), menu part mean 67 chars (4.2k chars total). 5 lines are raw provider errors (`rate_limit ... [overloaded] ...`, 239 chars mean). The lead's answer to those was always the same: `obor pause --model deepseek` plus `obor dispatch --model sonnet:medium`.

## Census by role

| Role | Message | Count/wk | Source | Cost and need |
|---|---|---|---|---|
| Lead | the table above | 388 lines, ~354 received | see above | 51% of the lead's calls follow a line |
| Worker (lane agents) | work job wake `[obor job T-n/n/work] call the obor job_next tool` | 158 sessions seen (313 work-wake intents) | `dispatch.go:1131-1133`, `fetch.go:29`, `wire.WakeText` | fresh session, first call 30k, then 52 calls, 8.0M each: real work; 157 of 158 begin with ToolSearch |
| Worker | fix job (sendback) as cross-session line | 65 (97 `fix` intents) | `steps.go:335-341`, `fetch.go:235-239` | first call 190k, 9.5 calls: real work |
| Worker | `[obor answer]` | 5 | `resume.go:277` | first call 252k, 32 calls: real work |
| Worker | Stop hook "not done yet - no task_done" | 3 (1,292 Stop hooks ran silently) | `hooks/hooks.json` `Stop`, `obor hook stop` | 3 extra turns |
| Worker | wrong-tab guard (UserPromptSubmit) | 2-3 | `obor hook user-prompt-submit` | only when a human types in the pane |
| Integrator | review job wake | 362 (108 in `wt-integrator`, 254 in the main checkout) | `jobstart.go:197`, wake `fetch.go:29` | 7.6 calls, 449k each, 163M total; 353 begin with ToolSearch; 329 accept calls, 28 sendback, 39 resolved, 5 hold across 396 sessions |
| Integrator | resolve job wake | 34 | `jobstart.go:242-250` | 11.1 calls, 634k each; 33 begin with ToolSearch |
| Integrator | role text read with Bash | 368 of 396 sessions | SessionStart hook says "read it" | a call per session that the worker's job already saves |
| Reviewer (Codex terra) | brief / fresh-start prompt | 55 briefs (rev 29, terra 26) plus 57 starts | `messages.go:1168-1192`, `review.go:495` | first prompt carries the brief; 16 rollouts show 0.4-9.2M input tokens per review (RV-9 aside), 96% cached: real work |
| Reviewer | verify re-brief `[obor obor] The RV-n findings were fixed...` | 1 per fix round (12 `review.verify.request`) | `reviewverify.go:216-265` | real work |
| Reviewer, deep-dive, advisor, researcher, fable | brief via `obor brief` | 20 (deep-dive 7, advisor 6, fable 3, researcher 4) | `messages.go:1192` | real work |
| Owner | herdr needs-you toast | 54 `notify.*` intents (50 facts; once each, retried only on failure) | `notify.go:35-39` | no agent turn; text is the raw reason |
| Any | role-agent usage-limit wake and alarm | rare | `stopwake.go:43`, `:244` | not seen in the window |

## Hooks and wrapper

- **Cross-session wrapper.** Header "Another Claude session sent a message:" is 39 chars. Footer ("This came from another Claude session ...") is 543 chars. About 140 tokens per line, ~50k tokens over ~354 lines. This is Claude Code's text, not obor's, and obor cannot shorten it short of typing the line into the pane. Not worth it.
- **SessionStart hook:** 592 `hook_success` attachments, 1.4 KB each (~350 tokens), once per session. It points the integrator at its role text (see proposal 3).
- **Stop hook:** 1,292 runs, 3 blocking (`not done yet`).
- **UserPromptSubmit hook:** 8 injections: wrong-tab guard (worker), lead's 500k-context handoff nudge.
- **Not obor's:** `silent_turn_reminder` ("The user hasn't heard from you in a while", 1,359 times, 145 chars), `queued_command` task-notifications (41 in lanes), `total_tokens_reminder` and `batching_reminder_sent` come from Claude Code itself.

## Proposals

Ranked by turns saved per line of change; "removes" says whether it takes out a step or a mechanism.

1. **Idle line without the ack request** (`idle.go:179`). The line says "dispatch next, or obor idle \"<why nothing>\"". 16 of 19 idle keys were answered with a status note, and the idle ping never fired (0 `idle: ... (again`). The ack only silences a ping that did not come. Change the text to "dispatch next" and keep the ack for the ping line only. Saves ~16 Bash calls a week at ~0.25M cache-read each (~4M). 1 line. Removes a step. Cost: the lead stops explaining why it idles, which only matters when the ping fires.
2. **Don't defer `job_next`.** 543 sessions per week start with a ToolSearch call whose only purpose is to load the tool the wake line names. The `mcp__plugin_obor_obor__job_next` tool is deferred. Fix on the launch side (Claude Code's tool-search setting, or load obor's MCP tools eagerly). I did not verify which setting does this for the pinned Claude Code version, so this needs a probe: start one worker with the setting and check the first call is `job_next`. Saves ~543 serial calls (~16M cache-read) and the round-trip latency, in front of every job. Lines: probably 3-10 in `internal/launch`. Removes a step.
3. **Inline the integrator's role text in the review/resolve job.** `workText` does this for workers (`dispatch.go:1137-1160`, `roleTexts` `:1166`, T-603: "one read instead of two turns"). The integrator's `reviewText`/`resolveText` (`jobstart.go:180-250`) don't, so 368 of 396 integrator sessions read `templates/agents/integrator.md` (6.8 KB) and `docs/agents/integrator.md` with Bash. Reuse `roleTexts` for the integrator. Saves ~368 calls (~13M); adds ~1.7k new input tokens per job (~0.7M a week). ~15 lines. Removes a step. With 2, an integrator job loses 2 of its median 7 calls.
4. **Reviewer finished line names the review.** `[obor] ob-rev finished (briefed 4m ago)` becomes `... finished RV-n: docs/reviews/RV-n.md - approved|changes requested` (the reviewer template already puts a "Result line: approved / changes requested" first, `templates/agents/reviewer.md:17`). 51 of 115 reviewer chains (44%) open with a discovery call (`ls -t docs/reviews`, `git log`, `obor status`). Saves ~51 calls (~12M). ~25 lines (read the first line of one file when composing the line at `messages.go:507`). Removes a step. Also gives proposal 7 its data.
5. **Drop the stopped echo.** `[obor] T-n: stopped ob-l-0x` is serve confirming a stop the lead just asked for (move/drop). 3 of 5 wakes needed nothing. Keep a line only when the stop failed (the needs-you path already covers it). Saves 5 wakes (~2M). About -10 lines in `stopped.go:149` / `steps.go:143`. Removes a mechanism (a line kind).
6. **Needs-you repeat.** The repeat (T-621/624: 30 min, then hourly) fired 8 times in the window, all for one open-question fact overnight. Cap at one repeat, or drop it: the herdr toast and `obor status` already carry an unanswered needs-you. 8 wakes one night, ~1.4M (first calls) to ~8M (chains). -35 lines (`messages.go:76-80`, `:987-1015`, `needsKey` plumbing). Removes a mechanism. The risk is a lead that never saw the first line; the repeat is the only second chance.
7. **Auto-close a zero-finding review.** A rule (`rules.go` pattern) that calls the existing `reviewCloseCall` (`reviewclose.go:58`) when the review file's result line is "approved" with no findings and the reviewer reports no sandbox-limited probe. Saves 7 (proven by close subjects) to 32 (upper bound: close-only chains) wakes, 7-34M cache-read. ~100 lines plus a parser of reviewer prose. It adds a mechanism and a failure mode (a wrongly clean review closes unseen). Do it after 4, with a structured verdict (`Result: approved`, `Findings: 0`, `Probes not run: none`) in the review file so serve reads fields, not prose.

Not worth a change:
- **Merge-watch lines.** Already gone by 11-06 as a side effect of `Review: on merge` and the verify rule; a guidance line for the lead ("no watch whose next step serve now does") is enough.
- **Needs-you "once and short".** Bytes only. "Once" is proposal 6.
- **Coalescing delay.** A 60 s hold on non-urgent lines would join 58 of 388 pairs, and 35 of those 58 were watch+finished, which are gone. Not worth the latency.
- **Worker/integrator jobs.** Fix, work, resolve and review jobs are real work. The integrator accepts 329 of 401 verdicts, a policy question for the owner (skip the integrator review for small tasks), not message chatter.

## Unknowns and how to find out

- **Whether the lead's close-only wakes were clean.** Review files are pruned. Keep them (or the `Result` line) for a week after proposal 4 to count zero-finding reviews directly.
- **Which setting un-defers `job_next`.** One probe: launch a worker with a candidate Claude Code setting and read its transcript's first tool call.
- **Cost beyond cache-read.** I counted cache-read tokens only. A hit also costs output and cache-creation tokens (lead chains: 2.0M cache-creation over 318 wakes, small).
- **The reviewer before 11-05.** No rollouts. The sender-side counts (121 finished lines, 147 briefs) cover it.

## Reproduce

- **Transcript scan.** Python over `~/.claude/projects/-Users-djordjeradakovic-Work-obor-kit*/*.jsonl`. Classify user turns that begin "Another Claude session sent a message:" or "[obor job"; chain = assistant messages until the next non-tool-result user turn; use `message.usage`. The scripts are in the researcher scratchpad (`ext.py`, `cls.py`, `act.py`).
- **Sender side.** `sqlite3 -readonly .obor/state/obor.db "select ts,data from events where step='message' and kind='fact' and ts>='2026-11-01'"`; prompts: `select kind,count(*) from prompts where ts>='2026-11-01' group by 1`.
- **Review closes.** `git log --since=2026-11-01 --format='%s' | grep -E '^RV-[0-9]+ closed'` (63 closes).
