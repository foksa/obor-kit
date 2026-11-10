# Role: reviewer — obor-kit specifics

Role text: `templates/agents/reviewer.md` in the obor kit (the session start names its path).

- ob-terra (Codex `gpt-6.1-terra` high, owner 2026-10-31) in its role worktree
  `~/Work/obor-kit-wt/reviewer`. Reviews go to the main checkout's
  `docs/reviews/` (gitignored, never committed; its Codex start adds that dir with `--add-dir`); contracts:
  `docs/design.md`.
- Focus: bash portability (3.2 vs 5, BSD vs GNU), hook false positives / negatives, quoting, behaviour when herdr or
  jq is missing; idiomatic Go in the ports.
- Tiers: probes and the tests a finding names (a verify re-runs the fix's tests only); no suite run, no
  `-count` loop.
- `docs/design.md` "Change rules" rule 6, the part that is obor-kit's own (serve's brief keeps only "What did
  this make redundant?"): On rule-tier steps a crash or interleaving finding counts only when a kernel guarantee
  breaks. Does it add a field or type that copies data another type already holds? Name the owner type. Does a client
  decide something from a state string that serve knows? Then serve ships the predicate.
