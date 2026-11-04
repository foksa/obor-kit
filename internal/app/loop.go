package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"obor/internal/app/event"
	"obor/internal/app/port"
	"strconv"
	"strings"
	"time"

	"obor/internal/app/wire"
)

// The merge loop (docs/state-model.md rows 16–38, rules 3–5). A worker's task_done submits its task; submit
// pins the lane's commit and the gate checks it in the scratch (Scratch: serve's own worktree); a green candidate is a review job for the integrator, whose verdict accepts, sends back or
// rejects it; an accepted candidate is published, then the after-merge work runs. A red gate sends the task back to
// its worker (K times, then the owner decides), a conflict is a resolve job for the integrator, a moved target
// re-gates (an identical patch keeps the accept). Each step is a record first: replay folds them into each task's
// state (foldLoop) and the loop goes on from there; settle closes what a crash left open, and each step's effect is
// a function of durable inputs, safe to run again after a crash at any point in it (docs/state-model.md "Crash
// classes").
//
// A job for the integrator has timers (watchJob): undelivered, idle with it, or the integrator gone, it is prompted once more, then the owner is
// told. The integrator holds a job for the owner with its hold tool; obor resume ends a needs-you (re-gate, re-prompt, or
// retry a failed step). Every effect follows one failure rule (App.do).

// Loop is what the merge loop drives in one project.
type Loop struct {
	Scratch port.Scratch
	Agents  port.Agents
	// Typer types job wakes and lines into agents' panes (nil: none typed; posted ones need Inbox).
	Typer port.Typer
	// Lane prepares a dispatched lane (and frees a dropped one); Specs is what a dispatch reads; Kit is the
	// obor kit (the worker prompt's template). Without Lane and Specs the app dispatches nothing (dispatch.go).
	Lane  port.LaneSetup
	Specs Specs
	Kit   string
	// Checkout is nil while the main checkout is on the target branch, else why the project's pass pauses: a loop
	// needs-you, nothing gated, published or dispatched until it is back (T-425; nil: never paused).
	Checkout func(ctx context.Context) error
	// Nightly is the project's nightly full check (merge.nightly_at; off is At "", T-464, T-782; nil: a Loop without
	// one, tests).
	Nightly *Nightly
	// Disk is the project volume's free space (nil: not watched, T-564).
	Disk *Disk
	// Review is the git of a review request's start (review.go; nil: no review is started, T-638).
	Review ReviewGit
	// Inbox posts the project's lines to its lead into the lead's Claude session (obor send, a brief's finished
	// line; nil: none posted, T-485): nothing is typed into the pane the owner writes in.
	Inbox Poster
	// Restart restarts this serve on the new build (T-652, T-656): after a green after_merge of a project with
	// commands.after_merge.restart_serve, and on a new obor binary (Options.Binary, T-719). The adapter ends serve as
	// a signal does (it cancels; then launchd starts it again, or serve execs the kit's shim itself) and returns nil
	// once it did. Nil: never restarted.
	Restart func(ctx context.Context) error
	// Config is the main checkout's config file and its git: the owner's setup edits (configset.go, T-753; nil: none).
	Config ConfigFile
}

// K is how often a task is sent back before the owner decides (rule 3).
const K = 2

// The merge loop's steps are in internal/app/event (the integrator's jobs: StepReview, StepResolve; its verdicts:
// <method>.<call id>); these are the prefixes of its own.
const (
	stepStart = "start." // intent/result (S) start.<job step>[.N]: the integrator started for that job
	stepRetry = "retry." // prompt intent (P) retry.<job step>: the job again, not delivered in time
	stepNudge = "nudge." // prompt intent (P) nudge.<job step>: the job again, the integrator idle with it
)

// The causes of the merge loop's needs-you (FactData.Cause); resumeDefault is what obor resume does with each by
// default (none: the owner says which, obor resume --regate or --sendback: a red gate or sendbacks past K, a resolve
// rejected). A step that failed tries times is cause "<step> failed" (its key without .N):
// obor resume retries it by default.
const (
	causeUndelivered     = "job not delivered"
	causeStalled         = "job stalled"
	causeRed             = "red"
	causeGate            = "gate"
	causePublish         = "publish"
	causeSendbacks       = "sendbacks"
	causeResolveRejected = "resolve rejected"
	causeFix             = "fix job not sent"
	causeRejected        = "rejected"
	// causeOpen: an older log's hold of a report's open questions before the review job (T-604, reversed by T-649):
	// the fold ends it, and the task gets its review job.
	causeOpen    = "open questions"
	failedSuffix = " failed"
)

