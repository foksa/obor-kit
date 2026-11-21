package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"obor/internal/app/event"
	"obor/internal/app/port"
	"obor/internal/app/wire"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// gatedScratch is schedScratch whose Publish reports its task on began, then waits for release (nil: no wait) before
// it moves the head.
type gatedScratch struct {
	*schedScratch
	git     *reviewGitFake
	began   chan string
	release chan struct{}
}

func (s *gatedScratch) Publish(_ context.Context, c port.Candidate) (port.PublishOutcome, error) {
	s.began <- c.Task
	if s.release != nil {
		<-s.release
	}
	s.git.mu.Lock()
	s.git.head = c.SHA
	s.git.mu.Unlock()
	return port.PublishOutcome{Kind: port.PublishMerged}, nil
}

// admitRig is seedRoles' project (T-1 accepted, candidate candidate1; reviewer pj-rev and deep dive pj-deep-dive idle)
// with reviews.dir, RV-5 open (its file written in the main checkout), its review git (head base1) and a gated publish.
type admitRig struct {
	h      *harness
	git    *reviewGitFake
	s      *gatedScratch
	agents *agentsSpy
	fx     *Loop
}

func newAdmitRig(t *testing.T, gated bool) *admitRig {
	t.Helper()
	h, f := seedRoles(t)
	h.write(filepath.Join(h.root, ".obor", "config.json"), admitCfg)
	if err := os.MkdirAll(filepath.Join(h.root, "reviews"), 0o755); err != nil {
		t.Fatal(err)
	}
	h.write(filepath.Join(h.root, "reviews", "RV-5.md"), "# RV-5\n")
	r := &admitRig{h: h, git: &reviewGitFake{head: "base1"}, agents: &agentsSpy{}}
	r.s = &gatedScratch{schedScratch: f, git: r.git, began: make(chan string, 4)}
	if gated {
		r.s.release = make(chan struct{})
	}
	r.fx = withCfg(h.root, &Loop{Workflow: WorkflowOf(r.s, r.git), Agents: r.agents, Prompter: f})
	h.a.opt.Loop = func(root string) (*Loop, error) { return withCfg(root, r.fx), nil }
	return r
}

// publishing is one pass whose publish of T-1 has begun and waits for its release.
func (r *admitRig) publishing(ctx context.Context) {
	r.h.t.Helper()
	r.h.a.advance(ctx)
	select {
	case task := <-r.s.began:
		if task != "T-1" {
			r.h.t.Fatalf("precondition: the publish of %s began, want T-1", task)
		}
	case <-time.After(5 * time.Second):
		r.h.t.Fatal("precondition: T-1's publish did not begin")
	}
}

// released ends the gated publish and settles the loop.
func (r *admitRig) released(ctx context.Context) {
	close(r.s.release)
	r.h.a.effects.Wait()
	r.h.pass(ctx)
}

// waiting: done has not answered within a short wait (the call waits for the publish).
func waiting[T any](done chan T) bool {
	select {
	case <-done:
		return false
	case <-time.After(200 * time.Millisecond):
		return true
	}
}

// selectPublish is the loop's step for T-1 (accepted, its publish due), selected under mu as drive does; it is run
// later, after the caller's admission.
func (r *admitRig) selectPublish() step {
	r.h.t.Helper()
	a := r.h.a
	a.mu.Lock()
	s := a.next(a.p, a.p.folded.loop["T-1"], cfgAt(a.p.root))
	a.mu.Unlock()
	if s == nil {
		r.h.t.Fatal("precondition: no step selected for T-1")
	}
	return s
}

