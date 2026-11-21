# internal/app: decisions tested at two levels (TP-7a)

For the lead. Brief: TP-7a of `docs/research/test-plan-advice.md` §2 c. Read-only: I ran no test. Every row below was
read from the test bodies in `internal/app/` (paths are relative to it).

## Answer

30 `X_test.go` / `X_ext_test.go` pairs. A pair counts only when both tests assert the same decision from the same inputs.

- **11 pairs have at least one such decision: 27 rows in all.** Resume 7, watchdog 5, messages 4, rules 2, status 2,
  stopped 2, and one each in idle, mergewatch, pause, prompt, reviewclose.
- **19 pairs have none:** cutwait, disk, dispatch, drive, fetch, jobs, keeplane, lanepick, lanerows, lanescan, loop,
  nightly, panes, rolestop, serve, steps, verdict, waiting, and rig (`rig_test.go` has no test; `rig_ext_test.go` has only
  `TestMain` at :2605. Both are rig files).
- **70 near-misses** are listed per pair. They share an area, but the input or the decision differs.
- **Which stays:** the in-package test in every match row. Where the ext test asserts something the in-package one
  does not, the row says what to move first. A match is a decision both assert, not an equal pair of tests: most ext
  tests carry unique assertions (the socket reply, status shape, an inbox post), so "drop the ext test" is
  not free. Most rows say "drop the ext test only after moving its unique line", or keep it trimmed to that line.
- **Weak matches** (marked *weak*): rules 1, stopped 2, watchdog 4, messages 2 (the second differs in why the brief waits;
  the fourth is only the first assertion of the ext test). The decision is the same, but the inputs agree only
  roughly. Check these before cutting.
- **Closest near-misses, if the lead wants to merge more:** `lanerows` (add a merge `Head` to the in-package row),
  `jobs` (job.lost after Gone), `mergewatch` set watch (add a third task row), `dispatch` paused-models wait text.

The matches come from three parallel readers of 9–10 pairs each; I checked the one near-miss they had not verified
(mergewatch set, below) myself. I did not re-read every row of the other 29 pairs, so treat a "no overlap" as the
reader's judgment, not a proof.

## Matches

Format: in-package test, ext test, the decision, the inputs, and what stays.

### idle
- `idle_test.go:106` TestIdleNowFreshProject ↔ `idle_ext_test.go:15` TestIdleFreshProject. A project that never merged or
  dropped is idle with "no merge yet" and no end key: text `IDLE (last: no merge yet)`. Input: fresh project, no records.
  **Stays: in-package** (direct `idleNow`/`idleText`, no rig). The ext test also asserts the ack reply, status idle nil,
  no line before the first end, and NOT IDLE after a dispatch; none of that is in-package. Trim the ext test to those.

### mergewatch
- `mergewatch_test.go:14` TestWatchRule (cases "a single task merged…", "a restart after the line: not sent again") ↔
  `mergewatch_ext_test.go:14` TestWatchedMergePostsOneLine. One line `[obor] T-1 merged <sha7>`, the watch ends, no second line
  after a restart. Inputs: watch T-1 with no note, T-1 merges, restart. **Stays: TestWatchRule.** Ext only: the watch
  replies (`WATCHING T-1`, `WATCH T-1 since 0s`, `NO WATCHES`), `Status().Project.Watches`, and the line reaching the lead's
  inbox. These go through the socket, not the rule: keep them in one socket test.

### messages
- `messages_test.go:996` TestBriefSupersedeSurvivesACrash (note=second) ↔ `messages_ext_test.go:668`
  TestBriefSupersedesAQueuedBrief. Brief "first" is queued for a busy pj-rev; brief "second" replies "(replaces 1 undelivered
  brief)", only "second" waits, "first" is never delivered, "second" is delivered once. **Stays: in-package** (adds the crash
  and the stored-reply retry). Ext only: delivery counted through a real inbox post (`inbox.count`).
- `messages_test.go:996` (note=) ↔ `messages_ext_test.go:618` TestBriefSupersedesAFreshStartsOldLine. An empty brief after
  "first" replies "(replaces 1 undelivered brief)"; "first" is never delivered, also after a restart. *Weak:* in-package "first"
  waits because the agent is busy, in ext because it has no inbox; the supersede decision does not read that. **Stays:
  in-package.** Ext only: the superseded record's By/Seq and the newer brief's empty Line (`supersededLines`).
