# Observations (lead; read at session start, rewrite at each handoff)

Patterns about obor and how we work on it that a fresh session would otherwise rediscover. Curated, not a log: at
each handoff merge, update or delete items (an item goes when its cause is fixed). Keep it under ~40 lines. Decisions
live in `docs/decisions.md`, open work in `BACKLOG.md`, the lead's own working rules in its memory.

## Where we go in circles
- **A question becomes a survey becomes 5-10 tasks.** 11-05: the queue went ~7 → ~25 in one afternoon and the
  milestone lost its exit (owner cut it). Before a survey, name the milestone it serves and what it may add.
- **Fix chains.** Each verify finds a gap the last fix opened: RV-119..121 (a consumer of a new state the spec did not
  list: the template now asks for every consumer), keep lanes T-1031 → T-1050 (a mode with no user yet: build a mode
  when a project asks), the start path T-1053 → T-1057 → T-1058 → T-1059 on 11-08 (the spec let the worker pick the
  evidence while `docs/research/herdr-0.9.5-probes.md` named it; the live probe covered starts without a prompt, serve
  starts with one). A spec that touches the terminal cites the probe findings and asks for every start form.
- **Bridge tests pass, the caller breaks** (RV-147, RV-149): adapter specs ask for a test at the boundary callers use.
- **Size does not fall by itself; replacing a mechanism grows, deleting one shrinks** (v0.28 flat prod, +2k tests;
  waves G-J: deletions −205/−50, replacements +103/+46 with regressions). Prefer tasks that remove a step outright,
  name the deletions in the spec, price a "replace X with Y" at its crash and race paths.
- **Old record shapes never go**: replay starts at record 1; a compat branch goes only with a log snapshot (parked).
- **Split by files means grepping the files** (T-880/T-891/T-893 all in `view/setup.go`, 11-05): grep before splitting.

## Load is the hidden tax
- Flaky reds track machine load, not the tests (11-05: ~12 different tests). Since v0.30 tests run in the lanes
  (T-961) and v0.31 dropped the tiers (lane median 40.5 s); herdr 0.9.5 still burns ~75 % of a core idle. Measure
  gate time and load before raising lanes.

## Premises to check before proposing
- The lead's premise was wrong twice on 11-05: E1 (integrator review "adds nothing": it sent back ~8% of merges) and
  "the next workers test main's MCP server" (agents run the installed kit's `obor mcp`, `~/.obor/kit/bin/obor`).
  Check processes/logs/code first; `ps` shows what actually runs.
- Reviewers rate mixed-version findings "High" from the window alone (RV-124 #1: new client vs old serve). Check
  the tags first (`git show v<last>:<file>`): a field no release shipped has no old reader, and the forward case needs
  someone running `obor stop` in the minutes before serve follows. Accept such findings unless the fix is ~free.
- Don't over-promise in a spec's guarantee: T-1055's "socket JSON byte-for-byte unchanged" was more than compatibility
  needed; the worker relaxed the test to fit and the review flagged both (RV-152). State the property that matters
  ("every key a server reads is sent, every old line decodes") and test exactly that.

## Local traps
- **External CLIs change under us.** Codex auto-upgraded (Homebrew cask 0.166 → 0.167) mid-day; its new helper was
  quarantined and macOS 27 shows a Gatekeeper dialog per binary per version, so unattended starts time out
  (docs/research/codex-quarantine.md). After a `brew upgrade`, check `codex --version` and the setup warning first.
- The lead's uncommitted BACKLOG edit held a task's publish (T-1043: "main checkout has local changes"): commit
  BACKLOG at once while any task in flight edits it.
- `obor web` does not follow a new binary (restart it after a merge that changes the view or
  an upgrade); raising `lanes` on obor web leaves the lanes `missing` until `obor setup lane`/`pane`.
- Re-dispatching a waiting task sends it to the back; `Priority: high` (T-1030) puts it first. Check `obor status`
  before dropping: a "queued" task may already be on a lane (T-899 lost 20 min).
- **Serve fires waiting work on its old binary** for ~30 s after a merge, before its self-restart: a review `--after` the
  fix of serve's own start bug fired on the buggy binary (11-08). Re-request after the restart (BACKLOG Concerns).
- `make release` runs the UPGRADE guide's shell blocks in a fixture (the upgrade check) and a version ratchet
  (`TestRemovedKeysWithinTwoVersions`): write guide steps that work in a fresh project; retire keys before the tag.
- `cmd | tail` hides a failed exit (a worker's timed-out check read as exit 0). The live conformance tier needs real
  sockets: reviewers' sandboxes cannot run it; the lead runs it in a pane.
