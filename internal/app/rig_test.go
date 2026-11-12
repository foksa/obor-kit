package app

import (
	"bufio"
	"bytes"
	"cmp"
	"context"
	"crypto/sha1"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"maps"
	"net"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"obor/internal/agents"
	"obor/internal/app/event"
	"obor/internal/app/identity"
	"obor/internal/app/outbox"
	"obor/internal/app/port"
	"obor/internal/app/review"
	"obor/internal/app/wire"
	"obor/internal/store"
	"obor/internal/version"
)

// An accept is of the spec rev its candidate was gated with. A green gate of another rev ends it
// (the candidate is reviewed again), even when the patch is the same; the same rev and patch keep it (row 20).

// acceptAt gates T-1 green (kind, spec rev, candidate sha), sends its review job and accepts it.
func acceptAt(t *testing.T, h *harness, f *schedScratch, gate, kind string, rev int, sha string) {
	t.Helper()
	ctx := context.Background()
	addRecord(t, h, "T-1", gate, event.KindIntent, gateIn{Kind: kind, Input: "s1"})
	addRecord(t, h, "T-1", gate, event.KindResult, gateOut{Outcome: outGreen, SpecRev: rev,
		Candidate: port.Candidate{Task: "T-1", SHA: sha, Target: "base1", Patch: "same-diff"}})
	p := h.a.p
	mt := *p.folded.loop["T-1"]
	if !h.a.job(ctx, p, &Loop{Agents: f, Prompter: f}, mt, nth(event.StepReview, mt.reviews+1)) {
		t.Fatal("precondition: the review job was not sent")
	}
	if _, err := h.a.verdict(ctx, wire.VerdictReq{Envelope: wire.Envelope{Root: h.root, Agent: "pj-int",
		Role: "integrator", CallID: "accept" + sha}, Verb: wire.MethodAccept, Task: "T-1",
		Job: JobID(p.folded.loop["T-1"].jobKey()), Patch: "same-diff"}); err != nil {
		t.Fatal(err)
	}
	if mt := p.folded.loop["T-1"]; mt.state != stAccepted || mt.accepted != "same-diff" || mt.gateRev != rev {
		t.Fatalf("precondition: T-1 %s accepted %q at rev %d; want accepted same-diff at rev %d", mt.state,
			mt.accepted, mt.gateRev, rev)
	}
}

// countStep is the number of T-1's records of step in the log.
func countStep(t *testing.T, h *harness, step string) int {
	t.Helper()
	recs, err := event.ReadLog(h.root)
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, r := range recs {
		if r.Task == "T-1" && r.Step == step {
			n++
		}
	}
	return n
}

// Serve moves the reviewer's and the deep dive's role worktree to the head it hands the role; the reviewer or
// the deep dive working holds no merge. A role's start is not admitted (it
// reads the head once); the publish admission is config_set's only.

// admitCfg is seedRoles' config with reviews.dir.
const admitCfg = `{"version":2,"project":{"name":"pj","agent_prefix":"pj"},"agents":{"roles":{"integrator":"","reviewer":"","deep_dive":""}},"reviews":{"dir":"reviews"}}`

// seedRoles is seedSched's project with a reviewer (pane w1:rev) and a deep dive (pane w1:dd), both idle in their
// role worktrees, and T-1 accepted.
func seedRoles(t *testing.T) (*harness, *schedScratch) {
	t.Helper()
	h := newHarness(t)
	h.write(filepath.Join(h.root, ".obor", "config.json"),
		`{"version":2,"project":{"name":"pj","agent_prefix":"pj"},"agents":{"roles":{"integrator":"","reviewer":"","deep_dive":""}}}`)
	h.snap.Panes = append(h.snap.Panes,
		port.Pane{ID: "w1:rev", Group: "pj", Cwd: filepath.Join(h.wt, "reviewer"), Agent: "claude", Label: "pj-rev", Status: "idle"},
		port.Pane{ID: "w1:dd", Group: "pj", Cwd: filepath.Join(h.wt, "deep_dive"), Agent: "claude", Label: "pj-deep-dive", Status: "idle"})
	f := &schedScratch{h: h}
	h.a.opt.Loop = func(string) (*Loop, error) { return &Loop{Workflow: WorkflowOf(f), Agents: f, Prompter: f}, nil }
	if err := os.MkdirAll(h.lanes.lanes[0].Dir, 0o755); err != nil {
		t.Fatal(err)
	}
	h.connect()
	h.a.mu.Lock()
	h.a.p.folded.lr.rows["L-01"] = store.LaneRow{Lane: "L-01", Task: "T-1"}
	h.a.mu.Unlock()
	addRecord(t, h, "T-1", event.StepSubmitted, event.KindFact, submitData{Lane: "L-01", Agent: "pj-l-01", Pane: "w1:p1"})
	addRecord(t, h, "T-1", event.StepSubmit, event.KindResult, submitOut{SHA: "s1"})
	addRecord(t, h, "T-1", event.StepGate, event.KindIntent, gateIn{Kind: port.InputLane, Lane: "L-01", Input: "s1"})
	addRecord(t, h, "T-1", event.StepGate, event.KindResult, gateOut{Outcome: outConflict})
	acceptAt(t, h, f, nth(event.StepGate, 2), port.InputLane, 1, "candidate1")
	return h, f
}

// work sets pane id working (or idle) as a herdr event reports it.
func (h *harness) work(id string, working bool) {
	h.status(id, func(p *port.Pane) {
		p.Status = map[bool]string{true: "working", false: "idle"}[working]
	})
}

// state is T-1's merge-loop state.
func (h *harness) state(task string) string {
	h.a.mu.Lock()
	defer h.a.mu.Unlock()
	if mt := h.a.p.folded.loop[task]; mt != nil {
		return mt.state
	}
	return ""
}

// phase is obor status' phase of task ("": not listed).
func (h *harness) phase(task string) string {
	for _, ts := range h.a.Status().Project.Tasks {
		if ts.Task == task {
			return ts.Phase
		}
	}
	return ""
}

// gatedScratch is schedScratch whose Publish reports its task on began, then waits for release (nil: no wait) before
// it moves the head.
type gatedScratch struct {
	*schedScratch
	git     *reviewGitFake
	began   chan string
	release chan struct{}
}

func (s *gatedScratch) Publish(_ context.Context, c port.Candidate) (port.PublishOutcome, error) {
	s.began <- c.Task
	if s.release != nil {
		<-s.release
	}
	s.git.mu.Lock()
	s.git.head = c.SHA
	s.git.mu.Unlock()
	return port.PublishOutcome{Kind: port.PublishMerged}, nil
}

// admitRig is seedRoles' project (T-1 accepted, candidate candidate1; reviewer pj-rev and deep dive pj-deep-dive idle)
// with reviews.dir, RV-5 open (its file written in the main checkout), its review git (head base1) and a gated publish.
type admitRig struct {
	h      *harness
	git    *reviewGitFake
	s      *gatedScratch
	agents *agentsSpy
	fx     *Loop
}

func newAdmitRig(t *testing.T, gated bool) *admitRig {
	t.Helper()
	h, f := seedRoles(t)
	h.write(filepath.Join(h.root, ".obor", "config.json"), admitCfg)
	if err := os.MkdirAll(filepath.Join(h.root, "reviews"), 0o755); err != nil {
		t.Fatal(err)
	}
	h.write(filepath.Join(h.root, "reviews", "RV-5.md"), "# RV-5\n")
	r := &admitRig{h: h, git: &reviewGitFake{head: "base1"}, agents: &agentsSpy{}}
	r.s = &gatedScratch{schedScratch: f, git: r.git, began: make(chan string, 4)}
	if gated {
		r.s.release = make(chan struct{})
	}
	r.fx = &Loop{Workflow: WorkflowOf(r.s, r.git), Agents: r.agents, Prompter: f}
	h.a.opt.Loop = func(string) (*Loop, error) { return r.fx, nil }
	return r
}

// publishing is one pass whose publish of T-1 has begun and waits for its release.
func (r *admitRig) publishing(ctx context.Context) {
	r.h.t.Helper()
	r.h.a.advance(ctx)
	select {
	case task := <-r.s.began:
		if task != "T-1" {
			r.h.t.Fatalf("precondition: the publish of %s began, want T-1", task)
		}
	case <-time.After(5 * time.Second):
		r.h.t.Fatal("precondition: T-1's publish did not begin")
	}
}

// released ends the gated publish and settles the loop.
func (r *admitRig) released(ctx context.Context) {
	close(r.s.release)
	r.h.a.effects.Wait()
	r.h.pass(ctx)
}

// waiting: done has not answered within a short wait (the call waits for the publish).
func waiting[T any](done chan T) bool {
	select {
	case <-done:
		return false
	case <-time.After(200 * time.Millisecond):
		return true
	}
}

// selectPublish is the loop's step for T-1 (accepted, its publish due), selected under mu as drive does; it is run
// later, after the caller's admission.
func (r *admitRig) selectPublish() step {
	r.h.t.Helper()
	a := r.h.a
	a.mu.Lock()
	s := a.next(a.p, a.p.folded.loop["T-1"])
	a.mu.Unlock()
	if s == nil {
		r.h.t.Fatal("precondition: no step selected for T-1")
	}
	return s
}

// answerSeed is the harness with T-1 dispatched in lane L-01 (the lane adapters faked), its loop state set to state
// and its attempt's job to job, then the needs-you of cause recorded (a watchdog's when watch: no merge-loop fact). It
// returns the fact in force.
func answerSeed(t *testing.T, cause, state, job string, watch bool) (*harness, *proj, event.Record) {
	t.Helper()
	h, _ := newIdentHarness(t)
	h.withLead()
	f := newConcFakes("")
	fx := &Loop{Workflow: WorkflowOf(f), Specs: f, Agents: f}
	h.a.opt.Loop = func(string) (*Loop, error) { return fx, nil }
	h.connect()
	p := h.proj()
	t.Cleanup(func() { h.proj().log.Close(); h.a.closeLog() })
	addRecord(t, h, "T-1", event.StepDispatched, event.KindFact, dispatchData{Lane: "L-01", Dir: h.lane("L-01"), Agent: "pj-l-01",
		Branch: "task/T-1", Target: "b"})
	addRecord(t, h, "T-1", event.StepPrepare, event.KindResult, prepareOut{Base: "b"})
	p.setLanes(h.lanes.lanes)
	h.a.reconcile(p)
	mt := p.folded.loop["T-1"]
	if watch {
		h.a.record(p, event.Record{Task: "T-1", Step: event.StepNeedsYou, Kind: event.KindFact}, event.FactData{Lane: "L-01", Reason: cause})
		return h, p, p.folded.active["T-1"][0]
	}
	mt.state, mt.job = state, job
	if state == stWaiting { // a waiting task holds no lane yet
		mt.lane = ""
	}
	switch cause {
	case causeGate, causePublish, causeSendbacks, causeResolveRejected: // its phase's work failed: a due block first
		mt.block(block{cause: cause, why: cause + ": probe"})
	}
	if !h.a.needsYou(p, mt, cause, cause+": probe") || !mt.needs.held() {
		t.Fatalf("precondition: %s's needs-you not in force", cause)
	}
	return h, p, mt.needs.fact
}

// The answer to an idle worker reaches it, a line that cannot go out says why, and obor
// resume takes a stalled worker's needs-you.

// staleHerdr is the ident harness with pj-l-01 idle in w1:p1, where herdr reports session s-herdr (stale: launch's
// start row carries it too) while the session its SessionStart hook reported from w1:p1, and its inbox record, is
// s-live; then the lead's answer to T-1's worker recorded (fact answered).
func staleHerdr(t *testing.T) *harness {
	t.Helper()
	h, _ := newIdentHarness(t)
	h.pane("w1:p1").Session = "s-herdr"
	h.started("pj-l-01", "w1:p1", "t1", "s-herdr")
	h.connect()
	h.session("pj-l-01", "s-live", "/tr/s-live.jsonl", 100)
	h.a.opt.Inboxed = func(string) (string, bool) { return "s-live", true }
	if w := h.where("pj-l-01"); !strings.HasPrefix(w, "identified w1:p1 ") {
		t.Fatalf("precondition: pj-l-01 is %q, want identified at w1:p1", w)
	}
	h.a.mu.Lock()
	s, _ := h.proj().session("pj-l-01")
	h.a.mu.Unlock()
	if s.ID != "s-live" || h.pane("w1:p1").Session != "s-herdr" || h.pane("w1:p1").Status != "idle" {
		t.Fatalf("precondition: recorded %q, herdr's %q, status %q; want s-live, s-herdr, idle", s.ID,
			h.pane("w1:p1").Session, h.pane("w1:p1").Status)
	}
	if !h.a.record(h.proj(), event.Record{Task: event.MsgTask, Step: event.StepAnswered, Kind: event.KindFact},
		event.Message{To: "pj-l-01", From: "pj-lead", Line: "[obor answer] move the helpers down", Task: "T-1"}) {
		t.Fatal("answer not recorded")
	}
	return h
}

// busyAnswered is staleHerdr with pj-l-01 working: one pass holds the queued answer (precondition).
func busyAnswered(t *testing.T) *harness {
	t.Helper()
	h := staleHerdr(t)
	h.status("w1:p1", func(p *port.Pane) { p.Status = "working" })
	in := &kindSpy{}
	h.a.deliver(context.Background(), h.proj(), &Loop{Inbox: in, Prompter: &linerSpy{}})
	if len(in.calls) != 0 || len(h.lines()) != 1 || len(h.a.Status().Project.Messages) != 1 {
		t.Fatalf("precondition: posted %q, queued %q; want the answer waiting for the busy worker", in.calls, h.lines())
	}
	return h
}

// askSeed is a project whose task T-1 is dispatched to lane L-01, its worker identified in pane w1:p1.
func askSeed(t *testing.T) (*harness, *proj) {
	t.Helper()
	h, _ := newIdentHarness(t)
	h.withLead()
	h.a.opt.Loop = func(string) (*Loop, error) { return &Loop{}, nil }
	h.connect()
	p := h.proj()
	t.Cleanup(func() { h.proj().log.Close(); h.a.closeLog() })
	addRecord(t, h, "T-1", event.StepDispatched, event.KindFact, dispatchData{Lane: "L-01", Dir: h.lane("L-01"), Agent: "pj-l-01", Branch: "task/T-1", Target: "b"})
	addRecord(t, h, "T-1", event.StepPrepare, event.KindResult, prepareOut{Base: "b"})
	p.setLanes(h.lanes.lanes)
	h.a.reconcile(p)
	return h, p
}

// closeTask ends task of project name as c says: merged or dropped (a fact), or rejected (the integrator's verdict on
// its open review job; T-4 of pr has one). Live: as the app does (record, verdict); replay: appended and folded only,
// so the next load settles it.
func (r *concRig) closeTask(name, task, end string, live bool) {
	r.t.Helper()
	a, p := r.app(name), r.proj(name)
	switch {
	case end != stRejected && live:
		if !a.record(p, event.Record{Task: task, Attempt: 1, Step: end, Kind: event.KindFact}, nil) {
			r.t.Fatal("the terminal fact was not recorded")
		}
	case end != stRejected:
		r.add(name, task, end, event.KindFact, nil)
	case live:
		if _, err := a.verdict(context.Background(), wire.VerdictReq{Envelope: wire.Envelope{Root: p.root,
			Agent: p.integrator, Role: "integrator", CallID: "rj"}, Verb: wire.MethodReject, Task: task,
			Job: task + "/1/review", Patch: "p", Note: "why"}); err != nil {
			r.t.Fatalf("reject: %v", err)
		}
	default:
		step := verdictStep(wire.MethodReject, "rj")
		r.add(name, task, step, event.KindIntent, verdictData{Job: task + "/1/review", Caller: p.integrator, Patch: "p", Note: "why"})
		r.add(name, task, step, event.KindResult, verdictResult{Job: task + "/1/review", Patch: "p", Note: "why"})
	}
	if got := r.state(name, task); got != end {
		r.t.Fatalf("%s is %s, want %s", task, got, end)
	}
}

// reload ends name's serve and loads its log again (the replay), the new app connected.
func (r *concRig) reload(name string) (*App, *proj) {
	r.t.Helper()
	a := r.app(name)
	a.closeLog()
	b := newTestApp(Options{Now: a.opt.Now, Loop: a.opt.Loop}, &fakePanes{}, &fakeLanes{})
	p, err := b.load(r.roots[name])
	if err != nil {
		r.t.Fatal(err)
	}
	b.p, b.connected = p, true
	r.apps[name] = b
	return b, p
}

// A gate runs in its own goroutine holding its project's scratch; the
// merge loop goes on with every other step meanwhile. The fakes below are safe for the loop goroutine, the gate's
// goroutine and the test at once.

// concFakes is one project's adapters: its Scratch the package's one fake (FakeWorkflow: gates green, a held gate,
// publish, resolved check or lane test waits for its release), its Agents, Prompter, LaneSetup and Specs.
type concFakes struct {
	*FakeWorkflow
	restarts int // under the FakeWorkflow's mu
	// obor drop by lane: the stops (agent, force) and Free inputs; refuse: Free's refusal ("": frees).
	stops  []string
	frees  []port.FreeInput
	refuse string
	// ignoresEsc: the worker keeps working after Escape, as launch.Stop sees it: a stop without force fails.
	ignoresEsc bool
	// stopFails: a stop's error by force ("" : none): a quit timeout, a forced stop that fails.
	stopFails func(force bool) string
	// freeErrs: how many Free calls fail first, as a cut git does (retried, never a half-done drop).
	freeErrs int
}

// newConcFakes is a project's adapters whose gate of hold waits for release; the target is at "b", and a candidate
// has no Base unless the test says so (unbased false: a lane test follows its gate).
func newConcFakes(hold string) *concFakes {
	s := NewFakeWorkflow("b")
	s.hold, s.release, s.entered, s.unbased, s.Checks = hold, make(chan struct{}), make(chan string, 16), true, "check 1 pass"
	return &concFakes{FakeWorkflow: s}
}

func (f *concFakes) Launch(context.Context, port.Launch) (port.Started, error) {
	f.mu.Lock()
	f.restarts++
	f.mu.Unlock()
	return port.Started{Line: "STARTED"}, nil
}

func (f *concFakes) Stop(_ context.Context, agent, _ string, force bool) (string, error) {
	f.mu.Lock()
	f.stops = append(f.stops, fmt.Sprintf("%s force=%t", agent, force))
	ignores, fails := f.ignoresEsc, f.stopFails
	f.mu.Unlock()
	if fails != nil && fails(force) != "" {
		return "", errors.New(fails(force))
	}
	if ignores && !force {
		return "", needsForceErr(agent + " still working 30s after Escape (--force stops it)")
	}
	return "STOPPED", nil
}

func (f *concFakes) Prompt(context.Context, string, string, bool) (bool, error) {
	return true, nil
}

func (f *concFakes) Prepare(context.Context, port.PrepareInput) (port.Prepared, error) {
	return port.Prepared{Base: "target"}, nil
}

func (f *concFakes) Free(_ context.Context, in port.FreeInput) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.refuse != "" {
		return "", port.Refused{Line: f.refuse}
	}
	if f.freeErrs > 0 {
		f.freeErrs--
		return "", errors.New("git branch -D task/T-2: signal: terminated — run the same obor drop again to finish it")
	}
	f.frees = append(f.frees, in)
	return "discarded in " + in.Lane, nil
}

