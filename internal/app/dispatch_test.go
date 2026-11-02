package app_test

import (
	"context"
	"errors"
	"obor/internal/app/port"
	"obor/internal/app/wire"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"obor/internal/app"
	"obor/internal/herdr"
	"obor/internal/herdr/herdrtest"
	"obor/internal/testkit"
)

// Dispatch as state (docs/state-model.md rows 2–9): the dispatch request over the socket, its fact, and the steps the
// merge loop runs after it (lane.prepare, agent.start, the work job), on the merge loop's rig with fake
// LaneSetup and Specs adapters. Lane O-01 starts free (its worker pj-o1 idle in its pane, session s0); T-1 is the
// task dispatched.

// dispatchFakes is the dispatch's adapters and what they did.
type dispatchFakes struct {
	r  *loopRig
	mu sync.Mutex
	on bool // the loop has the dispatch adapters (Loop.Lane, Loop.Specs)
	// The Specs: refusals by task, the target sha, the task branch ahead.
	refuse map[string]string
	texts  []string // the spec text each Check was given (the store's, T-440)
	ahead  bool
	// What the LaneSetup and the worker's starts applied (a call cut before it applies counts in calls only).
	prepares, starts int
	calls            map[string]int
	inputs           []port.PrepareInput
	frees            []port.FreeInput
	session          int           // the worker's session number (its transcript /s/<n>.jsonl)
	keepSession      bool          // a start leaves the session as it is
	sessionless      bool          // a start reports no session id for its agent (OpenCode; T-418)
	gate             chan struct{} // non-nil: a start, its agent up, waits for it to close before it returns
	// fail: an error a method gives instead of acting (nil: none); cut: where a method blocks until its ctx ends
	// ("before" or "after" it applies), hit: it blocked there.
	fail map[string]func() error
	cut  map[string]string
	hit  map[string]bool
}

// newDispatchRig is the merge loop's rig with the dispatch adapters and O-01 free; edits change the config further.
func newDispatchRig(t *testing.T, edits ...func(map[string]any)) *loopRig {
	t.Helper()
	// The live lane is typed sonnet: the config has that lane type (T-738 refuses a --types it lacks).
	edits = append([]func(map[string]any){testkit.Set("lane_types.sonnet", map[string]any{"kind": "claude"})}, edits...)
	r := newLoopRig(t, "echo 1 pass", edits...)
	r.disp = &dispatchFakes{r: r, on: true, refuse: map[string]string{}, calls: map[string]int{},
		fail: map[string]func() error{}, cut: map[string]string{}, hit: map[string]bool{}}
	r.setLane(func(l *port.Lane) { l.Task, l.Agent, l.Type, l.State = "", "", "sonnet", "free" })
	r.f.Set("w1:p1", func(p *herdrtest.Pane) { p.Transcript = "/s/0.jsonl" })
	return r
}

func (d *dispatchFakes) loop(fx *app.Loop, panes herdr.Panes) {
	d.mu.Lock()
	defer d.mu.Unlock()
	kit, _ := filepath.Abs("../..")
	if d.on {
		fx.Lane, fx.Specs = d, d
	}
	fx.Kit = kit
	fx.Typer = cutPrompter{d, panes}
}

// enter counts a call of method and gives its armed failure (nil: act).
func (d *dispatchFakes) enter(method string) error {
	d.mu.Lock()
	d.calls[method]++
	fail := d.fail[method]
	d.mu.Unlock()
	if fail != nil {
		return fail()
	}
	return nil
}

// hold blocks until ctx ends when method is cut at where.
func (d *dispatchFakes) hold(ctx context.Context, method, where string) error {
	d.mu.Lock()
	cut := d.cut[method] == where
	if cut {
		d.hit[method] = true
	}
	d.mu.Unlock()
	if !cut {
		return nil
	}
	<-ctx.Done()
	return ctx.Err()
}

func (d *dispatchFakes) Check(_ context.Context, task, text string) (string, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.texts = append(d.texts, text)
	return d.refuse[task], nil
}

func (d *dispatchFakes) Target(context.Context, string) (string, bool, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	return "target1", d.ahead, nil
}

// Prepare puts O-01 on T-1 in the lane table.
func (d *dispatchFakes) Prepare(ctx context.Context, in port.PrepareInput) (port.Prepared, error) {
	if err := d.enter("prepare"); err != nil {
		return port.Prepared{}, err
	}
	if err := d.hold(ctx, "prepare", "before"); err != nil {
		return port.Prepared{}, err
	}
	d.mu.Lock()
	d.prepares++
	d.inputs = append(d.inputs, in)
	d.mu.Unlock()
	d.r.setLane(func(l *port.Lane) { l.Task, l.State = in.Task, "working" })
	if err := d.hold(ctx, "prepare", "after"); err != nil {
		return port.Prepared{}, err
	}
	return port.Prepared{Base: "base-" + in.Target, Head: "head-" + in.Target}, nil
}

// Free is the lane given back by a drop: noted, nothing refused.
func (d *dispatchFakes) Free(ctx context.Context, in port.FreeInput) (string, error) {
	if err := d.enter("free"); err != nil {
		return "", err
	}
	d.mu.Lock()
	d.frees = append(d.frees, in)
	d.mu.Unlock()
	return "", nil
}

