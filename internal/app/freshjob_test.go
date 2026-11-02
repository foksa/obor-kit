package app

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"
	"testing"
	"time"
)

// T-602: integrator jobs start a fresh session each (Launch with Fresh, the job's wake its first prompt), not a wake
// typed into the session that runs.

// greenTask gates task green in r's project pj with candidate c.
func greenTask(r *concRig, task string, c Candidate) {
	r.add("pj", task, StepGate, KindIntent, gateIn{Kind: InputLane, Lane: "O-01", Input: "s1"})
	r.add("pj", task, StepGate, KindResult, gateOut{Outcome: outGreen, Candidate: c})
}

// runNext runs task's next step in p with f as its agents and typer: whether it ran and returned true.
func runNext(r *concRig, p *proj, task string, f *startJobFakes) bool {
	r.a.mu.Lock()
	step := r.a.next(p, p.folded.loop[task])
	r.a.mu.Unlock()
	if step == nil {
		r.t.Fatalf("%s: no step", task)
	}
	return step(context.Background(), &Loop{Agents: f, Typer: f})
}

// setStatus puts the integrator's pane w1:p9 at status.
func setStatus(r *concRig, status string) {
	r.a.mu.Lock()
	r.a.obs["w1:p9"].pane.Status = status
	r.a.mu.Unlock()
}

func TestFreshIntegratorPerJob(t *testing.T) {
	t.Parallel()
	r := newConcRig(t)
	p := r.proj("pj")
	ctx := context.Background()
	if w := r.a.whereIs(p, p.integrator); w.State != Identified {
		t.Fatalf("precondition: the integrator is not identified: %+v", w)
	}
	f := &startJobFakes{concScratch: r.fakes["pj"]}
	greenTask(r, "T-1", Candidate{Task: "T-1", SHA: "c1", Target: "b", Patch: "p1"})
	if !runNext(r, p, "T-1", f) {
		t.Fatal("T-1's review job not started")
	}
	greenTask(r, "T-5", Candidate{Task: "T-5", SHA: "c5", Target: "b", Patch: "p5"})

	// T-1's job is out: T-5's job waits, nothing started, typed or recorded for it.
	if runNext(r, p, "T-5", f) {
		t.Error("T-5's job went on while T-1's job is out")
	}
	same(t, len(f.launches), 1, "starts while T-1's job is out")
	same(t, p.log.Open(Key{"T-5", 1, StepReview}) || done(p, Key{"T-5", 1, StepReview}), false, "T-5's review recorded")

	if _, err := r.a.verdict(ctx, MethodAccept, Call{Root: p.root, Task: "T-1", Agent: "pj-int", Role: "integrator",
		CallID: "a1", Job: "T-1/1/review", Patch: "p1"}); err != nil {
		t.Fatal(err)
	}
	// Answered, but the integrator still ends its turn: not cut off.
	setStatus(r, "working")
	if runNext(r, p, "T-5", f) {
		t.Error("T-5's job went on while the integrator works")
	}
	same(t, len(f.launches), 1, "starts while the integrator works")

	setStatus(r, "idle")
	if !runNext(r, p, "T-5", f) {
		t.Fatal("T-5's review job not started after T-1's verdict")
	}
	for i, l := range f.launches {
		job := []string{"T-1/1/review", "T-5/1/review"}[i]
		if !l.Fresh || l.Force || l.Prompt != wakeLine(job) || l.Agent != "pj-int" {
			t.Errorf("launch %d: %+v, want a fresh pj-int (no force) with the first prompt %q", i, l, wakeLine(job))
		}
	}
	same(t, len(f.launches), 2, "starts (one per job)")
	same(t, len(f.typed), 0, "typed wakes")
	same(t, resultOf(p, Key{"T-5", 1, StepReview}), `"sent (start prompt)"`, "T-5's review result")
}

