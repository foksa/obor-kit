// Package app is obor's core (docs/module-map.md): the per-project event log, its replay, the watchdogs over the
// agents herdr reports and the merge loop (loop.go). Adapters reach it through the interfaces it defines: Panes
// (herdr's events and snapshot), Lanes (which lane holds which task) and the merge loop's Gate, Agents and Prompter.
// The watchdogs only observe: what needs the owner is a rule pass's (rules.go), which toasts the owner and lines the
// lead.
//
// One rule runs through the watchdogs: unknown is never idle. A failed process check, a transcript obor cannot find,
// a lane table it could not read or a herdr session it has not seen before never becomes an alarm to the owner.
package app

import (
	"context"
	"encoding/json"
	"io"
	"obor/internal/app/event"
	"obor/internal/app/identity"
	"obor/internal/app/outbox"
	"obor/internal/app/port"
	"obor/internal/app/review"
	"obor/internal/background"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"obor/internal/store"
)

// Machine is the machine part of an agent's address (machine, group, pane); only the local one for now.
const Machine = "local"

// Ready: an agent with this status is idle: it takes a prompt now (done is idle with an unviewed reply). It is
// port.Pane.Ready, for the callers that hold a status.
func Ready(status string) bool { return (port.Pane{Status: status}).Ready() }

// Options are the app's bounds and clock; zero fields take the defaults.
type Options struct {
	Root  string        // the project this serve serves (its main checkout); obor serve holds its serve.flock
	Idle  time.Duration // the watchdogs' bound (10 min)
	Gone  time.Duration // how long a lane's panes may run no agent before its task needs the owner (1 min: a restart)
	Tick  time.Duration // how often the watchdogs look (30 s)
	Retry time.Duration // the first wait before subscribing again after a subscribe error (2 s)
	// RetryMax caps the wait while herdr stays down: it doubles from Retry up to this (5 s)
	RetryMax time.Duration
	// StepRetry is the wait before a step whose attempt was not counted is tried again (5 s); it doubles per such
	// attempt in a row up to StepRetryMax (1 min; cutwait.go).
	StepRetry, StepRetryMax time.Duration
	Now                     func() time.Time
	Out                     io.Writer // one line per connect, stream end, herdr gone and back, and fact (nil: none)
	// Refresh: the stream ended with err only so the app works from a new snapshot (herdr's Code refresh): it
	// subscribes again with no line in Out (nil: never).
	Refresh func(err error) bool
	// Loop is the merge loop's adapters for the project at root, asked once per pass (nil: no merge loop, observe
	// only). A project without an integrator role (read at its load) has no merge loop either.
	Loop func(root string) (*Loop, error)
	// Inboxed: agent's session inbox is recorded (its SessionStart hook's record of the agent's slot in the project),
	// and the session that recorded it ("": none named). A lead line waits, untried,
	// while it is not; nil: always tried.
	Inboxed func(agent string) (session string, ok bool)
	// Exit ends serve gracefully, as a signal does (the socket's exit request, obor stop; nil: refused).
	Exit func()
	// Binary: the obor binary the kit's shim would run now is another file than this serve's (selfexec; nil:
	// not watched). Asked once per Follow; serve then restarts itself on it (follow.go).
	Binary func() bool
	// Follow is how often the binary watch asks Binary (30 s): its own timer, herdr connected or not.
	Follow time.Duration
	// Tokens is a session transcript's context size (the keep lane handoff rule; ok false: none read). Nil:
	// project.ContextTokens' read of the file.
	Tokens func(transcript string) (int, bool)
	// Activity is what the agents themselves record of their work, the watchdogs' evidence (activity.go; nil: the
	// agents' files under Home and the process table, with each pane's process info from Info).
	Activity AgentActivity
	// Home is the user's home holding the agents' transcripts: .claude/projects and .codex/sessions
	// ("" : os.UserHomeDir).
	Home string
	// Info is the terminal's process info of one pane (Activity's child-process check; nil: unknown activity).
	Info func(ctx context.Context, pane string) (port.ProcessInfo, error)
	// Screen is a pane's visible text, its last lines, as the terminal shows it: what a blocked pane asks a person
	// (nil: a blocked needs-you carries no question).
	Screen func(ctx context.Context, pane string, lines int) (string, error)
	// Procs is the machine's process table (Activity's child-process check; nil: background.ReadProcs).
	Procs func(ctx context.Context) ([]background.Proc, error)
	// Restore restarts the project's labelled panes that run no agent, after the terminal connection came back
	// (restore.go; nil: never).
	Restore port.Restorer
	// ReadDir is how load reads the state's logs dir once for the tasks it folds as ended (logs.go;
	// nil: os.ReadDir).
	ReadDir func(dir string) ([]os.DirEntry, error)
}

