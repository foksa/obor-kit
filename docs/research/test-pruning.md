# Test pruning survey (T-828)

Owner 11-04: the tests are ~75k lines against ~49k of production Go. The suite costs gate time when four lanes load the
machine, and every refactor (S2) drags old tests along. This survey measures the suite, lists the candidates with
their evidence, states what must stay, and proposes a plan. It changes no test. The pruning comes after it, as one task
per package or file group (§6).

**What it found.**
1. **About 12k lines of test functions are coverage-redundant and outside the keep list.** Each of these 433 tests
   covers only statements that other tests of its package also cover. As a set they can go together without the
   package losing one covered statement (§3.1). The pool is a ceiling: statement coverage cannot see orderings or
   races, so each file still needs a reviewer pass against the keep rules (§5). Over half the pool (5.7k lines,
   211 tests) is in `internal/app`.
2. **The removed-config-key expiry check is dead.** `TestRemovedKeysWithinTwoVersions` compares against a hard-coded
   `releaseMinor = 13`, but the tree ships v0.25. All 16 removed keys are 6 to 12 minors past their two-version
   window. The check that should have failed never did (§3.3).
3. **The slow part of the gate is a few packages and a few tests.** These packages set the wall time: `cmd/obor` 96 s,
   `internal/app` 55 s, `internal/checks` 54 s, `tests/upgrade` 36 s, `internal/setup` 33 s and `internal/web` 29 s.
   Run alone, few tests are slow: `TestLeadPromptOnlyForAFreshClaudeLead` 20.6 s, `TestMakeShip` 15.3 s,
   `TestMakeShipNotifiesTheLeads` 14.8 s and `TestTurnFailsAtItsDeadline` 8.8 s. The other 10–17 s figures in the
   package runs are contention between parallel tests (§2).
4. **Prose and asset tests are a separate kind.** 88 `cmd/obor` tests cover no Go at all (Makefile, release scripts,
   the `bin/obor` shim, skills and templates). Coverage cannot judge them, so they are judged only as near-duplicates
   (§3.2).

## 1. Method

All runs were made in lane L-03 on tree `d393ea76` (v0.25.1), one test process at a time; the guard denies load runs.
The machine was carrying other lanes at a load average of 16 to 71, so the seconds below are a loaded machine's. Use
them to rank, not as absolute values.

- **Lines:** `wc -l` over `*_test.go` and over non-test `.go` per package directory. *Test-function lines* come from
  `go/ast`: the span of each top-level `func TestX`, without helpers or rigs. The suite has 50.7k test-function lines
  in 75.4k test lines; the other 24.7k are helpers, rigs and tables.
- **Coverage and time:** one `go test -json -cover -coverprofile=<pkg>.cover <pkg>` per package, in sequence. The wall
  time is the package's `Elapsed`. The per-test time is each top-level test's `Elapsed` in that run. Parallel tests
  overlap, so their sum can exceed the wall.
- **Per-test coverage:** one `go test -c -cover` binary per package. Each test then runs alone
  (`-test.run '^TestX$' -test.coverprofile=…`), in sequence, all 1,476 tests, every one green.
  - **Per-test against package profile:** a test's covered blocks are compared with the union of all *other* tests'
    blocks, which stands in for "the package's profile without it". As a check, the union of the per-test profiles
    matched the package run's covered statements in every package: `run` 312/321 and `project` 254/256 differ only
    by timing branches.
  - **`cmd/obor`:** most of its tests exec a separately built `obor`, so in-process coverage sees 38.8%. For the
    per-test runs, `GOFLAGS="-cover -coverpkg=obor/cmd/obor"` and `GOCOVERDIR` made `TestMain`'s build covered, and
    `go tool covdata textfmt` merged the subprocess counters in. With them, `cmd/obor` is 1682/1991 statements (84.5%).
- **Unique statements:** a test's *unique* count is the statements no other test of the package covers. Zero
  means every statement it reaches is reached elsewhere.
- **Greedy set:** one consistent removable set. Tests are taken slowest first, and a test is dropped if every block
  it covers is still covered by a kept test. Several zero-unique tests can share a block, so the zero-unique tests
  cannot all go; the greedy set can. Every line and second figure in §3.1 and §6 comes from it.
- **Keep tags:** applied from a test's name and doc comment (the rules are in §5):
  - `refusal`: Refus or Reject.
  - `replay`: Replay, Crash, Restart or Recover.
  - `deny`: a guard deny, block or fail-closed test in `internal/guard`.
  - `finding`: `TestRV…`, or an RV-nn in the comment.
  - `table-row`: `TestState…Row<N>`, which pin `docs/state-model.md` rows.

  548 of the 1,483 tests carry a tag. Tagged tests are never in the candidate pool.
- **Near-duplicates:** each test body is tokenised with literals replaced by `S`/`N`. Pairs in a package whose 4-gram
  sets have a Jaccard similarity of at least 0.75 were then read by hand (72 pairs; the useful ones are in §3.2).

**Limits.** Statement coverage is a necessary condition for removal, not a sufficient one. A test can add no
statement and still pin an order of events, a race window or an exact text. The keep list covers the known kinds,
and the per-file review in each pruning task covers the rest. Prose tests (exact phrases in `templates/` and
`skills/`) and shell or Makefile tests carry little or no Go coverage, and are judged here only as duplicates.

## 2. Per package

Coverage is the package's own statements (`go test -cover`). The wall time is one `go test` of the package on the
loaded machine. The slowest tests are listed when they take 0.5 s or more, with their `Elapsed` inside that package
run. That time includes contention with the package's other parallel tests, so for the outliers, compare it with the
same test run alone (per-test runs, §1):

| Test | In the package run | Alone |
|---|---:|---:|
| `cmd/obor` TestLeadPromptOnlyForAFreshClaudeLead | 21.1 | 20.6 |
| `cmd/obor` TestMakeShip | 11.7 | 15.3 |
| `cmd/obor` TestMakeShipNotifiesTheLeads | 12.5 | 14.8 |
| `cmd/obor` TestShipPublishEvidence | 17.4 | 4.3 |
| `cmd/obor` TestReleaseRefusesTagWithRenamedPlugin | 16.4 | 2.6 |
| `cmd/obor` TestMakeReleaseKeepsItsFullLog | 13.8 | 2.8 |
| `cmd/obor` TestAffectedGoOborKit | 5.7 | 1.3 |
| `internal/affected` TestGoOborKit | 9.1 | 0.9 (the build cache is warm by then) |
| `internal/app` TestTurnFailsAtItsDeadline | 8.8 | 8.8 |

The lesson for the plan: `cmd/obor`'s 96 s is many process-spawning tests competing for CPU. It is not a few slow
tests, so the time shrinks with the number of `make`/`git` spawns, not by removing one test.

