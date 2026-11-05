# Decisions

Owner decisions on record, newest sections first. Folded from STATUS.md on 2026-10-27 (P4-0); append new ones
at the top of "Decisions on record". Phase plans: `docs/plan/`.

## Decisions on record
- 2026-11-05 (owner): **The lead keeps `docs/observations.md`**: a curated list (under ~40 lines) of patterns about
  obor and how we work (circles, load, wrong premises, traps), read at session start and rewritten at each handoff,
  so a fresh session starts with the last one's experience. BACKLOG's Handoff is rewritten to one current entry.
- 2026-11-05 (lead, T-877 trial's pass mark): **The official MCP Go SDK is adopted.** internal/mcp −220 production
  lines; live conformance against the real `obor mcp` on each kind's shipped launch (T-898, T-910; re-run by the lead
  for RV-122): codex 10/0 (CX-L4 through its normal tool discovery), claude 14/0 (CC-L7), opencode 10/0/1 skip (OC-L6).
- 2026-11-05 (owner): **obor-kit workers test only their packages before `task_done`** (`docs/agents/worker.md`):
  no `obor run check_affected` in a lane; the gate runs the class check once. Cause: a worker's class check (the full
  suite when its change hits `merge.affected_full`) ran beside the gates and the gate ran it again (its receipt is
  reused only on an unmoved target). Also seen: herdr 0.9.5 back at ~100% CPU 43 min after its restart, in its own
  loop (`pending_agent_resume_candidates`, `next_headless_loop_deadline_with_git_refresh`), not obor's API calls:
  a herdr report (sample in the lead's scratchpad), fewer stale workspaces may help.
- 2026-11-05 (owner): **Milestone cut for v0.28**: the gate/flake fixes, RV-121's fix, the live MCP check and the
  pause gap close it; the data-model, encapsulation and kit-specific tasks move to the next milestone, where each task
  must be net-negative in production lines or say why; no new surveys until v0.28 ships. Cause: prod lines flat and
  tests +2k since v0.27.1 while the queue grew from ~7 to ~25 tasks in one afternoon of surveys.
- 2026-11-05 (owner, docs/research/kit-specific-logic.md): **obor-kit's own development leaves the product.** The
  kit's release/Makefile/install tests move to `tests/release/` (T-902), `obor projects --notify` becomes a loop in
  `scripts/ship` (T-903), shipped text names no kit things and the config example is neutral (T-904), kit-config
  tests leave product packages (in T-901). **`obor flakes` stays and says it reads Go test output.** **The guard's
  Go load-run rule becomes a config deny list** (`guards.deny_commands`, T-905; obor-kit's config carries the Go rule).
  Go cache probe in serve's low-disk line: later (BACKLOG).
- 2026-11-05 (owner): **Gates: re-gate a moved candidate only on overlap** (T-900; ~38 gates for ~10 merges today,
  most of the load behind the flaky reds); non-Go files run only the checks that read them instead of the full suite:
  **in obor-kit's config + Makefile, not in obor's Go code** (owner: `internal/affected` stays as is; T-901 replaces T-899). **Go's test cache stays off** in gates for now (its inputs miss herdr, CLIs and sockets); revisit with
  numbers after T-899/T-900.
- 2026-11-05 (lead, T-877): **The official MCP Go SDK is adopted on trial** (c9b9eae: internal/mcp −220 production
  lines; v1.8.0; binary +1.9 MB). The live conformance it passed runs a stub MCP server, so the real server against
  real clients is proven by T-898 (correction 11-05 evening: workers' and the integrator's MCP server is the installed
  kit, `~/.obor/kit/bin/obor`, not main's binary, so no agent uses the SDK server before a release; T-898 must pass
  before v0.28 ships) and by (a live case against the real `obor mcp`). A failure there → revert c9b9eae as a task.
- 2026-11-05 (lead, from T-876's numbers; owner left it to them): **synctest stays at the pilot.** One test moved
  (`TestTurnFailsAtItsDeadline` 8.8 s → 0.25 s); the other target was already gone (T-872). The remaining slow waits
  (4.0 + 3.0 + 1.5 s) and the 11-05 load flakes (cmd/obor process and socket tests) need a live unix socket, where the
  bubble's clock stalls; a dialer seam in `wire` would be a production change for ~8.5 s of test time. Revisit only if
  a seam comes for another reason.
- 2026-11-05 (owner, DD-encapsulation): **Serve ships its predicates; readers while it is down read a projection.**
  Reviews (reviewer and integrator) also ask "does a client decide something from a state string that serve knows?
  then serve ships the predicate"; docs/design.md "Change rules" 7: a hook reading serve's state while serve may be
  down reads a column serve projects, never a query re-folding `events`. Tasks T-892..T-896 and amendments to T-883,
  T-885, T-888, T-891 from docs/reviews/DD-encapsulation.md.
- 2026-11-05 (owner): **Reviews ask about copied data.** The reviewer's briefs and the integrator's review ask "does
  it add a field or type that copies data another type already holds? name the owner type" (docs/design.md "Change
  rules" 6; integrator: sendback unless a real boundary or the spec says why). Cause: one task had five shapes in
  `internal/view` (a queued task lost its title); Fable's advice named it (items 6-7) but the combined plan had no task
  for it. T-883 gives the view one `Task`; the researcher surveys the rest (docs/research/data-model.md); T-884 puts
  the question into serve's brief text.
- 2026-11-05 (owner, library-adoption-proposal-2026-11-05.md): **try `testing/synctest`** (test-only pilot, T-876)
  and **the official MCP Go SDK** (trial, T-877: new dependency approved for it; adopted only if `internal/mcp` shrinks
  and all three kinds' live conformance passes). Not now: go-workflows (two recovery systems during a migration;
  workflow versioning repeats the old-record-shape problem), Cobra (T-873 removes the usage duplication; needs a
  policy exception).
- 2026-11-05 (owner, after DD-architecture + Fable's combined plan): **Change rules adopted** (docs/design.md "Change
  rules"): three guarantees (kernel / rule / refusal; judgments are standing obligations) with the rule tier's
  written failure mode (an action may run twice or a pass late after a crash); every spec carries `Guarantee:`,
  `Replaces:`, `Adds:`; no by-hand twins; compat branches expire by store census; reviews ask what was made
  redundant. **Go stays; no rewrite.** Plan waves A..J in docs/research/architecture-advice-fable.md "Combined plan",
  pilot A first (idle on a generic rule pass, net negative in one review round, else the rule-tier waves stop).
  **Drive/land stays for good** (out of the census). **`obor stop --force` drops nothing** (open tasks continue at
  `obor start`). **Idle ping and `obor idle` stay**, rebuilt as rules. `obor review verify` by hand goes (rule 4).
  Open: S1(a) serve starts the drive agent after the stop (changes where the drive pane opens).
- 2026-11-05 (owner): **The merge hold stays deleted** (T-853 removed it; replaces the 11-02 T-718 decision "merges
  wait while the reviewer or deep dive works"): the reviewer reads its own pinned worktree, so merges go on during a
  review and fall into the next review's scope. The lead had folded the deletion into E2's spec without asking; asked
  and confirmed afterwards.
- 2026-11-05 (owner): **Flow simplifications first, all four at once**: T-850 serve runs a review's fix round
  (`obor review fix`, close refused until verified); T-851 (E1, no integrator review) **dropped the same morning**: the
  integrator's reviews sent back ~39 of ~487 merges since 10-25 with real findings and it resolves every conflict; T-852 move/drop/take-over answer first, the stop is a loop
  step (E3); T-853 reviewer, deep dive and integrator out of the main checkout (owner: the worktree move
  simplifies enough; a smart model keeps looking at every merge; the integrator gets its own worktree, not the scratch), merge hold deleted (E2, partly reverses
  T-718; a serve-owned worktree at the reviewed head answers T-718's staleness).
- 2026-11-04 (owner): **The store opens schema 9 and newer** (`upgradeFrom` 9; T-840): an older store upgrades through
  v0.24 first. **setup's retire step for the old global opencode entry goes** (T-835): no machine has it; the next
  release notes say how to remove it by hand.
- 2026-11-04 (owner): **Wave 4 is S2, not S1** (S1 landed in T-775: `verbs` column, `mtask.may`, `p.busy`): a needs-you
  becomes a block on the phase (T-827, opus:high, case → test table, golden replay first). **Test pruning starts with a
  measured survey** (T-828, `docs/research/test-pruning.md`), then pruning tasks split by package. **Lanes stay 4**:
  flakes are fixed as they show up.
- 2026-11-04 (owner): **Spec changes reach a worker only at task_done**: the early spec-change line (T-819/T-821) is
  deleted (7 review findings in two rounds); `obor spec set --note` and the task_done refusal against an older spec
  rev stay (T-824). v0.25.1 waits for it.
- 2026-11-04 (owner): **P8 and P6 go ahead** (docs/research/agent-driver-deep-dive.md; probes in T-804's live run):
  an agent's identity env stays a typed export (herdr's agent.start takes only args, so no `env` prefix), but one
  registry of obor's vars: each start unsets every one it does not set (T-812, with obor.js keeping the identity vars: OC-L2); obor chooses a Claude agent's session id and uses the
  recorded transcript path (T-813). Codex (reviews only) keeps its `lsof` session lookup; opencode keeps its plugin's.
  crouton removed from config (trial not worth it).
- 2026-11-04 (owner): **Custom roles per project**: one `roles.<name>` row in config plus `docs/agents/<name>.md`, so
  the owner can ask the lead to create one (T-801). Main checkout, git read-only, write list from the row, briefed like
  advisor/researcher; no lanes or jobs. Revises the 11-03 removal of `agents.<name>` (one lookup, not a second key).
- 2026-11-04 (owner): **Agent-driver plan** (docs/research/agent-driver-deep-dive.md): after v0.25, wave 4a (herdr
  protocol >= 22 with a schema check, Codex efforts, the resume wake through `agent.prompt`), then the conformance
  suite (static tier nightly, live tier per kind on a version change), then the probed proposals by their results.
  Approved once their probes pass: opencode MCP + plugin registered per launch (setup removes the global entry and
  link; P1) and Codex `--no-daemon` (P3).
- 2026-11-04 (owner): **No push rule in the kit's lead skill.** axis-alies' "push at every wave end and handoff when
  allow_push names you" stays that project's own practice (its docs/agents/lead.md), not the kit's default.
- 2026-11-03 (owner): **Consolidation, not features.** obor's features are enough; next work simplifies the workflow
  and makes upgrades safe (deep dive DD-consolidation). herdr and the agent CLIs are core; npm stays for Svelte.
  Answers on the survey: drop `obor monitor` (obor web and `obor status` remain); remove the `agents.<name>` config
  key (roles cover advisor, researcher, triage); delete the one-time `setup migrate` code in v0.25 (one-time migration
  code lives one minor release); keep `obor lead` and `obor restart` as they are. Claude lane workers and drives start
  with `--strict-mcp-config` built in (T-770).
- 2026-11-03 (owner): **Config names models directly** (T-765, T-766): `difficulty` levels and `roles` are
  `<model>[:<effort>]` refs (built-in model table; optional `kinds` defaults and `models` overrides), `lanes` is a count
  (L-01…), no lane types and no caps. A level's first model is the default; later ones when a spec's `Model:` line names
  them or the first is paused (`obor pause --model`). Old configs: `obor setup migrate` once, then refused. Replaces
  lane types (T-721…T-724) and `lane_types.<type>.max` (2026-11-02 entry).
- 2026-11-03 (owner): **obor web moves to Svelte 5 + TypeScript + Vite** (redesign, drive T-760): sources in `web/`,
  `make web` builds into `internal/web/dist/` (committed, `go:embed`, marked generated and `-diff`), so `go build`,
  `make check` and the gate stay Node-free; a Go test fails when dist is stale against `web/`; page tests move to
  Vitest (skipped without node). Replaces T-667's standard-library, no-build-step page. `POST /api/config` takes a
  batch (validated together, all or nothing, one commit). Lead's caveat: a second toolchain and npm dependencies
  (supply chain, upgrades) in a tool that was Go-only; keep dependencies to Svelte/Vite/Vitest.
- 2026-11-02 (owner): **`lane_types.<type>.max` caps worker tasks only**: open dispatches of the type; role agents
  (lead, integrator, reviewer, deep dive, advisor, researcher) never count, at dispatch or in `obor setup pane`
  (RV-88 #7 showed three opus roles leaving one opus slot under `max: 4`). T-738 (dispatch), T-740 (setup, docs).
- 2026-11-02 (lead): **Reviewer and deep dive start only through `obor brief`** (RV-86 #1b, T-734): `obor restart
  <reviewer|deep-dive> --prompt` is refused, so serve's admission (wait for a publish, then hold merges) comes before
  the role reads the main checkout. A fresh start is `obor restart <agent> --stop`, then `obor brief`. Docs: T-735.
- 2026-11-02 (owner): **Lanes become a pool** (docs/research/untyped-lanes-advice-fable.md): untyped lanes, the task's
  `Type:`/`Difficulty:` picks model and effort per dispatch, worktrees reused, caps per type at dispatch; T-721..T-724
  after the current wave; obor-kit then runs L-01..L-04 untyped, no pinned lane (Codex only for reviews now). Until
  then `opus` is last in medium and simple. **Checks wait while the machine is overloaded** (T-720). **Self re-exec**
  of serve, monitor, web (T-719, Fable's advice); ship only notifies leads (T-715). **Reviewer and deep dive in the main
  checkout, merges wait while they work** (T-718). **At most two review rounds per feature.**
- 2026-11-02 (owner): **opencode model/variant via obor's plugin, for obor only** (no separate project, no picker:
  people have `/model`, `/effort`): T-703 probes `ctx.session.switchModel` on a real pane and, if it holds, replaces
  the per-agent `XDG_STATE_HOME` workaround (docs/research/opencode-model-selection.md). **Researcher role**: Sonnet,
  main checkout (no worktree), writes `docs/research/` only (T-704); research may also run on a worker lane when the
  result should be reviewed; subagents only for quick lookups in the lead's turn.
- 2026-11-02 (owner): **internal/app split**: deep dive steps 1-5 (docs/research/app-split-deep-dive.md) as T-690..T-694
  on Sonnet (owner: Sonnet instead of DeepSeek), after T-686/T-689; steps 6-7 not now; aliases removed in a later
  task. **Spec `Type:` line** (T-689): an optional lane type beside `Difficulty:`, preferred at dispatch, else the
  level's first free lane.
- 2026-11-02 (owner): **Gemini codebase review** (docs/research/codebase-performance-and-test-review.md) judged by Fable
  (docs/research/codebase-review-advice-fable.md; its 'proj split still to do' is wrong: T-600/T-610/T-615 did it).
  Gate time is the slowest package plus 15 s waits on a missing serve, not fixtures or sleeps: T-685 (short test
  deadlines), then T-686 (`t.Parallel()` in internal/app); T-687 three small cleanups. Rejected: guard emulator cut,
  stop/wake removal (false premise), lane-state caching, review-as-task, LOC-driven test consolidation, indexes and
  O(N) `Owned` (not measurable). Monitor caching dropped (measured 1.2-1.7 % CPU). Next architecture step: a deep dive
  on splitting internal/app into packages (owner decides on its proposal). Open: herdr at ~100 % CPU, cause unknown.
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