var resumeDefault = map[string]string{
	event.CauseHeld: wire.ResumeReprompt, causeUndelivered: wire.ResumeReprompt, causeStalled: wire.ResumeReprompt,
	causeGate: wire.ResumeRegate, causePublish: wire.ResumeRegate,
}

// resumeDefaultOf is what obor resume does by default with a needs-you of cause ("": the owner says which).
func resumeDefaultOf(cause string) string {
	if strings.HasSuffix(cause, failedSuffix) {
		return wire.ResumeRetry
	}
	return resumeDefault[cause]
}

// afterSteps are a merged task's steps, in order. An older log's dispatch.next (the legacy queue dispatch, T-408) and
// archive (the done file's, before T-437) are none of them: their results fold as no step's.
var afterSteps = []string{event.StepAfterMerge, event.StepStop}

// tries is how many counted failures of one step make a needs-you (the failure rule: App.do).
const tries = 3

// The states of a merge-loop task.
const (
	stSubmitted  = "submitted"           // submit it (pin the lane's commit), then gate it
	stGreen      = "green"               // gate green: keep the accept, or a review job
	stAwaiting   = "awaiting-accept"     // the review job is out
	stRed        = "red"                 // gate red: a sendback, or the owner after K
	stConflict   = wire.ScratchConflict  // the merge conflicts in the scratch: a resolve job
	stResolving  = wire.ScratchResolving // the resolve job is out
	stResolved   = wire.ScratchResolved  // gate the merge the integrator committed in the scratch
	stFail       = "fail"                // the owner decides (why)
	stAccepted   = "accepted"            // publish it
	stMoved      = "moved"               // gate the candidate again on the new target
	stPublished  = "published"           // the target holds the candidate
	stMerged     = "merged"              // terminal (then after_merge)
	stRejected   = "rejected"            // terminal; the owner is told
	stDropped    = "dropped"             // terminal; the lead abandoned it (obor drop)
	stWaiting    = "waiting"             // a dispatch waiting for its After tasks to merge (readiness rule 1)
	stDispatched = "dispatched"          // a dispatch's steps: lane.prepare, agent.start, the work job
	stWorking    = "working"             // the work job delivered, or sent back: the fix job; then task_done
	stNeedsYou   = "needs-you"           // the owner decides; a task_done is the next attempt
	stDriving    = wire.TaskDriving      // the owner's drive agent works on it (T-751): no jobs, no watchdog; obor land submits it
)

// mtask is one task in the merge loop, folded from its records.
type mtask struct {
	task, lane, agent string
	attempt           int
	state             string
	sendbacks         int
	attemptVars                    // what a new attempt resets (next)
	resolved          string       // stResolved: the resolved merge's sha, pinned at refs/obor/resolved/<task>
	why, cause        string       // stFail: the needs-you's reason and cause
	report            string       // the last submission's report (task_done's)
	after             int          // stMerged: how many afterSteps have a result (all: a merge outside the app did them)
	head              string       // stMerged: the merge's sha (mergedData.Head; "": a merge outside the app)
	mergedSeq         int64        // stMerged: the merged fact's seq (a watched set's last merge, mergewatch.go)
	dropped           string       // stDropped: obor drop's why
	droppedSeq        int64        // stDropped: the dropped fact's seq (the idle line's last end, T-803)
	needs             event.Record // the needs-you fact in force (Seq 0: none)
	left              string       // the state the last needs-you left: obor resume's retry goes back to it
	// A dispatched attempt (dispatch.go): the fact's data, time and seq (a waiting task: its waiting fact's data and
	// seq, which orders the waits; a submission keeps the seq: the lane's current task is its newest dispatch's, T-615); lane.prepare's
	// base (""), its refusal or agent.start's (refused, cause); the step counters (the last N used); agent.start done.
	disp                    dispatchData
	at                      time.Time
	dispSeq                 int64
	base, refused           string
	prepares, starts, works int
	started                 bool
	startAt                 time.Time // when the last start that started its agent was written
	specRev                 int       // the spec rev the worker's last fetched job carried, or the newer one a task_done refusal showed it (0: none)
	// drive: the owner drives the task (obor drive, T-751): every attempt's, from its dispatch or its take-over on; a red
	// gate, a sendback or a hold go back to driving, never to a worker.
	drive bool
}

