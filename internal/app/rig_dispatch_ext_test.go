package app_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"obor/internal/app"
	"obor/internal/app/event"
	"obor/internal/app/port"
	"obor/internal/app/wire"
	"obor/internal/config"
	"obor/internal/herdr"
	"obor/internal/herdr/herdrtest"
	"obor/internal/testkit"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

// briefs is the open briefs obor status shows.
func (r *loopRig) briefs() []wire.BriefStatus { return r.app.Status().Project.Briefs }

// agentGone changes pane by fn and waits until obor status no longer shows agent.
func (r *loopRig) agentGone(pane, agent string, fn func(p *port.Pane)) {
	r.t.Helper()
	r.setPane(pane, fn)
	r.waitFor(agent+" gone", func() bool {
		for _, a := range r.app.Status().Project.Agents {
			if a.Name == agent {
				r.push()
				return false
			}
		}
		return true
	})
}

// Serve's lines and job wakes reach a running agent as its kind takes them (agents.Kind.Deliver): a
// Claude session gets them posted to its inbox (nothing typed into its pane, no ctrl+u), Codex and OpenCode panes get
// them typed into theirs. A Claude session with no inbox record: a line stays queued, a job wake is typed (the fallback).

// keysTo is the agent.send_keys requests to agent.
func (r *loopRig) keysTo(agent string) int {
	n := 0
	for _, req := range r.srv.Requests() {
		var p struct{ Target string }
		_ = json.Unmarshal(req.Params, &p)
		if req.Method == "agent.send_keys" && p.Target == agent {
			n++
		}
	}
	return n
}

// obor dispatch <T-xx> takes its models from the spec's Difficulty level (config difficulty, built-in
// default), the spec's Model: line first: the first model not paused, on the first free lane (every lane is pooled);
// no free lane: the task waits for one; every model of the request paused: it waits, the reply says why. The rig has
// lanes L-01 and L-02, both free.

// holdPrepare holds every lane prepare before it runs, so a task stays dispatched; a new drive's reply, which waits
// for its lane's prepare, gives up after a second (of the bubble's clock). Call it before start.
func (r *loopRig) holdPrepare() {
	r.disp.set(func(d *dispatchFakes) { d.cut["prepare"] = "before" })
	r.driveWait = time.Second
}

// dispatchLevel is the lead's dispatch of T-1 by its spec's level (no lane, no types).
func (r *loopRig) dispatchLevel(callID string, types ...string) (string, error) {
	return r.dispatchCall(wire.DispatchReq{Envelope: wire.Envelope{Root: r.p.Root, Agent: "pj-lead", Role: "lead",
		CallID: callID}, Task: "T-1", Types: types})
}

func (r *loopRig) specLevel(level string) {
	r.t.Helper()
	r.setSpec("s-"+level, "# T-1 — Leveled\n\nDifficulty: "+level+"\n")
}

// factData is the data of task's first fact of step.
func (r *loopRig) factData(task, step string) map[string]any {
	r.t.Helper()
	for _, rec := range r.records() {
		if rec.Task == task && rec.Kind == event.KindFact && rec.Step == step {
			var d map[string]any
			if err := json.Unmarshal(rec.Data, &d); err != nil {
				r.t.Fatal(err)
			}
			return d
		}
	}
	r.t.Fatalf("no fact %s of %s: %q", step, task, r.steps())
	return nil
}

// Dispatch as state (docs/state-model.md rows 2–9): the dispatch request over the socket, its fact, and the steps the
// merge loop runs after it (lane.prepare, agent.start, the work job), on the merge loop's rig with fake
// LaneSetup and Specs adapters. Lane L-01 starts free (its worker pj-l-01 idle in its pane, session s0); T-1 is the
// task dispatched.

// dispatchFakes is the dispatch's adapters and what they did.
type dispatchFakes struct {
	r  *loopRig
	mu sync.Mutex
	on bool // the loop has the dispatch adapters (its lanes and target in Loop.Workflow, Loop.Specs)
	// The Specs: refusals by task, the target sha, the task branch ahead.
	refuse map[string]string
	texts  []string // the spec text each Check was given (the store's)
	ahead  bool
	// What the LaneSetup and the worker's starts applied (a call cut before it applies counts in calls only).
	prepares, starts int
	resumes          int // the starts without --fresh applied (the worker start rule's)
	calls            map[string]int
	inputs           []port.PrepareInput
	frees            []port.FreeInput
	session          int           // the worker's session number (its transcript /s/<n>.jsonl)
	keepSession      bool          // a start leaves the session as it is
	sessionless      bool          // a start reports no session id for its agent (OpenCode)
	gate             chan struct{} // non-nil: a start, its agent up, waits for it to close before it returns
	// fail: an error a method gives instead of acting (nil: none); cut: where a method blocks until its ctx ends
	// ("before" or "after" it applies), hit: it blocked there.
	fail map[string]func() error
	cut  map[string]string
	hit  map[string]bool
}

func (d *dispatchFakes) loop(fx *app.Loop, panes herdr.Panes) {
	d.mu.Lock()
	defer d.mu.Unlock()
	kit, _ := filepath.Abs("../..")
	if d.on {
		fx.Workflow, fx.Specs = app.WorkflowOf(fx.Workflow, d), d
	}
	fx.Kit = kit
	fx.Prompter = cutPrompter{d, panes.S}
}

// enter counts a call of method and gives its armed failure (nil: act).
func (d *dispatchFakes) enter(method string) error {
	d.mu.Lock()
	d.calls[method]++
	fail := d.fail[method]
	d.mu.Unlock()
	if fail != nil {
		return fail()
	}
	return nil
}

// hold blocks until ctx ends when method is cut at where.
func (d *dispatchFakes) hold(ctx context.Context, method, where string) error {
	d.mu.Lock()
	cut := d.cut[method] == where
	if cut {
		d.hit[method] = true
	}
	d.mu.Unlock()
	if !cut {
		return nil
	}
	<-ctx.Done()
	return ctx.Err()
}

func (d *dispatchFakes) Check(_ context.Context, task, text string) (string, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.texts = append(d.texts, text)
	return d.refuse[task], nil
}

func (d *dispatchFakes) Target(context.Context, string) (string, bool, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	return "target1", d.ahead, nil
}