// App is `obor serve`'s state. Make it with New, run it with Serve.
type App struct {
	opt      Options
	panes    port.Panes
	lanes    port.Lanes
	activity AgentActivity // what the agents themselves record of their work (activity.go, Options.Activity)

	// mu guards the state below. Serve's goroutine changes it; it decides under mu and asks the terminal, git and the
	// lane table outside it (Busy, Activity, Lanes and Notify are never called while mu is held). The rule for a verb (J1):
	// it reads (git, config, lanes, the spec store) with no lock, then checks, appends, folds and replies in one mu
	// section (recordLocked); only an effect in flight is a claim (held, running, rv.what, a line's sending). Every
	// append is inside mu with its fold (record, do's intent and result, a verdict's), so the live fold order is
	// the log's seq order; only the store write joins the section, never an effect (git, herdr, a launch). The
	// event log's lock is taken inside mu, never the other way round. The dispatch-class verbs (verdict, drop,
	// dispatch, drive, pause, job_next, resume, task_done, land, spec set, move) take a lock besides mu to order those
	// verbs across their git reads: `verdicts` for dispatch, drop, drive, pause, job_next and resume;
	// `submitMu` for task_done, land, spec set and move (a resume and a move take both: verdicts, then submitMu).
	mu        sync.Mutex
	since     time.Time // when New made it: obor status's "serve: up since"
	connected bool
	streams   int              // herdr streams connected (connect): a pass that saw one end ran on no snapshot of it
	termVer   string           // the terminal serve is connected to (or was last): its snapshot's version
	termNote  string           // that snapshot's note: the adapter's sentence on a terminal newer than it knows ("" otherwise)
	p         *proj            // Options.Root's project once loaded (loadProject); nil before
	obs       map[string]*seen // by pane id: every pane of the last snapshot and the events since
	skews     map[string]bool  // by agent and version: the wire callers whose obor differs from serve's and were logged
	// behind is, by agent, the obor version its calls carry when that is another release than serve's (version.Release):
	// Status's skew line. An agent's entry goes at a started request (a launcher's confirmed start: a new process,
	// status.go's startedCall) or at a session request (a hook's, a plugin's, a start's of the session it runs).
	behind    map[string]string
	stopped   bool // Serve has ended: the socket takes no more requests
	checks    int  // full checks done (a tick's, a connect's), for the tests to wait on
	follows   int  // the binary watch's ticks (Options.Follow, follow.go), for it and the tests
	passes    int  // merge loop passes done, for the tests to wait on
	onStream  int  // of those, the passes that ran connected to one herdr stream (mergeLoop), for the tests to wait on
	scratched int  // scratch effects started in their own goroutine, for the tests to wait on

	life     context.Context    // the daemon's lifetime: the socket requests' effects run under it
	stop     context.CancelFunc // ends life (Serve's end)
	reqs     sync.WaitGroup     // the socket requests being answered (Serve's end waits for them)
	loaded   chan struct{}      // closed at the project's first load: serve binds its socket then (Loaded)
	loadOnce sync.Once          // closes loaded once
	verdicts sync.Mutex         // the integrator's verdicts are answered one at a time
	submitMu sync.Mutex         // a submission's decision and its record are one step (task_done)
	configMu sync.Mutex         // one main-checkout writer at a time: a setup edit, a review's close
	kick     chan struct{}      // wakes the merge loop (buffered: kicks while it runs coalesce)
	rescan   chan struct{}      // wakes the lane table's refresher (buffered: kicks while it scans coalesce; lanescan.go)
	scanned  chan struct{}      // the refresher scanned: the run loop decides (buffered: coalesced)
	effects  sync.WaitGroup     // the scratch effects running in their own goroutine (the merge loop's end waits)
	admitted int                // the verdict calls in progress, waiting for the verdict lock included (under mu)
	follow   follow             // the watch of a new obor binary (follow.go; the merge loop's goroutine only)
	rst      restoreState       // the restore after a reconnect (restore.go; under mu)

	hooked atomic.Pointer[func(string)] // a test's hook at the points App.hook names (nil: none; drive.go)
}

