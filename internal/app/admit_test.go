package app

import (
	"context"
	"encoding/json"
	"obor/internal/app/event"
	"obor/internal/app/port"
	"obor/internal/app/wire"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// T-730: a review's start, obor review verify and a brief to the deep dive pass one serve-owned admission point: they
// wait while a publish runs (the head they read is the published one), and once admitted no publish starts until
// their brief is recorded. The hold's agents come from the current config at each check.

// admitCfg is seedHold's config with paths.reviews.
const admitCfg = `{"name":"pj","prefix":"pj","roles":{"integrator":"","reviewer":"","deep_dive":""},"paths":{"reviews":"reviews"}}`

// headGit is a ReviewGit whose head is where the last publish moved the integration branch (start: base1), the
// merges the publishes made; onHead runs once, inside the next Head read.
type headGit struct {
	mu     sync.Mutex
	head   string
	merges []string
	marked []string
	onHead func()
}

func (g *headGit) Last(context.Context, string) (ReviewClose, error) {
	return ReviewClose{Subject: "RV-4 closed · 1111111 · approved · T-9"}, nil
}
func (g *headGit) Head(context.Context, string) (string, error) {
	g.mu.Lock()
	hook := g.onHead
	g.onHead = nil
	g.mu.Unlock()
	if hook != nil {
		hook()
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.head, nil
}
func (g *headGit) Merges(context.Context, string, string) ([]string, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	return append([]string(nil), g.merges...), nil
}
func (g *headGit) Files(context.Context, string, string) ([]string, error) { return nil, nil }
func (g *headGit) Mark(_ context.Context, sha string) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.marked = append(g.marked, sha)
	return nil
}

// markedHeads is every Mark's sha so far.
func (g *headGit) markedHeads() string {
	g.mu.Lock()
	defer g.mu.Unlock()
	return strings.Join(g.marked, ",")
}

// gatedScratch is schedScratch whose Publish reports its task on began, then waits for release (nil: no wait) before
// it moves the head.
type gatedScratch struct {
	*schedScratch
	git     *headGit
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

// admitRig is seedHold's project (T-1 accepted, candidate candidate1; reviewer pj-rev and deep dive pj-deep-dive idle)
// with paths.reviews, RV-5 open (its file written), headGit and a gated publish.
type admitRig struct {
	h      *harness
	git    *headGit
	s      *gatedScratch
	agents *agentsSpy
	fx     *Loop
}

func newAdmitRig(t *testing.T, gated bool) *admitRig {
	t.Helper()
	h, f := seedHold(t)
	h.write(filepath.Join(h.root, ".obor", "config.json"), admitCfg)
	if err := os.MkdirAll(filepath.Join(h.root, "reviews"), 0o755); err != nil {
		t.Fatal(err)
	}
	h.write(filepath.Join(h.root, "reviews", "RV-5.md"), "# RV-5\n")
	r := &admitRig{h: h, git: &headGit{head: "base1"}, agents: &agentsSpy{}}
	r.s = &gatedScratch{schedScratch: f, git: r.git, began: make(chan string, 4)}
	if gated {
		r.s.release = make(chan struct{})
	}
	r.fx = &Loop{Scratch: r.s, Agents: r.agents, Typer: f, Review: r.git}
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

// A review request, now or after T-1, while T-1's publish runs: the review waits, then starts on the published head.
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
			if len(r.agents.launches) != 0 || r.git.markedHeads() != "" {
				t.Fatalf("publish running: %d launches, marked %q; want the review waiting", len(r.agents.launches), r.git.markedHeads())
			}
			r.released(ctx)
			if got := h.state("T-1"); got != stMerged {
				t.Fatalf("released: T-1 %s, want merged", got)
			}
			if len(r.agents.launches) != 1 || r.git.markedHeads() != "candidate1" || startedHead(t, h) != "candidate1" {
				t.Errorf("after the publish: %d launches, marked %q, started on %q; want one start on candidate1",
					len(r.agents.launches), r.git.markedHeads(), startedHead(t, h))
			}
		})
	}
}

