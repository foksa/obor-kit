# Backlog

The lead appends; nobody else writes here. Triaged 2026-10-28 after v0.12.0 (DD-v0.12 C4): items about deleted
mechanisms (heartbeat, queue, `obor tell`, inbox, the mod, `obor watch`/`merge`/`ack`/`owner`, handoff, lanes state
files, bash, keep_warm, multi-lead) were closed by their deletion and removed; history is in git
(`git show 449b79e:BACKLOG.md`).

## Handoff (lead; rewrite at each handoff)
Read `docs/observations.md` first (patterns from past sessions: where we went in circles, the load tax, traps).
- 2026-11-10 ~11:45 (lead; previous Handoff: `git show 4163dc6a:BACKLOG.md`). Every lane free, nothing queued, no
  watch. **v0.34.0 shipped** (origin/main 510405c4). Since: the owner's Fable audit (`docs/research/improve-plans.md`,
  plans 001-008 all DONE): nightly race run, web Host allowlist, Linux no longer claimed, the in-process synctest rig
  for internal/app (P0-P10), CLI/herdr version watch, `docs/accepted.md` for accepted findings, the status skew line
  (one `started` signal per confirmed start, T-1197), and the text sweeps (no ids, no previous states in texts and
  comments; rule in AGENTS.md). Config: `agents.difficulty.hard = fable:medium, opus:high`; deep_dive fable:medium,
  integrator fable:low (no fallback model: if Fable's quota ends, `obor pause --model fable` and move both roles back
  to opus with the owner's OK). RV-175 closed approved. **A review is running** (13 merges: comment sweeps T-1180,
  T-1181, T-1183, T-1184 and rig P4-P10, T-1188-T-1194); its finish line comes to the next lead.
  **Next steps:** (1) The review: fix tasks with `Fixes: RV-<n>` (its id), verify, close; findings in comments only are cheap
  to accept. (2) Ask the owner whether to keep Fable first when the usage resets end. (3) Owner decisions below:
  agent skills scope. (4) v0.35: delete the baseline/gate/e2e validate WARN (Later candidates), then cut a release
  (the audit wave is unreleased). (5) Owner when convenient: herdr instance id (T-1135). BACKLOG Open triaged 11-10
  (15 left).
## Owner decisions pending
- **Agent skills scope** (owner asked 11-09, research only so far): `docs/research/agent-skills-scope.md` §9. Q1 keep
  the owner's global plugins/skills out of non-lead agents (1A: Claude `--setting-sources project,local`, Codex `-c`
  feature keys, OpenCode env; ~60-90 lines; saves ~3-5k tokens per Claude start, 2.3k Codex; the lead's 12k estimate
  was high: the listing is 7.7k). Side effect: drops herdr's user hook for Claude (no `agent_session`). Q2 per-role
  project skills: 2A (one `.agents/skills` tree, role text names them, docs only) vs 2B (obor hides per role, ~150-250
  lines). Bigger lever found on the way: Claude's `Artifact` tool definition alone is ~9k tokens (`--disallowedTools`,
  not probed). Seven open choices listed in §9.

