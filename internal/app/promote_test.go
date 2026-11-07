package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"obor/internal/app/event"
	"obor/internal/app/port"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"obor/internal/config"
	"obor/internal/store"
)

// T-974: the promotion of branches.main (staging) to branches.promote (main): the trigger by weight and by idle, the
// full suite's run, the fast-forward to exactly R, the last closed review's reviewed head (T-978), after_promote, RED's
// needs-you, a restart mid-run, and after_merge off per publish.

// promoteGit is a ReviewGit of named shas: heads by branch, the first-parent merges of "<from>..<to>" (none when not
// set; from "" is all of to's, as git's: those of "h0..<to>", h0 being the rig's first commit with no merge before it)
// and the newest close commit (last, lastBody). Head of a branch it has no sha for is an error.
type promoteGit struct {
	mu             sync.Mutex
	heads          map[string]string
	merges         map[string][]string
	last, lastBody string
}

func (g *promoteGit) Last(context.Context, string) (ReviewClose, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	return ReviewClose{Subject: g.last, Body: g.lastBody}, nil
}
func (g *promoteGit) Head(_ context.Context, branch string) (string, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if sha, ok := g.heads[branch]; ok {
		return sha, nil
	}
	return "", errors.New("no branch " + branch)
}
func (g *promoteGit) Merges(_ context.Context, from, to string) ([]string, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if ms, ok := g.merges[from+".."+to]; ok || from != "" {
		return slices.Clone(ms), nil
	}
	return slices.Clone(g.merges["h0.."+to]), nil
}
func (g *promoteGit) Files(context.Context, string, string) ([]string, error) { return nil, nil }
func (g *promoteGit) Detach(context.Context, string, string) error            { return nil }
func (g *promoteGit) Close(context.Context, ReviewCommit) error               { return nil }

// set sets branch's head and the merges from..to (the merges' tasks as "Merge T-x: …" subjects).
func (g *promoteGit) set(branch, sha string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.heads[branch] = sha
}

func (g *promoteGit) between(from, to string, tasks ...string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	var ms []string
	for i, t := range tasks {
		ms = append(ms, fmt.Sprintf("m%d%s Merge %s: work", i, to, t))
	}
	g.merges[from+".."+to] = ms
}

func (g *promoteGit) head(branch string) string {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.heads[branch]
}

// promoteFake is the Loop's Promote on g: each sha's verdict (verdicts; none: GREEN; "fail": an error), every run's,
// fast-forward's and after_promote's sha in order; block, when set, holds a run until its ctx ends.
type promoteFake struct {
	mu       sync.Mutex
	g        *promoteGit
	verdicts map[string]string
	runs     []string
	ffs      []string
	afters   []string
	block    bool
	began    chan struct{}
}

func (f *promoteFake) promote(weight int) *Promote {
	var cfg *config.Config // the default weights
	return &Promote{Branch: "main", Target: "staging", Weight: weight, Weigh: cfg.MergeWeight,
		Run: func(ctx context.Context, sha string) (port.PromoteOut, error) {
			f.mu.Lock()
			f.runs = append(f.runs, sha)
			v, block, began := f.verdicts[sha], f.block, f.began
			f.mu.Unlock()
			if block {
				if began != nil {
					began <- struct{}{}
				}
				<-ctx.Done()
				return port.PromoteOut{}, ctx.Err()
			}
			switch v {
			case "":
				return port.PromoteOut{SHA: sha, Verdict: "GREEN", Line: "check 10 passed"}, nil
			case "fail":
				return port.PromoteOut{}, errors.New("cannot reset the worktree")
			}
			return port.PromoteOut{SHA: sha, Verdict: v, Line: "check 1 failed — TestFlaky", Retried: true}, nil
		},
		FF: func(_ context.Context, sha string) (string, error) {
			f.mu.Lock()
			f.ffs = append(f.ffs, sha)
			f.mu.Unlock()
			f.g.set("main", sha)
			return "promoted main", nil
		},
		After: func(_ context.Context, sha, _ string) (string, error) {
			f.mu.Lock()
			f.afters = append(f.afters, sha)
			f.mu.Unlock()
			return "ran", nil
		}}
}

