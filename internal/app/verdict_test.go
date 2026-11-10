package app

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"obor/internal/app/event"
	"obor/internal/app/port"
	"obor/internal/app/wire"
	"obor/internal/store"
)

// movedGreen publishes T-1 into a moved target and gates it green again at rev, the patch unchanged.
func movedGreen(t *testing.T, h *harness, gate, kind string, rev int) {
	t.Helper()
	addRecord(t, h, "T-1", event.StepPublish, event.KindResult, gateOut{Outcome: outMoved})
	addRecord(t, h, "T-1", gate, event.KindIntent, gateIn{Kind: kind, Input: "s1"})
	addRecord(t, h, "T-1", gate, event.KindResult, gateOut{Outcome: outGreen, SpecRev: rev,
		Candidate: port.Candidate{Task: "T-1", SHA: "candidate2", Target: "base2", Patch: "same-diff"}})
}

// pickGreenOnce runs the step pickGreen selects for T-1 (live, or after a replay of the log).
func pickGreenOnce(t *testing.T, h *harness, f *schedScratch, replay bool) *mtask {
	t.Helper()
	p := h.a.p
	if replay {
		p = reloadProj(t, h)
	}
	if mt := p.folded.loop["T-1"]; mt.state != stGreen || mt.cand.SHA != "candidate2" || mt.cand.Patch != "same-diff" {
		t.Fatalf("precondition: T-1 %s at %s (%s); want green at candidate2, the patch unchanged", mt.state,
			mt.cand.SHA, mt.cand.Patch)
	}
	s := h.a.pickGreen(p, *p.folded.loop["T-1"])
	if s == nil || !s(context.Background(), &Loop{Agents: f, Typer: f}) {
		t.Fatal("pickGreen: no step, or it failed")
	}
	return p.folded.loop["T-1"]
}

func TestAcceptNewSpecRevReviewsAgain(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		name, gate1, gate2, kind string
	}{
		{"lane then moved", nth(event.StepGate, 2), nth(event.StepGate, 3), port.InputMoved},
		{"two resolved candidates", nth(event.StepGate, 2), nth(event.StepGate, 3), port.InputResolved},
	} {
		for _, replay := range []bool{false, true} {
			name := c.name + "/live"
			if replay {
				name = c.name + "/replay"
			}
			t.Run(name, func(t *testing.T) {
				t.Parallel()
				h, f := seedSched(t, true)
				first := port.InputLane
				if c.kind == port.InputResolved {
					first = port.InputResolved
				}
				acceptAt(t, h, f, c.gate1, first, 1, "candidate1")
				movedGreen(t, h, c.gate2, c.kind, 2)
				mt := pickGreenOnce(t, h, f, replay)
				if mt.state != stAwaiting || mt.reviews != 2 || mt.job != nth(event.StepReview, 2) {
					t.Errorf("rev 2: T-1 %s, reviews %d, job %q; want awaiting review.2", mt.state, mt.reviews, mt.job)
				}
				if mt.accepted != "" {
					t.Errorf("rev 2: the rev 1 accept %q survived", mt.accepted)
				}
				if n := countStep(t, h, event.StepKept); n != 0 {
					t.Errorf("rev 2: %d kept facts; want none", n)
				}
			})
		}
	}
}

func TestAcceptSameSpecRevKept(t *testing.T) {
	t.Parallel()
	for _, replay := range []bool{false, true} {
		t.Run(map[bool]string{false: "live", true: "replay"}[replay], func(t *testing.T) {
			t.Parallel()
			h, f := seedSched(t, true)
			acceptAt(t, h, f, nth(event.StepGate, 2), port.InputLane, 1, "candidate1")
			movedGreen(t, h, nth(event.StepGate, 3), port.InputMoved, 1)
			mt := pickGreenOnce(t, h, f, replay)
			if mt.state != stAccepted || mt.reviews != 1 || countStep(t, h, event.StepKept) != 1 {
				t.Errorf("rev 1 again: T-1 %s, reviews %d, kept facts %d; want accepted, 1 review, 1 kept", mt.state,
					mt.reviews, countStep(t, h, event.StepKept))
			}
		})
	}
}

// A log written before the fix: kept recorded after a green gate of a new rev. It replays to the review, not to an
// accept of the old rev.
func TestAcceptKeptAfterRevChangeReplaysToReview(t *testing.T) {
	t.Parallel()
	h, f := seedSched(t, true)
	acceptAt(t, h, f, nth(event.StepGate, 2), port.InputLane, 1, "candidate1")
	movedGreen(t, h, nth(event.StepGate, 3), port.InputMoved, 2)
	addRecord(t, h, "T-1", event.StepKept, event.KindFact, JobData{Patch: "same-diff"})
	p := reloadProj(t, h)
	if countStep(t, h, event.StepKept) != 1 {
		t.Fatal("precondition: the log carries the kept fact")
	}
	mt := p.folded.loop["T-1"]
	if mt.state != stGreen || mt.accepted != "" || mt.stepName() != nth(event.StepReview, 2) {
		t.Errorf("replayed: T-1 %s, accepted %q, next %s; want green, no accept, review.2", mt.state, mt.accepted,
			mt.stepName())
	}
}

// isDropped: reply is a drop's of task: DROPPED <task> (no lane), or DROPPING <task> from <lane> (… : its lane's).
func isDropped(reply, task string) bool {
	return reply == dropReply(task) || strings.HasPrefix(reply, "DROPPING "+task+" from ")
}

// dropGone: st is a dropped task's, or a dropping one's (its lane's steps to come).
func dropGone(st string) bool { return st == stDropped || st == stDropping }

