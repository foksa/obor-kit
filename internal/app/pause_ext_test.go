package app_test

import (
	"strings"
	"testing"
	"time"

	"obor/internal/app/event"
	"obor/internal/app/wire"
)

// T-465 (docs/state-model.md row 46): obor pause records fact paused; while it holds a dispatch is refused with its
// reason and a waiting task stays waiting; --clear records unpaused and the waiting task dispatches; status shows the
// pause in force; a repeated call id gets the stored reply; a worker cannot pause.
func TestPauseGatesDispatch(t *testing.T) {
	t.Parallel()
	r := startWithT2(t)
	if _, err := r.pause("worker", "p0", wire.PauseReq{Why: "no"}); err == nil || !strings.Contains(err.Error(),
		"the lead or the owner") {
		t.Errorf("a worker's pause: %v, want refused", err)
	}
	if reply, err := r.pause("", "c0", wire.PauseReq{Clear: true}); err != nil || reply != "NOT PAUSED" {
		t.Errorf("clear with no pause: %q, %v", reply, err)
	}
	if reply, err := r.pause("", "p1", wire.PauseReq{}); err == nil {
		t.Errorf("a pause without a reason: %q, want refused", reply)
	}
	reply, err := r.pause("lead", "p2", wire.PauseReq{Why: "quota"})
	if err != nil || reply != "PAUSED until cleared" {
		t.Fatalf("pause: %q, %v", reply, err)
	}
	if reply, err := r.pause("lead", "p2", wire.PauseReq{Why: "other"}); err != nil || reply != "PAUSED until cleared" {
		t.Errorf("the same call id again: %q, %v; want the stored reply", reply, err)
	}
	if n := r.taskStep(event.PauseTask, "fact paused"); n != 1 {
		t.Errorf("paused facts %d, want 1: %q", n, r.steps())
	}
	ps := r.app.Status().Project.Paused
	if ps == nil || ps.Why != "quota" || ps.Until != 0 || ps.Caller != "pj-lead" {
		t.Fatalf("status paused %+v, want quota until cleared by pj-lead", ps)
	}

	// A plain dispatch is refused with the reason; a dispatch --after too (nothing recorded).
	if _, err := r.dispatch("d1", "L-01"); err == nil || !strings.Contains(err.Error(), "paused: quota") {
		t.Errorf("dispatch while paused: %v, want refused with the reason", err)
	}
	if _, err := r.waitAfter("d2", "T-2"); err == nil || !strings.Contains(err.Error(), "paused: quota") {
		t.Errorf("dispatch --after while paused: %v, want refused", err)
	}
	if n := r.taskStep("T-1", "fact dispatch") + r.taskStep("T-1", "fact waiting"); n != 0 {
		t.Fatalf("T-1 recorded %d dispatch/waiting facts while paused: %q", n, r.steps())
	}

	// A task waiting before the pause stays waiting through a merge of its dependency while paused.
	if reply, err := r.pause("", "c1", wire.PauseReq{Clear: true}); err != nil || reply != "PAUSE CLEARED" {
		t.Fatalf("clear: %q, %v", reply, err)
	}
	if reply, err := r.waitAfter("d3", "T-2"); err != nil || !strings.HasPrefix(reply, "WAITING T-1") {
		t.Fatalf("dispatch --after unpaused: %q, %v", reply, err)
	}
	if _, err := r.pause("", "p3", wire.PauseReq{Why: "night",
		Until: r.clock.Now().Add(time.Hour).Unix()}); err != nil {
		t.Fatal(err)
	}
	if ps := r.app.Status().Project.Paused; ps == nil || ps.Caller != "owner" || ps.Until == 0 {
		t.Errorf("status paused %+v, want the owner's, with an end", ps)
	}
	r.landT2()
	r.passes()
	if st := r.taskStatus("T-1"); st.State != "waiting" || r.taskStep("T-1", "fact dispatch") != 0 {
		t.Fatalf("T-1 %+v after its dependency merged while paused, want still waiting: %q", st, r.steps())
	}
	if reply, err := r.pause("lead", "c2", wire.PauseReq{Clear: true}); err != nil || reply != "PAUSE CLEARED" {
		t.Fatalf("clear: %q, %v", reply, err)
	}
	r.waitFor("T-1 dispatched after the clear", func() bool { return r.taskStep("T-1", "fact dispatch") == 1 })
	if ps := r.app.Status().Project.Paused; ps != nil {
		t.Errorf("status paused %+v after the clear, want none", ps)
	}
}

// T-465: a pause survives a restart (the fact is folded on replay); a pause past its end no longer holds.
func TestPauseReplayAndExpiry(t *testing.T) {
	t.Parallel()
	r := newLoopRig(t, dispatching())
	r.start()
	if _, err := r.pause("", "p1", wire.PauseReq{Why: "kept"}); err != nil {
		t.Fatal(err)
	}
	r.stop()
	r.start()
	if ps := r.app.Status().Project.Paused; ps == nil || ps.Why != "kept" {
		t.Fatalf("paused after the restart: %+v, want kept", ps)
	}
	if _, err := r.dispatch("d1", "L-01"); err == nil || !strings.Contains(err.Error(), "paused: kept") {
		t.Errorf("dispatch after the restart: %v, want refused", err)
	}
	if _, err := r.pause("", "p2", wire.PauseReq{Why: "past",
		Until: r.clock.Now().Add(-time.Minute).Unix()}); err == nil {
		t.Errorf("a pause ending in the past: want refused")
	}
	if _, err := r.pause("", "p3", wire.PauseReq{Why: "short",
		Until: r.clock.Now().Add(time.Hour).Unix()}); err != nil {
		t.Fatal(err)
	}
	if ps := r.app.Status().Project.Paused; ps == nil || ps.Why != "short" {
		t.Fatalf("paused %+v, want the new pause replacing the old", ps)
	}
	r.clock.Add(time.Hour)
	if ps := r.app.Status().Project.Paused; ps != nil {
		t.Fatalf("paused %+v past its end, want none", ps)
	}
	r.mustDispatch("d2")
}

// RV-49 #3: a --types dispatch under a held pause is refused with the reason and records nothing.
func TestPauseRefusesTypesDispatch(t *testing.T) {
	t.Parallel()
	r := startWithT2(t)
	if reply, err := r.pause("lead", "p1", wire.PauseReq{Why: "quota"}); err != nil || reply != "PAUSED until cleared" {
		t.Fatalf("pause: %q, %v", reply, err)
	}
	_, err := r.dispatchCall(wire.DispatchReq{Envelope: wire.Envelope{Root: r.p.Root, Agent: "pj-lead", Role: "lead",
		CallID: "t1"}, Task: "T-1", Types: []string{"sonnet"}})
	if err == nil || !strings.Contains(err.Error(), "paused: quota") {
		t.Fatalf("types dispatch while paused: %v, want refused with the reason", err)
	}
	if n := r.taskStep("T-1", "fact dispatch"); n != 0 {
		t.Fatalf("T-1 recorded %d dispatch facts while paused: %q", n, r.steps())
	}
}
