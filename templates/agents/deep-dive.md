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

## Rules
- Read-only on source; probes in a scratch dir. Fewer, verified findings beat many guesses. No subagents.
- Runs in its own role pane in the main checkout (`obor setup role deep_dive`, `OBOR_ROLE=deep_dive`), never a worker
  lane: obor never stops it as idle and `obor dispatch` refuses it. Serve holds merges while it works (30 min at most),
  so the code does not move under it. The lead briefs it with `obor brief <agent> "…"`: a brief to the stopped agent
  starts it fresh; a running one is first `obor restart <agent> --stop`, then `obor brief`. Follow-ups go to the same
  session the same way.

## This project
<!-- fill in: model/kind, areas that deserve emphasis, where the reviews live. -->