// obor drop of a task serve holds: a needs-you with no job open ends in dropped, which survives a replay; the
// needs-you leaves the facts in force; only the lead or the owner may drop, and not twice.
func TestDropNeedsYouTask(t *testing.T) {
	t.Parallel()
	r := newConcRig(t)
	r.add("pq", "T-3", event.StepNeedsYou, event.KindFact, event.FactData{Lane: "L-03", Merge: true, Cause: causeGate, Reason: "gate: FAIL T-3 is already in the target"})
	r.run()
	defer r.stop()
	r.waitFor("T-3 needs-you", func() bool { return r.state("pq", "T-3") == wire.PhaseNeedsYou })
	facts := func() int { return len(r.status("pq").Facts) }
	if facts() != 1 {
		t.Fatalf("precondition: want one fact in force, got %d", facts())
	}
	for _, role := range []string{"worker", "integrator"} {
		_, err := r.app("pq").drop(wire.DropReq{Envelope: wire.Envelope{Root: r.roots["pq"], Agent: "pq-x", Role: role,
			CallID: "w-" + role}, Task: "T-3", Why: "why"})
		if err == nil || !strings.Contains(err.Error(), "the lead or the owner drops a task") {
			t.Errorf("drop by %s: %v", role, err)
		}
	}
	if _, err := r.app("pq").drop(wire.DropReq{Envelope: wire.Envelope{Root: r.roots["pq"], Role: "lead", CallID: "nw"},
		Task: "T-3"}); err == nil ||
		!strings.Contains(err.Error(), "say why") {
		t.Errorf("drop without a why: %v", err)
	}
	if r.state("pq", "T-3") != wire.PhaseNeedsYou {
		t.Fatalf("a refused drop moved the task to %s", r.state("pq", "T-3"))
	}
	for _, id := range []string{"d1", "d1"} { // a repeated call id gets the stored reply
		if got, err := r.dropCall("pq", "T-3", id); err != nil || got != "DROPPED T-3" {
			t.Fatalf("drop %s: %q, %v", id, got, err)
		}
	}
	if _, err := r.dropCall("pq", "T-3", "d2"); err == nil || !strings.Contains(err.Error(), "dropped already") {
		t.Errorf("a second drop: %v", err)
	}
	r.waitFor("the task dropped", func() bool { return r.state("pq", "T-3") == stDropped })
	if facts() != 0 {
		t.Errorf("the needs-you is still in force after the drop: %d facts", facts())
	}
	var shown string
	for _, ts := range r.status("pq").Tasks {
		if ts.Task == "T-3" {
			shown = ts.State
		}
	}
	if shown != "dropped" {
		t.Errorf("obor status --facts shows T-3 %q; want dropped", shown)
	}
	if n := r.fakes["pq"].count(r.fakes["pq"].gates, "T-3"); n != 0 {
		t.Errorf("a dropped task gated %d times", n)
	}

	// A replay of the log keeps it ended, and the stored reply with it.
	r.app("pq").closeLog() // the first serve ends: the replay is the next one
	b := New(Options{Root: r.roots["pq"], Now: r.a.opt.Now}, &fakePanes{}, &fakeLanes{})
	b.loadProject()
	p := b.p
	if mt := p.folded.loop["T-3"]; mt == nil || mt.state != stDropped || mt.needs.fact.Seq != 0 {
		t.Errorf("replay: %+v; want T-3 dropped with no needs-you", mt)
	}
	if got := p.folded.calls[verdictStep(wire.MethodDrop, "d1")]; got != "DROPPED T-3" {
		t.Errorf("replay: stored reply %q", got)
	}
}

// A drop with a review job open ends the job: the integrator's later verdict is refused as superseded, and a drop
// while T-1's gate holds the scratch is refused until the gate has settled.
func TestDropRefusedWhilePendingThenSupersedesTheJob(t *testing.T) {
	t.Parallel()
	r := newConcRig(t)
	r.fakes["pr"].holdPub = "T-4"
	r.run()
	defer r.stop()
	if task := r.entered("pj"); task != "T-1" {
		t.Fatalf("pj's first gate: %s", task)
	}
	if _, err := r.dropCall("pj", "T-1", "g1"); err == nil || !strings.Contains(err.Error(), "T-1 has gate running; retry when it settles") {
		t.Errorf("drop while the gate runs: %v", err)
	}
	if dropGone(r.state("pj", "T-1")) {
		t.Fatal("a refused drop dropped the task")
	}

	// A publish in flight is refused the same way.
	if _, err := r.app("pr").verdict(context.Background(), wire.VerdictReq{Envelope: wire.Envelope{Root: r.roots["pr"],
		Role: "integrator", Agent: "pr-int", CallID: "v1"}, Verb: wire.MethodAccept, Task: "T-4", Job: "T-4/1/review",
		Patch: "p"}); err != nil {
		t.Fatal(err)
	}
	if got := r.entered("pr"); got != "publish T-4" {
		t.Fatalf("pr: %q; want publish T-4 held", got)
	}
	if _, err := r.dropCall("pr", "T-4", "p1"); err == nil || !strings.Contains(err.Error(), "T-4 has publish running; retry when it settles") {
		t.Errorf("drop while the publish runs: %v", err)
	}

	close(r.fakes["pj"].release)
	r.waitFor("T-1 awaiting its review", func() bool { return r.state("pj", "T-1") == stAwaiting })
	var got string
	var err error
	r.waitFor("the review prompt's step returned", func() bool { // a step that holds the task refuses the drop
		got, err = r.dropCall("pj", "T-1", "g2")
		return err == nil || !strings.Contains(err.Error(), "running; retry when it settles")
	})
	// its lane's row holds it: freed by the merge loop's lane.free (the lane names no worker to stop)
	if err != nil || got != "DROPPING T-1 from L-01 (abandoned)" {
		t.Fatalf("drop with the review job open: %q, %v", got, err)
	}
	r.waitFor("T-1 dropped", func() bool { return r.state("pj", "T-1") == stDropped })
	_, err = r.a.verdict(context.Background(), wire.VerdictReq{Envelope: wire.Envelope{Root: r.roots["pj"],
		Role: "integrator", Agent: "pj-int", CallID: "v2"}, Verb: wire.MethodAccept, Task: "T-1", Job: "T-1/1/review",
		Patch: "p"})
	if err == nil || !strings.Contains(err.Error(), "superseded: T-1 was dropped") {
		t.Errorf("accept after the drop: %v", err)
	}
	if r.count("pj", "T-1", event.KindFact, event.StepMerged) != 0 || r.fakes["pj"].count(r.fakes["pj"].pubs, "T-1") != 0 {
		t.Error("a dropped task was published")
	}
}

// laneT2 is the rig with pj's T-2 dispatched onto L-02 and its lane prepared (L-02's row holds it), the merge loops
// running.
func laneT2(t *testing.T) (*concRig, *proj, *concFakes) {
	t.Helper()
	r := newConcRig(t)
	dispatchT2(r)
	r.add("pj", "T-2", event.StepPrepare, event.KindResult, prepareOut{Base: "target"})
	r.run()
	t.Cleanup(r.stop)
	return r, r.proj("pj"), r.fakes["pj"]
}

// dropT2 is the lead's obor drop of pj's T-2 (why: moving on) with --discard and force as given, once no step of its
// holds it (the running loop may be in T-2's dispatch steps: a drop then is refused, "retry when it settles").
func dropT2(r *concRig, id string, force, discard bool) (reply string, err error) {
	r.waitFor("a drop of T-2 not refused as busy", func() bool {
		reply, err = r.a.drop(wire.DropReq{Envelope: wire.Envelope{Root: r.roots["pj"], Agent: "lead", Role: "lead",
			CallID: id}, Task: "T-2", Why: "moving on", Discard: discard, Force: force})
		return err == nil || !strings.Contains(err.Error(), "retry when it settles")
	})
	return reply, err
}

