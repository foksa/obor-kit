package app_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"obor/internal/app"
	"obor/internal/app/event"
	"obor/internal/app/port"
	"obor/internal/app/wire"
	"obor/internal/testkit"
)

// T-803: serve tells the lead once when the pipeline empties, the lead acknowledges with obor idle, and an
// unacknowledged idle state pings the lead and the owner once after 30 minutes.

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

// working2 writes T-2 dispatched and its work job fetched: in flight, on no lane of the rig.
func (r *loopRig) working2() {
	r.t.Helper()
	r.logRecords(
		record(r.t, "T-2", 1, event.StepDispatched, event.KindFact, "", map[string]any{"lane": "L-02", "dir": r.p.LaneDir("L-02"),
			"agent": "pj-l-02", "branch": "task/T-2", "target": "main"}),
		record(r.t, "T-2", 1, event.StepWork, event.KindIntent, event.ClassPrompt, map[string]any{"agent": "pj-l-02",
			"job": "T-2/1/work"}),
		record(r.t, "T-2", 1, event.StepDelivered, event.KindFact, "", event.FactData{Job: "T-2/1/work"}),
	)
}

// dropT drops task as the lead with why.
func (r *loopRig) dropT(callID, task, why string) {
	r.t.Helper()
	if _, err := wire.Verdict(context.Background(), r.sock, wire.MethodDrop, wire.Call{Root: r.p.Root, Role: "lead",
		Agent: "pj-lead", CallID: callID, Task: task, Note: why}); err != nil {
		r.t.Fatalf("drop %s: %v", task, err)
	}
}

// mergedTask runs T-1 from its task_done to its merge (as mergedT1, for a rig that has no dispatch to make).
func (r *loopRig) mergedTask() {
	r.t.Helper()
	r.work("feature.txt", "feature\n")
	r.mergedT1()
}

// T-803: the last task's merge posts one idle line, with the merge it followed; no second one on later passes.
func TestIdleAfterTheLastMergePostsOneLine(t *testing.T) {
	t.Parallel()
	r := newLoopRig(t, "echo 1 pass")
	r.start()
	r.mergedTask()
	line := idleLineFor(r, "T-1")
	r.passes()
	if n := len(r.idlePosts()); n != 0 {
		t.Fatalf("%d idle lines in the settle window, want none", n)
	}
	r.settleIdle()
	r.waitFor("the idle line posted", func() bool { return r.inbox.count("pj-lead", line) == 1 })
	r.passes()
	if got := r.idlePosts(); len(got) != 1 || got[0] != line {
		t.Errorf("idle lines %q, want one %q", got, line)
	}
	st := r.app.Status().Project.Idle
	sha := r.p.Git(r.p.Root, "rev-parse", "--short=7", "main")
	if st == nil || st.Acked || st.Why != "" || st.Last != "T-1 merged "+sha {
		t.Errorf("status idle %+v, want the unacknowledged state of T-1's merge", st)
	}
}

// T-803: a drop of the last task posts one idle line naming the drop.
func TestIdleAfterADropPostsOneLine(t *testing.T) {
	t.Parallel()
	r := newLoopRig(t, "echo 1 pass")
	r.start()
	r.dropT("x1", "T-1", "superseded by T-2")
	line := idleLineText("T-1 dropped: superseded by T-2")
	r.settleIdle()
	r.waitFor("the idle line posted", func() bool { return r.inbox.count("pj-lead", line) == 1 })
	r.passes()
	if got := r.idlePosts(); len(got) != 1 || got[0] != line {
		t.Errorf("idle lines %q, want one %q", got, line)
	}
}

