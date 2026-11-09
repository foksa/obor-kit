package app_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"obor/internal/app"
	"obor/internal/app/event"
	"obor/internal/app/port"
	"obor/internal/app/wire"
	"obor/internal/store"
	"obor/internal/testkit"
)

// A new drive takes the first free lane: a new task id, its spec "Owner drive: <name>" stored, fact dispatch
// {drive}; serve prepares the lane on the task branch and the task is driving: no worker start, no work job, no job at
// all. A repeated call id gets the stored reply; the same name again starts its agent again (T-755), nothing recorded.
func TestDriveNewOnAFreeUntypedLane(t *testing.T) {
	t.Parallel()
	r := newLoopRig(t, dispatching(), started())
	if n := len(r.records()); n != 0 {
		t.Fatalf("precondition: records before the drive: %q", r.steps())
	}
	want := "DRIVE T-1 → L-01 (pj-l-01-drive, " + r.hardType() + ")"
	if reply, err := r.drive("v1", "web design"); err != nil || reply != want {
		t.Fatalf("drive: %q, %v; want %q", reply, err, want)
	}
	r.waitFor("T-1 driving", func() bool { return r.stateOf("T-1") == wire.TaskDriving })
	r.passes()
	eq(t, r.steps(), []string{"1 fact dispatch", "1 intent lane.prepare", "1 result lane.prepare"}, "records")
	row, ok, err := store.ReadSpecAt(r.p.Root, "T-1")
	if err != nil || !ok || !strings.Contains(row.Text, "Owner drive: web design") {
		t.Errorf("T-1's spec %q (%v, %v), want the drive's", row.Text, ok, err)
	}
	lr, ok, err := store.ReadLaneAt(r.p.Root, "L-01")
	if err != nil || !ok || lr.Task != "T-1" || lr.Branch != "task/T-1" || lr.Base == "" ||
		lr.Finished != 0 {
		t.Errorf("L-01's lane row %+v (%v, %v): want T-1's drive, prepared, not finished", lr, ok, err)
	}
	if _, starts := r.disp.applied(); starts != 0 || r.launchesOf("pj-l-01") != 0 || r.workPrompts() != 0 {
		t.Errorf("a drive started a worker (%d starts, %d launches) or sent a work job (%d)", starts,
			r.launchesOf("pj-l-01"), r.workPrompts())
	}
	if ts := r.task("T-1"); ts.Job != "" {
		t.Errorf("a driving task has job %q open", ts.Job)
	}
	if reply, err := r.drive("v1", "web design"); err != nil || reply != want {
		t.Errorf("the same call id again: %q, %v; want the stored reply", reply, err)
	}
	if reply, err := r.drive("v2", "web design"); err != nil || reply != want+" again: its agent is not running" {
		t.Errorf("the same name again: %q, %v; want its start again (T-755), no second drive", reply, err)
	}
	if n := r.count("1 fact dispatch"); n != 1 {
		t.Errorf("dispatch facts %d after the same name again, want 1", n)
	}
}

// RV-167 #2: a project whose hard level names no model gives the shape-2 config path in the drive's hint.
func TestDriveNoModelNamesShape2Path(t *testing.T) {
	t.Parallel()
	r := newLoopRig(t, dispatching(), started(), withConfig(testkit.Set("difficulty.hard", []string{})))
	_, err := r.drive("v1", "web")
	if err == nil || !strings.Contains(err.Error(), "no model for level hard (obor config get .agents.difficulty)") {
		t.Fatalf("drive with no hard model: %v; want the shape-2 config path", err)
	}
}

// No free lane: refused with the lanes' states, nothing recorded and no wait (a working lane holds its task).
func TestDriveNoFreeLaneRefused(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		name string
		lane func(l *port.Lane)
		why  string
	}{
		{"working lane", func(l *port.Lane) { l.State, l.Task = "working", "T-9" }, "no free lane (L-01 working T-9)"},
		{"dirty lane", func(l *port.Lane) { l.State = "dirty-free" }, "no free lane (L-01 dirty-free)"},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			r := newLoopRig(t, dispatching(), started())
			r.setLane(c.lane)
			_, err := r.drive("v1", "web")
			if err == nil || !strings.Contains(err.Error(), c.why) {
				t.Fatalf("drive: %v, want refused with %q", err, c.why)
			}
			r.passes()
			if n := len(r.records()); n != 0 {
				t.Errorf("a refused drive recorded %q", r.steps())
			}
			if st := r.app.Status().Project.Tasks; len(st) != 0 {
				t.Errorf("a refused drive waits: %+v", st)
			}
		})
	}
}

