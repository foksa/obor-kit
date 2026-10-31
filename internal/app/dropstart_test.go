package app

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// RV-42 #1 (T-392, T-395): a step the merge loop selected starts only after one atomic check that its task/attempt
// is current and not ended, which also holds the task; drop never waits: under the same lock it is refused while a
// step holds the task, or marks the task ended before it records dropped.

// dropFx is a project's loop adapters over its concScratch that note every effect; the effect named block waits for
// unblock (blocked is closed first).
type dropFx struct {
	*concScratch
	mu      sync.Mutex
	effects []string
	block   string
	blocked chan struct{}
	unblock chan struct{}
}

func (f *dropFx) note(what string) {
	f.mu.Lock()
	f.effects = append(f.effects, what)
	f.mu.Unlock()
	if what == f.block {
		close(f.blocked)
		<-f.unblock
	}
}

func (f *dropFx) ran() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.effects...)
}

func (f *dropFx) Gate(ctx context.Context, in GateInput) (GateOutcome, error) {
	f.note("gate")
	return f.concScratch.Gate(ctx, in)
}
func (f *dropFx) Publish(ctx context.Context, c Candidate) (PublishOutcome, error) {
	f.note("publish")
	return f.concScratch.Publish(ctx, c)
}
func (f *dropFx) Submit(ctx context.Context, task, id, lane, summary, worker string) (string, error) {
	f.note("submit")
	return f.concScratch.Submit(ctx, task, id, lane, summary, worker)
}
func (f *dropFx) Launch(ctx context.Context, l Launch) (Started, error) {
	f.note("restart")
	return f.concScratch.Launch(ctx, l)
}
func (f *dropFx) Stop(ctx context.Context, agent, pane string, force bool) (string, error) {
	f.note("stop")
	return f.concScratch.Stop(ctx, agent, pane, force)
}
func (f *dropFx) Type(ctx context.Context, agent, text string, clear bool) (bool, error) {
	f.note("prompt")
	return true, nil
}
func (f *dropFx) Prepare(ctx context.Context, in PrepareInput) (Prepared, error) {
	f.note("prepare")
	return f.concScratch.Prepare(ctx, in)
}
func (f *dropFx) Unpin(ctx context.Context, task string) error {
	f.note("unpin")
	return nil
}
func (f *dropFx) AfterMerge(ctx context.Context, c Candidate) (string, error) {
	f.note("after")
	return "ran", nil
}

// loop is fx as a project's adapters (the dispatch's too).
func (f *dropFx) loop(kit string) *Loop {
	return &Loop{Scratch: f, Agents: f, Typer: f, Lane: f, Specs: f, Kit: kit}
}

// worker puts an idle claude pane labelled agent into proj's workspace and reconciles its identity table.
func (r *concRig) worker(proj, id, agent string) {
	a, p := r.app(proj), r.proj(proj)
	ws := map[string]string{"pj": "w1", "pq": "w2", "pr": "w3"}[proj]
	a.mu.Lock()
	defer a.mu.Unlock()
	a.obs[id] = &seen{pane: Pane{ID: id, Workspace: ws, Cwd: r.roots[proj], Agent: "claude", Label: agent,
		Status: "idle"}}
	a.reconcile(p)
}

// dispatchT2 records pj's T-2 dispatch onto lane O-02 (its worker's pane is in the rig).
func dispatchT2(r *concRig) {
	r.add("pj", "T-2", StepDispatched, KindFact, dispatchData{Lane: "O-02", Dir: filepath.Join(filepath.Dir(r.roots["pj"]),
		"pj-wt", "O-02"), Agent: "pj-o-02", Target: "target", Branch: "task/T-2"})
	p := r.proj("pj")
	r.a.mu.Lock()
	p.dispatching = true
	r.a.mu.Unlock()
}