// T-803: a merge while another task is in flight is no idle transition; its drop later is.
func TestIdleNotWhileAnotherTaskRuns(t *testing.T) {
	t.Parallel()
	r := newLoopRig(t, "echo 1 pass")
	r.p.Dispatched("L-01")
	r.working2()
	r.start()
	r.mergedTask()
	line := idleLineFor(r, "T-1")
	r.passes()
	r.passes()
	if st := r.app.Status().Project.Idle; st != nil {
		t.Fatalf("idle state %+v while T-2 works", st)
	}
	if n := r.inbox.count("pj-lead", line); n != 0 {
		t.Fatalf("an idle line %q while T-2 works", line)
	}
	r.dropT("x1", "T-2", "not needed")
	dropped := idleLineText("T-2 dropped: not needed")
	r.settleIdle()
	r.waitFor("the idle line after the drop", func() bool { return r.inbox.count("pj-lead", dropped) == 1 })
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

// T-803: without an acknowledgement the lead gets one more line and the owner a notification after 30 minutes, and
// never a third.
func TestIdlePingsOnceAfter30Minutes(t *testing.T) {
	t.Parallel()
	r := newLoopRig(t, "echo 1 pass")
	r.start()
	r.mergedTask()
	line := idleLineFor(r, "T-1")
	r.settleIdle()
	r.waitFor("the idle line posted", func() bool { return r.inbox.count("pj-lead", line) == 1 })
	r.later(28 * time.Minute) // 29 min from the idle fact, the line having waited out its window
	if got := len(r.idlePosts()); got != 1 {
		t.Fatalf("%d idle lines after 28m, want one", got)
	}
	if n := countReq(r.srv, "notification.show"); n != 0 {
		t.Fatalf("%d notifications after 28m, want none", n)
	}
	r.later(2 * time.Minute)
	r.waitFor("the ping line", func() bool { return len(r.idlePosts()) == 2 })
	r.waitFor("the owner's notification", func() bool { return countReq(r.srv, "notification.show") == 1 })
	if got := r.idlePosts(); !strings.HasPrefix(got[1], line) || !strings.Contains(got[1], "(again, ") {
		t.Errorf("the ping line %q, want the idle line again", got[1])
	}
	r.later(2 * time.Hour)
	r.passes()
	if got := len(r.idlePosts()); got != 2 {
		t.Errorf("%d idle lines after two hours, want two", got)
	}
	if n := countReq(r.srv, "notification.show"); n != 1 {
		t.Errorf("%d notifications, want one", n)
	}
}

// T-803: a restart derives the idle state from the store: an unacknowledged one pings once, an acknowledged one is
// silent.
func TestIdleRestartDerivesTheState(t *testing.T) {
	t.Parallel()
	t.Run("unacknowledged pings once", func(t *testing.T) {
		t.Parallel()
		r := newLoopRig(t, "echo 1 pass")
		r.start()
		r.mergedTask()
		line := idleLineFor(r, "T-1")
		r.settleIdle()
		r.waitFor("the idle line posted", func() bool { return r.inbox.count("pj-lead", line) == 1 })
		r.stop()
		r.start()
		r.passes()
		if got := len(r.idlePosts()); got != 1 {
			t.Fatalf("%d idle lines after a restart before 30m, want one", got)
		}
		r.later(31 * time.Minute)
		r.waitFor("the ping line", func() bool { return len(r.idlePosts()) == 2 })
		// The ping's notification is its own intent and result after the line: let it finish before the restart, or the
		// replay reconciles the open intent and notifies a second time (at-least-once, not what this test measures).
		r.waitFor("the owner's notification", func() bool { return countReq(r.srv, "notification.show") == 1 })
		r.stop()
		r.start()
		r.later(time.Hour)
		r.passes()
		if got := len(r.idlePosts()); got != 2 {
			t.Errorf("%d idle lines after a second restart, want two", got)
		}
		if n := countReq(r.srv, "notification.show"); n != 1 {
			t.Errorf("%d notifications after two restarts, want one", n)
		}
	})
	t.Run("acknowledged is silent", func(t *testing.T) {
		t.Parallel()
		r := newLoopRig(t, "echo 1 pass")
		r.start()
		r.mergedTask()
		line := idleLineFor(r, "T-1")
		r.settleIdle()
		r.waitFor("the idle line posted", func() bool { return r.inbox.count("pj-lead", line) == 1 })
		if _, err := r.line(wire.MethodIdle, "i1", wire.Call{Note: "nothing to dispatch"}); err != nil {
			t.Fatal(err)
		}
		r.stop()
		r.start()
		r.later(time.Hour)
		r.passes()
		if got := r.idlePosts(); len(got) != 1 || got[0] != line {
			t.Errorf("idle lines %q after the restart, want the one line", got)
		}
		if n := countReq(r.srv, "notification.show"); n != 0 {
			t.Errorf("%d notifications, want none", n)
		}
		if reply, err := r.line(wire.MethodIdle, "i2", wire.Call{}); err != nil || reply != "IDLE: nothing to dispatch" {
			t.Errorf("the state after the restart: %q, %v", reply, err)
		}
	})
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

// T-803: a dispatch ends the idle epoch (obor status shows none): the pipeline has work again.
func TestDispatchEndsIdle(t *testing.T) {
	t.Parallel()
	r := newDispatchRig(t)
	r.start()
	r.mustDispatch("d1")
	r.waitStep("1 result work")
	r.mergedTask()
	line := idleLineFor(r, "T-1")
	r.settleIdle()
	r.waitFor("the idle line posted", func() bool { return r.inbox.count("pj-lead", line) == 1 })
	r.addLanes(port.Lane{Name: "L-02", Dir: r.p.LaneDir("L-02"), State: "free"})
	if reply, err := r.dispatchCall(wire.Call{Root: r.p.Root, Agent: "pj-lead", Role: "lead", CallID: "d2", Task: "T-2",
		Lane: "L-02", Types: []string{"sonnet"}}); err != nil || !strings.HasPrefix(reply, "DISPATCHED T-2") {
		t.Fatalf("dispatch T-2: %q, %v", reply, err)
	}
	if st := r.app.Status().Project.Idle; st != nil {
		t.Errorf("idle state %+v after a dispatch, want none", st)
	}
}

// T-815 (RV-104 #2): idle is an epoch. A reviewer's or deep dive's brief ends the epoch — obor status shows no idle state and
// obor idle "…" is refused — and the brief's end starts a new one: one line, unacknowledged. A custom role's brief
// ends nothing.

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

// briefEndsIdle briefs agent with text and asserts that the acknowledged epoch ended: no idle state in obor status,
// and an acknowledgement refused as NOT IDLE.
func (r *loopRig) briefEndsIdle(agent, text string) {
	r.t.Helper()
	if reply, err := r.line(wire.MethodBrief, "b1", wire.Call{To: agent, Note: text}); err != nil ||
		!strings.HasPrefix(reply, "BRIEFED "+agent) {
		r.t.Fatalf("brief of %s: %q, %v", agent, reply, err)
	}
	if st := r.app.Status().Project.Idle; st != nil {
		r.t.Fatalf("idle state %+v while %s is briefed, want none", st, agent)
	}
	if reply, err := r.line(wire.MethodIdle, "i2", wire.Call{Note: "still nothing"}); err != nil || reply != "NOT IDLE" {
		r.t.Fatalf("the ack of an ended epoch: %q, %v; want NOT IDLE", reply, err)
	}
	// The brief delivered before the agent works on it (finishesBrief): work seen before its delivery is no work on it,
	// and under load the delivering pass may come late (T-852's red gate).
	r.waitFor("the brief posted", func() bool { return r.inbox.count(agent, "[obor pj-lead] "+text) == 1 })
	r.waitBriefDelivered()
}

// finishesBrief makes the agent in pane work on its brief and finish it; done is the finished line the lead gets.
func (r *loopRig) finishesBrief(pane, agent, done string) {
	r.t.Helper()
	r.agentIs(pane, agent, "working")
	r.clock.Add(5 * time.Minute)
	r.passes()
	r.agentIs(pane, agent, "idle")
	r.waitFor("the finished line posted", func() bool { return r.inbox.count("pj-lead", done) == 1 })
}

// newIdleEpoch waits for the line the ended epoch's successor posts (the same last), then checks it is one line of a
// new, unacknowledged epoch and that later passes add none.
func (r *loopRig) newIdleEpoch(line string) {
	r.t.Helper()
	r.passes() // the idle check that starts the new epoch's window
	since := r.clock.Now().Unix()
	r.later(idleSettle) // the first check past it posts the line
	r.waitFor("the new idle line", func() bool { return r.inbox.count("pj-lead", line) == 2 })
	st := r.app.Status().Project.Idle
	sha := r.p.Git(r.p.Root, "rev-parse", "--short=7", "main")
	if st == nil || st.Acked || st.Why != "" || st.Last != "T-1 merged "+sha || st.Since != since {
		r.t.Fatalf("status idle %+v, want a new, unacknowledged state since now with T-1's merge", st)
	}
	r.passes()
	r.passes()
	if n := r.inbox.count("pj-lead", line); n != 2 {
		r.t.Errorf("%d idle lines %q, want the first epoch's and the new one's", n, line)
	}
}

// T-815: a role worktree agent's brief (a reviewer's, a deep dive's) ends the acknowledged idle epoch, and the brief's end starts
// a new one.
func TestReviewerBriefEndsIdleAndANewEpochStarts(t *testing.T) {
	t.Parallel()
	for _, c := range []struct{ role, pane, agent, brief string }{
		{"reviewer", "w1:p6", "pj-rev", "review the wave"},
		{"deep_dive", "w1:p7", "pj-deep-dive", "dig into the flake"},
	} {
		t.Run(c.role, func(t *testing.T) {
			t.Parallel()
			r := newLoopRig(t, "echo 1 pass", testkit.Set("roles."+c.role, ""))
			r.review = rigReviewGit{} // an idle deep dive's brief moves its worktree (T-853)
			a := r.f.Agent(c.pane, c.agent, "idle", r.p.Root)
			a.Workspace, a.Label = "w1", c.agent
			line := r.idleAcked()
			r.briefEndsIdle(c.agent, c.brief)
			r.finishesBrief(c.pane, c.agent, "[obor] "+c.agent+" finished (briefed 5m ago)")
			r.newIdleEpoch(line)
		})
	}
}

// T-815: a brief of a role with no role worktree keeps the idle epoch in force: its acknowledgement stands.
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
		t.Fatalf("idle state %+v after a custom role's brief, want the acknowledged epoch in force", st)
	}
	if reply, err := r.line(wire.MethodIdle, "i2", wire.Call{}); err != nil || reply != "IDLE: nothing to dispatch" {
		t.Errorf("obor idle (the state): %q, %v; want the epoch in force", reply, err)
	}
	r.passes()
	if n := r.inbox.count("pj-lead", line); n != 1 {
		t.Errorf("%d idle lines, want the one", n)
	}
}

