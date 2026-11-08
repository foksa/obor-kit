package app

import (
	"context"
	"encoding/json"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"obor/internal/app/event"
	"obor/internal/app/port"
	"obor/internal/app/wire"
	"obor/internal/store"
)

// #4: a waiting task has no lane, so its needs-you names the task in the drop command, never <LANE>.
func TestWaitingNeedsYouDropCommand(t *testing.T) {
	t.Parallel()
	raw, _ := json.Marshal(event.FactData{Merge: true, Cause: causeAfter, Reason: "after: T-8 waits for T-1, which is dropped"})
	r := event.Record{Seq: 1, Task: "T-8", Step: event.StepNeedsYou, Data: raw}
	path := answerPath(nil, r, mtask{task: "T-8", state: stWaiting, needs: block{fact: r}})
	t.Log(path)
	if strings.Contains(path, "<LANE>") {
		t.Error("waiting task has no lane; advertised drop command cannot run")
	}
}

func TestHeldLaneOfTwoDispatchesIsNewestDispatch(t *testing.T) {
	t.Parallel()
	for range orderTries {
		p := twoInLane()
		if !p.folded.loop["T-1"].dispatching() || p.folded.loop["T-2"].dispSeq <= p.folded.loop["T-1"].dispSeq {
			t.Fatal("precondition: both dispatches hold L1, T-2's the newer, T-1 the first by name")
		}
		mt, held := (&App{}).held(p, "T-3")
		if mt.task != "" || len(held) != 1 || held["L1"] != "T-2" {
			t.Fatalf("held = %q %v; want no record of T-3 and L1 held by T-2", mt.task, held)
		}
	}
}

// TestPickLaneTakesALandedCleanLane (T-1012): dispatch picks by the one free rule's state: of lanes whose states are
// port.FreeState over their rows, a fresh dispatch takes the first whose task landed and that is clean, skips one whose
// task has not landed (working) and one landed but dirty (dirty-free) unless forced, then the dirty-free one first in
// config order.
func TestPickLaneTakesALandedCleanLane(t *testing.T) {
	t.Parallel()
	lane := func(name string, row store.LaneRow, dirty bool) port.Lane {
		return port.Lane{Name: name, Dir: "/wt/" + name, Task: row.Task, Dirty: dirty,
			State: port.FreeState(&row, "h", dirty, nil)} // each lane's HEAD its submitted head h
	}
	ls := []port.Lane{
		lane("L-01", store.LaneRow{Lane: "L-01", Task: "T-1", Head: "h"}, false),               // not landed: working
		lane("L-02", store.LaneRow{Lane: "L-02", Task: "T-2", Head: "h", Landed: true}, true),  // landed, dirty
		lane("L-03", store.LaneRow{Lane: "L-03", Task: "T-3", Head: "h", Landed: true}, false), // landed, clean
	}
	if ls[0].State != port.LaneWorking || ls[1].State != port.LaneDirtyFree || ls[2].State != port.LaneFree {
		t.Fatalf("precondition: states %s, %s, %s; want working, dirty-free, free", ls[0].State, ls[1].State,
			ls[2].State)
	}
	c := wire.DispatchReq{Task: "T-9", Types: []string{"sonnet"}}
	if l, ref, why := pickLane(ls, c, map[string]string{}, map[string]string{}, "", false); l.Name != "L-03" ||
		ref != "sonnet" || why != "" {
		t.Errorf("pickLane = %s, %q, %q; want L-03 (landed, clean) as sonnet", l.Name, ref, why)
	}
	c.Force = true
	if l, _, why := pickLane(ls, c, map[string]string{}, map[string]string{}, "", false); l.Name != "L-02" || why != "" {
		t.Errorf("pickLane --force = %s, %q; want L-02 (landed, dirty: the first in config order with force)", l.Name,
			why)
	}
	c.Force = false
	if l, _, why := pickLane(ls[:2], c, map[string]string{}, map[string]string{}, "", false); l.Name != "" || why == "" {
		t.Errorf("pickLane without a landed clean lane = %s, %q; want no lane and why", l.Name, why)
	}
}

