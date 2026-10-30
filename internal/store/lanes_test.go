package store

import (
	"encoding/json"
	"testing"
	"time"
)

// blockedEvent is one event of a blocked-report test (attempt 1, class none).
type blockedEvent struct {
	task, step, kind string
	data             any
}

// writeBlockedEvents writes evs to root's event log in order, as serve records them.
func writeBlockedEvents(t *testing.T, root string, evs []blockedEvent) {
	t.Helper()
	db, err := OpenRW(File(root))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for i, e := range evs {
		var data any // NULL: no data
		if e.data != nil {
			b, err := json.Marshal(e.data)
			if err != nil {
				t.Fatal(err)
			}
			data = string(b)
		}
		if _, err := db.Exec(`INSERT INTO events (seq, ts, task, attempt, step, kind, class, data)
			VALUES (?, ?, ?, 1, ?, ?, '', ?)`, i+1, time.Now().UTC().Format(time.RFC3339), e.task, e.step,
			e.kind, data); err != nil {
			t.Fatalf("event %d %s: %v", i+1, e.step, err)
		}
	}
}

// T-560 (RV-58 #1): a Blocked: report (needs-you{asks}) counts only until the lifecycle facts that start another attempt
// end it — an answer, an unblocked, a task_done submission, a drop, a dispatch with its lane.prepare result, a sendback
// — while other worker facts do not. Another task's facts never end it.
func TestHasBlockedReportAtEndsTheReportWithItsAttempt(t *testing.T) {
	t.Parallel()
	ask := blockedEvent{"T-01", "needs-you", "fact", map[string]string{"reason": "asks: may I?"}}
	fresh := blockedEvent{"T-01", "needs-you", "fact", map[string]string{"reason": "asks: may I now?"}}
	for _, c := range []struct {
		name   string
		events []blockedEvent
		want   bool
	}{
		{"no report", []blockedEvent{{"T-01", "dispatch", "fact", nil}}, false},
		{"a report", []blockedEvent{ask}, true},
		{"a report and ordinary activity", []blockedEvent{ask,
			{"T-01", "resumed", "fact", nil}, {"T-01", "stalled", "fact", nil}}, true},
		{"an answer for another task", []blockedEvent{ask,
			{"msg", "answered", "fact", map[string]string{"task": "T-02"}}}, true},
		{"an answer", []blockedEvent{ask, {"msg", "answered", "fact", map[string]string{"task": "T-01"}}}, false},
		{"an unblocked", []blockedEvent{ask, {"T-01", "unblocked", "fact", nil}}, false},
		{"a dispatch", []blockedEvent{ask, {"T-01", "dispatch", "fact", nil}}, false},
		{"a dispatch of another task", []blockedEvent{ask, {"T-02", "dispatch", "fact", nil}}, true},
		{"a lane.prepare result", []blockedEvent{ask, {"T-01", "lane.prepare", "result", nil}}, false},
		{"a retried lane.prepare result", []blockedEvent{ask, {"T-01", "lane.prepare.2", "result", nil}}, false},
		{"a submission", []blockedEvent{ask, {"T-01", "submitted", "fact", nil}}, false},
		{"a drop", []blockedEvent{ask, {"T-01", "dropped", "fact", nil}}, false},
		{"a sendback", []blockedEvent{ask, {"T-01", "sendback", "fact", nil}}, false},
		{"a fresh report after a dispatch", []blockedEvent{ask, {"T-01", "dispatch", "fact", nil}, fresh}, true},
		{"a fresh report after a sendback", []blockedEvent{ask, {"T-01", "sendback", "fact", nil}, fresh}, true},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			writeBlockedEvents(t, root, c.events)
			got, err := HasBlockedReportAt(root, "T-01")
			if err != nil {
				t.Fatalf("HasBlockedReportAt: %v", err)
			}
			if got != c.want {
				t.Errorf("HasBlockedReportAt = %v, want %v for %v", got, c.want, c.events)
			}
		})
	}
}
