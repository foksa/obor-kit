package app

import (
	"context"
	"strings"
	"testing"
	"time"
)

// askSeed is a project whose task T-1 is dispatched to lane O-01, its worker identified in pane w1:p1.
func askSeed(t *testing.T) (*harness, *proj) {
	t.Helper()
	h, _ := newIdentHarness(t)
	h.withLead()
	h.a.opt.Loop = func(string) (*Loop, error) { return &Loop{}, nil }
	h.connect()
	p := h.proj()
	t.Cleanup(func() { h.proj().log.Close(); h.a.closeLog() })
	addRecord(t, h, "T-1", StepDispatched, KindFact, dispatchData{Lane: "O-01", Dir: h.lane("O-01"), Agent: "pj-o-01", Branch: "task/T-1", Target: "b"})
	addRecord(t, h, "T-1", StepPrepare, KindResult, prepareOut{Base: "b"})
	p.setLanes(h.lanes.lanes)
	h.a.reconcile(p)
	return h, p
}

// TestActivityKeepsAnUnansweredAsk: a worker's activity ends the watchdog facts whose lifecycle activity ends (stalled,
// blocked, exited), never its open question (RV-54 #1, state model row 12), on the status path (working) and the
// sampled path (a child runs, a transcript write); the question stays after a replay and obor answer still takes it.
func TestActivityKeepsAnUnansweredAsk(t *testing.T) {
	for _, companion := range []string{"stalled", "blocked", "pane exited"} {
		for _, activity := range []string{"working", "child", "transcript"} {
			t.Run(companion+"/"+activity, func(t *testing.T) {
				h, p := askSeed(t)
				if companion != "stalled" {
					h.a.record(p, Record{Task: "T-1", Step: StepNeedsYou, Kind: KindFact}, FactData{Lane: "O-01", Reason: companion})
				}
				if companion == "stalled" || activity != "working" { // the sampled path resumes a stall only
					h.tick(11 * time.Minute)
					if !p.has("T-1", StepStalled, "idle") {
						t.Fatalf("precondition: no stall in force: %v", h.facts(0))
					}
				}
				if _, err := h.a.ask(p, "T-1", h.lanes.lanes[0], "may I edit x?"); err != nil {
					t.Fatal(err)
				}
				if !p.asks("T-1") || !p.watched("T-1") {
					t.Fatalf("precondition: want the asks and its companion in force: %v", p.active["T-1"])
				}
				switch activity {
				case "working":
					h.status("w1:p1", func(o *Pane) { o.Status = "working" })
				case "child":
					h.panes.busy = true
					h.tick(time.Minute)
				case "transcript":
					h.panes.last = h.now.Add(time.Second)
					h.tick(time.Minute)
				}
				if !strings.Contains(strings.Join(h.facts(0), "\n"), "T-1 resumed") {
					t.Fatalf("precondition: the activity recorded no resumed: %v", h.facts(0))
				}
				check := func(when string, p *proj) {
					t.Helper()
					if !p.asks("T-1") {
						t.Errorf("%s: the unanswered question ended: %v", when, p.active["T-1"])
					}
					if p.watched("T-1") {
						t.Errorf("%s: a fact activity ends is still in force: %v", when, p.active["T-1"])
					}
				}
				check("live", p)
				p.log.Close()
				h.a.closeLog()
				p = reloadProj(t, h)
				check("replay", p)
				if _, err := h.a.verdict(context.Background(), MethodAnswer, Call{Root: h.root, Task: "T-1", Role: "lead",
					Agent: "pj-lead", CallID: "a1", Note: "yes, edit x"}); err != nil {
					t.Fatalf("obor answer refused: %v", err)
				}
				if p.asks("T-1") {
					t.Error("the answer left the question in force")
				}
			})
		}
	}
}

// TestResumeKeepsTheAsksRetry: a resumed fact drops the notification retries of the facts it ends, not the open
// question's.
func TestResumeKeepsTheAsksRetry(t *testing.T) {
	p := &proj{active: map[string][]Record{}, retry: map[Key]retry{}}
	stall := Record{Seq: 1, Task: "T-1", Step: StepStalled, Kind: KindFact, Data: []byte(`{"reason":"idle"}`)}
	ask := Record{Seq: 2, Task: "T-1", Step: StepNeedsYou, Kind: KindFact, Data: []byte(`{"reason":"asks: may I?"}`)}
	p.fold(stall)
	p.fold(ask)
	p.retry[notifyKey(stall)] = retry{tries: 1}
	p.retry[notifyKey(ask)] = retry{tries: 1}
	p.fold(Record{Seq: 3, Task: "T-1", Step: StepResumed, Kind: KindFact})
	if len(p.active["T-1"]) != 1 || p.active["T-1"][0].Seq != 2 {
		t.Errorf("facts in force %v, want the asks only", p.active["T-1"])
	}
	if _, ok := p.retry[notifyKey(ask)]; !ok {
		t.Error("the question's notification retry ended")
	}
	if _, ok := p.retry[notifyKey(stall)]; ok {
		t.Error("the stall's notification retry stayed")
	}
}