// answerSeed is the harness with T-1 dispatched in lane L-01 (the lane adapters faked), its loop state set to state
// and its attempt's job to job, then the needs-you of cause recorded (a watchdog's when watch: no merge-loop fact). It
// returns the fact in force.
func answerSeed(t *testing.T, cause, state, job string, watch bool) (*harness, *proj, event.Record) {
	t.Helper()
	h, _ := newIdentHarness(t)
	h.withLead()
	f := newConcFakes("")
	fx := withAppCfg(h.a, &Loop{Workflow: WorkflowOf(f), Specs: f, Agents: f})
	h.a.opt.Loop = func(root string) (*Loop, error) { return withCfg(root, fx), nil }
	h.connect()
	p := h.proj()
	t.Cleanup(func() { h.proj().log.Close(); h.a.closeLog() })
	addRecord(t, h, "T-1", event.StepDispatched, event.KindFact, dispatchData{Lane: "L-01", Dir: h.lane("L-01"), Agent: "pj-l-01",
		Branch: "task/T-1", Target: "b"})
	addRecord(t, h, "T-1", event.StepPrepare, event.KindResult, prepareOut{Base: "b"})
	p.setLanes(h.lanes.lanes)
	h.a.reconcile(p)
	mt := p.folded.loop["T-1"]
	if watch {
		h.a.record(p, event.Record{Task: "T-1", Step: event.StepNeedsYou, Kind: event.KindFact}, event.FactData{Lane: "L-01", Reason: cause})
		return h, p, p.folded.active["T-1"][0]
	}
	mt.state, mt.job = state, job
	if state == stWaiting { // a waiting task holds no lane yet
		mt.lane = ""
	}
	switch cause {
	case causeGate, causePublish, causeSendbacks, causeResolveRejected: // its phase's work failed: a due block first
		mt.block(block{cause: cause, why: cause + ": probe"})
	}
	if !h.a.needsYou(p, mt, cause, cause+": probe") || !mt.needs.held() {
		t.Fatalf("precondition: %s's needs-you not in force", cause)
	}
	return h, p, mt.needs.fact
}

// The answer to an idle worker reaches it, a line that cannot go out says why, and obor
// resume takes a stalled worker's needs-you.

// staleHerdr is the ident harness with pj-l-01 idle in w1:p1, where herdr reports session s-herdr (stale: launch's
// start row carries it too) while the session its SessionStart hook reported from w1:p1, and its inbox record, is
// s-live; then the lead's answer to T-1's worker recorded (fact answered).
func staleHerdr(t *testing.T) *harness {
	t.Helper()
	h, _ := newIdentHarness(t)
	h.pane("w1:p1").Session = "s-herdr"
	h.started("pj-l-01", "w1:p1", "t1", "s-herdr")
	h.connect()
	h.session("pj-l-01", "s-live", "/tr/s-live.jsonl", 100)
	h.a.opt.Inboxed = func(string) (string, bool) { return "s-live", true }
	if w := h.where("pj-l-01"); !strings.HasPrefix(w, "identified w1:p1 ") {
		t.Fatalf("precondition: pj-l-01 is %q, want identified at w1:p1", w)
	}
	h.a.mu.Lock()
	s, _ := h.proj().session("pj-l-01")
	h.a.mu.Unlock()
	if s.ID != "s-live" || h.pane("w1:p1").Session != "s-herdr" || h.pane("w1:p1").Status != "idle" {
		t.Fatalf("precondition: recorded %q, herdr's %q, status %q; want s-live, s-herdr, idle", s.ID,
			h.pane("w1:p1").Session, h.pane("w1:p1").Status)
	}
	if !h.a.record(h.proj(), event.Record{Task: event.MsgTask, Step: event.StepAnswered, Kind: event.KindFact},
		event.Message{To: "pj-l-01", From: "pj-lead", Line: "[obor answer] move the helpers down", Task: "T-1"}) {
		t.Fatal("answer not recorded")
	}
	return h
}

// busyAnswered is staleHerdr with pj-l-01 working: one pass holds the queued answer (precondition).
func busyAnswered(t *testing.T) *harness {
	t.Helper()
	h := staleHerdr(t)
	h.status("w1:p1", func(p *port.Pane) { p.Status = "working" })
	in := &kindSpy{}
	h.a.deliver(context.Background(), h.proj(), withAppCfg(h.a, &Loop{Inbox: in, Prompter: &linerSpy{}}))
	if len(in.calls) != 0 || len(h.lines()) != 1 || len(h.a.Status().Project.Messages) != 1 {
		t.Fatalf("precondition: posted %q, queued %q; want the answer waiting for the busy worker", in.calls, h.lines())
	}
	return h
}

