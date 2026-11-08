package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"obor/internal/app/event"
	"obor/internal/app/port"
	"obor/internal/app/wire"
	"obor/internal/store"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// T-369 (docs/state-model.md "Concurrency"): a gate runs in its own goroutine holding its project's scratch; the
// merge loop goes on with every other step meanwhile. The fakes below are safe for the loop goroutine, the gate's
// goroutine and the test at once.

// concFakes is one project's adapters: its Scratch the package's one fake (FakeWorkflow: gates green, a held gate,
// publish, resolved check or lane test waits for its release), its Agents, Typer, LaneSetup and Specs.
type concFakes struct {
	*FakeWorkflow
	restarts int // under the FakeWorkflow's mu
	// obor drop by lane (T-439): the stops (agent, force) and Free inputs; refuse: Free's refusal ("": frees).
	stops  []string
	frees  []port.FreeInput
	refuse string
	// ignoresEsc: the worker keeps working after Escape, as launch.Stop sees it (T-773): a stop without force fails.
	ignoresEsc bool
	// stopFails: a stop's error by force ("" : none): a quit timeout, a forced stop that fails (RV-98 #2).
	stopFails func(force bool) string
	// freeErrs: how many Free calls fail first, as a cut git does (T-852: retried, never a half-done drop).
	freeErrs int
}

