package app

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// T-612 (a sendback of T-612's check: TestOpenCodeStoppedReset flaked): a worker's stop that reaches serve before the
// watchdog's next look — its task dispatched since that look, so no watch of it yet — still records its needs-you with
// the task; the loop's open task stands in for the watch, as the next check's would.
func TestStoppedBeforeTheNextCheck(t *testing.T) {
	h := newHarness(t)
	h.write(filepath.Join(h.root, ".obor", "config.json"), `{"name":"pj","prefix":"pj","roles":{"integrator":{}}}`)
	f := &schedScratch{h: h}
	h.a.opt.Loop = func(string) (*Loop, error) { return &Loop{Scratch: f, Agents: f, Typer: f}, nil }
	if err := os.MkdirAll(h.lanes.lanes[0].Dir, 0o755); err != nil {
		t.Fatal(err)
	}
	h.lanes.lanes[0].Task = "" // the last check saw the lane free: the dispatch writes T-1 after it
	h.connect()
	h.lanes.lanes[0].Task = "T-1"
	addRecord(t, h, "T-1", StepDispatched, KindFact, dispatchData{Lane: "O-01", Dir: h.lane("O-01"), Agent: "pj-o-01",
		Branch: "task/T-1", Target: "b"})
	addRecord(t, h, "T-1", StepPrepare, KindResult, prepareOut{Base: "b"})
	if mt := h.a.p.folded.loop["T-1"]; mt.state != stDispatched || !mt.awaitsTaskDone() {
		t.Fatalf("precondition: T-1 %s; want dispatched and waiting on its worker", mt.state)
	}
	if h.a.p.tasks["T-1"] != nil {
		t.Fatal("precondition: T-1 has a watch; the case is a stop before the watchdog's next look")
	}

	reply, err := h.a.stoppedCall(Call{Root: h.root, Agent: "pj-o-01", Role: "worker", Lane: "O-01", CallID: "s1",
		Error: "rate_limit", Details: "429 Too Many Requests"})
	if err != nil || reply != "STOPPED pj-o-01 (needs-you T-1)" {
		t.Fatalf("stopped: %q, %v; want the task's needs-you", reply, err)
	}
	var d stoppedData
	recs, err := ReadLog(h.root)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range recs {
		if r.Task == MsgTask && r.Step == StepStopped {
			if err := json.Unmarshal(r.Data, &d); err != nil {
				t.Fatal(err)
			}
		}
	}
	if d.Task != "T-1" || d.NeedsYou == nil || d.NeedsYou.Cause != causeStopped || d.NeedsYou.Lane != "O-01" {
		t.Fatalf("stopped %+v; want T-1's needs-you in lane O-01", d)
	}
	if w := h.a.p.tasks["T-1"]; w == nil || w.lane.Name != "O-01" || w.lane.Task != "T-1" {
		t.Errorf("the stop left no watch of T-1 in O-01: %+v", w)
	}

	h.tick(time.Minute) // the check keeps the same watch and records nothing more
	n := 0
	if recs, err := ReadLog(h.root); err != nil {
		t.Fatal(err)
	} else {
		for _, r := range recs {
			if r.Task == MsgTask && r.Step == StepStopped {
				n++
			}
		}
	}
	if n != 1 || !h.a.p.stops("T-1") {
		t.Errorf("%d stopped facts, stop in force %v; want 1 and the stop", n, h.a.p.stops("T-1"))
	}
}