| Package | Test lines | Prod lines | Cover | Wall s | Slowest tests |
|---|---:|---:|---:|---:|---|
| `cmd/obor` | 12059 | 4395 | 38.8% (84.5% with the subprocess, §1) | 96.5 | TestLeadPromptOnlyForAFreshClaudeLead 21.1; TestShipPublishEvidence 17.4; TestReleaseRefusesTagWithRenamedPlugin 16.4; TestReleaseRefusesTagWithRenamedOwner 15.2; TestShipPushDoneWhenOriginContainsMain 14.0; TestMakeReleaseKeepsItsFullLog 13.8 |
| `internal/app` | 31860 | 13490 | 92.6% | 55.2 | TestTurnFailsAtItsDeadline 8.8; TestFreshBriefAdmissionTimeoutStopsNothing 6.0; TestStartPromptDelivery 4.6; TestWatchedSetDropEndsIt 4.5; the TestWatched…/TestWatch… family 3.8–4.4 each |
| `internal/checks` | 3873 | 2714 | 86.1% | 54.2 | TestResolvedPinsOwnMergeOnly 5.1; TestScratchPublish 5.0; TestScratchAfterMerge 5.0; TestScratchAfterMergeMainNotAtMerge 4.6; TestScratchGateForeignState 4.5 |
| `tests/upgrade` | 1033 | 0 | – | 36.0 | TestUpgradeV0230ToV0231 35.7 (release binaries from the cache) |
| `internal/setup` | 4634 | 3480 | 84.3% | 33.5 | TestSetupPaneTypesTheEnvAtAReusedPane 5.6; TestPaneSplitsTheLargestPane 3.9; TestPaneLabelSetAndRefused 3.8; TestRoleTriageRuns… 3.8 |
| `internal/web` | 1169 | 641 | 90.7% | 29.2 | TestPhaseAgreesAcrossLanesPipelineAndWeb 3.6; TestAgentPageOfRoles 2.0; TestEventsSince 2.0 |
| `internal/config` | 2009 | 2282 | 91.5% | 13.1 | TestCustomRoleRefusals 4.2; TestShapeErrorsFailTheLoad 1.7; TestRemovedKeysWithinTwoVersions 1.6 |
| `internal/launch` | 6060 | 2484 | 88.0% | 12.4 | TestRestartSuspended 1.1 |
| `internal/affected` | 141 | 135 | 95.5% | 9.4 | TestGoOborKit 9.1; TestGoFixture 1.5 |
| `internal/view` | 974 | 2061 | 67.2% | 8.8 | TestBuildNoPluginWaitsForTheLastStart 2.2; TestLoadWithoutServe 1.7; TestBuildBackground 1.5 |
| `internal/service` | 582 | 373 | 92.5% | 7.5 | TestRestartKickstartErrorNotRestarted 2.2; TestRotateLog 1.6; TestRestartSlowKickstart 1.2 |
| `internal/run` | 1041 | 837 | 86.1% | 6.0 | TestExecuteLeavesNoGroup 5.1; TestRunWritesReceipts 2.6; TestRunRcArgsTimeout 1.4 |
| `internal/guard` | 3349 | 2738 | 91.9% | 5.9 | none ≥ 0.65 (100 tests at 0.3–0.6 each, run in parallel) |
| `internal/project` | 865 | 1234 | 50.1% | 1.7 | – |
| `tests/go/socketprobe` | 0 | 76 | – | 1.4 | (no tests) |
| `internal/app/wire` | 145 | 737 | 31.0% | 0.8 | TestVerdictDeadlineLine 0.6 |
| `internal/store` | 1022 | 850 | 74.5% | 0.8 | – |
| `internal/testkit` | 37 | 547 | 16.5% | 0.8 | – (test helper) |
| `internal/herdr` | 1745 | 1529 | 87.6% | 0.7 | – |
| `internal/selfexec` | 176 | 140 | 88.7% | 0.6 | – |
| `internal/app/port` | 0 | 467 | – | 0.6 | (interfaces, no tests) |
| `internal/mcp` | 741 | 557 | 88.7% | 0.3 | – |
| `internal/version` | 46 | 40 | 90.9% | 0.3 | – |
| `internal/agents/opencode` | 227 | 213 | 95.2% | 0.3 | – |
| `tests/conformance/stubmcp` | 142 | 149 | 67.8% | 0.3 | – |
| `internal/spec` | 165 | 255 | 85.4% | 0.2 | – |
| `internal/herdr/herdrtest` | 99 | 1247 | 26.7% | 0.2 | – (test helper) |
| `internal/app/event` | 68 | 684 | 21.6% | 0.2 | – |
| `internal/agents/claude` | 245 | 326 | 93.6% | 0.2 | – |
| `internal/agents` | 236 | 471 | 92.2% | 0.2 | – |
| `internal/agents/codex` | 249 | 316 | 93.7% | 0.2 | – |
| `internal/roles` | 183 | 195 | 98.0% | 0.2 | – |
| `tests/conformance` | 363 | 3366 | 85.3% of the untagged files | 0.2 | – (the live and static tiers are behind a build tag, not in `go test ./...`) |

Per-test coverage, per package (each "test" is a top-level `func Test…`):

- **No stmt:** tests that cover no statement of their own package.
- **Zero-unique:** tests that cover statements, none of them unique.
- **Greedy:** the size of the removable set (§1).
- **Greedy, untagged:** greedy tests that carry no keep tag. These form the candidate pool, and their lines and
  seconds are the last two columns.

| Package | Tests | No stmt | Zero-unique | Greedy | Greedy, untagged | Lines | Σ test s |
|---|---:|---:|---:|---:|---:|---:|---:|
| `internal/app` | 586 | 0 | 447 | 389 | 211 | 5705 | 94.0 |
| `internal/setup` | 105 | 0 | 53 | 51 | 40 | 1286 | 35.5 |
| `cmd/obor` | 235 | 88 | 59 | 47 | 36 | 987 | 51.4 |
| `internal/launch` | 86 | 2 | 42 | 36 | 20 | 868 | 1.4 |
| `internal/guard` | 100 | 0 | 65 | 53 | 32 | 571 | 8.9 |
| `internal/checks` | 67 | 5 | 33 | 30 | 22 | 554 | 36.5 |
| `internal/config` | 54 | 0 | 30 | 25 | 22 | 532 | 3.1 |
| `internal/herdr` | 38 | 1 | 13 | 12 | 9 | 306 | 0.5 |
| `internal/store` | 24 | 0 | 10 | 10 | 7 | 242 | 0.3 |
| `internal/web` | 23 | 1 | 12 | 9 | 7 | 226 | 10.6 |
| `internal/project` | 20 | 2 | 8 | 6 | 5 | 155 | 0.4 |
| `internal/view` | 19 | 0 | 4 | 3 | 3 | 118 | 2.3 |
| `internal/run` | 19 | 0 | 7 | 6 | 5 | 107 | 3.4 |
| `tests/conformance` | 12 | 0 | 4 | 3 | 3 | 68 | 0.0 |
| `internal/mcp` | 12 | 0 | 2 | 2 | 2 | 59 | 0.0 |
| `internal/affected` | 4 | 0 | 2 | 2 | 2 | 51 | 9.2 |
| `tests/conformance/stubmcp` | 8 | 0 | 3 | 3 | 3 | 38 | 0.0 |
| `internal/agents` | 9 | 0 | 1 | 1 | 1 | 32 | 0.0 |
| `internal/app/wire` | 4 | 0 | 2 | 1 | 1 | 21 | 0.0 |
| `internal/testkit` | 2 | 0 | 1 | 1 | 1 | 15 | 0.0 |
| `internal/roles` | 7 | 0 | 1 | 1 | 1 | 11 | 0.0 |
| `agents/{claude,codex,opencode}`, `app/event`, `herdr/herdrtest`, `selfexec`, `service`, `spec`, `version` | 42 | 0 | 2 | 2 | 0 | 0 | 0.0 |

The table's 1,476 tests are the ones run per test. The 1,483 in §1 also count `tests/upgrade` (7 tests), which
needs release binaries and was not run per test.

## 3. Candidates

### 3.1 Tests whose statements are all covered by other tests

The candidate pool is **433 tests and 11,952 test-function lines**: the greedy set minus every tagged test. Removing
the whole pool would leave each package's covered statements unchanged; that is how the greedy set is built. The
complete list per file is in the appendix (§7). The largest files are:

| File | Pool / tests in file | Lines |
|---|---:|---:|
| `internal/app/ident_test.go` | 25 / 46 | 462 |
| `internal/setup/setup_test.go` | 18 / 47 | 458 |
| `internal/setup/oborinit_test.go` | 8 / 24 | 288 |
| `internal/launch/launch_test.go` | 6 / 41 | 288 |
| `internal/app/loop_test.go` | 6 / 10 | 246 |
| `internal/app/idle_test.go` | 11 / 16 | 244 (17.9 s Σ) |
| `internal/app/dispatch_test.go` | 4 / 12 | 232 |
| `internal/setup/placement_test.go` | 4 / 6 | 223 |
| `cmd/obor/status_test.go` | 9 / 22 | 216 |
| `internal/app/rolestop_test.go` | 6 / 9 | 213 |
| `internal/guard/session_test.go` | 9 / 15 | 209 |
| `internal/launch/session_test.go` | 5 / 10 | 204 |
| `internal/checks/scratch_test.go` | 6 / 19 | 154 (23.5 s Σ) |
| `cmd/obor/lead_test.go` | 4 / 9 | 86 (26.6 s Σ) |

**Read with care.** Some pool members are the one end-to-end test of a path, such as `TestLoopHappyPath` and
`TestOborInitSetsUpTheProject`. Their statements are covered by the narrower tests, but they are the only test that
runs the path in order. The keep rule "one end-to-end test per entry point" (§5) keeps them. The pruning task picks
which narrower tests go instead.

### 3.2 Near-duplicates and duplicated work

These are the pairs the 4-gram pass found that differ only in an input a table row would hold, plus duplicated
expensive work found by hand:

- **The repo walked twice.** `internal/affected` `TestGoOborKit` and `cmd/obor` `TestAffectedGoOborKit` both run the
  affected-package walk over this kit through `go list`. The `cmd/obor` one covers no `cmd/obor` statement through
  its subprocess. Keep `internal/affected`'s, which covers the code. This saves 1.3 s alone and 5.7 s under the
  package's contention.
- **`cmd/obor/status_test.go`:** `TestStatusShowsRunningGate`, `TestStatusShowsRunningPublish`,
  `TestStatusIdleTaskHasNoRunning` and `TestStatusNumberedStepKeepsStep` use the same fixture (`heldGateStatus`) and
  one assertion shape. They should be one table over task and want. `TestStatusShowsTheEffort` and
  `TestStatusShowsTheDispatchsType` are the same (Jaccard 0.84).
- **`internal/guard/prompt_test.go`:** `TestPromptTaggedOrSlashCommandQuiet`, `…PeerEnvelopes`,
  `…PastedContentWrapperUnwrapped`, `…EnvelopeNeedsItsClosingTagAtTheEnd` and `…ClaudeCodeEnvelopesQuiet` have the
  same body shape (Jaccard 1.0) and differ only in rows. Merge them into one table and keep every row; these are deny
  and quiet rows of the guard.
- **`internal/guard/stop_test.go`:** `TestStopWorkerOncePerLoop`, `…RunningBackgroundTaskAllows` and
  `…AfterTaskDoneAllows` are the same (0.94–1.0). So are `…BlockedReportDropRedispatchedBlocks` and
  `…BlockedReportSubmitSendbackBlocks` (0.76). Keep the rows and drop the four function shells.
- **`internal/guard/guard_test.go`:** `TestWrapperValueOptionsPerWrapper`, `TestUnresolvedCommandWordWithGitDenied`,
  `TestUnresolvedCommandWordDeniesOnlyWithAWriteVerb`, `TestFindExec` and `TestHeadlessByArgvStructure` are already
  one table shape each (1.0). Merging them saves only function shells, so it is low value; the rows stay (deny).
- **`internal/checks/lanetable_test.go`:** `TestMergeEvidenceSubject` and `TestMergeEvidenceResolvedMerge` (1.0).
- **`internal/checks/after_merge_test.go`:** `TestScratchAfterMergeFailedNamesItsLog` and `…GreenLog` (0.88), each
  building a scratch repo. One fixture can carry both.
- **`internal/app`:**
  - `drive_test.go` `TestDriveTakeOverWhileGatingRefused` and `move_test.go` `TestMoveWhileGatingRefused` (0.87, same
    rig, a different verb).
  - `nightly_test.go` `TestNightlyOffSettlesAGreenByHand` and `TestNightlyRedEndedByAGreenByHand` (0.93).
  - `difficulty_test.go` `TestDifficultyRunsTheLevelsFirstModel` and `TestDifficultyModelLineFirst` (0.89).
  - `idle_test.go`: `TestReviewRequestEndsIdleAndANewEpochStarts`, `…AcrossARestart` and
    `TestQueuedReviewRequestEndsIdle…` (0.79–0.87, 3.0 s each).
  - `rolestop_test.go` `TestRoleStopIntegratorWaitsForQueuedJob` and `…WaitsForIdle` (0.85).
  - `worktext_test.go`: `TestFixJobNamesBothRoleTexts` with `…TheKitRoleTextAloneWithoutAProjectDoc`, and
    `TestWorkJobNamesTheConfiguredReportSections` with `…TheDefaultReportSectionsOnce` (0.84–0.85).
  - `ident_test.go`: `TestIdentUnnamedSessionNoProof` with `TestIdentOnePaneTwoAgents`, and `TestIdentPaneMoved`
    with `TestIdentStopMovedPane` (0.78–0.82).
  - `follow_test.go` `TestFollowBinaryAfterTenMinutes` and `…DeadlineWhileDown` (0.79).
- **`cmd/obor/drive_test.go`:** `TestDrivePromptNamesTheLead` and `TestDrivePromptSaysCommitAsYouGo` (0.88). Each
  spawns the binary for one phrase of the same prompt; one run can assert both.
- **Asset tests in `cmd/obor` (no Go coverage, 88 tests).** These are judged here by cost, not coverage:
  - `release_test.go`, `release_steps_test.go`, `release_*_test.go`, `makefile_test.go` and `install_test.go` run
    `make release`, `ship` and `publish` against fresh repos. Alone they take 2–15 s each, and 10–17 s in the package
    run. The `TestMakeRelease…` family (24 tests) repeats the full `make release` setup per refusal. Each refusal row
    needs only the step it refuses at. Fold the refusals into one fixture with a row per refusal, and keep one full
    run (`TestMakeReleaseFullRun`) and one resume (`TestMakeReleaseResumes`). `TestMakeShip` (15.3 s alone) and
    `TestMakeShipNotifiesTheLeads` (14.8 s) both run a full ship. One run can assert both.
  - `skills_test.go` and `plugin_wiring_test.go` (21 tests) assert phrases in `skills/` and `templates/`. They are
    cheap (no process) and the keep list does not protect them. Keep the structural checks (frontmatter, referenced
    files exist, no reference to a removed skill). The single-phrase checks, which change with every template edit,
    are the first to go.
- **`TestLeadPromptOnlyForAFreshClaudeLead` (20.6 s alone, the slowest test in the suite)** has no unique statement
  and no keep tag. It runs `obor lead` twice for a Codex lead (`luna`), about 10 s each. That is a wait the fixture's
  launcher tuning (`Wait` 1 s) does not shorten, and this survey did not trace its source. Its name says "Claude",
  but both of its runs are Codex. It is the single largest time win: delete it, or find the wait and keep the two
  assertions.

### 3.3 Tests of code that is gone or of deleted behaviour