// The wait for an integrator that does not go idle after the last verdict is bounded (T-602): for Gone from the first
// look the next job waits; past it the fresh start forces (stops the turn), with one log line.
func TestFreshIntegratorIdleBound(t *testing.T) {
	t.Parallel()
	r := newConcRig(t)
	p := r.proj("pj")
	now := r.a.opt.Now()
	r.a.opt.Now = func() time.Time { return now }
	var out strings.Builder
	r.a.opt.Out = &out
	f := &startJobFakes{concScratch: r.fakes["pj"]}
	greenTask(r, "T-1", Candidate{Task: "T-1", SHA: "c1", Target: "b", Patch: "p1"})
	for _, status := range []string{"working", "blocked"} {
		t.Run(status, func(t *testing.T) {
			f.launches = nil
			setStatus(r, status)
			r.a.mu.Lock()
			p.idleWait = idleWait{}
			r.a.mu.Unlock()
			if runNext(r, p, "T-1", f) {
				t.Fatalf("the job went on at once while the integrator is %s", status)
			}
			now = now.Add(r.a.opt.Gone - time.Second)
			if runNext(r, p, "T-1", f) || len(f.launches) != 0 {
				t.Fatalf("the job went on before Gone (%s): launches %+v", r.a.opt.Gone, f.launches)
			}
			if status == "working" {
				return // the blocked row starts it
			}
			now = now.Add(2 * time.Second)
			if !runNext(r, p, "T-1", f) {
				t.Fatal("the job not started past Gone")
			}
			if len(f.launches) != 1 || !f.launches[0].Fresh || !f.launches[0].Force ||
				f.launches[0].Prompt != wakeLine("T-1/1/review") {
				t.Errorf("launches %+v, want one fresh start with force and the job's wake", f.launches)
			}
			if n := strings.Count(out.String(), "started fresh with force"); n != 1 {
				t.Errorf("force log lines %d, want 1:\n%s", n, out.String())
			}
		})
	}
}

// T-606 (RV-65 #1): the idle wait is the integrator's pane's, not a job's. Two queued jobs alternating across Gone
// share one busy interval, so one of them forces a fresh start after one Gone; the other waits for its verdict.
func TestFreshIntegratorIdleBoundTwoJobs(t *testing.T) {
	t.Parallel()
	r := newConcRig(t)
	p := r.proj("pj")
	now := r.a.opt.Now()
	r.a.opt.Now = func() time.Time { return now }
	var out strings.Builder
	r.a.opt.Out = &out
	f := &startJobFakes{concScratch: r.fakes["pj"]}
	setStatus(r, "working")
	greenTask(r, "T-1", Candidate{Task: "T-1", SHA: "c1", Target: "b", Patch: "p1"})
	greenTask(r, "T-5", Candidate{Task: "T-5", SHA: "c5", Target: "b", Patch: "p5"})

	// Alternate both jobs while the clock is under Gone: neither starts, whichever job looked last.
	step := r.a.opt.Gone / 5
	for i := 0; i < 4; i++ {
		for _, task := range []string{"T-1", "T-5"} {
			if runNext(r, p, task, f) {
				t.Fatalf("%s's job went on in pass %d, under Gone", task, i)
			}
		}
		now = now.Add(step)
	}
	same(t, len(f.launches), 0, "starts before Gone")

	// Past Gone from that first look, the first job in advance order forces; the other waits for its verdict.
	now = now.Add(r.a.opt.Gone)
	if !runNext(r, p, "T-1", f) {
		t.Fatal("T-1's job did not start after Gone")
	}
	same(t, len(f.launches), 1, "starts after Gone")
	if l := f.launches[0]; !l.Fresh || !l.Force || l.Prompt != wakeLine("T-1/1/review") || l.Agent != "pj-int" {
		t.Errorf("launch %+v, want a fresh pj-int with force and T-1's wake", l)
	}
	same(t, len(f.typed), 0, "typed wakes")
	if n := strings.Count(out.String(), "started fresh with force"); n != 1 {
		t.Errorf("force log lines %d, want 1:\n%s", n, out.String())
	}
	if runNext(r, p, "T-5", f) {
		t.Error("T-5's job went on while T-1's job is out")
	}
	same(t, len(f.launches), 1, "starts while T-1's job is out")
	same(t, p.log.Open(Key{"T-5", 1, StepReview}) || done(p, Key{"T-5", 1, StepReview}), false,
		"T-5's review recorded")

	// T-1's verdict, then its turn's end: T-5 starts fresh, not forced.
	if _, err := r.a.verdict(context.Background(), MethodAccept, Call{Root: p.root, Task: "T-1", Agent: "pj-int",
		Role: "integrator", CallID: "a1", Job: "T-1/1/review", Patch: "p1"}); err != nil {
		t.Fatal(err)
	}
	if runNext(r, p, "T-5", f) {
		t.Error("T-5's job went on while the integrator works")
	}
	setStatus(r, "idle")
	if !runNext(r, p, "T-5", f) {
		t.Fatal("T-5's review job not started after T-1's verdict")
	}
	if len(f.launches) != 2 {
		t.Fatalf("launches %+v, want two", f.launches)
	}
	if l := f.launches[1]; !l.Fresh || l.Force || l.Prompt != wakeLine("T-5/1/review") {
		t.Errorf("launch %+v, want a fresh start (no force) with T-5's wake", l)
	}
}

