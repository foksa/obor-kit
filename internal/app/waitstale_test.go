package app

import (
	"context"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"obor/internal/store"
)

// RV-56 #1: a waiting task's step selected for one request (App.next) does nothing once an obor dispatch replaced that
// request (same attempt, a new call id): the next pass picks the request in force.

type staleFx struct{}

func (staleFx) Check(context.Context, string, string) (string, error)   { return "", nil }
func (staleFx) Target(context.Context, string) (string, bool, error)    { return "base", false, nil }
func (staleFx) Prepare(context.Context, PrepareInput) (Prepared, error) { return Prepared{}, nil }
func (staleFx) Free(context.Context, FreeInput) (string, error)         { return "free", nil }

// newStaleRig is pj with the lane types sonnet (S-01) and deepseek (D-01), both working on other tasks, specs T-1..T-3
// of level medium (sonnet, deepseek), dispatching on.
func newStaleRig(t *testing.T) (*harness, *Loop) {
	h := newHarness(t)
	h.write(filepath.Join(h.root, ".obor/config.json"), `{"name":"pj","prefix":"pj","roles":{"integrator":{}},`+
		`"lane_types":{"sonnet":{"kind":"claude"},"deepseek":{"kind":"claude"}},"lanes":{"S-01":"sonnet","D-01":"deepseek"}}`)
	h.lanes.lanes = []Lane{{Name: "S-01", Dir: h.lane("S-01"), Type: "sonnet", State: "working", Task: "T-9"},
		{Name: "D-01", Dir: h.lane("D-01"), Type: "deepseek", State: "working", Task: "T-8"}}
	fx := &Loop{Lane: staleFx{}, Specs: staleFx{}}
	h.a.opt.Loop = func(string) (*Loop, error) { return fx, nil }
	h.connect()
	h.a.p.dispatching = true
	for _, task := range []string{"T-1", "T-2", "T-3"} {
		if _, _, err := store.SetSpec(h.a.p.log.DB(), task, "# "+task+" — Test\n\nDifficulty: medium\n", "lead", h.now); err != nil {
			t.Fatal(err)
		}
	}
	return h, fx
}

// waitCall is the lead's dispatch of T-1 that must wait.
func (h *harness) waitCall(c Call) {
	h.t.Helper()
	c.Root, c.Task, c.Role = h.root, "T-1", "lead"
	if reply, err := h.a.dispatchCall(context.Background(), c); err != nil || !strings.HasPrefix(reply, "WAITING") {
		h.t.Fatalf("dispatch %s: %q, %v; want WAITING", c.CallID, reply, err)
	}
}

// selected is T-1's next step now (App.next, with its begin/release wrapper).
func (h *harness) selected() step {
	h.t.Helper()
	h.a.mu.Lock()
	defer h.a.mu.Unlock()
	return h.a.next(h.a.p, h.a.p.folded.loop["T-1"])
}

func (h *harness) freeLanes() {
	for i := range h.lanes.lanes {
		h.lanes.lanes[i].State, h.lanes.lanes[i].Task = "free", ""
	}
}

// fact records a fact of task (folded).
func (h *harness) fact(task, step string, data any) {
	h.t.Helper()
	if !h.a.record(h.a.p, Record{Task: task, Attempt: 1, Step: step, Kind: KindFact}, data) {
		h.t.Fatalf("fact %s of %s refused", step, task)
	}
}

func (h *harness) t1() mtask {
	h.a.mu.Lock()
	defer h.a.mu.Unlock()
	return *h.a.p.folded.loop["T-1"]
}

