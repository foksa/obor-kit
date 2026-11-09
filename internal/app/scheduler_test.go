package app

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"obor/internal/app/event"
	"obor/internal/app/port"
	"obor/internal/app/wire"
	"obor/internal/store"
)

// schedStatus is the scheduler's state obor status reports: the scratch's owner ("-": free) and the queue.
func schedStatus(t *testing.T, h *harness) (string, []wire.ScratchWait) {
	t.Helper()
	ps := h.a.Status().Project
	if ps == nil {
		t.Fatal("precondition: the project loaded")
	}
	owner := "-"
	if s := ps.Scratch; s != nil {
		owner = s.Task + " " + s.Why
	}
	return owner, ps.ScratchQueue
}

// T-669 (RV-75 #1), T-673: two submitted tasks and a free scratch name no task as next: which one the pass gates
// first is the scheduler's admission (here low disk refuses both), and copying that rule into status went stale. The
// queue keeps the facts: no owner, the tasks in the pass's order, nothing they wait for.
func TestScratchQueuePredictsNothing(t *testing.T) {
	t.Parallel()
	h, f := seedSched(t, false)
	h.write(filepath.Join(h.root, ".obor", "config.json"), `{"version":2,"project":{"name":"pj","agent_prefix":"pj"},"agents":{"roles":{"integrator":""}}}`)
	h.a.opt.Loop = func(string) (*Loop, error) {
		return &Loop{Workflow: WorkflowOf(f), Agents: f, Typer: f,
			Disk: &Disk{Volume: "/vol/pj", Free: func() (uint64, error) { return 1 << 30, nil }}}, nil
	}
	p := reloadProj(t, h)
	if !p.merging {
		t.Fatal("precondition: the merge loop runs")
	}
	for _, task := range []string{"T-9", "T-10"} {
		addRecord(t, h, task, event.StepSubmitted, event.KindFact, submitData{Lane: "L-01"})
		addRecord(t, h, task, event.StepSubmit, event.KindResult, submitOut{SHA: "s-" + task})
	}
	owner, q := schedStatus(t, h)
	want := []wire.ScratchWait{{Task: "T-10"}, {Task: "T-9"}} // the pass's order, by id as strings
	if owner != "-" || !slices.Equal(q, want) {
		t.Fatalf("scratch %s, queue %+v; want free, %+v", owner, q, want)
	}
	h.pass(context.Background())
	if len(f.gated) != 0 {
		t.Errorf("low disk, free scratch: gated %v; want none (status named no task next)", f.gated)
	}
}

// T-669 (RV-75 #2), T-673: a task in conflict, resolving or resolved, or a needs-you keeping one of those owns the
// scratch; a submitted task waits on it (and the pass does not gate it), as status says. At resolved the owner's own
// gate needs the scratch and nothing holds it back: it is listed with nothing to wait for, never as next.
func TestScratchOwnedByAnOpenMerge(t *testing.T) {
	t.Parallel()
	h, f := seedSched(t, true) // T-1's gate conflicted
	ctx := context.Background()
	addRecord(t, h, "T-2", event.StepSubmitted, event.KindFact, submitData{Lane: "L-02"})
	addRecord(t, h, "T-2", event.StepSubmit, event.KindResult, submitOut{SHA: "s2"})
	p := h.a.p
	check := func(state, why string, resolved bool) {
		t.Helper()
		if got := p.folded.loop["T-1"].shown(); got != state {
			t.Fatalf("precondition: T-1 %s, want %s", got, state)
		}
		owner, q := schedStatus(t, h)
		want := []wire.ScratchWait{{Task: "T-2", WaitsOn: "T-1", Why: why}}
		if resolved { // T-1's own gate needs the scratch: listed, with nothing it waits for
			want = []wire.ScratchWait{{Task: "T-1"}, {Task: "T-2", WaitsOn: "T-1", Why: why}}
		}
		if owner != "T-1 "+why || !slices.Equal(q, want) {
			t.Errorf("T-1 %s: scratch %s, queue %+v; want T-1 %s, %+v", state, owner, q, why, want)
		}
	}
	check(stConflict, wire.ScratchConflict, false)
	f.Launch(ctx, port.Launch{})
	h.pass(ctx)
	check(stResolving, wire.ScratchResolving, false)
	if len(f.gated) != 0 {
		t.Fatalf("resolving: gated %v; want none", f.gated)
	}
	h.a.needsYou(p, p.folded.loop["T-1"], event.CauseHeld, "held")
	check(wire.PhaseNeedsYou, wire.ScratchHeld, false)
	h.pass(ctx)
	if len(f.gated) != 0 {
		t.Fatalf("held: gated %v; want none", f.gated)
	}
	if _, err := h.a.resume(wire.ResumeReq{Envelope: wire.Envelope{Root: h.root, Role: "lead", CallID: "r"},
		Task: "T-1", Text: "resolve it"}); err != nil {
		t.Fatal(err)
	}
	h.pass(ctx)
	check(stResolving, wire.ScratchResolving, false)
	if _, err := h.a.verdict(ctx, wire.VerdictReq{Envelope: wire.Envelope{Root: h.root, Role: "integrator",
		Agent: "pj-int", CallID: "v"}, Verb: wire.MethodResolved, Task: "T-1",
		Job: JobID(p.folded.loop["T-1"].jobKey())}); err != nil {
		t.Fatal(err)
	}
	check(stResolved, wire.ScratchResolved, true)
	h.pass(ctx)
	if !slices.Equal(f.gated, []string{"T-1", "T-2"}) {
		t.Errorf("after T-1's resolution: gated %v; want [T-1 T-2]", f.gated)
	}
}