// --model is honoured, effort included (every prepare held, so each task stays on the lane it took): a drive of
// sonnet:high takes L-01, T-2's dispatch of sonnet L-02, a second drive L-03; an unknown model is refused.
func TestDriveModelHonoured(t *testing.T) {
	t.Parallel()
	r := newLoopRig(t, dispatching(), withConfig(testkit.Set("lanes.count", 3)))
	r.addLanes(port.Lane{Name: "L-02", Dir: r.p.Lane("L-02", ""), State: "free"},
		port.Lane{Name: "L-03", Dir: r.p.Lane("L-03", ""), State: "free"})
	r.disp.set(func(d *dispatchFakes) { d.cut["prepare"] = "before" })
	r.start()
	if _, err := wire.Verdict(context.Background(), r.sock, &wire.SpecSetReq{Envelope: wire.Envelope{Root: r.p.Root,
		Role: "lead", Agent: "lead", CallID: "s2"}, Task: "T-2",
		Spec: "# T-2 — Other\n\nDifficulty: medium\n"}); err != nil {
		t.Fatal(err)
	}
	reply, err := r.drive("v1", "tweak", "sonnet:high")
	if err != nil || reply != "DRIVE T-3 → L-01 (pj-l-01-drive, sonnet:high)" {
		t.Fatalf("drive: %q, %v", reply, err)
	}
	var d map[string]any
	for _, rec := range r.records() {
		if rec.Task == "T-3" && rec.Step == event.StepDispatched {
			_ = json.Unmarshal(rec.Data, &d)
		}
	}
	if d["type"] != "sonnet:high" || d["effort"] != "high" || d["drive"] != true {
		t.Errorf("the drive's dispatch fact %v: want type sonnet:high, effort high, drive", d)
	}
	if reply, err := r.dispatchCall(wire.DispatchReq{Envelope: wire.Envelope{Root: r.p.Root, Agent: "pj-lead",
		Role: "lead", CallID: "d2"}, Task: "T-2", Types: []string{"sonnet"}}); err != nil || !strings.HasPrefix(reply,
		"DISPATCHED T-2 → L-02") {
		t.Fatalf("T-2 of sonnet beside a sonnet drive: %q, %v; want dispatched", reply, err)
	}
	if reply, err := r.drive("v2", "second", "sonnet"); err != nil || !strings.HasPrefix(reply, "DRIVE T-4 → L-03 (") {
		t.Errorf("a second drive: %q, %v; want L-03", reply, err)
	}
	if _, err := r.drive("v3", "bad", "nosuch"); err == nil || !strings.Contains(err.Error(), "nosuch") {
		t.Errorf("an unknown --model: %v, want refused by name", err)
	}
}

