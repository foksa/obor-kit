# Questions for the lead

- T-647 adds `obor setup show`; `docs/design.md` §Setup (`:140`, lead-owned) lists the setup subcommands — add `show`
  (and `rename`, missing there too). No answer needed, just a doc line when you next touch it.
- T-783: `docs/design.md`'s opencode-marker paragraph (`:616`) says `no-plugin` means a marker older than the lane row's
  dispatch; it now also waits `view.pluginSettle` (2 min) after the lane's last `agent.start` in the log, because the
  plugin checks in on the agent session's first event (a restart or a resume's start comes after the dispatch). No
  answer needed, a doc line when you next touch it.
