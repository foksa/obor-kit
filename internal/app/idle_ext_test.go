package app_test

import (
	"strings"
	"testing"
	"time"

	"obor/internal/app/event"
	"obor/internal/app/wire"
	"obor/internal/testkit"
)

// A waiting dispatch, a needs-you and a reviewer's open brief each keep the pipeline from being idle.
func TestIdleBlockers(t *testing.T) {
	t.Parallel()
	t.Run("a waiting dispatch", func(t *testing.T) {
		t.Parallel()
		bubble(t, func(t *testing.T) {
			r := newLoopRig(t)
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
	})
	t.Run("a needs-you", func(t *testing.T) {
		t.Parallel()
		bubble(t, func(t *testing.T) {
			r := newLoopRig(t)
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
	})
	t.Run("a reviewer with a brief", func(t *testing.T) {
		t.Parallel()
		bubble(t, func(t *testing.T) {
			r := newLoopRig(t, withConfig(testkit.Set("agents.roles.reviewer", "")))
			rev := r.f.Agent("w1:p6", "pj-rev", "idle", r.p.Root)
			rev.Label = "pj-rev"
			r.start()
			if reply, err := r.line("b1", &wire.BriefReq{To: "pj-rev", Text: "review the wave"}); err != nil ||
				!strings.HasPrefix(reply, "BRIEFED pj-rev") {
				t.Fatalf("brief: %q, %v", reply, err)
			}
			r.work("feature.txt", "feature\n")
			r.submit()
			r.accept("c1", r.patch(r.waitJob("pj-int", "T-1/1/review")))
			// The brief holds no merge, but the reviewer still holds its brief, so the pipeline is not idle.
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
	})
}

// obor idle "<why>" acknowledges; no ping follows, and obor status shows the why.
func TestIdleAckStopsThePing(t *testing.T) {
	t.Parallel()
	bubble(t, func(t *testing.T) {
		r := newLoopRig(t)
		r.start()
		r.mergedTask()
		line := idleLineFor(r, "T-1")
		r.settleIdle()
		r.waitFor("the idle line posted", func() bool { return r.inbox.count("pj-lead", line) == 1 })
		if reply, err := r.line("i1", &wire.IdleReq{Why: "nothing to dispatch"}); err != nil ||
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
		if reply, err := r.line("i2", &wire.IdleReq{}); err != nil || reply != "IDLE: nothing to dispatch" {
			t.Errorf("obor idle (the state): %q, %v", reply, err)
		}
	})
}

// A watch on the last task comes first, then the idle line of the same merge.
func TestIdleLineAfterTheWatchLine(t *testing.T) {
	t.Parallel()
	bubble(t, func(t *testing.T) {
		r := newLoopRig(t)
		r.start()
		if reply, err := r.watch("w1", "T-1", false); err != nil || reply != "WATCHING T-1" {
			t.Fatalf("watch: %q, %v", reply, err)
		}
		r.mergedTask()
		sha := r.scratch.TargetHead()[:7]
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
	})
}

// A brief of a role with no role worktree keeps the idle state: its acknowledgement stands.
func TestCustomRoleBriefKeepsIdle(t *testing.T) {
	t.Parallel()
	bubble(t, func(t *testing.T) {
		r := newLoopRig(t, withConfig(testkit.Set("agents.roles.researcher", "sonnet")))
		line := r.idleAcked()
		if reply, err := r.line("b1", &wire.BriefReq{To: "pj-researcher", Text: "look at the docs"}); err != nil ||
			!strings.HasPrefix(reply, "BRIEFED pj-researcher") {
			t.Fatalf("brief of pj-researcher: %q, %v", reply, err)
		}
		st := r.app.Status().Project.Idle
		if st == nil || !st.Acked || st.Why != "nothing to dispatch" {
			t.Fatalf("idle state %+v after a custom role's brief, want the acknowledged state", st)
		}
		if reply, err := r.line("i2", &wire.IdleReq{}); err != nil || reply != "IDLE: nothing to dispatch" {
			t.Errorf("obor idle (the state): %q, %v; want the acknowledged state", reply, err)
		}
		r.passes()
		if n := r.inbox.count("pj-lead", line); n != 1 {
			t.Errorf("%d idle lines, want the one", n)
		}
	})
}

// The idle line waits out its window. A review request ten seconds after the pipeline empties resets it: no
// line, and only the request's end starts a window that posts the line.
func TestIdleSettleReviewRequestTenSecondsLater(t *testing.T) {
	t.Parallel()
	bubble(t, func(t *testing.T) {
		r := newLoopRig(t, withConfig(testkit.Set("agents.roles.reviewer", "")))
		// The reviewer busy: the request stays in flight (every workflow has a review git: an idle reviewer would
		// start the review at once).
		rev := r.f.Agent("w1:p5", "pj-rev", "working", r.p.Root)
		rev.Label = "pj-rev"
		r.start()
		r.mergedTask()
		line := idleLineFor(r, "T-1")
		r.passes() // the idle check that starts the window
		r.later(10 * time.Second)
		if n := len(r.idlePosts()); n != 0 {
			t.Fatalf("%d idle lines ten seconds in, want none", n)
		}
		if reply, err := r.line("r1", &wire.ReviewReq{}); err != nil || reply != "REVIEW REQUESTED (now)" {
			t.Fatalf("review: %q, %v", reply, err)
		}
		r.later(2 * idleSettle) // past the window: the request is work, and no line may go out
		if n := len(r.idlePosts()); n != 0 {
			t.Fatalf("%d idle lines while the review request is in flight, want none", n)
		}
		if reply, err := r.line("r2", &wire.ReviewReq{Clear: true}); err != nil || reply != "REVIEW REQUEST ENDED" {
			t.Fatalf("review --rm: %q, %v", reply, err)
		}
		r.settleIdle()
		r.waitFor("the idle line after the request ended", func() bool { return r.inbox.count("pj-lead", line) == 1 })
		if n := len(r.idlePosts()); n != 1 {
			t.Errorf("%d idle lines, want the one", n)
		}
	})
}
