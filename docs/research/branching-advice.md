# Feature branches, a staging branch, or a gate tier? (advisor, 2026-11-07)

Question (owner, BACKLOG.md:28-29): tasks merge into feature or staging branches with only the essential tests; the
full suite runs when a branch merges to main; features run in parallel; no release branches. Worry: the lead's load.
Lead's counter: one continuously promoted `staging` first. Numbers: docs/research/gate-time.md.

## Recommendation

1. **One `staging` branch, promoted to `main` by serve. No feature branches.** The merge loop keeps every
   mechanism it has; only its target branch changes (it is one config value, `branches.main`, read in
   `internal/checks/scratch.go:760-765` and `cmd/obor/serve_dispatch.go:70-72`), and one step is added: *promote*,
   which is the nightly's `check` step run on a trigger (`internal/checks/nightly.go:15-27,77`) plus a fast-forward
   of `main` to the checked sha. A red promotion re-runs its failing tests once (flakes are 15-19% of gate time,
   gate-time.md:55-59), then names the culprit by a first-parent bisect over at most N merge commits and leaves `main`
   where it was. Nothing in the lead's day changes on a green promotion (clean merges stay quiet, docs/decisions.md
   "Lead noise"); a real red is one line and one decision (fix task or revert), the same decision a nightly RED asks
   today (`internal/app/nightly.go:21-24`).
2. **Why not the tier alone (option C).** C gives fast merges but no solid main: main's only full-suite evidence is
   the 02:00 nightly, up to 24 h and ~70 merges late (`git log --merges`: 41, 67, 27, 72, 65 merges/day 11-01..05), and
   a RED nightly is a 30-60 min hand search for the lead (gate-time.md:121-124). That lag is also what stops C from
   tiering `internal/app`, the package that is the wall once the others are tiered (gate-time.md:115-117). A promotion
   every N merges bounds the search to N and makes the aggressive tier safe. C is still the first thing to build: it is
   the fast tier the staging shape needs, and it is a one-day test-only change.
3. **Why not feature branches.** Serve would need feature state (which tasks form a feature, when it is complete, who
   merges it, what its reviews cover): a new table and verbs, against change rule 3 (`docs/design.md:28-47`), and a
   conflict job per feature merge whose diff is the whole feature, not one task. "Features in parallel" exists today
   as parallel tasks on four lanes; the only thing a feature branch adds is dropping a feature as a whole, which no
   recorded case asked for. If one ever does (a store-schema change spanning several tasks is the closest, BACKLOG.md:221),
   it is the same promotion step with a branch parameter per spec, built then.
4. **Option D is subsumed.** "Never re-gate on a move, a post-merge check pauses merges on red"
   (docs/research/merge-without-regate.md:67,100) is exactly what the promotion is, except that the red lands on
   `staging` and `main` stays green. The T-914 compile-only re-gate stays in phase 1 (it costs ~10 s a move) and is a
   phase-3 deletion candidate once promotions show it catches nothing.
5. **Correction to gate-time.md:9-11.** The nightly *does* run the full `go test`: `nightly.go:77` runs the `check`
   command (`make -s check`), and `.obor/state/last-full-check` reads `GREEN check 33 pass, 0 fail, check_milestone
   rc=0, …`. So C needs no new nightly line, and the promotion reuses a runner that already exists.

## 1. The shapes against the owner's three goals

Per-merge check-slot time is what the serial gate costs (one `merge` slot, `internal/checks/runner.go:166-179`; the
nightly worktree takes the same slot, `checks.go:93-100`). Today's medians: 102 s (4-5 packages), 189 s (full), 144 s
over all (gate-time.md:27-31). A fast tier = build, gofmt, vet, deadcode (~5 s warm, gate-time.md:32) plus
`go test -short` on the affected packages: ~110 s with `internal/app` whole, under ~60 s with its ≥1 s tests
short-skipped (gate-time.md:115-117, estimates).

| Shape | Per-merge slot time | What `main` guarantees | A slipped break is found | New mechanism | Lead load added |
|---|---|---|---|---|---|
| Today | 144 s | nightly GREEN, ≤24 h old | ≤24 h, ≤70 merges to search | 0 | 0 |
| C, tier only (app whole) | ~110 s | same | same | ~60 test lines, 2 Makefile | a hand search per nightly RED |
| C with app tiered | ~60 s | same, more slips | same | same | more hand searches |
| **Staging + promote, N=6, app tiered** | ~60 + 190/6 ≈ 90 s | every `main` sha full-suite GREEN | ≤6 merges, ≤~25 min after the merge, culprit named | ~150-250 prod lines (§3) | ~0 green; 1 line + 1 decision per real red |
| Feature branches | as staging, plus a feature-merge gate per feature | as staging | as staging, plus cross-feature conflicts at feature merge | feature state, verbs, review scope | ≥3 decisions per feature, a conflict job per feature merge |
| D alone (never re-gate + main check) | saves ~10 s per move | a check of main pauses merges on red | at the check's cadence | ~40 lines (merge-without-regate.md:67) | pauses hit every task |

