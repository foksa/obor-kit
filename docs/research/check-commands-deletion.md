# Sizing the deletion of `commands.baseline`, `commands.gate` and `commands.e2e` (T-1080)

Question (owner, via `BACKLOG.md:35` and the advisor 11-09, `docs/research/adopter-test-flow-advice.md:69-71`): the
three reserved commands are set by neither project; `check_milestone` and `merge.nightly_commands` do the same job.
What would deleting them cost, what happens to a config that still sets one, and should it be done?

Conventions as in the other research docs: **Fact** = read from the code/config at `7f4787c3`, `file:line` given.
**Opinion** = my reading, marked. No code was changed; this is the deliverable.

## Answer first

**Delete all three, in one code task and one docs task.** They are ~65 production lines, ~45 test lines and ~20 doc
lines, no project sets one, and the one behaviour they add that nothing else has (`at: "merge"`, the paths-filtered
gate) has no test at all and runs only inside a full lane test.

Two things are not what the advisor's one-liner suggests, and they decide the shape of the deletion:

1. **`baseline`, `gate` and `e2e` are not keys a strict loader can refuse.** `commands` is an
   `Ordered[Command]` (`internal/config/config.go:43`) and `Ordered.knows` accepts every member name
   (`internal/config/json.go:189-190`): the three names are *free command keys* today and stay free after the
   deletion. So a config that sets `"e2e": "bun run e2e"` (the common shape) would lose the nightly run **silently**
   unless a WARN is added. Only the object fields `dir` / `paths` / `at` can become unknown keys, and those are the
   shape `baseline` and `gate` use.
