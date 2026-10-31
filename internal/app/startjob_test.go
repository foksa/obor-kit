package app

import (
	"context"
	"encoding/json"
	"testing"
)

// startJobFakes records the launches and the typed prompts; start runs inside Restart, as an agent that obeys its
// CLI first prompt before the launch returns.
type startJobFakes struct {
	*concScratch
	start    func(Launch)
	launches []Launch
	typed    []string
}

func (f *startJobFakes) Launch(_ context.Context, l Launch) (Started, error) {
	f.launches = append(f.launches, l)
	if f.start != nil {
		f.start(l)
	}
	return Started{Line: "STARTED"}, nil
}

func (f *startJobFakes) Type(_ context.Context, _, text string, _ bool) (bool, error) {
	f.typed = append(f.typed, text)
	return true, nil
}

// T-393 (RV-42 #2), T-396: the one start sequence (startJob). An integrator that does not run is started with its
// job's wake as the first prompt, the job open from its own intent, written before the start: a fetch during the start gets the job and
// records delivered once, the job's step is sent (start prompt), nothing typed. The same for review and resolve, a
// numbered job after job.lost and the owner's reprompt; an integrator that runs (idle, no other job out) is started
// fresh as well (T-602).
func TestStartJobIntegrator(t *testing.T) {
	gate := func(outcome string) func(r *concRig) {
		return func(r *concRig) {
			r.add("pj", "T-1", StepGate, KindIntent, gateIn{Kind: InputLane, Lane: "O-01", Input: "s1"})
			r.add("pj", "T-1", StepGate, KindResult, gateOut{Outcome: outcome, Candidate: Candidate{Task: "T-1", SHA: "c",
				Target: "b", Patch: "p"}})
		}
	}
	sent := func(step string) func(r *concRig) {
		return func(r *concRig) {
			r.add("pj", "T-1", step, KindIntent, promptData{Agent: "pj-int", Job: "T-1/1/" + step, Patch: "p"})
			r.add("pj", "T-1", step, KindResult, "sent")
		}
	}
	lost := func(step string) func(r *concRig) {
		return func(r *concRig) {
			r.add("pj", "T-1", StepLost, KindFact, FactData{Merge: true, Job: "T-1/1/" + step})
		}
	}
	reprompt := func(r *concRig) {
		r.add("pj", "T-1", StepNeedsYou, KindFact, FactData{Merge: true, Cause: causeUndelivered,
			Reason: "job not delivered"})
		r.add("pj", "T-1", StepUnblocked, KindFact, FactData{Merge: true, Reason: ResumeReprompt})
	}
	for _, row := range []struct {
		name    string
		setup   []func(r *concRig)
		running bool   // the integrator's pane stays in the snapshot
		job     string // the job the step sends
		start   string // the start that runs ("": none)
		typed   int    // Typer calls
	}{
		{name: "review: fresh start", setup: []func(*concRig){gate(outGreen)}, job: "review", start: "start.review"},
		{name: "resolve: fresh start", setup: []func(*concRig){gate(outConflict)}, job: "resolve",
			start: "start.resolve"},
		{name: "review.2 after job.lost", setup: []func(*concRig){gate(outGreen), sent("review"), lost("review")},
			job: "review.2", start: "start.review.2"},
		{name: "resolve.2 after job.lost", setup: []func(*concRig){gate(outConflict), sent("resolve"), lost("resolve")},
			job: "resolve.2", start: "start.resolve.2"},
		{name: "owner reprompt starts an absent integrator",
			setup: []func(*concRig){gate(outGreen), sent("review"), reprompt}, job: "review.2", start: "start.review.2"},
		{name: "running integrator: a fresh start too (T-602)", setup: []func(*concRig){gate(outGreen)},
			running: true, job: "review", start: "start.review"},
	} {
		t.Run(row.name, func(t *testing.T) {
			r := newConcRig(t)
			p := r.proj("pj")
			for _, s := range row.setup {
				s(r)
			}
			if !row.running {
				delete(r.a.obs, "w1:p9")
			}
			job := "T-1/1/" + row.job
			f := &startJobFakes{concScratch: r.fakes["pj"]}
			var early JobReply
			f.start = func(Launch) {
				raw, err := r.a.jobNext(Call{Root: p.root, Agent: p.integrator, Role: "integrator", CallID: "early"})
				if err != nil {
					t.Fatal(err)
				}
				_ = json.Unmarshal([]byte(raw), &early)
			}
			r.a.mu.Lock()
			step := r.a.next(p, p.folded.loop["T-1"])
			r.a.mu.Unlock()
			if step == nil {
				t.Fatal("no step")
			}
			if !step(context.Background(), &Loop{Agents: f, Typer: f}) {
				t.Fatal("the job step failed")
			}

			if row.start == "" {
				same(t, len(f.launches), 0, "starts")
			} else {
				if len(f.launches) != 1 || f.launches[0].Prompt != wakeLine(job) || !f.launches[0].Fresh {
					t.Fatalf("launches %+v, want one fresh with the first prompt %q", f.launches, wakeLine(job))
				}
				same(t, done(p, Key{"T-1", 1, row.start}), true, row.start+" done")
				same(t, early.State+" "+early.Job, "job "+job, "job_next during the start")
			}
			same(t, len(f.typed), row.typed, "Typer calls")
			want := `"sent (start prompt)"`
			if row.typed > 0 {
				want = `"sent"`
			}
			same(t, resultOf(p, Key{"T-1", 1, row.job}), want, row.job+" result")
			if row.start != "" {
				same(t, deliveredOf(r, job), 1, "delivered facts of "+job)
				r.a.mu.Lock()
				mt := *p.folded.loop["T-1"]
				r.a.mu.Unlock()
				same(t, mt.job+" "+mt.state, row.job+" "+mt.jobState(true), "the job out")
				same(t, mt.delivered, true, "the job delivered (fetched during its start)")
				// a later fetch is a repeat: the same job, nothing recorded
				raw, err := r.a.jobNext(Call{Root: p.root, Agent: p.integrator, Role: "integrator", CallID: "later"})
				var again JobReply
				_ = json.Unmarshal([]byte(raw), &again)
				if err != nil || again.Job != job || deliveredOf(r, job) != 1 {
					t.Errorf("a repeat fetch: %s %v, delivered facts %d", raw, err, deliveredOf(r, job))
				}
			}
		})
	}
}