// T-815: serve restarts while the reviewer's brief is open: the replay folds the ended epoch, so the brief keeps the
// pipeline not idle and its end starts the new epoch as without the restart.
func TestReviewerBriefEndsIdleAcrossARestart(t *testing.T) {
	t.Parallel()
	r := newLoopRig(t, "echo 1 pass", testkit.Set("roles.reviewer", ""))
	rev := r.f.Agent("w1:p6", "pj-rev", "idle", r.p.Root)
	rev.Workspace, rev.Label = "w1", "pj-rev"
	line := r.idleAcked()
	if reply, err := r.line(wire.MethodBrief, "b1", wire.Call{To: "pj-rev", Note: "review the wave"}); err != nil ||
		!strings.HasPrefix(reply, "BRIEFED pj-rev") {
		t.Fatalf("brief: %q, %v", reply, err)
	}
	r.waitFor("the brief posted", func() bool { return r.inbox.count("pj-rev", "[obor pj-lead] review the wave") == 1 })
	r.waitBriefDelivered()
	r.stop()
	r.start()
	if st := r.app.Status().Project.Idle; st != nil {
		r.t.Fatalf("idle state %+v after a restart mid-brief, want none", st)
	}
	if reply, err := r.line(wire.MethodIdle, "i2", wire.Call{Note: "still nothing"}); err != nil || reply != "NOT IDLE" {
		r.t.Fatalf("the ack of an ended epoch after a restart: %q, %v; want NOT IDLE", reply, err)
	}
	r.finishesBrief("w1:p6", "pj-rev", "[obor] pj-rev finished (briefed 5m ago)")
	r.newIdleEpoch(line)
}