## Later candidates
- (v0.34 release) validate's WARN for a set `commands.baseline`/`gate`/`e2e` (T-1083, "until v0.34") kept one more release: delete it in v0.35 with its test.
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
- (T-1135, lead 11-09) Serve tells a herdr restart by the herdr socket file's mtime (`.obor/state/herdr-instance`): herdr's API has no pid, start time or instance id (protocol 22 pong, SessionSnapshot). Undocumented: if `herdr update --handoff` recreates the socket, serve restores once (stopped role agents restart once, as the old plugin did at every herdr start). Owner action when convenient: ask herdr upstream for an instance id in pong/snapshot (with discussion #5684); then switch to it.
- (RV-145 design) the workflow contract table (T-1029) covers scratch, lane and target, not the review methods (tested on their own today): extend it with their portable outcomes before a second workflow implementation. Also `checks.ScratchFF` holds the config twice (`Scratch.Options.Cfg`, `Lane.Cfg`): one when next touched.
- (RV-143 verify design) `specSetRev` parses the rev from the spec-set reply's text (T-1026), coupling replay to reply wording: keep the format, or a structured rev when the record is next revised.
- (lead 11-08) `TestScratchCutTable` costs 31 s of work at 2 cores even after T-1000 (TestNightly 6.7 s, lane tests 2-4 s): they stay in -short with caps (T-1004 rev 5); a speed-up task for them lets the caps drop.
- (lead 11-07) opencode (deepseek) workers ignored Escape twice (T-958, T-992), both needed `obor drop --force`.
- (lead 11-06) Empty `index.lock` files appeared in all four lane worktrees (`.git/worktrees/L-0x/index.lock`) at 12:31, with no git running; every dispatch then failed `lane.prepare … checkout -B … exit status 128`. Serve had not restarted (up since 12:15); the deep dive (12:22) and the RV-127 verify (Codex sandbox) were the only other agents. Removed by hand. If it recurs: find who runs git in the lanes and dies; `lane.prepare` could name the lock in its FAIL.
- (lead 11-05) An empty untracked `.obor/obor.db` (0 bytes, 21:54) appeared in the main checkout and blocked `obor review close` (dirty); moved to the lead's scratchpad. Not `obor serve dump`. Something opened the store at `.obor/` instead of `.obor/state/` (an empty derived dir?): find the caller if it recurs (AGENTS.md: empty derived value fails closed).
- (T-606) Other "oldest/first" choices that range over `p.loop` (a map) go by Go's map order: job_next's did
  (T-602 → T-606, a flaky gate). Sweep: each such choice orders by intent seq or task; a candidate for the `proj`
  split's folded-state step.
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
- (T-563) opencode usage limits are still not read (its session store is not parsed); Codex done (T-563..T-569).
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
- (DD-v0.32, lead 11-09) The deep dive's role guard denies `git init` even in the scratchpad, while its role text says "probes in a scratch dir"; it used `go test -overlay` on tests/upgrade instead (a good probe). Decided 11-09 (owner): overlay route, named in templates/agents/deep-dive.md. Open: give tests/upgrade's alpha project every config key the new minor removed (would have caught DD-v0.32 #1).
- (lead 11-08) **Serve fires waiting work on the old binary before its self-restart after a merge:** `obor review --after T-1059,T-1060` fired at 19:35:16Z on 77df169's serve and hit the very bug T-1059 fixed; serve restarted on the new binary ~30 s later. A fix to serve's own code is live only after that restart, so an --after request or dispatch that tests the fix should fire after it (restart first, then fire). Small; decide when it bites again.
- (lead 11-07) **A task parked on a needs-you keeps its lane**: T-982 and T-983 (gate timeouts, not their diffs) held L-04 and L-01 while the fixes for the timeouts (T-989, T-990) waited for a lane. Since T-961 a merge-loop task holds its lane for its lane test; a parked one could release it (its tree is in its branch and candidate pin) and take a lane again when resumed. Decide with the next merge-flow change.
- (lead 11-07) **Flake, 3 sightings**: `TestWakePostRefusedThenReplayed` (internal/app) failed T-704's gate, a gate near serve.log:13435 and T-957's full re-gate 11-07 14:28 (0.7 s, passes on main in 0.8 s); each red sent a worker back. The rework's retest (plan §5 T-C) absorbs it; fix the test if it recurs after T-C.
- (lead 11-07) **Not a flake**: `TestJobNudgeFromDelivery/activity_at_8_min` red in T-956's gate was a rule bug: watchJob counted herdr 'not connected' as the integrator gone (against T-759); fixed in T-956 (jobs.go, joblost_test.go). Also: lead commits to main while candidates wait cost each a moved re-gate (12:40 BACKLOG commit moved T-950): bookkeeping only at a quiet merge queue.
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
