package app

import (
	"context"
	"encoding/json"
	"obor/internal/app/event"
	"obor/internal/app/port"
	"obor/internal/app/wire"
	"strings"
	"sync"
	"testing"
	"time"
)

// T-405 (RV-44 #1–#3): a start runs in its step again, as before T-404: the step returns when the launch has, so the
// task, the merge loop pass and its shutdown own the whole launch, two tasks never start the integrator at once, and a
// typed integrator's start goes on to its job's send in the same step.

// heldStart is a project's adapters whose start of agent (every start when agent is "") enters, then returns only once
// release is closed; the integrator, once started, shows in the app's snapshot (its herdr event).
type heldStart struct {
	*concScratch
	a       *App
	root    string
	agent   string
	entered chan string
	release chan struct{}

	mu             sync.Mutex
	inflight, most int
	starts         int
	returned       bool
	typed          []string // agent and job of each prompt
}

func (f *heldStart) Launch(ctx context.Context, l port.Launch) (port.Started, error) {
	if f.agent != "" && l.Agent != f.agent {
		return f.concScratch.Launch(ctx, l)
	}
	f.mu.Lock()
	f.starts++
	f.inflight++
	f.most = max(f.most, f.inflight)
	f.mu.Unlock()
	f.entered <- l.Agent
	<-f.release
	if strings.HasSuffix(l.Agent, "-int") {
		f.a.mu.Lock()
		f.a.obs["w1:p9"] = &seen{pane: port.Pane{ID: "w1:p9", Workspace: "w1", Cwd: f.root, Label: l.Agent, Agent: "claude",
			Status: "idle"}, since: f.a.opt.Now()}
		f.a.mu.Unlock()
	}
	f.mu.Lock()
	f.inflight--
	f.returned = true
	f.mu.Unlock()
	return port.Started{Line: "STARTED " + l.Agent}, nil
}

func (f *heldStart) Type(_ context.Context, agent, text string, _ bool) (bool, error) {
	job := jobOf(text)
	f.mu.Lock()
	f.typed = append(f.typed, agent+" "+job)
	f.mu.Unlock()
	return true, nil
}

// heldRig is newConcRig with pj's adapters f (its gate holds nothing).
func heldRig(t *testing.T, agent string) (*concRig, *proj, *heldStart) {
	r := newConcRig(t)
	r.fakes["pj"].hold = ""
	p := r.proj("pj")
	f := &heldStart{concScratch: r.fakes["pj"], a: r.a, root: p.root, agent: agent, entered: make(chan string, 4),
		release: make(chan struct{})}
	loop := r.a.opt.Loop
	r.a.opt.Loop = func(root string) (*Loop, error) {
		fx, err := loop(root)
		if err == nil && root == p.root {
			fx.Agents, fx.Typer = f, f
		}
		return fx, err
	}
	return r, p, f
}

// gateDone records task's lane gate with outcome (green, conflict): its integrator job is next.
func gateDone(r *concRig, task, outcome string) {
	r.add("pj", task, event.StepGate, event.KindIntent, gateIn{Kind: port.InputLane, Input: "s"})
	r.add("pj", task, event.StepGate, event.KindResult, gateOut{Outcome: outcome, Candidate: port.Candidate{Task: task, SHA: "c",
		Target: "b", Patch: "p"}})
}

// The reviewer's TestRV44DeliveryReleasesLiveLaunch, fixed: a worker that fetches its job while its start runs gets
// the job (T-396), but the start goes on in its step: no result before the launch's, a drop is refused, and the merge
// loop's shutdown waits for the launch.
func TestDeliveryKeepsTheStartOwned(t *testing.T) {
	t.Parallel()
	r, p, f := heldRig(t, "pj-l-02")
	dispatchT2(r)
	r.add("pj", "T-2", event.StepPrepare, event.KindResult, prepareOut{Base: "b"})
	r.run()
	select {
	case <-f.entered:
	case <-time.After(10 * time.Second):
		t.Fatal("T-2's start never began")
	}

	raw, err := r.a.jobNext(wire.Call{Root: p.root, Agent: "pj-l-02", Role: "worker", Lane: "L-02", CallID: "fetched"})
	var job wire.JobReply
	_ = json.Unmarshal([]byte(raw), &job)
	if err != nil || job.State != "job" || job.Job != "T-2/1/work" {
		t.Fatalf("job_next during the start: %s, %v; want T-2/1/work", raw, err)
	}
	time.Sleep(200 * time.Millisecond) // a delivery shortcut would have ended the start by now
	if n := r.count("pj", "T-2", event.KindResult, event.StepStartWork); n != 0 {
		t.Errorf("the start's result written while its launch runs (%d)", n)
	}
	if a := r.dropWithin("pj", "T-2", "drop"); a.err == nil || !strings.Contains(a.err.Error(), "is running; retry when it settles") {
		t.Errorf("drop while the launch runs: %q, %v; want refused", a.reply, a.err)
	}
	r.cancel()
	select {
	case <-r.done:
		t.Error("the merge loop's shutdown returned while the launch runs")
	case <-time.After(200 * time.Millisecond):
	}
	close(f.release)
	select {
	case <-r.done:
	case <-time.After(10 * time.Second):
		t.Fatal("the merge loop did not stop once the launch returned")
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.returned || f.starts != 1 {
		t.Errorf("launch returned %v, starts %d; want the one launch ended before the loop", f.returned, f.starts)
	}
}

// The reviewer's TestRV44IntegratorStartOwnership, fixed: two tasks whose jobs need the integrator, which does not
// run: one pass starts it once (the second task's step waits for the first's start); the second job then waits for
// the first job's verdict (T-602: nothing typed into the session the first job runs in, no second start).
func TestIntegratorStartsOneAtATime(t *testing.T) {
	t.Parallel()
	for _, c := range []struct{ name, first, second, job string }{
		{"review + review", outGreen, outGreen, "review"},
		{"review + resolve", outGreen, outConflict, "resolve"},
		{"resolve + review", outConflict, outGreen, "review"},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			r, p, f := heldRig(t, "")
			gateDone(r, "T-1", c.first)
			gateDone(r, "T-5", c.second)
			r.a.mu.Lock()
			delete(r.a.obs, "w1:p9") // no integrator runs
			r.a.mu.Unlock()
			passed := make(chan struct{})
			go func() { r.a.advance(context.Background()); close(passed) }()
			select {
			case agent := <-f.entered:
				if agent != "pj-int" {
					t.Fatalf("started %s; want pj-int", agent)
				}
			case <-time.After(10 * time.Second):
				t.Fatal("no integrator start began")
			}
			select {
			case <-f.entered:
				t.Error("a second integrator start while the first runs")
			case <-passed:
				t.Error("the pass ended while its start runs")
			case <-time.After(200 * time.Millisecond):
			}
			close(f.release)
			select {
			case <-passed:
			case <-time.After(10 * time.Second):
				t.Fatal("the pass did not end")
			}
			r.a.effects.Wait()
			f.mu.Lock()
			defer f.mu.Unlock()
			if f.starts != 1 || f.most != 1 {
				t.Errorf("integrator starts %d (at once: %d); want 1", f.starts, f.most)
			}
			if len(f.typed) != 0 {
				t.Errorf("typed %q; want nothing while T-1's job is out", f.typed)
			}
			if p.log.Open(event.Key{Task: "T-5", Attempt: 1, Step: c.job}) || p.log.Done(event.Key{Task: "T-5", Attempt: 1, Step: c.job}) {
				t.Errorf("T-5's %s recorded while T-1's job is out", c.job)
			}
		})
	}
}
