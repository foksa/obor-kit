package app_test

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"sync/atomic"
	"testing"

	"obor/internal/app/event"
	"obor/internal/app/port"
	"obor/internal/app/wire"
	"obor/internal/testkit"
)

// keepRig is the loop rig with L-01 a keep lane (T-1031): lanes [{"name": "L-01", "keep_agent": true}], handoff_tokens
// 1000, and pj-l-01's sessions sized by past: the first session (/s/0.jsonl) is past the threshold once past is set.
func keepRig(t *testing.T) (*loopRig, *atomic.Bool) {
	t.Helper()
	past := &atomic.Bool{}
	r := newLoopRig(t, dispatching(), withConfig(testkit.Set("lanes", `[{"name": "L-01", "keep_agent": true}]`),
		testkit.Set("handoff_tokens", 1000)))
	r.tokens = func(transcript string) (int, bool) {
		if transcript == "/s/0.jsonl" && past.Load() {
			return 5000, true
		}
		return 10, true
	}
	r.start()
	return r, past
}

// dispatchTo is the lead's dispatch of task to lane, as sonnet.
func (r *loopRig) dispatchTo(callID, task, lane string) (string, error) {
	return r.dispatchCall(wire.Call{Root: r.p.Root, Agent: "pj-lead", Role: "lead", CallID: callID, Task: task, Lane: lane,
		Types: []string{"sonnet"}})
}

// record is task's newest record of step and kind (ok false: none).
func (r *loopRig) record(task, kind, step string) (event.Record, bool) {
	var out event.Record
	ok := false
	for _, rec := range r.records() {
		if rec.Task == task && rec.Kind == kind && rec.Step == step {
			out, ok = rec, true
		}
	}
	return out, ok
}

// runToMerge takes task, its work job fetched by pj-l-01, through its task_done, the review and the accept to merged,
// then frees L-01 in the lane table (its row's task landed: what the lane table reads after a merge). inReview runs
// while its review job is out (nil: nothing).
func (r *loopRig) runToMerge(task string, inReview func()) {
	r.t.Helper()
	r.work(strings.ToLower(task)+".txt", task+"\n")
	r.waitFor(task+" working", func() bool { return r.stateOf(task) == "working" })
	reply, err := wire.Verdict(context.Background(), r.sock, wire.MethodTaskDone, wire.Call{Root: r.p.Root,
		Agent: "pj-l-01", Role: "worker", Lane: "L-01", Task: task, CallID: "done-" + task, Report: report})
	if err != nil || reply != "submitted" {
		r.t.Fatalf("%s's task_done: %q, %v", task, reply, err)
	}
	job := r.waitJob("pj-int", task+"/1/review")
	if inReview != nil {
		inReview()
	}
	if reply, err := wire.Verdict(context.Background(), r.sock, wire.MethodAccept, wire.Call{Root: r.p.Root,
		Agent: "pj-int", Role: "integrator", CallID: "accept-" + task, Task: task, Patch: r.patch(job)}); err != nil ||
		!strings.HasPrefix(reply, "ACCEPT "+task) {
		r.t.Fatalf("%s's accept: %q, %v", task, reply, err)
	}
	r.waitFor(task+"'s after_merge", func() bool {
		_, ok := r.record(task, event.KindResult, event.StepAfterMerge)
		return ok
	})
	r.setLane(func(l *port.Lane) { l.Task, l.State = "", "free" })
}

