package app

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"obor/internal/app/event"
	"obor/internal/app/port"
	"obor/internal/app/wire"
	"obor/internal/store"
)

// syncRec is T-1's record of attempt with data.
func syncRec(attempt int, step, kind string, data any) event.Record {
	raw, _ := json.Marshal(data)
	return event.Record{Task: "T-1", Attempt: attempt, Step: step, Kind: kind, Data: raw}
}

// submittedT1 is T-1 submitted from L-01 by pj-l-01 and its lane commit pinned (the lane row's head s1), then gated
// with outcome (green: no Base, so no lane test).
func submittedT1(outcome string) []event.Record {
	return []event.Record{
		syncRec(1, event.StepSubmitted, event.KindFact, event.Submit{Lane: "L-01", Agent: "pj-l-01"}),
		syncRec(1, event.StepSubmit, event.KindResult, event.SubmitOut{SHA: "s1"}),
		syncRec(1, event.StepGate, event.KindIntent, event.GateIn{Kind: port.InputLane, Lane: "L-01", Input: "s1"}),
		syncRec(1, event.StepGate, event.KindResult, event.GateOut{Outcome: outcome,
			Candidate: port.Candidate{Task: "T-1", SHA: "c1", Target: "t0", Patch: "p1"}}),
	}
}

// redSentBack is T-1's red gate sent back (row 21): attempt 2 working, its lane.sync due.
func redSentBack() []event.Record {
	return append(submittedT1(outRed), syncRec(1, event.StepSendback, event.KindFact, event.Note{Note: "RED"}))
}

// A sendback that sends the worker back to fix on its lane schedules lane.sync before the fix job (row 24a): the
// red gate's, the integrator's and the lead's; a drive's goes back to the owner and a move redoes the dispatch: neither
// syncs. Each folded live and on replay alike (the fold is the replay).
func TestStateWorkingRow24a(t *testing.T) {
	t.Parallel()
	review := append(submittedT1(outGreen), syncRec(1, event.StepReview, event.KindIntent, event.Job{Patch: "p1"}))
	drive := []event.Record{
		syncRec(1, event.StepDispatched, event.KindFact, event.Dispatch{Dispatch: port.Dispatch{Lane: "L-01", Agent: "pj-l-01",
			Branch: "task/T-1", Target: "t0"}, Drive: true}),
		syncRec(1, event.StepPrepare, event.KindResult, event.PrepareOut{Base: "t0"}),
	}
	drive = append(drive, submittedT1(outRed)...)
	for _, c := range []struct {
		name  string
		recs  []event.Record
		state string
		sync  bool
	}{
		{"red gate", redSentBack(), stWorking, true},
		{"the integrator's sendback", append(review, syncRec(1, verdictStep(wire.MethodSendback, "v1"), event.KindResult,
			event.VerdictResult{Job: "T-1/1/review", Note: "add a test"})), stWorking, true},
		{"the lead's sendback", append(submittedT1(outGreen), syncRec(1, verdictStep(wire.MethodSendback, "l1"),
			event.KindResult, event.VerdictResult{Note: "use the store", Lead: true})), stWorking, true},
		{"a drive's red gate", append(drive, syncRec(1, event.StepSendback, event.KindFact, event.Note{Note: "RED"})),
			stDriving, false},
		{"a move after the sendback", append(redSentBack(), syncRec(3, event.StepDispatched, event.KindFact,
			event.Dispatch{Dispatch: port.Dispatch{Lane: "L-01", Agent: "pj-l-01", Branch: "task/T-1", Target: "t1",
				Redo: true}, Fix: "RED", Stop: "pj-l-01"})), stDispatched, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			_, p := blockProj(t, c.recs)
			mt := p.folded.loop["T-1"]
			if mt.state != c.state || mt.attempt < 2 {
				t.Fatalf("T-1 %s at attempt %d; want %s at a new attempt", mt.state, mt.attempt, c.state)
			}
			same(t, mt.syncDue(), c.sync, "lane.sync due")
			if got := mt.stepName(); (got == event.StepSync) != c.sync {
				t.Errorf("step name %q; want lane.sync %v", got, c.sync)
			}
		})
	}
}

