package app

import (
	"context"
	"encoding/json"
	"errors"
	"obor/internal/app/event"
	"obor/internal/app/port"
	"obor/internal/app/wire"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"obor/internal/store"
)

// T-730: a review's start, a fix round's verify and a brief to the deep dive pass one serve-owned admission point: they
// wait while a publish runs (the head they read is the published one), and once admitted no publish starts until
// their brief is recorded. T-853: there serve moves the role's worktree to the head it hands the role; the reviewer
// or the deep dive working holds no merge (the merge hold of T-718 is gone).

// admitCfg is seedRoles' config with paths.reviews.
const admitCfg = `{"name":"pj","prefix":"pj","roles":{"integrator":"","reviewer":"","deep_dive":""},"paths":{"reviews":"reviews"}}`

// seedRoles is seedSched's project with a reviewer (pane w1:rev) and a deep dive (pane w1:dd), both idle in their
// role worktrees, and T-1 accepted.
func seedRoles(t *testing.T) (*harness, *schedScratch) {
	t.Helper()
	h := newHarness(t)
	h.write(filepath.Join(h.root, ".obor", "config.json"),
		`{"name":"pj","prefix":"pj","roles":{"integrator":"","reviewer":"","deep_dive":""}}`)
	h.snap.Panes = append(h.snap.Panes,
		port.Pane{ID: "w1:rev", Workspace: "w1", Cwd: filepath.Join(h.wt, "reviewer"), Agent: "claude", Label: "pj-rev", Status: "idle"},
		port.Pane{ID: "w1:dd", Workspace: "w1", Cwd: filepath.Join(h.wt, "deep_dive"), Agent: "claude", Label: "pj-deep-dive", Status: "idle"})
	f := &schedScratch{h: h}
	h.a.opt.Loop = func(string) (*Loop, error) { return &Loop{Scratch: f, Agents: f, Typer: f}, nil }
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
	s.git.merges = append(s.git.merges, c.SHA+" Merge "+c.Task+": x")
	s.git.mu.Unlock()
	return port.PublishOutcome{Kind: port.PublishMerged}, nil
}

// admitRig is seedRoles' project (T-1 accepted, candidate candidate1; reviewer pj-rev and deep dive pj-deep-dive idle)
// with paths.reviews, RV-5 open (its file written in the main checkout), its review git (head base1) and a gated publish.
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
	r := &admitRig{h: h, git: &reviewGitFake{head: "base1", last: "RV-4 closed · 1111111 · approved · T-9"}, agents: &agentsSpy{}}
	r.s = &gatedScratch{schedScratch: f, git: r.git, began: make(chan string, 4)}
	if gated {
		r.s.release = make(chan struct{})
	}
	r.fx = &Loop{Scratch: r.s, Agents: r.agents, Typer: f, Review: r.git}
	h.a.opt.Loop = func(string) (*Loop, error) { return r.fx, nil }
	return r
}

