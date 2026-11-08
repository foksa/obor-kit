package port

import (
	"testing"

	"obor/internal/store"
)

// TestFreeState (T-1012): the one free rule, case → state. A lane is free by serve's facts (its row, whether the row's
// task landed) and its own dirty status alone.
func TestFreeState(t *testing.T) {
	t.Parallel()
	held := &store.LaneRow{Lane: "L-01", Task: "T-1", Branch: "task/T-1"}
	landed := &store.LaneRow{Lane: "L-01", Task: "T-1", Branch: "task/T-1", Landed: true}
	for _, c := range []struct {
		name          string
		row           *store.LaneRow
		landed, dirty bool
		want          string
	}{
		{"landed and clean", landed, true, false, LaneFree},
		{"landed and dirty", landed, true, true, LaneDirtyFree},
		{"not landed", held, false, false, LaneWorking},
		{"not landed, dirty", held, false, true, LaneWorking},
		{"unknown row (the lanes table cannot be read)", nil, false, false, LaneUnknown},
		{"unknown row, a caller's landed reading", nil, true, false, LaneUnknown},
		// A task merged by hand: no landed column set yet, the workflow's merge-evidence reading says landed.
		{"hand-merged (evidence, no fact)", held, true, false, LaneFree},
		{"no row (never dispatched)", &store.LaneRow{}, false, false, LaneFree},
		{"no row, dirty", &store.LaneRow{}, false, true, LaneDirtyFree},
	} {
		if got := FreeState(c.row, c.landed, c.dirty); got != c.want {
			t.Errorf("%s: FreeState(%+v, landed %v, dirty %v) = %q, want %q", c.name, c.row, c.landed, c.dirty, got,
				c.want)
		}
	}
}
