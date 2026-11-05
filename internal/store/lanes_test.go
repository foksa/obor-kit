package store

import (
	"strconv"
	"testing"
)

// T-721, T-894: a lane row keeps its dispatch's type and its worker's blocked report through a write and both reads
// (all rows, one lane).
func TestLaneRowKeepsItsTypeAndBlocked(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	db, err := OpenRW(File(root))
	if err != nil {
		t.Fatal(err)
	}
	rows := []LaneRow{{Lane: "L-01", Task: "T-1", Branch: "task/T-1", Agent: "pj-l-01", Type: "opus:high",
		Blocked: true}, {Lane: "L-02", Task: "T-2", Branch: "task/T-2", Agent: "pj-l-02"}}
	if err := WriteLanes(db, rows); err != nil {
		t.Fatal(err)
	}
	db.Close()
	all, err := ReadLanesAt(root)
	if err != nil || all["L-01"] != rows[0] || all["L-02"] != rows[1] {
		t.Errorf("ReadLanesAt = %+v, %v; want %+v", all, err, rows)
	}
	if r, ok, err := ReadLaneAt(root, "L-01"); err != nil || !ok || r.Type != "opus:high" || !r.Blocked {
		t.Errorf("ReadLaneAt(L-01) = %+v, %v, %v; want type opus:high, blocked", r, ok, err)
	}
}

// T-894: a schema-11 lanes table has no blocked column: a reader (a newer CLI, serve not yet restarted) reads its rows
// unblocked, and OpenRW adds the column, keeps the rows and takes a blocked one.
func TestOpenRWMigratesTheLaneBlocked(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	db, err := OpenRW(File(root))
	if err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		"DROP TABLE lanes",
		`CREATE TABLE lanes (lane TEXT PRIMARY KEY, task TEXT NOT NULL, branch TEXT NOT NULL, agent TEXT NOT NULL,
			base TEXT NOT NULL, head TEXT NOT NULL, dispatched_at INTEGER NOT NULL, finished INTEGER NOT NULL,
			type TEXT NOT NULL DEFAULT '')`,
		"INSERT INTO lanes VALUES ('L-01', 'T-1', 'task/T-1', 'pj-l-01', 'b', 'h', 5, 0, 'opus:high')",
		"UPDATE meta SET value = '11' WHERE key = 'schema_version'",
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	db.Close()
	old := LaneRow{Lane: "L-01", Task: "T-1", Branch: "task/T-1", Agent: "pj-l-01", Base: "b", Head: "h",
		DispatchedAt: 5, Type: "opus:high"}
	if r, ok, err := ReadLaneAt(root, "L-01"); err != nil || !ok || r != old {
		t.Errorf("ReadLaneAt on schema 11 = %+v, %v, %v; want %+v", r, ok, err, old)
	}

	db, err = OpenRW(File(root))
	if err != nil {
		t.Fatalf("OpenRW on schema 11: %v", err)
	}
	defer db.Close()
	var version string
	if err := db.QueryRow("SELECT value FROM meta WHERE key = 'schema_version'").Scan(&version); err != nil ||
		version != strconv.Itoa(SchemaVersion) {
		t.Errorf("schema_version = %q, %v; want %d after the migration", version, err, SchemaVersion)
	}
	blocked := old
	blocked.Blocked = true
	if err := WriteLanes(db, []LaneRow{blocked}); err != nil {
		t.Fatalf("WriteLanes after the migration: %v", err)
	}
	if r, ok, err := ReadLaneAt(root, "L-01"); err != nil || !ok || r != blocked {
		t.Errorf("row after the migration = %+v, %v, %v; want %+v", r, ok, err, blocked)
	}
}