// startWorker is pj-o1's fresh start in its pane: a new session (unless keepSession), idle, seen by the app.
func (d *dispatchFakes) startWorker(ctx context.Context, req port.Launch) (port.Started, error) {
	if err := d.enter("start"); err != nil {
		return port.Started{}, err
	}
	if !req.Fresh || req.Pane != "w1:p1" {
		return port.Started{}, errors.New("unexpected start of pj-o1: " + req.Pane)
	}
	if err := d.hold(ctx, "start", "before"); err != nil {
		return port.Started{}, err
	}
	d.mu.Lock()
	d.starts++
	if !d.keepSession {
		d.session++
	}
	transcript := "/s/" + string(rune('0'+d.session)) + ".jsonl"
	d.mu.Unlock()
	d.r.f.Set("w1:p1", func(p *herdrtest.Pane) {
		p.Kind, p.Name, p.Status, p.Transcript = "claude", "pj-o1", "idle", transcript
	})
	d.r.push()
	d.mu.Lock()
	gate := d.gate
	d.mu.Unlock()
	if gate != nil {
		select {
		case <-gate:
		case <-ctx.Done():
			return port.Started{}, ctx.Err()
		}
	}
	if err := d.hold(ctx, "start", "after"); err != nil {
		return port.Started{}, err
	}
	d.mu.Lock()
	sid := transcript
	if d.sessionless {
		sid = ""
	}
	d.mu.Unlock()
	return port.Started{Line: "STARTED pj-o1", Pane: "w1:p1", Kind: "claude", Session: sid}, nil
}

// cutPrompter is the herdr typer with the work job's cut points.
type cutPrompter struct {
	d     *dispatchFakes
	inner port.Typer
}

func (c cutPrompter) Type(ctx context.Context, pane, text string, clear bool) (bool, error) {
	if !strings.HasPrefix(text, "[obor job ") || !strings.Contains(text, "/work") {
		return c.inner.Type(ctx, pane, text, clear)
	}
	if err := c.d.enter("prompt"); err != nil {
		return false, err
	}
	if err := c.d.hold(ctx, "prompt", "before"); err != nil {
		return false, err
	}
	if typed, err := c.inner.Type(ctx, pane, text, clear); err != nil {
		return typed, err
	}
	return true, c.d.hold(ctx, "prompt", "after")
}

// applied is what the fakes applied: prepares, starts.
func (d *dispatchFakes) applied() (int, int) {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.prepares, d.starts
}

func (d *dispatchFakes) called(method string) int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.calls[method]
}

func (d *dispatchFakes) set(fn func(d *dispatchFakes)) {
	d.mu.Lock()
	fn(d)
	d.mu.Unlock()
}

// dispatch is the lead's dispatch of T-1 (lane "" with types: the first free lane of them).
func (r *loopRig) dispatch(callID, lane string, types ...string) (string, error) {
	return r.dispatchCall(wire.Call{Root: r.p.Root, Agent: "pj-lead", Role: "lead", CallID: callID, Task: "T-1",
		Lane: lane, Types: types})
}

func (r *loopRig) dispatchCall(c wire.Call) (string, error) {
	return wire.Verdict(context.Background(), r.sock, wire.MethodDispatch, c)
}

func (r *loopRig) mustDispatch(callID string) {
	r.t.Helper()
	if reply, err := r.dispatch(callID, "O-01"); err != nil || reply != "DISPATCHED T-1 → O-01 (queued)" {
		r.t.Fatalf("dispatch: %q, %v", reply, err)
	}
}

// workerIs sets pj-o1's status in the fake herdr and waits until the app sees it.
func (r *loopRig) workerIs(status string) {
	r.t.Helper()
	r.f.Set("w1:p1", func(p *herdrtest.Pane) { p.Status = status })
	r.waitFor("pj-o1 "+status, func() bool {
		for _, a := range r.app.Status().Project.Agents {
			if a.Name == "pj-o1" && a.Status == status {
				return true
			}
		}
		r.push()
		return false
	})
}

// workPrompts is how many work job prompts pj-o1 got (job T-1/<attempt>/work…).
func (r *loopRig) workPrompts() int {
	n := 0
	for _, p := range r.prompts() {
		if p.to == "pj-o1" && strings.HasPrefix(p.text, "[obor job T-1/") && strings.Contains(p.text, "/work") {
			n++
		}
	}
	return n
}

// needsYouState waits until T-1's needs-you is folded (its record is written just before).
func (r *loopRig) needsYouState() {
	r.t.Helper()
	r.waitFor("T-1 needs-you", func() bool { return r.taskState() == "needs-you" })
}

// taskState is T-1's state in obor serve status ("" when not listed).
func (r *loopRig) taskState() string {
	for _, t := range r.app.Status().Project.Tasks {
		if t.Task == "T-1" {
			return t.State
		}
	}
	return ""
}

// dispatchSteps: the start carries the work job's wake (its intent first, the start's result its delivery, T-387).
var dispatchSteps = []string{"1 fact dispatch", "1 intent lane.prepare", "1 result lane.prepare",
	"1 intent work", "1 intent agent.start", "1 result agent.start", "1 result work"}