// askSeed is a project whose task T-1 is dispatched to lane L-01, its worker identified in pane w1:p1.
func askSeed(t *testing.T) (*harness, *proj) {
	t.Helper()
	h, _ := newIdentHarness(t)
	h.withLead()
	h.a.opt.Loop = func(root string) (*Loop, error) { return withCfg(root, &Loop{}), nil }
	h.connect()
	p := h.proj()
	t.Cleanup(func() { h.proj().log.Close(); h.a.closeLog() })
	addRecord(t, h, "T-1", event.StepDispatched, event.KindFact, dispatchData{Lane: "L-01", Dir: h.lane("L-01"), Agent: "pj-l-01", Branch: "task/T-1", Target: "b"})
	addRecord(t, h, "T-1", event.StepPrepare, event.KindResult, prepareOut{Base: "b"})
	p.setLanes(h.lanes.lanes)
	h.a.reconcile(p)
	return h, p
}

// closeTask ends task of project name as c says: merged or dropped (a fact), or rejected (the integrator's verdict on
// its open review job; T-4 of pr has one). Live: as the app does (record, verdict); replay: appended and folded only,
// so the next load settles it.
func (r *concRig) closeTask(name, task, end string, live bool) {
	r.t.Helper()
	a, p := r.app(name), r.proj(name)
	switch {
	case end != stRejected && live:
		if !a.record(p, event.Record{Task: task, Attempt: 1, Step: end, Kind: event.KindFact}, nil) {
			r.t.Fatal("the terminal fact was not recorded")
		}
	case end != stRejected:
		r.add(name, task, end, event.KindFact, nil)
	case live:
		if _, err := a.verdict(context.Background(), wire.VerdictReq{Envelope: wire.Envelope{Root: p.root,
			Agent: p.integrator, Role: "integrator", CallID: "rj"}, Verb: wire.MethodReject, Task: task,
			Job: task + "/1/review", Patch: "p", Note: "why"}); err != nil {
			r.t.Fatalf("reject: %v", err)
		}
	default:
		step := verdictStep(wire.MethodReject, "rj")
		r.add(name, task, step, event.KindIntent, verdictData{Job: task + "/1/review", Caller: p.integrator, Patch: "p", Note: "why"})
		r.add(name, task, step, event.KindResult, verdictResult{Job: task + "/1/review", Patch: "p", Note: "why"})
	}
	if got := r.state(name, task); got != end {
		r.t.Fatalf("%s is %s, want %s", task, got, end)
	}
}

// reload ends name's serve and loads its log again (the replay), the new app connected.
func (r *concRig) reload(name string) (*App, *proj) {
	r.t.Helper()
	a := r.app(name)
	a.closeLog()
	b := newTestApp(Options{Now: a.opt.Now, Loop: a.opt.Loop}, &fakePanes{}, &fakeLanes{})
	p, err := b.load(r.roots[name])
	if err != nil {
		r.t.Fatal(err)
	}
	b.p, b.connected = p, true
	r.apps[name] = b
	return b, p
}

// A gate runs in its own goroutine holding its project's scratch; the
// merge loop goes on with every other step meanwhile. The fakes below are safe for the loop goroutine, the gate's
// goroutine and the test at once.