// job_next hands out the job the call names when it is open, else the oldest open one, never a held one (T-602): a
// fresh session started for job B fetches B, also while an older job A is held (the T-598 resolve.2 shape).
func TestJobNextNamedNotHeld(t *testing.T) {
	t.Parallel()
	r := newConcRig(t)
	p := r.proj("pj")
	greenTask(r, "T-1", Candidate{Task: "T-1", SHA: "c1", Target: "b", Patch: "p1"})
	r.add("pj", "T-1", StepReview, KindIntent, promptData{Agent: "pj-int", Job: "T-1/1/review", Patch: "p1"})
	r.add("pj", "T-1", StepReview, KindResult, "sent")
	r.add("pj", "T-5", StepGate, KindIntent, gateIn{Kind: InputLane, Lane: "O-05", Input: "s5"})
	r.add("pj", "T-5", StepGate, KindResult, gateOut{Outcome: outConflict, Candidate: Candidate{Task: "T-5", SHA: "c5",
		Target: "b", Patch: "p5"}})
	r.add("pj", "T-5", nth(StepResolve, 2), KindIntent, promptData{Agent: "pj-int", Job: "T-5/1/resolve.2", Patch: "p5"})
	r.add("pj", "T-5", nth(StepResolve, 2), KindResult, "sent")
	fetch := func(id, job string) JobReply {
		t.Helper()
		raw, err := r.a.jobNext(Call{Root: p.root, Agent: p.integrator, Role: "integrator", CallID: id, Job: job})
		if err != nil {
			t.Fatal(err)
		}
		var jr JobReply
		_ = json.Unmarshal([]byte(raw), &jr)
		return jr
	}
	same(t, fetch("n1", "").Job, "T-1/1/review", "unnamed, nothing held: the oldest open job")
	same(t, fetch("n2", "T-5/1/resolve.2").Job, "T-5/1/resolve.2", "named: that job, though an older one is open")

	r.add("pj", "T-1", StepNeedsYou, KindFact, FactData{Merge: true, Cause: causeHeld, Reason: "held: why"})
	same(t, fetch("n3", "").Job, "T-5/1/resolve.2", "unnamed, the older job held: the open one not held")
	same(t, fetch("n4", "T-5/1/resolve.2").Job, "T-5/1/resolve.2", "named, an older job held")
	jr := fetch("n5", "T-1/1/review")
	same(t, jr.State+" "+jr.Current, "superseded T-5/1/resolve.2", "the held job named: not handed out")
}

// T-606: the unnamed job_next hands out the oldest open job on every call. Two sent jobs have no pending intent, so
// their order must be the log's, not the map's: each call ranges the loop afresh and must still name the first.
func TestJobNextOldestNotTheMapsFirst(t *testing.T) {
	t.Parallel()
	r := newConcRig(t)
	p := r.proj("pj")
	for _, task := range []string{"T-1", "T-5"} {
		greenTask(r, task, Candidate{Task: task, SHA: "c-" + task, Target: "b", Patch: "p-" + task})
		r.add("pj", task, StepReview, KindIntent, promptData{Agent: "pj-int", Job: task + "/1/review",
			Patch: "p-" + task})
		r.add("pj", task, StepReview, KindResult, "sent")
	}
	for _, task := range []string{"T-1", "T-5"} {
		mt := p.folded.loop[task]
		if k, ok := mt.integratorJob(); !ok || JobID(k) != task+"/1/review" {
			t.Fatalf("precondition: %s's review job is not open (%s, %v)", task, JobID(k), ok)
		}
	}
	for i := 0; i < 20; i++ {
		raw, err := r.a.jobNext(Call{Root: p.root, Agent: p.integrator, Role: "integrator",
			CallID: "old" + strconv.Itoa(i)})
		if err != nil {
			t.Fatal(err)
		}
		var jr JobReply
		_ = json.Unmarshal([]byte(raw), &jr)
		if jr.Job != "T-1/1/review" {
			t.Fatalf("call %d: the unnamed fetch got %q, want the oldest open job T-1/1/review", i, jr.Job)
		}
	}
}