func (f *concFakes) Check(context.Context, string, string) (string, error) { return "", nil }

func (f *concFakes) Target(context.Context, string) (string, bool, error) {
	return "target", false, nil
}

// concRig is three projects, each on its own app (obor serve serves one project): pj (workspace w1; T-1
// submitted and held in its gate, T-5 submitted behind it, T-2 dispatched onto lane L-02; the dispatch switch on), pq
// (w2; T-3 submitted) and pr (w3; T-4 green, its review job out). Each workspace runs its integrator; L-02's pane runs
// pj's worker. r.a is pj's app.
type concRig struct {
	t      *testing.T
	a      *App
	apps   map[string]*App   // project → its app
	roots  map[string]string // project → root
	fakes  map[string]*concFakes
	kit    string
	cancel context.CancelFunc
	done   chan struct{}
}

func newConcRig(t *testing.T) *concRig {
	base := t.TempDir()
	kit, _ := filepath.Abs("../..")
	r := &concRig{t: t, apps: map[string]*App{}, roots: map[string]string{}, fakes: map[string]*concFakes{}, kit: kit}
	now := time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC)
	lane := filepath.Join(base, "pj-wt", "L-02")
	for _, name := range []string{"pj", "pq", "pr"} {
		root := filepath.Join(base, name)
		if err := os.MkdirAll(filepath.Join(root, ".obor"), 0o755); err != nil {
			t.Fatal(err)
		}
		cfg := `{"version":2,"project":{"name":"` + name + `","agent_prefix":"` + name + `"},"agents":{"roles":{"integrator":""}}}`
		if err := os.WriteFile(filepath.Join(root, ".obor", "config.json"), []byte(cfg), 0o644); err != nil {
			t.Fatal(err)
		}
		r.roots[name] = root
		f := newConcFakes("")
		r.fakes[name] = f
		fx := &Loop{Workflow: WorkflowOf(f), Agents: f, Prompter: f, Kit: r.kit}
		if name == "pj" {
			fx.Specs = f
		}
		a := newTestApp(Options{Root: root, Now: func() time.Time { return now }}, &fakePanes{}, &fakeLanes{})
		a.opt.Loop = func(string) (*Loop, error) { return fx, nil }
		loadRoot(t, a)
		r.apps[name] = a
	}
	r.a = r.apps["pj"]
	r.fakes["pj"].hold = "T-1"
	for _, a := range r.apps { // one herdr session: each app watches its own workspace
		a.connect(context.Background(), port.Snapshot{Panes: []port.Pane{
			{ID: "w1:p9", Group: "pj", Cwd: r.roots["pj"], Agent: "claude", Label: "pj-int", Status: "idle"},
			{ID: "w2:p9", Group: "pq", Cwd: r.roots["pq"], Agent: "claude", Label: "pq-int", Status: "idle"},
			{ID: "w3:p9", Group: "pr", Cwd: r.roots["pr"], Agent: "claude", Label: "pr-int", Status: "idle"},
			{ID: "w1:p2", Group: "pj", Cwd: lane, Agent: "claude", Label: "pj-l-02", Status: "idle", Session: "s"},
		}})
	}

	r.add("pj", "T-1", event.StepSubmitted, event.KindFact, submitData{Lane: "L-01"})
	r.add("pj", "T-1", event.StepSubmit, event.KindResult, submitOut{SHA: "s1"})
	r.add("pj", "T-5", event.StepSubmitted, event.KindFact, submitData{Lane: "L-05"})
	r.add("pj", "T-5", event.StepSubmit, event.KindResult, submitOut{SHA: "s5"})
	r.add("pq", "T-3", event.StepSubmitted, event.KindFact, submitData{Lane: "L-03"})
	r.add("pq", "T-3", event.StepSubmit, event.KindResult, submitOut{SHA: "s3"})
	r.add("pr", "T-4", event.StepSubmitted, event.KindFact, submitData{Lane: "L-04"})
	r.add("pr", "T-4", event.StepSubmit, event.KindResult, submitOut{SHA: "s4"})
	r.add("pr", "T-4", event.StepGate, event.KindIntent, gateIn{Kind: port.InputLane, Lane: "L-04", Input: "s4"})
	r.add("pr", "T-4", event.StepGate, event.KindResult, gateOut{Outcome: outGreen, Candidate: port.Candidate{Task: "T-4", SHA: "c-T-4",
		Target: "b", Patch: "p"}})
	r.add("pr", "T-4", event.StepReview, event.KindIntent, promptData{Agent: "pr-int", Job: "T-4/1/review", Patch: "p"})
	r.add("pr", "T-4", event.StepReview, event.KindResult, "sent")
	return r
}

// loadRoot loads a's Options.Root as loadProject does, failing the test on an error.
func loadRoot(t *testing.T, a *App) {
	t.Helper()
	p, err := a.load(a.opt.Root)
	if err != nil {
		t.Fatal(err)
	}
	a.mu.Lock()
	a.p = p
	a.reconcile(p)
	a.mu.Unlock()
}

// app is project name's app.
func (r *concRig) app(name string) *App { return r.apps[name] }

// status is name's ProjectStatus.
func (r *concRig) status(name string) wire.ProjectStatus {
	if ps := r.app(name).Status().Project; ps != nil && ps.Name == name {
		return *ps
	}
	r.t.Fatalf("no status of %s", name)
	return wire.ProjectStatus{}
}

// add appends a record to project name's log and folds it.
func (r *concRig) add(name, task, step, kind string, d any) {
	r.t.Helper()
	a, p := r.app(name), r.proj(name)
	raw, _ := json.Marshal(d)
	rec, err := p.log.Append(event.Record{Task: task, Attempt: 1, Step: step, Kind: kind, Data: raw}, a.opt.Now())
	if err != nil {
		r.t.Fatal(err)
	}
	a.mu.Lock()
	p.foldRecord(rec)
	a.mu.Unlock()
}

func (r *concRig) proj(name string) *proj {
	a := r.app(name)
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.p
}

// run starts each app's merge loop goroutine, kicked at once.
func (r *concRig) run() {
	ctx, cancel := context.WithCancel(context.Background())
	r.cancel, r.done = cancel, make(chan struct{})
	var wg sync.WaitGroup
	for _, a := range r.apps {
		wg.Add(1)
		go func() {
			defer wg.Done()
			a.mergeLoop(ctx)
		}()
		a.wake()
	}
	go func() {
		wg.Wait()
		close(r.done)
	}()
}

// wake kicks each app's merge loop.
func (r *concRig) wake() {
	for _, a := range r.apps {
		a.wake()
	}
}

// stop ends the merge loops and waits for them (and the scratch effects it waits for).
func (r *concRig) stop() {
	r.cancel()
	select {
	case <-r.done:
	case <-time.After(10 * time.Second):
		r.t.Fatal("the merge loop did not stop")
	}
}

// count is how many records of name's log for task have kind and step.
func (r *concRig) count(name, task, kind, step string) int {
	recs, err := event.ReadLog(r.roots[name])
	if err != nil {
		r.t.Fatal(err)
	}
	n := 0
	for _, rec := range recs {
		if rec.Task == task && rec.Kind == kind && rec.Step == step {
			n++
		}
	}
	return n
}

// waitFor polls cond (kicking the loop) until it holds.
func (r *concRig) waitFor(what string, cond func() bool) {
	r.t.Helper()
	for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); time.Sleep(5 * time.Millisecond) {
		if cond() {
			return
		}
		r.wake()
	}
	r.t.Fatalf("timed out waiting for %s", what)
}

// entered waits for name's next Gate call: its task.
func (r *concRig) entered(name string) string {
	r.t.Helper()
	select {
	case task := <-r.fakes[name].entered:
		return task
	case <-time.After(10 * time.Second):
		r.t.Fatalf("%s: no gate began", name)
		return ""
	}
}

func (r *concRig) state(name, task string) string {
	a, p := r.app(name), r.proj(name)
	a.mu.Lock()
	defer a.mu.Unlock()
	return p.folded.loop[task].shown()
}

func (r *concRig) running(name, task string) string {
	for _, ts := range r.status(name).Tasks {
		if ts.Task == task {
			return ts.Running
		}
	}
	return ""
}

func (f *concFakes) loopWithSpecs(kit string) *Loop {
	return &Loop{Workflow: WorkflowOf(f), Agents: f, Prompter: f, Specs: f, Kit: kit}
}

// needsForceErr is launch's refusal of a stop without force that only force gets past (port.NeedsForce).
type needsForceErr string

func (e needsForceErr) Error() string  { return string(e) }
func (needsForceErr) NeedsForce() bool { return true }

// set is the owner's config_set of key to val (JSON) from the web.
func (h *harness) set(callID, key, val string) (string, error) {
	return h.a.configCall(wire.ConfigSetReq{Envelope: wire.Envelope{Root: h.root, CallID: callID}, Changes: changes(key,
		val), Source: "web"})
}

// changes is key, value pairs (the value JSON) as a config_set call's changes.
func changes(kv ...string) []wire.ConfigChange {
	var out []wire.ConfigChange
	for i := 0; i+1 < len(kv); i += 2 {
		out = append(out, wire.ConfigChange{Key: kv[i], Value: json.RawMessage(kv[i+1])})
	}
	return out
}

// leadLines is the lines to the lead in the log.
func leadLines(t *testing.T, h *harness) []string {
	t.Helper()
	var out []string
	for _, r := range readLog(t, h.root) {
		var m event.Message
		if r.Step == event.StepMessage && json.Unmarshal(r.Data, &m) == nil && m.To == "pj-lead" {
			out = append(out, m.Line)
		}
	}
	return out
}

// callReply is a call's answer, for a call run in its own goroutine.
type callReply struct {
	reply string
	err   error
}

// refuseLogs makes every event-log append of name's project fail with err; nil heals it.
func (r *concRig) refuseLogs(name string, err error) {
	p := r.proj(name)
	p.log.Mu.Lock()
	defer p.log.Mu.Unlock()
	if err == nil {
		p.log.Refuse = nil
		return
	}
	p.log.Refuse = func(event.Record) error { return err }
}

// concLoop is name's app's Loop (the rig's one fake for it).
func (r *concRig) concLoop(name string) *Loop {
	a, p := r.app(name), r.proj(name)
	fx, err := a.opt.Loop(p.root)
	if err != nil {
		r.t.Fatal(err)
	}
	return fx
}

// addNightly appends a nightly record (its key: attempt 0) at at and folds it.
func (r *concRig) addNightly(step, kind string, d any, at time.Time) {
	r.t.Helper()
	a, p := r.app("pj"), r.proj("pj")
	raw, _ := json.Marshal(d)
	rec, err := p.log.Append(event.Record{Task: event.NightlyTask, Step: step, Kind: kind, Data: raw}, at)
	if err != nil {
		r.t.Fatal(err)
	}
	a.mu.Lock()
	p.foldRecord(rec)
	a.mu.Unlock()
}

// runStep runs name's task's next production step once (App.next: begin, the step, release) and waits for the effects
// it started. A gate step always returns false: it waits, whether it is held or its gate runs in its own goroutine.
func (r *concRig) runStep(name, task string) {
	r.t.Helper()
	a := r.app(name)
	fx := r.concLoop(name)
	a.mu.Lock()
	p := a.p
	st := a.next(p, p.folded.loop[task])
	a.mu.Unlock()
	if st == nil {
		r.t.Fatalf("%s: no step for %s: state %s", name, task, r.state(name, task))
	}
	st(context.Background(), fx)
	a.effects.Wait()
}

// diskFactsInForce is name's project's low-disk facts in force.
func (r *concRig) diskFactsInForce(name string) []event.Record {
	a, p := r.app(name), r.proj(name)
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]event.Record(nil), p.folded.active[event.DiskTask]...)
}

// dropCall is the lead's obor drop of task in project name.
func (r *concRig) dropCall(name, task, id string) (string, error) {
	return r.app(name).drop(wire.DropReq{Envelope: wire.Envelope{Root: r.roots[name], Agent: "lead", Role: "lead",
		CallID: id}, Task: task, Why: "abandoned"})
}

func (r *concRig) entered2(name string) string { return r.entered(name) }

// row is the task L-02's row holds.
func (r *concRig) row(p *proj) string {
	r.a.mu.Lock()
	defer r.a.mu.Unlock()
	return p.laneTask("L-02")
}

// held is pj's T-2 needs-you reason in force ("": none).
func (r *concRig) heldWhy(p *proj, task string) string {
	r.a.mu.Lock()
	defer r.a.mu.Unlock()
	if mt := p.folded.loop[task]; mt != nil && mt.needs.held() {
		return mt.needs.fact.Fact().Reason
	}
	return ""
}

// A dropped fact is terminal for the task whatever attempt it names; any record of the task
// after it is ignored, and only a new dispatch reopens it.

// addAt is add at attempt n.
func (r *concRig) addAt(name, task string, n int, step, kind string, d any) {
	r.t.Helper()
	a, p := r.app(name), r.proj(name)
	raw, _ := json.Marshal(d)
	rec, err := p.log.Append(event.Record{Task: task, Attempt: n, Step: step, Kind: kind, Data: raw}, a.opt.Now())
	if err != nil {
		r.t.Fatal(err)
	}
	a.mu.Lock()
	p.foldRecord(rec)
	a.mu.Unlock()
}

// worker puts an idle claude pane labelled agent into proj's group and reconciles its identity table.
func (r *concRig) worker(proj, id, agent string) {
	a, p := r.app(proj), r.proj(proj)
	a.mu.Lock()
	defer a.mu.Unlock()
	a.obs[id] = &seen{pane: port.Pane{ID: id, Group: proj, Cwd: r.roots[proj], Agent: "claude", Label: agent,
		Status: "idle"}}
	a.reconcile(p)
}

// dispatchT2 records pj's T-2 dispatch onto lane L-02 (its worker's pane is in the rig).
func dispatchT2(r *concRig) {
	r.add("pj", "T-2", event.StepDispatched, event.KindFact, dispatchData{Lane: "L-02", Dir: filepath.Join(filepath.Dir(r.roots["pj"]),
		"pj-wt", "L-02"), Agent: "pj-l-02", Target: "target", Branch: "task/T-2"})
	p := r.proj("pj")
	r.a.mu.Lock()
	p.dispatching = true
	r.a.mu.Unlock()
}

// callAnswer is a call's reply.
type callAnswer struct {
	reply string
	err   error
}

// dropWithin is the lead's drop of task, which must answer within a second: drop never waits.
func (r *concRig) dropWithin(name, task, id string) callAnswer {
	r.t.Helper()
	got := make(chan callAnswer, 1)
	go func() {
		reply, err := r.dropCall(name, task, id)
		got <- callAnswer{reply, err}
	}()
	select {
	case a := <-got:
		return a
	case <-time.After(time.Second):
		r.t.Fatalf("drop %s did not answer within a second", task)
		return callAnswer{}
	}
}

// ended is whether a drop's mark on task of project name is set.
func (r *concRig) ended(name, task string) bool {
	a, p := r.app(name), r.proj(name)
	a.mu.Lock()
	defer a.mu.Unlock()
	return p.ended[task]
}

// pauseDrop makes project name's log hold the dropped fact's write until the returned release is called; writing is
// closed when it starts.
func (r *concRig) pauseDrop(name string) (writing chan struct{}, release func()) {
	p := r.proj(name)
	writing, gate := make(chan struct{}), make(chan struct{})
	p.log.Refuse = func(rec event.Record) error {
		if rec.Step == event.StepDropped {
			close(writing)
			<-gate
		}
		return nil
	}
	return writing, func() { close(gate) }
}

// StartIntegrator is startIntegrator for the external tests.
var StartIntegrator = (*App).startIntegrator

// JobTimer is the timer of task's integrator job in a's project at root: its job id ("": none), since when the
// integrator is seen gone (zero: it runs) and the quiet time's baseline (zero: none).
func JobTimer(a *App, root, task string) (job string, gone, quiet time.Time) {
	a.mu.Lock()
	defer a.mu.Unlock()
	t := a.projAt(root).timers[task]
	if t == nil {
		return "", time.Time{}, time.Time{}
	}
	return t.job, t.gone, t.quietSince
}

// Settled: no step of task runs in a's project at root (none held, no gate or publish effect of it in flight).
func Settled(a *App, root, task string) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	p := a.projAt(root)
	return p == nil || p.held[task] == "" && p.running.Task != task
}

// Refuse makes the event log of a's project at root refuse each append refuse returns an error for (crash tests: an
// effect ran, its result never reached the log).
func Refuse(a *App, root string, refuse func(event.Record) error) {
	a.mu.Lock()
	p := a.projAt(root)
	a.mu.Unlock()
	p.log.Mu.Lock()
	p.log.Refuse = refuse
	p.log.Mu.Unlock()
}

// Quiet is the stall timer of task's watch in a's project at root: its baseline (zero: none) and the newest
// transcript write accounted for (zero: not sampled yet).
func Quiet(a *App, root, task string) (quietSince, seenWrite time.Time) {
	a.mu.Lock()
	defer a.mu.Unlock()
	p := a.projAt(root)
	if p == nil || p.tasks[task] == nil {
		return time.Time{}, time.Time{}
	}
	return p.tasks[task].quietSince, p.tasks[task].seenWrite
}

// InstanceOf is snap's terminal instance as serve records it in InstanceFile (restore.go).
func InstanceOf(snap port.Snapshot) string { return snap.Instance }

// DB is the database of a's project at root, its one writer's connection (nil: a has not loaded the project): the
// rig reads and writes the store through it while a serves the project.
func DB(a *App, root string) *sql.DB {
	a.mu.Lock()
	defer a.mu.Unlock()
	if p := a.projAt(root); p != nil {
		return p.log.DB()
	}
	return nil
}

// Counts is how many full checks and merge loop passes a has done.
func Counts(a *App) (checks, passes int) {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.checks, a.passes
}

// StreamPasses is how many merge loop passes a has done connected to one herdr stream from start to end:
// the passes a test that moved the clock or changed herdr waits on.
func StreamPasses(a *App) int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.onStream
}

// Follows is how many binary-watch ticks a has counted (Options.Follow).
func Follows(a *App) int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.follows
}

// Admits is the admissions in force on a's project at root (p.admits).
func Admits(a *App, root string) int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.projAt(root).admits
}

// Admit is one admission in force on a's project at root, as a config_set's or a review close's call holds it until
// its commit is in; release ends it. The project must be served and no publish running.
func Admit(a *App, root string) (release func(), err error) {
	a.mu.Lock()
	p := a.projAt(root)
	a.mu.Unlock()
	if p == nil {
		return nil, errors.New("no served project at " + root)
	}
	return a.admit(context.Background(), p)
}

// WriteLog appends recs (stamped now) to the event log of root, whose serve is stopped (a crash left them).
func WriteLog(root string, now time.Time, recs ...event.Record) error {
	db, err := store.OpenRW(event.DBFile(root))
	if err != nil {
		return err
	}
	defer db.Close()
	log, _, err := event.OpenLog(db)
	for _, r := range recs {
		if err != nil {
			break
		}
		_, err = log.Append(r, now)
	}
	return err
}

// DiskEvery is how often the free space is read on a pass (disk.go).
const DiskEvery = diskEvery

// IsIdentified is whether serve identifies agent in a's project at root (whereIs: a running agent it can address).
func IsIdentified(a *App, root, agent string) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	p := a.projAt(root)
	return p != nil && a.whereIs(p, agent).State == Identified
}