// attemptVars are the fields of mtask a new attempt resets: next replaces them in one assignment.
type attemptVars struct {
	gates, reviews int // the step counters of the attempt (the last N used)
	resolves, pubs int
	submits        int
	input          string             // submit's sha, the lane gate's and the moved gate's input ("": submit first)
	cand           port.Candidate     // the attempt's last green candidate
	gateRev        int                // the spec rev in cand's merge message (T-521; 0: none)
	accepted       string             // the patch hash the integrator accepted in this attempt
	line           string             // the last gate or publish line, or the integrator's note
	kind           string             // the attempt's last gate's kind: lane, resolved, moved
	job            string             // the attempt's last integrator job's step (review, resolve.2 …; "": none yet)
	patch          string             // the patch hash that job was prompted with (its intent's JobData)
	delivered      bool               // the integrator took that job (fact delivered)
	lost           int                // how often the integrator exited with a job open since the last resume
	fixed          bool               // stWorking: the fix job of this attempt is out
	answer         bool               // stWorking: this attempt's fix job is the answer to the worker's question (an older log's obor resume)
	decision       string             // the lead's or the owner's text of this attempt's last obor resume (its next job carries it)
	fixes          int                // the attempt's last fix job's N (fix, fix.2 … after obor resume)
	fails          map[string]failure // counted failures per step key since the attempt began or the last resume
	work           string             // a dispatched attempt's work job step ("": not sent in this attempt, or sent anew after obor resume)
	lined          bool               // stDriving: the lead's line that the drive came back (line: why) is recorded (T-751)
}

// failure is one step's counted failures in an attempt: how many, and the last one's reason and time (obor status'
// pending line names them, T-662).
type failure struct {
	n   int
	at  time.Time
	why string // the failed fact's reason: "<step>: <err>"
}

// Records' data.
type (
	submitData struct {
		Lane  string `json:"lane"`
		Agent string `json:"agent,omitempty"`
		Pane  string `json:"pane,omitempty"`
		Call  string `json:"call,omitempty"` // task_done's call id ("": a done file's watch, before T-437)
		// Report is task_done's report ("": a done file's submission, before T-437).
		Report string `json:"report,omitempty"`
		// Land: obor land's submission of the owner's drive (T-751), Report its summary, Call its call id.
		Land bool `json:"land,omitempty"`
	}
	stopData struct {
		Lane  string `json:"lane"`
		Agent string `json:"agent,omitempty"`
	}
	gateIn struct {
		Kind  string `json:"kind"` // lane, resolved, moved
		Lane  string `json:"lane"`
		Input string `json:"input,omitempty"` // the sha the gate merges (resolved: checks)
	}
	submitOut struct {
		SHA     string `json:"sha,omitempty"`
		Refused string `json:"refused,omitempty"` // Scratch.Submit's final refusal
	}
	gateOut struct {
		Outcome   string         `json:"outcome"` // a gate's green, red, conflict, fail; a publish's merged, moved, hold
		Line      string         `json:"line"`
		Candidate port.Candidate `json:"candidate"`
		SpecRev   int            `json:"spec_rev,omitempty"` // a gate's: the spec rev in its merge's message (T-521)
	}
	noteData struct {
		Note string `json:"note"`
	}
)

// nth is step's N-th key within an attempt: step, step.2, step.3 …
func nth(step string, n int) string {
	if n <= 1 {
		return step
	}
	return step + "." + strconv.Itoa(n)
}

// wake kicks the merge loop.
func (a *App) wake() {
	select {
	case a.kick <- struct{}{}:
	default:
	}
}

// mergeLoop runs a pass (advance) at each kick until ctx ends; at its end it waits for the scratch effects still running (ctx cut them: their intents stay pending).
func (a *App) mergeLoop(ctx context.Context) {
	defer a.effects.Wait()
	for {
		select {
		case <-ctx.Done():
			return
		case <-a.kick:
		}
		a.advance(ctx)
		a.notify(ctx)
		a.mu.Lock()
		a.passes++
		a.mu.Unlock()
	}
}

