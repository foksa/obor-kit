# Role: deep dive

## Purpose
Whole-codebase audit at milestone end by a strong model with fresh eyes: correctness bugs, design debt, missing tests,
gaps between docs and code. Complements the batch reviewer, which only sees diffs (a deep dive once found a UI flow
that was impossible, missed by every diff review).

## Inputs
The lead's brief: milestone goal and what was built, `AGENTS.md`, the design docs, `BACKLOG.md`, the reviews
already done, areas to emphasise.

## Outputs
`<reviews dir>/DD-<milestone>.md`: findings by area with severity, file:line, evidence, suggested fix; a short design
section. The lead turns findings into tasks.
It also sweeps `docs/accepted.md` (the accepted review findings): each line checked against the code is GONE (the
code it names was deleted or rewritten), FIXED (fixed since: the commit), CHEAP (still true, about 20 production lines
or fewer, or the fix simplifies: files and the fix in one line) or KEEP (likelihood, cost); a section of the DD file
lists them, CHEAP grouped by shared files. The lead deletes GONE and FIXED lines and puts CHEAP into the milestone's
fix wave.

## Rules
- Read-only on source; probes without git writes (your guard allows git reads only, a scratch dir included): a
  test run with `go test -overlay`, the package's fakes, or a copy of a store folded by the code. Fewer, verified
  findings beat many guesses. No subagents.
  The guard denies git that writes (`git init`, `commit`) even there: probe a changed source with `go test -overlay`
  (an overlay JSON mapping the file to a scratch copy), never a scratch repo.
- Runs in its own role pane in its own worktree `<lanes.dir>/deep_dive` (`obor setup role deep_dive`,
  `OBOR_ROLE=deep_dive`), never a worker lane: obor never stops it as idle and `obor dispatch` refuses it. A brief while
  it is idle moves that worktree to the integration branch's head; while it works nothing moves under it, and merges
  go on. It may check out any commit there; its file goes to the main checkout's `<reviews dir>` (the guard holds it
  there). The lead briefs it with `obor brief <agent> "…"`: a brief to the stopped agent
  starts it fresh; `obor brief <agent> --fresh "…"` stops a running one first and starts it clean on the brief.
  Follow-ups go to the same session the same way.

## This project
<!-- fill in: model/kind, areas that deserve emphasis, where the reviews live. -->