// Prepare puts L-01 on T-1 in the lane table.
func (d *dispatchFakes) Prepare(ctx context.Context, in port.PrepareInput) (port.Prepared, error) {
	if err := d.enter("prepare"); err != nil {
		return port.Prepared{}, err
	}
	if err := d.hold(ctx, "prepare", "before"); err != nil {
		return port.Prepared{}, err
	}
	d.mu.Lock()
	d.prepares++
	d.inputs = append(d.inputs, in)
	d.mu.Unlock()
	d.r.setLane(func(l *port.Lane) { l.Task, l.State = in.Task, "working" })
	if err := d.hold(ctx, "prepare", "after"); err != nil {
		return port.Prepared{}, err
	}
	return port.Prepared{Base: "base-" + in.Target}, nil
}

// Free is the lane given back by a drop: noted, nothing refused.
func (d *dispatchFakes) Free(ctx context.Context, in port.FreeInput) (string, error) {
	if err := d.enter("free"); err != nil {
		return "", err
	}
	d.mu.Lock()
	d.frees = append(d.frees, in)
	d.mu.Unlock()
	return "", nil
}

// startWorker is pj-l-01's start in its pane: a fresh one a new session (unless keepSession), one without --fresh
// (the worker start rule's) the session resumed; idle, seen by the app.
func (d *dispatchFakes) startWorker(ctx context.Context, req port.Launch) (port.Started, error) {
	if err := d.enter("start"); err != nil {
		return port.Started{}, err
	}
	if err := d.hold(ctx, "start", "before"); err != nil {
		return port.Started{}, err
	}
	d.mu.Lock()
	d.starts++
	if !req.Fresh {
		d.resumes++
	} else if !d.keepSession {
		d.session++
	}
	transcript := "/s/" + string(rune('0'+d.session)) + ".jsonl"
	sid := transcript // herdr's session id of the agent: what the start reports and the pane shows
	if d.sessionless {
		sid = ""
	}
	d.mu.Unlock()
	// The lane's pane the start names: the lane's own, or the one serve placed for it.
	if !d.r.f.Set(req.Pane, func(p *herdrtest.Pane) {
		p.Kind, p.Name, p.Status, p.Transcript, p.Session = "claude", "pj-l-01", "idle", transcript, sid
	}) {
		return port.Started{}, errors.New("unexpected start of pj-l-01 in " + req.Pane)
	}
	d.r.push()
	d.mu.Lock()
	gate := d.gate
	d.mu.Unlock()
	if gate != nil {
		select {
		case <-gate:
		case <-ctx.Done():
			return port.Started{}, ctx.Err()
		}
	}
	if err := d.hold(ctx, "start", "after"); err != nil {
		return port.Started{}, err
	}
	return port.Started{Line: "STARTED pj-l-01", Pane: req.Pane, Kind: "claude", Session: sid}, nil
}

// cutPrompter is the herdr prompter with the work job's cut points.
type cutPrompter struct {
	d     *dispatchFakes
	inner port.Prompter
}

func (c cutPrompter) Prompt(ctx context.Context, pane, text string, clear bool) (bool, error) {
	if !strings.HasPrefix(text, "[obor job ") || !strings.Contains(text, "/work") {
		return c.inner.Prompt(ctx, pane, text, clear)
	}
	if err := c.d.enter("prompt"); err != nil {
		return false, err
	}
	if err := c.d.hold(ctx, "prompt", "before"); err != nil {
		return false, err
	}
	if typed, err := c.inner.Prompt(ctx, pane, text, clear); err != nil {
		return typed, err
	}
	return true, c.d.hold(ctx, "prompt", "after")
}

// applied is what the fakes applied: prepares, starts.
func (d *dispatchFakes) applied() (int, int) {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.prepares, d.starts
}

func (d *dispatchFakes) called(method string) int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.calls[method]
}

func (d *dispatchFakes) set(fn func(d *dispatchFakes)) {
	d.mu.Lock()
	fn(d)
	d.mu.Unlock()
}

// dispatch is the lead's dispatch of T-1 (lane "" with models: the first free lane for them). A named lane with no
// models runs sonnet: the rig's T-1 has no stored spec, so no level to take one from (every lane is pooled).
func (r *loopRig) dispatch(callID, lane string, types ...string) (string, error) {
	if lane != "" && len(types) == 0 {
		types = []string{"sonnet"}
	}
	return r.dispatchCall(wire.DispatchReq{Envelope: wire.Envelope{Root: r.p.Root, Agent: "pj-lead", Role: "lead",
		CallID: callID}, Task: "T-1", Lane: lane, Types: types})
}

func (r *loopRig) dispatchCall(c wire.DispatchReq) (string, error) {
	return r.ask(&c)
}

func (r *loopRig) mustDispatch(callID string) {
	r.t.Helper()
	r.dispatched(r.dispatch(callID, "L-01"))
}

// dispatched fails unless reply, err are the accepted dispatch of T-1 onto L-01 as sonnet: the reply's sentence is
// pinned here, once (AGENTS.md "A sentence is pinned once"), so a test of another dispatch shape passes its reply
// here too.
func (r *loopRig) dispatched(reply string, err error) {
	r.t.Helper()
	if err != nil || reply != "DISPATCHED T-1 → L-01 (queued) as sonnet" {
		r.t.Fatalf("dispatch: %q, %v; want the dispatch of T-1 onto L-01 as sonnet", reply, err)
	}
}

// dispatchedOnto fails unless reply, err are a dispatch of T-1 onto L-01 (err nil): where a sentence besides the one
// dispatched pins may follow (a pool's idle note), a test checks the lane here.
func (r *loopRig) dispatchedOnto(reply string, err error) {
	r.t.Helper()
	if err != nil || !strings.HasPrefix(reply, "DISPATCHED T-1 → L-01") {
		r.t.Fatalf("dispatch: %q, %v; want T-1 dispatched onto L-01", reply, err)
	}
}

// workerIs sets pj-l-01's status in the fake herdr and waits until the app sees it.
func (r *loopRig) workerIs(status string) {
	r.t.Helper()
	r.f.Set("w1:p1", func(p *herdrtest.Pane) { p.Status = status })
	r.waitFor("pj-l-01 "+status, func() bool {
		for _, a := range r.app.Status().Project.Agents {
			if a.Name == "pj-l-01" && a.Status == status {
				return true
			}
		}
		r.push()
		return false
	})
}

