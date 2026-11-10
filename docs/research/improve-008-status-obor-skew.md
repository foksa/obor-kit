# Plan 008: `obor status` names the agents whose calls come from another obor release

> **Executor instructions**: Follow this plan step by step. Run every verification command and confirm the expected
> result before moving to the next step. If anything in the "STOP conditions" section occurs, stop and report; do not
> improvise. When done, update the status row for this plan in `docs/research/improve-plans.md`, unless the lead
> dispatched you and keeps the index.
>
> **Drift check (run first)**: `git diff --stat 65389c8c..HEAD -- internal/app/status.go internal/app/app.go internal/app/wire/status.go internal/view/view.go cmd/obor/status.go internal/version`
> If any of these changed since this plan was written, compare the "Current state" excerpts against the live code
> before proceeding; on a mismatch, treat it as a STOP condition.

## Status

- **Priority**: P2
- **Effort**: S
- **Risk**: LOW
- **Depends on**: none
- **Category**: observability
- **Planned at**: commit `65389c8c`, 2026-11-10 (idea A5 of `docs/research/improve-architecture.md`)

Spec lines, if the lead turns this into an obor spec: `Difficulty: medium` · `Guarantee: none (a status line; it
refuses nothing)` · `Replaces: none (serve.log's first-mismatch line stays)` · `Adds: version.Release; wire.Status.Skew
and view.Serve.Skew; one line of obor status`.

## Why this matters

An agent's hooks and its MCP server are processes of the obor that was installed when its session started. After a
kit upgrade a pane that was not restarted keeps calling serve from the old release, and the old hooks keep guarding
it. `BACKLOG.md` records that this hid guard bugs twice ("stale panes"), and asks for an `obor doctor` that lists
the kit version per pane. Serve already receives the caller's obor version in every request and writes the first
mismatch per agent to `serve.log` only, where nobody looks. After this plan `obor status` has one line that names
each agent still on another release, until that agent starts a new session.

Not the same data as plan 006 (agent CLI and terminal versions): this plan reads the callers' **obor** version from
request envelopes, which serve holds in memory; plan 006 reads the CLIs' own versions, which serve does not hold
today. The two share no file except `internal/app/app.go`, where each adds one field to a different struct.

## Current state

Files:

- `internal/app/status.go`: `Listen` (reads each request's envelope and calls `noteSkew`), `versionSkew`, `noteSkew`,
  `herdrNote`, and `Status()`.
- `internal/app/app.go:98`: the `skews` field.
- `internal/app/wire/status.go`: `wire.Status`, serve's answer to `status`.
- `internal/view/view.go:74-90, 247`: `view.Serve` and `serveOf`, which copies serve's answer into the view.
- `cmd/obor/status.go:453-486`: `writeServe`, the `serve:` lines of `obor status`.
- `internal/version/version.go`: `version.Version` and its formats.

`internal/version/version.go:11-14`:

```go
// Version is the build version, stamped by the build (the Makefile): the tag (v0.14.0) when HEAD is exactly at one, else
// the newest release tag plus -dev+<short sha> (v0.14.0-dev+21242f62, -dirty when the tree is modified) — release tags
// sit on release commits off main (T-559). A plain `go build` keeps "dev".
var Version = "dev"
```

`internal/app/status.go`, in `Listen` (line 353-362) and the two functions (lines 470-502):

```go
			var env *wire.Envelope // the caller, read first from any request that carries a call
			...
				} else {
					a.noteSkew(*env)
				}
```

```go
// versionSkew is c's caller's obor as text when it differs from this serve's (T-826): its version, or "an older obor"
// for a call that carried none (a binary before the field); "" when they match. A difference alone refuses nothing:
// obor-kit's dev builds differ after every merge, so only a refusal names it and serve's log says it once.
func versionSkew(c wire.Envelope) string { ... }

// noteSkew writes serve's one log line for a call whose caller runs another obor: the first per (agent, version), not
// each call (T-826).
func (a *App) noteSkew(c wire.Envelope) {
	skew := versionSkew(c)
	if skew == "" {
		return
	}
	agent := callerOf(c)
	key := agent + "\x00" + c.Version
	a.mu.Lock()
	if a.skews == nil {
		a.skews = map[string]bool{}
	}
	seen := a.skews[key]
	a.skews[key] = true
	a.mu.Unlock()
	if !seen {
		a.logf("caller %s runs %s, serve %s", agent, skew, version.Version)
	}
}
```

`Status()` (line 22-26) builds the answer under `a.mu`; the agents serve observes in panes are listed in a loop over
`a.obs`, each named `a.shownAs(p, o.pane)` (lines 40-58).

The pattern this plan copies is `HerdrNote`: serve builds the text (`herdrNote()`, line 507), `wire.Status.HerdrNote`
carries it, `view.Serve.HerdrNote` copies it (`view.go:247`), `writeServe` prints it. The rule behind the pattern
(`docs/decisions.md` 2026-11-05): serve ships its predicates; a reader prints, it does not decide.

Test helpers that exist (use them, write no new rig): `newHarness(t)` (project `pj`, lane `L-01` with pane `w1:p1`,
an idle claude labelled `pj-l-01`; `h.connect()` loads the project and feeds the snapshot), `askApp(t, a)` (a
function that sends one request to the app through an in-memory listener and returns the `wire.Answer`), both in
`internal/app` (`rig_test.go`, `status_test.go:407`). `TestSkewLoggedOncePerAgentAndVersion`
(`status_test.go:470`) shows how a call with another version is sent.

## Commands you will need

| Purpose | Command | Expected on success |
|---|---|---|
| Build | `make -s build` | exit 0 |
| The new and nearby tests | `go test -count=1 -run 'Skew\|Release\|ServeLine' ./internal/app ./internal/version ./cmd/obor` | three `ok` lines |
| Your lane's check | `make -s check-affected` | last line `go test: N pass, 0 fail` |

Run one test command at a time. Never run `make check` yourself. In a table cell `\|` stands for a plain `|`.

## Scope

**In scope** (the only files you should modify):
- `internal/version/version.go`, `internal/version/version_test.go`
- `internal/app/app.go` (one field beside `skews`)
- `internal/app/status.go`, `internal/app/status_test.go`
- `internal/app/wire/status.go`
- `internal/view/view.go`
- `cmd/obor/status.go`, `cmd/obor/status_test.go`

**Out of scope** (do NOT touch):
- `versionSkew`, the refusal's suffix and the `serve.log` line (T-826): they compare exact versions and stay.
- `internal/web`, `web/src`: the field reaches the page's JSON through `view.Serve`; showing it there is a later
  task.
- `docs/design.md` (protected), `skills/lead/*.md`: sentences for the lead are in Maintenance notes.
- Any refusal: a call from another release is answered as today.

## Git workflow

- If you are an obor worker (your job came through `job_next`): work in your lane, make no commits, finish with
  `task_done`.
- Otherwise: a branch `advisor/008-status-obor-skew` cut from `staging`, in a separate git worktree. Commit message
  style: one sentence saying what is true now. Do not push.

## Steps

### Step 1: `version.Release`

In `internal/version/version.go` add:

```go
// Release is the release a build belongs to: its version less the dev suffix ("v0.34.0-dev+21242f62-dirty" is
// "v0.34.0"). A version without one (a tag, a plain "dev", a bare sha) is its own release. Two builds of one release
// are not a skew worth showing: obor-kit's own dev builds differ after every merge (T-826).
func Release(v string) string {
	if i := strings.Index(v, "-dev+"); i > 0 {
		return v[:i]
	}
	return v
}
```

In `internal/version/version_test.go` add `TestRelease`, a table: `v0.34.0` → `v0.34.0`; `v0.34.0-dev+21242f62` →
`v0.34.0`; `v0.34.0-dev+21242f62-dirty` → `v0.34.0`; `dev` → `dev`; `abc1234` → `abc1234`; `""` → `""`.

**Verify**: `go test -count=1 -run 'Release' ./internal/version` → `ok`.

### Step 2: Serve keeps, per agent, the other release its calls carry

In `internal/app/app.go`, under the `skews` field, add:

```go
	// behind is, by agent, the obor version its calls carry when that is another release than serve's (version.Release):
	// Status's skew line. An agent's entry goes when it reports a new session (status.go, noteSkew).
	behind map[string]string
```

In `internal/app/status.go`:

1. `noteSkew` takes the method too: `func (a *App) noteSkew(method string, c wire.Envelope)`; `Listen` passes
   `req.Method`.
2. Before the existing exact-version logic (which must keep its behaviour, including its early return), add:

   ```go
   	if c.Agent != "" { // the owner's own CLI is not an agent: nothing to restart
   		other := version.Release(c.Version) != version.Release(version.Version)
   		a.mu.Lock()
   		if method == wire.MethodSession {
   			delete(a.behind, c.Agent) // a new session: what the last one ran is gone
   		}
   		if other {
   			if a.behind == nil {
   				a.behind = map[string]string{}
   			}
   			a.behind[c.Agent] = c.Version
   		}
   		a.mu.Unlock()
   	}
   ```

   An entry is only set here, never cleared by a later call of the same release: one agent calls through several
   processes (its MCP server and hooks from its session's start, a typed `obor` command from today's install), and
   the line must not flicker between them. It goes when the agent's next session reports itself.
3. Add `skewNote`, beside `herdrNote`:

   ```go
   // skewNote is the status note for the agents in a pane whose calls carry another obor release than serve's (behind):
   // "pj-l-01 runs obor v0.33.0, pj-rev runs an older obor (serve v0.34.0)", agents in name order; "" when none. seen is
   // the agents serve observes in a pane now: one that left is not named. The caller holds a.mu.
   func (a *App) skewNote(seen map[string]bool) string
   ```

   Build each part with the existing `versionSkew(wire.Envelope{Version: v})` so the wording ("obor v0.33.0", "an
   older obor") has one owner.
4. In `Status()`, collect the names of the observed agents in the existing loop over `a.obs` (the `Name` each
   `wire.AgentStatus` gets) and set `st.Skew = a.skewNote(names)` once the project's part is built. With no project
   loaded the note stays empty.

In `internal/app/wire/status.go` add to `Status`, after `HerdrNote`:

```go
	// Skew names the agents whose calls carry another obor release than serve's, with both versions ("": none; T-826's
	// versions, compared by release). A reader prints it.
	Skew string `json:"skew,omitempty"`
```

**Verify**: `make -s build` exits 0.

### Step 3: Tests in `internal/app/status_test.go`

Add `TestStatusNamesAgentsOnAnotherRelease`, built on `newHarness(t)` + `h.connect()` + `askApp(t, h.a)`. Every call
below is a `wire.JobNextReq` with `Root: h.root`, `Role: "worker"`, its own `CallID`, unless it says otherwise. The
test's precondition: `version.Release(version.Version) != "v0.1.0"`.

| Step | Call | `h.a.Status().Skew` after it |
|---|---|---|
| 1 | none | `""` |
| 2 | `pj-l-01` at `version.Version` | `""` |
| 3 | `pj-l-01` at `v0.1.0` | `pj-l-01 runs obor v0.1.0 (serve <version.Version>)` |
| 4 | `pj-l-01` at `version.Version` again | unchanged (no flicker) |
| 5 | `pj-l-09` (in no pane) at `v0.1.0` | unchanged (not named) |
| 6 | a `wire.SessionRequest(h.root, store.Session{Agent: "pj-l-01", ID: "s2"})` with `Version: version.Version` | `""` |
| 7 | `pj-l-01` with no version | `pj-l-01 runs an older obor (serve <version.Version>)` |
| 8 | a call with `Agent: ""` at `v0.1.0` | unchanged (the owner is not named) |

The session call of step 6 may be refused by its handler (the harness has no such session to admit): that is fine,
the note is taken before the handler runs; assert on `Skew` only.

Add one row-table test of the release compare if `skewNote`'s dev-suffix case is not reachable with the test
binary's version (`"dev"`): a caller at `dev` is the same release, a caller at `v0.1.0-dev+abc` and one at `v0.1.0`
give the same note text apart from the version shown.

`TestSkewLoggedOncePerAgentAndVersion` and `TestRefusalNamesVersionSkew` must pass unchanged.

**Verify**: `go test -count=1 -run 'Skew|Release' ./internal/app` → `ok`.

### Step 4: The view and the status line

- `internal/view/view.go`: add `Skew string `json:"skew,omitempty"`` to `Serve` after `HerdrNote`, with a comment
  like `HerdrNote`'s; in `serveOf` (line 247) copy `ss.Skew` in the same statement style.
- `cmd/obor/status.go`, `writeServe`: after the `serve:` line is printed (and before the `event log broken` line),
  print when `s.Skew != ""`:

  ```go
  		fmt.Fprintf(w, "serve: another obor release: %s; restart them (obor restart <agent>)\n", s.Skew)
  ```

- `cmd/obor/status_test.go`: add `TestServeLineSkew`: `writeStatus` of a `view.Project{Serve: view.Serve{Up: true,
  Herdr: true, Project: true, Merging: true, Skew: "pj-l-01 runs obor v0.1.0 (serve v0.2.0)"}}` has the line
  `serve: another obor release: pj-l-01 runs obor v0.1.0 (serve v0.2.0); restart them (obor restart <agent>)` as its
  second line; with `Skew: ""` no line starts with `serve: another obor release`.

**Verify**: `go test -count=1 -run 'ServeLine|Status' ./cmd/obor` → `ok`.

### Step 5: Your lane's check

**Verify**: `make -s check-affected` → last line `go test: N pass, 0 fail`.

## Test plan

- Added: `TestRelease` (pure, `internal/version`); `TestStatusNamesAgentsOnAnotherRelease` (in-package, the
  existing harness and in-memory listener, no socket, no sleep); `TestServeLineSkew` (pure rendering).
- Changed: none. Deleted: none.
- No integration test: nothing here runs git or a built binary.

## Done criteria

- [ ] `go test -count=1 -run 'Release' ./internal/version` passes
- [ ] `go test -count=1 -run 'Skew' ./internal/app` passes, the two existing skew tests included
- [ ] `go test -count=1 -run 'ServeLine' ./cmd/obor` passes
- [ ] `grep -n 'Skew' internal/app/wire/status.go internal/view/view.go cmd/obor/status.go` shows one field in each of
      the first two and one print in the third
- [ ] `make -s check-affected` ends `0 fail`
- [ ] `git status --short` lists only in-scope files

## STOP conditions

Stop and report back (do not improvise) if:

- `noteSkew` is called from anywhere but `Listen`, or `Listen` no longer has the method in `req.Method` at that
  point.
- A session request does not carry the session's agent in its envelope (`wire.SessionRequest`,
  `internal/app/wire/call.go:75`, sets `Envelope.Agent` at planning time): the clearing rule of step 2 depends on it.
- `obor status --json`'s consumers have a test with a fixed list of `serve` keys that the new key breaks.
- An existing test asserts that `obor status`'s second line is always `running: …` for a serve that is up (the new
  line goes between them only when there is a skew; a test that sets no `Skew` must not notice).

## Maintenance notes

- What the line can and cannot say: it is the version of the binary that made a call. A Claude pane's hooks and MCP
  server come from the plugin version its session loaded; a typed `obor …` command comes from `PATH`. An agent with
  no skewed call since its session began is not named, even if one of its processes is old and silent.
- A restart does not help when the pane's CLI still loads an old plugin (a stale plugin cache): the agent is named
  again after its first call. That is the signal to update the plugin or kit, not to restart again.
- In obor-kit, sessions that span a release tag are named until restarted (`v0.33.0-dev+…` callers under a
  `v0.34.0` serve). That is correct, and the cheapest reminder to restart the long-lived roles after a release.
- For the lead: `docs/design.md`'s status section (search `herdr_note` or "newer than obor's") is where the skew
  line belongs; `skills/lead/commands.md`'s `obor status` entry may name the line.
- If idea A3 is taken (obor-kit's serve follows the nightly's build), serve and its clients differ by release only
  around a tag, so this line keeps its meaning; the exact-version log line (T-826) would then fire for every agent.
- Showing the line on `obor web` is one field on the page (`web/src/lib/types.ts` has `herdr_note` as the model).
