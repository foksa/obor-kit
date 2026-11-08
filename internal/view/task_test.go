package view

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"obor/internal/app/event"
	"obor/internal/app/wire"
	"obor/internal/checks"
	"obor/internal/config"
	"obor/internal/store"
	"obor/internal/testkit"
)

// T-883: Build makes one Task per task the view knows: one waiting for a lane, one waiting --after (a queued task kept
// its title only while never dispatched, T-664), one merging, one needing you and one only specified all carry their
// title and state in Tasks, and each section names them by id.
func TestBuildOneTaskPerTask(t *testing.T) {
	p := testkit.New(t)
	o1 := p.Lane("L-01", "task/T-03")
	p.Config()
	p.Commit(o1)
	p.Dispatched("L-01")
	ids := []string{"T-01", "T-02", "T-03", "T-04", "T-05"}
	for _, id := range ids {
		p.Spec(id, "# "+id+": title of "+id+"\n\nbody\n")
	}
	nf, _ := json.Marshal(event.FactData{Lane: "L-09", Reason: "asks"})
	ps := &wire.ProjectStatus{Name: "pj", Root: p.Root, Tasks: []wire.TaskStatus{
		{Task: "T-01", State: "waiting", Phase: wire.PhaseWaiting, Level: "medium", Types: []string{"opus"}},
		{Task: "T-02", State: "waiting", Phase: wire.PhaseWaiting, After: []string{"T-03"}},
		{Task: "T-03", Lane: "L-01", Attempt: 1, State: "accepted", Phase: wire.PhasePublish, Stage: wire.StagePublish},
		{Task: "T-04", Lane: "L-09", Attempt: 1, State: "needs-you", Phase: wire.PhaseNeedsYou},
	}, Facts: []event.Record{{Seq: 1, Task: "T-04", Step: event.StepNeedsYou, Kind: event.KindFact, Data: nf},
		{Seq: 2, Task: event.LoopTask, Step: event.StepNeedsYou, Kind: event.KindFact, Data: nf}}}
	cfg, err := config.Load(p.Root)
	if err != nil {
		t.Fatal(err)
	}
	db, err := store.OpenRO(store.File(p.Root))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	v := Build(Input{Status: wire.Status{Connected: true, Project: ps}, Lanes: checks.NewLaneTable(context.Background(), cfg),
		DB: db, Now: time.Now()})
	if !v.Serve.Project || len(v.Lanes) != 1 {
		t.Fatalf("precondition: serve has the project, one lane: %+v", v.Serve)
	}

	wantState := map[string]string{"T-01": "waiting", "T-02": "waiting", "T-03": "accepted", "T-04": "needs-you", "T-05": ""}
	for _, id := range ids {
		tk, ok := v.Tasks[id]
		if !ok || tk.Task != id || tk.Title != "title of "+id || tk.SpecRev != 1 || tk.TaskState != wantState[id] {
			t.Errorf("%s: %+v (in Tasks %v), want its title, spec rev 1 and state %q", id, tk, ok, wantState[id])
		}
	}
	if _, ok := v.Tasks[event.LoopTask]; ok || len(v.Tasks) != len(ids) {
		t.Errorf("tasks %v, want %v alone (a loop error is no task)", v.TaskIDs(), ids)
	}
	if tk := v.Tasks["T-03"]; tk.Lane != "L-01" || tk.Agent == "" || tk.Phase != wire.PhasePublish || tk.Step == nil ||
		*tk.Step != wire.TaskStepMerging {
		t.Errorf("merging T-03: %+v, want lane L-01 with its agent, phase publish on the merging step", tk)
	}
	if tk := v.Tasks["T-04"]; tk.Lane != "L-09" || tk.Step != nil {
		t.Errorf("needs-you T-04: %+v, want lane L-09 and no step (its records place it)", tk)
	}

	if l := v.Lanes[0]; l.Task != "T-03" || l.Column != wire.PhasePublish || l.Free {
		t.Errorf("lane %+v, want T-03 by id in phase publish, not free", l)
	}
	if m := v.Queue.Merge; len(m) != 1 || m[0].Task != "T-03" || m[0].Stage != wire.StagePublish {
		t.Errorf("merge %+v, want T-03 by id in stage publish", m)
	}
	waits := map[string]string{}
	for _, w := range v.Queue.Waiting {
		waits[w.Task] = w.WaitsOn
	}
	if len(waits) != 3 || waits["T-01"] != WaitsLane || waits["T-02"] != WaitsAfter || waits["T-05"] != WaitsNever {
		t.Errorf("waiting %+v, want T-01 on a lane, T-02 after, T-05 never dispatched", v.Queue.Waiting)
	}
	if n := v.NeedsYou; len(n) != 2 || n[0].Task != "T-04" || n[1].Task != event.LoopTask {
		t.Errorf("needs-you %+v, want T-04 then the loop's, by id", n)
	}
}

// T-1022: the Task obor status --json prints carries serve's ref of it (wire.TaskStatus.Ref, its spec's external id);
// a task without one ships no ref key.
func TestTaskCarriesItsRef(t *testing.T) {
	ps := wire.ProjectStatus{Tasks: []wire.TaskStatus{
		{Task: "T-1", State: "working", Phase: wire.PhaseWorking, Ref: "JIRA-12"},
		{Task: "T-2", State: "working", Phase: wire.PhaseWorking},
	}}
	got := tasks(ps, nil, nil, nil)
	if tk, ok := got["T-1"]; !ok || tk.Ref != "JIRA-12" {
		t.Errorf("T-1: %+v (found %v), want ref JIRA-12", tk, ok)
	}
	raw, err := json.Marshal(got["T-2"])
	if err != nil {
		t.Fatal(err)
	}
	if tk := got["T-2"]; tk.Ref != "" || strings.Contains(string(raw), `"ref":`) {
		t.Errorf("T-2: %+v (%s), want no ref", tk, raw)
	}
}