// cmdScratch is schedScratch whose gate tells Command its command, pauses between two commands (the slot wait, git
// work), then waits for release.
type cmdScratch struct {
	*schedScratch
	started, blank, blanked, release chan struct{}
}

func (f *cmdScratch) Gate(ctx context.Context, in port.GateInput) (port.GateOutcome, error) {
	in.Command("check_affected")
	close(f.started)
	<-f.blank
	in.Command("") // the command ended: the gate runs on, no command does (the runner's clear, T-673)
	close(f.blanked)
	<-f.release
	return f.schedScratch.Gate(ctx, in)
}

// T-669 (RV-75 #4), T-673: a gate running reports the command its gate told (the class's), and the gating task owns the
// scratch; between two commands the gate runs with no command; none once it ended.
func TestRunningGateCommand(t *testing.T) {
	t.Parallel()
	h, sf := seedSched(t, false)
	h.write(filepath.Join(h.root, ".obor", "config.json"), `{"version":2,"project":{"name":"pj","agent_prefix":"pj"},"agents":{"roles":{"integrator":""}}}`)
	f := &cmdScratch{schedScratch: sf, started: make(chan struct{}), blank: make(chan struct{}),
		blanked: make(chan struct{}), release: make(chan struct{})}
	h.a.opt.Loop = func(string) (*Loop, error) { return &Loop{Workflow: WorkflowOf(f), Agents: f, Typer: f}, nil }
	reloadProj(t, h)
	if err := os.MkdirAll(h.lanes.lanes[0].Dir, 0o755); err != nil {
		t.Fatal(err)
	}
	addRecord(t, h, "T-3", event.StepSubmitted, event.KindFact, submitData{Lane: "L-01"})
	addRecord(t, h, "T-3", event.StepSubmit, event.KindResult, submitOut{SHA: "s3"})
	h.a.advance(context.Background())
	<-f.started
	ps := h.a.Status().Project
	if len(ps.Running) != 1 || ps.Running[0].Step != event.StepGate || ps.Running[0].Command != "check_affected" {
		t.Errorf("running %+v; want T-3's gate running check_affected", ps.Running)
	}
	if s := ps.Scratch; s == nil || *s != (wire.ScratchStatus{Task: "T-3", Why: wire.ScratchGating}) {
		t.Errorf("scratch %+v; want T-3 gating", s)
	}
	close(f.blank)
	<-f.blanked
	h.a.mu.Lock()
	running := h.a.p.running
	h.a.mu.Unlock()
	ps = h.a.Status().Project
	close(f.release)
	h.a.effects.Wait()
	if running.Task != "T-3" {
		t.Fatalf("precondition: T-3's gate runs between its commands, running %+v", running)
	}
	if len(ps.Running) != 1 || ps.Running[0].Step != event.StepGate || ps.Running[0].Command != "" {
		t.Errorf("between two commands: running %+v; want T-3's gate with no command", ps.Running)
	}
	if ps := h.a.Status().Project; len(ps.Running) != 0 || ps.Scratch != nil {
		t.Errorf("after the gate: running %+v, scratch %+v; want none", ps.Running, ps.Scratch)
	}
}