// obor drop names the task; serve knows its lane (its row holds it) and answers DROPPING at once
// (fact dropping); the merge loop stops its worker (worker.stop), frees the lane (lane.free: the lane adapter's Free)
// and records dropped: the lane row clears. A refused Free is the owner's needs-you{lane} naming --discard: the task
// dropping, its row kept; the drop given again frees it (no second stop: the worker is stopped already).
func TestDropByTaskFreesItsLane(t *testing.T) {
	t.Parallel()
	r, p, f := laneT2(t)
	if r.row(p) != "T-2" {
		t.Fatalf("precondition: L-02's row holds %q, want T-2", r.row(p))
	}
	f.mu.Lock()
	f.refuse = "lane L-02 is dirty (--discard to drop its uncommitted changes)"
	f.mu.Unlock()
	reply, err := dropT2(r, "d1", true, false)
	if want := "DROPPING T-2 from L-02 (moving on; stopping pj-l-02)"; err != nil || reply != want {
		t.Fatalf("drop: %q, %v; want %q", reply, err, want)
	}
	r.waitFor("the free's needs-you", func() bool { return r.heldWhy(p, "T-2") != "" })
	if why := r.heldWhy(p, "T-2"); !strings.Contains(why, f.refuse) || !strings.Contains(why, "obor drop T-2 --discard") {
		t.Errorf("needs-you %q; want the refusal and --discard", why)
	}
	if st := r.state("pj", "T-2"); st != "needs-you" || r.row(p) != "T-2" || r.count("pj", "T-2", event.KindFact, event.StepDropped) != 0 {
		t.Errorf("after a refused Free: T-2 %s, L-02's row %q; want blocked, the row kept, not dropped", st, r.row(p))
	}
	f.mu.Lock()
	f.refuse = ""
	f.mu.Unlock()
	if reply, err := dropT2(r, "d2", true, true); err != nil || !strings.HasPrefix(reply, "DROPPING T-2 from L-02") {
		t.Fatalf("drop --discard: %q, %v", reply, err)
	}
	r.waitFor("dropped", func() bool { return r.state("pj", "T-2") == stDropped })
	f.mu.Lock()
	frees, stops := append([]port.FreeInput(nil), f.frees...), append([]string(nil), f.stops...)
	f.mu.Unlock()
	if len(frees) != 1 || frees[0] != (port.FreeInput{Lane: "L-02", Branch: "task/T-2", Target: "target", Discard: true}) {
		t.Errorf("Free calls %+v; want one for L-02's task/T-2 at the target, discarding", frees)
	}
	if len(stops) != 1 || stops[0] != "pj-l-02 force=true" {
		t.Errorf("stops %q; want pj-l-02 once, with force", stops)
	}
	if r.row(p) != "" {
		t.Errorf("after DROPPED: L-02's row %q, want none", r.row(p))
	}
	rows, err := store.ReadLanesAt(r.roots["pj"])
	if _, ok := rows["L-02"]; ok || err != nil {
		t.Errorf("the lanes table still has L-02: %+v, %v", rows, err)
	}
	dropping := r.count("pj", "T-2", event.KindFact, event.StepDropping)
	if reply, err := dropT2(r, "d1", true, false); err != nil || !strings.HasPrefix(reply, "DROPPING T-2") ||
		r.count("pj", "T-2", event.KindFact, event.StepDropping) != dropping || dropping != 2 {
		t.Errorf("the first call id again: %q, %v (dropping facts %d, was %d); want its stored reply, nothing recorded",
			reply, err, r.count("pj", "T-2", event.KindFact, event.StepDropping), dropping)
	}
}

// The drop's stop of the task's worker, without --force too; a worker still working after Escape is
// stopped only with --force: without it the stop is refused, a needs-you naming obor drop --force, and nothing freed
// or dropped; obor drop --force then stops it, frees the lane and drops it.
func TestDropStopsTheWorker(t *testing.T) {
	t.Parallel()
	t.Run("no --force: the worker stopped, the lane freed, dropped", func(t *testing.T) {
		t.Parallel()
		r, _, f := laneT2(t)
		if reply, err := dropT2(r, "d1", false, true); err != nil || !strings.HasPrefix(reply, "DROPPING T-2 from L-02") {
			t.Fatalf("drop: %q, %v", reply, err)
		}
		r.waitFor("dropped", func() bool { return r.state("pj", "T-2") == stDropped })
		f.mu.Lock()
		defer f.mu.Unlock()
		if len(f.stops) != 1 || f.stops[0] != "pj-l-02 force=false" || len(f.frees) != 1 {
			t.Errorf("stops %q, frees %d; want pj-l-02 once without force, then the free", f.stops, len(f.frees))
		}
	})
	// Any stop that fails holds the drop (the worker may still run in the lane the next task gets): a
	// needs-you naming the error and the retry, nothing freed or dropped, the row kept.
	for _, c := range []struct {
		name  string
		force bool
		fails func(force bool) string
	}{
		{"a quit timeout", false, func(bool) string { return "pj-l-02 did not quit within 30s" }},
		{"a forced stop failing", true, func(force bool) string {
			if force {
				return "pj-l-02: exit failed: pane w1:p2 still runs"
			}
			return ""
		}},
	} {
		t.Run(c.name+": a needs-you, the lane kept", func(t *testing.T) {
			t.Parallel()
			r, p, f := laneT2(t)
			f.mu.Lock()
			f.stopFails = c.fails
			f.mu.Unlock()
			if _, err := dropT2(r, "d1", c.force, true); err != nil {
				t.Fatal(err)
			}
			r.waitFor("the stop's needs-you", func() bool { return r.heldWhy(p, "T-2") != "" })
			want := "pj-l-02 not stopped: " + c.fails(c.force) + "; obor resume T-2 retries"
			if why := r.heldWhy(p, "T-2"); why != want {
				t.Errorf("needs-you %q; want %q", why, want)
			}
			f.mu.Lock()
			stops, frees := len(f.stops), len(f.frees)
			f.mu.Unlock()
			if stops != 1 || frees != 0 || r.row(p) != "T-2" || r.count("pj", "T-2", event.KindFact, event.StepDropped) != 0 {
				t.Errorf("the refused stop: %d stops, %d frees, L-02's row %q; want one stop tried, nothing freed or "+
					"dropped, the lane kept", stops, frees, r.row(p))
			}
		})
	}
	t.Run("a worker that ignores Escape: a needs-you without --force, stopped and dropped with it", func(t *testing.T) {
		t.Parallel()
		r, p, f := laneT2(t)
		f.mu.Lock()
		f.ignoresEsc = true
		f.mu.Unlock()
		if _, err := dropT2(r, "d1", false, true); err != nil {
			t.Fatal(err)
		}
		r.waitFor("the stop's needs-you", func() bool { return r.heldWhy(p, "T-2") != "" })
		if why := r.heldWhy(p, "T-2"); !strings.Contains(why, "pj-l-02 still working") ||
			!strings.HasSuffix(why, "; obor drop T-2 --force stops it") {
			t.Errorf("needs-you %q; want the refusal naming obor drop --force", why)
		}
		if reply, err := dropT2(r, "d2", true, true); err != nil || !strings.HasPrefix(reply, "DROPPING T-2") {
			t.Fatalf("drop --force: %q, %v", reply, err)
		}
		r.waitFor("dropped", func() bool { return r.state("pj", "T-2") == stDropped })
		f.mu.Lock()
		defer f.mu.Unlock()
		if len(f.stops) != 2 || f.stops[1] != "pj-l-02 force=true" || len(f.frees) != 1 {
			t.Errorf("drop --force: stops %q, frees %+v; want the stop with force, then the lane freed", f.stops, f.frees)
		}
	})
}