// WhereOf is whereIs(agent) in a's project at root as a line: "identified <pane> <source>", "ambiguous <panes>",
// "unavailable <why>".
func WhereOf(a *App, root, agent string) string {
	a.mu.Lock()
	defer a.mu.Unlock()
	p := a.projAt(root)
	if p == nil {
		return "no project"
	}
	w := a.whereIs(p, agent)
	switch w.State {
	case Identified:
		return "identified " + w.Pane.ID + " " + w.Source
	case Ambiguous:
		return "ambiguous " + strings.Join(w.Panes, " ")
	}
	return "unavailable " + w.Why
}

// WatchedPane is the pane the watchdog last saw task's agent in ("": none yet, or not watched).
func WatchedPane(a *App, root, task string) string {
	a.mu.Lock()
	defer a.mu.Unlock()
	p := a.projAt(root)
	if p == nil || p.tasks[task] == nil {
		return ""
	}
	return p.tasks[task].agent.ID
}

// WorkerStartSettle is how long the worker start rule waits on a worker's pane at its shell.
const WorkerStartSettle = workerStartSettle

// CachedLane is lane name as a's lane table last scanned it in the project at root (ok false: not in it).
func CachedLane(a *App, root, name string) (l port.Lane, ok bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	p := a.projAt(root)
	if p == nil {
		return port.Lane{}, false
	}
	for _, x := range p.scan.ls {
		if x.Name == name {
			return x, true
		}
	}
	return port.Lane{}, false
}

// NeedsSeq is the needs-you seq a needs rule line's did key names (0: another line).
func NeedsSeq(of event.Of) int64 {
	if of.Kind != ruleNeeds {
		return 0
	}
	return of.Seq / needsSpan
}

// ReplayLanes loads the project at root as serve does on start, with no herdr and no lanes source, and closes it: its
// log's replay writes the lanes table, the rows the lane table and dispatch read.
func ReplayLanes(root string, now time.Time) {
	a := newTestApp(Options{Root: root, Now: func() time.Time { return now }}, &fakePanes{}, &fakeLanes{})
	a.loadProject()
	a.closeLog()
}

// PickLane is pickLane for the external tests: the lane a dispatch takes of ls, its model ref, why none.
var PickLane = pickLane

// FakeWorkflow is the package's one port.Workflow for tests (as FakeScratch): the loop rig's (app_test)
// and the concurrency rig's, and the in-memory row of the workflow contract table (testkit.WorkflowContract). It runs
// no git and no command: shas are made up, the target is a head the test moves (Move, Land), pins and lane commits are
// maps. It keeps port.Workflow's contract (port/effects.go, port/workflow.go): an outcome is final for its step, an
// error is "not decided". Outcomes are scripted per call and task (Script); unscripted, a gate is green, a lane test
// green and a publish merges while the target is still the candidate's. The git behaviour behind each outcome (a real
// conflict, a moved merge, a held fast-forward) is internal/checks' to test, over real git.
//
// It knows only what it is told: a lane it was given (AddLane, Prepare) has a committed snapshot of its dir, so Submit
// commits what changed there and Test refuses it dirty; an unknown lane, sha or resolution is taken as it comes (the
// concurrency rig's records name lanes and shas the fake never saw). Its review git has the one target (Head of any
// branch is its head) and records the role worktrees it detached and the closes it committed. A test's own fake of one part is combined with it by WorkflowOf.
type FakeWorkflow struct {
	Dir    string // serve's worktree, the one a conflict names (nothing is written there)
	Checks string // a green lane test's checks summary ("": "rc=0", merge.check ran)

	Submits, Gates, Tests, Publishes, AfterMerges atomic.Int32 // calls, a failed one too

	mu         sync.Mutex
	head       string                // the target's head
	history    map[string]bool       // every sha the target held (a published candidate's is "already")
	commits    map[string]fakeCommit // what the fake made: lane commits, merges, resolutions
	lanes      map[string]*fakeLane
	pins       map[string]string // "submitted/<task>/<id>", "candidate/<task>", "resolved/<task>" → sha
	landed     map[string]bool   // the target holds merge evidence of the task
	merges     []string          // the tasks merged onto the target, in order (a publish, or Land)
	published  map[string]port.Candidate
	script     map[string]string // "<call> <task>" → its outcome (Script)
	fail       map[string]func(ctx context.Context) error
	onTest     func(ctx context.Context, dir string)
	inputs     []port.GateInput
	subjects   []string // each Submit's lane commit subject ("<task>: <summary>"), a pinned one too
	afterRuns  map[string]int
	detached   map[string]string   // a role worktree's dir → the sha Detach put it on
	closes     []port.ReviewCommit // the review closes committed, in order
	open       *fakeCommit         // the conflicting merge left open in Dir (nil: none)
	resolution string              // the merge the integrator committed in Dir ("": none)
	n          int

	// The concurrency rig's: a gate of hold, a publish of holdPub, a resolved check of holdResolved block until
	// release is closed (or their ctx ends), holdTest's lane test until testRelease is (release when nil); entered gets
	// each Gate's task as it begins ("publish <task>", "resolved <task>", "test <task>" when held; nil: not sent).
	// Every scratch call (Gate, Publish, Resolved) counts in inflight, max the most at once. unbased: a candidate has no
	// Base (green at once, no lane test).
	hold, holdPub, holdResolved, holdTest string
	unbased                               bool
	release, testRelease                  chan struct{}
	entered                               chan string
	gates, tests, pubs                    map[string]int
	inflight, max                         int
}

// fakeCommit is a commit the fake made: a lane's (parents: the lane's previous head; own: the task's files against its
// lane's base) or a merge (parents: the target, then the submitted sha).
type fakeCommit struct {
	task    string
	parents []string
	base    string            // a lane commit's fork point
	own     map[string]string // a lane commit's files changed since its base, by path ("": deleted)
}

// fakeLane is a lane the fake was given: its dir, the sha its branch forked from and its head, the files its head
// holds, and the sha a lane test checked out (at; "": on its branch).
type fakeLane struct {
	dir, branch string
	base, head  string
	files       map[string]string
	at          string
}

// NewFakeWorkflow is a scratch whose target is at head ("": a made-up sha).
func NewFakeWorkflow(head string) *FakeWorkflow {
	s := &FakeWorkflow{history: map[string]bool{}, commits: map[string]fakeCommit{}, lanes: map[string]*fakeLane{},
		pins: map[string]string{}, landed: map[string]bool{}, published: map[string]port.Candidate{},
		script: map[string]string{}, fail: map[string]func(context.Context) error{}, afterRuns: map[string]int{},
		detached: map[string]string{},
		gates:    map[string]int{}, tests: map[string]int{}, pubs: map[string]int{}}
	if head == "" {
		head = s.sha("target")
	}
	s.head, s.history[head] = head, true
	return s
}

// sha is a new made-up sha (under mu, or before the fake is shared).
func (s *FakeWorkflow) sha(what string) string {
	s.n++
	sum := sha1.Sum([]byte(fmt.Sprint(what, " ", s.n)))
	return hex.EncodeToString(sum[:])
}

// laneFiles is dir's files by path, as git would track them: .claude/ (obor init's settings, excluded) and a leftover
// done file are not.
func laneFiles(dir string) map[string]string {
	files := map[string]string{}
	_ = filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		rel, _ := filepath.Rel(dir, path)
		switch {
		case d.IsDir() && (rel == ".claude" || rel == ".git"):
			return filepath.SkipDir
		case d.IsDir(), rel == ".task-done.md":
			return nil
		}
		b, err := os.ReadFile(path)
		if err == nil {
			files[rel] = string(b)
		}
		return nil
	})
	return files
}

// AddLane gives the fake lane name at dir on branch, forked from the target's head: what dir holds now is committed.
func (s *FakeWorkflow) AddLane(name, dir, branch string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.lanes[name] = &fakeLane{dir: dir, branch: branch, base: s.head, head: s.head, files: laneFiles(dir)}
}

// commit commits what changed in lane l since its head (under mu): the new head, or the old one when nothing did.
func (s *FakeWorkflow) commit(l *fakeLane, task string) string {
	now := laneFiles(l.dir)
	if maps.Equal(now, l.files) {
		return l.head
	}
	own := map[string]string{}
	if prev, ok := s.commits[l.head]; ok {
		maps.Copy(own, prev.own)
	}
	for path, text := range now {
		if l.files[path] != text {
			own[path] = text
		}
	}
	for path := range l.files {
		if _, ok := now[path]; !ok {
			own[path] = ""
		}
	}
	sha := s.sha("commit")
	s.commits[sha] = fakeCommit{task: task, parents: []string{l.head}, base: l.base, own: own}
	l.head, l.files = sha, now
	return sha
}

// Commit is the worker's own commit in lane (git add -A; git commit): its head after it.
func (s *FakeWorkflow) Commit(lane string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.commit(s.lanes[lane], "")
}

// LaneHead is lane's head sha; LaneBase the sha its branch forked from; LaneAt the sha a lane test has checked out
// there ("": the lane is on its branch).
func (s *FakeWorkflow) LaneHead(lane string) string {
	return s.laneField(lane, func(l *fakeLane) string { return l.head })
}

func (s *FakeWorkflow) LaneBase(lane string) string {
	return s.laneField(lane, func(l *fakeLane) string { return l.base })
}

func (s *FakeWorkflow) LaneAt(lane string) string {
	return s.laneField(lane, func(l *fakeLane) string { return l.at })
}

func (s *FakeWorkflow) laneField(lane string, f func(*fakeLane) string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return f(s.lanes[lane])
}

// Commits is how many lane commits Submit made for task.
func (s *FakeWorkflow) Commits(task string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for _, c := range s.commits {
		if c.task == task && c.base != "" {
			n++
		}
	}
	return n
}

// Script makes call's outcome for task outcome, for every call until scripted again ("": unscripted). call is Gate
// (any input), "Gate lane", "Gate moved" or "Gate resolved" (that input kind's: a conflict is the merge's, never a
// committed resolution's), Test, Publish (hold, pending) or AfterMerge. outcome is a kind (port.Outcome*, port.Publish*), then its
// line; a conflict's and a hold's rest is its files (default shared.txt). AfterMerge's outcome is its line.
func (s *FakeWorkflow) Script(call, task, outcome string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if outcome == "" {
		delete(s.script, call+" "+task)
		return
	}
	s.script[call+" "+task] = outcome
}

// scripted is the outcome of call (the input kind's first, for a gate): its kind and the rest.
func (s *FakeWorkflow) scripted(task string, calls ...string) (string, string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, call := range calls {
		if out, ok := s.script[call+" "+task]; ok {
			kind, rest, _ := strings.Cut(out, " ")
			return kind, rest
		}
	}
	return "", ""
}

// Arm makes method (Submit, Gate, Test, Publish, AfterMerge, Landed, Unpin, Prepare, Free, Target, Head, Detach,
// Close) give fn's error instead of acting (nil:
// heals it); fn returning nil lets the method act.
func (s *FakeWorkflow) Arm(method string, fn func(ctx context.Context) error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.fail[method] = fn
}

func (s *FakeWorkflow) failing(ctx context.Context, method string) error {
	s.mu.Lock()
	f := s.fail[method]
	s.mu.Unlock()
	if f == nil {
		return nil
	}
	return f(ctx)
}

// OnTest runs fn in each lane test, with the candidate checked out in the lane's dir.
func (s *FakeWorkflow) OnTest(fn func(ctx context.Context, dir string)) {
	s.mu.Lock()
	s.onTest = fn
	s.mu.Unlock()
}

// Move is a commit on the target outside the app (an unrelated one): its new head.
func (s *FakeWorkflow) Move() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.head = s.sha("move")
	s.history[s.head] = true
	return s.head
}

// Land is task merged onto the target outside the app (obor merge by hand, "Merge <task>: by hand"): its new head.
func (s *FakeWorkflow) Land(task string) string {
	head := s.Move()
	s.mu.Lock()
	defer s.mu.Unlock()
	s.landed[task] = true
	s.merges = append(s.merges, task)
	return head
}

// TargetHead is the target's head.
func (s *FakeWorkflow) TargetHead() string { s.mu.Lock(); defer s.mu.Unlock(); return s.head }

// Parents is sha's parents (a merge: the target, then the submitted sha; nil: a sha the fake did not make).
func (s *FakeWorkflow) Parents(sha string) []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.commits[sha].parents)
}

// Merges is how many merges of task the target holds (all tasks': "").
func (s *FakeWorkflow) Merges(task string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for _, m := range s.merges {
		if task == "" || m == task {
			n++
		}
	}
	return n
}

// Published is task's candidate the last publish merged (false: none).
func (s *FakeWorkflow) Published(task string) (port.Candidate, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	c, ok := s.published[task]
	return c, ok
}

// Open: a conflict is open in Dir, not committed yet.
func (s *FakeWorkflow) Open() bool { s.mu.Lock(); defer s.mu.Unlock(); return s.open != nil }

// Resolve is the integrator's resolution of the open conflict committed in Dir (git add; git commit): the merge of the
// conflicting gate's submitted sha onto its target. Its sha ("": no conflict was open).
func (s *FakeWorkflow) Resolve() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.open == nil {
		return ""
	}
	sha := s.sha("resolution")
	s.commits[sha] = *s.open
	s.open, s.resolution = nil, sha
	return sha
}

// Pins is every pin (sorted); Pin one ("candidate/T-1": "" none).
func (s *FakeWorkflow) Pins() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := slices.Collect(maps.Keys(s.pins))
	sort.Strings(out)
	return out
}

func (s *FakeWorkflow) Pin(ref string) string { s.mu.Lock(); defer s.mu.Unlock(); return s.pins[ref] }

// Inputs is every gate's input, in order.
func (s *FakeWorkflow) Inputs() []port.GateInput {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.inputs)
}

// Subjects is each Submit's commit subject ("<task>: <summary>"), in order.
func (s *FakeWorkflow) Subjects() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.subjects)
}

// AfterRuns is how many after-merges of task ran (an armed failure is no run).
func (s *FakeWorkflow) AfterRuns(task string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.afterRuns[task]
}

func (s *FakeWorkflow) enter() {
	s.mu.Lock()
	s.inflight++
	s.max = max(s.max, s.inflight)
	s.mu.Unlock()
}

func (s *FakeWorkflow) leave() {
	s.mu.Lock()
	s.inflight--
	s.mu.Unlock()
}

// count is m[task] under mu (m: gates, tests or pubs).
func (s *FakeWorkflow) count(m map[string]int, task string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return m[task]
}

// note sends what to entered (when set).
func (s *FakeWorkflow) note(what string) {
	if s.entered != nil {
		s.entered <- what
	}
}

