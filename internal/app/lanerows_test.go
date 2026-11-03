package app

import (
	"encoding/json"
	"obor/internal/app/event"
	"obor/internal/app/port"
	"obor/internal/app/wire"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"obor/internal/store"
)

// T-439: serve's lanes table is the projection of its fold: after a dispatch (its lane.prepare), a submission, a
// merge, a re-dispatch of the lane and a drop the table equals the fold's rows and what the log says, and a replay
// writes the same table. T-721: the row carries its dispatch's type reference; a dispatch recorded before T-721 has
// none (its row's type is empty, a reader takes the config's lane type), the next dispatch's row has its own.
func TestLaneRowsProjectTheLog(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.write(filepath.Join(h.root, ".obor", "config.json"), `{"name":"pj","prefix":"pj","roles":{"integrator":""}}`)
	h.connect()
	a, p := h.a, h.proj()
	add := func(task string, attempt int, step, kind string, d any) event.Record {
		t.Helper()
		h.now = h.now.Add(time.Minute)
		raw, _ := json.Marshal(d)
		r, err := p.log.Append(event.Record{Task: task, Attempt: attempt, Step: step, Kind: kind, Data: raw}, h.now)
		if err != nil {
			t.Fatal(err)
		}
		a.mu.Lock()
		p.foldRecord(r)
		a.mu.Unlock()
		return r
	}
	table := func(when string, want ...store.LaneRow) {
		t.Helper()
		got, err := store.ReadLanesAt(h.root)
		if err != nil {
			t.Fatalf("%s: %v", when, err)
		}
		a.mu.Lock()
		fold := p.laneTable()
		a.mu.Unlock()
		byLane := map[string]store.LaneRow{}
		for _, r := range want {
			byLane[r.Lane] = r
		}
		if !reflect.DeepEqual(got, byLane) {
			t.Errorf("%s: table %+v, want %+v", when, got, byLane)
		}
		if want == nil {
			want = []store.LaneRow{}
		}
		if !reflect.DeepEqual(fold, want) {
			t.Errorf("%s: the fold's rows %+v, want %+v", when, fold, want)
		}
	}
	table("load: no records")

	disp := add("T-1", 1, event.StepDispatched, event.KindFact, dispatchData{Lane: "L-01", Dir: h.lane("L-01"), Agent: "pj-l-01",
		Branch: "task/T-1", Target: "b1"})
	table("dispatched, not prepared")
	add("T-1", 1, event.StepPrepare, event.KindIntent, port.PrepareInput{})
	add("T-1", 1, event.StepPrepare, event.KindResult, prepareOut{Base: "b1", Head: "h1"})
	at := event.RecordTime(disp).Unix()
	row := store.LaneRow{Lane: "L-01", Task: "T-1", Branch: "task/T-1", Agent: "pj-l-01", Base: "b1", Head: "h1",
		DispatchedAt: at}
	table("prepared: awaits task_done", row)

	sub := add("T-1", 1, event.StepSubmitted, event.KindFact, submitData{Lane: "L-01", Agent: "pj-l-01", Report: "r"})
	row.Finished = event.RecordTime(sub).Unix()
	table("submitted: finished", row)
	add("T-1", 1, event.StepMerged, event.KindFact, event.MergedData{})
	table("merged: the row stays", row)

	add("T-2", 1, event.StepDispatched, event.KindFact, dispatchData{Lane: "L-01", Dir: h.lane("L-01"), Agent: "pj-l-01",
		Branch: "task/T-2", Target: "b2", Effort: "high", Type: "opus:high"})
	add("T-2", 1, event.StepPrepare, event.KindIntent, port.PrepareInput{})
	add("T-2", 1, event.StepPrepare, event.KindResult, prepareOut{Base: "b2", Head: "b2"})
	row2 := store.LaneRow{Lane: "L-01", Task: "T-2", Branch: "task/T-2", Agent: "pj-l-01", Base: "b2", Head: "b2",
		DispatchedAt: h.now.Add(-2 * time.Minute).Unix(), Type: "opus:high"}
	table("the lane re-dispatched", row2)

	// A replay of the same log writes the same table (the projection is idempotent).
	if err := store.WriteLanes(p.log.DB(), nil); err != nil {
		t.Fatal(err)
	}
	h.a.closeLog()
	a = New(Options{Root: h.root, Now: func() time.Time { return h.now }}, h.panes, h.lanes)
	a.loadProject()
	p = a.p
	table("replayed", row2)
	defer a.closeLog()

	add("T-2", 1, event.StepDropped, event.KindFact, event.FactData{Lane: "L-01", Reason: "abandoned", Merge: true})
	table("dropped: the row clears")
}

// T-439: obor mcp's task_done names only the caller's lane: serve resolves it to its row's task; a lane with no row
// is refused, nothing recorded.
func TestTaskDoneByLane(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.write(filepath.Join(h.root, ".obor", "config.json"), `{"name":"pj","prefix":"pj","roles":{"integrator":""}}`)
	h.a.opt.Loop = func(string) (*Loop, error) { return &Loop{}, nil }
	h.connect()
	p := h.proj()
	h.a.mu.Lock()
	p.folded.lr.rows["L-01"] = store.LaneRow{Lane: "L-01", Task: "T-1"}
	h.a.mu.Unlock()
	if _, err := h.a.taskDone(wire.Call{Root: h.root, Role: "worker", Agent: "pj-l-01", Lane: "L-02", CallID: "c0",
		Report: "Summary: x"}); err == nil || err.Error() != "task_done: lane L-02 holds no task in obor serve" {
		t.Errorf("a lane with no row: %v", err)
	}
	_, err := h.a.taskDone(wire.Call{Root: h.root, Role: "worker", Agent: "pj-l-01", Lane: "L-01", CallID: "c1",
		Report: "Summary: x"})
	if err == nil || err.Error() == "task_done: lane L-01 holds no task in obor serve" {
		t.Errorf("L-01's row holds T-1: the call reaches the report check, got %v", err)
	}
}