// verify is a fix round's verify of tasks of RV-5 (requestVerify, as fixPass calls it; T-869: the only asker), then
// one admission pass.
func (r *admitRig) verify(tasks ...string) {
	r.h.t.Helper()
	if _, err := r.h.a.requestVerify(r.h.a.p, ReviewData{ID: "RV-5", Tasks: tasks, By: "obor"}); err != nil {
		r.h.t.Fatal(err)
	}
	r.h.a.verifyPass(context.Background(), r.h.a.p, r.fx)
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

// recSeq is the seq of the first record of task with step and kind (0: none).
func recSeq(t *testing.T, h *harness, task, step, kind string) int64 {
	t.Helper()
	for _, rec := range readLog(t, h.root) {
		if rec.Task == task && rec.Step == step && rec.Kind == kind {
			return rec.Seq
		}
	}
	return 0
}

// startedHead is the head the log's review.started names ("": none).
func startedHead(t *testing.T, h *harness) string {
	t.Helper()
	for _, rec := range readLog(t, h.root) {
		if rec.Task == event.ReviewTask && rec.Step == event.StepReviewStarted {
			var d ReviewData
			_ = json.Unmarshal(rec.Data, &d)
			return d.Head
		}
	}
	return ""
}

// A review request, now or after T-1, while T-1's publish runs: the review waits, then starts on the published head,
// the reviewer's worktree moved to it first.
func TestAdmitReviewWaitsForPublish(t *testing.T) {
	t.Parallel()
	for _, after := range [][]string{nil, {"T-1"}} {
		t.Run(map[bool]string{true: "now", false: "after T-1"}[after == nil], func(t *testing.T) {
			t.Parallel()
			r := newAdmitRig(t, true)
			h, ctx := r.h, context.Background()
			if _, err := h.a.reviewCall(wire.Call{Root: h.root, CallID: "r1", Role: "lead", Agent: "pj-lead", After: after}); err != nil {
				t.Fatal(err)
			}
			r.publishing(ctx) // the same pass's reviewPass ran with the publish in flight
			if len(r.agents.launches) != 0 || r.git.detachedAt() != "" {
				t.Fatalf("publish running: %d launches, detached %q; want the review waiting",
					len(r.agents.launches), r.git.detachedAt())
			}
			r.released(ctx)
			if got := h.state("T-1"); got != stMerged {
				t.Fatalf("released: T-1 %s, want merged", got)
			}
			if len(r.agents.launches) != 1 || startedHead(t, h) != "candidate1" || r.git.detachedAt() != "reviewer@candidate1" {
				t.Errorf("after the publish: %d launches, started on %q, detached %q; want one start on candidate1, the"+
					" reviewer's worktree there", len(r.agents.launches), startedHead(t, h), r.git.detachedAt())
			}
		})
	}
}

// A verify of T-1 requested while T-1's publish runs: not admitted then; the pass after the publish reads the published
// head (T-1's merge is on it) and moves the reviewer's worktree there; read before, T-1 would have no merge.
func TestAdmitVerifyWaitsForPublish(t *testing.T) {
	t.Parallel()
	r := newAdmitRig(t, true)
	h, ctx := r.h, context.Background()
	r.publishing(ctx)
	r.verify("T-1")
	if moved, b := r.git.detachedAt(), readBriefLine(t, h, "pj-rev"); moved != "" || b != "" {
		t.Fatalf("verify detached %q, briefed %q while the publish ran", moved, b)
	}
	r.released(ctx)
	if moved := r.git.detachedAt(); moved != "reviewer@candidate1" {
		t.Errorf("detached %q; want the reviewer's worktree at the published head candidate1", moved)
	}
	h.a.mu.Lock()
	b := h.a.p.out.brief("pj-rev")
	h.a.mu.Unlock()
	if b == nil {
		t.Fatal("no brief open to the reviewer")
	}
	brief := readBriefLine(t, h, "pj-rev")
	if !strings.Contains(brief, "fixed in T-1 (candida); main is at candida (your worktree too).") ||
		!strings.Contains(brief, "Write reviews/RV-5-verify.md in the main checkout "+h.root+".") {
		t.Errorf("the verify brief: %q", brief)
	}
}

// readBriefLine is the line of the last brief to agent in the log.
func readBriefLine(t *testing.T, h *harness, agent string) string {
	t.Helper()
	line := ""
	for _, rec := range readLog(t, h.root) {
		var m event.Message
		if rec.Step == event.StepBriefed && json.Unmarshal(rec.Data, &m) == nil && m.To == agent {
			line = m.Line
		}
	}
	return line
}

// A brief to the deep dive while T-1's publish runs is recorded only after the publish's result, its idle deep dive's
// worktree moved to the published head first.
func TestAdmitDeepDiveBriefWaitsForPublish(t *testing.T) {
	t.Parallel()
	r := newAdmitRig(t, true)
	h, ctx := r.h, context.Background()
	r.publishing(ctx)
	done := make(chan error, 1)
	go func() {
		_, err := h.a.briefCall(wire.Call{Root: h.root, CallID: "b1", Role: "lead", Agent: "pj-lead", To: "pj-deep-dive", Note: "look at x"})
		done <- err
	}()
	if !waiting(done) || r.git.detachedAt() != "" {
		t.Fatalf("the deep dive's brief answered or its worktree moved (%q) while the publish ran", r.git.detachedAt())
	}
	r.released(ctx)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	pub := recSeq(t, h, "T-1", event.StepPublish, event.KindResult)
	brief := recSeq(t, h, event.MsgTask, event.StepBriefed, event.KindFact)
	if pub == 0 || brief == 0 || brief < pub {
		t.Errorf("publish result seq %d, brief seq %d; want the brief after the publish", pub, brief)
	}
	if got := r.git.detachedAt(); got != "deep_dive@candidate1" {
		t.Errorf("detached %q, want the deep dive's worktree at the published head", got)
	}
}

// T-853: a brief moves only an idle deep dive's worktree: a working one (its pane working, or a brief open) keeps its
// head for its turn, and a plain brief to the reviewer never moves the reviewer's (its head is the reviewed one). A
// worktree's git failing refuses the brief, nothing recorded.
func TestBriefMovesAnIdleDeepDive(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		name, to, moved string
		prep            func(r *admitRig)
	}{
		{"idle deep dive", "pj-deep-dive", "deep_dive@base1", func(*admitRig) {}},
		{"working deep dive", "pj-deep-dive", "", func(r *admitRig) { r.h.work("w1:dd", true) }},
		// The first brief (idle) moved it; the second, with that brief open, moves nothing more.
		{"deep dive with a brief open", "pj-deep-dive", "deep_dive@base1", func(r *admitRig) {
			if _, err := r.h.a.briefCall(wire.Call{Root: r.h.root, CallID: "b0", Role: "lead", To: "pj-deep-dive", Note: "first"}); err != nil {
				r.h.t.Fatal(err)
			}
		}},
		{"reviewer", "pj-rev", "", func(*admitRig) {}},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			r := newAdmitRig(t, false)
			h := r.h
			c.prep(r)
			if _, err := h.a.briefCall(wire.Call{Root: h.root, CallID: "b1", Role: "lead", To: c.to, Note: "look at x"}); err != nil {
				t.Fatal(err)
			}
			if got := r.git.detachedAt(); got != c.moved {
				t.Errorf("detached %q, want %q", got, c.moved)
			}
			if readBriefLine(t, h, c.to) != "[obor lead] look at x" && readBriefLine(t, h, c.to) != "[obor owner] look at x" {
				t.Errorf("the brief was not recorded: %q", readBriefLine(t, h, c.to))
			}
		})
	}
	t.Run("git fails", func(t *testing.T) {
		t.Parallel()
		r := newAdmitRig(t, false)
		h := r.h
		r.git.detachErr = errors.New("no worktree")
		if _, err := h.a.briefCall(wire.Call{Root: h.root, CallID: "b1", Role: "lead", To: "pj-deep-dive", Note: "look at x"}); err == nil ||
			!strings.Contains(err.Error(), "pj-deep-dive's worktree: no worktree") {
			t.Fatalf("brief: %v, want refused naming the worktree", err)
		}
		if got := readBriefLine(t, h, "pj-deep-dive"); got != "" {
			t.Errorf("a refused brief was recorded: %q", got)
		}
	})
}

