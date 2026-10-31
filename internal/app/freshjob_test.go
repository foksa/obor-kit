package app

import (
	"context"
	"strings"
	"testing"
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
	step := r.a.next(p, p.loop[task])
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
		if !l.Fresh || l.Prompt != wakeLine(job) || l.Agent != "pj-int" {
			t.Errorf("launch %d: %+v, want a fresh pj-int with the first prompt %q", i, l, wakeLine(job))
		}
	}
	same(t, len(f.launches), 2, "starts (one per job)")
	same(t, len(f.typed), 0, "typed wakes")
	same(t, resultOf(p, Key{"T-5", 1, StepReview}), `"sent (start prompt)"`, "T-5's review result")
}

// A crash after the job's intent and its start's intent, before their results: the replay starts nothing again (the
// integrator runs: its wake typed), and a needs-you that left another task's job does not hold the next job.
func TestFreshIntegratorReplay(t *testing.T) {
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
		mt := *p.loop["T-1"]
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