// advance is one pass over the project when it merges: each task driven as far as it goes now, in passOrder (the
// waiting ones last: a task they wait for may merge in the same pass). It needs a herdr snapshot (the integrator's pane).
func (a *App) advance(ctx context.Context) {
	a.mu.Lock()
	p := a.p
	run := p != nil && p.merging && a.connected
	a.mu.Unlock()
	if p != nil && a.followBinary(ctx, p) { // a project with no merge loop follows a new binary too
		return // serve ends now
	}
	if !run {
		return
	}
	fx, err := a.opt.Loop(p.root)
	if err == nil {
		a.deliver(ctx, p, fx)   // lines need no checkout on the target branch (RV-50 #2)
		a.stopWakes(ctx, p, fx) // so do a stopped worker's wake and alarm (RV-60 #2)
	}
	if err == nil && fx.Checkout != nil {
		err = fx.Checkout(ctx)
	}
	a.loopStatus(ctx, p, &p.badLoop, err)
	if err != nil {
		a.logf("merge loop of %s: %v", p.name, err)
		return
	}
	a.mu.Lock()
	p.dispatching = fx.Lane != nil && fx.Specs != nil
	tasks := p.passOrder()
	a.mu.Unlock()
	a.diskTick(p, fx)
	for _, task := range tasks {
		if ctx.Err() != nil {
			return
		}
		a.drive(ctx, p, fx, task)
	}
	a.nightly(ctx, p, fx)
	a.reviewPass(ctx, p, fx)
	a.reviewDueTick(ctx, p, fx)
	a.stopRoles(ctx, p, fx)
}

// record appends the fact r with data and folds it; false when the log failed.
func (a *App) record(p *proj, r event.Record, data any) bool {
	_, ok := a.recordSeq(p, r, data)
	return ok
}

// recordSeq is record, also returning the record's seq (0 when the log failed).
func (a *App) recordSeq(p *proj, r event.Record, data any) (int64, bool) {
	r.Data, _ = json.Marshal(data) // plain structs: cannot fail
	r, err := p.log.Append(r, a.opt.Now())
	if err != nil {
		a.logf("FAIL event log of %s: %v", p.name, err)
		return 0, false
	}
	a.mu.Lock()
	p.foldRecord(r)
	if r.Kind == event.KindFact && (r.Step == event.StepMerged || r.Step == event.StepDropped) {
		a.settleEnded(p, r.Task) // the task ended: its pending intents never run
	}
	err = p.laneRowsErr()
	a.mu.Unlock()
	a.logf("%s %s %s (attempt %d)", p.name, r.Task, r, r.Attempt)
	if err != nil {
		a.logf("FAIL lane rows of %s: %v", p.name, err)
		return 0, false
	}
	return r.Seq, true
}