// workPrompts is how many work job prompts pj-l-01 got (job T-1/<attempt>/work…).
func (r *loopRig) workPrompts() int {
	n := 0
	for _, p := range r.prompts() {
		if p.to == "pj-l-01" && strings.HasPrefix(p.text, "[obor job T-1/") && strings.Contains(p.text, "/work") {
			n++
		}
	}
	return n
}

// needsYouState waits until T-1's needs-you is folded (its record is written just before).
func (r *loopRig) needsYouState() {
	r.t.Helper()
	r.waitFor("T-1 needs-you", func() bool { return r.taskState() == "needs-you" })
}

// taskState is T-1's state in obor status --facts ("" when not listed).
func (r *loopRig) taskState() string {
	for _, t := range r.app.Status().Project.Tasks {
		if t.Task == "T-1" {
			return t.State
		}
	}
	return ""
}

// dispatchSteps: the start carries the work job's wake (its intent first, the start's result its delivery).
var dispatchSteps = []string{"1 fact dispatch", "1 intent lane.prepare", "1 result lane.prepare",
	"1 intent work", "1 intent agent.start", "1 result agent.start", "1 result work"}

// addLanes appends lanes to the rig's lane table after L-01 (config order).
func (r *loopRig) addLanes(ls ...port.Lane) {
	r.lanes.mu.Lock()
	r.lanes.lanes = append(r.lanes.lanes, ls...)
	r.lanes.mu.Unlock()
}

// lastClose is a review git with no close commit; what a review covered is the log's (reviewsLog).
type lastClose struct{}

func (lastClose) Head(context.Context, string) (string, error) { return "head1", nil }

func (lastClose) Files(context.Context, string, string) ([]string, error) { return nil, nil }

func (lastClose) Detach(context.Context, string, string) error { return nil }

func (lastClose) Close(context.Context, port.ReviewCommit) error { return nil }

func (lastClose) Closed(context.Context, string, string, string) (string, error) { return "", nil }

// reviewsLog writes the log of a stopped app: RV-4 started and closed, then each of open started (not closed).
func (r *loopRig) reviewsLog(open ...string) {
	r.t.Helper()
	rec := func(step string, d app.ReviewData) event.Record {
		raw, _ := json.Marshal(d)
		return event.Record{Task: event.ReviewTask, Step: step, Kind: event.KindFact, Data: raw}
	}
	recs := []event.Record{rec(event.StepReviewStarted, app.ReviewData{ID: "RV-4", Head: "1111111"}),
		rec(event.StepReviewClosed, app.ReviewData{ID: "RV-4"})}
	for _, id := range open {
		recs = append(recs, rec(event.StepReviewStarted, app.ReviewData{ID: id, Head: "head0"}))
	}
	if err := app.WriteLog(r.p.Root, time.Now(), recs...); err != nil {
		r.t.Fatal(err)
	}
}

// The owner's drive on the dispatch rig: obor drive's request (a new drive on the first free lane; a worker's
// task refused), the driving state, obor land's request, and the merge loop after it. Lane L-01 is made
// free for a new drive; the CLI's start of the drive agent is the rig's pane w1:p5 (driveAgentUp).

// drive is obor drive's request by the owner (no role): a new drive named name (types: its --type), or a task's when
// name is a task id.
func (r *loopRig) drive(callID, name string, types ...string) (string, error) {
	c := &wire.DriveReq{Envelope: wire.Envelope{Root: r.p.Root, CallID: callID}, Name: name, Types: types}
	if strings.HasPrefix(name, "T-") {
		c.Task, c.Name = name, ""
	}
	reply, err := r.driveJSON(c)
	if err != nil {
		return "", err
	}
	return reply.Line(), nil
}

// driveJSON is the drive request c's structured reply.
func (r *loopRig) driveJSON(c *wire.DriveReq) (wire.DriveReply, error) {
	var d wire.DriveReply
	s, err := r.ask(c)
	if err != nil {
		return d, err
	}
	if err := json.Unmarshal([]byte(s), &d); err != nil {
		return d, fmt.Errorf("drive's reply %q is not a DriveReply: %v", s, err)
	}
	return d, nil
}

// landDrive is obor land's request by the owner for task with summary.
func (r *loopRig) landDrive(callID, task, summary string) (string, error) {
	return r.ask(&wire.LandReq{Envelope: wire.Envelope{Root: r.p.Root,
		CallID: callID}, Task: task, Report: summary})
}

// stateOf is task's state in obor status --facts ("" when not listed).
func (r *loopRig) stateOf(task string) string { return r.task(task).State }

// driveAgentUp is the CLI's start of the drive agent pj-l-01-drive in the lane (pane w1:p5, labelled), identified by the
// app (seen in a pane is not enough: obor drive's DRIVING answer needs it identified). The pane is announced as created
// rather than with a workspace event: that ends serve's stream (a refresh), and a call that landed in the resubscribe's
// gap answered "its agent is not running" under load.
func (r *loopRig) driveAgentUp() {
	r.t.Helper()
	r.f.Agent("w1:p5", "pj-l-01-drive", "idle", r.lane, func(p *herdrtest.Pane) { p.Label = "pj-l-01-drive" })
	r.waitFor("pj-l-01-drive identified", func() bool {
		if app.IsIdentified(r.app, r.p.Root, "pj-l-01-drive") {
			return true
		}
		r.paneCreated("w1:p5", "pj-l-01-drive")
		return false
	})
}

// paneCreated tells serve pane exists: a pane.created event, which serve answers by reading the pane again, in place (no
// refresh). The pane is the event's own, so serve's proof reads its label and workspace from here.
func (r *loopRig) paneCreated(pane, label string) {
	r.srv.Push("pane_created", map[string]any{"type": "pane_created", "pane_id": pane,
		"pane": map[string]any{"pane_id": pane, "label": label, "workspace_id": "w1"}})
}

// driveLines is the lead's lines that a drive came back to its owner.
func (r *loopRig) driveLines() []string {
	var out []string
	for _, rec := range r.records() {
		var m event.Message
		_ = json.Unmarshal(rec.Data, &m)
		if rec.Kind == event.KindFact && rec.Step == event.StepMessage && m.Drive != 0 {
			out = append(out, m.Line)
		}
	}
	return out
}

// launchesOf is how many Agents.Launch calls started agent.
func (r *loopRig) launchesOf(agent string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	n := 0
	for _, l := range r.launches {
		if l.Agent == agent {
			n++
		}
	}
	return n
}

