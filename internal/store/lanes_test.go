package store

import (
	"encoding/json"
	"strconv"
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

// T-721: a lane row keeps its dispatch's type through a write and both reads (all rows, one lane).
func TestLaneRowKeepsItsType(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	db, err := OpenRW(File(root))
	if err != nil {
		t.Fatal(err)
	}
	rows := []LaneRow{{Lane: "L-01", Task: "T-1", Branch: "task/T-1", Agent: "pj-l-01", Type: "opus:high"},
		{Lane: "L-02", Task: "T-2", Branch: "task/T-2", Agent: "pj-l-02"}}
	if err := WriteLanes(db, rows); err != nil {
		t.Fatal(err)
	}
	db.Close()
	all, err := ReadLanesAt(root)
	if err != nil || all["L-01"] != rows[0] || all["L-02"] != rows[1] {
		t.Errorf("ReadLanesAt = %+v, %v; want %+v", all, err, rows)
	}
	if r, ok, err := ReadLaneAt(root, "L-01"); err != nil || !ok || r.Type != "opus:high" {
		t.Errorf("ReadLaneAt(L-01) = %+v, %v, %v; want type opus:high", r, ok, err)
	}
}

// T-721: a schema-8 database's lanes table has no type column: a reader (a newer CLI, serve not yet restarted) reads
// its rows with none, and OpenRW adds the column, keeps the rows and takes a row with a type.
func TestOpenRWMigratesTheLanesType(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	db, err := OpenRW(File(root))
	if err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		"DROP TABLE lanes",
		`CREATE TABLE lanes (lane TEXT PRIMARY KEY, task TEXT NOT NULL, branch TEXT NOT NULL, agent TEXT NOT NULL,
			base TEXT NOT NULL, head TEXT NOT NULL, dispatched_at INTEGER NOT NULL, finished INTEGER NOT NULL)`,
		"INSERT INTO lanes VALUES ('L-01', 'T-1', 'task/T-1', 'pj-l-01', 'b', 'h', 5, 0)",
		"UPDATE meta SET value = '8' WHERE key = 'schema_version'",
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	db.Close()
	old := LaneRow{Lane: "L-01", Task: "T-1", Branch: "task/T-1", Agent: "pj-l-01", Base: "b", Head: "h", DispatchedAt: 5}
	if r, ok, err := ReadLaneAt(root, "L-01"); err != nil || !ok || r != old {
		t.Errorf("ReadLaneAt on schema 8 = %+v, %v, %v; want %+v", r, ok, err, old)
	}
	if all, err := ReadLanesAt(root); err != nil || all["L-01"] != old {
		t.Errorf("ReadLanesAt on schema 8 = %+v, %v; want %+v", all, err, old)
	}

	db, err = OpenRW(File(root))
	if err != nil {
		t.Fatalf("OpenRW on schema 8: %v", err)
	}
	defer db.Close()
	var version string
	if err := db.QueryRow("SELECT value FROM meta WHERE key = 'schema_version'").Scan(&version); err != nil ||
		version != strconv.Itoa(SchemaVersion) {
		t.Errorf("schema_version = %q, %v; want %d after the migration", version, err, SchemaVersion)
	}
	if rows, err := ReadLanes(db); err != nil || len(rows) != 1 || rows[0] != old {
		t.Errorf("rows after the migration: %+v, %v; want the old one, no type", rows, err)
	}
	typed := old
	typed.Type = "sonnet"
	if err := WriteLanes(db, []LaneRow{typed}); err != nil {
		t.Fatalf("WriteLanes after the migration: %v", err)
	}
	if rows, err := ReadLanes(db); err != nil || len(rows) != 1 || rows[0] != typed {
		t.Errorf("rows after a typed write: %+v, %v; want %+v", rows, err, typed)
	}
}