| Test(s) | File | Why it can go | What goes with it |
|---|---|---|---|
| `TestRemovedKeysWithinTwoVersions`, `TestValidateRemovedT470Keys`, `TestValidateRemovedLeadsWarn`, `TestSpecRemoved`, `TestLeadRemoved`, the "removed, no default" loop in `config_test.go:62`, the `stop_hook`/`done_file` rows in `load_test.go:13-33` | `internal/config/{validate,config,load}_test.go` | Decision 2026-10-28: a removed key WARNs for two minor versions and is unknown after that. Every key in the table was removed in v0.11–v0.17, and their windows ended at v0.13–v0.19; the tree is v0.25. The expiry check never fired because `releaseMinor` is the constant 13 (`validate_test.go:355`) instead of being read from `VERSION`. | The fields in `config.go:45-92` (`Obor`, `DoneFile`, `Leads`, `StopHook`, `Heartbeat`, `Inbox`, `Notify`, `Spec.*`, `Merge.FullEvery`, `paths.status`/`queue`/`tasks`, `mcp.jobs`, `cache.warn_tokens`) and their `removed(…)` lines in `validate.go:~370-385`. A code change, so it is its own task (P1). |
| `TestRemovedByV010KeysAreUnknown` | `internal/config/validate_test.go:400` | Asserts the generic "unknown field" error for 7 keys removed by v0.10. Any unknown-key test covers that, and `TestShapeErrorsFailTheLoad` does. | – |
| `TestOpenRWMigratesTheOldSchema` (6→7), `TestOpenRWMigratesTheOldPromptsKey` (7→8), `TestOpenRWMigratesTheLanesType` (8→9) | `internal/store/{prompts,lanes}_test.go` | `tests/upgrade` supports upgrades from v0.23.0 only (T-778), and v0.22.0 and v0.23.0 both shipped schema 9. The store still opens schema 6 (`upgradeFrom = 6`, `store.go:29`). | Needs an owner decision to raise `upgradeFrom` to 9 (a v0.22 store then upgrades through v0.24 first, as v0.12 does today). Then the 6→9 migration code goes with the three tests. `TestOpenRWMigratesTheSpecNote` (9→10) stays. |
| `TestMonitorIsRemoved` | `cmd/obor/main_test.go:187` | v0.24.0 notes: `obor monitor` prints its replacement "for one minor". From v0.26 it is an unknown command. | The `monitor` case in `cmd/obor/main.go:82,214`. Do it with the v0.26 release, not before. |
| `TestOldShapeRefused`, `TestConfigValidateOldShape` | `internal/config/load_test.go:73`, `cmd/obor/config_test.go:100` | The pre-v0.23 shape's message names `obor setup migrate` in v0.24 (T-789). Under the two-minor rule, v0.26 drops the special message and the shape becomes ordinary unknown keys. | The "predates v0.23" message in `internal/config/config.go`, at v0.26. |
| `TestSetupAgentsRetires…` (3 tests) | `internal/setup/setup_test.go:1488-1620` | Retire the global opencode entry and link (P1, v0.2x). They stay while a machine may still carry the global entry; the owner's machine was cleaned 11-04 (BACKLOG). | `agents.Retired` and setup's retire step, once the owner says no machine has it. Owner decision; not in the plan's first wave. |

No test was found for code that no longer exists at all: `deadcode` runs in the gate, and a test of a deleted function
would not compile. Deleted *behaviour* is what survives, as above.

### 3.4 Conformance probes that settled (BACKLOG "trim the conformance suite")

`tests/conformance` (3,729 lines, 363 of them `_test.go`) is a program behind a build tag. Trimming it saves lines
and live-run minutes, not gate time. The last recorded live run was 34 PASS (BACKLOG, T-820). The probes that
answered a one-time question in `docs/research/agent-driver-deep-dive.md` §7 are candidates to become an assertion,
or to go:

| Probe | Lines | Status | Proposal |
|---|---:|---|---|
| H-L3 `pane.rename` emits an event? (`live_herdr.go:126-157`) | 32 | Settled: no event; `socket.go:969` relies on that. | Keep the PASS branch as a one-line assertion; drop the WARN branch's comment-outdated text. |
| H-L1 answer timing (`live.go:~688-703`) | ~16 | Settled: herdr answers once the agent is up. The WARN says the driver's `launch_pending` comment and poll are redundant; `socket.go:426` and `start.go:112` still say "at once". | Settle the comment in the driver (a code task), then keep only the FAIL on obor's 10 s client timeout. |
| CX-L5 per-launch Codex SessionStart hook (`live_codex.go:124-176`) | 54 | Probes P6's Codex half, which the driver does not use: no `hooks.` or bypass flag in `internal/agents/codex`. | Delete until P6's Codex half is a task. |
| CC-L4 permanent SKIP lines: the plain-text line and `crossSessionInbound` (`live_claude.go:290-291`) | 2 | Never run: one model call per kind. | Delete the lines; record the two facts as "not probed" in the deep dive. |
| CC-L5 exit-dialog SKIP (`live_claude.go:122`) | 1 | Never run: needs model turns. | Same. |
| OC-L3/OC-L4 WARN branches: "another obor server beside it" and "set up more than once" (`live_opencode.go:99-135`) | ~10 | P1 shipped: setup retires the global entry and link. The FAIL branches guard the per-start config. | Keep the PASS/FAIL; drop the WARNs once the retire step goes (§3.3). |
| H-L6 restore (`live_herdr.go:287-357`) | 71 | Not settled: P9 is open. | Keep. |

Together, about 120 lines, plus the CX-L5 start in the live run (one Codex start, about 20 s). The other probes
guard a fact the driver depends on today (deep dive §6 "Guards" column) and stay.

## 4. Seconds: where the gate time is

The gate runs `go test ./...`, so packages run in parallel and the wall is set by the slowest packages: `cmd/obor`
96 s, then `internal/app` 55 s, `internal/checks` 54 s, `tests/upgrade` 36 s and `internal/setup` 33 s. Pruning
small packages saves lines, not gate seconds. The time wins, in order:

1. `cmd/obor`:
   - `TestLeadPromptOnlyForAFreshClaudeLead` 20.6 s even alone (§3.2).
   - The `make release`/`ship` family: fewer full runs mean fewer spawns competing for CPU (§3.2).
   - The duplicate `go list` walk.
2. `internal/checks`: 22 pool tests with Σ 36.5 s, mostly `scratch_test.go` and `after_merge_test.go` scratch repos,
   at 4–5 s each.
3. `internal/app`: 211 pool tests with Σ 94 s. `idle_test.go` is 17.9 s. The watch family is 34 s: 11 tests
   at 3.8–4.5 s each, which is a fixed wait per test and worth a look before any of them is cut; 5 carry a keep tag.
   Under `t.Parallel`, about a quarter of that is wall time.
4. `internal/setup`: the pane and placement tests, Σ 35.5 s in the pool.
5. `internal/affected` `TestGoOborKit`: 9.1 s on a cold build cache and 0.9 s warm. Keep it.

`tests/upgrade` (36 s) is one test that needs release binaries, and it skips without them. It is a release check;
making it a nightly-only test is a gate change, which is out of scope here, so this survey only names it.

## 5. Keep list (rules)

A pruning task keeps a test that matches any of these, whatever its coverage:

1. **Refusal text:** a test that asserts a refusal's or error's exact text, or that a call is refused. The text is
   the contract agents read.
2. **Replay and crash:** a test that restarts serve, replays the log, or cuts a step (`scratch` cut tables, crash
   after a gate commit, answers that replay). Coverage cannot see ordering.
3. **Guard deny:** every deny row of `internal/guard` (Bash, Edit/Write, load runs, headless runs, git writes) and
   every fail-closed test. A deny row is merged into a table, never dropped.
4. **Findings:** one test per entry point of a past finding's Extent (`TestRV…`, or RV-nn in the comment). Two
   tests of the same entry point of one finding may become one.
5. **Contract tables:** a test that pins a row of a documented table, such as `TestState…Row<N>` against
   `docs/state-model.md`. Its coverage is redundant by construction.
6. **One end-to-end test per entry point:** the single test that runs a whole path in order (the happy path of the
   loop, `obor init`, `make release`) stays even when narrower tests cover its statements.
7. **Probes of real behaviour** (races, timeouts, process groups such as `TestExecuteLeavesNoGroup`): they stay, but
   each may be shortened (a smaller timeout or wait) by a task that measures it.

And the converse: a test with no keep tag, in the greedy set, whose name and body add no assertion that another test
lacks, goes. Two tests that differ only in an input become one table.

## 6. Ranked plan

