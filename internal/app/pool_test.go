package app_test

import (
	"context"
	"encoding/json"
	"obor/internal/app"
	"obor/internal/app/event"
	"obor/internal/app/port"
	"obor/internal/app/wire"
	"strings"
	"testing"
	"time"

	"obor/internal/testkit"
)

// T-723: lanes are a pool. For each type of a request in order (the spec's Type:, then the level's), a type at its
// lane_types max is skipped, else the first free lane whose type is it or empty takes it: an untyped lane runs any
// type, a typed lane only its own (a reservation). The cap counts the open dispatches whose row type is the type plus
// the role agents configured with it. A freed lane goes to the waiting tasks oldest first, skipping those at cap.
//
// The rig: lane types opus (max 1) and sonnet (no max); lanes O-01 and O-02, both untyped and free.

func newPoolRig(t *testing.T, edits ...func(map[string]any)) *loopRig {
	t.Helper()
	edits = append([]func(map[string]any){
		testkit.Set("lane_types.opus", map[string]any{"kind": "claude", "args": []any{}, "max": 1}),
		testkit.Set("lane_types.sonnet", map[string]any{"kind": "claude"}),
		testkit.Set("lanes.O-01", map[string]any{"type": "", "agent": "pj-o1"}),
		testkit.Set("lanes.O-02", ""),
	}, edits...)
	r := newDispatchRig(t, edits...)
	r.setLane(func(l *port.Lane) { l.Type = "" })
	r.lanes.mu.Lock()
	r.lanes.lanes = append(r.lanes.lanes, port.Lane{Name: "O-02", Dir: r.p.LaneDir("O-02"), State: "free"})
	r.lanes.mu.Unlock()
	return r
}

// specOf stores task's spec (obor spec set).
func (r *loopRig) specOf(task, text string) {
	r.t.Helper()
	if _, err := wire.Verdict(context.Background(), r.sock, wire.MethodSpecSet, wire.Call{Root: r.p.Root, Agent: "lead",
		Role: "lead", CallID: "s-" + task, Task: task, Spec: text}); err != nil {
		r.t.Fatalf("spec set %s: %v", task, err)
	}
}

// dispatchOf is the lead's dispatch of task by its spec's level.
func (r *loopRig) dispatchOf(task string) (string, error) {
	return r.dispatchCall(wire.Call{Root: r.p.Root, Agent: "pj-lead", Role: "lead", CallID: "d-" + task, Task: task})
}

// seedOpenOpus writes T-2's open dispatch of type opus on O-01 (it holds O-01) to the log of the stopped app, and more
// after it.
func (r *loopRig) seedOpenOpus(more ...event.Record) {
	r.t.Helper()
	d, _ := json.Marshal(map[string]any{"lane": "O-01", "agent": "pj-o1", "type": "opus:high", "level": "hard",
		"types": []string{"opus:high"}})
	recs := append([]event.Record{{Task: "T-2", Attempt: 1, Step: event.StepDispatched, Kind: event.KindFact, Data: d}}, more...)
	if err := app.WriteLog(r.p.Root, time.Now(), recs...); err != nil {
		r.t.Fatal(err)
	}
}

// setPoolLane sets lane i of the rig's table to state and task.
func (r *loopRig) setPoolLane(i int, state, task string) {
	r.lanes.mu.Lock()
	r.lanes.lanes[i].State, r.lanes.lanes[i].Task = state, task
	r.lanes.mu.Unlock()
}

