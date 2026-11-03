package app_test

import (
	"context"
	"encoding/json"
	"obor/internal/app/event"
	"obor/internal/app/port"
	"obor/internal/app/wire"
	"slices"
	"strings"
	"testing"
	"time"

	"obor/internal/app"
)

// T-462: obor dispatch … --after waits for merges (docs/state-model.md row 2a, readiness rule 1). T-2 is the task T-1
// waits for: in the loop as a failed submission (a needs-you), merged once an obor merge lands it on main (row 34,
// outside); T-3 is a task with a stored spec and no loop record.

// seedT2 writes T-2's open attempt to the log of the stopped app: submitted on L-02, its submit refused (fail).
func (r *loopRig) seedT2(more ...event.Record) {
	r.t.Helper()
	sub, _ := json.Marshal(map[string]any{"lane": "L-02", "agent": "pj-l-02"})
	refused, _ := json.Marshal(map[string]any{"refused": "no commit"})
	recs := append([]event.Record{{Task: "T-2", Attempt: 1, Step: event.StepSubmitted, Kind: event.KindFact, Data: sub},
		{Task: "T-2", Attempt: 1, Step: event.StepSubmit, Kind: event.KindIntent, Class: event.ClassGit},
		{Task: "T-2", Attempt: 1, Step: event.StepSubmit, Kind: event.KindResult, Data: refused}}, more...)
	if err := app.WriteLog(r.p.Root, time.Now(), recs...); err != nil {
		r.t.Fatal(err)
	}
}

// landT2 is an obor merge of T-2 on main outside the app: the loop records merged{outside}.
func (r *loopRig) landT2() {
	r.t.Helper()
	r.p.Git(r.p.Root, "commit", "-q", "--allow-empty", "-m", "Merge T-2: the dependency")
	r.waitFor("T-2 merged", func() bool { return r.taskStep("T-2", "fact merged") > 0 })
}

// taskStep is how many of task's records read "<kind> <step>".
func (r *loopRig) taskStep(task, step string) int {
	n := 0
	for _, rec := range r.records() {
		if rec.Task == task && rec.Kind+" "+rec.String() == step {
			n++
		}
	}
	return n
}

// waitingAfter2, waitingAfter3: T-1's WAITING reply after T-2, T-3, the rig's L-01 still idle (T-483).
const (
	waitingAfter2 = "WAITING T-1 after T-2 (queued); idle: L-01"
	waitingAfter3 = "WAITING T-1 after T-3 (queued); idle: L-01"
)

// waitAfter is the lead's dispatch of T-1 onto L-01 after the tasks.
func (r *loopRig) waitAfter(callID string, after ...string) (string, error) {
	return r.dispatchCall(wire.Call{Root: r.p.Root, Agent: "pj-lead", Role: "lead", CallID: callID, Task: "T-1",
		Lane: "L-01", After: after, Types: []string{"sonnet"}})
}

// taskStatus is task's row in obor serve status (Task "": not listed).
func (r *loopRig) taskStatus(task string) wire.TaskStatus {
	for _, t := range r.app.Status().Project.Tasks {
		if t.Task == task {
			return t
		}
	}
	return wire.TaskStatus{}
}

// startWithT2 starts the rig with T-2 seeded and waits until its needs-you is folded.
func startWithT2(t *testing.T, more ...event.Record) *loopRig {
	t.Helper()
	r := newDispatchRig(t)
	r.seedT2(more...)
	r.start()
	r.waitFor("T-2 needs-you", func() bool { return r.taskStatus("T-2").State == "needs-you" })
	return r
}

