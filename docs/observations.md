# Observations (lead; read at session start, rewrite at each handoff)

Patterns about obor and how we work on it that a fresh session would otherwise rediscover. Curated, not a log: at
each handoff merge, update or delete items (an item goes when its cause is fixed). Keep it under ~40 lines. Decisions
live in `docs/decisions.md`, open work in `BACKLOG.md`, the lead's own working rules in its memory.

## Where we go in circles
- **A question becomes a survey becomes 5-10 tasks.** Before a survey, name the milestone it serves and what it may add.
- **Fix chains** (RV-119..121, T-1053 → T-1059): each verify finds a gap the last fix opened. Specs list every
  consumer of a new state and every start form; a spec touching the terminal cites the probe findings.
- **A shape picked without asking "can the user write this in the tool's own format?"** 11-10: the agent trim was
  redesigned three times in a day (drop the user settings layer → per-key flags → an invented `{settings, off}`
  config → native `*_override` files). Propose the native format first, and never drop a whole layer to remove a few
  entries.
- **A research claim "X is only used for Y" became a premise without a grep** (11-10: "agent_session is a
  cross-check only" was false in three places; the owner decided on it). Grep the field before relaying such a claim.
- **Bridge tests pass, the caller breaks** (RV-147, RV-149): adapter specs ask for a test at the boundary callers use.
- **Replacing a mechanism grows, deleting one shrinks.** Name the deletions in the spec; price a "replace X with Y"
  at its crash and race paths.
- **Split by files means grepping the files** before splitting.

## Load is the hidden tax
- Flaky reds track machine load. The release's full `make check` is the most loaded run: v0.35's first try hung a
  synctest test for 10 min (a retry loop with no timer kept the bubble's clock still; T-1221) and a log line was read
  as a package (T-1220). Run a release with the lanes idle and dispatch after it.

## Premises to check before proposing
- Check processes, logs and code first (`ps` shows what runs; agents run the installed kit's `obor mcp`).
- Reviewers rate mixed-version findings "High" from the window alone: check the tags first (`git show v<last>:<file>`).
- Don't over-promise a spec's guarantee: state the property that matters and test exactly that (RV-152).
- **Token figures:** price a part with `/context`, not from `cache_creation_input_tokens`.
- **BACKLOG candidates go stale:** grep git log before offering one.
- **The Claude 5h limit is shared by Fable and opus:** a hit stops every Claude agent; resume after the reset.
- **Only obor-kit and axis-alies count for compat** (owner 11-10): judge old-version code by their current state.

## Local traps
- **External CLIs change under us** (Codex auto-upgrades; Gatekeeper per binary per version): after a `brew
  upgrade`, check `codex --version` and the setup warning first.
- Commit BACKLOG at once while any task in flight edits it (an uncommitted edit held a publish).
- `obor web` does not follow a new binary: restart it after a merge that changes the view.
- Re-dispatching a waiting task sends it to the back; `Priority: high` puts it first. Check `obor status` before
  dropping: a "queued" task may already be on a lane, or already merged (T-1213/T-1214 merged before a drop, 11-10).
- **Serve fires waiting work on its old binary** for ~30 s after a merge, before its self-restart.
- `make release` runs the UPGRADE guide's `sh` blocks in a fixture (the upgrade check): write steps that work in a
  fresh project.
- `cmd | tail` hides a failed exit. The live conformance tier needs real sockets: the lead runs it in a pane.
- Live-checking a non-lead agent: `obor restart <agent> --fresh`, read `herdr agent list` and its settings file, then
  `--stop` (env from `--settings` does not show in `ps`).