Each task owns a set of files; no file is in two tasks. "Lines" and "Σ s" are the pool's ceiling from §3.1, plus the
§3.2/§3.3 items named. "Wall s" is the expected gate saving: the pool's per-test seconds scaled by the package's wall
over its per-test sum, rounded. Every task keeps its package's statement coverage at today's value (the "keep" column)
and runs its package's tests before and after with `-cover`.

| # | Task | Files (exclusive) | Lines | Wall s | Keep coverage |
|---|---|---|---:|---:|---|
| P1 | Removed config keys past their window: drop fields, WARNs and tests; read `releaseMinor` from `VERSION` so the check fires (§3.3) | `internal/config/config.go`, `validate.go`, `config_test.go`, `validate_test.go`, `load_test.go` | ~250 test + ~60 prod | ~3 | config 91.5% |
| P2 | `cmd/obor`: `TestLeadPromptOnlyForAFreshClaudeLead`, the `make release` refusal fixture and one ship run, drop `TestAffectedGoOborKit`, the status and drive tables, the rest of the pool (§7) | every `cmd/obor/*_test.go` | ~1,200 | 10–25 | cmd/obor 84.5% (measured with subprocess coverage, §1) |
| P3 | `internal/checks` scratch fixtures: pool + after-merge pair + lanetable pair | `internal/checks/*_test.go` | 554 | ~25 | checks 86.1% |
| P4 | `internal/app` (a): identity, idle, loop, dispatch, role stop, prompts, app, ask, work text, turn phase | `internal/app/{ident,idle,loop,dispatch,rolestop,prompts,app,ask,worktext,turnphase}_test.go` | 2,197 | ~11 | app 92.6% (P4+P5 together) |
| P5 | `internal/app` (b): every other `internal/app` test file, including the drive/move, nightly, difficulty, follow and needs-line pairs | `internal/app/*_test.go` minus P4's | ~3,500 | ~10 | app 92.6% |
| P6 | `internal/setup` pool: setup, `obor init`, placement, plugin links | `internal/setup/*_test.go` | 1,286 | ~15 | setup 84.3% |
| P7 | `internal/launch` pool | `internal/launch/*_test.go` | 868 | ~2 | launch 88.0% |
| P8 | `internal/guard` tables: prompt, stop and session pools merged into tables; no deny row dropped | `internal/guard/*_test.go` | 571 | ~2 | guard 91.9% |
| P9 | Small packages: herdr, store (pool only, not the migrations), web, project, view, run, mcp, affected, agents, roles, app/wire, testkit | their `*_test.go`, except `internal/store/{prompts,lanes}_test.go` | ~1,300 | ~20 (web 10, affected 0: keep `TestGoOborKit`) | each at today's value (§2) |
| P10 | Conformance trim: the settled probes (§3.4) | `tests/conformance/live_*.go`, `live.go` | ~120 | 0 (live run −20 s) | – (live tier: 34 PASS, same checks) |
| P11 | Store migrations below schema 9, **after the owner raises `upgradeFrom`** | `internal/store/store.go`, `prompts_test.go`, `lanes_test.go` | ~150 test + migration code | 0 | store 74.5% |
| P12 | v0.26 removals: `TestMonitorIsRemoved` and its stub, `TestConfigValidateOldShape`, `TestOldShapeRefused` and the loader's pre-v0.23 message. Dispatched only after P1 and P2 have merged (it shares `config.go` and `load_test.go` with P1 and two `cmd/obor` files with P2), so it overlaps no open task | `cmd/obor/main.go` (the `monitor` case), `cmd/obor/main_test.go`, `cmd/obor/config_test.go`, `internal/config/config.go` (the "predates v0.23" message), `internal/config/load_test.go` | ~60 | 0 | – |

**Order.** Start with P1 and P2: a bug-shaped finding, then the largest time win. Then P3, P4 and P5 for gate time
and the bulk of the lines, then P6 to P9. P10 is independent. P11 waits for an owner decision and P12 for v0.26.

**Expected total.** If every pool test went, the ceiling is about 12k test-function lines, plus about 1k from the
merges in §3.2 and §3.3: roughly 13k of 75k (17%). A realistic yield after the per-file review is about half: 6–8k
lines, and about 30–45 s off the `cmd/obor`, `checks` and `app` critical path on a loaded machine. Helpers and rigs
that lose their last caller go too; `make deadcode` excepts `testkit` and `herdrtest`, so each task checks them by
hand.

**Each task's evidence.**
- Re-measure the package's `-cover` before and after; it must not drop.
- Re-run per-test coverage for the file group when a removed test's statements look unique after edits (the scripts
  in §1 run in about 20 minutes over the whole suite).
- For every removed test, name in the report the test that covers it, or the table row it became.

## 7. Appendix: the candidate pool per file

The pool from §3.1: tests in the greedy set with no keep tag (`Test` prefix dropped). A file's line count is its
pool tests' function lines.

