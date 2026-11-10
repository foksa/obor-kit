# Plan 005: `internal/app`'s rig tests run in `testing/synctest` bubbles on in-memory connections

> **Executor instructions**: This plan has eleven pieces (P0 to P10). Your spec names the one piece you do. Read
> "Why", "Current state", "How a converted test looks" and your piece; follow your piece step by step, run every
> verification command, and honour the STOP conditions (the common ones and your piece's). Do not start another
> piece. When done, update your piece's row in this file's "Pieces" table only if nobody dispatched you; a lead that
> dispatched you keeps the table.
>
> **Drift check (run first)**: `git diff --stat 65389c8c..HEAD -- internal/app/rig_ext_test.go internal/app/rig_test.go internal/herdr/socket.go internal/herdr/panes.go internal/herdr/herdrtest/socket.go`
> P0: if any of these changed, compare the "Current state" excerpts against the live code; on a mismatch, STOP.
> P1 to P10: these files **must** have changed (P0 changed them); your drift check is that P0's row below says DONE
> and `grep -n 'func bubble' internal/app/*_ext_test.go` finds the helper.

## Status

- **Priority**: P1
- **Effort**: L in total: P0 is M (one hard task), P1 to P9 are S-M each and run in parallel, P10 is S
- **Risk**: MEDIUM for P0 (it is the proof), LOW for the rest (test files only)
- **Depends on**: none (plan 001's race line is the other half of idea A1's measurement, not a prerequisite)
- **Category**: tests
- **Planned at**: commit `65389c8c`, 2026-11-10 (idea A1, step 2, of `docs/research/improve-architecture.md`)

Spec lines per piece are in the "Pieces" table.

## Why this matters

The tests that fail under load are one family. The flake log (`.obor/state/flakes.jsonl`) held 221 entries for the
eight days 11-03 to 11-10. 159 of them name a test that still exists; **82 of those are `internal/app` tests, and
81 of the 82 are in its `_ext_test.go` files**, the ones that run the real `App.Serve` against a fake herdr on a
unix socket and poll for results on wall-clock time. The same package's in-package tests, which call the core
directly on a clock they move (`newHarness`, `newConcRig`), have one entry. `docs/observations.md` already says
flaky reds track machine load; this is where and why: a wait of real seconds expires when four lanes test at once.

`testing/synctest` (in the standard library; the toolchain is Go 1.27) runs a test in a "bubble" with a fake
clock that only advances when every goroutine of the bubble is blocked on something inside the bubble. A five-second
wait then cannot expire because the machine is busy: time stands still while anything is running. The 11-05 pilot
(T-876, `docs/decisions.md`) stopped because a goroutine blocked on a unix socket does not count as blocked inside
the bubble, so the clock stalls. This plan removes the sockets from these tests instead of working around them:
both of the rig's connections become in-memory pipes (`net.Pipe`), which a bubble can wait on.

After the plan: the rig tests run in bubbles, no wait in them is on wall-clock time, and the few tests that are
about a real socket (the wire client's retries, herdr's restart read from its socket file) are tagged
`integration`, as `AGENTS.md` already asks.

**What this plan measures** (P10 reports it): `internal/app`'s flake-log entries per week, before (82 in 8 days)
and after; its wall time in `tests/budget.json` (27.5 s at planning time).

## One decision for the lead before P0 is dispatched

**P0 adds one field to production code: `herdr.Socket.Conn`**, an optional function that opens a connection in
place of dialing the unix path (about 15 lines in `internal/herdr/socket.go` and `panes.go`; production leaves it
nil). The architecture note said "no production change is expected"; reading the rig showed why one is the cheaper
path. The alternative without it is a fake at the port level (`port.Panes` implemented over `herdrtest.Fake`, no
herdr client in the test): every test's herdr scripting (64 `srv.` calls, the rig's own `Launch`/`Stop`) would be
rewritten, and the tests would stop covering the herdr adapter together with the core, which is what "the app at
its boundary" tests are for. With the seam the tests keep their text and their coverage; only the transport
changes. The 11-05 decision declined a dialer seam in `wire` "for ~8.5 s of test time"; this one is in `herdr`, the
app socket needs none (`App.Listen` already takes a `net.Listener`), and its reason is the flake count, not seconds.
If the lead or owner does not want the field, do not dispatch P0: the port-level fake needs a different plan.

## Current state

The two rigs of the `_ext_test.go` files, both in `internal/app/rig_ext_test.go` (3,352 lines, package `app_test`):

