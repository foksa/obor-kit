# Upgrading an obor project from v0.17 to v0.18

## v0.18.1 (patch)
Re-run the installer, then `obor serve restart` in each project. No config change.
- **Built with Go 1.27.3**: `go.mod` pins `toolchain go1.27.3`, which fixes five net/http server issues govulncheck
  reports for go1.27.1 (reachable through `obor web`; it serves plain HTTP on localhost, so no real exposure) (T-681).
- Kit maintainers: every GitHub release gets the title `obor <x.y.z>` and the notes of `docs/releases/v<x.y.z>.md`,
  plus a Full Changelog link; `make release` and `make ship` refuse without that file (T-682).

## v0.18.0 (monitoring)
Re-run the installer, then `obor serve restart` in each project (the store adds its `prompts` table at start; nothing
to migrate by hand). Restart the monitor in each project's `system` tab (Ctrl-C, `obor monitor`) for its new look;
the lead picks up the new skill text at its next session (a break, `obor restart <lead> --fresh`).
- **`obor web`**: a read-only page at `http://127.0.0.1:8787/` for every obor project on the machine: what needs you,
  work in progress, the queue (merge pipeline, waiting dispatches, review cycle, watches), events, and per task its
  spec, report, gate, timeline and the prompts its agents got; per agent its sessions, briefs, lines and jobs. Run it
  in a pane; localhost only, no verbs (T-667, T-675, T-676).
- **Serve records what it sends**: the text of every job, brief and start prompt, one row per distinct text, kept 14
  days (T-665, T-670).
- **One read model** (`internal/view`) behind `obor status` (text and `--json`), `obor monitor` and `obor web`;
  `--json` adds `roles`, per-lane `spec_rev` and `job`, `events` and a `queue` section; the queue states facts (who
  holds the merge scratch and why, each task's stage), never a predicted next task (T-664, T-669, T-673).
- **`obor monitor`** looks like the agents' UIs: sections, state colors, a spinner; draws on the alternate screen, so
  it fits the pane and never scrolls your scrollback; shows tasks waiting for a lane (T-663, T-664, T-668).
- `running:` lists only work executing now, with the command actually running; stuck work shows as `pending`
  (T-662, T-669, T-673).
- **`obor watch T-a,T-b --note "<what to do>"`**: the merged line carries the note (T-666).
- **Role agents stop when done**: serve stops the integrator after each job (every job starts it fresh) and the
  reviewer when its review closes; the lead stops the advisor and deep dive (`obor restart <agent> --stop`); a brief to
  a stopped role agent starts it fresh; status shows them `stopped` (T-672, T-677).
- **Shorter worker and integrator jobs**: each rule said once; the job names the project's configured report sections
  (T-674, T-678).
