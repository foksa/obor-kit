// Package app is obor's core (docs/module-map.md): the per-project event log, its replay, the watchdogs over the
// agents herdr reports and the merge loop (loop.go). Adapters reach it through the interfaces it defines: Panes
// (herdr's events and snapshot), Lanes (which lane holds which task) and the merge loop's Gate, Agents and Typer.
// The watchdogs only observe: their one outside effect is a herdr notification when a task needs the owner.
//
// One rule runs through the watchdogs: unknown is never idle. A failed process check, a transcript obor cannot find,
// a lane table it could not read or a herdr session it has not seen before never becomes an alarm to the owner.
package app

import (
	"context"
	"encoding/json"
	"io"
	"obor/internal/app/event"
	"obor/internal/app/port"
	"sync"
	"sync/atomic"
	"time"

	"obor/internal/store"
)

// Machine is the machine part of an agent's address (machine, workspace, pane); only the local one for now.
const Machine = "local"

// Ready: an agent with this status is idle: it takes a prompt now (done is idle with an unviewed reply).
func Ready(status string) bool { return status == "idle" || status == "done" }

// Options are the app's bounds and clock; zero fields take the defaults.
type Options struct {
	HerdrNote string        // "herdr 0.9.4, protocol 23" when herdr speaks a newer protocol than obor (serve sets it; status shows it)
	Root      string        // the project this serve serves (its main checkout); obor serve holds its serve.flock
	Idle      time.Duration // the watchdogs' bound (10 min)
	Gone      time.Duration // how long a lane's panes may run no agent before its task needs the owner (1 min: a restart)
	Tick      time.Duration // how often the watchdogs look (30 s)
	Retry     time.Duration // the first wait before subscribing again after a subscribe error (2 s)
	// RetryMax caps the wait while herdr stays down: it doubles from Retry up to this (5 s; T-596)
	RetryMax time.Duration
	Now      func() time.Time
	Out      io.Writer // one line per connect, stream end, herdr gone and back, and fact (nil: none)
	// Refresh: the stream ended with err only so the app works from a new snapshot (herdr's Code refresh): it
	// subscribes again with no line in Out (nil: never).
	Refresh func(err error) bool
	// Loop is the merge loop's adapters for the project at root, asked once per pass (nil: no merge loop, observe
	// only). A project without an integrator role (read at its load) has no merge loop either.
	Loop func(root string) (*Loop, error)
	// Inboxed: agent's session inbox is recorded (its SessionStart hook's record of the agent's slot in the project,
	// T-485, DD-consolidation I4), and the session that recorded it ("": none named). A lead line waits, untried,
	// while it is not; nil: always tried.
	Inboxed func(agent string) (session string, ok bool)
	// Exit ends serve gracefully, as a signal does (the socket's exit request, obor stop, T-711; nil: refused).
	Exit func()
	// Binary: the obor binary the kit's shim would run now is another file than this serve's (selfexec, T-719; nil:
	// not watched). Asked once per Follow; serve then restarts itself on it (follow.go).
	Binary func() bool
	// Follow is how often the binary watch asks Binary (30 s): its own timer, herdr connected or not (T-737).
	Follow time.Duration
}

// notifyRetry is the first wait before a failed notification is sent again; it doubles with each failure up to
// notifyRetryMax, so a herdr that stays away does not write a retry intent a minute.
const (
	notifyRetry    = time.Minute
	notifyRetryMax = 30 * time.Minute
)

// notifyWait is the wait after tries failed sends: 1, 2, 4 … minutes, at most notifyRetryMax.
func notifyWait(tries int) time.Duration {
	d := notifyRetry
	for i := 1; i < tries && d < notifyRetryMax; i++ {
		d *= 2
	}
	return min(d, notifyRetryMax)
}

