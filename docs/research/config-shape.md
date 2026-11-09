# Config shape: `.obor/config.json` grouped by what it is about (advisor, 2026-11-09)

Question: owner 11-09, after `undecided-behaviour-audit.md`: "config looks all over the place, like stuff was added
to the json as it came, without structure. Reorganize it so it is readable for humans and structured, so I know
what is related to what. E.g. what is slots, is it related to lanes; lanes_dir and lanes (the count) are in two
different places." Tree: `staging` at 16a7b3f2, kit 0.32.0. Read-only; nothing was changed.

**DECIDE. The shape is the owner's to approve.** Five choices to look at; §2 shows obor-kit's whole file in the
proposed shape.

1. **Ten top-level keys in place of 24**: nine groups (`project`, `lanes`, `agents`, `rules`, `merge`, `nightly`,
   `commands`, `checks`, `reviews`) and `version`. The one grouping that differs from the lead's sketch: push
   rights, protected files and denied commands sit together in `rules` ("what an agent may not do"), not under
   `merge`. All three are enforced when an agent acts (`internal/guard/guard.go:211`,
   `internal/config/config.go:928`), none at merge.
2. **The seven commands obor runs by a fixed name move to the group that runs them**: `lane_setup` and
   `lane_ready` to `lanes.setup` and `lanes.ready`; `compile`, `check`, `retest`, `after_merge` to `merge`;
   `check_milestone` to `nightly.slow_check`. `commands` then holds only the project's own named commands, the
   ones `merge.by_path` and `nightly.also_run` refer to. `obor run check` and the other names keep working.
   Alternative: all commands stay in one list, as today. I recommend the move: today "what runs when" is read
   from four places.