- `messages_test.go:840` TestBriefCompletionReplay (fresh/reload) and `:1202` TestBriefWorkDurableAtOnce (fresh/brief) ↔
  `messages_ext_test.go:535` TestBriefOfAFreshStart. A text-less brief to a working agent gives one `[obor] pj-rev finished
  (briefed …)` line to the lead once the agent is idle. **Stays: `:1202` row fresh/brief** (it reloads before the idle).
  Ext only: the exact "briefed 0s ago" and that nothing is typed into pj-rev.
- `messages_test.go:1361` TestRunningLeadHint ↔ `messages_ext_test.go:415` TestSendQueuedUntilTheLeadIsRecorded (first
  assertion only). *Weak:* a send to a lead with no inbox record replies with the hint to run `obor restart pj-lead` or `obor
  lead`. **Stays: in-package** (reads the docs too). The rest of the ext test is not a match.

### pause
- `pause_test.go:44` TestPauseWaitsForDispatchAdmission (last assertion) ↔ `pause_ext_test.go:121`
  TestPauseRefusesTypesDispatch. A `--types sonnet` dispatch under a pause "quota" is refused with `paused: quota`.
  **Stays: in-package** (its point is admission order). Ext only: "records nothing" (no dispatch fact for T-1): add it to
  the in-package assertion before dropping the ext test.

### prompt
- `prompt_test.go:90` TestPromptResumeClears ↔ `prompt_ext_test.go:180` TestStartPromptDelivery (row "work.N after a
  sendback (no start)"). After two delivery waits and needs-you{job not delivered}, `obor resume` sends work.2 through the
  typed prompter, with no second agent start. *Weak:* work.1 goes by the typed prompter in-package and by the start prompt in
  ext; only work.2 is the same. **Stays: in-package** (asserts the box is cleared before each send). Ext only: result "sent",
  `called("start")==1`, the typed wakes to pj-l-01.

### resume
The in-package side is `resumeAct` in TestResumeActByState (`resume_test.go:652`) or a task-level call; the ext rows run the
same state through the socket. The table row stays in every case below.
- row "held", no flag ↔ `resume_ext_test.go:115` TestNeedsYouAnswerPathsAreAccepted "held → resume with the lead's decision"
  (:155). A held task takes a plain resume as reprompt. Ext only: the reply "RESUMED T-1: reprompt (held)", the unblocked fact,
  the decision text in the next review job and after a restart.
- row "held --sendback" (+ the `resumeNeedsText` assertion) ↔ `:178` "held → resume, no decision: refused". A hold's resume needs
  its text; `--sendback` is refused "--sendback does not fit…". Ext only: "--regate does not fit" on a hold (no hold+regate row in the
  table) and that no fact is recorded.
- row "red past K" ↔ `:202` "red past K, neither flag: refused naming both". Refused with "say --regate or --sendback". Ext
  only: the answerPath text and nothing recorded.
- row "red past K --regate" ↔ `:221` "red past K → resume --regate". Takes the regate action. Ext only: the reply "RESUMED T-1:
  regate (red)".
- row "red past K --sendback" ↔ `:228` "red past K → resume --sendback". Takes the sendback action. Ext only: the reply
  "SENDBACK T-1".
- row "working, nothing asked" ↔ `resume_ext_test.go:254` TestResumeLineToAnIdleWorker and the `asked` helper at :14
  (first answer). "T-1 is working with nothing to resume". Ext only: the idle-worker path "RESUMED T-1: reprompt (worker idle)".
- `resume_test.go:589` TestResumeRegateOfABlockedReport (subtest "the answer") ↔ `resume_ext_test.go:39`
  TestAnswerSendsTheWorkerTheAnswer. With a worker's Blocked question in force, the lead's text answers it: "ANSWERED T-1: obor
  serve sends pj-l-01 the answer", the question ends, same attempt, no fix job. **Stays: in-package.** Ext only: the answer reaching
  the worker's inbox once and the owner/worker/empty-text refusals through the socket (the delivery path is covered in-package by
  `resume_test.go:66` TestAnswerReachesIdleWorkerHerdrSessionStale).

### reviewclose
- `reviewclose_test.go:21` TestReviewCloseRefusals (case "no open review: no review file") ↔ `reviewclose_ext_test.go:13`
  TestReviewCloseOnTheSocket. A close of a started review with no review file is refused "no open review: no <dir>/RV-5.md
  in …", nothing committed. Inputs: RV-5 started, no file, lead's close "fixed". **Stays: in-package** for the decision. The ext
  test only adds the socket routing (`wire.MethodReviewClose`, config `reviews.dir`): if no other test routes the method, keep it
  as a one-line socket test, which is what its comment says it is for.