// T-818 (RV-104 #2): a review request is work (skills/lead/commands.md: a review request ends idle). It ends the
// acknowledged idle epoch — obor status shows no idle state and obor idle "…" is refused — and its end starts a new
// one: one line, unacknowledged.

// reviewEndsIdle asserts the request in flight ended the acknowledged epoch: no idle state in obor status, and an
// acknowledgement refused as NOT IDLE.
func (r *loopRig) reviewEndsIdle() {
	r.t.Helper()
	if st := r.app.Status().Project.Idle; st != nil {
		r.t.Fatalf("idle state %+v while a review request is in flight, want none", st)
	}
	if reply, err := r.line(wire.MethodIdle, "i2", wire.Call{Note: "still nothing"}); err != nil || reply != "NOT IDLE" {
		r.t.Fatalf("the ack of an ended epoch: %q, %v; want NOT IDLE", reply, err)
	}
}

// requestReview records a request from the lead and checks its reply.
func (r *loopRig) requestReview(callID string, after []string, want string) {
	r.t.Helper()
	if reply, err := r.line(wire.MethodReview, callID, wire.Call{After: after}); err != nil || reply != want {
		r.t.Fatalf("review: %q, %v; want %q", reply, err, want)
	}
}

// endReview ends the open request (--rm); the next pass starts a new idle epoch when nothing else is in flight.
func (r *loopRig) endReview(callID, line string) {
	r.t.Helper()
	if reply, err := r.line(wire.MethodReview, callID, wire.Call{Clear: true}); err != nil || reply != "REVIEW REQUEST ENDED" {
		r.t.Fatalf("review --rm: %q, %v", reply, err)
	}
	r.newIdleEpoch(line)
}

