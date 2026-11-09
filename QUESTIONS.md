# Questions for the lead

- T-1117 (the herdr plugin `obor.events` is gone): lead-owned `docs/design.md` still describes it — §"After a herdr
  restart" (`:159`: "obor's `[[startup]]` hook (`obor plugin restore`) runs `obor restart` …"), §Setup (`:222`/`:226`
  lists `obor.events` and `obor setup herdr-plugin` among init's steps; `:231` says "`herdr-plugin` links obor.events
  in herdr from `<kit>/herdr-plugin`") and §herdr integration (`:607`). Proposed: the restore is serve's alone (it
  restarts its project's labelled panes once its terminal connection is back, T-1051, T-1117); `herdr-plugin/`,
  `obor plugin restore` and init's events step are gone, and `obor setup herdr-plugin` now unlinks the plugin an
  older obor linked. No answer needed, just doc lines.
