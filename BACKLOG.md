# Backlog

The lead appends; nobody else writes here. Triaged 2026-10-28 after v0.12.0 (DD-v0.12 C4): items about deleted
mechanisms (heartbeat, queue, `obor tell`, inbox, the mod, `obor watch`/`merge`/`ack`/`owner`, handoff, lanes state
files, bash, keep_warm, multi-lead) were closed by their deletion and removed; history is in git
(`git show 449b79e:BACKLOG.md`).

## Handoff (lead; rewrite at each handoff)
Read `docs/observations.md` first (patterns from past sessions: where we went in circles, the load tax, traps).
- 2026-11-22 (lead): v0.41-v0.43 shipped since the last entry (v0.43.0 published 11-21 21:05Z; history in git and docs/releases). Test-time wave (owner 11-21 "test cuts buy time"): T-1456 cut internal/launch 9.25 s → 5.5 s (the restore fixture polled at the real 500 ms; cap 6 → 8.3 by the ×1.5 rule, so it now sits above the old cap), T-1457 internal/setup TestGithooks (hard-linked kit, 0.2 s watchdog; cap 21.8 → 16.4). Owner's next direction: kind-agy like kinds/opencode, kinds hardening, extracting kind-codex/kind-claude, non-Claude leads: the advisor is on it (docs/research/agent-kinds-advice.md); its plan goes to the owner before any task. Every lane free, no review open, no watch.
- 2026-11-18 (lead): **Shipped v0.40.7** (first `make ship` hit a GitHub upload 408; the re-run published). Owner's herdr restart: serve's own restore resumed stopped role agents in every project (not herdr; the 11-13 note was wrong). T-1340 restores only agents with open work; T-1342 (RV-213 #1) holds a brief open while a restore is pending. T-1341: obor's git runs with GIT_OPTIONAL_LOCKS=0 and lane.prepare clears a stale index.lock (third recurrence: the locks dated from the serve re-exec after v0.40.6); T-1343 keeps the lock path in the error. RV-213 approved after verify. Codex 0.169.0 needed the owner's quarantine xattr clear. druckometer is stopped (`obor stop`), so ship did not tell it. Every lane free, no review open, no watch.
- 2026-11-16 (lead): **Shipped v0.40.6.** Owner/druckometer report: lanes stuck `working` after its `git filter-repo` (rows held pre-rewrite shas). T-1338: a landed task frees its lane when the lane HEAD is the recorded head or in the target's history; druckometer's 3 lanes read free after the install. RV-212 approved. Every lane free, no review open, no watch.
- 2026-11-15 evening (lead): **Shipped v0.40.5.** Owner's report: the nav dot looked grey because the tab predated T-1321. T-1333/T-1335: a new web build shows a "New version: reload" link. Druckometer batch 2: T-1334/T-1336 (brief says each merge passed the gate, accepted.md only when present, `obor serve restart` and the merge body's report in commands.md); #4 answered with `merge.retest` config. RV-211 approved. Every lane free, no review open, no watch.
- 2026-11-15 ~17:00 (lead): **Shipped v0.40.4.** The druckometer field report (`obor send`, 7 items): T-1326 (serve opens a missing lane pane), T-1327/T-1331 (a fresh start held by a trust dialog counts as started, kind-specific guidance), T-1328 (`obor idle` before the first merge, `spec set` lists models), T-1329/T-1330 (config reload rule, no-compile WARN at init only); #7 is herdr's (Later candidates). RV-209 approved, RV-210 closed with 3 accepted. Features still paused. Every lane free, no review open, no watch.
- 2026-11-15 (lead): **Shipped v0.40.3** (v0.40.2 tagged, never shipped; folded in): `obor lead` asks first, the handoff restart is `obor restart <you> --handoff`, a hand nightly settles the RED needs-you plus `obor resume nightly "<why>"` (axis-alies report), web nav shows agents at work. After it: release smoke edits `docs/smoke.md` (docs-only, no own review); T-1325 (release catalog from origin/main) merged, unreviewed until the next release's review. Every lane free, no review open, no watch.
- 2026-11-13 (lead): **Shipped v0.40.0 and v0.40.1.** Lead start modes (`/obor:lead handoff`), `obor review start`, moved re-gate runs `merge.check` without compile, lane dirt via `project.LaneDirt`, Docs page redesign + color (designer role, `docs/ui/`), light tones at 4.5:1, panes placed by kind (lead's tab the lead's only), submit fix for ignored links. Every lane free, no review open, no watch. Field reports from knight-tour-web and tunnel-game came in by `obor send`; answer them the same way.
- 2026-11-12 ~19:45 (lead): **Features paused by the owner** (`docs/decisions.md` 11-12): they use obor first and bring what needs changing. Only nightly RED fixes and reported bugs go ahead; the next steps below (C1 included) wait for the owner. T-1291 merged (owner start presents options, `/obor:lead handoff` continues, `obor review start`), unreleased until `make ship`.
- 2026-11-11 ~23:00 (lead; previous Handoff: `git show 935956b4:BACKLOG.md`). Every lane free, nothing queued, no
  watch, no open review (RV-196 closed). **Shipped today: v0.37.0, v0.38.0, v0.39.0.** v0.38: terminal plan steps
  1-5 (herdr behind one interface), start identity (`Locate` asks serve's `where` before any stop/label; decisions
  O1-O4), review card in status/web. v0.39: web Tasks and Docs pages, `obor flakes` real flakes only (no ancestry
  rule: entries age out in 7 days), accepted sweep (80 → 46 lines; each deep dive now sweeps accepted.md). Config:
  Fable is the advisor only; hard/integrator/deep dive on opus. axis-maps was reset 11-11 (fresh store, v2 config).
  **Next steps:** (1) 11-16 or later: C1 store migrations (DD-v0.35 group 1). (2) After C1: decide A1 steps 3-4 from
  the check-then-act findings. (3) Parked until the owner decides a second terminal: D3 and terminal plan steps 6-9
  (`docs/plan/terminal-adapters.md`). (4) Watch the first live dispatches on axis-alies/axis-maps under start identity.