// do runs effect under the log's Do for in and folds its intent (once written: the effect ran) and its result. An
// effect that fails, or a ctx that ended (obor serve stops: nothing is decided on a cut-off effect), leaves the
// intent pending. A key with a result folds that again (folding is idempotent); an intent already in the log is not
// folded again (a job's intent before its start: a verdict given since stays).
//
// The failure rule (every merge-loop effect; the table in docs/state-model.md "Crash classes"): an effect returns a
// result only for an outcome final for its step. Anything else is an error: no result, the step stays pending and is
// tried again on the next pass; failed counts it, and after tries counted failures the owner is told.
//
// An effect that ran and whose result's write failed is never run again in this process: the next call writes the
// result it had (p.unwritten). A restart has only the log: the intent's crash class settles it.
//
// From the moment the effect runs until it returns its key is p.exec: obor status' running line lists those, and every
// other open intent as pending (T-662).
func (a *App) do(ctx context.Context, p *proj, in event.Intent, effect func(context.Context) (any, error)) (json.RawMessage, error) {
	data, _ := json.Marshal(in.Data)
	intent := event.Record{Task: in.Task, Attempt: in.Attempt, Step: in.Step, Kind: event.KindIntent, Class: in.Class, Data: data}
	a.mu.Lock()
	raw, lost := p.unwritten[in.Key]
	a.mu.Unlock()
	if lost {
		r, err := p.log.Append(event.Record{Task: in.Task, Attempt: in.Attempt, Step: in.Step, Kind: event.KindResult, Data: raw},
			a.opt.Now())
		if err != nil {
			a.logf("%s %s %s: its result: %v", p.name, in.Task, in.Step, err)
			return nil, err
		}
		a.mu.Lock()
		defer a.mu.Unlock()
		delete(p.unwritten, in.Key)
		p.foldRecord(r)
		return raw, p.laneRowsErr()
	}
	_, pending := p.log.Intent(in.Key)
	logged := pending || p.log.Done(in.Key) // folded already: folded again, it would undo what came after it
	ran, done := false, false
	raw, err := p.log.Do(ctx, in, a.opt.Now, func(ctx context.Context) (any, error) {
		if !logged {
			a.mu.Lock() // before the effect: a verdict on a job the prompt just delivered finds it open
			p.foldRecord(intent)
			a.mu.Unlock()
		}
		ran = true
		a.mu.Lock() // from here the effect runs: obor status' running line shows it, elapsed from now (T-662)
		p.exec[in.Key] = a.opt.Now()
		a.mu.Unlock()
		defer func() {
			a.mu.Lock()
			delete(p.exec, in.Key)
			a.mu.Unlock()
		}()
		v, err := effect(ctx)
		if err == nil && ctx.Err() != nil {
			err = ctx.Err()
		}
		done = err == nil
		return v, err
	})
	a.mu.Lock()
	defer a.mu.Unlock()
	if !ran && err == nil && !logged { // the stored result
		p.foldRecord(intent)
	}
	if err != nil && done { // the effect happened; only its result is not in the log
		p.unwritten[in.Key] = raw
	}
	if err != nil {
		a.logf("%s %s %s: %v", p.name, in.Task, in.Step, err)
		return nil, err
	}
	p.foldRecord(event.Record{Task: in.Task, Attempt: in.Attempt, Step: in.Step, Kind: event.KindResult, Data: raw,
		TS: a.opt.Now().UTC().Format(time.RFC3339)}) // as the log wrote it (recordTime)
	if err := p.laneRowsErr(); err != nil { // the result is in the log: the next pass finds it done and proceeds once the rows are written
		a.logf("%s %s %s: lane rows: %v", p.name, in.Task, in.Step, err)
		return nil, err
	}
	return raw, nil
}

// cutByServe: err is serve's own cancellation (its ctx ended: a restart, a stop), so the effect was cut off, not
// failed (T-660): nothing is recorded as a failure or needs-you, and the intent stays pending for the next serve's
// replay, as after a crash.
func cutByServe(ctx context.Context, err error) bool {
	return ctx.Err() != nil || errors.Is(err, context.Canceled)
}

// shutDown logs the one line a failure or needs-you leaves behind when serve is stopping (T-660) instead of being
// recorded: nothing was decided on the cut-off effect.
func (a *App) shutDown(what string) {
	a.logf("shutting down: %s not recorded", what)
}

// failed counts err, the failure of mt's step (the failure rule: do): fact failed{step: why}, so a restart keeps the
// count. The tries-th is needs-you{<step> failed: why}; while it is in force the step is not tried, and obor resume
// ends it and the count. An attempt cut off by ctx or by the loss of the herdr connection is not counted, nor herdr's
// agent_not_found within startSettle of the start of mt's agent (herdr may not know a new agent's name yet). The task
// waits either way.
func (a *App) failed(ctx context.Context, p *proj, mt mtask, step string, err error) bool {
	if cutByServe(ctx, err) { // serve is stopping: the step was cut, not failed (T-660)
		a.shutDown(mt.task + " " + event.StepFailed + "{" + step + ": " + err.Error() + "}")
		return false
	}
	a.mu.Lock()
	cut := !a.connected || a.p != p
	a.mu.Unlock()
	if port.ErrorCode(err) == "agent_not_found" && !mt.startAt.IsZero() && a.opt.Now().Sub(mt.startAt) < startSettle {
		cut = true
	}
	if cut || !a.record(p, event.Record{Task: mt.task, Attempt: mt.attempt, Step: event.StepFailed, Kind: event.KindFact},
		event.FactData{Lane: mt.lane, Machine: Machine, Reason: step + ": " + err.Error(), Merge: true, Step: step}) {
		return false
	}
	a.mu.Lock()
	m := p.folded.loop[mt.task]
	tell := m.attempt == mt.attempt && m.fails[step].n >= tries && m.needs.Seq == 0
	a.mu.Unlock()
	if tell {
		base, _ := event.StepN(step)
		a.needsYou(p, &mt, base+failedSuffix, step+failedSuffix+": "+err.Error())
	}
	return false
}