// The request records one fact and answers at once; the loop prepares the lane, starts a fresh worker in its pane and
// sends the work job's wake. The worker's job_next is delivery (working);
// its task_done then submits the task, and the merge loop takes it to merged.
func TestDispatchHappyPath(t *testing.T) {
	t.Parallel()
	r := newDispatchRig(t)
	r.work("feature.txt", "feature\n")
	r.start()
	const stored = "# T-1 — Happy\n\nDifficulty: medium\n\n## Files you may touch\n`feature.txt`\n"
	r.setSpec("s1", stored)
	r.passes()
	if n := len(r.records()); n != 0 {
		t.Fatalf("precondition: records before the dispatch: %q", r.steps())
	}
	// The CLI's call shape (T-538, T-545): a task and its types, no lane.
	if reply, err := r.dispatch("d1", "", "sonnet"); err != nil || reply != "DISPATCHED T-1 → O-01 (queued)" {
		t.Fatalf("dispatch: %q, %v", reply, err)
	}
	r.waitStep("1 result work")
	eq(t, r.steps(), dispatchSteps, "records")
	if r.taskState() != "dispatched" {
		t.Errorf("state %q before delivery, want dispatched", r.taskState())
	}
	job := r.waitJob("pj-o1", "T-1/1/work")
	r.waitFor("working", func() bool { return r.taskState() == "working" })
	// T-440: the job carries the stored spec (backticks kept: it is fetched, not typed), not a git read of it.
	// T-603: it carries the kit's worker role text inline (the rig's project has no project doc).
	for _, want := range []string{"implement the spec of T-1 exactly", "obor spec show T-1", "# Role: worker",
		"Did you mean to type here?", "The spec of T-1 (rev 1", "\n\n" + stored} {
		if !strings.Contains(job, want) {
			t.Errorf("work job has no %q:\n%s", want, job)
		}
	}
	for _, no := range []string{"Read AGENTS.md and your role text", "Also read", "fill in", "## This project"} {
		if strings.Contains(job, no) {
			t.Errorf("work job carries %q (the role text is inline):\n%s", no, job)
		}
	}
	if strings.Contains(job, "git show") {
		t.Errorf("work job still reads the spec from git:\n%s", job)
	}
	r.disp.mu.Lock()
	texts := slices.Clone(r.disp.texts)
	r.disp.mu.Unlock()
	if !slices.Equal(texts, []string{stored}) {
		t.Errorf("Specs.Check got %q, want the stored spec", texts)
	}
	if strings.Contains(job, "{{") || strings.Contains(job, "[obor obor-") {
		t.Errorf("work job has a placeholder or a second sender tag:\n%s", job)
	}
	p, starts := r.disp.applied()
	if p != 1 || starts != 1 {
		t.Errorf("prepares %d, starts %d; want 1 each", p, starts)
	}
	r.disp.mu.Lock()
	in := r.disp.inputs[0]
	r.disp.mu.Unlock()
	if in.Task != "T-1" || in.Lane != "O-01" || in.Dir != r.lane || in.Branch != "task/T-1" || in.Target != "target1" ||
		in.Redo || in.At.IsZero() {
		t.Errorf("prepare input %+v", in)
	}
	if in.Agent != "pj-o1" { // the lane's first alias: the lane state file's agent
		t.Errorf("prepare input agent %q, want pj-o1: %+v", in.Agent, in)
	}

	r.submit()
	r.waitStep("1 fact submitted")
	job = r.waitJob("pj-int", "T-1/1/review")
	r.accept("c1", r.patch(job))
	r.waitStep("1 result agent.stop")
	r.passes()
	if r.mainMerges() != 1 {
		t.Errorf("not merged: %d merges: %q", r.mainMerges(), r.steps())
	}
	if n := r.workPrompts(); n != 0 {
		t.Errorf("work prompts = %d, want 0 (the start carried the wake)", n)
	}
	r.noLead()
}

// --types takes the first free lane of the types in config order.
func TestDispatchByTypes(t *testing.T) {
	t.Parallel()
	r := newDispatchRig(t)
	r.start()
	if _, err := r.dispatch("d0", "", "opus"); err == nil || !strings.Contains(err.Error(), "no free lane of the types opus") {
		t.Fatalf("no lane of the type: %v", err)
	}
	if reply, err := r.dispatch("d1", "", "opus", "sonnet"); err != nil || reply != "DISPATCHED T-1 → O-01 (queued)" {
		t.Fatalf("by types: %q, %v", reply, err)
	}
	r.waitStep("1 result work")
}

