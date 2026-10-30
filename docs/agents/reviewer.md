# Role: reviewer — obor-kit specifics

Role text: `templates/agents/reviewer.md` in the obor kit (the session start names its path).

- ob-sol (Codex `gpt-6.1-sol`, user 2026-10-13: far cheaper than 5.6, keep it) in the review worktree
  `~/Work/obor-kit-wt/review` (detached). Reviews stay in that worktree, never in main; contracts: `docs/design.md`.
- Focus: bash portability (3.2 vs 5, BSD vs GNU), hook false positives / negatives, quoting, behaviour when herdr or
  jq is missing; idiomatic Go in the ports.
- Tiers (T-350): probes and the tests a finding names (a verify re-runs the fix's tests only); no suite run, no
  `-count` loop.
