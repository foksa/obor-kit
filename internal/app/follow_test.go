package app

import (
	"encoding/json"
	"strings"
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

// TestRestartHolds: a pending restart's wait by name: the admitted writers and verdict calls whatever has run how long,
// then the work restartBusy counts, each once (the slot step's effect is the slot step); past followWait that work is
// cut, the writers and verdicts still hold it.
func TestRestartHolds(t *testing.T) {
	gate := event.Key{Task: "T-1", Attempt: 1, Step: event.StepGate}
	test := event.Key{Task: "T-2", Attempt: 1, Step: event.StepTest}
	seen := time.Unix(1_000_000, 0)
	p := &proj{running: gate, tests: map[string]event.Key{"T-2": test},
		exec: map[event.Key]time.Time{gate: {}, test: {}}, night: nightlyState{running: true}, restarting: seen}
	p.admitOps.add("review close")
	a := &App{}
	a.verdictOps.add("accept T-3")
	holds, cut := a.restartHolds(p, seen.Add(time.Minute))
	want := "review close (writer), accept T-3 (verdict), nightly check, gate T-1, test T-2"
	if got := strings.Join(holds, ", "); got != want || len(cut) != 0 {
		t.Errorf("inside the wait: holds %q, cuts %q; want %q, none", got, cut, want)
	}
	holds, cut = a.restartHolds(p, seen.Add(followWait))
	if got, want := strings.Join(holds, ", "), "review close (writer), accept T-3 (verdict)"; got != want {
		t.Errorf("past the wait: holds %q, want %q", got, want)
	}
	if got, want := strings.Join(cut, ", "), "nightly check, gate T-1, test T-2"; got != want {
		t.Errorf("past the wait: cuts %q, want %q", got, want)
	}
	p.admitOps.del("review close")
	a.verdictOps.del("accept T-3")
	if holds, _ := a.restartHolds(p, seen.Add(followWait)); len(holds) != 0 {
		t.Errorf("writer and verdict ended, past the wait: holds %q, want none", holds)
	}
}