// T-545 (DD-v0.14 F1): a re-dispatch onto the lane that still holds the task is a redo again, through the CLI's call
// shape (a task and its types, no lane: T-538). A drop with the work kept leaves the lane working on the task; the
// re-dispatch takes that lane, whatever its type, and lane.prepare keeps its branch. With no other lane of the level
// free it is still the redo, not a wait for a lane that frees only when the task leaves it.
func TestDispatchRedoOntoTheTasksLane(t *testing.T) {
	t.Parallel()
	// drop ends T-1 with no lane: the lane, its branch and the work stay (obor drop <T-xx>, T-462).
	drop := func(t *testing.T, r *loopRig) {
		t.Helper()
		var reply string
		var err error
		r.waitFor("T-1 droppable", func() bool {
			reply, err = wire.Verdict(context.Background(), r.sock, wire.MethodDrop, wire.Call{Root: r.p.Root, Agent: "pj-lead",
				Role: "lead", CallID: "x1", Task: "T-1", Note: "continue later"})
			return err == nil
		})
		if reply != "DROPPED T-1" {
			t.Fatalf("drop: %q", reply)
		}
		r.waitFor("T-1 dropped", func() bool { return r.taskState() == "dropped" })
	}
	for _, state := range []string{"working", "blocked"} {
		t.Run("drop with the work kept, lane "+state+": the same lane, its branch kept", func(t *testing.T) {
			t.Parallel()
			r := newDispatchRig(t)
			r.start()
			if reply, err := r.dispatch("d1", "", "sonnet"); err != nil || reply != "DISPATCHED T-1 → O-01 (queued)" {
				t.Fatalf("the first dispatch: %q, %v", reply, err)
			}
			r.waitStep("1 result work")
			r.passes()
			r.disp.mu.Lock()
			first := r.disp.inputs[0]
			r.disp.mu.Unlock()
			drop(t, r)
			// The drop kept the work: O-01 still holds T-1 on its branch (no lane was freed); its worker may be
			// blocked (a live lane state, T-545: the redo takes it too).
			r.setLane(func(l *port.Lane) { l.State = state })
			r.lanes.mu.Lock()
			l := r.lanes.lanes[0]
			r.lanes.mu.Unlock()
			if l.Task != "T-1" || l.State != state {
				t.Fatalf("precondition: after the drop lane O-01 is %q on %q, want %s on T-1", l.State, l.Task, state)
			}
			reply, err := r.dispatch("d2", "", "sonnet")
			if err != nil || reply != "DISPATCHED T-1 → O-01 (queued)" {
				t.Fatalf("the re-dispatch onto the %s lane: %q, %v; want O-01, the lane that holds the branch", state,
					reply, err)
			}
			r.waitStep("2 result work")
			r.disp.mu.Lock()
			in := r.disp.inputs[1]
			r.disp.mu.Unlock()
			if !in.Redo || in.Lane != "O-01" || in.Branch != first.Branch || in.Target != first.Target {
				t.Errorf("the redo's prepare %+v; want lane O-01 on %s at %s with redo", in, first.Branch, first.Target)
			}
			if b := r.p.Git(r.lane, "branch", "--show-current"); b != "task/T-1" {
				t.Errorf("lane O-01 is on %q after the redo, want task/T-1 (its branch kept)", b)
			}
		})
	}
	t.Run("no other lane of the level free: the redo, not a wait", func(t *testing.T) {
		t.Parallel()
		r := newLevelRig(t)
		r.setLane(func(l *port.Lane) { l.Type = "deepseek" })
		r.lanes.mu.Lock()
		r.lanes.lanes[1].State, r.lanes.lanes[1].Task = "working", "T-9" // O-02 (sonnet) is busy
		r.lanes.mu.Unlock()
		r.start()
		r.specLevel("simple") // deepseek
		if reply, err := r.dispatchLevel("d1"); err != nil || !strings.HasPrefix(reply, "DISPATCHED T-1 → O-01") {
			t.Fatalf("the first dispatch: %q, %v", reply, err)
		}
		r.waitStep("1 result work")
		r.passes()
		r.lanes.mu.Lock()
		var free []string
		for _, l := range r.lanes.lanes {
			if l.Type == "deepseek" && l.State == "free" {
				free = append(free, l.Name)
			}
		}
		r.lanes.mu.Unlock()
		if len(free) != 0 {
			t.Fatalf("precondition: free deepseek lanes %v, want none (O-01 holds T-1)", free)
		}
		drop(t, r)
		reply, err := r.dispatchLevel("d2")
		if err != nil || !strings.HasPrefix(reply, "DISPATCHED T-1 → O-01") {
			t.Fatalf("the re-dispatch with no free deepseek lane: %q, %v; want the redo onto O-01, not a wait", reply, err)
		}
		r.waitStep("2 result work")
		if n := r.taskStep("T-1", "fact waiting"); n != 0 {
			t.Errorf("the re-dispatch waited %d times: %q", n, r.steps())
		}
	})
}

// addLanes appends lanes to the rig's lane table after O-01 (config order).
func (r *loopRig) addLanes(ls ...port.Lane) {
	r.lanes.mu.Lock()
	r.lanes.lanes = append(r.lanes.lanes, ls...)
	r.lanes.mu.Unlock()
}

// T-483: a dispatch reply names the lanes still free after it ("; idle: …"), not the lane it takes; dirty-free and
// working lanes are not idle, and nothing is appended when none stay free. A waiting request takes none: every free
// lane is listed.
func TestDispatchNamesIdleLanes(t *testing.T) {
	t.Parallel()
	busy := []port.Lane{{Name: "O-03", Type: "sonnet", State: "dirty-free"}, {Name: "O-04", Type: "opus", State: "working"}}
	t.Run("two free lanes: the other one is idle", func(t *testing.T) {
		t.Parallel()
		r := newDispatchRig(t)
		r.addLanes(append([]port.Lane{{Name: "O-02", Type: "opus", State: "free"}}, busy...)...)
		r.start()
		reply, err := r.dispatch("d1", "", "sonnet", "opus")
		if err != nil || reply != "DISPATCHED T-1 → O-01 (queued); idle: O-02 (opus)" {
			t.Fatalf("dispatch: %q, %v", reply, err)
		}
		r.waitStep("1 result work")
	})
	t.Run("the last free lane taken: no idle", func(t *testing.T) {
		t.Parallel()
		r := newDispatchRig(t)
		r.addLanes(busy...)
		r.start()
		reply, err := r.dispatch("d1", "", "sonnet")
		if err != nil || reply != "DISPATCHED T-1 → O-01 (queued)" {
			t.Fatalf("dispatch: %q, %v", reply, err)
		}
		r.waitStep("1 result work")
	})
	t.Run("waiting: every free lane is idle", func(t *testing.T) {
		t.Parallel()
		r := startWithT2(t)
		r.addLanes(append([]port.Lane{{Name: "O-02", Type: "opus", State: "free"}}, busy...)...)
		reply, err := r.waitAfter("d1", "T-2")
		if err != nil || reply != "WAITING T-1 after T-2 (queued); idle: O-01 (sonnet), O-02 (opus)" {
			t.Fatalf("dispatch --after: %q, %v", reply, err)
		}
	})
}