### rules
- `rules_test.go:378` TestNeedsRule (rows "the first line at once, once", "a restart sends no line again and keeps the clock") ↔
  `rules_ext_test.go:15` TestNeedsYouIsPostedToTheLeadOnce. *Weak:* one needs-you fact gives the lead one line, not repeated by
  passes or a restart. In-package injects the fact record; ext makes it by a refused dispatch. **Stays: the table row.** Ext also
  asserts the inbox post and one owner toast (rig-only).
- TestNeedsRule row "the project's own lines' needs-you: no line" (`fact(event.MsgTask)`) ↔ `rules_ext_test.go:78`
  TestWaitingLineNeedsYouQueuesNoLine. A needs-you on the lines task queues no needs-you line to the lead. **Stays: the row.** Ext
  also asserts no owner toast and that the original line stays queued.

### status
- `status_test.go:138` TestRunningStepsAreTheCommandStepsWhoseEffectRuns ↔ `status_ext_test.go:53`
  TestStatusRunningGateWhileItsEffectRuns. A gate with an open intent and a running effect is a Running step with Since = the
  effect's start, not in Pending. **Stays: in-package** (pure `runningSteps`/`pendingSteps`; also covers publish, nightly,
  restart, result-closes). The ext test is redundant for this decision.
- `status_test.go:217` TestPendingStepsWaitForTheirEffect ↔ `status_ext_test.go:97` TestStatusPendingAfterMergeFailure. A failed
  after_merge with its intent open is Pending with Error = the failed fact's reason (step prefix stripped) and FailedAt = its
  time, not Running. In-package folds the fact `after_merge: git I/O: index.lock exists`; ext produces it with a failing
  AfterMerge. **Stays: in-package.** Ext only adds that the real loop produces that state.

### stopped
- `stopped_test.go:439` TestTurnWorkerPhases ("hook start"/"hook end") ↔ `stopped_ext_test.go:279`
  TestStoppedWorkerNextTurnSameSecond (also the last step of `:166` TestStoppedWorkerNeedsYou). *Weak:* a worker stopped by
  rate_limit is ended by the turn request, reply "TURN pj-l-01 (the stop of T-1 ended)". Inputs: T-1 working, stoppedCall
  rate_limit, turnCall. **Stays: in-package** (also covers Ended true/false, "no briefs"). Ext only: the resumed fact and facts in
  force.
- TestTurnWorkerPhases ("rollout start"/"rollout end") ↔ `stopped_ext_test.go:307` TestStoppedCodexUsageLimit. *Weak:* a Codex
  turn that ended on usage_limit_exceeded is one stop; the same turn re-read decides nothing; the next turn's start ends it.
  In-package's "rollout end" (t2 Ended, clean) has an ext counterpart only in the tail ("its clean end records nothing"); the
  ext replay and idle-check parts are rig-only. **Stays: in-package.**

### watchdog
In-package rows are entries of `var rows` (`watchdog_test.go:63`), run by TestWatchdogTable (:433). The rows stay.
- rows :65, :66, :74, :76 ↔ `watchdog_ext_test.go:18` TestIdleStallsThenNeedsYou. An idle agent: nothing at 9 min, stalled{idle}
  at 11, needs-you{stalled} after 10 more, nothing 30 min later. Same inputs (one idle pane, ticks). Ext's agent age and facts
  length are status shape, not the decision.
- row :77 (blocked) ↔ `:168` TestBlockedNeedsYou. A blocked pane gives needs-you{blocked} at once and no second record 20 min
  later.
- rows :107, :109 (and :324, done file: no alarm) ↔ `:182` TestPaneExitedNeedsYou. A pane closed with the task open gives
  needs-you{pane exited}; with the task submitted (done file) no alarm.
- rows :267, :269–271 (T-335 replay) ↔ `:202` TestReplayAfterRestart. *Weak:* with stalled{idle} already in the log, a restart
  records no second stalled and escalates to needs-you{stalled} after the next quiet period. Timing differs a little: ext restarts
  30 min after the stall and asserts seq 2.
