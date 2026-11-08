package app_test

import (
	"context"
	"strings"
	"testing"

	"obor/internal/app/event"
	"obor/internal/app/wire"
)

// dropSpec is the lead's obor drop of task (a spec stored and never dispatched) with why; fatal unless DROPPED.
func (r *loopRig) dropSpec(callID, task, why string) {
	r.t.Helper()
	reply, err := wire.Verdict(context.Background(), r.sock, wire.MethodDrop, wire.Call{Root: r.p.Root, Role: "lead",
		CallID: callID, Task: task, Note: why})
	if err != nil || reply != "DROPPED "+task {
		r.t.Fatalf("drop of %s: %q, %v; want DROPPED %s", task, reply, err, task)
	}
}

// T-1016, T-1026: obor drop of a task with a stored spec and no loop record records the drop at the spec's rev (its why
// with it) and is the task's outcome: serve lists T-1 dropped (ended), a dispatch of it is refused naming the drop, and
// a restart's replay folds the same; a new obor spec set (a later rev) revives it: not listed, dispatchable. A task
// with neither a spec nor a loop record is refused as before.
func TestDropOfASpecOnlyTaskThenDispatchUntilANewRev(t *testing.T) {
	t.Parallel()
	r := newLoopRig(t, dispatching())
	r.start()
	if got := r.setSpec("s1", "# T-1 — Stored\n\nDifficulty: medium\n"); got != "SET T-1 rev 1" {
		t.Fatalf("spec set: %q, want SET T-1 rev 1", got)
	}
	if st := r.taskStatus("T-1"); st.Task != "" {
		t.Fatalf("precondition: T-1 listed before the drop: %+v", st)
	}
	if _, err := wire.Verdict(context.Background(), r.sock, wire.MethodDrop, wire.Call{Root: r.p.Root, Role: "lead",
		CallID: "x1", Task: "T-9", Note: "superseded on 11-07"}); err == nil ||
		!strings.Contains(err.Error(), "T-9 is not open in obor serve") {
		t.Errorf("drop of an unknown task: %v; want refused as before", err)
	}
	r.dropSpec("x2", "T-1", "superseded on 11-07")
	if d := r.factData("T-1", event.StepDropped); d["rev"] != float64(1) || d["reason"] != "superseded on 11-07" {
		t.Errorf("the dropped fact %+v, want rev 1 and its why", d)
	}
	refused := "T-1's spec rev 1 was dropped (superseded on 11-07): obor spec set T-1"
	for _, when := range []string{"live", "after a restart"} {
		if when != "live" {
			r.stop()
			r.start()
		}
		if st := r.taskStatus("T-1"); st.State != "dropped" || !st.Ended {
			t.Errorf("%s: T-1 %+v, want listed dropped (ended)", when, st)
		}
		if _, err := r.dispatch("d1", "L-01"); err == nil || !strings.Contains(err.Error(), refused) {
			t.Errorf("%s: dispatch of the dropped spec: %v; want refused naming the drop", when, err)
		}
		if _, err := r.dropCall("x3", "again"); err == nil || !strings.Contains(err.Error(), "T-1 is dropped already") {
			t.Errorf("%s: a second drop: %v; want refused as dropped", when, err)
		}
	}
	// The same text again is no new rev: still dropped.
	if got := r.setSpec("s2", "# T-1 — Stored\n\nDifficulty: medium\n"); got != "UNCHANGED T-1 rev 1" {
		t.Fatalf("spec set: %q, want UNCHANGED T-1 rev 1", got)
	}
	if st := r.taskStatus("T-1"); st.State != "dropped" {
		t.Errorf("after an unchanged set: T-1 %+v, want still dropped", st)
	}
	// A new rev revives it, also on a replay: not listed, and the same request dispatches.
	if got := r.setSpec("s3", "# T-1 — Stored, widened\n\nDifficulty: medium\n"); got != "SET T-1 rev 2" {
		t.Fatalf("spec set: %q, want SET T-1 rev 2", got)
	}
	if st := r.taskStatus("T-1"); st.Task != "" {
		t.Errorf("after rev 2: T-1 listed %+v, want revived (a spec never dispatched)", st)
	}
	r.stop()
	r.start()
	if st := r.taskStatus("T-1"); st.Task != "" {
		t.Errorf("after rev 2 and a restart: T-1 listed %+v, want revived", st)
	}
	if got, err := r.dispatch("d2", "L-01"); err != nil || !strings.HasPrefix(got, "DISPATCHED T-1 → L-01 (queued)") {
		t.Errorf("dispatch after rev 2: %q, %v; want dispatched", got, err)
	}
}