// newConcFakes is a project's adapters whose gate of hold waits for release; the target is at "b", and a candidate
// has no Base unless the test says so (unbased false: a lane test follows its gate, T-961).
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
func (f *concFakes) Type(context.Context, string, string, bool) (bool, error) {
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

// concRig is three projects, each on its own app (obor serve serves one project, T-522): pj (workspace w1; T-1
// submitted and held in its gate, T-5 submitted behind it, T-2 dispatched onto lane L-02; the dispatch switch on), pq
// (w2; T-3 submitted) and pr (w3; T-4 green, its review job out). Each workspace runs its integrator; L-02's pane runs
// pj's worker. r.a is pj's app.
type concRig struct {
	t      *testing.T
	a      *App
	apps   map[string]*App   // project → its app
	roots  map[string]string // project → root
	fakes  map[string]*concFakes
	kit    string
	cancel context.CancelFunc
	done   chan struct{}
}

func newConcRig(t *testing.T) *concRig {
	base := t.TempDir()
	kit, _ := filepath.Abs("../..")
	r := &concRig{t: t, apps: map[string]*App{}, roots: map[string]string{}, fakes: map[string]*concFakes{}, kit: kit}
	now := time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC)
	lane := filepath.Join(base, "pj-wt", "L-02")
	for _, name := range []string{"pj", "pq", "pr"} {
		root := filepath.Join(base, name)
		if err := os.MkdirAll(filepath.Join(root, ".obor"), 0o755); err != nil {
			t.Fatal(err)
		}
		cfg := `{"name":"` + name + `","prefix":"` + name + `","roles":{"integrator":""}}`
		if err := os.WriteFile(filepath.Join(root, ".obor", "config.json"), []byte(cfg), 0o644); err != nil {
			t.Fatal(err)
		}
		r.roots[name] = root
		f := newConcFakes("")
		r.fakes[name] = f
		fx := &Loop{Workflow: WorkflowOf(f), Agents: f, Typer: f, Kit: r.kit}
		if name == "pj" {
			fx.Specs = f
		}
		a := New(Options{Root: root, Now: func() time.Time { return now }}, &fakePanes{}, &fakeLanes{})
		a.opt.Loop = func(string) (*Loop, error) { return fx, nil }
		loadRoot(t, a)
		r.apps[name] = a
	}
	r.a = r.apps["pj"]
	r.fakes["pj"].hold = "T-1"
	for _, a := range r.apps { // one herdr session: each app watches its own workspace
		a.connect(context.Background(), port.Snapshot{Workspaces: map[string]string{"w1": "pj", "w2": "pq", "w3": "pr"},
			Panes: []port.Pane{
				{ID: "w1:p9", Workspace: "w1", Cwd: r.roots["pj"], Agent: "claude", Label: "pj-int", Status: "idle"},
				{ID: "w2:p9", Workspace: "w2", Cwd: r.roots["pq"], Agent: "claude", Label: "pq-int", Status: "idle"},
				{ID: "w3:p9", Workspace: "w3", Cwd: r.roots["pr"], Agent: "claude", Label: "pr-int", Status: "idle"},
				{ID: "w1:p2", Workspace: "w1", Cwd: lane, Agent: "claude", Label: "pj-l-02", Status: "idle", Session: "s"},
			}})
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

// T-369: while pj's T-1 gate is held, T-2's dispatch in pj runs lane.prepare, agent.start and its work prompt, pq's
// T-3 gates, and a verdict on pr's T-4 publishes it; pj's T-5 does not gate (one scratch). Released, T-1's gate folds
// once (one intent, one result: its review job follows), then T-5 gates; pj's scratch calls never overlapped.
func TestHeldGateBlocksNothingElse(t *testing.T) {
	t.Parallel()
	r := newConcRig(t)
	r.add("pj", "T-2", event.StepDispatched, event.KindFact, dispatchData{Lane: "L-02", Dir: filepath.Join(filepath.Dir(r.roots["pj"]),
		"pj-wt", "L-02"), Agent: "pj-l-02", Target: "target", Branch: "task/T-2"})
	r.run()
	defer r.stop()
	if task := r.entered("pj"); task != "T-1" {
		t.Fatalf("pj's first gate: %s; want T-1", task)
	}
	if got := r.running("pj", "T-1"); got != event.StepGate {
		t.Errorf("obor status --facts: T-1 running %q; want gate", got)
	}

	if task := r.entered("pq"); task != "T-3" {
		t.Fatalf("pq's gate: %s; want T-3", task)
	}
	r.waitFor("pq's T-3 gate result and pj's T-2 work prompt", func() bool {
		return r.count("pq", "T-3", event.KindResult, event.StepGate) == 1 && r.count("pj", "T-2", event.KindResult, event.StepWork) == 1
	})
	for _, step := range []string{event.StepPrepare, event.StepStartWork} {
		if r.count("pj", "T-2", event.KindResult, step) != 1 {
			t.Errorf("T-2's %s: no result while T-1's gate is held", step)
		}
	}
	if _, err := r.app("pr").verdict(context.Background(), wire.MethodAccept, wire.Call{Root: r.roots["pr"], Task: "T-4",
		Role: "integrator", Agent: "pr-int", CallID: "v1", Job: "T-4/1/review", Patch: "p"}); err != nil {
		t.Fatal(err)
	}
	r.waitFor("pr's T-4 published and merged", func() bool { return r.state("pr", "T-4") == stMerged })
	if n := r.fakes["pr"].count(r.fakes["pr"].pubs, "T-4"); n != 1 {
		t.Errorf("T-4 published %d times; want 1", n)
	}

	// Still held: T-1 has an intent and no result, T-5 has not gated.
	if r.count("pj", "T-1", event.KindIntent, event.StepGate) != 1 || r.count("pj", "T-1", event.KindResult, event.StepGate) != 0 {
		t.Fatalf("held: T-1's gate intent %d result %d; want 1, 0", r.count("pj", "T-1", event.KindIntent, event.StepGate),
			r.count("pj", "T-1", event.KindResult, event.StepGate))
	}
	if n := r.fakes["pj"].count(r.fakes["pj"].gates, "T-5"); n != 0 {
		t.Fatalf("T-5 gated %d times while T-1's gate holds the scratch", n)
	}

	close(r.fakes["pj"].release)
	if task := r.entered("pj"); task != "T-5" {
		t.Fatalf("pj's next gate: %s; want T-5", task)
	}
	r.waitFor("T-1 awaiting its review, T-5 gated", func() bool {
		return r.state("pj", "T-1") == stAwaiting && r.count("pj", "T-5", event.KindResult, event.StepGate) == 1
	})
	if i, res := r.count("pj", "T-1", event.KindIntent, event.StepGate), r.count("pj", "T-1", event.KindResult, event.StepGate); i != 1 || res != 1 {
		t.Errorf("T-1's gate: %d intents, %d results; want one each", i, res)
	}
	if n := r.fakes["pj"].count(r.fakes["pj"].gates, "T-1"); n != 1 {
		t.Errorf("T-1 gated %d times; want 1", n)
	}
	if got := r.running("pj", "T-1"); got != "" {
		t.Errorf("T-1 still shown running %q", got)
	}
	f := r.fakes["pj"]
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.max != 1 {
		t.Errorf("pj's scratch calls at once: %d; want 1", f.max)
	}
}

// T-369: a stop (a crash's cut) with T-1's gate running leaves its intent pending, no failure counted; a replay
// re-runs the gate once under the same key: one intent, one result.
func TestGateCutReplayRunsOnce(t *testing.T) {
	t.Parallel()
	r := newConcRig(t)
	r.run()
	if task := r.entered("pj"); task != "T-1" {
		t.Fatalf("pj's first gate: %s; want T-1", task)
	}
	r.stop()
	if r.count("pj", "T-1", event.KindIntent, event.StepGate) != 1 || r.count("pj", "T-1", event.KindResult, event.StepGate) != 0 ||
		r.count("pj", "T-1", event.KindFact, event.StepFailed) != 0 {
		t.Fatalf("cut: want T-1's gate intent pending, no result, no failure counted")
	}

	p, err := r.a.load(r.roots["pj"]) // the restart: pj's log replayed
	if err != nil {
		t.Fatal(err)
	}
	r.a.mu.Lock()
	r.a.p = p
	r.a.mu.Unlock()
	close(r.fakes["pj"].release)
	r.run()
	defer r.stop()
	if task := r.entered("pj"); task != "T-1" {
		t.Fatalf("pj's gate after the replay: %s; want T-1", task)
	}
	r.waitFor("T-1's gate result", func() bool { return r.count("pj", "T-1", event.KindResult, event.StepGate) == 1 })
	if i := r.count("pj", "T-1", event.KindIntent, event.StepGate); i != 1 {
		t.Errorf("T-1's gate intents: %d; want 1 (the replay reuses the pending key)", i)
	}
	if n := r.fakes["pj"].count(r.fakes["pj"].gates, "T-1"); n != 2 {
		t.Errorf("T-1's Gate calls: %d; want 2 (the cut one, the replay's)", n)
	}
}

// RV-48 #1: a failed lane-row write fails the step that caused it (no worker on a missing row) and shows in status;
// once the write works the step goes on.
func TestLaneRowWriteFailureStopsStep(t *testing.T) {
	t.Parallel()
	r := newConcRig(t)
	defer r.a.closeLog()
	dispatchT2(r)
	p, f := r.proj("pj"), r.fakes["pj"]
	if _, err := p.log.DB().Exec(`CREATE TRIGGER lanes_fail BEFORE INSERT ON lanes WHEN NEW.lane = 'L-02' BEGIN SELECT RAISE(FAIL, 'injected lane-row failure'); END`); err != nil {
		t.Fatal(err)
	}
	fx, err := r.a.loopOf(p)
	if err != nil {
		t.Fatal(err)
	}
	r.a.drive(context.Background(), p, fx, "T-2")
	if f.restarts != 0 {
		t.Error("worker started on a missing lane row")
	}
	if got := r.status("pj").LogError; !strings.Contains(got, "injected lane-row failure") {
		t.Errorf("status log_error = %q, want the lane-row error", got)
	}
	if _, err := p.log.DB().Exec("DROP TRIGGER lanes_fail"); err != nil {
		t.Fatal(err)
	}
	r.a.drive(context.Background(), p, fx, "T-2")
	if row, ok, err := store.ReadLaneAt(p.root, "L-02"); err != nil || !ok || row.Task != "T-2" {
		t.Errorf("row after the retry = %+v %v %v, want T-2", row, ok, err)
	}
	if f.restarts != 1 {
		t.Errorf("worker starts after the retry = %d, want 1", f.restarts)
	}
	if got := r.status("pj").LogError; got != "" {
		t.Errorf("status log_error after the retry = %q, want none", got)
	}
}

// RV-48 #1 (persistent): while the lane-row write keeps failing, no task effect starts, on any pass; the step's
// result is folded but the row is neither missing nor stale for a worker. Removing the failure repairs the row and
// the task goes on. The control is the failure removed: the worker starts.
func TestLaneRowWriteFailurePersistent(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		prev bool // the lane holds a previous task's row (T-0) the failed writes cannot replace
	}{
		{"absent row", false},
		{"previous task's row", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			r := newConcRig(t)
			defer r.a.closeLog()
			dispatchT2(r)
			p, f := r.proj("pj"), r.fakes["pj"]
			if c.prev {
				prev := []store.LaneRow{{Lane: "L-02", Task: "T-0", Branch: "task/T-0", Base: "b",
					Head: "h", DispatchedAt: 1}}
				if err := store.WriteLanes(p.log.DB(), prev); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := p.log.DB().Exec(`CREATE TRIGGER lanes_fail BEFORE INSERT ON lanes WHEN NEW.lane = 'L-02' BEGIN SELECT RAISE(FAIL, 'injected lane-row failure'); END`); err != nil {
				t.Fatal(err)
			}
			fx, err := r.a.loopOf(p)
			if err != nil {
				t.Fatal(err)
			}
			for pass := 1; pass <= 4; pass++ {
				r.a.drive(context.Background(), p, fx, "T-2")
				if got := r.status("pj").LogError; !strings.Contains(got, "injected lane-row failure") {
					t.Fatalf("pass %d: status log_error = %q, want the lane-row error", pass, got)
				}
				if row, ok, err := store.ReadLaneAt(p.root, "L-02"); err != nil || (ok && row.Task == "T-2") {
					t.Fatalf("pass %d: row = %+v %v %v; want not T-2 while the write fails", pass, row, ok, err)
				}
				if f.restarts != 0 {
					t.Fatalf("pass %d: worker started on a missing or stale lane row (restarts %d)", pass, f.restarts)
				}
			}
			if _, err := p.log.DB().Exec("DROP TRIGGER lanes_fail"); err != nil {
				t.Fatal(err)
			}
			r.a.drive(context.Background(), p, fx, "T-2")
			if row, ok, err := store.ReadLaneAt(p.root, "L-02"); err != nil || !ok || row.Task != "T-2" {
				t.Errorf("row after the failure is removed = %+v %v %v, want T-2", row, ok, err)
			}
			if f.restarts != 1 {
				t.Errorf("worker starts after the failure is removed = %d, want 1", f.restarts)
			}
			if got := r.status("pj").LogError; got != "" {
				t.Errorf("status log_error after the repair = %q, want none", got)
			}
		})
	}
}

func (f *concFakes) loopWithSpecs(kit string) *Loop {
	return &Loop{Workflow: WorkflowOf(f), Agents: f, Typer: f, Specs: f, Kit: kit}
}

// pauseSpecs is the spec check of a dispatch: it holds the admission at pause.
type pauseSpecs struct {
	*concFakes
	pause func()
}

func (s pauseSpecs) Check(context.Context, string, string) (string, error) {
	s.pause()
	return "", nil
}

// dispatchFacts is how many fact dispatch records task has in root's log.
func dispatchFacts(t *testing.T, root, task string) int {
	t.Helper()
	recs, err := event.ReadLog(root)
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, rec := range recs {
		if rec.Task == task && rec.Step == event.StepDispatched && rec.Kind == event.KindFact {
			n++
		}
	}
	return n
}

// #3: a pause waits for a dispatch in admission (it holds a.verdicts); the dispatch is admitted before PAUSED returns,
// and a dispatch after the pause is refused with its reason.
func TestPauseWaitsForDispatchAdmission(t *testing.T) {
	t.Parallel()
	r := newConcRig(t)
	defer r.a.closeLog()
	p := r.proj("pj")
	f := r.fakes["pj"]
	r.a.lanes = &fakeLanes{lanes: []port.Lane{{Name: "L-02", Dir: filepath.Join(t.TempDir(), "L-02"), State: "free"}}}
	fx := f.loopWithSpecs(r.kit)
	entered, release := make(chan struct{}), make(chan struct{})
	fx.Specs = pauseSpecs{f, func() { close(entered); <-release }}
	r.a.opt.Loop = func(string) (*Loop, error) { return fx, nil }

	type result struct {
		reply string
		err   error
	}
	done := make(chan result, 1)
	go func() {
		reply, err := r.a.dispatchCall(context.Background(), wire.Call{Root: p.root, CallID: "d1", Task: "T-8", Lane: "L-02", Types: []string{"sonnet"}})
		done <- result{reply, err}
	}()
	<-entered

	paused := make(chan result, 1)
	go func() {
		reply, err := r.a.pauseCall(wire.Call{Root: p.root, CallID: "p1", Note: "quota"})
		paused <- result{reply, err}
	}()
	select {
	case got := <-paused:
		t.Fatalf("PAUSED replied while a dispatch was in admission: %q, %v", got.reply, got.err)
	case <-time.After(50 * time.Millisecond):
	}
	close(release)
	disp := <-done
	pr := <-paused
	if pr.err != nil {
		t.Fatal(pr.err)
	}
	if disp.err != nil {
		t.Fatalf("the dispatch in admission before the pause: %v", disp.err)
	}
	if n := dispatchFacts(t, p.root, "T-8"); n != 1 {
		t.Fatalf("T-8 dispatch facts %d when PAUSED replied, want the admitted dispatch (1)", n)
	}
	t.Logf("pause replied %q after the admitted dispatch", pr.reply)

	_, err := r.a.dispatchCall(context.Background(), wire.Call{Root: p.root, CallID: "d2", Task: "T-9", Lane: "L-02", Types: []string{"sonnet"}})
	if err == nil || !strings.Contains(err.Error(), "paused: quota") {
		t.Errorf("dispatch after PAUSED: %v, want refused with the reason", err)
	}
}

// #4: a waiting task has no lane, so its needs-you names the task in the drop command, never <LANE>.
func TestWaitingNeedsYouDropCommand(t *testing.T) {
	t.Parallel()
	raw, _ := json.Marshal(event.FactData{Merge: true, Cause: causeAfter, Reason: "after: T-8 waits for T-1, which is dropped"})
	r := event.Record{Seq: 1, Task: "T-8", Step: event.StepNeedsYou, Data: raw}
	path := answerPath(nil, r, mtask{task: "T-8", state: stWaiting, needs: block{fact: r}})
	t.Log(path)
	if strings.Contains(path, "<LANE>") {
		t.Error("waiting task has no lane; advertised drop command cannot run")
	}
}

// replayProj reloads p from its log in a new App, as a restart does.
func replayProj(t *testing.T, r *concRig, p *proj) (*App, *proj) {
	t.Helper()
	b := New(Options{Now: r.a.opt.Now, Loop: r.a.opt.Loop}, &fakePanes{}, &fakeLanes{})
	q, err := b.load(p.root)
	if err != nil {
		t.Fatal(err)
	}
	b.p = q
	t.Cleanup(func() { q.log.Close(); b.closeLog() })
	return b, q
}

// notifyNightly appends an old log's notification intent of p's nightly needs-you in force, with its result when done
// (T-949: nothing runs these steps now; the replay must still ignore them).
func notifyNightly(t *testing.T, r *concRig, p *proj, done bool) {
	t.Helper()
	if len(p.folded.active[event.NightlyTask]) == 0 {
		t.Fatal("precondition: no nightly needs-you in force")
	}
	step := "notify." + fmt.Sprint(p.folded.active[event.NightlyTask][0].Seq)
	if _, err := p.log.Append(event.Record{Task: event.NightlyTask, Step: step, Kind: event.KindIntent, Class: event.ClassFact}, r.a.opt.Now()); err != nil {
		t.Fatal(err)
	}
	if done {
		raw, _ := json.Marshal("notified")
		if _, err := p.log.Append(event.Record{Task: event.NightlyTask, Step: step, Kind: event.KindResult, Data: raw}, r.a.opt.Now()); err != nil {
			t.Fatal(err)
		}
	}
}

// RV-49 #1 (T-468): the nightly's notification records (notify.<seq>, task key nightly) never move its run cursor: a
// restart replays the run's own step, and a day with a result, or with tries failures, runs no further check.
func TestNightlyNotificationsKeepRunCursor(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		name  string
		build func(t *testing.T, r *concRig, p *proj, day string) // the log before the restart
		done  bool                                                // the replayed run has a result
		fails int                                                 // the replayed day's counted failures
	}{
		{"completed RED + pending notification", func(t *testing.T, r *concRig, p *proj, day string) {
			r.a.record(p, event.Record{Task: event.NightlyTask, Step: day, Kind: event.KindIntent, Class: event.ClassGit}, nil)
			r.a.record(p, event.Record{Task: event.NightlyTask, Step: day, Kind: event.KindResult}, NightlyOut{SHA: "abc1234", Verdict: "RED"})
			r.a.nightlyVerdict(p, NightlyOut{SHA: "abc1234", Verdict: "RED"})
			notifyNightly(t, r, p, false)
		}, true, 0},
		{"three failures + completed notification", func(t *testing.T, r *concRig, p *proj, day string) {
			r.a.record(p, event.Record{Task: event.NightlyTask, Step: day, Kind: event.KindIntent, Class: event.ClassGit}, nil)
			for i := 0; i < tries; i++ {
				r.a.nightlyFailed(p, day, errors.New("cannot take the lock"))
			}
			notifyNightly(t, r, p, true)
		}, false, tries},
		{"a pending notification after a failed run", func(t *testing.T, r *concRig, p *proj, day string) {
			r.a.record(p, event.Record{Task: event.NightlyTask, Step: day, Kind: event.KindIntent, Class: event.ClassGit}, nil)
			for i := 0; i < tries; i++ {
				r.a.nightlyFailed(p, day, errors.New("cannot take the lock"))
			}
			notifyNightly(t, r, p, false)
		}, false, tries},
		{"a prior day's notification interleaved with a later run", func(t *testing.T, r *concRig, p *proj, day string) {
			prior := event.StepNightly + "." + r.a.opt.Now().Local().AddDate(0, 0, -1).Format("2006-01-02")
			r.a.record(p, event.Record{Task: event.NightlyTask, Step: prior, Kind: event.KindIntent, Class: event.ClassGit}, nil)
			r.a.record(p, event.Record{Task: event.NightlyTask, Step: prior, Kind: event.KindResult}, NightlyOut{SHA: "abc1234", Verdict: "RED"})
			r.a.nightlyVerdict(p, NightlyOut{SHA: "abc1234", Verdict: "RED"})
			r.a.record(p, event.Record{Task: event.NightlyTask, Step: day, Kind: event.KindIntent, Class: event.ClassGit}, nil)
			notifyNightly(t, r, p, false)
			r.a.record(p, event.Record{Task: event.NightlyTask, Step: day, Kind: event.KindResult}, NightlyOut{SHA: "abc1234", Verdict: "RED"})
		}, true, 0},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			r := newConcRig(t)
			p := r.proj("pj")
			now := r.a.opt.Now().Local()
			if now.Hour() < 2 {
				t.Fatalf("precondition: the rig's clock %v is before merge.nightly_at 02:00", now)
			}
			day := event.StepNightly + "." + now.Format("2006-01-02")
			c.build(t, r, p, day)
			b, q := replayProj(t, r, p)
			if q.night.step != day || q.night.done != c.done || q.night.fails[day] != c.fails {
				t.Errorf("replayed run step=%s done=%v fails=%d; want %s %v %d", q.night.step, q.night.done,
					q.night.fails[day], day, c.done, c.fails)
			}
			runs := 0
			n := &Nightly{At: "02:00", Run: func(context.Context) (NightlyOut, error) {
				runs++
				return NightlyOut{SHA: "abc1234", Verdict: "GREEN"}, nil
			}}
			b.nightly(context.Background(), q, &Loop{Nightly: n})
			b.effects.Wait()
			if runs != 0 {
				t.Errorf("nightly ran %d more times after the restart, want 0", runs)
			}
			recs, err := event.ReadLog(p.root)
			if err != nil {
				t.Fatal(err)
			}
			for _, rec := range recs {
				if rec.Task == event.NightlyTask && rec.Kind == event.KindResult && strings.HasPrefix(rec.Step, "notify.") && string(rec.Data) != `"notified"` {
					t.Errorf("a nightly outcome recorded under its notification's key %s: %s", rec.Step, rec.Data)
				}
			}
		})
	}
}

// needsForceErr is launch's refusal of a stop without force that only force gets past (port.CodeNeedsForce).
type needsForceErr string

func (e needsForceErr) Error() string   { return string(e) }
func (needsForceErr) ErrorCode() string { return port.CodeNeedsForce }
