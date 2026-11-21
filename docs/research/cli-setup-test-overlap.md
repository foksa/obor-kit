# Tests of `cmd/obor` and `internal/setup` whose decision a lower level already tests (researcher, 2026-11-21)

Brief: the lead's, from `docs/research/architectural-review-triage.md` §1 I and §5, owner 11-21 "test cuts buy time, not
lines". Scope: `cmd/obor` (not `status_test.go`, `skills_test.go`) and `internal/setup`. Commit `e2238d50`; nothing run
in the repo's tree was changed (probes ran from a scratch dir, the budget with `-record` pointed at a scratch file).

## Answer

1. **The overlap is real and small in time.** Eight `cmd/obor` tests and five `internal/setup` tests repeat a decision
   that an `internal/*` test already holds with the same inputs. Their own time is about **5.5 s of `cmd/obor`'s 49 s
   summed test time** (32.6 s wall) and **0.8 s of setup's** (17.2 s wall); git processes: 35 of `cmd/obor`'s 450, none of
   setup's 595. Cutting every row listed below buys about a sixth of `cmd/obor`'s wall time at best and nothing
   in setup.
2. **Where the time is, it is not overlap.**
   - `cmd/obor`: **about 15 s of the summed time is eight `TestLead*` tests that start an agent and each wait 1.8 s** on a
     serve that is not running (section 5). That is a fixture cost, not a duplicated decision, and no test needs to go.
     `obor init` runs in the binary (`TestInitEndToEnd` 2.9 s, `TestInitCachesThePluginKinds` 5.7 s, three-four init runs
     each) look like the same cost; not isolated.
   - `internal/setup`: **`TestGithooks` is 21 subtests, 17 s of subtest time, 236 of the 595 git processes**, and
     twelve more integration tests carry about 360. Their decisions are in `githooks/_hook`, `bin/obor` and `init`'s git
     writes, not in an `internal/*` package. Nothing here is a copy of a lower test.
3. **Several tests that look like copies are the only test of their decision** (section 3). They are move candidates (a
   test at the level that owns the code), not cut candidates.

For the delete task, the strongest rows (same decision, same inputs, a keeper named, no unique assertion lost) are
`cmd/obor` C1, C2, C4 rows 1, 3 and 5, C5, C6's state rows and C7's rows 1-3. The setup rows buy no time; take them
only if the lead wants the lines.

## 1. Package numbers now (`go run ./tests/budget -v -record <scratch>`, 2026-11-21 19:1x)

| Package | budget run: wall / git | cap (`tests/budget.json`) | my plain `go test -json` run (GOMAXPROCS=2, counting git wrapper) |
|---|---|---|---|
| `cmd/obor` | 35.2 s / 449 | 45.9 s / 675 | 32.6 s / 450; 257 tests, 49.0 s summed |
| `internal/setup` | 20.2 s / 595 | 21.8 s / 709 | 17.2 s / 595; 138 tests |
| context: `internal/checks` | 27.2 s / 1460 | | |
| context: `internal/app` | 20.4 s / 0 | | |
| context: `internal/launch` | 9.4 s / 33 (WARN over its 6.0 s cap) | | |
| all 41 packages | git 2,670 in total | | `budget OK: 41 packages (2 over their time cap)` |

Wall times are noisy: another agent's `go test -p 2 … obor/cmd/obor …` was running when the budget started (seen in
`ps`), and the budget doc itself says wall varies about 2x. Git counts are steady (449/450, 595/595). The elapsed per test
below is `go test -json`'s, each test also run alone (`-run '^Name$'`), one `go test` at a time.

## 2. `cmd/obor`: tests whose decision a lower level tests

"Keeps" is the test that holds the decision after the cut. Time and git are of the test alone.