// hardType is the rig config's hard level's first model ref (a new drive's default).
func (r *loopRig) hardType() string {
	r.t.Helper()
	cfg, err := config.Load(r.p.Root)
	if err != nil {
		r.t.Fatal(err)
	}
	types := cfg.DifficultyList("hard")
	if len(types) == 0 {
		r.t.Fatal("precondition: the rig config has no hard type")
	}
	return types[0]
}

// A drive and the lead message each other. The drive agent's obor tell-lead is a lead line naming its task; the
// lead's obor answer on a driving task is a line to the drive agent (no question open), delivered when its pane is idle.

// tellLead is obor tell-lead's request from agent with role in lane.
func (r *loopRig) tellLead(callID, agent, role, lane, text string) (string, error) {
	return r.ask(&wire.TellLeadReq{Envelope: wire.Envelope{Root: r.p.Root,
		CallID: callID, Agent: agent, Role: role}, Lane: lane, Text: text})
}

// driverTold is the facts message{driver} in the log (the drive's lines to the lead).
func (r *loopRig) driverTold() []event.Message {
	var out []event.Message
	for _, rec := range r.records() {
		var m event.Message
		_ = json.Unmarshal(rec.Data, &m)
		if rec.Kind == event.KindFact && rec.Step == event.StepMessage && m.Driver {
			out = append(out, m)
		}
	}
	return out
}

// toDrive is how often line reached the drive agent pj-l-01-drive (typed into its pane w1:p5, or posted to its inbox).
func (r *loopRig) toDrive(line string) int {
	return r.typedTo("w1:p5", line) + r.inbox.count("pj-l-01-drive", line)
}

// driveStatus sets the drive agent's pane status and waits until the app sees it.
func (r *loopRig) driveStatus(status string) {
	r.t.Helper()
	r.f.Set("w1:p5", func(p *herdrtest.Pane) { p.Status = status })
	r.waitFor("pj-l-01-drive "+status, func() bool {
		for _, a := range r.app.Status().Project.Agents {
			if a.Name == "pj-l-01-drive" && a.Status == status {
				return true
			}
		}
		r.push()
		return false
	})
	r.connectedAgain() // the pushes refreshed serve: its stream again before a call that asks its identity
}

// leadAnswer is the lead's obor answer on T-1.
func (r *loopRig) leadAnswer(callID, text string) (string, error) {
	return r.ask(&wire.ResumeReq{Envelope: wire.Envelope{Root: r.p.Root,
		Agent: "pj-lead", Role: "lead", CallID: callID}, Task: "T-1", Text: text})
}

// dropSpec is the lead's obor drop of task (a spec stored and never dispatched) with why; fatal unless DROPPED.
func (r *loopRig) dropSpec(callID, task, why string) {
	r.t.Helper()
	reply, err := r.ask(&wire.DropReq{Envelope: wire.Envelope{Root: r.p.Root,
		Role: "lead", CallID: callID}, Task: task, Why: why})
	if err != nil || reply != "DROPPED "+task {
		r.t.Fatalf("drop of %s: %q, %v; want DROPPED %s", task, reply, err, task)
	}
}

// launchOf is the first Agents.Launch of agent (ok false: none yet).
func (r *loopRig) launchOf(agent string) (port.Launch, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, l := range r.launches {
		if l.Agent == agent {
			return l, true
		}
	}
	return port.Launch{}, false
}

// An intent of an ended task (merged, dropped) is not pending on replay: it is settled as nothing ("not run"),
// never run again; one of an open task replays as today. The logs are what an earlier build left (dropped with
// agent.start and work open; merged with an earlier attempt's work open).

// logRec is one record a case writes to the log before the app starts.
type logRec struct {
	attempt         int
	step, kind, cls string
	data            any
}

// writeLog appends recs to T-1's log of a stopped app.
func (r *loopRig) writeLog(recs []logRec) {
	r.t.Helper()
	var out []event.Record
	for _, x := range recs {
		var raw json.RawMessage
		if x.data != nil {
			raw, _ = json.Marshal(x.data)
		}
		out = append(out, event.Record{Task: "T-1", Attempt: x.attempt, Step: x.step, Kind: x.kind, Class: x.cls, Data: raw})
	}
	if err := app.WriteLog(r.p.Root, time.Now(), out...); err != nil {
		r.t.Fatal(err)
	}
}

// resultOf is the data of T-1's result of step at attempt ("": none).
func (r *loopRig) resultOf(attempt int, step string) string {
	for _, rec := range r.records() {
		if rec.Kind == event.KindResult && rec.Attempt == attempt && rec.Step == step {
			return string(rec.Data)
		}
	}
	return ""
}

// The failure rule (docs/state-model.md "Crash classes"): an effect gives a result only for an outcome final
// for its step; anything else leaves the step pending, tried again, and its third counted failure is a needs-you
// during which the step is not tried (also after a restart); obor resume's default retries it.

// countPrefix is how many records' lines start with s.
func (r *loopRig) countPrefix(s string) int {
	n := 0
	for _, line := range r.steps() {
		if strings.HasPrefix(line, s) {
			n++
		}
	}
	return n
}

// gitIO is the error an armed Scratch method gives: git could not tell.
func gitIO(context.Context) error { return errors.New("git I/O: index.lock exists") }

// armScratch makes the Scratch's method fail; the heal is returned.
func (r *loopRig) armScratch(method string) func() {
	r.scratch.Arm(method, gitIO)
	return func() { r.scratch.Arm(method, nil) }
}

// notFound is an adapter's "no such agent" (app.NotFound).
type notFound struct{}

func (notFound) Error() string { return "agent_not_found: pj-l-01" }

func (notFound) NotFound() bool { return true }

// startsWere fails the test unless pj-int was started n times, each fresh with the job's wake, forced or not as
// force says, and nothing was typed to it.
func (r *loopRig) startsWere(n int, force bool) {
	r.t.Helper()
	r.mu.Lock()
	var starts []port.Launch
	for _, l := range r.launches {
		if l.Agent == "pj-int" {
			starts = append(starts, l)
		}
	}
	r.mu.Unlock()
	if len(starts) != n || len(r.prompts()) != 0 {
		r.t.Fatalf("want %d starts of pj-int and no prompt: %+v, prompts %d, %q", n, starts, len(r.prompts()), r.steps())
	}
	for _, l := range starts {
		if !l.Fresh || l.Force != force || l.Prompt != "[obor job T-1/1/review] call the obor job_next tool" {
			r.t.Errorf("start %+v: want fresh, force %v, the review job's wake", l, force)
		}
	}
}