// concFakes is one project's adapters: its Scratch the package's one fake (FakeWorkflow: gates green, a held gate,
// publish, resolved check or lane test waits for its release), its Agents, Prompter, LaneSetup and Specs.
type concFakes struct {
	*FakeWorkflow
	restarts int // under the FakeWorkflow's mu
	// obor drop by lane: the stops (agent, force) and Free inputs; refuse: Free's refusal ("": frees).
	stops  []string
	frees  []port.FreeInput
	refuse string
	// ignoresEsc: the worker keeps working after Escape, as launch.Stop sees it: a stop without force fails.
	ignoresEsc bool
	// stopFails: a stop's error by force ("" : none): a quit timeout, a forced stop that fails.
	stopFails func(force bool) string
	// freeErrs: how many Free calls fail first, as a cut git does (retried, never a half-done drop).
	freeErrs int
}

// newConcFakes is a project's adapters whose gate of hold waits for release; the target is at "b", and a candidate
// has no Base unless the test says so (unbased false: a lane test follows its gate).
func newConcFakes(hold string) *concFakes {
	s := NewFakeWorkflow("b")
	s.hold, s.release, s.entered, s.unbased, s.Checks = hold, make(chan struct{}), make(chan string, 16), true, "check 1 pass"
	return &concFakes{FakeWorkflow: s}
}

func (f *concFakes) Launch(context.Context, port.Launch) (port.Started, error) {
	f.mu.Lock()
	f.restarts++
	f.mu.Unlock()
	return port.Started{Line: "STARTED"}, nil
}

func (f *concFakes) Stop(_ context.Context, agent, _ string, force bool) (string, error) {
	f.mu.Lock()
	f.stops = append(f.stops, fmt.Sprintf("%s force=%t", agent, force))
	ignores, fails := f.ignoresEsc, f.stopFails
	f.mu.Unlock()
	if fails != nil && fails(force) != "" {
		return "", errors.New(fails(force))
	}
	if ignores && !force {
		return "", needsForceErr(agent + " still working 30s after Escape (--force stops it)")
	}
	return "STOPPED", nil
}

func (f *concFakes) Prompt(context.Context, string, string, bool) (bool, error) {
	return true, nil
}

func (f *concFakes) Prepare(context.Context, port.PrepareInput) (port.Prepared, error) {
	return port.Prepared{Base: "target"}, nil
}

func (f *concFakes) Free(_ context.Context, in port.FreeInput) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.refuse != "" {
		return "", port.Refused{Line: f.refuse}
	}
	if f.freeErrs > 0 {
		f.freeErrs--
		return "", errors.New("git branch -D task/T-2: signal: terminated — run the same obor drop again to finish it")
	}
	f.frees = append(f.frees, in)
	return "discarded in " + in.Lane, nil
}

func (f *concFakes) Check(context.Context, string, string) (string, error) { return "", nil }

func (f *concFakes) Target(context.Context, string) (string, bool, error) {
	return "target", false, nil
}

// concRig is three projects, each on its own app (obor serve serves one project): pj (workspace w1; T-1
// submitted and held in its gate, T-5 submitted behind it, T-2 dispatched onto lane L-02; the dispatch switch on), pq
// (w2; T-3 submitted) and pr (w3; T-4 green, its review job out). Each workspace runs its integrator; L-02's pane runs
// pj's worker. r.a is pj's app.
type concRig struct {
	t      *testing.T
	a      *App
	apps   map[string]*App   // project → its app
	roots  map[string]string // project → root
	fakes  map[string]*concFakes
	kept   map[string]*Loop // project → the Loop its factory copies per call (setup writes it before a pass; a pass never does)
	kit    string
	cancel context.CancelFunc
	done   chan struct{}
}