// T-912: a drive is new work only. obor drive <T-xx> on a worker's task, working, blocked (a needs-you) or finished
// (its gate running), is refused, "T-1 is not a drive; obor drive <name> starts one": nothing recorded, no stop, no
// launch, the task's state and attempt as they were (its phase may move on its own: a pass may start its gate, T-980).
// A repeated call id is refused again (nothing was stored).
func TestDriveOfAWorkersTaskRefused(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		name  string
		state string                // T-1's state before the call
		arm   func(r *loopRig) bool // T-1 into the case's state; false: not a needs-you
	}{
		{"working", "working", func(r *loopRig) bool { return false }},
		{"blocked", "needs-you", func(r *loopRig) bool {
			r.failStops(true)
			if reply, err := r.moveTo("m1", "opus"); err != nil || !strings.HasPrefix(reply, "MOVING T-1 on L-01") {
				r.t.Fatalf("move: %q, %v", reply, err)
			}
			r.waitFor("the stop's needs-you", func() bool { return r.task("T-1").Phase == wire.PhaseNeedsYou })
			r.waitFor("the refused stop's step ended", func() bool { return app.Settled(r.app, r.p.Root, "T-1") })
			return true
		}},
		{"finished", "submitted", func(r *loopRig) bool {
			release := make(chan struct{})
			r.t.Cleanup(func() { close(release) })
			r.scratch.Arm("Gate", func(ctx context.Context) error {
				select {
				case <-release:
				case <-ctx.Done():
				}
				return nil
			})
			r.work("feature.txt", "feature\n")
			r.submit()
			r.waitStep("1 intent gate")
			return false
		}},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			r := newLoopRig(t, working())
			needs := c.arm(r)
			before := r.task("T-1")
			if before.State != c.state || (before.Phase == wire.PhaseNeedsYou) != needs {
				t.Fatalf("precondition: T-1 %+v; want state %s, needs-you %v", before, c.state, needs)
			}
			n, stops, launches := len(r.records()), len(r.workerStops()), len(r.workerLaunches())
			for _, call := range []string{"v1", "v1"} {
				reply, err := r.drive(call, "T-1")
				if want := "T-1 is not a drive; obor drive <name> starts one"; err == nil || err.Error() != want {
					t.Errorf("obor drive T-1 (%s): %q, %v; want refused %q", call, reply, err, want)
				}
			}
			r.passes()
			if got := r.steps()[n:]; r.count("1 fact drive")+r.count("2 fact drive") != 0 || slices.Contains(got, "1 fact dispatch") {
				t.Errorf("the refusal recorded a drive: %q", got)
			}
			if len(r.workerStops()) != stops || len(r.workerLaunches()) != launches || r.launchesOf("pj-l-01-drive") != 0 {
				t.Errorf("the refusal stopped %q or launched %d (drive agent %d)", r.workerStops()[stops:],
					len(r.workerLaunches())-launches, r.launchesOf("pj-l-01-drive"))
			}
			if after := r.task("T-1"); after.State != before.State || after.Attempt != before.Attempt {
				t.Errorf("T-1 after the refusal %+v, before %+v: want the same state and attempt (its phase may move: "+
					"a pass may start its gate, T-980)", after, before)
			}
			if c.name == "working" && !app.IsIdentified(r.app, r.p.Root, "pj-l-01") {
				t.Errorf("the worker pj-l-01 is not running after the refusal")
			}
		})
	}
}

// obor land on the owner's behalf: submitted with the summary as its report, then gate, review, publish and the
// after-merge stop of the drive agent; the lane row is finished (the lane free once merged). A repeated call id gets
// the stored reply; land of a task not driving is refused.
func TestDriveLandGatesReviewsMergesAndStopsTheAgent(t *testing.T) {
	t.Parallel()
	r := newLoopRig(t, dispatching(), started())
	if _, err := r.landDrive("l0", "T-1", "x"); err == nil || !strings.Contains(err.Error(), "not open") {
		t.Fatalf("land before any drive: %v", err)
	}
	if _, err := r.drive("v1", "web"); err != nil {
		t.Fatal(err)
	}
	r.waitFor("T-1 driving", func() bool { return r.stateOf("T-1") == wire.TaskDriving })
	r.driveAgentUp()
	r.work("page.html", "<p>new</p>\n")
	r.scratch.Commit("L-01") // the drive agent commits its work
	if _, err := r.landDrive("l1", "T-1", ""); err == nil || !strings.Contains(err.Error(), "a summary is required") {
		t.Errorf("land with no summary: %v", err)
	}
	if reply, err := r.landDrive("l2", "T-1", "page"); err != nil || reply != "LANDED T-1: submitted (gate, review and merge follow)" {
		t.Fatalf("land: %q, %v", reply, err)
	}
	if reply, err := r.landDrive("l2", "T-1", "page"); err != nil || !strings.HasPrefix(reply, "LANDED T-1") {
		t.Errorf("the same call id again: %q, %v", reply, err)
	}
	r.waitStep("1 fact submitted")
	if _, err := r.landDrive("l3", "T-1", "page"); err == nil || !strings.Contains(err.Error(), "not driving") {
		t.Errorf("land of a submitted task: %v, want refused", err)
	}
	job := r.waitJob("pj-int", "T-1/1/review")
	if !strings.Contains(job, "page") {
		t.Errorf("the review job does not carry the land's summary:\n%s", job)
	}
	r.accept("c1", r.patch(job))
	r.waitStep("1 result agent.stop")
	r.passes()
	if r.mainMerges() != 1 {
		t.Errorf("not merged: %q", r.steps())
	}
	if !slices.Contains(r.workerStops(), "pj-l-01-drive w1:p5") {
		t.Errorf("the drive agent was not stopped after the merge: %q", r.workerStops())
	}
	if lr, _, _ := store.ReadLaneAt(r.p.Root, "L-01"); lr.Finished == 0 {
		t.Errorf("L-01's row %+v is not finished after the merge", lr)
	}
	if st := r.stateOf("T-1"); st != "" {
		t.Errorf("T-1 still open after the merge: %s", st)
	}
	if r.launchesOf("pj-l-01") != 0 || len(r.driveLines()) != 0 {
		t.Errorf("a worker started (%d) or a come-back line was sent (%q)", r.launchesOf("pj-l-01"), r.driveLines())
	}
}

