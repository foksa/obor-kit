package web

import (
	"context"
	"testing"
	"time"

	"obor/internal/app/wire"
	"obor/internal/checks"
	"obor/internal/config"
	"obor/internal/project"
	"obor/internal/testkit"
	"obor/internal/view"
)

// T-788 (S5): serve's phase is the one word for a task everywhere: the lanes row (obor status' column), the pipeline
// (the queue's merge item and its stage) and the fleet page's lane strip show it as serve sent it, for every phase,
// over a lane git reads as submitted (the lane table's done before, which the lanes row showed for a green task).
func TestPhaseAgreesAcrossLanesPipelineAndWeb(t *testing.T) {
	t.Parallel()
	p := testkit.New(t)
	o1 := p.Lane("L-01", "task/T-01")
	p.Config()
	p.Commit(o1)
	p.Submit("L-01")
	cfg, err := config.Load(p.Root)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct{ state, phase string }{ // a worker's needs-you (working) has no merge stage (T-797)
		{"waiting", wire.PhaseWaiting}, {"dispatched", wire.PhaseDispatching}, {"working", wire.PhaseWorking},
		{"driving", wire.PhaseDriving}, {"submitted", wire.PhaseSubmitted}, {"submitted", wire.PhaseGating},
		{"submitted", wire.PhaseGatePending}, {"green", wire.PhaseReview}, {"awaiting-accept", wire.PhaseReviewing},
		{"accepted", wire.PhasePublish}, {"accepted", wire.PhaseMerging}, {"accepted", wire.PhasePublishPending},
		{"working", wire.PhaseNeedsYou}, {"needs-you", wire.PhaseHeld},
		{"rejected", wire.PhaseRejected},
	} {
		stage := wire.PhaseStage(c.phase)
		if c.state == "working" {
			stage = ""
		}
		ss := wire.Status{Connected: true, Project: &wire.ProjectStatus{Name: "pj", Root: p.Root,
			Tasks: []wire.TaskStatus{{Task: "T-01", Lane: "L-01", Attempt: 1, State: c.state, Open: true, Phase: c.phase, Stage: stage}}}}
		v := view.Build(view.Input{Status: ss, Lanes: checks.NewLaneTable(context.Background(), cfg), Now: time.Now(), Events: 20})
		if len(v.Lanes) != 1 || v.Lanes[0].State != "working" || v.Lanes[0].DoneAge == nil {
			t.Fatalf("precondition: L-01 submitted (working, its task_done's age): %+v", v.Lanes)
		}
		if l, tk := v.Lanes[0], v.Tasks["T-01"]; l.Column != c.phase || tk.Phase != c.phase || tk.TaskState != c.state {
			t.Errorf("%s: lanes row column %q, its task's phase %q state %q, want %q, %q", c.phase, l.Column, tk.Phase,
				tk.TaskState, c.phase, c.state)
		}
		var merge []view.MergeItem
		for _, m := range v.Queue.Merge {
			if m.Task == "T-01" {
				merge = append(merge, m)
			}
		}
		if stage == "" {
			if len(merge) != 0 {
				t.Errorf("%s: in the pipeline %+v, want not (no merge-loop stage)", c.phase, merge)
			}
		} else if len(merge) != 1 || merge[0].Stage != stage {
			t.Errorf("%s: pipeline %+v, want T-01 %s at stage %s", c.phase, merge, c.phase, stage)
		}
		s := &Server{Projects: func() []project.Project { return []project.Project{{Name: "pj", Root: p.Root}} },
			Build: func(context.Context, string) (view.Project, error) { return v, nil }}
		var entries []ProjectsEntry
		decode(t, get(t, s.Handler(), "/api/projects"), &entries)
		if len(entries) != 1 || len(entries[0].Lanes) != 1 || entries[0].Lanes[0].Column != c.phase ||
			entries[0].Lanes[0].Phase != c.phase || entries[0].Busy != 1 {
			t.Errorf("%s: /api/projects %+v, want L-01 busy with column and phase %q", c.phase, entries, c.phase)
		}
	}
}