// Each refusal answers FAIL … (the error) and records nothing; a repeated call id gets the stored reply.
func TestDispatchRefusals(t *testing.T) {
	t.Parallel()
	for _, row := range []struct {
		name  string
		setup func(r *loopRig)
		call  func(c *wire.Call)
		want  string
	}{
		{name: "no call id", call: func(c *wire.Call) { c.CallID = "" }, want: "a call id and a task are required"},
		{name: "a worker calls", call: func(c *wire.Call) { c.Role, c.Agent = "worker", "pj-o2" },
			want: "the lead or the owner dispatches, not pj-o2"},
		{name: "no lane adapter", setup: func(r *loopRig) { r.disp.set(func(d *dispatchFakes) { d.on = false }) },
			want: "dispatch off for proj (no lane adapter)"},
		{name: "bad task id", setup: func(r *loopRig) {
			r.disp.set(func(d *dispatchFakes) { d.refuse["T-1"] = "bad task id T-1 (task_id_pattern)" })
		}, want: "dispatch T-1: bad task id T-1 (task_id_pattern)"},
		{name: "spec not on the target", setup: func(r *loopRig) {
			r.disp.set(func(d *dispatchFakes) { d.refuse["T-1"] = "no spec for T-1 in obor serve's store" })
		}, want: "no spec for T-1 in obor serve's store"},
		{name: "unknown lane", call: func(c *wire.Call) { c.Lane = "O-09" }, want: "unknown lane O-09"},
		{name: "no level and no types", call: func(c *wire.Call) { c.Lane = "" }, want: "no valid \"Difficulty: <level>\" line"},
		{name: "no free lane of the types", call: func(c *wire.Call) { c.Lane, c.Types = "", []string{"opus"} },
			want: "no free lane of the types opus"},
		{name: "dirty-free without force", setup: func(r *loopRig) { r.setLane(func(l *port.Lane) { l.State = "dirty-free" }) },
			want: "lane O-01 is dirty-free (uncommitted changes; --force to dispatch anyway)"},
		{name: "working on another task", setup: func(r *loopRig) {
			r.setLane(func(l *port.Lane) { l.State, l.Task = "working", "T-9" })
		}, want: "lane O-01 is working on T-9"},
		{name: "blocked on another task", setup: func(r *loopRig) {
			r.setLane(func(l *port.Lane) { l.State, l.Task = "blocked", "T-9" })
		}, want: "lane O-01 is blocked"},
		{name: "done", setup: func(r *loopRig) { r.setLane(func(l *port.Lane) { l.State, l.Task = "done", "T-9" }) },
			want: "lane O-01 is done"},
		// The CLI's shape: a task and its types, no lane (T-538); the branch's commits make only a redo legal (T-545).
		{name: "task branch ahead", setup: func(r *loopRig) { r.disp.set(func(d *dispatchFakes) { d.ahead = true }) },
			call: func(c *wire.Call) { c.Lane, c.Types = "", []string{"sonnet"} },
			want: "task/T-1 exists with commits not in the target"},
	} {
		t.Run(row.name, func(t *testing.T) {
			t.Parallel()
			r := newDispatchRig(t)
			if row.setup != nil {
				row.setup(r)
			}
			r.start()
			c := wire.Call{Root: r.p.Root, Agent: "pj-lead", Role: "lead", CallID: "d1", Task: "T-1", Lane: "O-01"}
			if row.call != nil {
				row.call(&c)
			}
			reply, err := r.dispatchCall(c)
			if err == nil || !strings.Contains(err.Error(), row.want) || reply != "" {
				t.Fatalf("reply %q, error %v; want an error with %q", reply, err, row.want)
			}
			r.passes()
			if n := len(r.records()); n != 0 {
				t.Errorf("a refusal recorded %q", r.steps())
			}
			if n := r.disp.called("prepare"); n != 0 {
				t.Errorf("prepared %d times after a refusal", n)
			}
		})
	}
	t.Run("task open, then the call id again", func(t *testing.T) {
		t.Parallel()
		r := newDispatchRig(t)
		r.start()
		r.mustDispatch("d1")
		r.waitStep("1 result work")
		r.setLane(func(l *port.Lane) { l.State, l.Task = "free", "" })
		if _, err := r.dispatch("d2", "O-01"); err == nil || !strings.Contains(err.Error(), "T-1 is open in obor serve (dispatched)") {
			t.Errorf("a second dispatch of the open task: %v", err)
		}
		if reply, err := r.dispatch("d1", "O-01"); err != nil || reply != "DISPATCHED T-1 → O-01 (queued)" {
			t.Errorf("the first call id again: %q, %v; want its stored reply", reply, err)
		}
		r.passes()
		if n := r.count("1 fact dispatch") + r.count("2 fact dispatch"); n != 1 {
			t.Errorf("dispatch facts %d, want 1: %q", n, r.steps())
		}
	})
	t.Run("lane held by another task's dispatch", func(t *testing.T) {
		t.Parallel()
		r := newDispatchRig(t)
		r.disp.set(func(d *dispatchFakes) { d.fail["prepare"] = func() error { return errors.New("git I/O") } })
		r.start()
		r.mustDispatch("d1")
		r.waitStep("1 fact failed{lane.prepare: git I/O}")
		c := wire.Call{Root: r.p.Root, Role: "lead", CallID: "d2", Task: "T-2", Lane: "O-01"}
		if _, err := r.dispatchCall(c); err == nil || !strings.Contains(err.Error(), "lane O-01 is dispatched to T-1") {
			t.Errorf("a dispatch onto the held lane: %v", err)
		}
	})
}