On a 70-merge day the staging row is ~70 min of fast gates plus 12 promotions of ~3 min: ~105 min of slot time, against
the 403-552 measured on 11-04/05 (gate-time.md:28; those include reds and the pre-T-914 moved re-gates, so the fair
comparison is per merge, above). Reds: the fast tier drops the `cmd/obor` subprocess and `checks` git tests that are
about half of `flakes.jsonl` (gate-time.md:118-120); the promotion's own flakes are filtered by its re-run.

Who consumes a solid `main` today: `install.sh` is fetched from `main` (RELEASING.md:6), `make release` is cut on
`main` and runs the full `make check` itself (RELEASING.md:20-25), lanes base on the loop's target
(`internal/app/dispatch.go:1240`), and serve's own binary is built at each publish (`commands.after_merge`,
`.obor/config.json`). With staging, lanes and serve follow `staging` (§2), releases and the installer follow `main`.

## 2. What serve owns in the staging shape, and what it deletes

**Branch state.** One new config key, `branches.promote` (the promoted branch; unset: no promotion, today's loop).
`branches.main` keeps its meaning, the merge target, and is set to `staging`; the main checkout sits on `staging`
(publish fast-forwards the checked-out branch and after_merge runs at its head, `scratch.go:593-605,648-660`;
`Loop.Checkout` pauses the loop off it, `loop.go:42-44`). `main` is never checked out by serve: the promotion moves it
with `update-ref` after an ancestor check (`scratch.go:767-771` has the helper). Lanes, reviews, the reviewer and
deep-dive worktrees, `obor send`, config_set and review-close commits all target `branches.main` already and need no
change. `internal/checks/flakes.go:243` hard-codes `"main"` as a fallback: one line.

**Promotion trigger** (a rule-tier condition in the pass, like `App.nightly` `internal/app/nightly.go:93-140`): at
least one merge since the last promoted sha, and either no gate pending or running (idle) or N merges since the last
promotion (`merge.promote_every`, default 6). Its intent is `promote.<seq>` (G, the nightly's class: a pending one runs
again after a crash, `nightly.go:21-22`). It takes the merge slot like a gate, so N bounds its share of the slot.

**Full-suite run.** `checks.Nightly`'s session in the `-nightly` worktree reset to the target head (`nightly.go:40-50`),
running `lane_setup` and `check` only. The 02:00 nightly keeps `check_milestone`, the backup and `nightly_commands`
and **drops `check`** when `branches.promote` is set: the deletion that pays for the new step (`nightly.go:77`,
`.obor/config.json` `nightly_commands`).

**Red promotion.** Step 1: re-run the failing tests alone at the same head (`go test ./<pkg> -run '<names>'`; the
runner already extracts the names for `flakes.jsonl`, `flakes.go:67-84`, and the record has `Retried`/`PassedOnRetry`
fields nobody in serve writes yet, `flakes.go:28-29`). Pass: the promotion is GREEN and the flake is logged. Step 2
(phase 2): bisect the first-parent commits `main..staging` (one merge commit per task, subject `Merge T-xx: …`,
`docs/state-model.md:43`; plus the lead's direct docs commits): log2(N) runs of the failing packages, ~1 min each.
Step 3: `needs-you{promotion RED <sha7>: TestX fails since T-xx (<sha7>)}` and its line, the nightly's shape
(`internal/app/nightly.go:21-24,219`); `main` stays. Merges to `staging` continue; the next promotion runs after the
fix merges. No automatic revert: later merges may build on the culprit, and the lead's revert task is one spec line.

**Sendback.** There is no worker to send back to: a merged task's worker is stopped and its lane freed
(`docs/state-model.md:323-324`). The line names the task and test; the lead dispatches a fix or a revert. That is the
same decision a RED nightly asks today, now with the culprit named and ≤N merges old.

**Serve's own binary** follows `staging`: `after_merge` (`make build`) runs at each staging publish as it runs at each
main publish today. The fast tier is no weaker than what gates serve's binary now: a partial gate tests the affected
packages only, and a clean move since T-914 compiles only (docs/decisions.md:9-13). The 11-07 outage was a by-hand build
of uncommitted code (BACKLOG.md:221), which no branch shape prevents. Later, on a trigger (a staging-only break takes
serve down): build at promotion in the promotion worktree into the root's `dist/obor` (an output path in the Makefile,
~5 lines) and drop `after_merge` from the per-merge loop.

**Reviews: unchanged, nothing counted per promotion.** The every-~3-merges review and `Review: on merge` count merges
to the loop's target (`internal/app/reviewonmerge.go:19-40` counts `stMerged`; `skills/lead/SKILL.md:90-95`), and the
reviewer reads the head serve hands it, now staging's. A review is about the code, not the tier it passed; a review per
promotion would be a second cadence with nothing deleted. Integrator per task: unchanged (`docs/state-model.md:306`).

**Moves.** T-900/T-914 stay in phase 1. Phase 3 may delete `keeps`' three-way answer (`scratch.go:330-372`) and the
Makefile's `OBOR_MOVED` branch (~60 lines) once promotions show the compile re-gate catches nothing: then a clean move
publishes at once and the promotion is the check, which is option D with main kept green.

## 3. The lead's load, concretely

Today's lead lines (docs/research/chatter-census.md:54-62, per day 11-01..07): needs-you 14, 7, 7, 6, 13, 6, 2;
reviewer finished 17, 32, 13, 23, 22, 13, 1; merge-watch 22, 44, 20, 30, 12, 2, 0 (now gone). Promotion adds:
- Green promotion: 0 lines, 0 decisions (quiet, as a clean merge). ~10-12 a day at N=6 on a busy day.
- Red promotion after the re-run: 1 line, 1 decision (fix task or revert; one spec line either way). Rate: unknown;
  the bound is today's nightly, 2 RED of 9 runs and neither a merge interaction (merge-without-regate.md:56-57), so
  under one a day, likely one or two a week once the tier skips the load-sensitive tests. Each saves the hand search a
  RED nightly costs today.
- Config: one decision, once (N and the branch names).
- What goes away: `obor nightly`'s `check` RED hand search; the question "is main broken?" (the answer is the promoted
  sha); `make release`'s own `make check` can be skipped when HEAD is the last promoted GREEN sha (phase 3, RELEASING.md:25).

