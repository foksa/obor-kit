//go:build integration

package app_test

import (
	"testing"
)

// A drive still dispatched (its lane's prepare held, so not driving yet) is refused too: the lead could not answer it.
func TestTellLeadRefusedWhileTheDriveIsDispatched(t *testing.T) {
	t.Parallel()
	r := newLoopRig(t, dispatching(), started())
	r.disp.set(func(d *dispatchFakes) { d.cut["prepare"] = "before" })
	r.start()
	if _, err := r.drive("v1", "web"); err != nil {
		t.Fatalf("drive: %v", err)
	}
	r.waitStep("1 intent lane.prepare") // the loop is in prepare, held: the drive stays dispatched
	if st := r.stateOf("T-1"); st != "dispatched" {
		t.Fatalf("precondition: T-1 is %s, want dispatched", st)
	}
	n := len(r.records())
	want := "tell-lead: T-1 is dispatched (landed: gate, review and merge follow; the lead cannot answer a drive " +
		"until it is driving again)"
	if _, err := r.tellLead("t1", "pj-l-01-drive", "driver", "L-01", "hi"); err == nil || err.Error() != want {
		t.Errorf("tell-lead on a dispatched drive: %v; want %q", err, want)
	}
	if got := len(r.records()); got != n {
		t.Errorf("the refused tell-lead recorded %q", r.steps()[n:])
	}
}