// Jobs by fetch (docs/state-model.md rows 5–8, 16–17) on the dispatch rig: every
// job's prompt is its wake, the agent's job_next fetches the job (its first fetch is fact delivered via mcp, its only
// delivery), and the worker's task_done, its report the argument, submits its task.

// fetchRig is the dispatch rig (edits change the config further).
func fetchRig(t *testing.T, edits ...func(map[string]any)) *loopRig {
	t.Helper()
	return newLoopRig(t, dispatching(), withConfig(edits...))
}

// task is serve's status of task (zero: none).
func (r *loopRig) task(task string) wire.TaskStatus {
	for _, t := range r.app.Status().Project.Tasks {
		if t.Task == task {
			return t
		}
	}
	return wire.TaskStatus{}
}

// wake is the one line typed for job (the Prompter's tag, then the wake).
func wake(job string) string { return "[obor job " + job + "] call the obor job_next tool" }

// call is a job tool's socket request by agent (role, lane) with call id: job_next, or a verdict on T-1; job names the
// job it answers ("": none).
func (r *loopRig) call(method, agent, role, lane, callID, job string) (string, error) {
	id := wire.Envelope{Root: r.p.Root, Agent: agent, Role: role, CallID: callID}
	var c wire.Req = &wire.VerdictReq{Envelope: id, Verb: method, Task: "T-1", Job: job}
	if method == wire.MethodJobNext {
		c = &wire.JobNextReq{Envelope: id, Job: job, Lane: lane}
	}
	return r.ask(c)
}

// jobNext is job_next by agent with call id, its reply decoded.
func (r *loopRig) jobNext(agent, role, lane, callID, job string) (wire.JobReply, string, error) {
	r.t.Helper()
	raw, err := r.call(wire.MethodJobNext, agent, role, lane, callID, job)
	var jr wire.JobReply
	if err == nil {
		if err := json.Unmarshal([]byte(raw), &jr); err != nil {
			r.t.Fatalf("job_next reply %q: %v", raw, err)
		}
	}
	return jr, raw, err
}

// workerNext is pj-l-01's job_next from lane L-01.
func (r *loopRig) workerNext(callID string) wire.JobReply {
	r.t.Helper()
	jr, _, err := r.jobNext("pj-l-01", "worker", "L-01", callID, "")
	if err != nil {
		r.t.Fatalf("job_next of pj-l-01: %v", err)
	}
	return jr
}

// promptsTo is the texts typed to agent, in order.
func (r *loopRig) promptsTo(agent string) []string {
	var out []string
	for _, p := range r.prompts() {
		if p.to == agent {
			out = append(out, p.text)
		}
	}
	return out
}

// deliveries is the log's delivered facts as "<job> <via>".
func (r *loopRig) deliveries() []string {
	var out []string
	for _, rec := range r.records() {
		if rec.Kind == event.KindFact && rec.Step == event.StepDelivered {
			out = append(out, rec.Fact().Job+" "+rec.Fact().Via)
		}
	}
	return out
}

// setConfig sets path in the project's config on disk (read at the app's next load). The file is shape 2, so path is
// one of its names.
func (r *loopRig) setConfig(path string, v any) {
	r.t.Helper()
	var c map[string]any
	if err := json.Unmarshal([]byte(r.p.Read(".obor/config.json")), &c); err != nil {
		r.t.Fatal(err)
	}
	testkit.Set(path, v)(c)
	b, _ := json.Marshal(c)
	r.p.Write(".obor/config.json", string(b)+"\n")
}

// fetched is the fetch rig with T-1 dispatched, its work job fetched and its task_done submitting it,
// the integrator's review job out and fetched: the review job's text.
func fetched(t *testing.T) (*loopRig, string) {
	r := fetchRig(t)
	r.work("feature.txt", "feature\n")
	r.start()
	r.mustDispatch("d1")
	r.waitStep("1 result work")
	r.workerNext("n1")
	r.submit()
	r.waitStep("1 fact submitted")
	r.waitStep("1 result review")
	eq(t, r.promptsTo("pj-int"), []string(nil), "typed to pj-int (its start carried the wake)")
	same(t, r.result("review"), `"sent (start prompt)"`, "review result")
	r.integratorIs("working")
	r.passes()
	if len(r.deliveries()) != 1 {
		t.Fatalf("the review job delivered by the status: %q", r.steps())
	}
	jr, _, err := r.jobNext("pj-int", "integrator", "", "i1", "")
	if err != nil || jr.State != "job" || jr.Job != "T-1/1/review" || !strings.Contains(jr.Text, "Patch hash: ") {
		t.Fatalf("the integrator's job_next: %+v, %v", jr, err)
	}
	r.waitFor("delivered", func() bool { return len(r.deliveries()) == 2 })
	eq(t, r.deliveries(), []string{"T-1/1/work mcp", "T-1/1/review mcp"}, "delivered facts")
	return r, jr.Text
}

// callDone is agent's task_done for T-1 (role, lane L-01) with call id and report.
func (r *loopRig) callDone(agent, role, callID, report string) (string, error) {
	return r.ask(&wire.TaskDoneReq{Envelope: wire.Envelope{Root: r.p.Root,
		Agent: agent, Role: role, CallID: callID}, Lane: "L-01", Task: "T-1", Report: report})
}

// A task whose move's stop of its worker is pending (recorded, not settled) is busy for every other
// verb: a drop is refused, "retry when it settles". A stop that failed (its needs-you) is settled, and a drop then
// stops the halt's recorded agent (the old worker) before it frees the lane. The rig: newLoopRig(t, working()) (T-1 working on L-01, its worker pj-l-01 in w1:p1).

// dropCall is obor drop of T-1 by the lead (its reply or refusal).
func (r *loopRig) dropCall(callID, why string) (string, error) {
	return r.ask(&wire.DropReq{Envelope: wire.Envelope{Root: r.p.Root,
		Role: "lead", Agent: "pj-lead", CallID: callID}, Task: "T-1", Why: why})
}

