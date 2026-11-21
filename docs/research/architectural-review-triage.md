# Triage of the architectural review of 2026-11-21 (advisor, 2026-11-21)

Subject: `docs/reviews/architectural-review-2026-11-21.md`, checked against the code at `be3d6cca`.
Classes: **ACT** (a task now), **LATER** (valid, not now), **REJECT** (wrong, done, or against a standing decision).
Sizes are production lines: S under 50, M 50-200, L more or wide.

## Recommendation

Act on eight small tasks and one test pass. None is urgent: the review found no bug that fires in obor-kit or
axis-alies today, and three of its four "hot paths" cost under a millisecond or a few tens of milliseconds when
measured. The eight tasks are worth doing because each is small and either deletes code, fixes a text that is wrong
today, or closes a silent failure.

The review's sizes and counts are accurate (197,216 Go lines, 120,711 in tests, 781 tests in `internal/app`, every
file size I recounted). Its conclusions from them are weaker than its numbers:

- The "-18k to -25k test lines" plan is a line target. Most of it is against decisions made this week (the budget,
  the setup cache, the rig split) or against "lines are not a goal". The part that buys time is the `cmd/obor` pass,
  which is already the lead's next candidate.
- The extensibility waves ("before agy", "before rex") have correct findings and the wrong time: a new agent kind
  enters through the plugin protocol, and the port is best changed with the adapter that needs the change.
- The structural waves (split `internal/project`, materialized projections, `config.Ref`) are deep-dive material with
  no symptom behind them.

Two things need the owner (section 2).

## 1. Tasks to act on, in order

No two tasks share a file. A and B first (texts wrong today, and the one silent failure); the rest in any order.

