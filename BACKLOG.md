# Backlog

The lead appends; nobody else writes here. Triaged 2026-10-28 after v0.12.0 (DD-v0.12 C4): items about deleted
mechanisms (heartbeat, queue, `obor tell`, inbox, the mod, `obor watch`/`merge`/`ack`/`owner`, handoff, lanes state
files, bash, keep_warm, multi-lead) were closed by their deletion and removed; history is in git
(`git show 449b79e:BACKLOG.md`).

## Handoff (lead; rewrite at each handoff)
- 2026-11-05 ~evening: third input docs/research/architecture-proposal-2026-11-05.md merged into the plan: its lane-read
  bug → T-871; context propagation → T-873 (F); ordered commit path = J; rule conversions/Of field = D/G/H; review
  close via serve = I; drive continuation = S1(a), still the owner's call. **Wave P after T-873** (fix error
  semantics before caching): P1 coalesce lane Git scans off the pane-event path (bounded refresh; reuse preflight
  stays authoritative; measure git calls per burst); P2 narrow view loaders for task/agent/setup reads (no git/ps
  for a task page; one config snapshot per pass); P3 measure then fix session ownership fold (O(A×S²)) and add only
  useful indexes (events(task,seq), prompts). P1/P2 should cut the machine load behind the 11-05 flakes.