// T-1031, end to end on fakes (no git): three tasks dispatched to one keep lane run in dispatch order, T-2 and T-3
// queued behind the lane's task (today a named busy lane is refused), each next one dispatched only once the one before
// merged; T-1 and T-2 run in pj-l-01's one session (its wake typed, no agent.start; no agent.stop after their merge);
// between T-2 and T-3, the session past handoff_tokens, the handoff rule stops the idle agent and T-3's dispatch starts
// a fresh one carrying its job. job_next hands each task's job to the lane's agent (by lane: its current task).
func TestKeepLaneRunsATaskListInOneSession(t *testing.T) {
	t.Parallel()
	r, past := keepRig(t)
	if got := r.workerStops(); len(got) != 0 {
		t.Fatalf("precondition: stops %q", got)
	}
	if reply, err := r.dispatchTo("d1", "T-1", "L-01"); err != nil || reply != "DISPATCHED T-1 → L-01 (queued) as sonnet" {
		t.Fatalf("T-1: %q, %v", reply, err)
	}
	// dispatch lane pick: a dispatch naming the busy keep lane queues behind its task, in dispatch order.
	for _, c := range []struct{ id, task, behind string }{{"d2", "T-2", "T-1"}, {"d3", "T-3", "T-2"}} {
		want := "WAITING " + c.task + " (queued): behind " + c.behind + " on keep lane L-01"
		if reply, err := r.dispatchTo(c.id, c.task, "L-01"); err != nil || reply != want {
			t.Fatalf("%s: %q, %v; want %q", c.task, reply, err, want)
		}
	}
	if q := r.app.Status().Project.Queues["L-01"]; !slices.Equal(q, []string{"T-2", "T-3"}) {
		t.Errorf("L-01's queue %q, want T-2, T-3", q)
	}

	r.waitJob("pj-l-01", "T-1/1/work")
	r.runToMerge("T-1", nil)
	r.waitJob("pj-l-01", "T-2/1/work") // job_next by lane: the lane's current task, to the same agent
	if _, ok := r.record("T-3", event.KindFact, event.StepDispatched); ok {
		t.Fatal("T-3 dispatched while T-2 holds the lane")
	}
	past.Store(true) // pj-l-01's session grows past handoff_tokens during T-2: no restart mid-task
	r.runToMerge("T-2", func() {
		r.passes() // the rule pass ran with T-2 in review and the session past the threshold
		if got := r.workerStops(); len(got) != 0 {
			t.Errorf("stops while T-2 is in review: %q, want none (never mid-task)", got)
		}
	})
	r.waitJob("pj-l-01", "T-3/1/work")

	// The order: each dispatch after the merge of the task before it.
	for _, c := range []struct{ prev, next string }{{"T-1", "T-2"}, {"T-2", "T-3"}} {
		merged, _ := r.record(c.prev, event.KindFact, event.StepMerged)
		disp, ok := r.record(c.next, event.KindFact, event.StepDispatched)
		if !ok || disp.Seq < merged.Seq {
			t.Errorf("%s dispatched at seq %d, %s merged at %d: want it after", c.next, disp.Seq, c.prev, merged.Seq)
		}
	}
	// after_merge step list: a keep lane's merged task stops no agent.
	for _, task := range []string{"T-1", "T-2"} {
		if _, ok := r.record(task, event.KindIntent, event.StepStop); ok {
			t.Errorf("%s ran agent.stop after its merge", task)
		}
	}
	// T-1 and T-2: one session, their wakes typed; T-3: the handoff rule's stop, then its own fresh start.
	for _, job := range []string{"T-1/1/work", "T-2/1/work"} {
		if !slices.ContainsFunc(r.prompts(), func(s sent) bool {
			return s.to == "pj-l-01" && s.text == "[obor job "+job+"] call the obor job_next tool"
		}) {
			t.Errorf("%s's wake was not typed into pj-l-01's session", job)
		}
	}
	for _, task := range []string{"T-1", "T-2"} {
		if _, ok := r.record(task, event.KindIntent, event.StepStartWork); ok {
			t.Errorf("%s started an agent: its keep lane had a live one", task)
		}
	}
	if got := r.workerStops(); !slices.Equal(got, []string{"pj-l-01 w1:p1"}) {
		t.Errorf("worker stops %q, want pj-l-01's one handoff stop", got)
	}
	r.mu.Lock()
	launches := slices.Clone(r.launches)
	r.mu.Unlock()
	var worker []port.Launch
	for _, l := range launches {
		if l.Agent == "pj-l-01" {
			worker = append(worker, l)
		}
	}
	if len(worker) != 1 || !worker[0].Fresh || worker[0].Prompt != "[obor job T-3/1/work] call the obor job_next tool" {
		t.Errorf("pj-l-01's launches %+v, want T-3's one fresh start carrying its wake", worker)
	}
	if _, ok := r.record("T-3", event.KindResult, event.StepStartWork); !ok {
		t.Error("T-3 has no agent.start result")
	}
	r.disp.mu.Lock()
	session := r.disp.session
	r.disp.mu.Unlock()
	if session != 1 {
		t.Errorf("pj-l-01's session %d after T-3's start, want 1 (one new session)", session)
	}
	r.waitFor("T-3 working", func() bool { return r.stateOf("T-3") == "working" })
	if q := r.app.Status().Project.Queues["L-01"]; len(q) != 0 {
		t.Errorf("L-01's queue %q after T-3's dispatch, want empty", q)
	}
}

// T-1031: a fresh lane (lanes as a count) still stops its merged task's agent: after_merge, then agent.stop.
func TestFreshLaneStillStopsAfterMerge(t *testing.T) {
	t.Parallel()
	r := newLoopRig(t, dispatching(), started())
	if reply, err := r.dispatchTo("d1", "T-1", "L-01"); err != nil || reply != "DISPATCHED T-1 → L-01 (queued) as sonnet" {
		t.Fatalf("T-1: %q, %v", reply, err)
	}
	r.waitJob("pj-l-01", "T-1/1/work")
	r.runToMerge("T-1", nil)
	r.waitFor("T-1's agent.stop", func() bool {
		_, ok := r.record("T-1", event.KindResult, event.StepStop)
		return ok
	})
	// a dispatch naming the busy fresh lane is still refused, not queued
	r.setLane(func(l *port.Lane) { l.Task, l.State = "T-1", "working" })
	if reply, err := r.dispatchTo("d2", "T-2", "L-01"); err == nil || reply != "" ||
		!strings.Contains(err.Error(), "lane L-01 is working on T-1") {
		t.Errorf("a dispatch naming a busy fresh lane: %q, %v; want refused", reply, err)
	}
	if got := fmt.Sprint(r.app.Status().Project.Queues); got != "map[]" {
		t.Errorf("queues %s, want none", got)
	}
}
