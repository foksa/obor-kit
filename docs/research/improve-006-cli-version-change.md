# Plan 006: Serve tells the lead when an agent CLI or the terminal changed version

> **Executor instructions**: Follow this plan step by step. Run every verification command and confirm the expected
> result before moving to the next step. If anything in the "STOP conditions" section occurs, stop and report; do not
> improvise. When done, update the status row for this plan in `docs/research/improve-plans.md`, unless the lead
> dispatched you and keeps the index.
>
> **Drift check (run first)**: `git diff --stat 65389c8c..HEAD -- internal/app/rules.go internal/app/disk.go internal/app/loop.go internal/app/project.go internal/app/app.go internal/app/describe.go internal/app/event/steps.go internal/app/panes.go cmd/obor/serve.go internal/launch/launch.go docs/module-map.md`
> If any of these changed since this plan was written, compare the "Current state" excerpts against the live code
> before proceeding; on a mismatch, treat it as a STOP condition.

## Status

- **Priority**: P2
- **Effort**: M
- **Risk**: LOW-MEDIUM (a new fact in the event log and a new rule; no refusal, no change to a start)
- **Depends on**: none
- **Category**: reliability
- **Planned at**: commit `65389c8c`, 2026-11-10 (idea A8 of `docs/research/improve-architecture.md`)

Spec lines, if the lead turns this into an obor spec: `Difficulty: hard` (the event log, the fold and the rule tier
of `internal/app`) · `Guarantee: rule (one line to the lead per change; it gates and refuses nothing)` ·
`Replaces: none` · `Adds: fact version (task key version), Loop.Versions, launch.CLIVersions, the rule
version.changed, one worded event`.

## Why this matters

obor drives other programs: the agent CLIs (claude, codex, opencode) and the terminal (herdr). They update
themselves. `docs/observations.md` records the cost under "External CLIs change under us": Codex upgraded itself
mid-day and its unattended starts timed out, and nobody connected the two for a while. The check after an upgrade
(the live conformance tier in obor-kit) is a manual habit. Noticing that a tool changed is exactly what a person
forgets and a daemon does not. After this plan serve reads each tool's version every ten minutes, records a change
as a fact, and sends the lead one line per change.