func newConcRig(t *testing.T) *concRig {
	base := t.TempDir()
	kit, _ := filepath.Abs("../..")
	r := &concRig{t: t, apps: map[string]*App{}, roots: map[string]string{}, fakes: map[string]*concFakes{}, kept: map[string]*Loop{}, kit: kit}
	now := time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC)
	lane := filepath.Join(base, "pj-wt", "L-02")
	for _, name := range []string{"pj", "pq", "pr"} {
		root := filepath.Join(base, name)
		if err := os.MkdirAll(filepath.Join(root, ".obor"), 0o755); err != nil {
			t.Fatal(err)
		}
		cfg := `{"version":2,"project":{"name":"` + name + `","agent_prefix":"` + name + `"},"agents":{"roles":{"integrator":""}}}`
		if err := os.WriteFile(filepath.Join(root, ".obor", "config.json"), []byte(cfg), 0o644); err != nil {
			t.Fatal(err)
		}
		r.roots[name] = root
		f := newConcFakes("")
		r.fakes[name] = f
		fx := withCfg(root, &Loop{Workflow: WorkflowOf(f), Agents: f, Prompter: f, Kit: r.kit})
		if name == "pj" {
			fx.Specs = f
		}
		r.kept[name] = fx
		a := newTestApp(Options{Root: root, Now: func() time.Time { return now }}, &fakePanes{}, &fakeLanes{})
		a.opt.Loop = func(root string) (*Loop, error) { return withCfg(root, fx), nil }
		loadRoot(t, a)
		r.apps[name] = a
	}
	r.a = r.apps["pj"]
	r.fakes["pj"].hold = "T-1"
	for _, a := range r.apps { // one herdr session: each app watches its own workspace
		a.connect(context.Background(), port.Snapshot{Panes: []port.Pane{
			{ID: "w1:p9", Group: "pj", Cwd: r.roots["pj"], Agent: "claude", Label: "pj-int", Status: "idle"},
			{ID: "w2:p9", Group: "pq", Cwd: r.roots["pq"], Agent: "claude", Label: "pq-int", Status: "idle"},
			{ID: "w3:p9", Group: "pr", Cwd: r.roots["pr"], Agent: "claude", Label: "pr-int", Status: "idle"},
			{ID: "w1:p2", Group: "pj", Cwd: lane, Agent: "claude", Label: "pj-l-02", Status: "idle", Session: "s"},
		}}, false)
	}

	r.add("pj", "T-1", event.StepSubmitted, event.KindFact, submitData{Lane: "L-01"})
	r.add("pj", "T-1", event.StepSubmit, event.KindResult, submitOut{SHA: "s1"})
	r.add("pj", "T-5", event.StepSubmitted, event.KindFact, submitData{Lane: "L-05"})
	r.add("pj", "T-5", event.StepSubmit, event.KindResult, submitOut{SHA: "s5"})
	r.add("pq", "T-3", event.StepSubmitted, event.KindFact, submitData{Lane: "L-03"})
	r.add("pq", "T-3", event.StepSubmit, event.KindResult, submitOut{SHA: "s3"})
	r.add("pr", "T-4", event.StepSubmitted, event.KindFact, submitData{Lane: "L-04"})
	r.add("pr", "T-4", event.StepSubmit, event.KindResult, submitOut{SHA: "s4"})
	r.add("pr", "T-4", event.StepGate, event.KindIntent, gateIn{Kind: port.InputLane, Lane: "L-04", Input: "s4"})
	r.add("pr", "T-4", event.StepGate, event.KindResult, gateOut{Outcome: outGreen, Candidate: port.Candidate{Task: "T-4", SHA: "c-T-4",
		Target: "b", Patch: "p"}})
	r.add("pr", "T-4", event.StepReview, event.KindIntent, promptData{Agent: "pr-int", Job: "T-4/1/review", Patch: "p"})
	r.add("pr", "T-4", event.StepReview, event.KindResult, "sent")
	return r
}

// loadRoot loads a's Options.Root as loadProject does, failing the test on an error.
func loadRoot(t *testing.T, a *App) {
	t.Helper()
	p, err := a.load(a.opt.Root)
	if err != nil {
		t.Fatal(err)
	}
	a.mu.Lock()
	a.p = p
	a.reconcile(p)
	a.mu.Unlock()
}

// app is project name's app.
func (r *concRig) app(name string) *App { return r.apps[name] }

// status is name's ProjectStatus.
func (r *concRig) status(name string) wire.ProjectStatus {
	if ps := r.app(name).Status().Project; ps != nil && ps.Name == name {
		return *ps
	}
	r.t.Fatalf("no status of %s", name)
	return wire.ProjectStatus{}
}

