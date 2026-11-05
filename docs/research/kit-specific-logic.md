# obor-kit-specific logic in the shipped obor product

Survey for the lead after owner decision 11-05 (docs/decisions.md line 9, T-901): obor-kit's own needs belong in its
config / Makefile / repo tooling, not in obor's code. Scope: `cmd/`, `internal/`, `bin/`, `hooks/`, `skills/`,
`templates/`, `.claude-plugin/`. Read-only; no code was changed. Line counts are `wc -l` at main `2e081751`.

## Answer

Nothing as big as `internal/affected` was found in the product's runtime code. The kit-specific weight is mostly in
**tests that ship in the product's Go packages**, and in a few **hard-coded Go assumptions** that every project gets.
Ranked by size and by how much each shapes the product for one user:

| # | What | Size | Used by | Verdict |
|---|------|------|---------|---------|
| 1 | Release / Makefile / install tests in `cmd/obor` that test `scripts/` and the kit's Makefile | ~2,800 lines of test | obor-kit only | move out of the product's test tree |
| 2 | `flakes`: log, parser and `obor flakes` read only Go `go test` output | ~880 lines + cmd 115 | every project writes, only Go projects get entries | generic idea, Go parser: owner call |
| 3 | Guard's load-run denial of `go test -count/-race` and parallel test commands | ~140 lines + 94 test + messages | every project, effective only on Go | Go-specific rule in a generic guard: owner call |
| 4 | Merge classes (`merge.classes`, `neutral`, `guard`, `affected_full`, `check_affected`) + `obor affected go` | ~900 lines incl. tests | obor-kit only (neither axis config sets any) | owner said `internal/affected` stays; the kit-only tests and docs move |
| 5 | Kit-config tests in product packages (`TestOborKitConfig`, `TestOborKitTrimGoCache`, `TestGoOborKit`, kit half of `TestValidateTemplatesAndKitConfig`) | ~250 lines | obor-kit only | delete or move to kit tooling (T-901) |
| 6 | `obor projects --notify` | 92 + 93 test + 42 | one caller: `scripts/ship` | move into `scripts/ship` |
| 7 | Go build cache probe (`go env GOCACHE`) in serve and the disk-low message | ~25 lines | runs in every project's serve; useful to Go projects only | make it config or drop |
| 8 | Skill / template text naming kit things | ~8 lines | every project's lead and workers read them | reword |
| 9 | Self-hosting (shim `self_host`, `dist/obor`, dev marketplace, `selfexec`) | ~330 lines | needed so obor runs on itself | stays; see "not a finding" |

## Findings

### 1. Release, Makefile and install tests live in the product's `cmd/obor` tests
- Files: `cmd/obor/makefile_test.go` (532), `release_test.go` (846), `release_steps_test.go` (491),
  `release_notes_test.go` (316), `release_flaky_test.go` (134), `release_market_test.go` (111),
  `release_upgrade_test.go` (139), `install_test.go` (251). About 2,800 lines, plus the `cmd/obor/notify_test.go` in #6.
- What: they run the kit's `Makefile` (`make check`, `make release`, `make publish`), `scripts/release`, `scripts/ship`,
  `scripts/publish` and `install.sh` against fixture repos, and assert obor-kit's marketplace names (`obor-kit-dev` on
  main, `obor-kit` at a tag), GitHub slug `foksa/obor-kit`, `bin/release-url`.
  Evidence: `grep -n obor-kit cmd/obor/release_test.go` (lines 22, 34, 373-410, 604), `makefile_test.go:257-259`,
  `release_notes_test.go:112-213`, `install_test.go:105`.
- Who uses: obor-kit only. A project that is not obor-kit has no Makefile like this one and never calls these scripts.
- Legit generic feature? No. These test the kit's release process, not an obor feature.
- Why it matters beyond size: they run in `cmd/obor`, the package every merge to `cmd/**` gates on (`make check-affected`
  selects it), so the kit's release tooling slows and can flake every product merge. `makefile_test.go` runs `make`
  with a fake `go`; `release_test.go` makes git repos.
