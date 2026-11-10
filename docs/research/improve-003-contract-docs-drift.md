# Plan 003: `docs/module-map.md` and `AGENTS.md` say what the code is today

> **Executor instructions**: Follow this plan step by step. Run every verification command and confirm the expected
> result before moving to the next step. If anything in the "STOP conditions" section occurs, stop and report; do not
> improvise. When done, update the status row for this plan in `docs/research/improve-plans.md`, unless the lead
> dispatched you and keeps the index.
>
> **Drift check (run first)**: `git diff --stat a73e814c..HEAD -- docs/module-map.md AGENTS.md go.mod internal/app`
> If `docs/module-map.md` or `AGENTS.md` changed since this plan was written, compare the "Current state" excerpts
> against the live files before proceeding; on a mismatch, treat it as a STOP condition. New or removed files in
> `internal/app` are not a mismatch: step 2's command lists whatever is missing now.

## Status

- **Priority**: P2
- **Effort**: S
- **Risk**: LOW
- **Depends on**: none
- **Category**: docs
- **Planned at**: commit `a73e814c`, 2026-11-10

Spec lines, if the lead turns this into an obor spec: `Difficulty: simple` · `Guarantee: none (docs only)` ·
`Replaces: three sentences about aliases that T-699 deleted` · `Adds: none`.

## Why this matters

`docs/module-map.md` is a contract: `AGENTS.md` says the integrator checks every task's file placement against it at
accept, and workers read it to find where code goes. It has drifted in three ways. It says three times that package
`app` "keeps aliases … until a later task removes them", naming files (`app/ports.go`, `app/events.go`,
`app/wire.go`) that task T-699 deleted (commit `b8485c3d`). Its file list for `internal/app` names 46 of the 64
production files. And `AGENTS.md` says "the two dependencies" while `go.mod` has four direct ones. A reader who
trusts these lines looks for files that are not there, or misses a third of the core package.

## Current state

`docs/module-map.md`, the three stale sentences (each wraps over two lines in the file):

- lines 129-130, in the `app/port` bullet:
  `` Adapters import it instead of `app`; `app` keeps type and const aliases of it until a later task removes them (`app/ports.go`). ``
- lines 134-135, in the `app/event` bullet:
  `` It imports `store` only. `app` keeps type, var and const aliases of it until a later task removes them (`app/events.go`). ``
- lines 154-155, in the `app/wire` bullet:
  `` `mcp`, `launch`, `checks`, `view` and `web` import it instead of `app`. `app` keeps type, var and const aliases of it until a later task removes them (`app/wire.go`). `Listen` and the handlers stay in `app`. ``

There are no aliases left: `grep -rn "= *\(port\|event\|wire\)\.[A-Z]" internal/app/*.go` finds no type, const or
var alias block, and `git log --oneline -1 b8485c3d` reads "T-699: Removed all type/const/var aliases from
internal/app. Deleted internal/app/ports.go, events.go and …".

`docs/module-map.md` line 181 names `gitlock_test.go` among `internal/checks`' integration test files; the file is
`internal/checks/gitlock_integration_test.go`.