// refusedWhileStopPending: verb's call (T-1, its stop pending, not running: cutStops) is refused busy, naming the
// pending worker.stop of pj-l-01, and records nothing. It is called until a call lands between two tries of the stop,
// where the app rests (cutStops); a call during a try is busy too, as a held step. Any call accepted fails the test.
func (r *loopRig) refusedWhileStopPending(verb string, call func() (string, error)) {
	r.t.Helper()
	want := verb + " T-1: T-1 has worker.stop of pj-l-01 pending; retry when it settles"
	r.waitFor(verb+" refused for the pending stop", func() bool {
		reply, err := call()
		if err == nil {
			r.t.Fatalf("%s while the stop is pending: accepted (%q); want refused busy", verb, reply)
		}
		if !strings.Contains(err.Error(), "retry when it settles") {
			r.t.Fatalf("%s while the stop is pending: %v; want refused busy", verb, err)
		}
		return err.Error() == want
	})
	if r.hasPrefix("1 fact dropping") || r.hasPrefix("2 fact dropping") || r.hasPrefix("1 fact submitted") {
		r.t.Errorf("%s while the stop is pending recorded: %q", verb, r.steps())
	}
}

// cutStops makes each stop cut (it returns a cancellation while serve runs: no result, its intent pending, tried
// again after its wait) until the returned settle lets the stops act; tried gets one value per try. The app rests
// between two tries with the stop pending (the wait of an uncounted step, the rig's stepRetry).
func (r *loopRig) cutStops() (tried <-chan struct{}, settle func()) {
	in := make(chan struct{}, 1)
	r.mu.Lock()
	r.onStop = func() error {
		select {
		case in <- struct{}{}:
		default:
		}
		return context.Canceled
	}
	r.mu.Unlock()
	return in, func() {
		r.mu.Lock()
		r.onStop = nil
		r.mu.Unlock()
	}
}

// intentData is the data of T-1's last intent of step at attempt (nil: none).
func (r *loopRig) intentData(attempt int, step string) map[string]any {
	var d map[string]any
	for _, rec := range r.records() {
		if rec.Kind == event.KindIntent && rec.Attempt == attempt && rec.Step == step {
			d = nil
			_ = json.Unmarshal(rec.Data, &d)
		}
	}
	return d
}

// failStops makes the rig's stops fail (on) or act (off).
func (r *loopRig) failStops(on bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.onStop = nil
	if on {
		r.onStop = func() error { return errors.New("herdr: pane busy") }
	}
}

// dropFrees: the drop answers DROPPING naming stopping, then T-1 is dropped with its lane freed once.
func (r *loopRig) dropFrees(callID, stopping string) {
	r.t.Helper()
	reply, err := r.dropCall(callID, "not needed")
	if want := "DROPPING T-1 from L-01 (not needed; stopping " + stopping + ")"; err != nil || reply != want {
		r.t.Fatalf("drop: %q, %v; want %q", reply, err, want)
	}
	r.waitFor("T-1 dropped", func() bool { return r.stateOf("T-1") == "dropped" })
	if n := r.disp.called("free"); n != 1 {
		r.t.Errorf("lane freed %d times, want once", n)
	}
}

// oldWorkerStoppedFirst: the drop's worker.stop.2 of attempt (the old worker, with force) settled before its
// lane.free, the old worker's stop tried twice, the drive agent never.
func (r *loopRig) oldWorkerStoppedFirst(attempt int) {
	r.t.Helper()
	at := string(rune('0' + attempt))
	if d := r.factData("T-1", event.StepDropping); d["worker"] != "pj-l-01" || d["force"] != true {
		r.t.Errorf("the dropping fact %v; want worker pj-l-01 with force (the halt's)", d)
	}
	if d := r.intentData(attempt, "worker.stop.2"); d["agent"] != "pj-l-01" || d["force"] != true {
		r.t.Errorf("the drop's worker.stop.2 intent %v; want pj-l-01 with force", d)
	}
	if !r.before(at+" result worker.stop.2", at+" intent lane.free") {
		r.t.Errorf("the steps %q; want the old worker's stop settled, then lane.free", r.steps())
	}
	if got := r.workerStops(); !slices.Equal(got, []string{"pj-l-01 w1:p1", "pj-l-01 w1:p1"}) {
		r.t.Errorf("stops %q; want pj-l-01 twice (the failed one, the drop's), never the drive agent", got)
	}
}

// The idle rules end to end (their table is rules_test.go's TestIdleRules): what keeps the pipeline from
// being idle, the line through the merge loop and the acknowledgement through the socket, and the line's order after a
// watch's.

// idleLine is the idle line after task's merge (idleLineFor) or a drop.
func idleLineFor(r *loopRig, task string) string {
	r.t.Helper()
	sha := r.scratch.TargetHead()[:7]
	return idleLineText(task + " merged " + sha)
}

func idleLineText(last string) string {
	return `[obor] idle: nothing in flight (last: ` + last + `) — dispatch next, or obor idle "<why nothing>"`
}

// idleSettle is the window: the idle line waits until the pipeline has stayed idle this long.
const idleSettle = time.Minute

// settleIdle lets the idle window pass: two passes start its clock, then the clock moves past idleSettle, so the next
// idle check posts the line.
func (r *loopRig) settleIdle() {
	r.t.Helper()
	r.passes()          // the idle check that starts the window
	r.later(idleSettle) // the first check past it: the line goes out
}

// idlePosts is the idle lines the lead's inbox got, in order.
func (r *loopRig) idlePosts() []string {
	var out []string
	for _, p := range r.inbox.posted() {
		if strings.HasPrefix(p, "pj-lead: [obor] idle:") {
			out = append(out, strings.TrimPrefix(p, "pj-lead: "))
		}
	}
	return out
}

// logRecords appends recs to the project's event log before the app starts.
func (r *loopRig) logRecords(recs ...event.Record) {
	r.t.Helper()
	if err := app.WriteLog(r.p.Root, time.Now(), recs...); err != nil {
		r.t.Fatal(err)
	}
}

// record is one fact or intent of task at attempt, its data as JSON.
func record(t *testing.T, task string, attempt int, step, kind, class string, data any) event.Record {
	t.Helper()
	raw, err := json.Marshal(data)
	if err != nil {
		t.Fatal(err)
	}
	return event.Record{Task: task, Attempt: attempt, Step: step, Kind: kind, Class: class, Data: raw}
}