3. **One block for the checks chosen by changed path**: `merge.by_path` with `guard`, `no_check` and `rules`
   replaces `merge.guard`, `merge.neutral` and `merge.classes`. They are one feature (design "Change-based merge
   checks"), and `guard` there no longer reads like `guards`.
4. **Twenty renamed keys** (§3, marked; the rest only move). The ones a reader meets first: `prefix` → `project.agent_prefix`,
   `branches.main` → `merge.target_branch`, `slots.max` → `checks.heavy_at_once`, `slots.cpus` →
   `checks.cpus_per_check`, `guards.allow_push` → `rules.may_push`, `merge.neutral` → `merge.by_path.no_check`.
5. **The migration**: serve rewrites the file and commits it, once, at its first start on the new version; the
   old shape is read for one minor release and then refused (§4). This is a commit obor makes without being
   asked, one per project. Alternative: the lead runs `obor config migrate` by hand. `version` becomes 2, so it
   stays: this takes back the `version` deletion in the audit's task B2.

The owner's two examples, answered by the shape: `slots` is not about lanes. It limits how many commands marked
`heavy` run at once on the machine (the merge slot's, the lanes', the nightly's and `obor run`'s alike,
`internal/run/slots.go:46-83`); it becomes `checks.heavy_at_once`. Two lane settings were put under it later
(`slots.lane_cpus` T-963, `slots.lane_env` T-1041); they move to `lanes`. `lanes` and `lanes_dir` become
`lanes.count` and `lanes.dir`, beside the four other lane keys that today sit in four other places.

## 1. Today

24 top-level keys in the order they were added. What belongs together and where it is today:

| what the owner thinks about | today's keys |
|---|---|
| a lane | `lanes`, `lanes_dir`, `lane_links`, `slots.lane_cpus`, `slots.lane_env`, `commands.lane_setup`, `commands.lane_ready`, `branches.task` (7 places) |
| which model does what | `roles`, `difficulty`, `kinds`, `models`, `handoff_tokens`, `setup.confirmed`, `paths.agents` (7 places) |
| what an agent may not do | `guards.allow_push`, `guards.deny_commands`, `protected_files` (2 places) |
| what a merge must pass | `branches.main`, `commands.compile`, `commands.check`, `commands.retest`, `commands.after_merge`, `merge.guard`, `merge.neutral`, `merge.classes`, `merge.retest_join`, `workflow.kind` (4 places) |
| the nightly | `merge.nightly_at`, `merge.nightly_commands`, `commands.check_milestone` (2 places) |
| how checks run and are read | `slots.max`, `slots.cpus`, `slots.dir`, `merge.summary_regex`, `merge.fail_grep`, `merge.flake_regex`, `commands.<name>.heavy` (3 places) |

Two words mean two things each: `guard` (`merge.guard` is a command; `guards` are the agent rules) and `merge`
(the merge loop's keys, and a drawer for the nightly and the output regexes).

## 2. obor-kit's config in the proposed shape

Today's values, moved; nothing else changed. Gone, per the audit: `done_file_sections` (task B1),
`merge.trailer` (T-1103), `merge.affected_full` (task P0 below; obor never read it). `merge.retest_join` equals
the default and is left out. The stale values of the audit's §2.2 are still in; they are separate edits.

```json
{
  "version": 2,
  "project": {
    "name": "obor-kit",
    "agent_prefix": "ob",
    "task_id_pattern": "(T|R)-[0-9]+[a-z]?"
  },
  "lanes": {
    "count": 4,
    "dir": "../obor-kit-wt",
    "env": { "GOMAXPROCS": "{cpus}", "GOFLAGS": "-p={cpus} -trimpath" }
  },
  "agents": {
    "roles": {
      "lead": "opus:high",
      "integrator": "opus:medium",
      "reviewer": "terra:xhigh",
      "deep_dive": "opus:high",
      "researcher": "sonnet:medium"
    },
    "difficulty": {
      "medium": ["deepseek", "sonnet:medium", "opus:medium"],
      "simple": ["deepseek", "sonnet:medium", "opus:medium"]
    },
    "kinds": {
      "opencode": {
        "plugin": "obor-kind-opencode",
        "job_note": "You run in opencode, not sandboxed: obor's opencode plugin, not hooks, enforces the role text's denials. Quote summary lines and failures only."
      }
    },
    "handoff_tokens": 500000,
    "confirmed": true
  },
  "rules": {
    "may_push": ["lead"],
    "protected_files": ["docs/design.md"],
    "deny_commands": [
      { "match": "<unchanged: config.json:206>", "alone": true, "message": "a load run of tests (`-count` > 1, `-race`) is denied …" },
      { "match": "<unchanged: config.json:211>", "message": "two or more test commands at once … are a load run …" }
    ]
  },
  "merge": {
    "target_branch": "staging",
    "compile": { "cmd": "make -s check-compile", "timeout": 600 },
    "check": { "cmd": "make -s check", "timeout": 1200, "heavy": true },
    "by_path": {
      "guard": "guard",
      "no_check": ["docs/**", "ADOPTION-*.md", "AGENTS.md", "BACKLOG.md", "BRIEF.md", "CLAUDE.md", "QUESTIONS.md", "STATUS.md", ".gitignore"],
      "rules": [
        { "name": "web", "paths": ["web/src/**", "internal/web/dist/**"], "run": ["check_web"], "also_on_full": true },
        { "name": "go", "paths": ["cmd/**", "internal/**", "kinds/**", "tools/**"], "run": ["check_affected"] },
        { "name": "release", "paths": ["Makefile", "scripts/**", "install.sh", "RELEASING.md", ".claude-plugin/marketplace.json", "tests/release/**"], "run": ["check_release"], "also_on_full": true },
        { "name": "readers", "paths": ["skills/**", "templates/**", "hooks/**", "bin/go-commands", ".claude-plugin/**"], "run": ["check_readers"] }
      ]
    },
    "retest": { "cmd": "make -s check-retest TESTS='^({tests})$'", "timeout": 600 },
    "after": { "cmd": "make build", "timeout": 300, "when_changed": ["cmd/**", "internal/**", "kinds/**", "go.mod", "go.sum", "Makefile"] }
  },
  "nightly": {
    "at": "02:00",
    "slow_check": { "cmd": "make -s vulncheck", "timeout": 600 },
    "also_run": ["conformance", "release_tests", "check_budget", "trim_go_cache"]
  },
  "commands": {
    "guard": { "cmd": "<unchanged: config.json:70>", "timeout": 120 },
    "check_affected": { "cmd": "make -s check-affected", "timeout": 900, "heavy": true },
    "check_readers": { "cmd": "make -s check-readers", "timeout": 1200, "heavy": true },
    "check_web": { "cmd": "make -s check-web", "timeout": 1200, "heavy": true },
    "check_release": { "cmd": "make -s check-release", "timeout": 1200, "heavy": true },
    "check_budget": { "cmd": "make -s check-budget", "timeout": 1800 },
    "release_tests": { "cmd": "make -s check-release", "timeout": 1200 },
    "conformance": { "cmd": "make -s conformance TIER=static", "timeout": 300 },
    "trim_go_cache": { "cmd": "make -s trim-go-cache", "timeout": 600 }
  },
  "checks": {
    "heavy_at_once": 4,
    "cpus_per_check": 4,
    "output": {
      "summary": "[0-9]+ pass|[0-9]+ fail",
      "failure": "^--- FAIL|^FAIL\\s",
      "failed_test": "^--- FAIL: (?P<test>\\S+)",
      "failed_package": "^FAIL\\s+(?P<package>[^\\s./][^\\s]*[./][^\\s]*)"
    }
  },
  "reviews": { "dir": "docs/reviews" }
}
```

obor-kit's `.obor/local.json` (same shape, same merge rule):

```json
{
  "agents": { "roles": { "advisor": "fable" } },
  "checks": { "slot_dir": "/tmp/herd-check-slots", "heavy_at_once": 4 }
}
```

## 3. Every key: new, today, meaning

`*` marks a renamed key (the other rows only moved). "Default" is what obor uses when the key is absent.

| new | today | what it means |
|---|---|---|
| `version` | `version` | the shape of this file: 2 |
| **project** | | |
| `project.name` | `name` | the name `obor send`, `obor projects` and the web page use. Default: the directory's name |
| `project.agent_prefix` * | `prefix` | agents are named `<prefix>-lead`, `<prefix>-int`, `<prefix>-l-01` |
| `project.task_id_pattern` | `task_id_pattern` | what a task id looks like. Default `T-[0-9]+[a-z]?` |
| `project.terminal` | `terminal.kind` | the terminal obor drives panes through. One value today: `herdr` |
| **lanes** | | |
| `lanes.count` | `lanes` (a number) | how many lanes: L-01 … L-NN |
| `lanes.list` | `lanes` (a list) | in place of `count`: named lanes, each `{name, keep_agent}` (a keep lane's agent stays between its tasks) |
| `lanes.dir` | `lanes_dir` | the directory of the lane worktrees. Default `../<repo>-wt` |
| `lanes.task_branch` | `branches.task` | the branch a lane works on. Default `task/{id}` |
| `lanes.links` | `lane_links` | paths symlinked from the main checkout into every lane (untracked files a lane needs) |
| `lanes.setup` | `commands.lane_setup` | the command that prepares a new lane (also run in the merge scratch and the nightly's worktree) |
| `lanes.ready` | `commands.lane_ready` | the command that says a lane is still set up |
| `lanes.cpus` | `slots.lane_cpus` | the CPUs every process in a lane may use. Default 2 |
| `lanes.env` | `slots.lane_env` | env every process in a lane gets; `{cpus}` stands for `lanes.cpus` |
| **agents** | | |
| `agents.roles.<role>` | `roles.<role>` | the model of each role, `<model>[:<effort>]`; an object `{model, write, verbs}` defines a custom role |
| `agents.difficulty.<level>` | `difficulty.<level>` | the models a `hard`, `medium` or `simple` task goes to, first free one first |
| `agents.kinds.<kind>.args` | `kinds.<kind>.args` | extra args for every start of that CLI (claude, codex, opencode) |
| `agents.kinds.<kind>.job_note` * | `kinds.<kind>.prompt_extra` | text added to every work job an agent of that kind gets |
| `agents.kinds.<kind>.plugin` * | `kinds.<kind>.command` | the executable that implements a plugin kind |
| `agents.models.<name>` `{kind, id}` * | `models.<name>` `{kind, model}` | a model the built-in table lacks, or one overridden |
| `agents.handoff_tokens` | `handoff_tokens` | context size above which the lead is reminded to hand off (and a keep lane's idle agent is restarted). Default 500000 |
| `agents.confirmed` | `setup.confirmed` | the owner went through the models above with the lead |
| `agents.docs_dir` * | `paths.agents` | where the project's role docs are. Default `docs/agents` |
| **rules** | | |
| `rules.may_push` * | `guards.allow_push` | the roles that may `git push`. Default: none |
| `rules.protected_files` | `protected_files` | files only the lead edits, unless a task's spec unlocks one |
| `rules.deny_commands` | `guards.deny_commands` | shell commands denied for every agent: `{match, alone, message}` |
| **merge** | | |
| `merge.target_branch` * | `branches.main` | the branch tasks merge into. Default `main` |
| `merge.workflow` | `workflow.kind` | how a task lands. One value today: `scratch-ff` |
| `merge.compile` | `commands.compile` | what the merge slot runs on every merge |
| `merge.check` | `commands.check` | what every merge must pass (run in the task's lane); the full check when `by_path` picks no smaller one |
| `merge.by_path.guard` | `merge.guard` | a command (by name) that must pass before a smaller check is trusted. Unset: every merge runs `merge.check` |
| `merge.by_path.no_check` * | `merge.neutral` | files that need no check of their own. Default `docs/**` |
| `merge.by_path.rules[]` * | `merge.classes[]` | `{name, paths, run, full, also_on_full}`: a change inside `paths` runs the commands in `run` instead of `merge.check` |
| `…rules[].run` * | `…classes[].checks` | the commands, by name |
| `…rules[].also_on_full` * | `…classes[].with_full` | run them after `merge.check` too when the merge is a full one |
| `merge.retest` `{cmd, timeout, join}` | `commands.retest`, `merge.retest_join` | a red check's failing tests are run once more with this; `{tests}` is their names joined by `join` (default `\|`) |
| `merge.after` `{cmd, timeout, when_changed}` * | `commands.after_merge` `{…, when}` | run in the main checkout after a merge, only when one of the globs changed |
| **nightly** | | |
| `nightly.at` | `merge.nightly_at` | local time of the nightly; absent: no nightly |
| `nightly.slow_check` * | `commands.check_milestone` | the slow tier: the nightly runs `lanes.setup`, `merge.check`, then this, and is RED when one fails |
| `nightly.also_run` * | `merge.nightly_commands` | commands, by name, run after those; a failure is a WARN line, never RED |
| **commands** | | |
| `commands.<name>` `{cmd, timeout, heavy}` | `commands.<name>` | the project's own commands: named in `by_path.rules[].run`, `by_path.guard`, `nightly.also_run`, or run by `obor run <name>`. `timeout` default 600 s |
| **checks** | | |
| `checks.heavy_at_once` * | `slots.max` | how many commands marked `"heavy": true` run at once on this machine. Default 2 |
| `checks.cpus_per_check` * | `slots.cpus` | exported to a check as `CHECK_CPUS`. Default: cores / `heavy_at_once` |
| `checks.slot_dir` * | `slots.dir` | where the slot files are. Default `/tmp/obor-check-slots` |
| `checks.output.summary` * | `merge.summary_regex` | the part of a check's output that goes into its result line |
| `checks.output.failure` * | `merge.fail_grep` | the lines of a red check's output that go into the RED line |
| `checks.output.failed_test`, `failed_package` * | `merge.flake_regex[0]`, `[1]` | how to read the failing tests and packages (for the retest and `obor flakes`) |
| **reviews** | | |
| `reviews.dir` | `paths.reviews` | where the reviewer writes an open review. Default `docs/reviews` (today the default is missing in one of three readers, `internal/app/review.go:190`) |

Gone without a new home: `done_file_sections`, `limits.max_panes_per_tab`, `merge.scratch_dir`, `merge.trailer`,
`paths.research`, `paths.questions`, `paths.tasks` (audit §2, T-1103); `merge.affected_full` (P0); the free-form
`paths` map itself.

Not renamed, on purpose: the names typed at a prompt and written in logs, steps and role texts stay (`obor run
check`, `lane_setup`, `after_merge`, `check_milestone`; `<task>-check.log`). One lookup maps them to their new
home (`Config.Command`, `internal/config/config.go:817`). The `CHECK_*` env names stay too.

axis-alies after the rewrite, for comparison (its values, 94 lines today):

```json
{
  "version": 2,
  "project": { "name": "axis-alies", "agent_prefix": "aa" },
  "lanes": {
    "count": 4,
    "dir": "../axis-alies-wt",
    "links": ["reference", "public/assets"],
    "setup": "bun install --frozen-lockfile && cp bun.lock node_modules/.lane-lock",
    "ready": "cmp -s bun.lock node_modules/.lane-lock"
  },
  "agents": {
    "roles": { "integrator": "sonnet:medium", "reviewer": "terra:xhigh", "lead": "opus:high", "deep_dive": "opus:high", "advisor": "fable:high", "triage": "opus:medium" },
    "difficulty": { "hard": ["opus:medium"], "medium": ["deepseek", "sonnet:medium"], "simple": ["deepseek"] },
    "kinds": {
      "claude": { "job_note": "<unchanged: config.json:72>" },
      "codex": { "args": ["-c", "sandbox_workspace_write.network_access=true"] },
      "opencode": { "plugin": "obor-kind-opencode", "job_note": "<unchanged: config.json:81>" }
    },
    "models": { "deepseek": { "kind": "opencode", "id": "opencode-go/deepseek-v4.1-flash" } },
    "handoff_tokens": 500000,
    "confirmed": true
  },
  "rules": { "may_push": ["lead"], "protected_files": ["src/engine/types.ts"] },
  "merge": {
    "check": { "cmd": "bun run check:engine", "timeout": 600, "heavy": true },
    "after": { "cmd": "bun install --frozen-lockfile", "timeout": 300, "when_changed": ["bun.lock"] }
  },
  "nightly": { "at": "02:00", "slow_check": { "cmd": "<unchanged: config.json:19>", "timeout": 14400, "heavy": true } },
  "commands": { "check_fast": "bun run check:engine" },
  "checks": { "output": { "summary": "[0-9]+ pass|[0-9]+ fail", "failure": "error TS|×|\\(fail\\)|Expected|Received" } },
  "reviews": { "dir": "docs/reviews" }
}
```

## 4. Migration

### 4.1 What `internal/config/testdata/migrate` is today

Six files: obor-kit's and axis-alies' configs before and after T-766's `obor setup migrate` (lane types → models,
v0.23 → v0.24). The code they tested (`internal/config/migrate.go`, 669 lines; `internal/setup/migrate.go`, 238)
was deleted by T-789 on 11-04, under the rule "one-time migration code lives one minor release" (decisions
11-03). No code or test reads the directory now (grep over `cmd/`, `internal/`, `tests/`, `tools/`, the
Makefile: 0 references); T-1103 still edited four of the files today (f6995342). It is dead test data. P2 below
replaces its content with this migration's fixtures; if the shape is not approved, the directory goes.

That migration changed what keys mean. This one only moves and renames: a table of about 45 rows (§3) and three
structural cases (`lanes` as a number or list; the seven commands leaving `commands`; the `paths` map).

### 4.2 How a config moves

One function, JSON in and JSON out: `config.Reshape(doc)`. It moves each key by the table, drops the keys listed
as gone, writes the groups in §2's order, and works on a partial file (`local.json`). It runs in three places:

1. **Every load, for one minor release.** `readWith` (`internal/config/config.go:437`) reads `version`; a file
   of shape 1 is reshaped in memory before the decode, and the load warns once. `local.json` has no `version`:
   it is reshaped whenever `config.json` is shape 1. So every command, hook and serve works from the moment the
   new binary runs, before the file is rewritten. There is one struct and one decode; the old shape exists only
   as the function's input.
2. **Serve's first start on the new version rewrites the file.** It reshapes `config.json`, commits it on the
   target branch through the path `config_set` already uses (`cmd/obor/configfile.go:45-58`; subject `config:
   shape 2 (obor <version>)`, the dropped keys in the body), rewrites `local.json` in place (untracked), and posts
   one `[obor] config: …` line to the lead. When that path refuses (the file has uncommitted changes, or the main
   checkout is on another branch, `configfile.go:22-36`), nothing is written, the project keeps running on the
   in-memory reshape, and the refusal is a needs-you.
3. **`obor config migrate`**, the same rewrite by hand, for a project whose serve is not running.

While a file is still shape 1, a writer refuses with that hint instead of mixing shapes: `config_set`, `obor
setup lane`, `obor init` on an existing config. `obor config validate` on a shape-1 file prints one WARN, not a
FAIL: obor-kit's merge guard runs validate (`.obor/config.json:70`), and a FAIL would turn gates red in the
minutes between the new build and the rewrite.

4. **The release after** deletes `Reshape`, its fixtures and the WARN; a shape-1 file is then refused by every
   loader with the message T-847 uses for a pre-v0.23 config ("migrate it with obor <N>, then upgrade").

obor-kit itself: the task that lands the shape does not edit `.obor/config.json`. After its merge, `after_merge`
builds, serve re-execs on the new binary and makes the rewrite commit on `staging`. Lanes in flight hold the old
file unchanged on their branches, so their merges do not conflict; obor reads the config from the main checkout
only. axis-alies: the rewrite happens at its serve's first start after the release; its lead gets the line.

An older binary that meets a shape-2 file (an `obor web` in the 30 s before it re-execs) loads it leniently
(T-1081): it warns per unknown key and runs on defaults. Hooks and commands are started per call by the shim
and are never older than the kit.

### 4.3 What follows the shape

- **validate**: every `FAIL` and `WARN` path string follows the struct (`internal/config/validate.go`, about 40
  strings); the three "removed key" notes go with their keys.
- **`templates/config.example.json`, `templates/local.example.json`**: rewritten in the new shape. The example's
  `difficulty` and `kinds` blocks, which init never writes (audit §2.2), go.
- **`obor init`**: it builds the config by editing the example at key paths (`internal/setup/init.go:164-179`)
  and writes detected commands, role models, level lists, the lane count and lane links at more paths
  (`oborinit.go:429`, `:502`, `:540`, `:692`; `lane.go:144`; `plugin.go:259`; `show.go:160`; `rename.go:205-228`).
  P1 below moves those writes behind functions of `internal/config`, so the shape changes them in one package.
- **`obor setup show`**: its table has no key paths except the header "config difficulty"
  (`internal/setup/show.go:34`); one string.
- **`config_set`**: the editable keys are an interface of their own (`EditKeys`, `internal/config/edit.go:48`:
  `difficulty.<level>`, `roles.<role>`, `lanes`, `kinds.<kind>.args`, `merge.nightly_at`), used by the web Setup
  page (`web/src/lib/setup.ts`), `obor nightly on|off` (`cmd/obor/checks.go:41`) and the lead's `[obor] config:`
  line. They are renamed in their own task (P4) to `agents.difficulty.<level>`, `agents.roles.<role>`,
  `lanes.count`, `agents.kinds.<kind>.args`, `nightly.at`; the alias `roles.<role>.type` (`edit.go:60`) goes.
  Past `config_set` records keep the old key names (`internal/app/event/data.go:36-40`); they are shown, never
  decided on (`configset.go:213-222`).
- **`obor config get <path>`**: paths follow the file. No script in the kit calls it (grep over `bin/`,
  `githooks/`, `hooks/`, `scripts/`, the Makefile).
- **Docs**: `docs/design.md` "Config" (`:89-162`, protected; 69 key mentions in the file), the release's
  `docs/UPGRADE-*.md` with §3's table, and about 50 mentions in 20 files of `skills/`, `templates/agents/`,
  `docs/agents/` and the README.
- **Tests**: 222 `testkit.Set("<dotted path>", …)` calls in 66 test files name old paths, and
  `internal/config`'s own tests 201 more. P2 keeps the old paths working in tests by running the test kit's
  config through `Reshape`; P6 rewrites them package by package before `Reshape` is deleted.

### 4.4 Tasks, split by files

All after T-1103 and the audit's B1 and B2 (they edit `internal/config`). P1a, P1b, P1c run in parallel; P2 after
them; P3, P4, P5, P6 after P2, in parallel. Outside `internal/config`, 66 direct reads of `cfg.Data.<field>` sit
in 32 files of 8 packages; P1 turns them into method calls so that P2 changes one package.

| | change | files | tests |
|---|---|---|---|
| **P0** | `tools/affected` takes its "full" globs from a flag the Makefile passes; `merge.affected_full` leaves obor's schema and obor-kit's config. Any time. | `tools/affected/main.go` (`:70-84`), `Makefile`, `internal/config/config.go:266-268` (the field), `.obor/config.json` (the lead) | changed: the tool's test of the config read |
| **P1a** | `internal/setup` reads the config through methods and writes it through edit functions of `internal/config` (no key path literals left in the package). The methods and functions it needs are added to `internal/config` first, in this task. | `internal/setup/*.go` (7 files, 24 reads, 11 path literals), `internal/config/config.go`, `edit.go` | none change: behaviour is the same |
| **P1b** | The same for `internal/checks` and `internal/run` (20 reads). After P1a's methods exist. | `internal/checks/*.go` (7 files), `internal/run/slots.go` | none change |
| **P1c** | The same for `internal/app`, `cmd/obor`, `internal/view`, `internal/guard`, `internal/launch` (22 reads). After P1a's methods exist. | 17 files in those packages | none change |
| **P2** | The shape: `config.Data` and its nested structs in §2's groups, each field with its one-line meaning (§3); `Reshape` and the one-release window in `readWith`; `Config.Command` finds the seven moved commands; validate's paths; the example and local example; the test kit's builder writes shape 2 and passes old `Set` paths through `Reshape`. | `internal/config/*.go`, `internal/config/testdata/migrate/*` (replaced: obor-kit and axis-alies, before and after, with obor-kit's local), `internal/testkit/testkit.go` (`:407-445`), `templates/config.example.json`, `templates/local.example.json` | added: `Reshape` on the four fixtures (the result equals §2's files; a second run changes nothing; a partial file); a shape-1 load warns once and equals the shape-2 load. Changed: `validate_test.go`'s path strings |
| **P3** | Serve's one rewrite at start, `obor config migrate`, and the writers' refusal on a shape-1 file. | `cmd/obor/serve.go`, `cmd/obor/configfile.go`, `cmd/obor/config.go`, `internal/app/configset.go` | added: a shape-1 project gets one commit and one lead line at serve's start, none at the next; a dirty file gets a needs-you and no write; `config_set` on shape 1 is refused |
| **P4** | `config_set`'s key names and their users. | `internal/config/edit.go` (key parsing only), `cmd/obor/checks.go:41`, `internal/view/setup.go`, `web/src/lib/setup.ts`, `types.ts`, `pages/Setup.svelte`, `internal/web/dist/` (generated), `skills/lead/commands.md:76` | changed: `edit_test.go`, `web/src/lib/setup.test.ts`, the view's golden file |
| **P5** | Docs follow: design.md "Config", the UPGRADE guide with §3's table, skills, role texts, README. | `docs/design.md` (protected), `docs/UPGRADE-<N>.md`, `skills/**`, `templates/agents/*.md`, `templates/README.md`, `docs/agents/*.md`, `README.md` (not `skills/lead/commands.md`: P4) | `make check-readers` |
| **P6** | Test configs name the new paths: 222 `testkit.Set` calls in 66 files, split by package (one task each for `internal/app`, `cmd/obor`, `internal/checks`, the rest). | `*_test.go` only | no test is added or removed |
| **P7** | The release after: `Reshape`, the window, the test kit's pass-through and the fixtures go; shape 1 is refused. | `internal/config/*.go`, `internal/config/testdata/migrate/`, `internal/testkit/testkit.go`, `cmd/obor/config.go` (`migrate`), `cmd/obor/serve.go` | deleted: P2's and P3's migration tests; added: a shape-1 file fails the load with the hint |

Size, estimated, not measured: P1 is mechanical and about net zero; P2 adds about 150 lines that P7 removes; P3
about 60, P7 removes most.

## 5. Risks

- **A rename is forever in other people's files.** After P7 a config written for 0.32 is refused. Adopters today:
  axis-alies and two projects with a `.obor/config.json` under `~/Work` (`codex-image-tool`, `codex-img-viewer`;
  not registered in `~/.obor/projects.json`, not read here). Each is rewritten when its serve next starts on the
  new version or by `obor config migrate`.
- **The rewrite reorders the file**: one diff of the whole file per project. Key order inside a group and all
  values are kept.
- **Choice 2 puts commands in two homes** (`merge.check` is an object, `commands.check_web` is in the list). If
  that reads worse than one list, choice 2's alternative keeps `commands` as it is and only groups the rest; the
  migration and the tasks stay the same, `Config.Command` loses its lookup.
- **`heavy` stays a flag on a command** while its limit is `checks.heavy_at_once`. They are far apart in the file;
  §3's line for `checks.heavy_at_once` is the link.
- **The audit's B1 and B2 must land first**, or P2's struct carries the keys they delete. If the order is
  reversed, `Reshape` drops them from old files anyway (§3, "Gone").

## Method

- Keys and readers: from the audit's part 1 (`undecided-behaviour-audit.md` §3.1), re-read for the commands obor
  runs by name (`internal/checks/runner.go:371`, `:388`, `:433`, `:497`; `scratch.go:644`, `:690`;
  `nightly.go:75-79`; `lane.go:146-153`) and for every writer of a key path (grep for `[]string{"<key>"` and
  dotted key strings over non-test Go).
- Counts: direct field reads by `grep -c '\.Data\.[A-Z]'` per non-test file; test sites by grep for
  `.Set("<path>"`; doc mentions by grep for the key names in 21 files.
- Not checked: `web/src` beyond the three files that name edit keys; whether a test outside `internal/config`
  writes a config as a JSON literal instead of through the test kit (13 Go files contain one of four old key
  names as a string); the two unregistered projects' configs; what `obor setup rename` writes beyond
  `guards.allow_push`.
- Not designed here: the names inside `rules.deny_commands` (`alone`), and whether `merge.workflow` and
  `project.terminal` should be objects for adapter settings later (they were, as `workflow.kind` and
  `terminal.kind`).