// A red gate goes back to the drive (the next attempt driving, one line to the lead), never to a worker: no fix job,
// no needs-you, whatever the count. The next land gates again. A serve restart replays the driving task as driving
// and sends no second line.
func TestDriveRedGateBackToDriving(t *testing.T) {
	t.Parallel()
	r := newLoopRig(t, dispatching(), started())
	r.scratch.Script("Test", "T-1", port.OutcomeRed) // red while the lane has red.txt
	if _, err := r.drive("v1", "web"); err != nil {
		t.Fatal(err)
	}
	r.waitFor("T-1 driving", func() bool { return r.stateOf("T-1") == wire.TaskDriving })
	for i, id := range []string{"l1", "l2", "l3"} { // past K: still the drive's
		r.work("red.txt", "red "+id+"\n")
		// The drive step's line can arrive before its step settles: a land then is refused ("retry when it settles").
		r.waitFor("T-1 settled", func() bool { return app.Settled(r.app, r.p.Root, "T-1") })
		if _, err := r.landDrive(id, "T-1", "try "+id); err != nil {
			t.Fatalf("land %d: %v", i+1, err)
		}
		r.waitFor("back to driving", func() bool {
			ts := r.task("T-1")
			return ts.State == wire.TaskDriving && ts.Attempt == i+2
		})
		r.waitFor("the lead's line", func() bool { return len(r.driveLines()) == i+1 })
	}
	r.passes()
	lines := r.driveLines()
	if len(lines) != 3 || !strings.HasPrefix(lines[0], "[obor] T-1 back to its owner's drive: ") ||
		!strings.Contains(lines[0], "RED") {
		t.Errorf("lead lines %q: want one per red gate naming it", lines)
	}
	for _, p := range r.prompts() {
		if strings.Contains(p.text, "/fix") || strings.Contains(p.text, "/work") {
			t.Errorf("a worker job went out: %+v", p)
		}
	}
	for _, f := range r.app.Status().Project.Facts {
		if f.Task == "T-1" && f.Step == event.StepNeedsYou {
			t.Errorf("a needs-you for the drive: %+v", f)
		}
	}
	r.stop()
	r.start() // the replay
	r.waitFor("T-1 driving after the replay", func() bool { return r.stateOf("T-1") == wire.TaskDriving })
	r.passes()
	if n := len(r.driveLines()); n != 3 {
		t.Errorf("lead lines after the replay: %d, want 3 (none again)", n)
	}
	if err := os.Remove(filepath.Join(r.lane, "red.txt")); err != nil {
		t.Fatal(err)
	}
	r.scratch.Script("Test", "T-1", "") // its lane test green again
	if _, err := r.landDrive("l4", "T-1", "fixed"); err != nil {
		t.Fatal(err)
	}
	r.waitJob("pj-int", "T-1/4/review")
}