// mergedTask runs T-1 from its task_done to its merge (as mergedT1, for a rig that has no dispatch to make).
func (r *loopRig) mergedTask() {
	r.t.Helper()
	r.work("feature.txt", "feature\n")
	r.mergedT1()
}

// idleAcked runs T-1 to its merge, waits for its idle line and acknowledges it; it returns the line.
func (r *loopRig) idleAcked() string {
	r.t.Helper()
	r.start()
	r.mergedTask()
	line := idleLineFor(r, "T-1")
	r.settleIdle()
	r.waitFor("the idle line posted", func() bool { return r.inbox.count("pj-lead", line) == 1 })
	if reply, err := r.line("i1", &wire.IdleReq{Why: "nothing to dispatch"}); err != nil ||
		reply != "ACKED idle: nothing to dispatch" {
		r.t.Fatalf("idle ack: %q, %v", reply, err)
	}
	return line
}

// The integrator's jobs: their delivery and stall timers on the app's clock, obor hold and obor resume.

// integratorIs sets pj-int's status in the fake herdr and waits until the app sees it.
func (r *loopRig) integratorIs(status string) {
	r.t.Helper()
	r.f.Set("w1:p9", func(p *herdrtest.Pane) { p.Status = status })
	r.waitFor("pj-int "+status, func() bool {
		for _, a := range r.app.Status().Project.Agents {
			if a.Name == "pj-int" && a.Status == status {
				return true
			}
		}
		r.push()
		return false
	})
}

// integratorStops ends pj-int's turn after a verdict that left it no job, and waits for serve's stop of it:
// its idle is gone again once the next pass stops it, so a wait on obor status's "idle" can miss it;
// the stop is a rule now and is done when the pane is at its shell.
//
// One refresh, then no push while it waits: the rule stops pj-int only on a pass that stays on one herdr stream
// from its start to the rule (whereIs is "not connected" in a resubscribe's gap), and a push ends the stream. Pushed
// on every poll, the streams were shorter than a pass under load, so the stop waited a minute and more; serve's tick
// runs the passes, and the stop's own refreshes (the rig's Stop) end once serve sees pj-int gone.
func (r *loopRig) integratorStops() {
	r.t.Helper()
	r.f.Set("w1:p9", func(p *herdrtest.Pane) { p.Status = "idle" })
	r.connectedAgain() // serve reads the idle from a snapshot taken after the Set
	r.waitFor("the stop of pj-int", func() bool { return slices.Contains(r.app.Status().Project.Stopped, "pj-int") })
}

// integratorGone makes pj-int's agent exit and waits until the merge loop has seen it gone for T-1's open job, job
// (its Gone grace counts from that pass, on the fake clock).
func (r *loopRig) integratorGone(job string) {
	r.t.Helper()
	r.f.Set("w1:p9", func(p *herdrtest.Pane) { p.Kind, p.Name, p.Status = "", "", "" })
	r.waitFor("pj-int gone", func() bool {
		for _, a := range r.app.Status().Project.Agents {
			if a.Name == "pj-int" {
				r.push()
				return false
			}
		}
		return true
	})
	r.waitTimer("pj-int seen gone with "+job, func(j string, gone, _ time.Time) bool { return j == job && !gone.IsZero() })
}

// waitTimer waits until T-1's job timer is as ok says (the merge loop has taken the sample a test step depends on).
func (r *loopRig) waitTimer(what string, ok func(job string, gone, quiet time.Time) bool) {
	r.t.Helper()
	r.waitFor(what, func() bool { return ok(app.JobTimer(r.app, r.p.Root, "T-1")) })
}

// quietBaseline waits for the stall timer's first quiet sample of T-1's job.
func (r *loopRig) quietBaseline(job string) {
	r.t.Helper()
	r.waitTimer("the quiet baseline of "+job, func(j string, _, quiet time.Time) bool { return j == job && !quiet.IsZero() })
}

// workspaceEvent is the rig's workspace-created event: pushed, it makes the app read a new snapshot.
var workspaceEvent = map[string]any{"type": "workspace_created",
	"workspace": map[string]any{"workspace_id": "w1", "label": "proj"}}

// push makes the app read a new snapshot: a workspace event ends serve's stream (a refresh) and serve subscribes again.
func (r *loopRig) push() { r.srv.Push("workspace_created", workspaceEvent) }

// connectedAgain waits, as an event, until serve works from a herdr stream that began after the rig's pushes: a refresh
// reaches the stream serve has now, serve reads it and ends that stream, and subscribes again — the wait is that new
// stream's lifecycle subscription and serve's connection to it. Status().Connected alone is no such event: it
// is still true in the instant before serve ends a stream (serve closes the stream, then clears the flag), and a push
// is only a socket write that the stream may read after the caller looked — the call then lands in the resubscribe's
// gap and is refused "serve is not connected to herdr".
func (r *loopRig) connectedAgain() {
	r.t.Helper()
	var next func() bool // that stream ended and a stream after it subscribed: true once it connected too
	r.waitFor("serve connected to herdr on the stream after the rig's pushes", func() bool {
		if next == nil {
			var reached bool
			if reached, next = r.srv.Refresh(workspaceEvent); !reached { // serve is between two streams: none to end
				next = nil
				return false
			}
		}
		return r.app.Status().Connected && next()
	})
}

// passes waits for two more passes of the merge loop: the second one started after the call, so it saw the clock and
// the state the test set before it. Only passes that ran connected to one herdr stream count (StreamPasses): the
// rig's pushes end serve's stream, and a pass in the resubscribe's gap skips the project.
func (r *loopRig) passes() {
	r.t.Helper()
	n := app.StreamPasses(r.app)
	r.waitFor("two more passes", func() bool { return app.StreamPasses(r.app) >= n+2 })
}

// later moves the app's clock on by d, then waits for a few passes.
func (r *loopRig) later(d time.Duration) {
	r.clock.Add(d)
	r.passes()
}

