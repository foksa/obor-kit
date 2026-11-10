# Architecture ideas after the 2026-11-10 audit (advisor)

An outside read of obor's architecture at commit `a73e814c`, with ideas ordered by what they would change. Not yet
plans: each idea names its first step and what that step's result decides. The plans that already exist are in
`docs/research/improve-plans.md`; where an idea starts with one of them, it says so.

How deep the read was: `docs/module-map.md`, `docs/decisions.md`, `BACKLOG.md`, `docs/observations.md`, the summary
of `docs/plan/architecture-target.md`, the socket's request handling (`internal/app/status.go`), the base packages
and the test rig's shape. The merge loop (`loop.go`, `steps.go`, `mtask.go`) was not read line by line. Numbers
below were counted on the tree; claims about the loop's inner behaviour are marked as assumptions.

## Recommendation

Keep the design. Close one gap: the core is specified as a state machine and built and tested as a concurrent
server. Make it single-threaded and deterministic in small steps (A1), starting with the two cheapest ones, which
pay off even if the rest never happens. Write each planned second implementation (rex or herdr-core, pi) against
its contract table, from a requirements list made first (A2). One serve per project stays the rule for multiple devices too (section "One serve, several devices").

What to keep as it is: one writer (serve) with an event log, intent before an effect and result after; hooks and
CLIs reading projections; adapters behind ports with the import rule enforced by `internal/checks/boundary_test.go`;
one read model (`internal/view`) for `obor status` and `obor web`; the decide/do split started in `checks.Scratch`.

## A1. A single-threaded, deterministic core

**Facts.**
- `internal/app` keeps its state under one mutex, taken at 183 places in 64 production files. `proj` has 41 fields,
  `App` 35. Which fields a function may touch is said in comments ("under mu", "the caller holds a.mu").
- Every socket request runs on its own goroutine (`internal/app/status.go:343`); effects run outside the lock and
  record their result afterwards.
