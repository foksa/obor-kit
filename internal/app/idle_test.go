package app_test

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"obor/internal/app"
	"obor/internal/app/event"
	"obor/internal/app/wire"
	"obor/internal/testkit"
)

// T-803, T-866: the idle rules end to end (their table is rules_test.go's TestIdleRules): what keeps the pipeline from
// being idle, the line through the merge loop and the acknowledgement through the socket, and the line's order after a
// watch's.

// idleLine is the idle line after task's merge (idleLineFor) or a drop.
func idleLineFor(r *loopRig, task string) string {
	r.t.Helper()
	sha := r.p.Git(r.p.Root, "rev-parse", "--short=7", "main")
	return idleLineText(task + " merged " + sha)
}

func idleLineText(last string) string {
	return `[obor] idle: nothing in flight (last: ` + last + `) — dispatch next, or obor idle "<why nothing>"`
}

// idleSettle is T-859's window: the idle line waits until the pipeline has stayed idle this long.
const idleSettle = time.Minute

// settleIdle lets the idle window pass: two passes start its clock, then the clock moves past idleSettle, so the next
// idle check posts the line.
func (r *loopRig) settleIdle() {
	r.t.Helper()
	r.passes()          // the idle check that starts the window
	r.later(idleSettle) // the first check past it: the line goes out
}

// idlePosts is the idle lines the lead's inbox got, in order.
func (r *loopRig) idlePosts() []string {
	var out []string
	for _, p := range r.inbox.posted() {
		if strings.HasPrefix(p, "pj-lead: [obor] idle:") {
			out = append(out, strings.TrimPrefix(p, "pj-lead: "))
		}
	}
	return out
}

// logRecords appends recs to the project's event log before the app starts.
func (r *loopRig) logRecords(recs ...event.Record) {
	r.t.Helper()
	if err := app.WriteLog(r.p.Root, time.Now(), recs...); err != nil {
		r.t.Fatal(err)
	}
}

// record is one fact or intent of task at attempt, its data as JSON.
func record(t *testing.T, task string, attempt int, step, kind, class string, data any) event.Record {
	t.Helper()
	raw, err := json.Marshal(data)
	if err != nil {
		t.Fatal(err)
	}
	return event.Record{Task: task, Attempt: attempt, Step: step, Kind: kind, Class: class, Data: raw}
}

// mergedTask runs T-1 from its task_done to its merge (as mergedT1, for a rig that has no dispatch to make).
func (r *loopRig) mergedTask() {
	r.t.Helper()
	r.work("feature.txt", "feature\n")
	r.mergedT1()
}

