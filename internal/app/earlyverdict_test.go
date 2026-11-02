package app

import (
	"context"
	"encoding/json"
	"obor/internal/app/event"
	"obor/internal/app/port"
	"obor/internal/app/wire"
	"strconv"
	"testing"
)

// T-396 (RV-42-verify #2, the reviewer's TestVerifyRV42EarlyVerdict to the fixed outcome): the job is open before its
// start, so an integrator that fetches its job while it starts can answer it then; the start's result after that is
// its delivery only and keeps the state the verdict set. Fresh review and resolve jobs, every method, and the
// numbered and recovery jobs (review.2 / resolve.2 after job.lost, the owner's reprompt).
func TestEarlyVerdict(t *testing.T) {
	t.Parallel()
	gate := func(outcome string) func(r *concRig) {
		return func(r *concRig) {
			r.add("pj", "T-1", event.StepGate, event.KindIntent, gateIn{Kind: port.InputLane, Lane: "O-01", Input: "s1"})
			r.add("pj", "T-1", event.StepGate, event.KindResult, gateOut{Outcome: outcome, Candidate: port.Candidate{Task: "T-1", SHA: "c",
				Target: "b", Patch: "p"}})
		}
	}
	sent := func(step string) func(r *concRig) {
		return func(r *concRig) {
			r.add("pj", "T-1", step, event.KindIntent, promptData{Agent: "pj-int", Job: "T-1/1/" + step, Patch: "p"})
			r.add("pj", "T-1", step, event.KindResult, "sent")
		}
	}
	lost := func(step string) func(r *concRig) {
		return func(r *concRig) {
			r.add("pj", "T-1", event.StepLost, event.KindFact, event.FactData{Merge: true, Job: "T-1/1/" + step})
		}
	}
	reprompt := func(r *concRig) {
		r.add("pj", "T-1", event.StepNeedsYou, event.KindFact, event.FactData{Merge: true, Cause: causeUndelivered,
			Reason: "job not delivered"})
		r.add("pj", "T-1", event.StepUnblocked, event.KindFact, event.FactData{Merge: true, Reason: wire.ResumeReprompt})
	}
	for _, row := range []struct {
		name    string
		setup   []func(r *concRig)
		job     string // the job the step starts the integrator for
		method  string
		state   string // after the verdict, and still after the start
		attempt int
	}{
		{name: "review accept", setup: []func(*concRig){gate(outGreen)}, job: "review", method: wire.MethodAccept,
			state: stAccepted, attempt: 1},
		{name: "review sendback", setup: []func(*concRig){gate(outGreen)}, job: "review", method: wire.MethodSendback,
			state: stWorking, attempt: 2},
		{name: "review reject", setup: []func(*concRig){gate(outGreen)}, job: "review", method: wire.MethodReject,
			state: stRejected, attempt: 1},
		{name: "review hold", setup: []func(*concRig){gate(outGreen)}, job: "review", method: wire.MethodHold,
			state: stNeedsYou, attempt: 1},
		{name: "resolve resolved", setup: []func(*concRig){gate(outConflict)}, job: "resolve", method: wire.MethodResolved,
			state: stResolved, attempt: 1},
		{name: "resolve reject", setup: []func(*concRig){gate(outConflict)}, job: "resolve", method: wire.MethodReject,
			state: stFail, attempt: 1},
		{name: "resolve hold", setup: []func(*concRig){gate(outConflict)}, job: "resolve", method: wire.MethodHold,
			state: stNeedsYou, attempt: 1},
		{name: "review.2 after job.lost: accept", setup: []func(*concRig){gate(outGreen), sent("review"), lost("review")},
			job: "review.2", method: wire.MethodAccept, state: stAccepted, attempt: 1},
		{name: "resolve.2 after job.lost: resolved",
			setup: []func(*concRig){gate(outConflict), sent("resolve"), lost("resolve")}, job: "resolve.2",
			method: wire.MethodResolved, state: stResolved, attempt: 1},
		{name: "owner reprompt: sendback", setup: []func(*concRig){gate(outGreen), sent("review"), reprompt},
			job: "review.2", method: wire.MethodSendback, state: stWorking, attempt: 2},
	} {
		t.Run(row.name, func(t *testing.T) {
			t.Parallel()
			r := newConcRig(t)
			p := r.proj("pj")
			for _, s := range row.setup {
				s(r)
			}
			delete(r.a.obs, "w1:p9") // the integrator does not run: the step starts it
			job := "T-1/1/" + row.job
			key := event.Key{Task: "T-1", Attempt: 1, Step: row.job}
			f := &startJobFakes{concScratch: r.fakes["pj"]}
			var fetched wire.JobReply
			var verdictErr error
			var during mtask
			f.start = func(port.Launch) {
				if done(p, key) {
					t.Errorf("precondition: %s has its result before the start returned", job)
				}
				raw, err := r.a.jobNext(wire.Call{Root: p.root, Agent: p.integrator, Role: "integrator", CallID: "early"})
				if err != nil {
					t.Fatal(err)
				}
				_ = json.Unmarshal([]byte(raw), &fetched)
				_, verdictErr = r.a.verdict(context.Background(), row.method, wire.Call{Root: p.root, Agent: p.integrator,
					Role: "integrator", Task: fetched.Task, Job: fetched.Job, Patch: "p", Note: "why", CallID: "answer"})
				r.a.mu.Lock()
				during = *p.folded.loop["T-1"]
				r.a.mu.Unlock()
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

			if len(f.launches) != 1 || f.launches[0].Prompt != wakeLine(job) {
				t.Fatalf("launches %+v, want one with the first prompt %q", f.launches, wakeLine(job))
			}
			same(t, fetched.State+" "+fetched.Job, "job "+job, "job_next during the start")
			if verdictErr != nil {
				t.Fatalf("%s during the start: %v", row.method, verdictErr)
			}
			same(t, during.state+" "+strconv.Itoa(during.attempt), row.state+" "+strconv.Itoa(row.attempt), "state after the verdict")
			r.a.mu.Lock()
			after := *p.folded.loop["T-1"]
			r.a.mu.Unlock()
			same(t, after.state+" "+strconv.Itoa(after.attempt), row.state+" "+strconv.Itoa(row.attempt), "state after the start")
			same(t, resultOf(p, key), `"sent (start prompt)"`, row.job+" result")
			same(t, len(f.typed), 0, "Prompter calls")
			same(t, deliveredOf(r, job), 1, "delivered facts of "+job)
			// the job's intent precedes its start's: open before the wake
			if j, s := seqOf(r, "intent", row.job), seqOf(r, "intent", stepStart+row.job); j == 0 || s == 0 || j > s {
				t.Errorf("intent seq: %s %d, %s %d; want the job's first", row.job, j, stepStart+row.job, s)
			}
			// the job is answered: the same verdict again is refused (no job open, or another attempt)
			if _, err := r.a.verdict(context.Background(), row.method, wire.Call{Root: p.root, Agent: p.integrator,
				Role: "integrator", Task: "T-1", Job: job, Patch: "p", Note: "why", CallID: "later"}); err == nil &&
				row.method != wire.MethodHold {
				t.Errorf("a second %s on %s was taken", row.method, job)
			}
		})
	}
}

// T-396: the worker's job is open before its start: a fetch and a task_done during the start are taken, and the
// start's result after that keeps the submission (state submitted, the work step sent (start prompt), nothing typed).
func TestEarlyTaskDone(t *testing.T) {
	t.Parallel()
	r := newConcRig(t)
	p := r.proj("pj")
	p.dispatching = true
	lane := r.a.obs["w1:p2"].pane.Cwd
	r.add("pj", "T-2", event.StepDispatched, event.KindFact, dispatchData{Lane: "O-02", Dir: lane, Agent: "pj-o-02"})
	r.add("pj", "T-2", event.StepPrepare, event.KindResult, prepareOut{Base: "b"})
	r.a.lanes.(*fakeLanes).lanes = []port.Lane{{Name: "O-02", Dir: lane, Task: "T-2", Agent: "pj-o-02"}}
	f := &startJobFakes{concScratch: r.fakes["pj"]}
	var fetched wire.JobReply
	var doneErr error
	var during string
	f.start = func(port.Launch) {
		raw, err := r.a.jobNext(wire.Call{Root: p.root, Agent: "pj-o-02", Role: "worker", Lane: "O-02", CallID: "early"})
		if err != nil {
			t.Fatal(err)
		}
		_ = json.Unmarshal([]byte(raw), &fetched)
		_, doneErr = r.a.taskDone(wire.Call{Root: p.root, Agent: "pj-o-02", Role: "worker", Lane: "O-02", Task: "T-2",
			CallID: "done", Report: "Summary: x\nSelf-check: x\nSkipped: x\nOpen questions: x"})
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

// seqOf is the seq of the first record of T-1 in pj's log with kind and step (0: none).
func seqOf(r *concRig, kind, step string) int64 {
	recs, err := event.ReadLog(r.roots["pj"])
	if err != nil {
		r.t.Fatal(err)
	}
	for _, rec := range recs {
		if rec.Task == "T-1" && rec.Kind == kind && rec.Step == step {
			return rec.Seq
		}
	}
	return 0
}

// T-396: a work job open before a start that was refused stays the job: after obor resume the next start carries the
// same job's wake (no work.2), and its result sends it.
func TestStartAgainSameJob(t *testing.T) {
	t.Parallel()
	r := newConcRig(t)
	p := r.proj("pj")
	p.dispatching = true
	lane := r.a.obs["w1:p2"].pane.Cwd
	r.add("pj", "T-2", event.StepDispatched, event.KindFact, dispatchData{Lane: "O-02", Dir: lane, Agent: "pj-o-02"})
	r.add("pj", "T-2", event.StepPrepare, event.KindResult, prepareOut{Base: "b"})
	r.add("pj", "T-2", event.StepWork, event.KindIntent, promptData{Agent: "pj-o-02", Job: "T-2/1/work"})
	r.add("pj", "T-2", event.StepStartWork, event.KindIntent, startData{Lane: "O-02", Agent: "pj-o-02", Pane: "w1:p2"})
	r.add("pj", "T-2", event.StepStartWork, event.KindResult, startOut{Refused: "has background tasks"})
	r.add("pj", "T-2", event.StepNeedsYou, event.KindFact, event.FactData{Merge: true, Cause: causeStart, Reason: "start: refused"})
	r.add("pj", "T-2", event.StepUnblocked, event.KindFact, event.FactData{Merge: true, Reason: wire.ResumeRetry})
	f := &startJobFakes{concScratch: r.fakes["pj"]}
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
