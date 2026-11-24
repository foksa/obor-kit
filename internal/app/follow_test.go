package app

import (
	"encoding/json"
	"testing"
	"time"

	"obor/internal/app/event"
)

// hasNeeds is a record of task's needs-you in force, as a watchdog's: any cause.
func hasNeeds(task, reason string) event.Record {
	data, _ := json.Marshal(event.FactData{Reason: reason})
	return event.Record{Task: task, Step: event.StepNeedsYou, Kind: event.KindFact, Data: data}
}

// TestRestartBusy: the work a pending self-restart waits for (follow.go). Every running thing holds it — a slot step, a
// lane test, an effect, the nightly — except work of a task waiting on a person: a needs-you in force means nothing of
// that task runs, so whatever an effect of it still holds holds nothing (the owner may take hours). A project key's
// needs-you (a line's, the nightly's) is no task's: its work holds the restart.
func TestRestartBusy(t *testing.T) {
	key := func(task, step string) event.Key { return event.Key{Task: task, Attempt: 1, Step: step} }
	test := key("T-1", event.StepTest)
	tests := []struct {
		name string
		p    *proj
		want bool
	}{
		{"nothing runs", &proj{}, false},
		{"a lane test", &proj{tests: map[string]event.Key{"T-1": test}}, true},
		{"a lane test of a task on a person", &proj{tests: map[string]event.Key{"T-1": test},
			folded: folded{active: map[string][]event.Record{"T-1": {hasNeeds("T-1", "agent exited")}}}}, false},
		{"a slot step", &proj{running: test}, true},
		{"a slot step of a task on a person", &proj{running: test,
			folded: folded{active: map[string][]event.Record{"T-1": {hasNeeds("T-1", "stalled")}}}}, false},
		{"an effect", &proj{exec: map[event.Key]time.Time{test: {}}}, true},
		{"an effect of a task on a person", &proj{exec: map[event.Key]time.Time{test: {}},
			folded: folded{active: map[string][]event.Record{"T-1": {hasNeeds("T-1", "asks: may I?")}}}}, false},
		{"a line's effect, a line's needs-you", &proj{exec: map[event.Key]time.Time{key(event.MsgTask, "deliver.3"): {}},
			folded: folded{active: map[string][]event.Record{event.MsgTask: {hasNeeds(event.MsgTask, "line waiting")}}}},
			true},
		{"the nightly", &proj{night: nightlyState{running: true}}, true},
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			if got := c.p.restartBusy(); got != c.want {
				t.Errorf("restartBusy = %v, want %v", got, c.want)
			}
		})
	}
}
