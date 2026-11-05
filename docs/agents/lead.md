# Role: lead — obor-kit specifics

Role text: the `obor:lead` skill (`/obor:lead`). This file holds only what differs in obor-kit.

## Project
- Session start (after the skill's step 4): read `docs/observations.md`, the lead's curated patterns (where we went in
  circles, the load tax, premises that were wrong, local traps). At each handoff rewrite it: merge, update, delete
  items whose cause is fixed; keep it under ~40 lines (owner 2026-11-05: new sessions lack the old one's experience).
- obor-kit is a Claude Code plugin; contracts in `docs/design.md` (lead-owned: read it at the start; change it before
  dispatching dependent tasks); config keys: `config.Data`'s field comments (`internal/config/config.go`). Specs from
  the kit's `templates/task-spec.md`. Changes to how a role works need the user's approval.
- Main checkout `~/Work/obor-kit` (branch `main`); lanes `~/Work/obor-kit-wt/<LANE>`; who runs where:
  `obor status` (or obor web). The `obor` on PATH is the stable checkout: in obor-kit use `./bin/obor` for main's code.
- Milestone end: tag `m<n>-<name>` after the deep dive's easy findings are fixed and verified.
- Integrator: ob-int (`roles.integrator`) since 2026-10-13. The lead never merges by hand (user: "why are you doing
  integrators job?" after finished lanes sat unmerged); `obor serve` gates a finished lane and sends ob-int its review
  job (`obor status --facts` shows the task move through gate → review).
- A merge-loop needs-you of obor serve (`obor status --facts`: held, job not delivered/stalled, red past K, publish,
  after_merge, scratch busy) ends with `obor resume <T-xx> ["<text>"] [--regate|--sendback]` once its cause is dealt with (serve picks the action
  from the state, T-775; a held review: the decision text is required and reaches the integrator's next job; red past
  K: say `--regate` or `--sendback`). A report's open questions go to the integrator's review job: it
  decides the in-goal ones and holds the rest for you (T-649).
- axis-alies (`~/Gamedev/axis-alies`) is source material only, read-only for obor-kit; its lead is the Claude session
  `axis-alies-30` or its successor; changes go to it as a change list.

## Panes (user, 2026-10-24)
- The lead's tab (`lead`) holds only the lead pane: never split it for another agent (the user types into the wrong
  pane). obor-kit's tabs: `lead`, `workers` (L-01..L-04), `roles` (integrator, reviewers,
  advisor), `system` (obor serve). New role panes go into `roles`, at most 4 panes per tab, laid out as a 2×2 grid (2 panes side
  by side, 3 = one left and two stacked right), never four stacked rows.
- Role agents done with their work are stopped (owner 2026-11-01: they hold memory): `obor restart <agent> --stop`
  quits the agent and keeps its labelled pane at a shell, as for workers; the next use is `obor brief <agent> "<text>"`
  (a stopped agent starts fresh on it; `--fresh` stops a running one first). A review start does it itself. Keep one
  only while its context is still needed (a reviewer until its verify, an advisor with a follow-up pending); stop it
  right after.
- Every agent run is an interactive agent in a herdr pane, never headless (`codex exec`, `claude -p`) or an in-process
  subagent, except a quick lookup inside the lead's own turn. Research goes to the researcher (`obor brief <researcher>
  "…"`; it writes only `docs/research/` in the main checkout, the lead commits; stop it when done), or, when the result
  should be reviewed and merged, to a worker lane as a task whose deliverable is `docs/research/<topic>.md`.

## Models and limits (user)
Opus by default for anything non-trivial; Sonnet only for small, well-specified tasks and docs; Haiku for simple or
repetitive bulk tasks with a mechanical oracle (review-finding fixes, small follow-ups, test-only changes with a
concrete spec: as rigorous as Opus there); Codex Luna sparingly (user 2026-10-13: "let's slow down on luna tasks,
not worth it"): only small mechanical tasks with a complete oracle; Fable advisor (`ob-advisor`) for hard-to-reverse
decisions and the second strike of a fix chain; Sol for batch reviews (the expensive Codex part: targeted, few). Max 4
panes per tab. No per-model caps (owner 2026-11-03, T-765): a model out of quota is paused (`obor pause --model <name>
--for 3h "<why>"`), and the level's next model runs.

- Difficulty (owner 2026-10-30, after 3 of 8 "hard" tasks in one day were medium): hard = state machines,
  crash/replay, concurrency, a cross-package redesign. Wide but mechanical (a rename with a script), one function with
  a full test table, or a probe plus a mapping onto existing calls is medium, however risky its deployment: the
  lead carries the deploy risk, not the lane's model.
- Effort beside the model (T-695, T-765): wherever a model is named (spec `Model:`, `--model`, `difficulty`,
  `roles.<role>`), `opus:high` is opus at effort high (the lead and deep dive run on `opus:high`). The config names
  models directly (built-in table in `internal/config/models.go`; `models.<name>` adds one); lanes are a count.

## Merge classes
- Before committing `.obor/config.json` on main: `./bin/obor config validate` (a merge's guard runs it too; a direct
  merge-class commit once broke main), or send the change through a lane.
- A spec that adds a script or test lists `.obor/config.json` in its files and adds its own merge-class entry + check
  (the classify audit fails otherwise; committing the class on main ahead of the merge names a test that doesn't
  exist yet and turns every other merge RED).

- No "Files you may touch" enforcement (T-496): no guard deny, no gate FAIL, a spec without the section dispatches; a protected file a task must edit goes under "Protected files this task may edit" (exact paths).
- `obor serve` is the only dispatcher: `obor dispatch` is a request to it (`DISPATCHED … (queued)`; `obor resume`
  retries a needs-you) and `obor drop <T-xx>` is serve's (it ends the task and frees its lane).
- A spec that adds code nothing calls yet (an adapter before its wiring) lists `Makefile` and allows deadcode
  exclusions marked "until T-xx wires it; remove then"; the wiring task removes them (T-341 hit the gate without it).

## Reviews
Review files are in the main checkout's `docs/reviews/` (gitignored: never committed, owner 2026-10-30); the reviewer
and the deep dive run in their role worktrees `~/Work/obor-kit-wt/reviewer` and `~/Work/obor-kit-wt/deep_dive` since
T-853 (serve moves each to the head it hands the role; merges never wait), the integrator in
`~/Work/obor-kit-wt/integrator` (its resolves in the merge scratch). `obor review close` deletes the cycle's files by
name. The old worktrees
`~/Work/obor-kit-wt/review` and `~/Work/obor-kit-wt/deep-dive` are unused: remove them with `git worktree remove`
once nothing runs there.

## Releases (owner 2026-10-30, v0.16)
- Public repo `foksa/obor-kit`; users install with `install.sh` into `~/.obor/kit` (the newest tag), marketplace
  `obor-kit`. This checkout is marketplace `obor-kit-dev` (`obor@obor-kit-dev` here), so both live on this machine.
  axis-alies runs `~/.obor/kit` since 10-31 (migrated, 54543f08).
- Tag only after the merges since the last tag are reviewed and verified, and `make check` is green. Steps
  (`RELEASING.md`, T-640): on main write `docs/UPGRADE-v<x.y>.md` (with the `v<x.y.z>` heading) and
  `docs/releases/v<x.y.z>.md` (the GitHub notes: a summary line, `- **Name:**` bullets, no task ids) and commit both;
  then `make release V=x.y.z` does the rest (version commit, full check, scratch worktree with marketplace `name`
  `obor-kit`, the four binaries and `bin/checksums.txt`, `git tag -a v<x.y.z>`, the catalog back on main). The owner
  runs `make ship` (push, `make publish`, `install.sh`, then one `obor send <project>` per other project, each telling
  its lead "obor v<x.y.z> is installed. <the release notes>"); the classifier refuses the lead's push. A pushed tag never
  moves. No `obor send` of what changed follows ship; the serve restarts do (RELEASING.md step 4). A release
  whose projects need a config change says so in its notes, and you `obor send` that change to the project's lead
  before shipping.
- Other projects' repos are their leads' (axis-alies lead 2026-11-02): never commit there yourself, not even an
  owner-approved config change; `obor send <project> "<the exact change and why>"` and the lead applies it. Hold a
  serve restart that needs the change until that lead confirms it.
- Before each tag: a Codex smoke with main's code (`~/Work/obor-kit/bin/obor dispatch <smoke T-xx> --model luna
  --force`: the job arrives over MCP `job_next`, nothing is typed into its input, the worker finishes with `task_done`; spec
  like T-461; the gate runs in serve's scratch, not the sandbox, and `obor run check` fails in Codex's sandbox: unix
  socket binds denied, 2026-10-27).
