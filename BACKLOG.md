# Backlog

The lead appends; nobody else writes here. Triaged 2026-10-28 after v0.12.0 (DD-v0.12 C4): items about deleted
mechanisms (heartbeat, queue, `obor tell`, inbox, the mod, `obor watch`/`merge`/`ack`/`owner`, handoff, lanes state
files, bash, keep_warm, multi-lead) were closed by their deletion and removed; history is in git
(`git show 449b79e:BACKLOG.md`).

## Handoff (lead; rewrite at each handoff)
Read `docs/observations.md` first (patterns from past sessions: where we went in circles, the load tax, traps).
- 2026-11-25 evening (lead; previous Handoff: `git show 11b429f3:BACKLOG.md`). **The internal/app plan is done**
  (`docs/plan/internal-app.md`, status line at its end): tasks 1-15 merged and reviewed (RV-268..RV-274 all closed;
  RV-267 closed with #2 accepted). 33 merges since v0.47.0, all reviewed: lane.sync fix attempts from the target
  (T-1573/1574/1579), the append/lane-row error rule (T-1582), the internal/app restructuring (T-1575..T-1588),
  test time 17.5 s → 7.5 s (T-1589..T-1605). Lead decision: no in-memory test store for now (`docs/decisions.md`).
- **v0.47.1 released 11-25 night** (tag v0.47.1, main 73204b72): T-1606..T-1611 + the two deep dives and advice
  (`docs/research/v0.47.1-deep-dives-advice.md`), RV-275/RV-276 closed (4 accepts). Shipped 11-25 (owner). After it: T-1612/T-1613 (a
  tracked path is never linked; RV-277 closed). 11-26: nightly RED 58e34f2 fixed (T-1614, resumed). T-1615 (recordIn
  true once appended), T-1616 (agy update_steps), T-1617 + T-1618 (reviewer/deep dive probe dir named in every brief,
  emptied at stop; a sandbox that cannot write the shared Go cache puts it there). RV-278 closed. **v0.47.2 released 11-26** (main
  360c02b8). Shipped 11-26 (owner). Then T-1619 (Claude sessions named `<project> <agent>`, owner from the phone),
  RV-279 closed; **v0.47.3 released 11-26** (main 00767555). Shipped 11-26 (owner). 11-27: T-1620 (nightly flake), T-1621..T-1623 (init in a
  folder with files and no commit; the lead's first-session files question), RV-280 closed after two rounds (2
  accepted); **v0.47.4 released 11-27** (main c45e444a); owner ships. Nothing in flight.
- **Next (owner picks):** (a) a release v0.47.1 (user-visible: fix attempts start from the target, link restore
  after abort; the rest is internal; the pre-release two deep dives rule applies, `docs/agents/lead.md`); (b) BACKLOG
  Open items (guard rejected-command count due ~11-30; the custom-role first restart / doc name; Rex field issues);
  (c) the smaller test topics only alongside other changes.
- Owner pending: nothing. Both advisors and the gemini_advisor stopped.

## Owner decisions pending
## Later candidates
- (DD-architecture verify 2, not scheduled) `Pane.Agent` → `Pane.Kind` rename (wide) and the remaining `Start` names (`agents.Start`, `port.AgentStart`, `port.Started` → `KindStart`/`TerminalStart`): take with the next task that rewrites those types. Spec rule for the next merge of two flows: list the texts that differ AND the refusals one side has and the other lacks.
- (herdr restore, RV-225 design note) A live probe in a throwaway named herdr session with resume on and several staggered panes, covering serve's replacement end to end (the recorded probe covers one bare resume): makes the 60 s settle boundary testable against herdr upgrades.
- (hc, owner 11-20: not worth a fix) `obor uninstall` on hc leaves the project's kept terminals as idle shells (~1 MB each) and herdr-app's space: the close's `terminal.kill` is undone by keep (T-1382 dropped; fix: `terminal.keep {keep:false}` before the kill, and hctest models keep). Revisit if a re-init after uninstall trips over terminals still labelled with its agents.
- (druckometer field report 11-15, #7) `herdr pane list` dumps every workspace as one JSON blob; finding a project's lane tab means scanning all of them. Owner action, upstream: ask herdr for a workspace/cwd filter. The other six items: T-1326..T-1329.
- **`obor run <name> [--after T-xx] -- <cmd>`** (axis-alies proposal 11-10; owner: parked, feels like feature creep). Serve places a run pane (`TerminalSetup.Place`, like role panes), a wrapper tees a log, writes the rc file and reports the end (one line + owner notice, the last output line); `obor status` lists it (no idle line); `--after` via watches; `--rm`. Survives lead handoffs and works for every agent kind. Sandbox: serve runs outside Codex's sandbox, so accept it from the lead (and the owner's shell) only; named config commands for roles only by decision. Build when a long run's result is lost to a lead handoff/restart, or a non-Claude lead needs one. Until then: the agent's own background run + `obor idle "<job> running"`.
- **Per-role skills** (decisions 11-10: config `agents.roles.<role>.skills`, skills unchanged; ~150-250 lines on top of T-1201's start input; OpenCode's global `permissions` deny unverified: probe first). Build when the first project has role-specific skills (e.g. the owner's 3D game) or a role pulls another role's skill.
- **Un-defer `job_next`** (chatter census proposal 2, `git show v0.44.0:docs/research/chatter-census.md`; advisor 11-07 docs/research/job-in-first-prompt-advice.md: no job-in-prompt; T-959/T-960/T-941 did the kind-neutral parts). Left: `alwaysLoad: true` on obor's MCP entry, Claude-only, so parked under the owner's agent-neutral rule (11-07); revisit if another kind gets the same deferral.
- **Log snapshot at upgrade** (parked 11-05, docs/research/log-snapshot.md): build when compat branches pass ~300
  prod lines or a record-shape change needs it.
- (DD-v0.14 U3/U6/S4) The difficulty map's type names and the Go-only load-run rules are obor-kit's own policy built
  into the kit; simpler: `obor init` writes them into the project's config. Not before a second adopter.
- `obor doctor`: hooks loaded + kit version per pane (stale panes hid guard bugs twice).
- Per-project kit pin (rollout lesson v0.6.0): moving `obor-kit-stable` upgrades every adopter's CLI at once while
  hooks follow per pane restart; a breaking release needs an opt-in pin or code that handles old state.

## Open
- (owner 11-25) **Rex trial works** on v0.47.0 (owner's first project on `project.terminal: "rex"`). Rex stays experimental (live tier non-gating); bring field issues as fix tasks. Watch on each Rex update: API revision (REX-R1 warn), the bundle path, and the stream-seq concern (Concerns).
- (11-23, T-1541 merged 11-23) Unparsed shell commands (advice Concerns: `echo ${(U)foo}; git commit -m x` passes for an advisor under zsh): owner 11-23 "agents often write complex bash commands, this might block their work" → keep allowing; next wave the guard logs each command its parser rejects (role, first 200 chars) and after a week of lanes the lead brings the count to the owner (near zero: deny for roles with git denies; else leave). Count due ~11-30.
- (lead 11-24) codex-image-tool (public repo) asked to keep obor files out of it: owner 11-24 "branch route for now" (private merge.target_branch + hand-filtered public push); no private-files mode. What breaks with ignored obor files: config_set's commit, review close --accept (docs/accepted.md), role-doc pointer and web Docs (tracked files only); review start fails once the previous review's head sha is gone after a history rewrite.
- (lead 11-24) agy's cli.log (`~/.gemini/antigravity-cli/cli.log`) is shared by every session: look for a per-session log setting.
- (lead 11-25) herdr upstream asks, for when the owner talks to herdr: env on `agent.start` (would let RV-257 #1 be fixed per process); a workspace/cwd filter for `herdr pane list` (druckometer #7, Later candidates).
- (RV-261 design, 11-25) OC-L7's probe treats every nonempty namespace as a connected MCP server (`tests/conformance/live_opencode.go:517`, `opencode_events.mjs:17`), while the runtime checks `ctx.mcp.list()`: a namespaced plugin tool (`probe-plug_probe_code`) needing a row is missed. Fix: give the probe the real connected-server names. Pre-existing; take with the next OpenCode conformance task.
- (lead 11-23) Nightly 11-23 02:00: agy live tier AG-L1 failed "model gemini-3.8-flash-low is not in agy models" although `agy models` lists it at 07:15 (and AG-L7 skipped). Likely an empty or partial live answer in the lab's env (F11, fixed by T-1521 for the cache case; the lab HOME may have no cache). Goes into W6's spec: find why, report the list the lab saw.
- (owner 11-22, widened 11-25) **Every read-only role may use subagents, on every kind** (researcher, advisor, reviewer, deep dive; not workers, the integrator or the lead's lanes). Today: Claude's Agent tool and OpenCode's `subagent` have no guard rule (allowed for every role), agy's `invoke_subagent` is denied for every obor agent (T-1505: a subagent's SessionStart is recorded as the agent's session, RV-250 #1), Codex unchecked. Needs: per kind, a child session told apart from the agent's (agy P13: no field names the parent; or skip session recording for a role agent's later SessionStarts while one is live) and the child's tool calls under the same role guard; then one per-role switch (e.g. `agents.roles.<role>.subagents`) instead of per-kind rules. Probe first; also `schedule`'s conversations (RV-250 accepted rest).
- (RV-238 design, lead 11-22) `ContextOf` asks every transcript-capable kind in name order, can hand one session's transcript tail to an unrelated plugin, and costs up to 5 s per plugin kind in a lead hook or under the keep-lane mutex. Harmless today (OpenCode has no transcript). **Before kind-agy (the first transcript plugin): route by the recorded session's kind and bound the call** — goes into the kind-agy start/wiring spec.
- (RV-214 design, lead 11-20) Two temp-index builders of lane work with different exclusions: `merge_preview` (`internal/checks/preview.go`, T-1349) and `obor run` (`internal/run/run.go:182`). Give them one owner if their rules converge; until then keep the differences named in both.
- (lead 11-13) `obor restart ob-designer --fresh` (pane w5:p2F, first start of the new designer role) printed `terminal agent start ob-designer: no claude agent came up in w5:p2F within 30s` and `FAIL start failed` while Claude was up and working the brief (screen and processes in the same output show it). First `obor restart` start after Claude Code 2.1.322 → 2.1.323; serve's dispatch starts (T-1300, T-1301) worked after the update. If the next restart start fails the same way: a bug task on the start's readiness detection (herdr agent field vs the new version).
- (RV-185 design, lead 11-10) T-1231 raised the real-binary git-hook test's watchdog to 30 s while the shipped hook limit is 2 s: no test shows a healthy cold binary fits 2 s on a loaded machine. Gate failure output is not kept, so the old flakes prove nothing either way. If a hook times out live (a commit hook denying a healthy binary), measure the cold path before touching the limit.
- (T-1225, lead 11-10) A role worktree's `lanes.setup` runs on the merge loop at a review's start (the loop held for the run, up to its timeout, as `lane.prepare` does), and is bounded at 30 s on the verify (`verifyGitWait`) and brief paths: a setup longer than 30 s there fails each pass. Fires only when the lockfile changed between a review's start and its verify. If seen: run the setup by hand in the role worktree; the fix is one task moving lane.prepare's and the role move's setup off the loop with the command's own timeout.
- (T-1221, lead 11-10) A step that fails at once (a cut stop returning `context.Canceled`) is retried on no timer: `do` marks the lane table dirty → the refresher scans → `a.wake()` → the next pass tries again, as fast as a lane scan runs. Found as a synctest hang (the test now paces the fake); in production a stop failing instantly would spin. Fix if seen: a backoff on a failed step's retry.
- (T-1135, lead 11-09) Serve tells a herdr restart by the herdr socket file's mtime (`.obor/state/herdr-instance`): herdr's API has no pid, start time or instance id (protocol 22 pong, SessionSnapshot). Undocumented: if `herdr update --handoff` recreates the socket, serve restores once (stopped role agents restart once, as the old plugin did at every herdr start). Owner action when convenient: ask herdr upstream for an instance id in pong/snapshot (with discussion #5684); then switch to it.
- (RV-145 design) the workflow contract table (T-1029) covers scratch, lane and target, not the review methods (tested on their own today): extend it with their portable outcomes before a second workflow implementation. Also `checks.ScratchFF` holds the config twice (`Scratch.Options.Cfg`, `Lane.Cfg`): one when next touched.
- (RV-143 verify design) `specSetRev` parses the rev from the spec-set reply's text (T-1026), coupling replay to reply wording: keep the format, or a structured rev when the record is next revised.
- (lead 11-08) `TestScratchCutTable` costs 31 s of work at 2 cores even after T-1000 (TestNightly 6.7 s, lane tests 2-4 s): they stay in -short with caps (T-1004 rev 5); a speed-up task for them lets the caps drop.
- (lead 11-07) opencode (deepseek) workers ignored Escape twice (T-958, T-992), both needed `obor drop --force`.
- (10-30, matters only for the next rename) After a project rename: serve kept panes' old labels until restart; lanes needed their labels cleared by hand; `obor setup role --pane` kept the reused shell's old `OBOR_AGENT` (`obor restart <agent> --fresh` fixes it). `obor setup rename` should relabel every project pane and export the agent env as restart does.
- (10-30) The lead's handoff reminder rides only on prompts: an idle lead (or one in a long tool run) gets none
  until its next prompt. Fold into phase 6's context % (status-line JSON).
- (T-563) opencode usage limits are still not read (its session store is not parsed); Codex done (T-563..T-569). Parsing it would also give OpenCode role agents the done reminder (T-1348/T-1353 need a last-activity time; RV-215 verify #1, accepted).
- (2026-10-26/27; Claude panes fixed by T-499) **A typed wake or brief lands in a Codex/opencode dialog** (plan approval, "Teach auto mode…"): it picks
  an option. Job wakes and role briefs are still typed. Candidate: no typing while herdr reports a dialog / blocked
  pane; or briefs over the inbox socket (Phase 5).
## Concerns
- (v0.47.1 advice, B design concern 1) Only gate, test, publish and (T-1609) lane.sync make a task busy while their
  intent waits for a replay; `submit`, `lane.prepare` and `lane.free` run git in the lane too and do not. Unprobed.
- (11-25, RV-273 notes) Test fakes left after one-fake-per-port: `lastClose` (`internal/app/rig_dispatch_ext_test.go:391`) looks redundant with `FakeWorkflow`; `concFakes` overrides Prepare/Free/Target with no reason named; `FakeWorkflow` keeps the integration head twice (`target` and `head`). Take with the next task that touches those rigs: one head owner, delete or document the exceptions.
- (11-25, RV-271 design) The step-table exhaustiveness test (`internal/app/states_test.go:404-484`) keeps its own classification of every non-kernel `event.Step*` and parses Go source assuming one string literal per constant: simplify to a shared step classification if it starts costing edits on unrelated steps (keep the missing-kernel-step check).
- (11-25, RV-264 design) Rex's stream protocol has sequence numbers but the `rex events` CLI drops them (`internal/rex/contract.go:71-79`): obor relies on Rex's delivery and disconnect guarantee and cannot detect a gap. On each Rex upgrade re-run the burst probe (`make conformance TIER=live KIND=rex`); move to a transport that keeps `seq` if Rex documents one, or if a gap is ever seen.
- (11-25, RV-263) A cleared wake sends `ctrl+u` before the prompt as a separate key (`internal/hc/control.go:34-44`, `internal/herdr/socket.go:487-494`): a dialog that opens after `port.GuardPrompts`' read can get that key before the native send refuses. Pre-existing; no atomic no-key guarantee. Matters more on Rex (no native refusal at all). Fix only with an upstream guarded clear-and-send, or drop the separate clear.
- (11-22, RV-248) Hook time budgets drift: the lead-lines deadline starts inside the hook while OpenCode's transport has its own 5 s timer and the hook works before asking. When this path grows, carry one end-to-end deadline (or reserve the preceding work's budget).
- (11-22, RV-246) A resumed idle OpenCode lead woken by a posted line (`OBOR_OPENCODE_RESUME`, T-1487) is unit-tested but not seen live: the reviewer's sandbox refused the listener. Check it at the next OpenCode lead restart (scratch project), before relying on it.
- (11-22, RV-245) agy's quota-limit patterns (`kinds/agy/kind/kind.go` ~400) match CLI texts whose stop-hook form was never observed, and `agents.ResetIn` cannot parse "until 18:00": capture a real agy quota stop when one happens and check both.
- (lead 11-22, T-1471) **OpenCode inbox has no ack**: a line posted to the plugin's socket counts as sent even if `session.prompt` then fails inside the plugin, so a lead line or a worker's wake can be lost. Since T-1471 OpenCode workers' wakes (deepseek lanes: the first model of medium and simple) go through it: watch for a deepseek lane that never picks up its job after a wake; if seen, the plugin acks after `session.prompt` returns.
- (lead 11-22) agy lanes: the first (T-1472) stalled twice on agy's auto-mode dialog; with obor-kit's `always-proceed` override the second (T-1477, gemini) ran with no dialog and its fix passed review. Two clean agy lanes so far; the codex/claude extraction waits for a few days of them (owner D5).
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
- (owner 11-26, parked) **cmux as a terminal** (`docs/research/cmux.md`): cmux consumes OSC 7501 but gives clients only
  `program_status.changed` events with state/kind (no `app`/`msg`, no snapshot query); its socket's default `cmuxOnly`
  refuses serve (a LaunchAgent); its own agent layer (hook wrappers, hibernation, auto-resume) would act on obor's
  agents. Revisit when cmux adds a per-surface status query with app/msg (the doc's upstream asks 1, 2, 5); then probes
  C1/C2 on an isolated instance, adapter ~ Rex-sized.
- (owner 11-25: maybe later) `obor interrupt <agent|T-xx> ["<line>"]`: Esc through serve's identity of the pane (today only `herdr pane send-keys <pane> esc` by hand), an optional redirect line, and no stall nudge until that line is delivered.
- More machines (user 2026-10-12): Steam Deck, a 2019 MBP; herdr has saved SSH machines. First use: heavy merge checks
  remotely rather than remote lanes.
- Phone push via Telegram (user 2026-10-08: later). Until then notify = `herdr notification show`.
- Optional Jev (TypeSafe) step that picks extra e2e specs relevant to a diff, on top of the deterministic set,
  measured against the nightly (user 2026-10-08, low priority).
- (DD-routing #1 (b), deferred) If workers still re-ask after T-655's template line: keep the lead's hold decision
  across attempts in serve (a decision stays in every later job of the task until it merges). Lead's call then.
