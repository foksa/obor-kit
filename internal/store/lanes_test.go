package store

import "testing"

// a lane row keeps its dispatch's type and its worker's blocked report through a write and both reads
// (all rows, one lane).
func TestLaneRowKeepsItsTypeAndBlocked(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	db, err := OpenRW(File(root))
	if err != nil {
		t.Fatal(err)
	}
	rows := []LaneRow{{Lane: "L-01", Task: "T-1", Branch: "task/T-1", Type: "opus:high",
		Blocked: true}, {Lane: "L-02", Task: "T-2", Branch: "task/T-2"}}
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
