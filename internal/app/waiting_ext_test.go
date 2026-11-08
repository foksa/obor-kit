package app_test

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"testing"
	"time"

	"obor/internal/app"
	"obor/internal/app/event"
	"obor/internal/app/port"
	"obor/internal/app/wire"
)

// A request that replaces a waiting one (new after, new models) is the task's waiting fact in force; the old one's
// reason does not carry over; a lane that is free then dispatches it.
func TestDifficultyRedispatchOfAWaitingTaskReplacesIt(t *testing.T) {
	t.Parallel()
	r := newLoopRig(t, pooled())
	r.lanes.mu.Lock()
	r.lanes.lanes[0].State, r.lanes.lanes[0].Task = "working", "T-9"
	r.lanes.lanes[1].State, r.lanes.lanes[1].Task = "working", "T-9"
	r.lanes.mu.Unlock()
	r.start()
	r.specLevel("medium")
	if reply, err := r.dispatchLevel("d1"); err != nil || !strings.HasPrefix(reply, "WAITING T-1 (queued): waiting for a free lane (medium: sonnet, deepseek)") {
		t.Fatalf("first request: %q, %v", reply, err)
	}
	if reply, err := r.dispatchLevel("d2", "deepseek"); err == nil || reply != "" || !strings.Contains(err.Error(), "no free lane for the models deepseek") {
		t.Fatalf("--model while all is busy: %q, %v; want the refusal (no waiting)", reply, err)
	}
	if st := r.taskStatus("T-1"); st.State != "waiting" || st.Level != "medium" {
		t.Fatalf("status %+v after a refused replacement, want the first request waiting", st)
	}
	r.setSpec("s2", "# T-1 — Leveled\n\nDifficulty: simple\n")
	reply, err := r.dispatchLevel("d3")
	if err != nil || !strings.HasPrefix(reply, "WAITING T-1 (queued): waiting for a free lane (simple: deepseek, sonnet, haiku)") {
		t.Fatalf("replacement at another level: %q, %v", reply, err)
	}
	if n := r.taskStep("T-1", "fact waiting"); n != 2 {
		t.Errorf("waiting facts %d, want 2 (the request, its replacement)", n)
	}
	if st := r.taskStatus("T-1"); st.Level != "simple" || strings.Join(st.Types, ",") != "deepseek,sonnet,haiku" {
		t.Errorf("status %+v, want the replacement's level and models", st)
	}
	r.later(2 * time.Hour)
	if st := r.taskStatus("T-1"); st.State != "waiting" {
		t.Errorf("status %+v after two hours, want still waiting (a busy lane is not a needs-you)", st)
	}
	r.lanes.mu.Lock()
	r.lanes.lanes[0].State, r.lanes.lanes[0].Task = "free", ""
	r.lanes.mu.Unlock()
	r.waitFor("the replacement's dispatch", func() bool { return r.taskStep("T-1", "fact dispatch") == 1 })
	if n := r.taskStep("T-1", "fact waiting"); n != 2 {
		t.Errorf("waiting facts %d after the dispatch, want 2", n)
	}
}