// A cut git branch -D must not leave the lane free while the task works: the drop is recorded before its
// lane steps, so a lane.free that fails after the record is the loop's to retry, counted, and the third failure is one
// needs-you{lane.free failed} that obor resume retries; throughout the task is dropping (no task_done, no step of its
// old state) with its lane row its own, and its worker is stopped once. Never a half-done drop.
func TestDropFreeFailsAfterTheRecord(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		name  string
		fails int  // Free's failures before it frees
		held  bool // they reach tries: the owner's needs-you, obor resume retries
	}{{"one failure: retried by the loop", 1, false}, {"three failures: a needs-you, obor resume retries", tries, true}} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			r, p, f := laneT2(t)
			f.mu.Lock()
			f.freeErrs = c.fails
			f.mu.Unlock()
			if reply, err := dropT2(r, "d1", true, true); err != nil || !strings.HasPrefix(reply, "DROPPING T-2 from L-02") {
				t.Fatalf("drop: %q, %v", reply, err)
			}
			if c.held {
				r.waitFor("the free's needs-you", func() bool { return r.heldWhy(p, "T-2") != "" })
				if why := r.heldWhy(p, "T-2"); !strings.HasPrefix(why, "lane.free failed: ") ||
					!strings.Contains(why, "git branch -D task/T-2") ||
					!strings.HasSuffix(why, "— run the same obor drop again to finish it") {
					t.Errorf("needs-you %q; want lane.free failed with its error and the retry hint", why)
				}
				r.a.mu.Lock()
				st := p.folded.loop["T-2"].state
				r.a.mu.Unlock()
				if st != stDropping || r.row(p) != "T-2" || r.count("pj", "T-2", event.KindFact, event.StepDropped) != 0 {
					t.Errorf("held: T-2 %s, L-02's row %q; want dropping, the row its own, not dropped", st, r.row(p))
				}
				if reply, err := r.a.resume(wire.ResumeReq{Envelope: wire.Envelope{Root: r.roots["pj"], Agent: "lead",
					Role: "lead", CallID: "r1"},
					Task: "T-2"}); err != nil || reply != "RESUMED T-2: retry (lane.free failed)" {
					t.Fatalf("resume: %q, %v", reply, err)
				}
			}
			r.waitFor("dropped", func() bool { return r.state("pj", "T-2") == stDropped })
			if n := r.count("pj", "T-2", event.KindFact, event.StepFailed); n != c.fails {
				t.Errorf("%d failed facts, want %d (one per cut free)", n, c.fails)
			}
			f.mu.Lock()
			stops, frees := len(f.stops), len(f.frees)
			f.mu.Unlock()
			if stops != 1 || frees != 1 || r.row(p) != "" {
				t.Errorf("%d stops, %d frees, L-02's row %q; want the worker stopped once, the lane freed, no row", stops,
					frees, r.row(p))
			}
		})
	}
}

// needsYouT7 leaves pq's T-7 needs-you at attempt 1: a submission of it records attempt 2.
func needsYouT7(r *concRig) {
	r.add("pq", "T-7", event.StepSubmitted, event.KindFact, submitData{Lane: "L-07", Agent: "pq-l-07"})
	r.add("pq", "T-7", event.StepNeedsYou, event.KindFact, event.FactData{Lane: "L-07", Merge: true, Cause: causeGate, Reason: "owner"})
}

// raceDrop is the reviewer's probe (TestVerifyRV42DropAgainstDecidedSubmission): a submission of T-7 passes its guards
// and pauses before its append; drop decides against attempt 1 and pauses before its; the submission records
// submitted at attempt 2, then drop records dropped at attempt 1. The barrier before each append is the hook at the end
// of its decision (task_done's check, the drop's mark), outside mu: the append and its fold are one mu section.
func raceDrop(t *testing.T, r *concRig) (sub, drop callAnswer) {
	p := r.proj("pq")
	lane := port.Lane{Name: "L-07", Task: "T-7", Agent: "pq-l-07"}
	r.app("pq").mu.Lock()
	p.lanes = []port.Lane{lane}
	r.app("pq").mu.Unlock()
	r.app("pq").lanes = &fakeLanes{lanes: []port.Lane{lane}}
	subAt, dropAt, subGo, dropGo := make(chan struct{}), make(chan struct{}), make(chan struct{}), make(chan struct{})
	hook := func(point string) {
		switch point {
		case hookTaskDoneCheck:
			close(subAt)
			<-subGo
		case hookDropDecided:
			close(dropAt)
			<-dropGo
		}
	}
	r.app("pq").hooked.Store(&hook)
	defer func() {
		none := func(string) {}
		r.app("pq").hooked.Store(&none)
	}()
	submitted, dropped := make(chan callAnswer, 1), make(chan callAnswer, 1)
	go func() {
		reply, err := r.app("pq").taskDone(wire.TaskDoneReq{Envelope: wire.Envelope{Root: p.root, Agent: "pq-l-07",
			Role: "worker", CallID: "done"}, Task: "T-7", Lane: "L-07",
			Report: "Summary: fixed\nSelf-check: passed\nSkipped: none\nOpen questions: none"})
		submitted <- callAnswer{reply, err}
	}()
	wait := func(c chan struct{}, what string) {
		select {
		case <-c:
		case <-time.After(5 * time.Second):
			t.Fatalf("%s did not reach its append", what)
		}
	}
	wait(subAt, "the submission")
	go func() {
		reply, err := r.dropCall("pq", "T-7", "drop")
		dropped <- callAnswer{reply, err}
	}()
	wait(dropAt, "the drop")
	close(subGo)
	select {
	case sub = <-submitted:
	case <-time.After(5 * time.Second):
		t.Fatal("the submission did not record")
	}
	close(dropGo)
	select {
	case drop = <-dropped:
	case <-time.After(5 * time.Second):
		t.Fatal("the drop did not record")
	}
	return sub, drop
}