- 2026-11-05 afternoon: DD-architecture + Fable's combined plan adopted (decisions, design "Change rules"). Running:
  T-864 (config keys, flags, + M3 load re-run), T-865 (lane rows), T-866 pilot A (rule pass + idle), T-867 (stop
  --force drops nothing). Next release notes must say: `merge.timeout_grep`, `integrator_handoff_tokens`,
  `stop_idle_workers`, `merge.max_load`/`load_wait`/`window_wait`, `slots.merge_block_max` removed (WARN two minors);
  `obor status --all` gone; `obor review verify` by hand gone (when C lands); `obor stop --force` keeps tasks open.
  After T-864 merges: docs/design.md "Machine load" drops the load-aware retry paragraph (lead's file).
- 2026-11-04 ~23:45 (end of day). **v0.26.0 tagged** (f1084988 on main; notes docs/releases/v0.26.0.md, guide
  docs/UPGRADE-v0.26.md). **Owner: `make ship`** (then each project: `obor config validate`, delete the keys it names,
  `obor serve restart`). Today: T-825..T-849 (CLI deadlines, P4 version on calls, S2 needs-you block, test pruning
  P1-P12 + restores, main-checkout guard, retired shape keys); RV-107..RV-112 closed. Nothing in flight, no watches.
  **Next (consolidation, each deletes a mechanism):** (1) review → fix → verify in one reviewer session, hardened in
  obor (Later); (2) integrator out of the main checkout (its own read-only worktree) instead of more guard deny rows;
  (3) move/drop/take-over answer at once and report the stop as a later line (ends the deadline patches, RV-108 #1);
  (4) judge the pruning wave's net (lines out vs 4 review rounds and 4 restore tasks) before any more pruning.
  **Owner to choose tomorrow — is simplification moving?** By size, no: prod Go 41.9k (v0.20.1) → 44.9k (v0.23.0,
  consolidation declared) → 43.8k (v0.25.0, low) → 45.5k (v0.26.0); tests 66.0k → 74.5k; pruning netted ~1k, not the
  survey's 6-8k (restores + new safety tables). The model got simpler (one resume verb, derived names, one phase,
  needs-you block), but each fix closes its gap by adding a mechanism. Experiments the lead proposed (one wave each,
  measured): **E1** no integrator review: merge on a green gate, batched terra review only; compare findings with
  today's waves (the integrator accepted every pruning task terra later found holes in) — if no worse, the role, its
  job type, held verdicts and part of the merge loop go (lead's pick to try first). **11-05: dropped** — the lead's
  premise was wrong: the integrator sent back T-831/T-834/T-838 itself and ~8% of merges since 10-25. **E2** reviewer and integrator in
  their own read-only worktree: if nothing breaks, the main-checkout deny rows (T-841, T-848) go. **E3** move, drop and
  take-over answer at once, the stop is a later line: then the deadline code (T-825, T-829, RV-108 #1) goes. **E4** a
  feature freeze for v0.27: each task names what it removes, no new config keys.
  **Owner 11-04: flow simplifications are worth more than code ones.** Rank by what leaves the lead's/owner's loop:
  E1 (one review step fewer per merge) and E3 (no false failures) first; also count today's flow friction: a fresh
  review per fix round (6 rounds), watches set by hand per wave, review notes written per batch, a needs-you recovery
  by hand (detached HEAD), idle lines to answer. Measure lead actions per merged task before and after.
- 2026-11-04 ~22:30: pruning wave merged except T-844/T-845 (restores); T-841 gating, T-846 (P12 v0.26 removals) on
  L-04; review queued after T-841, T-844, T-845, T-846 (covers T-838). **Then v0.26**: UPGRADE-v0.26 + release notes
  must say: store older than v0.22 upgrades through v0.24 first (T-840); remove a global opencode `mcp.servers.obor` by
  hand if an obor before v0.25 put it there (T-835); `obor monitor` and the pre-v0.23 config message are gone (T-846);
  removed config keys past their window are now unknown keys (T-830); every wire call carries the caller's version
  (T-826); only the lead moves HEAD in the main checkout (T-841); S2 needs-you as a block (T-827, internal).
- 2026-11-04 ~21:45: RV-109 closed (6 lost scenarios in T-833/T-834/T-836 → restores T-844, T-845; RV-108's → T-842,
  T-843). **Concern:** statement-coverage pruning keeps losing scenarios (entry point, timing) on deepseek; owner moved
  T-838 to sonnet:medium (attempt 2, same lane). Judge the wave's net when it ends (lines out vs restore rounds).
- 2026-11-04 ~20:55: ob-int detached the main checkout (`git checkout --detach <candidate>` while reviewing T-830);
  loop needs-you cleared by `git checkout main`. Guard fix T-841 queued after T-837 (guard tests). Until it lands the
  same can recur during the pruning reviews: the fix is `git checkout -q main` in the main checkout (clean tree).
- 2026-11-04 ~20:30: RV-107 closed (#1 → T-829 stop bound, L-01). T-828 survey merged
  (docs/research/test-pruning.md); pruning P1-P10 = T-830..T-839 dispatched (T-830..T-832 running, the rest queue for
  lanes; T-836, T-838 after T-829). P11 = T-840 (owner: raise to 9); retire step deletion folded into T-835 (owner); v0.26 notes must say both (store < v0.22: upgrade through v0.24; remove a global opencode `mcp.servers.obor` by hand). Held:
  **P12** v0.26 removals (`obor monitor` case, pre-v0.23 shape message) after T-830/T-831, at the v0.26 release;
  `TestSetupAgentsRetires…` + `agents.Retired` once the owner says no machine has the global opencode entry.
- 2026-11-04 ~19:40 (lead, fresh session). Steps 1-2 done (AGENTS.md rules, 5560ff43). Step 3 was misread: the
  11-04 15:02 `use of closed network connection` was a move outlasting the CLI's 15 s deadline (serve stops the
  worker inside the call), not a restart → T-825. In flight: T-825 (L-01), T-827 S2 (L-02, opus:high), T-828
  test-pruning survey (L-03); T-826 P4 waits on T-825 (same file). Owner: S2 for wave 4, survey first, keep 4 lanes.
  Next: review T-825..T-827 once merged; pruning tasks from T-828's ranked plan.
- 2026-11-04 ~19:00 (lead → fresh session). **v0.25.0 and v0.25.1 shipped** today; all upgrade steps done on this
  machine (`obor setup agents` removed the global opencode obor entry + link; backup was in the old session's
  scratchpad; `~/.obor/agents` and `~/.obor/inbox` removed). axis-alies on 0.25.1 (lanes restarted, its lead restarts
  for the skill); axis-maps stopped by the owner (`obor start` there brings 0.25.1). Reviews RV-102..RV-106 closed.
  Nothing in flight, no watches, no owner decisions pending. crouton removed from config (its stale pause is invisible).
  Conformance: `make conformance TIER=live TURN=1` (CONF_OPENCODE_MODEL=opencode-go/deepseek-v4.1-flash) in tab
  `conformance` (w5:t15): 34 pass, CC-L2 fail fixed by T-820.
  **Next, in order (owner OK'd 11-04):** (1) AGENTS.md rule "an empty derived value is an error" (T-791, T-814 class);
  (2) spec rule + template line: a task touching identity/MCP/launch runs `make conformance TIER=live` per kind before
  merge; (3) CLI retry on serve's closed socket during its restart; (4) wave 4: DD-consolidation S1 (one "may the lead
  act" check, hard, case → test table) + test pruning (tests 71.6k vs prod 43.9k); (5) P4 version on every wire call.
  Later: P9 herdr restore plugin; trim the conformance suite (~3.8k lines: settled probes become assertions).
  **Concern:** machine load (load avg up to 37) turned gates red several times today (flakes in internal/app,
  cmd/obor, internal/setup); owner to choose 3 lanes or keep fixing flakes. Owner-side optional: herdr 0.9.5 +
  `herdr integration install opencode`; restart the owner's own opencode service (pid 2883, old obor mcp children).
- 2026-11-04 ~11:15 (lead restart: T-787 moved inbox records into the project; this session had none, so other leads
  and serve could not reach it). **Wave 3 of DD-consolidation merged** on main (unreleased): T-785 brief --fresh, restart
  flags gone; T-786 derived identity, no OBOR_AGENT, allow_push roles; T-787 project inbox by slot, rename refuses old
  plist; T-788 one task phase, held = verdict; T-789 setup migrate deleted (codex projects migrated by owner 11-04);
  T-790 spec titles from `T-xx: title` (+ spec set refuses untitled). **Also merged**: T-791 (no-identity refusal + per-kind
  tests; the opencode failure was kit skew: global opencode MCP runs ~/.obor/kit 0.24.2), T-792 (dispatch refuses a
  merged task). Nothing in flight; the watches fired while this lead had no inbox. **Paused**: deepseek, crouton in obor-kit until v0.25 is installed
  (clear both after the install). Config has `models.crouton` (owner trial at xhigh, after v0.25). **Researcher** (tab research)
  writes docs/research/agent-cli-and-herdr-capabilities.md; then the agent-driver deep dive (Open, first line).
  **Next**: obor review T-785..T-792, close; v0.25 notes (restart flags gone,
  OBOR_AGENT gone, inbox per project: `rm -r ~/.obor/agents ~/.obor/inbox` + restart every role agent and lead,
  setup migrate gone, titles); `make release`; owner `make ship`; tell axis leads. Until v0.25 is everywhere, a 0.24.2
  `obor send obor-kit` may not reach this lead (it reads the global inbox). Owner feedback to act on after the review:
  AGENTS.md rule "an empty derived value is an error", spec rule live smoke per agent kind for identity/mcp/launch,
  S1 + test pruning as wave 4, CLI retry on closed serve socket.
- 2026-11-04: **v0.24.2 shipped** (v0.24.0 and v0.24.1 tagged, never shipped on their own; notes carry all three). v0.24.0: consolidation waves 1-2 (T-772..T-780: one `obor resume`, task ids, move a task, monitor gone, no `agents` key, derived names, upgrade check in make release; RV-98/RV-99). v0.24.1: nightly is the lead's switch, `obor nightly on|off` (T-781/T-782, RV-100; owner: no "due" logic). v0.24.2: dispatch uses the lane's labelled pane (T-783/T-784, RV-101; axis-alies' report). Axis leads sent the v0.24 steps (axis-alies: setup migrate for its agents key; axis-maps: close axis-monitor). Next: confirm both serves on 0.24.2 and migrated; wave 3 of DD-consolidation (C3+C4, I3+I4, S5+S6, C7 wording, I6+I7); C8 (delete migrate code) in v0.25 after codex-image-tool/codex-img-viewer migrate.
- 2026-11-03 evening: **v0.23.1 shipped** (T-770 strict MCP for Claude workers, T-771 serve restart; RV-97 closed); before it **v0.23.0 shipped** (axis leads sent the migrate steps; their serves refuse the old config until they migrate): config names models directly (T-765), Setup
  page for it (T-766), project list keeps unloadable projects (T-767), roles always name a model (T-769 replaced
  T-768 after two strikes). RV-95, RV-96 closed. After ship: obor send axis-alies/axis-maps the UPGRADE-v0.23 steps
  (migrate needs every lane free); codex-image-tool / codex-img-viewer are dormant obor projects needing migrate.
- 2026-11-03 midday: **v0.22.0 shipped** (leads told): obor web redesign (owner drive T-760, Svelte 5 + Vite, dist committed;
  decision in docs/decisions.md), drive<->lead lines (T-761, T-764), drive commits (T-762), live mark (T-763). RV-94
  closed. Nothing in flight.
- 2026-11-03 morning: **v0.21.0 shipped** (leads told) (Setup page + editing T-750/T-752..T-754/T-756, obor drive/land
  T-751/T-755/T-757/T-758, test flakes T-759 released UNREVIEWED with the owner's OK). RV-92, RV-93 closed (accepted
  lines in Open). Owner will redesign the Setup page (DeepSeek's UI looked ugly: UI specs now
  carry `Type: opus`); suggest `obor drive`. Reviewer stays terra:xhigh (owner: axis' Sol use drove Codex usage).
  Nothing in flight.
- 2026-11-03: **v0.20.1 shipped** (v0.20.0 never shipped on its own; v0.20.1 notes carry both; T-748/T-749 answer
  delivery states, RV-91 closed). Ship told axis-alies and axis-maps; axis-maps' serve is on v0.20.1 and its stale
  deep-dive brief is gone. Nothing in flight, no open owner decisions.
- 2026-11-02 night: **v0.20.0 tagged** (lane pool, binaries followed by serve/monitor/web, reviews in the main checkout
  with the merge hold, answer delivery to the live session). RV-86..RV-90 closed (accepted lines in Open). obor-kit
  now runs untyped lanes L-01..L-04 (old O/S/C/D lanes and their worktrees removed; tab "lanes"). `max` caps worker
  tasks only (owner). Medium/simple specs carry no `Type: sonnet` (owner: DeepSeek first; Claude quota shared with
  axis). **Next:** owner runs `make ship`; then check axis-maps' stale deep-dive brief cleared on v0.20 (T-732) and
  tell axis-alies their delivery fix is in v0.20.0, the rest in v0.20.1. No open owner decisions.
- 2026-11-02 late (lead context 500k → fresh session): **v0.19.1 shipped** earlier; since then merged and reviewed
  (RV-85 closed, `obor review close`'s first use): T-712 stop clears input, T-713 role agents on the web overview +
  monitor config reload, T-714 agents' long-running processes, T-715 ship notifies leads, T-716 `obor review
  verify|close` + brief reports real findings only, T-720 checks wait while load > 2×cores, T-725..T-727 RV-85 fixes.
  Merged, not yet reviewed: T-717 (release log + flaky rerun). **Merged**: T-718 (merge hold while reviewer/deep dive work; both in the main checkout). **In flight**: T-728 on S-01 (bg label only for the call that started
  it, RV-85 #3 remainder). **Queued**: T-719 (self re-exec of serve/monitor/web, Fable advice) after T-718; the lane
  pool T-721 → T-722 → T-723 → T-724 (docs/research/untyped-lanes-advice-fable.md) after T-719. Watches carry the
  next steps: T-718 done: review and deep-dive worktrees removed (their old files kept in docs/reviews/), ob-terra
  and ob-deep-dive panes re-placed in the main checkout; release notes must tell other leads they may remove theirs; review T-717/T-718/T-719/T-728 together
  (`obor review`, then `obor review verify`, `obor review close`; at most two rounds); after the pool: obor-kit lanes
  become L-01..L-04 untyped (no pinned lane; Codex only for reviews), remove the 8 old lane worktrees after checking
  each is clean and merged, then v0.20.0 (UPGRADE-v0.20 from T-724, releases/v0.20.0.md, `make release`, owner
  `make ship`). Reviewer is `terra:xhigh` (Codex: review ~3 %, verify 2-4 % of the 5 h window). No open owner
  decisions. Owner asked for simplification proposals any time; ideas logged: worker re-gate deadlock (Open).
- **Reviewer type**: `opus:high` since 2026-11-02 (Codex usage limit, resets 2026-11-04 08:00). After the reset set
  `roles.reviewer.type` to `terra:xhigh` (owner: wants to see how Terra's usage moves at xhigh) and report the Codex
  rate_limits from the newest rollout before and after its first review.
- 2026-11-02 evening: **v0.19.1 shipped** (8dd1eef7): obor stop/start, no agents at init/setup, init plugin in lanes,
  opencode model via obor.js, researcher role, role write limits on shell+git (T-702..T-711; RV-83, RV-84 closed). Serves
  restarted in obor-kit, axis-alies, axis-maps, codex-image-tool; their leads told. codex-img-viewer: the owner runs
  `obor stop` there (their own session). Reviewer is terra:xhigh (Codex usage: review ~3 %, verify 2-4 % of the 5 h
  window; the window is account-wide).
- 2026-11-02 day: **v0.18.1 shipped** (Go 1.27.3, release notes; T-681..T-684). Since then merged and reviewed
  (RV-79..RV-81 closed, d7e54704): T-685/T-686 (gate 158 → 115 s), T-687, T-688 (monitor bad flag), T-689 (spec
  `Type:` line), T-695 (`type:effort` everywhere; `opus-high` type replaced by `opus:high`), T-696..T-698 (opencode
  per-agent XDG_STATE_HOME, pinned model, no model guessing), T-690..T-694 + T-699/T-700 (internal/app split into
  port/event/wire, needs-you owner, mtask attempt group, aliases gone). Research: Gemini review + Fable advice +
  app-split deep dive in docs/research. **v0.19.0 shipped** (401a4b0d; T-701 fixed the affected test on the post-split graph). obor-kit and axis-alies serves
  restarted on it, axis-alies lead told, monitors restarted. axis-maps: deepseek type pinned to `opencode-go/deepseek-v4.1-flash` (owner OK; its commit 65d818a), serve
  restarted, lead told, monitor restarted. In flight:
  T-702 (internal/app parallel-test flake sweep, S-01). Earlier next was: owner's call on v0.19.0 (write
  docs/UPGRADE-v0.19.md + docs/releases/v0.19.0.md; projects need serve restart; lead/deep-dive role type is now
  `opus:high`); open: herdr ~100 % CPU cause unknown; deep dive steps 6-7 not now.
- 2026-11-02 early: **v0.18.0 tagged** (10a24b7c; monitoring release: `internal/view`, `obor web`, prompts table,
  new monitor, watch --note, role agents stop when done, deduped job prompts). RV-75 and RV-76 closed. Codex smoke
  T-679 passed. Nothing in flight, no watches; ob-int and ob-terra stopped by serve, ob-fable and ob-deep-dive by the
  lead. `obor web` runs in pane `ob-web` (system tab, http://127.0.0.1:8787/).
  **Shipped** 2026-11-02 07:05 (GitHub release v0.18.0, kit installed); all three serves restarted on it, both axis
  monitors restarted on 0.18.0, both axis leads sent the summary (axis-maps: queued, its lead has no recorded
  session; delivered once `obor restart axis-lead` or `obor lead` there).
  Later (owner): `obor drive`/`obor land` (Later candidates). Owner ended the day after this release.
  After the tag: nightly RED on govulncheck (five go1.27.1 net/http server vulns, reachable via `obor web`;
  localhost plain HTTP, so no real risk) → T-681 `toolchain go1.27.3` in go.mod, merged f19aedc, nightly GREEN.
  It ships in v0.18.1 or the next release (owner's call). T-682 merged c89eb04: releases get title + notes from
  `docs/releases/v<x.y.z>.md` (the lead writes it with the UPGRADE guide; release/ship refuse without it). GitHub
  releases v0.16.0..v0.18.0 backfilled by hand. T-681 + T-682 unreviewed since RV-76: review before v0.18.1.
  RV-77 (T-681..T-683) closed 4ee67cfe. `make release V=0.18.1` failed twice: internal/app hangs under full-check
  load (fix job waits for a stopped integrator; recovery cases with `stop.<int>` unsettled; likely T-672) → T-684
  (O-01, hard). After it: short verify, then release (log to a file: a tail loses the failing test).
- 2026-11-01 evening: **v0.17.5 released** (v0.17.3/v0.17.4 tagged, never published); all three serves on it;
  `obor monitor` panes in each project's system tab. Lead on opus-high (config; applies at the next lead restart).
  In flight: T-662 (running vs pending), T-663 (monitor fits rows), both RV-74 display fixes, unreviewed.
  Planner/coordinator split deferred: cheap fixes in (T-649..T-652); measure what still reaches the lead's chat.
  Plan + Fable advice: docs/research/planner-coordinator-*.md; the owner may add Gemini's read.
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
  from main): fixed by `git -C <lane> checkout -q -B <branch> main`. After a code merge serve runs `make build`
  and restarts itself on the new build (T-652, `commands.after_merge.restart_serve`; serve runs under launchd since 10-31,
  `obor.serve.obor-kit`; log `.obor/state/logs/serve.log`); a failed restart is a needs-you.
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
- **Harden review → fix → verify in obor** (owner 11-04, after the lead closed RV-107..RV-111 right after dispatching
  fixes, so each fix round was a fresh review without the findings' context). Options: `obor review close` refuses
  while a fix task it names is open or merged but unverified; or `obor review fix T-a,T-b` links fix tasks to the open
  review and serve briefs the same reviewer session to verify once they merge, closing only on the verify. Also the
  skill/review-brief §3 wording: close after the verify, not after the dispatch.
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
- (RV-118, accepted) (RV-118, accepted) idle's key/time ignore the record that clears a non-rule needs-you (e.g. the disk watcher's below-5-GiB needs-you, internal/app/disk.go:38-67; its resumed fact is not an idle end candidate, idle.go:138-145): after such a needs-you clears, the old key, age and acknowledgement stay and no new idle line comes. Rare (disk < 5 GiB); fix 25-50 lines (count the clearing record when it empties the pipeline)
- (RV-114, accepted) (RV-114, accepted) the idle settle window (60 s, T-859) is time-based: a fix pass that cannot record its verify request for more than a minute still yields one false idle ping; derive the owed verify from round state if long delays become routine
- (RV-113, accepted) (RV-113, accepted) 4b: an open review started by an obor older than the review.started fact closes at an unfinished manual verify's head (refs/obor/reviewed moves at brief time, reviewverify.go:519; reviewcycle.go:233 falls back to it); needs such an old open review + an unlinked manual verify that fails or is superseded; none exists; fix 25-55 lines (move the fallback ref only on a finished verify)
- (11-05) The idle line fired at T-856's merge although `obor review fix` was about to brief the verify (ob-rev working
  seconds later; `obor idle` then said NOT IDLE): idle should count linked fixes that all merged and a verify not yet
  briefed. A false ping per fix round; fix small (idle.go reads the review-fix state).
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
- (lead 11-04) **v0.25.1 opencode step order**: do not run `obor setup agents` (removes the global opencode MCP entry + plugin link, T-809) until every project on this machine runs v0.25.1: a 0.25.0 serve's opencode lanes still need the global entry. Until then the global plugin copy wins over the per-launch one (OC-L4) in obor-kit too, as before. UPGRADE-v0.25 note must say: install, restart serves everywhere, then `obor setup agents` once.
- (RV-103, accepted) conformance live runner uses fixed session names (obor-conf-<kind>) and stops a pre-existing one: two runs at once collide; unlikely (lead/owner run it by hand), fix: a per-run suffix if it ever bites.
- (lead 11-04) **Flake**: `TestHoldAndResume/resume_reprompts` (internal/app/jobs_test.go:394) failed under full-check load in T-804's gate ("resume T-1: T-1 has review.2 running; retry when it settles", 5.1 s), passes alone; T-804 touches no internal/app. Fix if it recurs: wait for the review step to settle instead of a fixed deadline.
- (lead 11-04) **crouton trial ended** (owner: not worth it; T-806 at xhigh stalled twice, needed a manual continue; moved to sonnet). crouton paused; `models.crouton` removed from config (owner).
- (RV-102, accepted) #2 remainder: setup rename interrupted between config.json and local.json, then rerun, leaves a legacy derived allow_push entry (e.g. ob-lead) unconverted; unlikely (an interrupted rename with the entry in local.json only), fix ~20-40 lines (normalize both files before any prefix write); fix by hand: write the role name (lead).
- (RV-102, accepted) T-793: a lead's git -C <lane> push passes the shell guard but the pre-push hook (run in the lane) classifies the caller as the lane's worker and refuses; unlikely (leads push main from the main checkout), fails closed, fix 20-50 lines (one identity contract for guard and hook).
- (lead 11-04) **Agent-driver deep dive, planned (owner):** ob-researcher writes
  docs/research/agent-cli-and-herdr-capabilities.md (Claude Code 2.1.318, codex 0.166.0, opencode 2.2.0, herdr 0.9.3:
  facts with sources). When it says done: commit it, `obor restart ob-researcher --stop`; once T-791/T-792 merged,
  `obor brief ob-deep-dive --fresh`: how internal/agents drivers and herdr calls use them vs that doc (identity channel
  per kind, MCP/plugin registration is user-global → kit version skew, T-791 hold (b): per-launch registration;
  herdr misuse history), proposals ranked, then a conformance suite per driver.
- (lead 11-04) T-787 moved inbox records into the project: lines to ob-lead are undelivered until the lead restarts
  (`obor restart ob-lead --fresh` at a break). v0.25 notes: restart every role agent.
- (DD-consolidation I6, lead 11-04) one relabel implementation: not a task; migrate's relabel goes with `setup
  migrate` in v0.25 (C8), leaving rename's alone. Wave 3 = T-785 (C3+C4), T-786 (I3), T-787 (I4+I7, after T-786),
  T-788 (S5+S6+C7), T-789 (C8, after T-786; codex projects migrated 11-04).
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
- (RV-90, accepted) #1 a pre-byte refused delivery leaves post set, so a settled answer is not retired and is sent later: fix in T-748 (v0.20.1)
- (RV-90, accepted) #2 a refused inbox post keeps no reason (status Held empty) and never falls back to typing for an idle worker: fix in T-748 (v0.20.1)
- (RV-90, accepted) #3 obor drop does not retire the task's undelivered answer; the lane's worker gets it later: fix in T-748 (v0.20.1)
- (RV-90, accepted) obor drop fails while the lane's agent still runs (git checkout --detach exit 128, the FAIL text does not say why); stop the agent first. Seen 2026-11-02 dropping smoke T-743
- (RV-89, accepted) #1 rest: a successful empty pane re-read followed by serve shutdown before the step applies it records needs-you{no pane in lane}: unlikely (shutdown in that interval); fix 3-8 lines (ctx.Err() check before the fact)
- (RV-89, accepted) #2 rest: an older re-read can overwrite the lane pane's cwd from a newer stream event during the read: unlikely; fix 20-40 lines (per-pane observation generation)
- (RV-88, accepted) #4/B redo of a pooled task with an ordered --types list (opus,sonnet) is refused when the first type is at max instead of trying the next: unlikely (lead redo with a list at cap), fails safe; fix 10-25 lines, share pickLane's ordered choice
- (RV-88, accepted) #3/A a legacy (pre-v0.20) lane blanked while its task is still open loses the task's type (cap count 0): documented in UPGRADE-v0.20 step 1; durable snapshot 30-70 lines, not worth it for a one-time migration
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
