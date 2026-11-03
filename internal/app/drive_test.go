package app_test

import (
	"context"
	"encoding/json"
	"obor/internal/app/event"
	"obor/internal/app/port"
	"obor/internal/app/wire"
	"slices"
	"strings"
	"testing"

	"obor/internal/config"
	"obor/internal/herdr/herdrtest"
	"obor/internal/store"
	"obor/internal/testkit"
)

// The owner's drive (T-751) on the dispatch rig: obor drive's request (a new drive on the first free untyped lane, or a
// take-over of a working task), the driving state, obor land's request, and the merge loop after it. Lane O-01 is made
// untyped and free for a new drive; the CLI's start of the drive agent is the rig's pane w1:p5 (driveAgentUp).

// newDriveRig is the dispatch rig with O-01 an untyped (pool) lane, free, and the app started.
func newDriveRig(t *testing.T, edits ...func(map[string]any)) *loopRig {
	t.Helper()
	r := newDispatchRig(t, edits...)
	r.setLane(func(l *port.Lane) { l.Type = "" })
	r.start()
	return r
}

// drive is obor drive's request by the owner (no role): a new drive named name (types: its --type), or a take-over
// when name is a task id.
func (r *loopRig) drive(callID, name string, types ...string) (string, error) {
	c := wire.Call{Root: r.p.Root, CallID: callID, Note: name, Types: types}
	if strings.HasPrefix(name, "T-") {
		c.Task, c.Note = name, ""
	}
	return wire.Verdict(context.Background(), r.sock, wire.MethodDrive, c)
}

// landDrive is obor land's request by the owner for task with summary.
func (r *loopRig) landDrive(callID, task, summary string) (string, error) {
	return wire.Verdict(context.Background(), r.sock, wire.MethodLand, wire.Call{Root: r.p.Root, CallID: callID, Task: task,
		Report: summary})
}

// stateOf is task's state in obor serve status ("" when not listed).
func (r *loopRig) stateOf(task string) string { return r.task(task).State }

// driveAgentUp is the CLI's start of the drive agent pj-o1-drive in the lane (pane w1:p5, labelled), seen by the app.
func (r *loopRig) driveAgentUp() {
	r.t.Helper()
	r.f.Agent("w1:p5", "pj-o1-drive", "idle", r.lane)
	r.f.Set("w1:p5", func(p *herdrtest.Pane) { p.Workspace, p.Label = "w1", "pj-o1-drive" })
	r.waitFor("pj-o1-drive seen", func() bool {
		for _, a := range r.app.Status().Project.Agents {
			if a.Pane == "w1:p5" {
				return true
			}
		}
		r.push()
		return false
	})
}

// driveLines is the lead's lines that a drive came back to its owner.
func (r *loopRig) driveLines() []string {
	var out []string
	for _, rec := range r.records() {
		var m event.Message
		_ = json.Unmarshal(rec.Data, &m)
		if rec.Kind == event.KindFact && rec.Step == event.StepMessage && m.Drive != 0 {
			out = append(out, m.Line)
		}
	}
	return out
}

// launchesOf is how many Agents.Launch calls started agent.
func (r *loopRig) launchesOf(agent string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	n := 0
	for _, l := range r.launches {
		if l.Agent == agent {
			n++
		}
	}
	return n
}

// hardType is the rig config's hard level's first lane type (a new drive's default).
func (r *loopRig) hardType() string {
	r.t.Helper()
	cfg, err := config.Load(r.p.Root)
	if err != nil {
		r.t.Fatal(err)
	}
	types, _ := cfg.DifficultyTypes("hard")
	if len(types) == 0 {
		r.t.Fatal("precondition: the rig config has no hard type")
	}
	return types[0]
}

