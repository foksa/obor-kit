# Undecided-behaviour audit: what obor does that nobody chose (advisor, 2026-11-09)

Question: owner 11-09, after `merge.trailer`: "who knows how much stuff like this I have hidden in code, where the
model just decided to do random stuff". Tree: `staging` at 16a7b3f2, kit 0.32.0. Read-only; nothing was changed.

Three sweeps: (1) every config key and every value in obor-kit's and axis-alies' config, (2) what obor writes
without a config key (commits, files, `~/`, agent settings, prompts), (3) hardcoded numbers and names. About 250
items. Result: **15 for the owner to decide** (§1), **9 delete tasks and 13 config values to edit** (§2), the rest
KEEP with its citation (§3).

Origin: `git log -S` on `staging`. History before 3dcc38a7 (v0.16.0, 10-30) is squashed there, but the local tag
`pre-public` holds the 2,408 commits before it, so most origins are traced through it and marked `pp:`. "No owner
line" means `docs/decisions.md` has no entry for it; a task id is an origin, not a decision.

What the trailer's pattern looks like in the rest of the code: a value from the first design drafts (10-08, commits
8758a3b4 "M0" and 66cab21c "Design v2") that every later task carried forward. 15 config keys come from those two
commits: `version`, `prefix`, `lanes_dir`, `task_id_pattern`, `paths.reviews`, `lane_links`, `protected_files`,
`limits.max_panes_per_tab`, `commands.<name>.when`, `done_file_sections`, `kinds.<kind>.prompt_extra` (then a lane
type's), `merge.scratch_dir`, `merge.summary_regex`, `merge.fail_grep` and `merge.trailer`. Five of them go:
the trailer in T-1103, four in §2.

## 1. DECIDE

Ordered by who sees it: other people first, then the owner, then adopters. Each has a recommendation; "yes" to a
line is enough.

**Public git history**

1. **Lane commits say `Implemented by ob-l-01, reviewed by integrator.`** (`internal/checks/scratch.go:108-110`).
   Written at submit, before any review has run. No code reads it. 600 commits on `origin/main` carry it. Origin:
   pp: 4d7dbfbc 10-08, spec T-04; no owner line. Choose: keep, or a lane commit is `T-xx: <summary>` only.
   Recommend: delete, in task A (§2).
2. **Claude Code's own trailers stay after T-1103.** The lead's and a drive's hand commits end with
   `Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>` and `Claude-Session: https://claude.ai/code/session_…`
   (42 of the 242 commits since 11-08; e361b15f and 16a7b3f2, the commits that removed `merge.trailer`, carry
   both). obor does not write them and passes no setting for them (`internal/config/models.go:248-250` sets two
   other keys). Choose: obor-kit's committed `.claude/settings.json` turns Claude Code's commit attribution off
   (today the file holds only `enabledPlugins`), or obor passes it for every Claude agent of every project.
   Recommend: the project file (decisions 11-08, "native files first"); the worker of the task checks the key's
   name in Claude Code's settings reference.
3. **What is already public.** `origin/main` (1,547 commits) holds 1,403 `Co-Authored-By: Claude …`, 600
   `Implemented by …`, 336 `Claude-Session:` URLs and 57 `Model: …` lines. Choose: leave, or rewrite the public
   history a second time (10-30 was the first). Recommend: leave; every sha would change.
4. **obor's commits skip the project's commit hooks** (`--no-verify`, `internal/checks/gitio.go:186`, `:190-191`;
   T-622, no owner line). An adopter's pre-commit formatter, secret scan or commit-msg check never runs on a lane
   commit or a merge. Only a code comment says so. Choose: keep and say it in `obor init`'s output and the README,
   or run the hooks and treat a failure as a RED gate. Recommend: keep, and say it.

**The owner's own sessions and machine**

5. **Every Claude agent starts in `--permission-mode auto`, the lead too** (`internal/config/models.go:39`). It
   was a config arg until T-765 (11-03) made it a constant; a project file cannot override it
   (`docs/research/agent-project-config.md:68`). decisions.md names auto mode only for the advisor (10-27).
   Choose for the lead's pane (the one the owner types in, which may push): auto, or the owner's own setting.
   Recommend: record "auto for every obor agent" as a decision; lanes cannot run without it.
6. **The owner's own `git push` from a plain shell is refused** unless `OBOR_ALLOW_PUSH=1` (`githooks/_hook:118-134`;
   `config.MayPush` answers no for an empty role, `internal/config/config.go:928-938`). Read from the code, not
   run. Choose: a person with no role may push, or the env var stays. Recommend: a person may push; the hook
   guards agents.
