# The default test-time flow for an adopter (advisor, 2026-11-09)

Question (owner 11-09, BACKLOG.md:35): what should obor give an adopter by default: what the lane gate runs, what runs
at merge, what runs nightly, whether promotion exists by default and as what, what `obor init` writes, what can go,
and what changes for obor-kit.

## Recommendation

**Two clocks by default, per merge and per night; the per-batch clock (promotion) is off and stays opt-in.** No new
mechanism: this is today's code with promotion unset, plus one init default and a few doc lines.

| When | Runs | Key | Default |
|---|---|---|---|
| Merge slot (seconds) | merge onto `main`, then the compile step if set | `commands.compile` | unset: merge only |
| Lane test, per merge | the project's merge check on the candidate, one retest of a red | `commands.check` | detected by init |
| Nightly, on `main`'s head | `check` again (covers moves), then the slow tier | `commands.check_milestone`, `merge.nightly_at` | init writes `"02:00"`; `check_milestone` null |
| Promotion | nothing | `branches.promote` | unset |

1. **`commands.check` means "what every merge must pass", not "the full suite".** That is how it already runs for an
   adopter: without `merge.guard` every lane is `full:no-guard` and runs `check` (internal/checks/classify.go:48-49,
   runner.go:432-438). axis-alies uses it exactly so: `check` = `check:engine` (short), `check_milestone` =
   `check:engine:nightly` + soak (its `.obor/config.json`; AGENTS.md "Checks"). design.md:529 still says "`check` is
   the full suite", which is true only for obor-kit (classes narrow its lanes). The adopter rule in one line: keep
   `check` to a few minutes at the lane's CPU budget; what is slower goes in `check_milestone`.
2. **Target stays `main`, promotion unset.** Both are the defaults today (internal/config/config.go:364, 93-95).
   Nothing to build.
3. **`obor init` writes `merge.nightly_at: "02:00"` and `commands.check_milestone: null`** beside the `check` it
   detects (internal/setup/oborinit.go:369-420, init.go:166), and its summary names the rule of point 1. Without a
   nightly a default adopter has no test of the combined head: a moved candidate publishes untested when `compile` is
   unset. The nightly is that test, non-blocking, one needs-you on RED. `obor nightly off` for a project whose CI on
   `main` already does it.
4. **Classes, the guard and `compile` stay opt-in refinements**, added when `check` outgrows a few minutes. obor-kit
   is the worked example, not the default.

## Options, and why not

- **A (recommended): the table above.** Zero prod lines in the loop; init +2 keys; docs.
- **B: promotion on by default, moving a separate ref** (`branches.main: main`, `branches.promote: green`). The config
  allows it today (validate.go:339-347 only asks for a branch other than the target). Why not:
  (1) a promotion runs `commands.check` (internal/checks/promote.go:76), the same command a default adopter's lane
  just ran, so it adds a batch-head run and no coverage; axis' extra coverage is `check_milestone` (15 min + a soak up
  to 4 h), which fits a night, not every 6 merges.
  (2) Setting `branches.promote` changes three more things than a ref: the nightly drops `check` (nightly.go:79-82),
  lanes get `OBOR_PROMOTION=1` (lanetest.go:86-88), and **`after_merge` stops running per publish and runs in the
  promotion worktree** (internal/app/steps.go:62,75; promote.go:50-51). axis' `after_merge` is `bun install` on a
  `bun.lock` change in the main checkout: under B it would install into the wrong worktree. That coupling is
  obor-kit's "serve's binary follows main", built into the kit.
  (3) It waits for a closed review (promote.go:21-38), so the green ref lags by a review cycle; 11-07 showed the cost
  (BACKLOG.md:78-80: six reruns, serve's own fixes stuck behind it).
  If an adopter ever asks for a green ref: first split (2) off (a ref move only; `after_merge` stays per publish), then
  recommend `promote: green`, never renaming `main`.
- **C: staging as the adopter default.** Rejected by the question's own premise: `main` is where CI, the installer and
  people look; the branch existed to limit test time, and test time is now limited in the lane.

## What can go or become optional

- **Promotion is the open one, and it is the owner's call.** It is 852 prod lines (internal/app/promote.go 627,
  internal/checks/promote.go 225) and ~1,600 test lines, off in the only project that had it on, with three known
  status bugs parked in `keep/T-988` (BACKLOG.md:78). Its reason was lane tests of ~10 min (docs/plan/
  test-suite-rework.md:15); the lane median is 40.5 s now and the 11-09 nightly ran the full suite GREEN
  (`.obor/state/last-full-check`: check 37 pass, the whole nightly 218 s). What it still gives is "`main` is
  full-suite GREEN and reviewed", which obor-kit gets at each release (`make release` runs the suite and moves `main`,
  RELEASING.md:26, 51-52). Recommendation: keep it one release as opt-in; if obor-kit has not turned it back on by
  v0.33, delete it with `branches.promote`, `merge.promote_weight`, `merge.weights`, `OBOR_PROMOTION` and the
  nightly's `check` exception (owner decision 11-07 "kept for obor-kit" is then reversed on record). Until then no
  adopter is told about it.
- **`check_fast`** in templates/config.example.json:18 and design.md:110: no code reads it; it suggests a tier obor
  runs. Drop it from the example. axis' own `check_fast` equals its `check`.
- **`baseline`, `gate` (with `paths`, `at`, `OBOR_GATE`) and `e2e`**: neither project sets one; `check_milestone` and
  `merge.nightly_commands` do the same job. A deletion candidate (runner.go:335-345, 461-487; nightly.go:83-97;
  init.go:166 writes two of them as null). Flagged, not sized: separate question.

## What changes for obor-kit

Nothing in the flow. It already is the default shape with refinements: classes and `check_affected` in the lane
(importers included while promotion is off, lanetest.go:81-88), `compile` in the slot, the full `check` at night.
`branches.main: staging` with `main` moved at release is a naming choice RELEASING.md owns, not a kit mechanism; moving
back to `main` would reopen T-1035's release steps for no test gain.

Small follow-ups (one simple task, docs and init only):
1. init: write `merge.nightly_at` and `commands.check_milestone: null`; the summary line of point 1; test changed:
   `internal/setup` init's golden config.
2. Example config: drop `check_fast`; add `check_milestone` and `nightly_at`.
3. design.md:529 (lead-owned): "`commands.check` is the merge check; a project with classes runs it only on a full
   lane and at night".
4. obor-kit's config: drop `merge.promote_weight` (read only while `branches.promote` is set, config.go:286-288).

## Risks and not verified

- **A default adopter's `check` may already be slow** (init detects `make check` / `go test ./...` / the `check`
  script, whatever its length). The flow does not fix that; the summary line and the lead's first session do. No
  measurement of a third project exists.
- **The nightly needs serve up at 02:00.** A day with no result runs none later unless serve was restarted with the
  intent pending (internal/app/nightly.go:20-23); I did not trace what a serve started at 09:00 does for that day.
- **Moves without `compile`** publish with no check until the nightly. Accepted for obor-kit with a compile step
  (0 real reds in 165 moves, docs/research/merge-without-regate.md §1); for an adopter without one the number is
  unknown. If nightly REDs from moves show up, the answer is `commands.compile` (a typecheck), not promotion.
- axis-alies was read, not run; whether its `main` has outside CI was not checked.