// startSettle is how long after an agent's start herdr's agent_not_found for it is transient (failed): herdr's own
// startup bound.
const startSettle = 30 * time.Second

// loopStatus notes that a pass over p failed (err) or did not (nil) at one source (the loop's adapters, the lanes
// read): a failure is recorded as a needs-you "loop: <error>" once per distinct error (the owner is notified, the
// lead has a reason), and it ends with the first pass in which no source fails. A pass cut by serve's own stop
// records nothing (T-660): it was not a failure, and the next serve's pass decides again.
func (a *App) loopStatus(ctx context.Context, p *proj, src *bool, err error) {
	if err != nil && cutByServe(ctx, err) {
		a.shutDown("loop " + event.StepNeedsYou + "{loop: " + err.Error() + "}")
		return
	}
	a.loopMu.Lock()
	defer a.loopMu.Unlock()
	reason := ""
	a.mu.Lock()
	*src = err != nil
	if err != nil {
		reason = "loop: " + err.Error()
	}
	ok := !p.badLoop && !p.badLanes
	a.mu.Unlock()
	if err == nil && !ok { // another source still holds the loop: nothing to end yet
		return
	}
	a.settleNeeds(p, event.LoopTask, reason)
}

// settleNeeds makes reason the one needs-you in force for task on p: "" ends the one in force (resumed); the same
// reason already in force, or "" with none in force, is nothing; another reason ends the old one first. True when the
// log took it (or there was nothing to record), false when a record was refused (the caller may try again).
func (a *App) settleNeeds(p *proj, task, reason string) bool {
	a.mu.Lock()
	held, same := len(p.folded.active[task]) > 0, reason != "" && p.has(task, event.StepNeedsYou, reason)
	a.mu.Unlock()
	switch {
	case same, reason == "" && !held:
		return true
	case held && !a.record(p, event.Record{Task: task, Step: event.StepResumed, Kind: event.KindFact}, event.FactData{Machine: Machine}):
		return false
	case reason != "":
		return a.record(p, event.Record{Task: task, Step: event.StepNeedsYou, Kind: event.KindFact}, event.FactData{Machine: Machine, Reason: reason})
	}
	return true
}

// needsYou records the merge loop's needs-you for mt: the owner decides (obor resume, by cause).
func (a *App) needsYou(p *proj, mt *mtask, cause, reason string) bool {
	return a.record(p, event.Record{Task: mt.task, Attempt: mt.attempt, Step: event.StepNeedsYou, Kind: event.KindFact},
		event.FactData{Lane: mt.lane, Machine: Machine, Reason: reason, Merge: true, Cause: cause})
}

// drive takes task as far as it goes now: one step after another until it waits for someone else (a verdict, a
// task_done, the owner) or a step fails (the next pass tries again).
func (a *App) drive(ctx context.Context, p *proj, fx *Loop, task string) {
	a.mu.Lock()
	mt := *p.folded.loop[task]
	running := p.running.Task == task
	a.mu.Unlock()
	if running {
		return // its gate or publish runs: the task goes on once that has folded
	}
	if states[mt.state].landed {
		// A merge outside the app (the owner's, by hand in git): merged, no second publish. Git that cannot tell:
		// the task waits for the next pass.
		landed, err := fx.Scratch.Landed(ctx, task)
		if err != nil {
			a.logf("merge loop of %s: %s: %v", p.name, task, err)
			return
		}
		if landed {
			a.record(p, event.Record{Task: task, Attempt: mt.attempt, Step: event.StepMerged, Kind: event.KindFact}, event.MergedData{Outside: true})
			return
		}
	}
	for i := 0; i < maxSteps && ctx.Err() == nil; i++ {
		a.mu.Lock()
		step := a.next(p, p.folded.loop[task])
		a.mu.Unlock()
		if step == nil || !step(ctx, fx) {
			return
		}
	}
}

// maxSteps bounds one drive (a task's longest run of steps is about ten): a step whose record did not move the
// state cannot spin.
const maxSteps = 32

// step is one step of a task, decided under mu and run outside it; false: the task waits.
type step func(ctx context.Context, fx *Loop) bool