func TestPoolPicksALaneAndAType(t *testing.T) {
	for _, c := range []struct {
		name    string
		edits   []func(map[string]any)
		lanes   func(r *loopRig)
		seed    func(r *loopRig)
		spec    string
		want    string // the reply's prefix
		lane    string // the dispatch fact's lane ("": no dispatch)
		typ     string // its row type
		waiting string // the waiting fact's reply ("": none)
	}{{
		name: "an untyped lane takes a sonnet task",
		spec: "Difficulty: medium\n", want: "DISPATCHED T-1 → O-01 (queued) as sonnet", lane: "O-01", typ: "sonnet",
	}, {
		name: "an untyped lane takes an opus task at its effort",
		spec: "Difficulty: hard\n", want: "DISPATCHED T-1 → O-01 (queued) as opus effort high", lane: "O-01", typ: "opus:high",
	}, {
		name:  "a typed lane takes only its type: a sonnet task skips the free opus lane for the pool's",
		edits: []func(map[string]any){testkit.Set("lanes.O-01", map[string]any{"type": "opus", "agent": "pj-o1"})},
		lanes: func(r *loopRig) { r.setLane(func(l *port.Lane) { l.Type = "opus" }) },
		spec:  "Difficulty: medium\n", want: "DISPATCHED T-1 → O-02 (queued) as sonnet", lane: "O-02", typ: "sonnet",
	}, {
		name:  "a typed lane takes its own type; the reply names no type",
		edits: []func(map[string]any){testkit.Set("lanes.O-01", map[string]any{"type": "opus", "agent": "pj-o1"})},
		lanes: func(r *loopRig) { r.setLane(func(l *port.Lane) { l.Type = "opus" }) },
		spec:  "Difficulty: hard\n", want: "DISPATCHED T-1 → O-01 (queued) effort high;", lane: "O-01", typ: "opus:high",
	}, {
		name:  "a typed lane only its type: with the pool busy a sonnet task waits beside the idle opus lane",
		edits: []func(map[string]any){testkit.Set("lanes.O-01", map[string]any{"type": "opus", "agent": "pj-o1"})},
		lanes: func(r *loopRig) {
			r.setLane(func(l *port.Lane) { l.Type = "opus" })
			r.setPoolLane(1, "working", "T-9")
		},
		spec: "Difficulty: medium\n", want: "WAITING T-1 (queued): waiting for a free lane (medium: sonnet); idle: O-01 (opus)",
		waiting: "WAITING T-1 (queued): waiting for a free lane (medium: sonnet); idle: O-01 (opus)",
	}, {
		name: "opus at its max by an open dispatch: the next type",
		seed: func(r *loopRig) { r.seedOpenOpus() },
		spec: "Difficulty: medium\nType: opus\n", want: "DISPATCHED T-1 → O-02 (queued) as sonnet", lane: "O-02", typ: "sonnet",
	}, {
		name: "opus at its max by an open dispatch: no next type, it waits",
		seed: func(r *loopRig) { r.seedOpenOpus() },
		spec: "Difficulty: hard\n", want: "WAITING T-1 (queued): opus at its max 1 (hard: opus:high); idle: O-02 (-)",
		waiting: "WAITING T-1 (queued): opus at its max 1 (hard: opus:high); idle: O-02 (-)",
	}, {
		name:  "opus at its max by a role agent configured with it: it waits",
		edits: []func(map[string]any){testkit.Set("roles.reviewer", map[string]any{"type": "opus"})},
		spec:  "Difficulty: hard\n", want: "WAITING T-1 (queued): opus at its max 1 (hard: opus:high)",
		waiting: "WAITING T-1 (queued): opus at its max 1 (hard: opus:high); idle: O-01 (-), O-02 (-)",
	}, {
		name: "a worker stopped on a usage limit still counts against its cap",
		seed: func(r *loopRig) {
			stop, _ := json.Marshal(event.FactData{Lane: "O-01", Reason: "stopped: usage limit", Merge: true, Cause: "stopped",
				Limit: true})
			r.seedOpenOpus(event.Record{Task: "T-2", Attempt: 1, Step: event.StepNeedsYou, Kind: event.KindFact, Data: stop})
		},
		spec: "Difficulty: hard\n", want: "WAITING T-1 (queued): opus at its max 1 (hard: opus:high)",
		waiting: "WAITING T-1 (queued): opus at its max 1 (hard: opus:high); idle: O-02 (-)",
	}, {
		name: "sonnet has no max: it fills the pool beside an open opus dispatch",
		seed: func(r *loopRig) { r.seedOpenOpus() },
		spec: "Difficulty: medium\n", want: "DISPATCHED T-1 → O-02 (queued) as sonnet", lane: "O-02", typ: "sonnet",
	}} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			r := newPoolRig(t, c.edits...)
			if c.lanes != nil {
				c.lanes(r)
			}
			if c.seed != nil {
				c.seed(r)
			}
			r.start()
			if c.seed != nil {
				r.waitFor("T-2 folded", func() bool { return r.taskStatus("T-2").Task == "T-2" })
			}
			r.specOf("T-1", "# T-1 — Pooled\n\n"+c.spec)
			reply, err := r.dispatchOf("T-1")
			if err != nil || !strings.HasPrefix(reply, c.want) {
				t.Fatalf("dispatch: %q, %v; want the prefix %q", reply, err, c.want)
			}
			if c.lane != "" {
				if d := r.factData("T-1", event.StepDispatched); d["lane"] != c.lane || d["type"] != c.typ {
					t.Errorf("dispatch fact %v, want lane %s, type %s", d, c.lane, c.typ)
				}
			} else if n := r.taskStep("T-1", "fact dispatch"); n != 0 {
				t.Errorf("dispatched although it should wait: %q", r.steps())
			}
			if c.waiting != "" {
				if d := r.factData("T-1", event.StepWaiting); d["reply"] != c.waiting {
					t.Errorf("waiting fact's reply %q, want %q", d["reply"], c.waiting)
				}
				r.passes()
				if n := r.taskStep("T-1", "fact dispatch"); n != 0 || r.taskStatus("T-1").State != "waiting" {
					t.Errorf("after a pass T-1 is %+v (%q), want still waiting", r.taskStatus("T-1"), r.steps())
				}
			} else if n := r.taskStep("T-1", "fact waiting"); n != 0 {
				t.Errorf("waited although it should dispatch: %q", r.steps())
			}
		})
	}
}

