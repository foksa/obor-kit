# Observations (lead; read at session start, rewrite at each handoff)

Patterns about obor and how we work on it that a fresh session would otherwise rediscover. Curated, not a log: at
each handoff merge, update or delete items (an item goes when its cause is fixed). Keep it under ~40 lines. Decisions
live in `docs/decisions.md`, open work in `BACKLOG.md`, the lead's own working rules in its memory.

## Where we go in circles
- **A question becomes a survey becomes 5-10 tasks.** 11-05: data model, encapsulation, kit-specific, gates: the
  queue went ~7 → ~25 in one afternoon and the milestone lost its exit (owner cut it). Before a survey, name the
  milestone it serves and what it may add; default is "next milestone".
- **Fix chains on rule code.** RV-119 → RV-120 → RV-121: each verify found a regression in the last fix, always a
  consumer of a new state the spec did not list (unknown lane → watchdog timers; reviewEnd → idle). The spec template
  now asks for every consumer of a new state; check it before dispatch.
- **Size does not fall by itself.** v0.27.1 → v0.28.0: prod lines flat (+~100), tests +~2k. Coverage-based pruning
  lost scenarios (11-04: 4 restore rounds). What works: deleting a mechanism with its tests (T-866, T-870, T-877 −220).
  Ask every task for net prod lines; next milestone requires net-negative.
- **Replacing a mechanism grows; deleting one shrinks** (11-06, waves G-J): G (a lead step gone) −205, T-935
  (restart_serve gone) −50; I (close into serve) +103, H ~0, J1b (locks → one section) +46 with two regressions and a
  revert. Even the deep dive's measured estimate missed: the lock ordered verbs across git reads. Prefer tasks that
  remove a step or a mechanism outright; price a "replace X with simpler Y" at its crash and race paths, not its idea.
- **Old record shapes never go.** Replay starts at record 1 and every store still holds old shapes (census 11-05), so
  change rule 5 cannot delete a compat branch or its tests without a log snapshot (owner decision pending).

## Load is the hidden tax
- Flaky reds track machine load (30-58 on 10 cores), not the tests: ~12 different tests one or two times each on
  11-05. Fixing them one by one does not keep up. Sources found: every merge re-gated every waiting candidate (T-900),
  non-Go files ran the full suite (T-901), workers ran the class check beside the gates (worker doc 11-05), herdr
  0.9.5 burns ~75% of a core idle in its own loop (not obor's calls; sample 11-05).
- Re-gates after main moves were ~35% of serial gate time on 11-05 (41 moved + 12 resolved of 133 gates) and caught
  no real break in 12 days (docs/research/merge-without-regate.md); T-913/T-914 address it: re-measure.
- More lanes made it worse: 6 lanes → 5 of 6 sat finished, waiting for one serial gate. Gates are the throughput
  limit; measure gate time and gates per merge before raising lanes.

- **Split by files means grepping the files.** 11-05 evening: T-880, T-891, T-893 all edited
  `internal/view/setup.go`; the specs' prose did not say so. T-891 resolved the same conflict three times (each main
  move re-merged the submitted sha; T-913 now reuses the resolution).
- **A spec lists what it adds, so the task adds.** Pre-milestone specs (T-880 +199, T-893 +116) grew despite the
  net-negative Report line added at dispatch; the integrator does not judge size. Name the deletions in the spec.

## Premises to check before proposing
- The lead's premise was wrong twice on 11-05: E1 (integrator review "adds nothing": it sent back ~8% of merges) and
  "the next workers test main's MCP server" (agents run the installed kit's `obor mcp`, `~/.obor/kit/bin/obor`).
  Check processes/logs/code first; `ps` shows what actually runs.
- Reviewers rate mixed-version findings "High" from the window alone (RV-124 #1: new client vs old serve). Check
  the tags first (`git show v<last>:<file>`): a field no release shipped has no old reader, and the forward case needs
  someone running `obor stop` in the minutes before serve follows. Accept such findings unless the fix is ~free.
- Review closes said "approved" while verifies requested changes (hard-coded until T-909). Read the verify, not the
  close line.

## Local traps
- `obor pause` did not hold serve's worker-start rule (fixed T-908); `obor lead` before `obor start` leaves an
  unregistered lead (T-911); `obor web` does not follow a new binary (restart it after a merge that changes the view or
  an upgrade); raising `lanes` on obor web leaves the lanes `missing` until `obor setup lane`/`pane`.
- No priority verb: re-dispatching a waiting task sends it to the back (the only way to reorder; repeat `--after`).
  Check `obor status` before dropping: a "queued" task may already be on a lane (T-899 lost 20 min).
- `cmd | tail` hides a failed exit (a worker's timed-out check read as exit 0). The live conformance tier needs real
  sockets: reviewers' sandboxes cannot run it; the lead runs it in a pane.
