package checks

import (
	"testing"
)

// T-720: a gate (or nightly) whose check slot was claimed after the load wait ran out carries "(run under load <x>)"
// in its result line, green or red; below the limit it carries none.
func TestResultLineUnderLoad(t *testing.T) {
	t.Parallel()
	t.Run("green gate under load: marked", func(t *testing.T) {
		t.Parallel()
		x := newFx(t, cmd("check", "echo 2 pass"))
		x.slotLoad = 42.5
		x.is(x.gate("O-01", "T-05", "green"), OK, `^GREEN T-05 \(.*\) \(run under load 42\.5\)$`)
		x.contains(x.out.String(), "waiting: machine load 42.5 (> 1)", "the wait line before the cap ran out")
	})
	t.Run("red gate under load: marked before the evidence", func(t *testing.T) {
		t.Parallel()
		x := newFx(t, cmd("check", `echo "Expected 1"; exit 1`))
		x.slotLoad = 42.5
		x.is(x.gate("O-01", "T-05", "red"), Red, `^RED check \(.*\) \(run under load 42\.5\) — `)
	})
	t.Run("nightly under load: marked", func(t *testing.T) {
		t.Parallel()
		x := newFx(t, cmd("check", "echo 2 pass"))
		x.slotLoad = 42.5
		x.is(x.nightly("MILESTONE"), OK, `^MILESTONE GREEN .*\) \(run under load 42\.5\)$`)
	})
	t.Run("green gate, no load wait: unmarked", func(t *testing.T) {
		t.Parallel()
		x := newFx(t, cmd("check", "echo 2 pass"))
		r := x.gate("O-01", "T-05", "green")
		x.is(r, OK, `^GREEN T-05 `)
		x.lacks(r.Line, "under load", "the line below the limit")
	})
}
