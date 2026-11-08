# Target architecture: core = agent automation, the rest behind adapters, plugins or config

Advisor (ob-fable, Claude Fable), 2026-11-08, at staging `15523f19`. For the owner to decide from; no tasks or specs
here. Inputs: the owner's 11-08 direction (`docs/decisions.md` top three entries), `docs/research/architecture-inventory.md`
and `docs/research/entity-model.md` (11-08, measured), `docs/research/architecture-proposal-2026-11-05.md`,
`docs/research/architecture-advice-fable.md` (11-05), `docs/research/herdr-core.md`, `docs/plan/test-suite-rework.md`,
and the code at the baseline. Where the 11-05 advice said "no behaviour in config, no generic workflow", the owner's
direction wins for the workflow, lanes and gate; §3 says how the two are reconciled (a selector and parameters, not an
interpreter).

Conventions, as in the research docs: **Fact** = read from code or measured, with `file:line` at the baseline or the
command. **Opinion** = mine, marked. What I measured today is in §8 and §7 (command given); everything else cites the
two 11-08 research docs.

## Answer first

1. **The boundary already exists in `internal/app`; the target is to move the five mixed pieces outside it behind the
   same kind of port, and to make the ports three groups: workflow, terminal, kinds.** Fact: `internal/app` imports no
   git or herdr code and its tests run 30 git processes, all project identity (inventory §5, §7 row 10); the mixed code
   is `checks.Scratch`, `launch.Launcher`, `cmd/obor/reviewcycle.go`, `config/validate.go:396-399`, `setup` (inventory
   "Where deciding and doing are mixed" 1, 5, 6, 7, 4). The core's adapter bundle is `app.Loop` with 13 fields
   (`internal/app/loop.go:33-57`); the target is about six.
2. **Plugin model: out-of-process for agent kinds and integrations; compiled-in adapters chosen by config for the
   terminal and the workflow.** Reason: kinds and integrations are written by people who are not obor's maintainers,
   in any language, and are called at human cadence (an agent start, a ticket update); the terminal and the workflow
   are streaming or git-heavy, have two or three implementations in sight, all written here, and are selected, not
   authored, by a project. Embedded scripting is out: it needs an interpreter dependency the policy forbids and the
   per-kind logic is a few hundred lines of string handling (§2).