// Each lane.sync result folds on replay: the attempt's sync is done (the fix job is next, carrying the result); a merge
// moves the lane row's head to the merge commit and its base to the target, any other result leaves the row as it was.
func TestLaneSyncResultReplays(t *testing.T) {
	t.Parallel()
	in := port.SyncInput{Task: "T-1", Attempt: 2, Lane: "L-01", Dir: "/lanes/L-01", Branch: "task/T-1", Target: "t1"}
	for _, c := range []struct {
		out        syncOut
		head, base string
	}{
		{syncOut{Outcome: port.SyncMerged, SHA: "m1", Target: "t1"}, "m1", "t1"},
		{syncOut{Outcome: port.SyncCurrent, Target: "t1"}, "s1", ""},
		{syncOut{Outcome: port.SyncConflict, Target: "t1", Files: []string{"a.go"}}, "s1", ""},
		{syncOut{Outcome: port.SyncDirty, Target: "t1", Files: []string{"wip.txt"}}, "s1", ""},
	} {
		t.Run(c.out.Outcome, func(t *testing.T) {
			t.Parallel()
			recs := append(redSentBack(), syncRec(2, event.StepSync, event.KindIntent, in),
				syncRec(2, event.StepSync, event.KindResult, c.out))
			_, p := blockProj(t, recs)
			mt := p.folded.loop["T-1"]
			if mt.state != stWorking || mt.synced == nil || mt.synced.Outcome != c.out.Outcome || mt.syncDue() {
				t.Fatalf("T-1 %s, synced %+v, due %v; want working, %s, done", mt.state, mt.synced, mt.syncDue(),
					c.out.Outcome)
			}
			same(t, mt.stepName(), "the fix", "step name")
			row := p.folded.lr.rows["L-01"]
			if row.Task != "T-1" || row.Head != c.head || row.Base != c.base {
				t.Errorf("L-01's row %+v; want T-1, head %s, base %q", row, c.head, c.base)
			}
		})
	}
}

// A crash between lane.sync's intent and its result: the replay finds the intent pending, the sync still due, and
// runs it again under the same key with the recorded input (the target sha of the intent, not the target now); its
// result is written once and the fix job is next.
func TestLaneSyncReplaysTheRecordedTarget(t *testing.T) {
	t.Parallel()
	in := port.SyncInput{Task: "T-1", Attempt: 2, Lane: "L-01", Dir: "/lanes/L-01", Branch: "task/T-1", Target: "t-old"}
	a, p := blockProj(t, append(redSentBack(), syncRec(2, event.StepSync, event.KindIntent, in)))
	key := event.Key{Task: "T-1", Attempt: 2, Step: event.StepSync}
	if mt := p.folded.loop["T-1"]; !p.log.Open(key) || !mt.syncDue() {
		t.Fatalf("precondition: intent open %v, sync due %v; want the crash's pending lane.sync", p.log.Open(key), mt.syncDue())
	}
	f := NewFakeWorkflow("t-new")
	f.Script("Sync", "T-1", port.SyncMerged)
	if !a.laneSync(context.Background(), p, &Loop{Workflow: WorkflowOf(f)}, *p.folded.loop["T-1"]) {
		t.Fatalf("lane.sync did not run: fails %+v", p.folded.loop["T-1"].fails)
	}
	if s := f.Syncs(); len(s) != 1 || s[0] != in {
		t.Errorf("Sync inputs %+v; want the recorded %+v once", s, in)
	}
	n := 0
	_, recs := openLog(t, p.root) // the log as a replay reads it
	for _, r := range recs {
		if r.Step == event.StepSync {
			n++
		}
	}
	mt := p.folded.loop["T-1"]
	if n != 2 || !p.log.Done(key) || mt.synced == nil || mt.synced.Target != "t-old" || mt.syncDue() {
		t.Errorf("lane.sync records %d, done %v, synced %+v; want its one intent and one result of t-old", n,
			p.log.Done(key), mt.synced)
	}
}

// The fix job's line for each lane.sync result (syncLine), the target branch and the target's short sha in it; none
// before a lane.sync ran.
func TestSyncLine(t *testing.T) {
	t.Parallel()
	const target = "abcdef1234567890"
	for _, c := range []struct {
		out  *syncOut
		want string
	}{
		{nil, ""},
		{&syncOut{Outcome: port.SyncMerged, SHA: "m1", Target: target}, "serve merged main (abcdef1) into your branch first."},
		{&syncOut{Outcome: port.SyncCurrent, Target: target}, "Your branch already holds main (abcdef1)."},
		{&syncOut{Outcome: port.SyncConflict, Target: target, Files: []string{"a.go", "b.go"}}, "serve did not merge main " +
			"(abcdef1) into your branch: it conflicts in a.go, b.go. Fix it on your branch as it is; the integrator " +
			"resolves the conflict at the gate."},
		{&syncOut{Outcome: port.SyncDirty, Target: target, Files: []string{"wip.txt"}}, "serve did not merge main " +
			"(abcdef1) into your branch: your lane has uncommitted changes (wip.txt). Fix it on your branch as it is."},
	} {
		name := "none"
		if c.out != nil {
			name = c.out.Outcome
		}
		same(t, syncLine(c.out, "main"), c.want, name)
	}
}

// The fix job carries its attempt's lane.sync line between the reasons and the task_done instruction.
func TestFixJobCarriesTheSyncLine(t *testing.T) {
	t.Parallel()
	mt := mtask{task: "T-9", attempt: 2, attemptVars: attemptVars{line: "add a test", sync: true,
		synced: &syncOut{Outcome: port.SyncMerged, SHA: "m1", Target: "abcdef1234"}}}
	text := fixText(mt, store.SpecRow{}, "read it.", "main")
	want := "T-9 is sent back to you (attempt 2): add a test\nserve merged main (abcdef1) into your branch first.\nFix it"
	if !strings.HasPrefix(text, want) {
		t.Errorf("the fix job:\n%s\nwant it to start with:\n%s", text, want)
	}
}
