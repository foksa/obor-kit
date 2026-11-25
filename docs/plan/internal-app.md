# internal/app: the agreed path (owner 11-25)

Two advisors (gemini_advisor, Fable) synced on this path; reasons, evidence and file:line in
`docs/research/internal-app-architecture-fable.md` §4 (task wording) and
`docs/research/internal-app-architecture-gemini.md` "Sync round 1" (its replies). Owner approved 11-25: run in
waves, file-split tasks alone in the package. Each task deletes what it replaces. Not on the path: `restore` or
`watchdog` subpackages, feature packages for the merge-loop core, an audit task, a helper-dedup wave, a second rig.

| # | Wave | Task | Task id |
|---|---|---|---|
| 1 | A | Every fact keeps the lanes table current (`fact` folds with `foldRecord`) | T-1575 |
| 2 | A | `laneReady` waits on `p.log.Changed()` instead of a 25 ms poll | T-1576 |
| 3 | B | One post-fold function for every appended record, no options; the dead reject branch goes | T-1577 |
| 4 | B | Rigs inject through it; one `App.do` contract test (SM-03) | T-1578 |
| 5 | C | Record data types live in `event` (`event` → `port`) | T-1580 |
| 6 | C | `internal/app/describe` leaf; `internal/view` and `internal/web` stop importing `internal/app` | T-1581 |
| 7 | D | A pass is one ordered list in `advance`; `doneRoles` becomes a rule | T-1583 |
| 8 | D | `socket.go` for the request entry; one lead-or-owner admission helper | T-1584 |
| 9 | D | Files along the flows (`messages.go`, `fetch.go` split; misnamed files folded); alone in the package | T-1585 |
| 10 | E | A dispatched task's stage is one derived function; one `halt` entry | T-1586 |
| 11 | E | `verifyRule` only reads (its read a named pass step) | T-1587 |
| 12 | E | One step table (`busy`, `stepPhases`, `commandStep`) | T-1588 |
| 13 | F | Measure where the package's test time goes (no code) | T-1589 |
| 13a | F | From the measurement: wire deadlines injected; rigs own home, no per-tick config reload; negative waits in bubbles; a flaky test | T-1590..T-1593 |
| 14 | F | One fake per port (a task per port) | |
| 15 | F | One level per behaviour, per topic, largest first | |
| 16 | G | `internal/app/activity` leaf (optional) | |