// obor review verify of T-1 while T-1's publish runs: the call waits, then reads the published head (T-1's merge is
// on it) and marks it; read before, T-1 would have no merge.
func TestAdmitVerifyWaitsForPublish(t *testing.T) {
	t.Parallel()
	r := newAdmitRig(t, true)
	h, ctx := r.h, context.Background()
	r.publishing(ctx)
	type answer struct {
		reply string
		err   error
	}
	done := make(chan answer, 1)
	go func() {
		reply, err := h.a.verifyCall(wire.Call{Root: h.root, CallID: "v1", Role: "lead", Agent: "pj-lead", After: []string{"T-1"}})
		done <- answer{reply, err}
	}()
	if !waiting(done) {
		t.Fatal("verify answered while the publish ran")
	}
	if got := r.git.markedHeads(); got != "" {
		t.Fatalf("verify marked %q while the publish ran", got)
	}
	r.released(ctx)
	a := <-done
	if a.err != nil || !strings.HasPrefix(a.reply, "BRIEFED pj-rev") {
		t.Fatalf("verify: %q, %v", a.reply, a.err)
	}
	if got := r.git.markedHeads(); got != "candidate1" {
		t.Errorf("marked %q, want the published head candidate1", got)
	}
	h.a.mu.Lock()
	b := h.a.p.out.brief("pj-rev")
	h.a.mu.Unlock()
	if b == nil {
		t.Fatal("no brief open to the reviewer")
	}
	brief := readBriefLine(t, h, "pj-rev")
	if !strings.Contains(brief, "fixed in T-1 (candida); main is at candida.") || !strings.Contains(brief, "Write reviews/RV-5-verify.md") {
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

// A brief to the deep dive while T-1's publish runs is recorded only after the publish's result.
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
	if !waiting(done) {
		t.Fatal("the deep dive's brief answered while the publish ran")
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
}

// A publish the loop picks between the head's read and the brief's record (a verify's, a review start's) waits: it
// does not begin while admitted, and the open brief holds it after.
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
				h.a.mu.Lock()
				held := h.a.holder(h.a.p)
				h.a.mu.Unlock()
				if held != "" {
					t.Errorf("precondition: %s holds the merge already: the pass could not publish anyway", held)
				}
				h.a.advance(ctx)
				h.a.effects.Wait()
				began = len(r.s.began)
			}
			if which == "verify" {
				if _, err := h.a.verifyCall(wire.Call{Root: h.root, CallID: "v1", Role: "lead", Agent: "pj-lead", After: []string{"T-0"}}); err != nil {
					t.Fatal(err)
				}
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
			if got := h.state("T-1"); got != stAccepted || h.taskHold("T-1") != "merge hold: reviewer" || len(r.s.began) != 0 {
				t.Errorf("after the brief: T-1 %s, hold %q, %d publishes; want accepted, held by the reviewer", got,
					h.taskHold("T-1"), len(r.s.began))
			}
		})
	}
}