// A crash after the job's intent and its start's intent, before their results: the replay starts nothing again (the
// integrator runs: its wake typed), and a needs-you that left another task's job does not hold the next job.
func TestFreshIntegratorReplay(t *testing.T) {
	t.Parallel()
	r := newConcRig(t)
	p := r.proj("pj")
	greenTask(r, "T-1", Candidate{Task: "T-1", SHA: "c1", Target: "b", Patch: "p1"})
	r.add("pj", "T-1", StepReview, KindIntent, promptData{Agent: "pj-int", Job: "T-1/1/review", Patch: "p1"})
	r.add("pj", "T-1", "start.review", KindIntent, startData{Agent: "pj-int"})
	f := &startJobFakes{concScratch: r.fakes["pj"]}
	if !runNext(r, p, "T-1", f) {
		t.Fatal("the replayed job step failed")
	}
	same(t, len(f.launches), 0, "starts on the replay")
	same(t, done(p, Key{"T-1", 1, "start.review"}), true, "start.review done")
	same(t, len(f.typed), 1, "typed wakes")
	same(t, resultOf(p, Key{"T-1", 1, StepReview}), `"sent"`, "T-1's review result")

	r.add("pj", "T-1", StepNeedsYou, KindFact, FactData{Merge: true, Cause: causeHeld, Reason: "held: why"})
	greenTask(r, "T-5", Candidate{Task: "T-5", SHA: "c5", Target: "b", Patch: "p5"})
	if !runNext(r, p, "T-5", f) {
		t.Fatal("T-5's job held by T-1's needs-you")
	}
	if len(f.launches) != 1 || !f.launches[0].Fresh {
		t.Errorf("launches %+v, want one fresh start for T-5", f.launches)
	}
}

// The review job carries a candidate's diff the gate kept, said to be the whole diff; without one, the git diff line.
func TestReviewTextDiff(t *testing.T) {
	t.Parallel()
	r := newConcRig(t)
	p := r.proj("pj")
	diff := "diff --git a/f b/f\n+the change\n"
	for _, c := range []struct {
		name string
		diff string
		want []string
		not  []string
	}{
		{"inline", diff, []string{"stat-line", "its whole diff", "+the change", "no need to run it"},
			[]string{"read it with git diff"}},
		{"too big", "", []string{"stat-line", "read it with git diff b c1."}, []string{"whole diff", "+the change"}},
	} {
		mt := *p.folded.loop["T-1"]
		mt.cand = Candidate{Task: "T-1", SHA: "c1", Target: "b", Patch: "p1", Stat: "stat-line", Diff: c.diff}
		text, err := r.a.reviewText(p, mt)
		if err != nil {
			t.Fatal(err)
		}
		for _, w := range c.want {
			if !strings.Contains(text, w) {
				t.Errorf("%s: the review job has no %q:\n%s", c.name, w, text)
			}
		}
		for _, w := range c.not {
			if strings.Contains(text, w) {
				t.Errorf("%s: the review job has %q:\n%s", c.name, w, text)
			}
		}
	}
}

// A pending first start of an integrator job (a crash, or a failed start) starts fresh again only when the snapshot
// shows the session its intent saw, or none: a new session there means the start ran (no second start: the wake
// typed); an unknown session cannot tell (the same).
func TestFreshIntegratorPendingStart(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		name          string
		at            startData // the start's intent
		now           string    // the integrator pane's session now ("gone": no integrator)
		start, forced bool
	}{
		{"the same session: start again", startData{Agent: "pj-int", Pane: "w1:p9", Ran: true, Session: "old"}, "old",
			true, false},
		{"none runs: start again", startData{Agent: "pj-int", Pane: "w1:p9", Ran: true, Session: "old"}, "gone",
			true, false},
		{"a new session: started", startData{Agent: "pj-int", Pane: "w1:p9", Ran: true, Session: "old"}, "new",
			false, false},
		{"none at the intent, one now: started", startData{Agent: "pj-int"}, "new", false, false},
		{"unknown at the intent: cannot tell", startData{Agent: "pj-int", Pane: "w1:p9", Ran: true}, "old", false, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			r := newConcRig(t)
			p := r.proj("pj")
			greenTask(r, "T-1", Candidate{Task: "T-1", SHA: "c1", Target: "b", Patch: "p1"})
			r.add("pj", "T-1", StepReview, KindIntent, promptData{Agent: "pj-int", Job: "T-1/1/review", Patch: "p1"})
			r.add("pj", "T-1", "start.review", KindIntent, c.at)
			r.a.mu.Lock()
			if c.now == "gone" {
				delete(r.a.obs, "w1:p9")
			} else {
				r.a.obs["w1:p9"].pane.Session = c.now
			}
			r.a.mu.Unlock()
			f := &startJobFakes{concScratch: r.fakes["pj"]}
			if !runNext(r, p, "T-1", f) {
				t.Fatal("the pending job step failed")
			}
			if c.start {
				if len(f.launches) != 1 || !f.launches[0].Fresh || f.launches[0].Prompt != wakeLine("T-1/1/review") {
					t.Errorf("launches %+v, want one fresh start with the job's wake", f.launches)
				}
				same(t, len(f.typed), 0, "typed wakes")
				return
			}
			same(t, len(f.launches), 0, "starts")
			same(t, len(f.typed), 1, "typed wakes")
		})
	}
}
