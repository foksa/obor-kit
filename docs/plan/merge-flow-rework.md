# Merge flow rework: tests in the lane, a seconds-long merge slot, staging promoted to main

Plan (advisor, 2026-11-07, for the lead). **Owner decision 11-07** (docs/decisions.md:7): (1) tests run in each task's
own lane before submit, in parallel across lanes, the fast tier (T-957's `-short`) against the current target; the
merge slot runs no tests. (2) The slot only merges onto `staging` and builds/vets (seconds); a move never re-runs tests
or re-reviews; a real conflict is a resolve job as today. (3) The full suite runs once per staging→main promotion
(docs/research/branching-advice.md §2); main stays green. (4) The integrator reviews the task's own diff once, whatever
the target does. Agent-neutral (docs/decisions.md:9: no agent-kind-specific mechanism).

What today costs (11-07 12:00-12:45 local; `.obor/state/obor.db` `events`, UTC): one serial slot
(`internal/app/scheduler.go:36-71`, one scratch token `internal/app/steps.go:22-27`) runs every gate; each publish moves
the queued candidates (16 `target.moved` for 18 merges today), each move re-gates (`internal/checks/scratch.go:200-218`,
compile-only on a clean move since T-914) and, when the patch hash changes, re-reviews (7 `review.N` of 21 `review`
intents today; `internal/app/loop.go:707-717`); gates take 2-6 min at load 30-50 (docs/research/gate-time.md §1);
publish waits for a running gate (`scheduler.go:67-69`, `loop.go:760-767`) though it only fast-forwards the main
checkout (`scratch.go:594-603`); serve's self-restart after a publish's `make build` cuts the running gate
(`internal/app/follow.go:12-14,76-77`: idle, else after 10 min; T-949's `gate` intent 12:06:44, result 12:16:33); a
timing flake sends the worker back (`loop.go:727-737`); a lead commit to main moves every candidate.

## Recommendation, and the risks first

**Build it in two phases, with the lane test step first and `staging` second.** Phase 1 (one hard task plus four
parallel follow-ups) moves `go test` out of the slot into a per-lane step serve runs in the lane worktree, keeps today's
target (`main`), and deletes the moved re-gate, `OBOR_MOVED`, the receipts and the load wait. It is a working system on
its own: the slot becomes seconds, moves stop re-testing and re-reviewing, and the restart problem dissolves. Phase 2
adds `staging` and the promotion (one hard task) on top. Phase 1 without phase 2 is today's test coverage per merge
(the fast tier plus a compile check on the moved merge); phase 2 is what makes `main` green by construction.

The one design choice the lead asked about (§2): **serve runs the fast tier in the lane worktree as a merge-loop step,
after the slot has merged and compiled the candidate**, not the worker before `task_done`. Serve's own run is the
proof (as the gate is today); it runs on the exact commit that publish fast-forwards to; it needs no agent-kind text,
no receipt check at `task_done`, and no worker discipline; and it is the only way to test "against the current
target" without a worker doing git writes.

Risks, most likely first:

1. **The lane worktree is shared with an idle worker.** The test step checks the candidate out in the lane (detached)
   and restores the branch after. A worker that still writes after `task_done` (the contract says it does not,
   AGENTS.md "Workflow") makes the checkout fail: a retry (the failure rule), three of them a needs-you. Today's
   `submit` has the same residual race (`docs/state-model.md:148`). Mitigation: the step refuses a dirty lane before it
   touches anything and tries again next pass; no data is ever lost (the branch is never moved).
2. **Load.** Up to four fast tiers, a promotion and the workers' own `go test` share 10 cores, and the load wait goes.
   Timing flakes will rise; the one re-run of the failing tests (§4, T-C) absorbs single flakes. The knob is
   `slots.max` (the lane steps take pool slots): the owner lowers it if reds climb. Measure for three days after T-A.
3. **The untested window.** A candidate tested on `S + T` publishes as `S + T'` after a move, compile-checked only.
   That is the window the owner accepted for T-914 (0 real reds in 165 moves, docs/research/merge-without-regate.md
   §1), now for every move; with phase 2 it is found by the next promotion (≤ N merges) and the culprit is one of ≤ N
   subjects (bisect in phase 3).
4. **`internal/app` churn.** T-A touches `states.go`, `steps.go`, `mtask.go`, `scheduler.go`, `loop.go` and every
   `TestLoop*` rig that assumes gate→green. It is the riskiest task of this plan; `Difficulty: hard`, `Review: on
   merge`. The step name `gate` is kept (its meaning narrows to merge + compile), so no record shape changes and no
   compat fold is needed.
5. **A conflict still stalls the slot** (`scheduler.go:36-52`: a merge open in the scratch blocks every gate until the
   integrator resolves it). Unchanged, but it becomes the only slow thing left in the slot. Phase 3 T-K moves resolve
   jobs to a second worktree.
6. **Serve's binary follows `main`, not `staging`** (§3): a serve fix reaches the live serve at the next promotion
   (minutes when the queue is idle, ≤ N merges otherwise), never untested. The reverse choice (build at each staging
   publish) keeps today's per-publish restart and runs serve on fast-tier-only code.
7. **Not verified:** the compile tier's wall time under four parallel lane tests (3.5 s build + 3.5 s vet measured warm
   at load 30, merge-without-regate.md §4; `make deadcode` unmeasured); the lane step's time at 4-way parallelism; the
   number of promotions a day at N = 6 (branching-advice.md §1 estimates 10-12).

## 1. The new flow and its state machine (a)

One sentence: `task_done` → submit (pin S) → **gate** (slot, seconds: merge S onto the target head T in the scratch,
compile, commit the candidate C, pin) → **test** (lane worktree, minutes, parallel across lanes: the class's checks
with `-short` on C) → review once (the task's own diff `merge-base(S,T)..S`) → accept → publish (ff) → merged; a move
between accept and publish is one more slot `gate.N` (seconds) and a publish, nothing else; every N merges (or when
idle) a **promotion** runs `make check` on staging's head in its own worktree and fast-forwards `main` on GREEN.

Changes to `docs/state-model.md`, by row (rows are not renumbered, `docs/state-model.md:274-276`). "goes" means the row
or the clause is deleted; "changes" names what; "new" is a new row.

| Row | Today | Rework |
|---|---|---|
| States (`:204-213`, `states.go:81-162`) | submitted · green · awaiting-accept · red · conflict · resolving · resolved · accepted · moved · published · merged · … | **new state `candidate`**: the slot committed and compiled the merge, its tests have not run (`uses` none, `scratch` false, phase `testing`); `moved` stays (its step is the slot's `gate.N`, no test after it). Everything else stays. |
| 16 | `task_done` → submitted | unchanged |
| 18 | submitted, moved, resolved → green \| red \| conflict \| fail: `gate[.N]` merges, **checks**, commits, pins (`scratch.go:118-259`) | **changes**: `gate[.N]` merges S onto T (resolved: checks R as committed), runs `commands.compile` only (obor-kit: build, gofmt, vet, deadcode), commits C with the spec message, pins `refs/obor/candidate/<task>`, fills `Candidate{SHA: C, Target: T, Base: merge-base(S,T), Patch/Stat/Diff: of Base..S}`. Outcomes: lane or resolved input green → **candidate**; moved input green → **green** (row 20 decides); red (does not compile) → red; conflict → conflict (open in the scratch, as today); fail → needs-you. The `keeps` decision (`scratch.go:200-218,315-369`) and `GateInput.Green/Resolved` (`port/effects.go:285-295`) **go**. |
| 18b | — | **new**: candidate → green \| red \| fail: step `test[.N]` in the lane worktree (§2): `checkout --detach C`, `lane_links`, the class's checks for `diff T..C` (`check_affected` with `-short`, the guard; `runChecks` as today minus the slot), one re-run of the failing tests (T-C), `checkout <branch>`; intent `test[.N]{candidate C, target T, lane}`, result `{green\|red\|fail, line, checks, tree}`. Runs outside the scratch token, one per lane, in parallel. Class G (§3). |
| 19 | green → awaiting-accept: the review job carries `diff Target..SHA` (`jobstart.go:237-241`) | **changes**: the job carries `diff Base..S` (the task's own diff; `Stat` and `Diff` of the same), with the candidate sha for context. One review per attempt whatever the target does. |
| 20 | green → accepted when the accept's patch hash equals the candidate's (`loop.go:707-717`) | **changes in meaning, not code**: `Patch` is now `sha256(diff Base..S)`, invariant under moves (`Base` is fixed by S's history; staging only fast-forwards), so a moved task's `gate.N` always lands here as `accept.kept`. The spec-rev rule stays (a lead spec change since the accept re-reviews, `mtask.go:162-164`). |
| 21, 22 | red → sendback; past K / fail → needs-you | **changes**: red comes from `gate` (compile) or `test`; both count toward K. Unchanged otherwise. |
| 23, 25, 30, 31 | conflict → resolving → resolved; the integrator resolves in the scratch | unchanged. After `resolved` (row 30) the slot's `gate.N` compiles R and goes to **candidate**: a resolution is tested once in the lane (`test.N`), then row 20 (the lane patch is unchanged → `accept.kept`) or row 19 (first review). |
| 32 | accepted → moved → `gate.N` (full or kept re-gate, rows 19/20 decide the accept) | **changes**: moved → slot `gate.N` (merge onto T', compile, seconds) → green → row 20 `accept.kept` → accepted → `publish.N`. No test, no review on a move. |
| 33, 34, 35 | publish; merged; hold | unchanged (`scratch.go:560-611`). `scratchWait`'s publish-waits-for-gate (`scheduler.go:67-69`) stays as code; it now waits seconds, so the BACKLOG.md:53 item closes without a change. T-958's ordering is harmless but unneeded. |
| 36 | the integrator's start | unchanged |
| 37, 38 | after-merge steps: `after_merge` then `agent.stop` | **changes**: `after_merge` runs after a publish only while `branches.promote` is unset (today's projects); with it set it runs after a GREEN promotion in the promotion worktree (row 51). `agent.stop` unchanged. |
| 45 | nightly: lane_setup, `check`, check_milestone, baseline, gate, e2e, backup, nightly_commands (`checks/nightly.go:73-98`) | **changes**: `check` is dropped from the list when `branches.promote` is set (the promotion runs it); the rest stays. |
| 50 | — | **new** (task key `promote`): trigger each pass when `branches.promote` is set, no promotion runs, staging's head H ≠ the last promoted sha, and either merges since the last promoted sha ≥ `merge.promote_every` (default 6) or ≥ 1 merge and the loop is idle (no scratch step or test running, no task in submitted/candidate/moved/accepted). Intent `promote.<seq>{sha H, since: last promoted sha, merges: [T-xx…]}` (G). Effect in `<scratch_dir>-promote`: reset to H, lane_setup, `check` (full); RED → the failing tests re-run once (T-C); GREEN → `update-ref refs/heads/<promote> H` after `merge-base --is-ancestor <promote> H`; writes `.obor/state/last-full-check` (`checks.LastFullCheck` and `obor status` keep working). Result `{sha, GREEN\|RED, line, retried}`. GREEN → fact `promoted{sha}` (ends a RED's needs-you); RED → `needs-you{promotion RED <sha7>: <tests>; merges since <last7>: T-a, T-b, …}` (one line, the nightly's shape `internal/app/nightly.go:21-24`). A run that ends neither is a counted failure, retried after 10 min as the nightly's (`nightly.go:26-28`). |
| 51 | — | **new**: after a GREEN promotion, intent `after_promote.<seq>` (G, idempotent by contract as `after_merge`, T-525): `commands.after_merge` in the promotion worktree at H with `OBOR_ROOT=<root>` in its environment (obor-kit: `make build OUT=$OBOR_ROOT/dist/obor`). Serve's binary follows `main`. |
| Scheduler rule 4 (`:355-359`) | "Before publish and on a refused ff, re-gate; a byte-identical patch keeps the accept" | **changes**: before publish and on a refused ff, re-merge and compile; the accept is bound to the task's own patch and always stands unless the spec rev changed. |
| Concurrency (`:370-384`) | "only what needs serve's worktree is serial: gate, publish, a resolved check" | **changes**: the scratch token covers `gate` and `publish` (seconds) and an open resolve; `test` steps run one per lane, in parallel, outside the token (`p.exec` tracks them); the promotion runs in its own worktree, outside the token, one at a time. The restart drain (§3): while `p.restarting` is set no new `test` or `promote` starts. |

Phases for `obor status`/web (`wire/status.go:145-162`): new `testing` (the lane's checks run) and `test pending`;
the `running:` line lists every running step (slot, lanes, promotion); a `promoted <sha7> (<n> merges since)` line
replaces the `full check:` line when `branches.promote` is set.

## 2. Where the lane's test run happens and how serve trusts it (b)

**Serve runs it, as merge-loop step `test[.N]`, in the lane worktree, after the slot's `gate`.** `checks.Scratch.Test`
(new file `internal/checks/lanetest.go`), the lane analogue of `Gate`:

1. The lane dir from the config (`scratch.go:59-66`); `status --porcelain` non-empty → error "lane dirty" (retry);
   the branch name recorded (`branch --show-current`; detached already → the last recorded branch, `mt.disp.Branch`).
2. `git checkout -q --detach C` (C = `Candidate.SHA`, the slot's merge commit; `clean -fdq` of untracked build
   outputs is not done: the worker's untracked files stay), `linkLaneLinks` (`scratch.go:145`).
3. A runner with `dir` = the lane (`runner.go:47-50`): `OBOR_CHANGED_FILES` = `diff --name-only T C` (the task's
   files), classify, `lane_setup` if configured, the class's checks and the guard (`runChecks` as today; the class
   check is `check_affected`, which T-957 gives `-short`), logs under `.obor/state/logs/<task>-<check>.log` as today,
   `onCommand` for the status line. It takes a **pool slot** (`run.Slots.Acquire` with prio `worker`,
   `internal/run/slots.go:25-28`), never the merge marker: the pool bounds how many lanes test at once (`slots.max`).
4. Red: the failing test names (`flakes.go:36-60` already extracts them) → `commands.retest` once with
   `OBOR_FAILED_TESTS`/`OBOR_FAILED_PKGS` (T-C); green then → green, the flake logged with `Retried`/`PassedOnRetry`
   (`flakes.go:21-30`, fields nobody writes yet); still red → red.
5. `git checkout -q <branch>`: the lane is back where the worker left it (a sendback's fix job finds its branch).
6. Result: `{outcome, line, checks, tree: C^{tree}}`.

**What proves it ran on the submitted sha.** The same thing that proves a gate today: serve's own intent/result pair
under the task's key. The intent names C and T; C's first parent is T and its second parent is S, the pinned submit
(`refs/obor/submitted/<task>/<id>`, `scratch.go:30-37`); publish fast-forwards the target to C itself
(`scratch.go:600`). So the published commit is the tested commit, byte for byte, unless the target moved (then C' = S
onto T', compiled in the slot: the accepted window, risk 3). No receipt, no hash of a worker's claim.

**What if the lane's target is stale at submit.** It always is (a worker's lane is based on the dispatch target,
hours old). It does not matter: the slot's `gate` merges S onto the head at that moment and the test runs on that
merge, so the tested tree is at most the queue time stale (minutes), not the task's age. A conflict between S and T is
found by the slot before any test runs (today's resolve path, rows 23-30, unchanged).

**Why not the worker's own run before `task_done`.** (i) Workers may not write git (`templates/agents/worker.md:18`), so
they cannot merge the target in: the run would be against the dispatch base, hours stale, the opposite of "against the
current target". (ii) Trust needs a receipt check at `task_done` (`internal/run/receipts.go` keyed by tree) and a
refusal loop when the worker edits after the run; each kind's role text needs the rule, and a rule a worker must remember is one more
thing to get wrong (today's refusal of a `task_done` on a stale spec rev is the same shape, `docs/state-model.md:34-38`). (iii) It keeps the receipt mechanism alive instead of deleting it (§4). (iv) The
owner's 11-05 decision that workers test only their packages (docs/decisions.md, `docs/agents/worker.md:9-11`) stays
true: the worker's `go test ./<pkg>` before `task_done` is still the cheap self-check; the lane step is serve's.

**Why after the slot's merge, not before.** Testing first in the lane would need the target merged into the lane's
branch (a merge commit on `task/T-xx`), or a test of S alone (stale). Merging first costs seconds, catches conflicts
before a 3-minute test, keeps the resolve path unchanged, and tests the actual candidate commit.

## 3. Crash and restart cases (c)

Every case follows the existing classes (`docs/state-model.md:83-162`); the new or changed rows:

| Case | What is recorded | Replay / recovery |
|---|---|---|
| Serve restart mid-`test` (cut by ctx) | intent `test[.N]` pending, no result | Not counted (`loop.go:472-485`, cut by serve). The next pass runs it again under the same key: the lane is checked out detached at C again (a lane left detached by the crash is dirty-free: `checkout --detach C` is a no-op), the checks run again, the branch restored. Minutes lost, same outcome. Class G: a function of (C, T, the lane's untracked files). |
| Crash after the test's restore, before its result | as above | Run again (wasted run, same result). An effect that ran whose result write failed in this process is not run again (T-371, `:126-127`). |
| Crash leaves the lane detached | — | The re-run restores it. Until then `obor status`'s lane table reads a detached lane (`checks.LaneTable`); `lane.prepare`'s `checkout -B` (`checks/lane.go:35-38`) fixes any leftover at the next dispatch. A sendback's fix job never goes out while a `test` intent is pending (the task is `candidate`, not `red`). |
| Serve restart mid-`gate` (slot) | intent `gate[.N]` pending | As today (`:149-150`): reset the scratch, merge again, compile again; the same two shas make the same merge commit (committer date rule, `scratch.go:109-112`). Seconds. |
| Restart mid-publish | intent `publish[.N]` pending | Unchanged (`:151`): target holds C → Already; moved → Moved; else ff. |
| Restart mid-promotion | intent `promote.<seq>{H}` pending | Run again at the recorded H (not staging's current head: the result must say "H is GREEN"). Main already at H (a crash between the ff and the result) → GREEN result, no second run. RED's needs-you missing → settled from the result at the next pass (as `nightly`, `:162`). |
| Crash between a GREEN promotion and `after_promote` | `after_promote` intent pending or absent | Run (again): `make build` is idempotent (T-525). |
| The new binary (built by `after_promote` into `dist/obor`) | — | `follow.go:53-106` sees it on its 30 s tick; restart at once when idle (`p.running` empty, `len(p.exec) == 0`, no nightly, no admitted call), else after 10 min. **New: the drain.** While `p.restarting` is set, `pick` starts no `test` and no `promote` (slot steps and publishes, seconds, still start), so idle comes within one lane test (≤ ~5 min) and nothing is cut except at the 10-min deadline. ~10 lines in `followBinary`/`pick`. This is BACKLOG.md:53's "a gate survives the restart", solved by not starting what the restart would cut. |
| Serve's binary follows `staging` (not chosen) | — | Would keep today's per-publish `after_merge` + restart (`.obor/config.json` `after_merge`), one restart per merge with four lane tests to drain each time, and run serve on fast-tier-only code. Rejected; see risk 6. |
| The main checkout not on `branches.main` (`staging`) | `Loop.Checkout` pauses the loop (`loop.go:42-44`, T-425) | The owner's one-time `git checkout staging` in the main checkout (§6); a lead `git checkout main` there pauses merges as it does today. |
| Lead commit to staging while a candidate is accepted | `target.moved` | `gate.N` re-merges and compiles (seconds), `accept.kept`, `publish.N`. No test, no review. |
| A conflict open in the scratch at restart | state conflict/resolving/resolved | Unchanged (`scheduler.go:36-52`): the scratch is held; the integrator's job is re-sent by the step that reaches it. |
| A `test` step's lane worktree has a stale `index.lock` | — | `gitLocked`'s 60 s stale-lock rule (`checks/gitlock.go`, `:104-112`) applies to every `Scratch` git call, the lane's included. BACKLOG.md:56's empty `index.lock` mystery becomes more visible with serve writing in lanes; the step's FAIL names the lock. |

## 4. What is deleted and what is added (d)

Deleted (production lines are estimates from the cited ranges; tests in brackets):

| What | Where | Lines |
|---|---|---|
| The moved re-gate's three-way `keeps` (keepFull/keepAll/keepMoved) and the kept result | `internal/checks/scratch.go:200-218, 248-256, 315-369`; `port/effects.go:285-295` (`Green`, `Resolved`), `:321-324` (`Kept`); `internal/app/steps.go:215-216`; `mtask.go:166-170` (`mt.green`) | −95 [−490: `internal/checks/keptgate_test.go` 334, `internal/app/keptgate_test.go` 155] |
| `OBOR_MOVED` | `Makefile:63-66, 80-81`; `internal/checks/runner.go:32, 35, 360` | −10 [−25: `tests/release/makefile_test.go:243-260`] |
| Tests in the slot: `runChecks` from `Gate` (moves to `Test`), the `Full`/partial distinction (`merged{full}`) | `scratch.go:206-218, 253-256`; `port/effects.go:240` (`classify.go`'s `Class.Full` is a different flag and stays) | moved, −10 (compat: `full` in old `merged` facts folds as nothing until v0.30) |
| `review.N` on a move | no code: it follows from the patch binding (row 20) | 0 |
| Slot load waits (T-720, T-726) | `internal/run/load.go` (49), `load_darwin.go` (24), `load_other.go` (6); `slots.go:34-36, 45-48, 54-55, 97-105, 250-290` (`LoadLimit`, `LoadCap`, `Load`, `overloaded`, the wait branch); `runner.go:33, 180-182` (`loaded`); `nightly.go:153-155`; `checks.load_limit` + `CHECK_LOAD_LIMIT` in `internal/config` | −140 [−440: `internal/run/load_test.go` 438, `internal/checks/underload_test.go`] |
| Nightly `check` (when `branches.promote` is set) | `internal/checks/nightly.go:77` | −1 (a condition) |
| Receipts (T-349): `obor run`'s receipt write and the gate's reuse | `internal/checks/receipts.go` (49); `internal/run/receipts.go` (154); `internal/run/run.go:104, 128-131, 144-146, 150-204`; `merge.receipt_max_age` (`internal/config`); `docs/design.md:541-548` (lead) | −270 [−390: `receipts_test.go` ×2]. `obor run` keeps the slot, the marker and `OBOR_CHANGED_FILES`. |
| `after_merge` per publish, when promotion is on | `internal/app/steps.go:61-80` (`afterSteps` by config); `scratch.go:613-675` reused by `after_promote` with a dir parameter | −0 (a condition), the per-publish restart goes |
| T-958's publish-first ordering | `scheduler.go:13-34` if it merges | 0 (harmless; drop the task if it has not started) |

Added:

| What | Where | Lines |
|---|---|---|
| `Scratch.Gate` narrowed to merge + `commands.compile` + commit + pin; `Candidate.Base`, `Patch/Stat/Diff` of `Base..S` | `internal/checks/scratch.go` (`Gate` 118-259 shrinks), `port/effects.go:232-241` | +25 / −60 |
| `Scratch.Test` (the lane step, §2) | new `internal/checks/lanetest.go` (detach, runner in the lane, retest, restore) | +120 |
| `commands.compile`, `commands.retest`; `make check-compile`, `make check-retest`; `OUT` for `build` | `.obor/config.json`, `Makefile` | +20 |
| State `candidate`, step `test`, the fold, `pickCandidate`, lane steps outside the scratch token, the drain | `internal/app/states.go` (+1 row), `steps.go` (+45), `mtask.go` (+15), `loop.go` (+20), `scheduler.go` (+10), `follow.go` (+10) | +100 |
| Phases `testing`/`test pending`, running list, promoted line | `internal/app/wire/status.go`, `internal/view`, `internal/web` | +40 |
| Retest once (names → env → `commands.retest`), flake fields written | `internal/checks/runner.go`, `flakes.go:21-30` | +50 |
| Promotion: trigger, fold, step, needs-you, `promoted` fact, status | new `internal/app/promote.go` (modelled on `internal/app/nightly.go` 232 lines) | +180 |
| Promotion effect: the `-promote` worktree session (reusing `open`/`reset`/`run` of `checks/nightly.go:39-50, 53-72`), ff of the promoted branch, `after_promote` | new `internal/checks/promote.go` | +90 |
| Config `branches.promote`, `merge.promote_every` + validation; `flakes.go:243` | `internal/config/config.go:92`, `validate.go`; `internal/checks/flakes.go` | +15 |
| Culprit bisect (phase 3) | `internal/checks/promote.go` | +120 |

Net production: about +640 added, −530 deleted, so roughly neutral; tests about −1,350 deleted against ~+600 new
(the lane step, the promotion, the drain). Not net-negative in production lines; the owner's rule is paid for in
mechanisms removed (five) and in slot minutes (a 70-merge day: ~70 × 10 s of slot time instead of 400-550 gate minutes,
plus ~12 promotions of ~3-6 min outside the slot).

## 5. Phased task list (e)

Each phase leaves a working system. "∥" = may run in parallel with.

**Phase 0 (in flight, 11-07).** T-954 (a test-only change affects one package): **keep**; the lane step's affected set
uses it. T-957 (`-short` in `check-affected`): **keep**; it is the lane tier's command, and `make check` (full) stays
the promotion's. T-958 (publish before a new gate in `passOrder`): **obsolete**; if it is green let it merge (30
harmless lines), do not wait on it or dispatch a fix for it.

**Phase 1: tests leave the slot (target stays `main`).**

| Task | Difficulty | Files | Depends on | Leaves |
|---|---|---|---|---|
| **T-A: the slot merges and compiles; the class's checks run in the lane.** `Scratch.Gate` → merge + `commands.compile` + commit + pin, `Candidate.Base/Patch` of `Base..S`; `Scratch.Test` in the lane (§2 steps 1-3, 5-6; the retest is T-C); state `candidate`, step `test`, phases; moved → `gate.N` → `accept.kept`; `keeps`/`OBOR_MOVED`/`Kept`/`Green` deleted with their tests; the review job's diff `Base..S`; `obor status` lane row `testing`, running list. Spec says: step name `gate` kept, no record shape change; old `gate` results fold as today. `Review: on merge`. | hard | `internal/checks/scratch.go`, new `lanetest.go`, `runner.go`; `internal/app/{states,steps,mtask,loop,scheduler,jobstart}.go`, `port/effects.go`, `wire/status.go`; `internal/view`, `internal/web`; `Makefile` (`check-compile`), `.obor/config.json` (`compile`); `docs/state-model.md` rows 18, 18b, 19, 20, 32, States, Scheduler rule 4, Concurrency; `docs/module-map.md` | T-957 merged (the `-short` tier) | a working loop: seconds in the slot, parallel tests, no re-test or re-review on a move |
| T-B: delete the slot load waits (T-720/T-726) and `checks.load_limit` | medium | `internal/run/{load.go,load_darwin.go,load_other.go,slots.go}`, `internal/checks/{runner,nightly}.go`, `internal/config`, tests | — (∥ T-A) | the pool slot alone bounds parallel checks |
| T-C: one re-run of the failing tests, in the lane step (and later the promotion): names → `OBOR_FAILED_TESTS`/`OBOR_FAILED_PKGS` → `commands.retest`; `Retried`/`PassedOnRetry` written | medium | `internal/checks/{runner,flakes,lanetest}.go`, `Makefile` (`check-retest`), `.obor/config.json` | T-A | a load flake no longer sends a worker back |
| T-D: the restart drain (no new `test`/`promote` while `p.restarting`) | simple | `internal/app/follow.go`, `loop.go` (`pick`) | T-A (∥ T-C, T-E) | a self-restart cuts nothing but at the 10-min deadline |
| T-E: delete receipts (T-349) | medium | `internal/checks/receipts.go`, `internal/run/{receipts,run}.go`, `internal/config`, tests; `docs/design.md:541-548` (lead) | T-A (∥ T-C, T-D) | `obor run` without receipts |
| T-F (only if T-A leaves it out): web view of lane tests and the running list | simple | `internal/web`, `web/src` | T-A | — |

Order: T-A ∥ T-B first; then T-C ∥ T-D ∥ T-E (∥ T-F). Measure three days after T-A: slot seconds per merge, lane
test minutes, reds and their re-run outcomes, needs-you from dirty lanes (risk 1).

**Phase 2: `staging` and the promotion.**

| Task | Difficulty | Files | Depends on | Leaves |
|---|---|---|---|---|
| **T-G: promotion.** `branches.promote`, `merge.promote_every`; the trigger (row 50); `promote.<seq>` in `<scratch_dir>-promote`; GREEN → ff the promoted branch, `promoted` fact, `last-full-check`; RED → retest (T-C) → needs-you listing the merges since; `after_promote` with `OBOR_ROOT` (row 51) and `after_merge` per publish off when promote is set; the nightly drops `check` when set; `flakes.go:243`; `obor status` promoted line. `Difficulty: hard`, `Review: on merge`. | hard | new `internal/app/promote.go`, new `internal/checks/promote.go`, `internal/checks/nightly.go:77`, `internal/app/steps.go` (`afterSteps`), `internal/config`, `internal/view`; `docs/state-model.md` rows 38, 45, 50, 51 | T-C (retest), T-A | with `branches.promote` unset: nothing changes (today's projects); set: `main` moves only on a GREEN full suite |
| T-H: `Makefile` `OUT ?= dist/obor` for `build`; `.obor/config.json` `after_merge.cmd` = `make build OUT=$OBOR_ROOT/dist/obor` | simple | `Makefile`, `.obor/config.json`, `docs/UPGRADE-v0.30.md` (adopters' order of steps) | — (∥ T-G; the env var is ignored until T-G exports it) | — |
| Owner's switch (§6) | — | config, one `git checkout -b` | T-G merged, T-H merged | lanes, reviews and serve's loop target `staging`; releases and `install.sh` follow `main` |

**Phase 3: on numbers.**

| Task | Difficulty | Trigger |
|---|---|---|
| T-I: culprit by first-parent bisect over `main..staging` (failing packages only, ~log2(N) runs) | medium | more than ~2 RED promotions a week where the lead could not name the culprit from the ≤ N subjects (branching-advice.md §4 phase 2) |
| T-J: `make release` skips its own `make check` when HEAD is the last promoted GREEN sha (`RELEASING.md:20-25`) | simple | the first release after T-G |
| T-K: resolve jobs in a second worktree so an open conflict does not hold the slot (`scheduler.go:36-52`) | medium | a conflict holding the slot ≥ 10 min while candidates wait, seen twice |
| T-L: remove the `full` compat fold of old `merged` facts | simple | v0.30 (change rule 5 census) |

## 6. The lead's and the owner's steps (f)

**Lead, phase 1 (now).** Spec T-A and T-B from §1-§4 (T-A: `Review: on merge`, `Model: opus`); dispatch both. After
T-A merges: T-C, T-D, T-E (∥). Rewrite `docs/design.md` "Test tiers and receipts" (`:536-572`, lead-owned) to the
new contract when T-A and T-E land: tests in the lane step, the slot compiles, no receipts, no kept moves. Watch for
three days: `obor status` running list, `flakes.jsonl` `retried`, needs-you with "lane dirty".

**Lead, phase 2.** Spec T-G and T-H with the owner's N and branch names (default 6, `staging` → `main`); dispatch T-H
first (∥ T-G). Write `docs/UPGRADE-v0.30.md`: adopters without `branches.promote` see no change; with it, the owner's
switch below.

**Owner, one-time switch (after T-G and T-H merge, at a quiet queue).**

1. `obor pause "staging switch"`, wait until `obor status` shows no task in `candidate`, `review`, `publish` or
   `testing` (dispatched and working tasks are fine: their lanes re-merge onto staging through the slot).
2. In the main checkout: `git checkout -b staging main` (staging = main at this moment; every open candidate's
   `Target` is still an ancestor, so publish fast-forwards as before). Optionally `git push -u origin staging`.
3. Config (`.obor/config.json`, then `obor config validate`, then `obor serve restart`):
   `branches.main: "staging"` (the loop's target; every reader follows it: `scratch.go:759-764`,
   `cmd/obor/serve_dispatch.go:70-72`, `flakes.go:243`), `branches.promote: "main"`, `merge.promote_weight: 6`,
   `slots.max: 4` (the pool bounds parallel lane tests; today's `1` would serialize them; the pool dir
   `/tmp/obor-check-slots` is shared with the other obor projects on the machine), `slots.cpus: 3` (→ `go test -p 3`).
4. `obor pause --clear`. The next pass promotes once staging has a merge (idle trigger), which builds `dist/obor`
   from `main` and restarts serve on it.

**Lead, after the switch.** Commits in the main checkout land on `staging` (docs, BACKLOG, specs' publish); they move
candidates by seconds. Never `git checkout main` in the main checkout (the loop pauses, T-425); cut releases from a
separate worktree at `main` or at a quiet moment. `git push origin staging main` when pushing (both refs; `main` is
moved by serve's `update-ref`, never checked out). A RED promotion is one line and one decision: a fix task (spec
`Fixes:`-less, ordinary) or a revert task; the needs-you ends at the next GREEN promotion. Green promotions are
silent. `docs/agents/worker.md:9-11` stays (workers test their own packages before `task_done`; the lane step is
serve's). The reviewer's cadence and `Review: on merge` count merges to `staging` (unchanged,
`internal/app/reviewonmerge.go:19-40`).

## 7. Decisions the spec of T-A should state (defaults I took)

- A **resolved** merge is tested once in the lane (`test.N`) and not re-reviewed: the integrator's own resolution is
  its review; the accept binds to the task's own diff. Reverted (T-973) for a first-gate conflict, whose resolve job
  carries only the conflict, not the spec, report or task diff: binding the accept there published an unreviewed task,
  so it gets a review job again (a moved conflict still keeps its accept). (Alternative: re-review after a resolution.
  Costs a job per conflict, ~19 per 165 moves on 11-05's volume.)
- A move is **never** tested, not even when the new commits touch the candidate's files (today's `keepFull`). The
  compile step and the promotion carry that. (Alternative: keep `keepFull`'s "shared file → full re-gate" as a test
  trigger: it keeps the three-way answer alive, which the owner asked to delete.)
- The guard command (`merge.guard`: `bash -n`, config validate, plugin JSON) runs in the lane step with the class's
  checks, not in the slot: the slot is `compile` only.
- The lane step takes a **pool slot** (prio `worker`), so `slots.max` is the owner's knob for parallelism; the slot's
  `gate` keeps the merge marker (seconds).
- `branches.main` keeps its name and gains the value `staging`; the new key is `branches.promote`. Renaming the key to
  `branches.target` would be truer and touch ~12 readers plus every adopter's config; not worth it now.