// T-803: a waiting dispatch, a needs-you and a reviewer's open brief each keep the pipeline from being idle.
func TestIdleBlockers(t *testing.T) {
	t.Parallel()
	t.Run("a waiting dispatch", func(t *testing.T) {
		t.Parallel()
		r := newLoopRig(t, "echo 1 pass")
		r.p.Dispatched("L-01")
		r.logRecords(record(t, "T-2", 1, event.StepWaiting, event.KindFact, "",
			map[string]any{"lane": "L-02", "after": []string{"T-1"}}))
		r.start()
		r.mergedTask()
		r.passes()
		r.passes()
		r.later(2 * idleSettle) // past the window: a line would have gone out if the pipeline were idle
		if st := r.app.Status().Project.Idle; st != nil {
			t.Errorf("idle state %+v while T-2 waits", st)
		}
		if n := len(r.idlePosts()); n != 0 {
			t.Errorf("%d idle lines while T-2 waits", n)
		}
	})
	t.Run("a needs-you", func(t *testing.T) {
		t.Parallel()
		r := newLoopRig(t, "echo 1 pass")
		r.p.Dispatched("L-01")
		r.logRecords(
			record(t, "T-2", 1, event.StepDispatched, event.KindFact, "", map[string]any{"lane": "L-02", "agent": "pj-l-02",
				"branch": "task/T-2", "target": "main"}),
			record(t, "T-2", 1, event.StepNeedsYou, event.KindFact, "", event.FactData{Lane: "L-02", Merge: true,
				Cause: "stalled", Reason: "stalled: no work seen"}))
		r.start()
		r.mergedTask()
		r.passes()
		r.passes()
		r.later(2 * idleSettle) // past the window: a line would have gone out if the pipeline were idle
		if st := r.app.Status().Project.Idle; st != nil {
			t.Errorf("idle state %+v while T-2 needs the owner", st)
		}
		if n := len(r.idlePosts()); n != 0 {
			t.Errorf("%d idle lines while T-2 needs the owner", n)
		}
	})
	t.Run("a reviewer with a brief", func(t *testing.T) {
		t.Parallel()
		r := newLoopRig(t, "echo 1 pass", testkit.Set("roles.reviewer", ""))
		rev := r.f.Agent("w1:p6", "pj-rev", "idle", r.p.Root)
		rev.Workspace, rev.Label = "w1", "pj-rev"
		r.start()
		if reply, err := r.line(wire.MethodBrief, "b1", wire.Call{To: "pj-rev", Note: "review the wave"}); err != nil ||
			!strings.HasPrefix(reply, "BRIEFED pj-rev") {
			t.Fatalf("brief: %q, %v", reply, err)
		}
		r.work("feature.txt", "feature\n")
		r.submit()
		r.accept("c1", r.patch(r.waitJob("pj-int", "T-1/1/review")))
		// The brief holds no merge (T-853), but the reviewer still holds its brief, so the pipeline is not idle.
		r.merged(1)
		r.passes()
		r.passes()
		r.later(2 * idleSettle) // past the window: a line would have gone out if the pipeline were idle
		if st := r.app.Status().Project.Idle; st != nil {
			t.Errorf("idle state %+v while the reviewer is briefed", st)
		}
		if n := len(r.idlePosts()); n != 0 {
			t.Errorf("%d idle lines while the reviewer is briefed", n)
		}
	})
}

// T-803: obor idle "<why>" acknowledges; no ping follows, and obor status shows the why.
func TestIdleAckStopsThePing(t *testing.T) {
	t.Parallel()
	r := newLoopRig(t, "echo 1 pass")
	r.start()
	r.mergedTask()
	line := idleLineFor(r, "T-1")
	r.settleIdle()
	r.waitFor("the idle line posted", func() bool { return r.inbox.count("pj-lead", line) == 1 })
	if reply, err := r.line(wire.MethodIdle, "i1", wire.Call{Note: "nothing to dispatch"}); err != nil ||
		reply != "ACKED idle: nothing to dispatch" {
		t.Fatalf("idle ack: %q, %v", reply, err)
	}
	st := r.app.Status().Project.Idle
	if st == nil || !st.Acked || st.Why != "nothing to dispatch" {
		t.Fatalf("status idle %+v, want the acknowledgement's why", st)
	}
	r.later(31 * time.Minute)
	r.passes()
	if got := r.idlePosts(); len(got) != 1 || got[0] != line {
		t.Errorf("idle lines %q after the ack and 31m, want the one line", got)
	}
	if n := countReq(r.srv, "notification.show"); n != 0 {
		t.Errorf("%d notifications after the ack, want none", n)
	}
	if reply, err := r.line(wire.MethodIdle, "i2", wire.Call{}); err != nil || reply != "IDLE: nothing to dispatch" {
		t.Errorf("obor idle (the state): %q, %v", reply, err)
	}
}

// T-803: a watch on the last task comes first, then the idle line of the same merge.
func TestIdleLineAfterTheWatchLine(t *testing.T) {
	t.Parallel()
	r := newLoopRig(t, "echo 1 pass")
	r.start()
	if reply, err := r.watch("w1", "T-1", false); err != nil || reply != "WATCHING T-1" {
		t.Fatalf("watch: %q, %v", reply, err)
	}
	r.mergedTask()
	sha := r.p.Git(r.p.Root, "rev-parse", "--short=7", "main")
	watch, idle := "[obor] T-1 merged "+sha, idleLineText("T-1 merged "+sha)
	r.settleIdle()
	r.waitFor("both lines posted", func() bool {
		return r.inbox.count("pj-lead", watch) == 1 && r.inbox.count("pj-lead", idle) == 1
	})
	wi, ii := -1, -1
	for i, p := range r.inbox.posted() {
		switch p {
		case "pj-lead: " + watch:
			wi = i
		case "pj-lead: " + idle:
			ii = i
		}
	}
	if wi < 0 || ii < 0 || wi > ii {
		t.Errorf("watch line at %d, idle line at %d; want the watch line first: %q", wi, ii, r.inbox.posted())
	}
}

