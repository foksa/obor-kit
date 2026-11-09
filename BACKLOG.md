# Backlog

The lead appends; nobody else writes here. Triaged 2026-10-28 after v0.12.0 (DD-v0.12 C4): items about deleted
mechanisms (heartbeat, queue, `obor tell`, inbox, the mod, `obor watch`/`merge`/`ack`/`owner`, handoff, lanes state
files, bash, keep_warm, multi-lead) were closed by their deletion and removed; history is in git
(`git show 449b79e:BACKLOG.md`).

## Handoff (lead; rewrite at each handoff)
Read `docs/observations.md` first (patterns from past sessions: where we went in circles, the load tax, traps).
- 2026-11-09 ~09:40 (lead; previous Handoff: `git show f541e819:BACKLOG.md`). Owner stopped for the day. Every lane
  free, nothing queued, no review open, no watch.
  **Released and shipped v0.32.0** (owner ran `make ship`; tag at f27d7edc; axis-alies confirmed no upgrade steps and
  committed `kinds.opencode.command`, b09493eb). In it: promotion deleted (T-1076, T-1078, T-1079; owner 11-09), the
  adopter test flow = merge check + nightly, promotion unset (advisor option A; T-1075 init writes `nightly_at`),
  `commands.baseline/gate/e2e` deleted with a validate WARN until v0.34 (T-1083; sizing T-1080), an unknown config key
  is a WARN for every loader and only `obor config validate` is strict (T-1081, T-1084; DD-v0.32 #1), an open promote
  needs-you ends on upgrade (T-1082), serve names every event's kind (T-1074), the gate fails a test package with no
  budget entry and `-seed <pkg>` seeds one (T-1073, T-1077). Reviews RV-156..RV-160 closed; DD-v0.32 + verify approved.
  **Next steps:** (1) Check the 11-10 02:00 nightly GREEN (first on v0.32; the budget line should be OK now).
  (2) The owner picks the next work. Verified not done: `obor doctor` (check first that stale panes still happen).
  The `job_next` saving left is Claude-only (`alwaysLoad`), parked under the agent-neutral rule. Deep-dive `git init` concern: decided 11-09, overlay route
  named in its role text.
## Owner decisions pending
- (none; branching decided 11-07, docs/decisions.md)

## Later candidates
- **Un-defer `job_next`** (chatter census proposal 2; advisor 11-07 docs/research/job-in-first-prompt-advice.md: no job-in-prompt; T-959/T-960/T-941 did the kind-neutral parts). Left: `alwaysLoad: true` on obor's MCP entry, Claude-only, so parked under the owner's agent-neutral rule (11-07); revisit if another kind gets the same deferral.
- **Log snapshot at upgrade** (parked 11-05, docs/research/log-snapshot.md): build when compat branches pass ~300
  prod lines or a record-shape change needs it.
- (RV-63 #4, accepted by rule) `scripts/publish` checks the local tag only, not that it is the remote one: a moved
  pushed tag could upload bytes fresh installs reject. Rule instead of code: a pushed tag never moves (RELEASING.md).
- **herdr-core (`hc`)** (`docs/research/herdr-core.md`, 10-30): a separate headless terminal core (own `herdr-core`/`hc`
  binary, own socket/state; no clash with `herdr`). Not now (0.1.x preview). Spike behind `app.PaneControl` when hc has a
  stable generation, herdr discussion #5684 is answered, and Claude/Codex/OpenCode all report OSC 7501. Until then: no
  new herdr-only shapes (tabs, layout, plugins) in `internal/app`; `HERDR_PANE_ID` read in one place.
- (RV-57-verify #2, #3, accepted) the load-run guard misses separately `bash -c`-wrapped sibling background tests and
  repeated `-P` options ending in 1 is still denied: constructed forms.
- (DD-v0.14 U3/U6/S4) The difficulty map's type names and the Go-only load-run rules are obor-kit's own policy built
  into the kit; simpler: `obor init` writes them into the project's config. Not before a second adopter.
- **A herdr restart takes every serve down** (10-30: obor-kit and axis-alies serves, axis' reviewer pane lost):
  phase 7 launchd. Who restarted herdr is unknown (S5a).
- `obor doctor`: hooks loaded + kit version per pane (stale panes hid guard bugs twice).
- Per-project kit pin (rollout lesson v0.6.0): moving `obor-kit-stable` upgrades every adopter's CLI at once while
  hooks follow per pane restart; a breaking release needs an opt-in pin or code that handles old state.

## Open
- (RV-162, accepted) (RV-162, accepted, constructed) pruning an integrator's only old verdict can change its verdicts > StartRow.Verdicts stop predicate on replay; needs an idle integrator running 30 days despite the stop rule
- (RV-161, accepted) (RV-161, accepted) an unparseable per-lane name (run-changed-<not a lane>) is removed though the spec said it stays; no writer makes such a name (likelihood: none today)
- (RV-158, accepted) (RV-158 redundancy) review.freeze sets rv.reviewed after the close (internal/app/reviewfold.go:46) though its only reader is guarded by cur.closed == 0 (reviewclose.go:39-42): drop the closed-review assignment on the next touch of reviewfold.go
- (RV-155, accepted) RV-155 redundancy: test helper proj.queuedAt (internal/app/messages_test.go ~700) duplicates p.out.Line(seq); swap its three call sites at the next touch of that file (no behaviour)
- (RV-154, accepted) RV-154 verify V1 (design.md slot sentence implied compile/nightly avoid the merge marker): fixed by the lead after the second verify (design.md, one sentence), not re-verified
- (RV-151, accepted) herdr adapter decodes an omitted launch_pending as false (launched): true for herdr 0.9.5 (live H-L12/H-L13); a herdr that stops sending the field would fall back to listing evidence — recheck the probe on each herdr upgrade (likelihood: low, at upgrade only; fix: a version check, ~10 lines)
- (RV-149, accepted) (RV-149 verify, unlikely: a Codex lead) an unattended restore (serve reconnect or the herdr plugin) of a Codex lead with a quarantined helper only logs the warning to the restore log and starts; fix when a project runs a Codex lead: restore refuses it and raises an owner-visible line (~30-70 lines)
- (RV-148, accepted) (RV-148 verify 2, unlikely: a config edit during an in-progress worker start on a keep lane, then the same alias next) the start result records the config rev re-read after the launch (dispatch.go:1167), not the one the launcher used; fix: carry the launch's config rev through the launcher result (~20-40 lines) when keep lanes are in real use
- (RV-147, accepted) (RV-147 verify design) plugin Start/StartEnv remain only to satisfy agents.Kind and could each run the plugin; drop them with plugin.Whole once agents.Kind has one combined start for every kind (with step 9b)
- (RV-146, accepted) (RV-146 design) a Claude lane that opts into project MCP servers starts non-strict, so it also loads the user's and plugins' MCP servers (CC-L9 listed many): document it in Customizing agents; revisit if Claude gains a narrower flag
- (RV-145 design) the workflow contract table (T-1029) covers scratch, lane and target, not the review methods (tested on their own today): extend it with their portable outcomes before a second workflow implementation. Also `checks.ScratchFF` holds the config twice (`Scratch.Options.Cfg`, `Lane.Cfg`): one when next touched.
- (RV-144, accepted) (RV-144 verify note) the fold keeps the review.closing pointer after the close although recovery no longer reads it (a retained copy of the frozen snapshot): clear it on close when next touching reviewfold.go
- (RV-143 verify design) `specSetRev` parses the rev from the spec-set reply's text (T-1026), coupling replay to reply wording: keep the format, or a structured rev when the record is next revised.
- (RV-141, accepted) (RV-141 verify design) the prepare-time head is no longer read: checks.Lane.Prepare's git merge-base and prepareOut.Head remain, and docs/state-model.md:293 and port/effects.go:380 still describe the old head; deleted in T-1017
- (RV-141 #2, accepted) A rejected task whose branch the owner merges by hand keeps its lane `working` until `obor drop` (T-1012 removed the ancestry read that freed it); the drop is the explicit step. Also accepted: view/dispatch/restore still compare lane state strings (one decider in port, several readers of its output).
- (lead 11-08, v0.30.0) **Release from a worktree loses the binaries:** `make release` builds the four `dist/obor-<v>-*` files in the checkout it runs in (gitignored), but `make ship` reads them from the main checkout's `dist/` and refuses off `main`; UPGRADE-v0.30 says to cut a release from a separate worktree at main (staging setups). The lead rebuilt them reproducibly (tag^ + the uncommitted marketplace edit: the binary embeds vcs.revision/modified). Fix: release copies `dist/` back to the main checkout, or ship reads the tag's assets from the release worktree; and ship's branch check allows a staging-based checkout.
- (RV-140, accepted) (RV-140 design) internal/run TestRunRcArgsTimeout and TestExecuteLeavesNoGroup mix a real wait with quick assertions; split the quick cases out of the Slow tests when next touching them (plan step 2 covers run's tests)
- (RV-139, accepted) (RV-139 verify 2, P2) OBOR_FULL=1 forced full returns before scanning files, so with_full release/web checks are skipped (classify.go:~43); fix in T-1008
- (RV-139, accepted) (RV-139 verify 2, P1) the Makefile's -seed recipe redirects into tests/budget.json, truncating it before -seed reads its caps; fix in T-1008 (seed writes the file itself, atomically)
- (RV-139, accepted) (RV-139 #3) restricted-PATH tests hide child git calls from the budget counter (already accepted)
- (lead 11-08) `TestScratchCutTable` costs 31 s of work at 2 cores even after T-1000 (TestNightly 6.7 s, lane tests 2-4 s): they stay in -short with caps (T-1004 rev 5); a speed-up task for them lets the caps drop.
- (RV-139, accepted) #3: tests that replace PATH (cmd/obor TestInitEndToEnd) hide their child's git calls from the budget's counting wrapper; one fixed test, its count does not grow by itself. Design notes: `release_tests` duplicates `check_release` only to drop `heavy` (merge if a second such duplicate appears); T-1001 lost TestUpgradeBrokenBuild's negative case (`make upgrade-check` runs the real upgrade).
- (RV-138, accepted) RV-138 #2: the queue card counts --after dispatches waiting for their dependency as queued for a lane; serve should ship the waiting-for-lane count (web/src/lib/pipeline.ts:~33)
- (RV-138, accepted) #2 the queue card's 'queued for a lane' counts --after dispatches still waiting for their dependency (web/src/lib/pipeline.ts:~33); serve should ship the waiting-for-lane count; next touch of the queue card.
- (lead 11-07) opencode (deepseek) workers ignored Escape twice (T-958, T-992), both needed `obor drop --force`.
- (RV-131, accepted) RV-131 #1: a crash between SetSpec's commit and the spec.set receipt, then another writer's spec within the 15 s retry, lets the retry write the old text as a new rev (internal/app/specs.go:88-100; docs/state-model.md:396-400). Very low likelihood; fix = spec + receipt in one transaction, 60-120 prod lines
- (RV-129, accepted) verify design note: app.go:83's J1 rule comment lists the dispatch-class verbs as exceptions but they take two different locks (verdicts: dispatch, drop, drive, pause, job_next, resume; submitMu: task_done, land, spec set, move); say which lock each takes on the next touch of app.go
- (lead 11-06) Empty `index.lock` files appeared in all four lane worktrees (`.git/worktrees/L-0x/index.lock`) at 12:31, with no git running; every dispatch then failed `lane.prepare … checkout -B … exit status 128`. Serve had not restarted (up since 12:15); the deep dive (12:22) and the RV-127 verify (Codex sandbox) were the only other agents. Removed by hand. If it recurs: find who runs git in the lanes and dies; `lane.prepare` could name the lock in its FAIL.
- (RV-126, accepted) #2 rest: a process crash inside the close's commit window leaves an --accept line staged (a retry commits it twice) or the review file orphaned (post-commit crash; gitignored here); a removal error after the commit keeps the fold open until serve restarts. Crash-only; a durable close transaction is 60-120 prod lines
- (RV-126, accepted) #3 a lost reply's same-id retry after a recovered close says 'no open review' although the close committed (one commit, one fact); needs a crash between commit and fact plus a retry; fix 30-70 lines
- (lead 11-06) A model pause (`obor pause --model`) shows in neither `obor status` nor `obor setup show`: the owner had to ask why medium tasks went to sonnet, and a new lead session cannot see it. Small, adds lines: one status line per paused model with its end and why.
- (RV-125, accepted) #1 upgrade with a released fix round open loses its links and verify: covered by docs/UPGRADE-v0.29.md (close the round first or add Fixes: lines); a legacy fold would be 70-140 prod lines of compat for one upgrade moment
- (RV-125, accepted) #2 a closed review's fixes stay in obor status until the next review starts (stale line, every cycle): goes with T-926 (close through serve: the review.closed fact clears the fix snapshot)
- (RV-125, accepted) design: a fix spec that also carries Review: on merge queues a fresh review its verify already covers; lead rule: a Fixes: spec carries no Review: on merge
- (RV-124, accepted) verify #1 reverse direction: a client built between T-896 and T-921 (open/locked fields) misreads a new serve's ended/free; no release ever shipped open/locked (v0.28.0 clients read state strings, which serve still ships) and the running obor web predates T-896; dev-only window, no legacy fields (they would be compat code for an unreleased shape)
- (RV-124, accepted) design concern: the lane section classifies waiting from the displayed state string (`cmd/obor/status.go`, `t.TaskState != "waiting"`); a waiting task whose dependency dropped shows as needs-you instead (waiting_test.go:232), pre-existing; serve ships a waiting predicate on the next touch of that line (the queue's half is T-1043)
- (lead 11-05) An empty untracked `.obor/obor.db` (0 bytes, 21:54) appeared in the main checkout and blocked `obor review close` (dirty); moved to the lead's scratchpad. Not `obor serve dump`. Something opened the store at `.obor/` instead of `.obor/state/` (an empty derived dir?): find the caller if it recurs (AGENTS.md: empty derived value fails closed).
- (RV-123, accepted) RV-123 #3: scripts/ship parses obor projects' rows with awk; wrong for a spaced path or an empty registry; ship runs only in obor-kit; fix 5-15 shell lines if a second shipper appears
- (RV-120, accepted) RV-120 V1 (P2, low): an unknown lane keeps its quiet/blocked/lost timestamps, so the unobserved interval counts on recovery (false stall/blocked needs-you) — folded into T-879 (unknown or stale observation resets every elapsed-time baseline, case table).
- (RV-119, accepted) RV-119 #1 rest (P2, low likelihood): a resumed worker whose job wake herdr refuses (blocked agent) stays idle; launch's warning goes to serve's io.Discard and the start rule counts it a success. Covered by the stalled-worker needs-you; fix only if seen (wake outcome back to the rule, 30-70 lines).
- (RV-118, accepted) (RV-118, accepted) idle's key/time ignore the record that clears a non-rule needs-you (e.g. the disk watcher's below-5-GiB needs-you, internal/app/disk.go:38-67; its resumed fact is not an idle end candidate, idle.go:138-145): after such a needs-you clears, the old key, age and acknowledgement stay and no new idle line comes. Rare (disk < 5 GiB); fix 25-50 lines (count the clearing record when it empties the pipeline)
- (RV-113, accepted) (RV-113, accepted) 4b: an open review started by an obor older than the review.started fact closes at an unfinished manual verify's head (refs/obor/reviewed moves at brief time, reviewverify.go:519; reviewcycle.go:233 falls back to it); needs such an old open review + an unlinked manual verify that fails or is superseded; none exists; fix 25-55 lines (move the fallback ref only on a finished verify)
- (11-05) Load flakes in internal/app's role-brief tests, only on T-852's gates so far (load ~30): TestReviewerBriefEndsIdleAndANewEpochStarts,
  TestBriefEndsWhenItsAgentIsGone/not_identified (62 s wait); both pass alone and in the package at load 19. If one
  recurs on another task: a flake task (what the test waits on under load).
  **Recurred 11-05 10:1x:** `TestJobRecovery/unblocked_refused` timed out (61 s, "waiting for the crash") in
  `make release V=0.27.0`, in the full check and its rerun; passes alone and in the package at load 34. Third
  different internal/app test in one day with the same ~60 s wait under load: a flake task after v0.27 (what the
  rig waits on; a shared cause in the fake herdr/loop timing is likely).
  Fourth the same day: `TestHoldAndResume` (release retry, FLAKY, passed on rerun).
- (11-05) `obor drop T-851 --discard` failed halfway: `FAIL drop: git branch -D task/T-851: signal: terminated` left
  the lane freed but the task `working`; the same drop again finished it. A drop that fails after freeing the lane
  should say "retry the same obor drop" (or record the drop before the branch delete). Likely under load; fix small.
- (RV-112, accepted) (RV-112, constructed) guard skips combined short (git checkout -qd) and abbreviated long (--det) detach spellings in the main checkout; no workflow uses them; fix in internal/guard/shell.go:830 if one shows up
- (RV-110, accepted) (RV-110, accepted) #1 T-840's schema-8 refusal test reads old rows only through ReadLanes; ReadLaneAt/ReadLanesAt (guard, Stop hook, whoami) are not exercised on schema 8; needs a pre-v0.22 serve during an upgrade (no project runs one); fix 10-15 test lines in internal/store/store_test.go if it ever matters
- (RV-108, accepted) (RV-108, accepted) #1 the stop bound (T-829) does not bound the herdr calls inside Launcher.Stop: a forced stop with a slow herdr can outlast the CLI's 75 s (drop) or be cut by drive/move's 60 s request context; second review round on T-825/T-829, low likelihood; fix: one derived deadline through every herdr call of Stop (20-40 prod + 40-70 test lines)
- (RV-106, accepted) review close leaves a still-queued verify brief alive (reviewverify.go:77, reviewcycle.go:132): it can start the stopped reviewer on a deleted review and hold merges up to the cap; low (close while a verify waits on a busy/unavailable reviewer); fix 40-80 lines (close retires it). Workaround: wait for the verify, or obor brief <reviewer> --fresh before closing.
- (RV-106, accepted) a verify brief whose delivery broke after some bytes (sent: <error>) counts its tasks as covered (review.go:641-653); low; fix 20-50 lines (uncertain delivery not counted).
- (RV-104, accepted) #2 remainder: obor idle's ack call reads the epoch under a.mu and records after releasing it, so an ack racing a brief/dispatch replies ACKED for an epoch that just ended (folding ignores it); low (two actions within ms), fix 20-50 lines.
- (RV-104, accepted) #3 remainder: Ctrl-C/SIGTERM during a live conformance run: cancellation is checked only before the kinds start, so setup or H-L6 can race the cleanup; occasional, recovery is a rerun (setup stops a pre-existing obor-conf-<kind> session), fix 40-100 lines.
- (RV-104, accepted) #4 remainder: obor status does not read .obor/state/conformance.json (passed/partial/failed); the run output and the file carry it; a status line is a 40-100 line feature, not asked for.
- (RV-103, accepted) conformance live runner uses fixed session names (obor-conf-<kind>) and stops a pre-existing one: two runs at once collide; unlikely (lead/owner run it by hand), fix: a per-run suffix if it ever bites.
- (lead 11-04) **Flake**: `TestHoldAndResume/resume_reprompts` (internal/app/jobs_test.go:394) failed under full-check load in T-804's gate ("resume T-1: T-1 has review.2 running; retry when it settles", 5.1 s), passes alone; T-804 touches no internal/app. Fix if it recurs: wait for the review step to settle instead of a fixed deadline.
- (RV-102, accepted) #2 remainder: setup rename interrupted between config.json and local.json, then rerun, leaves a legacy derived allow_push entry (e.g. ob-lead) unconverted; unlikely (an interrupted rename with the entry in local.json only), fix ~20-40 lines (normalize both files before any prefix write); fix by hand: write the role name (lead).
- (RV-102, accepted) T-793: a lead's git -C <lane> push passes the shell guard but the pre-push hook (run in the lane) classifies the caller as the lane's worker and refuses; unlikely (leads push main from the main checkout), fails closed, fix 20-50 lines (one identity contract for guard and hook).
- (RV-101, accepted) (RV-101 #2 rest, low) a no-pane reread can overwrite a newer stream label of the same pane (concurrent relabel); the launcher refuses the wrong pane, so at worst a needs-you. Fix if seen: observation generation per pane (internal/app/dispatch.go waitPane)
- (RV-101, accepted) (RV-101 #3, low) `no-plugin` can show right after a manual `obor restart` of an opencode worker whose marker predates the dispatch: the 2-min grace counts only serve's `agent.start` events. It clears once the new session's first event writes the marker. Fix if it confuses: record manual starts too (internal/view/view.go lastStarts).
- (RV-100, accepted) (RV-100 #2, low) obor status shows the next nightly as tomorrow while today's waits on a gate/publish or was just switched on
- (RV-99, accepted) (RV-99 #2 rest, low) setup migrate straight from a pre-v0.23 config: an agents entry mapped to a role by its name (fable-advisor) gets the bare model, dropping its old lane type's --effort. No live project needs it (axis-alies is v0.23-shaped with fable:high). Fix if seen: use ref(typ) for name-inferred roles (internal/config/migrate.go:454).
- (RV-98, accepted) (RV-98 #3 rest, low) obor restart with serve down AND an unreadable state db reads as 'no task' and restarts the lane agent idle. Fix if seen: propagate ReadLaneAt's error in resolveLaneJob (cmd/obor/restart.go).
- (RV-98, accepted) (RV-98 #2 rest, low; RV-93 class) a worker with no identity proof (hand-started, no session, pane relabelled) reads as gone, so drop/move/takeover free its lane without stopping it. Fix if seen: whereIs 'no proof' with a live pane in the lane → refuse, not gone (internal/app/steps.go stopAgent).
- (RV-95, accepted) (RV-95 #2, low) project list: a renamed project (name ≠ dir) whose config does not parse is dropped if another project registers meanwhile; NameAt falls back to the dir name. Fix if it happens: keep the stored name when the name cannot be read.
- (RV-93, accepted) (RV-93 verify #1, accepted) a worker started by hand in a replacement pane with no recorded session whose label is then cleared reads as gone (whereIs 'unavailable exited/no proof'): obor drive T-xx can start a driver beside it, drop omits its warning; post-merge stopWorker shares it (predates T-758). Needs three manual steps; fix would add a lane-wide live-agent check
- (RV-92, accepted) (RV-92 #5, accepted) a hand edit of .obor/config.json saved while a Setup edit commits can be overwritten: needs both at once
- (RV-92, accepted) (RV-92 #6 remainder, accepted) a Setup edit whose POST succeeds but whose refresh fails re-enables the controls on the stale list; a second edit can undo the first. Local server, rare; separate tabs keep their own snapshots (last writer wins)
- (RV-90, accepted) obor drop fails while the lane's agent still runs (git checkout --detach exit 128, the FAIL text does not say why); stop the agent first. Seen 2026-11-02 dropping smoke T-743
- (RV-89, accepted) #1 rest: a successful empty pane re-read followed by serve shutdown before the step applies it records needs-you{no pane in lane}: unlikely (shutdown in that interval); fix 3-8 lines (ctx.Err() check before the fact)
- (RV-89, accepted) #2 rest: an older re-read can overwrite the lane pane's cwd from a newer stream event during the read: unlikely; fix 20-40 lines (per-pane observation generation)
- (RV-87, accepted) #2 replayed worked brief with a closed pane ends after the 1-min grace, not on the first pass: unlikely (serve restart + pane closed), costs a minute; fix 30-70 lines
- (RV-87, accepted) verify: docs/design.md ~471 says a hold-role brief holds publishes while open without the 30-min cap (stated at ~241-245): one line, fold into the next task that edits docs/design.md
- (RV-86, accepted) #4 bg label lost for later commands in a background shell (make build && go test): display only, likely; third round on the bg label (RV-85 #3, T-728); fix ~15-30 lines if it bites
- (2026-11-02, T-714) A worker blocked on "the gate's RED is not my diff, re-gate" can only be answered
  (`obor answer`), and an answer saying "the lead re-gates" leaves both waiting: serve needs task_done, the worker
  waits for the lead; `obor resume T-x regate` refuses (no merge-loop needs-you). Fix idea: such a Blocked report's
  needs-you offers `obor resume T-x regate` (re-run the gate on the submitted diff, no worker turn).
- (RV-84 2nd verify #3, accepted: rare) `obor stop`: if unregister or the marker write fails after serve exited,
  the prior pause is lost (persist it in a recovery record before shutting serve down, ~20-50 lines).
- (RV-84 2nd verify #4, accepted: rare) macOS: an installed but unloaded plist plus a pane serve: stop exits the
  serve but leaves the plist (run the ownership-checked Uninstall even when unloaded, ~3-10 lines). Next time
  cmd/obor/sleep.go is touched.
- (RV-84 verify, accepted) `obor stop`: a pause cleared by another client during the stop transition is not
  guarded; a serve-owned stopping state would be (design concern), not worth it for a convenience command now.
- (T-684 concern) `patience()` lets one hung test eat the whole package deadline, so parallel siblings fail on the
  1 s floor (one failure read as eight at v0.18.1). A per-wait cap (~60 s) keeps failures local. Small; next test task.
- (RV-77 design concern, logged) `scripts/ship` counts a release done by draft state and asset names only, not its
  title/notes; a partly published release stays incomplete on rerun. Low once T-683 refuses before create.
- (v0.18.0 release, 11-02) `TestStopForceTerminates` failed once under the full check (60 ms waits for a fake agent;
  passes alone): same family as the flaky `TestStopEveryKind`. Widen the test's waits if it fails again.
- (RV-68 #3, accepted: low) A needs-you repeat claimed before a task closes can be appended after the close (claim
  and append are not serialized with the close); fixing needs a lock-order change.
- (RV-67-verify-2 #2, owner 11-01: ship + document) init placement infers ownership from pane cwds: (a) a workspace
  whose only pane is a shell cd'd into the project counts as the project's; (b) on a rerun after an earlier
  misplacement S-01 can follow O-01's clean tab inside a mixed workspace. Documented (README, design Setup); a stored
  project→workspace identity only if it bites.
- (RV-67-verify #1, accepted) `obor init` appends its lines to an existing `.gitignore` in a repo with commits (by
  design, printed, not committed); byte-identical pre-existing template files in an unborn repo count as init's
  (constructed).
- (owner 10-31) **v0.17.1 as soon as T-627, T-628, T-629 merge** (restore of never-prompted sessions; init places
  panes in the project's workspace + waits for the shell; init on an empty dir / Claude-only). Then: one Terra review
  over the three (RV-67), fixes if likely, tag v0.17.1 (RELEASING.md), owner pushes/publishes/reruns install.sh.
- (10-31, axis-alies v0.17) `obor restart aa-rev --fresh` (Codex, no prompt) warns "holds no codex transcript open
  within 10s (its session is not recorded)". Guess, unverified: Codex writes its rollout only at the first prompt.
  Check at axis' next review brief; if so, the warning should not fire for a fresh role start without a prompt.
- (v0.17 release) Until `~/.obor/kit` moves to v0.17, the global `obor.events` herdr plugin runs v0.16's `plugin
  restore` (expects `.sid`): a herdr restart may not resume obor-kit panes. Machine leftovers to retire with
  herd-kit-stable: `~/.obor/herdr-plugin` (symlink into it) and herdr's `herd.events` plugin.
- (RV-66 #1, accepted: by design) A pane labelled with an agent the config no longer names is still identified by
  its label: T-605 removed start-row and session proof only; labels name hand-started panes.
- (RV-65-verify #2, accepted: rare) The "session not recorded" warnings say a restart resumes the last recorded
  session; with none recorded it resumes herdr's, else asks for `--fresh` (state-model row 51 is the contract; T-599's
  "starts fresh" promise is dropped).
- (T-606) Other "oldest/first" choices that range over `p.loop` (a map) go by Go's map order: job_next's did
  (T-602 → T-606, a flaky gate). Sweep: each such choice orders by intent seq or task; a candidate for the `proj`
  split's folded-state step.
- (RV-64, accepted: unlikely, fixes add code) #1 a hand relabel of an idle pane reaches serve only with its next
  status/update event or a reconnect (herdr's rename emits no event; serve's own relabels are followed by an agent
  start's events); #2 an interrupted `obor setup rename` rerun reads the new prefix as the old one (rename is once per
  project: finish it by hand); #5 rename's lane `opencode.json` loop skips an unreadable file silently.
- (T-596) Under launchd `.obor/state/logs/serve.log` is never rotated (worker: rotate at serve start, keep `.1`); and
  `obor status` tests on darwin run a real read-only `launchctl print`. Small follow-ups, not urgent. While herdr is
  down serve stays up but gates/merges nothing (lead's call 10-31: outages are short).
- (10-30, rename) **serve keeps a pane's old label**: after `herdr pane rename` + `obor setup role --pane`, serve
  identified the new agents by their old labels (hk-int at w5:pZ, label proof) until serve restarted. Label changes
  seem not to refresh serve's pane cache; any relabel (setup pane/role on a reused pane) hits it. Also ob-int's
  recorded session is still hk-int's old one (serve: "kept").
  **Lanes did not pick `ob-*` at dispatch** (T-587 claim): agent.start FAILed "pane w5:p1N is labelled hk-d-01, not
  ob-d-01" until the lead cleared all 8 lane labels by hand. `obor setup rename` should relabel (or clear) every
  project pane, and grep per-worktree `opencode.json` (D-01's named the deleted `~/Work/herd-kit/bin/herd`: "MCP
  server failed: herd"; fixed by hand). Matters only for the next rename.
  **`obor setup role --pane` keeps the shell's old env**: ob-int/ob-sol/ob-fable started in their reused shells with
  `OBOR_AGENT=hk-*` still exported, so `job_next` refused ob-int ("only the integrator (ob-int) fetches its jobs,
  not hk-int"; T-590 review stuck → needs-you). `obor restart <agent> --fresh` sets it right (verified per pid).
  setup role should export the agent env the way restart does.
- (T-577) `obor plugin restore` could resume a Codex worker from its session in the store (restore restarts each
  pane via `obor restart`, which reads the store since T-599: check a Codex worker resumes before speccing). Goal 1.
- (10-30, migrate) **Role worktrees keep the old plugin**: a detached role worktree (deep-dive, review) carries the
  tracked `.claude/settings.json` of its old commit (`herd@herd-kit`): after the switch its Claude session had no
  plugin, no inbox, and its brief sat undelivered. migrate / the UPGRADE guide: move every role worktree to the new
  tag before restarting its pane (herd-kit: deep-dive moved by hand; review is Codex, moves at the next review).
- (10-30) The lead's handoff reminder rides only on prompts: an idle lead (or one in a long tool run) gets none
  until its next prompt. Fold into phase 6's context % (status-line JSON).
- (T-563) opencode usage limits are still not read (its session store is not parsed); Codex done (T-563..T-569).
- (RV-56 #4, accepted) A StopFailure that arrives after serve already queued a role's `finished` line gives the lead
  both lines; both are true, the `stopped` one says why. A guaranteed order needs a turn-outcome signal (40-70 lines).
- (T-524, low) The job intent's outer `a.do` around a start logs an extra error line when the start returns
  "running" or is refused; old logs' pending prompt intents stay pending and count in serve's load line (S1b removes
  them).
- (T-504, low) `obor status` could mark held message rows "waiting: not identified" (Ambiguous pane): needs the status
  schema + CLI renderer; today the reply note and the needs-you reason carry it. Test rigs: a herdrtest helper that
  labels panes the way launch does. Watch `TestRefreshSubscribesAgainQuietly` (timed out once under load).
- (2026-10-26/27; Claude panes fixed by T-499) **A typed wake or brief lands in a Codex/opencode dialog** (plan approval, "Teach auto mode…"): it picks
  an option. Job wakes and role briefs are still typed. Candidate: no typing while herdr reports a dialog / blocked
  pane; or briefs over the inbox socket (Phase 5).
- (2026-10-26, low) OpenCode dispatch: each `agent.start` result lands ~30 s after start even when the job was
  fetched in 4 s; starts run one at a time. The start could end on the delivered fact.
- (DD-v0.10-verify-2, low) `ask` retries a client timeout with the same call id.
- (T-463, low) `obor answer` reuses `verdictResult.Lead` and the `held` mark; a third lead verdict → a method→rule
  table in verdict.go.
- (T-194, low) `obor sendback` onto a moved main leaves the worker a hand rebase; serve could merge main into the
  lane branch first.
- (T-334) Two `sendback`s (the lead's lane form and the integrator's MCP verb): rename or drop the lane form.
## Concerns
- (DD-v0.32, lead 11-09) The deep dive's role guard denies `git init` even in the scratchpad, while its role text says "probes in a scratch dir"; it used `go test -overlay` on tests/upgrade instead (a good probe). Decided 11-09 (owner): overlay route, named in templates/agents/deep-dive.md. Open: give tests/upgrade's alpha project every config key the new minor removed (would have caught DD-v0.32 #1).
- (lead 11-08) **Serve fires waiting work on the old binary before its self-restart after a merge:** `obor review --after T-1059,T-1060` fired at 19:35:16Z on 77df169's serve and hit the very bug T-1059 fixed; serve restarted on the new binary ~30 s later. A fix to serve's own code is live only after that restart, so an --after request or dispatch that tests the fix should fire after it (restart first, then fire). Small; decide when it bites again.
- (lead 11-07) `internal/affected/affected.go:~60`: the directory walk never stops at `/` for an absolute path (infinite loop; only reachable with a malformed changed-files list).
- (lead 11-07) **A task parked on a needs-you keeps its lane**: T-982 and T-983 (gate timeouts, not their diffs) held L-04 and L-01 while the fixes for the timeouts (T-989, T-990) waited for a lane. Since T-961 a merge-loop task holds its lane for its lane test; a parked one could release it (its tree is in its branch and candidate pin) and take a lane again when resumed. Decide with the next merge-flow change.
- (lead 11-07) **Flake, 3 sightings**: `TestWakePostRefusedThenReplayed` (internal/app) failed T-704's gate, a gate near serve.log:13435 and T-957's full re-gate 11-07 14:28 (0.7 s, passes on main in 0.8 s); each red sent a worker back. The rework's retest (plan §5 T-C) absorbs it; fix the test if it recurs after T-C.
- (lead 11-07) **Not a flake**: `TestJobNudgeFromDelivery/activity_at_8_min` red in T-956's gate was a rule bug: watchJob counted herdr 'not connected' as the integrator gone (against T-759); fixed in T-956 (jobs.go, joblost_test.go). Also: lead commits to main while candidates wait cost each a moved re-gate (12:40 BACKLOG commit moved T-950): bookkeeping only at a quiet merge queue.
- (lead 11-07) **A role agent's 'finished' fires while it waits on its own background monitors**: ob-researcher ended its turn at 11:10 with 3 Monitor tasks running (go test -json runs) and no report yet; serve sent `[obor] ob-researcher finished` → a wasted lead wake. Chatter-census class: finished = ready pane, not done work. Candidate: the finished line waits for the brief's named output file, or herdr's completion_seq (T-953 probes it).
- (lead 11-07) **Flake**: `TestDriveOfAWorkersTaskRefused/finished` (internal/app/drive_test.go:~286) compares the task's Phase before and after `r.passes()`, which can start the pending gate (`gate pending` → `gating`) under load; failed T-938's gate, passes alone. Fix: compare state/attempt only, or snapshot after the pass settles.
- (lead 11-07) **A fetched job is never re-woken after its agent's call failed.** The integrator fetched T-938/4/review at 09:53:11, its `accept` hit serve's outage ("obor serve is not running") and it ended its turn; serve kept the job `fetched` and woke nobody, also after `obor restart ob-int` and `--fresh` (T-940 queued behind it). The lead typed the wake (`[obor job T-938/4/review] call the obor job_next tool`) into the pane. Fix candidate: a role agent's restart (or serve's start) re-wakes its open fetched job; the wake is harmless twice (fetch.go:22).
- (lead 11-07) **Serve down 09:53-09:57: store at schema 13.** The audit's uncommitted schema-13 code ran as the live serve (launchd start 09:15 on a `dist/obor` built from the dirty main checkout) and migrated `.obor/state/obor.db`; T-939's merge rebuilt `dist/obor` at 12 and serve refused the store. Lead backed it up (scratchpad obor-schema13-0957.db), dropped `spec_calls` (2 receipts) and set 12. Rule: never `make build` uncommitted code in the main checkout; serve runs `dist/obor`. A store-schema change needs its rollback said in the spec.
- (11-06, state-machine audit) Tests that inject effect results through `record` can miss differences in the real
  `App.do` path (SM-03: live idle keys differed from replay). Keep production-boundary regressions for durable
  metadata and retry receipts; findings, fixes and state tables are in `docs/state-model.md` (SM-01–04).
- (11-04) `make upgrade-check` takes the catalog's newest release below VERSION as the previous one, so a second release before the first ships 404s on its download (v0.24.1 over the unpublished v0.24.0). Ran it with `PREV=0.23.1` (what the axis projects run). Fix if it recurs: default to the newest release origin has, or ship each release before tagging the next.
- (11-03) T-765 rollout on obor-kit: the old serve could not load the merged config, so `dist/obor` was not rebuilt
  and serve stayed down ("lanes: must be an object") until the lead ran `make build`, `obor setup migrate`
  (local.json) and `obor serve restart`. UPGRADE-v0.23 must give other projects that order.
- (11-03) T-766's `--after` dispatch, queued under the old serve, was placed on DeepSeek after the upgrade despite its
  `Model: opus` line (a fresh `obor dispatch` honoured it). One-time, from the Type:→Model: rename; watch for another
  queued request ignoring its spec's line.
- (11-03) `obor drop` left the running worker up ("left running; --force stops it"), and `obor restart <lane agent>`
  then started an agent with no job (idle, `resume reprompt` refused). Recovery: `restart --stop`, drop, dispatch.
- (11-04) `TestRestartReleasesTheName` (internal/launch) flaked on T-775's gate: "no claude agent came up within
  60ms" under suite load; passes alone; T-775 does not touch launch. Second flake → widen the stub's start wait.
- (11-03) `TestDriveStartRetried` (T-755) flaked once on T-757's gate (red, then green on rerun): watch for a second flake, then fix its timing.
- (11-03 08:19) one-off `needs-you loop: the branch of the main checkout …: exit status 2` (`cmd/obor/serve_dispatch.go:77`, `git branch --show-current`) around serve's re-exec after T-754/T-756 merged; cleared by itself, main clean on `main`. If it recurs: capture git's stderr in that error.
- **`internal/app` is where all growth lands** (+789 in phase 4, 7.4k; DD-v0.12 Design 1). Keep "a new state or
  step is a row in `states.go`"; split files in phase 5.
- **Surface growth:** each feature brings 2–4 review findings. Consolidate before new features.
- **Accident guard long tail:** the headless check took three rounds (RV-50 #4, RV-51 #1); stopping rule on record
  (T-486): cover the forms agents actually type.
- **Release changes to hooks** need every Claude pane restarted (hooks are read at session start; v0.9.0 lesson): say
  so in each UPGRADE guide.
- **Live Claude-pane spikes stall under auto mode** (R-15): the classifier blocks self-driving (posting into one's own
  session, piped installers). Specs for such spikes: the owner or the lead runs the step.
- **Hotfixes without review** (v0.4.2, v0.4.4): fine for tiny fixes, watch the habit.

## Ideas (parked — don't start without the owner)
- More machines (user 2026-10-12): Steam Deck, a 2019 MBP; herdr has saved SSH machines. First use: heavy merge checks
  remotely rather than remote lanes.
- Phone push via Telegram (user 2026-10-08: later). Until then notify = `herdr notification show`.
- Optional Jev (TypeSafe) step that picks extra e2e specs relevant to a diff, on top of the deterministic set,
  measured against the nightly (user 2026-10-08, low priority).
- (RV-69 #2, accepted: low) A serve crash between the reviewer's launch and `review.started` leaves that review
  untracked (no started/finished line; a later request could fresh-start the same RV). Exactly-once across a crash
  needs an uncertain-launch state; not worth it now. T-639 at least reports failed appends after a launch.
- (RV-69 verify, accepted: low) `obor review --rm` in the instant between `review.fire` and the launch's return answers
  `NO REVIEW REQUEST` instead of "claimed" (wording only; nothing is cancelled). The review-started lead line's own
  append is unchecked: if only that append fails, the lead misses the line (a failing log fails the needs-you too).
- (RV-70 verify, accepted: low, owner's one-maintainer setup) `make ship` with origin's main ahead and not fetched
  stops on the push rejection instead of skipping main (lead decision on T-643: no fetch); the non-atomic push may
  have pushed the tag first, which is harmless (the tag is the release). Fetch, then re-run ship.
- (DD-routing #1 (b), deferred) If workers still re-ask after T-655's template line: keep the lead's hold decision
  across attempts in serve (a decision stays in every later job of the task until it merges). Lead's call then.