// A crash at each step (before its effect, or after it with its result lost: the effect is cut off by obor serve's
// stop and the result never written) replays to one effect and the same final state. lane.prepare (G) runs again and
// converges; agent.start (S) reads the snapshot: a new session in the pane is the start, the same one
// starts again; the work job's wake rides on the start, never typed on replay.
func TestDispatchCrashAtEachStep(t *testing.T) {
	t.Parallel()
	for _, row := range []struct {
		method, where string
		// want: what the fakes applied in all (prepares, starts), the calls of the method.
		prepares, starts, calls int
	}{
		{"prepare", "before", 1, 1, 2},
		{"prepare", "after", 2, 1, 2},
		{"start", "before", 1, 1, 2}, // the same session in the pane: started again
		{"start", "after", 1, 1, 1},  // a new session: started, no second Restart
	} {
		t.Run(row.method+" "+row.where, func(t *testing.T) {
			t.Parallel()
			r := newDispatchRig(t)
			r.disp.set(func(d *dispatchFakes) { d.cut[row.method] = row.where })
			r.start()
			r.mustDispatch("d1")
			r.waitFor(row.method+" cut "+row.where, func() bool {
				r.disp.mu.Lock()
				defer r.disp.mu.Unlock()
				return r.disp.hit[row.method]
			})
			r.stop()
			r.disp.set(func(d *dispatchFakes) { delete(d.cut, row.method) })
			r.start()
			r.waitStep("1 result work")
			r.passes()
			eq(t, r.steps(), dispatchSteps, "records")
			p, starts := r.disp.applied()
			if p != row.prepares || starts != row.starts || r.disp.called(row.method) != row.calls {
				t.Errorf("prepares %d, starts %d, %s calls %d; want %d, %d, %d", p, starts,
					row.method, r.disp.called(row.method), row.prepares, row.starts, row.calls)
			}
			same(t, r.result("work"), `"sent (start prompt)"`, "work result")
			if n := r.workPrompts(); n != 0 {
				t.Errorf("work prompts %d, want 0 (the start carried the wake)", n)
			}
		})
	}
}

// A final refusal from Prepare or the launch is a needs-you (lane: …, start: …); three counted failures of a step are
// one too. obor resume retries (its default): a refused step runs again under its next key, a failed one under its
// own.
func TestDispatchNeedsYouThenResume(t *testing.T) {
	t.Parallel()
	t.Run("prepare refused", func(t *testing.T) {
		t.Parallel()
		r := newDispatchRig(t)
		r.disp.set(func(d *dispatchFakes) {
			d.fail["prepare"] = func() error { return port.Refused{Line: "lane_setup in O-01: rc 1: boom"} }
		})
		r.start()
		r.mustDispatch("d1")
		r.waitStep("1 fact needs-you{lane: lane_setup in O-01: rc 1: boom}")
		r.passes()
		if n := r.disp.called("prepare"); n != 1 || r.has("1 intent agent.start") {
			t.Errorf("prepare calls %d (want 1), or a start after the refusal: %q", n, r.steps())
		}
		r.needsYouState()
		if _, err := r.resume("r0", wire.ResumeRegate); err == nil || !strings.Contains(err.Error(), "retry it") {
			t.Errorf("regate of a dispatch's needs-you: %v", err)
		}
		r.disp.set(func(d *dispatchFakes) { delete(d.fail, "prepare") })
		r.needsYouState()
		r.mustResume("r1", "", "RESUMED T-1: retry (lane)")
		r.waitStep("1 result lane.prepare.2")
		r.waitStep("1 result work")
	})
	t.Run("start refused", func(t *testing.T) {
		t.Parallel()
		r := newDispatchRig(t)
		r.disp.set(func(d *dispatchFakes) {
			d.fail["start"] = func() error { return errors.New("pj-o1 has background tasks (--force stops them)") }
		})
		r.start()
		r.mustDispatch("d1")
		r.waitPrefix("1 fact needs-you{start: pj-o1 has background tasks")
		r.disp.set(func(d *dispatchFakes) { delete(d.fail, "start") })
		r.needsYouState()
		r.mustResume("r1", "", "RESUMED T-1: retry (start)")
		r.waitStep("1 result agent.start.2")
		r.waitStep("1 result work")
	})
	t.Run("three counted failures", func(t *testing.T) {
		t.Parallel()
		r := newDispatchRig(t)
		r.disp.set(func(d *dispatchFakes) { d.fail["prepare"] = func() error { return errors.New("git I/O") } })
		r.start()
		r.mustDispatch("d1")
		r.waitStep("1 fact needs-you{lane.prepare failed: git I/O}")
		r.passes()
		if n := r.disp.called("prepare"); n != 3 || r.count("1 intent lane.prepare") != 1 {
			t.Errorf("prepare calls %d (want 3) under one intent: %q", n, r.steps())
		}
		r.disp.set(func(d *dispatchFakes) { delete(d.fail, "prepare") })
		r.needsYouState()
		r.mustResume("r1", "", "RESUMED T-1: retry (lane.prepare failed)")
		r.waitStep("1 result lane.prepare")
		r.waitStep("1 result work")
		if r.count("1 intent lane.prepare") != 1 {
			t.Errorf("the retry wrote a second intent: %q", r.steps())
		}
	})
	t.Run("no pane in the lane", func(t *testing.T) {
		t.Parallel()
		r := newDispatchRig(t)
		r.f.Locked(func() {
			r.f.Panes = slices.DeleteFunc(r.f.Panes, func(p *herdrtest.Pane) bool { return p.ID == "w1:p1" })
		})
		r.start()
		r.mustDispatch("d1")
		r.waitStep("1 result lane.prepare")
		r.passes()
		r.later(time.Minute)
		r.waitPrefix("1 fact needs-you{no pane in lane O-01")
		if r.has("1 intent agent.start") {
			t.Errorf("a start with no pane: %q", r.steps())
		}
	})
	// T-745: obor setup pane moved a pane into the lane after serve started (its shell cd'd there, and a cd gives no
	// herdr event), so the snapshot keeps its old cwd. Herdr's panes are read again before the needs-you: the start goes
	// ahead. When herdr has no pane in the lane either, the needs-you goes out as before.
	t.Run("a pane moved into the lane", func(t *testing.T) {
		t.Parallel()
		r := newDispatchRig(t)
		r.f.Set("w1:p1", func(p *herdrtest.Pane) { p.Cwd = r.p.Root }) // the snapshot serve took: the pane is outside
		r.start()
		r.mustDispatch("d1")
		r.waitStep("1 result lane.prepare")
		r.f.Set("w1:p1", func(p *herdrtest.Pane) { p.Cwd = r.lane }) // moved with no event: the snapshot's cwd is stale
		r.passes()
		r.later(time.Minute)
		r.waitStep("1 result agent.start")
		if r.hasPrefix("1 fact needs-you{no pane in lane") {
			t.Errorf("needs-you with a pane in the lane: %q", r.steps())
		}
		r.waitStep("1 result work")
	})
	t.Run("herdr has no pane in the lane either", func(t *testing.T) {
		t.Parallel()
		r := newDispatchRig(t)
		r.f.Set("w1:p1", func(p *herdrtest.Pane) { p.Cwd = r.p.Root })
		r.start()
		r.mustDispatch("d1")
		r.waitStep("1 result lane.prepare")
		r.passes()
		r.later(time.Minute)
		r.waitPrefix("1 fact needs-you{no pane in lane O-01")
		if r.has("1 intent agent.start") {
			t.Errorf("a start with no pane: %q", r.steps())
		}
	})
}