// next is mt's next step (under mu), nil when it waits. A selected step starts only after begin's atomic check (one
// rule for every kind, RV-42 #1): its task/attempt is still current and not ended (dropped, or a drop of it decided:
// p.ended); the same check holds the task (p.held) until the step returns, and a drop is refused meanwhile. Drop
// decides under the same lock (App.drop), so no effect starts after DROPPED, and none is still starting when DROPPED
// is returned.
func (a *App) next(p *proj, m *mtask) step {
	s := a.pick(p, m)
	if s == nil {
		return nil
	}
	mt := *m
	return func(ctx context.Context, fx *Loop) bool {
		if !a.begin(p, mt) {
			return false
		}
		defer a.release(p, mt.task)
		return s(ctx, fx)
	}
}

// begin checks that mt (as selected) is still p's current attempt, not dropped and not claimed (proj.claim), and holds it: false, the
// step does not start. The lane rows are written first (RV-48 #1): a step whose record moved a row does not run on a
// missing or stale row, so a failing write holds every step until it is repaired.
func (a *App) begin(p *proj, mt mtask) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	m := p.folded.loop[mt.task]
	if m == nil || m.attempt != mt.attempt || m.state == stDropped || p.claim(mt.task) != "" {
		return false
	}
	if p.laneRowsErr() != nil {
		return false
	}
	p.held[mt.task] = mt.stepName()
	return true
}

// release ends begin's hold of task.
func (a *App) release(p *proj, task string) {
	a.mu.Lock()
	delete(p.held, task)
	a.mu.Unlock()
}

// stepName names the step pick selects for mt, for a refused drop's reply.
func (mt mtask) stepName() string {
	if f := states[mt.state].name; f != nil {
		return f(mt)
	}
	return "a step (" + mt.state + ")"
}

// pick is mt's next step (under mu), nil when it waits: its state's pick.
func (a *App) pick(p *proj, m *mtask) step {
	if f := states[m.state].pick; f != nil {
		return f(a, p, *m)
	}
	return nil
}

// pickGate submits the lane (submitted, no input yet), else gates it: the lane, the resolved merge or the moved
// candidate (rows 16, 18). Another task's merge open in the scratch, or another gate or publish there: it waits
// (scratchWait; the submit needs no scratch, only no open merge).
func (a *App) pickGate(p *proj, mt mtask) step {
	holder, why := p.scratchWait(mt)
	if holder != "" && !runs(why) {
		return nil // a merge is open in the scratch: a gate would reset it
	}
	if mt.state == stSubmitted && mt.input == "" {
		return func(ctx context.Context, fx *Loop) bool { return a.submit(ctx, p, fx, mt) }
	}
	if holder != "" {
		return nil // another gate or publish holds the scratch
	}
	return func(ctx context.Context, fx *Loop) bool {
		if a.diskIsLow(p, fx) {
			return false // it would only fail; it starts once space is back
		}
		return a.scratchStep(ctx, p, mt, nth(event.StepGate, mt.gates+1), func(ctx context.Context) { a.gate(ctx, p, fx, mt) })
	}
}

// pickGreen keeps the accept bound to the patch (row 20), else sends a review job (row 19): a report's open questions
// go with it, for the integrator to decide or hold (T-649).
func (a *App) pickGreen(p *proj, mt mtask) step {
	if mt.accepted != "" && mt.accepted == mt.cand.Patch {
		return func(context.Context, *Loop) bool {
			return a.record(p, event.Record{Task: mt.task, Attempt: mt.attempt, Step: event.StepKept, Kind: event.KindFact},
				JobData{Patch: mt.cand.Patch})
		}
	}
	return func(ctx context.Context, fx *Loop) bool {
		return a.job(ctx, p, fx, mt, nth(event.StepReview, mt.reviews+1))
	}
}

// pickConflict sends a resolve job (row 23).
func (a *App) pickConflict(p *proj, mt mtask) step {
	return func(ctx context.Context, fx *Loop) bool {
		return a.job(ctx, p, fx, mt, nth(event.StepResolve, mt.resolves+1))
	}
}

// pickRed sends the task back (row 21), or to the owner after K (row 22); a drive back to driving, always (T-751).
func (a *App) pickRed(p *proj, mt mtask) step {
	if mt.sendbacks < K || mt.drive {
		return func(context.Context, *Loop) bool {
			return a.record(p, event.Record{Task: mt.task, Attempt: mt.attempt, Step: event.StepSendback, Kind: event.KindFact},
				noteData{Note: mt.line})
		}
	}
	return func(context.Context, *Loop) bool {
		return a.needsYou(p, &mt, causeRed, fmt.Sprintf("gate red after %d sendbacks: %s", K, mt.line))
	}
}