// T-853: the reviewer or the deep dive working — its pane working, a brief open to it — holds no merge: T-1 publishes
// in the same pass, and obor status shows no hold phase.
func TestMergeDuringAReviewPublishesAtOnce(t *testing.T) {
	t.Parallel()
	for _, c := range []struct{ name, pane, to string }{
		{"reviewer working", "w1:rev", ""}, {"deep dive working", "w1:dd", ""},
		{"reviewer briefed", "", "pj-rev"}, {"deep dive briefed", "", "pj-deep-dive"},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			h, _ := seedRoles(t)
			ctx := context.Background()
			if c.pane != "" {
				h.work(c.pane, true)
			}
			if c.to != "" {
				h.a.opt.Loop = func(string) (*Loop, error) {
					return &Loop{Scratch: &schedScratch{h: h}, Agents: &agentsSpy{}, Review: &reviewGitFake{head: "base1"}}, nil
				}
				if _, err := h.a.briefCall(wire.Call{Root: h.root, CallID: "b1", To: c.to, Note: "look", Role: "lead"}); err != nil {
					t.Fatal(err)
				}
				f := &schedScratch{h: h}
				h.a.opt.Loop = func(string) (*Loop, error) { return &Loop{Scratch: f, Agents: f, Typer: f}, nil }
			}
			h.a.mu.Lock()
			briefed := c.to != "" && h.a.p.out.brief(c.to) != nil
			working := c.pane != "" && !h.a.roleIdle(h.a.p, map[string]string{"w1:rev": "pj-rev", "w1:dd": "pj-deep-dive"}[c.pane])
			h.a.mu.Unlock()
			if !briefed && !working {
				t.Fatalf("precondition: the role neither briefed nor working")
			}
			if got := h.phase("T-1"); got != wire.PhasePublish {
				t.Errorf("accepted, %s: phase %q, want %q (no merge hold)", c.name, got, wire.PhasePublish)
			}
			h.pass(ctx)
			if got := h.state("T-1"); got != stMerged {
				t.Errorf("%s: T-1 %s after one pass, want merged", c.name, got)
			}
		})
	}
}