- row :111 (another group's pane) ↔ `:228` TestPaneInAnotherGroupIgnored. Panes of other groups in the lane's worktree are not
  watched. Ext adds that the status agent list excludes other workspaces.

## Near-misses

Same area, but a different decision or different inputs. One line each. Cross-file ones are marked.

### cutwait
- `cutwait_test.go:14` TestCutEndFact ↔ `cutwait_ext_test.go:18` TestCutStepWaitsBeforeRetry (and :100, :153, :190). In-package
  asserts which fact ends which step's wait (`cutEndFact` on a `proj`); ext asserts the retry delays doubling to a cap, run through
  the whole loop. The only overlap is "a counted failure ends the wait", and the inputs are a fact vs a scripted `boom`.

### disk
- `disk_test.go:69` TestRV59LowDiskPendingGate ↔ `disk_ext_test.go:35` TestDiskLowIsANeedsYouAndHoldsTheGate. Both: a gate does not run on low disk and runs after space is back. In-package: pending gate intent, every append refused, free 1 GiB. Ext: free 2 GiB through a real submit, and the needs-you records (one per transition). Different inputs and different assertions.
- `disk_test.go:34` TestRV59DiskRecordFailure and `:150` TestRV59LowDiskPendingNightly have no ext counterpart.

### dispatch
- `dispatch_test.go:13` TestLaneWaitNamesDirtyFreeLanes ↔ `dispatch_ext_test.go:290` TestDispatchRefusals (cases :313–317). The no-lane *wait* text (dirty-free lanes with paths, "+2 more", all models paused) vs the refusal for a *named* dirty-free lane without `--force`. Different error and input.
- (cross-file) `dispatch_test.go:13` (paused half) ↔ `lanepick_ext_test.go:16` TestDifficultySkipsAPausedModel. Same decision (both models paused: the wait reason is the pauses). Inputs differ: a `noLaneError` struct vs real pause calls, one open-ended and one timed. Ext asserts a prefix plus the whole dispatch flow. If merged, the in-package exact-format test stays.

### drive
- `drive_test.go:17` TestDriveNoStallWhileDriving ↔ `drive_ext_test.go:408` TestDriveReplayedAsDriving. Both reload a driving task as driving; in-package asserts no stall facts, ext no extra records, lane row kept, stored reply after a serve restart.
- `drive_test.go:99` TestLandSupersededByConcurrentDrop ↔ `drive_ext_test.go:380` TestDriveDrop / `:235` TestDriveLandGatesReviewsMergesAndStopsTheAgent. The drop-between-check-and-submit race ("superseded") is asserted only in-package.
- `drive_test.go:167` TestDriveRefusedForAnotherVersion ↔ `drive_ext_test.go:163` TestDriveOfAWorkersTaskRefused. Both refusals; different causes (version skew vs a worker's task).

### fetch
- `fetch_test.go:23` TestEarlyTaskDone ↔ `fetch_ext_test.go:19` TestFetchWorkJob. Both assert the work step result `sent (start prompt)`; in-package injects job_next and task_done *during* the start, ext is the normal flow.
- `fetch_test.go:77` TestStartAgainSameJob ↔ `fetch_ext_test.go:108` TestFetchUndelivered / `:288` TestFetchFixUndelivered. Refused start then resume reuses the same work job vs an unfetched job's retry/needs-you timers.
- `fetch_test.go:662` TestWorkJobSaysEachRuleOnce ↔ `fetch_ext_test.go:19`. Ext only smoke-checks the text.
- `fetch_test.go:780` TestFixJobCarriesAChangedRevOnly ↔ `fetch_ext_test.go:171` TestFetchFixAndTaskDone. Spec inclusion by rev vs the fix job's note and task_done guards.
- No ext counterpart for the job-selection tests (:289–:482) or the role-text tests (:519–:842).

### idle
- `idle_test.go:29` TestIdleRefusalNamesWhatIsInFlight (case "a reviewer's brief") ↔ `idle_ext_test.go:45` TestIdleBlockers ("a reviewer with a brief"). Same blocker; in-package asserts the refusal text, ext asserts no idle status or line after a merge.
- `idle_test.go:29` (case "a needs-you") ↔ `idle_ext_test.go:45` ("a needs-you"). Same blocker; refusal text on T-c vs no idle state with T-2 needs-you after a merge of T-1.
- `idle_test.go:122` TestIdleCallRefusalNamesWhatIsInFlight ↔ `idle_ext_test.go:15` (tail, "NOT IDLE: T-1 open (L-01)"). A submitted task via `idleCall` with and without why, no ack recorded, vs the dispatched-task tail of the fresh-project flow.
- `idle_test.go:29` (case "an open task"), `:122` ↔ `idle_ext_test.go:45` ("a waiting dispatch"). Different blockers; the in-package table has no waiting-dispatch row.
- `idle_test.go:305` TestIdleKeyIsTheRequestEnd ↔ `idle_ext_test.go:208` TestIdleSettleReviewRequestTenSecondsLater. A review request's end keys the idle state vs the settle window resetting on a request.
- `idle_test.go:256` TestIdleKeyIsTheVerifyEnd / `:161` TestIdleWaitsForTheVerifyBrief ↔ `idle_ext_test.go:120` TestIdleAckStopsThePing. Ack and idle-line posting in both, different scenario (verify pass vs plain merge).

### jobs
- `jobs_test.go:30` TestIntegratorJobLostOnlyOnProof (subtest "its pane gone: lost after Gone") ↔ `jobs_ext_test.go:86` TestJobStalled (:124–:145) and `:407` TestJobRecovery. Same decision: job.lost only after `Gone`. In-package: a direct `watchJob` call on a deleted pane; ext: a full serve with the integrator gone and the clock advanced, plus the restart. Closest to a merge. "Between two streams: no lost" has no ext counterpart.

### keeplane
- `keeplane_test.go:34` TestStartFitsNeedsTheRecordedConfigRev ↔ `keeplane_ext_test.go:264` TestKeepLaneRestartsWhenTheConfigChangedSinceItsStart. Overlap only in "same config rev → reuse"; inputs differ (a start record with no rev vs a changed model id / kind args).
- `keeplane_test.go:68` TestStartRowKeepsItsConfigRevAcrossAReplay ↔ the same ext test. Replay keeps the recorded rev vs a live config-change restart.
- `keeplane_test.go:104` TestStartFitsReadsTheRecordedSession has no ext counterpart.

### lanepick
- `lanepick_test.go:29` TestPickLaneTakesALandedCleanLane ↔ `lanepick_ext_test.go:99` TestDispatchNamesIdleLanes. `pickLane` over free/landed/dirty (and `--force`) vs the reply's "; idle: …" listing.
- `lanepick_test.go:11` TestHeldLaneOfTwoDispatchesIsNewestDispatch ↔ `lanepick_ext_test.go:72` TestDifficultyModelsWithNoFreeLaneRefuse. Held-lane attribution vs a refusal when no lane is free.

### lanerows
- `lanerows_test.go:22` TestLaneRowsProjectTheLog ↔ `lanerows_ext_test.go:19` TestFoldPinsTheLandedRowAtTheSubmittedHead. Both: head = the submit result, landed after merge. In-package's merge carries no Head, so it never tests "not the merge's head" (`MergedData{}` vs `MergedData{Head: …}`). Closest to a merge: give the in-package row a Head and drop the ext test.
- `lanerows_test.go:22` (dropped row clears, re-dispatch) ↔ `lanerows_ext_test.go:66` TestLaneRowsHoldConfiguredLanesOnly. Different decisions.

### lanescan
- `lanescan_test.go:212` TestEventBurstSharesOneScan / `:242` TestEventsDuringAScanWaitForNoGit ↔ `lanescan_ext_test.go:15` TestHandEditShowsWithinOneTick. Scan scheduling vs the tick scanning before it decides.
- `lanescan_test.go:21` TestUnknownObservationResetsTheTimers (etc.) ↔ `lanescan_ext_test.go:36` TestDispatchPreflightReadsGitNotTheCache. Cached-table staleness vs dispatch preflight reading git.

### loop
- `loop_test.go:102` TestDoCrashBetweenEffectAndResult ↔ `loop_ext_test.go:1028` TestLoopRecovery. A killed child after one `logDo` effect (intent pending with its class) vs a crash at each workflow step of a whole task that merges once.
- `loop_test.go:353` TestPendingRestartGoesInTheFirstGap ↔ `loop_ext_test.go:802` TestLoopRestartWhileAwaitingAccept. A pending self-restart for a new binary vs a serve restart while awaiting accept.
- No overlap: TestDoRunsOnce…, TestDoFailedEffectStaysPending, TestReplayProbe, TestReplayStateText.

### mergewatch
- `mergewatch_test.go:14` TestWatchRule case "a set: one line once all merged" (watch T-1,T-2) ↔ `mergewatch_ext_test.go:162` TestWatchedSetPostsOneLineAfterTheLast (watch T-1,T-2,T-3). *Verified by me:* same decision, one line `merged (<sha7>)` after the last merge and none before, none again after a restart. Inputs differ only in set size (2 vs 3). Merge by adding a third task to the rule case; ext only: the list replies "waiting T-1,T-3" and the inbox post.
- case "one of the set dropped" (T-1,T-2; line ends "(watch T-1,T-2 ended)") ↔ `:64` TestWatchedDropPostsTheDroppedLine (single task T-1, no suffix). Both fold the multi-line reason.
- case "a single task merged: its line with the note" ("release v1") ↔ `:300` TestWatchNoteOnTheMergedLine. The ext note has a newline folded to one line; the rule case has none.
- `mergewatch_test.go:89` TestWatchNoteEndsWithItsLine ↔ `:300`. A second watch of the same task after the line (reply carries no note) vs the first watch's line with its note.
- No overlap: unwatch, "recorded without of" (rule only); unwatched merge, ended/unknown task, set drop/reject (ext only).

### messages
- `messages_test.go:1312` TestQueuedLeadLineAfterRename (kind=send) ↔ `messages_ext_test.go:415` TestSendQueuedUntilTheLeadIsRecorded. Both: a line with no inbox is queued untried, posted once when recorded. In-package renames the lead first and asserts nothing is typed into the old pane; ext restarts twice, no rename.
- `messages_test.go:653` TestLeadLinesOldTargetNoInbox ↔ `messages_ext_test.go:450` TestSendWaitingADayNeedsYou. Both: a lead line waiting past the wait gets one needs-you. In-package has two leads; ext has one and checks the exact reason and that posting ends it.
- `messages_test.go:1265` TestFinishedLineAgeFromTyping ↔ `messages_ext_test.go:492` TestBriefFinishedLineToTheLead. Both end in "finished (briefed Nm ago)"; in-package counts age from typing after a queued hour and a reload (3m), ext counts 5m from a posted brief and also refuses briefing the lead.
- `messages_test.go:1074` TestBriefRetiresARoutedLine ↔ `messages_ext_test.go:668` TestBriefSupersedesAQueuedBrief. Supersede, but the in-package cases hit a line already routed or claimed.
- `messages_test.go:1144` TestBriefSupersedesSeveralLines ↔ `messages_ext_test.go:700` TestBriefLeavesLeadLinesQueued. Both end with only the newest brief waiting; ext adds that two lead lines stay.
- `messages_test.go:248` TestFreshBriefSupersedesQueued ↔ `messages_ext_test.go:668`. A `--fresh` brief stops and restarts the agent; the ext brief is a plain one.
- `messages_test.go:1265` ↔ `messages_ext_test.go:591` TestBriefToALabelledPaneWithoutAName. A finished line with an age; the ext pane is a codex pane found by label (2m).
- No overlap: ext BriefEndsWhenItsAgentIsGone, NotWorked…, UnderAStop…, ReplayedWorkedBrief…, WakeByKind, WakePost*, BriefByKind, LineToAnUnnamedPane, LinesRefusedWithoutAMergeLoop.

### nightly
- `nightly_test.go:48` TestNightlyNotificationsKeepRunCursor (row "completed RED + pending notification") ↔ `nightly_ext_test.go:591` TestNightlyReplay (row "a day with a result runs none"). Both: a day with a result runs no more after a restart. Inputs differ: a RED result with an old notify intent pending vs a GREEN result alone.
- `nightly_test.go:48` (row "three failures + completed notification") ↔ `nightly_ext_test.go:557` TestNightlyFailuresThenNeedsYou. Three counted failures → no run that day; ext drives the 10-minute retries live and the next-day run, in-package replays a log with notify records.
- No overlap: the other ext tests (on/off, RED needs-you, resume, switch, retry-after-restart).

### panes
- `panes_test.go:57` TestRereadErrorRecordsNothingAndRetries (and :100, :143, :285) ↔ `panes_ext_test.go:11` TestReconnectReadsANewSnapshot. In-package: the start's re-read when no lane pane is known; ext: the stream reconnect reading a new snapshot (pane gone, blocked agent → facts).
- `panes_test.go:343` TestLaneWithNoPaneGetsAPanePlaced ↔ `panes_ext_test.go:48` TestRefreshSubscribesAgainQuietly. A snapshot registers panes in both; one places a pane for a lane, the other is a workspace-created refresh with no stream-ended line.

### pause
- `pause_test.go:44` ↔ `pause_ext_test.go:15` TestPauseGatesDispatch. The ext refusal is a plain dispatch and `--after`, not Types; it also covers clear, status, a worker's pause, waiting tasks. Only the refusal string is shared.
- No overlap: ext TestPauseReplayAndExpiry.

### prompt
- `prompt_test.go:15` TestPromptFreshDelivered ↔ `prompt_ext_test.go:180` (row "fresh start with wake"). In-package types the work job and checks the box cleared first; in ext the first prompt rides the start ("sent (start prompt)"), nothing typed.
- `prompt_test.go:113` TestPromptReplayResends ↔ `:180` (row "replay after the start, seen started"). Replay of a pending typed prompt vs replay of a pending agent.start ("started (seen in the snapshot)").
- `prompt_test.go:188` TestStartReplay ↔ the same ext row. The table rows are the start's decision from session ids; the ext row is one end-to-end replay.
- `prompt_test.go:246` TestStartResultLost ↔ `prompt_ext_test.go:23` TestTypedDispatchStartsOnOneLine. Start result write fails vs the start's first prompt being one line.
- No overlap: ext TestDispatchStartRefusedWhileALiveAgentHoldsTheName, TestDispatchIntoALaneShellLabelledNotes; ext rows "fetch while the start runs", "no bridge".

### resume
- `resume_test.go:148` TestResumeStalledWorker ↔ `resume_ext_test.go:88` TestAnswerAfterTheStopHooksTurn. Different needs-you (stalled vs asks) and different checks.
- `resume_test.go:126` TestAnswerToWorkingWorkerAtNextIdle ↔ `resume_ext_test.go:254`. An answer to a working worker waits for idle vs a resume line to a working worker with no question is refused.
- row "a failed step" (retry) ↔ `resume_ext_test.go:235` "a refused dispatch step → resume" ("RESUMED T-1: retry"). The row's needs-you is a `submit` failure, the ext's is a refused dispatch step.
- No overlap: ext "refused dispatch step → drop" (:242).

### rolestop
- `rolestop_test.go:99` TestRoleStopIntegratorRule ↔ `rolestop_ext_test.go:14` TestRoleStopIntegratorAcrossRestart. In-package decides *when* a stop is issued (a table of verdict/job/pane states); ext decides what a restart does after the stop already happened (no job.lost, no needs-you, no second stop).

### rules
- No in-package test for `rules_ext_test.go:41` TestNeedsYouLineQueuedWithoutAnInbox (a needs-you line to a lead with no inbox record is queued, posted once when the record arrives).

### serve
- `serve_test.go:267` TestSocketEffectEndsWithServe ↔ `serve_ext_test.go:139` TestShutdownLoopNotRecorded. A socket request's effect ctx ends before Serve returns and the caller gets "context canceled" vs a cancelled loop pass recording no needs-you/resumed.
- `serve_test.go:577` TestSelfRestartEndsServeAndItsGateGroup ↔ `serve_ext_test.go:98` TestShutdownDeliveryNotRecorded. Exit 0 and the gate process group killed vs a cut line delivery recording no failed fact and replaying once.
- `serve_test.go:332` TestLoadedAfterReplay ↔ `serve_ext_test.go:16` TestServeWaitsForHerdr, `:67` TestServeStartsWithHerdrDown. `Loaded` closes after replay vs herdr down/back lines.
- `serve_test.go:29` TestLoadProjectRetriesAFailedLoad has no ext counterpart.

### status
- No in-package test of `status_ext_test.go:18` TestStatusStartedIsTheLastStartTry (started time of the last agent.start try).

### steps
- `steps_test.go:88` TestGateCutReplayRunsOnce ↔ `steps_ext_test.go:90` TestLoopRestartMidTestRunsItAgain. Same shape (cut step, replay reruns under the same key, no failure counted); the step differs (gate vs lane test) and so does its effect.
- `steps_test.go:127` TestLaneTestRunsBesideTheSlot ↔ `steps_ext_test.go:29` TestLoopDirtyLaneTestRetried. Both lane test; in-package: runs outside the scratch token and drop is refused while it runs; ext: a dirty lane refusal retried then needs-you.
- `steps_test.go:183` TestGateAndTestResultsFold ↔ `steps_ext_test.go:229` TestMergeIntentsCarryNoData, `:184` TestPublishPendingWaitsForLanded. Fold of gate/test results vs publish intents and pending-publish handling.

### stopped
- `stopped_test.go:23` TestStoppedCurrentTaskNotAnOldWatch ↔ `stopped_ext_test.go:166` TestStoppedWorkerNeedsYou. Both record a worker stop and end it by a turn; in-package decides which task it is recorded against, ext the facts/lines/needs-you shape.
- `stopped_test.go:130` TestTurnDecide (and :271–409 TestTurnCodex*, TestTurnClaude*) ↔ `stopped_ext_test.go:359` TestStoppedCodexOncePerTurn, `:385` TestStoppedCodexResumeIntoDialog, `:421` TestStoppedCodexNextTurnPastTheTail. Same turn-evidence area; ext's inputs are rollout tail sizes, dialog read counts and replays, not the table's rows. I did not find a row asserting the same.
- No in-package counterpart: ext TestOpenCodeStoppedReset (:18), TestStoppedRoleAgentLine (:91), TestStoppedWorkerOneRecordReplay (:218), TestStoppedWorkerRestartSameWorking (:259).

### verdict
- `verdict_test.go:129` TestDropNeedsYouTask ↔ `verdict_ext_test.go:23` TestDropOfASpecOnlyTaskThenDispatchUntilANewRev. Both refuse a second drop ("dropped already") on different inputs (a dispatched needs-you task vs a spec-only task).
- `verdict_test.go:613` TestReviewAfterASpecOnlyDrop ↔ `verdict_ext_test.go:87` TestSpecOnlyDropEndsAnAfterWait. A spec-only drop ends a waiter: a review request `--after T-1` ("review request ended: T-1 dropped") vs a dispatch `--after T-1` (needs-you{after}).
- `verdict_test.go:532` TestDroppedEndsTheTask, `:1075` TestDropWriteFailure ↔ `verdict_ext_test.go:246` TestDropSettlesOpenIntents, `:155` TestReplayEndedTaskIntents. Drop ends the task vs drop/ended task settles its open intents ("not run: T-1 dropped"); no in-package assertion of the settle result.
- `verdict_test.go:46–102` TestAccept* ↔ `verdict_ext_test.go:305` TestVerdictRefusals, `:346` TestVerdictRepeatedCallIDGetsStoredReply. Accept at a new spec rev vs verdict-call refusals and the call-id reply.
- No in-package counterpart: ext TestVerdictServeNotRunning (:388), TestStartIntegrator (:408).

### waiting
- `waiting_test.go:51` TestWaitStaleSelectionDispatchesNothing ↔ `waiting_ext_test.go:18` TestDifficultyRedispatchOfAWaitingTaskReplacesIt. Both are about a replacement request of a waiting task and its later dispatch; the stale-step no-op is in-package only.
- `waiting_test.go:88` TestWaitStaleDependencyFailedRecordsNothing ↔ `waiting_ext_test.go:285` TestWaitingDependencyDroppedLater. Opposite situations: a stale step records no needs-you{after} vs the current request records it and resumes retry.
- `waiting_test.go:142` TestWaitSelectionNotReplacedDispatches ↔ `waiting_ext_test.go:131` TestWaitingDispatchesOnceAfterMerge. A waiting task dispatches on a free lane with no after vs after T-2 merges.
- `waiting_test.go:111` TestWaitStaleRefusalRecordsNothing has no ext counterpart.

### watchdog
- `watchdog_ext_test.go:54` TestWorkingAgainResumes. Despite its name it asserts no resume: two lanes, T-2 stalls, T-1 stalls 1 min after its first quiet baseline. The closest in-package rows (T-335 :242–243) are single-lane with scripted Busy.
- `watchdog_ext_test.go:72` TestNotStalledWhileAChildRuns, `:129` TestNotStalledWhileTheTranscriptChanges ↔ rows :295–318 (#13) and :371–376 (#9). Same decisions (a child process or transcript write postpones the stall; the first quiet sample is a baseline), but ext uses real process trees and file mtimes, in-package scripts Busy and the transcript time.
- No in-package row for `watchdog_ext_test.go:102` TestWatchdogSamplesProcessInfoPerTick and `:226` TestClosedTaskWatchdogFactsEnd. `watchdog_test.go:463/:492/:539` (question text) has no ext pair.

## Unknowns

- I ran no test, so "stays" says nothing about coverage or duration. A match asserts the same decision; whether the ext test is slower, flaky or rig-bound is the test plan's data (`test-plan-advice.md`), not shown here.
- The "no overlap" claims for the 19 pairs with no match rest on the readers' reading of the test bodies. A decision can also hide in a helper or a subtest; the readers were told to read bodies, not names.