// T-617 (RV-66): a worker's stop, and its next turn clearing it, are recorded against the lane's current task — the
// one the loop has awaiting its task_done there — never an old watch: the lane's old task ended and T-1 was dispatched
// before the watchdog's next look, its watch of the old task still on the lane. No watch yet (T-612) still works; a
// watch of the current task is the one used.
func TestStoppedCurrentTaskNotAnOldWatch(t *testing.T) {
	old := func() *watch { return &watch{lane: Lane{Name: "O-01", Agent: "pj-o-01", Task: "T-0"}} }
	cur := func() *watch { return &watch{lane: Lane{Name: "O-01", Agent: "pj-o-01", Task: "T-1"}} }
	for _, c := range []struct {
		name  string
		watch func(p *proj) *watch // the watches on O-01 before each call; the one to keep (nil: a new one)
	}{
		{"old watch, new task", func(p *proj) *watch { p.tasks["T-0"] = old(); delete(p.tasks, "T-1"); return nil }},
		{"no watch yet", func(p *proj) *watch { delete(p.tasks, "T-0"); delete(p.tasks, "T-1"); return nil }},
		{"watch of the current task", func(p *proj) *watch { w := cur(); p.tasks["T-1"] = w; return w }},
	} {
		t.Run(c.name, func(t *testing.T) {
			h := newHarness(t)
			h.write(filepath.Join(h.root, ".obor", "config.json"), `{"name":"pj","prefix":"pj","roles":{"integrator":{}}}`)
			f := &schedScratch{h: h}
			h.a.opt.Loop = func(string) (*Loop, error) { return &Loop{Scratch: f, Agents: f, Typer: f}, nil }
			if err := os.MkdirAll(h.lanes.lanes[0].Dir, 0o755); err != nil {
				t.Fatal(err)
			}
			h.lanes.lanes[0].Task = "" // the last check saw the lane free (its old task over); T-1 dispatched after
			h.connect()
			h.lanes.lanes[0].Task = "T-1"
			addRecord(t, h, "T-1", StepDispatched, KindFact, dispatchData{Lane: "O-01", Dir: h.lane("O-01"),
				Agent: "pj-o-01", Branch: "task/T-1", Target: "b"})
			addRecord(t, h, "T-1", StepPrepare, KindResult, prepareOut{Base: "b"})
			p := h.a.p
			if mt := p.folded.loop["T-1"]; mt == nil || !mt.awaitsTaskDone() || mt.lane != "O-01" {
				t.Fatalf("precondition: T-1 %+v; want waiting on its worker in O-01", mt)
			}

			// the stop is recorded against T-1
			h.a.mu.Lock()
			keep := c.watch(p)
			h.a.mu.Unlock()
			if c.name == "old watch, new task" && (p.tasks["T-0"] == nil || p.tasks["T-1"] != nil) {
				t.Fatalf("precondition: watches %+v; want only T-0's on O-01", p.tasks)
			}
			reply, err := h.a.stoppedCall(Call{Root: h.root, Agent: "pj-o-01", Role: "worker", Lane: "O-01",
				CallID: "s1", Error: "rate_limit", Details: "429 Too Many Requests"})
			if err != nil || reply != "STOPPED pj-o-01 (needs-you T-1)" {
				t.Fatalf("stopped: %q, %v; want T-1's needs-you", reply, err)
			}
			if !p.stops("T-1") || p.stops("T-0") {
				t.Fatalf("stop in force: T-1 %v, T-0 %v; want T-1's only", p.stops("T-1"), p.stops("T-0"))
			}
			w := p.tasks["T-1"]
			if w == nil || w.lane.Name != "O-01" || w.lane.Task != "T-1" || (keep != nil && w != keep) {
				t.Errorf("T-1's watch %+v; want O-01's (the current one kept: %v)", w, keep != nil)
			}
			if p.tasks["T-0"] != nil {
				t.Errorf("the old watch of T-0 still in force after the stop")
			}

			// the next turn clears T-1's stop, the same watches on the lane again
			h.a.mu.Lock()
			keep = c.watch(p)
			h.a.mu.Unlock()
			reply, err = h.a.turnCall(Call{Root: h.root, Agent: "pj-o-01", Role: "worker", Lane: "O-01"})
			if err != nil || reply != "TURN pj-o-01 (the stop of T-1 ended)" {
				t.Fatalf("turn: %q, %v; want T-1's stop ended", reply, err)
			}
			if p.stops("T-1") {
				t.Errorf("T-1's stop still in force after the next turn")
			}
			if keep != nil && p.tasks["T-1"] != keep {
				t.Errorf("the turn replaced T-1's watch; want the current one used")
			}
			if p.tasks["T-0"] != nil {
				t.Errorf("the old watch of T-0 still in force after the turn")
			}
		})
	}
}