// Dropped ends the task: each case's first action after its setup is the named entry; then T-7 is in state want at
// attempt (live and after a replay of the log) and no drop mark is left.
func TestDroppedEndsTheTask(t *testing.T) {
	t.Parallel()
	dropOK := func(t *testing.T, a callAnswer) {
		if a.err != nil || !isDropped(a.reply, "T-7") {
			t.Fatalf("drop: %q, %v; want DROPPED T-7", a.reply, a.err)
		}
	}
	// dropAfterNext records the race's outcome by hand: submitted at attempt 2, then dropped at attempt 1.
	dropAfterNext := func(r *concRig) {
		r.addAt("pq", "T-7", 2, event.StepSubmitted, event.KindFact, submitData{Lane: "L-07", Agent: "pq-l-07"})
		if s := r.state("pq", "T-7"); s != stSubmitted {
			r.t.Fatalf("precondition: T-7 is %s after its next attempt's submission, want submitted", s)
		}
		r.addAt("pq", "T-7", 1, event.StepDropped, event.KindFact, event.FactData{Lane: "L-07", Merge: true, Reason: "why"})
	}
	for _, c := range []struct {
		name    string
		act     func(t *testing.T, r *concRig)
		want    string
		attempt int
	}{
		{"task_done decided before the drop", func(t *testing.T, r *concRig) {
			sub, drop := raceDrop(t, r)
			dropOK(t, drop)
			if sub.err != nil || sub.reply != taskDoneReply {
				t.Errorf("task_done: %q, %v; want submitted (recorded before the drop)", sub.reply, sub.err)
			}
			if n := r.count("pq", "T-7", event.KindFact, event.StepSubmitted); n != 2 {
				t.Errorf("precondition: %d submitted facts, want the race's second", n)
			}
		}, stDropped, 2},
		{"dropped at N after submitted at N+1", func(t *testing.T, r *concRig) { dropAfterNext(r) }, stDropped, 2},
		{"late gate result after dropped", func(t *testing.T, r *concRig) {
			dropAfterNext(r)
			r.addAt("pq", "T-7", 2, event.StepSubmit, event.KindResult, submitOut{SHA: "s7"})
			r.addAt("pq", "T-7", 2, event.StepGate, event.KindResult, gateOut{Outcome: outGreen, Candidate: port.Candidate{Task: "T-7",
				SHA: "c7", Target: "b", Patch: "p"}})
		}, stDropped, 2},
		{"late verdict result after dropped", func(t *testing.T, r *concRig) {
			dropAfterNext(r)
			r.addAt("pq", "T-7", 2, verdictStep(wire.MethodAccept, "v9"), event.KindResult, verdictResult{Job: "T-7/2/review",
				Reply: "ACCEPTED", Patch: "p"})
		}, stDropped, 2},
		{"dispatch after dropped", func(t *testing.T, r *concRig) {
			dropAfterNext(r)
			r.addAt("pq", "T-7", 3, event.StepDispatched, event.KindFact, dispatchData{Lane: "L-07", Agent: "pq-l-07",
				Target: "target", Branch: "task/T-7"})
		}, stDispatched, 3},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			r := newConcRig(t)
			needsYouT7(r)
			if s := r.state("pq", "T-7"); s != wire.PhaseNeedsYou {
				t.Fatalf("precondition: T-7 is %s, want needs-you", s)
			}
			c.act(t, r)
			p := r.proj("pq")
			r.app("pq").mu.Lock()
			live := *p.folded.loop["T-7"]
			ended := p.ended["T-7"]
			r.app("pq").mu.Unlock()
			if live.state != c.want || live.attempt != c.attempt {
				t.Errorf("live: %s at attempt %d; want %s at %d", live.state, live.attempt, c.want, c.attempt)
			}
			if ended {
				t.Error("the drop mark is left set")
			}
			r.app("pq").closeLog() // the first serve ends: the replay is the next one
			b := New(Options{Root: r.roots["pq"], Now: r.app("pq").opt.Now}, &fakePanes{}, &fakeLanes{})
			b.loadProject()
			if mt := b.p.folded.loop["T-7"]; mt == nil || mt.state != c.want || mt.attempt != c.attempt {
				t.Errorf("replay: %+v; want %s at attempt %d", mt, c.want, c.attempt)
			}
		})
	}
}

// obor review --after T-1, T-1 a spec stored and never dispatched, then dropped (the drop's record, with
// its rev): an open request ends with the line a dropped dispatched task gives, also after a replay, and a new request
// --after T-1 is refused as for a dropped task.
func TestReviewAfterASpecOnlyDrop(t *testing.T) {
	t.Parallel()
	r := newReviewRig(t)
	if _, _, err := store.SetSpec(r.p.log.DB(), "T-1", "# T-1 — Stored\n\nDifficulty: medium\n", "", "lead", r.now); err != nil {
		t.Fatal(err)
	}
	if got := r.mustCall("r1", []string{"T-1"}, "", false); got != "REVIEW REQUESTED (after T-1)" {
		t.Fatalf("reply %q", got)
	}
	r.pass()
	if r.p.folded.review == nil || len(r.agents.launches) != 0 {
		t.Fatalf("precondition: request %v, launches %+v; want it open, waiting on T-1", r.p.folded.review, r.agents.launches)
	}
	r.a.record(r.p, event.Record{Task: "T-1", Step: event.StepDropped, Kind: event.KindFact},
		event.FactData{Reason: "superseded", Merge: true, Rev: 1})
	r.pass()
	r.pass()
	if lines := r.leadLines(); len(lines) != 1 || lines[0] != "[obor] review request ended: T-1 dropped" {
		t.Errorf("lead lines %q, want the request ended on T-1's drop", lines)
	}
	if r.p.folded.review != nil || len(r.agents.launches) != 0 {
		t.Errorf("request %v, launches %+v; want ended, none", r.p.folded.review, r.agents.launches)
	}
	r.reload()
	r.pass()
	if lines := r.leadLines(); len(lines) != 1 || r.p.folded.review != nil {
		t.Errorf("after a replay: lines %q, request %v", lines, r.p.folded.review)
	}
	if _, err := r.call("r2", []string{"T-1"}, "", false); err == nil || !strings.Contains(err.Error(), "review: T-1 dropped already") {
		t.Errorf("a new request --after the dropped T-1: %v; want refused", err)
	}
}

// A step the merge loop selected starts only after one atomic check that its task/attempt
// is current and not ended, which also holds the task; drop never waits: under the same lock it is refused while a
// step holds the task, or marks the task ended before it records dropped.