// T-1026: a spec-only drop of T-1 ends a dispatch waiting --after it as a drop of a dispatched T-1 does (needs-you
// {after}), a new dispatch --after T-1 is refused as for any dropped task, and once a new rev revives T-1 an --after on
// it waits again.
func TestSpecOnlyDropEndsAnAfterWait(t *testing.T) {
	t.Parallel()
	r := newLoopRig(t, dispatching())
	r.start()
	r.setSpec("s1", "# T-1 — Stored\n\nDifficulty: medium\n")
	after := func(callID, task string) (string, error) {
		return r.dispatchCall(wire.Call{Root: r.p.Root, Agent: "pj-lead", Role: "lead", CallID: callID, Task: task,
			Lane: "L-01", After: []string{"T-1"}, Types: []string{"sonnet"}})
	}
	if reply, err := after("d1", "T-2"); err != nil || !strings.HasPrefix(reply, "WAITING T-2 after T-1 (queued)") {
		t.Fatalf("dispatch T-2 --after T-1: %q, %v; want waiting", reply, err)
	}
	r.passes()
	if st := r.taskStatus("T-2"); st.State != "waiting" {
		t.Fatalf("precondition: T-2 %+v, want waiting", st)
	}
	r.dropSpec("x1", "T-1", "superseded")
	r.waitPrefix("1 fact needs-you{after: T-2 waits for T-1, which is dropped")
	if n := r.taskStep("T-2", "fact dispatch"); n != 0 {
		t.Errorf("T-2 dispatched after its dependency was dropped: %q", r.steps())
	}
	if _, err := after("d2", "T-3"); err == nil || err.Error() != "dispatch T-3: T-1 is dropped: it never merges" {
		t.Errorf("dispatch T-3 --after the dropped T-1: %v; want refused as a dropped task", err)
	}
	r.setSpec("s2", "# T-1 — Stored, widened\n\nDifficulty: medium\n")
	if reply, err := after("d3", "T-3"); err != nil || !strings.HasPrefix(reply, "WAITING T-3 after T-1 (queued)") {
		t.Errorf("dispatch T-3 --after T-1 at rev 2: %q, %v; want waiting again", reply, err)
	}
}

// T-1026: a spec-only drop of T-1 ends a watch of T-1 and a set watch T-1,T-2 with the dropped line, and a watch made
// after the drop is answered with that outcome at once.
func TestSpecOnlyDropEndsWatches(t *testing.T) {
	t.Parallel()
	r := newLoopRig(t, dispatching())
	r.start()
	r.setSpec("s1", "# T-1 — Stored\n\nDifficulty: medium\n")
	if reply, err := r.setSpecTask("s2", "T-2", "# T-2 — Other\n\nDifficulty: medium\n", ""); err != nil || reply != "SET T-2 rev 1" {
		t.Fatalf("spec set T-2: %q, %v", reply, err)
	}
	for _, set := range []string{"T-1", "T-1,T-2"} {
		if reply, err := r.watch("w-"+set, set, false); err != nil || reply != "WATCHING "+set {
			t.Fatalf("watch %s: %q, %v", set, reply, err)
		}
	}
	r.dropSpec("x1", "T-1", "superseded")
	if d := r.factData("T-1", event.StepDropped); d["rev"] != float64(1) {
		t.Fatalf("precondition: the dropped fact %+v, want a spec-only drop at rev 1", d)
	}
	one, set := "[obor] T-1 dropped: superseded", "[obor] T-1 dropped: superseded (watch T-1,T-2 ended)"
	r.waitFor("the dropped lines posted", func() bool {
		return r.inbox.count("pj-lead", one) == 1 && r.inbox.count("pj-lead", set) == 1
	})
	if reply, err := r.watch("w3", "", false); err != nil || reply != "NO WATCHES" {
		t.Errorf("the list after the drop: %q, %v; want both ended", reply, err)
	}
	for set, want := range map[string]string{"T-1": "T-1 dropped: superseded",
		"T-1,T-2": "T-1 dropped: superseded (watch T-1,T-2 ended)"} {
		if reply, err := r.watch("w4-"+set, set, false); err != nil || reply != want {
			t.Errorf("watch %s after the drop: %q, %v; want %q at once", set, reply, err, want)
		}
	}
}
