package app

import (
	"context"
	"testing"

	"obor/internal/app/event"
	"obor/internal/app/wire"
)

// A dispatched task's stage (dispatchStage), from the records that lead to it: the one rule nextDispatch switches
// on, and the stage the readers outside the pick ask (foldStage: the lane prepared, no start tried, its own start
// done). A refusal's step is its needs-you, its cause the step refused (a move's stop, the lane, the start).
func TestDispatchStage(t *testing.T) {
	t.Parallel()
	disp := func(stop string) *blockLog {
		return (&blockLog{}).fact(1, event.StepDispatched, event.Dispatch{Lane: "L-01", Agent: "pj-w1", Stop: stop})
	}
	prepared := func() *blockLog { return disp("").result(1, event.StepPrepare, nil, event.PrepareOut{Base: "base"}) }
	work := func(b *blockLog) *blockLog { return b.add(1, event.StepWork, event.KindIntent, nil) }
	started := func(b *blockLog) *blockLog {
		return b.result(1, event.StepStartWork, nil, event.StartOut{Agent: "pj-w1", Pane: "w1:p1"})
	}
	sent := func(b *blockLog) *blockLog {
		return b.add(1, event.StepWork, event.KindResult, map[string]string{"line": "sent (start prompt)"})
	}
	for _, c := range []struct {
		name    string
		log     *blockLog
		kept    bool      // the keep lane's live agent takes the work job (App.kept)
		want    dispStage // dispatchStage
		cause   string    // its step's needs-you cause ("": no needs-you step)
		ready   bool      // foldStage: the lane is prepared
		before  bool      // foldStage: no start tried, no work job sent
		started bool      // foldStage: its own agent.start started the agent
	}{
		{name: "a move's stop of the old worker", log: disp("pj-old"), want: dsHalting, before: true},
		{name: "a move's refused stop", log: disp("pj-old").result(1, event.StepWorkerStop, nil, stopOut{Refused: "busy"}),
			want: dsHalting, cause: causeStop, before: true},
		{name: "a move's stop done: the prepare", log: disp("pj-old").result(1, event.StepWorkerStop, nil, stopOut{}),
			want: dsPrepare, before: true},
		{name: "a fresh dispatch: the prepare", log: disp(""), want: dsPrepare, before: true},
		{name: "a refused prepare", log: disp("").result(1, event.StepPrepare, nil, event.PrepareOut{Refused: "dirty"}),
			want: dsLaneRefused, cause: causeLane, before: true},
		{name: "prepared: the start", log: prepared(), want: dsStart, ready: true, before: true},
		{name: "a keep lane's live agent: no start", log: prepared(), kept: true, want: dsKept, ready: true, before: true},
		{name: "a refused start", log: work(prepared()).result(1, event.StepStartWork, nil, event.StartOut{Refused: "busy"}),
			want: dsStartRefused, cause: causeStart, ready: true},
		{name: "a start tried, no result: started again", log: work(prepared()).add(1, event.StepStartWork, event.KindIntent, nil),
			want: dsStartAgain, ready: true},
		{name: "a kept agent gone once its work job is out: started", log: sent(work(prepared())), want: dsStartAgain,
			ready: true},
		{name: "a kept agent's work job out: watched", log: sent(work(prepared())), kept: true, want: dsWatch, ready: true},
		{name: "a kept agent's failed work prompt: resent under its key", log: work(prepared()).failed(1, event.StepWork, "x", 1),
			kept: true, want: dsResend, ready: true},
		{name: "started, its work job not carried: resent under its key",
			log: started(work(prepared())).failed(1, event.StepWork, "x", 1), want: dsResend, ready: true, started: true},
		{name: "started, its work job carried: watched", log: sent(started(work(prepared()))), want: dsWatch, ready: true,
			started: true},
		{name: "an undelivered work job resumed: the work prompt anew",
			log: sent(started(work(prepared()))).needs(1, causeUndelivered, "not fetched").
				fact(1, event.StepUnblocked, event.FactData{Reason: wire.ResumeRetry, Cause: causeUndelivered}),
			want: dsWork, ready: true, started: true},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			a, p := blockProj(t, c.log.recs)
			m := p.folded.loop[blockTask]
			if m == nil || m.state != stDispatched || m.paused() {
				t.Fatalf("precondition: %s dispatched, no needs-you in force: %+v", blockTask, m)
			}
			mt := *m
			kept := func() bool { return c.kept }
			workOpen := func() bool { return p.log.Open(event.Key{Task: mt.task, Attempt: mt.attempt, Step: mt.work}) }
			same(t, mt.dispatchStage(kept, workOpen), c.want, "stage")
			fs := mt.foldStage()
			same(t, fs.laneReady(), c.ready, "fold stage: lane prepared")
			same(t, fs.beforeStart(), c.before, "fold stage: before the start")
			same(t, fs.started(), c.started, "fold stage: started")
			if c.want == dsResend {
				same(t, mt.work, event.StepWork, "the resend's key")
			}
			if c.cause == "" {
				return
			}
			p.dispatching = true
			s := a.nextDispatch(p, mt, nil)
			if s == nil || !s(context.Background(), nil) {
				t.Fatal("no needs-you step")
			}
			got := p.folded.loop[blockTask].needs
			same(t, got.held() && got.fact.Fact().Cause == c.cause, true, "needs-you of cause "+c.cause)
		})
	}
}