// The integrator's hold of a drive's review goes back to the drive, with one line to the lead; no needs-you.
func TestDriveHeldReviewBackToDriving(t *testing.T) {
	t.Parallel()
	r := newLoopRig(t, dispatching(), started())
	if _, err := r.drive("v1", "web"); err != nil {
		t.Fatal(err)
	}
	r.waitFor("T-1 driving", func() bool { return r.stateOf("T-1") == wire.TaskDriving })
	r.work("page.html", "x\n")
	if _, err := r.landDrive("l1", "T-1", "page"); err != nil {
		t.Fatal(err)
	}
	r.waitJob("pj-int", "T-1/1/review")
	reply, err := wire.Verdict(context.Background(), r.sock, &wire.VerdictReq{Envelope: wire.Envelope{Root: r.p.Root,
		Agent: "pj-int", Role: "integrator", CallID: "h1"}, Verb: wire.MethodHold, Task: "T-1", Note: "which layout?"})
	if err != nil || !strings.Contains(reply, "back to its owner's drive") {
		t.Fatalf("hold: %q, %v", reply, err)
	}
	r.waitFor("back to driving", func() bool { return r.stateOf("T-1") == wire.TaskDriving })
	r.waitFor("the lead's line", func() bool { return len(r.driveLines()) == 1 })
	if l := r.driveLines()[0]; !strings.Contains(l, "held: which layout?") {
		t.Errorf("lead line %q, want the hold's why", l)
	}
	for _, f := range r.app.Status().Project.Facts {
		if f.Task == "T-1" && f.Step == event.StepNeedsYou {
			t.Errorf("a needs-you for the held drive: %+v", f)
		}
	}
}

// obor drop works on a drive unchanged: by task id (T-775: serve knows the lane), DROPPING at once (T-852), then the
// drive agent stopped and the lane freed; the task dropped.
func TestDriveDrop(t *testing.T) {
	t.Parallel()
	r := newLoopRig(t, dispatching(), started())
	if _, err := r.drive("v1", "web"); err != nil {
		t.Fatal(err)
	}
	r.waitFor("T-1 driving", func() bool { return r.stateOf("T-1") == wire.TaskDriving })
	r.driveAgentUp()
	reply, err := wire.Verdict(context.Background(), r.sock, &wire.DropReq{Envelope: wire.Envelope{Root: r.p.Root,
		Agent: "pj-lead", Role: "lead", CallID: "x1"}, Task: "T-1", Why: "not needed"})
	if want := "DROPPING T-1 from L-01 (not needed; stopping pj-l-01-drive)"; err != nil || reply != want {
		t.Fatalf("drop: %q, %v", reply, err)
	}
	r.waitFor("dropped", func() bool { return r.stateOf("T-1") == "dropped" })
	if !slices.Contains(r.workerStops(), "pj-l-01-drive w1:p5") { // by its pane, as serve identifies it (T-758)
		t.Errorf("the drive agent was not stopped at its pane: %q", r.workerStops())
	}
	if n := r.disp.called("free"); n != 1 {
		t.Errorf("lane freed %d times, want once", n)
	}
	if _, err := r.landDrive("l1", "T-1", "x"); err == nil {
		t.Error("land of a dropped drive accepted")
	}
}

// A serve restart replays a driving task as driving: no second prepare, no worker start, its lane row kept.
func TestDriveReplayedAsDriving(t *testing.T) {
	t.Parallel()
	r := newLoopRig(t, dispatching(), started())
	if _, err := r.drive("v1", "web"); err != nil {
		t.Fatal(err)
	}
	r.waitFor("T-1 driving", func() bool { return r.stateOf("T-1") == wire.TaskDriving })
	r.passes()
	n := len(r.records())
	r.stop()
	r.start()
	r.waitFor("T-1 driving after the replay", func() bool { return r.stateOf("T-1") == wire.TaskDriving })
	r.passes()
	if len(r.records()) != n {
		t.Errorf("the replay recorded more: %q", r.steps()[n:])
	}
	if lr, _, _ := store.ReadLaneAt(r.p.Root, "L-01"); lr.Task != "T-1" || lr.Finished != 0 {
		t.Errorf("L-01's row after the replay: %+v", lr)
	}
	if reply, err := r.drive("v1", "web"); err != nil || !strings.HasPrefix(reply, "DRIVE T-1") {
		t.Errorf("the stored reply after the replay: %q, %v", reply, err)
	}
}

// driving is the drive rig with T-1 a new drive on L-01, driving, its agent pj-l-01-drive up in w1:p5.
func driving(t *testing.T) *loopRig {
	t.Helper()
	r := newLoopRig(t, dispatching(), started())
	r.connectedAgain()
	if _, err := r.drive("v1", "web"); err != nil {
		t.Fatal(err)
	}
	r.waitFor("T-1 driving", func() bool { return r.stateOf("T-1") == wire.TaskDriving })
	r.driveAgentUp()
	return r
}

