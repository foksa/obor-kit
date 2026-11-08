# Architecture inventory: what is hardcoded, per axis, and what a seam would have to cover

Brief: lead 11-08 (owner direction in `docs/decisions.md` lines 5-6: core = agent automation; agent kinds, terminal layer and integrations pluggable; git workflow, lane shape and gate configurable; standing data/architecture pass). Survey only: no code changed. Baseline `9d2d8131` (staging). Companion: `docs/research/entity-model.md`.

Conventions: **Fact** = read from code/config or measured (command and numbers given). **Opinion** = my reading, marked as such. `file:line` are at the baseline. Line counts are `wc -l` of tracked files, comments included.

## Answer first

1. **Three of the six axes already have a seam; the owner's "pluggable" and "configurable" list is mostly about moving it, not creating it.**
   - Agent kinds: `agents.Kind` (12 methods) + `agents.Caps` (11 flags), one package per kind, registered from `init` (`internal/agents/agents.go:19,192,278`). Adding a kind is one package + a config entry (the package doc says so, `agents.go:1-6`). What is *not* pluggable is registration (compile-time `import _`, `cmd/obor/main.go:19-21`, `internal/testkit/testkit.go:17-19`, `tests/conformance/static.go:23-25`) and the kinds' dependence on herdr's own agent detection (below).
   - Terminal: `port.Panes` / `port.PaneControl` / `port.Typer` / `port.Starter` / `port.Agents` (`internal/app/port/port.go:78`, `effects.go:15,50,56,164`). `internal/app` imports no herdr code. But **three things sit outside the ports**: `setup.Herdr` (6 extra herdr methods for tabs, splits, layout, plugin registry; `internal/setup/setup.go:76`), the identity env `HERDR_PANE_ID` (9 production files), and the herdr plugin that restores panes after a herdr restart (`herdr-plugin/herdr-plugin.toml`, `cmd/obor/plugin.go`).
   - Gate: already config (`commands.*`, `merge.classes`, `merge.guard`, `merge.neutral`, `summary_regex`, `fail_grep`). The Go-specific residue is small and listed in §5 (~1,000 production lines, mostly `flakes`, `affected`, `LaneEnv`).
2. **Git workflow and lane shape are the two that are woven into the core**, not seamed. Reviews, promotion and lane freeing use git history and file edits as their state (§3): the review close is a commit with a parsed subject and a `Covered:` body; a lane is "free" when its HEAD is an ancestor of the *main checkout's* HEAD; a merged task's worker is stopped (`afterSteps`, `internal/app/loop.go:112`), so each task is a fresh agent start. Feature branches/PRs and "one lane, long list" each change a rule that more than one package reads.
3. **There is no integration surface today**; no Jira/GitHub/webhook code exists (`rg -i 'jira|webhook|slack'` in `internal cmd` finds only unrelated words). Task intake is `obor spec set` text (lead-only), output to humans is a herdr toast plus lines typed/posted into the lead's session. The wire socket (28 methods, `internal/app/wire/call.go`) and `/api/*` web endpoints (36 handlers, `internal/web/web.go`) are the only outside-facing contracts, both private to the one binary.
4. **The 20.7k git processes are a cap, not a measurement, and 57% of the measured ones build fixtures, not test obor's git code.** `tests/budget.json` sums to 20,688 because `seed` writes measured × 1.5 (`tests/budget/main.go:224-235`). I measured every git-using package once with a counting `git` wrapper: **≈14.4k processes, ≈8.2k (57%) are `testkit.New` fixtures (`init`, `add`, `commit`, `worktree add`), ≈6.2k are made by code under test, 4.0k of those by `internal/checks` (the merge slot, where real git is the rule).** Details and ranking in §7.
5. **The mixing of deciding and doing is already mostly behind ports in `internal/app`** (30 git processes in its whole test run, fs calls in 6 non-test sites; `app.Loop`, `internal/app/loop.go:33`, is the adapter bundle). The remaining mixed code is outside `app`: `checks.Scratch` (policy interleaved with 49 git calls), `launch.Launcher` (policy + 43 herdr calls), `cmd/obor/reviewcycle.go` (BACKLOG policy + git index plumbing in the CLI package), `config/validate.go:396-399` (validation that shells out to git), `setup`.

## Method and limits

