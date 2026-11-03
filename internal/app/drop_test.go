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

// isDropped: reply is a drop's of task: DROPPED <task>, or its lane's lines then DROPPED <task> from <lane> (<why>).
func isDropped(reply, task string) bool {
	lines := strings.Split(reply, "\n")
	last := lines[len(lines)-1]
	return last == dropReply(task) || strings.HasPrefix(last, dropReply(task)+" from ")
}

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
	r.waitFor("T-3 needs-you", func() bool { return r.state("pq", "T-3") == stNeedsYou })
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
	if r.state("pq", "T-3") != stNeedsYou {
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
		t.Errorf("obor serve status shows T-3 %q; want dropped", shown)
	}
	if n := r.fakes["pq"].count(r.fakes["pq"].gates, "T-3"); n != 0 {
		t.Errorf("a dropped task gated %d times", n)
	}

	// A replay of the log keeps it ended, and the stored reply with it.
	r.app("pq").closeLog() // the first serve ends: the replay is the next one
	b := New(Options{Root: r.roots["pq"], Now: r.a.opt.Now}, &fakePanes{}, &fakeLanes{})
	b.loadProject()
	p := b.p
	if mt := p.folded.loop["T-3"]; mt == nil || mt.state != stDropped || mt.needs.Seq != 0 {
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
	if r.state("pj", "T-1") == stDropped {
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
	if err != nil || !strings.HasSuffix(got, "DROPPED T-1 from L-01 (abandoned)") { // its lane's row holds it: freed (T-775)
		t.Fatalf("drop with the review job open: %q, %v", got, err)
	}
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

// T-439, T-775: obor drop names the task; serve knows its lane (its row holds the task), stops its worker, frees the
// lane (the lane adapter's Free with --discard), then records dropped: the lane row clears. A refused Free ends the drop
// with nothing recorded: the task and its row as they were, its worker stopped first (T-773), the refusal says so.
func TestDropByTaskFreesItsLane(t *testing.T) {
	t.Parallel()
	r := newConcRig(t)
	dispatchT2(r)
	r.add("pj", "T-2", event.StepPrepare, event.KindResult, prepareOut{Base: "target", Head: "target"})
	p, f := r.proj("pj"), r.fakes["pj"]
	row := func() string {
		r.a.mu.Lock()
		defer r.a.mu.Unlock()
		return p.laneTask("L-02")
	}
	if row() != "T-2" {
		t.Fatalf("precondition: L-02's row holds %q, want T-2", row())
	}
	drop := func(id string) (string, error) {
		return r.a.drop(wire.Call{Root: r.roots["pj"], Agent: "lead", Role: "lead", CallID: id, Task: "T-2",
			Note: "abandoned", Discard: true, Force: true})
	}
	f.refuse = "lane L-02 is dirty (--discard to drop its uncommitted changes)"
	if _, err := drop("d1"); err == nil || err.Error() != "drop: "+f.refuse+"\nSTOPPED" {
		t.Fatalf("refused Free: %v", err)
	}
	r.a.mu.Lock()
	ended := p.ended["T-2"]
	r.a.mu.Unlock()
	if st := r.state("pj", "T-2"); st == stDropped || ended || row() != "T-2" {
		t.Errorf("a refused drop left T-2 %s (ended mark %t), L-02's row %q", st, ended, row())
	}
	f.refuse = ""
	reply, err := drop("d2")
	if want := "STOPPED\ndiscarded in L-02\nDROPPED T-2 from L-02 (abandoned)"; err != nil || reply != want {
		t.Fatalf("drop: %q, %v; want %q", reply, err, want)
	}
	if len(f.frees) != 1 || f.frees[0] != (port.FreeInput{Lane: "L-02", Branch: "task/T-2", Target: "target", Discard: true}) {
		t.Errorf("Free calls %+v; want one for L-02's task/T-2 at the target, discarding", f.frees)
	}
	if len(f.stops) != 2 || f.stops[1] != "pj-l-02 force=true" {
		t.Errorf("stops %q; want pj-l-02 with force, by each drop", f.stops)
	}
	if st := r.state("pj", "T-2"); st != stDropped || row() != "" {
		t.Errorf("after DROPPED: T-2 %s, L-02's row %q; want dropped, no row", st, row())
	}
	rows, err := store.ReadLanesAt(r.roots["pj"])
	if _, ok := rows["L-02"]; ok || err != nil {
		t.Errorf("the lanes table still has L-02: %+v, %v", rows, err)
	}
}

// T-773: obor drop <T-xx> stops the task's worker as part of the drop, without --force too; a worker still working after Escape is stopped only with --force: without it the drop is
// refused, nothing freed or recorded, and the task stays open.
func TestDropStopsTheWorker(t *testing.T) {
	t.Parallel()
	setup := func(t *testing.T) (*concRig, *proj, *concScratch, func(id string, force bool) (string, error)) {
		t.Helper()
		r := newConcRig(t)
		dispatchT2(r)
		r.add("pj", "T-2", event.StepPrepare, event.KindResult, prepareOut{Base: "target", Head: "target"})
		p, f := r.proj("pj"), r.fakes["pj"]
		return r, p, f, func(id string, force bool) (string, error) {
			return r.a.drop(wire.Call{Root: r.roots["pj"], Agent: "lead", Role: "lead", CallID: id, Task: "T-2",
				Note: "moving on", Discard: true, Force: force})
		}
	}
	t.Run("no --force: the worker stopped, the lane freed, dropped", func(t *testing.T) {
		t.Parallel()
		r, _, f, drop := setup(t)
		if len(f.stops) != 0 || r.state("pj", "T-2") == stDropped {
			t.Fatalf("precondition: T-2 open, no stop: %q", f.stops)
		}
		reply, err := drop("d1", false)
		if want := "STOPPED\ndiscarded in L-02\nDROPPED T-2 from L-02 (moving on)"; err != nil || reply != want {
			t.Fatalf("drop: %q, %v; want %q", reply, err, want)
		}
		if len(f.stops) != 1 || f.stops[0] != "pj-l-02 force=false" {
			t.Errorf("stops %q; want pj-l-02 once, without force", f.stops)
		}
		if st := r.state("pj", "T-2"); st != stDropped {
			t.Errorf("T-2 is %s after the drop, want dropped", st)
		}
	})
	t.Run("a worker that ignores Escape: refused without --force, stopped and dropped with it", func(t *testing.T) {
		t.Parallel()
		r, p, f, drop := setup(t)
		f.mu.Lock()
		f.ignoresEsc = true
		f.mu.Unlock()
		_, err := drop("d1", false)
		if err == nil || !strings.Contains(err.Error(), "worker of L-02 not stopped: pj-l-02 still working") ||
			!strings.HasSuffix(err.Error(), "(--force stops it) (nothing dropped)") {
			t.Fatalf("drop without --force of a worker ignoring Escape: %v; want refused naming --force", err)
		}
		r.a.mu.Lock()
		ended := p.ended["T-2"]
		r.a.mu.Unlock()
		if len(f.stops) != 1 || len(f.frees) != 0 || ended || r.state("pj", "T-2") == stDropped {
			t.Errorf("the refused drop: stops %q, frees %+v, ended %t, T-2 %s; want one stop tried, nothing freed or "+
				"dropped", f.stops, f.frees, ended, r.state("pj", "T-2"))
		}
		reply, err := drop("d2", true)
		if err != nil || !strings.HasSuffix(reply, "DROPPED T-2 from L-02 (moving on)") {
			t.Fatalf("drop --force: %q, %v", reply, err)
		}
		if len(f.stops) != 2 || f.stops[1] != "pj-l-02 force=true" || len(f.frees) != 1 {
			t.Errorf("drop --force: stops %q, frees %+v; want the stop with force, then the lane freed", f.stops, f.frees)
		}
	})
}