// The drive agent's obor tell-lead: one fact message{driver} to the lead, its line naming the task and the reply, posted
// to the lead's session once; a repeated call id gets the stored reply and records nothing.
func TestTellLeadReachesTheLead(t *testing.T) {
	t.Parallel()
	r := driving(t)
	if n := len(r.driverTold()); n != 0 {
		t.Fatalf("precondition: drive lines before tell-lead: %d", n)
	}
	want := "TOLD pj-lead (T-1; its answer arrives here as an [obor answer] line)"
	reply, err := r.tellLead("t1", "pj-l-01-drive", "driver", "L-01", "may I change the  API contract?")
	if err != nil || reply != want {
		t.Fatalf("tell-lead: %q, %v; want %q", reply, err, want)
	}
	line := `[obor T-1 drive] may I change the API contract? (reply: obor resume T-1 "…")`
	r.waitFor("the line posted to the lead", func() bool { return r.inbox.count("pj-lead", line) == 1 })
	if again, err := r.tellLead("t1", "pj-l-01-drive", "driver", "L-01", "may I change the  API contract?"); err != nil ||
		again != want {
		t.Errorf("a repeated call id: %q, %v; want the stored reply", again, err)
	}
	r.passes()
	if told := r.driverTold(); len(told) != 1 || told[0].To != "pj-lead" || told[0].Task != "T-1" {
		t.Errorf("drive lines %+v, want one to pj-lead for T-1", told)
	}
	if n := r.inbox.count("pj-lead", line); n != 1 {
		t.Errorf("the line posted %d times, want once", n)
	}
}

// Anyone but a drive agent is refused, naming who it is for: a worker, a reviewer, the lead itself, the owner; a drive
// agent of a lane with no open drive is refused too. Nothing is recorded.
func TestTellLeadRefusesOthers(t *testing.T) {
	t.Parallel()
	r := driving(t)
	n := len(r.records())
	for i, c := range []struct{ agent, role, lane string }{
		{"pj-l-01", "worker", "L-01"}, {"pj-rev", "reviewer", ""}, {"pj-lead", "lead", ""}, {"", "", ""},
	} {
		_, err := r.tellLead("r"+string(rune('0'+i)), c.agent, c.role, c.lane, "hello")
		if err == nil || !strings.Contains(err.Error(), "for a drive agent to tell its lead") {
			t.Errorf("tell-lead by %q (role %q): %v; want refused naming the drive agent", c.agent, c.role, err)
		}
	}
	if _, err := r.tellLead("r9", "pj-l-02-drive", "driver", "L-02", "hello"); err == nil ||
		!strings.Contains(err.Error(), "no open drive in lane L-02") {
		t.Errorf("tell-lead from a lane with no drive: %v; want refused", err)
	}
	r.passes()
	if len(r.records()) != n || len(r.driverTold()) != 0 {
		t.Errorf("a refused tell-lead recorded %q", r.steps()[n:])
	}
}

