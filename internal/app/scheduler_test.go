package app

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"testing"
)

// schedStatus is the scheduler's state obor status reports: the scratch's owner ("-": free) and the queue.
func schedStatus(t *testing.T, h *harness) (string, []ScratchWait) {
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
	h.write(filepath.Join(h.root, ".obor", "config.json"), `{"name":"pj","prefix":"pj","roles":{"integrator":{}}}`)
	h.a.opt.Loop = func(string) (*Loop, error) {
		return &Loop{Scratch: f, Agents: f, Typer: f,
			Disk: &Disk{Volume: "/vol/pj", Free: func() (uint64, error) { return 1 << 30, nil }}}, nil
	}
	p := reloadProj(t, h)
	if !p.merging {
		t.Fatal("precondition: the merge loop runs")
	}
	for _, task := range []string{"T-9", "T-10"} {
		addRecord(t, h, task, StepSubmitted, KindFact, submitData{Lane: "O-01"})
		addRecord(t, h, task, StepSubmit, KindResult, submitOut{SHA: "s-" + task})
	}
	owner, q := schedStatus(t, h)
	want := []ScratchWait{{Task: "T-10"}, {Task: "T-9"}} // the pass's order, by id as strings
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
	addRecord(t, h, "T-2", StepSubmitted, KindFact, submitData{Lane: "O-02"})
	addRecord(t, h, "T-2", StepSubmit, KindResult, submitOut{SHA: "s2"})
	p := h.a.p
	check := func(state, why string, resolved bool) {
		t.Helper()
		if got := p.folded.loop["T-1"].state; got != state {
			t.Fatalf("precondition: T-1 %s, want %s", got, state)
		}
		owner, q := schedStatus(t, h)
		want := []ScratchWait{{Task: "T-2", WaitsOn: "T-1", Why: why}}
		if resolved { // T-1's own gate needs the scratch: listed, with nothing it waits for
			want = []ScratchWait{{Task: "T-1"}, {Task: "T-2", WaitsOn: "T-1", Why: why}}
		}
		if owner != "T-1 "+why || !slices.Equal(q, want) {
			t.Errorf("T-1 %s: scratch %s, queue %+v; want T-1 %s, %+v", state, owner, q, why, want)
		}
	}
	check(stConflict, ScratchConflict, false)
	f.Launch(ctx, Launch{})
	h.pass(ctx)
	check(stResolving, ScratchResolving, false)
	if len(f.gated) != 0 {
		t.Fatalf("resolving: gated %v; want none", f.gated)
	}
	h.a.needsYou(p, p.folded.loop["T-1"], causeHeld, "held")
	check(stNeedsYou, ScratchHeld, false)
	h.pass(ctx)
	if len(f.gated) != 0 {
		t.Fatalf("held: gated %v; want none", f.gated)
	}
	if _, err := h.a.resume(Call{Root: h.root, Task: "T-1", Role: "lead", CallID: "r"}); err != nil {
		t.Fatal(err)
	}
	h.pass(ctx)
	check(stResolving, ScratchResolving, false)
	if _, err := h.a.verdict(ctx, MethodResolved, Call{Root: h.root, Task: "T-1", Role: "integrator", Agent: "pj-int",
		CallID: "v", Job: JobID(p.folded.loop["T-1"].jobKey())}); err != nil {
		t.Fatal(err)
	}
	check(stResolved, ScratchResolved, true)
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

func (f *cmdScratch) Gate(ctx context.Context, in GateInput) (GateOutcome, error) {
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
	h.write(filepath.Join(h.root, ".obor", "config.json"), `{"name":"pj","prefix":"pj","roles":{"integrator":{}}}`)
	f := &cmdScratch{schedScratch: sf, started: make(chan struct{}), blank: make(chan struct{}),
		blanked: make(chan struct{}), release: make(chan struct{})}
	h.a.opt.Loop = func(string) (*Loop, error) { return &Loop{Scratch: f, Agents: f, Typer: f}, nil }
	reloadProj(t, h)
	if err := os.MkdirAll(h.lanes.lanes[0].Dir, 0o755); err != nil {
		t.Fatal(err)
	}
	addRecord(t, h, "T-3", StepSubmitted, KindFact, submitData{Lane: "O-01"})
	addRecord(t, h, "T-3", StepSubmit, KindResult, submitOut{SHA: "s3"})
	h.a.advance(context.Background())
	<-f.started
	ps := h.a.Status().Project
	if len(ps.Running) != 1 || ps.Running[0].Step != StepGate || ps.Running[0].Command != "check_affected" {
		t.Errorf("running %+v; want T-3's gate running check_affected", ps.Running)
	}
	if s := ps.Scratch; s == nil || *s != (ScratchStatus{Task: "T-3", Why: ScratchGating}) {
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
	if len(ps.Running) != 1 || ps.Running[0].Step != StepGate || ps.Running[0].Command != "" {
		t.Errorf("between two commands: running %+v; want T-3's gate with no command", ps.Running)
	}
	if ps := h.a.Status().Project; len(ps.Running) != 0 || ps.Scratch != nil {
		t.Errorf("after the gate: running %+v, scratch %+v; want none", ps.Running, ps.Scratch)
	}
}
