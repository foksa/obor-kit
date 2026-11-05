# Upgrading an obor project from v0.27 to v0.28

## v0.28.0 (fewer gate runs, workers come back, `Review: on merge`, MCP on the Go SDK, one task shape)
Upgrade from v0.27.x. Close an open review first (`obor review close`). Re-run the installer, then in every project:

```sh
obor config validate      # names any removed key you still set: delete it
obor stop --force "v0.28" # stops serve and every agent; open tasks stay open
obor start                # serve on the new binary; workers of working tasks come back with their job
```

Restart the agents too (the stop/start above does it): the session fact's wire shape changed (one `store.Session`),
and an agent's MCP server is the binary it started with. Restart a running `obor web` as well: it does not follow a new
binary the way serve does, and the page reads the new `--json` shape.

What changes:
- **Removed config keys:** `merge.timeout_grep`, `merge.max_load`, `merge.load_wait`, `merge.window_wait`,
  `slots.merge_block_max`, `integrator_handoff_tokens`, `stop_idle_workers`, and the empty `mcp` and `cache`
  sections. The load-aware retry they drove is gone.
  `obor config validate` warns for two minor releases; delete them.
- **Gates:** after a clean rebase onto a moved target, a green candidate keeps its result when the new commits change
  none of its files, their affected Go packages and its own share none, neither side needs the full suite, and its last
  gate was green on an ancestor (`kept <base>`, a partial merge). Overlap is computed on Go packages only: the nightly
  full check covers non-Go readers.
- **`obor stop --force` drops nothing:** open tasks continue at `obor start`; a working task whose worker is not running
  gets it back with its job. A project pause (`obor pause`, any reason) holds that restart.
- **`Review: on merge`:** a spec line (between the title and the first `##` heading) that makes serve record a review
  request once the task merged and no request is open; it waits for an open review's close like any request.
  `obor spec set` refuses `Review:` with another value.
- **`obor review verify` by hand is gone:** link fixes with `obor review fix`; serve runs the verify.
- **`obor status --json`:** `tasks` is a map of task entries keyed by id (title, state, phase, lane, attempt, job …);
  `lanes[]`, `needs_you[]`, `queue.merge[]` and `queue.waiting[]` name the task by id and keep only their own fields;
  `lanes[].free` is new. A script that read task fields from those sections reads `tasks[<id>]` now.
- **`obor status --all` is gone.**
- **Review close commits** say `accepted <n>` when the close carries `--accept` lines; older `approved` subjects still
  parse.