3. **The workflow is one interface with six steps** (submit → candidate → test → publish → landed → free, plus the
   review's git) with two implementations: today's scratch merge + fast-forward, and push-branch / PR / CI gate /
   merge-on-approval. Lane "free" and review coverage become facts the core holds (a workflow answer and a fold), not
   readings of the main checkout's git history (§3, §4).
4. **Go stays, with out-of-process plugins.** Measured today: the Go binary starts a hook in 7.4 ms (16.3 MB static
   binary); the TypeScript runtimes start in 9 to 23 ms from 63 to 147 MB; a port is 53.9k production and 85k test
   lines whose behaviour lives in those tests, and it would port the mixed responsibilities verbatim unless redesigned,
   so the redesign is owed either way (§8).
5. **Migration: fourteen small steps, each a deletion or an unmixing, measured** (§9). The first three are where the
   cost hurts today: the fixture fix (≈8.2k of ≈14.4k git processes build fixtures, inventory §7), the
   `checks.Scratch` decide/do split (the 47 s cut table), and lane "free" decided in one place instead of five.

## 1. The core/adapter boundary

**Core = agent automation.** Fact: what the core does today, by file: task and job state (`mtask` fold, `app/mtask.go`,
the one transition table `app/states.go:18-30`, 20 states `app/loop.go:120-140`); needs-you as a block on a phase
(`app/block.go`); watching (rules `app/rules.go:46-58`: needs, stop alarm, watch, review-on-merge, verify, idle, idle
ping, integrator stop, reviewer stop, worker start; the disk and nightly triggers); the rules agents forget (the same
list plus the kernel steps: placing a task, submit, gate, publish, verdicts, drop, job delivery; `docs/design.md`
"Change rules" 1); chatter (the outbox and briefs, `app/messages.go` 1,340 lines, `event.Message`). Everything
with I/O that the core needs is reached through `app.Loop`'s fields and the `port` package.

**What is core, what is an adapter, what is entry** (opinion for the labels; the import rule is already enforced by
`layerOf` in `internal/checks/boundary_test.go`, `docs/module-map.md` "Layers"):

| Layer | Packages | Owns | May import |
|---|---|---|---|
| core | `app` (+ `event`, `port`, `wire`), `store`, `view` (read model), `config`, `spec`, `roles` | task, lane, job, review, spec, agent-identity state; rules; chatter; projections | base packages, `agents` (bridge: pure) |
| bridge | `agents`, one package per compiled kind, and the plugin kind (§2) | what differs per agent CLI, as data | standard library |
| adapter | `checks` (workflow + gate runner), `launch` (agent lifecycle over the terminal), `herdr` (terminal), `setup`, `guard` (hook policy: a core policy that runs out of process; its transport is per kind), `mcp` | one outside system each | core's `port`/`wire`/`event`, never another adapter |
| entry | `cmd/obor`, `web` | wiring, flags, rendering | everything |

The target rule has one line more than today's: **an adapter translates one port call into I/O; a decision in an adapter
is a pure function the adapter calls, tested without the outside system.** Fact: `internal/app` already works this way
(decisions are folds and rules, effects are `App.do` calls on `Loop` fields; inventory "mixed" 8). The five places that
do not, and what the split looks like for each:

| Mixed today (fact, inventory) | Decision part (pure) | I/O part (adapter) |
|---|---|---|
| `checks.Scratch` (754 lines, 49 `s.call` git calls interleaved with the cut table, replay mismatch, patch binding, lost edits; `checks/scratch.go:115-556`) | the outcome of a gate/publish given git's answers: conflict/red/green/moved, mismatch, lost edits | a `gitReader` (refs, merge-base, diff, status) and `gitWriter` (merge, commit, update-ref, ff) under `Scratch` |
| `launch.Launcher` (2,425 lines; 43 herdr calls; `launch/launch.go:453-473`, `stop.go`, `evidence.go`) | resume vs fresh, session choice, the stop ladder's next rung from the screen and process facts | `port.PaneControl`, `Starter`, process reads |
| `cmd/obor/reviewcycle.go:63-150` | may this close happen (on branch, dirty only in allowed files, `## Open` exists, reviewed commit exists); what the commit says | the index plumbing and the commit, in the main checkout |
| `config/validate.go:396-399` (two `git rev-parse` in a validator) | the validation | a `branchExists` the caller injects, or a warning the serve start checks instead |
| `setup` (3,466 lines: hooks, worktrees, tabs, plugin, config edits) | what to install, given what is there | fs, `git config`, the terminal's layout calls |

Opinion: the first two are the ones worth doing for cost (§7, §9 steps 3 and 4); the other three are small and
can ride along with the step that touches their file.

**The port groups.** Today `Loop` has: `Scratch`, `Agents`, `Typer`, `Lane`, `Specs`, `Kit`, `Checkout`, `Nightly`,
`Promote`, `Disk`, `Review`, `Inbox`, `Restart`, `Config` (`loop.go:33-57`). The target groups them by the outside
system they talk to, which is also the axis the owner wants pluggable or configurable:

| Group | Today's fields and ports | Target |
|---|---|---|
| Workflow (git, CI, forge) | `Scratch`, `Lane` (`port.LaneSetup`), `Specs.Target`, `Review` (`ReviewGit`), `Promote`, `Checkout` | one `port.Workflow` (§3), chosen by config |
| Terminal (panes) | `Agents` (launch), `Typer`, and `port.Panes`/`PaneControl`/`Starter` behind launch | `port.Terminal` + `port.Launcher` (§2), chosen by config |
| Kinds | `agents.Kind` via launch, guard, setup, inbox | compiled kinds + the plugin kind (§2) |
| Notify | `Inbox` (Poster), toast via `Panes.Notify` | `port.Notifier`, plus the outbound event feed (§2 integrations) |
| Host | `Disk`, `Restart`, `Config`, `Nightly`'s clock, `Kit` | stay as small closures; not an axis |

## 2. Plugins

### The choice, with reasons

| Model | Fits | Does not fit | Why |
|---|---|---|---|
| **Out-of-process protocol** (an executable per plugin, JSON over stdin/stdout, one call per invocation; a manifest cached at setup) | agent kinds, integrations | terminal, workflow | Language-neutral (the owner's concern about Go). A kind's calls happen at agent start/stop and turn ends (seconds apart); an integration's at ticket events. A process start costs 7 to 25 ms depending on the plugin's language (§8 table), nothing at that cadence. The terminal is a stream (`Panes.Subscribe`, a snapshot per pass) and the workflow is a git session with locks: a process per call is the wrong grain for both |
| **Embedded scripting** (Lua, Starlark, JS in the binary) | nothing here | all | Needs an interpreter dependency; the policy allows only published specs and a database engine (AGENTS.md "Runtime constraints"). The per-kind logic is 188 to 326 lines of string handling (inventory axis 1); an interpreter to host that is disproportionate. Kinds also need I/O (read a transcript, write a config): the sandbox would have to expose it |
| **Compiled-in, chosen by config** | terminal, workflow, the three reference kinds, the gate runner | third-party kinds, integrations | Two or three implementations each, all written here, latency- or lock-sensitive. Pluggability comes from a contract test table run against every implementation and the fake, not from loading code at run time |

Fact: Go's in-process `plugin` package needs the same toolchain and build flags as the host and is unsupported on
Windows; it is not an option, and it is not what "pluggable" has to mean. The three agent kinds today are compiled in
and registered by `import _` in three places (`cmd/obor/main.go:19-21`, `internal/testkit/testkit.go:17-19`,
`tests/conformance/static.go:23-25`; inventory axis 1).

### 2a. Agent kinds (owner's first)

**What a kind is today (fact).** `agents.Kind`: 15 methods, "pure functions over strings and bytes"
(`internal/agents/agents.go:1-6, 19-69`), `Caps` with 11 flags (`agents.go:192-206`). Three Go closures cross the
boundary: `Start.Read` (a file read for an arg the kind names), `Start.WriteState` (writes a generated config into
the state dir), `MCPRegistration.Lane` (whether a lane start gets the MCP server, given the args) (`agents.go:79, 95,
153`); the env lookups (`Start.Env`, `Install(kit, env)`, `Transcript(env, sid)`, `InboxRecord(env)`) are also
functions. The kind's data that lives elsewhere: default models and launch args (`config/models.go:22-38`), the hook
transport (`hooks/hooks.json`: 5 Claude events, each `bash $CLAUDE_PLUGIN_ROOT/bin/obor hook …`; `opencode/obor.js`:
363 lines mapping OpenCode events to the same `obor hook` calls; Codex: `HooksNone`, turns read from the transcript,
`internal/agents/codex/codex.go:318`), and the live conformance script (`tests/conformance/live_<kind>.go`).

**The protocol (opinion, the shape).** An executable `obor-kind-<name>` (any language) answering one JSON request per
run:

| Call | Replaces | When called | Notes |
|---|---|---|---|
| `describe` | `Name`, `Caps`, `Efforts`, `Vars`, `Stop`, `Headless`, default models and args, the transcript glob template, the inbox record template, the hook transport and how to install it | once at `obor setup agents`; cached as a manifest in the state dir | hooks and the guard read the manifest, never the plugin: hook time stays at the binary's own (§8) |
| `start` | `Start`, `StartEnv` | each agent start | **closure 1** `Read`: the plugin reads the file itself (it is a process; the path is in the request). **closure 2** `WriteState`: the reply carries `files: [{name, bytes}]` and obor writes them into the state dir, then substitutes their paths into the argv the reply names. **closure 3** `MCPRegistration.Lane`: a `describe` field when the rule is static (Claude, Codex: manifest or config file), else an `mcp-lane {args}` call |
| `not-ready {screen}` | `NotReady` | during a start, per poll | or a regex in the manifest when it is one |
| `install {kit, env}` | `Install` | setup | returns the file edits as data; setup applies them (as today, `agents.go` comment on `Install`) |
| `turn {tail}` | `LastTurn`, `Session`, `Reset` | per turn end or transcript poll (Codex: `Caps.Turns`) | the one call at machine cadence; per pane event, tens of ms, acceptable; a kind with hooks needs none |
| `deliver {text, record}` | `Deliver` | per line delivered | or a manifest flag `typed: true` for kinds with no inbox |

The env lookups become a map of the variables the manifest names, snapshotted by obor into the request. **Registration**:
`kinds.<name>.command: path` in config (today `kinds` holds launch args per kind, `config/config.go:49`), the manifest
written by setup, and a `plugin` package implementing `agents.Kind` by running the command, so nothing above the
bridge changes. The three compiled kinds stay as reference implementations and keep the live conformance; the
protocol's own test is the static conformance tier run against a plugin kind written as a shell script.

**The cross-axis dependency (fact, inventory axis 1 last bullet).** Pane status (idle, working, blocked, done) comes
from herdr's agent detection via `--kind` (`port.go:17-38`, `effects.go:79`); a kind herdr cannot detect has no
status. Codex's turns already come from its transcript (`Caps.Turns`). The target names the status source per kind in
the manifest: `status: terminal | hooks | transcript`. Claude could run on `hooks` today (its `UserPromptSubmit` and
`Stop` hooks already reach serve, `hooks/hooks.json`); that is also what herdr-core needs (no agent detection,
`docs/research/herdr-core.md` TL;DR). The core already branches on `Caps`, never on names (inventory axis 1 table:
~15 sites); this adds one cap and removes the herdr dependency of the stop and turn rules.

### 2b. The terminal layer (owner's second)

**Fact.** The core drives `port.Panes` (Subscribe, Snapshot, Busy, Activity, Turn, Notify), `PaneControl` (15
methods), `Typer`, `Starter`, `Agents` (`port/port.go:78`, `port/effects.go:15, 50, 56, 164`); `internal/app` imports
no herdr code. Outside the ports: `setup.Herdr` with six more calls (layout, split, new tab, new workspace, plugin
list/link/unlink; `internal/setup/setup.go:76-86`), `HERDR_PANE_ID` read in 9 production files, and the restore after
a herdr restart as a herdr plugin command (`herdr-plugin/herdr-plugin.toml`, `cmd/obor/plugin.go:21-55`). The port
types carry herdr shapes (`Pane.Workspace/Tab/Terminal`, `Rect`, `Plugin`; inventory axis 2). `herdrtest` is a second
implementation of herdr's server (1,332 lines) used by 41 test files (measured today: `grep -rl herdrtest. --include
'*_test.go'`).

**Target (opinion).** Compiled-in adapters, `terminal.kind: herdr | herdr-core | none` in config, behind one
`port.Terminal` that is today's `Panes` + `PaneControl` + `Typer` + `Starter` with the herdr shapes removed:

- `Pane{ID, Agent, Status, Cwd, Label, Session, Transcript}` only; `Rect`, `Plugin`, `Workspace`, `Tab` move to the
  herdr adapter and to setup's herdr-only interface. Setup's layout work ("put L-01 and L-02 in a workers tab",
  `setup/oborinit.go:188, 643-651`) is herdr's and stays in the herdr adapter; a terminal-neutral setup asks the
  adapter for "N panes for these roles" and the adapter decides the layout.
- Identity: obor exports its own `OBOR_PANE=<id>` into the pane before a start (`port.Starter.SetEnv` exists,
  `effects.go:165`); the adapter maps it to the terminal's id. The 9 `HERDR_PANE_ID` readers become one, in the
  adapter.
- Restore after the terminal restarts: serve notices the reconnect on `Subscribe` and runs the restore itself; the herdr
  plugin becomes optional (a faster trigger), not the mechanism.
- `none`: obor owns the ptys. Fact: that needs a pty library (`creack/pty` or raw syscalls), a new dependency outside
  the policy, and reimplements what herdr does. Opinion: list it as an adapter slot, build it only if a project asks;
  herdr-core is the realistic second adapter (`herdr-core.md` §"Moving obor to it means rewriting the adapter layer";
  the core can mostly stay).
- The contract: one test table (start, status, type, keys, screen, wait, rename, process info, notify, reconnect) run
  against `herdrtest` and against each adapter's live probe (the herdr live tier already exists for conformance).

### 2c. Integrations (owner's third)

**Fact.** No integration surface exists (inventory axis 6): intake is `obor spec set` text, lead-only
(`app/specs.go:25-70`); output is a herdr toast and lines into the lead's session; readers of the event log poll the
store or call serve (no subscription); the wire socket has 28 methods in one `Call` struct of 33 fields
(`wire/call.go:84-150`); custom roles exist as `roles.<name>` rows with a write list but no verbs (T-801,
`roles/roles.go:30-46`).

**Target (opinion).** Out-of-process, nothing compiled in. An integration is a process the project runs (a script, a
service) that uses three things obor provides:

| Need | Provides | Shape |
|---|---|---|
| take a task | `spec_set` + `dispatch` wire calls, already there | a custom role with **verbs**: `roles.<name>.verbs: [spec_set, dispatch, send]`; the integration runs with `OBOR_ROLE=<name>` and serve admits the call by the role's verbs (today `spec_set` admits lead/owner only). No pane, no lane: the role row says so (custom roles already are "main checkout, git read-only, never lane-bound") |
| brief the lead | `send`/`brief`, already there | the same role's verbs |
| update the ticket | **an outbound event feed**, new | `obor events --follow --after <seq>` (a wire method streaming records as JSON lines from a seq, or a store poll while serve is down): the integration keeps its cursor, so a restart repeats nothing and misses nothing; idempotent by seq. The alternative, a command hook serve runs per record kind as a rule-tier action, is sugar on top of the feed; build the feed first |
| external id ↔ `T-xx` | a `Ref:` line in the spec text | parsed once by `internal/spec` like `Fixes:` (`spec/spec.go:243`), carried on `wire.TaskStatus` and therefore in the feed's records; no new column, no mapping table. The id itself stays obor's (`task_id_pattern`, `config/config.go:312`) |

What this costs in the core: the verbs column on roles, the feed method, the `Ref` field. What it deletes: nothing at
first. What it makes possible: the per-method request types for `wire.Call` (§6) get a reason, because an external
caller needs a documented request shape.

### What stays compiled in

The core; the store; the three reference kinds and their conformance; the terminal adapters; the workflow
implementations; the gate runner (a command pipeline in config, `checks/runner.go`); the guard (its rules are the
project's safety, read from config and the manifest; its transport is per kind). Opinion: the guard's shell emulator
(1,489 lines) is not on any axis and is left alone, as the 11-05 advice said.

## 3. Configurable workflow

**Fact: what is woven in today** (inventory axis 3). Worktree lanes on `task/T-xx` branches; a scratch merge slot that
pins `refs/obor/submitted/<task>/<id>`, merges onto the target, compiles, pins `refs/obor/candidate/<task>`
(`checks/scratch.go:34-36, 51, 115`); the lane test in the task's worktree (`Scratch.Test`); publish = fast-forward the
target when it still equals the candidate's base, else `moved` (`scratch.go:500-556`); promotion = the same for
staging → main after the full suite and a closed review (`app/promote.go`, `checks/promote.go`); **lane free = the
lane's HEAD is an ancestor of the main checkout's HEAD**, run in `p.Root` (`checks/lanetable.go:100-112`); the pass
pauses when the main checkout leaves the target branch (`Loop.Checkout`, `loop.go:43`); the review close is a commit
whose subject and `Covered:` body are parsed back with `git log --grep` (`app/review.go:69`, `cmd/obor/review.go:59-110`,
`app/promote.go:394-397`: "No state: git alone").

**The interface (opinion).** The six steps the core already has as states (`submitted → candidate → … → published →
merged`, `loop.go:120-140`) and their lane side, named once:

```
Workflow
  Submit(task, lane)            → ref            today Scratch.Submit: commit + pin the lane head
  Candidate(task, ref, target)  → Candidate|conflict|red|moved
                                                 today Scratch.Gate: merge onto target in the slot, compile, pin
  Test(Candidate)               → green|red|fail today Scratch.Test: the class's checks in the lane
  Publish(Candidate)            → done|moved|pending|refused
                                                 today Scratch.Publish: ff; "pending" is new (an external merge)
  Landed(task)                  → bool           today Scratch.Landed (exists, effects.go:274)
  Prepare(lane, task, target) / Free(lane)       today port.LaneSetup (effects.go:377-388)
  Target(task)                  → sha, ahead     today app.Specs.Target (dispatch.go:49-50)
  Review: Head, Detach, Close(rendered)          today app.ReviewGit (review.go:74-93), minus Last/Closes/Merges
```

| Step | `scratch-ff` (today) | `branch-pr` (new) |
|---|---|---|
| Submit | commit the lane's work, pin under `refs/obor/submitted` | commit, push `task/T-xx` to the remote |
| Candidate | merge onto the target in serve's worktree, compile, pin | open or update the PR against the base; `Candidate{SHA: branch head, Target: base sha, Base, Patch}` (no merge commit; the patch binding `Base..S` still holds, `effects.go:233-244`) |
| Test | the class's checks in the lane | CI: `Test` returns `pending` until the PR's checks report; a rule polls (the `Landed` poll below is the same shape) |
| Publish | ff the target, else `moved` | request merge (merge-on-approval): `pending` until the forge merges; `moved` when the base moved and the PR needs a rebase (the lane re-submits, as today's moved re-gate) |
| Landed | the target holds merge evidence of the task | the PR is merged, or the base holds the merge |
| Promotion | staging → main ff after the full suite and a closed review | not a step: the base *is* main, or a release PR the project configures |
| Review coverage | from the fold (below), the close commit rendered | the same fold; the close may also be a PR comment the project configures |

The core's state table does not change: `published` already differs from `merged` (the publish was done; the target
holds it). `pending` adds one phase inside `published` whose exit is the `Landed` rule firing. Fact: the `moved` loop
(one merge commit per task, T-403) and the patch binding (row 20) are properties of `scratch-ff`; the core keeps only
"an accept is bound to a patch hash".

**Lane free and review coverage come from the workflow, not from the main checkout's history.**

- Fact: `LaneState` decides free by `merge-base --is-ancestor <lane head> HEAD` in the *main checkout* plus merge
  evidence and the row (`lanetable.go:100-129`), and five places decide "free" (entity-model §Lane: the git rule,
  `pickLane` `dispatch.go:492`, `view.Lane.Free` `view/view.go:103-106`, `Lane.Column`, `guard/stop.go:123` via
  `Finished`/`Blocked`). Target: one function `Lane.Free(row, landed, dirty)` where `landed` is the workflow's
  `Landed(task)` or the task's terminal state in the fold, and `dirty` is a lane-dir observation (`git status` in the
  lane, which any workflow has). Serve projects `landed` into the lanes table so hooks and `obor status` while serve is
  down read a column (design rule 7), and `LaneTable` stops touching `p.Root`. `Loop.Checkout`'s pause then has one
  reader left (the review close commit in the main checkout) and goes with step 5 of §9.
- Fact: the `review.closed` fact already carries `ReviewData.Tasks` (`app/review.go:53-62`); `unreviewed` reads git
  instead (`promote.go:394-397`). Target: **the log is the source**: the fold holds `Review{id, head, covered,
  fixes, state}`; the close commit and the `BACKLOG.md` line are rendered from it by `Workflow.Review.Close`; a
  reconcile check (not a decision) compares the commit with the fact and warns. Old stores whose closes exist only in
  git: a one-time import of `Covered:` lines into facts at the upgrade, or a compat branch named for its release
  (design rule 5); no permanent `git log --grep` reader.

**Config vs. code, and the reversal.** The 11-05 advice rejected "behaviour in config" because it needs an interpreter
(`architecture-advice-fable.md` §4 "Into config"), and the 11-05 proposal rejected "generic workflow configuration"
(`architecture-proposal-2026-11-05.md` last section). The owner's 11-08 direction asks for feature branches and PRs.
Opinion: both hold at once if the config is a **selector and parameters** and the implementations are Go:

```json
"workflow": {
  "kind": "scratch-ff",                       // or "branch-pr"
  "target": "staging", "promote": "main",     // scratch-ff (today's branches.main/promote)
  "pr": { "remote": "origin", "base": "main", "approvals": 1, "checks": ["ci"] }   // branch-pr
}
```

No step order, no conditions, no templates in JSON: those stay in the two Go implementations and their contract
table. `branches.task` (the `task/{id}` pattern, `config/config.go:93-97`) and `lanes_dir` stay as they are.

## 4. Lane shape

**Fact.** Lanes are a count: `lanes: N`, names `L-01…`, dirs under `lanes_dir` (`config/config.go:47, 654-665`); one
task per lane row (`store/lanes.go:18-31`), written by one fold (`app/lanerows.go`, 156 lines); the lane is picked as
the first free lane of the dispatch's types (`dispatch.go:492`); a merged task's steps are `after_merge, agent.stop`
(`loop.go:112`) and a dispatch's are `lane.prepare → agent.start → work`, so **every task starts a fresh agent**;
`job_next` resolves the caller's job by lane → task (`app/fetch.go:128`); a dispatch to a lane already dispatched is
refused ("lane L is dispatched to T", `dispatch.go:~505`); waiting dispatches exist (`stWaiting`, `--after`). `LaneRow.Agent`
stores a derived name (entity-model §Agent, against AGENTS.md "a name obor can derive is never stored").

**Target (opinion).** A `Lane` entity owns its row, its mode and its queue; the N-lane shape is the degenerate case.

| | N lanes (today) | one lane, long list |
|---|---|---|
| config | `lanes: 4` (sugar for four `fresh` lanes) | `lanes: [{"name": "L-01", "keep_agent": true}]` |
| after merge | `after_merge, agent.stop` | `after_merge` only; the agent stays at its shell |
| next dispatch | `lane.prepare → agent.start → work` | `lane.prepare` (the agent is idle and the lane clean: `checkout -B` is safe) `→ work`; `agent.start` runs only when the lane has no live agent |
| the list | one dispatch per free lane | a dispatch naming a busy lane **queues** behind its current task (today refused); order = dispatch order, `--after` still honoured |
| job delivery | unchanged: the wake line is constant, the job is fetched (`job_next` by lane) | unchanged; `job_next` returns the next task's job to the same session |
| context growth | n/a | a rule: a keep-lane agent past `handoff_tokens` (`config/config.go:53`) and idle between tasks is restarted; `agent.stop → agent.start` as a rule-tier action, never mid-task |
| session facts | `store.Session.Task` = the task | `Task` = the current task (a session spans several); the row's `task` is "current task" |

What it deletes: the stored `LaneRow.Agent` (derive it: `roles.Role.AgentName`, `roles/roles.go:170`) and the two
precedence functions `app.laneAgent` / `view.laneAgent` (entity-model §Lane). What it adds: `Mode` and `Queue` on the
lane, one config shape. What stays: `L-NN` names, worktrees, one branch per task (the branch is the workflow's, §3).
Risk: 168 test files use `"L-0N"` literals (inventory axis 4), none of which change meaning.

## 5. The gate

**Fact.** The gate is already config: `commands.*` (11 named commands), `merge.classes` globs, `merge.guard`,
`neutral`, `affected_full`, `summary_regex`, `fail_grep`, `nightly_commands` (inventory axis 5). The Go-specific
residue in the kit (inventory §5 table): `obor affected go` (219 production lines, called only from `Makefile:112`),
`CHECK_SHORT=1`/`CHECK_DIRECT=1` exported when `branches.promote` is set (`checks/lanetest.go:84-87`), `LaneEnv`
(`GOMAXPROCS`, `GOFLAGS=-p`; `config/config.go:874-890`), `flakes` (439 lines reading `go test` output), `testRegexp`
(`go test -run '^(A|B)$'`, `runner.go:551`), `go.mod` detection at `obor init`, `slots.cpus` → `go test -p`. About
700 to 1,000 lines, none in the merge loop's decisions.

**What stays generic config (opinion):** classes and their commands; the lane tier vs. full tier as two commands per
class (`lane_cmd`, `full_cmd`) instead of an env pair; a generic env the lane test exports as *facts*, not policy
(`OBOR_LANE_CPUS`, `OBOR_TARGET`, `OBOR_SHA`, `OBOR_PROMOTION=1`); an env map per role or lane in config, templated
with `{cpus}`; the retest command with a `{tests}` placeholder; the flake parser as a regex pair beside
`summary_regex`/`fail_grep`; heavy-command slots; the nightly list.

**How obor-kit's Go policy leaves the kit:**

| Today | Target | Where it goes |
|---|---|---|
| `CHECK_SHORT=1 CHECK_DIRECT=1` from `lanetest.go:84-87` | serve exports `OBOR_PROMOTION=1` (a fact); the Makefile maps it | `.obor/config.json` + `Makefile` of obor-kit |
| `LaneEnv` → `GOMAXPROCS`, `GOFLAGS` | `slots.lane_env: {"GOMAXPROCS": "{cpus}", "GOFLAGS": "-p={cpus}"}`; serve always exports `OBOR_LANE_CPUS` | obor-kit's config carries the Go lines |
| `obor affected go` (`internal/affected`, `cmd/obor/affected.go`) | a kit tool (`tools/affected`, `go run` from the Makefile), or a shell `go list` pipeline | out of the product |
| `flakes` parser (`--- FAIL:`, `FAIL <pkg>`) | the log and `obor flakes` stay (decision 11-05); the parser reads `merge.flake_regex` with the Go default in obor-kit's config | config |
| `testRegexp` | `commands.retest` with `{tests}` joined by the config's separator | config |
| `go.mod` → `go test ./...` at init | an init template per detected ecosystem (a suggestion, not policy) | `templates/` |

The kit's tiers (`-short`, `testkit.Slow`, the budget) are the kit's own test policy and are addressed in §7.

## 6. Entities

Fact: the entity-model doc measured the spread; its "Targets" table (§Targets) gives owners and costs. This section
takes those as the target and adds the union-struct decision. Owner and "one place it changes" per entity:

| Entity | Owns | Changes in one place | What goes | Cost (measured, entity-model) |
|---|---|---|---|---|
| **Task** | `mtask` + `attemptVars` + `disp` as one value; `Lane`, `Agent`, `Attempt`, `State`, `Needs`, current `Job`, `Candidate`, `SpecRev`, counters; `Status()` builds `wire.TaskStatus` | the fold (`mtask.go:13`) | 6 `state` writes outside the fold (`dispatch.go` 2, `states.go` 3, `stopstep.go` 1); the inline 19-field literal at `status.go:70` | ~12 write sites in 3 files; 96 test writes of `mt.state` in 12 files |
| **Lane** | row facts + mode + queue + one `Free()` / `Holds(task)` (§4) | `app/lanerows.go` (row) and one observation source | the 5 deciders of free; `LaneRow.Agent`; the two `laneAgent`s; `LaneRow.Branch` vs `port.Lane.Branch` (keep the row's) | 44 write / 82 read sites, 4 writer files; 256 test writes |
| **Step / Job** | `Step{Key, Class, Intent, Result, Tries}`, `Job{Key, Text, Sent, Delivered, Lost, Patch}`; `Next(base)` replaces `nth` + 10 counters | `App.do` / `record` | 10 counter fields; `StepN`/`nth` in two packages; 8 `attemptVars.job*` fields | 27 read / ~8 write sites; `JobReply` unchanged |
| **Agent** | derived `Name`, `Kind`, `Role`, `Lane`, current `Session`, pane binding; the proof ranks stay in `registry.go` | `registry` + the `store.Session` append | stored names; `wire.AgentStatus`'s 3 copied session fields | small in production; 768 test writes (fake pane literals): mechanical |
| **Review** | `{id, head, covered, fixes, state}` as one fold value; the commit and the BACKLOG line are rendered outputs (§3) | `foldReview` + `reviewClose` | `unreviewed`'s git read; `ReviewGit.Last/Closes/Merges`; `CloseCommitGrep` | 13 write sites in 3 files + 4 production files reading git; real-git tests in `cmd/obor` |
| **Spec** | parsed fields (title, difficulty, models, review, fixes, protected, **ref**) beside `Text`, `Rev` | `specSet` (`app/specs.go:25`) | 24 free-function parse sites in 13 files become field reads | mechanical; stored text unchanged |
| **Candidate** | as today | `checks/scratch.go` | nothing; watch `Diff`/`Stat` size (15 KiB) | 1 writer file |

**The union structs.** Fact: `wire.Call` 33 JSON fields for 28 methods (`wire/call.go:84-150`; `Note` has six
meanings), `event.Message` 15 fields (`event/data.go:44-80`; `Fresh`, `Stop`, `Supersedes` each read in one
production file), `event.FactData` 18 fields (`event/log.go:43-68`: watchdog, merge-loop and role facts in one
payload). All three are persisted or wire shapes, so a split is a compat window under design rule 5. Opinion on
whether and when:

| Struct | Split? | When | Why |
|---|---|---|---|
| `wire.Call` | yes, per method: `type SpecSetReq`, `DispatchReq`, … with `Call` as the envelope (`Root, Agent, Role, Version, CallID`) | with the integration surface (§2c, §9 step 11) | an external caller needs a documented request per method; splitting earlier pays the compat cost with no one to use the result. Not persisted (a socket shape): both sides ship in one binary, so the window is one release |
| `event.Message` | no migration; each rule conversion moves its field onto `Of{Kind, Seq}` (the 11-05 plan, partly done) and the census deletes dead fields | as the rules land | persisted; the 11-05 M1 route is already the mechanism |
| `event.FactData` | no, unless a new feature would add a 14th pseudo-task kind (entity-model §Targets last row) | then | persisted; 68 write sites in 28 files and 953 test writes for `Record` itself |

The standing pass's numbers (entity-model §"What the standing pass could check"): production files per owner struct,
new fields on the three unions since the last pass, stored derived values. Those are the per-pass measurement for this
section.

## 7. Tests

**Fact.** 301 test files, 1,542 `func Test`, 85,028 test lines (inventory §Method); 39 `testkit.Slow` marks in 19
files and 124 `testkit.New`/`NewAt` calls in 60 files (`cmd/obor` 85, `view` 14, `web` 5; measured today with `grep
-rn`), 2 `NewDir`, 2 `Adopt`. The budget caps 33 packages at 20,688 git processes, 271 s `-short` and 368 s full at
2 cores (`tests/budget.json`, sums computed today). The measured git split: ≈14.4k processes per full run, **≈8.2k
(57%) are fixtures** (`testkit.New`: `init`, `add`, `commit`, `worktree add`, 7.4 per project), ≈6.2k are code under
test, 4.0k of those in `internal/checks` (inventory §7 table). Per package the fixture share: `launch` 1,967 of 2,041,
`guard` 1,117 of 1,127, `cmd/obor` 1,579 of ≈2,230, `setup` 1,164 of 2,024, `config` 675 of 921, `checks` 1,240 of
5,280. `internal/checks` already builds a template once per run and copies it (T-998, `checks/fixture_test.go:29-120`).

**Three kinds of test, by what they need (opinion; the rework plan's §2 table is the same split per package):**

| Kind | Needs | Tests | Where it is today |
|---|---|---|---|
| **unit** (decisions) | no git, no herdr, no sockets; a fake per port with scripted outcomes | the fold and rules (`internal/app` on `FakeScratch`, done: 30 git processes), the cut table as a function over git answers (new, §1), launch's start/stop policy over a scripted terminal (new), `view` over injected lane facts (new), the guard's rules, config validation without `rev-parse` | app done; checks, launch, view, config not |
| **integration** (an adapter's contract) | the real outside system, one temp repo or `herdrtest`; **a few per port method**, not per decision path | `checks`: Submit/Candidate/Test/Publish/Landed/Prepare/Free on real git, a scenario table whose names match the fake's scripted cases (rework plan risk 2); `herdr`: the contract table against `herdrtest` and the live probe; `setup`'s githooks (git runs them) | checks has it mixed with decisions (85 tests, 5,280 processes); herdr has `contract_test.go` |
| **end-to-end** | the built binary, one fixture project, a stub `herdr` | one flow per entry point: dispatch → merge, drop, review close, stop/start, serve-down reads; live conformance per kind (`make conformance TIER=live`) | `cmd/obor` (85 fixtures, 43 test files, 34 build one) |

**The fixture fix.** Generalize T-998 into `testkit`: `testkit.Template(t)` returns a copy of a repo built once per
test binary (a `cp -R` of a two-commit repo with one worktree is no git process); `New` becomes that copy, `NewDir`
(a plain dir, no git) becomes the default for packages whose code under test runs no git (`launch` 74 code processes
of 2,041, `guard` 10 of 1,127: those two are almost pure fixture cost). Fact to check first: `project.Find` falls back
to `git rev-parse` (`project/project.go:48-52`); `NewDir` writes `.obor/config.json`, which `Find` takes first. The
measurement: the budget's counting wrapper per package (`tests/budget/main.go:325-330`); target fixture share under
10%, total under 7k, and the caps lowered by the task that gets under them (the budget rule).

**How this replaces the slow/fast tiers.** Fact: the tiers exist because tests are slow for I/O reasons: `-short`
skips `Slow` tests (`testkit.go:32-38`), the lane runs `-short` of the affected packages and the nightly runs
everything (decision 11-08 lead). Opinion: once unit tests have no I/O and integration tests are a few per port
method, the affected packages' whole suite fits a lane at 2 cores and the distinction has nothing left to separate:
the gate is `go test` of the affected packages with no `-short`, the nightly adds the live tiers and the budget
check. The budget stays as the size ratchet (it measures wall time, git processes and ≥ 1 s tests: exactly the three
things this section removes). The tiers are deleted last, when the measurement says so: the lane test step's median
from the event log (597 s before the rework, `test-suite-rework.md` table; the rework's target 60-90 s at 2 cores) and
`make check-budget` green with the `untagged_s` and `tests` sections empty.

## 8. The language question

**Measured today** (this machine, arm64 macOS, 10 runs each, median wall time of a cold process start; the Python
driver is in the session log; `obor hook pre-tool-use` was run outside any project, so it is the no-op path: the floor
of a hook, not a hook with a project):

| Process | Start | Binary |
|---|---:|---:|
| `dist/obor hook pre-tool-use` (Go, static) | 7.4 ms | 16.3 MB |
| `bash bin/obor version` (the shim, then the binary) | 12.0 ms | 205 lines of bash |
| `bun -e 0` | 8.8 ms | 63 MB |
| `deno eval 0` | 12.4 ms | 83 MB |
| `node -e 0` (v26) | 22.8 ms | 147 MB |
| `python3 -I -c 0` | 27.6 ms | runtime |
| `sh -c true` (the floor) | 3.4 ms | |

Fact: hooks run on every `Bash|Edit|Write|NotebookEdit` tool call plus prompt submit, stop and session start
(`hooks/hooks.json`); the design's bound is < 200 ms (`docs/design.md:20`). Sizes: 53,868 production and 85,028 test
lines, 1,542 tests (inventory §Method); the 10-13 bash → Go rewrite targeted 12k lines and the tree is 54k three
weeks later (`architecture-advice-fable.md` §4); 11-05's estimate of a port was four to eight weeks of the whole swarm
with a double implementation.

**The owner's concern: does Go make plugins harder?** Opinion, from §2: the difficulty of a plugin is its boundary,
not the host's language. An out-of-process protocol is language-neutral; a kind plugin in TypeScript or Python works
against a Go host exactly as against a TypeScript host. Where TypeScript would be easier is *in-process* plugins
(import a module). But the in-process code a kind needs already lives inside the agent CLI in the CLI's language
(`opencode/obor.js` is a JS plugin for OpenCode, not for obor), and obor's side is string handling. The terminal and
workflow adapters are written here either way.

| Option | Gains | Costs | Verdict |
|---|---|---|---|
| **Go + out-of-process plugins** (this doc) | keeps 7 ms hooks, one 16 MB static binary, no runtime, the two dependencies; plugins in any language | the kind protocol (~500 production lines, opinion), the feed (~150), role verbs (~50); one extra process per plugin call at agent cadence | **recommended** |
| TypeScript (Bun/Deno compiled) | shared types with the web page; in-process plugins; perhaps 25-35% fewer glue lines (11-05 estimate) | 63-83 MB binary; 9-12 ms hook floor (fine against 200 ms); npm supply chain in a tool with write authority over repos; the "no node or bun in shipped code" rule; porting 85k test lines, which is where the behaviour is; months with two implementations; and the mixed responsibilities port verbatim unless redesigned first | no |
| Rust / Elixir / a durable-execution engine | as the 11-05 table (`advice §4`) | unchanged | no |

What a port would not fix: §1's five mixed pieces, §4's lane-free-in-five-places, §6's unions. Those are design, and
the fix is the same work in either language. Doing it in place is the cheaper order.

## 9. Migration order

Small steps; each deletes a mechanism or unmixes a responsibility; each has a measurement. The first three are where
the cost hurts now. Dependencies are named; steps without one can run in parallel lanes. No tasks or specs here: the
owner picks, the lead writes them.

| # | Step | Deletes | Adds | Risk | Measurement |
|---|---|---|---|---|---|
| 1 | **Fixture template in testkit** (generalize T-998): `New` copies a repo built once per test binary | ≈8k per-test `init/add/commit/worktree` git processes (57% of all) | `testkit.Template`, a `cp -R` | a test that mutates the template (copy, never share); `PATH`-sensitive tests in `cmd/obor` (inventory §Method) | git processes per package by the budget wrapper: fixture share < 10%; `tests/budget.json` caps lowered |
| 2 | **`NewDir` where code under test runs no git**: `launch`, `guard`, most of `cmd/obor`'s status/flag tests | the remaining fixtures there | nothing (`NewDir` exists) | `project.Find`'s `rev-parse` fallback | `launch` and `guard` under 100 git processes each |
| 3 | **`checks.Scratch` decide/do split**: the cut table as a pure function over git answers; `gitReader`/`gitWriter` under `Scratch`; one real-git scenario table whose names match `FakeScratch`'s cases | the real-git decision tests (`TestScratchCutTable` 47 s cap) | two small interfaces, a fake of them | a conflict case the real merge produced for free (rework plan risk 1): the scenario table lists each | `internal/checks` git processes 5,280 → < 1,500; its wall < 10 s at 2 cores; the 47 s cap gone |
| 4 | **Lane free in one place**: `Lane.Free(row, landed, dirty)`; `landed` projected into the lanes table; `LaneTable` stops reading the main checkout's HEAD | 4 of the 5 deciders; the `p.Root` ancestry read; `view`/`web`/`cmd` tests' per-call git | one function, one column | `landed` for tasks merged by hand (today's merge evidence read, kept as the workflow's `Landed`) | `rg` finds one decider; `cmd/obor` code-under-test git 651 → ≈0; `web` 108 → 0 |
| 5 | **Review state from the log**: `review.closed` carries covered tasks and head; `unreviewed` reads the fold; the close commit and BACKLOG line are rendered; `Loop.Checkout` pause goes | `CloseCommitGrep` parsing, `ReviewGit.Last/Closes/Merges`, `promote.go`'s git read, the checkout pause | an import of old `Covered:` lines at upgrade (or a named compat branch) | stores whose closes are only in git | `promote.go` runs no git for coverage; `cmd/obor/reviewcycle.go` under 100 lines |
| 6 | **Spec parsed once**, `Ref:` added | 24 free-function parse sites → field reads | `spec.Parse`, fields on `SpecRow`, `Ref` on `TaskStatus` | parse errors readers ignored today surface once at set | `spec.` call sites outside `internal/spec` = 1 (the set) |
| 7 | **`port.Workflow`**: `Scratch` + `LaneSetup` + `Specs.Target` + `ReviewGit.Head/Detach/Close` + `Promote` under one interface; `scratch-ff` implements it; `Publish` gains `pending`; config `workflow.kind` | 6 `Loop` fields → 1; `branches.promote` moves under `workflow` | the interface, the config key, a `branch-pr` stub that compiles | `Promote` is woven with coverage (needs 5) | `Loop` has ≤ 6 fields; a stub second implementation passes the contract table's non-git rows |
| 8 | **Lane entity + keep mode**: `Mode`, `Queue`; `after_merge` without `agent.stop` for keep lanes; a named-lane dispatch queues; the handoff-tokens restart as a rule | stored `LaneRow.Agent`, the two `laneAgent`s, the "dispatched to" refusal for named lanes | `lanes[].keep_agent`, the queue | `checkout -B` under a live idle agent (the lane must be clean: `Prepare` already requires it) | an e2e runs three tasks on one keep lane in one session; `rg LaneRow.Agent` = projection writer only |
| 9 | **Kind plugin protocol** (owner's first axis): `describe`/`start`/`install`/`turn`/`deliver`; the three closures become data; `Caps.Status` source; a `plugin` kind implementing `agents.Kind`; manifest cached at setup; registration `kinds.<name>.command` | nothing at first; then one compiled kind (opinion: OpenCode, 188 lines, its JS already lives in the CLI) moves out as the reference plugin and its package goes | ~500 lines (opinion) | hook time if anything reads the plugin instead of the manifest | static conformance passes for a shell-script kind; live conformance for the moved kind; `obor hook` start unchanged (≤ 10 ms) |
| 10 | **Terminal port cleanup** (second axis): herdr shapes out of `port`; `OBOR_PANE` identity; a contract table against `herdrtest` and the live probe; restore on reconnect | 9 `HERDR_PANE_ID` readers → 1; `Rect/Plugin/Workspace/Tab` from `port` | `OBOR_PANE`, the table | the registry's proof ranks assume terminal-reported session ids (`registry.go:45-55`) | `rg HERDR_PANE_ID internal cmd` = adapter only; a herdr-core spike compiles against the port |
| 11 | **Integration surface** (third axis): `obor events --follow --after N`; role verbs; a sample integration under `tests/` or `examples/` | nothing | ~200 lines | an integration role's write rights (the guard already holds custom roles to a list) | the sample takes a fake ticket → `spec_set` + `dispatch` → `merged` event → ticket updated, in e2e |
| 12 | **Gate residue out** (§5): generic env facts, `slots.lane_env`, `affected` as a kit tool, flake and retest regexes in config | ~700 lines from `internal/` and `cmd/obor` | config keys in obor-kit's `.obor/config.json`, Makefile lines | a lane's Go env missing when the config forgets it (the kit's config is the one that carries it) | `rg -i 'go test|GOFLAGS|GOMAXPROCS|--- FAIL' internal cmd` = 0 outside tests |
| 13 | **`wire.Call` per method** (with 11) ; `Message`/`FactData` by rule conversions and census | the 33-field union as the request shape | an envelope + 28 small types | one-release wire window | the entity probe's field count per union; `Note` has one meaning |
| 14 | **Tiers go**: `-short`, `Slow`, `CHECK_SHORT` deleted when the full affected run fits a lane | the two-tier mechanism and its tests | nothing | the measurement must come first | lane test step median ≤ 90 s at 2 cores (from the event log); `make check-budget` green with the per-test section empty |

Order notes (opinion): 1-2 are independent of every design choice and cut cost at once; 3-6 remove the mixed pieces
the workflow interface would otherwise wrap; 7-8 are the configurability the owner asked for; 9-11 are the three
plugin axes in the owner's order (9 can start any time: it touches `agents` and `launch` only; its `Caps.Status`
source is what lets 10 drop herdr's detection); 12 is the kit's own and fits in any gap; 13-14 close. Each step is a
deletion or an unmixing under the Change rules (replacement deletes, spec names what it adds, compat by census).

## Not verified

- No prototype of the kind protocol, the PR workflow or the keep-lane mode; §2 and §3 are my reading of the interfaces
  (as the inventory's "would need" sections were).
- The start times in §8 are cold starts on one machine today; a hook with a project loads config and opens the store,
  which the no-op path does not. The 11-05 "30 to 50 ms" for TypeScript (`advice §4`) is higher than what I measured
  for Bun and Deno.
- Line estimates in §8 and §9 (~500, ~150, ~200, ~700) are from reading; the inventory's measured lines are cited where
  they exist.
- I did not check who outside this repo reads `obor status --json` (unknown on 11-05 and 11-08), which §4's row change
  and §6's `TaskStatus.Ref` touch.
- `docs/design.md` beyond "Change rules" was not re-read for rules a step here would break.