7. **Clocks that end in a line or a needs-you to the owner.** None has an owner line, none is configurable:
   a quiet worker 10 + 10 min (`internal/app/serve.go:18`), no agent in a lane 1 min (`serve.go:21`), an unfetched
   integrator job 5 + 5 min (`jobs.go:15`), the second idle line 30 min (`idle.go:26`), a needs-you repeated after
   30 min then every 60 (`messages.go:74-75`), an undelivered line 24 h (`messages.go:68`), a refused dispatch 1 h
   (`waiting.go:158`), a stopped agent with no known reset typed at every 30 min (`stopwake.go:39`), a nightly
   retried after 10 min, 3 times (`nightly.go:28`, `loop.go:120`). Choose: accept the set, or name the ones to
   change. Recommend: accept, one line in decisions.md, no config keys (T-864 removed such keys on purpose).
8. **Counts before a task goes to the owner**: 2 sendbacks (`internal/app/loop.go:62`), 3 failures of one step
   (`loop.go:120`), 3 failures of a rule (`rules.go:45`). All from 10-24 and 11-05 tasks, no owner line.
   Recommend: accept, same decisions.md line as 7.
9. **Below 5 GiB free, no gate or nightly starts** (`internal/app/disk.go:14`; T-564, "a constant, no config
   key"). Every project, every machine. Choose: keep 5, another number, or a key. Recommend: keep.
10. **obor-kit's and axis-alies' heavy checks do not count against each other.** obor-kit's `.obor/local.json`
    sets `slots.dir: /tmp/herd-check-slots` (the pre-rename name; origin not found, the file is untracked);
    axis-alies uses the default `/tmp/obor-check-slots` (`internal/run/slots.go:77`). The design calls the slots
    machine-wide (pp: 9b48b11d 10-08). Choose: shared (delete the local line) or separate. Recommend: shared.
11. **The herdr plugin `obor.events`** is linked user-global by `obor init` and runs `obor plugin restore` at every
    herdr start (`internal/herdr/setup.go:284-338`, `herdr-plugin/herdr-plugin.toml:10-17`). Since T-1051 serve
    restores its own panes; the toml calls the plugin "the faster trigger". Its `version = "0.4.0"` and
    `min_herdr_version = "0.9.0"` are from 10-09 and follow nothing. Choose: keep both restore paths, or drop the
    plugin and init's `events` step. Recommend: drop, if a restore by serve alone is fast enough for the owner.

**What adopters get**

12. **The rule "no model names in anything public" against the built-in model table.** The table
    (`internal/config/models.go:24-33`: opus, sonnet, haiku, fable, deepseek, terra, sol, luna, with ids such as
    `gpt-6.1-terra` and `opencode-go/deepseek-v4.1-flash`) is an owner decision (11-03, "Config names models
    directly"). The same names are in 2 error strings (`cmd/obor/lead.go:108`, `internal/setup/agent.go:136`), 8
    lines of skills and templates, and `templates/task-spec.md:7-8`, which init copies into every adopter's repo.
    Choose the rule's reach: (a) commit messages only, (b) also example text obor copies into other repos, (c)
    everything, and the table moves to config. Recommend (b): the table stays, examples read `<model>[:<effort>]`.
13. **What `obor init` picks without asking**, beyond what `setup.confirmed` (T-647) has the lead confirm:
    - `roles.lead/integrator/reviewer` from the example (`opus:high`, `opus`, `sol:high`,
      `templates/config.example.json:23`), rewritten to the literal `opus` when the CLI is missing
      (`internal/setup/oborinit.go:502`, T-608).
    - `difficulty` from `DifficultyDefault` (`internal/config/config.go:1173-1177`: simple = deepseek, sonnet,
      haiku; T-647). The decision on record says simple = deepseek, luna and "Haiku only via `--types`" (10-29);
      `skills/lead/commands.md:173` still says Haiku "is in no level". Three sources, three lists.
    - `merge.summary_regex` and `merge.fail_grep` of the example, which fit bun's output
      (`config.example.json:36-37`), for every stack; a Go project gets `check: go test ./...` with
      `fail_grep: error|FAIL|Expected|Received`.
    - Role-doc stubs for 9 roles, 4 of which init does not set up (advisor, researcher, triage, deep-dive;
      `internal/setup/init.go:97-113`).
    Choose per line. Recommend: the level lists become the 10-29 decision or a new one; init writes the two
    regexes only for a stack it detected them for; stubs only for lead, integrator, reviewer, worker.
14. **Every project's reviewer is briefed in obor-kit's vocabulary** (`internal/app/review.go:503-513`, used at
    `:552` and `reviewverify.go:358-360`): "rule-tier steps", "kernel guarantee", "serve ships the predicate".
    Two owner decisions collide: 11-05 put these questions into serve's brief (T-869, T-884); 11-05 also says
    obor-kit's own development leaves the product (T-904). axis-alies' reviewer reads them today. Recommend: the
    three kit sentences move to obor-kit's `docs/agents/reviewer.md`; "What did this make redundant?" stays.
15. **The kind protocol lets a plugin kind make obor edit files under `~/`**: `install` may return a user config
    entry and a symlink (`docs/agents/kind-protocol.md:59`; `internal/setup/mcp.go:59-126`,
    `internal/setup/plugin.go:274-317`, about 150 lines). No shipped kind uses it since T-809; decisions 11-04
    and 11-08 moved registration to per launch and native files. Choose: keep it in the protocol, or remove it.
    Recommend: remove.

Small, for the lead to settle with the owner in passing: `skills/lead/SKILL.md:106` "Milestone end: deep dive, then
tag" (a tag is public once pushed; no owner line); `skills/lead/SKILL.md:123` "two strikes" (no owner line); a lead
command whose command word is `$(…)` is denied (`internal/guard/shell.go:782`, T-199).

## 2. DELETE

### 2.1 Tasks, split by files

Letters stand for the ids the lead assigns. A and B touch files T-1103 edits, so both run `--after` it. B2 and E
run `--after` B1 (`internal/config/config.go`, `internal/guard/stop.go`). No other two rows share a file.

| | change | files | tests |
|---|---|---|---|
| **A** | The spec embedded in a merge message loses its `Model:` line (owner 11-09: no model names in merge commits; 20 of 242 merges since 11-08 carry one). With §1 item 1: the lane commit's `Implemented by …` line goes. | `internal/checks/scratch.go` (`:108-110`, `:369-374`) | changed: the commit-message tests T-1103 leaves; added: one, a spec with `Model: x` merges without the line |
| **B1** | `done_file_sections` goes; the four sections are built in. No project sets another value (obor-kit repeats the default, axis-alies has none). The done file itself went on 10-27; the key is from pp: 66cab21c 10-08. | `internal/config/config.go` (`:42`, `:319`), `internal/app/fetch.go` (`:341`, `:523`), `internal/app/worktext.go` (`:143-151`), `internal/guard/stop.go` (`:138`), `templates/config.example.json:11`, `docs/design.md:103` (protected) | changed: `validate_test.go` template cases; T-678's job-text test reads the built-in list |
| **B2** | Config keys and branches nothing sets: `limits.max_panes_per_tab` (only `templates/local.example.json:3`; 4 stays a constant), `merge.scratch_dir` (no project; the derived dir stays), `version` (read only by `validate.go:117`; 1 since 10-08 through every shape change), the `roles.leads` special case (`config.go:173-175`, removed in v0.12.0), the `allow_push` agent-name compat (`config.go:933-966`; both configs say `lead`), the `paths.research` read (`config.go:1073`) together with its "no longer used" WARN and `paths.questions`' (`validate.go:559-560`; both older than v0.16), the second copy of `merge.neutral`'s default (`validate.go:541-544`; owner: `internal/checks/classify.go:53-56`). `templates/local.example.json:2` stops naming axis-alies. | `internal/config/config.go`, `validate.go`, `internal/setup/agent.go` (`:365-368`), `templates/config.example.json` (`:2`, `:33`), `templates/local.example.json` | changed: `validate_test.go:114-115`, `:128-129`, `:449-472`; deleted: the tests of each removed branch |
| **C** | Shipped text that contradicts the code or names kit history: `templates/BACKLOG.md:3` "(obor writes nothing here)" (`obor review close` writes there, `internal/app/reviewclose.go:226-241`); `templates/agents/gotchas.md:35-36` (lane types, gone in T-765) and `:58` (`roles.lead.handoff_tokens`: no such key); `skills/setup/SKILL.md:16` (`obor setup lane O-01 opus`); `skills/lead/commands.md:173` and `skills/lead/SKILL.md:48` (Haiku "in no level"; follows §1 item 13); `skills/lead/SKILL.md:90` ("never > ~5 unreviewed": nothing enforces 5); the two obor-kit anecdotes at `templates/agents/reviewer.md:30` and `templates/agents/advisor.md:6`; `templates/README.md:13` (says `AGENTS-section.md` is pasted; no code does it). Each gets the current fact in its place. | the files named | `make check-readers` |
| **D** | Model names out of example text (§1 item 12, if (b) or (c)): `<model>[:<effort>]` in place of `opus:high` and the rest. | `templates/task-spec.md` (`:7-8`), `cmd/obor/lead.go:108`, `internal/setup/agent.go:136`, `skills/lead/commands.md` (`:76`, `:178-179`), `skills/setup/SKILL.md:47` | changed: the tests that quote the two error lines. After C and B2 (shared files) |
| **E** | The integrator's 150,000-token handoff reminder goes (`internal/guard/stop.go:96-103`, the integrator branch of `prompt.go:123-126`). pp: e5493d61 10-14 T-219, no owner line; the integrator starts fresh per job since T-602 (10-31), and the reminder tells it to `obor restart` itself. Not measured: how many integrator sessions passed 150k since. | `internal/guard/stop.go`, `internal/guard/prompt.go` | deleted: the integrator cases of the handoff tests |
| **F** | `CHECK_LOAD_LIMIT` goes: its reader went in T-962 (11-07); the test kit still sets it. | `internal/testkit/testkit.go` (`:210`, `:222`, `:318`), `tests/upgrade/check_test.go` | none change |
| **G** | Compat branches past their own label ("until v0.28", "until v0.29", kit 0.32.0), after the store census decisions 11-05 asks for (obor-kit: 558 `roles` rows, stored-data-audit §8; axis-alies not counted). | `internal/app/idle.go:71`, `internal/app/event/steps.go` (`:30`, `:142`), `internal/app/messages.go:761` | deleted: each branch's test |
| **H** | `paths.tasks` and `legacySpecDir` go (`internal/checks/lanetable.go:204`, `:218-224`): it recognises spec-only commits of before T-521 (10-28). Condition: no lane holds a task dispatched before 10-28. | `internal/checks/lanetable.go`, `internal/config/validate_test.go:116-120` | deleted: the legacy-evidence case |

Follow-up (owner 11-09, "config looks all over the place"): the proposed grouping of the keys that stay, its
migration and its tasks are in `docs/research/config-shape.md`. If that shape is approved, `version` leaves B2: it
becomes 2 and is how an old file is told from a new one. B1 and B2 land before the shape.

Already scheduled, no task here: `merge.trailer` (T-1103); the `baseline`/`gate`/`e2e` WARN (v0.34,
`validate.go:563-568`).

### 2.2 Config lines (edits by each project's lead, no task)

**obor-kit `.obor/config.json`**

| line | value | why it goes |
|---|---|---|
| `:6-11` | `done_file_sections` | equals the default (goes with B1) |
| `:113` | `merge.summary_regex: "[0-9]+ pass\|[0-9]+ fail"` | copied from the bun example; `go test` and the Makefile print no such line, so every result reads `rc=<n>` (`internal/checks/runner.go:232-248`) |
| `:177`, `:180`, `:183` | `merge.neutral`: `ADOPTION-*.md`, `BRIEF.md`, `STATUS.md` | the files were deleted on 10-27 and 10-28 |
| `:189` | `merge.affected_full`: `internal/herdrtest/**` | no such directory (`internal/herdr/herdrtest/**` at `:190` is the live one) |
| `:197` | `merge.retest_join: "\|"` | equals the default (`config.go:280`) |
| `:199` | `task_id_pattern: "(T\|R)-…"` | 0 of 651 specs are `R-`; the default covers `T-` |
| `:227` | `handoff_tokens: 500000` | equals the default (`config.go:794`); the 500k itself is decided (10-17) |
| `:65-68` | `commands.release_tests` | the same command as `check_release` (`:56-60`); it exists because `nightly_commands` refuses a class's check (`validate.go:215`). Flagged, §4 |

`.obor/local.json`: `slots.max: 4` repeats `config.json:217`; `slots.dir` is §1 item 10.

**axis-alies `.obor/config.json`**

| line | value | why it goes |
|---|---|---|
| `:81` | `kinds.opencode.prompt_extra` | sent with every OpenCode work job. It says "without herd hooks" (obor's plugin guards OpenCode), "model from the lane's opencode.json" (that file has only `mcp`), and "edit only the files your spec lists" (the per-file rule the owner removed on 10-28, T-496). Written 10-17 (527dc68c) |
| `:72` | `kinds.claude.prompt_extra`: "AGENTS.md 'Test load'" | AGENTS.md has no such section since 10-14 (2f5b19bf); the section is `## Checks` (`:62`) |
| `:16` | `commands.check_fast` | the same command as `check`; no reader in obor, none in the repo (`git grep`) |
| `:85-90` | `models.deepseek` | equals the built-in row (`internal/config/models.go:29`) |
| `:69` | `handoff_tokens: 500000` | equals the default |

Its `docs/agents/worker.md:9`, `:14`, `task-spec.md:16` and `AGENTS.md:50` still describe "Files you may touch"
and `docs/tasks/T-xx.md`. That is the project's own text; its lead decides.

**`templates/config.example.json`**: `:22` `difficulty` and `:24-30` `kinds` are never written by init
(`internal/setup/init.go:173` strips them); they are a third list of level defaults beside `DifficultyDefault` and
the 10-29 decision (§1 item 13).

## 3. KEEP

### 3.1 Config keys

Every key of `config.Data` (`internal/config/config.go:34-68`) and its nested structs. Seven are in §2:
`done_file_sections` (B1); `version`, `limits.max_panes_per_tab`, `merge.scratch_dir`, `paths.research`,
`paths.questions` (B2); `paths.tasks` (H). The rest, with the reader and what decided or needs them:

| key | reader | origin and citation |
|---|---|---|
| `name`, `prefix`, `lanes_dir` | `config.go:806`, `validate.go:120`, `config.go:506-509` | pp: 8758a3b4, 66cab21c 10-08; decisions 10-30 (rename) names all three |
| `branches.main` | `internal/app/review.go:186`, `validate.go:525` | decisions 11-09 ("`branches.main` stays") |
| `branches.task` | `config.go:1146-1160` | pp: 4d7dbfbc 10-08; no project sets another value; CLAUDE.md names `task/T-xx` |
| `task_id_pattern` | `cmd/obor/serve_dispatch.go:30`, `verdict.go:80`, `drive.go:60` | pp: 8758a3b4 10-08; needed |
| `paths.agents`, `paths.reviews` | `internal/app/worktext.go:81`, `review.go:186-190` | T-20, T-731; decisions 10-30 (reviews). `paths` is a free map: any other name loads silently |
| `commands.<name>` (`cmd`, `timeout`, `heavy`, `when`) | `internal/checks/runner.go:371`, `:388`, `:433`, `:497`; `scratch.go:644-653`; `internal/run/run.go:43-48` | decisions 11-09 (check + nightly), 11-07 (compile, retest). Timeout default 600 s: pp: 72de9c89 10-14, stated at `docs/design.md:104`. Other names are for `obor run <name>` (`design.md:108`) |
| `lane_links`, `protected_files` | `internal/checks/checks.go:221-223`, `internal/guard/guard.go:211` | pp: 8758a3b4 10-08; decisions 10-28 (protected files stay) |
| `guards.allow_push`, `guards.deny_commands` | `config.go:928-953` | SCOPE 10-10 (T-105); decisions 11-05 (T-905) |
| `lanes` (count, or list with `keep_agent`) | `config.go:757-769` | decisions 11-03 (count), 11-08 (one lane with a long task list; T-1031) |
| `kinds`, `models`, `difficulty`, `roles` (string, or `model`/`write`/`verbs`) | `internal/config/models.go:61`, `:169`, `:188-190`; `config.go:830`, `:1181` | decisions 11-03, 11-04 (custom roles), 11-08 (plugin kinds). `verbs` (T-1039): no project sets one |
| `handoff_tokens` | `internal/guard/stop.go:102`, `internal/app/keeplane.go:123` | user 10-13, 10-17 (500k). T-1031 gave it a second use (a keep lane's restart) with no owner line |
| `setup.confirmed` | `internal/setup/show.go:40`, `:171` | T-647 11-01, no decisions line; the owner answers it in the lead's first session |
| `merge.summary_regex`, `fail_grep` | `internal/checks/runner.go:234`, `:290` | pp: 66cab21c 10-08; needed for the result and RED lines |
| `merge.flake_regex`, `retest_join` | `internal/checks/flakes.go:37`, `runner.go:534` | T-1047 11-08; decisions 11-08 (the gate is configurable) |
| `merge.classes` (`full`, `with_full`), `neutral`, `guard` | `internal/checks/classify.go:35-60` | pp: 82bfcaf0 10-08, T-91 (user 10-09); decisions 11-08 (T-1007) |
| `merge.affected_full` | `tools/affected/main.go:78-84` only; obor reads nothing | decisions 11-05 ("in obor-kit's config + Makefile"). A key in obor's schema for one project's tool |
| `merge.nightly_at`, `nightly_commands` | `cmd/obor/serve.go:268`, `internal/checks/nightly.go:105` | decisions 11-09 (02:00), 11-08 (cache trim; T-817) |
| `workflow.kind`, `terminal.kind` | `cmd/obor/serve.go:239`, `:187` | decisions 11-08 (target architecture). One legal value each today (`config.go:104`, `:123`) |
| `slots.dir`, `max`, `cpus` | `internal/run/slots.go:48-77`, `:272` | pp: 9b48b11d 10-08 (design v2.4); decisions 10-25 (cpus), 11-07 (max 4). Defaults 2 and `/tmp/obor-check-slots` live in `slots.go:74`, `:77` |
| `slots.lane_cpus`, `lane_env` | `config.go:995-1020` | decisions 11-07 (2 cores per lane), T-1041 |

Settings outside the file: `CHECK_SLOT_DIR`, `CHECK_SLOTS`, `CHECK_PROJECT` override `slots` and `name`
(`slots.go:48`, design v2.4); `OBOR_FULL=1` forces the full check (`runner.go:363`, in design.md). Five env knobs
exist for tests and are read by shipped code, undocumented: `OBOR_CALL_DEADLINE` (`internal/app/wire/client.go:91`),
`OBOR_SERVE_READY_WAIT` (`cmd/obor/service.go:110`), `OBOR_RESTART_WAIT` (`internal/app/port/effects.go:93`),
`OBOR_RESTORE_TIMEOUT` (`internal/launch/restore.go:121`), `OBOR_HOOK_TIMEOUT` (`bin/obor:190`). KEEP: mechanics.

### 3.2 What obor writes (66 items checked)

| what | where | citation |
|---|---|---|
| Merge commit: `Merge T-xx: <title>`, the spec, `Summary:` | `internal/checks/scratch.go:369-409` | decisions 10-28 (AR-01 C4), T-802 |
| Merge commit dates forced to the newer parent's | `scratch.go:457-469` | needed: a replay after a crash makes the same sha. `git log` dates of merges are not merge times |
| Commits under the owner's git identity | `internal/checks/gitio.go:181-187` | no setting; decisions 10-30 (noreply email) |
| `rerere` on in the scratch (writes the shared `.git/rr-cache`) | `scratch.go:43-48` | T-913 |
| `RV-n closed · …` commit; `- (RV-n, accepted) …` lines into `BACKLOG.md` | `internal/app/review.go:455-463`, `reviewclose.go:226-241` | decisions 10-30; T-716 |
| `config: <key> <old> → <new>` commits from the web page and `config_set` | `internal/app/configset.go:135-150` | decisions 11-03 |
| `obor init`: `git init`, the `obor init` commit, `.gitignore` lines, detected commands, `core.hooksPath`, the Claude plugin, the launchd service (`RunAtLoad`, `KeepAlive`), `~/.obor/projects.json`, a herdr workspace with tabs `workers` and `review` | `internal/setup/init.go:228-250`, `:345-374`; `oborinit.go:371-440`, `:549-567`; `internal/setup/plugin.go:48-116`; `internal/service/service.go:118-152`; `internal/herdr/setup.go:86-95` | decisions 10-30 (init with no questions, launchd per project), 10-24 (project = workspace); T-608, T-620, T-629, T-631 |
| `install.sh` and the shim: clone to `~/.obor/kit`, `~/.local/bin/obor`, the release binary from `github.com/foksa/obor-kit` | `install.sh:12-39`, `bin/obor:96-127` | decisions 10-30. The only network call; no update check |
| Claude starts: `remoteControlAtStartup` and `autoMemoryEnabled` off for all but the lead; `--strict-mcp-config` on lanes | `internal/config/models.go:247-250` | decisions 10-27, 11-03, 11-08 |
| Codex starts: `-s workspace-write -a never --no-daemon`, update check off | `models.go:40`, `internal/agents/codex/codex.go:39-70` | decisions 10-30, 11-04, 11-08 |
| OpenCode starts: `--auto --standalone`, a generated config, the model through obor's plugin | `models.go:41`, `kinds/opencode/kind/kind.go:135-196` | decisions 11-02, 11-04 |
| Hooks for every Claude session in the project, the owner's too | `hooks/hooks.json:3-21` | pp: c020fc9e; needed (the guards) |
| Prompts: session-start role line, wrong-tab answer, handoff reminder (lead), worker stop block, idle and needs-you lines, review and work job texts, drive prompt, `[obor …]` wrappers | `internal/guard/session.go:68-90`, `prompt.go:24-126`, `stop.go:111-139`; `internal/app/idle.go:171-196`, `messages.go:672-719`, `jobstart.go:181-260`, `worktext.go:138-162` | user request (wrong tab; pp: e11977f4), decisions 10-17, 10-31, 11-01, 11-05 |
| Guard denials: push, headless runs, `pkill` by pattern, `git commit -a` in the main checkout | `internal/guard/shell.go:481-498`, `:609`, `:1155`, `:1330-1335` | decisions 10-27 (headless), SCOPE 10-10 (push); the other two: T-08 10-08, no owner line, stated in `skills/lead/SKILL.md:134` |

### 3.3 Hardcoded numbers and names (about 120 values checked)

- **Decided, with the line**: stopped-agent alarm 30 min (`internal/app/stopwake.go:40`, 10-30); review diff inlined
  up to 15 KiB (`internal/app/port/effects.go:341`, 10-31); `review due` after 3 merges (`review.go:44`, 10-24);
  two review rounds (11-02); herdr protocol 22 (`internal/herdr/socket.go:115`, 11-04); store schema 9 and newer
  (`internal/store/store.go:31`, 11-04); `obor web` on localhost (`internal/web/web.go:43`, 11-01; the port 8787
  has no owner line, `--listen` changes it); serve's self re-exec clocks (11-02); 2 CPUs per lane (11-07).
- **Set by T-864 on purpose** (keys no project set became constants): nightly lock wait 900 s
  (`internal/checks/checks.go:177`), merge marker 900 s (`internal/run/slots.go:46`).
- **Mechanics** (only obor's processes or an agent experience them; 47 poll, socket and buffer values plus about
  30 start, stop and hook waits): `internal/app/port/effects.go:97-108`, `internal/launch/launch.go:214-228`,
  `internal/agents/plugin/calls.go:25-35`, `internal/app/wire/client.go:83-85`, `hooks/hooks.json:4-19`,
  `internal/checks/gitlock.go:24-25`, and the rest.
- **Display caps** (14 numbers that cap what a page or line shows and act on nothing): `internal/view/detail.go:94-96`,
  `internal/web/web.go:51-52`, `cmd/obor/status.go:89`, `internal/launch/evidence.go:16-17`. None has an owner
  line; none needs one.
- **Vendor text in generic code**: `internal/guard/prompt.go:137` matches Claude Code's "Your claude.ai usage limit
  has reset…" sentence (pp: 041e92c0 10-11, "user yes"). It stops matching when Claude Code rewords it.

## 4. Flagged, not expanded

- **`slots.cpus: 4` was set for one slot** (c8bca0c6 10-31, owner: "one heavy check at a time, go test -p 4").
  `slots.max` is 4 since 11-07, and the Makefile passes `-p $CHECK_CPUS` (`Makefile:118`, `:135`, `:152`) on a
  10-core machine. Whether a lane's check gets `-p 4` beside `GOFLAGS=-p=2` was not traced.
- **`nightly_commands` cannot name a class's check** (`internal/config/validate.go:215`), so obor-kit defines
  `release_tests` as a copy of `check_release`.
- **decisions.md is behind the code in two places**: 11-02 "Checks wait while the machine is overloaded" (T-720)
  was removed by T-962 with no line; 10-29's level defaults are not the code's (§1 item 13).
- **`docs/design.md:102-103`** still shows `paths.tasks` and a two-section `done_file_sections`.
- **`stillStopped`** types "30m" a second time (`internal/app/stopwake.go:42`) beside `wakeAlarm`.
- **`validate.go:164`** lists `(claude, codex, opencode)` by hand; plugin kinds exist since T-1036.
- **`core.hooksPath` is an absolute path into `~/.obor/kit`** (`internal/setup/plugin.go:48-116`): a clone on
  another machine runs no hooks, and nothing says so.
- **AGENTS.md "Layout"** lists `mods/`; the tree has none.

## Method

- Config: the structs in `internal/config/config.go` and `models.go`, `validate.go`, what `obor init` writes
  (`internal/setup/init.go:150-188`, `oborinit.go`), both templates, both projects' `config.json` and obor-kit's
  `local.json`. Readers by grep over non-test Go in `cmd/`, `internal/`, `kinds/`, `tools/`. Origins: 57
  `git log -S` searches on `pre-public` and on `3dcc38a7..staging`.
- Writes and policy: two sweeps over non-test code in `cmd/`, `internal/`, `kinds/`, `bin/obor`, `install.sh`,
  `githooks/`, `hooks/`, `herdr-plugin/`, `opencode/`, `skills/`, `templates/`. 39 of their cited line ranges
  were read again for this file: 38 matched, one was corrected (the `$(…)` denial is `shell.go:782`). One verdict
  was changed on that check (§1 item 15: the protocol does document the user-config write). The other citations
  in §3.2 and §3.3 are the sweeps', not re-read.
- Counts of commit lines: `git log --format=%B` on `staging` since 11-08 and on `origin/main`, grep by line start.
- Skipped, as asked: what `docs/research/stored-data-audit.md` covers (retention of logs, backups, events,
  prompts; files under `.obor/state` and `~/.obor/bin`).

Not checked:

- `scripts/`, the `Makefile`, `tests/`, `web/src`, and the numbers `docs/design.md` and `docs/state-model.md` state
  as contract.
- `internal/mcp` tool descriptions (text every agent reads at each tool list); `internal/setup/rename.go`;
  `internal/launch/restore.go` and `self.go` line by line; `internal/web`'s POST routes other than the config batch.
- `templates/agents/*.md` and `skills/lead/*.md` were grepped for commit, push, tag, BACKLOG, model names and
  numbers, not read in full.
- §1 item 6 was read from the code; `obor whoami --can-push` was not run in a shell with no role.
- Task E rests on "fresh per job"; integrator sessions above 150k were not counted.
- Task G's census was not run; task H's condition was not checked.
- Origins of strings that contain `obor` stop at the rename commit e65746c7 (T-573, 10-30) on `pre-public`.