- Sizes: `git ls-files '*.go'` at `9d2d8131`: **53,868 production lines** (199 files; 46,889 without `internal/testkit`, `herdrtest`, `tests/**`), **85,028 test lines** in 301 files, 1,542 `func Test`. 11-05's proposal had 44,717 runtime / 76,498 test (its definition of "runtime" may differ by a few hundred lines): about +2k runtime, +8.5k test lines in three days.
- Git processes: wrapper at `$SCRATCH/bin/git` appending one line per call, then `exec` of the real git, put first on `PATH`; one plain `go test -count=1 ./<pkg>/` per package, sequentially (same method as the budget's `writeGitWrapper`, `tests/budget/main.go:325-330`; the budget runs the full tier with a private TMPDIR). A call is *fixture* if it carries `-c user.name=obor test` (testkit's `Git`/`Commit`) or is testkit's own `diff --cached --quiet -- .obor/config.json` / `show-ref --verify` (`internal/testkit/testkit.go:206,255`); everything else is code under test. Logs and scripts are in the researcher scratchpad, not committed.
- **`cmd/obor`'s run had two failures under the wrapper** (`TestShimHookSkipsHangingDevBuild`, `TestPreToolUseHangingBinaryDenies`, `PATH`-sensitive); its 2,230 is therefore approximate. Other packages passed. `internal/checks` 5,280 is above `cap/1.5` = 4,863 (its cap is 7,295); I did not investigate why; every other package's measured count ≈ cap/1.5, which confirms the cap rule (`view` 203 vs 305; `launch` 2,041 vs 3,062; `app` 30 vs 45).
- Call sites: by regex, production git calls (`.call(`/`.git(`/`gitLocked`/`project.Git*` etc., minus the 24 herdr socket `s.call(` hits) are **~127 in 24 files** plus 7 direct `exec.Command("git", …)`; test-side `Git(` helper calls are 381. I could not reproduce "~300 call sites" as a production count; ~300 is closer to the production + 2/3 of the test helper calls. Treat my number as a lower bound: calls through function values are not matched.
- Not done: no live herdr or agent run; no gate-time measurement (see `docs/research/gate-time.md`); import graph checked with `rg`, not `go list`.

## Baseline sizes by package (production / test lines)

| Package | Prod | Test | Role |
|---|---:|---:|---|
| `internal/app` (+ `event` 795, `port` 577, `wire` 859) | 15,762 (+2,231) | 38,393 (+309) | coordinator: fold, rules, effects through ports |
| `cmd/obor` | 4,308 | 9,492 | CLI, serve wiring, review close adapter |
| `tests/conformance` | 4,246 | 363 | live/static agent-kind conformance (not in `go test` budget) |
| `internal/checks` | 3,303 | 5,266 | merge slot (`Scratch`), lane steps, gate runner, nightly, flakes, promote |
| `internal/setup` | 3,466 | 4,289 | `obor init/setup`: fs, git config, herdr tabs/plugin |
| `internal/guard` | 2,729 | 3,504 | hook policy (Claude hooks), shell classification |
| `internal/config` | 2,462 | 1,990 | config, validation |
| `internal/launch` | 2,425 | 5,706 | agent start/stop/restore over herdr |
| `internal/view` | 2,105 | 1,492 | read model for CLI and web |
| `internal/herdr` (+`herdrtest` 1,332) | 1,511 | 1,873 | herdr socket client; `herdrtest` is a fake of herdr's server |
| `internal/agents/**` | 1,299 | 946 | kind bridges |
| `internal/project`, `store`, `web`, `mcp`, `run`, `spec`, `roles`, `testkit` | 1,346 / 967 / 710 / 337 / 553 / 308 / 204 / 602 | 875 / 1,116 / 1,611 / 638 / 503 / 218 / 172 / 33 | |

## Axis 1: agent kinds

**Where it is hardcoded (fact).**

| What | file:line |
|---|---|
| Kind interface (Name, Start, NotReady, Efforts, StartEnv, Vars, Stop, Headless, Install, Transcript, LastTurn, Session, Reset, Deliver, InboxRecord, Caps) | `internal/agents/agents.go:19-69` |
| Caps: Hooks transport (none/native/plugin), Inbox, Resume, Skills, Transcript, Turns, Sessions, NamedSession, ProjectPlugin, ModelArg, VersionVar | `agents.go:192-206` |
| Default kind `"claude"` when herdr names none | `agents.go:211` |
| Registration by `import _` in three places | `cmd/obor/main.go:19-21`, `internal/testkit/testkit.go:17-19`, `tests/conformance/static.go:23-25` |
| Default model table (`opus`, `sonnet`, `deepseek`, `terra`… → kind) and default launch args per kind | `internal/config/models.go:22-38` |
| Kind names in the herdr fake and in testkit | `internal/herdr/herdrtest/fake.go:102,279`; `internal/testkit/testkit.go:322` |
| Behaviour branches on Caps (not on names): hooks, transcript/turn reads, inbox delivery, resume, plugin gating | `guard/session.go:100`, `view/view.go:504-507`, `herdr/panes.go:137,195`, `launch/launch.go:453-473,601`, `launch/start.go:56,195`, `launch/session.go:114`, `app/stopped.go:197-200`, `app/messages.go:723-726`, `app/dispatch.go:1268`, `app/prompt.go:16`, `roles/roles.go:198-200`, `config/validate.go:449`, `cmd/obor/send.go:153` |
| Claude-only transport: `hooks/hooks.json` (22 lines, 5 events, all `bash $CLAUDE_PLUGIN_ROOT/bin/obor hook …`) | `hooks/hooks.json` |
| OpenCode transport: a JS plugin that runs inside opencode | `opencode/obor.js` (363 lines) + `obor.test.js` (506) |
| herdr's `--kind` is the kind's name: a kind herdr cannot detect has no pane status | `agents.go:20` comment; `port.PaneControl.Start(... kind ...)` `effects.go:79` |