| # | Task | Files | Size | Difficulty |
|---|---|---|---|---|
| A | Guard texts name the config's real keys, and a guard that fails open says so. Four messages cite `guards.allow_push`, `guards.deny_commands`, `protected_files`; the keys are `rules.may_push`, `rules.deny_commands`, `rules.protected_files`. A panic that is no denial still allows (the documented rule) but leaves one line. | `internal/guard/guard.go:79-85,342`, `session.go:221,223`, `shell.go:1379`, their tests' pinned texts; `cmd/obor/main.go:348` for the line | S | simple |
| B | No task ids in error texts (AGENTS.md "text says what is"): `T-1039`, `T-1119` twice. | `internal/config/validate.go:496`, `internal/agents/plugin/calls.go:289,292`, the tests that pin them | S | simple |
| C | Four one-line fixes in checks and run: relink the lane links after the nightly's scrub; the slot marker's name from `ProjectName()`; optionally the nightly's and `obor run`'s target from `merge.target_branch` (lead decides, see 3.7). | `internal/checks/nightly.go:53-56`, `internal/checks/checks.go:108`, `internal/run/slots.go:74`, `internal/run/run.go:185`, one test row each | S | simple |
| D | Delete the store's column probes: `laneCols`, `laneCol`, `specCols` read `pragma_table_info` on every read for stores older than schema 14, which no project has. The select lists become constants. `docs/accepted.md`'s RV-110 line goes with it. | `internal/store/lanes.go:35-61`, `specs.go:29-41`, their tests | S, net negative | simple |
| E | OpenCode's generated config gets one file per content: the trimmed start writes its own name, so a lead's and a worker's start never write the same file with different bytes. | `kinds/opencode/kind/kind.go:41,188-197`, its test | S | simple |
| F | Web: one `/api/projects` reader instead of two on the fleet page; the page's cursor from a cheap read instead of the full view; `obor web` asks each project's own terminal for process info; the dead `TaskStatus` type goes; the search snippet counts runes in the string it searched. | `web/src/App.svelte`, `web/src/pages/Fleet.svelte`, `web/src/lib/live.svelte.ts`, `web/src/lib/types.ts`, `internal/web/web.go`, `internal/web/dist/`, `cmd/obor/web.go:58-66`, `internal/view/docsearch.go:120` | M | medium |
| G | TOML by the parser the repo already has: `configKeys` (a 90-line scanner) becomes `toml.Decode` and `MetaData.Keys()`; herdr's `ResumeHint` the same. The spec says what a file that does not parse means (today: a partial key list under a table). The `T-1045` id in `codex.go:474` goes here, since B must not share the file. | `internal/agents/codex/codex.go:474,492-591`, `internal/herdr/setup.go:352-377`, their tests | M, net negative | medium |
| H | One command list fewer: the shim stops checking `bin/go-commands` and passes any well-formed subcommand to the binary, which already refuses unknown ones; `review verify` leaves `commands`. Minimal version if the lead prefers: delete the three stale entries only. | `bin/obor:54-58,276-280`, `bin/go-commands` (deleted), `cmd/obor/main.go:205-241`, `cmd/obor/skills_test.go:113-127`, `Makefile:157`, the two test fixtures that copy the file | S-M | simple |
| I | The `cmd/obor` test pass (BACKLOG Concerns 11-21, the lead's own next candidate). First part: `status_test.go`'s formatting rows call `writeStatus` on a literal `view.Project`; one or two rows keep the wiring through `view.Build`. Second part: a researcher lists the remaining CLI tests whose decision is tested in `internal/*`, then one delete task from that list. Excludes `skills_test.go` (task H). | `cmd/obor/status_test.go`, then `cmd/obor/*_test.go` as listed | L in tests | medium, review on merge |

Tasks F, H and I all change `cmd/obor`, in different files: they can run in parallel but each lane check runs the
package (33 s).

Small things to take with the next task that opens the file, not as tasks: `rename.go:141` reads the lanes for a
parameter `agentPairs` never uses (`rename.go:234-247`); the second quarantine warning (`launch/start.go:104-107`,
after `launch.go:547-549`); `TestRunningLeadHint`'s second half, which pins a sentence in `docs/state-model.md`
(`messages_test.go:1372-1384`); `Split.StartEnv`, which both compiled kinds answer with nothing
(`claude.go:405`, `codex.go:604`).

## 2. Needs an owner decision

1. **What should the test reduction buy?** The review's constraint 4 says prune aggressively; the standing rule says
   a test goes only when it adds no defect coverage. The nightly's record settles which cost is real: `cmd/obor`
   takes 33.5 s for 257 tests, `internal/checks` 20 s, `internal/setup` 17 s, while `internal/app`'s 778 tests
   and 46,773 lines take 16.9 s. So lane time and git processes are in `cmd/obor`, `checks` and `setup`, and the
   lines are in `internal/app`.
   - (a) Buy time: task I, then the same pass for `internal/setup`. Recommended.
   - (b) Buy lines too: the rig collapse, `launch_test.go` and the release harness (section 5), each hard or wide,
     each needing a row-by-row list first. I would not: the 11-04 pruning had 390 lines sent back by review.
2. **The budget.** The review wants `tests/budget` replaced by a 100-line reporter and the `git_procs` caps dropped.
   That reverses the owner's lines of 11-11 and 11-21. I recommend keeping it (5.4). It only goes if the owner says so.

For the lead, not the owner: whether the nightly resets to `merge.target_branch` instead of the main checkout's
current branch (3.7; `docs/design.md` changes with it).

## 3. Claimed bugs

| # | Claim | At HEAD | Verdict |
|---|---|---|---|
| 3.1 | `opencode.json` is one path per project; concurrent OpenCode starts overwrite each other | True (`kind.go:41`, written by `plugin/calls.go:186-197`). The bytes differ only between a lead (untrimmed) and any other agent (trimmed) when `.obor/opencode/opencode_override.json` exists. Both projects run a Claude lead, so every OpenCode start writes the same bytes today. | **ACT** (E). Fix by content, not per agent: the kind protocol's start carries no agent name, and two names need no protocol change. |
| 3.2 | The guard recovers every panic as allow | True (`guard.go:78-86`), and documented: "any internal error → allow" (`guard.go:23`). The two `role[0]` sites (`guard.go:500`, `shell.go:1218`) are reached with a role name; I found no path that panics. | Rewriting `deny` to return errors: **REJECT** (a wide change to a 1,400-line file for a documented rule). One line when it fails open: **ACT** (A). A re-panic is wrong: Go exits 2 on a panic, which the hook contract reads as a denial. |
| 3.3 | `slots.go` uses `cfg.Name()`; two unnamed projects share `merge-running-default` | True (`slots.go:74-78`). `obor init` always writes `project.name`, so it needs two hand-made configs; they would wait on each other's merge checks, not deadlock. | **ACT** (C): one word, and it removes a silent default. |
| 3.4 | The nightly's scrub removes the lane links before `nightly.also_run` | True, and the code says so (`nightly.go:55`; `checks.go:219` runs `clean -fdq`). A link whose path is gitignored survives; a link ignored only by a directory pattern (`node_modules/`) does not, since a symlink is no directory. obor-kit has no links. | **ACT** (C): one call after the reset. |
| 3.5 | `obor web` hardcodes herdr for the background scan | True (`web.go:58-60`, `terminal(nil, …)`); the comment says it holds until a second terminal exists, and hc exists. An hc project's pages get no process info. | **ACT** (F): the owner's hc trial is the next test run (decisions 11-20). |
| 3.5b | `srv.Close()` instead of `Shutdown` on a binary follow | True and intended: the page's long poll reconnects (`web.go:95-97`); a graceful shutdown would wait out 20 s polls. | **REJECT** |
| 3.6 | `lockRe` matches English git output; `GitEnv` sets no `LC_ALL` | True in the code. Cannot fire on Apple git, which ships no translations (checked: `LC_ALL=de_DE.UTF-8 git` answers in English), and obor claims macOS only (decisions 11-10). | **LATER**: three lines when Linux or a Homebrew git is claimed. |
| 3.7 | The nightly and `obor run` use `git branch --show-current`, not `MainBranch()` | True, and documented for the nightly (`nightly.go:16-18`). The two differ only when the main checkout is off the target, where a publish refuses anyway (`scratch.go:633`). The owner's 11-09 line reads "the nightly tests the target's head". | **ACT** as an option in C (lead decides). |
| 3.7b | `run.changedFiles` skips the index copy and the links exclusion | Known: BACKLOG Open (RV-214 design, 11-20) keeps the two tree builders apart until their rules converge. | **LATER** (already tracked) |
| 3.8 | `docsearch.go:120` slices `line` with an offset found in its lowercase copy | True. A panic needs a character whose lowercase is longer in bytes (Turkish `İ`); Serbian Latin has none. | **ACT** (F): one identifier. |
| 3.9 | The quarantine warning prints twice | True (`launch.go:547-549`, `start.go:104-107`), for a quarantined Codex lead or drive only. | Ride-along |
| 3.10 | `RunEnvLine` and `until()` ignore `ctx.Done()` | **Wrong**: both check `ctx.Err()` every poll (`shell.go:101`, `launch.go:271`). `ps` and `lsof` do run without a context (`launch.go:430`, `session.go:245`, `gitlock.go:109,165`). | **REJECT** the first half; the second **LATER** (no hang seen; each fix threads a context). |
| 3.11 | `background.go:44-57` can drop an entry at the 64 KB rewrite | True in principle (stat, read, rewrite beside an append). The file is a display list of background calls. | **REJECT** (accepted: display only, two hook calls within the same millisecond at the bound) |
| 3.12 | Four command lists have drifted | True: `bin/go-commands` lists `answer` and `sendback`, which have no dispatch case; `commands` lists `review verify`, which is refused. No harm today (the binary refuses them). | **ACT** (H) |
| 3.13 | Denial texts cite shape-1 keys; three error texts carry task ids | True, and visible: this session's start line read "2 deny rules from guards.deny_commands". | **ACT** (A, B) |
| 3.14 | `types.ts` drift | `TaskStatus` is unused (one hit, its definition). The contract test the review asks for is a new test with no drift bug behind it. | Dead type: **ACT** (F). Contract test: **LATER** |

## 4. Hot paths, measured

| # | Claim | Measured | Verdict |
|---|---|---|---|
| 4.1 | `config.Load` under `App.mu` on every pane event | True (`registry.go:128`, called from `panes.go:158`). One load is **0.34 ms** (benchmark on obor-kit's config, M4). `TaskIDOK`'s compile is **1.7 µs**. | **REJECT**: a stat-keyed cache adds a staleness class to save a third of a millisecond. The 11-20 wave D already put one read per pass. |
| 4.2 | N+1 spec reads under `App.mu` in `Status()` | True (`status.go:87,106`, `specs.go:105`): one point read per open task, twice for a waiting one without a level. Not measured; a handful of point reads per status call. | **REJECT** as a task (same class as the 11-02 "not measurable" rejections). Task D removes the extra `pragma` read from each. |
| 4.3 | Five processes per hook; `has_hook` runs the binary twice | True. The whole shim costs **36 ms** a hook; the binary's two runs are 8 and 9 ms of it. `jq` runs only when no binary is usable (`bin/obor:212,242-250`), not on this path. | **REJECT**: the probe is the design's answer to a stale or hanging binary (`docs/design.md:582`); replacing `jq` with `sed` on JSON is worse. Prompts indexes: **REJECT** (schema comment, `store.go:176-180`; decisions 11-02). One read-only handle per hook: **REJECT** (the hook is 9 ms). |
| 4.4a | Two `/api/projects` loops | True on the fleet page (`App.svelte:27`, `Fleet.svelte:26`). One call is **0.31 s**: four projects in a row, each a full view with git, `ps` and a process-info call per pane to the terminal. It runs every 5 s on every page, and twice on the fleet page. | **ACT** (F). It also halves obor's calls to herdr from that page (BACKLOG Concerns, herdr CPU). |
| 4.4b | `takeCursor` reads the full view | True (`live.svelte.ts:33-36`), once per page start, not per poll: 0.10 s and 127 KB. `/api/events?since=0` cannot replace it as is: it returns the oldest 200 records (`view/events.go:59-75`), so it needs a small server change. | **ACT** (F), second priority inside it. |
| 4.4c | Docs polls every 5 s | True; one poll is **10 ms**. | **REJECT** |
| 4.4d | `/api/events` opens a handle per request and polls at 300 ms | True; one small query every 300 ms per open page. An SSE stream is a second transport. | **REJECT** |
| 4.4e | `projectsAPI` is sequential | True. Parallel reads cut latency, not work. A narrower fleet read would cut the work. | **LATER**: measure what the 80 ms per project is first. |

## 5. Test tracks

- **T1, collapse seven rigs to two (-6k to -10k).** **LATER, owner decision 2(1).** The rigs were split by concern
  and moved onto `testkit.Term` this week (TP-8 to TP-10, decisions 11-21). The decisions tested at two levels were
  listed and cut to one level (TP-7a; T-1421, 27 rows). What is left is a line target in the package where every other task lands, and
  the package's tests run in 17 s. AGENTS.md already stops growth: a rig constructor or a fake needs its reason in
  the spec, and a decision is tested at the level whose code makes it.
- **T2, release logic into a Go tool (-3k).** **LATER.** It replaces a working release path of one maintainer to
  save lines no lane runs (`tests/release` runs under `-tags release` only). It would need the old scripts deleted
  in the same task, and a release to prove it.
- **T3.1, `launch_test.go` into tables.** **LATER.** 9.4 s for the package; the gain is reading cost. Take it with
  the next task that rewrites `Launcher`'s types (BACKLOG "Later candidates", the `Start` names).
- **T3.2 and T3.3, `status_test.go` and the `cmd/obor` pass.** **ACT** (I). This is where the time is.
- **T4.1, delete `live_hc.go`.** **REJECT.** It is not retired: `make conformance TIER=live KIND=hc` runs it
  (`main.go:71`), and `live_hcterm.go:92` uses its `hcLab`. hc is a 0.1.x preview; the probes are the recheck at each
  hc upgrade. No gate compiles or runs it, so it costs nothing per change.
- **T4.2, budget to a reporter; T4.3, drop the setup cache.** **REJECT**: owner 11-11 (the budget fails on git
  processes and leftovers), owner 11-21 (the testkit cap raised for the cache, which cut 494 git calls). The budget is
  what took git per run from 5,351 to 2,928. Owner decision 2(2) if they want it reversed.
- **T5.1, delete four meta-tests.** `modulemap_test.go`: **REJECT** (built on the owner's 11-10 plan 003; the
  integrator checks placement against the map). `TestConfigReadAtEntries`: **REJECT** (a day old, it holds wave D's
  rule; a new entry costs one line). `TestOborKitReaders`: **REJECT** (TP-2 kept it on purpose: it fails when a
  package reads a path the readers map lacks). `TestRunningLeadHint`'s doc half: ride-along.
- **T5.2, move the architecture tests to `tests/arch/`.** **REJECT**: they take 0.3 s (`TestImportBoundaries` 0.30 s,
  the rest 0.01 s).
- **T5.3, `laneGit` into `gitIO`, `decide_test.go` into rows.** **LATER**: the checks test plan (tasks A-G,
  `docs/research/checks-test-seam-advice.md`, owner 11-21) owns `internal/checks`' tests; fold it in there.
- **T6, guardrails.** The 1.5× ratio rule: **REJECT** (a line target; owner 10-14; TP-11 already prints lines per
  package as a warning). "A fix adds a row, not a scenario" and "no test pins a sentence in a doc": already what
  AGENTS.md's test rules say; one sentence there if the lead wants it explicit.

## 6. Extensibility (review section 4)

The findings are true: 15 optional interfaces in `agents.go`, two passes of `Start` for a compiled kind
(`agents.go:697-721`), `AgentStart.Env` and `Cwd` filled and read by neither adapter, `AgentStarter` asserted at
`launch/start.go:100`, the `hc.Panes` assertion at `cmd/obor/terminal.go:81`.

- **Agent kinds before agy: LATER.** A new kind is an out-of-process plugin (decisions 11-08). It speaks the
  protocol and is served by `plugin.Kind`, which already implements the optional interfaces once. The compiled
  kinds' `Split` and `Preparer` are not in its way. What agy needs from the protocol (an efforts list in the
  manifest, a fallible install) shows when agy is written. `CheckEffort` running `start` is the protocol as
  documented (`calls.go:209-217`), one process at validate time.
- **The TOML scanners: ACT** (G). **The per-agent OpenCode file: ACT** (E), by content.
- **Transcript parsing out of `internal/project`: LATER**, with the first kind whose transcript obor reads.
- **Port before rex: LATER.** BACKLOG holds the port's next steps for a decided terminal. `Env` and `Cwd` are kept
  for an adapter that starts a program with its own environment, which the port's comment says
  (`terminal.go:66-70`); deleting them now and adding them back for rex is two changes. Typed error sentinels,
  capability interfaces for `LeadSpace` and `ResumeHint`, and embedding `AgentStarter` are best decided with the
  third adapter's needs on the table. The one item with a user today is `obor web`'s terminal (F).
- **`launch` (28 fields, pane list read several times per launch): LATER**, not verified call by call.

## 7. Structure (review section 5)

All **LATER** unless noted; they are refactors with no symptom, and the 11-20 waves A-D ended two days ago. They
belong to the next deep dive.

- **Split `internal/project` into a leaf and the rest.** True that it imports `store`, `config`, `roles` and
  `agents`, and that sqlite reaches `version`, `herdr`, `hc` through it. obor is one static binary, so the cost is
  layering only. It touches 16 importers: nothing else can run beside it.
- **Materialized projections for starts, sessions and pauses.** A state-model change. Needs the owner and a design;
  nothing is slow or wrong today.
- **`config.Ref`** (29 `ParseModelRef` call sites now): the 11-20 deep dive left it "for a decision, not scheduled".
- **A helper for the 68 ignored `json.Unmarshal` results:** **REJECT**. A record field that is absent decodes to
  zero by design; logging "corrupt" records adds behaviour nobody asked for.
- **`internal/docs`, `view` off `launch` and `app`, `Lane.MarshalJSON`:** true and tidy; no bug.
- **`setup` and `cmd/obor` slimming:** the dead `rows` parameter is a ride-along; the rest waits for a task in
  those files.
- **Guard `shell.go`:** the review's own advice is to stop adding wrapper parsers, which is the stopping rule on
  record (BACKLOG Concerns, T-486).
- **HTTPS `Origin`:** **REJECT**. `Hosts` has no flag since T-1170 (decisions 11-10).

The review's risk matrix (its section 6) proposes nothing. Its rows match what BACKLOG and `docs/accepted.md`
already hold.

## 8. Risks of the ACT list

- **A:** the texts are pinned by tests and may be quoted in skills or role texts; the spec lists every place.
  How the fail-open line reaches a person depends on what `bin/obor` does with the binary's exit code: the spec
  author reads `run` in the shim first.
- **C:** the target-branch option changes what a nightly tests when the main checkout is off the target. Leave it
  out if the lead is unsure; the other three fixes stand alone.
- **D:** a hook of a newer binary reading a store a serve has not yet upgraded would fail instead of reading
  defaults. No such store exists: schema 14 dates from 11-09 (T-1089), both projects run v0.42, and serve opens
  no other schema (`store.go:25-27`). The spec says so.
- **G:** `toml.Decode` refuses a file the scanner read in part. The spec decides what a start does then.
- **I:** deleting a CLI test that only looked like a copy. The rule from TP-7b applies: same decision, same inputs,
  and the spec names the test that keeps each removed row.

## Not verified

- The reachability of a panic in the guard beyond the two sites the review names.
- `hc/control.go`'s duplicate event decoding, `herdrapp`'s missing `check` calls, `oborInit`'s count of config
  writes, the number of `Panes()` calls per launch: read as claimed, not traced.
- What `App.Status()` costs under the lock: reasoned from the query count, not timed.
- Where the 80 ms per project in `/api/projects` goes (git, `ps`, or the terminal).
- Timings are single runs on the owner's Mac, on a machine that was not quiet.