// dropStartCases is one per selected step kind (RV-42 #1's Extent): the setup leaves task of project proj with that
// step next, and effect is what the step does first.
var dropStartCases = []struct {
	name, proj, task, effect string
	setup                    func(r *concRig)
}{
	{"lane gate", "pj", "T-1", "gate", func(*concRig) {}},
	{"publish", "pr", "T-4", "publish", acceptT4},
	{"moved gate (gate.2)", "pr", "T-4", "gate", func(r *concRig) {
		acceptT4(r)
		r.add("pr", "T-4", StepPublish, KindResult, gateOut{Outcome: outMoved, Line: "moved"})
	}},
	{"numbered publish (publish.2)", "pr", "T-4", "publish", func(r *concRig) {
		acceptT4(r)
		r.add("pr", "T-4", StepPublish, KindResult, gateOut{Outcome: outMoved, Line: "moved"})
		r.add("pr", "T-4", nth(StepGate, 2), KindResult, gateOut{Outcome: outGreen, Candidate: Candidate{Task: "T-4",
			SHA: "c2", Target: "b", Patch: "p"}})
		r.add("pr", "T-4", StepKept, KindFact, JobData{Patch: "p"})
	}},
	{"resolved gate", "pq", "T-3", "gate", func(r *concRig) {
		r.add("pq", "T-3", StepGate, KindResult, gateOut{Outcome: outConflict, Candidate: Candidate{Task: "T-3", SHA: "c",
			Target: "b", Patch: "p"}})
		r.add("pq", "T-3", StepResolve, KindIntent, promptData{Agent: "pq-int", Job: "T-3/1/resolve", Patch: "p"})
		r.add("pq", "T-3", StepResolve, KindResult, "sent")
		r.add("pq", "T-3", verdictStep(MethodResolved, "r1"), KindResult, verdictResult{Job: "T-3/1/resolve", SHA: "m"})
	}},
	{"submit", "pq", "T-6", "submit", func(r *concRig) {
		r.add("pq", "T-6", StepSubmitted, KindFact, submitData{Lane: "O-06"})
	}},
	{"review", "pq", "T-3", "restart", func(r *concRig) {
		r.add("pq", "T-3", StepGate, KindResult, gateOut{Outcome: outGreen, Candidate: Candidate{Task: "T-3", SHA: "c",
			Target: "b", Patch: "p"}})
	}},
	{"resolve", "pq", "T-3", "restart", func(r *concRig) {
		r.add("pq", "T-3", StepGate, KindResult, gateOut{Outcome: outConflict, Candidate: Candidate{Task: "T-3", SHA: "c",
			Target: "b", Patch: "p"}})
	}},
	{"prepare", "pj", "T-2", "prepare", dispatchT2},
	{"start", "pj", "T-2", "restart", func(r *concRig) {
		dispatchT2(r)
		r.add("pj", "T-2", StepPrepare, KindResult, prepareOut{Base: "b"})
	}},
	{"work", "pj", "T-2", "prompt", func(r *concRig) {
		dispatchT2(r)
		r.add("pj", "T-2", StepPrepare, KindResult, prepareOut{Base: "b"})
		r.add("pj", "T-2", StepStartWork, KindResult, startOut{Pane: "w1:p2"})
	}},
	{"fix", "pq", "T-7", "prompt", func(r *concRig) {
		r.add("pq", "T-7", StepSubmitted, KindFact, submitData{Lane: "O-07", Agent: "pq-o-07"})
		r.add("pq", "T-7", StepSendback, KindFact, noteData{Note: "fix it"})
		r.worker("pq", "w2:p7", "pq-o-07") // for its fix job's wake (an agent no pane proves gets none)
	}},
}

func acceptT4(r *concRig) {
	if _, err := r.app("pr").verdict(context.Background(), MethodAccept, Call{Root: r.roots["pr"], Task: "T-4", Role: "integrator",
		Agent: "pr-int", CallID: "accept", Job: "T-4/1/review", Patch: "p"}); err != nil {
		r.t.Fatal(err)
	}
}

// Drop first (the reviewer's TestRV42DropSelectedScratchStep, every kind): a step selected before a successful drop
// does not start once DROPPED is returned: no effect, the task stays dropped. Control: the same step, not dropped,
// runs its effect.
func TestDropSelectedStep(t *testing.T) {
	for _, c := range dropStartCases {
		t.Run(c.name, func(t *testing.T) {
			for _, dropped := range []bool{false, true} {
				r := newConcRig(t)
				r.fakes["pj"].hold = "" // a gate that runs returns at once
				c.setup(r)
				p := r.proj(c.proj)
				ap := r.app(c.proj)
				fx := &dropFx{concScratch: r.fakes[c.proj]}
				ap.mu.Lock()
				selected := ap.next(p, p.folded.loop[c.task])
				ap.mu.Unlock()
				if selected == nil {
					t.Fatalf("precondition: %s has no step selected", c.task)
				}
				if dropped {
					if reply, err := r.dropCall(c.proj, c.task, "drop"); err != nil || reply != dropReply(c.task) {
						t.Fatalf("drop: %q, %v", reply, err)
					}
				}
				selected(context.Background(), fx.loop(r.kit))
				ap.effects.Wait() // a gate's or publish's goroutine, with its records
				ran := strings.Join(fx.ran(), ",")
				switch {
				case !dropped && !strings.HasPrefix(ran, c.effect):
					t.Fatalf("control: the selected step ran %q; want %s first", ran, c.effect)
				case dropped && ran != "":
					t.Errorf("after DROPPED the selected step ran %q", ran)
				case dropped && r.state(c.proj, c.task) != stDropped:
					t.Errorf("after DROPPED the task is %s", r.state(c.proj, c.task))
				}
			}
		})
	}
}