// RV-56 #1: a waiting task's step selected for one request (App.next) does nothing once an obor dispatch replaced that
// request (same attempt, a new call id): the next pass picks the request in force.

type staleFx struct{}

func (staleFx) Check(context.Context, string, string) (string, error) { return "", nil }

func (staleFx) Target(context.Context, string) (string, bool, error) { return "base", false, nil }

func (staleFx) Prepare(context.Context, port.PrepareInput) (port.Prepared, error) {
	return port.Prepared{}, nil
}

func (staleFx) Free(context.Context, port.FreeInput) (string, error) { return "free", nil }

// newStaleRig is pj with the lanes L-01 and L-02, both working on other tasks, specs T-1..T-3 of level medium (sonnet,
// deepseek), dispatching on.
func newStaleRig(t *testing.T) (*harness, *Loop) {
	h := newHarness(t)
	h.write(filepath.Join(h.root, ".obor/config.json"), `{"name":"pj","prefix":"pj","roles":{"integrator":""},"lanes":2}`)
	h.lanes.lanes = []port.Lane{{Name: "L-01", Dir: h.lane("L-01"), State: "working", Task: "T-9"},
		{Name: "L-02", Dir: h.lane("L-02"), State: "working", Task: "T-8"}}
	fx := &Loop{Workflow: WorkflowOf(staleFx{}), Specs: staleFx{}}
	h.a.opt.Loop = func(string) (*Loop, error) { return fx, nil }
	h.connect()
	h.a.p.dispatching = true
	for _, task := range []string{"T-1", "T-2", "T-3"} {
		if _, _, err := store.SetSpec(h.a.p.log.DB(), task, "# "+task+" — Test\n\nDifficulty: medium\n", "", "lead", h.now); err != nil {
			t.Fatal(err)
		}
	}
	return h, fx
}

func TestWaitStaleSelectionDispatchesNothing(t *testing.T) {
	t.Parallel()
	h, fx := newStaleRig(t)
	defer h.a.closeLog()
	h.waitCall(wire.DispatchReq{Envelope: wire.Envelope{CallID: "d1"}}) // level medium: waits for a sonnet or deepseek lane
	stale := h.selected()
	if stale == nil {
		t.Fatal("no step selected for d1")
	}
	h.waitCall(wire.DispatchReq{Envelope: wire.Envelope{CallID: "d2"}, Types: []string{"deepseek"},
		After: []string{"T-2"}})
	if m := h.t1(); m.disp.Call != "d2" || m.attempt != 1 {
		t.Fatalf("replacement not in force: %+v", m.disp)
	}
	h.freeLanes()
	if stale(context.Background(), fx) {
		t.Error("the stale step reported progress")
	}
	if m := h.t1(); m.state != stWaiting || m.disp.Call != "d2" || h.count(event.KindFact, event.StepDispatched) != 0 {
		t.Fatalf("the stale step dispatched: state %s call %s lane %s", m.state, m.disp.Call, m.lane)
	}
	if s := h.selected(); s != nil && s(context.Background(), fx) || h.count(event.KindFact, event.StepDispatched) != 0 {
		t.Fatal("d2 dispatched before T-2 merged")
	}
	h.fact("T-2", event.StepSubmitted, map[string]string{"lane": "L-09"}) // T-2 in the loop, then merged
	h.fact("T-2", event.StepMerged, event.MergedData{Outside: true})
	s := h.selected()
	if s == nil || !s(context.Background(), fx) {
		t.Fatal("d2 did not dispatch once T-2 merged")
	}
	if m := h.t1(); m.state != stDispatched || m.lane != "L-01" || m.disp.Call != "d2" ||
		!slices.Equal(m.disp.Types, []string{"deepseek"}) || m.disp.Type != "deepseek" {
		t.Errorf("d2 dispatched as state %s lane %s call %s types %v; want deepseek on L-01 for d2", m.state, m.lane,
			m.disp.Call, m.disp.Types)
	}
}