// A new drive takes the first free untyped lane: a new task id, its spec "Owner drive: <name>" stored, fact dispatch
// {drive}; serve prepares the lane on the task branch and the task is driving: no worker start, no work job, no job at
// all. A repeated call id gets the stored reply; the same name again is refused while the drive is open.
func TestDriveNewOnAFreeUntypedLane(t *testing.T) {
	t.Parallel()
	r := newDriveRig(t)
	if n := len(r.records()); n != 0 {
		t.Fatalf("precondition: records before the drive: %q", r.steps())
	}
	want := "DRIVE T-1 → O-01 (pj-o1-drive, " + r.hardType() + ")"
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
	lr, ok, err := store.ReadLaneAt(r.p.Root, "O-01")
	if err != nil || !ok || lr.Task != "T-1" || lr.Agent != "pj-o1-drive" || lr.Branch != "task/T-1" || lr.Base == "" ||
		lr.Finished != 0 {
		t.Errorf("O-01's lane row %+v (%v, %v): want T-1's drive, prepared, not finished", lr, ok, err)
	}
	if _, starts := r.disp.applied(); starts != 0 || r.launchesOf("pj-o1") != 0 || r.workPrompts() != 0 {
		t.Errorf("a drive started a worker (%d starts, %d launches) or sent a work job (%d)", starts,
			r.launchesOf("pj-o1"), r.workPrompts())
	}
	if ts := r.task("T-1"); ts.Job != "" {
		t.Errorf("a driving task has job %q open", ts.Job)
	}
	if reply, err := r.drive("v1", "web design"); err != nil || reply != want {
		t.Errorf("the same call id again: %q, %v; want the stored reply", reply, err)
	}
	if _, err := r.drive("v2", "web design"); err == nil || !strings.Contains(err.Error(), "T-1 drives it already") {
		t.Errorf("the same name again: %v, want refused", err)
	}
}

