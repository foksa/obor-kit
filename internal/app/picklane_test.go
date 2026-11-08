package app

import (
	"testing"

	"obor/internal/app/port"
	"obor/internal/app/wire"
	"obor/internal/store"
)

// TestPickLaneTakesALandedCleanLane (T-1012): dispatch picks by the one free rule's state: of lanes whose states are
// port.FreeState over their rows, a fresh dispatch takes the first whose task landed and that is clean, skips one whose
// task has not landed (working) and one landed but dirty (dirty-free) unless forced, then the dirty-free one first in
// config order.
func TestPickLaneTakesALandedCleanLane(t *testing.T) {
	t.Parallel()
	lane := func(name string, row store.LaneRow, dirty bool) port.Lane {
		return port.Lane{Name: name, Dir: "/wt/" + name, Task: row.Task, Dirty: dirty,
			State: port.FreeState(&row, "h", dirty, nil)} // each lane's HEAD its submitted head h
	}
	ls := []port.Lane{
		lane("L-01", store.LaneRow{Lane: "L-01", Task: "T-1", Head: "h"}, false),               // not landed: working
		lane("L-02", store.LaneRow{Lane: "L-02", Task: "T-2", Head: "h", Landed: true}, true),  // landed, dirty
		lane("L-03", store.LaneRow{Lane: "L-03", Task: "T-3", Head: "h", Landed: true}, false), // landed, clean
	}
	if ls[0].State != port.LaneWorking || ls[1].State != port.LaneDirtyFree || ls[2].State != port.LaneFree {
		t.Fatalf("precondition: states %s, %s, %s; want working, dirty-free, free", ls[0].State, ls[1].State,
			ls[2].State)
	}
	c := wire.DispatchReq{Task: "T-9", Types: []string{"sonnet"}}
	if l, ref, why := pickLane(ls, c, map[string]string{}, map[string]string{}, "", false); l.Name != "L-03" ||
		ref != "sonnet" || why != "" {
		t.Errorf("pickLane = %s, %q, %q; want L-03 (landed, clean) as sonnet", l.Name, ref, why)
	}
	c.Force = true
	if l, _, why := pickLane(ls, c, map[string]string{}, map[string]string{}, "", false); l.Name != "L-02" || why != "" {
		t.Errorf("pickLane --force = %s, %q; want L-02 (landed, dirty: the first in config order with force)", l.Name,
			why)
	}
	c.Force = false
	if l, _, why := pickLane(ls[:2], c, map[string]string{}, map[string]string{}, "", false); l.Name != "" || why == "" {
		t.Errorf("pickLane without a landed clean lane = %s, %q; want no lane and why", l.Name, why)
	}
}
