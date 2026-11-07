# herdr 0.9.5 probes (T-953)

`docs/research/herdr-usage-deep-dive.md` §7 lists five facts obor's polls and proposals rest on and that nothing had
re-run since herdr 0.9.3. This run re-probed them on 0.9.5 in the live conformance tier and records each answer, with
the command that produced it, so the proposals H3, H6, H7, H9 and H10 can be decided on data.

- Date: 2026-11-07. herdr 0.9.5, protocol 22 (`H-L8`); claude 2.1.319; codex-cli 0.166.0. macOS.
- Method: the suite's live tier (deep dive §6): one throwaway session per kind (`obor-conf-<kind>`), its server under
  a clean environment, agents started interactively in its panes, no real pane and no herdr plugin touched. Each answer
  is a `PASS` line of `make conformance TIER=live KIND=…`; the cases are `tests/conformance/live_herdr.go` (H-L3, H-L9,
  H-L1's timeout), `live_claude.go` (`CC-L8`), `live_codex.go` (`H-L11`) and `live.go` (`reportStart`, H-L1).
- The lines below are quoted from the runs; timings are one machine's and vary by a few tenths of a second. The herdr
  run's one `SKIP` is `H-L2` (it needs Claude's trust dialog and runs in the claude kind).

Summary:

| Probe | Question | 0.9.5 answer | Proposal |
|---|---|---|---|
| H-L1 | Does `agent.start` answer before or after the agent is ready? | At once (0.0 s) with `launch_pending: true`, `interactive_ready: false`; the agent takes input 3.7–4.3 s later | H6: the "it still answers at once" branch; decode the two fields, keep the start poll's `up` |
| H-L3 | Does `pane.rename` emit `pane.updated`? | No event in 3 s | H9: the reread stays; no large deletion on an event |
| H-L9 | Does a `cd` emit `pane.updated` with the new cwd? | No event in 3 s; `pane.get` does read the new cwd | H10: killed; `rereadPanes` stays |
| CC-L8 | `completion_seq` across two short Claude turns; per agent? screen-detected? reset on restart? | null → 4 → 6; set for a screen-detected agent; a fresh agent in the same pane read 12 after its first turn (the quit agent 4) | H3: confirmed, with a caveat (it is not one per turn, and it does not reset per agent) |
| H-L11 | Is `agent_session` filled for a fresh `codex --no-daemon` start, and when? | No at start (4.0 s after the start began); `id` after the first prompt, 0.5 s later | H7: killed as written; the value is not there at start and is an id, not a rollout path |

## H-L1 (H6): `agent.start` answers before the agent is ready

```
$ make conformance TIER=live KIND=codex
PASS H-L1 codex --no-daemon: agent.start (timeout_ms 30000) answered after 0.0s with status unknown, launch_pending true,
interactive_ready false; the agent took input after 3.7s: the answer came before it was ready
PASS H-L1 codex default (no --no-daemon): agent.start (timeout_ms 30000) answered after 0.0s with status unknown,
launch_pending true, interactive_ready false; the agent took input after 4.3s: the answer came before it was ready
```

```
$ make conformance TIER=live KIND=claude TURN=1
PASS H-L1 claude: agent.start (timeout_ms 30000) answered after 0.1s with status idle, launch_pending true,
interactive_ready false; the agent took input after 3.8s: the answer came before it was ready
```

```
$ make conformance TIER=live KIND=herdr
PASS H-L1 agent.start of claude with a flag it refuses (it exits at once), timeout_ms 4000: answered after 0.0s with
status unknown, launch_pending true, interactive_ready false; the pending launch ended after 0.6s: agent.get
agent_not_found: agent target conf-timeout-45 not found
```

Same as 0.9.3, and against the 0.9.5 skill's sentence ("A successful `agent start` returns only after Herdr detects the
expected agent … and considers it ready"): the answer is at once, `launch_pending` is true and `interactive_ready` is
false in it, and the agent takes input ~4 s later. The status in the at-once answer is `unknown` for codex and can be a
stale `idle` for claude, so `start`'s "is it up" is `launch_pending` leaving (or `interactive_ready` becoming true),
never the status alone. An agent that never comes up (a flag its CLI refuses) stays pending until `timeout_ms`
(another run ended the pending launch at 4.1 s, the timeout; the runs above ended it in 0.6 s when herdr saw the CLI
exit), then `agent_not_found` — `timeout_ms` is honoured.

For H6: herdr did not start waiting for ready, so the branch "if it still answers at once" holds — decode
`launch_pending` and `interactive_ready` and stop guessing `up` from kind and status. The ~40-line deletion (ticker,
`pre`, most of `started`) is not available on 0.9.5.

## H-L3 (H9): `pane.rename` emits no event

```
$ make conformance TIER=live KIND=herdr
PASS H-L3 pane.rename to conf-renamed (pane.get reads the new label) emits no event in 3s on a stream subscribed to
pane.updated and the pane's status: the stream's reread stays the way a relabel is seen
```

A stream subscribed to `pane.updated` and the pane's `pane.agent_status_changed` saw nothing for 3 s after the rename,
while `pane.get` read the new label. The same held on 0.9.3 (T-839), so the 0.9.5 `pane_updated` payload's `label` is
not sent on a rename.

For H9: the reread (and `waitPane`'s label patch, and the stream's merge) stays; the large deletion that would follow a
rename event is not on the table. The proposal's small step (`labelledShell` prefers the start row's terminal) does not
depend on this answer.

