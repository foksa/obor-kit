package app

import (
	"context"
	"fmt"
	"obor/internal/app/event"
	"obor/internal/app/port"
	"obor/internal/app/wire"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

// T-672: serve stops the integrator once a verdict leaves no job for it, and the reviewer once its review closes; a
// brief to a stopped role agent starts it fresh on the brief.

// stopSpy is startJobFakes that also records every Stop ("<agent> <pane> force=<force>").
type stopSpy struct {
	*startJobFakes
	stops []string
}

func (s *stopSpy) Stop(_ context.Context, agent, pane string, force bool) (string, error) {
	s.stops = append(s.stops, fmt.Sprintf("%s %s force=%t", agent, pane, force))
	return "STOPPED " + agent, nil
}

func newStopSpy(r *concRig) *stopSpy {
	return &stopSpy{startJobFakes: &startJobFakes{concScratch: r.fakes["pj"]}}
}

// runJob runs task's next step (its review job's start) with s as agents and typer.
func runJob(r *concRig, p *proj, task string, s *stopSpy) bool {
	r.a.mu.Lock()
	step := r.a.next(p, p.folded.loop[task])
	r.a.mu.Unlock()
	if step == nil {
		r.t.Fatalf("%s: no step", task)
	}
	return step(context.Background(), &Loop{Agents: s, Typer: s})
}

// acceptJob is the integrator's accept of task's first review job (patch p).
func acceptJob(r *concRig, p *proj, task, patch, callID string) {
	r.t.Helper()
	if _, err := r.a.verdict(context.Background(), wire.MethodAccept, wire.Call{Root: p.root, Task: task, Agent: "pj-int",
		Role: "integrator", CallID: callID, Job: task + "/1/review", Patch: patch}); err != nil {
		r.t.Fatal(err)
	}
}

// atShell puts the integrator's pane w1:p9 at its shell, as launch.Stop leaves it (the label stays).
func atShell(r *concRig) {
	r.a.mu.Lock()
	o := r.a.obs["w1:p9"]
	o.pane.Agent, o.pane.Status = "", ""
	r.a.mu.Unlock()
}

// atAgent puts the integrator's pane w1:p9 back to running an agent, as herdr reports it once a fresh start is in.
func atAgent(r *concRig) {
	r.a.mu.Lock()
	o := r.a.obs["w1:p9"]
	o.pane.Agent, o.pane.Status = "claude", "idle"
	r.a.mu.Unlock()
}

func TestRoleStopIntegratorAfterVerdict(t *testing.T) {
	t.Parallel()
	r := newConcRig(t)
	p := r.proj("pj")
	s := newStopSpy(r)
	ctx := context.Background()
	greenTask(r, "T-1", port.Candidate{Task: "T-1", SHA: "c1", Target: "b", Patch: "p1"})
	if !runJob(r, p, "T-1", s) {
		t.Fatal("precondition: T-1's review job not started")
	}
	r.a.stopRoles(ctx, p, &Loop{Agents: s})
	same(t, len(s.stops), 0, "stops while T-1's job is open")

	acceptJob(r, p, "T-1", "p1", "a1")
	r.a.stopRoles(ctx, p, &Loop{Agents: s})
	if want := []string{"pj-int w1:p9 force=false"}; !slices.Equal(s.stops, want) {
		t.Fatalf("stops %q after the verdict, no other job: want %q", s.stops, want)
	}
	same(t, done(p, event.Key{Task: event.RoleTask, Step: stepRoleStop + "pj-int"}), true, "the stop's result recorded")
	atShell(r)
	if got := r.a.Status().Project.Stopped; !slices.Equal(got, []string{"pj-int"}) {
		t.Errorf("status stopped %q, want [pj-int]", got)
	}
	r.a.stopRoles(ctx, p, &Loop{Agents: s})
	same(t, len(s.stops), 1, "stops after a second pass (the stop is done)")
}

// A verdict with another job queued (a green task to review) does not stop the integrator; that job's verdict does.
func TestRoleStopIntegratorWaitsForQueuedJob(t *testing.T) {
	t.Parallel()
	r := newConcRig(t)
	p := r.proj("pj")
	s := newStopSpy(r)
	ctx := context.Background()
	greenTask(r, "T-1", port.Candidate{Task: "T-1", SHA: "c1", Target: "b", Patch: "p1"})
	greenTask(r, "T-5", port.Candidate{Task: "T-5", SHA: "c5", Target: "b", Patch: "p5"})
	if !runJob(r, p, "T-1", s) {
		t.Fatal("precondition: T-1's review job not started")
	}
	acceptJob(r, p, "T-1", "p1", "a1")
	r.a.stopRoles(ctx, p, &Loop{Agents: s})
	same(t, len(s.stops), 0, "stops with T-5's job queued")

	if !runJob(r, p, "T-5", s) {
		t.Fatal("T-5's review job not started")
	}
	r.a.stopRoles(ctx, p, &Loop{Agents: s})
	same(t, len(s.stops), 0, "stops while T-5's job is open")
	acceptJob(r, p, "T-5", "p5", "a5")
	r.a.stopRoles(ctx, p, &Loop{Agents: s})
	same(t, strings.Join(s.stops, ","), "pj-int w1:p9 force=false", "stops after T-5's verdict")
}

// The integrator still ends its turn after the verdict: it is stopped once idle, never cut off.
func TestRoleStopIntegratorWaitsForIdle(t *testing.T) {
	t.Parallel()
	r := newConcRig(t)
	p := r.proj("pj")
	s := newStopSpy(r)
	ctx := context.Background()
	greenTask(r, "T-1", port.Candidate{Task: "T-1", SHA: "c1", Target: "b", Patch: "p1"})
	if !runJob(r, p, "T-1", s) {
		t.Fatal("precondition: T-1's review job not started")
	}
	acceptJob(r, p, "T-1", "p1", "a1")
	setStatus(r, "working")
	r.a.stopRoles(ctx, p, &Loop{Agents: s})
	same(t, len(s.stops), 0, "stops while it works")
	setStatus(r, "idle")
	r.a.stopRoles(ctx, p, &Loop{Agents: s})
	same(t, len(s.stops), 1, "stops once idle")
}

// The next job after a stop starts the integrator fresh: no job.lost, no needs-you, and status no longer shows it
// stopped once its start is in and herdr reports the pane running it.
func TestRoleStopNextJobStartsFresh(t *testing.T) {
	t.Parallel()
	r := newConcRig(t)
	p := r.proj("pj")
	s := newStopSpy(r)
	ctx := context.Background()
	greenTask(r, "T-1", port.Candidate{Task: "T-1", SHA: "c1", Target: "b", Patch: "p1"})
	if !runJob(r, p, "T-1", s) {
		t.Fatal("precondition: T-1's review job not started")
	}
	acceptJob(r, p, "T-1", "p1", "a1")
	r.a.stopRoles(ctx, p, &Loop{Agents: s})
	atShell(r)
	if len(s.stops) != 1 || r.a.whereIs(p, "pj-int").State != Unavailable {
		t.Fatalf("precondition: pj-int not stopped: %q", s.stops)
	}

	now := r.a.opt.Now().Add(2 * r.a.opt.Gone)
	r.a.opt.Now = func() time.Time { return now }
	greenTask(r, "T-5", port.Candidate{Task: "T-5", SHA: "c5", Target: "b", Patch: "p5"})
	if !runJob(r, p, "T-5", s) {
		t.Fatal("T-5's review job not started after the stop")
	}
	if n := len(s.launches); n != 2 || !s.launches[1].Fresh || s.launches[1].Prompt != wakeLine("T-5/1/review") {
		t.Fatalf("launches %+v, want T-5's fresh start with its wake", s.launches)
	}
	for _, rec := range readLog(t, p.root) {
		if rec.Step == event.StepLost || rec.Step == event.StepNeedsYou {
			t.Errorf("record %s %s on the way to the fresh start", rec.Task, rec)
		}
	}
	atAgent(r) // herdr reports the fresh start in w1:p9: the pane is no longer at its shell
	if got := r.a.Status().Project.Stopped; len(got) != 0 {
		t.Errorf("status stopped %q after the start, want none", got)
	}
}

// A brief to a stopped advisor (its labelled pane at a shell) starts it fresh in that pane with the brief's line as
// its first prompt, its briefed fact recorded and the line delivered by the start; a running advisor's brief is typed
// as before, nothing started.
func TestBriefToStoppedRoleAgent(t *testing.T) {
	t.Parallel()
	root := lineRoot(t)
	cfg := `{"name":"proj","prefix":"pj","roles":{"integrator":"","advisor":""}}`
	if err := os.WriteFile(filepath.Join(root, ".obor/config.json"), []byte(cfg), 0o644); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
	for _, c := range []struct {
		name    string
		agent   string // the advisor pane's agent kind ("": at its shell)
		started bool
	}{{"stopped", "", true}, {"running", "codex", false}} {
		t.Run(c.name, func(t *testing.T) {
			a, p := lineApp(t, root, &now)
			a.connect(context.Background(), port.Snapshot{Workspaces: map[string]string{"w": "proj"}, Panes: []port.Pane{
				{ID: "w:lead", Workspace: "w", Cwd: root, Agent: "claude", Label: p.out.lead(), Status: "idle"},
				{ID: "w:adv", Workspace: "w", Cwd: root, Agent: c.agent, Label: "pj-advisor", Status: "idle"},
			}})
			if w := a.whereIs(p, "pj-advisor"); (w.State == Identified) == c.started {
				t.Fatalf("precondition: pj-advisor %+v", w)
			}
			reply, err := a.briefCall(wire.Call{Root: p.root, CallID: "b-" + c.name, To: "pj-advisor", Note: "why X?",
				Role: "lead", Agent: "pj-lead"})
			if err != nil {
				t.Fatal(err)
			}
			if got := strings.Contains(reply, "serve starts it fresh"); got != c.started {
				t.Errorf("reply %q: says it starts fresh %v, want %v", reply, got, c.started)
			}
			var seq int64
			for _, rec := range readLog(t, p.root) {
				if rec.Step == event.StepBriefed {
					seq = rec.Seq
				}
			}
			if seq == 0 {
				t.Fatal("no briefed fact")
			}
			f := &startJobFakes{concScratch: newConcScratch("")}
			a.deliver(context.Background(), p, &Loop{Agents: f, Typer: f})
			line := "[obor pj-lead] why X?"
			key := event.Key{Task: event.MsgTask, Step: stepDeliver + fmt.Sprint(seq)}
			if c.started {
				if len(f.launches) != 1 || f.launches[0] != (port.Launch{Agent: "pj-advisor", Pane: "w:adv", Fresh: true,
					Prompt: line}) {
					t.Errorf("launches %+v, want pj-advisor fresh in w:adv with the brief's line", f.launches)
				}
				same(t, len(f.typed), 0, "lines typed")
				same(t, resultOf(p, key), `"`+sentStart+`"`, "the line's delivery")
				return
			}
			same(t, len(f.launches), 0, "starts of a running advisor")
			if len(f.typed) != 1 || f.typed[0] != line {
				t.Errorf("typed %q, want the brief's line once", f.typed)
			}
		})
	}
}

// reviewStopSpy is agentsSpy that records every Stop.
type reviewStopSpy struct {
	agentsSpy
	stops []string
}

func (s *reviewStopSpy) Stop(_ context.Context, agent, pane string, force bool) (string, error) {
	s.stops = append(s.stops, fmt.Sprintf("%s %s force=%t", agent, pane, force))
	return "STOPPED " + agent, nil
}

// The reviewer runs on after its review's brief is finished (its verify goes to the same session) until the
// review's "RV-<n> closed" commit, then it is stopped once; the advisor is never stopped by serve.
func TestRoleStopReviewerOnClose(t *testing.T) {
	t.Parallel()
	r := newReviewRig(t)
	r.started()
	s := &reviewStopSpy{}
	ctx := context.Background()
	fx := &Loop{Review: r.git, Agents: s}
	r.a.stopRoles(ctx, r.p, fx)
	same(t, len(s.stops), 0, "stops while RV-5 is open (finished, not closed)")

	r.git.last = "RV-5 closed · abcdef0 · approved · T-a"
	r.now = r.now.Add(reviewDueEvery) // the open review is read again
	r.a.stopRoles(ctx, r.p, fx)
	if want := []string{"pj-rev w:rev force=false"}; !slices.Equal(s.stops, want) {
		t.Fatalf("stops %q after RV-5 closed, want %q", s.stops, want)
	}
	r.now = r.now.Add(reviewDueEvery)
	r.a.stopRoles(ctx, r.p, fx)
	same(t, len(s.stops), 1, "stops after a second pass")
	r.reload()
	r.a.stopRoles(ctx, r.p, &Loop{Review: r.git, Agents: s})
	same(t, len(s.stops), 1, "stops after a replay")
}

// An advisor that finished its brief keeps running: serve stops it never (the lead does).
func TestRoleStopAdvisorKeptRunning(t *testing.T) {
	t.Parallel()
	root := lineRoot(t)
	cfg := `{"name":"proj","prefix":"pj","roles":{"integrator":"","advisor":""}}`
	if err := os.WriteFile(filepath.Join(root, ".obor/config.json"), []byte(cfg), 0o644); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
	a, p := lineApp(t, root, &now)
	a.connect(context.Background(), port.Snapshot{Workspaces: map[string]string{"w": "proj"}, Panes: []port.Pane{
		{ID: "w:lead", Workspace: "w", Cwd: root, Agent: "claude", Label: p.out.lead(), Status: "idle"},
		{ID: "w:adv", Workspace: "w", Cwd: root, Agent: "codex", Label: "pj-advisor", Status: "idle"},
	}})
	if _, err := a.briefCall(wire.Call{Root: p.root, CallID: "b1", To: "pj-advisor", Role: "lead"}); err != nil {
		t.Fatal(err)
	}
	lineStatus(a, "w:adv", "working", now)
	lineStatus(a, "w:adv", "idle", now)
	a.deliver(context.Background(), p, &Loop{Inbox: &inboxSpy{}})
	if p.out.brief("pj-advisor") != nil {
		t.Fatal("precondition: the advisor's brief is not finished")
	}
	s := &reviewStopSpy{}
	now = now.Add(time.Hour)
	a.stopRoles(context.Background(), p, &Loop{Agents: s, Review: &reviewGitFake{}})
	same(t, len(s.stops), 0, "stops of the finished advisor")
	if w := a.whereIs(p, "pj-advisor"); w.State != Identified {
		t.Errorf("pj-advisor %+v, want still running", w)
	}
}

// readLog is root's event log.
func readLog(t *testing.T, root string) []event.Record {
	t.Helper()
	recs, err := event.ReadLog(root)
	if err != nil {
		t.Fatal(err)
	}
	return recs
}

// A brief to a lane's worker whose labelled pane is at a shell (stopped after its merge) starts nothing (T-672
// sendback: only a role agent is started on a brief; workers are dispatched): its line stays queued, nothing typed.
func TestBriefToStoppedWorkerStartsNothing(t *testing.T) {
	t.Parallel()
	root := lineRoot(t)
	cfg := `{"name":"proj","prefix":"pj","roles":{"integrator":"","advisor":""},"lanes":1}`
	if err := os.WriteFile(filepath.Join(root, ".obor/config.json"), []byte(cfg), 0o644); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
	a, p := lineApp(t, root, &now)
	a.connect(context.Background(), port.Snapshot{Workspaces: map[string]string{"w": "proj"}, Panes: []port.Pane{
		{ID: "w:lead", Workspace: "w", Cwd: root, Agent: "claude", Label: p.out.lead(), Status: "idle"},
		{ID: "w:o1", Workspace: "w", Cwd: root, Agent: "", Label: "pj-l-01"},
	}})
	a.mu.Lock()
	w := a.whereIs(p, "pj-l-01")
	a.mu.Unlock()
	if w.State != Unavailable || w.Why == notConnected {
		t.Fatalf("precondition: pj-l-01 %+v, want no agent running (its pane at a shell)", w)
	}
	if _, ok := a.obs["w:o1"]; !ok {
		t.Fatal("precondition: the worker's labelled pane is not observed")
	}
	reply, err := a.briefCall(wire.Call{Root: p.root, CallID: "b-w", To: "pj-l-01", Note: "why X?", Role: "lead",
		Agent: "pj-lead"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(reply, "serve starts it fresh") {
		t.Errorf("reply %q says the worker is started fresh", reply)
	}
	f := &startJobFakes{concScratch: newConcScratch("")}
	a.deliver(context.Background(), p, &Loop{Agents: f, Typer: f})
	same(t, len(f.launches), 0, "starts of the stopped worker")
	same(t, len(f.typed), 0, "lines typed")
	a.mu.Lock()
	queued := 0
	for _, q := range p.queuedLines() {
		if q.msg.To == "pj-l-01" {
			queued++
		}
	}
	a.mu.Unlock()
	same(t, queued, 1, "the worker's brief lines still queued")
}