// dropFx is a project's loop adapters over its concFakes that note every effect; the effect named block waits for
// unblock (blocked is closed first).
type dropFx struct {
	*concFakes
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

func (f *dropFx) Gate(ctx context.Context, in port.GateInput) (port.GateOutcome, error) {
	f.note("gate")
	return f.concFakes.Gate(ctx, in)
}

func (f *dropFx) Publish(ctx context.Context, c port.Candidate) (port.PublishOutcome, error) {
	f.note("publish")
	return f.concFakes.Publish(ctx, c)
}

func (f *dropFx) Submit(ctx context.Context, task, id, lane, summary string) (string, error) {
	f.note("submit")
	return f.concFakes.Submit(ctx, task, id, lane, summary)
}

func (f *dropFx) Launch(ctx context.Context, l port.Launch) (port.Started, error) {
	f.note("restart")
	return f.concFakes.Launch(ctx, l)
}

func (f *dropFx) Stop(ctx context.Context, agent, pane string, force bool) (string, error) {
	f.note("stop")
	return f.concFakes.Stop(ctx, agent, pane, force)
}

func (f *dropFx) Type(ctx context.Context, agent, text string, clear bool) (bool, error) {
	f.note("prompt")
	return true, nil
}

func (f *dropFx) Prepare(ctx context.Context, in port.PrepareInput) (port.Prepared, error) {
	f.note("prepare")
	return f.concFakes.Prepare(ctx, in)
}

func (f *dropFx) Unpin(ctx context.Context, task string) error {
	f.note("unpin")
	return nil
}

func (f *dropFx) AfterMerge(ctx context.Context, c port.Candidate) (string, error) {
	f.note("after")
	return "ran", nil
}

// loop is fx as a project's adapters (the dispatch's too).
func (f *dropFx) loop(kit string) *Loop {
	return &Loop{Workflow: WorkflowOf(f), Agents: f, Typer: f, Specs: f, Kit: kit}
}

// dropStartCases is one per selected step kind: the setup leaves task of project proj with that
// step next, and effect is what the step does first.
var dropStartCases = []struct {
	name, proj, task, effect string
	setup                    func(r *concRig)
}{
	{"lane gate", "pj", "T-1", "gate", func(*concRig) {}},
	{"publish", "pr", "T-4", "publish", acceptT4},
	{"moved gate (gate.2)", "pr", "T-4", "gate", func(r *concRig) {
		acceptT4(r)
		r.add("pr", "T-4", event.StepPublish, event.KindResult, gateOut{Outcome: outMoved, Line: "moved"})
	}},
	{"numbered publish (publish.2)", "pr", "T-4", "publish", func(r *concRig) {
		acceptT4(r)
		r.add("pr", "T-4", event.StepPublish, event.KindResult, gateOut{Outcome: outMoved, Line: "moved"})
		r.add("pr", "T-4", nth(event.StepGate, 2), event.KindResult, gateOut{Outcome: outGreen, Candidate: port.Candidate{Task: "T-4",
			SHA: "c2", Target: "b", Patch: "p"}})
		r.add("pr", "T-4", event.StepKept, event.KindFact, JobData{Patch: "p"})
	}},
	{"resolved gate", "pq", "T-3", "gate", func(r *concRig) {
		r.add("pq", "T-3", event.StepGate, event.KindResult, gateOut{Outcome: outConflict, Candidate: port.Candidate{Task: "T-3", SHA: "c",
			Target: "b", Patch: "p"}})
		r.add("pq", "T-3", event.StepResolve, event.KindIntent, promptData{Agent: "pq-int", Job: "T-3/1/resolve", Patch: "p"})
		r.add("pq", "T-3", event.StepResolve, event.KindResult, "sent")
		r.add("pq", "T-3", verdictStep(wire.MethodResolved, "r1"), event.KindResult, verdictResult{Job: "T-3/1/resolve", SHA: "m"})
	}},
	{"submit", "pq", "T-6", "submit", func(r *concRig) {
		r.add("pq", "T-6", event.StepSubmitted, event.KindFact, submitData{Lane: "L-06"})
	}},
	{"review", "pq", "T-3", "restart", func(r *concRig) {
		r.add("pq", "T-3", event.StepGate, event.KindResult, gateOut{Outcome: outGreen, Candidate: port.Candidate{Task: "T-3", SHA: "c",
			Target: "b", Patch: "p"}})
	}},
	{"resolve", "pq", "T-3", "restart", func(r *concRig) {
		r.add("pq", "T-3", event.StepGate, event.KindResult, gateOut{Outcome: outConflict, Candidate: port.Candidate{Task: "T-3", SHA: "c",
			Target: "b", Patch: "p"}})
	}},
	{"prepare", "pj", "T-2", "prepare", dispatchT2},
	{"start", "pj", "T-2", "restart", func(r *concRig) {
		dispatchT2(r)
		r.add("pj", "T-2", event.StepPrepare, event.KindResult, prepareOut{Base: "b"})
	}},
	{"work", "pj", "T-2", "prompt", func(r *concRig) {
		dispatchT2(r)
		r.add("pj", "T-2", event.StepPrepare, event.KindResult, prepareOut{Base: "b"})
		r.add("pj", "T-2", event.StepStartWork, event.KindResult, startOut{Pane: "w1:p2"})
	}},
	{"fix", "pq", "T-7", "prompt", func(r *concRig) {
		r.add("pq", "T-7", event.StepSubmitted, event.KindFact, submitData{Lane: "L-07", Agent: "pq-l-07"})
		r.add("pq", "T-7", event.StepSendback, event.KindFact, noteData{Note: "fix it"})
		r.worker("pq", "w2:p7", "pq-l-07") // for its fix job's wake (an agent no pane proves gets none)
	}},
}

func acceptT4(r *concRig) {
	if _, err := r.app("pr").verdict(context.Background(), wire.VerdictReq{Envelope: wire.Envelope{Root: r.roots["pr"],
		Role: "integrator", Agent: "pr-int", CallID: "accept"}, Verb: wire.MethodAccept, Task: "T-4",
		Job: "T-4/1/review", Patch: "p"}); err != nil {
		r.t.Fatal(err)
	}
}

// Drop first (the reviewer's TestRV42DropSelectedScratchStep, every kind): a step selected before a successful drop
// does not start once DROPPED is returned: no effect, the task stays dropped. Control: the same step, not dropped,
// runs its effect.
func TestDropSelectedStep(t *testing.T) {
	t.Parallel()
	for _, c := range dropStartCases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			for _, dropped := range []bool{false, true} {
				r := newConcRig(t)
				r.fakes["pj"].hold = "" // a gate that runs returns at once
				c.setup(r)
				p := r.proj(c.proj)
				ap := r.app(c.proj)
				fx := &dropFx{concFakes: r.fakes[c.proj]}
				ap.mu.Lock()
				selected := ap.next(p, p.folded.loop[c.task])
				ap.mu.Unlock()
				if selected == nil {
					t.Fatalf("precondition: %s has no step selected", c.task)
				}
				if dropped {
					if reply, err := r.dropCall(c.proj, c.task, "drop"); err != nil || !isDropped(reply, c.task) {
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
				case dropped && !dropGone(r.state(c.proj, c.task)):
					t.Errorf("after DROPPED the task is %s", r.state(c.proj, c.task))
				}
			}
		})
	}
}