// Rows 6–8: no delivery 5 min after the work job → retry.work; 5 more → needs-you{job not delivered}; obor resume
// sends the job anew (work.2), and its delivery makes the task working.
func TestDispatchDelivery(t *testing.T) {
	t.Parallel()
	r := newDispatchRig(t)
	r.start()
	r.mustDispatch("d1")
	r.waitStep("1 result work")
	r.later(4 * time.Minute)
	if r.has("1 intent retry.work") {
		t.Fatalf("retried before 5 min: %q", r.steps())
	}
	r.later(time.Minute)
	r.waitStep("1 result retry.work")
	if n := r.workPrompts(); n != 1 { // the first rode on the start
		t.Errorf("work prompts after the retry %d, want 1", n)
	}
	if got := r.cleared("pj-o1"); len(got) != 1 || !got[0] {
		t.Errorf("input line cleared before prompts %v, want [true] (the retry clears)", got)
	}
	r.later(5 * time.Minute)
	r.waitStep("1 fact needs-you{job not delivered: T-1/1/work prompted twice to pj-o1}")
	r.waitFor("one notification", func() bool { return countReq(r.srv, "notification.show") == 1 })
	r.needsYouState()
	r.mustResume("r1", "", "RESUMED T-1: retry (job not delivered)")
	r.waitJob("pj-o1", "T-1/1/work.2")
	r.waitStep("1 result work.2")
	r.workerIs("working")
	r.waitStep("1 fact delivered")
	r.waitFor("working", func() bool { return r.taskState() == "working" })
	r.passes()
	if n := r.workPrompts(); n != 2 || r.has("1 intent retry.work.2") {
		t.Errorf("work prompts %d (want 2), or a retry of the delivered job: %q", n, r.steps())
	}
}

// RV-51 #2: a waiting request that replaces a refused one (obor drop, then dispatch it again --after another task)
// starts with no refusal: no inherited reason in status, and its own hour before the needs-you; also across a reload.
func TestDispatchReplacedWaitStartsAfresh(t *testing.T) {
	t.Parallel()
	r := startWithT2(t)
	r.setLane(func(l *port.Lane) { l.State, l.Task = "working", "T-9" })
	if _, err := r.waitAfter("d1", "T-2"); err != nil {
		t.Fatal(err)
	}
	r.landT2()
	const why = "dispatch T-1: lane O-01 is working on T-9"
	r.waitFor("the first wait's reason", func() bool { return r.taskStatus("T-1").Why == why })
	r.later(59 * time.Minute)
	if st := r.taskStatus("T-1"); st.State != "waiting" {
		t.Fatalf("precondition: the first wait %+v, want waiting 59 min into its refusal", st)
	}
	if reply, err := wire.Verdict(context.Background(), r.sock, wire.MethodDrop, wire.Call{Root: r.p.Root, Role: "lead",
		CallID: "x1", Task: "T-1", Note: "requeue"}); err != nil || reply != "DROPPED T-1" {
		t.Fatalf("drop: %q, %v", reply, err)
	}
	if _, err := wire.Verdict(context.Background(), r.sock, wire.MethodSpecSet, wire.Call{Root: r.p.Root, Role: "lead",
		CallID: "s3", Task: "T-3", Spec: "# T-3\n\nDifficulty: medium\n"}); err != nil {
		t.Fatal(err)
	}
	if reply, err := r.waitAfter("d2", "T-3"); err != nil || reply != "WAITING T-1 after T-3 (queued)" {
		t.Fatalf("the replacement: %q, %v", reply, err)
	}
	if st := r.taskStatus("T-1"); st.State != "waiting" || st.Why != "" {
		t.Errorf("the replacement %+v, want waiting with no reason (the old refusal's)", st)
	}
	r.later(time.Minute)
	r.p.Git(r.p.Root, "commit", "-q", "--allow-empty", "-m", "Merge T-3: by hand")
	r.waitFor("the replacement's own reason", func() bool { return r.taskStatus("T-1").Why == why })
	r.later(2 * time.Minute)
	if st := r.taskStatus("T-1"); st.State != "waiting" || strings.Contains(strings.Join(r.steps(), "\n"), "needs-you{after: T-1") {
		t.Errorf("the replacement %+v, steps %q: want waiting, no needs-you (its hour is its own)", st, r.steps())
	}
	r.stop()
	r.start()
	r.waitFor("the replacement's reason after the reload", func() bool {
		st := r.taskStatus("T-1")
		return st.State == "waiting" && st.Why == why
	})
	r.later(59 * time.Minute)
	if st := r.taskStatus("T-1"); st.State != "waiting" || strings.Contains(strings.Join(r.steps(), "\n"), "needs-you{after: T-1") {
		t.Errorf("after the reload %+v, steps %q: want waiting, no needs-you", st, r.steps())
	}
}