// App is `obor serve`'s state. Make it with New, run it with Serve.
type App struct {
	opt   Options
	panes port.Panes
	lanes port.Lanes

	// mu guards the state below. Serve's goroutine changes it; it decides under mu and asks herdr, git and the lane
	// table outside it (Busy, Activity, Lanes and Notify are never called while mu is held).
	mu        sync.Mutex
	since     time.Time // when New made it: obor status's "serve: up since"
	connected bool
	p         *proj             // Options.Root's project once loaded (loadProject); nil before
	spaces    map[string]string // workspace id → label, from the last snapshot
	obs       map[string]*seen  // by pane id: every pane of the last snapshot and the events since
	stopped   bool              // Serve has ended: the socket takes no more requests
	checks    int               // full checks done (a tick's, a connect's), for the tests to wait on
	follows   int               // the binary watch's ticks (Options.Follow, follow.go), for it and the tests
	passes    int               // merge loop passes done, for the tests to wait on
	scratched int               // scratch effects started in their own goroutine, for the tests to wait on

	life      context.Context    // the daemon's lifetime: the socket requests' effects run under it
	stop      context.CancelFunc // ends life (Serve's end)
	reqs      sync.WaitGroup     // the socket requests being answered (Serve's end waits for them)
	loaded    chan struct{}      // closed at the project's first load: serve binds its socket then (Loaded, T-659)
	loadOnce  sync.Once          // closes loaded once
	verdicts  sync.Mutex         // the integrator's verdicts are answered one at a time
	loopMu    sync.Mutex         // loopStatus's decision and its record are one step (the merge loop and the check both call it)
	submitMu  sync.Mutex         // a submission's decision and its record are one step (task_done)
	sessions  sync.Mutex         // a session report's check and its record are one step (sessions.go)
	configMu  sync.Mutex         // one setup edit at a time (configset.go, T-753)
	notifying sync.Mutex         // one notify pass at a time (the watchdogs' and the merge loop's; a second one skips)
	kick      chan struct{}      // wakes the merge loop (buffered: kicks while it runs coalesce)
	effects   sync.WaitGroup     // the scratch effects running in their own goroutine (the merge loop's end waits)
	admitted  int                // the verdict calls in progress, waiting for the verdict lock included (under mu)
	follow    follow             // the watch of a new obor binary (follow.go, T-719; the merge loop's goroutine only)

	hooked atomic.Pointer[func(string)] // a test's hook at the points App.hook names (nil: none; drive.go)
}

// proj is the served project: its herdr workspace is the one labelled Name. Its state is in two parts: folded, what a
// replay of the event log alone rebuilds (written by foldRecord's folds), and the live state below it.
type proj struct {
	root, name string
	log        *event.Log
	folded     folded
	// The live state (T-615): none of it is rebuilt by a replay; a restart starts it afresh (docs/state-model.md "Not
	// recorded"), or it is the project's config, a read of the lanes or herdr, or a claim held while a step runs. A fold
	// may reset a live entry its record ends (resume and clear a task's retries, answered its stall timer, a dispatch
	// or a wait its refused dispatch line).
	lanes      []port.Lane          // as last read
	tasks      map[string]*watch    // the open tasks (a lane holds it, not in the merge loop), by task
	retry      map[event.Key]retry  // a failed notification: when to try it again, and how often it failed
	diskAt     time.Time            // when the free space was last read (disk.go)
	due        reviewDueLine        // obor status' "review due" line, counted in git on a pass (review.go)
	rv         reviewClaim          // a pass's claim of the open review request (review.go)
	integrator string               // the project's integrator agent: the one caller of the verdicts
	merging    bool                 // the merge loop runs for it (Options.Loop and an integrator role)
	timers     map[string]*jobTimer // the work jobs' delivery timers, the integrator's jobs' timers, by task
	// replies: the stored replies of the calls no fact records (job_next, spec set), by <method>.<call id>; a restart
	// forgets them (folded.calls holds the recorded calls').
	replies map[string]string
	// idleWait: since when the integrator's pane has been identified and not Ready, the interval its jobs' fresh
	// starts wait out (not recorded: a replay starts it afresh; T-602, T-606).
	idleWait idleWait
	// dispatching: the merge loop's last pass had the dispatch adapters (Loop.Lane, Loop.Specs); nopane: since when a
	// dispatched task's lane has no pane, by task; waitLine: a waiting task's last refused dispatch (logged on change)
	// and since when that refusal holds.
	dispatching bool
	nopane      map[string]time.Time
	waitLine    map[string]waitRefusal
	// unwritten: an effect that ran in this process and whose result's write failed, the result to write instead of
	// running it again (App.do).
	unwritten map[event.Key]json.RawMessage
	// badLoop, badLanes: the merge loop's adapters / the lanes read failed in their last pass (loopStatus).
	badLoop, badLanes bool
	// scratch is the token of serve's worktree (capacity 1): a gate, a publish or a resolved verdict's check holds it
	// while it runs. running is the key of the merge loop's gate or publish running in its own goroutine (Task "":
	// none; under mu); runCmd the command its gate runs now (GateInput.Command; "": none started yet).
	scratch chan struct{}
	running event.Key
	runCmd  string
	// exec: the effects running now, each with when its start began (App.do). obor status' running line lists the
	// command steps among them; an open intent not here is pending (T-662). Under mu.
	exec map[event.Key]time.Time
	// restarting: since when serve ends itself on the new build after a green after_merge (T-652): a status request
	// answered meanwhile shows "restarting serve" (T-657; zero: no self-restart pending).
	restarting time.Time
	// held: the tasks whose selected step runs now (from its start's check to its return, App.next), each with the
	// step's name; ended: the tasks whose drop is decided and being recorded (App.drop). Both under mu.
	held  map[string]string
	ended map[string]bool
	// roleFail: when a role agent's stop last failed, by agent; rvRead: the reviewer stop's last git read of an open
	// review (rolestop.go, T-672). Under mu.
	roleFail map[string]time.Time
	rvRead   rvRead
	// reg, out, night: the identity table (registry.go), the lines to the project's agents (messages.go, T-466, T-610)
	// and the nightly full check (nightly.go): each folds its records itself and keeps live state beside them.
	reg   registry
	out   outbox
	night nightlyState
	// holders: the configured reviewer and deep dive as the config last loaded names them (holdAgentsNow), whose work
	// holds the merge loop's publish; holds: the tasks whose
	// publish waits for one of them, by task (mergehold.go, T-718). Under mu.
	holders []holdAgent
	holds   map[string]mergeHold
	// admits: the admissions in force (a review start, a verify or a hold agent's brief, from its wait for the running
	// publish to its brief recorded: no publish starts meanwhile); published: closed when the running publish ends
	// (nil: none runs). Under mu (mergehold.go, T-730).
	admits    int
	published chan struct{}
}