// RV-51 #2: a waiting request that replaces a refused one (obor drop, then dispatch it again --after another task)
// starts with no refusal: no inherited reason in status, and its own hour before the needs-you; also across a reload.
func TestDispatchReplacedWaitStartsAfresh(t *testing.T) {
	t.Parallel()
	r := startWithT2(t)
	r.setLane(func(l *port.Lane) { l.State, l.Task = "working", "T-9" })
	if _, err := r.waitAfter("d1", "T-2"); err != nil {
		t.Fatal(err)
	}
	r.landT2()
	const why = "dispatch T-1: lane L-01 is working on T-9"
	r.waitFor("the first wait's reason", func() bool { return r.taskStatus("T-1").Why == why })
	r.later(59 * time.Minute)
	if st := r.taskStatus("T-1"); st.State != "waiting" {
		t.Fatalf("precondition: the first wait %+v, want waiting 59 min into its refusal", st)
	}
	// A retry pass may hold the dispatch for a moment: the drop is refused "retry when it settles" (same call id).
	var reply string
	var err error
	r.waitFor("the drop", func() bool {
		reply, err = wire.Verdict(context.Background(), r.sock, &wire.DropReq{Envelope: wire.Envelope{Root: r.p.Root,
			Role: "lead", CallID: "x1"}, Task: "T-1", Why: "requeue"})
		return err == nil || !strings.Contains(err.Error(), "retry when it settles")
	})
	if err != nil || reply != "DROPPED T-1" {
		t.Fatalf("drop: %q, %v", reply, err)
	}
	if _, err := wire.Verdict(context.Background(), r.sock, &wire.SpecSetReq{Envelope: wire.Envelope{Root: r.p.Root,
		Role: "lead", CallID: "s3"}, Task: "T-3", Spec: "# T-3 — Other\n\nDifficulty: medium\n"}); err != nil {
		t.Fatal(err)
	}
	if reply, err := r.waitAfter("d2", "T-3"); err != nil || reply != "WAITING T-1 after T-3 (queued)" {
		t.Fatalf("the replacement: %q, %v", reply, err)
	}
	if st := r.taskStatus("T-1"); st.State != "waiting" || st.Why != "" {
		t.Errorf("the replacement %+v, want waiting with no reason (the old refusal's)", st)
	}
	r.later(time.Minute)
	r.scratch.Land("T-3") // obor merge by hand
	r.waitFor("the replacement's own reason", func() bool { return r.taskStatus("T-1").Why == why })
	r.later(2 * time.Minute)
	if st := r.taskStatus("T-1"); st.State != "waiting" || strings.Contains(strings.Join(r.steps(), "\n"), "needs-you{after: T-1") {
		t.Errorf("the replacement %+v, steps %q: want waiting, no needs-you (its hour is its own)", st, r.steps())
	}
	r.stop()
	r.start()
	r.waitFor("the replacement's reason after the reload", func() bool {
		st := r.taskStatus("T-1")
		return st.State == "waiting" && st.Why == why
	})
	r.later(59 * time.Minute)
	if st := r.taskStatus("T-1"); st.State != "waiting" || strings.Contains(strings.Join(r.steps(), "\n"), "needs-you{after: T-1") {
		t.Errorf("after the reload %+v, steps %q: want waiting, no needs-you", st, r.steps())
	}
}

// pooled is dispatching with lanes 2: L-02 free too.
func pooled() rigOpt { return func(o *rigOpts) { o.dispatch, o.pool = true, true } }

