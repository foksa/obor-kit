package app

import (
	"context"
	"errors"
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

// T-672, T-870: serve stops the integrator once a verdict leaves no job for it, and the reviewer once its review
// closes: two rules on the rule pass (rules.go, rolestop.go), whose action runs on an idle pane and is done when the
// pane is at its shell. A brief to a stopped role agent starts it fresh on the brief.

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

// stopRig is a rule harness (a merging project pj with the lead pj-lead) whose snapshot has the integrator's pane
// w1:int, running and idle, and a stop spy for the rule pass.
func stopRig(t *testing.T) (*harness, *reviewStopSpy) {
	t.Helper()
	h := ruleHarness(t)
	h.snap = port.Snapshot{Workspaces: map[string]string{"w1": "pj"}, Panes: []port.Pane{
		{ID: "w1:lead", Workspace: "w1", Cwd: h.root, Agent: "claude", Label: "pj-lead", Status: "idle"},
		{ID: "w1:int", Workspace: "w1", Cwd: h.root, Agent: "claude", Label: "pj-int", Status: "idle"},
	}}
	h.a.connect(context.Background(), h.snap)
	p := h.proj()
	h.a.mu.Lock()
	w := h.a.whereIs(p, p.integrator)
	h.a.mu.Unlock()
	if p.integrator != "pj-int" || w.State != Identified || !Ready(w.Pane.Status) {
		t.Fatalf("precondition: the integrator %q at %+v, want pj-int in w1:int idle", p.integrator, w)
	}
	return h, &reviewStopSpy{}
}

// T-870: the integrator's stop rule, one table (condition → action). Its pane runs it and is idle, no task has an
// integrator job open or queued, and a verdict came since its start: only then does it stop it, and only once the
// pane is at its shell is the action done.
func TestRoleStopIntegratorRule(t *testing.T) {
	t.Parallel()
	type step func(t *testing.T, h *harness, s *reviewStopSpy)
	pass := func(t *testing.T, h *harness, s *reviewStopSpy) {
		h.a.rulePass(context.Background(), rules, &Loop{Agents: s})
	}
	submitted := func(t *testing.T, h *harness, _ *reviewStopSpy) { // its task done, no integrator job yet
		addRecord(t, h, "T-1", event.StepSubmitted, event.KindFact, submitData{Lane: "L-01"})
	}
	green := func(t *testing.T, h *harness, s *reviewStopSpy) {
		submitted(t, h, s)
		addRecord(t, h, "T-1", event.StepGate, event.KindResult, gateOut{Outcome: outGreen,
			Candidate: port.Candidate{Task: "T-1", SHA: "c1", Target: "b", Patch: "p1"}})
	}
	jobOut := func(t *testing.T, h *harness, s *reviewStopSpy) { // the review job is out
		green(t, h, s)
		addRecord(t, h, "T-1", event.StepReview, event.KindIntent, promptData{Agent: "pj-int", Job: "T-1/1/review", Patch: "p1"})
	}
	answered := func(t *testing.T, h *harness, s *reviewStopSpy) { // the job's verdict is in
		jobOut(t, h, s)
		addRecord(t, h, "T-1", "accept.c1", event.KindResult, verdictResult{Job: "T-1/1/review", Patch: "p1"})
	}
	started := func(t *testing.T, h *harness, s *reviewStopSpy) { // serve starts it again: the verdict is not its start's
		answered(t, h, s)
		addRecord(t, h, "T-1", stepStart+event.StepReview, event.KindResult, startOut{Line: "STARTED pj-int",
			Agent: "pj-int", Pane: "w1:int", Kind: "claude"})
	}
	status := func(id, status, agent string) step {
		return func(t *testing.T, h *harness, _ *reviewStopSpy) {
			h.status(id, func(p *port.Pane) { p.Status, p.Agent = status, agent })
		}
	}
	secondPane := func(t *testing.T, h *harness, _ *reviewStopSpy) {
		h.snap.Panes = append(h.snap.Panes, port.Pane{ID: "w1:int2", Workspace: "w1", Cwd: h.root,
			Agent: "claude", Label: "pj-int", Status: "idle"})
		h.a.connect(context.Background(), h.snap)
		h.a.mu.Lock()
		w := h.a.whereIs(h.proj(), "pj-int")
		h.a.mu.Unlock()
		if w.State != Ambiguous {
			t.Fatalf("precondition: pj-int %+v, want ambiguous", w)
		}
	}
	cases := []struct {
		name  string
		steps []step
		stops []string // the stops, "agent pane force"
	}{
		{name: "no verdict since its start: none", steps: []step{submitted, pass}},
		{name: "its last verdict is before its start: none", steps: []step{started, pass}},
		{name: "its job is out: none", steps: []step{jobOut, pass}},
		{name: "its job is queued (green, no accept): none", steps: []step{green, pass}},
		{name: "it still works after the verdict: none, then idle: stopped",
			steps: []step{answered, status("w1:int", "working", "claude"), pass, status("w1:int", "idle", "claude"), pass},
			stops: []string{"pj-int w1:int force=false"}},
		{name: "it is at its shell: none", steps: []step{answered, status("w1:int", "", ""), pass}},
		{name: "two panes claim it: none", steps: []step{answered, secondPane, pass}},
		{name: "the pane at its shell after the stop: no second stop",
			steps: []step{answered, pass, status("w1:int", "", ""), pass},
			stops: []string{"pj-int w1:int force=false"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			h, s := stopRig(t)
			for _, st := range c.steps {
				st(t, h, s)
			}
			if !slices.Equal(s.stops, c.stops) {
				t.Errorf("stops %q, want %q", s.stops, c.stops)
			}
		})
	}
}

// T-870, T-875: an older log's role stop (stop.<agent>[.N], task key roles) folds to nothing (compat until v0.28): no
// fold reads it (T-875 removed the no-op foldRoles), and the rule still stops an integrator whose job was answered;
// status never reads a record for "stopped" (the pane says).
func TestRoleStopOldRecordsFoldToNothing(t *testing.T) {
	t.Parallel()
	h, s := stopRig(t)
	addRecord(t, h, "T-1", event.StepSubmitted, event.KindFact, submitData{Lane: "L-01"})
	addRecord(t, h, "T-1", event.StepGate, event.KindResult, gateOut{Outcome: outGreen,
		Candidate: port.Candidate{Task: "T-1", SHA: "c1", Target: "b", Patch: "p1"}})
	addRecord(t, h, "T-1", event.StepReview, event.KindIntent, promptData{Agent: "pj-int", Job: "T-1/1/review", Patch: "p1"})
	addRecord(t, h, "T-1", "accept.c1", event.KindResult, verdictResult{Job: "T-1/1/review", Patch: "p1"})
	addRecord(t, h, event.RoleTask, stepRoleStop+"pj-int", event.KindIntent, stopData{Agent: "pj-int"})
	addRecord(t, h, event.RoleTask, stepRoleStop+"pj-int", event.KindResult,
		map[string]string{"agent": "pj-int", "role": "integrator", "line": "STOPPED pj-int"})
	old := 0
	for _, rec := range readLog(t, h.root) {
		if rec.Task == event.RoleTask {
			old++
		}
	}
	if old != 2 {
		t.Fatalf("precondition: %d old role stop records in the log, want 2", old)
	}
	h.a.rulePass(context.Background(), rules, &Loop{Agents: s})
	if want := []string{"pj-int w1:int force=false"}; !slices.Equal(s.stops, want) {
		t.Errorf("stops %q with the old record and its verdict in, want %q", s.stops, want)
	}
	if got := h.a.Status().Project.Stopped; len(got) != 0 {
		t.Errorf("status stopped %q with the record in and pj-int running, want none", got)
	}
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
	r.a.rulePass(ctx, rules, &Loop{Agents: s})
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

// T-801: a custom role's agent is reached by obor brief like the advisor: a brief to it stopped (its labelled pane at a
// shell) starts it fresh with the brief's line, and serve never stops it once the brief is finished (the lead does).
func TestBriefReachesCustomRole(t *testing.T) {
	t.Parallel()
	root := lineRoot(t)
	cfg := `{"name":"proj","prefix":"pj","roles":{"integrator":"","ux_buddy":{"model":"","write":["docs/ux/"]}}}`
	if err := os.WriteFile(filepath.Join(root, ".obor/config.json"), []byte(cfg), 0o644); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC)
	a, p := lineApp(t, root, &now)
	a.connect(context.Background(), port.Snapshot{Workspaces: map[string]string{"w": "proj"}, Panes: []port.Pane{
		{ID: "w:lead", Workspace: "w", Cwd: root, Agent: "claude", Label: p.out.lead(), Status: "idle"},
		{ID: "w:ux", Workspace: "w", Cwd: root, Label: "pj-ux-buddy", Status: "idle"},
	}})
	reply, err := a.briefCall(wire.Call{Root: p.root, CallID: "b-ux", To: "pj-ux-buddy", Note: "review the flows",
		Role: "lead", Agent: "pj-lead"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(reply, "serve starts it fresh") {
		t.Errorf("reply %q, want it started fresh", reply)
	}
	f := &startJobFakes{concScratch: newConcScratch("")}
	a.deliver(context.Background(), p, &Loop{Agents: f, Typer: f})
	if want := (port.Launch{Agent: "pj-ux-buddy", Pane: "w:ux", Fresh: true, Prompt: "[obor pj-lead] review the flows"}); len(f.launches) != 1 || f.launches[0] != want {
		t.Errorf("launches %+v, want %+v", f.launches, want)
	}
	s := &reviewStopSpy{}
	now = now.Add(time.Hour)
	a.rulePass(context.Background(), rules, &Loop{Agents: s, Review: &reviewGitFake{}})
	same(t, len(s.stops), 0, "serve's stops of the custom role")
}

// reviewStopSpy is agentsSpy that records every Stop; err, when set, is the error every stop fails with.
type reviewStopSpy struct {
	agentsSpy
	stops []string
	err   error
}

func (s *reviewStopSpy) Stop(_ context.Context, agent, pane string, force bool) (string, error) {
	s.stops = append(s.stops, fmt.Sprintf("%s %s force=%t", agent, pane, force))
	if s.err != nil {
		return "", s.err
	}
	return "STOPPED " + agent, nil
}

// reviewStopRig is a review rig (RV-4 closed in git, the reviewer pj-rev idle in w:rev) with a stop spy.
func reviewStopRig(t *testing.T) (*reviewRig, *reviewStopSpy) {
	t.Helper()
	r := newReviewRig(t)
	s := &reviewStopSpy{}
	r.a.mu.Lock()
	w := r.a.whereIs(r.p, "pj-rev")
	r.a.mu.Unlock()
	if w.State != Identified || !Ready(w.Pane.Status) {
		t.Fatalf("precondition: the reviewer %+v, want identified and idle", w)
	}
	return r, s
}

// T-870: the reviewer's stop rule, one table (condition → action). The last review serve started is closed in git
// (read at most every reviewDueEvery), the reviewer has no open brief and its pane runs it and is idle: only then does
// it stop it, and only once the pane is at its shell is the action done.
func TestRoleStopReviewerRule(t *testing.T) {
	t.Parallel()
	type step func(t *testing.T, r *reviewRig, s *reviewStopSpy)
	pass := func(t *testing.T, r *reviewRig, s *reviewStopSpy) {
		r.a.rulePass(context.Background(), rules, &Loop{Review: r.git, Agents: s})
	}
	started := func(t *testing.T, r *reviewRig, _ *reviewStopSpy) { r.started() }
	closed := func(t *testing.T, r *reviewRig, _ *reviewStopSpy) {
		r.git.last = "RV-5 closed · abcdef0 · approved · T-a"
	}
	status := func(status, agent string) step {
		return func(t *testing.T, r *reviewRig, _ *reviewStopSpy) {
			pane := r.a.obs["w:rev"].pane
			pane.Status, pane.Agent = status, agent
			r.a.event(context.Background(), port.Event{Kind: port.EventStatus, Pane: "w:rev", Status: status,
				Agent: agent, New: pane})
		}
	}
	later := func(d time.Duration) step {
		return func(_ *testing.T, r *reviewRig, _ *reviewStopSpy) { r.now = r.now.Add(d) }
	}
	cases := []struct {
		name  string
		steps []step
		stops []string
	}{
		{name: "no review started: none", steps: []step{pass}},
		{name: "its review is open: none", steps: []step{started, pass}},
		{name: "closed: stopped", steps: []step{started, closed, pass},
			stops: []string{"pj-rev w:rev force=false"}},
		{name: "closed with its brief open: none", steps: []step{started, closed,
			func(t *testing.T, r *reviewRig, _ *reviewStopSpy) {
				if _, err := r.a.briefCall(wire.Call{Root: r.p.root, CallID: "b2", To: "pj-rev", Note: "check X",
					Role: "lead", Agent: "pj-lead"}); err != nil {
					t.Fatal(err)
				}
				if r.p.out.brief("pj-rev") == nil {
					t.Fatal("precondition: the reviewer has no open brief")
				}
			}, pass}},
		{name: "closed but it works: none, then idle: stopped",
			steps: []step{started, closed, status("working", "codex"), pass, status("idle", "codex"), pass},
			stops: []string{"pj-rev w:rev force=false"}},
		{name: "closed but it is at its shell: none",
			steps: []step{started, closed, status("", ""), pass}},
		{name: "closed and the pane at its shell after the stop: no second stop",
			steps: []step{started, closed, pass, status("", ""), later(reviewDueEvery), pass},
			stops: []string{"pj-rev w:rev force=false"}},
		{name: "its open review was read less than reviewDueEvery ago: none, then stopped",
			steps: []step{started, pass, closed, pass, later(reviewDueEvery), pass},
			stops: []string{"pj-rev w:rev force=false"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			r, s := reviewStopRig(t)
			for _, st := range c.steps {
				st(t, r, s)
			}
			if !slices.Equal(s.stops, c.stops) {
				t.Errorf("stops %q, want %q", s.stops, c.stops)
			}
		})
	}
}

// T-875: a reviewer stop that keeps failing is a needs-you after ruleFails passes in a row, though every pass falls
// inside the git poll's reviewDueEvery: a review read closed stays eligible on every pass until the stop's effect
// shows, not only on the pass that read it (before, the second pass had no condition, so rulePass dropped the live
// count and three failures never happened).
func TestRoleStopReviewerFailuresAccumulate(t *testing.T) {
	t.Parallel()
	r, s := reviewStopRig(t)
	r.started()
	r.git.last = "RV-5 closed · abcdef0 · approved · T-a"
	s.err = errors.New("herdr refused the stop")
	reason := fmt.Sprintf("rule %s failed %d passes in a row (serve's log names the errors)", ruleReviewerStop, ruleFails)
	for i := 1; i <= ruleFails; i++ {
		r.a.rulePass(context.Background(), rules, &Loop{Review: r.git, Agents: s})
		if len(s.stops) != i {
			t.Fatalf("after pass %d: %d stop attempts, want %d (the closed review was not eligible again)", i, len(s.stops), i)
		}
		if inForce := r.p.has(event.RuleTask, event.StepNeedsYou, reason); inForce != (i == ruleFails) {
			t.Errorf("after pass %d: the rule's needs-you in force %v, want %v", i, inForce, i == ruleFails)
		}
	}
	if n := len(r.p.folded.active[event.RuleTask]); n != 1 {
		t.Errorf("%d needs-you facts in force, want one", n)
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