// Row 2a: the request records fact waiting (no lane touched); the loop dispatches T-1 once T-2 is merged, by the
// dispatch's own steps; a second request of the waiting task replaces its fact (T-538); status shows what it waits for.
func TestWaitingDispatchesOnceAfterMerge(t *testing.T) {
	t.Parallel()
	r := startWithT2(t)
	reply, err := r.waitAfter("d1", "T-2")
	if err != nil || reply != waitingAfter2 {
		t.Fatalf("dispatch --after: %q, %v", reply, err)
	}
	if reply, err := r.waitAfter("d2", "T-2"); err != nil || reply != waitingAfter2 {
		t.Errorf("a second dispatch of the waiting task: %q, %v; want its reply", reply, err)
	}
	r.passes()
	if n, d := r.taskStep("T-1", "fact waiting"), r.taskStep("T-1", "fact dispatch"); n != 2 || d != 0 {
		t.Fatalf("T-1 waiting facts %d (want 2: the request and its replacement), dispatch facts %d (want 0): %q", n, d, r.steps())
	}
	if st := r.taskStatus("T-1"); st.State != "waiting" || !slices.Equal(st.After, []string{"T-2"}) || st.Lane != "" {
		t.Errorf("status %+v, want waiting after T-2, no lane", st)
	}
	if n := r.disp.called("prepare"); n != 0 {
		t.Errorf("prepared %d times while waiting", n)
	}
	r.landT2()
	r.waitFor("T-1's work job", func() bool { return r.taskStep("T-1", "result work") == 1 })
	r.passes()
	if n := r.taskStep("T-1", "fact dispatch"); n != 1 {
		t.Errorf("T-1 dispatch facts %d, want 1: %q", n, r.steps())
	}
	for _, rec := range r.records() {
		if rec.Task == "T-1" && rec.Step == event.StepDispatched && rec.Attempt != 1 {
			t.Errorf("the dispatch's attempt %d, want the wait's 1", rec.Attempt)
		}
	}
	if st := r.taskStatus("T-1"); st.State != "dispatched" && st.State != "working" || st.Lane != "L-01" {
		t.Errorf("status after the merge %+v, want dispatched on L-01", st)
	}
	if reply, err := r.waitAfter("d2", "T-2"); err != nil || reply != "DISPATCHED T-1 → L-01 (queued) as sonnet" {
		t.Errorf("the replacing call id again: %q, %v; want the dispatch's reply", reply, err)
	}
}

// Row 2a, replay: a waiting task survives a restart and dispatches exactly once after its dependency merges.
func TestWaitingSurvivesRestart(t *testing.T) {
	t.Parallel()
	r := startWithT2(t)
	if _, err := r.waitAfter("d1", "T-2"); err != nil {
		t.Fatal(err)
	}
	r.stop()
	r.start()
	r.waitFor("T-1 waiting after the restart", func() bool { return r.taskStatus("T-1").State == "waiting" })
	if reply, err := r.waitAfter("d2", "T-2"); err != nil || reply != waitingAfter2 {
		t.Errorf("the waiting task's request after the restart: %q, %v", reply, err)
	}
	r.landT2()
	r.waitFor("T-1's work job", func() bool { return r.taskStep("T-1", "result work") == 1 })
	r.passes()
	if n, w := r.taskStep("T-1", "fact dispatch"), r.taskStep("T-1", "fact waiting"); n != 1 || w != 2 {
		t.Errorf("T-1 dispatch facts %d, waiting facts %d; want 1 and 2 (the request, its replacement after the restart): %q", n, w, r.steps())
	}
}

// Row 2a, kill -9 between the merge and the dispatch: the log holds T-2's merged fact and T-1's waiting fact, no
// dispatch; the replay dispatches once, and a second restart dispatches nothing more.
func TestWaitingCrashAfterMerge(t *testing.T) {
	t.Parallel()
	r := newDispatchRig(t)
	wait, _ := json.Marshal(map[string]any{"lane": "L-01", "after": []string{"T-2"}, "types": []string{"sonnet"},
		"caller": "pj-lead", "call": "d1", "reply": "WAITING T-1 after T-2 (queued)"})
	merged, _ := json.Marshal(map[string]any{"outside": true})
	r.seedT2(event.Record{Task: "T-1", Attempt: 1, Step: event.StepWaiting, Kind: event.KindFact, Data: wait},
		event.Record{Task: "T-2", Attempt: 1, Step: event.StepMerged, Kind: event.KindFact, Data: merged})
	r.start()
	r.waitFor("T-1's work job", func() bool { return r.taskStep("T-1", "result work") == 1 })
	r.stop()
	r.start()
	r.passes()
	if n := r.taskStep("T-1", "fact dispatch"); n != 1 {
		t.Errorf("T-1 dispatch facts %d, want 1: %q", n, r.steps())
	}
	if p, _ := r.disp.applied(); p != 1 {
		t.Errorf("prepares %d, want 1", p)
	}
}