**Production lines involved:** `internal/agents/**` 1,299 (claude 326, codex 319, opencode 188, shared 466) + `opencode/obor.js` 363 + `hooks.json` 22 + the Caps-branching sites above (~15 call sites, ~100 lines) + `tests/conformance` 4,246 (live_claude/live_codex/live_opencode/live_mcp/static; run by `make conformance`, not `go test`).

**Tests that depend on it (fact).** `internal/agents/**` tests 946 lines; JS 506; 44 test files outside `internal/agents` name `"codex"`/`"opencode"` (guard session/stop-failure tests, `cmd/obor/opencode_test.go`, `view/setup_test.go`, `project/laneagent_test.go`…); the live tier is the only evidence for a real CLI (AGENTS.md "Tests": T-791 passed every unit test).

**What a seam would need to cover.**
- Registration without a recompile: today a `Kind` is a Go value. Its methods take and return strings, bytes, times and small structs (`agents.go:1-6` says "pure functions over strings and bytes"), which suits an out-of-process protocol; **but** `Start.Read`, `Start.WriteState` and `MCPRegistration.Lane` are Go funcs (`agents.go:79,95,153`) that would become data or callbacks over the protocol. *Opinion:* the kind interface is the best-shaped boundary in the tree for a plugin protocol; the cost is the three closures plus a loader.
- The data the kind owns that today lives in other packages: default models and launch args (`config/models.go:22-38`), the hook/plugin transport (`hooks.json`, `opencode/obor.js`), the conformance script (`live_<kind>.go`).
- **A dependency across axes:** a new kind needs the terminal to report its status (idle/working/blocked/done). Today herdr's detection supplies `Pane.Status` (`port.go:17-38`); the stop/turn rules read it (`app/stopped.go`, `watchdog.go`). A terminal without agent detection (herdr-core has none, `docs/research/herdr-core.md` §1) needs per-kind status from hooks/transcripts, which `Caps.Turns` covers only for Codex.

## Axis 2: terminal layer

**Where it is hardcoded (fact).**

| What | file:line |
|---|---|
| Ports the core drives: `Panes` (Subscribe, Snapshot, Busy, Activity, Turn, Notify), `PaneControl` (15 methods), `Typer`, `Starter`, `Agents` | `port/port.go:78`, `port/effects.go:56,50,164,15` |
| Terminal vocabulary in the port types: `Pane{ID, Tab, Workspace, Terminal, Label, Status…}`, `Rect`, `Plugin`, `ProcessInfo` | `port/port.go:17`, `effects.go:~120-126,196-206` |
| The adapter: socket client (1,157 lines), `Panes` adapter (304), contract pin (`herdr.Protocol`, 50) | `internal/herdr/socket.go`, `panes.go`, `contract.go` |
| **Outside the ports:** setup's own interface with 6 more calls (layout, split, new tab, new workspace, plugin list/link/unlink) | `internal/setup/setup.go:76-86` |
| Concrete `herdr.Socket` built in the CLI | `cmd/obor/serve.go:88,173`, `cmd/obor/setup.go`, `lead.go`, `drive.go`, `internal/view/background.go:22` |
| Identity = herdr pane id from env, 9 production files | `guard/guard.go:563`, `guard/session.go:109`, `mcp/mcp.go:114`, `agents/agents.go:219`, `launch/self.go:30`, `launch/launch.go:28`, `setup/agent.go:479`, `setup/oborinit.go:655`, `cmd/obor/sleep.go:185,333,416` |
| Registry keyed by herdr's `terminal_id` | `internal/app/registry.go:60` |
| Restore-after-restart is a herdr plugin hook | `herdr-plugin/herdr-plugin.toml` (startup command), `cmd/obor/plugin.go:21-55`, `setup/plugin.go` (347 lines), `launch/restore.go` |
| herdr config/env: `HERDR_SOCKET_PATH`, `HERDR_CONFIG_PATH`, `HERDR_BIN_PATH` | `herdr/socket.go:121-131`, `setup/plugin.go`, `cmd/obor/plugin.go:32-36`, `service/service.go` |
| herdr's toast as the owner notification | `app/toast.go:15`; `Panes.Notify` |

**Production lines involved:** `internal/herdr` 1,511 + `launch` 2,425 + the herdr parts of `setup` (`agent.go` 577, `plugin.go` 347, `rename.go` 441, `lane.go` 169) + `cmd/obor/sleep.go` 528 (stop/restart, uses the caller's pane) + `view/background.go` 404 (`ps`-based background processes beside herdr's `process_info`).

**Tests that depend on it (fact).** 41 test files use `herdrtest`/`Project.Herdr()` (12 `cmd/obor`, 15 `internal/app`, 4 `launch`, 4 `herdr`, 3 `setup`, `guard`, `checks`, `tests/upgrade`); `herdrtest` is a second implementation of herdr's server: 1,332 lines (fake.go 391, socket.go 941). `internal/herdr/contract_test.go` ties the two. `launch` tests 5,706 lines and 2,041 git processes (96% fixtures, §7) test herdr-driving logic.