// TestPoolFreedLaneGoesToTheOldestWaitingTaskNotAtItsCap: T-2's open opus dispatch holds O-01; three tasks wait for
// O-02 (busy with T-9): T-1 (hard: opus, at its max by T-2) first, then T-5, then T-3 (both medium: sonnet). Once O-02
// frees, T-5 (the oldest not at its cap, though T-3 sorts first by name) gets it; T-1 waits with its reason, T-3 waits
// for a free lane.
func TestPoolFreedLaneGoesToTheOldestWaitingTaskNotAtItsCap(t *testing.T) {
	t.Parallel()
	r := newPoolRig(t)
	r.setPoolLane(1, "working", "T-9")
	r.seedOpenOpus()
	r.start()
	r.waitFor("T-2 folded", func() bool { return r.taskStatus("T-2").Task == "T-2" })
	for _, w := range []struct{ task, level string }{{"T-1", "hard"}, {"T-5", "medium"}, {"T-3", "medium"}} {
		r.specOf(w.task, "# "+w.task+" — Waits\n\nDifficulty: "+w.level+"\n")
		if reply, err := r.dispatchOf(w.task); err != nil || !strings.HasPrefix(reply, "WAITING "+w.task+" (queued)") {
			t.Fatalf("dispatch %s with every lane busy: %q, %v; want it waiting", w.task, reply, err)
		}
	}
	r.passes()
	for _, task := range []string{"T-1", "T-3", "T-5"} {
		if st := r.taskStatus(task); st.State != "waiting" {
			t.Fatalf("%s is %+v before a lane frees, want waiting", task, st)
		}
	}
	r.setPoolLane(1, "free", "")
	r.waitFor("a dispatch onto the freed O-02", func() bool {
		return r.taskStep("T-1", "fact dispatch")+r.taskStep("T-3", "fact dispatch")+r.taskStep("T-5", "fact dispatch") > 0
	})
	r.passes()
	if d := r.factData("T-5", event.StepDispatched); d["lane"] != "O-02" || d["type"] != "sonnet" {
		t.Errorf("T-5's dispatch %v, want O-02 as sonnet", d)
	}
	if n := r.taskStep("T-1", "fact dispatch") + r.taskStep("T-3", "fact dispatch"); n != 0 {
		t.Errorf("T-1 or T-3 dispatched too: %q", r.steps())
	}
	if st := r.taskStatus("T-1"); st.State != "waiting" || st.Why != "opus at its max 1" {
		t.Errorf("T-1 %+v, want waiting with the reason opus at its max 1", st)
	}
	if st := r.taskStatus("T-3"); st.State != "waiting" || st.Why != app.LaneWaitFree {
		t.Errorf("T-3 %+v, want waiting for a free lane", st)
	}
}