// T-393, T-396: a start alone hands out no job (no lookup beside the open job); the job's intent, which its step
// writes before the start, does, with the job's patch.
func TestStartJobOtherPatch(t *testing.T) {
	r := newConcRig(t)
	p := r.proj("pj")
	r.add("pj", "T-1", StepGate, KindIntent, gateIn{Kind: InputLane, Lane: "O-01", Input: "s1"})
	r.add("pj", "T-1", StepGate, KindResult, gateOut{Outcome: outGreen, Candidate: Candidate{Task: "T-1", SHA: "c",
		Target: "b", Patch: "p"}})
	r.add("pj", "T-1", "start.review", KindIntent, startData{Agent: "pj-int"})
	raw, err := r.a.jobNext(Call{Root: p.root, Agent: p.integrator, Role: "integrator", CallID: "n1"})
	if err != nil || raw != `{"state":"none"}` {
		t.Fatalf("job_next with a start and no job intent: %s %v, want none", raw, err)
	}
	r.add("pj", "T-1", StepReview, KindIntent, promptData{Agent: "pj-int", Job: "T-1/1/review", Patch: "p"})
	raw, err = r.a.jobNext(Call{Root: p.root, Agent: p.integrator, Role: "integrator", CallID: "n3"})
	var jr JobReply
	_ = json.Unmarshal([]byte(raw), &jr)
	if err != nil || jr.Job != "T-1/1/review" {
		t.Errorf("job_next with the job's intent: %s %v, want T-1/1/review", raw, err)
	}
	r.a.mu.Lock()
	patch := p.folded.loop["T-1"].patch
	r.a.mu.Unlock()
	same(t, patch, "p", "the open job's patch")
}

func resultOf(p *proj, k Key) string {
	p.log.mu.Lock()
	defer p.log.mu.Unlock()
	return string(p.log.results[k].Data)
}

func done(p *proj, k Key) bool { return p.log.Done(k) }

func deliveredOf(r *concRig, job string) int {
	recs, err := ReadLog(r.roots["pj"])
	if err != nil {
		r.t.Fatal(err)
	}
	n := 0
	for _, rec := range recs {
		if rec.Kind == KindFact && rec.Step == StepDelivered && rec.Fact().Job == job {
			n++
		}
	}
	return n
}

