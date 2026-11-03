package app_test

import (
	"encoding/json"
	"obor/internal/app/event"
	"strings"
	"testing"
	"time"

	"obor/internal/app"
)

// T-635: a task's merge or drop ends its watchdog facts, a worker's question (needs-you{asks}) too, so a replay of an
// old log (T-509: asked, then merged; its lane row still naming it) has no fact in force and posts no line or repeat.
// An asks of an open task still repeats as today.
func TestClosedTaskWatchdogFactsEnd(t *testing.T) {
	t.Parallel()
	asks := event.FactData{Lane: "L-01", Agent: "pj-l-01", Reason: "asks: which colour?"}
	submitted := map[string]any{"lane": "L-01", "agent": "pj-l-01"}
	for _, c := range []struct {
		name  string
		end   []event.Record // after the asks
		lines bool           // the asks is in force: its line and repeats are posted
	}{
		{"merged", []event.Record{
			{Task: "T-1", Attempt: 1, Step: event.StepSubmitted, Kind: event.KindFact, Data: raw(submitted)},
			{Task: "T-1", Attempt: 1, Step: event.StepMerged, Kind: event.KindFact, Data: raw(map[string]any{"outside": true})},
		}, false},
		{"dropped", []event.Record{
			{Task: "T-1", Attempt: 1, Step: event.StepSubmitted, Kind: event.KindFact, Data: raw(submitted)},
			{Task: "T-1", Attempt: 1, Step: event.StepDropped, Kind: event.KindFact,
				Data: raw(event.FactData{Lane: "L-01", Merge: true, Reason: "why"})},
		}, false},
		{"open", nil, true},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			r := newLoopRig(t, "echo 1 pass")
			recs := append([]event.Record{{Task: "T-1", Attempt: 1, Step: event.StepNeedsYou, Kind: event.KindFact,
				Data: raw(asks)}}, c.end...)
			if err := app.WriteLog(r.p.Root, r.clock.Now(), recs...); err != nil {
				t.Fatal(err)
			}
			var seq int64
			for _, rec := range r.records() {
				if rec.Step == event.StepNeedsYou {
					seq = rec.Seq
				}
			}
			if seq == 0 {
				t.Fatalf("precondition: no needs-you{asks} in the log: %q", r.steps())
			}
			r.start()
			r.passes()
			r.later(31 * time.Minute) // past the first repeat's 30 min
			r.later(61 * time.Minute) // and the next's 60
			var inForce []event.Record
			for _, f := range r.app.Status().Project.Facts {
				if f.Seq == seq { // the asks (the rig's static lane row still names T-1: the watchdog may decide anew)
					inForce = append(inForce, f)
				}
			}
			lines := 0
			for _, rec := range r.needsLineFacts() {
				var m event.Message
				if json.Unmarshal(rec.Data, &m) == nil && m.Needs == seq {
					lines++
				}
			}
			if !c.lines {
				if len(inForce) != 0 || lines != 0 {
					t.Errorf("after the task's %s: needs-you in force %+v, lines %d; want none", c.name, inForce, lines)
				}
				return
			}
			if len(inForce) != 1 {
				t.Errorf("open task: needs-you in force %+v, want its asks", inForce)
			}
			if again := r.againLines(seq); len(again) == 0 || !strings.Contains(again[0], "which colour?") {
				t.Errorf("open task: repeats %q (lines %d), want the asks repeated", again, lines)
			}
		})
	}
}

// raw is v as JSON.
func raw(v any) json.RawMessage {
	b, _ := json.Marshal(v)
	return b
}