// No free untyped lane: refused with the lanes' states, nothing recorded and no wait (a typed lane is a reservation
// for its type, a working one holds its task).
func TestDriveNoFreeLaneRefused(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		name string
		lane func(l *port.Lane)
		why  string
	}{
		{"typed lane", func(l *port.Lane) { l.Type = "sonnet" }, "no free untyped lane (O-01 (sonnet) free)"},
		{"working lane", func(l *port.Lane) { l.State, l.Task = "working", "T-9" }, "no free untyped lane (O-01 working T-9)"},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			r := newDriveRig(t)
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

// --type is honoured, effort included, and a drive is no worker task for lane_types' max (sonnet's max 1; every
// prepare held, so each task stays on the lane it took): a drive of sonnet:high on the untyped O-01 does not count, so
// T-2's dispatch of sonnet still takes O-02; and with sonnet at its max (T-2's) a second drive of sonnet still takes
// the untyped O-03.
func TestDriveTypeHonouredNotCapped(t *testing.T) {
	t.Parallel()
	r := newDispatchRig(t, testkit.Set("lane_types.sonnet", map[string]any{"kind": "claude", "max": 1}))
	r.setLane(func(l *port.Lane) { l.Type = "" })
	r.addLanes(port.Lane{Name: "O-02", Dir: r.p.Lane("O-02", ""), Type: "sonnet", State: "free"},
		port.Lane{Name: "O-03", Dir: r.p.Lane("O-03", ""), State: "free"})
	r.disp.set(func(d *dispatchFakes) { d.cut["prepare"] = "before" })
	r.start()
	if _, err := wire.Verdict(context.Background(), r.sock, wire.MethodSpecSet, wire.Call{Root: r.p.Root, Role: "lead",
		Agent: "lead", CallID: "s2", Task: "T-2", Spec: "# T-2 — Other\n\nDifficulty: medium\n"}); err != nil {
		t.Fatal(err)
	}
	reply, err := r.drive("v1", "tweak", "sonnet:high")
	if err != nil || reply != "DRIVE T-3 → O-01 (pj-o1-drive, sonnet:high)" {
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
	if reply, err := r.dispatchCall(wire.Call{Root: r.p.Root, Agent: "pj-lead", Role: "lead", CallID: "d2", Task: "T-2",
		Types: []string{"sonnet"}}); err != nil || !strings.HasPrefix(reply, "DISPATCHED T-2 → O-02") {
		t.Fatalf("T-2 of sonnet beside a sonnet drive: %q, %v; want dispatched (a drive does not count)", reply, err)
	}
	if reply, err := r.drive("v2", "second", "sonnet"); err != nil || !strings.HasPrefix(reply, "DRIVE T-4 → O-03 (") {
		t.Errorf("a drive with sonnet at its max: %q, %v; want O-03", reply, err)
	}
	if _, err := r.drive("v3", "bad", "nosuch"); err == nil || !strings.Contains(err.Error(), "nosuch") {
		t.Errorf("an unknown --type: %v, want refused by name", err)
	}
}

// obor drive <T-xx> takes over a task its worker works on: fact drive, serve stops the worker (with force, as obor
// restart --stop does), the lane, branch and prepare stay (no second lane.prepare), the lane row names the drive agent,
// and the task is driving.
func TestDriveTakeOverStopsTheWorkerKeepsTheBranch(t *testing.T) {
	t.Parallel()
	r := newDispatchRig(t)
	r.start()
	r.setSpec("s1", "# T-1 — Work\n\nDifficulty: medium\n")
	r.mustDispatch("d1")
	r.waitJob("pj-o1", "T-1/1/work")
	r.waitFor("working", func() bool { return r.stateOf("T-1") == "working" })
	before, _, _ := store.ReadLaneAt(r.p.Root, "O-01")
	reply, err := r.drive("v1", "T-1")
	if err != nil || !strings.HasPrefix(reply, "DRIVE T-1 → O-01 (pj-o1-drive, sonnet) taking over from pj-o1; STOPPED pj-o1") {
		t.Fatalf("take-over: %q, %v", reply, err)
	}
	if !slices.Contains(r.workerStops(), "pj-o1 ") {
		t.Errorf("the worker was not stopped: %q", r.workerStops())
	}
	r.waitFor("T-1 driving", func() bool { return r.stateOf("T-1") == wire.TaskDriving })
	r.passes()
	if n := r.count("1 intent lane.prepare"); n != 1 {
		t.Errorf("lane.prepare ran %d times, want once (the take-over keeps the branch)", n)
	}
	if !r.has("1 fact drive") {
		t.Errorf("no drive fact: %q", r.steps())
	}
	after, _, _ := store.ReadLaneAt(r.p.Root, "O-01")
	if after.Branch != before.Branch || after.Base != before.Base || after.Agent != "pj-o1-drive" || after.Task != "T-1" {
		t.Errorf("lane row %+v after the take-over, before %+v: want the branch and base kept, the drive agent", after, before)
	}
	if _, err := r.drive("v2", "T-1"); err == nil || !strings.Contains(err.Error(), "driving already") {
		t.Errorf("a second take-over: %v, want refused", err)
	}
}

// A task gating, under review or merging is not taken over: refused by its state, nothing recorded.
func TestDriveTakeOverWhileGatingRefused(t *testing.T) {
	t.Parallel()
	r := newDispatchRig(t)
	r.work("feature.txt", "feature\n")
	r.start()
	r.setSpec("s1", "# T-1 — Work\n\nDifficulty: medium\n")
	r.mustDispatch("d1")
	r.waitJob("pj-o1", "T-1/1/work")
	release := make(chan struct{})
	r.scratch.arm("Gate", func(ctx context.Context) error {
		select {
		case <-release:
		case <-ctx.Done():
		}
		return nil
	})
	defer close(release)
	r.submit()
	r.waitStep("1 intent gate")
	n := len(r.records())
	_, err := r.drive("v1", "T-1")
	if err == nil || !strings.Contains(err.Error(), "not one gating, merging or under review") {
		t.Fatalf("take-over while gating: %v, want refused", err)
	}
	if len(r.records()) != n || r.has("1 fact drive") {
		t.Errorf("a refused take-over recorded: %q", r.steps())
	}
	if stops := r.workerStops(); len(stops) != 0 {
		t.Errorf("a refused take-over stopped %q", stops)
	}
}

// obor land on the owner's behalf: submitted with the summary as its report, then gate, review, publish and the
// after-merge stop of the drive agent; the lane row is finished (the lane free once merged). A repeated call id gets
// the stored reply; land of a task not driving is refused.
func TestDriveLandGatesReviewsMergesAndStopsTheAgent(t *testing.T) {
	t.Parallel()
	r := newDriveRig(t)
	if _, err := r.landDrive("l0", "T-1", "x"); err == nil || !strings.Contains(err.Error(), "not open") {
		t.Fatalf("land before any drive: %v", err)
	}
	if _, err := r.drive("v1", "web"); err != nil {
		t.Fatal(err)
	}
	r.waitFor("T-1 driving", func() bool { return r.stateOf("T-1") == wire.TaskDriving })
	r.driveAgentUp()
	r.work("page.html", "<p>new</p>\n")
	r.p.Git(r.lane, "add", "-A")
	r.p.Git(r.lane, "commit", "-qm", "page")
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
	if !slices.Contains(r.workerStops(), "pj-o1-drive w1:p5") {
		t.Errorf("the drive agent was not stopped after the merge: %q", r.workerStops())
	}
	if lr, _, _ := store.ReadLaneAt(r.p.Root, "O-01"); lr.Finished == 0 {
		t.Errorf("O-01's row %+v is not finished after the merge", lr)
	}
	if st := r.stateOf("T-1"); st != "" {
		t.Errorf("T-1 still open after the merge: %s", st)
	}
	if r.launchesOf("pj-o1") != 0 || len(r.driveLines()) != 0 {
		t.Errorf("a worker started (%d) or a come-back line was sent (%q)", r.launchesOf("pj-o1"), r.driveLines())
	}
}

// A red gate goes back to the drive (the next attempt driving, one line to the lead), never to a worker: no fix job,
// no needs-you, whatever the count. The next land gates again. A serve restart replays the driving task as driving
// and sends no second line.
func TestDriveRedGateBackToDriving(t *testing.T) {
	t.Parallel()
	r := newDriveRig(t, testkit.Set("commands.check", "test ! -f red.txt"))
	if _, err := r.drive("v1", "web"); err != nil {
		t.Fatal(err)
	}
	r.waitFor("T-1 driving", func() bool { return r.stateOf("T-1") == wire.TaskDriving })
	for i, id := range []string{"l1", "l2", "l3"} { // past K: still the drive's
		r.work("red.txt", "red "+id+"\n")
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
	r.p.Git(r.lane, "rm", "-q", "red.txt")
	if _, err := r.landDrive("l4", "T-1", "fixed"); err != nil {
		t.Fatal(err)
	}
	r.waitJob("pj-int", "T-1/4/review")
}

// The integrator's hold of a drive's review goes back to the drive, with one line to the lead; no needs-you.
func TestDriveHeldReviewBackToDriving(t *testing.T) {
	t.Parallel()
	r := newDriveRig(t)
	if _, err := r.drive("v1", "web"); err != nil {
		t.Fatal(err)
	}
	r.waitFor("T-1 driving", func() bool { return r.stateOf("T-1") == wire.TaskDriving })
	r.work("page.html", "x\n")
	if _, err := r.landDrive("l1", "T-1", "page"); err != nil {
		t.Fatal(err)
	}
	r.waitJob("pj-int", "T-1/1/review")
	reply, err := wire.Verdict(context.Background(), r.sock, wire.MethodHold, wire.Call{Root: r.p.Root, Agent: "pj-int",
		Role: "integrator", CallID: "h1", Task: "T-1", Note: "which layout?"})
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

// obor drop works on a drive unchanged: by lane, the lane freed and the drive agent stopped; the task dropped.
func TestDriveDrop(t *testing.T) {
	t.Parallel()
	r := newDriveRig(t)
	if _, err := r.drive("v1", "web"); err != nil {
		t.Fatal(err)
	}
	r.waitFor("T-1 driving", func() bool { return r.stateOf("T-1") == wire.TaskDriving })
	r.driveAgentUp()
	reply, err := wire.Verdict(context.Background(), r.sock, wire.MethodDrop, wire.Call{Root: r.p.Root, Agent: "pj-lead",
		Role: "lead", CallID: "x1", Lane: "O-01", Note: "not needed"})
	if err != nil || !strings.Contains(reply, "DROPPED T-1 from O-01") {
		t.Fatalf("drop: %q, %v", reply, err)
	}
	r.waitFor("dropped", func() bool { return r.stateOf("T-1") == "dropped" })
	if !slices.Contains(r.workerStops(), "pj-o1-drive ") {
		t.Errorf("the drive agent was not stopped: %q", r.workerStops())
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
	r := newDriveRig(t)
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
	if lr, _, _ := store.ReadLaneAt(r.p.Root, "O-01"); lr.Task != "T-1" || lr.Agent != "pj-o1-drive" || lr.Finished != 0 {
		t.Errorf("O-01's row after the replay: %+v", lr)
	}
	if reply, err := r.drive("v1", "web"); err != nil || !strings.HasPrefix(reply, "DRIVE T-1") {
		t.Errorf("the stored reply after the replay: %q, %v", reply, err)
	}
}