// T-494, RV-52 #2 (T-587): agent.start names the lane's worker by its lane row's agent when that is still one of the
// lane's names, else by the lane pane's label when it is one of the lane's names (pj-o1, the compact alias, stays pj-o1), else by the lane's default name (pj-o-01: no
// agent in the config here). herdr's live agent name never counts: a label and a live name that are two of the
// lane's aliases give the label, a live name with no label gives the default.
func TestDispatchKeepsTheLaneAgentName(t *testing.T) {
	t.Parallel()
	for _, row := range []struct {
		name         string
		rowAgent     string // the lane row's agent
		label, agent string // w1:p1's label and the agent running in it ("": a shell)
		want         string
	}{
		{"lane row, a lane name", "pj-o1", "", "", "pj-o1"},
		{"lane row, not a lane name (before a rename): the default", "hk-o1", "", "", "pj-o-01"},
		{"lane row not a lane name, a labelled pane: the label", "hk-o1", "pj-o1", "", "pj-o1"},
		{"labelled pane", "", "pj-o1", "", "pj-o1"},
		{"live name and label conflict: the label", "", "pj-o1", "pj-o-01", "pj-o1"},
		{"live name and label conflict, reversed: the label", "", "pj-o-01", "pj-o1", "pj-o-01"},
		{"a live name, no label: the default", "", "", "pj-o1", "pj-o-01"},
		{"label not a lane name", "", "notes", "", "pj-o-01"},
		{"neither", "", "", "", "pj-o-01"},
	} {
		t.Run(row.name, func(t *testing.T) {
			t.Parallel()
			r := newDispatchRig(t, testkit.Set("lanes.O-01", map[string]any{"type": "opus"}))
			r.setLane(func(l *port.Lane) { l.Agent = row.rowAgent })
			r.f.Set("w1:p1", func(p *herdrtest.Pane) {
				p.Label, p.Name = row.label, row.agent
				if row.agent == "" {
					p.Kind, p.Status = "", ""
				}
			})
			r.start()
			r.mustDispatch("d1")
			r.waitStep("1 intent agent.start")
			r.disp.mu.Lock()
			inputs := slices.Clone(r.disp.inputs)
			r.disp.mu.Unlock()
			if len(inputs) != 1 {
				t.Fatalf("prepares %d, want 1", len(inputs))
			}
			if inputs[0].Agent != row.want {
				t.Errorf("prepare agent %q, want %q", inputs[0].Agent, row.want)
			}
			r.waitFor("the start", func() bool { r.mu.Lock(); defer r.mu.Unlock(); return len(r.launches) > 0 })
			r.mu.Lock()
			launch := r.launches[0]
			r.mu.Unlock()
			if launch.Agent != row.want || launch.Pane != "w1:p1" {
				t.Errorf("agent.start launched %q in %q, want %q in w1:p1", launch.Agent, launch.Pane, row.want)
			}
		})
	}
}

// T-587: serve restarted after `obor setup rename` replays a log whose dispatch fact names the old agent, so the lane's
// row says hk-o1 again; the next dispatch still starts the worker by the lane's name under the config (pj-o-01).
func TestDispatchAfterARenameIgnoresTheReplayedRowAgent(t *testing.T) {
	t.Parallel()
	r := newDispatchRig(t, testkit.Set("lanes.O-01", map[string]any{"type": "opus"}))
	r.setLane(func(l *port.Lane) { l.Agent = "hk-o1" })
	r.f.Set("w1:p1", func(p *herdrtest.Pane) { p.Label, p.Name, p.Kind, p.Status = "", "", "", "" }) // a shell
	r.start()
	r.stop()
	r.start() // the replay
	r.mustDispatch("d1")
	r.waitFor("the start", func() bool { r.mu.Lock(); defer r.mu.Unlock(); return len(r.launches) > 0 })
	r.mu.Lock()
	got := r.launches[0].Agent
	r.mu.Unlock()
	if got != "pj-o-01" {
		t.Errorf("agent.start launched %q, want pj-o-01 (the row's hk-o1 is no name of the lane)", got)
	}
}