**What a seam would need to cover.**
- Everything in `PaneControl` + `Panes` + `Typer` (status events, snapshot, type/keys/screen/wait, process info, rename, start-agent-in-pane, toast) **plus the layout/tab/workspace/plugin calls of `setup.Herdr`**, or a decision that setup stops creating layout (a terminal-neutral `setup` would only need "give me N panes").
- Identity: a terminal-neutral "who am I" instead of `HERDR_PANE_ID`; the registry's proof ranks (`srcSession/srcStart/srcLabel`, `registry.go:~45-55`) assume the terminal reports agent session and terminal id.
- The restart hook: `herdr-plugin.toml` is herdr-specific; another terminal needs its own trigger for `obor plugin restore` or serve must do it.
- A contract suite that runs against any adapter and the fake (today only herdr's fake has one). *Opinion:* `herdr-core.md`'s advice (keep `PaneControl` free of herdr-only shapes) is half done: `Pane.Workspace/Tab/Terminal` and `Rect/Plugin` are herdr shapes in the port package.

## Axis 3: git workflow

**Where it is hardcoded (fact).**

| Piece | file:line |
|---|---|
| Worktree lanes: `lanes_dir/L-NN`, created by setup, prepared by `checkout -B <task branch> <target>` | `config/config.go:657-665`; `checks/lane.go:29-51` (Prepare), `:70-123` (Free: refuse dirty/unmerged unless Discard; detach at target; delete branch) |
| `task/T-xx` branches: `branches.task` pattern `task/{id}`, default and derivations in one place | `config/config.go:93-97,311,1002-1010` |
| Target branch `branches.main` (live config: `staging`), `branches.promote` (unset in the live config: promotion off) | `config/config.go:91-97`; `.obor/config.json` (`branches: {main: staging}`); `cmd/obor/serve.go:231`, `serve_dispatch.go:45-70`; `app/dispatch.go:49-50,353` (`Specs.Target` is per task already) |
| Scratch merge slot: serve's own worktree; submit pins `refs/obor/submitted/<task>/<id>`, gate merges onto target and pins `refs/obor/candidate/<task>`, resolve pins `refs/obor/resolved/<task>` | `checks/scratch.go:34-36` (refs), `:51` Submit, `:115` Gate, `:420` Resolved, `:675-700` worktree/target |
| Publish = fast-forward the target to the candidate if the target still equals the candidate's base, else "moved" | `checks/scratch.go:500-556`; outcomes `port.PublishOutcome`; `app/steps.go` |
| Promotion = same ff for `staging → main` after the full suite and a closed review | `app/promote.go` (684), `checks/promote.go` (225), `port.FFOut` `effects.go:~425-440` |
| **Lane "free" = lane HEAD is an ancestor of the main checkout's HEAD** (not of `branches.main`), plus merge evidence | `checks/lanetable.go:100-112` (`merge-base --is-ancestor head HEAD` run in `p.Root`), `:124-129` |
| The main checkout must be on the target branch or the whole pass pauses | `app/loop.go:43` (`Loop.Checkout`), T-425 |
| Git is the store of review state: close = commit with subject `RV-<n> closed · <sha> · approved|accepted <n> · …` and body `Covered:`; read back by `git log --grep` | `app/review.go:69` (ReviewClose, `CloseCommitGrep`), `cmd/obor/review.go:59-110` (adapter), `app/promote.go:394-397` (`unreviewed`: "No state: git alone", `promote.go:40`) |
| Review close edits `BACKLOG.md` and deletes review files, then commits (policy in the CLI package) | `cmd/obor/reviewcycle.go:63-150` |
| Commit trailer `merge.trailer`; merge subject `Merge <task>: <Title>` | `config/config.go` Merge.Trailer; `checks/scratch.go:325` (mergeMessage) |
| Lock handling for git, stale `index.lock` removal | `checks/gitlock.go:36-90` |
| Role worktrees (reviewer, deep dive, integrator) detached at a head by serve | `app/roleworktree.go`, `ReviewGit.Detach` (`review.go:87`) |

**Production lines involved:** `internal/checks` 3,303 (of which `scratch.go` 754, `runner.go` 575, `lanetable.go` 286, `promote.go` 225, `lane.go` 185, `gitlock.go` 163, `lanetest.go` 138) + `app` (`dispatch.go` 1,272, `promote.go` 684, `review.go` 754, `reviewverify.go` 512, `reviewclose.go` 207, `reviewonmerge.go` 77, `stopstep.go` 233, `drive.go` 420, `steps.go` 415) + `cmd/obor/review.go` 144, `reviewcycle.go` 202, `serve_dispatch.go` 89 + `project/worktree.go` 33.

**Tests that depend on it (fact).** `internal/checks` is the only package where real git is the rule (AGENTS.md): 17 test files, 5,266 lines, 85 test funcs, **5,280 git processes in one run**, per-test caps in `tests/budget.json` (`TestScratchCutTable` 47 s). 76 test files mention `task/T-…` or `refs/obor` (222 occurrences); 6 files read `Branches`/`promote`/`staging`. `internal/app` tests use an in-memory scratch since T-996 (30 git processes total).

**What a seam would need to cover.**
- *Publish*: replace "ff target to candidate" with "push branch, open PR, wait for merged": needs a `Publish` that can return "pending external" and a `Landed(task)` fed by an integration (`port.Scratch.Landed` exists, `effects.go:~274`). The candidate model (`port.Candidate`: sha on a scratch merge, patch hash bound to the accept) assumes the slot owns the merge commit.
- *Lane free*: ancestry into one local HEAD (`lanetable.go:100`) becomes "task landed" from whatever the workflow says.
- *Target*: `Specs.Target(ctx, task)` is per task today, so a per-feature target is a config/adapter change, not a core one; the `Loop.Checkout` guard and `lanetable.go`'s use of the checked-out HEAD are not.
- *Reviews*: today git history is the state store; a PR workflow would have to supply "which tasks a review covered" from elsewhere (`unreviewed`, `app/promote.go:397`).
- *Opinion:* the cleanest cut is to name the **workflow steps the core needs** (submit → candidate → test → publish → landed → free) as one interface, of which `port.Scratch` + `port.LaneSetup` are the current git implementation; the review-close-as-commit is the part that is not behind it yet.

## Axis 4: lane shape

**Where it is hardcoded (fact).**

| What | file:line |
|---|---|
| Lanes are a count: `lanes: N`, names `L-01 … L-NN`, dirs `<lanes_dir>/L-NN` | `config/config.go:47,654-665,1002`; `HasLane` `:668` |
| One task per lane: lane row holds one `task`, `branch`, `agent`, `base`, `head`, `finished`, `blocked`, `type` | `store/lanes.go:18-31`; written only by `app/lanerows.go` (156 lines, serve's projection) |
| Lane choice: first free lane of the dispatch's model types, held/paused lanes skipped | `app/dispatch.go:492` (`pickLane`) |
| A merged task's worker is stopped; each task starts a fresh agent: `afterSteps = [after_merge, agent.stop]`, then dispatch steps `lane.prepare → agent.start → work job` | `app/loop.go:112`; `app/steps.go:51-70`; `app/dispatch.go` |
| Agent name derived per lane | `config/config.go:770`, `roles/roles.go:170`, `project/project.go:285` |
| Session facts carry one task: `store.Session{Agent, ID, …, Lane, Task}` | `store/sessions.go:19` |
| `job_next` resolves the caller's job by lane → task | `app/fetch.go:128` |
| Setup places panes L-01, L-02 in a "workers" tab | `setup/oborinit.go:188,643-651`; `setup/agent.go` (577 lines: tabs/splits) |
| Lane state is observed from git (3 git processes per lane per read): rev-parse, merge-base, status | `checks/lanetable.go:78-135`; cache/coalesce in serve `app/lanescan.go` (T-879) |
| 168 test files use `"L-0N"` literals (1,045 occurrences) | `rg '"L-0[0-9]"' --type go` |

**Production lines involved:** `config` lane parts (~60), `store/lanes.go` 149, `app/lanerows.go` 156, `app/lanescan.go` 123, `checks/lanetable.go` 286, `checks/lane.go` 185, `setup/lane.go` 169, `view` lane/queue (`view/view.go`, `queue.go`), `app/dispatch.go` (lane choice, ~150 of 1,272).

**Tests that depend on it.** Anything built on `testkit.New` (lane worktrees via `Project.Lane`, `testkit.go:195-216`): 1,045 literals; `cmd/obor` 34 of 43 test files build a fixture; `checks/lanetable_test.go`; `app` loop rigs.

**What a seam would need to cover (opinion, derived from the above).**
- *N lanes* is the current shape and works with config alone.
- *One lane with a long list* means: keep the worker session across tasks (skip the `agent.stop` after-step and the fresh `agent.start`); `lane.prepare` of the next branch while the agent is idle; `job_next` handing the same agent the next task's job; `store.Session.Task` and the lane row's single `task` become "current task". The queue concept exists (`view.WaitItem`, `WaitsLane`; waiting dispatches), so the list is already representable; the agent-per-task assumption is what is not.
- A lane abstraction would need to own: row, worktree, branch-per-task (or not), the agent bound to it, and "when is it free".

## Axis 5: gate

**Where it is hardcoded (fact).** The gate is a command pipeline in config: `commands.lane_setup`, `compile`, `check`, `check_affected`, `check_milestone`, `baseline`, `gate`, `e2e`, `retest`, `after_merge`, `lane_ready` (`config/validate.go:127`, `checks/runner.go`), classified by `merge.classes` globs (`checks/classify.go:36-90`), with `merge.guard`, `neutral`, `affected_full`, `summary_regex`, `fail_grep`. The obor-kit config (`.obor/config.json`) puts its Go policy there: classes `web`/`go`/`release`/`readers`, `go` → `check_affected` → `make check-affected` (`Makefile:107-124`).

**Go-specific bits still in the kit (fact):**

| Bit | file:line | Lines |
|---|---|---:|
| `obor affected go [--direct]`: Go package graph from changed files; reads `merge.affected_full` | `cmd/obor/affected.go` (71), `internal/affected/affected.go` (148); called only from `Makefile:112` | 219 prod, 198 test |
| `CHECK_SHORT=1` and `CHECK_DIRECT=1` exported to the lane test when `branches.promote` is set | `internal/checks/lanetest.go:84-87`; read by `Makefile:101,111,169,183` | 4 |
| `LaneEnv`: `GOMAXPROCS` and `GOFLAGS=-p` of the lane CPU budget, put into lane agents' env and lane checks | `config/config.go:874-890`, used at `checks/lanetest.go:78`, `launch/launch.go:517-518`, `setup/agent.go:243` | ~25 |
| `flakes`: log, parser and `obor flakes` read `go test` output (`--- FAIL:`, `FAIL <pkg>`) | `checks/flakes.go` (400), `cmd/obor/flakes.go` (39); decision 11-05 kept it ("says it reads Go test output") | 439 prod, 396 test |
| `testRegexp` builds `go test -run '^(A\|B)$'` for the retest | `checks/runner.go:551` | ~15 |
| Detection of `go.mod` → `go test ./...` at `obor init` | `setup/oborinit.go:365-370` | ~10 |
| `slots.cpus`/`CHECK_CPUS` mapped to `go test -p` | `run/slots.go:~270`, `Makefile` | ~5 |
| The kit's tiers (`-short`, budget, `testkit.Slow`) | `Makefile`, `tests/budget/**` (574), `internal/testkit/testkit.go:32` | kit policy, not in the product's core |

**Not Go-specific, already configurable:** classes, guard, neutral, summary/fail regexes, heavy-command slots, nightly commands (`merge.nightly_commands`).

**Tests that depend on it.** `checks/classify_test.go` 341, `affected_test.go` 126 + `cmd/obor/affected_test.go` 72, `flakes_test.go` 396, plus the gate/cut tables inside `internal/checks` (5,266 lines total; `TestScratchCutTable` 47 s budget cap).

**What a seam would need to cover.** Mostly already there: the "what a project's gate is" lives in commands + classes. To make the kit's Go policy not the kit's: move `affected` (and `flakes` if desired) behind a command (they already are invoked through `make`/`obor flakes`), replace the `CHECK_SHORT/CHECK_DIRECT` pair by a generic "env the lane test sets" config (`lanetest.go:84-87`), make `LaneEnv` a config env map instead of Go names. *Opinion:* the gate is the axis closest to done; ~700 production lines are Go-specific, none in the merge loop's decisions.

## Axis 6: integrations and events

**What exists (fact).**
- Inbound: `obor spec set` (lead/owner only, `app/specs.go:25-70`) stores a spec *text* in `specs` (`store/specs.go:19`); `Title`, `Difficulty`, `Model`, `Review`, `Fixes`, `Protected` are parsed from that text by `internal/spec` (308 lines; 24 production call sites in 13 files). There is no ticket concept; "task id" is `task_id_pattern` (`T-[0-9]+[a-z]?`, `config/config.go:312`) and the lead mints ids (`app/drive.go:310-327` for drives).
- Outbound: owner = herdr toast (`app/toast.go:15`) and lines typed/posted into the lead's session (`app/messages.go`, 1,340 lines; `Message` has 15 fields, `event/data.go:44-80`); cross-project `obor send` (`cmd/obor/send.go`, 220); `obor projects --notify` was moved to `scripts/ship` (decision 11-05).
- Event log: `event.Record` (`event/log.go:22`), 53 step constants and 13 project-level pseudo-task keys (`disk, watch, nightly, pause, loop, review, msg, serve, roles, config, idle, rule, promote`, `event/steps.go:13-43`); readers: `view`, `web` (`/api/events`), `obor status --facts`, `obor flakes` (`ReadMerges`). No subscription API: readers poll the store or call serve.
- Contracts a plugin could use: wire socket `Call` (33 fields, 28 methods, `wire/call.go`), MCP tools (`internal/mcp`, 337 lines, job tools only), web `/api/*`.

**What a seam would need to cover (opinion).** An integration is three of the existing flows: *take a task* (create spec + dispatch: `spec_set` + `dispatch` calls), *brief the lead* (`brief`/`send`/lines), *update the ticket* (react to merged/dropped/needs-you facts). The first two are wire calls an out-of-process integration can already make with a caller identity that is `lead` (`spec_set` refuses other roles, `specs.go:~60`); the third needs an outbound event feed (subscription or hook command on selected records), which does not exist. Mapping of external id ↔ `T-xx` has no home (the task id pattern is config, the spec is text; a ticket key would be a `Fixes:`-like directive in the text, or a new column).

## Where deciding and doing are mixed, ranked by test cost

**Measured** (one `go test -count=1` per package, git counting wrapper; `fixture` = testkit-built, `code` = made by code under test; caps from `tests/budget.json`).

| # | Package | git procs (cap) | fixture | code | What the code-under-test git is | Wall (this run) |
|---|---|---:|---:|---:|---|---:|
| 1 | `internal/checks` | 5,280 (7,295) | 1,240 | 4,040 | rev-parse 912, diff 417, merge-base 294, update-ref 251, reset 217, clean 213, commit 198, branch 175, status 151: the merge slot, lane free/prepare/test | 26.4 s |
| 2 | `cmd/obor` | ≈2,230 (3,020) | 1,579 | 651 | rev-parse 224, status 152, merge-base 126: `LaneTable` per `obor status`/`view.Load` | 34.1 s (2 failures) |
| 3 | `internal/launch` | 2,041 (3,062) | 1,967 | 74 | worktree 37, rev-parse 15: launch needs a repo for config/identity, not for its logic | 16.0 s |
| 4 | `internal/setup` | 2,024 (3,036) | 1,164 | 860 | `git config` 418, branch 157, worktree 157, rev-parse 67, ls-files 47 (githooks, lane setup) | 21.0 s |
| 5 | `internal/guard` | 1,127 (1,691) | 1,117 | 10 | log, ls-files | 5.7 s |
| 6 | `internal/config` | 921 (1,355) | 675 | 246 | rev-parse 246: `validate.go:396-399` and `project.Find` | 4.7 s |
| 7 | `internal/web` | 327 (491) | 219 | 108 | `LaneTable` (rev-parse, merge-base, status ×36 each) | 4.6 s |
| 8 | `internal/run` | 228 (342) | 127 | 101 | `run.go:68` + check-ignore/read-tree (the dirty-work snapshot) | 5.4 s |
| 9 | `internal/view` | 203 (305) | 152 | 51 | `LaneTable` | 3.6 s |
| 10 | `internal/app` | 30 (45) | 0 | 30 | rev-parse (project identity) | 6.5 s |
| 11 | `internal/project` | 25 (38) | 0 | 25 | worktree add/init in its own tests | 2.4 s |
| | **Total** | **≈14.4k (20.7k)** | **≈8.2k (57%)** | **≈6.2k** | | |

Per fixture: `launch` runs 275 `git init` for 2,041 processes = 7.4 per test project (init, add, commit ×2, worktree, diff, show-ref); `guard` 112 inits for 1,127.

**Ranked by test cost, the mixed responsibility behind each (fact for the site, opinion for the diagnosis):**

1. **`checks.Scratch`** (`checks/scratch.go`, 754 lines): the gate's decisions (cut table: conflict/red/green/moved, replay-mismatch, patch binding, lost-edit detection `lostEdits :473`) are interleaved with 49 `s.call(…)` git calls (Gate 9, Publish 5, …). The only seam is `Options.Git` (a shim binary, `checks.go:49`) and `GitAlive`; every decision test therefore needs real git (AGENTS.md accepts this here). 4.0k git processes, and the budget's biggest single cap (47 s). *Opinion:* the largest single test-cost block; a `git` reader/writer interface under `Scratch` (read refs/merge-base/diff; write merge/pin/ff) would let the cut table run in memory while a smaller real-git test checks the adapter. `internal/app` already did this for its side with `FakeScratch` (T-996: 30 git processes).
2. **Lane reads on every status call**: `checks.LaneTable.LaneState` (3 git per lane, `lanetable.go:78-135`) is reached by `view.Load` (CLI `status`, web every 5 s), serve's lane scan and dispatch. Tests that call it through the CLI or web pay 3N per call: `cmd/obor` 651, `web` 108, `view` 51, `app` 30. T-879 coalesces serve's scans (`app/lanescan.go`), but the read model still shells out per call. *Opinion:* a `LaneObservation` source with injected git facts makes the view and web tests git-free.
3. **Fixtures that exist because project identity is a git repo**: `project.Find` falls back to `git rev-parse` (`project/project.go:48-52`); `config.Load` needs a root; testkit builds `init + add + commit + worktree` for every project (`testkit.go:105-216`, `Slow`/`Isolate` `:32,41`). `launch` (1,967), `guard` (1,117), `cmd/obor` (1,579), `setup` (1,164), `config` (675), `checks` (1,240): ≈8.2k processes whose code under test never reads history. `testkit.NewDir` (a plain dir, `testkit.go:130`) exists but git-less projects cannot hold lanes. T-998 already copies one fixture repo in `internal/checks`; the other packages still build each fixture. *Opinion:* the cheapest cut by processes (≈half of all git processes) and independent of any architecture change: copy a template repo, or build the fixture with `git worktree` once per package.
4. **`config/validate.go:396-399`** runs two `git rev-parse` calls to check that branches exist while *validating a config*: a pure validator with an I/O dependency (246 processes in `config` tests). Small, self-contained.
5. **`cmd/obor/reviewcycle.go:63-150`**: close policy (checkout must be on the integration branch, dirty only in BACKLOG.md and the review's files, `## Open` section exists, reviewed commit exists) and the index plumbing (`update-index --cacheinfo`, `reset`, `add`, `commit`) in the CLI package, behind `app.ReviewGit` (`review.go:74-93`) for serve. Tested with real git in `cmd/obor` (reviewcycle_test.go). The mixing is real but small (200 lines).
6. **`launch.Launcher`** (2,425 lines): start/stop policy (resume vs fresh, session choice `launch.go:453-473`, the stop ladder `stop.go`, evidence `evidence.go`) calls herdr 43 times (`.H.`: Agent 11, SendKeys 9, ProcessInfo 7, Panes 4, Screen 3, …) and `ps`/procs. Tests use `herdrtest` (real socket) and testkit repos: 5,706 test lines, 16 s. The git cost is all fixture (#3); the herdr cost is the fake's.
7. **`setup`** (3,466 lines): installs hooks (`git config core.hooksPath`: 418 processes), lane worktrees (`setup/lane.go`), herdr tabs and plugin, config edits: decisions and I/O together; real git allowed for the githook tests only (AGENTS.md).
8. **`internal/app`**: already separated: decisions are `mtask` folds and `rules`; effects are `Loop` fields (`loop.go:33-57`: Scratch, Agents, Typer, Lane, Specs, Review, Promote, Disk, Nightly, Inbox); fs in `dispatch.go` (3), `reviewverify.go` (2), `fetch.go` (1); 7 inline `fx.Scratch/Agents/Typer` calls in `steps.go`, 7 in `dispatch.go` (these are the effect calls themselves, run via `App.do`).

## What the earlier research already covers, and what changed since

| Earlier doc | What it said | State at 9d2d8131 |
|---|---|---|
| `architecture-proposal-2026-11-05.md` §1 observation errors | `LaneState` drops `git status` errors; `Rows` returns an empty map | **Fixed**: `LaneState` returns `unknown` with a `Reason` for rev-parse/merge-base/status errors (`checks/lanetable.go:81-125,150-158`; T-871); ctx reaches git reads (T-873) |
| §2 one ordered commit | `recordSeq` appends before it takes `App.mu` | **Fixed**: append and fold in one `mu` section (T-944, T-955); a wider merge of the verdict/submit locks was tried and reverted (T-933, T-936); 3 feature mutexes remain: `verdicts`, `submitMu`, `configMu` (`app/app.go:100-102`) |
| §3 rule tier | rules registered: idle, idle-ping | **Advanced**: 10 rules (`app/rules.go:49-58`): needs, stop alarm, watch, review-on-merge, verify, idle, idle ping, integrator stop, reviewer stop, worker start |
| §4 `event.Message` has feature fields | move acknowledgements to `Of` | **Partly**: `Of` exists (`event/data.go:60-66`); 15 fields remain, 3 (`Fresh`, `Stop`, `Supersedes`) are referenced in one production file each (`entity-model.md` §Message) |
| §5 repeated git on the event path | lane scan per pane event | **Fixed**: dirty flag + refresher goroutine, one scan per pass (T-879, `app/lanescan.go`) |
| §6 narrow read paths | `/api/job` narrow, task/agent detail built the whole view | **Fixed in part**: `view.LoadTask`, `LoadAgent` (`view/load.go:76,96`), `web.taskRead/agentRead` (`web/web.go:516,524`); `projectsAPI` still builds a full view per project (`web.go:~230`) |
| §7 session fold, indexes | `events` had no secondary index | **Fixed**: `events_task_seq`, `prompts_job` (`store/store.go:162-163`); session ownership one pass (T-881) |
| §8 review close through serve | CLI did the commit | **Fixed**: serve calls `ReviewGit.Close` admitted (T-926; `reviewclose.go`) |
| `architecture-advice-fable.md` combined plan waves A..J | rule tier, close through serve, one lock, snapshot dropped | A, C-I largely landed (see rules above); J (one decide-and-append lock) attempted, partly reverted. **Not covered by either doc: any of the six axes below as pluggability** (the advice rejected "behaviour in config", `advice §4`, and "generic workflow configuration", `proposal` last section; the owner's 11-08 direction reverses that for the workflow, lanes and gate) |
| `branching-advice.md` (11-07) | one `staging`, promoted by serve; no feature branches | **Built** (T-974 etc.); live config has `branches.main: staging` and **no `promote` key** (promotion off during the test rework, commit `2548df4c`). The 11-08 direction ("configurable: feature branches, PRs") reopens what that doc closed |
| `herdr-core.md` (10-30) | moving to herdr-core means rewriting the adapter layer; keep `PaneControl` free of herdr shapes | Still true; `PaneControl` carries `Rect/Plugin/Workspace/Tab` (above) |
| `kit-specific-logic.md` (11-05) | kit weight is in tests + a few Go assumptions | Mostly done (T-901..T-905): tests moved to `tests/release`, guard Go rule is `guards.deny_commands`; `flakes`, `affected`, `LaneEnv`, `CHECK_SHORT/DIRECT` remain (§5) |
| `untyped-lanes-advice-fable.md` | lanes untyped, type recorded per dispatch | Done (`LaneRow.Type`, `pickLane` by level types) |
| `test-audit-external.md`, `test-pruning.md`, `gate-time.md`, `docs/plan/test-suite-rework.md` | test cost | rework landed (T-995..T-1008); this doc adds the per-package git split (§7) |

## Not verified

- Production-site count of git calls (~127 + 7) is a regex lower bound; "~300" was not reproduced.
- `cmd/obor` git count is approximate (two tests failed under the wrapper).
- I did not run live conformance, herdr, or the gate; claims about what a new terminal/agent/integration "would need" are my reading of the interfaces, not a prototype.
- I did not check `docs/design.md` for rules that would forbid any of the seams.
- Which scripts or skills read `obor status --json` (relevant to any shape change) is unknown (also unknown on 11-05).