// same fails t when got is not want.
func same[T comparable](t *testing.T, got, want T, what string) {
	t.Helper()
	if got != want {
		t.Errorf("%s = %v, want %v", what, got, want)
	}
}

// RV-42's probe effects: start runs inside Restart (an agent obeying its CLI first prompt); typed are the Typer's
// texts.
type startEffects struct {
	*concScratch
	start func(Launch)
	typed []string
}

func (f *startEffects) Launch(_ context.Context, l Launch) (Started, error) {
	if f.start != nil {
		f.start(l)
	}
	return Started{Line: "STARTED"}, nil
}

func (f *startEffects) Type(_ context.Context, agent, text string, clear bool) (bool, error) {
	f.typed = append(f.typed, text)
	return true, nil
}

// RV-42 #2 (the reviewer's probe, fixed outcome, T-393): a fresh integrator's job is fetchable while the start that
// carries its wake runs, and no second wake is typed.
func TestIntegratorJobFetchableDuringStart(t *testing.T) {
	for _, kind := range []string{StepReview, StepResolve} {
		t.Run(kind, func(t *testing.T) {
			r := newConcRig(t)
			p := r.proj("pj")
			outcome := outGreen
			if kind == StepResolve {
				outcome = outConflict
			}
			r.add("pj", "T-1", StepGate, KindIntent, gateIn{Kind: InputLane, Lane: "O-01", Input: "s1"})
			r.add("pj", "T-1", StepGate, KindResult, gateOut{Outcome: outcome, Candidate: Candidate{Task: "T-1", SHA: "c",
				Target: "b", Patch: "p"}})
			delete(r.a.obs, "w1:p9")
			f := &startEffects{concScratch: r.fakes["pj"]}
			var early JobReply
			f.start = func(l Launch) {
				raw, err := r.a.jobNext(Call{Root: p.root, Agent: p.integrator, Role: "integrator", CallID: "early"})
				if err != nil {
					t.Fatal(err)
				}
				_ = json.Unmarshal([]byte(raw), &early)
				t.Logf("first prompt=%q; job_next during start=%s", l.Prompt, raw)
			}
			fx := &Loop{Agents: f, Typer: f}
			mt := *p.folded.loop["T-1"]
			if !r.a.job(context.Background(), p, fx, mt, kind) {
				t.Fatal("job failed")
			}
			t.Logf("Prompter calls after first prompt=%d", len(f.typed))
			if early.State != "job" {
				t.Errorf("job_next during first prompt=%s, want job", early.State)
			}
			if len(f.typed) != 0 {
				t.Errorf("typed a second wake %q", f.typed)
			}
		})
	}
}

// RV-42 #2's control: the worker's early fetch during its start, delivered, the work step sent with nothing typed.
func TestWorkerEarlyFetchDuringStart(t *testing.T) {
	r := newConcRig(t)
	p := r.proj("pj")
	r.add("pj", "T-2", StepDispatched, KindFact, dispatchData{Lane: "O-02", Dir: r.a.obs["w1:p2"].pane.Cwd, Agent: "pj-o-02"})
	r.add("pj", "T-2", StepPrepare, KindResult, prepareOut{Base: "b"})
	f := &startEffects{concScratch: r.fakes["pj"]}
	f.start = func(Launch) {
		raw, err := r.a.jobNext(Call{Root: p.root, Agent: "pj-o-02", Role: "worker", Lane: "O-02", CallID: "early"})
		if err != nil {
			t.Fatal(err)
		}
		var jr JobReply
		_ = json.Unmarshal([]byte(raw), &jr)
		if jr.State != "job" || jr.Job != "T-2/1/work" {
			t.Fatalf("early worker fetch=%s", raw)
		}
	}
	fx := &Loop{Agents: f, Typer: f, Kit: r.kit}
	mt := *p.folded.loop["T-2"]
	d := startData{Agent: mt.agent, Pane: "w1:p2"}
	if !r.a.startWorker(context.Background(), p, fx, mt, Key{mt.task, mt.attempt, StepStartWork}, d,
		Key{mt.task, mt.attempt, StepWork}) {
		t.Fatal("start failed")
	}
	if !r.a.sendWork(context.Background(), p, fx, *p.folded.loop[mt.task], StepWork) {
		t.Fatal("work failed")
	}
	if len(f.typed) != 0 || p.folded.loop[mt.task].state != stWorking {
		t.Fatalf("typed=%q state=%s", f.typed, p.folded.loop[mt.task].state)
	}
}