// A publish the loop picks between the head's read and the brief's record (a verify's, a review start's) waits: it
// does not begin while admitted; once the brief is recorded it publishes at once (T-853: no hold after).
func TestAdmitPublishWaitsForTheBrief(t *testing.T) {
	t.Parallel()
	for _, which := range []string{"verify", "review start"} {
		t.Run(which, func(t *testing.T) {
			t.Parallel()
			r := newAdmitRig(t, false)
			h, ctx := r.h, context.Background()
			r.git.merges = []string{"aaaaaaa1 Merge T-0: x"}
			if which == "review start" {
				if _, err := h.a.reviewCall(wire.Call{Root: h.root, CallID: "r1", Role: "lead", Agent: "pj-lead"}); err != nil {
					t.Fatal(err)
				}
			}
			began := -1
			r.git.onHead = func() { // the loop's pass inside the admission, between the head's read and the brief
				h.a.advance(ctx)
				h.a.effects.Wait()
				began = len(r.s.began)
			}
			if which == "verify" {
				r.verify("T-0")
			} else {
				h.a.reviewPass(ctx, h.a.p, r.fx)
				if len(r.agents.launches) != 1 {
					t.Fatalf("precondition: the review did not start: %+v", r.agents.launches)
				}
			}
			if began != 0 {
				t.Fatalf("publishes begun inside the admission: %d (-1: the head was never read)", began)
			}
			h.pass(ctx)
			if got := h.state("T-1"); got != stMerged || len(r.s.began) != 1 {
				t.Errorf("after the brief: T-1 %s, %d publishes; want merged at once, the reviewer's brief open", got,
					len(r.s.began))
			}
		})
	}
}

// The role worktree agents are the current config's: a deep dive added or a role removed while serve runs counts at
// the next check; a config that does not load keeps the last good set.
func TestWorktreeAgentsFollowTheConfig(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name, from, to, agent string
		before, after         string // the agent's role worktree role before and after the change
	}{
		{"deep dive added", `{"name":"pj","prefix":"pj","roles":{"integrator":"","reviewer":""}}`, admitCfg,
			"pj-deep-dive", "", "deep_dive"},
		{"role removed", admitCfg, `{"name":"pj","prefix":"pj","roles":{"integrator":"","deep_dive":""}}`,
			"pj-rev", "reviewer", ""},
		{"config broken", admitCfg, `{"name":`, "pj-rev", "reviewer", "reviewer"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			h, _ := seedRoles(t)
			h.write(filepath.Join(h.root, ".obor", "config.json"), c.from)
			role := func() string {
				h.a.mu.Lock()
				defer h.a.mu.Unlock()
				return h.a.p.wtRole(c.agent)
			}
			if got := role(); got != c.before {
				t.Fatalf("precondition: %s's role %q, want %q", c.agent, got, c.before)
			}
			h.write(filepath.Join(h.root, ".obor", "config.json"), c.to)
			if got := role(); got != c.after {
				t.Errorf("after the change: %s's role %q, want %q", c.agent, got, c.after)
			}
		})
	}
}

