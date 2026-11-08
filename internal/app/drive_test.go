package app

import (
	"strings"
	"testing"
	"time"

	"obor/internal/app/event"
	"obor/internal/app/wire"
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

// landSeed is a harness with T-1 an owner's drive, driving, on L-01: the seeded dispatch and its prepare result.
// adapters: the merge loop has the lane adapters a drop frees a lane with (its drop records dropping); else a drop
// records dropped at once.
func landSeed(t *testing.T, adapters bool) (*harness, *proj) {
	t.Helper()
	h, _ := newIdentHarness(t)
	h.withLead()
	f := newConcFakes("")
	h.a.opt.Loop = func(string) (*Loop, error) {
		if !adapters {
			return &Loop{}, nil
		}
		return &Loop{Workflow: WorkflowOf(f), Specs: f, Agents: f}, nil
	}
	h.connect()
	p := h.proj()
	t.Cleanup(func() { h.proj().log.Close(); h.a.closeLog() })
	addRecord(t, h, "T-1", event.StepDispatched, event.KindFact, dispatchData{Lane: "L-01", Dir: h.lane("L-01"),
		Agent: "pj-l-01-drive", Branch: "task/T-1", Target: "b", Drive: true})
	addRecord(t, h, "T-1", event.StepPrepare, event.KindResult, prepareOut{Base: "b"})
	p.setLanes(h.lanes.lanes)
	h.a.reconcile(p)
	if st := p.folded.loop["T-1"].state; st != stDriving {
		t.Fatalf("precondition: T-1 is %s, want driving", st)
	}
	return h, p
}

// RV-132 #2 (T-945): a drop recorded between obor land's checks and its submission is the terminal fact. The fold
// ignores land's submission, so land checks its own receipt and answers superseded instead of LANDED: both a drop
// still dropping (its lane adapters) and one already dropped.
func TestLandSupersededByConcurrentDrop(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		name      string
		adapters  bool
		dropReply string // what the drop answers
		shown     string // the state land names
		state     string // T-1's state after both
	}{
		{"dropping", true, "DROPPING", stDropping, stDropping},
		{"dropped", false, "DROPPED", stDropped, stDropped},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			h, p := landSeed(t, c.adapters)
			dropped := false
			hook := func(point string) {
				if point != hookLandCheck || dropped {
					return
				}
				dropped = true
				reply, err := h.a.drop(wire.DropReq{Envelope: wire.Envelope{Root: h.root, Role: "lead",
					Agent: "pj-lead", CallID: "d1"}, Task: "T-1", Why: "not needed"})
				if err != nil || !strings.HasPrefix(reply, c.dropReply) {
					t.Errorf("drop: %q, %v; want %s", reply, err, c.dropReply)
				}
			}
			h.a.hooked.Store(&hook)
			defer func() { none := func(string) {}; h.a.hooked.Store(&none) }()
			reply, err := h.a.landCall(wire.LandReq{Envelope: wire.Envelope{Root: h.root, Role: "lead",
				Agent: "pj-lead", CallID: "l1"}, Task: "T-1", Report: "done"})
			if !dropped {
				t.Fatal("precondition: the hook never ran before land's record")
			}
			if want := "land: superseded: T-1 is " + c.shown; err == nil || err.Error() != want {
				t.Errorf("land: %q, %v; want %q", reply, err, want)
			}
			if reply != "" {
				t.Errorf("land answered %q; want no LANDED reply", reply)
			}
			h.a.mu.Lock()
			st := p.folded.loop["T-1"].state
			_, receipt := p.folded.calls[verdictStep(wire.MethodLand, "l1")]
			h.a.mu.Unlock()
			if st != c.state {
				t.Errorf("T-1 is %s after the drop; want %s", st, c.state)
			}
			if receipt {
				t.Error("land's call kept a receipt despite the ignored submission")
			}
		})
	}
	t.Run("normal land still answers LANDED", func(t *testing.T) {
		t.Parallel()
		h, p := landSeed(t, true)
		reply, err := h.a.landCall(wire.LandReq{Envelope: wire.Envelope{Root: h.root, Role: "lead", Agent: "pj-lead",
			CallID: "l1"}, Task: "T-1", Report: "done"})
		if err != nil || reply != landReply("T-1") {
			t.Fatalf("land: %q, %v; want the LANDED reply", reply, err)
		}
		if st := p.folded.loop["T-1"].state; st != stSubmitted {
			t.Errorf("T-1 is %s after a normal land; want submitted", st)
		}
	})
}