// proj is the served project: its panes are the terminal's group named Name (herdr: the workspace labelled Name).
// Its state is in two parts: folded, what a replay of the event log alone rebuilds (written by foldRecord's folds),
// and the live state below it.
type proj struct {
	root, name string
	// terminal is the project's terminal kind (config.TerminalKind): its name among the tools the version pass reads.
	terminal string
	log      *event.Log
	folded   folded
	// The live state: none of it is rebuilt by a replay; a restart starts it afresh (docs/state-model.md "Not
	// recorded"), or it is the project's config, a read of the lanes or herdr, or a claim held while a step runs. A fold
	// may reset a live entry its record ends (resume and clear a task's retries, answered its stall timer, a dispatch
	// or a wait its refused dispatch line).
	lanes      []port.Lane          // as last read
	scan       laneScan             // the lane table as last scanned: the watchdogs' cached observation (lanescan.go)
	tasks      map[string]*watch    // the open tasks (a lane holds it, not in the merge loop), by task
	diskAt     time.Time            // when the free space was last read (disk.go)
	versionsAt time.Time            // when the tools' versions were last read (versions.go)
	due        string               // obor status' "review due" line, counted from the fold on each pass (review.go)
	rv         reviewClaim          // a pass's claim of the open review request (review.go)
	integrator string               // the project's integrator agent: the one caller of the verdicts
	merging    bool                 // the merge loop runs for it (Options.Loop and an integrator role)
	timers     map[string]*jobTimer // the work jobs' delivery timers, the integrator's jobs' timers, by task
	// fixes: the open review's fix round as the verify rule last read it (the review card's fixes); verifyDue: a
	// verify is owed, which idle waits for (reviewverify.go); closeRead: this process read git once for the open
	// review's close commit, the close's crash cut (reviewclose.go).
	fixes     review.Round
	verifyDue bool
	closeRead bool
	// lastBefore: the newest record's time when the replay ended, before this serve's start-up appended any (zero: no
	// record, or its time unreadable): serve's first connect compares the terminal's start with it (restore.go).
	lastBefore time.Time
	// replies: the stored replies of the calls no fact records (job_next, spec set), by <method>.<call id>; a restart
	// forgets them (folded.calls holds the recorded calls').
	replies map[string]string
	// idleWait: since when the integrator's pane has been identified and not Ready, the interval its jobs' fresh
	// starts wait out (not recorded: a replay starts it afresh).
	idleWait idleWait
	// rules: each rule's live state in the rule pass (rules.go): the key its condition holds with, since when,
	// and its failed actions in a row. Not recorded: a restart starts it again; under mu.
	rules map[string]ruleLive
	// dispatching: the merge loop's last pass had the dispatch adapters (Loop.Workflow, Loop.Specs); nopane: since when a
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
	// tests: the lane tests running now in their own goroutines (outside the scratch token, one per lane), the
	// step's key by task; testCmd the command each runs now (TestInput.Command; "": none). Under mu.
	tests   map[string]event.Key
	testCmd map[string]string
	// exec: the effects running now, each with when its start began (App.do). obor status' running line lists the
	// command steps among them; an open intent not here is pending. Under mu.
	exec map[event.Key]time.Time
	// restarting: since when serve ends itself on a new obor binary (claimed by the binary follow): a
	// status request answered meanwhile shows "restarting serve" (zero: no self-restart pending).
	restarting time.Time
	// restartClaim: followBinary decided to restart on a new obor binary: admit refuses until it clears (a
	// failed restart). Under mu.
	restartClaim bool
	// held: the tasks whose selected step runs now (from its start's check to its return, App.next), each with the
	// step's name; ended: the tasks whose drop is decided and being recorded (App.drop). Both under mu.
	held  map[string]string
	ended map[string]bool
	// roleFail: when a fresh brief's stop of a role agent last failed, by agent (rolestop.go). Under mu.
	roleFail map[string]time.Time
	// cuts: the steps that wait after an attempt that was not counted, by their intent's key (cutwait.go). Under mu.
	cuts map[event.Key]*cutWait
	// reg, out, night: the identity table (package identity), the lines to the project's agents (package outbox;
	// delivered by messages.go) and the nightly full check (nightly.go): each folds its records itself
	// and keeps live state beside them.
	reg   identity.Registry
	out   outbox.Outbox
	night nightlyState
	// wtAgents: the configured reviewer and deep dive as the config last loaded names them (wtAgentsNow): the role
	// agents whose role worktree serve moves (roleworktree.go). Under mu.
	wtAgents []wtAgent
	// admits: the admissions in force (a config_set, from its wait for the running publish to its commit in: no
	// publish starts meanwhile); published: closed when the running publish ends (nil: none runs). Under mu
	// (roleworktree.go).
	admits    int
	published chan struct{}
}

