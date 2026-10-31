# Role: lead — obor-kit specifics

Role text: the `obor:lead` skill (`/obor:lead`). This file holds only what differs in obor-kit.

## Project
- obor-kit is a Claude Code plugin; contracts in `docs/design.md` (lead-owned: read it at the start; change it before
  dispatching dependent tasks); config keys: `config.Data`'s field comments (`internal/config/config.go`). Specs from
  the kit's `templates/task-spec.md`. Changes to how a role works need the user's approval.
- Main checkout `~/Work/obor-kit` (branch `main`); lanes `~/Work/obor-kit-wt/<LANE>`; who runs where:
  [roster.md](roster.md). The `obor` on PATH is the stable checkout: in obor-kit use `./bin/obor` for main's code.
- Milestone end: tag `m<n>-<name>` after the deep dive's easy findings are fixed and verified.
- Integrator: ob-int (`roles.integrator`) since 2026-10-13. The lead never merges by hand (user: "why are you doing
  integrators job?" after finished lanes sat unmerged); `obor serve` gates a finished lane and sends ob-int its review
  job (`obor status --facts` shows the task move through gate → review).
- A merge-loop needs-you of obor serve (`obor status --facts`: held, job not delivered/stalled, red past K, publish,
  after_merge, scratch busy) ends with `obor resume <T-xx> [regate|reprompt] ["<decision>"]` once its cause is dealt with (a held review: put the
  decision in the text; it reaches the integrator's next job; open questions: the decision is required and reaches the
  worker too).
- axis-alies (`~/Gamedev/axis-alies`) is source material only, read-only for obor-kit; its lead is the Claude session
  `axis-alies-30` or its successor; changes go to it as a change list.

## Panes (user, 2026-10-24)
- The lead's tab (`lead`) holds only the lead pane: never split it for another agent (the user types into the wrong
  pane). obor-kit's tabs: `lead`, `opus lanes` (O-01..O-04), `small lanes` (S/H/C/D), `roles` (integrator, reviewers,
  advisor), `system` (obor serve). New role panes go into `roles`, at most 4 panes per tab, laid out as a 2×2 grid (2 panes side
  by side, 3 = one left and two stacked right), never four stacked rows.
- Every agent run is an interactive agent in a herdr pane, never headless (`codex exec`, `claude -p`) or an in-process
  subagent.

## Models and limits (user)
Opus by default for anything non-trivial; Sonnet only for small, well-specified tasks and docs; Haiku for simple or
repetitive bulk tasks with a mechanical oracle (review-finding fixes, small follow-ups, test-only changes with a
concrete spec: as rigorous as Opus there); Codex Luna sparingly (user 2026-10-13: "let's slow down on luna tasks,
not worth it"): only small mechanical tasks with a complete oracle; Fable advisor (`ob-fable`) for hard-to-reverse
decisions and the second strike of a fix chain; Sol for batch reviews (the expensive Codex part: targeted, few). Max 4 Opus workers, max 4 panes per tab.

- Difficulty (owner 2026-10-30, after 3 of 8 "hard" tasks in one day were medium): hard = state machines,
  crash/replay, concurrency, a cross-package redesign. Wide but mechanical (a rename with a script), one function with
  a full test table, or a probe plus a mapping onto existing calls is medium, however risky its deployment: the
  lead carries the deploy risk, not the lane's model.

## Merge classes
- Before committing `.obor/config.json` on main: `go test ./internal/checks -run TestHerdKitConfig ./internal/config`
  (it reads the real config; a direct merge-class commit once broke main), or send the change through a lane.
- A spec that adds a script or test lists `.obor/config.json` in its files and adds its own merge-class entry + check
  (the classify audit fails otherwise; committing the class on main ahead of the merge names a test that doesn't
  exist yet and turns every other merge RED).

- No "Files you may touch" enforcement (T-496): no guard deny, no gate FAIL, a spec without the section dispatches; a protected file a task must edit goes under "Protected files this task may edit" (exact paths).
- `obor serve` is the only dispatcher: `obor dispatch` is a request to it (`DISPATCHED … (queued)`; `obor resume`
  retries a needs-you) and `obor drop` is serve's (it ends the lane's task and frees the lane).
- A spec that adds code nothing calls yet (an adapter before its wiring) lists `Makefile` and allows deadcode
  exclusions marked "until T-xx wires it; remove then"; the wiring task removes them (T-341 hit the gate without it).

## Reviews
Review files stay in the review worktree (`~/Work/obor-kit-wt/review/docs/reviews/`), never in main (owner
2026-10-30); delete them there when the cycle closes, by file name (an `rm -rf <review>/docs` once deleted tracked
docs there).

## Releases (owner 2026-10-30, v0.16)
- Public repo `foksa/obor-kit`; users install with `install.sh` into `~/.obor/kit` (the newest tag), marketplace
  `obor-kit`. This checkout is marketplace `obor-kit-dev` (`obor@obor-kit-dev` here), so both live on this machine.
  axis-alies still runs `~/Work/herd-kit-stable` (v0.15.0) until it migrates to `~/.obor/kit`.
- Tag only after the merges since the last tag are reviewed and verified, and `make check` is green. Steps
  (`RELEASING.md`): on main `VERSION`, plugin `version` + `docs/UPGRADE-v<x.y>.md` (committed); in a scratch worktree at
  main set marketplace `name` `obor-kit`, `make release`, commit with `bin/checksums.txt`, `git tag -a v<x.y.z>`; copy
  `bin/checksums.txt` to main; `OBOR_ALLOW_PUSH=1 git push origin main v<x.y.z>`; the owner runs `make publish` in the
  scratch worktree (the classifier refuses the lead's upload); check `install.sh` in a scratch HOME. A pushed tag
  never moves. Then `obor send <project> "<what changed>"` to the adopting leads.
- Before each tag: a Codex smoke on C-01 with main's code (`~/Work/obor-kit/bin/obor dispatch <smoke T-xx> --types luna
  --force`: the job arrives over MCP `job_next`, nothing is typed into its input, the worker finishes with `task_done`; spec
  like T-461; the gate runs in serve's scratch, not the sandbox, and `obor run check` fails in Codex's sandbox: unix
  socket binds denied, 2026-10-27).