`docs/module-map.md` lines 102-122 are the **core** `app` bullet: one long paragraph that starts
`` **core** — `app`: the only place with obor logic. Files by concern: `app.go` (types, options), `serve.go` (New, Serve, … ``
and ends `` `effects.go` (`JobID`), `describe.go` (`Describe`: …): one concern each. `` Each entry is a file name in
backticks followed by a short parenthesis saying what the file holds, often with a task id.

The 18 production files of `internal/app` that paragraph does not name, each with the first line of its own leading
comment (your source for the parenthesis; read the file's comment for the rest):

| File | Its own first comment line |
|---|---|
| `block.go` | the merge loop's needs-you as a block on a task's phase (T-827, DD-consolidation S2) |
| `disk.go` | Low disk (T-564): the project volume's free space below diskLow is the project's needs-you … |
| `fetch.go` | Jobs by fetch (docs/state-model.md rows 5–8 and 16–17). Every agent fetches its … |
| `finish.go` | awaitsTaskDone: mt's attempt is its worker's and no task_done submitted it yet … |
| `fold.go` | foldStart folds a start's result into p's identity table … |
| `follow.go` | serve follows a new obor binary (T-719) … |
| `lanepick.go` | the lane a dispatch takes (held, freeFor, pickLane, pausedModels) and the replies that say why … |
| `move.go` | obor dispatch T-xx --model <ref> on a task its worker works on moves it to that model (T-773) … |
| `review.go` | A review request (T-638; docs/state-model.md row 52) … |
| `reviewclose.go` | a review's close is a call to serve (T-926) … |
| `reviewfold.go` | the reviews and the merges as the log holds them (T-1023) … |
| `reviewonmerge.go` | the Review: on merge rule (T-878, T-932) … |
| `reviewverify.go` | A review's fix round and its verify (T-924) … |
| `roleworktree.go` | Role worktrees (T-853): the reviewer and the deep dive read their own worktree … |
| `scheduler.go` | The scheduler's rule for serve's worktree (the scratch), in one place (T-669, RV-75) … |
| `stopstep.go` | the stop of a task's worker as a step of the merge loop (T-852) … |
| `waiting.go` | a dispatch that waits for the tasks it names (--after) or for a lane … |
| `worktext.go` | the work job's wake: its text (the worker prompt and the role texts) and the send of it … |

`AGENTS.md` lines 22-23, in "Runtime constraints":

```
  Today the two dependencies are the shell parser (`mvdan.cc/sh`) and SQLite (`modernc.org/sqlite`, cgo-free, with its
  matching `modernc.org/libc` pin). One static binary
```

`go.mod` lines 7-12, the direct requirements:

```
require (
	github.com/google/jsonschema-go v0.4.3
	github.com/modelcontextprotocol/go-sdk v1.8.0
	modernc.org/sqlite v1.60.1
	mvdan.cc/sh/v3 v3.14.1
)
```

`internal/mcp/tools.go` imports both `github.com/modelcontextprotocol/go-sdk/mcp` and
`github.com/google/jsonschema-go/jsonschema` (the tools' input schemas: `jsonschema.For`, line 93). The owner's
decision is on record in `docs/decisions.md`: "2026-11-05 (lead, T-877 trial's pass mark): **The official MCP Go SDK
is adopted.**"

Style of both documents: dense prose, lines wrapped at about 120 columns, file and symbol names in backticks, a task
id in parentheses where one explains a thing. Match it; no new headings, no tables.

## Commands you will need

| Purpose | Command | Expected on success |
|---|---|---|
| `internal/app` files the map does not name | ``for f in $(ls internal/app/*.go \| grep -v _test.go \| xargs -n1 basename); do grep -q "\`$f\`" docs/module-map.md \|\| echo "missing $f"; done`` | no output |
| Stale alias sentences | `grep -c 'aliases' docs/module-map.md` | `0` |
| The wrong test file name | `grep -c 'gitlock_test.go' docs/module-map.md` | `0` |
| Longest line | `awk 'length > 122' docs/module-map.md AGENTS.md \| wc -l` | no more than before your change (run it first and note the number) |

No Go command is needed: this plan changes two Markdown files, and both paths are in the project's `no_check` list.

## Scope

**In scope** (the only files you should modify):
- `docs/module-map.md`
- `AGENTS.md` (the one sentence)

**Out of scope** (do NOT touch):
- `docs/design.md`: protected, the lead's.
- Any `.go` file, including the leading comments you read.
- Rewording or reordering the entries the `app` paragraph already has, and the other packages' bullets. If you see
  another stale statement there, list it in your report under "Concerns"; do not fix it here.
- A test that ties the map to the file list: see Maintenance notes.

## Git workflow

- If you are an obor worker (your job came through `job_next`): work in your lane, make no commits, finish with
  `task_done`.
- Otherwise: a branch `advisor/003-contract-docs-drift` cut from `staging`, in a separate git worktree. Commit
  message style: one sentence saying what is true now. Do not push.

## Steps

### Step 1: Remove the three alias sentences and fix the test file name

In `docs/module-map.md`:
- `app/port` bullet: `` Adapters import it instead of `app`; `app` keeps … (`app/ports.go`). `` becomes
  `` Adapters import it instead of `app`. ``
- `app/event` bullet: delete the sentence `` `app` keeps type, var and const aliases of it until a later task removes them (`app/events.go`). ``
- `app/wire` bullet: delete the sentence `` `app` keeps type, var and const aliases of it until a later task removes them (`app/wire.go`). ``
  and keep `` `Listen` and the handlers stay in `app`. ``
- line 181: `gitlock_test.go` becomes `gitlock_integration_test.go`.

Re-wrap only the lines you touched.

**Verify**: `grep -c 'aliases' docs/module-map.md` → `0`; `grep -c 'gitlock_test.go' docs/module-map.md` → `0`;
`grep -c 'gitlock_integration_test.go' docs/module-map.md` → `1`.

### Step 2: Name the 18 missing files in the `app` paragraph

For each file in the table above, read its leading comment in `internal/app/<file>` and add an entry
`` `<file>` (<what it holds, in under 20 words, with its task id>) `` to the **core** `app` paragraph, next to the
entries of the same concern where one is obvious (the five `review*.go` files together; `stopstep.go` and `move.go`
after `stopwake.go`; `waiting.go` and `lanepick.go` after `dispatch.go`; `fetch.go`, `finish.go` and `worktext.go`
after `jobstart.go`), otherwise before `` `effects.go` (`JobID`) ``. Keep the paragraph's closing `: one concern each.`

**Verify**: the first command of "Commands you will need" prints nothing.

### Step 3: `AGENTS.md` names the dependencies `go.mod` has

Replace the sentence that starts `Today the two dependencies are` with:

```
  Today the dependencies are the shell parser (`mvdan.cc/sh`), SQLite (`modernc.org/sqlite`, cgo-free, with its
  matching `modernc.org/libc` pin) and the official MCP Go SDK (`github.com/modelcontextprotocol/go-sdk`, adopted
  2026-11-05, with `github.com/google/jsonschema-go` for the tools' schemas). One static binary
```

Keep the rest of that bullet as it is.

**Verify**: `grep -c 'two dependencies' AGENTS.md` → `0`; `grep -c 'modelcontextprotocol/go-sdk' AGENTS.md` → `1`.

## Test plan

No test is added, changed or deleted: two Markdown files change, and no test reads the sentences touched
(`grep -rn 'module-map' --include='*_test.go' .` finds only `internal/checks/boundary_test.go`, which names the
document in its messages and reads none of its text).

## Done criteria

- [ ] `grep -c 'aliases' docs/module-map.md` prints `0`
- [ ] The "files the map does not name" command prints nothing
- [ ] `grep -c 'gitlock_test.go' docs/module-map.md` prints `0`
- [ ] `grep -c 'two dependencies' AGENTS.md` prints `0`
- [ ] `git status --short` lists only `docs/module-map.md` and `AGENTS.md` (and the index, if you keep it)

## STOP conditions

Stop and report back (do not improvise) if:

- The alias sentences are not at the quoted places, or `grep -rn "= *\(port\|event\|wire\)\.[A-Z]" internal/app/*.go`
  now finds an alias block (someone brought aliases back: the sentences would be true again).
- A file in the table no longer exists, or its leading comment says something that contradicts the table's line.
- `go.mod`'s direct requirements differ from the four quoted above.

## Maintenance notes

- Why the list drifted: every task that adds a file to `internal/app` must also add it to this paragraph, and
  nothing checks that. Two ways to stop it, both the lead's call and neither part of this plan: a test beside
  `internal/checks/boundary_test.go` that fails when a production file of `internal/app` is not named in the map
  (the step 2 command as a Go test, about 20 lines), or dropping the per-file list from the map and relying on each
  file's own leading comment (the map keeps packages and import rules only). The second removes text every task has
  to maintain.
- `AGENTS.md` also says `govulncheck` runs as `check_milestone`; the config key is `nightly.slow_check` since config
  shape 2, while the step name in the code is still `check_milestone`. Left as it is: the name is still what the
  nightly's summary line prints.
