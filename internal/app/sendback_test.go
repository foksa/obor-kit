package app

import (
	"context"
	"strings"
	"testing"
)

// leadSendback is the lead's obor sendback of task in project name (the lane form resolves the lane to it).
func (r *concRig) leadSendback(name, task, id string) (string, error) {
	return r.app(name).verdict(context.Background(), MethodSendback, Call{Root: r.roots[name], Agent: "pq-lead", Role: "lead",
		CallID: id, Task: task, Note: "fix the edge case"})
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

// The lead's sendback is serve's own transition (T-410, RV-45 #1): from a green candidate, an open job or a needs-you
// after a red gate or a hold the task goes back to its worker as the next attempt, its job, candidate and needs-you
// ended; any other state is refused by name with nothing recorded.
func TestLeadSendback(t *testing.T) {
	sub := func(r *concRig) {
		r.add("pq", "T-6", StepSubmitted, KindFact, submitData{Lane: "O-06", Agent: "pq-o-06"})
		r.add("pq", "T-6", StepSubmit, KindResult, submitOut{SHA: "s6"})
	}
	gate := func(r *concRig, outcome string) {
		r.add("pq", "T-6", StepGate, KindIntent, gateIn{Kind: InputLane, Lane: "O-06", Input: "s6"})
		r.add("pq", "T-6", StepGate, KindResult, gateOut{Outcome: outcome, Line: "gate " + outcome,
			Candidate: Candidate{Task: "T-6", SHA: "c-T-6", Target: "b", Patch: "p6"}})
	}
	for _, c := range []struct {
		name  string
		prep  func(r *concRig)
		state string // the precondition
		reply string // "": refused
		err   string
		facts int // facts in force for T-6 before
	}{
		{"green", func(r *concRig) { sub(r); gate(r, outGreen) }, stGreen, "SENDBACK T-6 (attempt 1, green)", "", 0},
		{"awaiting", func(r *concRig) {
			sub(r)
			gate(r, outGreen)
			r.add("pq", "T-6", StepReview, KindIntent, promptData{Agent: "pq-int", Job: "T-6/1/review", Patch: "p6"})
		}, stAwaiting, "SENDBACK T-6 (job T-6/1/review)", "", 0},
		{"resolving", func(r *concRig) {
			sub(r)
			gate(r, outConflict)
			r.add("pq", "T-6", StepResolve, KindIntent, promptData{Agent: "pq-int", Job: "T-6/1/resolve"})
		}, stResolving, "SENDBACK T-6 (job T-6/1/resolve)", "", 0},
		{"needs-you after a red", func(r *concRig) {
			sub(r)
			gate(r, outRed)
			r.add("pq", "T-6", StepNeedsYou, KindFact, FactData{Lane: "O-06", Merge: true, Cause: causeRed,
				Reason: "gate red after 2 sendbacks"})
		}, stNeedsYou, "SENDBACK T-6 (attempt 1, needs-you)", "", 1},
		{"needs-you after a hold", func(r *concRig) {
			sub(r)
			gate(r, outGreen)
			r.add("pq", "T-6", StepReview, KindIntent, promptData{Agent: "pq-int", Job: "T-6/1/review", Patch: "p6"})
			r.add("pq", "T-6", StepNeedsYou, KindFact, FactData{Lane: "O-06", Merge: true, Cause: causeHeld,
				Reason: "held: ask the owner", Job: "T-6/1/review"})
		}, stNeedsYou, "SENDBACK T-6 (job T-6/1/review)", "", 1},
		{"needs-you after a gate fail", func(r *concRig) {
			sub(r)
			gate(r, outFail)
			r.add("pq", "T-6", StepNeedsYou, KindFact, FactData{Lane: "O-06", Merge: true, Cause: causeGate,
				Reason: "gate: gate fail"})
		}, stNeedsYou, "", "sendback: T-6 is needs-you (gate: gate fail): obor resume or obor drop it", 1},
		{"accepted", func(r *concRig) {
			sub(r)
			gate(r, outGreen)
			r.add("pq", "T-6", StepReview, KindIntent, promptData{Agent: "pq-int", Job: "T-6/1/review", Patch: "p6"})
			r.add("pq", "T-6", verdictStep(MethodAccept, "a1"), KindResult, verdictResult{Job: "T-6/1/review", Patch: "p6"})
		}, stAccepted, "", "sendback: T-6 is accepted; retry when it settles", 0},
		{"submitted", sub, stSubmitted, "", "sendback: T-6 is submitted; retry when it settles", 0},
		{"no task", func(*concRig) {}, "", "", "sendback: T-6 is not open in obor serve", 0},
	} {
		t.Run(c.name, func(t *testing.T) {
			r := newConcRig(t)
			c.prep(r)
			if c.state != "" && (r.state("pq", "T-6") != c.state || r.factsOf("pq", "T-6") != c.facts) {
				t.Fatalf("precondition: T-6 %s with %d facts; want %s with %d", r.state("pq", "T-6"),
					r.factsOf("pq", "T-6"), c.state, c.facts)
			}
			got, err := r.leadSendback("pq", "T-6", "s1")
			if c.reply == "" {
				if err == nil || err.Error() != c.err {
					t.Fatalf("sendback: %q, %v; want the refusal %q", got, err, c.err)
				}
				if c.state != "" && (r.state("pq", "T-6") != c.state || r.attempt("pq", "T-6") != 1) {
					t.Errorf("a refused sendback moved T-6 to %s (attempt %d)", r.state("pq", "T-6"), r.attempt("pq", "T-6"))
				}
				if n := r.count("pq", "T-6", KindResult, verdictStep(MethodSendback, "s1")); n != 0 {
					t.Errorf("a refused sendback recorded %d results", n)
				}
				return
			}
			if err != nil || got != c.reply {
				t.Fatalf("sendback: %q, %v; want %q", got, err, c.reply)
			}
			if st, n := r.state("pq", "T-6"), r.attempt("pq", "T-6"); st != stWorking || n != 2 {
				t.Errorf("after the sendback T-6 is %s (attempt %d); want working, attempt 2", st, n)
			}
			if n := r.factsOf("pq", "T-6"); n != 0 {
				t.Errorf("the needs-you is still in force: %d facts", n)
			}
			_, err = r.app("pq").verdict(context.Background(), MethodAccept, Call{Root: r.roots["pq"], Task: "T-6",
				Role: "integrator", Agent: "pq-int", CallID: "v1", Job: "T-6/1/review", Patch: "p6"})
			if err == nil {
				t.Error("the old review is still accepted after the sendback")
			}
			if again, err := r.leadSendback("pq", "T-6", "s1"); err != nil || again != c.reply {
				t.Errorf("a repeated call id: %q, %v; want the stored reply", again, err)
			}

			// A replay keeps the next attempt, and the stored reply.
			r.app("pq").closeLog() // the first serve ends: the replay is the next one
			b := New(Options{Root: r.roots["pq"], Now: r.a.opt.Now}, &fakePanes{}, &fakeLanes{})
			b.loadProject()
			p := b.p
			if mt := p.folded.loop["T-6"]; mt == nil || mt.state != stWorking || mt.attempt != 2 || mt.needs.Seq != 0 {
				t.Errorf("replay: %+v; want T-6 working, attempt 2, no needs-you", mt)
			}
			if got := p.folded.calls[verdictStep(MethodSendback, "s1")]; got != c.reply {
				t.Errorf("replay: stored reply %q", got)
			}
		})
	}
}

// The reviewer's probe (RV-45 #1): the lead sends back T-4 with its review job out; the integrator's accept of the old
// candidate is refused, nothing is published, and the worker gets its fix job from serve.
func TestLeadSendbackEndsTheReview(t *testing.T) {
	r := newConcRig(t)
	if r.state("pr", "T-4") != stAwaiting {
		t.Fatalf("precondition: T-4 %s; want awaiting-accept", r.state("pr", "T-4"))
	}
	if got, err := r.leadSendback("pr", "T-4", "s1"); err != nil || got != "SENDBACK T-4 (job T-4/1/review)" {
		t.Fatalf("sendback: %q, %v", got, err)
	}
	a, p := r.app("pr"), r.proj("pr")
	a.mu.Lock()
	p.setLanes([]Lane{{Name: "O-04", Task: "T-4", Agent: "pr-o-04", State: "working"}})
	a.mu.Unlock()
	_, err := r.app("pr").verdict(context.Background(), MethodAccept, Call{Root: r.roots["pr"], Task: "T-4", Agent: "pr-int",
		Role: "integrator", CallID: "old-review", Job: "T-4/1/review", Patch: "p"})
	if err == nil || !strings.Contains(err.Error(), "no job open for T-4") {
		t.Errorf("accept of the old candidate after the sendback: %v; want refused", err)
	}
	if st := r.state("pr", "T-4"); st != stWorking {
		t.Errorf("T-4 is %s; want working", st)
	}

	// The loop hands the task back: the fix job, never a publish.
	a.mu.Lock()
	p.folded.loop["T-4"].agent = "pr-o-04"
	a.mu.Unlock()
	r.worker("pr", "w3:p4", "pr-o-04")
	r.run()
	defer r.stop()
	r.waitFor("T-4's fix job", func() bool { return r.count("pr", "T-4", KindIntent, StepFix) == 1 })
	if n := r.fakes["pr"].count(r.fakes["pr"].pubs, "T-4"); n != 0 || r.count("pr", "T-4", KindIntent, StepPublish) != 0 {
		t.Errorf("the old candidate was published (%d)", n)
	}
}

// A sendback while the task's gate runs is refused until it settles; the worker and the integrator may not send a
// task back as the lead, and a sendback says why.
func TestLeadSendbackRefusals(t *testing.T) {
	r := newConcRig(t)
	r.run()
	defer r.stop()
	if task := r.entered("pj"); task != "T-1" {
		t.Fatalf("pj's first gate: %s", task)
	}
	_, err := r.a.verdict(context.Background(), MethodSendback, Call{Root: r.roots["pj"], Agent: "pj-lead", Role: "lead",
		CallID: "g1", Task: "T-1", Note: "why"})
	if err == nil || !strings.Contains(err.Error(), "retry when it settles") {
		t.Errorf("sendback while the gate runs: %v", err)
	}
	if st := r.state("pj", "T-1"); st != stSubmitted {
		t.Errorf("a refused sendback moved T-1 to %s", st)
	}
	close(r.fakes["pj"].release)

	r.waitFor("T-4 awaiting with no step held", func() bool {
		a := r.app("pr")
		a.mu.Lock()
		defer a.mu.Unlock()
		p := a.p
		return p.folded.loop["T-4"].state == stAwaiting && p.held["T-4"] == ""
	})
	_, err = r.app("pr").verdict(context.Background(), MethodSendback, Call{Root: r.roots["pr"], Agent: "pr-o-04",
		Role: "worker", CallID: "w1", Task: "T-4", Note: "why"})
	if err == nil || !strings.Contains(err.Error(), "the integrator, the lead or the owner sends a task back") {
		t.Errorf("sendback by a worker: %v", err)
	}
	_, err = r.app("pr").verdict(context.Background(), MethodSendback, Call{Root: r.roots["pr"], Agent: "pr-lead", Role: "lead",
		CallID: "w2", Task: "T-4", Note: " "})
	if err == nil || err.Error() != "sendback: say why" {
		t.Errorf("sendback without a why: %v", err)
	}
	if st := r.state("pr", "T-4"); st != stAwaiting {
		t.Errorf("refused sendbacks moved T-4 to %s", st)
	}
}