// folded is a project's state that a replay of its event log alone rebuilds: foldRecord's folds write it, live
// code reads it. Two exceptions, both named where they live: a lanes read ends the watchdog facts (active, woke) of a
// task no lane holds (setLanes: nothing recorded), and lr carries a live claim (its write state). The identity table (identity.Registry), the lines (outbox) and the nightly check (nightlyState) hold folded parts of
// their own beside their live state.
type folded struct {
	active map[string][]event.Record // task → its watchdog facts since the last resumed
	woke   map[string]time.Time      // task → its stopped worker's first wake since the last resumed (stopwake.go)
	// calls: answered verdict calls: the stored reply by <method>.<call id>; verdicts: how many verdict results were
	// folded (a start row holds the count at its start: "a verdict came since its start", rolestop.go).
	calls    map[string]string
	verdicts int
	// refusals: refused calls whose refusal is recorded, by <method>.<call id>: a task_done of an older spec rev
	// (spec.seen), refused again on a retry.
	refusals map[string]string
	loop     map[string]*mtask // the merge loop's tasks, by task
	pause    *event.Pause      // the project's last pause fact (pause.go; nil: none or cleared)
	// starts: each task's newest agent.start record time, intent or result (foldStart; wire.TaskStatus.Started).
	starts map[string]time.Time
	// models: each model's last pause fact (obor pause --model; a cleared one is gone).
	models map[string]*event.Pause
	// Jobs by fetch (fetch.go): delivered, the job id a fact delivered names, the fact's seq and its time (a zero
	// seq: not delivered).
	delivered map[string]idleEnd
	// lr: the lanes table's projection (lanerows.go; its write state is live).
	lr      laneRows
	watches map[string]*mergeWatch // each set's last watch, open or closed (mergewatch.go; current state: openWatch)
	// review: the open review request (review.go; nil: none; held is live).
	review *reviewReq
	// rv: the reviews (started, verified, closed) and the merges, one fold (package review).
	rv review.State
	// sessions: the agents' recorded sessions as one fold in log order (sessions.go).
	sessions store.SessionFold
	// did: the rules' lines by their key (rules.go: a lead line's Of) and when each was recorded.
	did map[event.Of]time.Time
	// idleAck: the lead's last acknowledgement of an idle state (idle.go; zero: none).
	idleAck idleAck
	// ends: the records that ended work idleNow waits on besides a task's merge or drop, so the idle key and
	// its age are the newest of them: briefEnds, each role worktree agent's last brief end (its finished line, or the
	// stop that ended it); reviewIdleEnd, the last review
	// request's end (a failed --after end, a --rm or a firing: every end, so idle does not read the marker
	// below); reviewEnd, the last end that suppresses the on-merge rule (a --rm or a firing, not a failed --after
	// end).
	briefEnds     map[string]idleEnd
	reviewIdleEnd idleEnd
	reviewEnd     idleEnd
	// versions: each tool's newest version fact, by tool (versions.go).
	versions map[string]versionAt
}

// newFolded is the folded state before the log's first record.
func newFolded() folded {
	return folded{active: map[string][]event.Record{}, woke: map[string]time.Time{}, calls: map[string]string{},
		refusals: map[string]string{}, loop: map[string]*mtask{}, starts: map[string]time.Time{}, delivered: map[string]idleEnd{}, watches: map[string]*mergeWatch{}, did: map[event.Of]time.Time{},
		briefEnds: map[string]idleEnd{}, versions: map[string]versionAt{}, lr: laneRows{rows: map[string]store.LaneRow{}, submitted: map[string]int64{}}}
}

// watch is what the watchdogs know of one open task beyond its facts. A new task, a replay and a new lane start one
// afresh: no timer inherits time from before it.
type watch struct {
	lane       port.Lane
	agent      port.Pane // the last pane an agent ran in while the lane held the task (ID "": none yet)
	lost       time.Time // since when the lane's panes run no agent (zero: one runs)
	quietTimer           // the stall timer's quiet time
}

// unread: w's lane could not be read at the last check (unknown): the watchdog keeps its task and facts and
// observes nothing there until a read succeeds.
func (w *watch) unread() bool { return w.lane.State == port.LaneUnknown }

// forget drops w's elapsed-time baselines (the quiet time, the stall's escalation counting from it, lost):
// an unknown or stale observation of its lane, so the next good read starts new ones and no unobserved interval
// counts. Its facts stay; seenWrite stays, a watermark, not a time counted.
func (w *watch) forget() {
	w.quietSince, w.lost = time.Time{}, time.Time{}
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
