package app

import (
	"context"
	"testing"
)

// T-401 (RV-43 #1): settling an ended task keeps the notification of every needs-you still in force, a watchdog's
// (p.active) as well as the merge loop's, merged or dropped, live or on replay: notify shows it once. A notification
// whose fact cleared before the task ended is settled as before.
func TestEndedTaskKeepsNotificationInForce(t *testing.T) {
	for _, c := range []struct {
		name    string
		replay  bool
		end     string
		cleared bool // the watchdog fact ended (resumed) before the task did
	}{
		{"live merged", false, StepMerged, false},
		{"live dropped", false, StepDropped, false},
		{"replay merged", true, StepMerged, false},
		{"replay dropped", true, StepDropped, false},
		{"cleared live merged", false, StepMerged, true},
		{"cleared live dropped", false, StepDropped, true},
		{"cleared replay merged", true, StepMerged, true},
		{"cleared replay dropped", true, StepDropped, true},
	} {
		t.Run(c.name, func(t *testing.T) {
			r := newConcRig(t)
			a, p := r.app("pq"), r.proj("pq")
			r.add("pq", "T-3", StepNeedsYou, KindFact, FactData{Lane: "O-03", Reason: "pane exited"})
			if len(p.folded.active["T-3"]) != 1 {
				t.Fatalf("watchdog facts of T-3: %v, want one", p.folded.active["T-3"])
			}
			fact := p.folded.active["T-3"][0]
			k := notifyKey(fact)
			in, err := p.log.Append(Record{Task: k.Task, Attempt: k.Attempt, Step: k.Step, Kind: KindIntent,
				Class: ClassFact}, a.opt.Now()) // its notification interrupted before the send
			if err != nil {
				t.Fatal(err)
			}
			a.mu.Lock()
			p.foldRecord(in)
			a.mu.Unlock()
			if _, pending := p.log.Intent(k); !pending {
				t.Fatal("precondition: the notification is not pending")
			}
			if c.cleared {
				r.add("pq", "T-3", StepResumed, KindFact, FactData{Lane: "O-03"})
				if p.inForce(fact) {
					t.Fatal("precondition: the resumed fact is still in force")
				}
			}

			if c.replay {
				r.add("pq", "T-3", c.end, KindFact, nil)
				a.closeLog() // the first serve ends: the replay is the next one
				a = New(Options{Now: a.opt.Now, Loop: a.opt.Loop}, &fakePanes{}, &fakeLanes{})
				if p, err = a.load(p.root); err != nil {
					t.Fatal(err)
				}
				a.p, a.connected = p, true
			} else if !a.record(p, Record{Task: "T-3", Attempt: 1, Step: c.end, Kind: KindFact}, nil) {
				t.Fatal("the terminal fact was not recorded")
			}

			if got := p.folded.loop["T-3"].state; got != c.end {
				t.Fatalf("T-3 is %s, want %s", got, c.end)
			}
			_, pending := p.log.Intent(k)
			if c.cleared {
				if pending {
					t.Error("the notification of a cleared fact is still pending")
				}
				if got := string(p.log.results[k].Data); got != `"not run: T-3 `+c.end+`"` {
					t.Errorf("its result = %s, want not run", got)
				}
				return
			}
			if !pending {
				t.Fatalf("the notification of a needs-you in force was settled: %s", p.log.results[k].Data)
			}
			// An unfinished lane still holds the task: its watchdog stays in force.
			p.setLanes([]Lane{{Name: "O-03", Task: "T-3", Agent: "pq-o-03"}})
			if !p.inForce(fact) {
				t.Fatal("precondition: the watchdog fact is not in force")
			}
			panes := a.panes.(*fakePanes)
			a.notify(context.Background())
			a.notify(context.Background())
			if len(panes.notes) != 1 {
				t.Errorf("notifications shown: %v, want one", panes.notes)
			}
			if got := string(p.log.results[k].Data); got != `"shown"` {
				t.Errorf("its result = %s, want shown", got)
			}
		})
	}
}
