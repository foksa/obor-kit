package app

import (
	"testing"
	"time"

	"obor/internal/app/event"
)

// T-751: no watchdog watches a driving task. The same seed as a worker's prepared dispatch (askSeed, whose idle worker
// stalls after Idle, then needs the owner) with the dispatch a drive's: the lane's idle agent is never stalled{idle} or
// needs-you{stalled}, live or after a replay, while the control (a worker's dispatch) is.
func TestDriveNoStallWhileDriving(t *testing.T) {
	t.Parallel()
	for _, drive := range []bool{false, true} {
		name := "worker (control)"
		if drive {
			name = "drive"
		}
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			h, _ := newIdentHarness(t)
			h.withLead()
			h.a.opt.Loop = func(string) (*Loop, error) { return &Loop{}, nil }
			h.connect()
			p := h.proj()
			t.Cleanup(func() { h.proj().log.Close(); h.a.closeLog() })
			addRecord(t, h, "T-1", event.StepDispatched, event.KindFact, dispatchData{Lane: "L-01", Dir: h.lane("L-01"),
				Agent: "pj-l-01-drive", Branch: "task/T-1", Target: "b", Drive: drive})
			addRecord(t, h, "T-1", event.StepPrepare, event.KindResult, prepareOut{Base: "b"})
			p.setLanes(h.lanes.lanes)
			h.a.reconcile(p)
			if got, want := p.folded.loop["T-1"].state, map[bool]string{false: stDispatched, true: stDriving}[drive]; got != want {
				t.Fatalf("precondition: T-1 is %s, want %s", got, want)
			}
			h.tick(11 * time.Minute)
			h.tick(11 * time.Minute)
			h.tick(11 * time.Minute)
			stalled, needs := p.has("T-1", event.StepStalled, "idle"), p.has("T-1", event.StepNeedsYou, reasonStalled)
			if drive && (stalled || needs) {
				t.Errorf("a driving task is watched: stalled %v, needs-you %v (%v)", stalled, needs, p.folded.active["T-1"])
			}
			if !drive && !stalled && !needs {
				t.Fatalf("precondition: the control's idle worker never stalled (%v)", p.folded.active["T-1"])
			}
			if !drive {
				return
			}
			p.log.Close()
			h.a.closeLog()
			p = reloadProj(t, h)
			if st := p.folded.loop["T-1"].state; st != stDriving {
				t.Errorf("replayed as %s, want driving", st)
			}
			h.tick(11 * time.Minute)
			h.tick(11 * time.Minute)
			if p.has("T-1", event.StepStalled, "idle") || p.has("T-1", event.StepNeedsYou, reasonStalled) {
				t.Errorf("after the replay a driving task is watched: %v", p.folded.active["T-1"])
			}
		})
	}
}
