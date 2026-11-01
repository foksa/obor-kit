# Backlog

The lead appends; nobody else writes here. Triaged 2026-10-28 after v0.12.0 (DD-v0.12 C4): items about deleted
mechanisms (heartbeat, queue, `obor tell`, inbox, the mod, `obor watch`/`merge`/`ack`/`owner`, handoff, lanes state
files, bash, keep_warm, multi-lead) were closed by their deletion and removed; history is in git
(`git show 449b79e:BACKLOG.md`).

## Handoff (lead; rewrite at each handoff)
- 2026-10-31 session 13, evening: **v0.17.0 released** (tag pushed, GitHub release published, `~/.obor/kit` at
  v0.17.0). Merged today T-595..T-626; RV-64..66 closed; DD-v0.17 + verify approved (V2 → T-626, V1/V3 docs done).
  axis-alies migrated (54543f08) and was told to follow UPGRADE-v0.17 (githooks, serve install, restarts). Nothing
  in flight; all lanes free. Config since 10-31: simple = deepseek, sonnet; slots max 1, cpus 4. Lead rule: never
  end a turn with work in flight unless the next task is `--after`-queued or the wave is under `obor watch`.
  Next: owner picks the next milestone; candidates: `obor plugin restore` resumes a Codex worker (check first),
  S5a done 21:18 (herdr restart: lead/int/deep-dive/Codex terra resumed; never-prompted sessions failed → T-627).
- 2026-10-30 ~17:45, session 12 → 13 (ob-lead, pane w5:p1R in tab `lead`). **All limits near their end** (owner;
  Codex weekly 10% left at 17:40): dispatches paused (`obor pause`, clear it first). Nothing in flight; all 8 lanes
  free; ob-int, ob-sol, ob-fable idle in the `roles` tab; serve up in w5:p1F.
- **Done this session:** rename verified (old tabs closed; role agents restarted with their `ob-*` env; lane labels
  cleared). **v0.16.0 released**: P7-1 (T-590, T-591, RV-63 closed, #4 by rule: a pushed tag never moves); main
  re-rooted onto a fresh public history (old history: local tag `pre-public`), pushed to `foksa/obor-kit`, release
  published by the owner, `install.sh` checked in a clean HOME; README added; dev marketplace `obor-kit-dev`
  (T-592, re-registered here); Codex smoke T-593 passed (after the tag, see `docs/agents/lead.md` Releases).
- **How to work now:** push with `OBOR_ALLOW_PUSH=1 git push` (pre-push hook); main is public: every commit is
  published, author email is the noreply. After the re-root, old `task/*` lane branches looked `done` (not reachable
  from main): fixed by `git -C <lane> checkout -q -B <branch> main`. After every code merge: `make build`, then
  `./bin/obor serve restart` (serve runs under launchd since 10-31, `obor.serve.obor-kit`; log `.obor/state/logs/serve.log`).
- **Owner decisions pending:** none.
- **Next (owner 10-31: start from the architectural review**, `~/Work/obor-kit-wt/review/docs/reviews/architectural-review.md`): (1) #1 labels/env/rename relabel (one fix task: `herdr/panes.go`,
  `app/panes.go`, `setup/agent.go`, `setup/rename.go`); (2) #2 drop `.sid` (store only; restore reads the store) in
  parallel with P7-3 (`obor serve install`); (3) #4 split `proj` in three hard, sequential, reviewed steps:
  registry, outbox, folded vs live state (existing app tests are the oracle; two strikes = rethink); (4) #3 boundary
  cleanup with whichever task touches `setup/agent.go`; #5 stays low. axis-alies migrates at its own handoff
  (`~/.obor/kit` is installed on this machine since 10-31). Unreviewed merges: T-592, T-594 (review due).
- Difficulty rule (`docs/agents/lead.md`): wide or risky-to-deploy is medium, not hard.

## Owner decisions pending

## Later candidates
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