// The lead's line is posted only while the drive can be answered (T-764): driving, it goes out; landed (here under
// review) tell-lead is refused, its reason the state answer would be refused in, and nothing is recorded; sent back to
// driving by the integrator, it goes out again.
func TestTellLeadOnlyWhileTheDriveCanBeAnswered(t *testing.T) {
	t.Parallel()
	r := driving(t)
	if reply, err := r.tellLead("t1", "pj-l-01-drive", "driver", "L-01", "while driving"); err != nil ||
		!strings.HasPrefix(reply, "TOLD pj-lead (T-1;") {
		t.Fatalf("tell-lead while driving: %q, %v; want posted", reply, err)
	}
	r.work("page.html", "x\n")
	if _, err := r.landDrive("l1", "T-1", "page"); err != nil {
		t.Fatalf("land: %v", err)
	}
	r.waitJob("pj-int", "T-1/1/review")
	if st := r.stateOf("T-1"); st != "awaiting-accept" {
		t.Fatalf("precondition: T-1 is %s after the land, want awaiting-accept", st)
	}
	r.passes()
	n := len(r.records())
	want := "tell-lead: T-1 is awaiting-accept (landed: gate, review and merge follow; the lead cannot answer a drive " +
		"until it is driving again)"
	if _, err := r.tellLead("t2", "pj-l-01-drive", "driver", "L-01", "while landed"); err == nil || err.Error() != want {
		t.Errorf("tell-lead while landed: %v; want %q", err, want)
	}
	if got := len(r.records()); got != n {
		t.Errorf("the refused tell-lead recorded %q", r.steps()[n:])
	}
	if _, err := r.verdict(wire.MethodSendback, "s1", "", "try again"); err != nil {
		t.Fatalf("sendback: %v", err)
	}
	r.waitFor("back to driving", func() bool { return r.stateOf("T-1") == wire.TaskDriving })
	if reply, err := r.tellLead("t3", "pj-l-01-drive", "driver", "L-01", "back driving"); err != nil ||
		!strings.HasPrefix(reply, "TOLD pj-lead (T-1;") {
		t.Errorf("tell-lead after the sendback: %q, %v; want posted again", reply, err)
	}
}

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

// The lead's obor answer on a driving task needs no question: held while the drive agent is busy, delivered once when
// it is idle; a second answer is its own line (the first is not ended by it). A serve restart between an answer and its
// delivery delivers it once.
func TestAnswerToDriveDelivered(t *testing.T) {
	t.Parallel()
	r := driving(t)
	r.driveStatus("working")
	want := "ANSWERED T-1: obor serve sends pj-l-01-drive the answer"
	if reply, err := r.leadAnswer("a1", "yes, change it"); err != nil || reply != want {
		t.Fatalf("answer on the drive: %q, %v; want %q", reply, err, want)
	}
	if reply, err := r.leadAnswer("a2", "and bump the version"); err != nil || reply != want {
		t.Fatalf("a second answer: %q, %v; want %q", reply, err, want)
	}
	first, second := "[obor answer] yes, change it", "[obor answer] and bump the version"
	r.passes()
	if n, m := r.toDrive(first), r.toDrive(second); n != 0 || m != 0 {
		t.Fatalf("delivered while the drive agent is busy: %d, %d", n, m)
	}
	if q := r.app.Status().Project.Messages; len(q) != 2 || !strings.Contains(q[0].Held, "busy") {
		t.Errorf("queued lines %+v, want both held, its agent busy", q)
	}
	r.stop()
	r.start() // the restart between the answers and their delivery
	r.waitFor("T-1 driving after the replay", func() bool { return r.stateOf("T-1") == wire.TaskDriving })
	r.connectedAgain()
	r.driveStatus("idle")
	r.waitFor("both answers delivered", func() bool { return r.toDrive(first) == 1 && r.toDrive(second) == 1 })
	r.passes()
	r.stop()
	r.start()
	r.waitFor("T-1 driving after the second replay", func() bool { return r.stateOf("T-1") == wire.TaskDriving })
	r.passes()
	if n, m := r.toDrive(first), r.toDrive(second); n != 1 || m != 1 {
		t.Errorf("answers delivered %d, %d times, want once each", n, m)
	}
	if q := r.app.Status().Project.Messages; len(q) != 0 {
		t.Errorf("lines still queued %+v", q)
	}
}

// obor resume of a task that is not driving keeps today's behaviour: a working task with nothing asked is refused.
func TestAnswerNotDrivingUnchanged(t *testing.T) {
	t.Parallel()
	r := newLoopRig(t, working())
	n := len(r.records())
	if _, err := r.leadAnswer("a1", "yes"); err == nil || !strings.Contains(err.Error(), "T-1 is working with nothing to resume") {
		t.Errorf("answer on a working task with no question: %v; want refused", err)
	}
	if len(r.records()) != n {
		t.Errorf("a refused answer recorded %q", r.steps()[n:])
	}
}

