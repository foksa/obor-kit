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
