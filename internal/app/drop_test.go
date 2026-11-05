package app

import (
	"context"
	"obor/internal/app/event"
	"obor/internal/app/port"
	"obor/internal/app/wire"
	"strings"
	"testing"

	"obor/internal/store"
)

// isDropped: reply is a drop's of task: DROPPED <task> (no lane), or DROPPING <task> from <lane> (… : its lane's, T-852).
func isDropped(reply, task string) bool {
	return reply == dropReply(task) || strings.HasPrefix(reply, "DROPPING "+task+" from ")
}

// dropGone: st is a dropped task's, or a dropping one's (its lane's steps to come: T-852).
func dropGone(st string) bool { return st == stDropped || st == stDropping }

// dropCall is the lead's obor drop of task in project name.
func (r *concRig) dropCall(name, task, id string) (string, error) {
	return r.app(name).drop(wire.Call{Root: r.roots[name], Agent: "lead", Role: "lead", CallID: id, Task: task, Note: "abandoned"})
}

// obor drop of a task serve holds (T-388): a needs-you with no job open ends in dropped, which survives a replay; the
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
		_, err := r.app("pq").drop(wire.Call{Root: r.roots["pq"], Agent: "pq-x", Role: role, CallID: "w-" + role, Task: "T-3", Note: "why"})
		if err == nil || !strings.Contains(err.Error(), "the lead or the owner drops a task") {
			t.Errorf("drop by %s: %v", role, err)
		}
	}
	if _, err := r.app("pq").drop(wire.Call{Root: r.roots["pq"], Role: "lead", CallID: "nw", Task: "T-3"}); err == nil ||
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
	if _, err := r.app("pr").verdict(context.Background(), wire.MethodAccept, wire.Call{Root: r.roots["pr"], Task: "T-4", Role: "integrator",
		Agent: "pr-int", CallID: "v1", Job: "T-4/1/review", Patch: "p"}); err != nil {
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
	// its lane's row holds it: freed by the merge loop's lane.free (T-775, T-852; the lane names no worker to stop)
	if err != nil || got != "DROPPING T-1 from L-01 (abandoned)" {
		t.Fatalf("drop with the review job open: %q, %v", got, err)
	}
	r.waitFor("T-1 dropped", func() bool { return r.state("pj", "T-1") == stDropped })
	_, err = r.a.verdict(context.Background(), wire.MethodAccept, wire.Call{Root: r.roots["pj"], Task: "T-1", Role: "integrator",
		Agent: "pj-int", CallID: "v2", Job: "T-1/1/review", Patch: "p"})
	if err == nil || !strings.Contains(err.Error(), "superseded: T-1 was dropped") {
		t.Errorf("accept after the drop: %v", err)
	}
	if r.count("pj", "T-1", event.KindFact, event.StepMerged) != 0 || r.fakes["pj"].count(r.fakes["pj"].pubs, "T-1") != 0 {
		t.Error("a dropped task was published")
	}
}
func (r *concRig) entered2(name string) string { return r.entered(name) }

// laneT2 is the rig with pj's T-2 dispatched onto L-02 and its lane prepared (L-02's row holds it), the merge loops
// running.
func laneT2(t *testing.T) (*concRig, *proj, *concScratch) {
	t.Helper()
	r := newConcRig(t)
	dispatchT2(r)
	r.add("pj", "T-2", event.StepPrepare, event.KindResult, prepareOut{Base: "target", Head: "target"})
	r.run()
	t.Cleanup(r.stop)
	return r, r.proj("pj"), r.fakes["pj"]
}

// dropT2 is the lead's obor drop of pj's T-2 (why: moving on) with --discard and force as given, once no step of its
// holds it (the running loop may be in T-2's dispatch steps: a drop then is refused, "retry when it settles").
func dropT2(r *concRig, id string, force, discard bool) (reply string, err error) {
	r.waitFor("a drop of T-2 not refused as busy", func() bool {
		reply, err = r.a.drop(wire.Call{Root: r.roots["pj"], Agent: "lead", Role: "lead", CallID: id, Task: "T-2",
			Note: "moving on", Discard: discard, Force: force})
		return err == nil || !strings.Contains(err.Error(), "retry when it settles")
	})
	return reply, err
}

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

// T-439, T-775, T-852: obor drop names the task; serve knows its lane (its row holds it) and answers DROPPING at once
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

// T-773, T-852: the drop's stop of the task's worker, without --force too; a worker still working after Escape is
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
	// RV-98 #2: any stop that fails holds the drop (the worker may still run in the lane the next task gets): a
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

// T-852 rev 2 (11-05: a cut git branch -D left the lane free and the task working): the drop is recorded before its
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
					!strings.Contains(why, "git branch -D task/T-2") {
					t.Errorf("needs-you %q; want lane.free failed with its error", why)
				}
				r.a.mu.Lock()
				st := p.folded.loop["T-2"].state
				r.a.mu.Unlock()
				if st != stDropping || r.row(p) != "T-2" || r.count("pj", "T-2", event.KindFact, event.StepDropped) != 0 {
					t.Errorf("held: T-2 %s, L-02's row %q; want dropping, the row its own, not dropped", st, r.row(p))
				}
				if reply, err := r.a.resume(wire.Call{Root: r.roots["pj"], Agent: "lead", Role: "lead", CallID: "r1",
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