// waitOn blocks until ch is closed (release when nil) or ctx ends.
func (s *FakeWorkflow) waitOn(ctx context.Context, ch chan struct{}) error {
	if ch == nil {
		ch = s.release
	}
	select {
	case <-ch:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Submit commits the lane's changes ("<task>: <summary>") and pins its head under id; a pin id has is returned as it
// is. A lane with nothing beyond its fork point is refused, as checks.Scratch refuses a branch already in the target.
// An unknown lane's sha is "submitted".
func (s *FakeWorkflow) Submit(ctx context.Context, task, id, lane, summary string) (string, error) {
	s.Submits.Add(1)
	if err := s.failing(ctx, "Submit"); err != nil {
		return "", err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.subjects = append(s.subjects, task+": "+summary)
	pin := "submitted/" + task + "/" + id
	if sha, ok := s.pins[pin]; ok {
		return sha, nil
	}
	sha := "submitted"
	if l := s.lanes[lane]; l != nil {
		if sha = s.commit(l, task); sha == l.base {
			return "", port.Refused{Line: "FAIL " + l.branch + " is already in the target"}
		}
	}
	s.pins[pin] = sha
	return sha, nil
}

// Gate merges in.Input onto the target (a resolved input is taken as committed) and pins the candidate, unless
// scripted otherwise.
func (s *FakeWorkflow) Gate(ctx context.Context, in port.GateInput) (port.GateOutcome, error) {
	s.enter()
	defer s.leave()
	s.Gates.Add(1)
	s.mu.Lock()
	s.gates[in.Task]++
	s.mu.Unlock()
	if err := s.failing(ctx, "Gate"); err != nil {
		return port.GateOutcome{}, err
	}
	s.mu.Lock()
	s.inputs = append(s.inputs, in)
	s.mu.Unlock()
	s.note(in.Task)
	if in.Task == s.hold {
		if err := s.waitOn(ctx, nil); err != nil {
			return port.GateOutcome{}, err
		}
	}
	kind, rest := s.scripted(in.Task, "Gate "+in.Kind, "Gate")
	s.mu.Lock()
	defer s.mu.Unlock()
	target, submitted := s.head, in.Input
	if c, ok := s.commits[in.Input]; ok && in.Kind == port.InputResolved && len(c.parents) == 2 {
		target, submitted = c.parents[0], c.parents[1]
	}
	switch kind {
	case port.OutcomeConflict:
		if in.Kind != port.InputResolved {
			s.open, s.resolution = &fakeCommit{task: in.Task, parents: []string{target, submitted}}, ""
			return port.GateOutcome{Kind: port.OutcomeConflict, Files: strings.Fields(cmp.Or(rest, "shared.txt")), Dir: s.Dir}, nil
		}
	case port.OutcomeRed:
		return port.GateOutcome{Kind: port.OutcomeRed, Line: cmp.Or(rest, "RED compile (rc 1)")}, nil
	case port.OutcomeFail:
		return port.GateOutcome{Kind: port.OutcomeFail, Line: cmp.Or(rest, "FAIL "+in.Task+": scripted")}, nil
	}
	sub := s.commits[submitted]
	sha := in.Input
	if in.Kind != port.InputResolved || len(s.commits[sha].parents) != 2 {
		sha = s.pins["candidate/"+in.Task] // a repeat makes the same commit, as git's gate does
		if !slices.Equal(s.commits[sha].parents, []string{target, submitted}) {
			sha = s.sha("merge")
			s.commits[sha] = fakeCommit{task: in.Task, parents: []string{target, submitted}}
		}
	}
	s.pins["candidate/"+in.Task] = sha
	c := port.Candidate{Task: in.Task, Lane: in.Lane, SHA: sha, Target: target}
	var stat, diff []string
	for _, path := range slices.Sorted(maps.Keys(sub.own)) {
		stat = append(stat, " "+path+" | 1 +")
		diff = append(diff, "diff --git a/"+path+" b/"+path+"\n+++ b/"+path+"\n+"+strings.TrimRight(sub.own[path], "\n"))
	}
	c.Stat, c.Diff = strings.Join(stat, "\n"), strings.Join(diff, "\n")
	sum := sha256.Sum256([]byte(cmp.Or(c.Diff, submitted)))
	c.Patch = hex.EncodeToString(sum[:])
	if !s.unbased {
		c.Base = cmp.Or(sub.base, "b0")
	}
	return port.GateOutcome{Kind: port.OutcomeGreen, Candidate: c}, nil
}

// Test is the lane test: a dirty lane is an error (never checked out); the candidate checked out in the lane while
// OnTest's hook runs, then the lane back on its branch; a cut test (its ctx ended) is an error.
func (s *FakeWorkflow) Test(ctx context.Context, in port.TestInput) (port.TestOutcome, error) {
	s.Tests.Add(1)
	s.mu.Lock()
	s.tests[in.Task]++
	s.mu.Unlock()
	if err := s.failing(ctx, "Test"); err != nil {
		return port.TestOutcome{}, err
	}
	if in.Task == s.holdTest {
		s.note("test " + in.Task)
		if err := s.waitOn(ctx, s.testRelease); err != nil {
			return port.TestOutcome{}, err
		}
	}
	s.mu.Lock()
	l, during := s.lanes[in.Lane], s.onTest
	dir := ""
	if l != nil {
		if !maps.Equal(laneFiles(l.dir), l.files) {
			s.mu.Unlock()
			return port.TestOutcome{}, fmt.Errorf("lane %s dirty: its test runs once it is clean", in.Lane)
		}
		l.at, dir = in.SHA, l.dir
	}
	s.mu.Unlock()
	if during != nil {
		during(ctx, dir)
	}
	if l != nil {
		s.mu.Lock()
		l.at = ""
		s.mu.Unlock()
	}
	if err := ctx.Err(); err != nil {
		return port.TestOutcome{}, err
	}
	switch kind, rest := s.scripted(in.Task, "Test"); kind {
	case port.OutcomeRed:
		return port.TestOutcome{Kind: port.OutcomeRed, Line: cmp.Or(rest, "RED check (rc 1): 0 pass, 1 fail")}, nil
	case port.OutcomeFail:
		return port.TestOutcome{Kind: port.OutcomeFail, Line: cmp.Or(rest, "FAIL "+in.Task+": scripted")}, nil
	}
	checks := cmp.Or(s.Checks, "rc=0")
	return port.TestOutcome{Kind: port.OutcomeGreen, Line: "GREEN " + in.Task + " (" + checks + ")", Checks: checks}, nil
}

// Resolved pins the resolution committed in Dir: refused while the conflict is still open, or when it is no merge of
// input. With no conflict seen at all (the concurrency rig) it is "resolution".
func (s *FakeWorkflow) Resolved(ctx context.Context, task, input string, _ *port.Mismatch) (string, error) {
	s.enter()
	defer s.leave()
	if task == s.holdResolved {
		s.note("resolved " + task)
		if err := s.waitOn(ctx, nil); err != nil {
			return "", err
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	switch c := s.commits[s.resolution]; {
	case s.open != nil:
		return "", port.Refused{Line: "the merge in " + s.Dir + " is not committed"}
	case s.resolution == "":
		return "resolution", nil
	case c.parents[1] != input:
		return "", port.Refused{Line: s.Dir + " holds no merge of " + input}
	}
	s.pins["resolved/"+task] = s.resolution
	return s.resolution, nil
}

// Publish fast-forwards the target to c.SHA while it is at c.Target (unless scripted: a hold), then unpins the task.
func (s *FakeWorkflow) Publish(ctx context.Context, c port.Candidate) (port.PublishOutcome, error) {
	s.enter()
	defer s.leave()
	s.Publishes.Add(1)
	s.mu.Lock()
	s.pubs[c.Task]++
	s.mu.Unlock()
	if c.Task == s.holdPub {
		s.note("publish " + c.Task)
		if err := s.waitOn(ctx, nil); err != nil {
			return port.PublishOutcome{}, err
		}
	}
	if err := s.failing(ctx, "Publish"); err != nil {
		return port.PublishOutcome{}, err
	}
	kind, rest := s.scripted(c.Task, "Publish")
	s.mu.Lock()
	defer s.mu.Unlock()
	switch {
	case s.history[c.SHA]:
		s.unpin(c.Task)
		return port.PublishOutcome{Kind: port.PublishAlready}, nil
	case s.head != c.Target:
		return port.PublishOutcome{Kind: port.PublishMoved, Head: s.head}, nil
	case kind == port.PublishHold:
		return port.PublishOutcome{Kind: port.PublishHold, Files: strings.Fields(cmp.Or(rest, "shared.txt"))}, nil
	case kind == port.PublishPending: // the merge asked for, not done: Land is the forge's merge
		return port.PublishOutcome{Kind: port.PublishPending}, nil
	}
	s.head, s.history[c.SHA], s.landed[c.Task] = c.SHA, true, true
	s.merges, s.published[c.Task] = append(s.merges, c.Task), c
	s.unpin(c.Task)
	return port.PublishOutcome{Kind: port.PublishMerged}, nil
}

// AfterMerge is "ran" (scripted: its line).
func (s *FakeWorkflow) AfterMerge(ctx context.Context, c port.Candidate) (string, error) {
	s.AfterMerges.Add(1)
	if err := s.failing(ctx, "AfterMerge"); err != nil {
		return "", err
	}
	s.mu.Lock()
	s.afterRuns[c.Task]++
	line := s.script["AfterMerge "+c.Task]
	s.mu.Unlock()
	return cmp.Or(line, "ran"), nil
}

// Landed: the target holds a merge of task.
func (s *FakeWorkflow) Landed(ctx context.Context, task string) (bool, error) {
	if err := s.failing(ctx, "Landed"); err != nil {
		return false, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.landed[task], nil
}

// Unpin deletes task's pins.
func (s *FakeWorkflow) Unpin(ctx context.Context, task string) error {
	if err := s.failing(ctx, "Unpin"); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.unpin(task)
	return nil
}

func (s *FakeWorkflow) unpin(task string) {
	for ref := range s.pins {
		if strings.HasPrefix(ref, "submitted/"+task+"/") || ref == "candidate/"+task || ref == "resolved/"+task {
			delete(s.pins, ref)
		}
	}
}

// Prepare puts lane in.Lane at in.Dir on in.Branch at in.Target (in.Redo, a lane it knows: kept as it is), committed
// as its dir is now; no lane dir is a final refusal, as checks.Lane's. The base is in.Target.
func (s *FakeWorkflow) Prepare(ctx context.Context, in port.PrepareInput) (port.Prepared, error) {
	if err := s.failing(ctx, "Prepare"); err != nil {
		return port.Prepared{}, err
	}
	if st, err := os.Stat(in.Dir); err != nil || !st.IsDir() {
		return port.Prepared{}, port.Refused{Line: "no lane dir " + in.Dir}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if l := s.lanes[in.Lane]; !in.Redo || l == nil {
		s.lanes[in.Lane] = &fakeLane{dir: in.Dir, branch: in.Branch, base: in.Target, head: in.Target, files: laneFiles(in.Dir)}
	}
	return port.Prepared{Base: in.Target}, nil
}

// Free gives lane in.Lane back: refused while it has uncommitted changes or commits the target lacks, unless
// in.Discard; then the fake forgets it (an unknown lane is gone already). Its line names what it discarded.
func (s *FakeWorkflow) Free(ctx context.Context, in port.FreeInput) (string, error) {
	if err := s.failing(ctx, "Free"); err != nil {
		return "", err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	l := s.lanes[in.Lane]
	if l == nil {
		return "", nil
	}
	var lost []string
	if !maps.Equal(laneFiles(l.dir), l.files) {
		lost = append(lost, "uncommitted changes")
	}
	if s.ahead(l) {
		lost = append(lost, "commits not in the target")
	}
	if len(lost) > 0 && !in.Discard {
		return "", port.Refused{Line: "lane " + in.Lane + " has " + strings.Join(lost, " and ")}
	}
	for path := range laneFiles(l.dir) { // the uncommitted changes discarded (git reset --hard, git clean -fd)
		if _, ok := l.files[path]; !ok {
			_ = os.Remove(filepath.Join(l.dir, path))
		}
	}
	for path, text := range l.files {
		_ = os.WriteFile(filepath.Join(l.dir, path), []byte(text), 0o644)
	}
	delete(s.lanes, in.Lane)
	if len(lost) == 0 {
		return "", nil
	}
	return "discarded " + strings.Join(lost, " and "), nil
}

// ahead: lane l's head has commits the target lacks (under mu): a commit past its base no merge on the target took,
// and its task not landed by hand.
func (s *FakeWorkflow) ahead(l *fakeLane) bool {
	if l.head == l.base || s.landed[s.commits[l.head].task] {
		return false
	}
	for h := range s.history {
		if c := s.commits[h]; len(c.parents) == 2 && c.parents[1] == l.head {
			return false
		}
	}
	return true
}

// Target is the target's head; ahead: a lane on task's branch (task/<task>) has commits the target lacks.
func (s *FakeWorkflow) Target(ctx context.Context, task string) (string, bool, error) {
	if err := s.failing(ctx, "Target"); err != nil {
		return "", false, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, l := range s.lanes {
		if l.branch == "task/"+task && s.ahead(l) {
			return s.head, true, nil
		}
	}
	return s.head, false, nil
}

// Head is the target's head, whatever branch is asked for (the fake has one target).
func (s *FakeWorkflow) Head(ctx context.Context, _ string) (string, error) {
	if err := s.failing(ctx, "Head"); err != nil {
		return "", err
	}
	return s.TargetHead(), nil
}

// Files is the files the task diffs merged from from to to changed: to's first parents back to from (from "": as far
// as the fake made them).
func (s *FakeWorkflow) Files(_ context.Context, from, to string) ([]string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	files := map[string]bool{}
	for sha := to; sha != "" && sha != from; {
		c, ok := s.commits[sha]
		if !ok || len(c.parents) != 2 {
			break
		}
		for path := range s.commits[c.parents[1]].own {
			files[path] = true
		}
		sha = c.parents[0]
	}
	return slices.Sorted(maps.Keys(files)), nil
}

// Detach records role worktree dir at sha.
func (s *FakeWorkflow) Detach(ctx context.Context, dir, sha string) error {
	if err := s.failing(ctx, "Detach"); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.detached[dir] = sha
	return nil
}

// Detached is the sha Detach put role worktree dir on ("": never).
func (s *FakeWorkflow) Detached(dir string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.detached[dir]
}

// Close commits review close c: recorded, its subject Closed's answer for its id from then on.
func (s *FakeWorkflow) Close(ctx context.Context, c port.ReviewCommit) error {
	if err := s.failing(ctx, "Close"); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closes = append(s.closes, c)
	return nil
}

// Closed is the subject of review id's newest close ("": none).
func (s *FakeWorkflow) Closed(_ context.Context, _, _, id string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := len(s.closes) - 1; i >= 0; i-- {
		if s.closes[i].ID == id {
			return s.closes[i].Subject, nil
		}
	}
	return "", nil
}

var _ port.Workflow = (*FakeWorkflow)(nil)

// WorkflowOf is a port.Workflow of a test's own fakes: each part takes over every piece of port.Workflow it
// implements (port.Scratch, port.LaneSetup, port.Review, Target), a later
// part over an earlier one; the pieces no part implements are a NewFakeWorkflow's. A nil part is skipped.
func WorkflowOf(parts ...any) port.Workflow {
	f := NewFakeWorkflow("")
	w := &workflowOf{Scratch: f, LaneSetup: f, Review: f, targeter: f}
	for _, p := range parts {
		if x, ok := p.(port.Scratch); ok {
			w.Scratch = x
		}
		if x, ok := p.(port.LaneSetup); ok {
			w.LaneSetup = x
		}
		if x, ok := p.(port.Review); ok {
			w.Review = x
		}
		if x, ok := p.(targeter); ok {
			w.targeter = x
		}
	}
	return w
}

type targeter interface {
	Target(ctx context.Context, task string) (string, bool, error)
}

type workflowOf struct {
	port.Scratch
	port.LaneSetup
	port.Review
	targeter
}

// Integrator jobs start a fresh session each (Launch with Fresh, the job's wake its first prompt), not a wake
// typed into the session that runs.

// greenTask gates task green in r's project pj with candidate c.
func greenTask(r *concRig, task string, c port.Candidate) {
	r.add("pj", task, event.StepGate, event.KindIntent, gateIn{Kind: port.InputLane, Lane: "L-01", Input: "s1"})
	r.add("pj", task, event.StepGate, event.KindResult, gateOut{Outcome: outGreen, Candidate: c})
}

// The identity table (registry.go), its case table, on the watchdogs'
// harness: lane L-01's worker pj-l-01 in w1:p1 (terminal t1, session s1, labelled by its launch), a shell w1:p2 beside
// it. Each test asserts whereIs and the consumer's observable call (the fake Agents' Stop pane, the Prompter/Poster/
// Liner target, the queued line, the fact written). Rows 12–20 are the transitions the model asks for.

// newIdentHarness is the harness with w1:p1 labelled pj-l-01 on terminal t1 (session s1), the lane row and the config
// naming pj-l-01 (lane L-01's worker), and a shell w1:p2 (terminal t2) in the project's workspace; its log lines in
// out.
func newIdentHarness(t *testing.T) (*harness, *bytes.Buffer) {
	h := newHarness(t)
	h.write(filepath.Join(h.root, ".obor", "config.json"), `{"version":2,"project":{"name":"pj","agent_prefix":"pj"},"lanes":{"count":1}}`)
	h.lanes.lanes[0].Agent = "pj-l-01"
	p := h.pane("w1:p1")
	p.Label, p.Terminal, p.Session = "pj-l-01", "t1", "s1"
	h.snap.Panes = append(h.snap.Panes, port.Pane{ID: "w1:p2", Group: "pj", Cwd: h.root, Terminal: "t2"})
	out := &bytes.Buffer{}
	h.a.opt.Out = out
	return h, out
}

// withLead gives pj a lead role (pj-lead), an integrator (pj-int) and an advisor (pj-advisor, no lane): every name a
// start row or a recorded session may prove a pane with, lane L-01's worker pj-l-01 included.
func (h *harness) withLead() {
	h.write(filepath.Join(h.root, ".obor", "config.json"), `{"version":2,"project":{"name":"pj","agent_prefix":"pj"},`+
		`"lanes":{"count":1},"agents":{"roles":{"integrator":"","lead":"","advisor":""}}}`)
}

// sids makes agent → session each agent's current recorded session in the store (once the project is loaded): put in
// the fold directly, so two agents may hold one id (the tie cases; sessionCall refuses that). A tie is reached the way
// a log can: the owner moves on, the other agent takes the id, and the owner's confirmation of its newer record hands
// its current session back to the id.
func (h *harness) sids(m map[string]string) {
	h.sidMap = m
	h.applySids()
}

func (h *harness) applySids() {
	if h.a.p == nil {
		return
	}
	h.a.mu.Lock()
	defer h.a.mu.Unlock()
	f := &h.a.p.folded.sessions
	byID := map[string][]string{}
	for agent, id := range h.sidMap {
		byID[id] = append(byID[id], agent)
	}
	for _, id := range slices.Sorted(maps.Keys(byID)) {
		agents := byID[id]
		slices.Sort(agents)
		for _, agent := range agents {
			if cur, ok := f.Current(agent); ok && cur.ID == id {
				continue
			}
			h.sidSeq++
			rec := store.Session{Agent: agent, Kind: "claude", ID: id, Started: h.sidSeq}
			if f.Add(rec) {
				continue
			}
			owner := agents[0]
			moved := id + "-moved-" + owner
			h.sidSeq++
			f.Add(store.Session{Agent: owner, Kind: "claude", ID: moved, Started: h.sidSeq})
			h.sidSeq++
			f.Add(rec)
			h.sidSeq++
			f.Add(store.Session{Agent: owner, Kind: "claude", ID: moved, Transcript: "/t/moved", Started: 0})
		}
	}
}

// started writes serve's start of agent in pane (terminal term, session sid; "": none) to the log: agent.start's
// intent and result.
func (h *harness) started(agent, pane, term, sid string) {
	in, _ := json.Marshal(startData{Lane: "L-01", Agent: agent, Pane: pane})
	res, _ := json.Marshal(startOut{Line: "STARTED " + agent, Agent: agent, Pane: pane, Terminal: term, Kind: "claude",
		Session: sid})
	h.log(event.Record{Task: "T-1", Attempt: 1, Step: event.StepStartWork, Kind: event.KindIntent, Class: event.ClassStart, Data: in},
		event.Record{Task: "T-1", Attempt: 1, Step: event.StepStartWork, Kind: event.KindResult, Data: res})
}

// where is whereIs(agent) in pj: "identified <pane> <source>", "ambiguous <panes>", "unavailable <why>".
func (h *harness) where(agent string) string {
	h.a.mu.Lock()
	defer h.a.mu.Unlock()
	w := h.a.whereIs(h.proj(), agent)
	switch w.State {
	case Identified:
		return "identified " + w.Pane.ID + " " + w.Source
	case Ambiguous:
		return "ambiguous " + strings.Join(w.Panes, " ")
	}
	return "unavailable " + w.Why
}

// who is whoIs of pane id in pj ("": nobody).
func (h *harness) who(id string) string {
	h.a.mu.Lock()
	defer h.a.mu.Unlock()
	o := h.a.obs[id]
	if o == nil {
		return ""
	}
	agent, _ := h.proj().reg.WhoIs(o.pane)
	return agent
}

// table is the identity table as its lookups give it: whoIs of each pane of h.snap (by id) and whereIs of each agent.
func (h *harness) table(agents ...string) string {
	ids := make([]string, 0, len(h.snap.Panes))
	for _, pane := range h.snap.Panes {
		ids = append(ids, pane.ID)
	}
	slices.Sort(ids)
	var b strings.Builder
	for _, id := range ids {
		fmt.Fprintf(&b, "%s=%s; ", id, h.who(id))
	}
	for _, agent := range agents {
		fmt.Fprintf(&b, "%s: %s; ", agent, h.where(agent))
	}
	return b.String()
}

// move replaces pane id in h.snap by pane to (its terminal kept, as herdr's pane.move) and connects again (herdr's
// pane.move is a refresh: a new snapshot).
func (h *harness) move(id, to string) {
	h.pane(id).ID = to
	h.connect()
}

// add puts pane into h.snap and sends its created event.
func (h *harness) add(pane port.Pane) {
	h.snap.Panes = append(h.snap.Panes, pane)
	eventNow(h.a, port.Event{Kind: port.EventCreated, Pane: pane.ID, New: pane})
}

// twin is a second pane labelled pj-l-01 (w1:p3, terminal t3), no session.
func (h *harness) twin() port.Pane {
	return port.Pane{ID: "w1:p3", Group: "pj", Cwd: h.root, Agent: "claude", Status: "idle", Label: "pj-l-01",
		Terminal: "t3"}
}

func eqs(t *testing.T, got, want, what string) {
	t.Helper()
	if got != want {
		t.Errorf("%s:\n got %q\nwant %q", what, got, want)
	}
}

// idAgents is an Agents recording each Stop's agent and pane.
type idAgents struct{ stops []string }

func (f *idAgents) Launch(context.Context, port.Launch) (port.Started, error) {
	return port.Started{Line: "STARTED"}, nil
}

func (f *idAgents) Stop(_ context.Context, agent, pane string, _ bool) (string, error) {
	f.stops = append(f.stops, agent+" "+pane)
	return "STOPPED " + agent + " (pane " + pane + ")", nil
}

// stop runs the after-merge stop of pj-l-01 (stopWorker) while lane L-01 holds T-1.
func (h *harness) stop(f *idAgents) (string, error) {
	p := h.proj()
	h.a.mu.Lock()
	p.folded.lr.rows["L-01"] = store.LaneRow{Lane: "L-01", Task: "T-1"}
	h.a.mu.Unlock()
	return h.a.stopWorker(context.Background(), p, &Loop{Agents: f}, mtask{task: "T-1", attempt: 1, lane: "L-01",
		agent: "pj-l-01"})
}

// wake sends pj-l-01's work job T-1/1/work (send) through fx; true: sent.
func (h *harness) wake(fx *Loop) bool {
	return h.a.send(context.Background(), h.proj(), fx, mtask{task: "T-1", attempt: 1, lane: "L-01", agent: "pj-l-01"},
		event.Key{Task: "T-1", Attempt: 1, Step: event.StepWork}, "T-1/1/work", promptData{Agent: "pj-l-01"}, wakeText, "work job")
}

// brief records a brief to agent (line "": its start carried it).
func (h *harness) brief(agent, line string) {
	if !h.a.record(h.proj(), event.Record{Task: event.MsgTask, Step: event.StepBriefed, Kind: event.KindFact},
		event.Message{To: agent, From: "pj-lead", Line: line}) {
		h.t.Fatal("brief not recorded")
	}
}

// worked: pj's open brief of agent saw its work.
func (h *harness) worked(agent string) bool {
	h.a.mu.Lock()
	defer h.a.mu.Unlock()
	b := h.proj().out.Brief(agent)
	return b != nil && b.Worked()
}

// lines is the lines of pj not delivered yet, as "<to>: <line>".
func (h *harness) lines() []string {
	h.a.mu.Lock()
	defer h.a.mu.Unlock()
	var out []string
	for _, q := range h.proj().out.Queued() {
		out = append(out, q.Msg().To+": "+q.Msg().Line)
	}
	return out
}

// kindSpy is a Poster recording each post's kind, agent and line.
type kindSpy struct{ calls []string }

func (s *kindSpy) Post(_ context.Context, kind, agent, line string) (bool, error) {
	s.calls = append(s.calls, kind+" "+agent+": "+line)
	return true, nil
}

// stopWith runs the after-merge stop of pj-l-01 (stopWorker) through ag while lane L-01 holds T-1.
func (h *harness) stopWith(ag port.Agents) (string, error) {
	p := h.proj()
	h.a.mu.Lock()
	p.folded.lr.rows["L-01"] = store.LaneRow{Lane: "L-01", Task: "T-1"}
	h.a.mu.Unlock()
	return h.a.stopWorker(context.Background(), p, &Loop{Agents: ag}, mtask{task: "T-1", attempt: 1, lane: "L-01",
		agent: "pj-l-01"})
}

// deliverIntents is the number of delivery intents in pj's log.
func (h *harness) deliverIntents() int {
	n := 0
	for _, r := range h.records() {
		if r.Kind == event.KindIntent && strings.HasPrefix(r.Step, outbox.StepDeliver) {
			n++
		}
	}
	return n
}

// queuedLeadThenBrief queues a lead line, then a brief to pj-l-01 (a Codex pane, typed).
func (h *harness) queuedLeadThenBrief() {
	h.a.opt.Inboxed = func(string) (string, bool) { return "", true }
	h.a.record(h.proj(), event.Record{Task: event.MsgTask, Step: event.StepMessage, Kind: event.KindFact},
		event.Message{To: "pj-lead", Line: "[obor] lead line"})
	h.brief("pj-l-01", "[obor] verify")
}

// Idle counts a verify the pipeline still owes (the verify rule's condition, before its brief), so no
// idle line fires between a fix round's last merge and the verify's brief; the verify rule runs before idle's in the
// one rule pass.

// afterStepsDone records task's after-merge steps as done: its merge alone is no work in flight.
func (r *reviewRig) afterStepsDone(task string) {
	r.t.Helper()
	for _, step := range afterSteps {
		r.a.record(r.p, event.Record{Task: task, Attempt: 1, Step: step, Kind: event.KindResult}, nil)
	}
}

// idlePass is the merge loop's rule pass after its advance.
func (r *reviewRig) idlePass() {
	r.a.rulePass(context.Background(), rules, &Loop{Workflow: WorkflowOf(r.git), Agents: r.agents})
}

// idleSettled lets the idle window pass at the rig's clock: the first check starts (or keeps) it, the clock moves past
// idleSettle, and the next check posts the line.
func (r *reviewRig) idleSettled() {
	r.t.Helper()
	r.idlePass()
	r.now = r.now.Add(idleSettle)
	r.idlePass()
}

// idleOK is idleNow's answer.
func (r *reviewRig) idleOK() bool {
	r.a.mu.Lock()
	defer r.a.mu.Unlock()
	return r.p.idleNow().ok
}

// idlePosts are the idle lines the log recorded to the lead, in order.
func (r *reviewRig) idlePosts() []string {
	r.t.Helper()
	var out []string
	for _, rec := range readLog(r.t, r.p.root) {
		var m event.Message
		if rec.Task == event.MsgTask && rec.Step == event.StepMessage && json.Unmarshal(rec.Data, &m) == nil && m.Of.Kind == ruleIdle {
			out = append(out, m.Line)
		}
	}
	return out
}

// eventNow is a.event as Serve completes it: the refresher's pass over the lane table the event marked, then the run
// loop's decision on the table it read (lanescan.go); for the tests that drive the core without Serve's
// goroutines.
func eventNow(a *App, ev port.Event) {
	ctx := context.Background()
	a.event(ctx, ev)
	if a.rescanDue(ctx) {
		a.check(ctx, false)
	}
}

// waitFor polls cond for up to 10 s.
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	for end := time.Now().Add(10 * time.Second); time.Now().Before(end); time.Sleep(time.Millisecond) {
		if cond() {
			return
		}
	}
	t.Fatalf("timed out waiting for %s", what)
}

// lineStep is a step of a line rule's table: a record, a rule pass, a delivery, a restart.
type lineStep func(t *testing.T, h *harness)

// passOf is a pass of rule r alone after the clock moved d.
func passOf(r rule, d time.Duration) lineStep {
	return func(_ *testing.T, h *harness) {
		h.now = h.now.Add(d)
		h.a.rulePass(context.Background(), []rule{r}, &Loop{})
	}
}

// restartRig is serve's restart: the log closed, the project loaded from it again.
func restartRig(t *testing.T, h *harness) {
	h.a.closeLog()
	p, err := h.a.load(h.root)
	if err != nil {
		t.Fatal(err)
	}
	h.a.mu.Lock()
	h.a.p = p
	h.a.mu.Unlock()
	if !p.merging || p.out.Lead() != "pj-lead" {
		t.Fatalf("precondition: after the restart merging %v, lead %q", p.merging, p.out.Lead())
	}
}

// taskMerged is task submitted and merged at head (outside the app when head is ""), no after-merge step left.
func taskMerged(task, head string) lineStep {
	return func(t *testing.T, h *harness) {
		addRecord(t, h, task, event.StepSubmitted, event.KindFact, submitData{Lane: "L-01"})
		addRecord(t, h, task, event.StepMerged, event.KindFact, event.MergedData{Head: head, Outside: head == ""})
	}
}

// The project's lines survive a restart (orderly and SIGKILL) and an off-target checkout.

// linerSpy is a Prompter and a Poster that records each line typed or posted (the lead's); refuse: refused before
// typing; hang: typing begins and never returns (the process is killed there).
type linerSpy struct {
	calls  []string
	refuse bool
	hang   func()
}

func (l *linerSpy) Prompt(_ context.Context, agent, text string, _ bool) (bool, error) {
	l.calls = append(l.calls, agent+": "+text)
	switch {
	case l.hang != nil:
		l.hang()
		park()
	case l.refuse:
		return false, errors.New("agent_not_ready: no typing")
	}
	return true, nil
}

func (l *linerSpy) Post(ctx context.Context, _, agent, line string) (bool, error) {
	return l.Prompt(ctx, agent, line, false)
}

// linerLoop is a loop whose lines go to l, typed and posted.
func linerLoop(l *linerSpy) *Loop { return &Loop{Prompter: l, Inbox: l} }

// lineRoot is a merging project with a reviewer role.
func lineRoot(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".obor"), 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := `{"version":2,"project":{"name":"proj","agent_prefix":"pj"},"agents":{"roles":{"integrator":"","reviewer":""}}}`
	if err := os.WriteFile(filepath.Join(root, ".obor/config.json"), []byte(cfg), 0o644); err != nil {
		t.Fatal(err)
	}
	return root
}

// lineApp loads root's log and connects to a snapshot with the lead and the reviewer pj-rev idle.
func lineApp(t *testing.T, root string, now *time.Time) (*App, *proj) {
	t.Helper()
	a := newTestApp(Options{Now: func() time.Time { return *now }, Loop: func(string) (*Loop, error) { return &Loop{}, nil }},
		&fakePanes{}, &fakeLanes{})
	p, err := a.load(root)
	if err != nil {
		t.Fatal(err)
	}
	a.p = p
	t.Cleanup(func() { p.log.Close(); a.closeLog() })
	a.connect(context.Background(), port.Snapshot{Panes: []port.Pane{
		{ID: "w:lead", Group: "proj", Cwd: root, Agent: "claude", Label: p.out.Lead(), Status: "idle"},
		{ID: "w:rev", Group: "proj", Cwd: root, Agent: "codex", Label: "pj-rev", Status: "idle"},
	}})
	return a, p
}

// lineReload closes a's log and loads it again (an orderly restart).
func lineReload(t *testing.T, a *App, p *proj, now *time.Time) (*App, *proj) {
	t.Helper()
	p.log.Close()
	a.closeLog()
	return lineApp(t, p.root, now)
}

// lineStatus sets pane id's status as herdr reports it (an event at a's clock: observed, reconciled, its work noted).
func lineStatus(a *App, id, status string, _ time.Time) {
	pane := a.obs[id].pane
	pane.Status = status
	eventNow(a, port.Event{Kind: port.EventStatus, Pane: id, Status: status, Agent: pane.Agent, New: pane})
}

// queueLine queues one line of kind (nothing typed yet).
func queueLine(t *testing.T, a *App, p *proj, kind string, now time.Time) {
	t.Helper()
	var err error
	switch kind {
	case "send":
		_, err = a.sendCall(wire.SendReq{Envelope: wire.Envelope{Root: p.root, CallID: "send1", Role: "lead"},
			From: "other", Text: "hello"})
	case "brief":
		_, err = a.briefCall(wire.BriefReq{Envelope: wire.Envelope{Root: p.root, CallID: "b1", Role: "lead"},
			To: "pj-rev", Text: "verify finding 1"})
	case "finished":
		_, err = a.briefCall(wire.BriefReq{Envelope: wire.Envelope{Root: p.root, CallID: "b1", Role: "lead"},
			To: "pj-rev"}) // a fresh start's brief
		lineStatus(a, "w:rev", "working", now)
		lineStatus(a, "w:rev", "idle", now)
	}
	if err != nil {
		t.Fatal(err)
	}
}

// briefCount is the number of p's open briefs, by its outbox's allBriefs.
func (p *proj) briefCount() int {
	n := 0
	for range p.out.Briefs() {
		n++
	}
	return n
}

// park blocks the calling goroutine with a pending timer, so the runtime's deadlock detector leaves the process for the
// parent's SIGKILL. A bare select{} let a loaded child exit 2 ("all goroutines are asleep") before the kill landed, red
// in a gate run.
func park() {
	t := time.NewTimer(time.Hour)
	defer t.Stop()
	<-t.C
}

// inboxSpy records each line posted to an inbox.
type inboxSpy struct{ calls []string }

func (s *inboxSpy) Post(_ context.Context, _, agent, line string) (bool, error) {
	s.calls = append(s.calls, agent+": "+line)
	return true, nil
}

// replayed is h's project loaded again from its log (an orderly restart).
func replayed(t *testing.T, h *harness) *proj {
	t.Helper()
	h.a.closeLog()
	a := newTestApp(Options{Root: h.root, Now: func() time.Time { return h.now }}, h.panes, h.lanes)
	p, err := a.load(h.root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { p.log.Close() })
	return p
}

var logNow = time.Date(2026, 10, 24, 12, 0, 0, 0, time.FixedZone("x", 3600))

// logRoot is a project root with an empty state dir: no database yet.
func logRoot(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".obor", "state"), 0o755); err != nil {
		t.Fatal(err)
	}
	return root
}

// openLog opens root's database as serve does and replays it; closed when the test ends.
func openLog(t *testing.T, root string) (*event.Log, []event.Record) {
	t.Helper()
	db, err := store.OpenRW(event.DBFile(root))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	l, recs, err := event.OpenLog(db)
	if err != nil {
		t.Fatal(err)
	}
	return l, recs
}

func fact(t *testing.T, task, step, reason string) event.Record {
	t.Helper()
	r := event.Record{Task: task, Step: step, Kind: event.KindFact}
	if reason != "" {
		b, err := json.Marshal(event.FactData{Reason: reason})
		if err != nil {
			t.Fatal(err)
		}
		r.Data = b
	}
	return r
}

// The choices of one merge-loop task over p's loop map never follow Go's map order: a lane's task is its newest
// dispatch's, the other choices are by task name. Each test has two tasks the choice could take and asks it many
// times: map order would pick the other one in about half of them.

const orderTries = 64

// twoInLane is a project whose loop holds T-2 and T-1, both dispatched to lane L1 by agent w1, their work jobs out;
// T-2's dispatch is the newer (seq 7 after T-1's 3): name order and dispatch order disagree.
func twoInLane() *proj {
	mk := func(task string, seq int64) *mtask {
		return &mtask{task: task, lane: "L1", agent: "w1", attempt: 1, state: stDispatched, attemptVars: attemptVars{work: event.StepWork}, dispSeq: seq}
	}
	return &proj{merging: true, log: &event.Log{}, ended: map[string]bool{}, held: map[string]string{},
		folded: folded{loop: map[string]*mtask{"T-2": mk("T-2", 7), "T-1": mk("T-1", 3)}, calls: map[string]string{}}}
}

// A needs-you is a block on the task's phase. These golden folds were captured with the
// code before the change (the three encodings: fail, needs-you with left, merged/rejected with needs): each case is a
// log in the event log's format, replayed, and what obor status, the needs-you list, the task verbs, the next step
// and obor resume make of it (each action folded in turn) must stay the same. OBOR_UPDATE_GOLDEN=1 rewrites the file.

const blockTask = "T-1"

// blockLog builds a case's records: each step appends records of blockTask in the event log's format.
type blockLog struct{ recs []event.Record }

func (b *blockLog) add(attempt int, step, kind string, data any) *blockLog {
	r := event.Record{Task: blockTask, Attempt: attempt, Step: step, Kind: kind}
	if data != nil {
		r.Data, _ = json.Marshal(data)
	}
	b.recs = append(b.recs, r)
	return b
}

func (b *blockLog) fact(attempt int, step string, data any) *blockLog {
	return b.add(attempt, step, event.KindFact, data)
}

// result appends step's intent and its result.
func (b *blockLog) result(attempt int, step string, in, out any) *blockLog {
	return b.add(attempt, step, event.KindIntent, in).add(attempt, step, event.KindResult, out)
}

func (b *blockLog) needs(attempt int, cause, reason string) *blockLog {
	return b.fact(attempt, event.StepNeedsYou, event.FactData{Lane: "L-01", Machine: Machine, Reason: reason, Merge: true,
		Cause: cause})
}

func (b *blockLog) failed(attempt int, step, why string, n int) *blockLog {
	for range n {
		b.fact(attempt, event.StepFailed, event.FactData{Lane: "L-01", Machine: Machine, Reason: step + ": " + why,
			Merge: true, Step: step})
	}
	return b
}

func (b *blockLog) copy() *blockLog { return &blockLog{recs: slices.Clone(b.recs)} }

// dispatched: attempt 1 of blockTask dispatched to L-01, prepared and started, its work job delivered (working).
func dispatched() *blockLog {
	b := &blockLog{}
	b.fact(1, event.StepDispatched, dispatchData{Lane: "L-01", Agent: "pj-w1", Reply: "DISPATCHED"})
	b.result(1, event.StepPrepare, nil, prepareOut{Base: "base"})
	b.result(1, event.StepStartWork, nil, startOut{Agent: "pj-w1"})
	b.add(1, event.StepWork, event.KindIntent, nil)
	b.fact(1, event.StepDelivered, event.FactData{Job: JobID(event.Key{Task: blockTask, Attempt: 1, Step: event.StepWork})})
	return b
}

// submitted: attempt n submitted by its worker and pinned; its gate's intent written.
func (b *blockLog) submitted(n int) *blockLog {
	b.fact(n, event.StepSubmitted, submitData{Lane: "L-01", Agent: "pj-w1", Report: "Summary: x"})
	b.result(n, event.StepSubmit, nil, submitOut{SHA: fmt.Sprintf("sha%d", n)})
	return b
}

func (b *blockLog) gate(n int, outcome string) *blockLog {
	out := gateOut{Outcome: outcome, Line: "gate " + outcome}
	if outcome == outGreen {
		out.Candidate = port.Candidate{SHA: "cand", Patch: "patch"}
	}
	return b.result(n, event.StepGate, gateIn{Kind: port.InputLane, Lane: "L-01"}, out)
}

// review: attempt n green and its review job out.
func (b *blockLog) review(n int) *blockLog {
	return b.gate(n, outGreen).add(n, event.StepReview, event.KindIntent, JobData{Patch: "patch"})
}

func (b *blockLog) verdict(n int, method, job string, v verdictResult) *blockLog {
	v.Job = JobID(event.Key{Task: blockTask, Attempt: n, Step: job})
	return b.result(n, verdictStep(method, "c"+method), nil, v)
}

func (b *blockLog) accepted(n int) *blockLog {
	return b.review(n).verdict(n, wire.MethodAccept, event.StepReview, verdictResult{Patch: "patch", Reply: "ACCEPTED"})
}

// blockProj replays recs as a log serve opens: written to a new log, then read back and folded.
func blockProj(t *testing.T, recs []event.Record) (*App, *proj) {
	t.Helper()
	root := logRoot(t)
	appendRecs(t, root, recs)
	return blockProjAt(t, root)
}

// appendRecs appends recs to root's log.
func appendRecs(t *testing.T, root string, recs []event.Record) {
	t.Helper()
	l, _ := openLog(t, root)
	for _, r := range recs {
		if _, err := l.Append(r, logNow); err != nil {
			t.Fatal(err)
		}
	}
	l.Close()
}

// blockProjAt is serve's project of root's log, replayed.
func blockProjAt(t *testing.T, root string) (*App, *proj) {
	t.Helper()
	l, recs := openLog(t, root)
	a := &App{opt: Options{Now: func() time.Time { return logNow }, Out: io.Discard}}
	p := &proj{root: root, name: "pj", log: l, tasks: map[string]*watch{},
		integrator: "pj-integrator", out: outbox.New("pj-lead", ""), merging: true, timers: map[string]*jobTimer{},
		replies: map[string]string{}, nopane: map[string]time.Time{}, waitLine: map[string]waitRefusal{},
		unwritten: map[event.Key]json.RawMessage{}, exec: map[event.Key]time.Time{}, scratch: make(chan struct{}, 1),
		held: map[string]string{}, ended: map[string]bool{}, roleFail: map[string]time.Time{}, reg: identity.New(),
		folded: newFolded()}
	for _, r := range recs {
		p.foldRecord(r)
	}
	a.p = p
	return a, p
}

// landedScratch: the target holds merge evidence of every task (a merge outside the app).
type landedScratch struct{ port.Scratch }

func (landedScratch) Landed(context.Context, string) (bool, error) { return true, nil }

// needsMessages is the message facts of p's log that announce a needs-you.
func needsMessages(t *testing.T, p *proj) []event.Message {
	t.Helper()
	recs, err := event.ReadLog(p.root)
	if err != nil {
		t.Fatal(err)
	}
	var out []event.Message
	for _, r := range recs {
		if r.Task != event.MsgTask || r.Step != event.StepMessage || r.Kind != event.KindFact {
			continue
		}
		var m event.Message
		if err := json.Unmarshal(r.Data, &m); err != nil {
			t.Fatal(err)
		}
		if m.Of.Kind == ruleNeeds {
			out = append(out, m)
		}
	}
	return out
}

// docs/state-model.md "Prompts" (the wake contract) and row 4's agent.start replay. The first
// action after the setup is the case's entry.

// boxPrompter is a Prompter whose agents never take a prompt by themselves: what is typed stays in the pane's box (box),
// so a second typing on top of an unsent one shows. Each call takes the next scripted outcome (then: typed).
type boxPrompter struct {
	outcomes []boxOutcome
	jobs     []string // the job of each call
	clears   []bool   // its clear
	box      map[string]string
}

type boxOutcome struct {
	typed bool
	err   error
}

func (f *boxPrompter) Prompt(_ context.Context, pane, text string, clear bool) (bool, error) {
	job := jobOf(text)
	f.jobs, f.clears = append(f.jobs, job), append(f.clears, clear)
	o := boxOutcome{typed: true}
	if len(f.outcomes) > 0 {
		o, f.outcomes = f.outcomes[0], f.outcomes[1:]
	}
	if clear {
		f.box[pane] = ""
	}
	if o.typed {
		f.box[pane] += "[obor job " + job + "]"
	}
	return o.typed, o.err
}

// jobOf is the job id in a typed wake's [obor job <id>] tag ("": a line, no job tag).
func jobOf(text string) string {
	rest, ok := strings.CutPrefix(text, "[obor job ")
	if !ok {
		return ""
	}
	job, _, _ := strings.Cut(rest, "]")
	return job
}

// startAgents is an Agents whose Launch puts a fresh session (sid, "": none known) into the pane it starts in (its
// input's; lag: the snapshot does not show it yet).
type startAgents struct {
	h        *harness
	restarts int
	sid      string
	lag      bool
	pane     string // the pane of its last launch
}

func (f *startAgents) Launch(_ context.Context, in port.Launch) (port.Started, error) {
	f.restarts, f.pane = f.restarts+1, in.Pane
	if f.lag {
		return port.Started{Line: "STARTED pj-l-01"}, nil
	}
	o := f.h.a.obs[in.Pane]
	if o == nil {
		f.h.t.Fatalf("launch in pane %s, which the snapshot does not have", in.Pane)
	}
	o.pane.Agent, o.pane.Session, o.pane.Transcript = "claude", f.sid, ""
	return port.Started{Line: "STARTED pj-l-01"}, nil
}

func (f *startAgents) Stop(context.Context, string, string, bool) (string, error) {
	return "STOPPED", nil
}

// promptRig is T-1 dispatched onto L-01 (lane prepared, rows removed; started: its worker started too), the switch
// on, with a boxPrompter and startAgents. before, when given, acts on the harness's snapshot before the connect takes
// it.
func promptRig(t *testing.T, started bool, before ...func(h *harness)) (*harness, *Loop, *boxPrompter, *startAgents) {
	h, s := seedSched(t, true, before...)
	addRecord(t, h, "T-1", event.StepDispatched, event.KindFact, dispatchData{Lane: "L-01", Dir: h.lane("L-01"), Agent: "pj-l-01",
		Target: "target", Branch: "task/T-1"})
	addRecord(t, h, "T-1", event.StepPrepare, event.KindResult, prepareOut{Base: "target"})
	if started {
		addRecord(t, h, "T-1", event.StepStartWork, event.KindResult, startOut{Line: "STARTED", Pane: "w1:p1"})
	}
	h.a.p.dispatching = true
	kit, _ := filepath.Abs("../..")
	f := &boxPrompter{box: map[string]string{}}
	ag := &startAgents{h: h, sid: "new-session"}
	return h, &Loop{Workflow: WorkflowOf(s), Agents: ag, Prompter: f, Kit: kit}, f, ag
}

// proj is pj now (a reload replaces it).
func (h *harness) proj() *proj { return h.a.p }

// step runs T-1's next dispatch step (false: none, or it waits).
func (h *harness) step(t *testing.T, fx *Loop) bool {
	t.Helper()
	p := h.proj()
	h.a.mu.Lock()
	s := h.a.nextDispatch(p, *p.folded.loop["T-1"])
	h.a.mu.Unlock()
	return s != nil && s(context.Background(), fx)
}

// count is how many records of pj's log have kind and step (step "": any step with kind).
func (h *harness) count(kind, step string) int {
	n := 0
	for _, r := range h.records() {
		if r.Kind == kind && (step == "" || r.Step == step) {
			n++
		}
	}
	return n
}

// result is key's result data in pj's log ("": none).
func (h *harness) result(step string) string {
	for _, r := range h.records() {
		if r.Kind == event.KindResult && r.Task == "T-1" && r.Step == step {
			return string(r.Data)
		}
	}
	return ""
}

// refuseOnce makes pj's log refuse the first append of a result of step.
func refuseOnce(p *proj, step string) {
	first := true
	p.log.Refuse = func(r event.Record) error {
		if first && r.Kind == event.KindResult && r.Step == step {
			first = false
			return errors.New("result append failed")
		}
		return nil
	}
}

// mustJSON is v as JSON.
func mustJSON(v any) json.RawMessage {
	b, _ := json.Marshal(v)
	return b
}

// has: a fact of pj's log contains s.
func (h *harness) has(s string) bool {
	for _, f := range h.facts(0) {
		if strings.Contains(f, s) {
			return true
		}
	}
	return false
}

// A green task whose worker's report leaves open questions gets its review job, the questions
// in it: the integrator decides the ones inside the spec's goal and holds the rest for the lead.

// seedQuestions is project pj with an integrator, lane L-01's T-1 submitted with report and its lane gate green.
func seedQuestions(t *testing.T, report string) (*harness, *schedScratch) {
	t.Helper()
	h := newHarness(t)
	h.write(filepath.Join(h.root, ".obor", "config.json"), `{"version":2,"project":{"name":"pj","agent_prefix":"pj"},"agents":{"roles":{"integrator":""}}}`)
	f := &schedScratch{h: h}
	h.a.opt.Loop = func(string) (*Loop, error) {
		return &Loop{Workflow: WorkflowOf(f), Agents: f, Prompter: f, Kit: "../.."}, nil
	}
	if err := os.MkdirAll(h.lanes.lanes[0].Dir, 0o755); err != nil {
		t.Fatal(err)
	}
	h.connect()
	h.a.mu.Lock()
	h.a.p.folded.lr.rows["L-01"] = store.LaneRow{Lane: "L-01", Task: "T-1"} // the lane holds T-1
	h.a.mu.Unlock()
	addRecord(t, h, "T-1", event.StepSubmitted, event.KindFact, submitData{Lane: "L-01", Agent: "pj-l-01", Pane: "w1:p1",
		Report: report})
	addRecord(t, h, "T-1", event.StepSubmit, event.KindResult, submitOut{SHA: "s1"})
	addRecord(t, h, "T-1", event.StepGate, event.KindIntent, gateIn{Kind: port.InputLane, Lane: "L-01", Input: "s1"})
	addRecord(t, h, "T-1", event.StepGate, event.KindResult, gateOut{Outcome: outGreen, SpecRev: 1,
		Candidate: port.Candidate{Task: "T-1", Lane: "L-01", SHA: "c1", Target: "base1", Patch: "patch1"}})
	if mt := h.a.p.folded.loop["T-1"]; mt.state != stGreen || mt.report != report {
		t.Fatalf("precondition: T-1 %s with report %q; want green with the submitted report", mt.state, mt.report)
	}
	return h, f
}

// pickGreenStep runs the step pickGreen selects for T-1 (live, or after a replay of the log).
func pickGreenStep(t *testing.T, h *harness, f *schedScratch, replay bool) *mtask {
	t.Helper()
	p := h.a.p
	if replay {
		p = reloadProj(t, h)
	}
	s := h.a.pickGreen(p, *p.folded.loop["T-1"])
	if s == nil || !s(context.Background(), &Loop{Agents: f, Prompter: f}) {
		t.Fatal("pickGreen: no step, or it failed")
	}
	return p.folded.loop["T-1"]
}

func nextJob(t *testing.T, h *harness, c wire.JobNextReq) wire.JobReply {
	t.Helper()
	raw, err := h.a.jobNext(c)
	if err != nil {
		t.Fatal(err)
	}
	var jr wire.JobReply
	if err := json.Unmarshal([]byte(raw), &jr); err != nil {
		t.Fatal(err)
	}
	return jr
}

// known is pane id as the snapshot has it now (zero: not known).
func known(h *harness, id string) port.Pane {
	h.a.mu.Lock()
	defer h.a.mu.Unlock()
	if o := h.a.obs[id]; o != nil {
		return o.pane
	}
	return port.Pane{}
}

// since is when the no-pane clock of T-1 started (zero: not set).
func since(h *harness) time.Time {
	h.a.mu.Lock()
	defer h.a.mu.Unlock()
	return h.a.p.nopane["T-1"]
}

// The fold of a resolution's green gate (live and replayed: a serve restart replays
// these records). A first-gate conflict has no accept, so its resolved candidate goes to its lane test and then to a
// review job (row 19). A moved conflict's earlier accept is kept, unless the spec rev changed.

// resolvedGate folds a resolved gate's intent and green result under gate: the task's own patch, green at rev.
func (b *blockLog) resolvedGate(gate string, rev int) *blockLog {
	return b.add(1, gate, event.KindIntent, gateIn{Kind: port.InputResolved, Lane: "L-01"}).
		result(1, gate, nil, gateOut{Outcome: outGreen, SpecRev: rev,
			Candidate: port.Candidate{Task: blockTask, SHA: "candidate2", Target: "base2", Base: "base", Patch: "diff"}})
}

// showsRestart: obor status' running line shows the pending self-restart.
func (r *concRig) showsRestart(name string) bool {
	for _, s := range r.status(name).Running {
		if s.Step == event.StepServeRestart {
			return true
		}
	}
	return false
}

// obor review records a request; a merge-loop pass starts the review once its --after tasks merged and the
// reviewer is idle.

// reviewGitFake is the tests' one port.Review: head the integration branch's head ("": headSHA), which a
// gated publish moves (admit_test.go); files the fixed answer; detachErr fails the detach, detached every Detach as
// <dir>@<sha>. onHead runs once, inside the next Head read. closes is every close committed (Closed finds its
// subject), closeErr refuses one; closeGate, when set, holds a close until it is closed and closeBegan gets a value
// when one reaches it; reads counts the Closed reads, headReads the Head reads. What merged and what a review covered are the log's
// mu guards every field a method touches (a publish runs beside the admission tests' calls).
type reviewGitFake struct {
	mu         sync.Mutex
	head       string
	files      []string
	detachErr  error
	detached   []string
	onHead     func()
	closes     []port.ReviewCommit
	closeErr   error
	closeGate  chan struct{}
	closeBegan chan struct{}
	closedRead int
	headReads  int
}

func (g *reviewGitFake) Head(context.Context, string) (string, error) {
	g.mu.Lock()
	hook := g.onHead
	g.onHead = nil
	g.mu.Unlock()
	if hook != nil {
		hook()
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	g.headReads++
	return cmp.Or(g.head, "abcdef0123456789abcdef0123456789abcdef01"), nil
}

func (g *reviewGitFake) Files(context.Context, string, string) ([]string, error) { return g.files, nil }

func (g *reviewGitFake) Detach(_ context.Context, dir, sha string) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.detachErr != nil {
		return g.detachErr
	}
	g.detached = append(g.detached, dir+"@"+sha)
	return nil
}

func (g *reviewGitFake) Close(ctx context.Context, c port.ReviewCommit) error {
	g.mu.Lock()
	gate, began := g.closeGate, g.closeBegan
	g.mu.Unlock()
	if gate != nil { // a close in flight inside configMu
		if began != nil {
			began <- struct{}{}
		}
		select {
		case <-gate:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.closeErr != nil {
		return g.closeErr
	}
	g.closes = append(g.closes, c)
	return nil
}

// Closed is the newest committed close of id (its subject; "": none).
func (g *reviewGitFake) Closed(_ context.Context, _, _, id string) (string, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.closedRead++
	for i := len(g.closes) - 1; i >= 0; i-- {
		if g.closes[i].ID == id {
			return g.closes[i].Subject, nil
		}
	}
	return "", nil
}

// reads is how many times Closed was read.
func (g *reviewGitFake) reads() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.closedRead
}

// detachedAt is every Detach so far, as <worktree base name>@<sha>.
func (g *reviewGitFake) detachedAt() string {
	g.mu.Lock()
	defer g.mu.Unlock()
	var out []string
	for _, d := range g.detached {
		out = append(out, filepath.Base(d))
	}
	return strings.Join(out, ",")
}

// agentsSpy is an Agents that records every Launch and starts nothing; launchErr fails each launch.
type agentsSpy struct {
	launches  []port.Launch
	launchErr error
}

func (s *agentsSpy) Launch(_ context.Context, l port.Launch) (port.Started, error) {
	s.launches = append(s.launches, l)
	if s.launchErr != nil {
		return port.Started{}, s.launchErr
	}
	return port.Started{Line: "RESTARTED " + l.Agent}, nil
}

func (s *agentsSpy) Stop(context.Context, string, string, bool) (string, error) { return "", nil }

// reviewRig is a merging project with a reviewer pj-rev (idle, worktree review), reviews.dir and git and agent fakes.
type reviewRig struct {
	t      *testing.T
	a      *App
	p      *proj
	now    time.Time
	git    *reviewGitFake
	agents *agentsSpy
	// seeded is the seq of seed's last record (0: none); next the id the rig's first review starts as.
	seeded int64
	next   string
}

// newReviewRig is a review rig on its seeded log (seed: RV-4 closed, T-a and T-b merged since).
func newReviewRig(t *testing.T) *reviewRig { return newReviewRigWith(t, true) }

// newReviewRigWith is a review rig, its log seeded or empty (no review yet: the first is RV-1).
func newReviewRigWith(t *testing.T, seed bool) *reviewRig {
	t.Helper()
	root := lineRoot(t)
	cfg := `{"version":2,"project":{"name":"proj","agent_prefix":"pj"},"agents":{"roles":{"integrator":"","reviewer":""}},"reviews":{"dir":"reviews"}}`
	if err := os.WriteFile(filepath.Join(root, ".obor/config.json"), []byte(cfg), 0o644); err != nil {
		t.Fatal(err)
	}
	r := &reviewRig{t: t, now: time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC), git: &reviewGitFake{
		files: []string{"internal/app/x.go", "docs/y.md"}}, agents: &agentsSpy{}}
	r.a, r.p = lineApp(t, root, &r.now)
	r.wire()
	r.next = "RV-1"
	if seed {
		r.seed()
	}
	return r
}

// seed is the rig's log before a test: RV-4 started at 1111111 (covering T-9) and closed, then T-a and T-b merged
// (aaaaaaa1, bbbbbbb2): the next review is RV-5, its scope T-a and T-b.
func (r *reviewRig) seed() {
	r.t.Helper()
	r.fact(event.ReviewTask, event.StepReviewStarted, ReviewData{ID: "RV-4", Head: "1111111", Tasks: []string{"T-9"}})
	r.fact(event.ReviewTask, event.StepReviewClosed, ReviewData{ID: "RV-4", Reply: review.ClosedReply + "RV-4 closed · 1111111 · approved · T-9"})
	r.mergedAt("T-a", "aaaaaaa1")
	r.mergedAt("T-b", "bbbbbbb2")
	recs := readLog(r.t, r.p.root)
	r.seeded, r.next = recs[len(recs)-1].Seq, "RV-5"
}

// fact records a fact of task (fails when the log refuses it).
func (r *reviewRig) fact(task, step string, d any) {
	r.t.Helper()
	if !r.a.record(r.p, event.Record{Task: task, Step: step, Kind: event.KindFact}, d) {
		r.t.Fatalf("the log refused fact %s %s", task, step)
	}
}

// mergedAt records task's merge by the merge kernel at sha (fact merged; no loop task needed).
func (r *reviewRig) mergedAt(task, sha string) {
	r.t.Helper()
	r.fact(task, event.StepMerged, event.MergedData{Head: sha})
}

// wire gives obor review's call the rig's git (its open-review check reads the merge loop's).
func (r *reviewRig) wire() {
	r.a.opt.Loop = func(string) (*Loop, error) { return &Loop{Workflow: WorkflowOf(r.git), Agents: r.agents}, nil }
}

// reload is a restart: the log replayed.
func (r *reviewRig) reload() {
	r.a, r.p = lineReload(r.t, r.a, r.p, &r.now)
	r.wire()
}

// started is RV-5 (RV-1 unseeded) fired and started on request r1 (the reviewer's pane then works and is idle again: its brief ends).
func (r *reviewRig) started() {
	r.t.Helper()
	r.mustCall("r1", nil, "", false)
	r.pass()
	if len(r.agents.launches) != 1 || r.p.folded.rv.Last().ID != r.next {
		r.t.Fatalf("precondition: %s not started: %+v", r.next, r.agents.launches)
	}
	lineStatus(r.a, "w:rev", "working", r.now)
	lineStatus(r.a, "w:rev", "idle", r.now)
	r.a.deliver(context.Background(), r.p, &Loop{Inbox: &inboxSpy{}}) // records the finished line
	if r.p.out.Brief("pj-rev") != nil {
		r.t.Fatal("precondition: the reviewer's brief is still open")
	}
}

// closedBy is a close as serve's close leaves it: fact review.closed of id, its reply naming subject.
func (r *reviewRig) closedBy(id, subject string) {
	r.t.Helper()
	r.fact(event.ReviewTask, event.StepReviewClosed, ReviewData{ID: id, Reply: review.ClosedReply + subject})
}

// close is obor review close's call from the lead (call id id, the summary, its accepted lines, --force).
func (r *reviewRig) close(id, summary string, force bool, accepts ...string) (string, error) {
	return r.a.reviewCloseCall(wire.ReviewCloseReq{Envelope: wire.Envelope{Root: r.p.root, CallID: id, Role: "lead",
		Agent: "pj-lead"}, Summary: summary, Accept: accepts, Force: force})
}

// refuseStep makes the event log refuse every fact of step (nil: none refused).
func (r *reviewRig) refuseStep(step string) {
	r.p.log.Refuse = func(rec event.Record) error {
		if rec.Step == step {
			return errors.New("disk full")
		}
		return nil
	}
}

// pass is one review pass.
func (r *reviewRig) pass() {
	r.a.reviewPass(context.Background(), r.p, &Loop{Workflow: WorkflowOf(r.git), Agents: r.agents})
}

// tick is one "review due" count.
func (r *reviewRig) tick() {
	r.a.reviewDueTick(context.Background(), r.p, &Loop{Workflow: WorkflowOf(r.git), Agents: r.agents})
}

// call is an obor review request from the lead.
func (r *reviewRig) call(id string, after []string, note string, rm bool) (string, error) {
	return r.a.reviewCall(wire.ReviewReq{Envelope: wire.Envelope{Root: r.p.root, CallID: id, Role: "lead",
		Agent: "pj-lead"}, After: after, Note: note, Clear: rm})
}

func (r *reviewRig) mustCall(id string, after []string, note string, rm bool) string {
	r.t.Helper()
	reply, err := r.call(id, after, note, rm)
	if err != nil {
		r.t.Fatal(err)
	}
	return reply
}

// submitted opens task (attempt 1 on a lane); merged and dropped end it.
func (r *reviewRig) submitted(task string) {
	// The seed's merged facts (no dispatch) are merged loop tasks, and a submission never reopens an ended
	// task: one the seed merged is dispatched again first, as a real log has it.
	if mt := r.p.folded.loop[task]; mt != nil && mt.dispSeq == 0 && states[mt.state].ended {
		r.a.record(r.p, event.Record{Task: task, Attempt: 1, Step: event.StepDispatched, Kind: event.KindFact},
			dispatchData{Dispatch: port.Dispatch{Lane: "L-01"}})
	}
	r.a.record(r.p, event.Record{Task: task, Attempt: 1, Step: event.StepSubmitted, Kind: event.KindFact}, submitData{Lane: "L-01"})
}

func (r *reviewRig) merged(task string) {
	r.a.record(r.p, event.Record{Task: task, Attempt: 1, Step: event.StepMerged, Kind: event.KindFact}, event.MergedData{Head: "cccccccdddd"})
}

func (r *reviewRig) dropped(task string) {
	r.a.record(r.p, event.Record{Task: task, Attempt: 1, Step: event.StepDropped, Kind: event.KindFact}, event.FactData{Reason: "superseded"})
}

// leadLines is the lines to the lead recorded so far.
func (r *reviewRig) leadLines() []string {
	var out []string
	for _, q := range r.p.out.Queued() {
		if q.Msg().To == "pj-lead" {
			out = append(out, q.Msg().Line)
		}
	}
	return out
}

// facts is the log's review facts by step since the seed.
func (r *reviewRig) facts(step string) int {
	recs, err := event.ReadLog(r.p.root)
	if err != nil {
		r.t.Fatal(err)
	}
	n := 0
	for _, rec := range recs {
		if rec.Task == event.ReviewTask && rec.Kind == event.KindFact && rec.Step == step && rec.Seq > r.seeded {
			n++
		}
	}
	return n
}

// needs is the review key's needs-you reasons in force.
func (r *reviewRig) needs() []string {
	var out []string
	for _, f := range r.p.folded.active[event.ReviewTask] {
		if f.Step == event.StepNeedsYou {
			out = append(out, f.Fact().Reason)
		}
	}
	return out
}

// obor review close is a call to serve: serve checks the open review against its fold, commits the close in the
// main checkout (port.Review.Close, admitted) and records fact review.closed, which every "is a review open" reads.

// closedFacts is the log's review.closed facts' data since the seed, in order.
func (r *reviewRig) closedFacts() []ReviewData {
	r.t.Helper()
	var out []ReviewData
	for _, rec := range readLog(r.t, r.p.root) {
		var d ReviewData
		if rec.Task == event.ReviewTask && rec.Step == event.StepReviewClosed && rec.Seq > r.seeded &&
			json.Unmarshal(rec.Data, &d) == nil {
			out = append(out, d)
		}
	}
	return out
}

// commits is how many closes the main checkout committed.
func (r *reviewRig) commits() int {
	r.git.mu.Lock()
	defer r.git.mu.Unlock()
	return len(r.git.closes)
}

func tasksOf(ms []review.Merge) []string {
	var out []string
	for _, m := range ms {
		out = append(out, m.Task())
	}
	return out
}

// A fix task links itself to the open review with its spec's Fixes: line (derived, no record); the verify is a
// rule: once every linked task merged (or ended unmerged) and the reviewer is idle, serve detaches the reviewer's
// worktree at the integration head and briefs the verify in the same session; only the finished line covers the tasks.

// headSHA is the rig's integration branch head (reviewGitFake.Head).
const headSHA = "abcdef0123456789abcdef0123456789abcdef01"

// startHead is the head the open review started at (startedReview).
const startHead = "1234567890abcdef"

// openReviewFile writes the open review's file (RV-5) in the rig's main checkout.
func openReviewFile(t *testing.T, root string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(root, "reviews"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "reviews", "RV-5.md"), []byte("# RV-5\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

// startedReview records the open review's start fact (RV-5 covers T-start at startHead; the verifies add to it).
func startedReview(t *testing.T, r *reviewRig) {
	t.Helper()
	if !r.a.record(r.p, event.Record{Task: event.ReviewTask, Step: event.StepReviewStarted, Kind: event.KindFact},
		ReviewData{ID: "RV-5", Head: startHead, Tasks: []string{"T-start"}}) {
		t.Fatal("the log refused the review start")
	}
}

// fixTask stores task's spec with line between its title and its first heading, then dispatches and submits it.
func (r *reviewRig) fixTask(task, line string) {
	r.t.Helper()
	text := "# " + task + " — fix\n\nDifficulty: medium\n" + line + "\n\n## Do\nfix it\n"
	if _, _, err := store.SetSpec(r.p.log.DB(), task, text, "", "pj-lead", r.now); err != nil {
		r.t.Fatal(err)
	}
	if !r.a.record(r.p, event.Record{Task: task, Attempt: 1, Step: event.StepDispatched, Kind: event.KindFact},
		dispatchData{}) {
		r.t.Fatal("the log refused the dispatch")
	}
	r.submitted(task)
}

// fixRig is a rig with the open review RV-5 (its file written, RV-4 closed) started, and T-a, T-b dispatched with
// Fixes: RV-5 lines (T-a's with a note).
func fixRig(t *testing.T) *reviewRig {
	t.Helper()
	r := newReviewRig(t)
	openReviewFile(t, r.p.root)
	startedReview(t, r)
	r.fixTask("T-a", "Fixes: RV-5 #1, #3 — re-run with an old status JSON")
	r.fixTask("T-b", "Fixes: RV-5")
	return r
}

// verifyPass is one rule pass of the verify rule alone.
func (r *reviewRig) verifyPass() {
	r.a.rulePass(context.Background(), []rule{{name: ruleVerify, when: verifyRule}}, &Loop{Workflow: WorkflowOf(r.git), Agents: r.agents})
}

// cycle is RV-5's cycle as serve's close reads it (the test fails on an error).
func (r *reviewRig) cycle() reviewCycle {
	r.t.Helper()
	c, err := r.a.reviewCycle(r.p, "RV-5")
	if err != nil {
		r.t.Fatal(err)
	}
	return c
}

// pending is close's pending set for RV-5, comma-joined.
func (r *reviewRig) pending() string { return strings.Join(r.cycle().pending, ",") }

// verifyBriefs are the log's verify briefs of the reviewer (a brief naming tasks and a head), in order.
func verifyBriefs(t *testing.T, root string) []event.Message {
	t.Helper()
	var out []event.Message
	for _, r := range readLog(t, root) {
		var m event.Message
		if r.Task == event.MsgTask && r.Step == event.StepBriefed && json.Unmarshal(r.Data, &m) == nil &&
			m.To == "pj-rev" && m.Task != "" && m.Reviewed != "" {
			out = append(out, m)
		}
	}
	return out
}

// verifyLines are the log's lead lines of the verify rule (its own and its git failure's), in order.
func (r *reviewRig) verifyLines() []string {
	r.t.Helper()
	var out []string
	for _, rec := range readLog(r.t, r.p.root) {
		var m event.Message
		if rec.Task == event.MsgTask && rec.Step == event.StepMessage && json.Unmarshal(rec.Data, &m) == nil &&
			(m.Of.Kind == ruleVerify || m.Of.Kind == ruleVerifyGit) {
			out = append(out, m.Line)
		}
	}
	return out
}

// working sets the reviewer's pane working (true) or idle.
func (r *reviewRig) working(on bool) {
	status := "idle"
	if on {
		status = "working"
	}
	lineStatus(r.a, "w:rev", status, r.now)
}

// finish is the reviewer taking its open brief: its line delivered, its pane working, then idle; the finished line
// that ends the brief recorded.
func (r *reviewRig) finish() {
	r.t.Helper()
	if r.p.out.Brief("pj-rev") == nil {
		r.t.Fatal("precondition: the reviewer has no open brief to finish")
	}
	r.a.deliver(context.Background(), r.p, &Loop{Prompter: &linerSpy{}})
	r.working(true)
	r.working(false)
	r.a.deliver(context.Background(), r.p, &Loop{Prompter: &linerSpy{}})
	if r.p.out.Brief("pj-rev") != nil {
		r.t.Fatal("precondition: the reviewer's brief is still open after its work")
	}
}

// leadBrief is the lead's brief to the reviewer (call id id).
func (r *reviewRig) leadBrief(id string) {
	r.t.Helper()
	if _, err := r.a.briefCall(wire.BriefReq{Envelope: wire.Envelope{Root: r.p.root, CallID: id, Role: "lead",
		Agent: "pj-lead"}, To: "pj-rev", Text: "look at the flaky test"}); err != nil {
		r.t.Fatal(err)
	}
}

// stop is the reviewer's StopFailure hook (call id call) on errText (details).
func (r *reviewRig) stop(call, errText, details string) {
	r.t.Helper()
	if _, err := r.a.stoppedCall(wire.StoppedReq{Envelope: wire.Envelope{Root: r.p.root, CallID: call, Agent: "pj-rev",
		Role: "reviewer"}, Error: errText, Details: details}); err != nil {
		r.t.Fatal(err)
	}
}

// briefedVerify is fixRig's verify of T-a, T-b briefed, delivered and taken by the reviewer (its pane working).
func briefedVerify(t *testing.T) *reviewRig {
	t.Helper()
	r := fixRig(t)
	r.merged("T-a")
	r.merged("T-b")
	r.verifyPass()
	r.a.deliver(context.Background(), r.p, &Loop{Prompter: &linerSpy{}})
	r.working(true)
	if n := len(verifyBriefs(t, r.p.root)); n != 1 || r.p.out.Brief("pj-rev") == nil || r.pending() != "T-a,T-b" {
		t.Fatalf("precondition: %d verify briefs, brief open %t, pending %q; want one open, T-a,T-b pending", n,
			r.p.out.Brief("pj-rev") != nil, r.pending())
	}
	return r
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

// readLog is root's event log.
func readLog(t *testing.T, root string) []event.Record {
	t.Helper()
	recs, err := event.ReadLog(root)
	if err != nil {
		t.Fatal(err)
	}
	return recs
}

// ruleHarness is a loaded project with a lead and a merge loop, its rule pass run by hand at the harness's clock.
func ruleHarness(t *testing.T) *harness {
	t.Helper()
	h, _ := newIdentHarness(t)
	h.withLead()
	h.a.opt.Loop = func(string) (*Loop, error) { return &Loop{}, nil }
	h.connect()
	t.Cleanup(func() { h.proj().log.Close(); h.a.closeLog() })
	if p := h.proj(); !p.merging || p.out.Lead() != "pj-lead" {
		t.Fatalf("precondition: merging %v, lead %q", p.merging, p.out.Lead())
	}
	return h
}

// ruleLines is the rules' lines the log recorded, in order: their Of and line.
func ruleLines(t *testing.T, h *harness) (ofs []event.Of, lines []string) {
	t.Helper()
	for _, r := range readLog(t, h.root) {
		var m event.Message
		if r.Task == event.MsgTask && r.Step == event.StepMessage && json.Unmarshal(r.Data, &m) == nil && m.Of.Kind != "" {
			ofs, lines = append(ofs, m.Of), append(lines, m.Line)
		}
	}
	return ofs, lines
}

// gone: pid is no longer running within a second (init reaps an orphan).
func gone(pid int) bool {
	for range 50 {
		if syscall.Kill(pid, 0) != nil {
			return true
		}
		time.Sleep(20 * time.Millisecond)
	}
	return false
}

// leadSendback is the lead's obor resume --sendback of task in project name.
func (r *concRig) leadSendback(name, task, id string) (string, error) {
	return r.app(name).resume(wire.ResumeReq{Envelope: wire.Envelope{Root: r.roots[name], Agent: "pq-lead",
		Role: "lead", CallID: id}, Task: task, Flag: wire.ResumeSendback, Text: "fix the edge case"})
}

// factsOf is how many facts obor serve's status shows in force for task of project name.
func (r *concRig) factsOf(name, task string) int {
	n := 0
	for _, f := range r.status(name).Facts {
		if f.Task == task {
			n++
		}
	}
	return n
}

// attempt is task's current attempt in project name.
func (r *concRig) attempt(name, task string) int {
	a, p := r.app(name), r.proj(name)
	a.mu.Lock()
	defer a.mu.Unlock()
	return p.folded.loop[task].attempt
}

// A broken event log is visible. Serve loads the one project at Options.Root.

// rootApp is an app serving root; out is its output.
func rootApp(root string, out *bytes.Buffer) *App {
	now := time.Date(2026, 10, 27, 12, 0, 0, 0, time.UTC)
	return newTestApp(Options{Root: root, Out: out, Now: func() time.Time { return now }}, &fakePanes{}, &fakeLanes{})
}

// projectRoot is project pj in a temp dir.
func projectRoot(t *testing.T) string {
	t.Helper()
	root := filepath.Join(t.TempDir(), "pj")
	if err := os.MkdirAll(filepath.Join(root, ".obor"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".obor", "config.json"), []byte(`{"version":2,"project":{"name":"pj"}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	return root
}

// schedScratch is a fake Scratch and Agents for the scheduling tests: gates are green and logged by task,
// Resolved logs the input it was given.
type schedScratch struct {
	h        *harness
	gated    []string // the task of each Gate call
	resolved []string // the input of each Resolved call
}

func (f *schedScratch) Submit(context.Context, string, string, string, string) (string, error) {
	return "submitted", nil
}

func (f *schedScratch) Gate(_ context.Context, in port.GateInput) (port.GateOutcome, error) {
	f.gated = append(f.gated, in.Task)
	return port.GateOutcome{Kind: port.OutcomeGreen, Candidate: port.Candidate{Task: in.Task, Lane: in.Lane, SHA: "c-" + in.Task,
		Target: "b", Patch: "p"}}, nil
}

func (f *schedScratch) Test(_ context.Context, in port.TestInput) (port.TestOutcome, error) {
	return port.TestOutcome{Kind: port.OutcomeGreen, Line: "GREEN " + in.Task, Checks: "check 1 pass"}, nil
}

func (f *schedScratch) Resolved(_ context.Context, _, input string, _ *port.Mismatch) (string, error) {
	f.resolved = append(f.resolved, input)
	return "resolution", nil
}

func (f *schedScratch) Publish(context.Context, port.Candidate) (port.PublishOutcome, error) {
	return port.PublishOutcome{Kind: port.PublishMerged}, nil
}

func (f *schedScratch) Landed(context.Context, string) (bool, error) { return false, nil }

func (f *schedScratch) Unpin(context.Context, string) error { return nil }

func (f *schedScratch) AfterMerge(context.Context, port.Candidate) (string, error) { return "ran", nil }

func (f *schedScratch) Launch(context.Context, port.Launch) (port.Started, error) {
	f.h.a.obs["w1:p9"] = &seen{pane: port.Pane{ID: "w1:p9", Group: "pj", Cwd: f.h.root, Label: "pj-int",
		Agent: "claude", Status: "idle"}, since: f.h.now}
	return port.Started{Line: "started", Pane: "w1:p9", Kind: "claude"}, nil
}

func (f *schedScratch) Stop(context.Context, string, string, bool) (string, error) {
	return "stopped", nil
}

func (f *schedScratch) Prompt(context.Context, string, string, bool) (bool, error) {
	return true, nil
}

// seedSched is project pj with an integrator (serve false: without, so no merge loop), lane L-01's T-1 submitted at
// sha s1 and gated into a conflict. before, when given, acts on the harness's snapshot before the connect takes it.
func seedSched(t *testing.T, serve bool, before ...func(h *harness)) (*harness, *schedScratch) {
	h := newHarness(t)
	cfg := `{"version":2,"project":{"name":"pj","agent_prefix":"pj"},"agents":{"roles":{"integrator":""}}}`
	if !serve {
		cfg = `{"version":2,"project":{"name":"pj","agent_prefix":"pj"}}`
	}
	h.write(filepath.Join(h.root, ".obor", "config.json"), cfg)
	f := &schedScratch{h: h}
	h.a.opt.Loop = func(string) (*Loop, error) { return &Loop{Workflow: WorkflowOf(f), Agents: f, Prompter: f}, nil }
	if err := os.MkdirAll(h.lanes.lanes[0].Dir, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, fn := range before {
		fn(h)
	}
	h.connect()
	h.a.mu.Lock()
	h.a.p.folded.lr.rows["L-01"] = store.LaneRow{Lane: "L-01", Task: "T-1"} // the lane holds T-1
	h.a.mu.Unlock()
	if !serve {
		return h, f
	}
	addRecord(t, h, "T-1", event.StepSubmitted, event.KindFact, submitData{Lane: "L-01", Agent: "pj-l-01", Pane: "w1:p1"})
	addRecord(t, h, "T-1", event.StepSubmit, event.KindResult, submitOut{SHA: "s1"})
	addRecord(t, h, "T-1", event.StepGate, event.KindIntent, gateIn{Kind: port.InputLane, Lane: "L-01", Input: "s1"})
	addRecord(t, h, "T-1", event.StepGate, event.KindResult, gateOut{Outcome: outConflict})
	return h, f
}

func addRecord(t *testing.T, h *harness, task, step, kind string, d any) {
	t.Helper()
	p := h.a.p
	raw, _ := json.Marshal(d)
	r, err := p.log.Append(event.Record{Task: task, Attempt: 1, Step: step, Kind: kind, Data: raw}, h.now)
	if err != nil {
		t.Fatal(err)
	}
	p.foldRecord(r)
}

// pass is merge loop passes until no more scratch effects start (each started one has folded): what the loop
// goroutine, woken by each effect's end, does.
func (h *harness) pass(ctx context.Context) {
	for range 16 {
		h.a.mu.Lock()
		n := h.a.scratched
		h.a.mu.Unlock()
		h.a.advance(ctx)
		h.a.effects.Wait()
		h.a.mu.Lock()
		more := h.a.scratched != n
		h.a.mu.Unlock()
		if !more {
			return
		}
	}
	h.t.Fatal("merge loop passes never settled")
}

// reloadProj replays the log, as a serve restart does.
func reloadProj(t *testing.T, h *harness) *proj {
	t.Helper()
	p, err := h.a.load(h.root)
	if err != nil {
		t.Fatal(err)
	}
	h.a.p = p
	p.setLanes(h.lanes.lanes)
	h.a.reconcile(p) // as loadProject does
	return p
}

// memListener is an in-memory net.Listener: dial hands Listen one end of a net.Pipe.
type memListener struct {
	conns chan net.Conn
	once  sync.Once
	done  chan struct{}
}

func newMemListener() *memListener {
	return &memListener{conns: make(chan net.Conn), done: make(chan struct{})}
}

func (l *memListener) Accept() (net.Conn, error) {
	select {
	case c := <-l.conns:
		return c, nil
	case <-l.done:
		return nil, net.ErrClosed
	}
}

func (l *memListener) Close() error { l.once.Do(func() { close(l.done) }); return nil }

func (l *memListener) Addr() net.Addr { return &net.UnixAddr{Name: "mem", Net: "unix"} }

// send writes one request line to the app listening on l and returns the connection to read the answer from: the
// call carries this binary's version, as wire.Verdict's does.
func (l *memListener) send(t *testing.T, c wire.Req) net.Conn {
	t.Helper()
	c.Env().Version = version.Version
	return l.sendRaw(t, c)
}

// sendRaw writes one request line as given: a test of the skew itself sends another version, or none (an older binary).
func (l *memListener) sendRaw(t *testing.T, c wire.Req) net.Conn {
	t.Helper()
	client, server := net.Pipe()
	l.conns <- server
	call, err := json.Marshal(c)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(wire.Request{Method: c.Method(), Call: call})
	go func() { _, _ = client.Write(append(b, '\n')) }()
	return client
}

// MemServe is a's socket answered in memory: Listen on an in-memory listener until the test ends. Call is one
// request as wire.Verdict sends it (the envelope stamped with this binary's version): the reply, or the app's refusal
// as the error. It has none of the wire client's retries or deadlines: a test of those uses a unix socket.
type MemServe struct{ ln *memListener }

// ServeMem answers a's socket in memory until t ends (the connections a testing/synctest bubble can wait on).
func ServeMem(t *testing.T, a *App) *MemServe {
	t.Helper()
	ln := newMemListener()
	t.Cleanup(func() { ln.Close() })
	go a.Listen(ln)
	return &MemServe{ln: ln}
}

// Call sends c and gives the app's reply, or its refusal as the error.
func (s *MemServe) Call(t *testing.T, c wire.Req) (string, error) {
	t.Helper()
	client := s.ln.send(t, c)
	defer client.Close()
	ans := read(t, client)
	if ans.Error != "" {
		return "", errors.New(ans.Error)
	}
	return ans.Reply, nil
}

// read is the answer on client.
func read(t *testing.T, client net.Conn) wire.Answer {
	t.Helper()
	_ = client.SetDeadline(time.Now().Add(5 * time.Second))
	b, err := bufio.NewReader(client).ReadBytes('\n')
	if err != nil {
		t.Fatalf("no answer: %v", err)
	}
	var ans wire.Answer
	if err := json.Unmarshal(b, &ans); err != nil {
		t.Fatal(err)
	}
	return ans
}

// session reports agent's session id (transcript tr, started at) to h's serve as the SessionStart hook does.
func (h *harness) session(agent, id, tr string, at int64) string {
	h.t.Helper()
	reply, err := h.a.sessionCall(*wire.SessionRequest(h.root, store.Session{Agent: agent, Kind: "claude", ID: id, Transcript: tr,
		Pane: "w1:p1", Lane: "L-01", Task: "T-1", Started: at, Source: "SessionStart hook"}))
	if err != nil {
		h.t.Fatal(err)
	}
	return reply
}

// sessions is pj's recorded sessions of agent, as "<id> <transcript>".
func (h *harness) sessions(agent string) []string {
	h.a.mu.Lock()
	defer h.a.mu.Unlock()
	var out []string
	for _, s := range h.proj().folded.sessions.All() {
		if s.Agent == agent {
			out = append(out, s.ID+" "+s.Transcript)
		}
	}
	return out
}

// current is pj's current session of agent ("": none).
func (h *harness) current(agent string) string {
	h.a.mu.Lock()
	defer h.a.mu.Unlock()
	s, _ := h.proj().session(agent)
	return s.ID
}

// has fails when s lacks sub.
func has(t *testing.T, s, sub, what string) {
	t.Helper()
	if !strings.Contains(s, sub) {
		t.Errorf("%s: %q lacks %q", what, s, sub)
	}
}

// refused reports agent's session id to h's serve: the error ("" none).
func (h *harness) refused(agent, id string, at int64) string {
	h.t.Helper()
	_, err := h.a.sessionCall(*wire.SessionRequest(h.root, store.Session{Agent: agent, Kind: "codex", ID: id, Started: at,
		Source: "codex transcript"}))
	if err == nil {
		return ""
	}
	if !strings.Contains(err.Error(), refusedMark) {
		h.t.Fatalf("an error that is not a refusal: %v", err)
	}
	return err.Error()
}

// startJobFakes records the launches and the typed prompts; start runs inside Restart, as an agent that obeys its
// CLI first prompt before the launch returns.
type startJobFakes struct {
	*concFakes
	start    func(port.Launch)
	launches []port.Launch
	typed    []string
}

func (f *startJobFakes) Launch(_ context.Context, l port.Launch) (port.Started, error) {
	f.launches = append(f.launches, l)
	if f.start != nil {
		f.start(l)
	}
	return port.Started{Line: "STARTED"}, nil
}

func (f *startJobFakes) Prompt(_ context.Context, _, text string, _ bool) (bool, error) {
	f.typed = append(f.typed, text)
	return true, nil
}

func resultOf(p *proj, k event.Key) string {
	p.log.Mu.Lock()
	defer p.log.Mu.Unlock()
	return string(p.log.Results[k].Data)
}

func done(p *proj, k event.Key) bool { return p.log.Done(k) }

func deliveredOf(r *concRig, job string) int {
	recs, err := event.ReadLog(r.roots["pj"])
	if err != nil {
		r.t.Fatal(err)
	}
	n := 0
	for _, rec := range recs {
		if rec.Kind == event.KindFact && rec.Step == event.StepDelivered && rec.Fact().Job == job {
			n++
		}
	}
	return n
}

// same fails t when got is not want.
func same[T comparable](t *testing.T, got, want T, what string) {
	t.Helper()
	if got != want {
		t.Errorf("%s = %v, want %v", what, got, want)
	}
}

// Probe effects: start runs inside Restart (an agent obeying its CLI first prompt); typed are the Prompter's
// texts.
type startEffects struct {
	*concFakes
	start func(port.Launch)
	typed []string
}

func (f *startEffects) Launch(_ context.Context, l port.Launch) (port.Started, error) {
	if f.start != nil {
		f.start(l)
	}
	return port.Started{Line: "STARTED"}, nil
}

func (f *startEffects) Prompt(_ context.Context, agent, text string, clear bool) (bool, error) {
	f.typed = append(f.typed, text)
	return true, nil
}

// allVerbs are the task verbs, verbName their names for a failure.
var (
	allVerbs = []verb{vResume, vDrop, vLand, vMove, vSubmit}
	verbName = map[verb]string{vResume: "resume", vDrop: "drop", vLand: "land", vMove: "move", vSubmit: "submit"}
)

// needsOf is a needs-you fact of cause.
func needsOf(cause string) event.Record {
	data, _ := json.Marshal(event.FactData{Merge: true, Cause: cause, Reason: cause + ": why"})
	return event.Record{Seq: 1, Kind: event.KindFact, Step: event.StepNeedsYou, Data: data}
}

// heldOf is a held block: the needs-you fact of cause in force.
func heldOf(cause string) block { return block{fact: needsOf(cause)} }

// closedOf is a held block that began due (a closed one): its phase's work ended with cause.
func closedOf(cause string) block {
	return block{cause: cause, why: cause + ": why", fact: needsOf(cause)}
}

// busy: another task than task holds p's scratch (scratchHolder).
func busy(p *proj, task string) bool {
	t, _ := p.scratchHolder(task)
	return t != ""
}

// waitCall is the lead's dispatch of T-1 that must wait.
func (h *harness) waitCall(c wire.DispatchReq) {
	h.t.Helper()
	c.Root, c.Task, c.Role = h.root, "T-1", "lead"
	if reply, err := h.a.dispatchCall(context.Background(), c); err != nil || !strings.HasPrefix(reply, "WAITING") {
		h.t.Fatalf("dispatch %s: %q, %v; want WAITING", c.CallID, reply, err)
	}
}

// selected is T-1's next step now (App.next, with its begin/release wrapper).
func (h *harness) selected() step {
	h.t.Helper()
	h.a.mu.Lock()
	defer h.a.mu.Unlock()
	return h.a.next(h.a.p, h.a.p.folded.loop["T-1"])
}

func (h *harness) freeLanes() {
	for i := range h.lanes.lanes {
		h.lanes.lanes[i].State, h.lanes.lanes[i].Task = "free", ""
	}
}

// fact records a fact of task (folded).
func (h *harness) fact(task, step string, data any) {
	h.t.Helper()
	if !h.a.record(h.a.p, event.Record{Task: task, Attempt: 1, Step: step, Kind: event.KindFact}, data) {
		h.t.Fatalf("fact %s of %s refused", step, task)
	}
}

func (h *harness) t1() mtask {
	h.a.mu.Lock()
	defer h.a.mu.Unlock()
	return *h.a.p.folded.loop["T-1"]
}

// newTestApp is New with the test's own scripted activity and screen: a package test's app reads Busy, Activity and
// Turn from its fake panes (activity.go) and a blocked pane's question from their Screen, as the test scripts them.
func newTestApp(opt Options, panes *fakePanes, lanes *fakeLanes) *App {
	if opt.Activity == nil {
		opt.Activity = panes
	}
	if opt.Screen == nil {
		opt.Screen = panes.Screen
	}
	return New(opt, panes, lanes)
}

// The watchdogs' case table: each row is (prior state, event or tick, evidence) → the facts and notifications that
// follow. The core runs without Serve: the row calls connect, event and check itself on a clock it moves, over an
// in-memory herdr (fakePanes) and lane table. setup builds the prior state; the event is the first action after it.

type fakePanes struct {
	busy      bool
	busyErr   error
	last      time.Time // the transcript's last write (zero: unknown)
	screen    string    // what Screen shows
	screenErr error
	screens   []string // the panes Screen was asked about
	notifyFn  func() error
	notes     []string
	asked     []string // the panes Busy was asked about
	turn      agents.Turn
	turned    bool // Turn knows turn
	// Snapshot's answer: snap when an error does not come first (nil snap, nil err: "not in the table", as for the
	// table's rows); after runs once the answer is read, before it returns (a stream event landing in between);
	// snaps counts the calls.
	snap    *port.Snapshot
	snapErr error
	after   func()
	snaps   int
}

func (f *fakePanes) Subscribe(context.Context) (*port.Stream, error) {
	return nil, errors.New("not in the table")
}

// Snapshot is the read a lane's start makes again (rereadPanes): snap's panes, unless the context or snapErr says
// otherwise; a row that never set snap gets the error Subscribe gives.
func (f *fakePanes) Snapshot(ctx context.Context) (port.Snapshot, error) {
	f.snaps++
	if err := ctx.Err(); err != nil {
		return port.Snapshot{}, err
	}
	if f.snapErr != nil {
		return port.Snapshot{}, f.snapErr
	}
	if f.snap == nil {
		return port.Snapshot{}, errors.New("not in the table")
	}
	snap := *f.snap
	snap.Panes = append([]port.Pane(nil), f.snap.Panes...)
	if f.after != nil {
		f.after()
	}
	return snap, nil
}

func (f *fakePanes) Busy(_ context.Context, pane port.Pane) (bool, error) {
	f.asked = append(f.asked, pane.ID)
	return f.busy, f.busyErr
}

func (f *fakePanes) Activity(port.Pane) (time.Time, bool) {
	return f.last, !f.last.IsZero()
}

// Transcript is the path the test's pane reports, if any (the fake panes have no session id: the file's lookup is
// agentFiles').
func (f *fakePanes) Transcript(pane port.Pane) string { return pane.Transcript }

// Screen is what the pane shows, its last lines raw (lines is honoured, as herdr's pane.read does): the question a
// blocked pane shows sits above the TUI's blank rows and footer.
func (f *fakePanes) Screen(_ context.Context, pane string, lines int) (string, error) {
	f.screens = append(f.screens, pane)
	text := f.screen
	if lines > 0 {
		raw := strings.Split(strings.TrimSuffix(text, "\n"), "\n")
		if len(raw) > lines {
			raw = raw[len(raw)-lines:]
		}
		text = strings.Join(raw, "\n")
	}
	return text, f.screenErr
}

func (f *fakePanes) Turn(port.Pane, string) (agents.Turn, bool) {
	return f.turn, f.turned
}

func (f *fakePanes) Notify(_ context.Context, title, body string) error {
	if f.notifyFn != nil {
		if err := f.notifyFn(); err != nil {
			return err
		}
	}
	f.notes = append(f.notes, title+": "+body)
	return nil
}

// fakeLanes is the lane table; err, when set, fails the whole read instead.
type fakeLanes struct {
	lanes []port.Lane
	err   error
}

func (l *fakeLanes) Lanes(context.Context, string) ([]port.Lane, error) {
	if l.err != nil {
		return nil, l.err
	}
	return append([]port.Lane(nil), l.lanes...), nil
}

// harness is project pj (workspace w1) with lane L-01 holding T-1, its pane w1:p1 an idle claude; not connected.
type harness struct {
	t        *testing.T
	a        *App
	panes    *fakePanes
	lanes    *fakeLanes
	now      time.Time
	root, wt string
	snap     port.Snapshot     // herdr's session now
	sidMap   map[string]string // sids: agent → session id, put in the store (applySids)
	sidSeq   int64
}

func newHarness(t *testing.T) *harness {
	base := t.TempDir()
	h := &harness{t: t, panes: &fakePanes{}, lanes: &fakeLanes{}, now: time.Date(2026, 10, 24, 12, 0, 0, 0, time.UTC),
		root: filepath.Join(base, "pj"), wt: filepath.Join(base, "pj-wt")}
	if err := os.MkdirAll(filepath.Join(h.root, ".obor"), 0o755); err != nil {
		t.Fatal(err)
	}
	h.write(filepath.Join(h.root, ".obor", "config.json"), `{"version":2,"project":{"name":"pj"}}`)
	h.lanes.lanes = []port.Lane{{Name: "L-01", Dir: h.lane("L-01"), Task: "T-1"}}
	h.snap = port.Snapshot{Panes: []port.Pane{{ID: "w1:p1", Group: "pj", Cwd: h.lane("L-01"), Agent: "claude", Label: "pj-l-01", Status: "idle"}}}
	h.a = newTestApp(Options{Root: h.root, Now: func() time.Time { return h.now }}, h.panes, h.lanes)
	return h
}

func (h *harness) write(path, text string) {
	h.t.Helper()
	if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
		h.t.Fatal(err)
	}
}

func (h *harness) lane(name string) string { return filepath.Join(h.wt, name) }

// pane is pane id of h.snap (to change it before a connect, or to build an event from).
func (h *harness) pane(id string) *port.Pane {
	for i := range h.snap.Panes {
		if h.snap.Panes[i].ID == id {
			return &h.snap.Panes[i]
		}
	}
	h.t.Fatalf("no pane %s", id)
	return nil
}

// setLane puts task on the lane; done: its task_done is in (fact submitted in the loaded project's log).
func (h *harness) setLane(task string, done bool) {
	h.lanes.lanes[0].Task = task
	if !done {
		return
	}
	h.a.mu.Lock()
	defer h.a.mu.Unlock()
	if p := h.a.p; p != nil {
		rec, err := p.log.Append(event.Record{Task: task, Attempt: 1, Step: event.StepSubmitted, Kind: event.KindFact,
			Data: []byte(`{"lane":"L-01"}`)}, h.a.opt.Now())
		if err != nil {
			h.t.Fatal(err)
		}
		p.foldRecord(rec)
	}
}

// connect is a subscription: load the project, then work from h.snap.
func (h *harness) connect() {
	h.a.loadProject()
	h.applySids()
	snap := h.snap
	snap.Panes = append([]port.Pane(nil), h.snap.Panes...)
	h.a.connect(context.Background(), snap)
}

// tick moves the clock by d and runs one watchdog tick.
func (h *harness) tick(d time.Duration) {
	h.now = h.now.Add(d)
	h.a.loadProject()
	h.a.check(context.Background(), true)
}

// status changes pane id in h.snap and sends the status event that carries it.
func (h *harness) status(id string, fn func(p *port.Pane)) {
	p := h.pane(id)
	fn(p)
	eventNow(h.a, port.Event{Kind: port.EventStatus, Pane: id, Status: p.Status, Agent: p.Agent, New: *p})
}

// remove drops pane id from h.snap and sends kind (closed or exited).
func (h *harness) remove(id, kind string) {
	var keep []port.Pane
	for _, p := range h.snap.Panes {
		if p.ID != id {
			keep = append(keep, p)
		}
	}
	h.snap.Panes = keep
	eventNow(h.a, port.Event{Kind: kind, Pane: id})
}

// log writes prior records to pj's log (a crash left them).
func (h *harness) log(recs ...event.Record) {
	if err := WriteLog(h.root, h.now, recs...); err != nil {
		h.t.Fatal(err)
	}
}

// records is pj's log.
func (h *harness) records() []event.Record {
	h.t.Helper()
	recs, err := event.ReadLog(h.root)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		h.t.Fatal(err)
	}
	return recs
}

// facts is pj's facts from seq on, as "task step{reason}".
func (h *harness) facts(from int64) []string {
	var out []string
	for _, r := range h.records() {
		if r.Kind == event.KindFact && r.Seq > from {
			out = append(out, r.Task+" "+r.String())
		}
	}
	return out
}

func (h *harness) lastSeq() int64 {
	recs := h.records()
	if len(recs) == 0 {
		return 0
	}
	return recs[len(recs)-1].Seq
}

type row struct {
	name  string
	setup func(h *harness) // the prior state (nil: connect)
	event func(h *harness) // the event or tick the row is about
	facts []string         // the facts the event records
	then  func(h *harness) // later steps (nil: none)
	later []string         // the facts recorded from the event through then
	notes int              // the notifications shown from the event on
	check func(t *testing.T, h *harness)
}

func connect(h *harness) { h.connect() }

func tick(d time.Duration) func(h *harness) { return func(h *harness) { h.tick(d) } }

func steps(fs ...func(h *harness)) func(h *harness) {
	return func(h *harness) {
		for _, f := range fs {
			f(h)
		}
	}
}

const minute = time.Minute

func eq(t *testing.T, got, want []string, what string) {
	t.Helper()
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("%s = %q, want %q", what, got, want)
	}
}

// The work job carries the worker's role texts, so the worker reads them once (in the job) instead of spending
// turns on the files, and a role text too big to carry is named for the worker to read.

// roleFixture is a project root whose config puts the agents docs in doc/roles, and the obor kit it is dispatched from:
// a worker prompt template and the kit's worker role text (kitRole).
func roleFixture(t *testing.T, kitRole, doc string) (*proj, string) {
	t.Helper()
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".obor"), 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := `{"version":2,"project":{"name":"pj"},"agents":{"docs_dir":"doc/roles"}}`
	if err := os.WriteFile(filepath.Join(root, ".obor", "config.json"), []byte(cfg), 0o644); err != nil {
		t.Fatal(err)
	}
	if doc != "" {
		writeRoleFile(t, filepath.Join(root, "doc/roles/worker.md"), doc)
	}
	kit := t.TempDir()
	writeRoleFile(t, filepath.Join(kit, "templates", "worker-prompt.txt"), "Implement {{task}} on {{base}}.\n"+
		"Report: {{done_sections}}.\n")
	writeRoleFile(t, filepath.Join(kit, "templates", "agents", "worker.md"), kitRole)
	return &proj{root: root}, kit
}

// writeRoleFile writes text to path, creating its directories.
func writeRoleFile(t *testing.T, path, text string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
}