- `rig` (line 156, `newRig` line 178): a project with two lanes, the app observing only. `start()` (line 233)
  builds the herdr client and runs `App.Serve`:

  ```go
  	panes := herdr.Panes{S: r.srv.Client(), PS: func(context.Context) ([]byte, error) { ... }}
  	...
  	r.app = app.New(app.Options{Root: r.root, Tick: 2 * time.Millisecond, Retry: 5 * time.Millisecond,
  		Now: r.clock.Now, Out: r.out, Refresh: ..., Loop: r.loop, Restore: r.restore}, panes, r.lanes)
  ```

  `r.srv` is `herdrtest.Serve(t, r.f)` (line 198): the fake herdr behind a unix socket in a temp dir.
- `loopRig` (line 1581, `newLoopRig` line 1660): a project with the merge loop, a `FakeWorkflow`, a fake herdr.
  `start()` (line 1823) is `launch()` + `firstCheck()` + `r.sock = (&rig{t: r.t, app: r.app}).listen()`.
  `listen()` (line 3174) is `net.Listen("unix", …)` and `go r.app.Listen(ln)`.
- Requests reach the app through `wire.Verdict(context.Background(), r.sock, &wire.XReq{…})`: 27 call sites in
  `rig_ext_test.go` (the rig's helpers: `taskDone`, `answer`, `pause`, `tellLead`, …) and 65 in the other
  `_ext_test.go` files.
- Waits: `rig.waitFor` (line 277) polls every millisecond for 5 s of wall-clock time; `loopRig.waitFor`
  (line 1913) polls every 5 ms for `patience()` (line 1902), which reads `t.Deadline()`. The rig's `Launch` and
  `Stop` (lines 1741-1800) poll the app's status for 5 s. The watchdogs' time is the rig's own `clock` (line 100),
  moved by the test; only the polling is real time.
- The fake herdr: `internal/herdr/herdrtest/socket.go` (`Server`, `Serve` at line 124, `listen` at line 147,
  `Client()` at line 204: `&herdr.Socket{Path: s.Path, Timeout: 5 * time.Second}`), newline JSON on a unix socket.
- The herdr client: `internal/herdr/socket.go:141-144`:

  ```go
  type Socket struct {
  	Path    string
  	Timeout time.Duration // per request (default 10 s); slow requests take their own bound
  }
  ```

  `dial` (line 193) is its one place that opens a connection (called at lines 216 and 819); `Dial(ctx, path)`
  (line 147) builds a `Socket` from a path and pings; `Panes.Subscribe` (`internal/herdr/panes.go:43`) calls
  `Dial(ctx, p.S.Path)` and then copies `p.S.Timeout`; `Panes.started` (`panes.go:107`) reads the socket file's
  modification time (zero when there is no file: "not known").
- In-memory pieces that exist: `memListener` in `internal/app/rig_test.go:3547` (package `app`: a `net.Listener`
  over `net.Pipe`, with `send` and `read`), used by `askApp` (`status_test.go:407`).
- The one test already in a bubble: `testTurnFailsAtItsDeadline` (`internal/app/stopped_ext_test.go:139`).

What `synctest.Test` allows (Go 1.27, `testing/synctest`): `t.Cleanup` functions run inside the bubble; **`t.Run`,
`t.Parallel` and `t.Deadline` must not be called inside it**; every goroutine started inside must have ended when
the bubble's function and its cleanups return; blocking on a channel made in the bubble, `time.Sleep`,
`sync.Cond.Wait` and `sync.WaitGroup.Wait` count as blocked, while a mutex, a system call and network I/O do not
(a goroutine in a file read or a SQLite call is running, which is fine: time simply does not advance meanwhile).

Per-file numbers at planning time (`internal/app/*_ext_test.go`; "flakes" is the file's entries in the flake log):

| File | Lines | Tests | Direct `wire.Verdict` | `t.Run` | Flakes |
|---|---|---|---|---|---|
| `loop_ext_test.go` | 1506 | 27 | 1 | 9 | 11 |
| `dispatch_ext_test.go` | 945 | 19 | 5 | 20 | 2 |
| `fetch_ext_test.go` | 782 | 13 | 8 | 3 | 3 |
| `messages_ext_test.go` | 775 | 20 | 0 | 5 | 4 |
| `drive_ext_test.go` | 677 | 18 | 4 | 3 | 8 |
| `waiting_ext_test.go` | 587 | 16 | 9 | 0 | 9 |
| `stopped_ext_test.go` | 577 | 9 | 4 | 3 | 1 |
| `jobs_ext_test.go` | 502 | 8 | 7 | 15 | 7 |
| `nightly_ext_test.go` | 462 | 10 | 1 | 2 | 2 |
| `specs_ext_test.go` | 428 | 14 | 7 | 1 | 3 |
| `verdict_ext_test.go` | 423 | 9 | 10 | 1 | 0 |
| `steps_ext_test.go` | 383 | 7 | 1 | 1 | 0 |
| `keeplane_ext_test.go` | 369 | 6 | 0 | 2 | 1 |
| `stopwake_ext_test.go` | 362 | 9 | 0 | 4 | 0 |
| `mergewatch_ext_test.go` | 340 | 10 | 2 | 0 | 1 |
| `follow_ext_test.go` | 321 | 8 | 0 | 0 | 1 |
| `restore_ext_test.go` | 321 | 7 | 0 | 1 | 0 |
| `watchdog_ext_test.go` | 317 | 10 | 0 | 1 | 7 |
| `prompts_ext_test.go` | 282 | 8 | 0 | 0 | 2 |
| `move_ext_test.go` | 261 | 7 | 0 | 1 | 3 |
| `resume_ext_test.go` | 231 | 3 | 3 | 7 | 3 |
| the 18 files under 200 lines | 2,074 | 45 | 3 | 11 | 13 |

## How a converted test looks

Before:

```go
func TestWorkerStartRule(t *testing.T) {
	t.Parallel()
	t.Run("a worker that exits while working: …", func(t *testing.T) {
		t.Parallel()
		r := workingRig(t)
		r.workerExits()
		…
		if _, err := wire.Verdict(context.Background(), r.sock, &wire.PauseReq{…}); err != nil { … }
	})
}
```

After:

```go
func TestWorkerStartRule(t *testing.T) {
	t.Parallel()
	t.Run("a worker that exits while working: …", func(t *testing.T) {
		t.Parallel()
		bubble(t, func(t *testing.T) {
			r := workingRig(t)
			r.workerExits()
			…
			if _, err := r.verdict(&wire.PauseReq{…}); err != nil { … }
		})
	})
}
```

The rules of a conversion (P1 to P9 apply exactly these; P0 writes the helpers they name):

1. The whole body of a test, or of a subtest that builds its own rig, goes inside `bubble(t, func(t *testing.T) { … })`.
   `t.Parallel()` stays outside, before it. The rig is built inside: a rig built in a bubble uses in-memory
   connections, one built outside uses the unix sockets as before.
2. `wire.Verdict(context.Background(), r.sock, X)` becomes `r.verdict(X)`. Nothing else may read `r.sock`.
3. No `t.Run`, `t.Parallel` or `t.Deadline` inside a bubble. A subtest with its own rig keeps its `t.Run` and gets
   its own bubble inside it. Subtests that share one rig with their parent become plain blocks in the parent's
   bubble, each starting with `t.Log("<the subtest's name>")`; their order and asserts stay.
4. `time.Sleep`, `time.Now`, `time.After` in the test stay as they are: inside the bubble they are fake time. Do not
   lengthen or shorten a wait to make a test pass.
5. Asserts, names and comments of the test do not change. A test is converted, not rewritten: the diff of a test
   is its indentation, rule 2's replacements and rule 3's blocks.
6. A test that cannot run in a bubble by its nature (rule out below) is left exactly as it is and listed in your
   report with the reason.

What cannot run in a bubble, and stays on the sockets: a test of the wire client itself (it calls `serveStoring`,
`sockDir`, `wire.Ask` or `wire.Feed` on a socket path, or asserts on `wire.Verdict`'s retries or deadline text); a
test that restarts herdr by its socket file (`srv.Restart`: the instance is the file's modification time); a test
that starts a process.

## Commands you will need

| Purpose | Command | Expected on success |
|---|---|---|
| Build the tests without running | `go vet -tags integration ./internal/app ./internal/herdr/...` | exit 0 |
| One file's tests | `go test -count=1 -tags integration -run '<Test names of the file, joined with \|>' ./internal/app` | `ok  	obor/internal/app` |
| One test, verbose (its time) | `go test -count=1 -tags integration -v -run '^TestWorkerStartRule$' ./internal/app` | `--- PASS` lines with times |
| The package | `go test -count=1 -tags integration ./internal/app` | `ok  	obor/internal/app` |
| Your lane's check | `make -s check-affected` | last line `go test: N pass, 0 fail` |
| Tests of a file | `grep -n '^func Test' internal/app/<file>` | the list for `-run` |

Run one test command at a time: the guard denies two at once, `-count` above 1 and `-race`. Never run `make check`.
In a table cell `\|` stands for a plain `|`.

## Pieces

Each piece owns the files in its row and no other. P1 to P9 touch **no** rig file (`rig_ext_test.go`,
`rig_test.go`), nothing in `internal/herdr`, and no production file: they can all run at once.

| Piece | Owns | Tests | Depends on | Difficulty | Status |
|---|---|---|---|---|---|
| **P0 pilot** | `internal/herdr/socket.go`, `internal/herdr/panes.go`, `internal/herdr/herdrtest/socket.go`, `internal/herdr/socket_test.go` (one test added), `internal/app/rig_ext_test.go`, `internal/app/rig_test.go`, `internal/app/workerstart_ext_test.go`, `internal/app/watchdog_ext_test.go`, and in `internal/app/jobs_ext_test.go` the function `TestHoldAndResume` only | 2 + 10 + 1 | none | hard | TODO |
| P1 loop | `loop_ext_test.go` | 27 | P0 | medium | TODO |
| P2 dispatch | `dispatch_ext_test.go`, `lanepick_ext_test.go`, `keeplane_ext_test.go` | 28 | P0 | medium | TODO |
| P3 waiting | `waiting_ext_test.go`, `move_ext_test.go`, `pause_ext_test.go`, `stopstep_ext_test.go` | 30 | P0 | medium | TODO |
| P4 drive | `drive_ext_test.go`, `resume_ext_test.go`, `rolestop_ext_test.go`, `exit_ext_test.go` | 23 | P0 | medium | TODO |
| P5 jobs | `jobs_ext_test.go` (all but `TestHoldAndResume`), `prompts_ext_test.go`, `prompt_ext_test.go`, `worktext_ext_test.go` | 19 | P0 | medium | TODO |
| P6 messages | `messages_ext_test.go`, `idle_ext_test.go`, `rules_ext_test.go`, `mergewatch_ext_test.go` | 38 | P0 | medium | TODO |
| P7 verdicts | `verdict_ext_test.go`, `specs_ext_test.go`, `steps_ext_test.go`, `status_ext_test.go` | 34 | P0 | medium | TODO |
| P8 nightly | `nightly_ext_test.go`, `follow_ext_test.go`, `lanerows_ext_test.go`, `lanescan_ext_test.go`, `disk_ext_test.go`, `reviewclose_ext_test.go` | 25 | P0 | medium | TODO |
| P9 fetch | `fetch_ext_test.go`, `stopwake_ext_test.go`, `stopped_ext_test.go` | 31 | P0 | medium | TODO |
| **P10 close** | `serve_ext_test.go`, `panes_ext_test.go`, `restore_ext_test.go`, `verbs_ext_test.go`, `internal/app/rig_ext_test.go`, `tests/budget.json`, one sentence each in `AGENTS.md` ("Tests") and `docs/module-map.md`; new files `internal/app/*_integration_test.go` for what stays on sockets | 15 | P1 to P9 | medium | TODO |

All paths without a directory are in `internal/app/`. Spec lines for every piece: `Guarantee: none (tests only;
P0: one optional field in the herdr client)` · `Replaces: the rig's wall-clock waits and unix sockets for the
converted tests` · `Adds: P0 only: herdr.Socket.Conn, herdrtest.ServeMem, the rig's bubble mode`.

Overlaps with running tasks at planning time: none with T-1169 (`Makefile`, config, `AGENTS.md`), T-1170
(`internal/web`, merged) or T-1171 (`docs/module-map.md`, merged), except **P10**, which adds one sentence to
`AGENTS.md`'s "Tests" section (T-1169 and T-1172 edit other sentences of that file) and one to
`docs/module-map.md`: P10 runs last, after they merged.

---

## P0: the pilot

Goal: prove that `App.Serve`, the herdr client and the fake herdr run inside a bubble, on three sets of tests
chosen for their flake counts and for covering both rigs and a direct call: `workerstart_ext_test.go` (2 tests on
`loopRig`; `TestWorkerStartRule` has 5 flake entries, the most of any test), `watchdog_ext_test.go` (10 tests on
`rig`; 7 entries), and `TestHoldAndResume` (subtests, direct `wire.Verdict` calls; 4 entries). And leave the rig so
that P1 to P9 need no rig change.

### P0 step 1: the herdr client can be given its connection

In `internal/herdr/socket.go`:

```go
type Socket struct {
	Path    string
	Timeout time.Duration // per request (default 10 s); slow requests take their own bound
	// Conn, when set, opens each connection in place of dialing Path: a herdr served in memory (herdrtest.ServeMem),
	// whose connections a testing/synctest bubble can wait on (T-xx; a unix socket's it cannot). Production leaves it
	// nil.
	Conn func(ctx context.Context) (net.Conn, error)
}
```

- `dial`: when `s.Conn != nil`, return `s.Conn(ctx)`, its error wrapped as the dial's own is
  (`&Error{Kind: Unavailable, Op: op, Msg: err.Error()}`); `CheckSocketPath` is for a path only.
- `Dial(ctx, path)` keeps its signature and becomes `(&Socket{Path: path}).checked(ctx)`, where
  `func (s *Socket) checked(ctx context.Context) (*Socket, error)` is today's body of `Dial` after the `Socket` is
  built (the path check only when `s.Conn == nil`, the ping, `checkProtocol`).
- `internal/herdr/panes.go:43`: `s, err := Dial(ctx, p.S.Path)` becomes
  `s, err := (&Socket{Path: p.S.Path, Conn: p.S.Conn}).checked(ctx)`. The next lines (`s.Timeout = p.S.Timeout`)
  stay.

Replace `T-xx` with your task id. Nothing else in production changes in this plan.

**Verify**: `go vet ./internal/herdr/... ./cmd/... ./internal/...` exits 0; `go test -count=1 -tags integration ./internal/herdr`
→ `ok` (every existing test still dials a path).

### P0 step 2: the fake herdr can serve in memory

In `internal/herdr/herdrtest/socket.go`:

- An unexported `memListener`: a `net.Listener` whose `Accept` receives from a channel and whose
  `dial(ctx) (net.Conn, error)` makes a `net.Pipe`, hands one end to `Accept` and returns the other; after `Close`,
  `dial` fails with `syscall.ECONNREFUSED` (what a dial of a dead herdr's socket gives), and with `ctx.Err()` when
  ctx ends first. (`internal/app/rig_test.go:3547` has the same shape for the app's side; copy it, add `dial`.)
- `func ServeMem(t testing.TB, f *Fake) *Server`: `Serve` without the temp dir and the path check; `s.Path` stays
  `""`. `listen()` opens a `memListener` when `s.Path == ""`. `Client()` returns
  `&herdr.Socket{Conn: s.dialMem, Timeout: 5 * time.Second}` for such a server, where `dialMem` dials the server's
  **current** listener (read under `s.mu`), so `Die()` then `Revive()` works: a dial between them is refused.
- `Die()` must not try to remove a file when `s.Path == ""`. `Restart(t, at)` on such a server is
  `t.Fatal("Restart needs herdr's socket file: this test stays on Serve")`.

Add to `internal/herdr/socket_test.go` one test, `TestSocketOverGivenConn`: a `ServeMem` fake with one agent pane;
its `Client()` answers `Snapshot` with that pane; after `Die()` a call fails with an `*herdr.Error` of kind
`Unavailable`; after `Revive(t)` it answers again. No bubble needed for it.

**Verify**: `go test -count=1 -tags integration -run 'TestSocketOverGivenConn' ./internal/herdr` → `ok`.

### P0 step 3: the app's socket in memory, for `app_test`

In `internal/app/rig_test.go` (package `app`, beside `memListener`), export what the outer rig needs:

```go
// MemServe is a's socket answered in memory: Listen on an in-memory listener until the test ends. Call is one
// request as wire.Verdict sends it (the envelope stamped with this binary's version): the reply, or the app's refusal
// as the error. It has none of the wire client's retries or deadlines: a test of those uses a unix socket.
type MemServe struct{ ln *memListener }

func ServeMem(t *testing.T, a *App) *MemServe
func (s *MemServe) Call(t *testing.T, c wire.Req) (string, error)
```

`Call` is `s.ln.send(t, c)`, `read(t, client)`, then `errors.New(ans.Error)` when the answer has an error, else
`ans.Reply` (the mapping of `askOnce`, `internal/app/wire/client.go:205-209`).

**Verify**: `go vet -tags integration ./internal/app` exits 0.

### P0 step 4: the rig knows whether it is in a bubble

In `internal/app/rig_ext_test.go`:

```go
// bubbles holds the tests running in a bubble (bubble): a rig built by one uses in-memory connections.
var bubbles sync.Map // *testing.T → true

// bubble runs f in a testing/synctest bubble. A rig built inside it has no unix socket: the fake herdr and the app's
// socket are in-memory pipes, so every wait of the test and of the app is on the bubble's clock, which stands still
// while anything runs. Machine load cannot expire such a wait. No t.Run, t.Parallel or t.Deadline inside.
func bubble(t *testing.T, f func(t *testing.T)) {
	t.Helper()
	synctest.Test(t, func(t *testing.T) {
		bubbles.Store(t, true)
		defer bubbles.Delete(t)
		f(t)
	})
}

func inBubble(t *testing.T) bool { _, ok := bubbles.Load(t); return ok }
```

Then make both rigs follow it. Every change below is in `rig_ext_test.go`:

1. `newRig` and `newLoopRig`: `r.srv = herdrtest.ServeMem(t, r.f)` when `inBubble(t)`, else `herdrtest.Serve` as
   today. (Any other place of the file that calls `herdrtest.Serve` for a rig: the same.)
2. The app's socket: `loopRig` gets a field `mem *app.MemServe`. `loopRig.start()` sets
   `r.mem = app.ServeMem(r.t, r.app)` in a bubble and `r.sock = …listen()` outside it. Give the rig one predicate
   for "the socket answers" and use it where `r.sock == ""` is read today (`submit`, line 1937).
3. `func (r *loopRig) verdict(c wire.Req) (string, error)`: `r.mem.Call(r.t, c)` in a bubble,
   `wire.Verdict(context.Background(), r.sock, c)` outside. The same method on `rig` if a test of `rig` sends
   requests (`rig.listen()` callers: `grep -n 'listen()' internal/app/*_ext_test.go`).
4. Replace **all 27** `wire.Verdict(context.Background(), r.sock, …)` in `rig_ext_test.go` with `r.verdict(…)`.
   After this step `grep -c 'wire.Verdict(' internal/app/rig_ext_test.go` counts only the body of `verdict` and
   helpers that are about a socket by name (`serveStoring`'s users, if any are in this file).
5. `patience()`: in a bubble return `waitCap` without calling `r.t.Deadline()`. `rig.waitFor`'s fixed 5 s stays (it
   is fake time in a bubble).
6. Stops: both rigs stop the app in a `t.Cleanup` already; check that after `r.stop()` and the fake herdr's cleanup
   no goroutine of the rig is left (the bubble panics with "blocked goroutines remain" if one is). The rig's own
   goroutines (the `go func() { _ = r.app.Serve(ctx) }()`, feed readers) must end on stop.
7. The feed helper at line 3160 (`wire.Feed` on `r.sock`): leave it on the socket; in a bubble it must fail the
   test with a clear message (`r.t.Fatal("the event feed needs the unix socket: this test stays outside a bubble")`).

Do not change what any helper does outside a bubble: the other 37 files still run on sockets until their piece.

**Verify**: `go test -count=1 -tags integration ./internal/app` → `ok` (nothing is in a bubble yet; this proves the
rig's socket mode is unchanged).

### P0 step 5: convert the three sets

Apply "How a converted test looks" to `workerstart_ext_test.go` (both tests), `watchdog_ext_test.go` (all ten)
and `TestHoldAndResume` in `jobs_ext_test.go`. Note for `TestWorkerStartAfterStopForce`: it stops the app and
starts it again inside one test (`r.stop()`, `r.start()`); both happen in the one bubble.

Before converting, record each of these tests' time: `go test -count=1 -tags integration -v -run
'^(TestWorkerStart|TestIdleStalls|TestWorkingAgain|TestHoldAndResume)' ./internal/app` (extend the pattern to the
ten watchdog tests' names). After converting, run the same command again.

**Verify**: the command passes; then `go test -count=1 -tags integration ./internal/app` → `ok`.

### P0 step 6: the report is the pilot's result

Your `task_done` report must have, beyond the usual sections:

- **Blocked in a bubble**: every thing that stalled, panicked ("blocked goroutines remain", "deadlock") or behaved
  differently, with what it was (a goroutine of which package, blocked on what) and what you did. "Nothing" is an
  answer.
- **Times**: the before and after table of step 5.
- **Left on sockets**: any of the 13 tests you could not convert, with the reason.
- **For P1 to P9**: anything a converter must know that "How a converted test looks" does not say. If the recipe
  needs a new rule, say it here; the lead adds it to this file before dispatching the pieces.

### P0 done criteria

- [ ] `grep -n 'Conn ' internal/herdr/socket.go` shows the field; `git diff --stat` shows no other production file
      than `internal/herdr/socket.go` and `internal/herdr/panes.go`
- [ ] `go test -count=1 -tags integration -run 'TestSocketOverGivenConn' ./internal/herdr` passes
- [ ] `grep -c 'bubble(t' internal/app/workerstart_ext_test.go internal/app/watchdog_ext_test.go` shows at least
      2 and 10; `TestHoldAndResume`'s subtests each have one
- [ ] `grep -n 'r\.sock' internal/app/workerstart_ext_test.go internal/app/watchdog_ext_test.go` prints nothing
- [ ] `go test -count=1 -tags integration ./internal/app` and `./internal/herdr/...` pass
- [ ] `make -s check-affected` ends `0 fail`
- [ ] the report has the four sections of step 6

### P0 STOP conditions

- A second production change is needed (anything beyond `Socket.Conn`, `checked` and the one line in `panes.go`),
  for example a goroutine of `internal/app`, `internal/store` or `database/sql` that never ends, or production
  code that compares `time.Now()` with a file's time. Report what and where; do not add a seam for it.
- A converted test passes only with a changed assert, a changed wait length or a removed step.
- A converted test is slower in the bubble than before by more than a second (fake time should cost passes, not
  seconds: say which wait spins).
- The fake herdr's error for a dead server differs in memory in a way a watchdog test asserts on (the client maps
  a closed pipe differently from a reset socket): report the test and both errors.
- More than two of the 13 tests cannot be converted.

---

## P1 to P9: one family each

Precondition: P0 is DONE, and the lead has added to "How a converted test looks" whatever P0's report asked for.

### Steps (the same for every piece)

1. List your files' tests (`grep -n '^func Test' internal/app/<file>`), and run them once before any change with
   `-v` to record their times and that they pass.
2. Convert test by test with the rules of "How a converted test looks". Constructors that your tests call and that
   live in `rig_ext_test.go` (`reviewed`, `fetchRig`, `startWithT2`, `workingRig`, …) need no change: they build
   the rig with the `t` they are given, so they follow the bubble.
3. A helper defined **in one of your files** that calls `wire.Verdict(…, r.sock, …)`: change it to `r.verdict(…)`.
4. After each file: run that file's tests. After all: `go test -count=1 -tags integration ./internal/app`, then
   `make -s check-affected`.
5. Report: the before and after times per file; every test left on sockets with its reason; anything that blocked.

### Notes per piece

- **P1** (`loop_ext_test.go`): `TestSampleIntegrationTicketToMerged` reads the event feed (`wire.Feed`, line 1420):
  it stays outside a bubble. One `time.Sleep` and one direct `wire.Verdict`.
- **P2**: `dispatch_ext_test.go` has 20 `t.Run`: most build their own rig (own bubble each); check each.
- **P3**: nine direct `wire.Verdict` in `waiting_ext_test.go`.
- **P4**: `exit_ext_test.go`'s test is about the socket's `exit` request and the app ending: if the app's end
  inside the bubble leaves the rig's cleanup with nothing to stop, that is fine; if it cannot be made to end
  cleanly, leave it on the socket.
- **P5**: `TestHoldAndResume` is already converted (P0): do not touch it. `jobs_ext_test.go` has 15 `t.Run`.
- **P6**: `messages_ext_test.go` has no direct call but 5 `t.Run` and 3 uses of `srv.` scripting, which work
  unchanged on the in-memory server.
- **P7**: `status_ext_test.go:37-45` asks a real socket path with `wire.Ask` (and a missing one): that test stays.
- **P8**: `follow_ext_test.go` uses `srv.` twice for herdr going away (`Die`/`Revive` work in memory; `Restart`
  does not).
- **P9**: the mixed one. `fetch_ext_test.go` and `stopped_ext_test.go` hold the wire client's own tests
  (`serveStoring`, `sockDir`, 11 uses each): those stay as they are. Convert only the tests built on `fetchRig`
  or `newLoopRig` (`stopwake_ext_test.go` is all `fetchRig`). `testTurnFailsAtItsDeadline` is already a bubble.

### Done criteria (every piece)

- [ ] every test of your files is either inside `bubble(t, …)` or named in your report as left on sockets
- [ ] `grep -n 'r\.sock' <your files>` shows only lines of tests left on sockets
- [ ] `git diff --stat` lists only your files
- [ ] `go test -count=1 -tags integration ./internal/app` passes; `make -s check-affected` ends `0 fail`
- [ ] no assert, test name or wait length changed (`git diff -w` of a test is replacements and added lines of
      `bubble(` / `})` / `t.Log(`)

### STOP conditions (every piece)

- You need to change `rig_ext_test.go`, `rig_test.go`, `internal/herdr` or a production file: report what the rig
  lacks. (Another piece is editing its own files at the same time; the rig is shared.)
- A test passes outside a bubble and fails inside it, and the cause is not one of the recipe's rules (a `t.Run`, a
  `t.Deadline`, a socket path). Leave that test on sockets, report it with the failure text, go on with the rest.
  If this happens for more than a fifth of your tests, stop.
- A converted test passes only with a changed assert or wait.

---

## P10: close

Precondition: P1 to P9 are DONE.

1. Convert what can be converted of `serve_ext_test.go`, `panes_ext_test.go`, `restore_ext_test.go` and
   `verbs_ext_test.go` by the same recipe. Expect most to stay: `restore_ext_test.go` restarts herdr by its socket
   file (7 uses), `verbs_ext_test.go`'s `TestEventFeed` reads the feed, `serve_ext_test.go` starts with herdr down.
2. Collect every test of `internal/app` that is still outside a bubble **and** opens a unix socket (from the
   pieces' reports and `grep -ln 'herdrtest.Serve(\|sockDir(\|serveStoring(\|\.listen()' internal/app/*_test.go`
   after excluding tests in bubbles). Move them, unchanged, into files named `<family>_integration_test.go` with
   `//go:build integration` as their first line (`AGENTS.md` "Tests": a test that needs herdr's socket goes in such
   a file). Helpers only they use move with them.
3. In `rig_ext_test.go`: if no test outside a bubble builds a `loopRig` or a `rig` any more, delete the socket
   branches of step P0-4 (the rig is in-memory only, `bubble` no longer needs the `bubbles` map: the rig is always
   in one) and `patience()`'s deadline arithmetic. If some do, leave both modes and say which tests need them.
4. `tests/budget.json`: run `make -s check-budget` (it prints each package's wall time); set
   `obor/internal/app`'s `full_s` to the measured time rounded up to the next half second, if it is lower than the
   file's value. Never raise it.
5. `AGENTS.md`, "Tests", after the rule "One split, not two tiers": one sentence, `` `internal/app`'s rig tests run
   in a `testing/synctest` bubble on in-memory connections (`bubble`, `internal/app/rig_ext_test.go`): no wait in
   them is wall-clock time; a test that needs a unix socket is in an `_integration_test.go` file. ``
   `docs/module-map.md`: where it names `internal/app`'s test files or rig, the same fact in one clause.
6. Report: the flake log's `internal/app` entries for the last seven days against 82 in the eight days before
   this plan (`jq -r .test .obor/state/flakes.jsonl` filtered by time, mapped to files as the plan's "Why" did);
   the package's wall time before (27.5 s budget) and after; the list of tests left on sockets.

Done: `go test -count=1 ./internal/app` (no tag) opens no unix socket (`grep` of step 2 on the untagged files
prints nothing but bubble-free helpers); `make -s check-affected` and `make -s check-budget` pass.

STOP: the budget run shows `internal/app` slower than before; or more than 30 tests remain on sockets (the
conversion did not reach what it was for: report the reasons by group, do not tag them away).

---

## Common STOP conditions (every piece)

- The guard denies a command of "Commands you will need".
- `go test ./internal/app` fails in a test that is not yours, twice in a row, with nobody else's piece merged in
  between: report the test (it may be a flake this plan is about; say whether it is in a bubble).

## Maintenance notes

- **What a bubble does not fix.** A real race in the core (plan 001's nightly line counts those) and a test whose
  logic depends on the order of two things the app does in one pass. If a converted test still flakes, it is one of
  these two, and worth a task of its own: load is no longer an explanation.
- **Why `waitFor` stays.** The app works in passes on a ticker; a condition holds after some passes. In a bubble
  the poll costs fake time only, so the loop is deterministic enough and nobody needs to turn each wait into a
  `synctest.Wait()`. If a later task wants exact pass counts, the place is `rig.settle()`/`passes()`.
- **A failing wait spins.** A wait that never comes true now runs the app's passes until its fake deadline (60 s of
  fake time at a 2 ms tick is 30,000 passes) before it fails. That is seconds of CPU for a failing test, not a
  hang. If it annoys, lower `waitCap` for bubbles.
- **The rig's own clock stays** (`clock`, moved by the test): the watchdogs read it through `Options.Now`. Making
  the bubble's clock the app's clock (`Now: time.Now`, the test sleeping ten fake minutes) would delete `clock` and
  `Move`; it is a possible later step and changes every test that moves time, so it is not in this plan.
- **New rig tests** go in a bubble from the start (P10's `AGENTS.md` sentence). A reviewer who sees
  `wire.Verdict(…, r.sock, …)` in a new `_ext_test.go` test asks why it needs a socket.
- **`cmd/obor`'s process and socket tests** (37 flake entries, the second family) are not touched: they start the
  built binary, which no bubble can hold. Their fix is fewer of them (`AGENTS.md`: a decision is a unit test), not
  this plan.
- For the lead: `docs/decisions.md` 2026-11-05 ("synctest stays at the pilot … revisit only if a seam comes for
  another reason") needs a new line when P0 is dispatched: the seam is `herdr.Socket.Conn`, the reason the flake
  count. If P0 stops on its first STOP condition, steps 3-4 of idea A1 lose their test oracle too; decide then
  between the port-level fake and leaving the rig as it is.
- Idea A1's later steps (a queue in front of the handlers, shrinking the mutex) get their oracle from this: a
  suite that is red only when the code is wrong.