| # | Test (file:line) | Time / git | What it decides | Keeps the decision (same decision, same inputs) | What stays in `cmd/obor` |
|---|---|---|---|---|---|
| C1 | `TestOpencodeHooksResolveTheLaneAgent` (`main_test.go:731`) | 0.28 s / 0 | an OpenCode lane's hooks, with no `OBOR_AGENT`, name the lane's agent from the cwd; a cwd outside a lane names none and sends nothing | `guard.TestTurnAndStopFailureResolveTheLaneAgent` (`internal/guard/stopfailure_test.go:10`): `Turn` and `StopFailure` for claude, codex and **opencode**, lane cwd and main checkout, `rate_limit`/`429`, a stale `OBOR_AGENT` | `TestTurnHooks`, `TestStopFailureHook` already prove the CLI sends turn/stopped to serve with the right fields |
| C2 | `TestSessionStartOpencodeLaneReportsNoSid` (`main_test.go:512`) | 0.23 s / 0 | an OpenCode lane's session-start reports the session under the lane's agent, kind and id, and writes no `.sid` | `guard.TestSessionOpencodeBegan` (`internal/guard/session_test.go:286`: deepseek lane, `sid-o`, the same transcript path) and `TestSessionWritesNoSidFile` (`:269`) | `TestSessionStartHookReports` is the one wire row of session-start |
| C3 | `TestTriageSessionStartRecordsItsInbox` (`main_test.go:783`), **first half** | 0.25 s / 0 | a triage session's start records its slot's inbox | `guard.TestSessionRecordsTheInbox` (`session_test.go:528`), its `the triage agent (T-796)` row: slot `triage`, mode 0600, same socket/token/session | the second half (a brief to `pj-triage` lands in the `triage` slot) is not tested elsewhere: **keep it** (poster's slot choice is cmd's) |
| C4 | `TestRestartHandoffOnlyWithTheFlag` rows 1, 3, 5 (`restart_test.go:20`) | 0.24 s / 0 (5 subtests of 0.05 s) | the first prompt of a restart: the lead's handoff gets `/obor:lead handoff`, a fresh lead the plain prompt, a reviewer none | `launch.TestFreshStartRolePrompt` (`internal/launch/launch_test.go:3659`): `lead, handoff restart`, `lead, a fresh start without the handoff`, `reviewer, no Prompt in its role` | rows 2 (`--pane … --handoff`: the flag reaches the request with a named pane) and 4 (a recorded session with no transcript: the lead starts fresh) have inputs launch's table lacks: **keep** |
| C5 | `TestLeadPromptWithAnEffort` (`lead_test.go:201`) | **3.6 s** / 0 (two starts) | `roles.lead = opus:high`: the start carries `--effort high` and the plain `/obor:lead`, never the handoff form | `launch.TestStartEffort` (`internal/launch/effort_test.go:16`, rows `the role's effort`, `the request's effort before the role's`) for the effort; `launch.TestFreshStartRolePrompt` (`lead, a fresh start without the handoff`) for the prompt | the two placements (a labelled pane at its shell, a new tab) are `TestLeadLabelledPaneSession` and `TestLeadNoPaneSetsUp`, which stay |
| C5b | `TestLeadPromptOnlyForAFreshClaudeLead` (`lead_test.go:327`) | 0.05 s / 0 | only a fresh Claude lead gets the skill prompt; codex and `--resume` none | `launch.TestFreshStartRolePrompt` rows `lead on a kind without skills` and `lead, resume carries none` | cheap; cut only for the lines |
| C6 | `TestLaneTable` (`status_integration_test.go:139`), the **state rows** | 0.28 s / 16 | a lane's task stays `working` after dispatch and after `task_done` until it lands | `checks.TestLaneStates` (`internal/checks/lanetable_integration_test.go:161`): `L-04` submitted not landed, `L-05` dispatched no `task_done` → `working`, same real-git fixture | the agent name `pj-l-01` (`lanetable.go:75`, `AgentName("worker", lane)`) is asserted only here: keep one assertion of it |
| C7 | `TestStatusJSON` (`status_integration_test.go:20`), rows 1-3 | 0.53 s / 19 | with serve down: `serve.up` false and its error; lane rows; a stored never-dispatched spec is not in the queue | `view.TestLoadWithoutServe` (`internal/view/load_test.go:20`), `view.TestQueueListsNoNeverDispatchedSpecWithoutServeStatus` (`queue_test.go`, T-09-shaped), `checks.TestLaneStates` | the JSON shape (indented, `needs_you: []`, `messages: []`, no `ready` field, `paused` absent, no `ready:` text line) is not tested below: **keep** as the one `status --json` row; it needs no second lane (`p.Lane("L-02", "")`) |