- BACKLOG holds about six accepted findings of one shape, state read under the lock and acted on after it was
  released: RV-104 #2 (idle ack reads the epoch, records after), RV-68 #3 (a needs-you repeat appended after the
  close; "fixing needs a lock-order change"), RV-89 #2 and RV-101 #2 (an older pane re-read overwrites a newer
  one), RV-84 (a pause cleared during the stop transition), RV-148 (the config rev re-read after the launch). Each
  was rated unlikely by itself. T-606 (a choice that followed Go's map order) is the same family: behaviour that
  depends on scheduling.
- The tests of the core are shaped like the server: the rig (`internal/app/rig_test.go`, `rig_ext_test.go`, 7,436
  lines) starts a fake herdr on a real unix socket, 28 of 89 test files dial the app socket, and the tests wait on
  wall-clock time at 379 places. Many of the flake log's tests are this package's (`TestWorkerStartRule`,
  `TestIdleStallsThenNeedsYou`, `TestHoldAndResume`, `TestWakePostRefusedThenReplayed`; a per-package count was not
  made), and `docs/observations.md` says flaky reds track machine load.
- Production code already takes its time from one place (4 direct `time.Now()` calls in `internal/app`).
- The 11-05 `synctest` pilot stopped at exactly this point (`docs/decisions.md`: the slow waits "need a live unix
  socket, where the bubble's clock stalls").

**Target.** One goroutine owns the project's state. Everything that can change it arrives as an input on one
queue: a socket request, a terminal event, an effect's result, a tick. Handling an input is a plain function:
state and input in, new state and a list of effects out. Effects run outside, as today, and their results come back
as inputs. No mutex over the state, no "under mu" comments, and the order of two things that happen close together
is the order they were queued in, the same on every run.

**What it buys.** The accepted-race family above cannot be written any more: there is no gap between reading state
and acting on it. Core tests feed inputs and assert states and effects, with no socket and no sleep, so load cannot
make them red. A new state or rule is tested as a table row.

**Steps, each useful alone.**
1. **Measure** (plan 001): the nightly's race count. Many reports mean the lock discipline leaks in practice and
   the later steps are worth their cost; zero for two weeks means the value of A1 is in the tests (step 2), and
   steps 3-4 can wait.
2. **An in-process path for the rig**: the test rig calls serve's handlers directly (`(*App).handlers()` already
   maps each method to a function) and gives the app a fake `port.Panes` in process, instead of dialing two unix
   sockets. A handful of integration tests keep the sockets. No production change is expected; if one is needed it
   is a seam for calling a handler, not a dialer seam in `wire`. Then move the timing-bound tests into
   `testing/synctest` bubbles, a package's worth at a time, and delete their waits. Measure: `internal/app`'s wall
   time in `tests/budget.json` and its flake-log entries per week, before and after.
3. **A queue in front of the handlers**: socket requests and terminal events go through one channel to one
   goroutine that takes the mutex for them. Behaviour is unchanged, but requests no longer interleave, which closes
   the request-against-request races without touching the handlers.
4. **Shrink the mutex**: one concern at a time, in the pattern `app/identity` and `app/outbox` already use (state
   with unexported maps, a fold, "no lock: under `App.mu`"), until the mutex guards nothing the queue does not
   already order. Assumption to check first: the merge loop's `advance`/`do` can hand an effect off and take its
   result as an input without holding state across the wait.

**Cost and risk.** Steps 3-4 are hard tasks in the most-changed package; `docs/observations.md` says replacing a
mechanism grows the code. The oracle is the existing suite plus the golden replay. Stop after step 2 if step 1
shows no races and the flakes are gone: that already removes the daily cost.

**Not part of this.** Crash windows between an effect and its record (RV-69, RV-126, RV-131) are a different
family; a single goroutine does not close them. They stay priced one by one.

## A2. Second implementations: a requirements list first, then against the contract table

Revised 2026-11-10 after the owner's note: the second implementations are named plans, not speculation. A second
terminal (herdr-core, or rex, a similar tool) and a further agent kind (the pi harness) are intended; OpenCode
left the main executable to prove the kind protocol and to contain third-party risk.

**Facts.** `port.Workflow` (T-1029), `port.TerminalSetup` (T-1044) and the kind protocol (T-1036, T-1046) each have
one implementation today: scratch-ff, herdr, and the kit's own OpenCode binary. Each has a contract table, a fake
and conformance rows. `docs/observations.md` records what a mode without a user cost: keep lanes (T-1031, then
T-1050).

**Idea, terminal.** Before choosing between rex and herdr-core, write "what obor needs from a terminal" as one
short list, taken from what herdr does not give today and obor works around:
- an instance identity: herdr's API has no pid, start time or instance id, so serve tells a restart by the socket
  file's mtime (BACKLOG, T-1135);
- an event when a pane is relabelled: herdr's rename emits none (BACKLOG, RV-64 #1; the 10-30 rename notes);
- an environment on an agent start: `agent.start` takes only args, so identity is a typed export
  (`docs/decisions.md` 2026-11-04, T-812);
- a dialog or blocked state that typing respects: a typed wake or brief can land in a Codex or OpenCode dialog and
  pick an option (BACKLOG, 2026-10-26/27);
- stable optional fields: an omitted `launch_pending` decodes as "launched" (BACKLOG, RV-151);
- a plugin or hook registry that is not user-global (`AGENTS.md`, "Tests stub herdr", T-150);
- idle cost: herdr 0.9.5 burns about 75 % of a core idle (`docs/observations.md`).
Each tool is then judged against the list, and the list says which workarounds a new adapter may delete. The
adapter is written against the terminal contract table (`internal/herdr/herdrtest/contract.go`) and the live
tier's H-C rows first.

**Idea, kinds.** A pi kind is the first one written by someone reading `docs/agents/kind-protocol.md` cold:
OpenCode's was written by the protocol's authors. Build it from the document alone, run the live conformance tier
on it, and treat every question the document did not answer as a finding about the protocol, not about pi.

**Rule that stays.** A task that adds a method to a port or to the kind protocol names the implementation that
needs it. No mode, selector or method for an implementation nobody has named.

**First step.** The terminal requirements list: a research note, no code.

## A3. obor-kit's own serve follows a quieter build (an option)

**Facts.** In obor-kit the live serve runs `dist/obor`, rebuilt by `merge.after` on every merge, and re-execs onto
it. BACKLOG and the observations hold four costs of that: a serve that could not load a just-merged config
(11-03, T-765), uncommitted schema-13 code running as the live serve and migrating the store (11-07), waiting work
fired on the old binary in the 30 s before the re-exec (11-08), and the standing rule never to `make build`
uncommitted code in the main checkout.

**Idea.** Serve follows the build of the last GREEN nightly (or the installed kit, as adopters' serves do), while
lanes, hooks and the CLI keep using the fresh `dist/obor`.

**Trade-off.** A fix to serve's own code goes live a day later (or by one explicit `obor serve restart`), and
immediate dogfooding has found real bugs. Mixed versions between serve and its clients become the normal case
instead of a 30 s window, so the version-skew handling (`internal/app/status.go:473-502`) would carry more weight.
Decide only if one of the four costs shows up again.

## A4. The review cycle as its own leaf package

**Facts.** Five files in `internal/app` hold the review cycle (`review.go`, `reviewclose.go`, `reviewfold.go`,
`reviewonmerge.go`, `reviewverify.go`), and the review rules produced a long run of findings (RV-106, RV-113,
RV-125, RV-126). `app/identity` (T-1066) and `app/outbox` (T-1067) show the pattern: the state with unexported
maps, a `Fold` that returns what ended, queries, and delivery left in `app`.

**Idea.** `app/review`: the open review, its fix round and verify as state plus fold; `app` keeps the effects. It is
the next candidate because its state is already separate in the files and it has no lane or merge-slot coupling.
The decision of 11-08 ("the merge-loop core stays one package") is not touched.

## A5. Version skew visible in `obor status`

Serve receives every caller's obor version in the request envelope and logs the first mismatch per agent to
`serve.log` only (`internal/app/status.go:473-502`). BACKLOG's Later candidates has "`obor doctor`: hooks loaded +
kit version per pane (stale panes hid guard bugs twice)". A status line for agents on another release covers most
of that without a new command. It must compare release versions only: in obor-kit dev builds differ after every
merge. It gains weight if A3 is taken.

## A6. `obor stats`: weekly numbers from the event log, rule firings first

**Facts.** The event log holds every dispatch, gate, review, delivery and rule action (about 36,000 records in
obor-kit at planning time), and obor has no command that turns them into numbers (`obor flakes` and `obor events`
are the nearest). The numbers are measured by hand in research notes instead (`gate-time.md`, `chatter-census.md`,
the token figures in `docs/observations.md`). The owner's stated purpose for obor is to do what agents forget or
overlook during a session, and the owner's rule is that a simpler flow is worth more than simpler code.

**Idea.** One read-only command over the store, per week: how often each rule acted (a nudge, a stop, a stalled
worker started again, a review kept open, a question routed), merges, time from dispatch to merge, needs-you per
merge, sendback rate, retests. Rule firings are the product's own measure: they count the things an agent would
have dropped. A rule that never fires is a candidate to delete; a flow change is judged by the numbers before and
after it.

**First step.** List the rule steps the log already records and check that each firing is one countable record.
It belongs in `internal/view` (every reader of project state goes through it).

## A7. `BACKLOG.md` holds what is next; accepted findings live elsewhere

**Facts.** `BACKLOG.md` is 38 KB. 67 lines of its "Open" section are accepted findings, each to be fixed only if
seen again. Its own Handoff says "a full pass is still due".

**Idea.** `obor review close --accept` writes its line to a second file (for example `docs/accepted.md`) that no
role reads at session start; `BACKLOG.md` keeps the handoff, the owner's pending decisions, later candidates and
concerns. An accepted item is looked up when its symptom shows, not re-read every session.

**First step.** Where `--accept` writes (`app.AcceptLines`, called from `cmd/obor/reviewcycle.go`), and a one-time
move of the existing lines.

## A8. Serve says when an agent CLI or the terminal changed version

**Facts.** `docs/observations.md`: "External CLIs change under us" (Codex auto-upgraded mid-day and its unattended
starts timed out); the check after a `brew upgrade` is a manual habit. Serve already notes the terminal's version
and protocol for its status line (`herdrNote`, `internal/app/status.go:507`).

**Idea.** Serve records the version of each agent CLI and of the terminal it last started agents with, and raises
one line to the lead when one differs ("codex 0.167, not yet checked: run the live conformance tier for codex").
It does what a person forgets, which is obor's own job description.

**First step.** Whether each kind can report its CLI version cheaply (a field in the kind's manifest answer would
keep it kind-neutral).

## A9. For several devices, needs-you first

**Facts.** `obor web` has two write routes, `/api/config` and `/api/pause`. A needs-you is answered with the CLI
(`obor resume T-xx --<answer>`). BACKLOG's Ideas has a phone push, parked.

**Idea.** The owner's attention is the scarce resource, so the first multi-device feature worth having is seeing
and answering a needs-you from another device: one more write route on `obor web` that sends the same resume
request the CLI sends. It needs the viewer path of the section below (plan 002, then a login) and nothing from
remote lanes.

## One serve, several devices

The question: how does one serve (and a single-threaded core) behave when obor runs on more than one device?

**One serve per project stays, on the machine that holds the main checkout.** A single-threaded core does not limit
this: serve's own work is small and at human cadence; the time goes into agents and checks, which run outside it.
A remote effect (a start over SSH, a check on another machine) is only slower and fails more often, and the model
already has a place for that: an intent recorded, a result pending, a timeout. What must not happen is a second
writer: no second serve for the same project, no copy of the event log that accepts writes. Other devices are
either viewers or workers.

**Three places assume "same machine" today.** `docs/module-map.md` says the core addresses (machine, workspace,
pane) and "no interface may assume local", but `internal/app/port` has no machine in any type yet, and:

1. **The app socket is a unix socket in the project** (`wire.SocketPath`: `.obor/state/serve.sock`). An agent's MCP
   shim and the CLI on another machine cannot reach it. A forwarded unix socket over SSH is enough; nothing in the
   protocol is local.
2. **Hooks read the database file.** The guard, the Stop hook and `whoami` call `store.ReadLaneAt` and
   `store.ReadSpecAt` on `.obor/state/obor.db` (`internal/guard/guard.go:150`, `stop.go:165`, `whoami.go:27`). On
   another machine there is no file, and the hooks' rule is "fast, no network". The 11-05 decision already points
   the way ("readers while serve is down read a projection"): serve writes the lane's row and spec as a small file
   and copies it to the worker's machine at dispatch and at each spec change; the hook reads that file.
3. **A lane is a git worktree of the main repository** (`internal/checks/gitio.go:242`), and serve commits a lane's
   tree and tests it in place. A lane on another machine is a clone, and its work reaches serve by fetch. That is a
   second `port.Workflow` implementation, the largest of the three.

**An order that keeps each step small.**
- **Viewer devices first**: `obor web` behind a name (plan 002's `--allow-host`), then a login and a non-loopback
  listen. Nothing in serve changes.
- **Remote checks next** (BACKLOG, Ideas: "heavy merge checks remotely rather than remote lanes"): serve pushes the
  candidate to a clone on the other machine and runs the check command there over SSH. Only the `checks` adapter
  changes; no hook, MCP shim or agent runs remotely. Plan 004 is its first fact-finding step if that machine is
  Linux.
- **Remote lanes last**: all three assumptions above, plus the terminal. Whether herdr shows a remote machine's
  panes through the same socket API (so `Panes` and `Typer` work unchanged) was not checked here; it decides how
  much of this is obor's work.

**The fleet view and messages between projects.** Neither goes through a central server today, and neither needs
one. Both rest on a file per machine, `~/.obor/projects.json` (name → main checkout, written by `obor serve` and
`obor init`; `internal/project/projects.go`):
- `obor web` is its own process. For each listed project it asks that project's serve for its status over the
  project's socket and reads the project's files itself: the store read-only, the git lane table
  (`checks.LaneTable`) and one `ps` for background processes (`internal/view`, `view.Load`).
- `obor send <project>` looks the name up in that file and sends one request to the *target* project's serve socket
  (`cmd/obor/send.go`); that serve posts the line to its lead.

So one serve per project changes nothing on one machine, and it keeps what a machine-wide server would lose: a
project's crash, replay or upgrade touches no other project, and projects on different obor versions stay listed
side by side. Across machines the same "same machine" assumptions return one level up: the list is per machine,
the page reads local files, and send dials a local socket. Steps, smallest first:
1. **The list names where a project lives**: an entry is a root on this machine, or a machine plus a root.
2. **`obor send` to a project elsewhere runs the other machine's CLI over SSH** (`ssh <machine> obor send …`), or
   uses a forwarded socket. No protocol change. The reply line names the sender, so the other machine's list must
   know the sender's project too.
3. **One `obor web` per machine, and the fleet page merges them**: the page (or the local `obor web`) asks each
   known machine's `obor web` for `/api/projects` and the drill-down routes. The reader stays where the files are;
   this needs plan 002's `--allow-host` and a login.
4. **Only for a project whose lanes run on another machine**: the lane table and the background scan in
   `view.Load` must come from serve's status (serve already holds the lane table as a cached observation,
   `internal/app/lanescan.go`), not from local git and `ps`.

Not recommended: a machine-wide or fleet-wide server that owns the list or relays messages. It would be a second
place of state and a shared point of failure, for a lookup a file does today (T-522 already moved obor from one serve
per kit install to one per project, `docs/decisions.md` 2026-10-28).

**What a partition does.** With the link down, a remote worker cannot fetch a job or submit, and serve sees its
pane as unknown. Workers stall and resume; nothing forks, because there is one log. The existing stall and
needs-you rules cover it, with the caveat RV-120 already names: an unobserved interval must not count as idle time.
If the serve machine sleeps, every device waits. That is the price of one writer, and at this scale it is the right
price.