// Step first, a scratch step: a gate or publish started (it holds the scratch) refuses the drop; its result folds
// and the task goes on. Not dropped: no dropped fact.
func TestDropAfterScratchStepStarted(t *testing.T) {
	for _, c := range dropStartCases[:2] {
		t.Run(c.name, func(t *testing.T) {
			r := newConcRig(t)
			c.setup(r)
			f := r.fakes[c.proj]
			f.hold, f.holdPub = "T-1", "T-4"
			p := r.proj(c.proj)
			ap := r.app(c.proj)
			ap.mu.Lock()
			selected := ap.next(p, p.folded.loop[c.task])
			ap.mu.Unlock()
			fx := &dropFx{concScratch: f}
			selected(context.Background(), fx.loop(r.kit))
			<-f.entered // the effect runs
			if _, err := r.dropCall(c.proj, c.task, "drop"); err == nil ||
				!strings.Contains(err.Error(), c.task+" has "+c.effect+" pending") {
				t.Errorf("drop while the %s runs: %v", c.effect, err)
			}
			close(f.release)
			ap.effects.Wait()
			if r.count(c.proj, c.task, KindResult, c.effect) != 1 {
				t.Errorf("the %s's result did not fold", c.effect)
			}
			if r.count(c.proj, c.task, KindFact, StepDropped) != 0 || r.state(c.proj, c.task) == stDropped {
				t.Error("a refused drop dropped the task")
			}
		})
	}
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

// Step first (T-395's table): a drop while a selected step holds the task (gate, publish, start, a prompt) is
// refused at once, naming the step, and changes nothing; once the step has returned, a drop goes through.
func TestDropRefusedWhileStepHeld(t *testing.T) {
	for _, c := range []struct {
		name string
		i    int
	}{{"gate", 0}, {"publish", 1}, {"start", 9}, {"start (review)", 6}} {
		c := dropStartCases[c.i]
		t.Run(c.name, func(t *testing.T) {
			r := newConcRig(t)
			r.fakes["pj"].hold = ""
			c.setup(r)
			p := r.proj(c.proj)
			ap := r.app(c.proj)
			fx := &dropFx{concScratch: r.fakes[c.proj], block: c.effect, blocked: make(chan struct{}),
				unblock: make(chan struct{})}
			ap.mu.Lock()
			selected := ap.next(p, p.folded.loop[c.task])
			ap.mu.Unlock()
			if selected == nil {
				t.Fatalf("precondition: %s has no step selected", c.task)
			}
			stepped := make(chan struct{})
			go func() { selected(context.Background(), fx.loop(r.kit)); close(stepped) }()
			<-fx.blocked // the step's effect runs
			a := r.dropWithin(c.proj, c.task, "d1")
			if a.err == nil || !strings.Contains(a.err.Error(), c.task) ||
				!strings.Contains(a.err.Error(), "is running; retry when it settles") &&
					!strings.Contains(a.err.Error(), "pending: retry once it settles") {
				t.Errorf("drop while the %s runs: %q, %v; want refused, naming the step", c.effect, a.reply, a.err)
			}
			if r.count(c.proj, c.task, KindFact, StepDropped) != 0 || r.state(c.proj, c.task) == stDropped ||
				r.ended(c.proj, c.task) {
				t.Errorf("a refused drop changed the task: state %s, ended mark %v", r.state(c.proj, c.task),
					r.ended(c.proj, c.task))
			}
			close(fx.unblock)
			<-stepped
			ap.effects.Wait()
			if a := r.dropWithin(c.proj, c.task, "d2"); a.err != nil || a.reply != dropReply(c.task) {
				t.Errorf("drop once the step returned: %q, %v", a.reply, a.err)
			}
		})
	}
}

// The reviewer's probe (RV-42-verify #1), rewritten to its outcome: a drop during a held integrator start whose agent
// fetches its job early is refused at once, and the fetch answers: no lock cycle.
func TestDropDuringEarlyFetch(t *testing.T) {
	r := newConcRig(t)
	p := r.proj("pj")
	r.add("pj", "T-1", StepGate, KindIntent, gateIn{Kind: InputLane, Lane: "O-01", Input: "s1"})
	r.add("pj", "T-1", StepGate, KindResult, gateOut{Outcome: outGreen, Candidate: Candidate{Task: "T-1", SHA: "c",
		Target: "b", Patch: "p"}})
	delete(r.a.obs, "w1:p9") // no integrator runs: the review job's step starts it
	entered, fetch := make(chan struct{}), make(chan struct{})
	fetched, stepped := make(chan callAnswer, 1), make(chan struct{})
	f := &startEffects{concScratch: r.fakes["pj"]}
	f.start = func(Launch) {
		close(entered)
		<-fetch
		reply, err := r.a.jobNext(Call{Root: p.root, Agent: p.integrator, Role: "integrator", CallID: "early"})
		fetched <- callAnswer{reply, err}
	}
	r.a.mu.Lock()
	selected := r.a.next(p, p.folded.loop["T-1"])
	r.a.mu.Unlock()
	if selected == nil {
		t.Fatal("precondition: T-1 has no step selected")
	}
	go func() { selected(context.Background(), &Loop{Agents: f, Typer: f}); close(stepped) }()
	<-entered // the start holds the task
	a := r.dropWithin("pj", "T-1", "drop")
	if a.err == nil || !strings.Contains(a.err.Error(), "is running; retry when it settles") {
		t.Errorf("drop during the start: %q, %v; want refused at once", a.reply, a.err)
	}
	close(fetch)
	select {
	case f := <-fetched:
		if f.err != nil || !strings.Contains(f.reply, "T-1") {
			t.Errorf("early job_next: %q, %v; want T-1's job", f.reply, f.err)
		}
	case <-time.After(time.Second):
		t.Fatal("job_next did not answer during the start: a lock cycle")
	}
	select {
	case <-stepped:
	case <-time.After(time.Second):
		t.Fatal("the start did not return")
	}
	if r.count("pj", "T-1", KindFact, StepDropped) != 0 || r.state("pj", "T-1") == stDropped {
		t.Error("a refused drop dropped the task")
	}
}

// pauseDrop makes project name's log hold the dropped fact's write until the returned release is called; writing is
// closed when it starts.
func (r *concRig) pauseDrop(name string) (writing chan struct{}, release func()) {
	p := r.proj(name)
	writing, gate := make(chan struct{}), make(chan struct{})
	p.log.refuse = func(rec Record) error {
		if rec.Step == StepDropped {
			close(writing)
			<-gate
		}
		return nil
	}
	return writing, func() { close(gate) }
}

// submitT7 is pq-o-07's task_done for T-7 (lane O-07 holds it in the lane table).
func submitT7(r *concRig) (string, error) {
	r.app("pq").lanes = &fakeLanes{lanes: []Lane{{Name: "O-07", Task: "T-7", Agent: "pq-o-07"}}}
	return r.app("pq").taskDone(Call{Root: r.proj("pq").root, Task: "T-7", Agent: "pq-o-07", Role: "worker", Lane: "O-07",
		CallID: "done", Report: "Summary: x\nSelf-check: x\nSkipped: x\nOpen questions: x"})
}

// Submission vs drop (both orders, no shared lock): a submission decided while a drop is being recorded (the task
// marked ended) records nothing; one recorded before the drop leaves the drop to go through. A submitted fact after
// dropped, at the same or the next attempt, leaves the task dropped (live and on replay).
func TestDropAndSubmission(t *testing.T) {
	setup := func(t *testing.T) *concRig {
		r := newConcRig(t)
		r.add("pq", "T-7", StepSubmitted, KindFact, submitData{Lane: "O-07", Agent: "pq-o-07"})
		r.add("pq", "T-7", StepSendback, KindFact, noteData{Note: "fix it"})
		return r
	}
	t.Run("control: task_done submits", func(t *testing.T) {
		r := setup(t)
		if reply, err := submitT7(r); err != nil || reply != taskDoneReply {
			t.Fatalf("task_done: %q, %v", reply, err)
		}
		if r.count("pq", "T-7", KindFact, StepSubmitted) != 2 {
			t.Fatalf("task_done did not submit T-7: %d submitted facts", r.count("pq", "T-7", KindFact, StepSubmitted))
		}
	})
	t.Run("drop first", func(t *testing.T) {
		r := setup(t)
		writing, release := r.pauseDrop("pq")
		dropped := make(chan callAnswer, 1)
		go func() {
			reply, err := r.dropCall("pq", "T-7", "drop")
			dropped <- callAnswer{reply, err}
		}()
		<-writing
		if !r.ended("pq", "T-7") {
			t.Error("the ended mark is not set while dropped is written")
		}
		done := make(chan error, 1)
		go func() { _, err := submitT7(r); done <- err }()
		var subErr error
		select {
		case subErr = <-done:
		case <-time.After(time.Second):
			t.Error("task_done waited on a task whose drop is being recorded")
		}
		release()
		if subErr == nil {
			subErr = <-done
		}
		if subErr == nil || !strings.Contains(subErr.Error(), "dropped, not open for a submission") {
			t.Errorf("task_done during the drop: %v; want refused", subErr)
		}
		if a := <-dropped; a.err != nil || a.reply != "DROPPED T-7" {
			t.Fatalf("drop: %q, %v", a.reply, a.err)
		}
		if n := r.count("pq", "T-7", KindFact, StepSubmitted); n != 1 {
			t.Errorf("submitted facts %d; want only the first", n)
		}
		if r.state("pq", "T-7") != stDropped || r.ended("pq", "T-7") {
			t.Errorf("after the drop: %s, ended mark %v", r.state("pq", "T-7"), r.ended("pq", "T-7"))
		}
	})
	t.Run("submission first", func(t *testing.T) {
		r := setup(t)
		if _, err := submitT7(r); err != nil {
			t.Fatal(err)
		}
		if r.state("pq", "T-7") != stSubmitted {
			t.Fatalf("precondition: T-7 is %s, want submitted", r.state("pq", "T-7"))
		}
		if a := r.dropWithin("pq", "T-7", "drop"); a.err != nil || a.reply != "DROPPED T-7" {
			t.Fatalf("drop after the submission: %q, %v", a.reply, a.err)
		}
		p := r.proj("pq")
		r.app("pq").mu.Lock()
		attempt := p.folded.loop["T-7"].attempt
		r.app("pq").mu.Unlock()
		for _, n := range []int{attempt, attempt + 1} { // decided before the drop, recorded after
			rec, err := p.log.Append(Record{Task: "T-7", Attempt: n, Step: StepSubmitted, Kind: KindFact,
				Data: []byte(`{"lane":"O-07","agent":"pq-o-07"}`)}, r.app("pq").opt.Now())
			if err != nil {
				t.Fatal(err)
			}
			r.app("pq").mu.Lock()
			p.foldRecord(rec)
			r.app("pq").mu.Unlock()
			if s := r.state("pq", "T-7"); s != stDropped {
				t.Errorf("a submitted fact (attempt %d) after dropped: %s", n, s)
			}
		}
		r.app("pq").closeLog() // the first serve ends: the replay is the next one
		b := New(Options{Root: r.roots["pq"], Now: r.app("pq").opt.Now}, &fakePanes{}, &fakeLanes{})
		b.loadProject()
		if mt := b.p.folded.loop["T-7"]; mt == nil || mt.state != stDropped {
			t.Errorf("replay: %+v; want T-7 dropped", mt)
		}
	})
}

// Mark, then record: while dropped is written the task is marked ended, and a selected step does not start; a write
// that fails clears the mark (the step starts again) and the drop fails; a later drop goes through.
func TestDropWriteFailure(t *testing.T) {
	r := newConcRig(t)
	p := r.proj("pq")
	var during struct{ ended, began bool }
	p.log.refuse = func(rec Record) error {
		if rec.Step != StepDropped {
			return nil
		}
		r.app("pq").mu.Lock()
		during.ended = p.ended["T-3"]
		mt := *p.folded.loop["T-3"]
		r.app("pq").mu.Unlock()
		if during.began = r.app("pq").begin(p, mt); during.began {
			r.app("pq").release(p, "T-3")
		}
		return errors.New("disk full")
	}
	a := r.dropWithin("pq", "T-3", "d1")
	if a.err == nil || !strings.Contains(a.err.Error(), "the event log of pq refused it") {
		t.Errorf("drop with a failing write: %q, %v", a.reply, a.err)
	}
	if !during.ended || during.began {
		t.Errorf("while dropped was written: ended mark %v, a step began %v; want marked, none", during.ended,
			during.began)
	}
	r.app("pq").mu.Lock()
	mt := *p.folded.loop["T-3"]
	r.app("pq").mu.Unlock()
	if r.ended("pq", "T-3") || mt.state == stDropped {
		t.Fatalf("after the failed write: ended mark %v, state %s", r.ended("pq", "T-3"), mt.state)
	}
	if !r.app("pq").begin(p, mt) {
		t.Error("a step does not start after the failed drop")
	}
	r.app("pq").release(p, "T-3")
	p.log.refuse = nil
	if a := r.dropWithin("pq", "T-3", "d2"); a.err != nil || a.reply != "DROPPED T-3" {
		t.Errorf("a later drop: %q, %v", a.reply, a.err)
	}
}
