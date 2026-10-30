# Backlog

The lead appends; nobody else writes here. Triaged 2026-10-28 after v0.12.0 (DD-v0.12 C4): items about deleted
mechanisms (heartbeat, queue, `obor tell`, inbox, the mod, `obor watch`/`merge`/`ack`/`owner`, handoff, lanes state
files, bash, keep_warm, multi-lead) were closed by their deletion and removed; history is in git
(`git show 449b79e:BACKLOG.md`).

## Handoff (lead; rewrite at each handoff)
- 2026-10-30 ~16:10, session 11 → 12. **Renaming everything to obor-kit** (owner): the owner runs
  `~/Work/rename-to-obor-kit.sh` after this lead exits (serve and all agents stopped), then `./bin/obor lead` in
  `~/Work/obor-kit`: you start as **ob-lead**. The script: `obor setup rename --name obor-kit --prefix ob`, `lanes_dir`,
  folders moved (`~/Work/obor-kit`, `~/Work/obor-kit-wt`), worktrees repaired incl. axis-alies' `~/Work/herd-kit-stable`
  (a worktree of this repo!), Claude plugin + `obor.events` + opencode at the new path, Codex trust, this memory dir
  copied, serve started in w5:p1F.
- **Rename done** (session 12, 16:30): serve up, role agents ob-int (w5:pZ), ob-sol (w5:pA), ob-fable (w5:p1G);
  herd-kit-stable clean at v0.15.0; rename script deleted (owner). Closed tabs: old lead
  (w5:t1, `obor lead` found no `ob-lead` pane and opened a new tab), the c5probe shell, deep-dive (research done;
  `obor setup role deep_dive --tab w5:tH` when needed) and the stale hk-rev pane. Lane labels cleared by hand (see Open).
- **`foksa/obor-kit` created** (owner, 10-30 16:40, public, empty). Pre-publish scan of the 339 tracked files: no
  secrets, no `/Users/…` paths, no email; ~15 files name axis-alies or `~/Work/` (project names, kept). Push the
  one-commit export after T-590 (P7-1) merges, with the v0.16 tag; `make publish` then uploads the binaries.
- Done today: v0.15.0 tagged and on stable (axis upgraded); phase 7 planned (`docs/plan/phase-7.md`); P7-0a rename
  (T-573) + P7-0b `obor migrate` (T-574); usage-limit auto-continue (T-563..T-572); sessions recorded by obor (T-577,
  T-578, T-581..T-589); reviews RV-59..RV-62 closed (empty `RV-xx closed` commits); old docs deleted.
- **Next:** P7-1 (install script, `bin/release-url` → GitHub releases, `make release` uploads), then the public
  `foksa/obor-kit` from a **fresh one-commit history** (owner), tag v0.16, axis-alies migrates once (its lead, at a
  handoff: `obor migrate`; move its role worktrees to the new tag first). The lead may push (`guards.allow_push`).
- Codex weekly ~80% (10-30 10:30). Difficulty rule tightened (`docs/agents/lead.md`): wide or risky-to-deploy is
  medium, not hard.
- How to work: drafts `.obor/drafts/T-xx.md` (with `Difficulty:`) → `obor spec set` → `obor dispatch T-xx`, **then
  `obor watch` the wave** and check `obor status` after every brief (undelivered lines show there). After every code
  merge: `make build`, restart serve in w5:p1F (`timeout 20 herdr pane send-keys w5:p1F C-c`; `herdr pane run w5:p1F
  "./dist/obor serve"`). Reviews stay in the reviewer's worktree. Release: build the release branch in a scratch
  worktree, cherry-pick VERSION/UPGRADE to main.

## Owner decisions pending
- S5a: when a herdr restart is OK (resume spike).

## Later candidates
- (RV-63 #4, accepted by rule) `scripts/publish` checks the local tag only, not that it is the remote one: a moved
  pushed tag could upload bytes fresh installs reject. Rule instead of code: a pushed tag never moves (RELEASING.md).
- (RV-62, v0.17) Drop the `<agent>.sid` fallback (only for serve down) once the store is the session record: RV-62 #1
  took five rounds (T-582, T-586, T-588, T-589 after T-577), half of it in the fallback's ownership. Live process
  races beyond the pinned pid are accepted (unchecked live).
- **herdr-core (`hc`)** (`docs/research/herdr-core.md`, 10-30): a separate headless terminal core (own `herdr-core`/`hc`
  binary, own socket/state; no clash with `herdr`). Not now (0.1.x preview). Spike behind `app.PaneControl` when hc has a
  stable generation, herdr discussion #5684 is answered, and Claude/Codex/OpenCode all report OSC 7501. Until then: no
  new herdr-only shapes (tabs, layout, plugins) in `internal/app`; `HERDR_PANE_ID` read in one place.
- (RV-57-verify #2, #3, accepted) the load-run guard misses separately `bash -c`-wrapped sibling background tests and
  repeated `-P` options ending in 1 is still denied: constructed forms.
- (DD-v0.14 U3/U6/S4) The difficulty map's type names and the Go-only load-run rules are obor-kit's own policy built
  into the kit; simpler: `obor setup init` writes them into the project's config. Not before a second adopter.
- **A herdr restart takes every serve down** (10-30: obor-kit and axis-alies serves, axis' reviewer pane lost):
  phase 7 launchd. Who restarted herdr is unknown (S5a).
- `obor doctor`: hooks loaded + kit version per pane (stale panes hid guard bugs twice).
- Per-project kit pin (rollout lesson v0.6.0): moving `obor-kit-stable` upgrades every adopter's CLI at once while
  hooks follow per pane restart; a breaking release needs an opt-in pin or code that handles old state.

## Open
- (10-30, rename) **serve keeps a pane's old label**: after `herdr pane rename` + `obor setup role --pane`, serve
  identified the new agents by their old labels (hk-int at w5:pZ, label proof) until serve restarted. Label changes
  seem not to refresh serve's pane cache; any relabel (setup pane/role on a reused pane) hits it. Also ob-int's
  recorded session is still hk-int's old one (serve: "kept").
  **Lanes did not pick `ob-*` at dispatch** (T-587 claim): agent.start FAILed "pane w5:p1N is labelled hk-d-01, not
  ob-d-01" until the lead cleared all 8 lane labels by hand. `obor setup rename` should relabel (or clear) every
  project pane, and grep per-worktree `opencode.json` (D-01's named the deleted `~/Work/herd-kit/bin/herd`: "MCP
  server failed: herd"; fixed by hand). Matters only for the next rename (axis-alies' `obor migrate`?).
  **`obor setup role --pane` keeps the shell's old env**: ob-int/ob-sol/ob-fable started in their reused shells with
  `OBOR_AGENT=hk-*` still exported, so `job_next` refused ob-int ("only the integrator (ob-int) fetches its jobs,
  not hk-int"; T-590 review stuck → needs-you). `obor restart <agent> --fresh` sets it right (verified per pid).
  setup role should export the agent env the way restart does.
- (T-577) `obor plugin restore` could resume a Codex worker from its session in the store (today it starts fresh
  after a herdr restart; restore.go reads only `.sid`). Goal 1; not yet specced.
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
- Reviewer / deep_dive are read-only only by role text: the guard could deny their writes outside the reviews dir.
## Concerns
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