- Home: kit's own repo tooling, not shipped. Move to a package outside the product's import graph (e.g. `tests/release/`,
  like `tests/upgrade`, which already tests `make upgrade-check`), run by the Makefile's `check` as a separate step or by
  `make release` only. Needs a lead decision on the gate (see tasks).
- Caveat: `plugin_wiring_test.go` (564) mostly checks manifest invariants (hooks.json wiring, MCP servers, VERSION vs
  plugin.json); `:117` asserts the dev marketplace name. Those invariants are about what ships, so I would keep the file
  and only move the marketplace-name lines with the release tests. Not read line by line.

### 2. `obor flakes` and the flake log parse only Go output
- Files: `internal/checks/flakes.go` (366), `flakes_test.go` (396), `cmd/obor/flakes.go` (39), `flakes_test.go` (76);
  `internal/checks/runner.go:103,494` call `logFlakes` after every check run of every project.
- What: `failingNames` (`internal/checks/flakes.go:38-62`) reads `--- FAIL: <name>` and `FAIL <pkg>` /
  `[build failed]` lines: `go test` output only. `merge.fail_grep` is configurable, but the name extraction is not. For
  axis-alies (`error TS|×|\(fail\)|…`) and axis-maps (`error|FAIL|Expected|Received`) the `--- FAIL:` prefix never
  matches, and the `FAIL <pkg>` branch fires only on a line that starts with `FAIL` and a second word. So bun projects
  mostly record nothing, and `obor flakes` is blank for them.
- The struct comment says `Retried`/`PassedOnRetry` are written by `scripts/release`'s own rerun, "the checks have had
  no rerun since T-864". So the one remaining producer of the retry fields is the kit's release script
  (`cmd/obor/release_flaky_test.go` tests it).
- Who uses: the reader (`obor flakes`) and the retry fields are kit-only; the writer runs in all projects.
- Legit generic feature? The idea (a 7-day flake log) is generic; the parser is Go-only. This is the closest thing to
  `internal/affected`'s situation: shipped, generic name, one effective user.
- Home options (owner call): (a) stays, but name the parser honestly in docs (Go test output) and keep as is;
  (b) make the name extraction a config regex (`merge.fail_name`, like `summary_regex`); (c) move the log and the reader
  to the kit's release tooling and drop `logFlakes` from the runner.
- I did not measure how many lines of `flakes.go` are the retry fields.