// add appends a record to project name's log and folds it.
func (r *concRig) add(name, task, step, kind string, d any) {
	r.t.Helper()
	a, p := r.app(name), r.proj(name)
	raw, _ := json.Marshal(d)
	rec, err := p.log.Append(event.Record{Task: task, Attempt: 1, Step: step, Kind: kind, Data: raw}, a.opt.Now())
	if err != nil {
		r.t.Fatal(err)
	}
	a.mu.Lock()
	p.foldRecord(rec)
	a.mu.Unlock()
}

func (r *concRig) proj(name string) *proj {
	a := r.app(name)
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.p
}

// run starts each app's merge loop goroutine, kicked at once.
func (r *concRig) run() {
	ctx, cancel := context.WithCancel(context.Background())
	r.cancel, r.done = cancel, make(chan struct{})
	var wg sync.WaitGroup
	for _, a := range r.apps {
		wg.Add(1)
		go func() {
			defer wg.Done()
			a.mergeLoop(ctx)
		}()
		a.wake()
	}
	go func() {
		wg.Wait()
		close(r.done)
	}()
}

// wake kicks each app's merge loop.
func (r *concRig) wake() {
	for _, a := range r.apps {
		a.wake()
	}
}

// stop ends the merge loops and waits for them (and the scratch effects it waits for).
func (r *concRig) stop() {
	r.cancel()
	select {
	case <-r.done:
	case <-time.After(10 * time.Second):
		r.t.Fatal("the merge loop did not stop")
	}
}

// count is how many records of name's log for task have kind and step.
func (r *concRig) count(name, task, kind, step string) int {
	recs, err := event.ReadLog(r.roots[name])
	if err != nil {
		r.t.Fatal(err)
	}
	n := 0
	for _, rec := range recs {
		if rec.Task == task && rec.Kind == kind && rec.Step == step {
			n++
		}
	}
	return n
}

// waitFor polls cond (kicking the loop) until it holds.
func (r *concRig) waitFor(what string, cond func() bool) {
	r.t.Helper()
	for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); time.Sleep(5 * time.Millisecond) {
		if cond() {
			return
		}
		r.wake()
	}
	r.t.Fatalf("timed out waiting for %s", what)
}

// entered waits for name's next Gate call: its task.
func (r *concRig) entered(name string) string {
	r.t.Helper()
	select {
	case task := <-r.fakes[name].entered:
		return task
	case <-time.After(10 * time.Second):
		r.t.Fatalf("%s: no gate began", name)
		return ""
	}
}

func (r *concRig) state(name, task string) string {
	a, p := r.app(name), r.proj(name)
	a.mu.Lock()
	defer a.mu.Unlock()
	return p.folded.loop[task].shown()
}

func (r *concRig) running(name, task string) string {
	for _, ts := range r.status(name).Tasks {
		if ts.Task == task {
			return ts.Running
		}
	}
	return ""
}

func (f *concFakes) loopWithSpecs(kit string) *Loop {
	return &Loop{Workflow: WorkflowOf(f), Agents: f, Prompter: f, Specs: f, Kit: kit}
}

// needsForceErr is launch's refusal of a stop without force that only force gets past (port.NeedsForce).
type needsForceErr string

func (e needsForceErr) Error() string  { return string(e) }
func (needsForceErr) NeedsForce() bool { return true }

// callReply is a call's answer, for a call run in its own goroutine.
type callReply struct {
	reply string
	err   error
}

// refuseLogs makes every event-log append of name's project fail with err; nil heals it.
func (r *concRig) refuseLogs(name string, err error) {
	p := r.proj(name)
	p.log.Mu.Lock()
	defer p.log.Mu.Unlock()
	if err == nil {
		p.log.Refuse = nil
		return
	}
	p.log.Refuse = func(event.Record) error { return err }
}