// An answer still undelivered when the drive lands or is dropped closes unsent: never delivered, no line queued.
func TestAnswerToDriveClosedOnLandOrDrop(t *testing.T) {
	t.Parallel()
	for _, end := range []string{"land", "drop"} {
		t.Run(end, func(t *testing.T) {
			t.Parallel()
			r := driving(t)
			r.driveStatus("working")
			if _, err := r.leadAnswer("a1", "later"); err != nil {
				t.Fatal(err)
			}
			r.passes()
			if q := r.app.Status().Project.Messages; len(q) != 1 {
				t.Fatalf("precondition: queued lines %+v, want the answer", q)
			}
			switch end {
			case "land":
				r.work("page.html", "x\n")
				if _, err := r.landDrive("l1", "T-1", "page"); err != nil {
					t.Fatal(err)
				}
				r.waitStep("1 fact submitted")
			case "drop":
				if _, err := wire.Verdict(context.Background(), r.sock,
					&wire.DropReq{Envelope: wire.Envelope{Root: r.p.Root, Agent: "pj-lead", Role: "lead", CallID: "x1"},
						Task: "T-1", Why: "not needed"}); err != nil {
					t.Fatal(err)
				}
				r.waitFor("dropped", func() bool { return r.stateOf("T-1") == "dropped" })
			}
			r.waitFor("the answer closed", func() bool { return len(r.app.Status().Project.Messages) == 0 })
			if end == "land" { // a drop stops the drive agent with its lane (T-775): no pane to go idle
				r.driveStatus("idle")
			}
			r.passes()
			if n := r.toDrive("[obor answer] later"); n != 0 {
				t.Errorf("the answer delivered %d times after the %s, want never", n, end)
			}
		})
	}
}

// T-755 (RV-92 #2 #4): a drive's start is retryable (obor drive on a drive already starts its agent again, nothing
// recorded).

// A new drive whose agent never started (the CLI's start failed): obor drive <T-xx> and obor drive <name> answer the
// drive's DRIVE reply on its recorded lane and type, so the CLI starts the agent again; nothing is recorded, nothing
// launched. Once its agent runs: DRIVING, naming it (the CLI starts nothing). The same after a replay.
func TestDriveStartRetried(t *testing.T) {
	t.Parallel()
	r := newLoopRig(t, dispatching(), started())
	// Every drive below decides by serve's identity: wait for serve's herdr connection as an event, not a sample
	// (T-759).
	r.connectedAgain()
	want := "DRIVE T-1 → L-01 (pj-l-01-drive, " + r.hardType() + ")"
	if reply, err := r.drive("v1", "web"); err != nil || reply != want {
		t.Fatalf("drive: %q, %v; want %q", reply, err, want)
	}
	r.waitFor("T-1 driving", func() bool { return r.stateOf("T-1") == wire.TaskDriving })
	r.passes()
	n := len(r.records())
	for i, name := range []string{"T-1", "web"} {
		reply, err := r.drive("r"+name, name)
		if err != nil || reply != want+" again: its agent is not running" {
			t.Errorf("retry %d (obor drive %s), its agent not running: %q, %v; want %q again", i+1, name, reply, err, want)
		}
	}
	r.passes()
	if len(r.records()) != n || r.count("1 fact dispatch") != 1 {
		t.Errorf("a retry recorded: %q", r.steps()[n:])
	}
	r.driveAgentUp()
	for _, name := range []string{"T-1", "web"} {
		reply, err := r.drive("u"+name, name)
		if err != nil || reply != "DRIVING T-1 → L-01: pj-l-01-drive runs already (pane w1:p5)" {
			t.Errorf("obor drive %s while its agent runs: %q, %v; want DRIVING naming it", name, reply, err)
		}
	}
	if len(r.records()) != n || r.launchesOf("pj-l-01-drive") != 0 || r.launchesOf("pj-l-01") != 0 {
		t.Errorf("recorded %q or launched (drive %d, worker %d)", r.steps()[n:], r.launchesOf("pj-l-01-drive"),
			r.launchesOf("pj-l-01"))
	}
	r.stop()
	r.start() // the replay
	r.waitFor("T-1 driving after the replay", func() bool { return r.stateOf("T-1") == wire.TaskDriving })
	r.connectedAgain() // the replay's serve, before a drive that asks its identity
	r.driveAgentUp()
	if reply, err := r.drive("w1", "web"); err != nil || !strings.HasPrefix(reply, "DRIVING T-1 → L-01: pj-l-01-drive") {
		t.Errorf("obor drive web after the replay: %q, %v; want DRIVING", reply, err)
	}
}