// The hold's agents are the current config's: a deep dive added or a role removed while serve runs counts at the next
// check; a config that does not load keeps the last good set.
func TestHoldAgentsFollowTheConfig(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name, from, to, pane string
		before, after        string // the holder before and after the change
		merged               bool   // T-1 merges after the change
	}{
		{"deep dive added", `{"name":"pj","prefix":"pj","roles":{"integrator":"","reviewer":""}}`, admitCfg,
			"w1:dd", "", "deep_dive", false},
		{"role removed", admitCfg, `{"name":"pj","prefix":"pj","roles":{"integrator":"","deep_dive":""}}`,
			"w1:rev", "reviewer", "", true},
		{"config broken", admitCfg, `{"name":`, "w1:rev", "reviewer", "reviewer", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			h, _ := seedHold(t)
			ctx := context.Background()
			h.write(filepath.Join(h.root, ".obor", "config.json"), c.from)
			h.work(c.pane, true)
			holder := func() string {
				h.a.mu.Lock()
				defer h.a.mu.Unlock()
				h.a.reconcile(h.a.p)
				return h.a.holder(h.a.p)
			}
			if got := holder(); got != c.before {
				t.Fatalf("precondition: holder %q, want %q", got, c.before)
			}
			h.write(filepath.Join(h.root, ".obor", "config.json"), c.to)
			if got := holder(); got != c.after {
				t.Errorf("after the change: holder %q, want %q", got, c.after)
			}
			h.pass(ctx)
			if got, want := h.state("T-1"), map[bool]string{true: stMerged, false: stAccepted}[c.merged]; got != want {
				t.Errorf("after the change: T-1 %s, want %s", got, want)
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

// T-733: a brief admitted, recorded and released between the publish's selection and its execution holds the publish
// (scratchStep decides the hold at the start, not pickAccepted at the selection); the next pass picks it again.
func TestHoldDecidedAtPublishStart(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name, holder string
		gap          func(t *testing.T, r *admitRig)
	}{
		{"deep dive brief", "deep_dive", func(t *testing.T, r *admitRig) {
			if _, err := r.h.a.briefCall(wire.Call{Root: r.h.root, CallID: "b1", Role: "lead", Agent: "pj-lead", To: "pj-deep-dive", Note: "look at x"}); err != nil {
				t.Fatal(err)
			}
		}},
		{"reviewer brief", "reviewer", func(t *testing.T, r *admitRig) {
			if _, err := r.h.a.briefCall(wire.Call{Root: r.h.root, CallID: "b1", Role: "lead", Agent: "pj-lead", To: "pj-rev", Note: "look at y"}); err != nil {
				t.Fatal(err)
			}
		}},
		{"verify", "reviewer", func(t *testing.T, r *admitRig) {
			r.git.merges = []string{"aaaaaaa1 Merge T-0: x"}
			if _, err := r.h.a.verifyCall(wire.Call{Root: r.h.root, CallID: "v1", Role: "lead", Agent: "pj-lead", After: []string{"T-0"}}); err != nil {
				t.Fatal(err)
			}
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			r := newAdmitRig(t, false)
			h, ctx := r.h, context.Background()
			s := r.selectPublish()
			c.gap(t, r)
			h.a.mu.Lock()
			admits, holder := h.a.p.admits, h.a.holder(h.a.p)
			h.a.mu.Unlock()
			if admits != 0 || holder != c.holder {
				t.Fatalf("precondition: admits %d, holder %q; want the admission released, %s holding", admits, holder, c.holder)
			}
			s(ctx, r.fx)
			h.a.effects.Wait()
			if n := len(r.s.began); n != 0 || countStep(t, h, nth(event.StepPublish, 1)) != 0 || h.state("T-1") != stAccepted {
				t.Fatalf("publish T-1 started despite the %s's open brief: %d publishes, %d records, T-1 %s", c.holder, n,
					countStep(t, h, nth(event.StepPublish, 1)), h.state("T-1"))
			}
			if got, want := h.taskHold("T-1"), "merge hold: "+c.holder; got != want {
				t.Errorf("status hold = %q, want %q", got, want)
			}
		})
	}
}

// T-733: with no brief in the gap the selected publish starts; a hold past holdCap still lets the selected publish go,
// with the lead's one line.
func TestHoldAtPublishStartGoesAhead(t *testing.T) {
	t.Parallel()
	t.Run("no brief", func(t *testing.T) {
		t.Parallel()
		r := newAdmitRig(t, false)
		h, ctx := r.h, context.Background()
		r.selectPublish()(ctx, r.fx)
		h.a.effects.Wait()
		if n := len(r.s.began); n != 1 || countStep(t, h, nth(event.StepPublish, 1)) == 0 {
			t.Fatalf("no brief: %d publishes begun, %d records; want the publish started", n, countStep(t, h, nth(event.StepPublish, 1)))
		}
		h.pass(ctx)
		if got := h.state("T-1"); got != stMerged {
			t.Errorf("no brief: T-1 %s, want merged", got)
		}
	})
	t.Run("cap", func(t *testing.T) {
		t.Parallel()
		r := newAdmitRig(t, false)
		h, ctx := r.h, context.Background()
		h.work("w1:rev", true)
		h.pass(ctx) // the hold's clock starts
		if got := h.state("T-1"); got != stAccepted || len(r.s.began) != 0 {
			t.Fatalf("precondition: T-1 %s, %d publishes; want held", got, len(r.s.began))
		}
		h.now = h.now.Add(holdCap)
		s := r.selectPublish()
		if got := h.leadLinesWith("went ahead"); len(got) != 0 {
			t.Fatalf("the cap's line before the publish's start: %q", got)
		}
		s(ctx, r.fx)
		h.a.effects.Wait()
		h.pass(ctx)
		if n := len(r.s.began); n != 1 || h.state("T-1") != stMerged {
			t.Fatalf("30m held: %d publishes, T-1 %s; want one, merged", n, h.state("T-1"))
		}
		want := []string{"[obor] merge of T-1 went ahead: reviewer still working after 30m"}
		if got := h.leadLinesWith("went ahead"); strings.Join(got, "|") != strings.Join(want, "|") {
			t.Errorf("lead lines = %q, want %q", got, want)
		}
	})
}