- `internal/app/ident_test.go` (25 tests, 462 lines): IdentStartByServe, IdentFreshSession, IdentStartedByHand, IdentTwoLabels, IdentPaneMoved, IdentAtShell, IdentMoveAddressReused, IdentStartRowRetired, IdentNamedStartBeatsLabel, IdentUnnamedSessionNoProof, IdentAmbiguityEnds, IdentLeadTwoPanes, IdentOutsideLane, IdentOrderIndependent, IdentStopWhileDisconnected, IdentInboxOfAnotherSession, IdentSessionUnknown, IdentAmbiguityStillPrompts, IdentOnePaneTwoAgents, IdentStopMovedPane, IdentBriefWorkByRegistry, IdentBriefUsesPaneAddress, IdentEqualProofTerminal, IdentStartClaimWithoutTie, IdentQueuedBriefMoveBeforeEffect
- `internal/setup/setup_test.go` (18 tests, 458 lines): InitCreatesConfigDocsAndGitignoreUncommitted, InitCopiesNoQueueFile, InitAddsGitignoreLinesOnce, RoleSetupGitignoresTheReviewsDir, RoleSetupLeavesAnExistingReviewsLineAlone, InitDoesNotWriteThePluginMarkerLine, PaneSplitsTheTabAndStartsTheAgentInOrder, PaneSplitsTheLargestPane, PaneSplitsTheLastOfTiedPanes, PaneNewTabCreatesATabWithCwdAndEnv, SetupRoleStartsNoAgentWithoutStart, NewTabInTheProjectsWorkspace, ReviewerAndDeepDiveRunInTheRoot, RoleIntegratorStartsInTheRootAndNeedsAType, RoleLeadNewTabGetsRoleEnv, LeadKeepsRemoteControl, SetupAgentsRetiresUnderHomeConfig, KitManifestMCPServers
- `internal/setup/oborinit_test.go` (8 tests, 288 lines): OborInitSetsUpTheProject, OborInitCommitsAnUnbornRepository, OborInitLeavesACommittedRepositoryAlone, OborInitInstallsTheGitHooks, OborInitRerunChangesNothing, LaneDoneChecksTheMarkersCommand, OborInitReviewerOpusWithoutCodex, OborInitStartsNoAgentSoNoPaneWaitsOnTheTrustDialog
- `internal/launch/launch_test.go` (6 tests, 288 lines): StartFirstPrompt, StartEndsWithTheAgentUp, StopPerKind, StopGone, LaunchReportsIdentity, FreshStartRolePrompt
- `internal/app/loop_test.go` (6 tests, 246 lines): LoopHappyPath, LoopConflictResolvedThenMerged, LoopMovedConflictResolved, LoopMergedOutsideTheApp, LoopErrorIsAFactOnceAndClears, LoopPausedOffTargetBranch
- `internal/app/idle_test.go` (11 tests, 244 lines): IdleAfterTheLastMergePostsOneLine, IdleAfterADropPostsOneLine, IdleNotWhileAnotherTaskRuns, IdleBlockers, IdleAckStopsThePing, IdlePingsOnceAfter30Minutes, IdleLineAfterTheWatchLine, DispatchEndsIdle, ReviewerBriefEndsIdleAndANewEpochStarts, DeepDiveBriefEndsIdleAndANewEpochStarts, ReviewRequestEndsIdleAndANewEpochStarts
- `internal/app/dispatch_test.go` (4 tests, 232 lines): DispatchHappyPath, DispatchRedoOntoTheTasksLane, DispatchNamesIdleLanes, DispatchDelivery
- `internal/setup/placement_test.go` (4 tests, 223 lines): PlacementFollowsTheProjectsWorkspace, PlacementPicksTheProjectsBusiestWorkspace, PlacementTable, PlacementHonoursAnExplicitTabIntoAMixedTab
- `cmd/obor/status_test.go` (9 tests, 216 lines): StatusJSON, StatusShowsRunningGate, StatusShowsRunningPublish, StatusNumberedStepKeepsStep, StatusIdleTaskHasNoRunning, StatusShort, StatusEventsBad, StatusShowsTheEffort, StatusWarnsAClaudeLaneWithoutThePlugin
- `internal/app/rolestop_test.go` (6 tests, 213 lines): RoleStopIntegratorAfterVerdict, RoleStopIntegratorWaitsForIdle, RoleStopNextJobStartsFresh, BriefToStoppedRoleAgent, RoleStopAdvisorKeptRunning, BriefToStoppedWorkerStartsNothing
- `internal/guard/session_test.go` (9 tests, 209 lines): SessionWorkerArmed, SessionWorkerUnarmed, SessionRoleTextLine, SessionWritesNoSidFile, SessionOpencodeBegan, SessionNoIdentityFromRecords, SessionWritesOpencodePluginMarker, SessionWithOborAgentIgnoresOtherRecords, SessionLabelledPaneNoLostIdentity
- `internal/launch/session_test.go` (5 tests, 204 lines): ResumeRecordedSession, CodexStartReportsRollout, CodexRolloutSearchInBackground, CodexNoRolloutOpen, CodexSearchPinnedToStartedProcess
- `internal/app/prompts_test.go` (6 tests, 195 lines): PromptsOfAFetchedJob, PromptsOfABriefAndAStart, PromptsReviewJobAfterAChangedPatch, PromptsWorkJobRefetchedAfterASpecRev, PromptsFixJobRefetchedAfterASpecRev, PromptsReviewJobRefetchedAfterASpecChange
- `internal/herdr/socket_test.go` (5 tests, 186 lines): SocketSnapshotAndAgents, SocketStartSentOnce, SubscribeEvents, SubscribeClosesCreateRace, StreamEnds
- `internal/store/store_test.go` (5 tests, 179 lines): OpenRWIsTheWriter, BackupMakesAReadableCopy, TwoReadersWhileWriterWrites, OpenRWInAPathWithSpaces, OpenOddPath
- `internal/guard/guard_test.go` (9 tests, 178 lines): ProtectedUnlockReadsTheStoredSpec, NoConfigAllowsEverything, RoleFromEnvAndEventCwdNotClaudeProjectDir, LeadEditsAnything, NoIdentityFromTheSessionID, ReviewsWriteProfile, FindExec, DriverLaneBoundCommitsNoPush, TriageRoleGuard
- `internal/launch/effort_test.go` (3 tests, 166 lines): StartEffort, OpencodeModelIsTheOpencodeStartsAlone, OpencodeStartNamesItsGeneratedConfig
- `internal/app/app_test.go` (6 tests, 159 lines): IdleStallsThenNeedsYou, WorkingAgainResumes, NotStalledWhileAChildRuns, NotStalledWhileTheTranscriptChanges, BlockedNeedsYou, PaneExitedNeedsYou
- `internal/checks/scratch_test.go` (6 tests, 154 lines): ScratchMovedGateMergesSubmitted, ScratchMovedGateConflict, ScratchGateForeignState, ScratchGateResolvedAfterRed, ScratchAfterMerge, ScratchGateKeepsSmallDiff
- `internal/app/worktext_test.go` (8 tests, 151 lines): WorkTextCarriesBothRoleTexts, WorkTextSkipsAMissingProjectDoc, RoleTextAtTheCapIsInlined, WorkerPromptNamesNoRoleText, WorkJobSaysEachRuleOnce, WorkJobNamesTheConfiguredReportSections, WorkJobNamesTheDefaultReportSectionsOnce, FixJobNamesTheKitRoleTextAloneWithoutAProjectDoc
- `internal/app/ask_test.go` (6 tests, 151 lines): AskStatusDoesNotWaitForServe, AskRetriesStopAtCallerDeadline, AskDoesNotRetryTimeout, StopFailureLandsNearItsDeadline, TurnFailsAtItsDeadline, LostReplyAnsweredFromStore
- `internal/web/web_test.go` (5 tests, 149 lines): ViewAPIIsTheViewsJSON, SetupAPI, JobTextIsThePromptsRow, AgentPage, LongPollAnswersWithANewRecord
- `internal/app/messages_test.go` (5 tests, 149 lines): SendWaitingADayNeedsYou, BriefOfAFreshStart, BriefToALabelledPaneWithoutAName, BriefSupersedesAQueuedBrief, BriefLeavesLeadLinesQueued
- `internal/app/turnphase_test.go` (5 tests, 144 lines): TurnDecide, TurnCodexWorkingFirst, TurnClaudeStartThenStop, TurnCodexAbortedAndNewError, TurnWorkerPhases
- `internal/app/answer_test.go` (2 tests, 141 lines): AnswerAfterTheStopHooksTurn, NeedsYouAnswerPathsAreAccepted
- `internal/config/config_test.go` (6 tests, 139 lines): DefaultsAndDerivedKeys, LocalOverlay, SpecRemoved, Get, Defaults, LaunchArgs
- `internal/config/validate_test.go` (6 tests, 131 lines): ValidateRemovedLeadsWarn, ValidateRemovedT470Keys, RemovedKeysWithinTwoVersions, RemovedByV010KeysAreUnknown, ValidateTemplatesAndKitConfig, ValidateAcceptsTheResearcherRole
- `internal/app/fetch_test.go` (4 tests, 122 lines): FetchUndelivered, FetchReviewJob, FetchBlockedAsks, FetchCodexOpenCodeFinishByTaskDone
- `internal/launch/mcp_test.go` (2 tests, 117 lines): StartStrictMCPConfig, LaneStartStrictMCP
- `internal/app/freshjob_test.go` (3 tests, 117 lines): FreshIntegratorPerJob, FreshIntegratorIdleBound, JobNextOldestNotTheMapsFirst
- `internal/app/stopped_test.go` (2 tests, 116 lines): OpenCodeStoppedReset, StoppedRoleAgentLine
- `internal/herdr/panes_test.go` (3 tests, 109 lines): SocketSnapshotWorkspacesAndTranscript, PanesPromptSendsNoEnter, PanesPromptAgainClearsTheLine
- `cmd/obor/send_test.go` (2 tests, 103 lines): SendToProjectWhoseConfigDoesNotLoad, PosterKeepsProjectsWithOnePrefixApart
- `internal/project/projects_test.go` (2 tests, 97 lines): ProjectsKeepsConfigsItCannotLoad, ProjectsRegisterAtOnce
- `internal/app/waiting_test.go` (4 tests, 97 lines): WaitingDispatchesOnceAfterMerge, WaitingDependencyDroppedLater, WaitingDropped, WaitingForATaskMergedByHand
- `internal/app/freshbrief_test.go` (2 tests, 97 lines): FreshBriefPerRole, FreshBriefSupersedesQueued
- `internal/app/answerreach_test.go` (5 tests, 97 lines): AnswerReachesIdleWorkerHerdrSessionStale, AnswerToWorkingWorkerAtNextIdle, UndeliveredAnswerEndsWhenSettled, SettledAnswerInFlightOrOtherTaskStays, ResumeStalledEndsOlderAnswer
- `internal/config/models_test.go` (4 tests, 96 lines): BuiltinModelsResolve, KindsArgsAppended, ModelsOverride, LanesCount
- `internal/app/lines_test.go` (3 tests, 95 lines): WorkedNotNewerBrief, BriefKeepsAStartedDelivery, BriefStatus
- `internal/app/review_test.go` (4 tests, 91 lines): MergeSubjectResolved, ReviewClaimIsInFlight, ReviewScopeCoversTasks, ReviewVerifyOutsideScope
- `internal/app/phase_test.go` (2 tests, 91 lines): PhaseOfEveryStateAndOverlay, PhaseOverlaysFromTheLog
- `internal/app/mergehold_test.go` (4 tests, 91 lines): MergeHoldWaitsWhileARoleWorks, MergeHoldWaitsForAnOpenBrief, MergeHoldCap, MergeHoldGatesGoOn
- `internal/app/needsline_test.go` (4 tests, 89 lines): WaitingLineNeedsYouQueuesNoLine, NeedsYouLineRepeatsWhileItStays, NeedsYouLineRepeatsEndWithTheNeedsYou, NeedsYouLineRepeatWaitsForTheDeliveryOfThePreviousLine
- `internal/setup/pluginlinks_test.go` (3 tests, 87 lines): OborInitLinksThePluginIntoTheLanes, OborInitRerunLinksIntoAnExistingLane, PaneWithoutStartStartsNoAgent
- `internal/app/admit_test.go` (3 tests, 87 lines): AdmitReviewWaitsForPublish, AdmitVerifyWaitsForPublish, AdmitDeepDiveBriefWaitsForPublish
- `cmd/obor/serve_loop_test.go` (4 tests, 86 lines): ServeLoopHappyPath, ServeLoopMergeBodyKeepsSummary, ServeLoopConflictResolved, ServeLoopMovedTarget
- `cmd/obor/lead_test.go` (4 tests, 86 lines): LeadRunningTouchesNothing, LeadLabelledPaneSession, LeadUntypedRunningOrLabelled, LeadPromptOnlyForAFreshClaudeLead
- `internal/app/log_test.go` (4 tests, 84 lines): Ready, AppendInsertFailure, DoRunsOnceAndReturnsStoredResult, DoFailedEffectStaysPending
- `internal/checks/specgate_test.go` (2 tests, 83 lines): GateSpecInMessage, GateResolvedSpecInMessage
- `internal/setup/rename_test.go` (2 tests, 82 lines): RenameMapsConfigStoreFilesAndWorkspace, RenameRerunIsAlready
- `internal/app/nightly_test.go` (3 tests, 82 lines): NightlyEveryNightWhileOn, NightlyOffNeverRuns, NightlyRedNeedsYouUntilGreen
- `internal/app/deliverkind_test.go` (3 tests, 82 lines): WakeByKind, BriefByKind, BriefTypedWithoutAnInboxRecord
- `cmd/obor/triage_test.go` (2 tests, 80 lines): TriageSessionStartRecordsItsInbox, TriageStopFailureNamesIt
- `cmd/obor/config_test.go` (3 tests, 80 lines): ConfigValidateOldShape, WhoamiDeepDiveAndLead, WhoamiCustomRole
- `internal/app/rolestatus_test.go` (5 tests, 78 lines): StoppedRolesLeadStopped, StoppedRolesAtShellWithoutServeStop, StoppedRolesTwoLabelledPanesUnknown, StoppedRolesHerdrDownUnknown, StoppedRolesNoPane
- `internal/app/mergewatch_test.go` (3 tests, 76 lines): WatchedDropPostsTheDroppedLine, WatchedSetDropEndsIt, WatchNoteOnTheMergedLine
- `internal/config/load_test.go` (3 tests, 75 lines): OverlayMatchesJq, OverlayUnknownKeyInLocalFails, OverlayOrderedMapsKeepOrder
- `internal/guard/prompt_test.go` (7 tests, 71 lines): PromptWorkerUntaggedGetsTheQuestion, PromptTaggedOrSlashCommandQuiet, PromptPastedContentWrapperUnwrapped, PromptPeerEnvelopes, PromptResumeSentenceQuiet, PersonTyped, PromptLeadHandoffLimits
- `internal/app/statuspending_test.go` (2 tests, 71 lines): StatusRunningGateWhileItsEffectRuns, StatusPendingAfterMergeFailure
- `internal/app/watch_test.go` (2 tests, 70 lines): WatchdogTable, NotifyRetryBacksOff
- `internal/app/leadbatch_test.go` (3 tests, 70 lines): LeadLinesOnePost, LeadLateResultNewLine, LeadLinesTwoTargets
- `internal/app/effort_test.go` (4 tests, 70 lines): EffortHardLevelStartsAnOpusLaneAtHigh, EffortSpecTypeAndTypesNameIt, EffortPlainTypeNamesNone, DispatchWarnsAClaudeLaneWithoutThePlugin
- `tests/conformance/report_test.go` (3 tests, 68 lines): SaveKeepsOtherKindsTiersAndTheLastPassOnAFail, SaveOnlySkipsIsNoPass, APartialRunKeepsTheLastGaplessPassedVersion
- `internal/app/difficulty_test.go` (4 tests, 68 lines): DifficultyRunsTheLevelsFirstModel, DifficultyModelLineFirst, DifficultyWaitsForAFreeLane, DifficultyModelsOverrideTheLevel
- `internal/setup/mcp_test.go` (2 tests, 66 lines): ConfigMCPKeepsMemberOrder, SetupAgentsEveryKind
- `internal/app/placement_test.go` (1 test, 63 lines): PaneInRanksTheLabel
- `internal/view/setup_test.go` (1 test, 62 lines): CustomRolesListed
- `internal/app/stoppedwatch_test.go` (1 test, 61 lines): StoppedBeforeTheNextCheck
- `internal/app/briefgone_test.go` (3 tests, 61 lines): BriefEndsWhenItsAgentIsGone, BriefNotWorkedStaysOpenWhenItsAgentStops, BriefUnderAStopStaysOpenWhenItsAgentIsGone
- `cmd/obor/stopfailure_test.go` (1 test, 60 lines): TurnHooks
- `internal/run/run_test.go` (3 tests, 59 lines): RunHeavyHoldsSlot, RunHeavySlotHeld, RunSignalReleases
- `internal/mcp/mcp_test.go` (2 tests, 59 lines): NoRoleWarns, JobNextPerKind
- `internal/config/customroles_test.go` (1 test, 59 lines): FoldedRolesFromStrings
- `internal/app/serve_test.go` (2 tests, 57 lines): LogAppendErrorInStatus, MergeLoopNeedsIntegrator
- `internal/app/loggedwork_test.go` (1 test, 57 lines): LogWorkedTwoPassesAtOnceRecordOneFact
- `internal/app/looporder_test.go` (4 tests, 56 lines): LoopTasksSortedByName, WorkerJobNextTwoJobsInLaneTakesFirstTaskByName, IntegratorJobNextEqualSeqsTakesFirstTaskByName, HeldLaneOfTwoDispatchesIsNewestDispatch
- `internal/web/phase_test.go` (1 test, 55 lines): PhaseAgreesAcrossLanesPipelineAndWeb
- `internal/app/sessions_test.go` (2 tests, 54 lines): SessionIdentityCrossCheck, SessionInStatus
- `internal/app/startsync_test.go` (1 test, 53 lines): IntegratorStartsOneAtATime
- `internal/app/accept_test.go` (2 tests, 52 lines): AcceptNewSpecRevReviewsAgain, AcceptSameSpecRevKept
- `internal/checks/classify_test.go` (1 test, 51 lines): OborKitConfig
- `internal/app/stopwake_test.go` (3 tests, 51 lines): StopWakeNewTurnNoAlarm, StopWakeNotALimit, StopWakeCodexRateLimits
- `internal/affected/affected_test.go` (2 tests, 51 lines): GoOborKit, GoFullGlobsAreConfig
- `internal/checks/lanetable_test.go` (3 tests, 50 lines): LaneTypeIsTheDispatchs, MergeEvidenceSubject, MergeEvidenceResolvedMerge
- `internal/app/earlyverdict_test.go` (1 test, 49 lines): EarlyTaskDone
- `internal/run/receipts_test.go` (2 tests, 48 lines): RunExportsChangedFiles, RunExportsCheckCPUs
- `internal/launch/dispatchtype_test.go` (1 test, 48 lines): StartTakesTheDispatchType
- `internal/checks/gitlock_test.go` (2 tests, 48 lines): SubmitWaitsForLockOfLiveGit, SubmitRemovesStaleLock
- `internal/app/herdrdown_test.go` (1 test, 47 lines): ServeWaitsForHerdr
- `internal/guard/stopfailure_test.go` (1 test, 46 lines): TurnAndStopFailureResolveTheLaneAgent
- `internal/checks/lane_test.go` (2 tests, 46 lines): LanePrepare, LanePrepareRedo
- `internal/guard/stop_test.go` (4 tests, 45 lines): StopWorkerHandoffUnaffected, StopWorkerRunningBackgroundTaskAllows, StopNoConfigAllows, StopRoleErrorWorkerLogUnreadable
- `internal/app/sendback_test.go` (1 test, 45 lines): LeadSendbackFromNeedsYou
- `internal/app/failure_test.go` (1 test, 45 lines): LoopFailureNotCounted
- `cmd/obor/plugin_wiring_test.go` (1 test, 45 lines): HooksJSONWiring
- `internal/app/configset_test.go` (2 tests, 44 lines): ConfigSetNightlyOffWhenOff, ConfigSetWaitsForPublish
- `internal/setup/opencode_model_test.go` (1 test, 43 lines): SetupOpencodePaneEnvCarriesTheModel
- `cmd/obor/plugin_test.go` (1 test, 43 lines): HerdrManifest
- `internal/store/prompts_test.go` (1 test, 42 lines): OpenRWMigratesTheOldSchema
- `internal/app/specrev_test.go` (2 tests, 42 lines): SpecSetOnAWorkingTaskSendsNoLine, TaskDoneOnTheJobsSpecRevSubmits
- `internal/setup/show_test.go` (2 tests, 39 lines): SetupShowPrintsTheTable, SetupShowListsTheResearcher
- `internal/app/drivelines_test.go` (1 test, 39 lines): AnswerToDriveClosedOnLandOrDrop
- `tests/conformance/stubmcp/main_test.go` (3 tests, 38 lines): ReplyInitializeEchoesProtocolAndOffersTools, ReplyToolsListHasPing, ReplyIgnoresNotificationsAndNoise
- `internal/checks/nightly_test.go` (1 test, 38 lines): NightlyCommands
- `internal/app/verdict_test.go` (2 tests, 38 lines): VerdictServeNotRunning, StartIntegrator
- `internal/app/move_test.go` (1 test, 38 lines): MoveWorkingTaskToAnotherModel
- `internal/app/dropstart_test.go` (1 test, 38 lines): DropSelectedStep
- `cmd/obor/opencode_test.go` (1 test, 37 lines): OpencodePlugin
- `internal/project/project_test.go` (2 tests, 36 lines): FindReadsGitFilesWithoutForkingGit, LockKilledHolderFreesAtOnce
- `internal/checks/flakes_test.go` (1 test, 35 lines): RunPrunesOldLogs
- `internal/app/linestate_test.go` (1 test, 33 lines): SettledInFlightDeliveryLeftAlone
- `internal/view/background_test.go` (1 test, 32 lines): BackgroundOfOneIndexPerScan
- `internal/agents/agents_test.go` (1 test, 32 lines): VarsRegistry
- `internal/app/reportsection_test.go` (1 test, 30 lines): ReportSectionEndsOnlyAtAReportSection
- `internal/checks/after_merge_test.go` (2 tests, 28 lines): ScratchAfterMergeRunsInMainCheckout, ScratchAfterMergeGreenLog
- `cmd/obor/watch_test.go` (1 test, 28 lines): WatchSetRequest
- `internal/app/reviewverify_test.go` (1 test, 27 lines): ReviewCoveredVerifyDelivery
- `cmd/obor/sessionstart_test.go` (1 test, 27 lines): SessionStartOpencodeLaneReportsNoSid
- `cmd/obor/dispatch_serve_test.go` (2 tests, 26 lines): DispatchObsoleteSwitch, MergeLoopDispatchWiring
- `internal/view/load_test.go` (1 test, 24 lines): LoadWithoutServe
- `internal/app/startjob_test.go` (1 test, 24 lines): StartJobOtherPatch
- `internal/app/drive_test.go` (1 test, 24 lines): DriveDrop
- `internal/config/edit_test.go` (1 test, 23 lines): EditNightlyAtOnOff
- `internal/web/idle_test.go` (1 test, 22 lines): PageShowsTheIdleState
- `internal/project/laneagent_test.go` (1 test, 22 lines): LaneAgentReadsTheRow
- `internal/guard/config_test.go` (2 tests, 22 lines): PromptConfigUnknownKeyAndUnreadable, HeadlessFormsFromTheBridges
- `internal/app/subject_test.go` (1 test, 22 lines): SummarySection
- `internal/store/lanes_test.go` (1 test, 21 lines): LaneRowKeepsItsType
- `internal/checks/receipts_test.go` (2 tests, 21 lines): ReceiptGuardAlwaysRuns, GateExportsCheckCPUs
- `internal/app/wire/client_test.go` (1 test, 21 lines): CallDeadlineEnv
- `internal/app/states_test.go` (2 tests, 21 lines): StatesOneRowPerState, StateDrivingRowT751
- `cmd/obor/reviewcycle_test.go` (1 test, 21 lines): ReviewCloseNoStartFact
- `internal/app/questions_test.go` (1 test, 20 lines): OpenQuestionsFirstWord
- `cmd/obor/main_test.go` (1 test, 20 lines): MergeIsUnknown
- `internal/launch/restore_test.go` (1 test, 19 lines): RestoreFreeLaneWorkerNotStarted
- `internal/app/waitstale_test.go` (1 test, 18 lines): WaitSelectionNotReplacedDispatches
- `internal/app/prompt_test.go` (1 test, 18 lines): PromptFreshDelivered
- `cmd/obor/drive_test.go` (1 test, 18 lines): DrivePromptNamesTheLead
- `internal/app/follow_test.go` (1 test, 16 lines): FollowBinaryStreamLost
- `internal/testkit/testkit_test.go` (1 test, 15 lines): IsolateUnsetsCheckVariables
- `internal/launch/stop_internal_test.go` (1 test, 14 lines): Leads
- `internal/app/lanetype_test.go` (1 test, 14 lines): RedoViaTheRealLaneTableKeepsItsTypeAndEffort
- `internal/launch/oneline_test.go` (1 test, 12 lines): StartPromptWithoutLineBreaksUnchanged
- `internal/roles/roles_test.go` (1 test, 11 lines): OptionalRoles
- `internal/herdr/contract_test.go` (1 test, 11 lines): CheckProtocol
- `cmd/obor/sleep_test.go` (1 test, 11 lines): StartClearsOnlyTheStopsPause
- `internal/app/status_test.go` (1 test, 10 lines): AnswerPathNamesTheTaskNotTheLane
- `internal/app/concurrency_test.go` (1 test, 10 lines): WaitingNeedsYouDropCommand
- `internal/config/daemon_test.go` (1 test, 9 lines): LoadDaemonTypeErrors

