# Questions for the lead

- T-1117 (the herdr plugin `obor.events` is gone): lead-owned `docs/design.md` still describes it — §"After a herdr
  restart" (`:159`: "obor's `[[startup]]` hook (`obor plugin restore`) runs `obor restart` …"), §Setup (`:222`/`:226`
  lists `obor.events` and `obor setup herdr-plugin` among init's steps; `:231` says "`herdr-plugin` links obor.events
  in herdr from `<kit>/herdr-plugin`") and §herdr integration (`:607`). Proposed: the restore is serve's alone (it
  restarts its project's labelled panes once its terminal connection is back, T-1051, T-1117); `herdr-plugin/`,
  `obor plugin restore` and init's events step are gone, and `obor setup herdr-plugin` now unlinks the plugin an
  older obor linked. No answer needed, just doc lines.

- T-1116 (a person with no obor role may push): lead-owned `docs/design.md` §"Git hooks layer" (`:419`-`:422`) still
  says `pre-push` refuses unless `OBOR_ALLOW_PUSH=1` (the owner by hand) or the binary says the caller may push, and
  that its refusal names both ways out. Proposed: `pre-push` lets a shell with no `OBOR_ROLE` push (the block is for
  agents, T-1116); a caller with a role asks `obor whoami --can-push` (`config.MayPush`) as before, and
  `OBOR_ALLOW_PUSH=1` is gone (also from `scripts/ship`, which is the owner's command in the main checkout). No answer
  needed, just doc lines.