## Owner decisions pending
## Later candidates
- (DD-architecture verify 2, not scheduled) `Pane.Agent` → `Pane.Kind` rename (wide) and the remaining `Start` names (`agents.Start`, `port.AgentStart`, `port.Started` → `KindStart`/`TerminalStart`): take with the next task that rewrites those types. Spec rule for the next merge of two flows: list the texts that differ AND the refusals one side has and the other lacks.
- (herdr restore, RV-225 design note) A live probe in a throwaway named herdr session with resume on and several staggered panes, covering serve's replacement end to end (the recorded probe covers one bare resume): makes the 60 s settle boundary testable against herdr upgrades.
- (hc, owner 11-20: not worth a fix) `obor uninstall` on hc leaves the project's kept terminals as idle shells (~1 MB each) and herdr-app's space: the close's `terminal.kill` is undone by keep (T-1382 dropped; fix: `terminal.keep {keep:false}` before the kill, and hctest models keep). Revisit if a re-init after uninstall trips over terminals still labelled with its agents.
- (druckometer field report 11-15, #7) `herdr pane list` dumps every workspace as one JSON blob; finding a project's lane tab means scanning all of them. Owner action, upstream: ask herdr for a workspace/cwd filter. The other six items: T-1326..T-1329.
- **Terminal port: herdr, rex, or both** (advisor 11-11, `docs/research/terminal-port-advice.md`, `docs/plan/terminal-adapters.md`): steps 1-5 done 11-11 (T-1245..T-1253, RV-188/189, DD-v0.38; strings T-1255, name rule T-1254). Next only with a decided second terminal: D3, step 6 (the start split), step 7, step 8 a Rex probe. Step 6's spec lists every message and state file it touches and what each becomes (DD-v0.38: T-1250..T-1253 each repaired one a step had changed).
- **`obor run <name> [--after T-xx] -- <cmd>`** (axis-alies proposal 11-10; owner: parked, feels like feature creep). Serve places a run pane (`TerminalSetup.Place`, like role panes), a wrapper tees a log, writes the rc file and reports the end (one line + owner notice, the last output line); `obor status` lists it (no idle line); `--after` via watches; `--rm`. Survives lead handoffs and works for every agent kind. Sandbox: serve runs outside Codex's sandbox, so accept it from the lead (and the owner's shell) only; named config commands for roles only by decision. Build when a long run's result is lost to a lead handoff/restart, or a non-Claude lead needs one. Until then: the agent's own background run + `obor idle "<job> running"`.
- **Compat after v0.35** (DD-v0.35): C1 done (T-1347, 11-20); P1 one-off SQL record delete: obor-kit done 11-10 (636 rows; pre-delete copy `.obor/state/backup/obor-2026-11-10-preP1.db`), axis-alies done 11-10 (151 rows, owner confirmed); C15-C18 replay branches deleted by T-1222; C25 `tests/upgrade` kept for v0.35, decide after. Optional: `obor serve prune-retired` if more project-key shapes retire.
- **Per-role skills** (decisions 11-10: config `agents.roles.<role>.skills`, skills unchanged; ~150-250 lines on top of T-1201's start input; OpenCode's global `permissions` deny unverified: probe first). Build when the first project has role-specific skills (e.g. the owner's 3D game) or a role pulls another role's skill.
- **Un-defer `job_next`** (chatter census proposal 2; advisor 11-07 docs/research/job-in-first-prompt-advice.md: no job-in-prompt; T-959/T-960/T-941 did the kind-neutral parts). Left: `alwaysLoad: true` on obor's MCP entry, Claude-only, so parked under the owner's agent-neutral rule (11-07); revisit if another kind gets the same deferral.
- **Log snapshot at upgrade** (parked 11-05, docs/research/log-snapshot.md): build when compat branches pass ~300
  prod lines or a record-shape change needs it.
- (RV-63 #4, accepted by rule) `scripts/publish` checks the local tag only, not that it is the remote one: a moved
  pushed tag could upload bytes fresh installs reject. Rule instead of code: a pushed tag never moves (RELEASING.md).
- **herdr-core (`hc`)**: superseded 11-20 by `docs/plan/herdr-core.md` (owner: second adapter, HD1-HD9; T-1350/T-1351 steps 1/0). Old note (`docs/research/herdr-core.md`, 10-30): a separate headless terminal core (own `herdr-core`/`hc`
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
- (lead 11-22, RV-240) A task dispatched before a review started and linked to it later (`Fixes:` by `spec set`) stops counting once merged: `fixCandidates` (`internal/app/reviewverify.go:47`) skips ended tasks with `dispSeq <= start`, so no verify fires (the lead briefed RV-240's by hand). T-1472 fixes it.
- (lead 11-22, T-1460) **No way to bring the target into a lane**: a task whose change breaks a test merged on the target after its lane started cannot edit that file (workers have no git writes; adding it is an add/add conflict); the lead merged staging into task/T-1460 by hand. First time seen; if it recurs, a serve step (the fix job of a red gate merges the target into the lane when it is clean) instead of the lead's hand merge.
- (RV-239 design, lead 11-22) Two deciders of a lead's delivery mode: `obor whoami`'s `lines:` (configured kind caps, `internal/guard/whoami.go:31`) and serve's route (`internal/app/leadlines.go:24-33`, the identified pane's kind). Give it one owner (whoami asks serve's decision, or says it is the configured prediction) with the next task touching either; accepted rest in `docs/accepted.md`.
- (T-1462 hold, lead 11-22) OpenCode's `opencode/obor.js` runs user-prompt-submit and stop but ignores their stdout, so its lead gets no lines through hooks (new cap `hook_answers` false). Later task: obor.js acts on the answers (additionalContext through its context hook; live probe first), then OpenCode sets the cap.
- (RV-238 design, lead 11-22) `ContextOf` asks every transcript-capable kind in name order, can hand one session's transcript tail to an unrelated plugin, and costs up to 5 s per plugin kind in a lead hook or under the keep-lane mutex. Harmless today (OpenCode has no transcript). **Before kind-agy (the first transcript plugin): route by the recorded session's kind and bound the call** — goes into the kind-agy start/wiring spec.
- (RV-214 design, lead 11-20) Two temp-index builders of lane work with different exclusions: `merge_preview` (`internal/checks/preview.go`, T-1349) and `obor run` (`internal/run/run.go:182`). Give them one owner if their rules converge; until then keep the differences named in both.
- (lead 11-13) `obor restart ob-designer --fresh` (pane w5:p2F, first start of the new designer role) printed `terminal agent start ob-designer: no claude agent came up in w5:p2F within 30s` and `FAIL start failed` while Claude was up and working the brief (screen and processes in the same output show it). First `obor restart` start after Claude Code 2.1.322 → 2.1.323; serve's dispatch starts (T-1300, T-1301) worked after the update. If the next restart start fails the same way: a bug task on the start's readiness detection (herdr agent field vs the new version).
- (lead 11-12) One local `go test ./internal/app/` failed after 1.9 s with a panic through `serve.go:88` (output not kept); the next run passed. Not the T-1290 SQLITE_BUSY hang (that takes 2 minutes). If it recurs, keep the output (`go test ./internal/app/ > <file> 2>&1`) before re-running.
- (RV-185 design, lead 11-10) T-1231 raised the real-binary git-hook test's watchdog to 30 s while the shipped hook limit is 2 s: no test shows a healthy cold binary fits 2 s on a loaded machine. Gate failure output is not kept, so the old flakes prove nothing either way. If a hook times out live (a commit hook denying a healthy binary), measure the cold path before touching the limit.
- (T-1225, lead 11-10) A role worktree's `lanes.setup` runs on the merge loop at a review's start (the loop held for the run, up to its timeout, as `lane.prepare` does), and is bounded at 30 s on the verify (`verifyGitWait`) and brief paths: a setup longer than 30 s there fails each pass. Fires only when the lockfile changed between a review's start and its verify. If seen: run the setup by hand in the role worktree; the fix is one task moving lane.prepare's and the role move's setup off the loop with the command's own timeout.
- (T-1222, lead 11-10) **C16 premise was wrong**: deleting `oldLineOf` made serve resend every pre-Nov-6 watch line to the lead once (206 stale lines, msg seq 38062-39259; now stored with `of`, no repeat in obor-kit). T-1223 restores it; **v0.36 must include T-1223** (axis-alies' log still has old lines). Lesson: before deleting a replay branch, grep who reads the field at every replay, not only whether an item is open.
- (T-1221, lead 11-10) A step that fails at once (a cut stop returning `context.Canceled`) is retried on no timer: `do` marks the lane table dirty → the refresher scans → `a.wake()` → the next pass tries again, as fast as a lane scan runs. Found as a synctest hang (the test now paces the fake); in production a stop failing instantly would spin. Fix if seen: a backoff on a failed step's retry.
- (T-1135, lead 11-09) Serve tells a herdr restart by the herdr socket file's mtime (`.obor/state/herdr-instance`): herdr's API has no pid, start time or instance id (protocol 22 pong, SessionSnapshot). Undocumented: if `herdr update --handoff` recreates the socket, serve restores once (stopped role agents restart once, as the old plugin did at every herdr start). Owner action when convenient: ask herdr upstream for an instance id in pong/snapshot (with discussion #5684); then switch to it.
- (RV-145 design) the workflow contract table (T-1029) covers scratch, lane and target, not the review methods (tested on their own today): extend it with their portable outcomes before a second workflow implementation. Also `checks.ScratchFF` holds the config twice (`Scratch.Options.Cfg`, `Lane.Cfg`): one when next touched.
- (RV-143 verify design) `specSetRev` parses the rev from the spec-set reply's text (T-1026), coupling replay to reply wording: keep the format, or a structured rev when the record is next revised.
- (lead 11-08) `TestScratchCutTable` costs 31 s of work at 2 cores even after T-1000 (TestNightly 6.7 s, lane tests 2-4 s): they stay in -short with caps (T-1004 rev 5); a speed-up task for them lets the caps drop.
- (lead 11-07) opencode (deepseek) workers ignored Escape twice (T-958, T-992), both needed `obor drop --force`.
- (lead 11-05) An empty untracked `.obor/obor.db` (0 bytes, 21:54) appeared in the main checkout and blocked `obor review close` (dirty); moved to the lead's scratchpad. Not `obor serve dump`. Something opened the store at `.obor/` instead of `.obor/state/` (an empty derived dir?): find the caller if it recurs (AGENTS.md: empty derived value fails closed).
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
- (10-30) The lead's handoff reminder rides only on prompts: an idle lead (or one in a long tool run) gets none
  until its next prompt. Fold into phase 6's context % (status-line JSON).
- (T-563) opencode usage limits are still not read (its session store is not parsed); Codex done (T-563..T-569). Parsing it would also give OpenCode role agents the done reminder (T-1348/T-1353 need a last-activity time; RV-215 verify #1, accepted).
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
## Concerns
- (lead 11-22, T-1472, first agy lane) agy's auto-mode reviewer asked before `go test -tags integration` and a read-only `git status` (sandbox bypasses), stalling the lane twice; owner 11-22: obor-kit's agy agents run `always-proceed` (`.obor/agy/settings_override.json`). Check the next agy lane runs with no dialog; `git status` fails in agy's sandbox in a linked worktree (moot under always-proceed).
- (lead 11-22) **`internal/launch` time cap**: T-1456 brought it to 5.5 s (was 9.2 s against 6.0); cap now 8.3. Confirm at the next full check, then drop this line. Git per full test run 2,670 (11-21 full check); the race fix (T-1437) verified: 0 data races.
- (lead 11-11) **herdr server CPU bursts** (pid 98146, up 5d16h, 2912 CPU-min = ~35% average): at 09:50 it ran at 100% for a while (`sample`: main thread looping in `handle_scheduled_tasks_headless` → `App::start_pending_agent_resumes` with no wait, log quiet), at 09:58 8%. Not the budget breaches' main cause (macOS StorageManagement at ~120% for an hour was). Check whether obor's `launch_pending` agents feed that resume list before an upstream report.
- (lead 11-08) **Serve fires waiting work on the old binary before its self-restart after a merge:** `obor review --after T-1059,T-1060` fired at 19:35:16Z on 77df169's serve and hit the very bug T-1059 fixed; serve restarted on the new binary ~30 s later. A fix to serve's own code is live only after that restart, so an --after request or dispatch that tests the fix should fire after it (restart first, then fire). Small; decide when it bites again.
- (lead 11-07) **A task parked on a needs-you keeps its lane**: T-982 and T-983 (gate timeouts, not their diffs) held L-04 and L-01 while the fixes for the timeouts (T-989, T-990) waited for a lane. Since T-961 a merge-loop task holds its lane for its lane test; a parked one could release it (its tree is in its branch and candidate pin) and take a lane again when resumed. Decide with the next merge-flow change.
- (11-06, state-machine audit) Tests that inject effect results through `record` can miss differences in the real
  `App.do` path (SM-03: live idle keys differed from replay). Keep production-boundary regressions for durable
  metadata and retry receipts; findings, fixes and state tables are in `docs/state-model.md` (SM-01–04).
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
