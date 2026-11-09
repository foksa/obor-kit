package app

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"obor/internal/app/event"
	"obor/internal/app/port"
	"obor/internal/app/wire"
	"obor/internal/config"
	"obor/internal/store"
)

// T-396: the worker's job is open before its start: a fetch and a task_done during the start are taken, and the
// start's result after that keeps the submission (state submitted, the work step sent (start prompt), nothing typed).
func TestEarlyTaskDone(t *testing.T) {
	t.Parallel()
	r := newConcRig(t)
	p := r.proj("pj")
	p.dispatching = true
	lane := r.a.obs["w1:p2"].pane.Cwd
	r.add("pj", "T-2", event.StepDispatched, event.KindFact, dispatchData{Lane: "L-02", Dir: lane, Agent: "pj-l-02"})
	r.add("pj", "T-2", event.StepPrepare, event.KindResult, prepareOut{Base: "b"})
	r.a.lanes.(*fakeLanes).lanes = []port.Lane{{Name: "L-02", Dir: lane, Task: "T-2", Agent: "pj-l-02"}}
	f := &startJobFakes{concFakes: r.fakes["pj"]}
	var fetched wire.JobReply
	var doneErr error
	var during string
	f.start = func(port.Launch) {
		raw, err := r.a.jobNext(wire.JobNextReq{Envelope: wire.Envelope{Root: p.root, Agent: "pj-l-02", Role: "worker",
			CallID: "early"}, Lane: "L-02"})
		if err != nil {
			t.Fatal(err)
		}
		_ = json.Unmarshal([]byte(raw), &fetched)
		_, doneErr = r.a.taskDone(wire.TaskDoneReq{Envelope: wire.Envelope{Root: p.root, Agent: "pj-l-02",
			Role: "worker", CallID: "done"}, Lane: "L-02", Task: "T-2",
			Report: "Summary: x\nSelf-check: x\nSkipped: x\nOpen questions: x"})
		r.a.mu.Lock()
		during = p.folded.loop["T-2"].state
		r.a.mu.Unlock()
	}
	r.a.mu.Lock()
	step := r.a.next(p, p.folded.loop["T-2"])
	r.a.mu.Unlock()
	if step == nil {
		t.Fatal("no step")
	}
	if !step(context.Background(), &Loop{Agents: f, Typer: f, Kit: r.kit}) {
		t.Fatal("the start step failed")
	}
	if len(f.launches) != 1 || f.launches[0].Prompt != wakeLine("T-2/1/work") {
		t.Fatalf("launches %+v, want one with the work job's wake", f.launches)
	}
	same(t, fetched.State+" "+fetched.Job, "job T-2/1/work", "job_next during the start")
	if doneErr != nil {
		t.Fatalf("task_done during the start: %v", doneErr)
	}
	same(t, during, stSubmitted, "state after task_done")
	r.a.mu.Lock()
	after := p.folded.loop["T-2"].state
	r.a.mu.Unlock()
	same(t, after, stSubmitted, "state after the start")
	same(t, resultOf(p, event.Key{Task: "T-2", Attempt: 1, Step: event.StepWork}), `"sent (start prompt)"`, "work result")
	same(t, len(f.typed), 0, "Prompter calls")
}