Feature branches would add, per feature: the branch's open and base, the feature-merge's conflict job (the whole
feature's diff against everything merged since), the review's scope, and the merge decision: ≥3 lead decisions per
feature plus the cross-feature conflicts the owner's worry names. C alone adds the hand searches.

## 4. Phased plan

| Phase | Task | Difficulty | Lines (est.) | What it proves or deletes |
|---|---|---|---|---|
| 0, try first | **Tier the gate (gate-time.md option C), `cmd/obor`, `checks`, `setup` only.** `testing.Short()` skips on the ≥1 s tests, `-short` in `check-affected`'s `go test`; `make check` and the nightly unchanged (they already run everything). Also gate-time.md's recommendation 2 (a `_test.go` change affects one package, `internal/affected/affected.go:55-60`). | medium | ~60 test, 2 Makefile, ~15 prod | Measures the fast tier for real: gate median and reds over 3-4 days; counts nightly REDs caused by a skipped test, which is the staging shape's expected real-red rate. No branch yet. |
| 1 | **Promotion.** `branches.promote`, `merge.promote_every`; the trigger in the pass; `promote.<seq>` intent reusing `checks.Nightly`'s session with `check` only; GREEN → ff `main`; RED → re-run the failing tests once, then needs-you + line; the nightly drops `check` when promotion is on; `flakes.go:243`. Owner's one-time setup: `git checkout -b staging main`, config keys. `obor status` shows the promoted sha and merges since. | hard | ~150-250 prod, −10 (nightly's check) | A solid `main` with no change to the per-task loop. Try it with the tier of phase 0 before tiering `internal/app`. |
| 1b | **Tier `internal/app`** (its ≥1 s `TestLoop*`/`TestDrive*`/`TestWatch*` families, gate-time.md:65-67). | simple | ~60 test | The gate's median under ~60 s; safe now that a slip is ≤N merges old. |
| 2 | **Bisect on a real red**: first-parent walk `main..staging`, failing packages only, culprit in the line. Build only if phase 1 shows more than ~2 real reds a week where the lead could not name the culprit from the N merge subjects. | medium | ~100-150 prod | Names the culprit instead of the lead reading N subjects. |
| 3 | Deletions on numbers: `keeps`' moved re-gate and `OBOR_MOVED` (option D); `make release` skips `make check` on a promoted HEAD; `after_merge` moves to promotion if serve ever follows a broken staging. | medium each | −60, −5, ~+5/−10 | Each is a mechanism removed, each on a trigger from phase 1's log. |

Cheapest first: phase 0 is test-only, reversible by deleting the skips, and it answers the one unknown that decides
the rest (how often a tiered gate lets a real break through). Phase 1 is one task; the spec names the two config
keys, the intent, the fact, and the nightly's deletion (change rule 3).

## 5. Risks and what is not verified

- The per-merge numbers are the researcher's estimates from alone-times and the gate/alone ratio (gate-time.md:175);
  phase 0 measures them.
- A compile error on `staging` still turns every gate red until a fix merges (merge-without-regate.md:45-48): the
  fast tier keeps build and vet, so a worker's own gate catches its own compile error; only a move's interaction slips,
  and the T-914 compile re-gate stays for that in phase 1.
- Promotions share the merge slot: at N=6 and ~190 s each, ~12 promotions on a 70-merge day are ~38 min of slot time.
  The idle trigger moves most of them into gaps; the slot's load wait (`runner.go:33`, T-720) is the fallback.
- Serve runs staging's binary between promotions, which is today's level of testing, not better. Said in §2.
- Non-Go readers of files (`tests/upgrade`, conformance) stay with the nightly, as T-900 left them
  (merge-without-regate.md:96-97).
- Not sized: `obor status` lines for the promoted sha, and the web view. Small, inside phase 1.