// Step first, a scratch step: a gate or publish started (it holds the scratch) refuses the drop; its result folds
// and the task goes on. Not dropped: no dropped fact.
func TestDropAfterScratchStepStarted(t *testing.T) {
	t.Parallel()
	for _, c := range dropStartCases[:2] {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			r := newConcRig(t)
			c.setup(r)
			f := r.fakes[c.proj]
			f.hold, f.holdPub = "T-1", "T-4"
			p := r.proj(c.proj)
			ap := r.app(c.proj)
			ap.mu.Lock()
			selected := ap.next(p, p.folded.loop[c.task])
			ap.mu.Unlock()
			fx := &dropFx{concFakes: f}
			selected(context.Background(), fx.loop(r.kit))
			<-f.entered // the effect runs
			if _, err := r.dropCall(c.proj, c.task, "drop"); err == nil ||
				!strings.Contains(err.Error(), c.task+" has "+c.effect+" running; retry when it settles") {
				t.Errorf("drop while the %s runs: %v", c.effect, err)
			}
			close(f.release)
			ap.effects.Wait()
			if r.count(c.proj, c.task, event.KindResult, c.effect) != 1 {
				t.Errorf("the %s's result did not fold", c.effect)
			}
			if r.count(c.proj, c.task, event.KindFact, event.StepDropped) != 0 || dropGone(r.state(c.proj, c.task)) {
				t.Error("a refused drop dropped the task")
			}
		})
	}
}

// The step's table: a drop while a selected step holds the task (gate, publish, start, a prompt) is
// refused at once, naming the step, and changes nothing; once the step has returned, a drop goes through.
func TestDropRefusedWhileStepHeld(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		name string
		i    int
	}{{"gate", 0}, {"publish", 1}, {"start", 9}, {"start (review)", 6}} {
		c := dropStartCases[c.i]
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			r := newConcRig(t)
			r.fakes["pj"].hold = ""
			c.setup(r)
			p := r.proj(c.proj)
			ap := r.app(c.proj)
			fx := &dropFx{concFakes: r.fakes[c.proj], block: c.effect, blocked: make(chan struct{}),
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
				!strings.Contains(a.err.Error(), "running; retry when it settles") &&
					!strings.Contains(a.err.Error(), "pending; retry when it settles") {
				t.Errorf("drop while the %s runs: %q, %v; want refused, naming the step", c.effect, a.reply, a.err)
			}
			if r.count(c.proj, c.task, event.KindFact, event.StepDropped) != 0 || dropGone(r.state(c.proj, c.task)) ||
				r.ended(c.proj, c.task) {
				t.Errorf("a refused drop changed the task: state %s, ended mark %v", r.state(c.proj, c.task),
					r.ended(c.proj, c.task))
			}
			close(fx.unblock)
			<-stepped
			ap.effects.Wait()
			if a := r.dropWithin(c.proj, c.task, "d2"); a.err != nil || !isDropped(a.reply, c.task) {
				t.Errorf("drop once the step returned: %q, %v", a.reply, a.err)
			}
		})
	}
}