func TestWaitStaleSelectionDispatchesNothing(t *testing.T) {
	t.Parallel()
	h, fx := newStaleRig(t)
	defer h.a.closeLog()
	h.waitCall(Call{CallID: "d1"}) // level medium: waits for a sonnet or deepseek lane
	stale := h.selected()
	if stale == nil {
		t.Fatal("no step selected for d1")
	}
	h.waitCall(Call{CallID: "d2", Types: []string{"deepseek"}, After: []string{"T-2"}})
	if m := h.t1(); m.disp.Call != "d2" || m.attempt != 1 {
		t.Fatalf("replacement not in force: %+v", m.disp)
	}
	h.freeLanes()
	if stale(context.Background(), fx) {
		t.Error("the stale step reported progress")
	}
	if m := h.t1(); m.state != stWaiting || m.disp.Call != "d2" || h.count(KindFact, StepDispatched) != 0 {
		t.Fatalf("the stale step dispatched: state %s call %s lane %s", m.state, m.disp.Call, m.lane)
	}
	if s := h.selected(); s != nil && s(context.Background(), fx) || h.count(KindFact, StepDispatched) != 0 {
		t.Fatal("d2 dispatched before T-2 merged")
	}
	h.fact("T-2", StepSubmitted, map[string]string{"lane": "O-09"}) // T-2 in the loop, then merged
	h.fact("T-2", StepMerged, mergedData{Outside: true})
	s := h.selected()
	if s == nil || !s(context.Background(), fx) {
		t.Fatal("d2 did not dispatch once T-2 merged")
	}
	if m := h.t1(); m.state != stDispatched || m.lane != "D-01" || m.disp.Call != "d2" ||
		!slices.Equal(m.disp.Types, []string{"deepseek"}) {
		t.Errorf("d2 dispatched as state %s lane %s call %s types %v; want D-01 (deepseek) for d2", m.state, m.lane,
			m.disp.Call, m.disp.Types)
	}
}

func TestWaitStaleDependencyFailedRecordsNothing(t *testing.T) {
	t.Parallel()
	h, fx := newStaleRig(t)
	defer h.a.closeLog()
	h.waitCall(Call{CallID: "d1", After: []string{"T-2"}})
	h.fact("T-2", StepSubmitted, map[string]string{"lane": "O-09"}) // T-2 in the loop, then dropped
	h.fact("T-2", StepDropped, FactData{Reason: "gone"})
	stale := h.selected() // needs-you{after: T-2 dropped}
	if stale == nil {
		t.Fatal("no step selected for d1 with T-2 dropped")
	}
	h.waitCall(Call{CallID: "d2", After: []string{"T-3"}})
	if stale(context.Background(), fx) {
		t.Error("the stale step reported progress")
	}
	if n := h.count(KindFact, StepNeedsYou); n != 0 {
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
	h.waitCall(Call{CallID: "d1", Lane: "S-01", After: []string{"T-2"}})
	h.fact("T-2", StepSubmitted, map[string]string{"lane": "O-09"}) // T-2 in the loop, then merged
	h.fact("T-2", StepMerged, mergedData{Outside: true})
	stale := h.selected() // waitDispatch: S-01 is working on T-9
	if stale == nil {
		t.Fatal("no step selected for d1 with T-2 merged")
	}
	h.waitCall(Call{CallID: "d2", After: []string{"T-3"}})
	// The refusal d1 would meet has held for over an hour: a current step escalates it to needs-you.
	held := waitRefusal{line: "dispatch T-1: lane S-01 is working on T-9", since: h.now.Add(-2 * time.Hour)}
	h.a.mu.Lock()
	h.a.p.waitLine["T-1"] = held
	h.a.mu.Unlock()
	if stale(context.Background(), fx) {
		t.Error("the stale step reported progress")
	}
	h.a.mu.Lock()
	w, ok := h.a.p.waitLine["T-1"]
	h.a.mu.Unlock()
	if n := h.count(KindFact, StepNeedsYou); n != 0 || !ok || w != held {
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
	h.waitCall(Call{CallID: "d1"})
	s := h.selected()
	if s == nil {
		t.Fatal("no step selected for d1")
	}
	h.freeLanes()
	if !s(context.Background(), fx) {
		t.Fatal("d1 did not dispatch on a free lane")
	}
	if m := h.t1(); m.state != stDispatched || m.lane != "S-01" || m.disp.Call != "d1" {
		t.Errorf("d1 dispatched as state %s lane %s call %s; want S-01 (sonnet, the first type) for d1", m.state, m.lane,
			m.disp.Call)
	}
}