// waitingAfter2, waitingAfter3: T-1's WAITING reply after T-2, T-3, the rig's L-01 still idle (T-483).
const (
	waitingAfter2 = "WAITING T-1 after T-2 (queued); idle: L-01"
	waitingAfter3 = "WAITING T-1 after T-3 (queued); idle: L-01"
)

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
	r := newLoopRig(t, dispatching())
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
	if _, err := r.dispatchCall(wire.DispatchReq{Envelope: wire.Envelope{Root: r.p.Root, Role: "lead", CallID: "d0"},
		Task: "T-1", Lane: "L-09", After: []string{"T-2"},
		Types: []string{"sonnet"}}); err == nil || !strings.Contains(err.Error(), "unknown lane L-09") {
		t.Errorf("an unknown lane: %v", err)
	}
	r.passes()
	if n := r.taskStep("T-1", "fact waiting"); n != 0 {
		t.Fatalf("a refusal recorded: %q", r.steps())
	}
	if _, err := wire.Verdict(context.Background(), r.sock, &wire.SpecSetReq{Envelope: wire.Envelope{Root: r.p.Root,
		Role: "lead", CallID: "s3"}, Task: "T-3", Spec: "# T-3 — Other\n\nDifficulty: medium\n"}); err != nil {
		t.Fatal(err)
	}
	if reply, err := r.waitAfter("d3", "T-3"); err != nil || reply != waitingAfter3 {
		t.Errorf("after T-3 (a spec, no loop record): %q, %v", reply, err)
	}
	if _, err := wire.Verdict(context.Background(), r.sock, &wire.DropReq{Envelope: wire.Envelope{Root: r.p.Root,
		Role: "lead", CallID: "x1"}, Task: "T-1", Why: "redo"}); err != nil {
		t.Fatal(err)
	}
	if _, err := wire.Verdict(context.Background(), r.sock, &wire.DropReq{Envelope: wire.Envelope{Root: r.p.Root,
		Role: "lead", CallID: "x3"}, Task: "T-2", Why: "gone"}); err != nil {
		t.Fatal(err)
	}
	// a task holding a lane is dropping until the merge loop stops its worker and frees its lane (T-852)
	r.waitFor("T-2 dropped", func() bool { return r.task("T-2").State == "dropped" })
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
	if _, err := wire.Verdict(context.Background(), r.sock, &wire.DropReq{Envelope: wire.Envelope{Root: r.p.Root,
		Role: "lead", CallID: "x1"}, Task: "T-2", Why: "gone"}); err != nil {
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
	reply, err := wire.Verdict(context.Background(), r.sock, &wire.DropReq{Envelope: wire.Envelope{Root: r.p.Root,
		Role: "lead", CallID: "x1"}, Task: "T-1", Why: "not needed"})
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
	if _, err := wire.Verdict(context.Background(), r.sock, &wire.SpecSetReq{Envelope: wire.Envelope{Root: r.p.Root,
		Role: "lead", CallID: "s3"}, Task: "T-3", Spec: "# T-3 — Other\n\nDifficulty: medium\n"}); err != nil {
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
	r.scratch.Land("T-3") // obor merge by hand
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
		if _, err := wire.Verdict(context.Background(), r.sock, &wire.SpecSetReq{Envelope: wire.Envelope{Root: r.p.Root,
			Role: "lead", CallID: "s-" + task}, Task: task,
			Spec: "# " + task + " — Other\n\nDifficulty: medium\n"}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := r.waitAfter("d1", "T-3"); err != nil { // T-1 after T-3
		t.Fatal(err)
	}
	if _, err := r.dispatchCall(wire.DispatchReq{Envelope: wire.Envelope{Root: r.p.Root, Role: "lead", CallID: "d4"},
		Task: "T-4", Lane: "L-01", After: []string{"T-1"}}); err != nil { // T-4 after T-1
		t.Fatal(err)
	}
	for _, c := range []struct {
		call, after, want string
	}{
		{"d3", "T-1", "dispatch T-3: T-1 waits for T-3 itself: a cycle never dispatches"},
		{"d3b", "T-4", "dispatch T-3: T-4 waits for T-3 itself: a cycle never dispatches"},
	} {
		reply, err := r.dispatchCall(wire.DispatchReq{Envelope: wire.Envelope{Root: r.p.Root, Role: "lead",
			CallID: c.call}, Task: "T-3", Lane: "L-01", After: []string{c.after}})
		if err == nil || err.Error() != c.want {
			t.Errorf("T-3 after %s: %q, %v; want %q", c.after, reply, err, c.want)
		}
	}
	if n := r.taskStep("T-3", "fact waiting"); n != 0 {
		t.Errorf("a refused cycle recorded: %q", r.steps())
	}
}

// T-977: a waiting task's status carries its Difficulty and the model it will run on: the level's first model not
// paused, or a --model override's (the override names no level: the Difficulty is the stored spec's).
func TestWaitingTaskCarriesDifficultyAndModel(t *testing.T) {
	t.Parallel()
	r := newLoopRig(t, pooled())
	r.lanes.mu.Lock()
	for i := range r.lanes.lanes {
		r.lanes.lanes[i].State, r.lanes.lanes[i].Task = "working", "T-9"
	}
	r.lanes.mu.Unlock()
	r.start()

	// The level: medium is sonnet, deepseek; sonnet paused, so deepseek.
	r.specLevel("medium")
	if reply, err := r.pause("lead", "p1", wire.PauseReq{Model: "sonnet", Why: "quota"}); err != nil {
		t.Fatalf("pause sonnet: %q, %v", reply, err)
	}
	if reply, err := r.dispatchLevel("d1"); err != nil || !strings.HasPrefix(reply, "WAITING T-1") {
		t.Fatalf("medium with no free lane: %q, %v; want it waiting", reply, err)
	}
	st := r.taskStatus("T-1")
	if st.State != "waiting" || st.Level != "medium" || strings.Join(st.Types, ",") != "sonnet,deepseek" {
		t.Fatalf("precondition: T-1 %+v, want waiting at medium on sonnet, deepseek", st)
	}
	if st.Difficulty != "medium" || st.Model != "deepseek" {
		t.Errorf("T-1 difficulty %q model %q; want medium on deepseek (sonnet paused)", st.Difficulty, st.Model)
	}

	// A --model override, after a task not merged: no level in the request, the spec's Difficulty; the override's model.
	r.specOf("T-2", "# T-2 — First\n\nDifficulty: simple\n")
	r.specOf("T-3", "# T-3 — Override\n\nDifficulty: hard\n")
	if reply, err := r.dispatchCall(wire.DispatchReq{Envelope: wire.Envelope{Root: r.p.Root, Agent: "pj-lead",
		Role: "lead", CallID: "d3"}, Task: "T-3", Types: []string{"opus"},
		After: []string{"T-2"}}); err != nil || !strings.HasPrefix(reply, "WAITING T-3") {
		t.Fatalf("--model opus --after T-2: %q, %v; want it waiting", reply, err)
	}
	st = r.taskStatus("T-3")
	if st.State != "waiting" || st.Level != "" || strings.Join(st.After, ",") != "T-2" {
		t.Fatalf("precondition: T-3 %+v, want waiting after T-2 with no level (the override's)", st)
	}
	if st.Difficulty != "hard" || st.Model != "opus" {
		t.Errorf("T-3 difficulty %q model %q; want hard (its spec) on opus (the override)", st.Difficulty, st.Model)
	}
}

// T-1030: the next free lane goes to the first waiting dispatch by priority, then dispatch order. T-1 and T-3 both
// wait after T-9; T-3, dispatched second but high, takes L-01 once T-9 merged, and T-1 keeps waiting.
func TestHighWaitingTaskTakesTheNextFreeLane(t *testing.T) {
	t.Parallel()
	r := newLoopRig(t, dispatching())
	r.start()
	r.specOf("T-9", "# T-9 — Dependency\n\nDifficulty: medium\n")
	r.specOf("T-1", "# T-1 — Ordinary\n\nDifficulty: medium\n")
	r.specOf("T-3", "# T-3 — High\n\nDifficulty: medium\nPriority: high\n")
	for _, task := range []string{"T-1", "T-3"} {
		reply, err := r.dispatchCall(wire.DispatchReq{Envelope: wire.Envelope{Root: r.p.Root, Agent: "pj-lead",
			Role: "lead", CallID: "d-" + task}, Task: task, After: []string{"T-9"}})
		if err != nil || !strings.HasPrefix(reply, "WAITING "+task+" after T-9 (queued)") {
			t.Fatalf("dispatch of %s after T-9: %q, %v; want WAITING after T-9", task, reply, err)
		}
	}
	if st := r.taskStatus("T-1"); st.State != "waiting" || st.High {
		t.Fatalf("precondition: T-1's status %+v, want waiting, not high", st)
	}
	if st := r.taskStatus("T-3"); st.State != "waiting" || !st.High {
		t.Fatalf("precondition: T-3's status %+v, want waiting, high", st)
	}
	r.scratch.Land("T-9") // obor merge by hand: both waits are ready on the next pass
	r.waitFor("T-3's dispatch", func() bool { return r.taskStep("T-3", "fact dispatch") == 1 })
	r.passes()
	if n := r.taskStep("T-1", "fact dispatch"); n != 0 {
		t.Errorf("T-1, the ordinary waiting task, took the lane: %q", r.steps())
	}
	if st := r.taskStatus("T-1"); st.State != "waiting" {
		t.Errorf("T-1's status %+v, want waiting for the next free lane", st)
	}
	if d := r.factData("T-3", event.StepDispatched); d["lane"] != "L-01" {
		t.Errorf("T-3's dispatch %v, want L-01", d)
	}
}

// T-1030: --after still holds. A high task whose dependency has not merged waits as before; the ordinary task that is
// ready takes the free lane.
func TestHighWaitingTaskWithAnOpenAfterStillWaits(t *testing.T) {
	t.Parallel()
	r := newLoopRig(t, dispatching())
	r.start()
	r.specOf("T-9", "# T-9 — Dependency\n\nDifficulty: medium\n")
	r.specOf("T-1", "# T-1 — Ordinary\n\nDifficulty: medium\n")
	r.specOf("T-3", "# T-3 — High\n\nDifficulty: medium\nPriority: high\n")
	if reply, err := r.dispatchCall(wire.DispatchReq{Envelope: wire.Envelope{Root: r.p.Root, Agent: "pj-lead",
		Role: "lead", CallID: "d-T-3"}, Task: "T-3", After: []string{"T-9"}}); err != nil || !strings.HasPrefix(reply,
		"WAITING T-3 after T-9") {
		t.Fatalf("dispatch of the high task after T-9: %q, %v; want WAITING", reply, err)
	}
	if reply, err := r.dispatchCall(wire.DispatchReq{Envelope: wire.Envelope{Root: r.p.Root, Agent: "pj-lead",
		Role: "lead", CallID: "d-T-1"}, Task: "T-1"}); err != nil || !strings.HasPrefix(reply,
		"DISPATCHED T-1 → L-01") {
		t.Fatalf("dispatch of the ready ordinary task: %q, %v; want DISPATCHED onto L-01", reply, err)
	}
	r.passes()
	if n := r.taskStep("T-3", "fact dispatch"); n != 0 {
		t.Errorf("the high task dispatched before its --after merged: %q", r.steps())
	}
	if st := r.taskStatus("T-3"); st.State != "waiting" {
		t.Errorf("T-3's status %+v, want waiting after T-9", st)
	}
}

// T-1030: a high task whose model is paused does not hold the next task's start: the ordinary one takes the free lane
// on its own model.
func TestHighWaitingTaskOnAPausedModelDoesNotBlockTheNext(t *testing.T) {
	t.Parallel()
	r := newLoopRig(t, dispatching())
	r.start()
	if _, err := r.pause("lead", "p1", wire.PauseReq{Model: "opus", Why: "quota"}); err != nil {
		t.Fatal(err)
	}
	r.specOf("T-9", "# T-9 — Dependency\n\nDifficulty: medium\n")
	r.specOf("T-1", "# T-1 — Ordinary\n\nDifficulty: medium\n")
	r.specOf("T-3", "# T-3 — High\n\nDifficulty: medium\nPriority: high\n")
	if reply, err := r.dispatchCall(wire.DispatchReq{Envelope: wire.Envelope{Root: r.p.Root, Agent: "pj-lead",
		Role: "lead", CallID: "d-T-1"}, Task: "T-1", After: []string{"T-9"}}); err != nil || !strings.HasPrefix(reply,
		"WAITING T-1 after T-9") {
		t.Fatalf("dispatch of T-1 after T-9: %q, %v; want WAITING", reply, err)
	}
	// --model opus: T-3's one model, paused: no other of its level is tried.
	if reply, err := r.dispatchCall(wire.DispatchReq{Envelope: wire.Envelope{Root: r.p.Root, Agent: "pj-lead",
		Role: "lead", CallID: "d-T-3"}, Task: "T-3", Types: []string{"opus"}, After: []string{"T-9"}}); err != nil ||
		!strings.HasPrefix(reply, "WAITING T-3 after T-9") {
		t.Fatalf("dispatch of T-3 --model opus after T-9: %q, %v; want WAITING", reply, err)
	}
	if st := r.taskStatus("T-3"); !st.High {
		t.Fatalf("precondition: T-3's status %+v, want high", st)
	}
	r.scratch.Land("T-9")
	r.waitFor("T-1's dispatch", func() bool { return r.taskStep("T-1", "fact dispatch") == 1 })
	if d := r.factData("T-1", event.StepDispatched); d["type"] != "sonnet" {
		t.Errorf("T-1's dispatch %v, want its own model sonnet", d)
	}
	r.passes()
	if n := r.taskStep("T-3", "fact dispatch"); n != 0 {
		t.Errorf("the high task on a paused model dispatched: %q", r.steps())
	}
	if st := r.taskStatus("T-3"); st.State != "waiting" || !strings.Contains(st.Why, "opus paused: quota") {
		t.Errorf("T-3's status %+v, want waiting with the pause reason", st)
	}
}
