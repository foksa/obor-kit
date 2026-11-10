# Untyped lanes advice (ob-fable, 2026-11-02)

## Recommendation

Yes, with two changes to the draft: keep `lanes` a map where an empty type means "any" (one code path, no
migration), and record the type chosen per dispatch in the lane row, because three readers take a lane's type
*after* dispatch and all read config today.

Relief exists now: `pickLane` tries the level's types in order, first free lane of each
(`internal/app/dispatch.go:443-451`), so `"medium": ["sonnet", "opus"]` would have sent today's medium tasks to
the idle opus lanes. The pool is still right: that spends opus on sonnet work; the pool runs sonnet work anywhere.

## 1. Sound, with three late readers of the type

Identity never uses the type: the guard and MCP know a worker by `OBOR_LANE` and the lane's cwd
(`internal/project/project.go:194-202`, `internal/guard/prompt.go:71-77`, `internal/mcp/mcp.go:235`); agent names
and labels come from the lane name (`internal/roles/roles.go:96-105`: `ob-l1`). A dispatch starts fresh
(`dispatch.go:807`) and fix jobs go to that session (`steps.go:337-340`), so a task never changes kind. A kind
change in a pane is handled: every other kind's start unsets OpenCode's model variable (`internal/config/config.go:645-660`, T-707).

Readers that must take the row's type instead of config's:

- **The start.** `port.Launch` carries no type (`internal/app/port/effects.go:33-41`); `Launcher.args` takes
  `lanes.<LANE>.type` via `AgentLookup` (`config.go:780-790`, `internal/launch/launch.go:709-716`). Add `Type` to
  `Launch` and `dispatchData` (`dispatch.go:319-321`); the launch's type wins.
- **`obor restart <agent>`** resolves the same way and resumes the recorded session (`launch.go:232-246`,
  `425-461`). Resume only when the recorded kind (`internal/store/sessions.go:21`) is the row type's kind, else fresh.
- **The view and prompt**: `Type` is config's (`internal/checks/lanetable.go:71`, `cmd/obor/status.go:177`), as is
  `prompt_extra` (`dispatch.go:993`). A free lane shows none.

Flag only: Codex's trust dialog is once per directory, so each lane pays it the first time a
Codex type lands there (`internal/launch/start.go:284-290`: the refused start is a needs-you); the Claude plugin link
must be in every lane (`dispatch.go:1001-1007`; `obor setup lane` links it).

## 2. Reuse N worktrees

Reuse. `lane_setup`, `lane_links`, the plugin link, Codex trust, pane and label are per directory
(`internal/setup/setup.go:4-21`), and the lane states (`free`, `dirty-free`, `working`, `done`) assume one that
persists. Per-task worktrees also mean a pane per task under `max_panes_per_tab`. Only the agent process is per task,
as now.

## 3. Caps and waiting

`lane_types.<type>.max` is enforced only by `obor setup pane`, counting running agents in lanes of the type plus role
agents of it (`internal/setup/agent.go:191-218`); dispatch never checks it. The cap at dispatch is new: open
dispatches whose row type is T (not merged, rejected, dropped) plus configured role agents of T, setup's rule kept.
Pool size is the lane count; a type without `max` can fill it, which is the S-01 fix.

Pick: for each type in the request's order (`Type:`, then the level's, `dispatch.go:159-195`), skip it at cap, else
the first free lane whose type is it or empty. A typed lane keeps today's meaning: a reservation.

Waiting changes once: an opus task used to wait for a lane nothing else could take; now simple tasks can keep
taking free lanes ahead of it. Offer each freed lane to waiting tasks oldest first, skipping those at cap
(`waitDispatch`, `dispatch.go:618-646`, runs per pass already). No per-type minimum: pin a lane when a reservation is
wanted. Quotas: a worker stopped on a usage limit holds its lane and continues by itself
(`internal/app/stopwake.go:15-43`); the cap counts it. Skipping a rate-limited kind is a later nicety.

## 4. Migration and shape

`"lanes": {"L-01": "", "L-02": "", "OH-01": "opus_high"}`. `LaneEntry` already unmarshals a string
(`config.go:187-192`); validate's `required` on lanes goes (`internal/config/validate.go:117-123`); `obor setup lane
<LANE> [<type>]`. No count form: names are worktree dirs, agent names and `lanes.<LANE>.agent`.

axis-alies (D-01..D-04, OH-01), axis-maps (O-01, S-01, D-01, D-02) and codex-image-tool change nothing: typed lanes
stay pinned. To pool, blank a type (`obor config set lanes.D-01 ""`); worktree, pane and label stay. One paragraph in
the v0.20 upgrade guide; `obor init` makes L-01 and L-02 untyped (`internal/setup/oborinit.go:185`).

## 5. Deletes versus adds

Deletes: the type loop in `pickLane`, `HasLaneOfType`, the "level has no lane: using …" note (T-629,
`dispatch.go:174-176`), the "no lane type of level" refusal (now "no such lane_type"), the "waiting for a sonnet
lane" wording (now "waiting for a free lane" or "opus at its max 4"), setup's `<type>` argument, and `--start` on a
lane pane (a worker without a dispatch has no type).

Adds: the type in `dispatchData`, the lane row and the view; `Launch.Type`; the cap count; the restart's kind check.
Code about even; the concept simpler (one pool; type is the task's), at the price of one more fact in the
row beside task, branch and agent.

## Tasks

1. Row type: store column, `dispatchData.Type`, view and status (hard, state).
2. Launch: `Launch.Type`, `Launcher.args` prefers it, restart's kind check (medium).
3. Dispatch: cap count, pick rule, oldest-first waiting, wording (hard).
4. Config: optional type, validate, setup lane, init, upgrade note (simple).
