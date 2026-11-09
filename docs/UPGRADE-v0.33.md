# Upgrading an obor project from v0.32 to v0.33

## v0.33.0 (serve prunes ended tasks' records after 30 days; store schema 14)

Serve's nightly backup now deletes, after the backup is written, the records of every task that ended over 30 days
ago (all but its merged or dropped fact) and the session records as old that are no agent's current session. The
store's schema becomes 14 at the first start and an older obor refuses it. **To roll back** (within 7 nights of the
upgrade): restore a pre-upgrade backup (`.obor/state/backup/obor-<date>.db`), `git revert` the `config: shape 2`
commit, and put back `.obor/local.json`'s old keys by hand (copy it before upgrading if you might roll back).

The herdr plugin `obor.events` is gone: `obor serve` restarts its own panes after a herdr restart (T-1117). The block
unlinks the plugin an older obor linked (user-global, once per machine); `obor init` does the same once the config is
shape 2.

```sh
# The obor.events herdr plugin an older obor linked is gone: obor serve restarts its own panes.
obor setup herdr-plugin
```

## v0.33.0 (config shape 2: ten top-level keys, nine groups, and one automatic rewrite)

`.obor/config.json` is **shape 2** (`version: 2`): ten top-level keys — `project`,
`lanes`, `agents`, `rules`, `merge`, `nightly`, `commands`, `checks`, `reviews` and `version` — in place of 24, with
twenty keys renamed. No value changes meaning: the file's values move as they are. Two structural changes: the seven
commands obor runs by a fixed name leave `commands` for the group that runs them, and `paths` goes (only `reviews`
survives, as `reviews.dir`). The names typed at a prompt stay: `obor run check`, `lane_setup`, `after_merge`,
`check_milestone`, `<task>-check.log`. One lookup (`Config.Command`) maps each to its new home.

`.obor/local.json` has the same shape and follows `config.json`'s: it is rewritten too, in place (it is untracked). A
second clone of the project has its own untracked copy: run `obor config migrate` there.

**What happens on the upgrade.** A shape-1 file is read for this release: every load reshapes it in memory
(`config.Reshape`) and warns once, so every command, hook and `obor serve` works from the first start of the new
binary. Serve's first start on v0.33 then rewrites `config.json` and commits it on the merge's target branch — one
commit per project, made without being asked:

```
config: shape 2 (obor <version>)
```

with the dropped keys in the commit's body. Nothing is written when the file has uncommitted changes or the main
checkout is on another branch: the project keeps running on the in-memory reshape and the refusal is a needs-you. A
project whose serve is not running rewrites the file with `obor config migrate`; `config_set`, `obor setup lane` and
`obor init` refuse a shape-1 file with that hint until it is rewritten. `obor config validate` on a shape-1 file prints
`WARN .version: config shape 1, read as shape 2 for this release and refused by the next: serve rewrites it at its
start (obor config migrate by hand)` and then `OK` (exit 0), so a merge guard that runs validate keeps its gates green
in the minutes before the rewrite. The next release refuses shape 1 like a pre-v0.23 config ("migrate it with obor
v0.33, then upgrade").

A lane in flight holds its branch's copy of the file and merges cleanly: obor reads the config from the main checkout
only. An older obor refuses a shape-2 file (`unknown field "agents"`): a stale `obor web` shows the project failing
until it re-execs; hooks and commands are started per call by the shim and are never older than the kit.

`*` marks a renamed key; every other row only moved. "Default" is what obor uses when the key is absent.

| new | shape 1 | what it means |
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
| `reviews.dir` | `paths.reviews` | where the reviewer writes an open review. Default `docs/reviews` |

Gone without a new home: `done_file_sections`, `limits.max_panes_per_tab`, `merge.scratch_dir`, `merge.trailer`,
`merge.affected_full`, `paths.research`, `paths.questions`, `paths.tasks`, and the free-form `paths` map itself (its
`agents` and `reviews` members became `agents.docs_dir` and `reviews.dir`).

`obor config get <path>` takes the new paths. obor-kit's own `.obor/local.json` keeps the same two lines, rewritten:

```json
{
  "agents": { "roles": { "advisor": "fable" } },
  "checks": { "slot_dir": "/tmp/herd-check-slots", "heavy_at_once": 4 }
}
```
