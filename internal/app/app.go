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
	"sync"
	"time"

	"obor/internal/agents"
	"obor/internal/store"
)

// Machine is the machine part of an agent's address (machine, workspace, pane); only the local one for now.
const Machine = "local"

// Pane is one herdr pane as the app sees it.
type Pane struct {
	ID, Workspace string // Workspace: herdr's workspace id
	Cwd           string
	Agent         string // the agent's kind ("" when none runs); herdr's agent name is never read (registry.go)
	Label         string // the pane's label (obor's agent name, set by its launch); kept when an event has none
	Status        string // idle, working, blocked, done, unknown
	Transcript    string // the agent's transcript file when herdr reports one, else ""
	// Session is herdr's raw agent session (agent_session: an id, or a path), "" when herdr reports none: the
	// session's identity, independent of whether its transcript file is found.
	Session string
	// Terminal is herdr's terminal_id of the pane: kept across pane.move, the identity table's key (registry.go); ""
	// when herdr reports none. Kept when an event has none.
	Terminal string
}

// Snapshot is herdr's session at one moment: the workspace labels by id and every pane.
type Snapshot struct {
	Workspaces map[string]string
	Panes      []Pane
}

// Ready: an agent with this status is idle: it takes a prompt now (done is idle with an unviewed reply).
func Ready(status string) bool { return status == "idle" || status == "done" }

// Event kinds.
const (
	EventStatus  = "status"  // Pane's agent, its status or its metadata changed: New
	EventCreated = "created" // a new pane: New
	EventClosed  = "closed"  // Pane was closed
	EventExited  = "exited"  // Pane's shell exited and herdr removed it
)

// Event is one herdr event. New is the whole pane as herdr reported it after the event (status, created): it replaces
// what the app knew of the pane, metadata too. An event without it (New.ID "") carries Status and Agent only.
type Event struct {
	Kind, Pane    string
	Status, Agent string
	New           Pane
}

// Stream is one subscription: Snapshot was read once it was live, C carries the later events and closes when the
// stream ends (Err says why). Close ends it.
type Stream struct {
	Snapshot Snapshot
	C        <-chan Event
	Err      func() error
	Close    func()
}

// Panes is herdr, as the core uses it.
type Panes interface {
	Subscribe(ctx context.Context) (*Stream, error)
	// Busy: the agent in pane still runs a child besides the agent CLI (a command, a background job). An error means
	// unknown, not idle.
	Busy(ctx context.Context, pane string) (bool, error)
	// Activity is when pane's agent last wrote its transcript; known is false when obor cannot tell.
	Activity(ctx context.Context, pane string) (last time.Time, known bool)
	// Turn is the last turn pane's agent's transcript records, for a kind whose turns are read (agents.Caps.Turns):
	// the file transcript names (its agent's recorded session's, T-577), else the one herdr's session names; ok false
	// when obor cannot tell.
	Turn(ctx context.Context, pane, transcript string) (t agents.Turn, ok bool)
	Notify(ctx context.Context, title, body string) error
}

// Lane is one lane of a project: its worktree, the task its branch holds open ("" for none) and the lane's worker
// agent; for dispatch: its lane type and its status state (checks.LaneStatus: free,
// dirty-free, working, done, …).
type Lane struct {
	Name, Dir, Task string
	Agent           string
	Type, State     string
}

// Lanes reads a project's lanes.
type Lanes interface {
	Lanes(root string) ([]Lane, error)
}

// Options are the app's bounds and clock; zero fields take the defaults.
type Options struct {
	Root  string        // the project this serve serves (its main checkout); obor serve holds its serve.flock
	Idle  time.Duration // the watchdogs' bound (10 min)
	Gone  time.Duration // how long a lane's panes may run no agent before its task needs the owner (1 min: a restart)
	Tick  time.Duration // how often the watchdogs look (30 s)
	Retry time.Duration // the first wait before subscribing again after a subscribe error (2 s)
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
	// Inboxed: agent's session inbox is recorded (its SessionStart hook's <agent>.inbox, T-485), and the session that
	// recorded it ("": none named). A lead line waits, untried, while it is not; nil: always tried.
	Inboxed func(agent string) (session string, ok bool)
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
	panes Panes
	lanes Lanes

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
	notifying sync.Mutex         // one notify pass at a time (the watchdogs' and the merge loop's; a second one skips)
	kick      chan struct{}      // wakes the merge loop (buffered: kicks while it runs coalesce)
	effects   sync.WaitGroup     // the scratch effects running in their own goroutine (the merge loop's end waits)
}

// proj is the served project: its herdr workspace is the one labelled Name. Its state is in two parts: folded, what a
// replay of the event log alone rebuilds (written by foldRecord's folds), and the live state below it.
type proj struct {
	root, name string
	log        *Log
	folded     folded
	// The live state (T-615): none of it is rebuilt by a replay; a restart starts it afresh (docs/state-model.md "Not
	// recorded"), or it is the project's config, a read of the lanes or herdr, or a claim held while a step runs. A fold
	// may reset a live entry its record ends (resume and clear a task's retries, answered its stall timer, a dispatch
	// or a wait its refused dispatch line).
	lanes      []Lane               // as last read
	tasks      map[string]*watch    // the open tasks (a lane holds it, not in the merge loop), by task
	retry      map[Key]retry        // a failed notification: when to try it again, and how often it failed
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
	unwritten map[Key]json.RawMessage
	// badLoop, badLanes: the merge loop's adapters / the lanes read failed in their last pass (loopStatus).
	badLoop, badLanes bool
	// scratch is the token of serve's worktree (capacity 1): a gate, a publish or a resolved verdict's check holds it
	// while it runs. running is the key of the merge loop's gate or publish running in its own goroutine (Task "":
	// none; under mu).
	scratch chan struct{}
	running Key
	// exec: the effects running now, each with when its start began (App.do). obor status' running line lists the
	// command steps among them; an open intent not here is pending (T-662). Under mu.
	exec map[Key]time.Time
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
}

// folded is a project's state that a replay of its event log alone rebuilds (T-615): foldRecord's folds write it, live
// code reads it. Two exceptions, both named where they live: a lanes read ends the watchdog facts (active, woke) of a
// task no lane holds (setLanes: nothing recorded), and lr and watches carry live claims (lr's write state, a watch's
// lining). The identity table (registry), the lines (outbox) and the nightly check (nightlyState) hold folded parts of
// their own beside their live state.
type folded struct {
	active map[string][]Record  // task → its watchdog facts since the last resumed
	woke   map[string]time.Time // task → its stopped worker's first wake since the last resumed (stopwake.go)
	calls  map[string]string    // answered verdict calls: the stored reply by <method>.<call id>
	loop   map[string]*mtask    // the merge loop's tasks, by task
	pause  *Pause               // the project's last pause fact (pause.go; nil: none or cleared)
	// Jobs by fetch (fetch.go): delivered, the jobs a fact delivered names.
	delivered map[string]bool
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
}

// newFolded is the folded state before the log's first record.
func newFolded() folded {
	return folded{active: map[string][]Record{}, woke: map[string]time.Time{}, calls: map[string]string{},
		loop: map[string]*mtask{}, delivered: map[string]bool{}, watches: map[string]*mergeWatch{},
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
	lane       Lane
	agent      Pane      // the last pane an agent ran in while the lane held the task (ID "": none yet)
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
	pane  Pane
	since time.Time // its status and agent since
}