## H-L9 (H10): a `cd` gives no event, but `pane.get` has the new cwd

```
$ make conformance TIER=live KIND=herdr
PASS H-L9 a cd to /private/tmp/obc-herdr-735049483/cwd-probe in a fresh pane: no pane.updated event in 3s; pane.get then
reads cwd "/private/tmp/obc-herdr-735049483/cwd-probe" (foreground_cwd
"/private/tmp/obc-herdr-735049483/cwd-probe"); a cd gives no event (H10 has nothing to use, the reread stays)
```

A split pane's shell was sent `cd <scratch>/cwd-probe`: no `pane.updated` came within 3 s, and a `pane.get` read both
`cwd` and `foreground_cwd` as the new directory (so the reread is the way obor learns it, and it learns the right value).
The case first asserts the cd took, so the no-event answer is not a silent failure.

For H10: whatever the 0.9.5 schema's `pane_updated` payload could carry, a `cd` does not send one; `rereadPanes` (and
its RV-89 care) stays, and the ~45-line deletion H10 proposed is dead on 0.9.5.

## CC-L8 (H3): `completion_seq` is a monotonic counter that does not reset per agent

```
$ make conformance TIER=live KIND=claude TURN=1
PASS CC-L8 completion_seq on agent.get of one Claude agent: null after the start, 4 after turn 1, 6 after turn 2
(grew: true, true); screen_detection_skipped false, herdr's agent_session id from herdr:claude
"c806bb33-ff5d-441b-a478-3de218aa2d06"
PASS CC-L8 a fresh Claude start in the pane the first one was quit from: completion_seq null after it was ready,
12 after its first turn; the quit agent read 4 after its first turn; not per agent: the counter goes on across the
restart
```

One Claude agent, two short turns (`Reply with exactly OK`, the first through the driver's inbox envelope, the second
through `agent.prompt`): the value is `null` before the first turn, 4 after it and 6 after the second — so it is set for
a screen-detected agent (`screen_detection_skipped: false`) and it grows with completed work. It does not grow by one
per turn (null → 4 → 6): a comparison must be "greater", never "previous + 1". A fresh Claude agent started in the same
pane after the first was quit read `null` when ready and 12 after its first turn, above the quit agent's 4: the counter
is not per agent, it goes on across an agent restart in that pane. (A herdr *server* restart was not probed.)

For H3: `completion_seq` can carry "finished" the way the proposal says ("grew since the line's delivery, and ready"),
and it closes the missed-short-turn gap. Two caveats for the change: obor must record the baseline at delivery (it
decodes `state_change_seq` today, not `completion_seq`), and a restart of the pane's agent does not reset the value, so
the baseline is what makes it per-delivery, not a reset rule.

## H-L11 (H7): herdr's `agent_session` arrives only at the session's first turn

```
$ make conformance TIER=live KIND=codex
PASS H-L11 a fresh codex --no-daemon start with herdr's Codex integration: agent.get reported agent_session none when
the start was ready (4.2s after it began); whether it arrives at the first turn needs TURN=1
```

```
$ make conformance TIER=live KIND=codex TURN=1
PASS H-L11 a fresh codex --no-daemon start with herdr's Codex integration: agent.get reported agent_session none when
the start was ready (4.0s after it began); id from herdr:codex "01a1b5a5-d23d-77a3-ba4d-fc70b17ab605" arrived 0.5s
after the first prompt
```

The start is obor's driver argv (`codex --strict-config --no-daemon` plus the scratch kit's MCP config) in a
`CODEX_HOME` that holds the user's `hooks.json` (herdr's Codex integration v10). Codex 0.166 dispatches that hook's
`SessionStart` with the session's *first turn*, not at TUI startup — a scratch hook that dumped its input saw
`hook_event_name: SessionStart`, `session_id`, `transcript_path` and the pane's `HERDR_*` there, and no hook ran before
the prompt. So `agent.get` has no `agent_session` at the moment `agent.start` is ready, and 0.5 s after the first prompt
it has `kind: id` with the thread id.

Two setup facts the lab needed that a real start does not: the scratch home has no `[features] hooks = true` (the
user's `~/.codex/config.toml` has it) and no persisted hook trust (`[hooks.state."…/hooks.json:session_start:0:0"]`,
codex's review prompt otherwise blocks startup), so this one start passes `--enable hooks` and
`--dangerously-bypass-hook-trust`. The integration file is copied in for this start alone and removed after it.

For H7: the proposal "record herdr's `agent_session` for a fresh Codex start (one `agent.get`, waited for with a bound)
instead of the lsof search" is killed as written — the value is not there when the start is ready, and when it arrives
it is a session id, not the rollout path obor pins. A later fallback (after the agent's first prompt, e.g. at its first
`job_next`) could still use it, but that is a different change and it depends on codex's user-global hook trust.

## Limits

- One machine, one day, one run per kind; the timings above are a sample, not bounds.
- `completion_seq`'s reset on a herdr *server* restart, and whether it is per pane or server-wide, were not probed;
  `interactive_ready` was read in the at-once answer only (false), not after the agent came up.
- H-L11's answer is one machine's codex 0.166 with herdr's integration installed; without that integration (or with
  hooks disabled) `agent_session` is never filled.
- The TURN=1 runs make one or two short model calls per kind ("Reply with exactly OK"), as the tier's TURN gate allows;
  H-L11 needs one because that is the event the hook rides on.
