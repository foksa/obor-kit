package setup

import (
	"strings"
	"testing"
)

// The lane's occupancy is one decision (launch.Launcher's laneFree), whether setup places a pane with no start (a
// NoAgent launch) or starts the agent in it: another agent in the lane's worktree is refused with one text, naming the
// occupant by its label when the config names that agent, else its pane, and nothing is placed, labelled or started
// either way.
func TestPaneLaneOccupiedRefusesTheSameWithAndWithoutStart(t *testing.T) {
	t.Parallel()
	for _, c := range []struct{ name, label, want string }{
		{"unlabelled", "", "FAIL claude still runs in lane L-01 (pane w1:x1): quit the agent in pane w1:x1 first"},
		{"a person's label", "notes", "FAIL notes still runs in lane L-01 (pane w1:x1): quit the agent in pane w1:x1 first"},
		{"labelled with a configured agent", "pj-int", "FAIL pj-int still runs in lane L-01 (pane w1:x1): obor restart pj-int --stop first"},
	} {
		for _, start := range []bool{false, true} {
			name := c.name + ", pane"
			args := []string{"pane", "L-01", "--tab", "w1:t1"}
			if start {
				name, args = c.name+", pane --start", []string{"pane", "--start", "L-01", "--tab", "w1:t1"}
			}
			t.Run(name, func(t *testing.T) {
				t.Parallel()
				x := newFx(t)
				x.lane(t, "L-01")
				x.where = serveSaysNone
				x.h.Agent("w1:x1", "claude", "idle", x.LaneDir("L-01")).Label = c.label
				x.reset()
				x.failing(t, c.want, args...)
				calls := strings.Join(x.reqs(), "\n")
				for _, m := range []string{"pane.split", "tab.create", "pane.rename", "pane.send_input", "agent.start"} {
					hasNot(t, calls, m, "refused: no "+m)
				}
			})
		}
	}
}