// pickFail goes to the owner (row 22), unless a merge outside the app took the lane meanwhile (row 34).
func (a *App) pickFail(p *proj, mt mtask) step {
	return func(ctx context.Context, fx *Loop) bool {
		landed, err := fx.Scratch.Landed(ctx, mt.task)
		switch {
		case err != nil:
			a.logf("merge loop of %s: %s: %v", p.name, mt.task, err)
			return false
		case landed: // the gate refused a lane a merge outside the app took meanwhile
			return a.record(p, event.Record{Task: mt.task, Attempt: mt.attempt, Step: event.StepMerged, Kind: event.KindFact},
				event.MergedData{Outside: true})
		}
		return a.needsYou(p, &mt, mt.cause, mt.why)
	}
}

// pickAccepted publishes the candidate (row 33) once the scratch is free, no start reading the main checkout is
// admitted (T-730) and no reviewer or deep dive holds the merge (holdPublish, T-718). These are a cheap filter at
// selection: scratchStep decides the admissions and the hold (its cap and the lead's line) again at the publish's
// actual start, under the same lock that sets p.running (T-733).
func (a *App) pickAccepted(p *proj, mt mtask) step {
	if holder, _ := p.scratchWait(mt); holder != "" || p.admits > 0 || a.holding(p, mt.task) {
		return nil
	}
	return func(ctx context.Context, fx *Loop) bool {
		return a.scratchStep(ctx, p, mt, nth(event.StepPublish, mt.pubs+1), func(ctx context.Context) { a.publish(ctx, p, fx, mt) })
	}
}

// pickPublished records the merge (row 34).
func (a *App) pickPublished(p *proj, mt mtask) step {
	return func(context.Context, *Loop) bool {
		return a.record(p, event.Record{Task: mt.task, Attempt: mt.attempt, Step: event.StepMerged, Kind: event.KindFact},
			event.MergedData{Head: mt.cand.SHA})
	}
}

// pickJob sends an open job again whose prompt failed, else watches it (rows 7–8, 31).
func (a *App) pickJob(p *proj, mt mtask) step {
	if p.log.Open(mt.jobKey()) { // its prompt failed: sent again under its key
		return func(ctx context.Context, fx *Loop) bool { return a.job(ctx, p, fx, mt, mt.job) }
	}
	return a.watchJob(p, mt)
}

// pickMerged runs the next after-merge step (rows 37–38).
func (a *App) pickMerged(p *proj, mt mtask) step {
	if mt.after >= len(afterSteps) || mt.needs.Seq != 0 {
		return nil // done, or a step's needs-you: no more tries until obor resume
	}
	return func(ctx context.Context, fx *Loop) bool { return a.afterStep(ctx, p, fx, mt) }
}

// pickWorking sends the fix job of a sent-back attempt and watches its delivery (row 24); a dispatched attempt waits
// for its task_done.
func (a *App) pickWorking(p *proj, mt mtask) step {
	fix := event.Key{Task: mt.task, Attempt: mt.attempt, Step: nth(event.StepFix, max(mt.fixes, 1))}
	switch {
	case mt.work != "":
		return nil // a dispatched attempt: its task_done submits it
	case mt.agent == "":
		return func(context.Context, *Loop) bool {
			return a.needsYou(p, &mt, causeFix, fmt.Sprintf("fix job %s not sent: the lane names no worker agent",
				JobID(fix)))
		}
	case !mt.fixed:
		fix.Step = nth(event.StepFix, mt.fixes+1)
	case !p.log.Open(fix):
		return a.watchFix(p, mt, fix) // the same delivery timers as the work job's
	}
	return func(ctx context.Context, fx *Loop) bool { return a.fix(ctx, p, fx, mt, fix) }
}

// pickRejected deletes the pins, then tells the owner (row 29).
func (a *App) pickRejected(p *proj, mt mtask) step {
	switch {
	case mt.needs.Seq != 0:
		return nil
	case !p.log.Done(event.Key{Task: mt.task, Attempt: mt.attempt, Step: event.StepUnpin}):
		return func(ctx context.Context, fx *Loop) bool { return a.unpin(ctx, p, fx, mt) }
	}
	return func(context.Context, *Loop) bool {
		return a.needsYou(p, &mt, causeRejected, "rejected by the integrator: "+mt.line)
	}
}