// T-396: a work job open before a start that was refused stays the job: after obor resume the next start carries the
// same job's wake (no work.2), and its result sends it.
func TestStartAgainSameJob(t *testing.T) {
	t.Parallel()
	r := newConcRig(t)
	p := r.proj("pj")
	p.dispatching = true
	lane := r.a.obs["w1:p2"].pane.Cwd
	r.add("pj", "T-2", event.StepDispatched, event.KindFact, dispatchData{Lane: "L-02", Dir: lane, Agent: "pj-l-02"})
	r.add("pj", "T-2", event.StepPrepare, event.KindResult, prepareOut{Base: "b"})
	r.add("pj", "T-2", event.StepWork, event.KindIntent, promptData{Agent: "pj-l-02", Job: "T-2/1/work"})
	r.add("pj", "T-2", event.StepStartWork, event.KindIntent, startData{Lane: "L-02", Agent: "pj-l-02", Pane: "w1:p2"})
	r.add("pj", "T-2", event.StepStartWork, event.KindResult, startOut{Refused: "has background tasks"})
	r.add("pj", "T-2", event.StepNeedsYou, event.KindFact, event.FactData{Merge: true, Cause: causeStart, Reason: "start: refused"})
	r.add("pj", "T-2", event.StepUnblocked, event.KindFact, event.FactData{Merge: true, Reason: wire.ResumeRetry})
	f := &startJobFakes{concFakes: r.fakes["pj"]}
	r.a.mu.Lock()
	step := r.a.next(p, p.folded.loop["T-2"])
	r.a.mu.Unlock()
	if step == nil {
		t.Fatal("no step")
	}
	if !step(context.Background(), &Loop{Agents: f, Typer: f, Kit: r.kit}) {
		t.Fatal("the start step failed")
	}
	if len(f.launches) != 1 || f.launches[0].Prompt != wakeLine("T-2/1/work") {
		t.Fatalf("launches %+v, want one carrying T-2/1/work", f.launches)
	}
	same(t, resultOf(p, event.Key{Task: "T-2", Attempt: 1, Step: event.StepWork}), `"sent (start prompt)"`, "work result")
	same(t, done(p, event.Key{Task: "T-2", Attempt: 1, Step: nth(event.StepWork, 2)}), false, "work.2 done")
	same(t, len(f.typed), 0, "Prompter calls")
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
	f := &startJobFakes{concFakes: r.fakes["pj"]}
	greenTask(r, "T-1", port.Candidate{Task: "T-1", SHA: "c1", Target: "b", Patch: "p1"})
	if !runNext(r, p, "T-1", f) {
		t.Fatal("T-1's review job not started")
	}
	greenTask(r, "T-5", port.Candidate{Task: "T-5", SHA: "c5", Target: "b", Patch: "p5"})

	// T-1's job is out: T-5's job waits, nothing started, typed or recorded for it.
	if runNext(r, p, "T-5", f) {
		t.Error("T-5's job went on while T-1's job is out")
	}
	same(t, len(f.launches), 1, "starts while T-1's job is out")
	same(t, p.log.Open(event.Key{Task: "T-5", Attempt: 1, Step: event.StepReview}) || done(p, event.Key{Task: "T-5", Attempt: 1, Step: event.StepReview}), false, "T-5's review recorded")

	if _, err := r.a.verdict(ctx, wire.VerdictReq{Envelope: wire.Envelope{Root: p.root, Agent: "pj-int",
		Role: "integrator", CallID: "a1"}, Verb: wire.MethodAccept, Task: "T-1", Job: "T-1/1/review",
		Patch: "p1"}); err != nil {
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
	same(t, resultOf(p, event.Key{Task: "T-5", Attempt: 1, Step: event.StepReview}), `"sent (start prompt)"`, "T-5's review result")
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
	f := &startJobFakes{concFakes: r.fakes["pj"]}
	greenTask(r, "T-1", port.Candidate{Task: "T-1", SHA: "c1", Target: "b", Patch: "p1"})
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
	f := &startJobFakes{concFakes: r.fakes["pj"]}
	setStatus(r, "working")
	greenTask(r, "T-1", port.Candidate{Task: "T-1", SHA: "c1", Target: "b", Patch: "p1"})
	greenTask(r, "T-5", port.Candidate{Task: "T-5", SHA: "c5", Target: "b", Patch: "p5"})

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
	same(t, p.log.Open(event.Key{Task: "T-5", Attempt: 1, Step: event.StepReview}) || done(p, event.Key{Task: "T-5", Attempt: 1, Step: event.StepReview}), false,
		"T-5's review recorded")

	// T-1's verdict, then its turn's end: T-5 starts fresh, not forced.
	if _, err := r.a.verdict(context.Background(), wire.VerdictReq{Envelope: wire.Envelope{Root: p.root,
		Agent: "pj-int", Role: "integrator", CallID: "a1"}, Verb: wire.MethodAccept, Task: "T-1", Job: "T-1/1/review",
		Patch: "p1"}); err != nil {
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
	greenTask(r, "T-1", port.Candidate{Task: "T-1", SHA: "c1", Target: "b", Patch: "p1"})
	r.add("pj", "T-1", event.StepReview, event.KindIntent, promptData{Agent: "pj-int", Job: "T-1/1/review", Patch: "p1"})
	r.add("pj", "T-1", event.StepReview, event.KindResult, "sent")
	r.add("pj", "T-5", event.StepGate, event.KindIntent, gateIn{Kind: port.InputLane, Lane: "L-05", Input: "s5"})
	r.add("pj", "T-5", event.StepGate, event.KindResult, gateOut{Outcome: outConflict, Candidate: port.Candidate{Task: "T-5", SHA: "c5",
		Target: "b", Patch: "p5"}})
	r.add("pj", "T-5", nth(event.StepResolve, 2), event.KindIntent, promptData{Agent: "pj-int", Job: "T-5/1/resolve.2", Patch: "p5"})
	r.add("pj", "T-5", nth(event.StepResolve, 2), event.KindResult, "sent")
	fetch := func(id, job string) wire.JobReply {
		t.Helper()
		raw, err := r.a.jobNext(wire.JobNextReq{Envelope: wire.Envelope{Root: p.root, Agent: p.integrator,
			Role: "integrator", CallID: id}, Job: job})
		if err != nil {
			t.Fatal(err)
		}
		var jr wire.JobReply
		_ = json.Unmarshal([]byte(raw), &jr)
		return jr
	}
	same(t, fetch("n1", "").Job, "T-1/1/review", "unnamed, nothing held: the oldest open job")
	same(t, fetch("n2", "T-5/1/resolve.2").Job, "T-5/1/resolve.2", "named: that job, though an older one is open")

	r.add("pj", "T-1", event.StepNeedsYou, event.KindFact, event.FactData{Merge: true, Cause: event.CauseHeld, Reason: "held: why"})
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
		greenTask(r, task, port.Candidate{Task: task, SHA: "c-" + task, Target: "b", Patch: "p-" + task})
		r.add("pj", task, event.StepReview, event.KindIntent, promptData{Agent: "pj-int", Job: task + "/1/review",
			Patch: "p-" + task})
		r.add("pj", task, event.StepReview, event.KindResult, "sent")
	}
	for _, task := range []string{"T-1", "T-5"} {
		mt := p.folded.loop[task]
		if k, ok := mt.integratorJob(); !ok || JobID(k) != task+"/1/review" {
			t.Fatalf("precondition: %s's review job is not open (%s, %v)", task, JobID(k), ok)
		}
	}
	for i := 0; i < 20; i++ {
		raw, err := r.a.jobNext(wire.JobNextReq{Envelope: wire.Envelope{Root: p.root, Agent: p.integrator,
			Role: "integrator", CallID: "old" + strconv.Itoa(i)}})
		if err != nil {
			t.Fatal(err)
		}
		var jr wire.JobReply
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
	greenTask(r, "T-1", port.Candidate{Task: "T-1", SHA: "c1", Target: "b", Patch: "p1"})
	r.add("pj", "T-1", event.StepReview, event.KindIntent, promptData{Agent: "pj-int", Job: "T-1/1/review", Patch: "p1"})
	r.add("pj", "T-1", "start.review", event.KindIntent, startData{Agent: "pj-int"})
	f := &startJobFakes{concFakes: r.fakes["pj"]}
	if !runNext(r, p, "T-1", f) {
		t.Fatal("the replayed job step failed")
	}
	same(t, len(f.launches), 0, "starts on the replay")
	same(t, done(p, event.Key{Task: "T-1", Attempt: 1, Step: "start.review"}), true, "start.review done")
	same(t, len(f.typed), 1, "typed wakes")
	same(t, resultOf(p, event.Key{Task: "T-1", Attempt: 1, Step: event.StepReview}), `"sent"`, "T-1's review result")

	r.add("pj", "T-1", event.StepNeedsYou, event.KindFact, event.FactData{Merge: true, Cause: event.CauseHeld, Reason: "held: why"})
	greenTask(r, "T-5", port.Candidate{Task: "T-5", SHA: "c5", Target: "b", Patch: "p5"})
	if !runNext(r, p, "T-5", f) {
		t.Fatal("T-5's job held by T-1's needs-you")
	}
	if len(f.launches) != 1 || !f.launches[0].Fresh {
		t.Errorf("launches %+v, want one fresh start for T-5", f.launches)
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
			greenTask(r, "T-1", port.Candidate{Task: "T-1", SHA: "c1", Target: "b", Patch: "p1"})
			r.add("pj", "T-1", event.StepReview, event.KindIntent, promptData{Agent: "pj-int", Job: "T-1/1/review", Patch: "p1"})
			r.add("pj", "T-1", "start.review", event.KindIntent, c.at)
			r.a.mu.Lock()
			if c.now == "gone" {
				delete(r.a.obs, "w1:p9")
			} else {
				r.a.obs["w1:p9"].pane.Session = c.now
			}
			r.a.mu.Unlock()
			f := &startJobFakes{concFakes: r.fakes["pj"]}
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

func TestWorkerJobNextTwoJobsInLaneTakesFirstTaskByName(t *testing.T) {
	t.Parallel()
	for range orderTries {
		p := twoInLane()
		if _, ok := p.folded.loop["T-2"].workerJob(); !ok {
			t.Fatal("precondition: T-2 has a worker job open")
		}
		mt, k, ok, err := (&App{}).callerJob(p, wire.JobNextReq{Envelope: wire.Envelope{Role: "worker", Agent: "w1"},
			Lane: "L1"})
		if err != nil || !ok || mt.task != "T-1" || k != (event.Key{Task: "T-1", Attempt: 1, Step: event.StepWork}) {
			t.Fatalf("callerJob = %s %v %v %v; want T-1's work job", mt.task, k, ok, err)
		}
	}
}

func TestIntegratorJobNextEqualSeqsTakesFirstTaskByName(t *testing.T) {
	t.Parallel()
	for range orderTries {
		p := twoInLane()
		p.integrator = "int"
		for _, mt := range p.folded.loop {
			mt.state, mt.job = stAwaiting, event.StepReview
		}
		if _, ok := p.folded.loop["T-2"].integratorJob(); !ok {
			t.Fatal("precondition: T-2 has an integrator job open")
		}
		if _, ok := p.log.IntentOf(event.Key{Task: "T-2", Attempt: 1, Step: event.StepReview}); ok {
			t.Fatal("precondition: no intent recorded (equal seqs)")
		}
		mt, k, ok, err := (&App{}).callerJob(p, wire.JobNextReq{Envelope: wire.Envelope{Role: "integrator",
			Agent: "int"}})
		if err != nil || !ok || mt.task != "T-1" || k != (event.Key{Task: "T-1", Attempt: 1, Step: event.StepReview}) {
			t.Fatalf("callerJob = %s %v %v %v; want T-1's review job", mt.task, k, ok, err)
		}
	}
}

// RV-133 #1 (T-959): restart_job is callerJob's answer, never the status list's first review job. A held (earlier)
// review and a later active one leave the integrator's restart with the active job — the one its job_next would hand
// it — although the status list shows the held task first (by task name) with its job open.
func TestRestartJobSkipsHeld(t *testing.T) {
	t.Parallel()
	r := newConcRig(t)
	defer r.a.closeLog()
	// T-6's review job goes out after T-4's: its intent is the newer one, so it is the integrator's oldest open job
	// once T-4 is held.
	r.add("pr", "T-6", event.StepSubmitted, event.KindFact, submitData{Lane: "L-06"})
	r.add("pr", "T-6", event.StepSubmit, event.KindResult, submitOut{SHA: "s6"})
	r.add("pr", "T-6", event.StepGate, event.KindIntent, gateIn{Kind: port.InputLane, Lane: "L-06", Input: "s6"})
	r.add("pr", "T-6", event.StepGate, event.KindResult, gateOut{Outcome: outGreen, Candidate: port.Candidate{Task: "T-6",
		SHA: "c-T-6", Target: "b", Patch: "p"}})
	r.add("pr", "T-6", event.StepReview, event.KindIntent, promptData{Agent: "pr-int", Job: "T-6/1/review", Patch: "p"})
	r.add("pr", "T-6", event.StepReview, event.KindResult, "sent")
	a := r.app("pr")
	if _, err := a.verdict(context.Background(), wire.VerdictReq{Envelope: wire.Envelope{Root: r.roots["pr"],
		Agent: "pr-int", Role: "integrator", CallID: "h1"}, Verb: wire.MethodHold, Task: "T-4", Job: "T-4/1/review",
		Note: "the owner decides"}); err != nil {
		t.Fatalf("hold T-4: %v", err)
	}
	var shown []string
	for _, ts := range r.status("pr").Tasks {
		shown = append(shown, ts.Task+" "+ts.State+" "+ts.Job)
	}
	if want := []string{"T-4 needs-you T-4/1/review", "T-6 awaiting-accept T-6/1/review"}; !slices.Equal(shown, want) {
		t.Fatalf("precondition: the status lists the held job first: %q, want %q", shown, want)
	}
	job, err := a.restartJob(wire.RestartJobReq{Envelope: wire.Envelope{Root: r.roots["pr"], Agent: "pr-int",
		Role: "integrator"}})
	if err != nil || job != "T-6/1/review" {
		t.Errorf("the integrator's restart job: %q, %v; want the active T-6/1/review, not the held T-4's", job, err)
	}
	if job, err := r.app("pq").restartJob(wire.RestartJobReq{Envelope: wire.Envelope{Root: r.roots["pq"],
		Agent: "pq-int", Role: "integrator"}}); job != "" || err != nil {
		t.Errorf("no open job: %q, %v; want no wake", job, err)
	}
}

func TestWorkTextCarriesBothRoleTexts(t *testing.T) {
	t.Parallel()
	p, kit := roleFixture(t, "# Role: worker\nkit rules\n", "# Worker here\nproject rules\n")
	text, err := workText(p, mtask{task: "T-9", lane: "L-01"}, kit)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Implement T-9 on main.", "# Role: worker\nkit rules", "# Worker here\nproject rules",
		filepath.Join(kit, "templates", "agents", "worker.md"), filepath.Join(p.root, "doc/roles/worker.md")} {
		if !strings.Contains(text, want) {
			t.Errorf("work text has no %q:\n%s", want, text)
		}
	}
	for _, no := range []string{"Read AGENTS.md and your role text", "Also read", "{{"} {
		if strings.Contains(text, no) {
			t.Errorf("work text carries %q (the roles are in the job, not a read line):\n%s", no, text)
		}
	}
}

func TestWorkTextSkipsAMissingProjectDoc(t *testing.T) {
	t.Parallel()
	p, kit := roleFixture(t, "# Role: worker\nkit rules\n", "")
	text, err := workText(p, mtask{task: "T-9", lane: "L-01"}, kit)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(text, "kit rules") {
		t.Errorf("the kit's role text is not in the job:\n%s", text)
	}
	if strings.Contains(text, filepath.Join(p.root, "doc/roles/worker.md")) || strings.Contains(text, "This project's") {
		t.Errorf("a project worker doc the checkout lacks is in the job:\n%s", text)
	}
}

func TestWorkTextNamesARoleTextTooLargeToCarry(t *testing.T) {
	t.Parallel()
	big := "# Role: worker\n" + strings.Repeat("K", roleTextMax)
	p, kit := roleFixture(t, big, "# Worker here\nproject rules\n")
	text, err := workText(p, mtask{task: "T-9", lane: "L-01"}, kit)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(kit, "templates", "agents", "worker.md")
	if !strings.Contains(text, "Read your role text before you act: "+path+" (too large to carry here).") {
		t.Errorf("no read line naming the oversized role text %s:\n%s", path, text)
	}
	if strings.Contains(text, strings.Repeat("K", 64)) {
		t.Error("a role text over roleTextMax is inlined")
	}
	if !strings.Contains(text, "project rules") {
		t.Errorf("the role text under the cap is not inlined:\n%s", text)
	}
}

func TestRoleTextAtTheCapIsInlined(t *testing.T) {
	t.Parallel()
	at := "# Role: worker\n" + strings.Repeat("K", roleTextMax-len("# Role: worker\n"))
	p, kit := roleFixture(t, at, "")
	text, err := workText(p, mtask{task: "T-9", lane: "L-01"}, kit)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(text, "Read your role text") || !strings.Contains(text, strings.Repeat("K", 64)) {
		t.Errorf("a role text of exactly roleTextMax bytes is not inlined:\n%s", text)
	}
}

// T-721, T-765: the job's prompt_extra is the kind's of the model its dispatch chose (kinds.<kind>.prompt_extra); a
// dispatch recorded before T-721 (no model) carries none.
func TestWorkTextPromptExtraIsTheDispatchsModels(t *testing.T) {
	t.Parallel()
	p, kit := roleFixture(t, "# Role: worker\nkit rules\n", "")
	cfg := `{"name":"pj","paths":{"agents":"doc/roles"},"lanes":1,"kinds":{
		"claude":{"prompt_extra":"Claude extra."},"codex":{"prompt_extra":"Codex extra."}}}`
	writeRoleFile(t, filepath.Join(p.root, ".obor", "config.json"), cfg)
	text, err := workText(p, mtask{task: "T-9", lane: "L-01", disp: dispatchData{Lane: "L-01", Type: "luna:high"}}, kit)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(text, "Codex extra.") || strings.Contains(text, "Claude extra.") {
		t.Errorf("a luna:high dispatch: want codex's prompt_extra only:\n%s", text)
	}
	text, err = workText(p, mtask{task: "T-9", lane: "L-01", disp: dispatchData{Lane: "L-01", Type: "opus"}}, kit)
	if err != nil || !strings.Contains(text, "Claude extra.") || strings.Contains(text, "Codex extra.") {
		t.Errorf("an opus dispatch: want claude's prompt_extra only (%v):\n%s", err, text)
	}
	text, err = workText(p, mtask{task: "T-9", lane: "L-01", disp: dispatchData{Lane: "L-01"}}, kit)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(text, "Claude extra.") || strings.Contains(text, "Codex extra.") {
		t.Errorf("a dispatch with no model: want no prompt_extra:\n%s", text)
	}
}

// The kit's own prompt template names no role text: the job carries it.
func TestWorkerPromptNamesNoRoleText(t *testing.T) {
	t.Parallel()
	kit, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	prompt, err := workerPrompt(config.Fallback(t.TempDir()), kit, "T-9", config.Model{PromptExtra: "Extra."})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"implement the spec of T-9 exactly", "obor spec show T-9", "Extra."} {
		if !strings.Contains(prompt, want) {
			t.Errorf("the rendered prompt has no %q:\n%s", want, prompt)
		}
	}
	for _, no := range []string{"worker.md", "Also read", "{{", "task_done", "commands.check", "Wrong-tab", "AGENTS.md"} {
		if strings.Contains(prompt, no) {
			t.Errorf("the rendered prompt carries %q:\n%s", no, prompt)
		}
	}
}

// T-674: only an empty "## This project" section is dropped from the job; a project's filled one is carried.
func TestWorkTextDropsOnlyAnEmptyProjectSection(t *testing.T) {
	t.Parallel()
	kitRole := "# Role: worker\nkit rules\n\n## This project\n<!-- fill in: kit note -->\n"
	doc := "# Worker here\nproject rules\n\n## This project\n- run make build\n"
	p, kit := roleFixture(t, kitRole, doc)
	text, err := workText(p, mtask{task: "T-9", lane: "L-01"}, kit)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(text, "- run make build") {
		t.Errorf("the project's filled This project section is not in the job:\n%s", text)
	}
	if strings.Contains(text, "fill in") {
		t.Errorf("the kit's empty This project section is in the job:\n%s", text)
	}
	if n := strings.Count(text, "## This project"); n != 1 {
		t.Errorf("## This project appears %d times, want the project's one:\n%s", n, text)
	}
}

// T-674: the work job of this kit (templates/worker-prompt.txt, templates/agents/worker.md and obor-kit's
// docs/agents/worker.md) says each rule once, carries no empty "This project" section and does not tell a worker to
// fetch the job it is reading.
func TestWorkJobSaysEachRuleOnce(t *testing.T) {
	t.Parallel()
	kit, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".obor"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".obor", "config.json"),
		[]byte(`{"name":"pj","paths":{"agents":"docs/agents"}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	doc, err := os.ReadFile(filepath.Join(kit, "docs", "agents", "worker.md"))
	if err != nil {
		t.Fatal(err)
	}
	writeRoleFile(t, filepath.Join(root, "docs", "agents", "worker.md"), string(doc))
	text, err := workText(&proj{root: root}, mtask{task: "T-9", lane: "L-01"}, kit)
	if err != nil {
		t.Fatal(err)
	}
	// Precondition: the job carries the kit's role text and this project's doc, not an empty job.
	for _, want := range []string{"# Role: worker", "obor run check_affected", "implement the spec of T-9 exactly"} {
		if !strings.Contains(text, want) {
			t.Fatalf("precondition: the work job has no %q:\n%s", want, text)
		}
	}
	for _, rule := range []string{"Read AGENTS.md", "test cache on", "commands.check",
		"call `task_done` with your final report", "wrong tab"} {
		if n := strings.Count(text, rule); n != 1 {
			t.Errorf("the rule %q appears %d times, want once:\n%s", rule, n, text)
		}
	}
	for _, no := range []string{"fill in", "## This project", "before anything else", "your first prompt is a job tag",
		"Call the `job_next` tool", "Finish by calling the obor task_done tool"} {
		if strings.Contains(text, no) {
			t.Errorf("the work job carries %q:\n%s", no, text)
		}
	}
}

// workJobText is the work job of a project whose .obor/config.json is cfg, from the kit's shipped templates: the test
// fails when the prompt or the kit's role text is not in it (an empty job would pass every "names no" check below).
func workJobText(t *testing.T, kit, cfg string) string {
	t.Helper()
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".obor"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".obor", "config.json"), []byte(cfg), 0o644); err != nil {
		t.Fatal(err)
	}
	text, err := workText(&proj{root: root}, mtask{task: "T-9", lane: "L-01"}, kit)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"implement the spec of T-9 exactly", "# Role: worker"} {
		if !strings.Contains(text, want) {
			t.Fatalf("precondition: the work job has no %q:\n%s", want, text)
		}
	}
	return text
}

// T-678, T-1107: the work job names the built-in report sections (config.ReportSections) once; a config that still
// has the retired done_file_sections does not change them.
func TestWorkJobNamesTheReportSections(t *testing.T) {
	t.Parallel()
	kit, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	want := "Your final report has these sections: " + strings.Join(config.ReportSections, ", ") + "."
	for _, c := range []struct{ name, cfg string }{
		{"default", `{"name":"pj"}`},
		{"retired key ignored", `{"name":"pj","done_file_sections":["Summary","Verification"]}`},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			text := workJobText(t, kit, c.cfg)
			if n := strings.Count(text, want); n != 1 {
				t.Errorf("the job names the sections %d times, want once (%q):\n%s", n, want, text)
			}
			if strings.Contains(text, "Verification") {
				t.Errorf("the job names a retired key's sections:\n%s", text)
			}
		})
	}
}

// The fix job names the role texts for a worker started fresh (T-603): the kit's and the project's when the checkout has
// one, the kit's alone when it does not.
func TestFixJobNamesBothRoleTexts(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		name, doc string
		want      func(p *proj, kit string) string
		absent    bool // the checkout has no project doc to name
	}{
		{"both", "# Worker here\nproject rules\n", func(p *proj, kit string) string {
			return "If this session did not get the work job (a fresh start), read " +
				filepath.Join(kit, "templates", "agents", "worker.md") + " and " +
				filepath.Join(p.root, "doc/roles/worker.md") + " first."
		}, false},
		{"kit alone", "", func(_ *proj, kit string) string {
			return "read " + filepath.Join(kit, "templates", "agents", "worker.md") + " first."
		}, true},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			p, kit := roleFixture(t, "# Role: worker\nkit\n", c.doc)
			read, err := fixRead(p, kit)
			if err != nil {
				t.Fatal(err)
			}
			text := fixText(mtask{task: "T-9", attempt: 2, attemptVars: attemptVars{line: "add a test"}}, store.SpecRow{}, read)
			if !strings.Contains(text, c.want(p, kit)) {
				t.Errorf("the fix job has no %q:\n%s", c.want(p, kit), text)
			}
			if c.absent && strings.Contains(text, p.root) {
				t.Errorf("the fix job names a project doc the checkout lacks:\n%s", text)
			}
		})
	}
}

// T-819: a fix job whose worker never had the spec's rev carries it (a change made while the task was not working);
// at the rev it already has, it does not.
func TestFixJobCarriesAChangedRevOnly(t *testing.T) {
	t.Parallel()
	row := store.SpecRow{Task: "T-9", Rev: 2, Text: "# T-9 — W\n\nDifficulty: medium\n"}
	text := fixText(mtask{task: "T-9", attempt: 2, attemptVars: attemptVars{line: "add a test"}, specRev: 1}, row,
		"read it.")
	if !strings.Contains(text, "The spec of T-9 (rev 2") {
		t.Errorf("a fix job of a worker at rev 1: %q; want the rev 2 spec", text)
	}
	if text := fixText(mtask{task: "T-9", attempt: 2, attemptVars: attemptVars{line: "add a test"}, specRev: 2}, row,
		"read it."); strings.Contains(text, "The spec of T-9") {
		t.Errorf("a fix job at the spec's rev repeats it: %q", text)
	}
}

// integratorJobs is the review and resolve job texts of a rig project whose kit and project doc carry integrator role
// texts (kitRole under the kit's templates/agents), and the paths of the two files.
func integratorJobs(t *testing.T, kitRole string) (jobs map[string]string, kitPath, docPath string) {
	t.Helper()
	r := newConcRig(t)
	p := r.proj("pj")
	cfg, err := config.Load(p.root)
	if err != nil {
		t.Fatal(err)
	}
	kit := t.TempDir()
	kitPath = filepath.Join(kit, "templates", "agents", "integrator.md")
	docPath = filepath.Join(p.root, cfg.AgentsDir(), "integrator.md")
	writeRoleFile(t, kitPath, kitRole)
	writeRoleFile(t, docPath, "# Integrator here\nproject integrator rules\n")
	mt := *p.folded.loop["T-1"]
	mt.cand = port.Candidate{Task: "T-1", SHA: "c1", Target: "b", Patch: "p1", Stat: "stat-line"}
	mt.line = "CONFLICT in /s: a.go"
	review, err := r.a.reviewText(p, mt, kit)
	if err != nil {
		t.Fatal(err)
	}
	resolve, err := resolveText(p, mt, kit)
	if err != nil {
		t.Fatal(err)
	}
	return map[string]string{"review": review, "resolve": resolve}, kitPath, docPath
}

func TestIntegratorJobsCarryBothRoleTexts(t *testing.T) {
	t.Parallel()
	jobs, kitPath, docPath := integratorJobs(t, "# Role: integrator\nkit integrator rules\n")
	for name, text := range jobs {
		if !strings.HasPrefix(text, map[string]string{"review": "Review T-1", "resolve": "Resolve T-1"}[name]) {
			t.Fatalf("%s job is not the %s job:\n%s", name, name, text)
		}
		for _, want := range []string{"# Role: integrator\nkit integrator rules", "# Integrator here\nproject integrator rules",
			"Your role text (" + kitPath + "):", "This project's integrator rules (" + docPath + "):"} {
			if !strings.Contains(text, want) {
				t.Errorf("%s job has no %q:\n%s", name, want, text)
			}
		}
		if strings.Contains(text, "Read your role text") {
			t.Errorf("%s job names a read:\n%s", name, text)
		}
	}
}

func TestIntegratorJobNamesARoleTextTooLargeToCarry(t *testing.T) {
	t.Parallel()
	jobs, kitPath, _ := integratorJobs(t, "# Role: integrator\n"+strings.Repeat("K", roleTextMax))
	for name, text := range jobs {
		if !strings.Contains(text, "Read your role text before you act: "+kitPath+" (too large to carry here).") {
			t.Errorf("%s job has no read line naming %s:\n%s", name, kitPath, text)
		}
		if strings.Contains(text, strings.Repeat("K", 64)) {
			t.Errorf("%s job inlines a role text over roleTextMax", name)
		}
		if !strings.Contains(text, "project integrator rules") {
			t.Errorf("%s job lacks the project doc under the cap:\n%s", name, text)
		}
	}
}