// Row 2a's refusals at request time, nothing recorded: a task it names that is itself, unknown (no spec, no loop
// record) or dropped, an unknown lane. One not in the loop yet but with a spec: it waits. All merged: a plain dispatch
// at once.
func TestWaitingRefusals(t *testing.T) {
	t.Parallel()
	r := startWithT2(t)
	for _, c := range []struct {
		after []string
		want  string
	}{
		{[]string{"T-1"}, "dispatch T-1: it cannot wait for itself"},
		{[]string{"T-2", "T-9"}, "dispatch T-1: T-9 is no task (no spec in obor serve's store)"},
	} {
		if reply, err := r.waitAfter("x-"+c.want, c.after...); err == nil || err.Error() != c.want {
			t.Errorf("after %v: %q, %v; want %q", c.after, reply, err, c.want)
		}
	}
	if _, err := r.dispatchCall(wire.Call{Root: r.p.Root, Role: "lead", CallID: "d0", Task: "T-1", Lane: "L-09",
		After: []string{"T-2"}, Types: []string{"sonnet"}}); err == nil || !strings.Contains(err.Error(), "unknown lane L-09") {
		t.Errorf("an unknown lane: %v", err)
	}
	r.passes()
	if n := r.taskStep("T-1", "fact waiting"); n != 0 {
		t.Fatalf("a refusal recorded: %q", r.steps())
	}
	if _, err := wire.Verdict(context.Background(), r.sock, wire.MethodSpecSet, wire.Call{Root: r.p.Root, Role: "lead",
		CallID: "s3", Task: "T-3", Spec: "# T-3\n\nDifficulty: medium\n"}); err != nil {
		t.Fatal(err)
	}
	if reply, err := r.waitAfter("d3", "T-3"); err != nil || reply != waitingAfter3 {
		t.Errorf("after T-3 (a spec, no loop record): %q, %v", reply, err)
	}
	if _, err := wire.Verdict(context.Background(), r.sock, wire.MethodDrop, wire.Call{Root: r.p.Root, Role: "lead",
		CallID: "x1", Task: "T-1", Note: "redo"}); err != nil {
		t.Fatal(err)
	}
	if _, err := wire.Verdict(context.Background(), r.sock, wire.MethodDrop, wire.Call{Root: r.p.Root, Role: "lead",
		CallID: "x3", Task: "T-2", Note: "gone"}); err != nil {
		t.Fatal(err)
	}
	if _, err := r.waitAfter("d4", "T-2"); err == nil || err.Error() != "dispatch T-1: T-2 is dropped: it never merges" {
		t.Errorf("after a dropped task: %v", err)
	}
	// T-4 merged before (its facts in the log): a dispatch --after it is a plain dispatch at once.
	r.stop()
	merged, _ := json.Marshal(map[string]any{"outside": true})
	sub, _ := json.Marshal(map[string]any{"lane": "L-04", "agent": "pj-l4"})
	if err := app.WriteLog(r.p.Root, time.Now(), event.Record{Task: "T-4", Attempt: 1, Step: event.StepSubmitted,
		Kind: event.KindFact, Data: sub}, event.Record{Task: "T-4", Attempt: 1, Step: event.StepMerged, Kind: event.KindFact,
		Data: merged}); err != nil {
		t.Fatal(err)
	}
	r.start()
	if reply, err := r.waitAfter("d5", "T-4"); err != nil || reply != "DISPATCHED T-1 → L-01 (queued) as sonnet" {
		t.Errorf("after a merged task: %q, %v; want a plain dispatch", reply, err)
	}
}

