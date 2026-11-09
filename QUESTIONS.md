# Questions for the lead

- T-1116 (a person with no obor role may push): lead-owned `docs/design.md` §"Git hooks layer" (`:419`-`:422`) still
  says `pre-push` refuses unless `OBOR_ALLOW_PUSH=1` (the owner by hand) or the binary says the caller may push, and
  that its refusal names both ways out. Proposed: `pre-push` lets a shell with no `OBOR_ROLE` push (the block is for
  agents, T-1116); a caller with a role asks `obor whoami --can-push` (`config.MayPush`) as before, and
  `OBOR_ALLOW_PUSH=1` is gone (also from `scripts/ship`, which is the owner's command in the main checkout). No answer
  needed, just doc lines.