// keptLoop is the Loop name's factory copies for each pass: a test sets its adapters here before a pass runs, as
// runStep and the rig's drivers see them. Never write it while a pass runs.
func (r *concRig) keptLoop(name string) *Loop { return r.kept[name] }

// concLoop is a Loop of name's app as its factory builds one now (a copy of the kept Loop on the config as it reads):
// the one a test passes to a pass it runs itself, and may write.
func (r *concRig) concLoop(name string) *Loop {
	a, p := r.app(name), r.proj(name)
	fx, err := a.opt.Loop(p.root)
	if err != nil {
		r.t.Fatal(err)
	}
	return fx
}

// addNightly appends a nightly record (its key: attempt 0) at at and folds it.
func (r *concRig) addNightly(step, kind string, d any, at time.Time) {
	r.t.Helper()
	a, p := r.app("pj"), r.proj("pj")
	raw, _ := json.Marshal(d)
	rec, err := p.log.Append(event.Record{Task: event.NightlyTask, Step: step, Kind: kind, Data: raw}, at)
	if err != nil {
		r.t.Fatal(err)
	}
	a.mu.Lock()
	p.foldRecord(rec)
	a.mu.Unlock()
}

// runStep runs name's task's next production step once (App.next: begin, the step, release) and waits for the effects
// it started. A gate step always returns false: it waits, whether it is held or its gate runs in its own goroutine.
func (r *concRig) runStep(name, task string) {
	r.t.Helper()
	a := r.app(name)
	fx := r.concLoop(name)
	a.mu.Lock()
	p := a.p
	st := a.next(p, p.folded.loop[task], cfgAt(p.root))
	a.mu.Unlock()
	if st == nil {
		r.t.Fatalf("%s: no step for %s: state %s", name, task, r.state(name, task))
	}
	st(context.Background(), fx)
	a.effects.Wait()
}

// diskFactsInForce is name's project's low-disk facts in force.
func (r *concRig) diskFactsInForce(name string) []event.Record {
	a, p := r.app(name), r.proj(name)
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]event.Record(nil), p.folded.active[event.DiskTask]...)
}

// dropCall is the lead's obor drop of task in project name.
func (r *concRig) dropCall(name, task, id string) (string, error) {
	return r.app(name).drop(wire.DropReq{Envelope: wire.Envelope{Root: r.roots[name], Agent: "lead", Role: "lead",
		CallID: id}, Task: task, Why: "abandoned"})
}

func (r *concRig) entered2(name string) string { return r.entered(name) }

// row is the task L-02's row holds.
func (r *concRig) row(p *proj) string {
	r.a.mu.Lock()
	defer r.a.mu.Unlock()
	return p.laneTask("L-02")
}

// held is pj's T-2 needs-you reason in force ("": none).
func (r *concRig) heldWhy(p *proj, task string) string {
	r.a.mu.Lock()
	defer r.a.mu.Unlock()
	if mt := p.folded.loop[task]; mt != nil && mt.needs.held() {
		return mt.needs.fact.Fact().Reason
	}
	return ""
}

// A dropped fact is terminal for the task whatever attempt it names; any record of the task
// after it is ignored, and only a new dispatch reopens it.

// addAt is add at attempt n.
func (r *concRig) addAt(name, task string, n int, step, kind string, d any) {
	r.t.Helper()
	a, p := r.app(name), r.proj(name)
	raw, _ := json.Marshal(d)
	rec, err := p.log.Append(event.Record{Task: task, Attempt: n, Step: step, Kind: kind, Data: raw}, a.opt.Now())
	if err != nil {
		r.t.Fatal(err)
	}
	a.mu.Lock()
	p.foldRecord(rec)
	a.mu.Unlock()
}

// worker puts an idle claude pane labelled agent into proj's group and reconciles its identity table.
func (r *concRig) worker(proj, id, agent string) {
	a, p := r.app(proj), r.proj(proj)
	a.mu.Lock()
	defer a.mu.Unlock()
	a.obs[id] = &seen{pane: port.Pane{ID: id, Group: proj, Cwd: r.roots[proj], Agent: "claude", Label: agent,
		Status: "idle"}}
	a.reconcile(p)
}

