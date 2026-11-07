package app_test

import (
	"testing"

	"obor/internal/app"
)

// T-861: a merge-loop pass in a resubscribe's gap (the rig's push ends serve's stream; herdr has not answered the next
// subscribe yet) skips the project, so it is no pass a test waits on (passes, later: StreamPasses). Counted, two such
// passes let a wait return before any pass saw the clock the test had moved (TestBriefEndsWhenItsAgentIsGone/
// not_identified's 62 s wait: the brief's lost-since was sampled 30 s late). Passes count again on the next stream.
func TestPassInResubscribeGapNotCounted(t *testing.T) {
	t.Parallel()
	r := newLoopRig(t, noCheck())
	r.binary = func() bool { return false } // the binary watch wakes the merge loop each tick, herdr connected or not
	r.start()
	r.passes()
	release, reached := r.srv.Hold("events.subscribe")
	defer release()
	r.srv.Push("workspace_created", workspaceEvent) // raw: the next subscribe is held
	<-reached
	r.waitFor("serve between two streams", func() bool { return !r.app.Status().Connected })
	_, all := app.Counts(r.app)
	before := app.StreamPasses(r.app)
	r.waitFor("three passes in the gap", func() bool { _, n := app.Counts(r.app); return n >= all+3 })
	during := app.StreamPasses(r.app)
	if r.app.Status().Connected {
		t.Fatal("precondition: serve connected again while its subscribe is held")
	}
	if during != before {
		t.Errorf("stream passes %d → %d while serve was not connected: want none counted", before, during)
	}
	release()
	r.passes() // the next stream's passes count: this wait ends
}