// jobPrompts is how many prompts pj-int got for job.
func (r *loopRig) jobPrompts(job string) int {
	n := 0
	for _, p := range r.prompts() {
		if p.to == "pj-int" && strings.HasPrefix(p.text, "[obor job "+job+"] ") {
			n++
		}
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, l := range r.launches { // a start's first prompt: the job's wake
		if l.Agent == "pj-int" && l.Prompt == "[obor job "+job+"] call the obor job_next tool" {
			n++
		}
	}
	return n
}

// candidatePatch is the patch hash of the attempt's last green candidate (the gate's result), what a review job names.
func (r *loopRig) candidatePatch() string {
	r.t.Helper()
	var patch string
	for _, rec := range r.records() {
		if rec.Kind == event.KindResult && strings.HasPrefix(rec.Step, "gate") {
			var g struct{ Candidate struct{ Patch string } }
			_ = json.Unmarshal(rec.Data, &g)
			patch = g.Candidate.Patch
		}
	}
	if patch == "" {
		r.t.Fatalf("no green candidate: %q", r.steps())
	}
	return patch
}

// hasPrefix: a record's "<attempt> <kind> <step>{reason}" line starts with s.
func (r *loopRig) hasPrefix(s string) bool {
	for _, line := range r.steps() {
		if strings.HasPrefix(line, s) {
			return true
		}
	}
	return false
}

func (r *loopRig) waitPrefix(s string) {
	r.t.Helper()
	r.waitFor("a record "+s+"…", func() bool { return r.hasPrefix(s) })
}

// resume is the lead's obor resume of T-1 (to "": the cause's default).
func (r *loopRig) resume(callID, to string) (string, error) {
	return r.resumeWith(callID, to, "")
}

// resumeWith is the lead's obor resume of T-1 with flag to and text note.
func (r *loopRig) resumeWith(callID, to, note string) (string, error) {
	return r.ask(&wire.ResumeReq{Envelope: wire.Envelope{Root: r.p.Root,
		Agent: "pj-lead", Role: "lead", CallID: callID}, Task: "T-1", Flag: to, Text: note})
}

// mustResume is resume with the reply it must give.
func (r *loopRig) mustResume(callID, to, want string) {
	r.t.Helper()
	if reply, err := r.resume(callID, to); err != nil || reply != want {
		r.t.Fatalf("resume %s: %q, %v; want %q", to, reply, err, want)
	}
}

// facts is how many facts are in force for the project.
func (r *loopRig) facts() int { return len(r.app.Status().Project.Facts) }

// reviewed is the rig at T-1's first review job fetched (delivered; the integrator idle, the clock where the job went
// out): the job's text.
func reviewed(t *testing.T) (*loopRig, string) {
	r := woke(t)
	return r, r.waitJob("pj-int", "T-1/1/review")
}

// woke is the rig at T-1's first review job sent, its wake carried by the integrator's start, not fetched.
func woke(t *testing.T) *loopRig {
	r := newLoopRig(t)
	r.work("feature.txt", "feature\n")
	r.start()
	r.submit()
	r.waitWake("pj-int", "T-1/1/review")
	return r
}

// dispatchTo is the lead's dispatch of task to lane, as sonnet.
func (r *loopRig) dispatchTo(callID, task, lane string) (string, error) {
	return r.dispatchCall(wire.DispatchReq{Envelope: wire.Envelope{Root: r.p.Root, Agent: "pj-lead", Role: "lead",
		CallID: callID}, Task: task, Lane: lane, Types: []string{"sonnet"}})
}

// record is task's newest record of step and kind (ok false: none).
func (r *loopRig) record(task, kind, step string) (event.Record, bool) {
	var out event.Record
	ok := false
	for _, rec := range r.records() {
		if rec.Task == task && rec.Kind == kind && rec.Step == step {
			out, ok = rec, true
		}
	}
	return out, ok
}

// runToMerge takes task, its work job fetched by pj-l-01, through its task_done, the review and the accept to merged,
// then frees L-01 in the lane table (its row's task landed: what the lane table reads after a merge). inReview runs
// while its review job is out (nil: nothing).
func (r *loopRig) runToMerge(task string, inReview func()) {
	r.t.Helper()
	r.work(strings.ToLower(task)+".txt", task+"\n")
	r.waitFor(task+" working", func() bool { return r.stateOf(task) == "working" })
	reply, err := r.ask(&wire.TaskDoneReq{Envelope: wire.Envelope{Root: r.p.Root,
		Agent: "pj-l-01", Role: "worker", CallID: "done-" + task}, Lane: "L-01", Task: task, Report: report})
	if err != nil || reply != "submitted" {
		r.t.Fatalf("%s's task_done: %q, %v", task, reply, err)
	}
	job := r.waitJob("pj-int", task+"/1/review")
	if inReview != nil {
		inReview()
	}
	if reply, err := r.ask(&wire.VerdictReq{Envelope: wire.Envelope{Root: r.p.Root,
		Agent: "pj-int", Role: "integrator", CallID: "accept-" + task}, Verb: wire.MethodAccept, Task: task,
		Patch: r.patch(job)}); err != nil ||
		!strings.HasPrefix(reply, "ACCEPT "+task) {
		r.t.Fatalf("%s's accept: %q, %v", task, reply, err)
	}
	r.waitFor(task+"'s after_merge", func() bool {
		_, ok := r.record(task, event.KindResult, event.StepAfterMerge)
		return ok
	})
	r.setLane(func(l *port.Lane) { l.Task, l.State = "", "free" })
}

// A redo runs the model ref its dispatch ran unless its --model names another; a model ref naming no
// model of the project is refused before a lane is picked. The rig: lanes L-01 and L-02, both free.

// specOf stores task's spec (obor spec set).
func (r *loopRig) specOf(task, text string) {
	r.t.Helper()
	if _, err := r.ask(&wire.SpecSetReq{Envelope: wire.Envelope{Root: r.p.Root,
		Agent: "lead", Role: "lead", CallID: "s-" + task}, Task: task, Spec: text}); err != nil {
		r.t.Fatalf("spec set %s: %v", task, err)
	}
}

// dispatchOf is the lead's dispatch of task by its spec's level.
func (r *loopRig) dispatchOf(task string) (string, error) {
	return r.dispatchCall(wire.DispatchReq{Envelope: wire.Envelope{Root: r.p.Root, Agent: "pj-lead", Role: "lead",
		CallID: "d-" + task}, Task: task})
}

// redoTypes is the type of each of T-1's dispatch facts, in order.
func (r *loopRig) redoTypes() []any {
	var types []any
	for _, rec := range r.records() {
		if rec.Task == "T-1" && rec.Kind == event.KindFact && rec.Step == event.StepDispatched {
			var m map[string]any
			_ = json.Unmarshal(rec.Data, &m)
			types = append(types, m["type"])
		}
	}
	return types
}