// idleAcked runs T-1 to its merge, waits for its idle line and acknowledges it; it returns the line.
func (r *loopRig) idleAcked() string {
	r.t.Helper()
	r.start()
	r.mergedTask()
	line := idleLineFor(r, "T-1")
	r.settleIdle()
	r.waitFor("the idle line posted", func() bool { return r.inbox.count("pj-lead", line) == 1 })
	if reply, err := r.line(wire.MethodIdle, "i1", wire.Call{Note: "nothing to dispatch"}); err != nil ||
		reply != "ACKED idle: nothing to dispatch" {
		r.t.Fatalf("idle ack: %q, %v", reply, err)
	}
	return line
}

// T-815: a brief of a role with no role worktree keeps the idle state: its acknowledgement stands.
func TestCustomRoleBriefKeepsIdle(t *testing.T) {
	t.Parallel()
	r := newLoopRig(t, "echo 1 pass", testkit.Set("roles.researcher", "sonnet"))
	line := r.idleAcked()
	if reply, err := r.line(wire.MethodBrief, "b1", wire.Call{To: "pj-researcher", Note: "look at the docs"}); err != nil ||
		!strings.HasPrefix(reply, "BRIEFED pj-researcher") {
		t.Fatalf("brief of pj-researcher: %q, %v", reply, err)
	}
	st := r.app.Status().Project.Idle
	if st == nil || !st.Acked || st.Why != "nothing to dispatch" {
		t.Fatalf("idle state %+v after a custom role's brief, want the acknowledged state", st)
	}
	if reply, err := r.line(wire.MethodIdle, "i2", wire.Call{}); err != nil || reply != "IDLE: nothing to dispatch" {
		t.Errorf("obor idle (the state): %q, %v; want the acknowledged state", reply, err)
	}
	r.passes()
	if n := r.inbox.count("pj-lead", line); n != 1 {
		t.Errorf("%d idle lines, want the one", n)
	}
}

// T-859: the idle line waits out its window. A review request ten seconds after the pipeline empties resets it: no
// line, and only the request's end starts a window that posts the line.
func TestIdleSettleReviewRequestTenSecondsLater(t *testing.T) {
	t.Parallel()
	r := newLoopRig(t, "echo 1 pass", testkit.Set("roles.reviewer", ""))
	r.start()
	r.mergedTask()
	line := idleLineFor(r, "T-1")
	r.passes() // the idle check that starts the window
	r.later(10 * time.Second)
	if n := len(r.idlePosts()); n != 0 {
		t.Fatalf("%d idle lines ten seconds in, want none", n)
	}
	if reply, err := r.line(wire.MethodReview, "r1", wire.Call{}); err != nil || reply != "REVIEW REQUESTED (now)" {
		t.Fatalf("review: %q, %v", reply, err)
	}
	r.later(2 * idleSettle) // past the window: the request is work, and no line may go out
	if n := len(r.idlePosts()); n != 0 {
		t.Fatalf("%d idle lines while the review request is in flight, want none", n)
	}
	if reply, err := r.line(wire.MethodReview, "r2", wire.Call{Clear: true}); err != nil || reply != "REVIEW REQUEST ENDED" {
		t.Fatalf("review --rm: %q, %v", reply, err)
	}
	r.settleIdle()
	r.waitFor("the idle line after the request ended", func() bool { return r.inbox.count("pj-lead", line) == 1 })
	if n := len(r.idlePosts()); n != 1 {
		t.Errorf("%d idle lines, want the one", n)
	}
}