### 3. The guard denies Go load runs for every project
- Files: `internal/guard/shell.go:538-677` (`loadRunDeny`, `goTest`, `testBinary`, `testRun`, `testFlags`, `goSub`,
  `overCount`, `flagInt`, `raceOn`, plus `case "go"` at `:749`, the `par` fan-out flag at `:59` and the background-unit
  tracking in `stmts`), `internal/guard/session.go:214` (`loadRuns`, shown in every session's hook line),
  `guard_test.go:321` (`TestLoadTestRunsDenied`, 94 lines).
- What: `go test -count>1`, `-race`, `-test.count`, `-test.race`, and two test commands at once are denied for all roles
  in all projects. The deny text says "one plain `go test <pkgs>` run". Origin: T-543, the kit's own 4-lane load flakes
  (AGENTS.md "No load runs").
- Who uses: obor-kit; it has an effect on any Go project and no effect on bun projects (`bun test` is not denied, and the
  axis configs show no equivalent rule).
- Legit generic feature? The need (stop a lane from loading the machine) is generic; the implementation is Go flags.
- Home options (owner call): (a) stays as a documented Go-aware guard; (b) generalize to a config list
  (`guards.deny_commands` with a regex + message) so axis can add `bun test --coverage`, and obor-kit's config carries the
  Go rule; (c) move the rule into obor-kit's own githooks / project hook, not the product. The existing machinery
  already resolves wrappers (env, xargs, sudo), which is the hard part, so (b) is a smaller change than it looks, but
  it is a design change, not a cleanup.

### 4. Merge classes, `affected_full`, `check_affected`, `obor affected go`
- Files: `internal/affected/affected.go` (135) + test (131), `cmd/obor/affected.go` (62) + test (63),
  `internal/checks/classify.go` (103) + `classify_test.go` (249), `internal/config/validate.go:127-165,350-365`
  (class uniqueness, neutral overlap warnings, nightly gating names), `internal/config/config.go:220-240`.
  docs/design.md "Change-based merge checks" (line 499) and its "Go projects" paragraph (line 558).
- What: the class machinery is generic (globs → checks), but both axis configs set none of `classes`, `neutral`,
  `guard`, `affected_full`, `check_affected`. Only obor-kit does. `obor affected go` is Go-specific.
- Owner decision 11-05: `internal/affected` stays as is. I therefore list it only for completeness: do not remove it.
  What does move: the kit-only tests of #5, and any kit-flavored docs.
- Legit generic feature with one user: yes. Home: stays.

### 5. Tests in product packages that read the kit's own config or Makefile
- `internal/checks/classify_test.go:139` `TestOborKitConfig` (51 lines): asserts the kit's `commands.check` is
  `make -s check`, `check_affected` is `make -s check-affected`, and that Go paths classify `partial:go`. It fails if the
  kit changes its own gate. T-901 will change that config, so this test is the first thing to break (this is the case
  the brief named).
- `internal/checks/classify_test.go:193` `TestOborKitTrimGoCache` (57 lines): runs `make trim-go-cache` against a
  throwaway `GOCACHE`. It tests the kit's Makefile target, not obor.
- `internal/affected/affected_test.go:96` `TestGoOborKit` (36 lines) + `fullGlobs` at `:13` ("obor-kit's
  merge.affected_full"): runs `affected.Go` against this repo's real import graph, so the expectations list the kit's
  own packages (`obor/internal/app`, `…/guard`, …). It breaks whenever the package layout changes; the kit's own
  refactors (T-690..T-692) already edited it. The fixture-based tests above it cover the logic.
- `internal/config/validate_test.go:400` `TestValidateTemplatesAndKitConfig`: validates `templates/*.example.json`
  (a product invariant, keep) **and** the kit's `.obor/config.json` (kit-only half).
- `internal/app/worktext_test.go:189` `TestWorkJobSaysEachRuleOnce` reads the kit's own `docs/agents/worker.md`; it also
  reads `templates/`, so it checks a real shipped text against a kit doc. Borderline; low priority.
- `internal/config/testdata/migrate/obor-kit.*.json` (4 fixtures) next to `axis-alies.*`: copies of real configs used to
  test config migration. Legit fixtures; only the name is kit-specific. No change.
- Home: delete `TestOborKitConfig`'s command-string assertions (keep the `Validate` call as a kit CI step, e.g.
  `bin/obor config validate`, which the kit's `guard` command already runs); move `TestOborKitTrimGoCache` to the same
  place as #1; delete `TestGoOborKit` (the fixture test covers the same code) or turn it into a smaller invariant.

### 6. `obor projects --notify`
- Files: `cmd/obor/notify.go` (92), `notify_test.go` (93), the `--notify` branch in `cmd/obor/projects.go` (42 total).
- What: sends the release notes to every other project's lead after an obor release. Header comment: "make ship runs
  it last". Single caller: `scripts/ship`.
- Who uses: obor-kit's release only.
- Legit generic feature? It is a user-visible command in `obor help` (`projects`), but no project but the kit's releases
  has release notes to broadcast. `obor projects` and `obor send <project> …` already exist.
- Home: kit tooling: `scripts/ship` loops over `obor projects` and `obor send`. Removes ~230 lines from the product.

### 7. Go build cache probe in serve
- `cmd/obor/serve.go:178-191` (`cacheDir`, runs `go env GOCACHE` with a 10 s timeout on first low-disk read),
  `internal/app/disk.go:23,57-59` (adds "Go build cache <dir> (go clean -cache)" to the low-disk needs-you).
- Who uses: obor-kit (its cache reached 53 GiB in a day, per the Makefile's `trim-go-cache` comment). In a bun project
  `go` is absent and the probe returns "". No harm, but the product knows Go's cache dir.
- Home: either a config hint (`paths`/`merge` string appended to the message) or drop it; kit's `trim_go_cache`
  nightly already handles the cause. About 25 lines; low priority.

### 8. Skill and template text that names kit things
All read by every project's agents.
- `skills/lead/commands.md:87`: "`merge.nightly_commands` — obor-kit's `conformance` and `trim_go_cache`": say "e.g. a
  cache trim" without naming the kit.
- `templates/task-spec.md:27`: "Identity, MCP or launch tasks: `make conformance TIER=live TURN=1 KIND=<kind>`…
  Delete this line otherwise." `make conformance` exists only in obor-kit's Makefile; in a project's spec it is a
  dead instruction the author deletes each time. Move it to obor-kit's own spec template (a project-local override) or
  make it a comment.
- `templates/agents/integrator.md:31-32`: two mentions of `docs/module-map.md`; only the kit has that file (the
  integrator's accept check "placement against `docs/module-map.md`"). Generalize to "the project's layout doc if it has
  one" or move to the kit's `docs/agents/integrator.md`.
- `skills/lead/SKILL.md:89` and `skills/lead/review-brief.md:113`: "`make release` refuses otherwise / counts it":
  `make release` is the kit's. The generic feature is `obor status`' `review due`. Reword.
- `templates/config.example.json`: an axis-alies config (name, `../axis-alies-baseline/m8`, `src/ai/**`) whose opencode
  `prompt_extra` says `go test ./<packages>` and "never make check". Not obor-kit, but the same defect: one project's
  settings presented as the template, with a Go line inside a bun config. It is also what `TestValidateTemplatesAndKitConfig`
  validates. Make it a neutral example.
- Not reviewed: `templates/agents/gotchas.md` (only skimmed), `templates/agents/triage.md`, `worker.md` beyond greps for
  Go and kit words.

### 9. Self-hosting and dev convenience (what is *not* a finding)
Needed so obor runs on itself; keep. The split you asked for:
- **Self-hosting needs it:** `bin/obor:36-46` `self_host` (inside an obor checkout, run the main checkout's binary, not a
  lane's); `OBOR_NO_SELF_HOST` pin (`internal/agents/agents.go:215`); `internal/selfexec` (140 + 176 test; serve follows
  a new binary: also used by plain users when `install.sh` upgrades the kit, so generic); the shim's `dist/obor` dev
  build precedence (`bin/obor:142,200`) and `has_hook` stale probe (T-195); `commands.after_merge.restart_serve`
  (T-652); `internal/version` `devBuild`; `internal/app/status.go:415` skew comment; `.claude-plugin/marketplace.json`
  `obor-kit-dev` (the release flips the name on the tag).
- **Dev convenience, not a need:** the repair hint's `make -C <kit> build` allow-list in `bin/obor:138-175` is only used
  when no usable binary exists (the kit's checkout without a build); it keeps the lead from being locked out. It is
  about 25 bash lines and ties the shim to a Makefile target. Defensible as self-hosting; flagged for the owner only.
- Also kit-flavoured but outside the shipped list: `tests/conformance` (live agent CLI checks), `tests/upgrade`,
  `tests/go/socketprobe`, `scripts/`, `Makefile`, `RELEASING.md`. `install.sh` clones the whole repo into
  `~/.obor/kit`, so they sit on every user's disk, but they are not obor code; nothing to move.
- Toolchain detection in `internal/setup/oborinit.go:361-385` (Makefile `check:`, `go.mod` → `go test ./...`,
  `package.json` → bun/pnpm/npm) is one of N toolchains: generic.

## Config keys: who sets what (read-only comparison)
Keys only obor-kit's `.obor/config.json` sets, versus `~/Gamedev/axis-alies/.obor/config.json` and
`~/Gamedev/axis-maps/.obor/config.json`: `merge.classes`, `merge.neutral`, `merge.guard`, `merge.affected_full`,
`merge.nightly_commands`, `commands.check_affected`, `commands.guard`, `commands.conformance`, `commands.trim_go_cache`,
`commands.after_merge.restart_serve`, `slots` (and `slots.cpus` → `CHECK_CPUS`, documented as `go test -p`),
`roles.researcher`. Keys axis sets and the kit does not: `lane_setup`, `lane_ready`,
`lane_links`, `check_fast`, `check_milestone` (kit uses it for govulncheck), `models`, `merge.timeout_grep` (now WARN
"no longer used").
Reading: the class / affected / nightly_commands family is a feature only obor-kit turns on, matching the 11-05
finding. No key is wrong to exist; they are generic features with one user. The risk is that they were designed around
the kit's shape (Go packages, one Makefile).

## What I could not verify
- Whether anyone outside these three projects uses the class keys. Only the two axis configs on this machine were read.
- How much of `internal/checks/flakes.go`'s 366 lines is retry / release-script support versus the log itself.
- Whether moving the cmd/obor release tests out changes the `make check-affected` time; I did not time them. Running
  them was out of scope (guard: one plain `go test`, no load runs).
- `web/`, `opencode/obor.js`, `herdr-plugin/`, `githooks/` were only listed; nothing in them matched the kit-name or
  Go-toolchain greps, so I treat them as generic.

## Task list, split by files
Each task is one lane's file set. Order is by value; 1-3 are the clear wins.

| T | Task | Files (only these) | Notes |
|---|------|--------------------|-------|
| A | Move release, Makefile and install tests out of the product test tree | move `cmd/obor/makefile_test.go`, `release_test.go`, `release_steps_test.go`, `release_notes_test.go`, `release_flaky_test.go`, `release_market_test.go`, `release_upgrade_test.go`, `install_test.go` to `tests/release/` (shared helpers `writeFile`, `writeExec`, `treeFile` come with them or stay in `cmd/obor`); `Makefile` (gate step), `.obor/config.json` (`merge.classes` for `tests/**`, `affected_full`) | Needs a lead call on when the kit tests run (every merge to `Makefile`/`scripts/` only, per T-901's "config, not code"). |
| B | Move `obor projects --notify` into `scripts/ship` | delete `cmd/obor/notify.go`, `notify_test.go`; edit `cmd/obor/projects.go`, `cmd/obor/main.go` (usage), `internal/help/help.go`, `scripts/ship`, `docs/releases` mention if any | `obor send` already exists. Check no project's docs name `--notify`. |
| C | Remove kit-config tests from product packages | `internal/checks/classify_test.go` (`TestOborKitConfig`, `TestOborKitTrimGoCache`), `internal/affected/affected_test.go` (`TestGoOborKit`, `fullGlobs` comment), `internal/config/validate_test.go` (kit-config half) | Do with T-901 so it does not break on the config change. The trim test goes with task A. |
| D | Reword skill/template text | `skills/lead/commands.md:87`, `skills/lead/SKILL.md:89`, `skills/lead/review-brief.md:113`, `templates/task-spec.md:27`, `templates/agents/integrator.md:31-32`, `docs/agents/*.md` of the kit if the lines move there | Text only; `cmd/obor/skills_test.go` and `internal/app/worktext_test.go` read these files, so run them. |
| E | Neutral `templates/config.example.json` | `templates/config.example.json`; check `internal/config/validate_test.go` and `templates/README.md` | Not a kit finding; same defect. |
| F | Owner call: flakes (#2) | `internal/checks/flakes.go`, `runner.go`, `cmd/obor/flakes.go`, `internal/help/help.go` | Choose (a)/(b)/(c) first. |
| G | Owner call: Go load-run guard (#3) | `internal/guard/shell.go`, `session.go`, `guard_test.go`, `AGENTS.md`, config schema if made configurable | Design change; do not start without a decision. |
| H | Go cache probe (#7) | `cmd/obor/serve.go`, `internal/app/disk.go` (+ test) | Low value, small; fold into another disk task. |

Order check for the lead: A and C touch the same package set as T-901, so sequence them after T-901 lands or fold C
into it. B, D and E are independent and small. F and G need the owner.

What T-901's own wording left open: the brief says "tests in internal/checks that read obor-kit's own config
(TestOborKitConfig) shipped as product tests". Confirmed (item 5); the same pattern is also in `internal/affected`,
`internal/config` and `cmd/obor` (item 1), which is where most of the lines are.