// Row 2a → needs-you: a task T-1 waits for is dropped later: needs-you{after}, nothing dispatched; obor resume
// retries: T-1 waits again (and is told again while T-2 stays dropped).
func TestWaitingDependencyDroppedLater(t *testing.T) {
	t.Parallel()
	r := startWithT2(t)
	if _, err := r.waitAfter("d1", "T-2"); err != nil {
		t.Fatal(err)
	}
	if _, err := wire.Verdict(context.Background(), r.sock, wire.MethodDrop, wire.Call{Root: r.p.Root, Role: "lead",
		CallID: "x1", Task: "T-2", Note: "gone"}); err != nil {
		t.Fatal(err)
	}
	r.waitPrefix("1 fact needs-you{after: T-1 waits for T-2, which is dropped")
	r.needsYouState()
	if n := r.taskStep("T-1", "fact dispatch"); n != 0 {
		t.Errorf("dispatched after its dependency was dropped: %q", r.steps())
	}
	r.mustResume("r1", "", "RESUMED T-1: retry (after)")
	r.waitFor("needs-you again", func() bool { return r.countPrefix("1 fact needs-you{after: ") == 2 })
}

// Row 44 for a waiting task: obor drop <T> ends it; its dependency's merge later dispatches nothing.
func TestWaitingDropped(t *testing.T) {
	t.Parallel()
	r := startWithT2(t)
	if _, err := r.waitAfter("d1", "T-2"); err != nil {
		t.Fatal(err)
	}
	reply, err := wire.Verdict(context.Background(), r.sock, wire.MethodDrop, wire.Call{Root: r.p.Root, Role: "lead",
		CallID: "x1", Task: "T-1", Note: "not needed"})
	if err != nil || reply != "DROPPED T-1" {
		t.Fatalf("drop of the waiting task: %q, %v", reply, err)
	}
	if st := r.taskStatus("T-1"); st.State != "dropped" {
		t.Errorf("status %+v, want dropped", st)
	}
	r.landT2()
	r.passes()
	if n := r.taskStep("T-1", "fact dispatch"); n != 0 {
		t.Errorf("a dropped waiting task dispatched: %q", r.steps())
	}
}

// Row 2a with no free lane once its dependency merged: it keeps waiting (the refusal logged once), and dispatches when
// the lane is free.
func TestWaitingLaneBusy(t *testing.T) {
	t.Parallel()
	r := startWithT2(t)
	r.setLane(func(l *port.Lane) { l.State, l.Task = "working", "T-9" })
	if _, err := r.waitAfter("d1", "T-2"); err != nil {
		t.Fatal(err)
	}
	r.landT2()
	r.passes()
	r.passes()
	if n := r.taskStep("T-1", "fact dispatch"); n != 0 || r.taskStatus("T-1").State != "waiting" {
		t.Fatalf("dispatched onto a busy lane, or not waiting: %q", r.steps())
	}
	if n := strings.Count(r.out.String(), "T-1 waiting: dispatch T-1: lane L-01 is working on T-9"); n != 1 {
		t.Errorf("the refusal logged %d times, want once:\n%s", n, r.out.String())
	}
	r.setLane(func(l *port.Lane) { l.State, l.Task = "free", "" })
	r.waitFor("T-1's work job", func() bool { return r.taskStep("T-1", "result work") == 1 })
}

// DD-v0.12 A1: a waiting task whose dispatch is refused once all it waits for merged says why in obor status; the same
// refusal for an hour is needs-you{after: …}, kept by a restart (the store's fact); obor resume sends it back to
// waiting, its hour from the start.
func TestWaitingRefusedSaysWhyThenNeedsYou(t *testing.T) {
	t.Parallel()
	r := startWithT2(t)
	r.setLane(func(l *port.Lane) { l.State, l.Task = "working", "T-9" })
	if _, err := r.waitAfter("d1", "T-2"); err != nil {
		t.Fatal(err)
	}
	if st := r.taskStatus("T-1"); st.Why != "" {
		t.Fatalf("a reason before T-2 merged: %+v", st)
	}
	r.landT2()
	const why = "dispatch T-1: lane L-01 is working on T-9"
	r.waitFor("the reason in status", func() bool { return r.taskStatus("T-1").Why == why })
	r.later(59 * time.Minute)
	const needs = "1 fact needs-you{after: T-1 is not dispatched for 1h since what it waits for merged: " + why + "}"
	if r.has(needs) {
		t.Fatalf("a needs-you before the hour: %q", r.steps())
	}
	r.later(time.Minute)
	r.waitStep(needs)
	r.stop()
	r.start()
	r.waitFor("the needs-you after the restart", func() bool { return r.taskStatus("T-1").State == "needs-you" })
	if r.taskStatus("T-1").Why != "" || r.count(needs) != 1 {
		t.Errorf("after the restart: %+v, needs-you %d; want needs-you once, no waiting reason", r.taskStatus("T-1"),
			r.count(needs))
	}
	r.mustResume("r1", "", "RESUMED T-1: retry (after)")
	r.waitFor("waiting again, with its reason", func() bool {
		st := r.taskStatus("T-1")
		return st.State == "waiting" && st.Why == why
	})
	r.later(30 * time.Minute)
	if n := r.count(needs); n != 1 {
		t.Errorf("needs-you %d times 30 min after the resume, want 1 (its hour starts again)", n)
	}
	r.setLane(func(l *port.Lane) { l.State, l.Task = "free", "" })
	r.waitFor("T-1's work job", func() bool { return r.taskStep("T-1", "result work") == 1 })
}