2. **`at: "merge"` does not run at merge.** Since T-961 the merge slot runs only `lane_setup` + `compile`
   (`internal/checks/scratch.go:176-183`) and its line carries `r.check` only (`scratch.go:198`); `runChecks` — and
   so the `at: "merge"` baseline and gate — runs in the **lane test** (`internal/checks/lanetest.go:80-92`), and the
   gate's counts reach only the lane test's `GREEN …` line (`lanetest.go:92`). `design.md:510` ("runs them on every
   full merge") describes the pre-T-961 gate. The value of the `at` key today is "one more command in a full lane
   test", which a class's `checks` already give.

## 1. Use today (fact)

| Where | `baseline` | `gate` | `e2e` |
|---|---|---|---|
| obor-kit `.obor/config.json` | no | no | no |
| axis-alies `~/Gamedev/axis-alies/.obor/config.json` (read-only) | no | no | no |
| `docs/design.md` example (105-110) | yes (with `dir`, `timeout`) | yes (`paths`) | yes (as the free-key example) |
| `templates/config.example.json` | yes (16) | yes (17) | yes (19) |
| `templates/local.example.json` | yes (4, a `dir` override) | no | no |
| `obor init` output | `null` (init.go:166) | `null` (init.go:166) | not written |
| `merge.nightly_commands`, either project | no | no | no |
| `merge.classes[].checks`, either project | no | no | no |

Neither project uses one; the only "real" uses are the kit's own examples. axis-alies' equivalent is
`check_milestone` (its nightly tier, `bun run check:engine:nightly` + soak) and `merge.nightly_commands` is unset
there.

## 2. What each one does today: readers and writers

### `commands.baseline`

**Writers**
- `internal/setup/init.go:166` — the `commands` block init writes into a new config contains `"baseline": null`
  (and `"gate": null`); the example's real entry never reaches the project because init replaces `commands` whole
  (`init.go:154-166`).
- `templates/config.example.json:16`, `templates/local.example.json:4` (the per-machine `dir` override the design
  documents at `docs/design.md:127`).
- `docs/design.md:107-108` (the example), `:127` (local.json "baseline `dir` overrides").

**Readers (production)**
- `internal/checks/runner.go:334-345` — `baselineDir`: `commands.baseline.dir`, made absolute, must be a directory;
  `""` otherwise. The only reader of `Command.Dir` in the tree.
- `internal/checks/runner.go:460-467` — in `runChecks`, when `atMerge("baseline")` (`runner.go:490-493`,
  `Command.At == "merge"`): exports `OBOR_BASELINE=<dir>` and runs it; a red is red.
- `internal/checks/nightly.go:76-83` — in the nightly list; skipped when `baselineDir` is empty, else
  `OBOR_BASELINE` is exported and it runs. Its `at` is ignored here (it runs whenever the dir exists).
- `internal/config/validate.go:200` — `gatingCommands`: `merge.nightly_commands` may not name it
  (`validate.go:204-218`).
- Comments/docs: `internal/checks/runner.go:32` (env list), `internal/checks/classify.go:12`,
  `internal/app/nightly.go:18`, `skills/lead/commands.md:86`, `templates/agents/integrator.md:82`,
  `docs/design.md:331,514,532`, `docs/state-model.md:335` (row 45), `docs/plan/merge-flow-rework.md:88`
  (historical plan), `docs/releases/v0.24.1.md:4` (historical), `docs/research/architecture-inventory.md:155`.

**Tests**
- `internal/checks/nightly_integration_test.go:35-49` (in the list, `dir` → `$OBOR_BASELINE` is a directory, the
  `ran` log and the state-file summary), `:66-79` (a red baseline in `NIGHTLY RED … failed: … baseline …`).
- `internal/checks/flakes_integration_test.go:120-131` (a red baseline is logged as a flake).

### `commands.gate`

**Writers**
- `internal/setup/init.go:166` (`"gate": null`), `templates/config.example.json:17`, `docs/design.md:109`.

**Readers (production)**
- `internal/checks/runner.go:468-487` — in `runChecks`' full path, when `atMerge("gate")`: runs only if the merge
  `pre..HEAD` touches one of `Command.Paths` (the only reader of `Command.Paths`) or `OBOR_GATE=1`; on success sets
  `r.gate = ", gate <counts>"` (`runner.go:39` is the field, `:484` the write).
- `internal/checks/runner.go:490-493` — `atMerge` (also the `Command.At` reader; no other reader exists).
- `internal/checks/lanetest.go:92` — `checks := r.check + r.gate`: the **only** reader of `r.gate`; the merge slot's
  line (`scratch.go:198`) drops it.
- `internal/checks/nightly.go:76,84-92` — in the nightly list, forced (`OBOR_GATE=1`, `paths` and `at` ignored).
- `internal/config/validate.go:200` — `gatingCommands`.
- Comments/docs: `internal/checks/runner.go:32,393-396`, `internal/checks/checks.go:40` (`Options.Env`),
  `internal/checks/classify.go:12`, `internal/app/nightly.go:18`, `skills/lead/commands.md:86`,
  `templates/agents/integrator.md:82`, `docs/design.md:331,510-511,532-533`, `docs/state-model.md:335`.

**Tests**
- `internal/checks/nightly_integration_test.go:18-20` (the `logged` helper echoes `$OBOR_GATE`), `:35-49` (forced
  gate, `ran` log `gate H merge 1`), `:66-79` (a red gate in the nightly).
- `internal/checks/flakes_integration_test.go:120-131`.
- `internal/checks/fixture_test.go:201,249-252` — the fixture strips `OBOR_GATE=` from its environment so an outer
  merge's value does not leak in; that line (and its comment) goes with the variable.
- **No test covers the `at: "merge"` path or the `paths` match** (no test sets `at`; grep for `"at"` in
  `internal/checks/*_test.go` finds nothing). The lane-test GREEN line's gate suffix is untested too
  (`lanetest_integration_test.go:150` asserts `"1 pass"` with no gate).

### `commands.e2e`

**Writers**
- `templates/config.example.json:19`; `docs/design.md:110` (as the free-key example — that line stays valid after
  the deletion). init does **not** write it.

**Readers (production)**
- `internal/checks/nightly.go:76` — the nightly list. That is the only reader; no `paths`, no `at`, no env.
- `internal/config/validate.go:200` — `gatingCommands` (so `merge.nightly_commands` may not name it today).
- Comments/docs: `internal/app/nightly.go:18`, `skills/lead/commands.md:86`, `docs/design.md:514`,
  `docs/state-model.md:335`.

**Tests**
- `internal/checks/nightly_integration_test.go:41,45-49,66-79` (in the list; a red e2e is the nightly's RED).
- `internal/checks/flakes_integration_test.go:124-131`.

`cmd/obor/status_integration_test.go:41,91` and `internal/view/golden_test.go:39` contain the word `e2e` in a
`last-full-check` sample line and an event line: fixtures, not readers; they stay.

### `obor status`, `internal/view`, `obor web` (no reader)

None of the three reads the commands. `obor status` prints the last full check's summary string verbatim
(`cmd/obor/status.go:249`), which is `readFullCheck`'s `Summary` (`internal/view/view.go:161-168,455-474`), and the web
pages show only that verdict/sha/age (`web/src/pages/Project.svelte:151-153`, `Fleet.svelte:155`). The merge-gate step,
`causeGate` and the "gate" event/stage words (`internal/app/event/steps.go:93`, `internal/app/loop.go:80`,
`web/src/lib/stages.ts:8`) are the merge pipeline, not `commands.gate`. A historical `last-full-check` line that says
`failed: e2e` keeps printing as it is.

## 3. What goes (lines that would be deleted or edited)

| File | Lines | Kind |
|---|---|---|
| `internal/checks/runner.go` | 334-345 (`baselineDir`), 460-467 (baseline block), 468-487 (gate block), 490-493 (`atMerge`), 39 (`gate` field), 32 + 393-396 (comments) | prod |
| `internal/checks/nightly.go` | 76-93 (list + `switch`) → `{"check", "check_milestone"}`, 18 (comment) | prod |
| `internal/checks/lanetest.go` | 92 (`r.check` only) | prod |
| `internal/checks/classify.go`, `internal/checks/checks.go` | 12, 40 (comments) | prod |
| `internal/config/validate.go` | 200 (`gatingCommands` minus three names) | prod |
| `internal/setup/init.go` | 166 (drop the two nulls) | prod |
| `internal/app/nightly.go` | 18-19 (comment) | prod |
| `templates/config.example.json` | 16, 17, 19 | prod (data) |
| `templates/local.example.json` | 4 (the `commands` block goes empty) | prod (data) |
| `internal/checks/nightly_integration_test.go` | 18-20, 35-49, 66-79 | test |
| `internal/checks/flakes_integration_test.go` | 120-131 | test |
| `internal/checks/fixture_test.go` | 201, 249-252 (`OBOR_GATE` strip) | test |
| `docs/design.md` (lead-owned) | 107-109, 127, 331, 508, 510-511, 514, 532-533 | docs |
| `docs/state-model.md` | 335 (row 45, the nightly's command list) | docs |
| `skills/lead/commands.md`, `templates/agents/integrator.md` | 86, 82 | docs |
| `BACKLOG.md` | 35 (close the candidate) | docs |

Rough size: **~65 production lines removed, ~8 comments edited, ~45 test lines, ~20 doc lines.** No new helper is
needed; the deletion leaves `runChecks` with one less branch pair and the nightly list two names shorter.

## 4. An existing config after the deletion

`commands` keeps accepting any name (`internal/config/json.go:189-190`), so the **name** never becomes an unknown
key. Three cases:

- `"e2e": "bun run e2e"` / `"baseline": "…"` / `"gate": "…"` (string): loads, is **not run** by the nightly or a
  lane test any more, and `obor run <name>` still runs it. **No warning** unless one is added — this is the silent
  case, and the reason the recommendation below keeps a WARN.
- `{ "cmd": …, "dir": … }` / `{ …, "paths": … }` / `{ …, "at": "merge" }`: with the `Command` fields kept, loads as
  today and only the name WARN fires; with the fields deleted, the strict loader refuses the file:
  `obor: .obor/config.json: unknown field "dir" (commands.baseline.dir)` (`internal/config/config.go:356-362`,
  `json.go:194-253`). `obor config validate`, `obor run`, `obor spec`, `obor dispatch` … all fail at load with that
  line (every strict caller: `cmd/obor/main.go:147-165`). serve (`config.go:367-372`), the readers (`:384-392`,
  `obor status`/`obor stop`) and the hooks (`config.go:375-378`, the SessionStart hook names it) are lenient and print
  the unknown-key WARN (`config.go:497-504`) instead. There is no `retiredKeys` entry to use for this
  (`config.go:402` is top-level keys only).
- `"baseline": null` (what init writes today): harmless either way — a null command is unset
  (`config.go:148-152`, `:817-822`).

The codebase already has the migration pattern for exactly this: `Config.warnings` (`internal/config/validate.go:
521-551`) prints `WARN <key>: no longer used, remove it` for removed keys, for two minors after the removing
release, and `TestRemovedKeysWithinTwoVersions` (`internal/config/validate_test.go:354-405`) fails once `VERSION`
passes the window (`checks.load_limit`: removed 29, `keepUntil` 31). The three names fit that pattern: the name is
still a legal key, so the WARN is the only place the loss can be shown.

## 5. `obor run <key>`, and what stays

- **`obor run` is not affected.** It looks the name up with `config.Command` and runs it
  (`internal/run/run.go:62-77`, `cmd/obor/run.go:15-48`); nothing refuses a reserved name, so `obor run gate` today
  and after the deletion run `commands.gate` with no paths filter and no `OBOR_GATE`. `obor run baseline` likewise
  (it never received `OBOR_BASELINE` from `run` either — only the checks exported it).
- **Nothing that stays depends on the three.** A `merge.classes[].checks` entry, `merge.guard` or
  `merge.nightly_commands` names a command by key; after the deletion they still resolve (`runChecks` →
  `r.must`, `internal/checks/runner.go:414-427`). Neither project names one in any of those lists.
- `gatingCommands` keeps `lane_setup`, `compile`, `check`, `check_milestone`, `guard` (`validate.go:200`).
- One **behaviour change in validate** follows: `merge.nightly_commands` may then name `baseline`, `gate` or `e2e`
  (today refused as "a gating command", `validate.go:215-217`) and they would run non-gating (a WARN line, never
  RED, `nightly.go:113-130`). That is the intended migration path for a project that wants `e2e` nightly.
- `OBOR_BASELINE` and `OBOR_GATE` are exported by no one else and read by nothing else (`internal/checks/runner.go:
  462,469`, `nightly.go:83,86`; the only other mentions are test comments and the fixture's env hygiene), so both
  variables go with the commands. No `obor` command promises them to users outside `docs/design.md`.
- The words "gate" (the merge step, `internal/app/event/steps.go:93`, `internal/app/loop.go:80`, `obor status`,
  `web/src/lib/stages.ts:8`), `merge.guard`, `check_milestone`, `compile` and `after_merge` are different things and
  stay.

## 6. Risks and not verified

- **An adopter outside the two known projects may rely on one**; the WARN (or the release note) is the whole
  mitigation. I did not search GitHub or the installer's users for `.obor/config.json` files.
- **The `at` key's other value is not validated** (only `== "merge"` is read, `runner.go:490-493`): a config with
  `"at": "mrege"` already means "nightly only" silently. Deleting the key removes that trap; keeping it would want a
  `FAIL` for an unknown value.
- **`docs/design.md:510` and `:532` are stale today** (they say `at: "merge"` runs at merge; it runs in the lane
  test). The deletion's doc task should not "fix" this by writing the new behaviour as if it existed; the lines go.
- I did not run the nightly or a lane test with a config setting the three; the reader list is from the code and a
  tree-wide grep for the three names, `OBOR_GATE` and `OBOR_BASELINE`.

## 7. Recommendation

**Delete all three.** Reasons: no project sets one; the nightly's `check` + `check_milestone` and
`merge.nightly_commands` cover the same ground with fewer moving parts; `baseline`'s `dir`/`OBOR_BASELINE` and
`gate`'s `paths`/`at`/`OBOR_GATE` are the last readers of three `Command` fields and two env vars; the one thing they
add that a class's `checks` do not (`at: "merge"`) runs in the lane test rather than at merge and is untested.

With the deletion, to keep the loss visible (opinion, and the only part that is a choice):

1. **Keep the `Command` fields `dir`, `paths`, `at` for the two-minor window** (removed in v0.32, gone at v0.34) so an
   object-shaped config loads and WARNs instead of failing every strict CLI verb, and
2. **add a retired-name WARN** for `baseline`, `gate`, `e2e` in `Config.warnings`, in the `removed(...)` style:
   `WARN commands.gate: obor no longer runs it at merge or nightly; delete it, or name it in merge.classes or
   merge.nightly_commands` — skipped when the name is already in a class's `checks`, `merge.guard` or
   `merge.nightly_commands` (those uses still work, so a WARN would be false there), with a row in
   `TestRemovedKeysWithinTwoVersions` (`keepUntil` 34).

   The cheaper alternative — delete the fields now and accept the strict-load error, with a line in the release's
   upgrade block (`RELEASING.md:14-15`) — costs nothing in code and is still loud (the message names the exact key),
   but a config with `commands.baseline.dir` then cannot run any strict `obor` verb until it is edited, and a string
   `"e2e"` still stops silently. I would pay the two versions of three dead struct fields for that.

   Either way, `baseline`'s `dir` must stop being advertised: `templates/local.example.json:4` is the only place a
   per-machine `dir` override is shown, and `docs/design.md:127` promises it.

## 8. Task split (for the deletion)

**T-A — code and tests (one task).** `internal/checks/runner.go` (drop `baselineDir`, both `at`-merge blocks,
`atMerge`, the `gate` field; edit the two comments), `internal/checks/nightly.go` (list and `switch`), `lanetest.go:92`,
`classify.go:12`, `checks.go:40`, `internal/config/validate.go:200` (+ the retired-name WARN if taken),
`internal/setup/init.go:166`, `internal/app/nightly.go:18-19`; tests: `nightly_integration_test.go`,
`flakes_integration_test.go`, `fixture_test.go` (env strip), and `validate_test.go` (the WARN rows, if taken).
Expected test change: the three nightly tests lose the commands from their lists and expectations; no new behaviour
to test beyond the WARN.

**T-B — docs and templates (one task; `docs/design.md` is lead-owned, so it needs a `Protected files this task may
edit` line or the lead's own commit).** `templates/config.example.json:16,17,19`,
`templates/local.example.json:4`, `templates/agents/integrator.md:82`, `skills/lead/commands.md:86`,
`docs/state-model.md:335`, `docs/design.md:107-109,127,331,508,510-511,514,532-533`, `BACKLOG.md:35`.
Leave `docs/plan/merge-flow-rework.md:88`, `docs/releases/v0.24.1.md:4` and
`docs/research/architecture-inventory.md:155` as history.

**T-C — v0.34, one small task.** Delete the `dir`/`paths`/`at` fields (`internal/config/config.go:140,141,143`), the
retired-name WARN, and its row in `TestRemovedKeysWithinTwoVersions`.