// One interleaving across the loop, a review start and a brief: T-1's publish runs while a review request is due and
// a brief to the deep dive comes in. Neither starts on the old head: the publish's result comes first, the review
// starts on candidate1, and the brief is recorded after the publish.
func TestAdmitInterleaving(t *testing.T) {
	t.Parallel()
	r := newAdmitRig(t, true)
	h, ctx := r.h, context.Background()
	if _, err := h.a.reviewCall(wire.Call{Root: h.root, CallID: "r1", Role: "lead", Agent: "pj-lead"}); err != nil {
		t.Fatal(err)
	}
	r.publishing(ctx)
	done := make(chan error, 1)
	go func() {
		_, err := h.a.briefCall(wire.Call{Root: h.root, CallID: "b1", Role: "lead", Agent: "pj-lead", To: "pj-deep-dive", Note: "look at x"})
		done <- err
	}()
	if !waiting(done) || len(r.agents.launches) != 0 {
		t.Fatalf("publish running: brief answered or %d review starts", len(r.agents.launches))
	}
	r.released(ctx)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	pub := recSeq(t, h, "T-1", event.StepPublish, event.KindResult)
	started := recSeq(t, h, event.ReviewTask, event.StepReviewStarted, event.KindFact)
	brief := recSeq(t, h, event.MsgTask, event.StepBriefed, event.KindFact)
	if pub == 0 || started < pub || brief < pub || startedHead(t, h) != "candidate1" {
		t.Errorf("seqs: publish %d, review started %d (on %q), brief %d; want both after the publish, on candidate1",
			pub, started, startedHead(t, h), brief)
	}
}

// selectPublish is the loop's step for T-1 (accepted, its publish due), selected under mu as drive does; it is run
// later, after the caller's interleaving.
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

// T-733: an admission in force between the publish's selection and its execution refuses the publish's start
// (scratchStep decides at the start, not pickAccepted at the selection); released, the next pass publishes. A brief
// recorded and released in the gap holds nothing (T-853).
func TestAdmissionDecidedAtPublishStart(t *testing.T) {
	t.Parallel()
	t.Run("admitted", func(t *testing.T) {
		t.Parallel()
		r := newAdmitRig(t, false)
		h, ctx := r.h, context.Background()
		s := r.selectPublish()
		release, err := h.a.admit(ctx, h.a.p)
		if err != nil {
			t.Fatal(err)
		}
		s(ctx, r.fx)
		h.a.effects.Wait()
		if n := len(r.s.began); n != 0 || countStep(t, h, nth(event.StepPublish, 1)) != 0 || h.state("T-1") != stAccepted {
			t.Fatalf("publish T-1 started while admitted: %d publishes, T-1 %s", n, h.state("T-1"))
		}
		release()
		h.pass(ctx)
		if got := h.state("T-1"); got != stMerged {
			t.Errorf("released: T-1 %s, want merged", got)
		}
	})
	for _, c := range []struct {
		name string
		gap  func(t *testing.T, r *admitRig)
	}{
		{"deep dive brief", func(t *testing.T, r *admitRig) {
			if _, err := r.h.a.briefCall(wire.Call{Root: r.h.root, CallID: "b1", Role: "lead", Agent: "pj-lead", To: "pj-deep-dive", Note: "look at x"}); err != nil {
				t.Fatal(err)
			}
		}},
		{"verify", func(t *testing.T, r *admitRig) {
			r.git.merges = []string{"aaaaaaa1 Merge T-0: x"}
			r.verify("T-0")
		}},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			r := newAdmitRig(t, false)
			h, ctx := r.h, context.Background()
			s := r.selectPublish()
			c.gap(t, r)
			h.a.mu.Lock()
			admits, briefs := h.a.p.admits, len(h.a.p.out.briefs)
			h.a.mu.Unlock()
			if admits != 0 || briefs == 0 {
				t.Fatalf("precondition: admits %d, %d briefs open; want the admission released, a brief open", admits, briefs)
			}
			s(ctx, r.fx)
			h.a.effects.Wait()
			h.pass(ctx)
			if n := len(r.s.began); n != 1 || h.state("T-1") != stMerged {
				t.Errorf("%s in the gap: %d publishes, T-1 %s; want the selected publish run, merged", c.name, n, h.state("T-1"))
			}
		})
	}
}