func TestWaitStaleDependencyFailedRecordsNothing(t *testing.T) {
	t.Parallel()
	h, fx := newStaleRig(t)
	defer h.a.closeLog()
	h.waitCall(wire.DispatchReq{Envelope: wire.Envelope{CallID: "d1"}, After: []string{"T-2"}})
	h.fact("T-2", event.StepSubmitted, map[string]string{"lane": "L-09"}) // T-2 in the loop, then dropped
	h.fact("T-2", event.StepDropped, event.FactData{Reason: "gone"})
	stale := h.selected() // needs-you{after: T-2 dropped}
	if stale == nil {
		t.Fatal("no step selected for d1 with T-2 dropped")
	}
	h.waitCall(wire.DispatchReq{Envelope: wire.Envelope{CallID: "d2"}, After: []string{"T-3"}})
	if stale(context.Background(), fx) {
		t.Error("the stale step reported progress")
	}
	if n := h.count(event.KindFact, event.StepNeedsYou); n != 0 {
		t.Errorf("the stale step recorded %d needs-you", n)
	}
	if m := h.t1(); m.state != stWaiting || m.disp.Call != "d2" {
		t.Errorf("T-1 %s for %s; want waiting for d2", m.state, m.disp.Call)
	}
}

func TestWaitStaleRefusalRecordsNothing(t *testing.T) {
	t.Parallel()
	h, fx := newStaleRig(t)
	defer h.a.closeLog()
	h.waitCall(wire.DispatchReq{Envelope: wire.Envelope{CallID: "d1"}, Lane: "L-01", After: []string{"T-2"}})
	h.fact("T-2", event.StepSubmitted, map[string]string{"lane": "L-09"}) // T-2 in the loop, then merged
	h.fact("T-2", event.StepMerged, event.MergedData{Outside: true})
	stale := h.selected() // waitDispatch: L-01 is working on T-9
	if stale == nil {
		t.Fatal("no step selected for d1 with T-2 merged")
	}
	h.waitCall(wire.DispatchReq{Envelope: wire.Envelope{CallID: "d2"}, After: []string{"T-3"}})
	// The refusal d1 would meet has held for over an hour: a current step escalates it to needs-you.
	held := waitRefusal{line: "dispatch T-1: lane L-01 is working on T-9", since: h.now.Add(-2 * time.Hour)}
	h.a.mu.Lock()
	h.a.p.waitLine["T-1"] = held
	h.a.mu.Unlock()
	if stale(context.Background(), fx) {
		t.Error("the stale step reported progress")
	}
	h.a.mu.Lock()
	w, ok := h.a.p.waitLine["T-1"]
	h.a.mu.Unlock()
	if n := h.count(event.KindFact, event.StepNeedsYou); n != 0 || !ok || w != held {
		t.Errorf("the stale step's bookkeeping: needs-you %d, refusal %+v (%v); want none, %+v kept", n, w, ok, held)
	}
	if m := h.t1(); m.state != stWaiting || m.disp.Call != "d2" {
		t.Errorf("T-1 %s for %s; want waiting for d2", m.state, m.disp.Call)
	}
}

func TestWaitSelectionNotReplacedDispatches(t *testing.T) {
	t.Parallel()
	h, fx := newStaleRig(t)
	defer h.a.closeLog()
	h.waitCall(wire.DispatchReq{Envelope: wire.Envelope{CallID: "d1"}})
	s := h.selected()
	if s == nil {
		t.Fatal("no step selected for d1")
	}
	h.freeLanes()
	if !s(context.Background(), fx) {
		t.Fatal("d1 did not dispatch on a free lane")
	}
	if m := h.t1(); m.state != stDispatched || m.lane != "L-01" || m.disp.Call != "d1" {
		t.Errorf("d1 dispatched as state %s lane %s call %s; want L-01 (sonnet, the first model) for d1", m.state, m.lane,
			m.disp.Call)
	}
}
