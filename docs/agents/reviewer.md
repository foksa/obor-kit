# Role: reviewer — obor-kit specifics

Role text: `templates/agents/reviewer.md` in the obor kit (the session start names its path).

- ob-terra (Codex `gpt-6.1-terra` high, owner 2026-10-31; was `gpt-6.1-sol`) in its role worktree
  `~/Work/obor-kit-wt/reviewer` (T-853; T-718 had it in the main checkout). Reviews go to the main checkout's
  `docs/reviews/` (gitignored, never committed; its Codex start adds that dir with `--add-dir`); contracts:
  `docs/design.md`.
- Focus: bash portability (3.2 vs 5, BSD vs GNU), hook false positives / negatives, quoting, behaviour when herdr or
  jq is missing; idiomatic Go in the ports.
- Tiers (T-350): probes and the tests a finding names (a verify re-runs the fix's tests only); no suite run, no
  `-count` loop.
