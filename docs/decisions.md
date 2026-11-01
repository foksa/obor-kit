# Decisions

Owner decisions on record, newest sections first. Folded from STATUS.md on 2026-10-27 (P4-0); append new ones
at the top of "Decisions on record". Phase plans: `docs/plan/`.

## Decisions on record
- 2026-11-01 (owner): **Monitoring is the next need** ("the kit is in great part feature complete, but I need
  monitoring: not only what is going on but also what is queued"); the 10-24 web-UI deferral is lifted. One read
  model `internal/view` feeds `obor status --json`, the monitor and `obor web` (read-only, localhost, stdlib, all
  projects); serve records prompt texts (`prompts` table); the monitor stays plain ANSI with a modern look ("like what
  we have in agents"), no Bubble Tea; `obor show` and herdr sidebar metadata not now. Wave T-664..T-668; advice
  docs/research/monitor-ui-advice-fable.md.
- 2026-11-01 (owner): **T-604 reversed**: a report's open questions go to the integrator's review job again; it
  decides in-goal ones and holds the rest for the lead (T-649). The lead-before-review hold sent 7 questions to the
  lead in a day, 5 answered "keep as built"; its token saving was not real. Planner/coordinator split: not now; the
  cheap fixes first (T-649..T-652, docs/research/planner-coordinator-advice-fable.md), then measure.
- 2026-10-31 (owner): LICENSE is MIT.
- (owner 2026-10-31, architectural review Part 3) **Token cuts:** the integrator starts fresh per job, still Opus,
  and its review job carries the diff when it is ≤ 15 KB (T-602); workers get their role text inside the job and do not
  repeat the report after `task_done` (T-603); a report with open questions goes to the lead before the integrator's
  review (T-604). Not now: auto-accepting simple tasks, per-task token accounting, merging main on sendback.
- (owner 2026-10-31) **Structure debt is worth fixing without a bug behind it**: a big shared struct "makes every new
  feature harder to navigate, more error prone, and more token intensive". The architectural review's #4 (split
  `internal/app`'s `proj`: registry, outbox, folded vs live state) is in, as behaviour-preserving hard steps.
- (owner 2026-10-30) **Public history: re-root local main.** v0.16.0 is one orphan commit holding main's tree; local
  `main` moves onto it and is pushed to `foksa/obor-kit`; every later commit pushes normally. The old history stays
  local under tag `pre-public` (`git log pre-public` for history and blame before v0.16). Commits use the GitHub
  noreply email (`git config user.email` in this repo, set 10-30), not the personal one.
- (owner 2026-10-30) **Rename everything to obor-kit** ("it is not herd kit anymore"): folder `~/Work/obor-kit` (lanes `~/Work/obor-kit-wt`), project name `obor-kit`, prefix `ob` (`ob-lead`, `ob-int`, `ob-sol`, `ob-fable`, `ob-o-01` …). By hand, everything stopped, after the RV-62 fixes (T-582..T-584) are merged and verified; the public fresh-history `foksa/obor-kit` is cut right after.
- (owner 2026-10-30) **Publishing obor-kit:** public `foksa/obor-kit` starts from a fresh one-commit history of the tree at v0.16 (the full history, with every deleted review and research note, stays local only). The herd-kit lead may push (`guards.allow_push: [herd-lead]`).
- (owner 2026-10-30) **No old docs in the tree** (before the repo goes public): reviews stay in the reviewer's worktree and close with an empty `RV-xx closed · <sha>` commit; `docs/reviews/` (INDEX, AR-01), `docs/research/`, done phase plans and old UPGRADE guides deleted. Kept: BACKLOG, contracts (design, state-model, module-map), `docs/agents/`, the current phase plan, the newest UPGRADE guide, this file. Git keeps the rest.
- (owner 2026-10-30) **Phase 7 plan accepted** (`docs/plan/phase-7.md`, advice whole): names `obor`/`obor-kit`/`.obor`/`OBOR_*`; public GitHub repo + Releases (docs go public with it); install `~/.obor/kit` + `~/.local/bin/obor`; launchd per project; no kit pin, `herd.version` deleted; v0.16 rename+migrate+install, v0.17 init+launchd+cleanup.
- (owner 2026-10-30) **herd is renamed obor** (Serbian: a corral/pen; `herd` is one letter from `herdr`): command, plugin, `.herd/`, `HERD_*`, skills; first task of phase 7, before publishing. No npm, Homebrew or GitHub CLI by that name (checked 10-30).
- (owner 2026-10-30) **Phase 7 = easy onboarding + a serve that outlives herdr** (`docs/plan/phase-7-draft.md`): install herd, `herd init` (no questions; installs the plugin itself), `herd lead`; serve as a launchd service. axis-maps is the first project set up this way.
- (owner 2026-10-30) **Codex starts skip its update menu** (T-570): herd's Codex bridge adds `-c check_for_update_on_startup=false` to every start; the owner's own Codex sessions keep the check.
- (owner 2026-10-30) **Stopped agents continue by themselves** (T-568 workers, T-569 role agents): serve wakes an agent stopped on a usage limit after the reset; still stopped 30 min after the wake → an alarm.
- (owner 2026-10-29) **`herd lead` starts fresh by default** (T-553); `--resume` continues the last session.
- (owner 2026-10-29) **Dispatch by difficulty** (T-538): each spec carries `Difficulty: hard|medium|simple`; config maps levels to lane types (default hard: opus; medium: sonnet, deepseek; simple: deepseek, luna); herd picks the first free lane, else the task waits (never steps up); Haiku only via `--types`.
- (owner 2026-10-28, AR-01) **Fewer documents.** Specs go into the merge commit's message instead of
  `docs/tasks/` (C4; reverses phase 3's file, keeps its reasons: the accept binds to the reviewed rev, git shows the
  spec beside its merge); `docs/tasks/` is deleted once that ships. A review's files leave the tree when its verify
  approves; `docs/reviews/INDEX.md` keeps one line. Unread docs (13 research notes, UPGRADE v0.6–v0.11, adoption
  plan, BRIEF.md, done phase plans 3–4, the daily-plan trial) deleted; git keeps them. One serve per project (S2, T-522;
  reverses T-415): socket in the project, `herd send` dials it, old kit route kept as a fallback until v0.14.
- (owner 2026-10-28) **v0.13.0 tagged** (phase 5 complete).
- (owner 2026-10-28) **No v0.12.1**: T-497's fix sits on the post-split code (RV-52-verify backport check); axis-alies
  gets T-497, T-502, T-503 with **v0.13.0** at the end of phase 5. **Quota line:** the owner does not need it; add it for the lead in phase 6 with context % (same source:
  Claude Code's status-line JSON).
- (owner 2026-10-28) **The per-file guard goes** (T-496): no "Files you may touch" enforcement; worktree boundary, git
  denies, the headless ban and protected files stay (a spec unlocks a protected file by exact path). Settled in the
  rebuild plan 2026-10-24 but kept by phase 3 without asking; owner: "the most toxic feature we had".
- (owner 2026-10-28) **Phase 5 = split, agent bridges and cleanup before the UI** (`docs/plan/phase-5.md`): one
  `agents.Kind` interface, a bridge per kind (Claude, Codex, opencode; Cursor dropped, re-added later as the
  architecture test); identity in serve; nothing typed into Claude panes (briefs, lines and job wakes over the inbox).
  UI inside herdr = phase 6; cutover = phase 7.
- (owner 2026-10-28) **Nothing types into the pane the owner writes in** (the lead's). Phase 4 broke this: the herd
  mod that knew the owner's draft went (T-466) and serve's lines to the lead were typed instead; the reason the mod
  existed lived only in BACKLOG Concerns. An idle lead must still be woken when it needs to know something; screen
  reading (T-484) dropped. Merge status: only when the owner asks.
- (owner 2026-10-28) **Parallel work:** fix waves are split by files, not by source (lead skill "Split by files");
  a dispatch reply names lanes still idle (T-483). Splitting `internal/app` into smaller files: phase 5.
- (owner 2026-10-28) **Brief completion stays observation-based** (herdr status: worked, then idle): it works for
  every agent kind; a Stop hook would cover Claude only, so no `herd done` and no hook path beside it. Panes are found
  by herdr name, else label (T-480).
- (owner 2026-10-28) **`herd lead` is back** (T-479): `herd lead [--fresh]`, resumes by default.
- (lead 2026-10-28, owner did not object) **Removed config keys** WARN for two minor versions, then are unknown keys.
- (owner 2026-10-27 evening) **Phase 4 directions** (`docs/plan/phase-4.md`, end): CLI, no lead MCP tools; queue →
  `herd dispatch --after`; `herd tell` replaced by serve-delivered `herd send` (lead↔lead) in phase 4; the lead is told
  when a role pane finishes; no lead work overnight; STATUS.md folded here and into BACKLOG.md; headless ban in the
  guard; auto-memory for the lead pane only; `herd pause` kept as a serve request; quota line not approved.
- (user 2026-10-27 ~12:40) **Phase 3 task list approved** (docs/plan/phase-3.md, T-433..T-441) with DeepSeek instead of Sonnet for P3-0/P3-2a/P3-6 and reminder rows kept in the tasks table until phase 4; done file goes entirely. **Advisor runs in auto mode** (was plan mode) and writes its own answer file.
- (user 2026-10-27 ~12:10) **Phase 3 store = SQLite** (owner's decision; modernc.org/sqlite, cgo-free, under the AGENTS.md dependency policy). Not up for advisor review.
- (user 2026-10-27 ~07:40) **One herd serve per kit install** (T-415): the stable kit and the dev kit each run their own serve; axis-alies stays on stable. **Claude Code's auto-mode setup dialog off** machine-wide: `skillOverrides["auto-mode-setup"]="off"` in ~/.claude/settings.json (it took T-413's typed jobs twice, in hk-s1 and hk-int).
- (user 2026-10-27) **Dependency policy** (build-vs-buy §5) in AGENTS.md: stdlib first; a dependency only when it implements a published spec or a database engine herd would otherwise write, cgo-free, pinned; new ones need the owner. `go mod tidy` done; `govulncheck@v1.1.4` runs as nightly `check_milestone` (0 reachable vulns at 6648feb). **No Claude Code background agents** for lanes (spike declined; herd stays on herdr panes). **axis-alies moves to serve** before the next stable release: T-413 writes docs/UPGRADE-v0.10.md as its change list (Q155 sends it). **Codex lanes C-02, C-03, C-04 removed** (clean, tasks merged; worktrees, state files, roster rows gone).
- (user 2026-10-25 ~11:50) **MCP agent registration OK** (T-375): plugin `.mcp.json` for Claude, Codex `-c mcp_servers.herd.*` launch args (no config.toml edit), `herd setup opencode-plugin` writes the OpenCode `mcp.herd` entry, wake as the first CLI prompt arg; all behind `mcp.jobs` (default off).
- (user 2026-10-25 ~08:45) **Test config keys OK:** `slots.cpus` (default cores / slots.max) and `merge.receipt_max_age` (default 86400 s) for T-349. **Model tiers:** Opus hard, Sonnet + DeepSeek medium, Haiku only for small tasks under 100k tokens.
- (user 2026-10-24 ~21:50) **Change-based merge gate:** per merge `go test` only on affected packages (changed + their importers); full suite nightly and before a release tag (T-347). Lead: no more load runs / `-count=N` loops in specs unless the task is about a load-only failure.
- (user 2026-10-24 ~19:45) **The app owns the scratch:** after RV-37 verify-2 (third round on "a result means the effect happened": #6 archive read branch, #7 resume loses job auth, #8 index.lock → final HOLD), the merge loop's git side is rewritten natively for the app, each step idempotent from scratch, no reuse of the stateful legacy merge helpers. Chosen over one more fix round and over handing merging to `herd merge`. Live cutover waits for it. Design + task split from hk-fable first.
- (lead 2026-10-24, reversible) T-336 open questions: the done file absent→present is the only resume from a loop needs-you for phase 2; a non-terminal "hold for the owner" verb, job delivery timers (rows 7–8, 31) and a scratch reset on replay after SIGKILL go into one follow-up loop task before the old routing is deleted; a red gate after `resolved` keeps dropping the resolution (the next attempt gets a new resolve job).
Older decisions: `docs/decisions.md` (read on demand). New ones here; move them there at a wave end.
- 2026-10-13 (lead, T-205 worker concern): Stop's unpaused combined path (role reason + stale inbox) appends the inbox line on every role-reason block — intended: the agent is blocked by its role reason anyway, the line costs no extra block and keeps it informed; the ledger bump means the later inbox-only path won't block again for a message it was already shown.

## WAITING FOR USER (lead keeps this current; newest first)
- (decided 2026-10-17 ~22:00, user) **Lead handoff stays at 500k** until the architect workflow is tested: re-reading
  cache is cheaper than a fresh lead re-investigating. Revisit after the plan trial.
- (decided 2026-10-14 ~12:00, user) **Lines of code are not a goal:** "If it can be made simpler that is ok... but LOC as a goal itself should not be chased." Replaces the size targets below (≤ 12k at v1.0, no package > 1,200): cut real duplication, dead code and over-general code; report sizes, never chase a number. Merge gate keeps: deadcode clean, one owner per helper. T-233 (guard second pass) dropped; T-234 → T-236 without a target.
- (decided 2026-10-14 ~09:00, user) **Lead noise:** yes to (a) stale heartbeat reasons dropped before delivery, (b) integrator messages the lead only when it must act (clean MERGED → merges.log only; done in role text 66a6f45), (d) heartbeat wakes a lead only for lead work. **No** to (c) holding messages while the person talks. (a)+(d) = T-216.
- (decided 2026-10-14, user) **Go target: cut first, then ≤ 12k shipped Go at v1.0 (ceiling 13k)** per docs/research/go-size-audit.md: a cut wave (~2.4k: config typed structs, guard wrapper/writer emulation, cross-lead path guard, spec-cache, dispatch --fresh/T-70/RESENT, restore summary, stop's duplicated helpers, dead code, cmd/herd args helper) BEFORE phase 3; phase 3/4 cut ~1.8k bash instead of porting (audit §2/§4); merge gate: deadcode clean, no package > 1,200 lines, one owner per helper.
- (open 2026-10-13, user thinking) `--strict-mcp-config` on herd-kit's Claude lane types + a `lead` type: measured
  ~22.0k → ~19.2k startup tokens (−2.9k/session). Prepared config is in the 2026-10-13 lead session's scratchpad (diff:
  args += flag, `lane_types.lead`, `roles.lead.type`). Per project — axis keeps Claude in Chrome on its own lanes.
- (decided 2026-10-13, user) Go rewrite = **idiomatic Go + Go tests; bash tests deleted** with each port ("they are one of the reasons for rewrite"; "the Go way, not bash logic copied 1:1"). Byte-identity dropped once a bash body is gone. T-202 does it for hooks/status after T-201.
- (decided 2026-10-13 ~20:30, user) **Go only, fail visibly**: ported commands lose their bash bodies (T-201 for phase 1; every later port deletes its bash on merge). No usable binary → guard denies (except the repair `make build`), other hooks allow, commands FAIL with the hint. Reason: after phase 1 Go was 7.7k lines and bash 10.6k unchanged (double implementation).
- Nothing blocking. Decisions open (not urgent): `max_idle_pings` 14 → 6 for keep-warm (break-even at ~670k ctx is
  ~6 pings); version pin per project before the next breaking release (lead recommends yes).

## REBUILD (user 2026-10-24) — replaces the SCOPE below
Herd is being rebuilt as one app (`herd serve`) that owns state and reacts to events; agents call tools instead of
messaging each other; the agent the user talks to doesn't orchestrate; herdr used wherever possible. Pages: "Herd, as
built" https://claude.ai/artifact/Jtbzt8FvDyDFesJm7vKCQd and "Herd rebuild plan"
https://claude.ai/artifact/BtvnamQnWQZTU55JquLYMQ. Approved design: `docs/module-map.md`, `docs/state-model.md`.
Old conventions (reviews every ~3 merges via the reviewer pane, queue rows, STATUS) stay until the app replaces them.
- Queue (T-438): the rows live in herd serve's store (`herd queue`, `herd queue add|rm`), not in docs/QUEUE.md (imported once at serve's load, then `git rm` it).
- Phase 0 spikes done (T-314..T-316; reports in `.herd/state/spikes/`): herdr socket events PASS, prompt PASS, output
  match PARTIAL, MCP from Claude/Codex/opencode PASS, Claude quota via status line JSON PASS. S5a (herdr restart) waits
  for the user.
- Phase 1 merged (T-317..T-324): module map + boundary test, merge split, socket client + fake, keep-warm removed,
  state model, `herd serve` observe-only (running in the `system` tab since 15:02 for a day run). RV-36 (hk-sol) and a
  Fable phase review running.
- Decided 2026-10-24 (user): integrator accept for every task; work records live in `.herd/state/` for one milestone,
  open items carried over, never dropped; BACKLOG.md/BUGS.md stay in the repo; UI deferred, inside herdr first;
  one app per machine, per-project agent cap, quota shown not shared; worker priority = ordered lane-type list per
  tier; project = herdr workspace, core addresses (machine, workspace, pane); opencode lanes stay, keep-warm goes;
  "approve" = the milestone task list; quota wrapper only on herd's agents; reviewer back to hk-sol (override dropped).
- Concern (2026-10-24 afternoon): load 30–41 on 10 cores with 3 Opus lanes + the integrator's full gate + a review;
  three merge flakes in one afternoon (TestRestartSuspended ×2 → T-329, TestSubscribeClosesCreateRace → T-327,
  heartbeat TestOnceReasons ×1). Lead keeps ≤ 3 heavy lanes. Phase 2 dispatch: count machine load, not only agents;
  lanes run their own tests with limited parallelism.
- Watch in the serve day run (T-327 concerns): each changed status event costs one sequential agent.get (slow herdr
  delays the stream, a re-read error ends it); live pane.updated volume (title changes?). serve restarted on the
  15:43 build at 15:44.
- Waiting for user: when a herdr restart is OK (S5a); headless ban (not approved yet); auto-memory (decide in phase 4).

## SCOPE (user 2026-10-10 ~17:15) — everything serves one of these four; nothing else gets started
1. Leads don't get stuck at night (forgot to wake up) — heartbeat --wake, lock, optional herdr plugin.
2. Roles with skills, incl. commands/skills to assign a role to an agent by hand (T-103, T-104 stay queued).
3. Keep the prompt cache warm (keep_warm — measure it first).
4. Never send Enter while the user types to a lead (herd mod, inbox.only).
`herd restart` and T-105 `allow_push` (goal 1: a lead that can't push can't advance a milestone) are in scope. Feature freeze otherwise: lead-mp reports → bug fixes only; their feature
ideas go to a list for the user. Review rounds: fix medium+ only; lows go to a list unless trivial.
Next: T-123/T-124 merge → short live test → v0.5.1 (no extra review) → measure keep_warm overnight → one real night
run → simplify (one lock helper, fewer heartbeat paths).

## Milestones
| M | Goal | State |
|---|---|---|
| M0 | Bootstrap: STATUS, role docs, design, plugin skeleton, team | done |
| M1 | Config + scripts: loader/schema, lane-status, dispatch, merge-task, watch-done, watch-agent; bash tests | done |
| M2 | Hooks: Stop no-idle (lead/integrator), PreToolUse guards, worker done-file Stop, notify (herdr; Telegram later) | done |
| M3 | Ready queue parsing + heartbeat (wake the lead) | done |
| M4 | Skills: setup, lead, integrator, worker, review-brief, night-shift; templates | done |
| M5 | Dogfood in herd-kit; axis-alies change list → its lead | in progress: herd-kit uses its plugin; axis-alies adopted stages 0+1 |