Sum of the cuttable parts: about 5.5 s of test time, 35 git.

## 3. Look like copies and are not

Each of these has a lower test that seems to cover it. It does not, for the reason given.

| Test | Why it is not a copy |
|---|---|
| `TestRestartJob`, `TestServeJobServeDown`, `TestRestartJobUnreadableLaneRow` (`restart_test.go:156-243`) | The decision is `resolveAgentJob` in `cmd/obor/restart.go:127`. `launch.TestRestartLaneWorkerJobUnknownRefuses` and `TestRestartIntegratorServeDownRefuses` *inject* the error text with `l.Job = …`; they test how launch handles a refusal, not when cmd makes one. |
| `TestWhoami`, `TestWhoamiCustomRole`, `TestWhoamiDeepDiveAndLead`, `TestWhoamiCanPush`, `TestTriageWhoamiAndCaller` (`config_cli_test.go`, `config_test.go`) | `guard.Whoami` has **no test in `internal/guard`** (`grep Whoami internal/**/*_test.go` finds only comments in setup that point at these). They are its only tests. Move candidate, not cut. |
| `TestLeadTakesTheCallersHCTerminal`, `TestLeadNewTabWhenTheCallersHCTerminalHasASibling` (`lead_here_test.go:157,194`) and setup's `TestInitPlacesInTheCallersSpaceAndTheLeadTakesIt`, `TestInitLeavesTheCallersSpaceWhenItDoesNotQualify` (`leadspace_test.go:150,189`) | `layout.TestHome` (`internal/app/layout/layout_test.go:233`) tests the *rule* over a `layout.Caller`. The facts that fill the `Caller` for hc (`hc.leadCaller`, `internal/hc/place.go:192-246`: foreground → `Program`, a labelled terminal of another project → `Foreign`, the project's own space → `Other`, tab mates) have **no test in `internal/hc`** (`TakeLead` appears in `internal/herdr/setup_test.go:509,546` only). These four are hc's only tests of that adapter. |
| `TestNightlySwitch` (`checks_test.go:24`) | The refusal texts ("takes a local time HH:MM", "the owner or the lead changes the setup") are canned replies of a fake serve; the test asserts the request cmd builds and the line it prints. Serve's validation is `internal/app`'s, not repeated here. |
| `TestStatusJSONModelPauses` (`status_integration_test.go:80`) | `view.TestSetup` passes `ModelPauses` in (`setup_test.go:65`) but never asserts them; the `wire.Status` → `view` → `model_pauses` JSON mapping (reason, `until`, absent after the clear) is asserted only here. It needs no real git: `testkit.New` could be `NewPlain` (6 git saved). |
| `TestReviewGit*`, `TestReviewGitClose*` (`review_integration_test.go`, `reviewcycle_integration_test.go`) | `reviewGit` is in `cmd/obor` (`reviewcycle.go:30`): the dirty-checkout rules, path shapes and commit plumbing are its own decisions over real git. `internal/app`'s review-close tests use a fake port. Together about 250 git; no lower test. |
| `TestServeDown*` (`uninstall_test.go:41-90`) | `serveDown` is cmd's. Setup's uninstall tests inject serve's answers. |
| `TestInitCachesThePluginKinds` (`setup_integration_test.go:131`) | Only test of init's step `kinds … opencode: manifest cached` (`grep "manifest cached"` finds setup's `setup agents` lines only). Its rows 2 and 3 (`setup mcp --model deepseek`, the rerun) overlap `setup.TestSetupMCPLane` and `TestSetupAgentsCachesTheKitKindWithoutWriting`, but they use the real `obor-kind-opencode`, which setup's stub kinds do not. Ask before cutting; its cost (5.7 s) is the init runs, see section 5. |
| setup `TestSetupAgentsAddsTheModelsTheConfigNames`, `TestSetupAgentsFillsTheKindOfAFormerBuiltin`, `TestSetupAgentsMigratesTheSharedConfigOnly` (`mcp_test.go:151`, `models_test.go:42,78`) | The decisions are `config.UpgradeEntries`, `KindFills`, `MigrateModels` (`internal/config/edit.go:422`), and `grep` finds **no test of them in `internal/config`**. Setup's are their only tests. |
| setup `TestGithooks` subtests, `TestGithooksInstallIsWhatGitRuns`, `TestPlanHooks` | The decisions live in `githooks/_hook` (bash, its own watchdog at line 60) and in `planHooks`. `bin/obor`'s `has_hook` (line 190) and `hooks.json` are separate code with their own hang tests; five hang tests (setup 1.7 s + 1.8 s, cmd `TestPreToolUseHangingBinaryDenies` 1.4 s, `TestOtherHooksHangingBinaryAllow` 1.35 s, `TestShimHookSkipsHangingDevBuild` 1.25 s) wait the watchdog's real 1 s. Same *kind* of decision in three scripts, not one decision twice. |

