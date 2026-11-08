package port

import (
	"testing"

	"obor/internal/store"
)

// TestFreeState (T-1012, T-1015): the one free rule, case → state. A lane is free by serve's facts (its row: whether
// its task landed, the head its submission pinned) and its own git (HEAD, dirty status) alone: a landed task frees
// its lane only while the lane's HEAD is the submitted head.
func TestFreeState(t *testing.T) {
	t.Parallel()
	const sub, later = "aaaa", "bbbb" // the submitted head; a commit made after it
	held := &store.LaneRow{Lane: "L-01", Task: "T-1", Branch: "task/T-1", Head: sub}
	landed := &store.LaneRow{Lane: "L-01", Task: "T-1", Branch: "task/T-1", Head: sub, Landed: true}
	noHead := &store.LaneRow{Lane: "L-01", Task: "T-1", Branch: "task/T-1", Landed: true}
	yes := func() bool { return true }
	no := func() bool { return false }
	for _, c := range []struct {
		name   string
		row    *store.LaneRow
		head   string
		dirty  bool
		merged func() bool
		want   string
	}{
		{"landed, HEAD the submitted head, clean", landed, sub, false, nil, LaneFree},
		{"landed, HEAD past the submitted head (a later commit), clean", landed, later, false, yes, LaneWorking},
		{"landed, HEAD the submitted head, dirty", landed, sub, true, nil, LaneDirtyFree},
		{"landed, HEAD past the submitted head, dirty", landed, later, true, nil, LaneWorking},
		{"not landed", held, sub, false, yes, LaneWorking},
		{"not landed, dirty", held, sub, true, nil, LaneWorking},
		{"no recorded head, evidence the HEAD merged", noHead, later, false, yes, LaneFree},
		{"no recorded head, evidence, dirty", noHead, later, true, yes, LaneDirtyFree},
		{"no recorded head, no evidence", noHead, later, false, no, LaneWorking},
		{"no recorded head, no evidence read", noHead, later, false, nil, LaneWorking},
		{"unknown row (the lanes table cannot be read)", nil, sub, false, yes, LaneUnknown},
		{"no row (never dispatched)", &store.LaneRow{}, later, false, nil, LaneFree},
		{"no row, dirty", &store.LaneRow{}, later, true, nil, LaneDirtyFree},
	} {
		if got := FreeState(c.row, c.head, c.dirty, c.merged); got != c.want {
			t.Errorf("%s: FreeState(%+v, head %s, dirty %v) = %q, want %q", c.name, c.row, c.head, c.dirty, got, c.want)
		}
	}
}

// TestFreeStateReadsEvidenceOnlyWithoutHead (T-1015): the merge-evidence read (a git call) runs only for a landed row
// with no recorded head, never for a row whose head decides.
func TestFreeStateReadsEvidenceOnlyWithoutHead(t *testing.T) {
	t.Parallel()
	n := 0
	merged := func() bool { n++; return true }
	FreeState(&store.LaneRow{Task: "T-1", Head: "aaaa", Landed: true}, "aaaa", false, merged)
	FreeState(&store.LaneRow{Task: "T-1", Head: "aaaa"}, "aaaa", false, merged)
	FreeState(&store.LaneRow{}, "aaaa", false, merged)
	if n != 0 {
		t.Fatalf("evidence read %d times for rows whose head (or no task) decides, want 0", n)
	}
	FreeState(&store.LaneRow{Task: "T-1", Landed: true}, "aaaa", false, merged)
	if n != 1 {
		t.Errorf("evidence read %d times for a landed row with no head, want 1", n)
	}
}