// The reviewer's probe, rewritten to its outcome: a drop during a held integrator start whose agent
// fetches its job early is refused at once, and the fetch answers: no lock cycle.
func TestDropDuringEarlyFetch(t *testing.T) {
	t.Parallel()
	r := newConcRig(t)
	p := r.proj("pj")
	r.add("pj", "T-1", event.StepGate, event.KindIntent, gateIn{Kind: port.InputLane, Lane: "L-01", Input: "s1"})
	r.add("pj", "T-1", event.StepGate, event.KindResult, gateOut{Outcome: outGreen, Candidate: port.Candidate{Task: "T-1", SHA: "c",
		Target: "b", Patch: "p"}})
	delete(r.a.obs, "w1:p9") // no integrator runs: the review job's step starts it
	entered, fetch := make(chan struct{}), make(chan struct{})
	fetched, stepped := make(chan callAnswer, 1), make(chan struct{})
	f := &startEffects{concFakes: r.fakes["pj"]}
	f.start = func(port.Launch) {
		close(entered)
		<-fetch
		reply, err := r.a.jobNext(wire.JobNextReq{Envelope: wire.Envelope{Root: p.root, Agent: p.integrator,
			Role: "integrator", CallID: "early"}})
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
	if a.err == nil || !strings.Contains(a.err.Error(), "running; retry when it settles") {
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
	if r.count("pj", "T-1", event.KindFact, event.StepDropped) != 0 || dropGone(r.state("pj", "T-1")) {
		t.Error("a refused drop dropped the task")
	}
}

// submitT7 is pq-l-07's task_done for T-7 (lane L-07 holds it in the lane table).
func submitT7(r *concRig) (string, error) {
	r.app("pq").lanes = &fakeLanes{lanes: []port.Lane{{Name: "L-07", Task: "T-7", Agent: "pq-l-07"}}}
	return r.app("pq").taskDone(wire.TaskDoneReq{Envelope: wire.Envelope{Root: r.proj("pq").root, Agent: "pq-l-07",
		Role: "worker", CallID: "done"}, Task: "T-7", Lane: "L-07",
		Report: "Summary: x\nSelf-check: x\nSkipped: x\nOpen questions: x"})
}

// Submission vs drop (both orders, no shared verb lock): while a drop is being recorded (the task marked ended) the
// write and its fold hold mu, so a submission begun meanwhile decides on the dropped task and records nothing;
// one recorded before the drop leaves the drop to go through. A submitted fact after dropped, at the same or the next
// attempt, leaves the task dropped (live and on replay).
func TestDropAndSubmission(t *testing.T) {
	t.Parallel()
	setup := func(t *testing.T) *concRig {
		r := newConcRig(t)
		r.add("pq", "T-7", event.StepSubmitted, event.KindFact, submitData{Lane: "L-07", Agent: "pq-l-07"})
		r.add("pq", "T-7", event.StepSendback, event.KindFact, noteData{Note: "fix it"})
		return r
	}
	t.Run("control: task_done submits", func(t *testing.T) {
		t.Parallel()
		r := setup(t)
		if reply, err := submitT7(r); err != nil || reply != taskDoneReply {
			t.Fatalf("task_done: %q, %v", reply, err)
		}
		if r.count("pq", "T-7", event.KindFact, event.StepSubmitted) != 2 {
			t.Fatalf("task_done did not submit T-7: %d submitted facts", r.count("pq", "T-7", event.KindFact, event.StepSubmitted))
		}
	})
	t.Run("drop first", func(t *testing.T) {
		t.Parallel()
		r := setup(t)
		a, p := r.app("pq"), r.proj("pq")
		writing, release := r.pauseDrop("pq")
		dropped := make(chan callAnswer, 1)
		go func() {
			reply, err := r.dropCall("pq", "T-7", "drop")
			dropped <- callAnswer{reply, err}
		}()
		<-writing
		if a.mu.TryLock() {
			a.mu.Unlock()
			t.Error("dropped is written outside mu: a submission could decide before its fold")
		}
		if !p.ended["T-7"] { // the drop's goroutine holds mu, paused in its write (ordered by writing)
			t.Error("the ended mark is not set while dropped is written")
		}
		done := make(chan error, 1)
		go func() { _, err := submitT7(r); done <- err }()
		release()
		var subErr error
		select {
		case subErr = <-done:
		case <-time.After(5 * time.Second):
			t.Fatal("task_done never answered after the drop's record")
		}
		if subErr == nil || !strings.Contains(subErr.Error(), "T-7 is dropped, not open for a submission") {
			t.Errorf("task_done during the drop: %v; want refused by the dropped task", subErr)
		}
		if a := <-dropped; a.err != nil || !isDropped(a.reply, "T-7") {
			t.Fatalf("drop: %q, %v", a.reply, a.err)
		}
		if n := r.count("pq", "T-7", event.KindFact, event.StepSubmitted); n != 1 {
			t.Errorf("submitted facts %d; want only the first", n)
		}
		if r.state("pq", "T-7") != stDropped || r.ended("pq", "T-7") {
			t.Errorf("after the drop: %s, ended mark %v", r.state("pq", "T-7"), r.ended("pq", "T-7"))
		}
	})
	t.Run("submission first", func(t *testing.T) {
		t.Parallel()
		r := setup(t)
		if _, err := submitT7(r); err != nil {
			t.Fatal(err)
		}
		if r.state("pq", "T-7") != stSubmitted {
			t.Fatalf("precondition: T-7 is %s, want submitted", r.state("pq", "T-7"))
		}
		if a := r.dropWithin("pq", "T-7", "drop"); a.err != nil || !isDropped(a.reply, "T-7") {
			t.Fatalf("drop after the submission: %q, %v", a.reply, a.err)
		}
		p := r.proj("pq")
		r.app("pq").mu.Lock()
		attempt := p.folded.loop["T-7"].attempt
		r.app("pq").mu.Unlock()
		for _, n := range []int{attempt, attempt + 1} { // decided before the drop, recorded after
			rec, err := p.log.Append(event.Record{Task: "T-7", Attempt: n, Step: event.StepSubmitted, Kind: event.KindFact,
				Data: []byte(`{"lane":"L-07","agent":"pq-l-07"}`)}, r.app("pq").opt.Now())
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
	t.Parallel()
	r := newConcRig(t)
	p := r.proj("pq")
	var during struct{ ended, open bool }
	p.log.Refuse = func(rec event.Record) error {
		if rec.Step != event.StepDropped {
			return nil
		}
		// The write runs in the drop's mu section with its fold: no step's start check (begin, under mu) runs
		// meanwhile.
		during.ended = p.ended["T-3"]
		if during.open = r.app("pq").mu.TryLock(); during.open {
			r.app("pq").mu.Unlock()
		}
		return errors.New("disk full")
	}
	a := r.dropWithin("pq", "T-3", "d1")
	if a.err == nil || !strings.Contains(a.err.Error(), "the event log of pq refused it") {
		t.Errorf("drop with a failing write: %q, %v", a.reply, a.err)
	}
	if !during.ended || during.open {
		t.Errorf("while dropped was written: ended mark %v, mu free for a step's start %v; want marked, held",
			during.ended, during.open)
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
	r.app("pq").release(p, "T-3", mt.stepName())
	p.log.Refuse = nil
	if a := r.dropWithin("pq", "T-3", "d2"); a.err != nil || !isDropped(a.reply, "T-3") {
		t.Errorf("a later drop: %q, %v", a.reply, a.err)
	}
}

// The reviewer's early-verdict probe to the fixed outcome: the job is open before its
// start, so an integrator that fetches its job while it starts can answer it then; the start's result after that is
// its delivery only and keeps the state the verdict set. Fresh review and resolve jobs, every method, and the
// numbered and recovery jobs (review.2 / resolve.2 after job.lost, the owner's reprompt).
func TestEarlyVerdict(t *testing.T) {
	t.Parallel()
	gate := func(outcome string) func(r *concRig) {
		return func(r *concRig) {
			r.add("pj", "T-1", event.StepGate, event.KindIntent, gateIn{Kind: port.InputLane, Lane: "L-01", Input: "s1"})
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
			state: wire.PhaseNeedsYou, attempt: 1},
		{name: "resolve resolved", setup: []func(*concRig){gate(outConflict)}, job: "resolve", method: wire.MethodResolved,
			state: stResolved, attempt: 1},
		{name: "resolve reject", setup: []func(*concRig){gate(outConflict)}, job: "resolve", method: wire.MethodReject,
			state: "fail", attempt: 1}, // a due block: its needs-you is recorded next,
		{name: "resolve hold", setup: []func(*concRig){gate(outConflict)}, job: "resolve", method: wire.MethodHold,
			state: wire.PhaseNeedsYou, attempt: 1},
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
			f := &startJobFakes{concFakes: r.fakes["pj"]}
			var fetched wire.JobReply
			var verdictErr error
			var during mtask
			f.start = func(port.Launch) {
				if done(p, key) {
					t.Errorf("precondition: %s has its result before the start returned", job)
				}
				raw, err := r.a.jobNext(wire.JobNextReq{Envelope: wire.Envelope{Root: p.root, Agent: p.integrator,
					Role: "integrator", CallID: "early"}})
				if err != nil {
					t.Fatal(err)
				}
				_ = json.Unmarshal([]byte(raw), &fetched)
				_, verdictErr = r.a.verdict(context.Background(), wire.VerdictReq{Verb: row.method,
					Envelope: wire.Envelope{Root: p.root, Agent: p.integrator, Role: "integrator", CallID: "answer"},
					Task:     fetched.Task, Job: fetched.Job, Patch: "p", Note: "why"})
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
			same(t, during.shown()+" "+strconv.Itoa(during.attempt), row.state+" "+strconv.Itoa(row.attempt), "state after the verdict")
			r.a.mu.Lock()
			after := *p.folded.loop["T-1"]
			r.a.mu.Unlock()
			same(t, after.shown()+" "+strconv.Itoa(after.attempt), row.state+" "+strconv.Itoa(row.attempt), "state after the start")
			same(t, resultOf(p, key), `"sent (start prompt)"`, row.job+" result")
			same(t, len(f.typed), 0, "Prompter calls")
			same(t, deliveredOf(r, job), 1, "delivered facts of "+job)
			// the job's intent precedes its start's: open before the wake
			if j, s := seqOf(r, "intent", row.job), seqOf(r, "intent", stepStart+row.job); j == 0 || s == 0 || j > s {
				t.Errorf("intent seq: %s %d, %s %d; want the job's first", row.job, j, stepStart+row.job, s)
			}
			// the job is answered: the same verdict again is refused (no job open, or another attempt)
			if _, err := r.a.verdict(context.Background(), wire.VerdictReq{Verb: row.method,
				Envelope: wire.Envelope{Root: p.root, Agent: p.integrator, Role: "integrator", CallID: "later"},
				Task:     "T-1", Job: job, Patch: "p", Note: "why"}); err == nil &&
				row.method != wire.MethodHold {
				t.Errorf("a second %s on %s was taken", row.method, job)
			}
		})
	}
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