// dispatchT2 records pj's T-2 dispatch onto lane L-02 (its worker's pane is in the rig).
func dispatchT2(r *concRig) {
	r.add("pj", "T-2", event.StepDispatched, event.KindFact, dispatchData{Lane: "L-02", Dir: filepath.Join(filepath.Dir(r.roots["pj"]),
		"pj-wt", "L-02"), Agent: "pj-l-02", Target: "target", Branch: "task/T-2"})
	p := r.proj("pj")
	r.a.mu.Lock()
	p.dispatching = true
	r.a.mu.Unlock()
}

// callAnswer is a call's reply.
type callAnswer struct {
	reply string
	err   error
}

// dropWithin is the lead's drop of task, which must answer within a second: drop never waits.
func (r *concRig) dropWithin(name, task, id string) callAnswer {
	r.t.Helper()
	got := make(chan callAnswer, 1)
	go func() {
		reply, err := r.dropCall(name, task, id)
		got <- callAnswer{reply, err}
	}()
	select {
	case a := <-got:
		return a
	case <-time.After(time.Second):
		r.t.Fatalf("drop %s did not answer within a second", task)
		return callAnswer{}
	}
}

// ended is whether a drop's mark on task of project name is set.
func (r *concRig) ended(name, task string) bool {
	a, p := r.app(name), r.proj(name)
	a.mu.Lock()
	defer a.mu.Unlock()
	return p.ended[task]
}

// pauseDrop makes project name's log hold the dropped fact's write until the returned release is called; writing is
// closed when it starts.
func (r *concRig) pauseDrop(name string) (writing chan struct{}, release func()) {
	p := r.proj(name)
	writing, gate := make(chan struct{}), make(chan struct{})
	p.log.Refuse = func(rec event.Record) error {
		if rec.Step == event.StepDropped {
			close(writing)
			<-gate
		}
		return nil
	}
	return writing, func() { close(gate) }
}

// showsRestart: obor status' running line shows the pending self-restart.
func (r *concRig) showsRestart(name string) bool {
	for _, s := range r.status(name).Running {
		if s.Step == event.StepServeRestart {
			return true
		}
	}
	return false
}

// leadSendback is the lead's obor resume --sendback of task in project name.
func (r *concRig) leadSendback(name, task, id string) (string, error) {
	return r.app(name).resume(wire.ResumeReq{Envelope: wire.Envelope{Root: r.roots[name], Agent: "pq-lead",
		Role: "lead", CallID: id}, Task: task, Flag: wire.ResumeSendback, Text: "fix the edge case"})
}

// factsOf is how many facts obor serve's status shows in force for task of project name.
func (r *concRig) factsOf(name, task string) int {
	n := 0
	for _, f := range r.status(name).Facts {
		if f.Task == task {
			n++
		}
	}
	return n
}

// attempt is task's current attempt in project name.
func (r *concRig) attempt(name, task string) int {
	a, p := r.app(name), r.proj(name)
	a.mu.Lock()
	defer a.mu.Unlock()
	return p.folded.loop[task].attempt
}

// A broken event log is visible. Serve loads the one project at Options.Root.

// rootApp is an app serving root; out is its output.
func rootApp(root string, out *bytes.Buffer) *App {
	now := time.Date(2026, 10, 27, 12, 0, 0, 0, time.UTC)
	return newTestApp(Options{Root: root, Out: out, Now: func() time.Time { return now }}, &fakePanes{}, &fakeLanes{})
}

// projectRoot is project pj in a temp dir.
func projectRoot(t *testing.T) string {
	t.Helper()
	root := filepath.Join(t.TempDir(), "pj")
	if err := os.MkdirAll(filepath.Join(root, ".obor"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".obor", "config.json"), []byte(`{"version":2,"project":{"name":"pj"}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	return root
}