## 4. `internal/setup`: tests whose decision a lower level tests

All cheap and fake-git: 0 git each.

| # | Test (file:line) | Time | Keeps the decision | Not a copy |
|---|---|---|---|---|
| S1 | `TestPaneLaneOccupiedRefusesTheSameWithAndWithoutStart` (`lanefree_test.go:12`, 6 subtests) | 0.04 s | `launch.TestLaunchNoStopRefusesARunningAgent` (`internal/launch/place_test.go:84`, rows `a configured agent in the lane`, `an unlabelled agent in the lane`) and `TestNoAgentRefusesARunningAgent` (`noagent_test.go:145`): same lane, same occupant, the same text (`pj-int still runs in lane L-01 … obor restart pj-int --stop first`, `claude still runs … quit the agent in pane …`) | the row `a person's label` (`notes still runs …`) has no launch row; that `pane` and `pane --start` both reach the one rule is the wiring row: keep one row per entry |
| S2 | `TestRV47SetupMCPLaneVariadic` (`mcp_test.go:108`) | 0.01 s | `launch.TestRV47ExplicitVariadicConfig` (`internal/launch/mcp_test.go`, later and repeated `--mcp-config`) and `claude`'s `Lane` table (`internal/agents/claude/claude_test.go:427`) | `TestSetupMCPLane` keeps the text and wiring |
| S3 | `TestPlacementFollowsTheProjectsGroup` (`placement_test.go:17`), first half | 0.01 s | `herdr.TestPlaceChoosesTheProjectsWorkspace` (`internal/herdr/setup_test.go:280`, `only panes of the project and unknown-cwd ones` with a foreign `w5` pane) | the second half (`--new-tab workers` goes to `workspace_id=wA`) is not shown in herdr's subtests; I did not find it elsewhere |
| S4 | `TestNewPaneShellIsWaitedFor` (`placement_test.go:53`), subtest `busy 15 s: FAIL with the hint` | 0.66 s for both subtests (real 0.3 s waits) | `launch.TestRestartWaitsForTheShellAfterTheStop` (`internal/launch/shell_test.go:14`, `the shell never comes back: FAIL naming the pane`) and `steps_test.go:161` carry the same text (`pane w1:p3 is not an available shell after 7s: start the agent there with obor restart …`) | the input is a *new* pane (`herdrtest.NewPaneBusy`), which launch's table does not use (it uses `StartBusy` on an exited agent's pane); setup's own comment calls launch's table the case table |
| S5 | `TestSetupOpencodePaneEnvCarriesTheModel` rows 1-2 and `TestSetupNonOpencodePaneDropsTheInheritedModel` (`opencode_model_test.go:16,62`) | 0.03 s + 0.06 s | `launch.TestStartEffort` (`effort_test.go:16`: same `oc`→`go/ds#high`, `bare` → unset, codex), `config.TestOpencodeModelEffort` (`internal/config/opencode_test.go:14`), `launch.TestEveryStartPathTypesTheRegistryEnv` (`startenv_test.go`, stale `OBOR_OPENCODE_MODEL`) | the carrier differs: setup asserts the `pane.split` env map and the generated `OPENCODE_CONFIG` file, launch the typed env line; the inherited-env row (`inherited/m#max`) through the split is not in launch |