// DD-v0.12 A7 (a): a task T-1 waits for with no loop record (T-3: a spec, done by hand) counts as merged once the
// target holds its merge evidence; until then T-1 waits.
func TestWaitingForATaskMergedByHand(t *testing.T) {
	t.Parallel()
	r := startWithT2(t)
	if _, err := wire.Verdict(context.Background(), r.sock, wire.MethodSpecSet, wire.Call{Root: r.p.Root, Role: "lead",
		CallID: "s3", Task: "T-3", Spec: "# T-3\n\nDifficulty: medium\n"}); err != nil {
		t.Fatal(err)
	}
	if reply, err := r.waitAfter("d1", "T-3"); err != nil || reply != waitingAfter3 {
		t.Fatalf("after T-3: %q, %v", reply, err)
	}
	r.passes()
	r.passes()
	if n := r.taskStep("T-1", "fact dispatch"); n != 0 || r.taskStatus("T-1").State != "waiting" {
		t.Fatalf("dispatched before T-3 merged: %q", r.steps())
	}
	r.p.Git(r.p.Root, "commit", "-q", "--allow-empty", "-m", "Merge T-3: by hand")
	r.waitFor("T-1's work job", func() bool { return r.taskStep("T-1", "result work") == 1 })
	if n := r.taskStep("T-3", "fact merged"); n != 0 {
		t.Errorf("T-3 got loop records: %q", r.steps())
	}
}

// DD-v0.12 A7 (b): a dispatch --after a task that waits (itself, or through what it waits for) for this one is refused:
// a cycle never dispatches. Nothing is recorded.
func TestWaitingCycleRefused(t *testing.T) {
	t.Parallel()
	r := startWithT2(t)
	for _, task := range []string{"T-3", "T-4"} {
		if _, err := wire.Verdict(context.Background(), r.sock, wire.MethodSpecSet, wire.Call{Root: r.p.Root, Role: "lead",
			CallID: "s-" + task, Task: task, Spec: "# " + task + "\n\nDifficulty: medium\n"}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := r.waitAfter("d1", "T-3"); err != nil { // T-1 after T-3
		t.Fatal(err)
	}
	if _, err := r.dispatchCall(wire.Call{Root: r.p.Root, Role: "lead", CallID: "d4", Task: "T-4", Lane: "L-01",
		After: []string{"T-1"}}); err != nil { // T-4 after T-1
		t.Fatal(err)
	}
	for _, c := range []struct {
		call, after, want string
	}{
		{"d3", "T-1", "dispatch T-3: T-1 waits for T-3 itself: a cycle never dispatches"},
		{"d3b", "T-4", "dispatch T-3: T-4 waits for T-3 itself: a cycle never dispatches"},
	} {
		reply, err := r.dispatchCall(wire.Call{Root: r.p.Root, Role: "lead", CallID: c.call, Task: "T-3", Lane: "L-01",
			After: []string{c.after}})
		if err == nil || err.Error() != c.want {
			t.Errorf("T-3 after %s: %q, %v; want %q", c.after, reply, err, c.want)
		}
	}
	if n := r.taskStep("T-3", "fact waiting"); n != 0 {
		t.Errorf("a refused cycle recorded: %q", r.steps())
	}
}
