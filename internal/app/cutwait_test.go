package app

import (
	"encoding/json"
	"testing"
	"time"

	"obor/internal/app/event"
)

// A fact of a task ends the waits of that task's steps (a resume's unblocked, a counted failure) and no other
// task's; a line's wait ends with its own failed fact, not with another line's or with a message, and a call's
// receipt (a stored spec, a delivered job) ends none.
func TestCutEndFact(t *testing.T) {
	t.Parallel()
	gate := event.Key{Task: "T-1", Attempt: 1, Step: "gate"}
	other := event.Key{Task: "T-2", Attempt: 1, Step: "gate"}
	line7 := event.Key{Task: event.MsgTask, Step: "deliver.7"}
	line8 := event.Key{Task: event.MsgTask, Step: "deliver.8"}
	fact := func(task, step string, d event.FactData) event.Record {
		raw, _ := json.Marshal(d)
		return event.Record{Task: task, Step: step, Kind: event.KindFact, Data: raw}
	}
	for _, c := range []struct {
		name string
		fact event.Record
		ends event.Key // the one wait the fact ends (zero: none)
	}{
		{"a resume", fact("T-1", event.StepUnblocked, event.FactData{}), gate},
		{"a counted failure", fact("T-1", event.StepFailed, event.FactData{Step: "gate"}), gate},
		{"a line's failure", fact(event.MsgTask, event.StepFailed, event.FactData{Step: "deliver.7"}), line7},
		{"a message", fact(event.MsgTask, event.StepMessage, event.FactData{}), event.Key{}},
		{"a spec's receipt", fact("T-1", event.StepSpecSet, event.FactData{}), event.Key{}},
		{"a delivery's receipt", fact("T-1", event.StepDelivered, event.FactData{}), event.Key{}},
	} {
		p := &proj{cuts: map[event.Key]*cutWait{}}
		for _, key := range []event.Key{gate, other, line7, line8} {
			p.cuts[key] = &cutWait{n: 2, timer: time.NewTimer(time.Hour)}
		}
		if !p.taskCutWaits("T-1") || !p.cutWaits(line7) {
			t.Fatalf("%s: precondition: T-1's gate and line 7 wait", c.name)
		}
		p.cutEndFact(c.fact)
		for _, key := range []event.Key{gate, other, line7, line8} {
			if waits := p.cutWaits(key); waits == (key == c.ends) {
				t.Errorf("%s: %v waits: %t, want %t", c.name, key, waits, key != c.ends)
			}
		}
		if waits := p.taskCutWaits("T-1"); waits == (c.ends == gate) {
			t.Errorf("%s: a step of T-1 waits: %t, want %t", c.name, waits, c.ends != gate)
		}
	}
}
