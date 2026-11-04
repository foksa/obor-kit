# Questions for the lead

- T-647 adds `obor setup show`; `docs/design.md` §Setup (`:140`, lead-owned) lists the setup subcommands — add `show`
  (and `rename`, missing there too). No answer needed, just a doc line when you next touch it.
- T-810 adds `--no-daemon` to Codex's base args; `docs/design.md` §Models (`:105`, lead-owned) lists them
  (`codex -s workspace-write -a never`) — add `--no-daemon` there. No answer needed, just a doc line.
- T-822: a review cycle now also records its covered tasks and review start sha in the close commit's body
  (`Covered:`/`Start:`), and the scope/`review due`/release check read them — `docs/design.md` §merge hold (`:336`) and
  `docs/state-model.md` row 52 still describe the sha-range-only scope and the single `git log <prev sha>..HEAD` read.
  No answer needed, just doc lines when you next touch them.
- T-841: a non-lead may no longer move HEAD or change the tree/index in the main checkout (`checkout`/`switch` of a ref,
  `reset`, `rebase`, `merge`, `stash`, `cherry-pick`, `revert`, `am`, `apply`, `clean`); `docs/design.md` §Hooks
  (`:255`, lead-owned) lists the guard's git rules without it. No answer needed, just a doc line when you next touch it.