// T-818: a review request (for now, or queued on --after) ends the acknowledged epoch, and its end starts a new one.
func TestReviewRequestEndsIdleAndANewEpochStarts(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		name  string
		after []string
		want  string
	}{
		{"now", nil, "REVIEW REQUESTED (now)"},
		{"after T-1", []string{"T-1"}, "REVIEW REQUESTED (after T-1)"},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			r := newLoopRig(t, "echo 1 pass", testkit.Set("roles.reviewer", ""))
			line := r.idleAcked()
			r.requestReview("r1", c.after, c.want)
			r.reviewEndsIdle()
			r.endReview("r2", line)
		})
	}
}

// T-818: a restart with the request pending replays it: not idle, the acknowledgement refused, and its end starts the
// new epoch as without the restart.
func TestReviewRequestEndsIdleAcrossARestart(t *testing.T) {
	t.Parallel()
	r := newLoopRig(t, "echo 1 pass", testkit.Set("roles.reviewer", ""))
	line := r.idleAcked()
	r.requestReview("r1", nil, "REVIEW REQUESTED (now)")
	r.stop()
	r.start()
	r.reviewEndsIdle()
	r.endReview("r2", line)
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
	r.requestReview("r1", nil, "REVIEW REQUESTED (now)")
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

// T-859: the line goes out on the first idle check past the window, never before; the state's reminder follows 30
// minutes after the pipeline emptied, as before.
func TestIdleSettleWaitsTheWindow(t *testing.T) {
	t.Parallel()
	r := newLoopRig(t, "echo 1 pass")
	r.start()
	r.mergedTask()
	line := idleLineFor(r, "T-1")
	r.passes() // the idle check that starts the window
	r.later(idleSettle - time.Second)
	if n := len(r.idlePosts()); n != 0 {
		t.Fatalf("%d idle lines one second before the window ended, want none", n)
	}
	r.later(time.Second) // the first check past it
	r.waitFor("the idle line posted", func() bool { return r.inbox.count("pj-lead", line) == 1 })
	r.passes()
	if n := len(r.idlePosts()); n != 1 {
		t.Errorf("%d idle lines after another pass, want the one", n)
	}
	r.later(28 * time.Minute) // 29 minutes after the pipeline emptied
	if n := len(r.idlePosts()); n != 1 {
		t.Errorf("%d idle lines before the reminder, want the one", n)
	}
	r.later(2 * time.Minute)
	r.waitFor("the reminder", func() bool { return len(r.idlePosts()) == 2 })
}

// T-859: a restart while the pipeline is idle starts the window again: no line on the old clock, one after the new
// window.
func TestIdleRestartStartsTheWindowAgain(t *testing.T) {
	t.Parallel()
	r := newLoopRig(t, "echo 1 pass")
	r.start()
	r.mergedTask()
	line := idleLineFor(r, "T-1")
	r.passes() // the first app's window starts, the line stays unsent
	r.later(30 * time.Second)
	r.stop()
	r.start()
	r.passes() // the restarted app's window starts
	r.later(idleSettle - time.Second)
	if n := len(r.idlePosts()); n != 0 {
		t.Fatalf("%d idle lines before the restarted window ended, want none", n)
	}
	r.later(time.Second)
	r.waitFor("the idle line posted", func() bool { return r.inbox.count("pj-lead", line) == 1 })
	if n := len(r.idlePosts()); n != 1 {
		t.Errorf("%d idle lines, want the one", n)
	}
}

// idleAck is the lead's acknowledgement of the idle state in force, with why.
func (r *loopRig) idleAck(callID, why string) {
	r.t.Helper()
	if reply, err := r.line(wire.MethodIdle, callID, wire.Call{Note: why}); err != nil ||
		reply != "ACKED idle: "+why {
		r.t.Fatalf("idle ack: %q, %v", reply, err)
	}
}

// T-860: an acknowledgement made inside the settle window holds as one made after the line: no line, no reminder. A
// new epoch after work resumes and ends posts its one line as usual.
func TestIdleAckInsideTheSettleWindowPostsNothing(t *testing.T) {
	t.Parallel()
	r := newLoopRig(t, "echo 1 pass", testkit.Set("roles.reviewer", ""))
	r.start()
	r.mergedTask()
	line := idleLineFor(r, "T-1")
	r.passes() // the idle check that starts the window
	r.later(25 * time.Second)
	r.idleAck("i1", "nothing to dispatch")
	r.later(idleSettle) // past the window: the ack holds
	r.passes()
	if got := r.idlePosts(); len(got) != 0 {
		t.Fatalf("%d idle lines after an ack inside the window, want none: %q", len(got), got)
	}
	r.later(31 * time.Minute) // past the reminder's deadline
	r.passes()
	if got := r.idlePosts(); len(got) != 0 {
		t.Errorf("%d idle lines after the reminder's deadline, want none: %q", len(got), got)
	}
	if n := countReq(r.srv, "notification.show"); n != 0 {
		t.Errorf("%d notifications, want none", n)
	}
	if reply, err := r.line(wire.MethodIdle, "i2", wire.Call{}); err != nil || reply != "IDLE: nothing to dispatch" {
		t.Errorf("obor idle (the state): %q, %v; want the acknowledgement's why", reply, err)
	}
	// Work resumes (a review request, T-818) and ends: a new epoch posts its own one line.
	r.requestReview("r1", nil, "REVIEW REQUESTED (now)")
	if reply, err := r.line(wire.MethodReview, "r2", wire.Call{Clear: true}); err != nil || reply != "REVIEW REQUEST ENDED" {
		t.Fatalf("review --rm: %q, %v", reply, err)
	}
	r.settleIdle()
	r.waitFor("the new epoch's idle line", func() bool { return r.inbox.count("pj-lead", line) == 1 })
	r.passes()
	if got := r.idlePosts(); len(got) != 1 || got[0] != line {
		t.Errorf("idle lines %q, want the new epoch's one %q", got, line)
	}
}

// T-860: a restart with the acknowledgement recorded inside the window replays it: no line, and the state stays
// acknowledged.
func TestIdleAckInsideTheSettleWindowSurvivesARestart(t *testing.T) {
	t.Parallel()
	r := newLoopRig(t, "echo 1 pass")
	r.start()
	r.mergedTask()
	r.passes() // the idle check that starts the window
	r.later(25 * time.Second)
	r.idleAck("i1", "nothing to dispatch")
	r.stop()
	r.start()
	r.later(2 * idleSettle) // the replayed window and well past it
	r.passes()
	if got := r.idlePosts(); len(got) != 0 {
		t.Errorf("%d idle lines after the restart, want none: %q", len(got), got)
	}
	if n := countReq(r.srv, "notification.show"); n != 0 {
		t.Errorf("%d notifications after the restart, want none", n)
	}
	if reply, err := r.line(wire.MethodIdle, "i2", wire.Call{}); err != nil || reply != "IDLE: nothing to dispatch" {
		t.Errorf("obor idle (the state) after the restart: %q, %v", reply, err)
	}
}