**The first step of idea A8 is answered** (measured 2026-11-10 on the owner's Mac): every agent CLI prints its
version with `--version`, in 23 ms (claude: `2.1.322 (Claude Code)`), 48 ms (codex: `codex-cli 0.167.0`) and 80 ms
(opencode: `opencode v2.2.0`). So the kind protocol and the manifest need no new field: the program is the kind's
name on `PATH`, the argument is `--version`, the answer is its first line, compared as text. herdr's version is
already in every snapshot serve reads (`port.Snapshot.Version`).

Not the same data as plan 008 (callers' obor versions, from request envelopes). The two plans share no file except
`internal/app/app.go` (different structs).

## Current state

Files and the patterns this plan copies:

- `internal/app/disk.go`: **the model for the periodic read.** `Disk` is a small struct of adapters in `Loop`;
  `diskTick` reads it once `diskEvery` has passed, on the merge loop's pass; `diskIsLow` records one fact per
  transition. Its tests are `internal/app/disk_test.go` (in-package, on `newConcRig`, a scripted reader).
- `internal/app/loop.go:33-59`: `Loop`, the adapters of one pass (built per pass by `Options.Loop`, in production
  by `cmd/obor/serve.go:180-196`). Line 346 calls `a.diskTick(p, fx)` in the pass.
- `internal/app/idle.go:65-83` and `internal/app/event/steps.go:35-36, 143`: **the model for a project-level fact**:
  a task key constant (`IdleTask`), a step constant (`StepIdleAck`), a fold function (`foldIdle`) called from
  `foldRecord` (`internal/app/project.go:99-113`), its state a field of `folded` (`internal/app/app.go:231-265`;
  the maps are made in the literal at line 265).
- `internal/app/rules.go`: **the model for the line.** A rule is `{name, settle, when}`; `when` returns a key (an
  `int64`), an `action{call, line, after}` and whether the condition holds. The pass sends `action.line` to the lead
  once per key (`p.folded.did[event.Of{Kind: name, Seq: key}]`). `handoffRule` (`internal/app/keeplane.go:171`) is
  a small complete rule; `internal/app/rules_test.go` tests rules.
- `internal/app/panes.go:41-48`: serve keeps herdr's version from each snapshot in memory:

  ```go
  	if snap.Protocol != 0 && (snap.Version != a.herdrVer || snap.Protocol != a.herdrProt) {
  		a.logf("herdr: %s protocol %d", snap.Version, snap.Protocol)
  	}
  	if snap.Protocol != 0 {
  		a.herdrVer, a.herdrProt, a.herdrOwn = snap.Version, snap.Protocol, snap.Speaks
  	}
  ```

  `a.herdrVer` is read under `a.mu`; `a.connected` says whether the subscription is live.
- `internal/app/describe.go:49-`: `worded`, the table that words a record for `obor events` and the web page
  (key `"<kind> <step>"`, a `worder` that fills `Described.Line` and `Described.Kind`).
- `internal/launch/launch.go:655-661`: `installHost()` and `project.InstallHost(env)` (`internal/project/files.go:223`):
  how launch finds a kind's CLI on the launcher's `PATH` (`project.LookPath(path, name)`) without running it.
  `agents.StartRefusal(kind, host)` (`internal/agents/agents.go:168`) is why a kind may not start on this machine
  (a quarantined Codex install, T-1052); `""` when it may.
- `internal/project/project.go:164`: `project.Group(cmd)`, which makes a command the leader of its own process
  group (the rule for every process obor starts, `AGENTS.md` "Runtime constraints").
- `internal/config/validate.go:611-625`: `(*Config).UsedModels()`, the models the config's levels and roles name;
  `(*Config).Model(name)` gives a model's `Kind`.

`internal/app/disk.go:26-34`:

```go
func (a *App) diskTick(p *proj, fx *Loop) {
	if fx.Disk == nil {
		return
	}
	now := a.opt.Now()
	if !p.diskAt.IsZero() && now.Sub(p.diskAt) < diskEvery {
		return
	}
	a.diskIsLow(p, fx)
}
```

`internal/app/rules.go:134-150`, how a rule's line reaches the lead (`lineTo`): a `message` fact to the lead with
`Of{Kind: <rule name>, Seq: <key>}`; the fold records the key in `p.folded.did`.

Conventions: one concern per file in `internal/app`, its leading comment saying what it is with its task id;
"under mu" in a function's comment when the caller holds `a.mu`; a derived value that comes back empty is an error
or is left out, never a default (`AGENTS.md` "Helpers"); no production file outside the bridges holds an agent
kind's name (`claude`, `codex`, …) as a string literal (`TestNoKindLiterals`, `internal/checks/boundary_test.go:260`;
test files may); `docs/module-map.md`'s **core** `app` paragraph names
every production file of `internal/app` with a short parenthesis.

## Commands you will need

| Purpose | Command | Expected on success |
|---|---|---|
| Build | `make -s build` | exit 0 |
| The app's new tests | `go test -count=1 -run 'Version' ./internal/app` | `ok  	obor/internal/app` |
| The reader's test | `go test -count=1 -tags integration -run 'CLIVersions' ./internal/launch` | `ok  	obor/internal/launch` |
| Import rules, kind literals | `go test -count=1 -run 'Boundar\|KindLiterals' ./internal/checks` | `ok` |
| Your lane's check | `make -s check-affected` | last line `go test: N pass, 0 fail` |

Run one test command at a time (the guard denies two at once). Never run `make check` yourself. In a table cell
`\|` stands for a plain `|`.

## Scope

**In scope** (the only files you should modify):
- `internal/app/versions.go` (new), `internal/app/versions_test.go` (new)
- `internal/app/event/steps.go` (two constants)
- `internal/app/app.go` (one field of `folded` and its map in the literal; one field of `proj` beside `diskAt`)
- `internal/app/loop.go` (one field of `Loop`; one call beside `diskTick`)
- `internal/app/project.go` (one fold call), `internal/app/rules.go` (one rule in the list)
- `internal/app/describe.go` (one worded record), `internal/app/describe_test.go` (its row)
- `internal/launch/versions.go` (new), `internal/launch/versions_integration_test.go` (new)
- `cmd/obor/serve.go` (the wiring)
- `docs/module-map.md` (two entries: `versions.go` in the `app` paragraph, `CLIVersions` in `launch`'s line)

**Out of scope** (do NOT touch):
- `docs/agents/kind-protocol.md`, `internal/agents`, `kinds/opencode`: no manifest field, no `Kind` method. The
  program is the kind's name.
- A start: `internal/launch/start.go`, `port.Started`, `startOut`. Nothing is read or refused at a start.
- `obor status`: this plan adds no status line. The change reaches the lead as a line, and `obor events` lists it.
- An acknowledgement ("checked") or a refusal of starts on an unchecked version: see Maintenance notes.
- `docs/design.md`, `docs/state-model.md`: the lead's; their sentences are in Maintenance notes.
- `internal/app/panes.go`: herdr's version is read from `a.herdrVer` as it is.

## Git workflow

- If you are an obor worker (your job came through `job_next`): work in your lane, make no commits, finish with
  `task_done`.
- Otherwise: a branch `advisor/006-cli-version-change` cut from `staging`, in a separate git worktree. Commit message
  style: one sentence saying what is true now. Do not push.

## Design in one paragraph

Every `versionsEvery` (10 minutes, and on the first pass after the project loads) the pass asks `fx.Versions` for
the version line of each agent CLI the config uses, adds herdr's from the last snapshot, and compares each with the
last one the log holds. A tool whose version differs gets one fact, `version{what, version, was}`. The fold keeps
the newest fact per tool. A rule, `version.changed`, sends the lead one line for each folded fact that has a `was`
(a first sighting has none and sends nothing). No reading (the tool is not installed, did not answer) is no
version: nothing is recorded and nothing is compared.

## Steps

### Step 1: The fact's names

In `internal/app/event/steps.go`, in the block of project task keys, add:

```go
	// VersionTask is the task key of the versions of the tools obor drives (the agent CLIs, the terminal): the
	// project's facts, one per change (internal/app/versions.go).
	VersionTask = "version"
```

and, beside `StepIdleAck`'s block, `StepVersion = "version" // fact: a tool's version as serve read it (data: versionData)`.

**Verify**: `go build ./internal/app/...` exits 0.

### Step 2: `internal/app/versions.go`: the read, the fact, the fold, the rule

Create the file with a leading comment that says what it is (model: `disk.go`'s) and these parts:

```go
// versionsEvery is how often the tools' versions are read on a pass.
const versionsEvery = 10 * time.Minute

// terminalTool is the terminal's name among the tools (the one terminal adapter, T-1044).
const terminalTool = config.DefaultTerminal

// ruleVersion is the version rule's name: its key in the rule pass.
const ruleVersion = "version.changed"

// versionData is fact version's data: the tool (an agent kind's name, or terminalTool), its version as read, and
// the one the log held before ("": the first sighting).
type versionData struct {
	What    string `json:"what"`
	Version string `json:"version"`
	Was     string `json:"was,omitempty"`
}

// versionAt is a tool's newest version fact as folded: its data and the record's seq.
type versionAt struct {
	versionData
	seq int64
}
```

- `Loop` gets `Versions func(ctx context.Context) map[string]string` (in `loop.go`, after `Disk`): "the version
  line of each agent CLI the config uses, by kind; a kind with no entry was not read. Nil: not watched."
- `proj` gets `versionsAt time.Time` beside `diskAt`; `folded` gets `versions map[string]versionAt` (made in the
  literal at `app.go:265`).
- `func (a *App) versionTick(ctx context.Context, p *proj, fx *Loop)`: returns when `fx.Versions == nil` or
  `versionsEvery` has not passed since `p.versionsAt`; else sets `p.versionsAt`, calls `fx.Versions(ctx)` **outside
  `a.mu`**, then under `a.mu` adds `terminalTool: a.herdrVer` when `a.connected && a.herdrVer != ""` and collects,
  in tool-name order, each tool whose read version is not `""` and differs from `p.folded.versions[what].Version`.
  After unlocking it records one fact per such tool:
  `a.record(p, event.Record{Task: event.VersionTask, Step: event.StepVersion, Kind: event.KindFact}, versionData{…})`
  with `Was` the folded version. A refused append is not retried here: the next read sees the same difference.
- `func (p *proj) foldVersion(r event.Record)`: for a fact of `VersionTask` / `StepVersion` whose data decodes and
  has a `What` and a `Version`, `p.folded.versions[d.What] = versionAt{d, r.Seq}`. Call it from `foldRecord` in
  `project.go`, after `p.foldIdle(r)`.
- `func versionRule(_ context.Context, a *App, p *proj, _ *Loop, _ time.Time) (int64, action, bool)`: under `a.mu`,
  the folded entry with the lowest `seq` that has `Was != ""` and no `p.folded.did[event.Of{Kind: ruleVersion,
  Seq: seq}]`; none → `0, action{}, false`. Its action is a line only:

  ```
  <what> changed version: <version> (was <was>). obor drives it and was not tested with this one: watch its next
  start and stop, and suspect it first if an agent stalls.
  ```

  One line, no newline in it (build it with `fmt.Sprintf`; the two sentences are one string).
  Add `{name: ruleVersion, when: versionRule}` to `rules` in `rules.go`, after `ruleHandoff`.
- In `loop.go`, call `a.versionTick(ctx, p, fx)` on the line after `a.diskTick(p, fx)`.

**Verify**: `make -s build` exits 0.

### Step 3: The record is worded

In `internal/app/describe.go` add to `worded`, key `"fact " + event.StepVersion` (check the key format of the other
fact entries in the table and match it): `Line` is `<what> <version>` for a first sighting and
`<what> <version> (was <was>)` for a change, `Kind` is `KindOther`, `Text` the version. Add its row to the table
test in `internal/app/describe_test.go`.

**Verify**: `go test -count=1 -run 'Describe' ./internal/app` → `ok`.

### Step 4: Tests in `internal/app/versions_test.go`

In-package, on `newConcRig(t)` as `disk_test.go` does (`r.proj("pj")`, `r.concLoop("pj")`, a scripted
`fx.Versions` that returns a map the test changes and counts its calls). The rig's clock: see how `disk_test.go`
and `idle_test.go` move time; use the same means. No socket, no sleep.

| Test | Steps | Expect |
|---|---|---|
| first sighting | `Versions` = `{claude: "2.1.322 (Claude Code)"}`; one `versionTick` | one `version` fact with `Was` `""`; `versionRule` does not hold |
| no change | a second tick after `versionsEvery`, same map | no new fact; the reader was called twice |
| not due | a tick before `versionsEvery` has passed | the reader is not called |
| a change | map → `{claude: "2.1.400 (Claude Code)"}`; tick after `versionsEvery` | a fact with `Was` the old line; `versionRule` holds with that fact's seq and the line names both versions |
| lined once | run the rule pass (as `rules_test.go` does) twice | one message fact to the lead with `Of{version.changed, seq}`; the rule no longer holds |
| not read | map → `{}` (the CLI gone) | no fact; then the old line again → no fact (no flapping through "unknown") |
| the terminal | `a.herdrVer`, `a.connected` set under `a.mu` to `0.9.5` then `0.9.6` across two due ticks, `Versions` returning `{}` | a first-sighting fact for `herdr`, then a change fact; not connected → none |
| replay | write the two facts with `WriteLog` (or `r.add`), reload the project (`r.reload`) | `p.folded.versions` holds the newest; a lined change is not lined again |
| two changes | claude and codex both change in one read | two facts in name order; the rule lines the lower seq first, then the other on the next pass |

**Verify**: `go test -count=1 -run 'Version' ./internal/app` → `ok`.

### Step 5: `internal/launch/versions.go`: the reader

```go
// versionTimeout bounds one CLI's --version (claude 23 ms, codex 48 ms, opencode 80 ms when measured, 2026-11-10).
const versionTimeout = 3 * time.Second

// CLIVersions is how each of kinds' CLI names its own version: the first line of `<kind> --version`, trimmed
// ("2.1.322 (Claude Code)", "codex-cli 0.167.0"), by kind, for the CLIs on env's PATH. A kind has no entry when its
// CLI is not on PATH, may not start on this machine (agents.StartRefusal: a quarantined install is not run), fails,
// prints nothing, or does not answer in versionTimeout: not read is not a version. The line is compared as text,
// never parsed.
func CLIVersions(ctx context.Context, env project.Env, kinds []string) map[string]string
```

Implementation notes: `project.LookPath` on `env`'s `PATH` (as `project.InstallHost` reads it); skip a kind with
`agents.StartRefusal(kind, project.InstallHost(env)) != ""`; `exec.CommandContext` under
`context.WithTimeout(ctx, versionTimeout)`, made a process group with `project.Group(cmd)`; stdin nil; the output
capped (read at most a few hundred bytes of stdout; ignore stderr); the first non-blank line, trimmed, at most 80
characters. The kinds run one after another.

Test, `internal/launch/versions_integration_test.go` (`//go:build integration`: it starts processes): a temp dir as
`PATH` with stub executables written by the test: `claude` prints `2.1.322 (Claude Code)`; `codex` prints two lines
(the first is taken); `slow` sleeps past the timeout (make the timeout a parameter of an unexported function so the
test passes 100 ms, not 3 s); `fails` exits 1; a kind with no file. Expect exactly the entries for `claude` and
`codex`. The stubs are shell scripts with `#!/bin/sh`; no real CLI is run.

**Verify**: `go test -count=1 -tags integration -run 'CLIVersions' ./internal/launch` → `ok`.

### Step 6: The wiring

In `cmd/obor/serve.go`, where the `Loop` is built (line 190-195), set:

```go
		l.Versions = func(ctx context.Context) map[string]string { return launch.CLIVersions(ctx, env, usedKinds(cfg)) }
```

with `usedKinds(cfg *config.Config) []string` beside `diskOf`: the kinds of `cfg.UsedModels()` (each through
`cfg.Model(name)`; a model the config does not know is skipped), unique, sorted. A project that names no codex
model never reads codex's version, so it gets no line about a CLI it does not start.

**Verify**: `make -s build` exits 0; `go test -count=1 -run 'Boundar|KindLiterals' ./internal/checks` → `ok`
(cmd/obor may import launch; `internal/app` must not; no production file of this plan holds a kind's name as a
string literal: the kinds come from the config, `TestNoKindLiterals`).

### Step 7: The map

In `docs/module-map.md`: add `` `versions.go` (the versions of the tools obor drives: the periodic read, fact
`version`, the rule that lines the lead on a change) `` to the **core** `app` paragraph next to `disk.go`, and name
`CLIVersions` in `launch`'s bullet (one clause).

**Verify**: ``grep -c '`versions.go`' docs/module-map.md`` → `1`.

### Step 8: Your lane's check

**Verify**: `make -s check-affected` → last line `go test: N pass, 0 fail`.

Do not run a real agent CLI from your lane to try the reader: the stub test is its proof. The lead sees the real
thing after the merge: within one pass of serve's restart `obor events` lists a first-sighting line per tool
(`claude 2.1.322 (Claude Code)`, `herdr 0.9.5`, …).

## Test plan

- Added: `internal/app/versions_test.go` (nine rows above, decisions on fakes), one `describe_test.go` row,
  `internal/launch/versions_integration_test.go` (the one exec behaviour: PATH lookup, first line, timeout, failure).
- Changed: none. Deleted: none.
- Not needed: the live conformance tier. This plan does not touch agent identity, the MCP server or how an agent is
  launched (`AGENTS.md` "Tests", last rule): it runs `--version` beside the launcher and changes no start.

## Done criteria

- [ ] `go test -count=1 -run 'Version' ./internal/app` passes
- [ ] `go test -count=1 -tags integration -run 'CLIVersions' ./internal/launch` passes
- [ ] `go test -count=1 -run 'Boundar|KindLiterals' ./internal/checks` passes
- [ ] `grep -n 'ruleVersion' internal/app/rules.go` shows the rule in the list
- [ ] `grep -rn 'version_var\|VersionVar' internal/app internal/launch/versions.go` prints nothing (no kind field used)
- [ ] `make -s check-affected` ends `0 fail`
- [ ] `git status --short` lists only in-scope files

## STOP conditions

Stop and report back (do not improvise) if:

- A record with a task key the merge loop does not know (`version`) is not ignored by `foldLoop`, the prune
  (`internal/app/prune.go`, `pruneCandidates` ranges over `p.folded.loop` only) or the view (`internal/view`): run
  the app's and the view's tests after step 2; a failure that names the new task key is this condition. `IdleTask`
  is the precedent that it works.
- The store has a schema or a check that lists the allowed task keys or steps (a new fact would need a migration).
- `fx.Versions` would have to be called under `a.mu` to make a test pass.
- The rule pass cannot line two pending changes over two passes with one rule (its `ruleLive` keeps one key): then
  line only the newest and say so in the report.
- A real CLI is needed to make a test pass. Tests use stubs only.

## Maintenance notes

- **The read runs on the merge loop's pass.** Normally 150 ms every ten minutes. If a CLI hangs on `--version`, the
  pass waits up to `versionTimeout` per kind. If that shows in `serve.log` as slow passes, move the read to its own
  goroutine that hands its map to the next pass; do not shorten the timeout below a second (a cold `node`-based CLI
  takes that long).
- **A change is noticed at most ten minutes late and before any start is refused or delayed.** The line says "watch
  its next start". Making serve hold starts of a kind until someone checked it needs an acknowledgement command and
  a state; decide that only after the line has fired a few times and the owner wants more than a reminder.
- **What the lead does with the line in obor-kit**: run `make conformance TIER=live TURN=1 KIND=<kind>` (for herdr:
  the H-C rows). That sentence belongs in `docs/agents/lead.md` (project text), not in the product's line: an
  adopter has no conformance target.
- For the lead: `docs/state-model.md` gets a row for fact `version` (project-level, class F, no intent);
  `docs/design.md`'s rule list ("Change rules" 1, the rule tier) gets `version.changed`; `skills/lead/SKILL.md` may
  say what the line means.
- The fact count is small (one per tool per upgrade). The prune never deletes them (its candidates are tasks of the
  merge loop); if that ever matters, keep the newest per tool.
- `herdrNote` (a herdr with a newer protocol, in `obor status`) stays: it is about the protocol serve speaks, this
  is about any change.
- Idea A6 (`obor stats`) can count `version` facts against stalls and failed starts per week: the data for whether
  CLI upgrades are a real cost.
- A plugin kind whose program is not its kind's name gets no reading (and so no line). If such a kind is ever
  written, the manifest's `stop.program` is where its program's name already is.