func (f *promoteFake) calls() (runs, ffs, afters []string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.runs), slices.Clone(f.ffs), slices.Clone(f.afters)
}

// promoteRig is a merging project (reviewer pj-rev; paths.reviews only when reviews is set) whose loop has a review git
// on staging at h1 and main at h0, and the promotion fake at weight 6.
type promoteRig struct {
	t   *testing.T
	a   *App
	p   *proj
	now time.Time
	git *promoteGit
	f   *promoteFake
	fx  *Loop
}

func newPromoteRig(t *testing.T, reviews bool) *promoteRig {
	t.Helper()
	root := lineRoot(t)
	if reviews {
		cfg := `{"name":"proj","prefix":"pj","roles":{"integrator":"","reviewer":""},"paths":{"reviews":"reviews"}}`
		if err := os.WriteFile(filepath.Join(root, ".obor/config.json"), []byte(cfg), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	r := &promoteRig{t: t, now: time.Date(2026, 11, 7, 12, 0, 0, 0, time.UTC),
		git: &promoteGit{heads: map[string]string{"staging": "h1", "main": "h0"}, merges: map[string][]string{}}}
	r.f = &promoteFake{g: r.git, verdicts: map[string]string{}}
	r.a, r.p = lineApp(t, root, &r.now)
	r.wire()
	return r
}

func (r *promoteRig) wire() {
	r.fx = &Loop{Review: r.git, Agents: &agentsSpy{}, Promote: r.f.promote(6)}
	r.a.opt.Loop = func(string) (*Loop, error) { return r.fx, nil }
}

// pass is one pass's promotion step, waited for until what it started has ended.
func (r *promoteRig) pass() {
	r.t.Helper()
	r.a.promotion(context.Background(), r.p, r.fx)
	r.a.effects.Wait()
}

// spec stores task's spec at level ("": no Difficulty line).
func (r *promoteRig) spec(task, level string) {
	r.t.Helper()
	text := "# " + task + ": work\n"
	if level != "" {
		text += "\nDifficulty: " + level + "\n"
	}
	if _, _, err := store.SetSpec(r.p.log.DB(), task, text, "", "lead", r.now); err != nil {
		r.t.Fatal(err)
	}
}

// busy puts a task between its submit and its merge: the loop is not idle.
func (r *promoteRig) busy() {
	r.a.mu.Lock()
	r.p.folded.loop["T-99"] = &mtask{task: "T-99", attempt: 1, state: stSubmitted}
	r.a.mu.Unlock()
}

// steps is the promotion's records as "<kind> <step>" lines.
func (r *promoteRig) steps() []string {
	recs, _ := event.ReadLog(r.p.root)
	var out []string
	for _, rec := range recs {
		if rec.Task == event.PromoteTask {
			out = append(out, rec.Kind+" "+rec.String())
		}
	}
	return out
}

// intent is the data of the promotion's intent step (fails when there is none).
func (r *promoteRig) intent(step string) promoteIn {
	r.t.Helper()
	recs, _ := event.ReadLog(r.p.root)
	for _, rec := range recs {
		if rec.Task == event.PromoteTask && rec.Kind == event.KindIntent && rec.Step == step {
			var in promoteIn
			if err := json.Unmarshal(rec.Data, &in); err != nil {
				r.t.Fatal(err)
			}
			return in
		}
	}
	r.t.Fatalf("no intent %s: %q", step, r.steps())
	return promoteIn{}
}

// needs is the promotion's needs-you reasons in force.
func (r *promoteRig) needs() []string {
	r.a.mu.Lock()
	defer r.a.mu.Unlock()
	var out []string
	for _, f := range r.p.folded.active[event.PromoteTask] {
		if f.Step == event.StepNeedsYou {
			out = append(out, f.Fact().Reason)
		}
	}
	return out
}

func TestPromoteTriggerByWeight(t *testing.T) {
	t.Parallel()
	t.Run("5 simple merges do not trigger at threshold 6 while the loop is busy", func(t *testing.T) {
		t.Parallel()
		r := newPromoteRig(t, false)
		r.busy()
		var tasks []string
		for i := 1; i <= 5; i++ {
			task := fmt.Sprintf("T-%d", i)
			r.spec(task, "simple")
			tasks = append(tasks, task)
		}
		r.git.between("h0", "h1", tasks...)
		r.pass()
		r.pass()
		if runs, _, _ := r.f.calls(); len(runs) != 0 || len(r.steps()) != 0 {
			t.Errorf("weight 5 < 6, loop busy: runs %q, records %q; want none", runs, r.steps())
		}
		if st := r.a.Status().Project.Promote; st == nil || st.Merges != 5 || st.SHA != "h0" {
			t.Errorf("status promote = %+v, want main at h0, 5 merges since", st)
		}
	})
	t.Run("2 hard merges trigger at threshold 6 while the loop is busy", func(t *testing.T) {
		t.Parallel()
		r := newPromoteRig(t, false)
		r.busy()
		r.spec("T-1", "hard")
		r.spec("T-2", "hard")
		r.git.between("h0", "h1", "T-1", "T-2")
		r.pass()
		runs, _, _ := r.f.calls()
		if len(runs) != 1 || runs[0] != "h1" {
			t.Fatalf("weight 6 >= 6: runs %q, want one at h1; records %q", runs, r.steps())
		}
		in := r.intent("promote.1")
		if in.SHA != "h1" || in.Since != "h0" || in.Weight != 6 || strings.Join(in.Merges, ",") != "T-1,T-2" {
			t.Errorf("intent promote.1 = %+v, want sha h1, since h0, weight 6, merges T-1,T-2", in)
		}
	})
	// Rev 3: the weight is only a trigger, never a cap: 2 medium then 1 hard all merge (the promotion holds no merge
	// back), and the third's merge triggers the promotion of all three at weight 7 over the threshold 6.
	t.Run("2 medium then 1 hard: all three merge, the promotion triggers after the third (7)", func(t *testing.T) {
		t.Parallel()
		r := newPromoteRig(t, false)
		r.busy()
		r.spec("T-1", "medium")
		r.spec("T-2", "medium")
		r.spec("T-3", "hard")
		r.git.between("h0", "h1", "T-1", "T-2")
		r.pass()
		if runs, _, _ := r.f.calls(); len(runs) != 0 {
			t.Fatalf("2 medium (4 < 6): runs %q, want none", runs)
		}
		r.a.mu.Lock()
		state := r.p.folded.loop["T-99"].state
		r.a.mu.Unlock()
		if state != stSubmitted {
			t.Fatalf("the promotion touched a task in the merge loop: T-99 %s", state)
		}
		r.git.set("staging", "h2")
		r.git.between("h0", "h2", "T-1", "T-2", "T-3")
		r.pass()
		runs, _, _ := r.f.calls()
		if len(runs) != 1 || runs[0] != "h2" {
			t.Fatalf("after the third merge (7 >= 6): runs %q, want one at h2", runs)
		}
		if in := r.intent("promote.1"); in.Weight != 7 || strings.Join(in.Merges, ",") != "T-1,T-2,T-3" || in.SHA != "h2" {
			t.Errorf("intent promote.1 = %+v, want weight 7 over the threshold, all three merges, at h2", in)
		}
	})
	t.Run("a merge with no spec weighs medium", func(t *testing.T) {
		t.Parallel()
		r := newPromoteRig(t, false)
		r.busy()
		r.spec("T-1", "hard")
		r.spec("T-2", "") // no Difficulty line
		r.git.between("h0", "h1", "T-1", "T-2", "T-3")
		r.pass()
		if in := r.intent("promote.1"); in.Weight != 7 {
			t.Errorf("weight = %d, want 3 + 2 + 2 (no line, no spec: medium)", in.Weight)
		}
	})
}

func TestPromoteTriggerByIdle(t *testing.T) {
	t.Parallel()
	r := newPromoteRig(t, false)
	r.spec("T-1", "simple")
	r.git.between("h0", "h1", "T-1")
	r.a.mu.Lock()
	idle := r.p.loopIdle()
	r.a.mu.Unlock()
	if !idle {
		t.Fatal("precondition: the loop is idle")
	}
	r.pass()
	if runs, _, _ := r.f.calls(); len(runs) != 1 || runs[0] != "h1" {
		t.Errorf("one merge (weight 1) with the loop idle: runs %q, want one at h1", runs)
	}
}

// GREEN, no reviewer configured (covered): main fast-forwarded, fact promoted, after_promote once; the next passes
// promote nothing more and status says main is at the promoted sha.
func TestPromoteGreenPromotes(t *testing.T) {
	t.Parallel()
	r := newPromoteRig(t, false)
	r.git.between("h0", "h1", "T-1")
	r.pass() // the run
	if got := r.git.head("main"); got != "h0" {
		t.Fatalf("precondition: main moves only after the run's result: %s", got)
	}
	r.pass() // the fast-forward and fact promoted
	r.pass() // after_promote
	r.pass()
	runs, ffs, afters := r.f.calls()
	if len(runs) != 1 || strings.Join(ffs, ",") != "h1" || strings.Join(afters, ",") != "h1" {
		t.Errorf("runs %q, ffs %q, afters %q; want one each at h1", runs, ffs, afters)
	}
	if got := r.git.head("main"); got != "h1" {
		t.Errorf("main = %s, want h1", got)
	}
	want := []string{"intent promote.1", "result promote.1", "fact promoted", "intent after_promote.1", "result after_promote.1"}
	if got := r.steps(); !slices.Equal(got, want) {
		t.Errorf("records = %q, want %q", got, want)
	}
	if st := r.a.Status().Project.Promote; st == nil || st.SHA != "h1" || st.Merges != 0 || st.Wait != "" {
		t.Errorf("status promote = %+v, want main at h1, 0 merges since, no wait", st)
	}
}

// RED (after the retest): needs-you naming the merges since, main unchanged, no second run at the same head; the next
// merge's GREEN promotion ends it.
func TestPromoteRedThenGreen(t *testing.T) {
	t.Parallel()
	r := newPromoteRig(t, false)
	r.git.between("h0", "h1", "T-1", "T-2")
	r.f.verdicts["h1"] = "RED"
	r.pass()
	r.pass()
	r.pass()
	runs, ffs, _ := r.f.calls()
	if len(runs) != 1 || len(ffs) != 0 || r.git.head("main") != "h0" {
		t.Fatalf("RED at h1: runs %q, ffs %q, main %s; want one run, no ff, main at h0", runs, ffs, r.git.head("main"))
	}
	want := "promotion RED h1: check 1 failed — TestFlaky; merges since h0: T-1, T-2"
	if got := r.needs(); len(got) != 1 || got[0] != want {
		t.Fatalf("needs-you = %q, want [%q]", got, want)
	}
	r.git.set("staging", "h2")
	r.git.between("h0", "h2", "T-1", "T-2", "T-3")
	r.pass() // the run at h2
	r.pass() // its fast-forward
	if got := r.git.head("main"); got != "h2" {
		t.Errorf("main = %s after a GREEN at h2, want h2", got)
	}
	if got := r.needs(); len(got) != 0 {
		t.Errorf("needs-you after the GREEN promotion = %q, want none", got)
	}
}

// A promotion that ends neither GREEN nor RED is counted; the third is a needs-you.
func TestPromoteFailureCounted(t *testing.T) {
	t.Parallel()
	r := newPromoteRig(t, false)
	r.git.between("h0", "h1", "T-1")
	r.f.verdicts["h1"] = "fail"
	for i := 0; i < tries; i++ {
		r.pass()
		r.now = r.now.Add(promoteRetry)
	}
	if runs, _, _ := r.f.calls(); len(runs) != tries {
		t.Fatalf("runs = %q, want %d", runs, tries)
	}
	if got := r.needs(); len(got) != 1 || !strings.HasPrefix(got[0], "promotion failed: promote.1: cannot reset") {
		t.Errorf("needs-you = %q, want one promotion failed: promote.1", got)
	}
	r.pass()
	if runs, _, _ := r.f.calls(); len(runs) != tries {
		t.Errorf("a spent promotion ran again at the same head: %q", runs)
	}
}

// A restart mid-run: the pending intent runs again at its recorded H, not at the target's new head.
func TestPromoteRestartRunsRecordedHead(t *testing.T) {
	t.Parallel()
	r := newPromoteRig(t, false)
	r.git.between("h0", "h1", "T-1")
	r.f.block, r.f.began = true, make(chan struct{}, 1)
	ctx, cancel := context.WithCancel(context.Background())
	r.a.promotion(ctx, r.p, r.fx)
	<-r.f.began
	cancel()
	r.a.effects.Wait()
	if got := r.steps(); !slices.Equal(got, []string{"intent promote.1"}) {
		t.Fatalf("precondition: the cut run's intent is pending: %q", got)
	}
	r.a, r.p = lineReload(t, r.a, r.p, &r.now)
	r.wire()
	r.f.block = false
	r.git.set("staging", "h2")
	r.git.between("h0", "h2", "T-1", "T-2")
	r.pass()
	runs, _, _ := r.f.calls()
	if len(runs) != 2 || runs[1] != "h1" {
		t.Errorf("runs = %q, want the cut one at h1 run again at h1", runs)
	}
	if got := r.steps(); !slices.Contains(got, "result promote.1") {
		t.Errorf("records = %q, want promote.1's result", got)
	}
}

// Main already at the GREEN sha (a crash between the fast-forward and fact promoted): the fact, no second run.
func TestPromoteCrashAfterFastForward(t *testing.T) {
	t.Parallel()
	r := newPromoteRig(t, false)
	r.git.between("h0", "h1", "T-1")
	r.pass() // GREEN at h1
	r.git.set("main", "h1")
	r.pass()
	runs, _, _ := r.f.calls()
	if len(runs) != 1 {
		t.Errorf("runs = %q, want one", runs)
	}
	if got := r.steps(); !slices.Contains(got, "fact promoted") {
		t.Errorf("records = %q, want fact promoted", got)
	}
}

// fact records a fact of task (fails when the log refuses it).
func (r *promoteRig) fact(task, step string, d any) {
	r.t.Helper()
	if !r.a.record(r.p, event.Record{Task: task, Step: step, Kind: event.KindFact}, d) {
		r.t.Fatalf("the log refused fact %s %s", task, step)
	}
}

// started starts review id at head (fact review.started).
func (r *promoteRig) started(id, head string) {
	r.t.Helper()
	r.fact(event.ReviewTask, event.StepReviewStarted, ReviewData{ID: id, Head: head})
}

// verified is a finished verify of tasks at head: the reviewer's verify brief, then its finished line.
func (r *promoteRig) verified(head string, tasks ...string) {
	r.t.Helper()
	r.fact(event.MsgTask, event.StepBriefed, event.Message{To: "pj-rev", Line: "verify", Task: strings.Join(tasks, ","),
		Reviewed: head})
	r.fact(event.MsgTask, event.StepMessage, event.Message{To: "lead", From: "pj-rev", Line: "finished", Ends: "pj-rev"})
}

// closed closes review id (fact review.closed; its close commit is not read by the promotion).
func (r *promoteRig) closed(id string) {
	r.t.Helper()
	r.fact(event.ReviewTask, event.StepReviewClosed, ReviewData{ID: id})
}

// reviewedRig is a rig with reviews set up whose last closed review, RV-4, reviewed h0, main's sha: nothing to promote.
func reviewedRig(t *testing.T) *promoteRig {
	t.Helper()
	r := newPromoteRig(t, true)
	r.started("RV-4", "h0")
	r.closed("RV-4")
	r.a.mu.Lock()
	head := r.p.folded.closedHead
	r.a.mu.Unlock()
	if head != "h0" {
		t.Fatalf("precondition: RV-4's reviewed head = %q, want h0", head)
	}
	return r
}

// Review before promotion (owner 11-07, T-978): the trigger requests a review of the batch and runs the suite at H;
// that GREEN does not move main while the review is requested or open; the review closes at H (R = H): the GREEN
// recorded at R is reused, main moves to R without a second run, and the close commit after R promotes nothing.
func TestPromoteReusesTheGreenAtR(t *testing.T) {
	t.Parallel()
	r := reviewedRig(t)
	r.git.between("h0", "h1", "T-1", "T-2")
	r.pass() // due: the review request and the run
	r.a.mu.Lock()
	q := r.p.folded.review
	r.a.mu.Unlock()
	if q == nil || q.by != "obor" || q.note != "promotion batch h0..h1: T-1, T-2" {
		t.Fatalf("review request = %+v, want obor's naming the batch h0..h1: T-1, T-2", q)
	}
	if runs, _, _ := r.f.calls(); !slices.Equal(runs, []string{"h1"}) {
		t.Fatalf("runs = %q, want one at h1 (the suite runs while the review is out)", runs)
	}
	r.pass()
	if st := r.a.Status().Project.Promote; r.git.head("main") != "h0" || st == nil ||
		st.Wait != "GREEN h1 waits: the review request" {
		t.Fatalf("review requested: main %s, status %+v; want h0 and the wait on the review request",
			r.git.head("main"), st)
	}
	r.fact(event.ReviewTask, event.StepReviewFire, ReviewData{Request: q.seq})
	r.started("RV-5", "h1")
	r.pass()
	if st := r.a.Status().Project.Promote; r.git.head("main") != "h0" || st == nil ||
		st.Wait != "GREEN h1 waits: RV-5 open (not closed)" {
		t.Fatalf("RV-5 open: main %s, status %+v; want h0 and the wait on RV-5", r.git.head("main"), st)
	}
	r.a.mu.Lock()
	req := r.p.folded.review
	r.a.mu.Unlock()
	if req != nil {
		t.Errorf("a review open: a second request %+v, want none", req)
	}
	// RV-5 closes at h1 (no fix); its close commit (no merge) is the new head.
	r.git.set("staging", "h2")
	r.closed("RV-5")
	r.pass()
	runs, ffs, _ := r.f.calls()
	if r.git.head("main") != "h1" || !slices.Equal(ffs, []string{"h1"}) || !slices.Equal(runs, []string{"h1"}) {
		t.Fatalf("after RV-5's close at h1: main %s, ffs %q, runs %q; want main at h1 by the GREEN recorded there, "+
			"no second run", r.git.head("main"), ffs, runs)
	}
	r.pass()
	r.pass()
	if runs, ffs, _ := r.f.calls(); len(runs) != 1 || len(ffs) != 1 {
		t.Errorf("the close commit alone (no merge since h1) ran or promoted: runs %q, ffs %q", runs, ffs)
	}
}

// RV-136 #1: GREEN at h1, the review requests changes, the fix merges at h2, its verify finishes at h2 and the review
// closes (R = h2): the suite runs at h2 and main moves to h2 only on its GREEN; never to h1. RED at h2: main stays,
// the promotion's needs-you as T-974's.
func TestPromoteReviewFixCycle(t *testing.T) {
	t.Parallel()
	for _, verdict := range []string{"GREEN", "RED"} {
		t.Run(verdict+" at the verified fix", func(t *testing.T) {
			t.Parallel()
			r := reviewedRig(t)
			r.spec("T-1", "simple")
			r.spec("T-3", "simple")
			r.git.between("h0", "h1", "T-1")
			r.pass() // idle: the review request and the run at h1
			r.pass()
			if runs, ffs, _ := r.f.calls(); !slices.Equal(runs, []string{"h1"}) || len(ffs) != 0 {
				t.Fatalf("precondition: GREEN at h1, not promoted: runs %q, ffs %q", runs, ffs)
			}
			r.started("RV-5", "h1")
			// The review requests changes; the fix T-3 merges at h2 (weight 2 with the loop busy: no trigger).
			r.busy()
			r.git.set("staging", "h2")
			r.git.between("h0", "h2", "T-1", "T-3")
			r.pass()
			if runs, ffs, _ := r.f.calls(); len(runs) != 1 || len(ffs) != 0 || r.git.head("main") != "h0" {
				t.Fatalf("RV-5 open, fix merged: runs %q, ffs %q, main %s; want nothing more", runs, ffs,
					r.git.head("main"))
			}
			if verdict == "RED" {
				r.f.verdicts["h2"] = "RED"
			}
			r.verified("h2", "T-3")
			r.git.set("staging", "h3") // the close commit
			r.closed("RV-5")
			r.pass() // the suite at R = h2
			runs, ffs, _ := r.f.calls()
			if !slices.Equal(runs, []string{"h1", "h2"}) || len(ffs) != 0 || r.git.head("main") != "h0" {
				t.Fatalf("after the close at h2: runs %q, ffs %q, main %s; want the suite at h2 first, main at h0",
					runs, ffs, r.git.head("main"))
			}
			if in := r.intent("promote.2"); in.SHA != "h2" || in.Since != "h0" {
				t.Errorf("intent promote.2 = %+v, want sha h2 since h0", in)
			}
			r.pass()
			r.pass()
			r.pass()
			runs, ffs, _ = r.f.calls()
			if slices.Contains(ffs, "h1") {
				t.Fatalf("ffs %q: the old GREEN h1 was promoted past the verified fix", ffs)
			}
			if len(runs) != 2 {
				t.Errorf("runs %q, want no third run (h2 has its result)", runs)
			}
			if verdict == "GREEN" {
				if r.git.head("main") != "h2" || !slices.Equal(ffs, []string{"h2"}) {
					t.Errorf("GREEN at h2: main %s, ffs %q; want main fast-forwarded to h2", r.git.head("main"), ffs)
				}
				return
			}
			want := "promotion RED h2: check 1 failed — TestFlaky; merges since h0: T-1, T-3"
			if got := r.needs(); r.git.head("main") != "h0" || len(ffs) != 0 || !slices.Equal(got, []string{want}) {
				t.Errorf("RED at h2: main %s, ffs %q, needs-you %q; want main at h0, no ff, [%q]", r.git.head("main"),
					ffs, got, want)
			}
		})
	}
}

// A review closed with accepted lines at R (no GREEN there yet, no trigger due): the suite runs at R and main moves to
// R on its GREEN.
func TestPromoteAcceptedCloseRunsAtR(t *testing.T) {
	t.Parallel()
	r := reviewedRig(t)
	r.busy()
	r.spec("T-1", "simple")
	r.git.between("h0", "h1", "T-1")
	r.pass()
	if runs, _, _ := r.f.calls(); len(runs) != 0 {
		t.Fatalf("precondition: weight 1, loop busy: runs %q, want none", runs)
	}
	r.started("RV-5", "h1")
	r.git.mu.Lock()
	r.git.last, r.git.lastBody = "RV-5 closed · h1 · accepted 2 · batch", "Covered: T-1\nStart: h0"
	r.git.mu.Unlock()
	r.git.set("staging", "h2")
	r.closed("RV-5")
	r.pass()
	r.pass()
	runs, ffs, _ := r.f.calls()
	if !slices.Equal(runs, []string{"h1"}) || !slices.Equal(ffs, []string{"h1"}) || r.git.head("main") != "h1" {
		t.Errorf("accepted close at h1: runs %q, ffs %q, main %s; want one run and main at h1", runs, ffs,
			r.git.head("main"))
	}
}

// A restart mid-run at R: the pending intent runs again at R, not at the target's newer head.
func TestPromoteRestartMidRunAtR(t *testing.T) {
	t.Parallel()
	r := reviewedRig(t)
	r.busy()
	r.started("RV-5", "h1")
	r.git.set("staging", "h2")
	r.closed("RV-5")
	r.f.block, r.f.began = true, make(chan struct{}, 1)
	ctx, cancel := context.WithCancel(context.Background())
	r.a.promotion(ctx, r.p, r.fx)
	<-r.f.began
	cancel()
	r.a.effects.Wait()
	if got := r.steps(); !slices.Equal(got, []string{"intent promote.1"}) || r.intent("promote.1").SHA != "h1" {
		t.Fatalf("precondition: the cut run's intent at h1 is pending: %q", got)
	}
	r.a, r.p = lineReload(t, r.a, r.p, &r.now)
	r.wire()
	r.f.block = false
	r.git.set("staging", "h3")
	r.pass()
	r.pass()
	runs, ffs, _ := r.f.calls()
	if !slices.Equal(runs, []string{"h1", "h1"}) || !slices.Equal(ffs, []string{"h1"}) {
		t.Errorf("after the restart: runs %q, ffs %q; want the cut run again at h1, then main to h1", runs, ffs)
	}
}

// branches.promote unset (no Loop.Promote): no promotion, and after_merge runs after the publish as before; set: the
// publish's after_merge runs nothing (after_promote does).
func TestPromoteAfterMergePerPublish(t *testing.T) {
	t.Parallel()
	for _, set := range []bool{false, true} {
		t.Run(fmt.Sprintf("promote set %v", set), func(t *testing.T) {
			t.Parallel()
			r := newPromoteRig(t, false)
			r.git.between("h0", "h1", "T-1")
			spy := &afterSpy{}
			r.fx.Scratch = spy
			if !set {
				r.fx.Promote = nil
			}
			r.pass()
			if runs, _, _ := r.f.calls(); set != (len(runs) == 1) || !set && len(r.steps()) != 0 {
				t.Errorf("promote set %v: runs %q, records %q", set, runs, r.steps())
			}
			mt := mtask{task: "T-1", attempt: 1, state: stMerged}
			if !r.a.afterStep(context.Background(), r.p, r.fx, mt) {
				t.Fatal("after_merge failed")
			}
			recs, _ := event.ReadLog(r.p.root)
			var line string
			for _, rec := range recs {
				if rec.Task == "T-1" && rec.Kind == event.KindResult && rec.Step == event.StepAfterMerge {
					_ = json.Unmarshal(rec.Data, &line)
				}
			}
			if set && (spy.n != 0 || line != afterMergePromoted) || !set && (spy.n != 1 || line != "ran") {
				t.Errorf("promote set %v: after_merge ran %d times, result %q", set, spy.n, line)
			}
		})
	}
}

// afterSpy is a Scratch whose AfterMerge counts its runs (the rest is not called).
type afterSpy struct {
	port.Scratch
	n int
}

func (s *afterSpy) AfterMerge(context.Context, port.Candidate) (string, error) {
	s.n++
	return "ran", nil
}