// waitSpec is task's waiting spec at level, with its head lines added.
func waitSpec(task, level string, lines ...string) string {
	text := "# " + task + " — Waiting\n\nDifficulty: " + level + "\n"
	for _, l := range lines {
		text += l + "\n"
	}
	return text
}

// T-1030: the pass order's waiting part is the lane's pick order (a freed lane goes to the first waiting task there):
// a high task before every ordinary one — its spec's `Priority: high` — then the waiting fact's order (dispatch order). Its first is the task that starts on the
// next free lane. T-2 waits before T-3, and status marks exactly the high ones.
func TestPassOrderPutsAHighWaitingTaskFirst(t *testing.T) {
	t.Parallel()
	plain2, plain3 := waitSpec("T-2", "medium"), waitSpec("T-3", "medium")
	high2, high3 := waitSpec("T-2", "medium", "Priority: high"), waitSpec("T-3", "medium", "Priority: high")
	fix3 := waitSpec("T-3", "medium", "Fixes: RV-1")
	for _, c := range []struct {
		name  string
		specs map[string]string // task → its spec
		open  string            // a review the fold has open ("" none)
		want  []string
		high  []string // the tasks obor status marks high
	}{
		{name: "ordinary: the dispatch order", specs: map[string]string{"T-2": plain2, "T-3": plain3},
			want: []string{"T-2", "T-3"}},
		{name: "high first though dispatched second", specs: map[string]string{"T-2": plain2, "T-3": high3},
			want: []string{"T-3", "T-2"}, high: []string{"T-3"}},
		{name: "both high: the dispatch order", specs: map[string]string{"T-2": high2, "T-3": high3},
			want: []string{"T-2", "T-3"}, high: []string{"T-2", "T-3"}},
		{name: "a fix of the open review is ordinary",
			specs: map[string]string{"T-2": plain2, "T-3": fix3}, open: "RV-1", want: []string{"T-2", "T-3"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			now := time.Date(2026, 11, 8, 12, 0, 0, 0, time.UTC)
			a, p := lineApp(t, lineRoot(t), &now)
			for task, text := range c.specs {
				if _, _, err := store.SetSpec(p.log.DB(), task, text, "", "lead", now); err != nil {
					t.Fatal(err)
				}
			}
			if c.open != "" {
				startReview(t, a, p, c.open)
			}
			// The waiting facts in dispatch order: T-2's first.
			for _, task := range []string{"T-2", "T-3"} {
				if !a.record(p, event.Record{Task: task, Attempt: 1, Step: event.StepWaiting, Kind: event.KindFact},
					dispatchData{Level: "medium", Types: []string{"sonnet"}}) {
					t.Fatalf("the log refused %s's waiting fact", task)
				}
			}
			if got := waitingOrder(a, p); !slices.Equal(got, c.want) {
				t.Errorf("passOrder's waiting tasks %v, want %v: the order the next free lane is offered in", got, c.want)
			}
			if got := statusHigh(a); !slices.Equal(got, c.high) {
				t.Errorf("status marks %v high, want %v: it names the tasks the pass picks first", got, c.high)
			}
		})
	}
}

// startReview records a review's start for id: an open review in the fold.
func startReview(t *testing.T, a *App, p *proj, id string) {
	t.Helper()
	if !a.record(p, event.Record{Task: event.ReviewTask, Step: event.StepReviewStarted, Kind: event.KindFact},
		ReviewData{ID: id, Head: "h1"}) {
		t.Fatalf("the log refused the review start %s", id)
	}
}

// waitingOrder is a's pass order with its non-waiting tasks dropped: its waiting part.
func waitingOrder(a *App, p *proj) []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	var out []string
	for _, task := range p.passOrder() {
		if mt := p.folded.loop[task]; mt != nil && mt.state == stWaiting {
			out = append(out, task)
		}
	}
	return out
}

// statusHigh is the waiting tasks a's status marks high, by task id.
func statusHigh(a *App) []string {
	var out []string
	for _, ts := range a.Status().Project.Tasks {
		if ts.State == "waiting" && ts.High {
			out = append(out, ts.Task)
		}
	}
	slices.Sort(out)
	return out
}