Not checked here: `TestRenameKeepsAllowPushWorking` (`rename_test.go:138`, 0.17 s) against `internal/config`; `TestPluginKindStartsOnce` (`pluginstart_test.go:69`, 0.33 s) against `agents/plugin` and `launch`
(`plugin_test.go`, `opencode_start_test.go`).

## 5. The 1.8 s of the lead tests (found while measuring)

`TestLeadLabelledPaneSession`, `TestLeadNoPaneSetsUp`, `TestLeadNewTabWhenTheCallersPaneDoesNotQualify`,
`TestLeadNewTabWhenTheCallersHCTerminalHasASibling` take **1.81 s each**, `TestLeadPromptWithAnEffort` 3.6 s (two starts),
`TestLeadWakesAStoppedProjectFirst` 4.0 s; all in-process, 0 git. `TestLeadRunningTouchesNothing` and the rest, which
start nothing, take 0.01 s.

- A block profile of `TestLeadNoPaneSetsUp` alone (`-blockprofile`, rate 1) puts 1.80 of its 1.81 s in
  `Launcher.Launch → start → report → recordSession → wire.Verdict → verdictAsk`'s `select` (`internal/app/wire/client.go:150-170`:
  retries every 500 ms until the ctx deadline less a 200 ms margin) against a serve that is not running.
- `OBOR_CALL_DEADLINE=0.2` in the test process did not shorten it (six of these tests rerun with it: still 1.81 s), so the
  caller's ctx deadline governs, not that variable.
- `internal/launch`'s fixture stubs `Launcher.Report` (`launch_test.go:264`); `cmd/obor`'s `leadFx.launcher`
  (`lead_test.go:46-51`) tunes `Wait`, `Poll`, `Sched`, `Rollout` and leaves `Report` real.
- Not tried (I may not edit tests): stubbing `Report` there. Expected to remove about 15 s of summed test time in
  `cmd/obor` with no test deleted. `obor init` ends by starting the lead, which would pay the same 1.8 s per run inside
  the binary: `TestInitEndToEnd` (2.9 s, two runs), `TestInitCachesThePluginKinds` (5.7 s, four runs). Consistent, **not
  isolated** (the profile of a test process does not see its child).

## What I did not verify

- I read every `cmd/obor` and setup test's name and top comment, and the bodies of those named above. The remaining
  `cmd/obor` tests (`spec`, `pause`, `watch`, `events`, `send`, `dispatch`, `drive`, `serve`, `service`, `terminal`, `mcp`,
  `plugin_wiring`, `shim_*`, `sleep`, `lead`'s other rows) build a request or exercise cmd's own code (`sleep.go`, `lead.go`,
  `service.go`); by their comments the decision is in `cmd/obor`, but I did not read each body against a lower test.
- Setup's `uninstall_test.go`, `oborinit_decisions_test.go` (1,042 lines) and `setup_test.go` (1,510 lines) were read by
  name and comment only. `oborinit_decisions_test.go`'s `TestOborInitModelsAndDifficultyOfDetectedKinds` and
  `TestDetectCommands` were not compared with `config`.
- "Same inputs" is judged from the test bodies; I did not run a mutation of the production code to see which test
  fails. Before a delete task, each keeper should be shown red under the decision's removal.
- Per-test git counts of the non-integration tests are 0 by their fake git (confirmed for the setup rows above and
  for `cmd/obor` C1-C4 run alone), not for all 257 + 138.
- Probes: `/private/tmp/claude-501/-Users-djordjeradakovic-Work-obor-kit/3374b62e-7020-45a2-98d2-ee818fcfc56d/scratchpad/`
  (`runpkg.sh`, `each.sh`, the `-json` outputs, `block.out`); the counting `git` wrapper is the budget's own idea
  (`tests/budget/main.go:474`).