// folded is a project's state that a replay of its event log alone rebuilds (T-615): foldRecord's folds write it, live
// code reads it. Two exceptions, both named where they live: a lanes read ends the watchdog facts (active, woke) of a
// task no lane holds (setLanes: nothing recorded), and lr and watches carry live claims (lr's write state, a watch's
// lining). The identity table (registry), the lines (outbox) and the nightly check (nightlyState) hold folded parts of
// their own beside their live state.
type folded struct {
	active map[string][]event.Record // task → its watchdog facts since the last resumed
	woke   map[string]time.Time      // task → its stopped worker's first wake since the last resumed (stopwake.go)
	calls  map[string]string         // answered verdict calls: the stored reply by <method>.<call id>
	loop   map[string]*mtask         // the merge loop's tasks, by task
	pause  *event.Pause              // the project's last pause fact (pause.go; nil: none or cleared)
	// models: each model's last pause fact (obor pause --model, T-765; a cleared one is gone).
	models map[string]*event.Pause
	// Jobs by fetch (fetch.go): delivered, the job id a fact delivered names and the fact's seq (0: not delivered).
	delivered map[string]int64
	// lr: the lanes table's projection (lanerows.go, T-439; its write state is live).
	lr      laneRows
	watches map[string]*mergeWatch // the open watches of a task's merge by task (mergewatch.go; lining is live)
	// review: the open review request (review.go; nil: none; held is live); reviewed: the last review started.
	review   *reviewReq
	reviewed lastReview
	// sessions: the agents' recorded sessions in log order (sessions.go, T-577).
	sessions []store.Session
	// roles: serve's stops of role agents (rolestop.go, T-672).
	roles roleStops
	// idle: the project's last idle announce and the lead's acknowledgement (idle.go, T-803; nil: none in force).
	idle *idleState
}

// newFolded is the folded state before the log's first record.
func newFolded() folded {
	return folded{active: map[string][]event.Record{}, woke: map[string]time.Time{}, calls: map[string]string{},
		loop: map[string]*mtask{}, delivered: map[string]int64{}, watches: map[string]*mergeWatch{},
		lr: laneRows{rows: map[string]store.LaneRow{}, submitted: map[string]int64{}}}
}

// retry is one failed notification's next try; tries counts the failures so far (the wait doubles from notifyRetry).
type retry struct {
	at    time.Time
	tries int
}

// watch is what the watchdogs know of one open task beyond its facts. A new task, a replay and a new lane start one
// afresh: no timer inherits time from before it.
type watch struct {
	lane       port.Lane
	agent      port.Pane // the last pane an agent ran in while the lane held the task (ID "": none yet)
	lost       time.Time // since when the lane's panes run no agent (zero: one runs)
	quietTimer           // the stall timer's quiet time
	blocked    time.Time // since when the agent is seen blocked for the task (zero: it is not)
}

// quietTimer is the stall timer's quiet time (docs/state-model.md "Timers"), the watchdogs' and the integrator jobs'.
type quietTimer struct {
	quietSince time.Time // the baseline: the first quiet sample of this quiet period (zero: none)
	seenWrite  time.Time // the newest transcript write accounted for (zero: not sampled yet)
}

// seen is one observed pane.
type seen struct {
	pane  port.Pane
	since time.Time // its status and agent since
}
