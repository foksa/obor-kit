package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"obor/internal/app/event"
	"obor/internal/app/port"
	"obor/internal/app/wire"
	"obor/internal/config"
	"obor/internal/store"
)

// T-974: the promotion of branches.main (staging) to branches.promote (main): the trigger by weight and by idle, the
// full suite's run, the fast-forward to exactly R, the last closed review's reviewed head (T-978), after_promote, RED's
// needs-you, a restart mid-run, and after_merge off per publish.

// promoteGit is a port.Review of named shas: heads by branch (the history linear: h0, h1, …). Head of a branch it has
// no sha for is an error. What merged and what a review covered are the log's (T-1023): promoteRig.between, started.
type promoteGit struct {
	mu    sync.Mutex
	heads map[string]string
}

// shaN is hN's N (the rig's linear history).
func shaN(s string) int {
	n, err := strconv.Atoi(strings.TrimPrefix(s, "h"))
	if err != nil {
		panic("promoteGit: no hN sha " + s)
	}
	return n
}

func (g *promoteGit) Head(_ context.Context, branch string) (string, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if sha, ok := g.heads[branch]; ok {
		return sha, nil
	}
	return "", errors.New("no branch " + branch)
}

func (g *promoteGit) Files(context.Context, string, string) ([]string, error) { return nil, nil }

func (g *promoteGit) Detach(context.Context, string, string) error { return nil }

func (g *promoteGit) Close(context.Context, port.ReviewCommit) error { return nil }

func (g *promoteGit) Closed(context.Context, string, string, string) (string, error) {
	return "", nil
}

// set sets branch's head.
func (g *promoteGit) set(branch, sha string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.heads[branch] = sha
}

func (g *promoteGit) head(branch string) string {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.heads[branch]
}

// passed: main is at sha or past it, the rig's history being linear h0, h1, h2, … (hN an ancestor of hM when N <= M).
func (g *promoteGit) passed(sha string) bool {
	return shaN(sha) <= shaN(g.head("main"))
}

// promoteFake is the Loop's Promote on g: each sha's verdict (verdicts; none: GREEN; "fail": an error), every suite
// run's, fast-forward's and after_promote's sha in order; block, when set, holds a run until its ctx ends. As checks'
// (T-984): a run at a sha main is at or past is GREEN with no suite (not in runs), a fast-forward there is already;
// during, when set, runs inside each suite run (main moved meanwhile), movedAt moves main inside the fast-forward
// before it decides, and refuse makes it refused.
type promoteFake struct {
	mu       sync.Mutex
	g        *promoteGit
	verdicts map[string]string
	runs     []string
	ffs      []string
	afters   []string
	block    bool
	began    chan struct{}
	during   func()
	movedAt  string
	refuse   bool
}

func (f *promoteFake) promote(weight int) *port.Promote {
	var cfg *config.Config // the default weights
	return &port.Promote{Branch: "main", Target: "staging", Weight: weight, Weigh: cfg.MergeWeight,
		Run: func(ctx context.Context, sha string) (port.PromoteOut, error) {
			if f.g.passed(sha) {
				return port.PromoteOut{SHA: sha, Verdict: "GREEN", Line: "main past " + sha + " already"}, nil
			}
			f.mu.Lock()
			f.runs = append(f.runs, sha)
			v, block, began, during := f.verdicts[sha], f.block, f.began, f.during
			f.mu.Unlock()
			if during != nil {
				during()
			}
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
		Passed: func(_ context.Context, sha string) (bool, error) { return f.g.passed(sha), nil },
		FF: func(_ context.Context, sha string) (port.FFOut, error) {
			f.mu.Lock()
			f.ffs = append(f.ffs, sha)
			movedAt, refuse := f.movedAt, f.refuse
			f.mu.Unlock()
			switch {
			case refuse:
				return port.FFOut{Outcome: port.FFRefused, Line: "main diverged from " + sha}, nil
			case movedAt != "":
				f.g.set("main", movedAt)
			}
			if f.g.passed(sha) {
				return port.FFOut{Outcome: port.FFAlready, Line: "main past " + sha + " already"}, nil
			}
			f.g.set("main", sha)
			return port.FFOut{Outcome: port.FFMoved, Line: "promoted main"}, nil
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
		git: &promoteGit{heads: map[string]string{"staging": "h1", "main": "h0"}}}
	r.f = &promoteFake{g: r.git, verdicts: map[string]string{}}
	r.a, r.p = lineApp(t, root, &r.now)
	r.wire()
	return r
}

func (r *promoteRig) wire() {
	r.fx = &Loop{Workflow: WorkflowOf(r.git, r.f.promote(6)), Agents: &agentsSpy{}}
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
		r.between("h0", "h1", tasks...)
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
		r.between("h0", "h1", "T-1", "T-2")
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
		r.between("h0", "h1", "T-1", "T-2")
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
		r.between("h0", "h2", "T-1", "T-2", "T-3")
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
		r.between("h0", "h1", "T-1", "T-2", "T-3")
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
	r.between("h0", "h1", "T-1")
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

// RV-144 #2 (T-1032): the promoted branch's head is one the log never named (main older than obor's merge records)
// and two reviews started after T-1's merge (reviews since turned off): the batch starts at the log's first merge, so
// the idle trigger runs it at h1, T-1 its merge and weight.
func TestPromoteUnknownHeadRunsItsBatch(t *testing.T) {
	t.Parallel()
	r := newPromoteRig(t, false)
	r.spec("T-1", "simple")
	r.between("h0", "h1", "T-1")
	r.started("RV-1", "h1", "T-1")
	r.started("RV-2", "h1")
	r.a.mu.Lock()
	_, named := r.p.folded.rv.pos("h0")
	base, idle := r.p.folded.rv.base(), r.p.loopIdle()
	r.a.mu.Unlock()
	if named || base <= 1 || !idle {
		t.Fatalf("precondition: h0 named %v, base %d (want after T-1's merge), idle %v", named, base, idle)
	}
	r.pass()
	if runs, _, _ := r.f.calls(); !slices.Equal(runs, []string{"h1"}) {
		t.Fatalf("runs %q, want one at h1", runs)
	}
	if in := r.intent("promote.1"); !slices.Equal(in.Merges, []string{"T-1"}) || in.Weight != 1 || in.Since != "h0" {
		t.Errorf("intent %+v, want T-1's batch since h0, weight 1", in)
	}
}

// RV-144 #2 (T-1032): with reviews set up and main at a head the log never named (moved past R by hand), the review
// request's batch note names every logged merge since: T-1 (before the review cutoff, RV-2's start) and T-2.
func TestPromoteUnknownHeadBatchNote(t *testing.T) {
	t.Parallel()
	r := newPromoteRig(t, true)
	r.spec("T-1", "simple")
	r.spec("T-2", "simple")
	r.between("h0", "h1", "T-1")
	r.started("RV-1", "h1", "T-1")
	r.closed("RV-1")
	r.started("RV-2", "h1")
	r.closed("RV-2")
	r.between("h1", "h6", "T-2")
	r.git.set("main", "h5")
	r.git.set("staging", "h6")
	r.a.mu.Lock()
	_, named := r.p.folded.rv.pos("h5")
	R := r.p.folded.rv.closedHead()
	r.a.mu.Unlock()
	if named || R != "h1" {
		t.Fatalf("precondition: h5 named %v, R %q (want h1)", named, R)
	}
	r.pass()
	r.a.mu.Lock()
	q := r.p.folded.review
	merges := r.p.prom.merges
	r.a.mu.Unlock()
	if q == nil || q.by != "obor" || q.note != "promotion batch h5..h6: T-1, T-2" {
		t.Errorf("review request %+v, want obor's naming the batch h5..h6: T-1, T-2", q)
	}
	if merges != 2 {
		t.Errorf("the batch's merges %d, want 2 (T-1, T-2)", merges)
	}
}

// GREEN, no reviewer configured (covered): main fast-forwarded, fact promoted, after_promote once; the next passes
// promote nothing more and status says main is at the promoted sha.
func TestPromoteGreenPromotes(t *testing.T) {
	t.Parallel()
	r := newPromoteRig(t, false)
	r.between("h0", "h1", "T-1")
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
	r.between("h0", "h1", "T-1", "T-2")
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
	r.between("h0", "h2", "T-1", "T-2", "T-3")
	r.pass() // the run at h2
	r.pass() // its fast-forward
	if got := r.git.head("main"); got != "h2" {
		t.Errorf("main = %s after a GREEN at h2, want h2", got)
	}
	if got := r.needs(); len(got) != 0 {
		t.Errorf("needs-you after the GREEN promotion = %q, want none", got)
	}
}

// T-1013: with branches.promote unset (no Workflow.Promotion) the pass ends the promotion's open needs-you (one resumed
// fact) before returning; a pass with nothing open records nothing; with promotion set, the needs-you stays.
func TestPromoteOffSettlesNeedsYou(t *testing.T) {
	t.Parallel()
	red := func(t *testing.T) *promoteRig {
		t.Helper()
		r := newPromoteRig(t, false)
		r.between("h0", "h1", "T-1")
		r.f.verdicts["h1"] = "RED"
		r.pass() // the run
		r.pass() // its needs-you
		if got := r.needs(); len(got) != 1 || !strings.HasPrefix(got[0], promoteRedPrefix) {
			t.Fatalf("precondition: a promotion RED needs-you is open, got %q", got)
		}
		return r
	}
	t.Run("promotion off: one resumed fact, then nothing more", func(t *testing.T) {
		t.Parallel()
		r := red(t)
		r.fx.Workflow = WorkflowOf(r.git) // branches.promote unset
		before := r.steps()
		r.pass()
		if got := r.needs(); len(got) != 0 {
			t.Errorf("promotion off, after a pass: needs-you %q, want none", got)
		}
		want := append(slices.Clone(before), "fact "+event.StepResumed)
		if got := r.steps(); !slices.Equal(got, want) {
			t.Errorf("promotion off: records %q, want %q", got, want)
		}
		r.pass()
		if got := r.steps(); !slices.Equal(got, want) {
			t.Errorf("promotion off, nothing open: records %q, want unchanged %q", got, want)
		}
	})
	t.Run("promotion set: the needs-you stays", func(t *testing.T) {
		t.Parallel()
		r := red(t)
		r.pass()
		if got := r.needs(); len(got) != 1 || !strings.HasPrefix(got[0], promoteRedPrefix) {
			t.Errorf("promotion set: needs-you %q, want the RED one still open", got)
		}
		if got := r.steps(); slices.Contains(got, "fact "+event.StepResumed) {
			t.Errorf("promotion set: records %q, want no resumed fact", got)
		}
	})
}

// A promotion that ends neither GREEN nor RED is counted; the third is a needs-you.
func TestPromoteFailureCounted(t *testing.T) {
	t.Parallel()
	r := newPromoteRig(t, false)
	r.between("h0", "h1", "T-1")
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
	r.between("h0", "h1", "T-1")
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
	r.between("h0", "h2", "T-1", "T-2")
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
	r.between("h0", "h1", "T-1")
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

// started starts review id at head, its in-scope tasks (fact review.started).
func (r *promoteRig) started(id, head string, tasks ...string) {
	r.t.Helper()
	r.fact(event.ReviewTask, event.StepReviewStarted, ReviewData{ID: id, Head: head, Tasks: tasks})
}

// between records the merges of tasks the log does not hold yet (fact merged, in order): the last at to, the others at
// to's sha with a suffix (from is git's range start, for the reader: the log orders them).
func (r *promoteRig) between(_, to string, tasks ...string) {
	r.t.Helper()
	var add []string
	for _, task := range tasks {
		if r.mergedSHA(task) == "" {
			add = append(add, task)
		}
	}
	for i, task := range add {
		sha := to
		if i < len(add)-1 {
			sha = fmt.Sprintf("%s.%d", to, i)
		}
		r.fact(task, event.StepMerged, event.MergedData{Head: sha})
	}
}

// mergedSHA is task's merge's head in the fold ("": none).
func (r *promoteRig) mergedSHA(task string) string {
	r.a.mu.Lock()
	defer r.a.mu.Unlock()
	for _, m := range r.p.folded.rv.merges {
		if m.task == task {
			return m.sha
		}
	}
	return ""
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
	head := r.p.folded.rv.closedHead()
	r.a.mu.Unlock()
	if head != "h0" {
		t.Fatalf("precondition: RV-4's reviewed head = %q, want h0", head)
	}
	return r
}

// T-990 (owner 11-07): with reviews set up the trigger only requests the review; the suite runs at R alone, the closed
// review's reviewed head, never at the target's head. A review open at h1 while two more merges land runs nothing;
// its close at R = h1 runs the suite there once and main moves to h1.
func TestPromoteReviewSuiteOnlyAtR(t *testing.T) {
	t.Parallel()
	r := reviewedRig(t) // R = h0, main h0
	r.busy()            // the weight triggers, never the idle loop
	r.spec("T-1", "hard")
	r.spec("T-2", "hard")
	r.between("h0", "h1", "T-1", "T-2") // weight 6, the threshold
	r.pass()                            // due: the review request, no suite
	r.a.mu.Lock()
	q := r.p.folded.review
	r.a.mu.Unlock()
	if q == nil || q.by != "obor" || q.note != "promotion batch h0..h1: T-1, T-2" {
		t.Fatalf("review request = %+v, want obor's naming the batch h0..h1: T-1, T-2", q)
	}
	if runs, ffs, _ := r.f.calls(); len(runs) != 0 || len(ffs) != 0 {
		t.Fatalf("review requested: runs %q, ffs %q; want no suite at the target's head", runs, ffs)
	}
	if st := r.a.Status().Project.Promote; st == nil || st.Wait != "waiting for review" || st.Run != nil {
		t.Fatalf("review requested: status %+v, want the wait for the review and no run", st)
	}
	r.fact(event.ReviewTask, event.StepReviewFire, ReviewData{Request: q.seq})
	r.started("RV-5", "h1", "T-1", "T-2")
	r.pass()
	// Two more merges while RV-5 is open: the weight is still reached, nothing runs.
	r.spec("T-3", "hard")
	r.spec("T-4", "hard")
	r.git.set("staging", "h2")
	r.between("h0", "h2", "T-1", "T-2", "T-3")
	r.pass()
	r.git.set("staging", "h3")
	r.between("h0", "h3", "T-1", "T-2", "T-3", "T-4")
	r.pass()
	if runs, ffs, _ := r.f.calls(); len(runs) != 0 || len(ffs) != 0 {
		t.Fatalf("RV-5 open, two more merges: runs %q, ffs %q; want none", runs, ffs)
	}
	if got := r.git.head("main"); got != "h0" {
		t.Fatalf("precondition: main %s, want h0 while RV-5 is open", got)
	}
	if st := r.a.Status().Project.Promote; st == nil || st.Wait != "waiting for review RV-5" || st.Run != nil {
		t.Fatalf("RV-5 open: status %+v, want the wait for RV-5, no run", st)
	}
	// RV-5 closes at h1 (no fix): R = h1, the suite runs there once and main moves to it.
	r.git.set("staging", "h4")
	r.closed("RV-5")
	r.pass() // the suite at R = h1
	if runs, ffs, _ := r.f.calls(); !slices.Equal(runs, []string{"h1"}) || len(ffs) != 0 {
		t.Fatalf("RV-5 closed at h1: runs %q, ffs %q; want the suite once at h1 and no fast-forward yet", runs, ffs)
	}
	r.pass() // the fast-forward
	if runs, ffs, _ := r.f.calls(); !slices.Equal(runs, []string{"h1"}) || !slices.Equal(ffs, []string{"h1"}) {
		t.Fatalf("after the GREEN at h1: runs %q, ffs %q; want main moved to h1 with no second run", runs, ffs)
	}
	if got := r.git.head("main"); got != "h1" {
		t.Errorf("main = %s, want h1", got)
	}
}

// RV-136 #1, T-990: the review requests changes, the fix merges at h2, its verify finishes at h2 and the review
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
			r.between("h0", "h1", "T-1")
			r.pass() // idle: the review request, no suite (T-990)
			r.pass()
			if runs, ffs, _ := r.f.calls(); len(runs) != 0 || len(ffs) != 0 {
				t.Fatalf("precondition: review requested, nothing run or promoted: runs %q, ffs %q", runs, ffs)
			}
			r.started("RV-5", "h1", "T-1")
			// The review requests changes; the fix T-3 merges at h2 (weight 2 with the loop busy: no trigger).
			r.busy()
			r.git.set("staging", "h2")
			r.between("h0", "h2", "T-1", "T-3")
			r.pass()
			if runs, ffs, _ := r.f.calls(); len(runs) != 0 || len(ffs) != 0 || r.git.head("main") != "h0" {
				t.Fatalf("RV-5 open, fix merged: runs %q, ffs %q, main %s; want nothing run or promoted", runs, ffs,
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
			if !slices.Equal(runs, []string{"h2"}) || len(ffs) != 0 || r.git.head("main") != "h0" {
				t.Fatalf("after the close at h2: runs %q, ffs %q, main %s; want the suite at h2 first, main at h0",
					runs, ffs, r.git.head("main"))
			}
			if in := r.intent("promote.1"); in.SHA != "h2" || in.Since != "h0" {
				t.Errorf("intent promote.1 = %+v, want sha h2 since h0", in)
			}
			r.pass()
			r.pass()
			r.pass()
			runs, ffs, _ = r.f.calls()
			if slices.Contains(ffs, "h1") {
				t.Fatalf("ffs %q: h1, never reviewed to a close, was promoted past the verified fix", ffs)
			}
			if len(runs) != 1 {
				t.Errorf("runs %q, want no second run (h2 has its result)", runs)
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
	r.between("h0", "h1", "T-1")
	r.pass()
	if runs, _, _ := r.f.calls(); len(runs) != 0 {
		t.Fatalf("precondition: weight 1, loop busy: runs %q, want none", runs)
	}
	r.started("RV-5", "h1", "T-1")
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

// branches.promote unset (no Workflow.Promotion): no promotion, and after_merge runs after the publish as before; set: the
// publish's after_merge runs nothing (after_promote does).
func TestPromoteAfterMergePerPublish(t *testing.T) {
	t.Parallel()
	for _, set := range []bool{false, true} {
		t.Run(fmt.Sprintf("promote set %v", set), func(t *testing.T) {
			t.Parallel()
			r := newPromoteRig(t, false)
			r.between("h0", "h1", "T-1")
			spy := &afterSpy{}
			r.fx.Workflow = WorkflowOf(r.fx.Workflow, spy)
			if !set {
				r.fx.Workflow = WorkflowOf(spy, r.git)
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

// expect checks the fake's suite runs, fast-forwards and after_promotes and the promotion's records.
func (r *promoteRig) expect(what string, runs, ffs, afters, recs []string) {
	r.t.Helper()
	gr, gf, ga := r.f.calls()
	if !slices.Equal(gr, runs) || !slices.Equal(gf, ffs) || !slices.Equal(ga, afters) {
		r.t.Errorf("%s: runs %q, ffs %q, afters %q; want %q, %q, %q", what, gr, gf, ga, runs, ffs, afters)
	}
	if got := r.steps(); !slices.Equal(got, recs) {
		r.t.Errorf("%s: records %q, want %q", what, got, recs)
	}
}

// replay restarts serve on the same log (the git and the fake kept) and runs three passes.
func (r *promoteRig) replay() {
	r.t.Helper()
	r.a, r.p = lineReload(r.t, r.a, r.p, &r.now)
	r.wire()
	r.pass()
	r.pass()
	r.pass()
}

// nothingPending: status shows main at at and no promotion pending (no merges, wait, run or RED).
func (r *promoteRig) nothingPending(what, at string) {
	r.t.Helper()
	st := r.a.Status().Project.Promote
	if st == nil || st.SHA != at || st.Merges != 0 || st.Wait != "" || st.Run != nil || st.Red != nil {
		r.t.Errorf("%s: status promote = %+v, want main at %s, nothing pending", what, st, at)
	}
}

// closeAt starts and closes RV-5 at reviewed (its scope tasks) with the loop busy (no trigger) and staging at head past
// it.
func (r *promoteRig) closeAt(reviewed, head string, tasks ...string) {
	r.t.Helper()
	r.busy()
	r.started("RV-5", reviewed, tasks...)
	r.git.set("staging", head)
	r.closed("RV-5")
	r.a.mu.Lock()
	got := r.p.folded.rv.closedHead()
	r.a.mu.Unlock()
	if got != reviewed {
		r.t.Fatalf("precondition: R = %q, want %s", got, reviewed)
	}
}

// T-984 (RV-136 verify #1): R, the last closed review's reviewed head, at or behind the promoted branch's head is
// passed already: no intent, no suite, no fast-forward, no fact promoted, no after_promote (serve's binary never
// rebuilt from an older R), and status shows nothing pending; the same after a restart.
func TestPromoteRPassedAlready(t *testing.T) {
	t.Parallel()
	t.Run("the switch: staging = main = h1, R h0 behind it", func(t *testing.T) {
		t.Parallel()
		r := reviewedRig(t) // R = h0
		r.git.set("staging", "h1")
		r.git.set("main", "h1")
		if !r.git.passed("h0") || r.git.head("main") == "h0" {
			t.Fatal("precondition: R h0 strictly behind main")
		}
		r.pass()
		r.pass()
		r.pass()
		r.expect("R behind main", nil, nil, nil, nil)
		r.nothingPending("R behind main", "h1")
		r.replay()
		r.expect("replayed", nil, nil, nil, nil)
		r.nothingPending("replayed", "h1")
	})
	t.Run("the switch past a GREEN at R: not fast-forwarded back, not promoted", func(t *testing.T) {
		t.Parallel()
		r := reviewedRig(t)
		r.between("h0", "h1", "T-1")
		r.closeAt("h1", "h2", "T-1")
		r.pass() // the suite at R h1, GREEN
		r.a.mu.Lock()
		ran := r.p.prom.ran["h1"]
		r.a.mu.Unlock()
		if ran != "GREEN" {
			t.Fatalf("precondition: a GREEN at h1: %q", ran)
		}
		r.git.set("main", "h2") // the switch at the current head h2
		r.pass()
		r.pass()
		r.pass()
		recs := []string{"intent promote.1", "result promote.1"}
		r.expect("GREEN at R h1, main h2", []string{"h1"}, nil, nil, recs)
		if got := r.git.head("main"); got != "h2" {
			t.Errorf("main = %s, want h2 (never moved back to R)", got)
		}
		r.nothingPending("GREEN at R h1, main h2", "h2")
		r.replay()
		r.expect("replayed", []string{"h1"}, nil, nil, recs)
		r.nothingPending("replayed", "h2")
	})
	t.Run("R == main: nothing to do", func(t *testing.T) {
		t.Parallel()
		r := reviewedRig(t) // R = main = h0
		r.git.set("staging", "h0")
		r.pass()
		r.pass()
		r.expect("R == main", nil, nil, nil, nil)
		r.nothingPending("R == main", "h0")
		r.replay()
		r.expect("replayed", nil, nil, nil, nil)
		r.nothingPending("replayed", "h0")
	})
}

// T-984: R ahead of main (the normal case): the suite at R, main moved to R, fact promoted, after_promote; a restart
// repeats none of it.
func TestPromoteRAheadOfMain(t *testing.T) {
	t.Parallel()
	r := reviewedRig(t)
	r.closeAt("h1", "h2")
	if r.git.passed("h1") {
		t.Fatal("precondition: R h1 ahead of main")
	}
	r.pass() // the suite at R
	r.pass() // the fast-forward, fact promoted
	r.pass() // after_promote
	r.pass()
	recs := []string{"intent promote.1", "result promote.1", "fact promoted", "intent after_promote.1",
		"result after_promote.1"}
	one := []string{"h1"}
	r.expect("R ahead", one, one, one, recs)
	if got := r.git.head("main"); got != "h1" {
		t.Errorf("main = %s, want h1", got)
	}
	r.replay()
	r.expect("replayed", one, one, one, recs)
}

// T-984: main moved past R during the run (the suite's GREEN at R, or inside the fast-forward): already, no fact
// promoted, no after_promote, main not moved back; a restart, also one that cut the run, the same.
func TestPromoteMainMovedPastRDuringTheRun(t *testing.T) {
	t.Parallel()
	recs := []string{"intent promote.1", "result promote.1"}
	t.Run("during the suite", func(t *testing.T) {
		t.Parallel()
		r := reviewedRig(t)
		r.closeAt("h1", "h3")
		r.f.during = func() { r.git.set("main", "h2") }
		r.pass() // the suite at h1; main moves to h2 meanwhile
		r.pass()
		r.pass()
		r.expect("main past R after the run", []string{"h1"}, nil, nil, recs)
		if got := r.git.head("main"); got != "h2" {
			t.Errorf("main = %s, want h2", got)
		}
		r.nothingPending("main past R", "h2")
		r.replay()
		r.expect("replayed", []string{"h1"}, nil, nil, recs)
	})
	t.Run("inside the fast-forward: already", func(t *testing.T) {
		t.Parallel()
		r := reviewedRig(t)
		r.closeAt("h1", "h3")
		r.f.movedAt = "h2"
		r.pass() // the suite at h1
		r.pass() // the fast-forward finds main at h2: already
		r.pass()
		r.expect("FF already", []string{"h1"}, []string{"h1"}, nil, recs)
		if got := r.git.head("main"); got != "h2" {
			t.Errorf("main = %s, want h2", got)
		}
		r.replay()
		r.expect("replayed", []string{"h1"}, []string{"h1"}, nil, recs)
	})
	t.Run("a restart cut the run", func(t *testing.T) {
		t.Parallel()
		r := reviewedRig(t)
		r.closeAt("h1", "h3")
		r.f.block, r.f.began = true, make(chan struct{}, 1)
		ctx, cancel := context.WithCancel(context.Background())
		r.a.promotion(ctx, r.p, r.fx)
		<-r.f.began
		cancel()
		r.a.effects.Wait()
		if got := r.steps(); !slices.Equal(got, []string{"intent promote.1"}) {
			t.Fatalf("precondition: the cut run's intent is pending: %q", got)
		}
		r.f.block = false
		r.git.set("main", "h2")
		r.replay()
		r.expect("replayed past R", []string{"h1"}, nil, nil, recs)
		r.nothingPending("replayed past R", "h2")
	})
}

// T-984: a refused fast-forward (main diverged from R) is a counted failure, no fact promoted.
func TestPromoteFFRefusedIsCounted(t *testing.T) {
	t.Parallel()
	r := reviewedRig(t)
	r.closeAt("h1", "h2")
	r.f.refuse = true
	r.pass()
	r.pass()
	r.a.mu.Lock()
	fails := r.p.prom.fails["ff h1"]
	r.a.mu.Unlock()
	if fails != 1 || slices.Contains(r.steps(), "fact promoted") || r.git.head("main") != "h0" {
		t.Errorf("refused: fails %d, records %q, main %s; want one counted failure, no fact, main at h0", fails,
			r.steps(), r.git.head("main"))
	}
}

// T-985: main moves to R only when every task merged up to R (the first-parent merges of main..R) is on a close
// commit's Covered: line. A task merged during the review and inside its verify's head holds R back (no suite, no
// fast-forward, status naming it) and the trigger requests the next review, whose close promotes; a restart repeats
// none of it. h0 main; T-1 at h1, the review's start; the fix T-3 at h2; T-x at h2 or h3 (as each case says).
func TestPromoteEveryMergeReviewed(t *testing.T) {
	t.Parallel()
	rig := func(t *testing.T, tasks ...string) *promoteRig {
		r := reviewedRig(t)
		r.busy() // no trigger: the close alone decides
		for _, task := range tasks {
			r.spec(task, "simple")
		}
		r.between("h0", "h1", "T-1")
		r.started("RV-5", "h1", "T-1")
		return r
	}
	t.Run("no fix, T-x merged after the start: R = h1 promoted, T-x waits", func(t *testing.T) {
		t.Parallel()
		r := rig(t, "T-1", "T-x")
		r.between("h0", "h3", "T-1", "T-x") // T-x at h2, the close commit h3
		r.between("h1", "h3", "T-x")
		r.git.set("staging", "h3")
		r.closed("RV-5")
		r.pass() // the suite at h1
		r.pass() // the fast-forward
		r.pass() // after_promote
		r.pass()
		one := []string{"h1"}
		recs := []string{"intent promote.1", "result promote.1", "fact promoted", "intent after_promote.1",
			"result after_promote.1"}
		r.expect("R = h1", one, one, one, recs)
		st := r.a.Status().Project.Promote
		if r.git.head("main") != "h1" || st == nil || !slices.Equal(st.Tasks, []string{"T-x"}) || st.Wait != "" {
			t.Errorf("R = h1: main %s, status %+v; want main at h1, T-x merged since and waiting for no review",
				r.git.head("main"), st)
		}
		r.a, r.p = lineReload(t, r.a, r.p, &r.now) // the replay, the loop still busy (no trigger for T-x)
		r.wire()
		r.busy()
		r.pass()
		r.pass()
		r.expect("replayed", one, one, one, recs)
	})
	for _, c := range []struct{ name, verdict string }{
		{"a fix verified at h2, all covered: the verify head promoted", "approved"},
		{"closed with accepted lines, all covered: promoted", "accepted 2"},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			r := rig(t, "T-1", "T-3")
			r.between("h0", "h2", "T-1", "T-3")
			r.git.set("staging", "h2")
			r.verified("h2", "T-3")
			r.git.set("staging", "h3")
			r.closed("RV-5")
			r.pass()
			r.pass()
			r.pass()
			two := []string{"h2"}
			recs := []string{"intent promote.1", "result promote.1", "fact promoted", "intent after_promote.1",
				"result after_promote.1"}
			r.expect("R = h2", two, two, two, recs)
			if r.git.head("main") != "h2" {
				t.Errorf("main = %s, want h2", r.git.head("main"))
			}
			r.replay()
			r.expect("replayed", two, two, two, recs)
		})
	}
	t.Run("T-x merged before the verify: no promotion until the next review covers it", func(t *testing.T) {
		t.Parallel()
		r := rig(t, "T-1", "T-3", "T-x")
		r.between("h0", "h3", "T-1", "T-3", "T-x") // T-3 at h2, T-x at h3
		r.between("h0", "h4", "T-1", "T-3", "T-x") // h4: RV-5's close commit
		r.git.set("staging", "h3")
		r.verified("h3", "T-3")
		r.git.set("staging", "h4")
		r.closed("RV-5")
		r.a.mu.Lock()
		head := r.p.folded.rv.closedHead()
		r.a.mu.Unlock()
		if head != "h3" {
			t.Fatalf("precondition: R = %q, want the verify's head h3", head)
		}
		r.pass()
		r.pass()
		r.expect("T-x not reviewed, loop busy", nil, nil, nil, nil)
		want := "waiting: T-x merged during RV-5, not reviewed yet"
		if st := r.a.Status().Project.Promote; r.git.head("main") != "h0" || st == nil || st.Wait != want || st.Run != nil {
			t.Fatalf("T-x not reviewed: main %s, status %+v; want main at h0 and the wait %q", r.git.head("main"),
				st, want)
		}
		// The loop idle: the trigger requests the next review although no merge followed R; nothing runs (T-990).
		r.a.mu.Lock()
		delete(r.p.folded.loop, "T-99")
		r.a.mu.Unlock()
		r.pass()
		r.pass()
		r.a.mu.Lock()
		q := r.p.folded.review
		r.a.mu.Unlock()
		if q == nil || q.by != "obor" {
			t.Fatalf("T-x waiting, loop idle: review request %+v, want obor's", q)
		}
		r.expect("the next review requested", nil, nil, nil, nil)
		if st := r.a.Status().Project.Promote; r.git.head("main") != "h0" || st == nil || st.Wait != want || st.Run != nil {
			t.Fatalf("the review requested, T-x waiting: main %s, status %+v; want main at h0, the wait %q", r.git.head("main"),
				st, want)
		}
		// RV-6 starts at h4 (its scope T-x), finds nothing and closes; its close commit h5 covers T-x: R = h4.
		r.fact(event.ReviewTask, event.StepReviewFire, ReviewData{Request: q.seq})
		r.started("RV-6", "h4", "T-x")
		r.pass()
		r.expect("RV-6 open", nil, nil, nil, nil)
		r.git.set("staging", "h5")
		r.closed("RV-6")
		r.pass() // the GREEN at h4 fast-forwards main
		r.pass() // after_promote
		r.pass()
		four := []string{"h4"}
		recs := []string{"intent promote.1", "result promote.1", "fact promoted", "intent after_promote.1",
			"result after_promote.1"}
		r.expect("RV-6 closed", four, four, four, recs)
		if st := r.a.Status().Project.Promote; r.git.head("main") != "h4" || st == nil || st.Wait != "" {
			t.Errorf("RV-6 closed: main %s, status %+v; want main at h4, nothing waiting", r.git.head("main"), st)
		}
		r.replay()
		r.expect("replayed", four, four, four, recs)
	})
}

// T-977: serve's status carries the promotion the monitor shows: the tasks merged since and their weight against the
// threshold, the run under way (the suite, then the wait for review RV-n), the RED run while its needs-you is open, and
// when the promoted sha was promoted; none of it with branches.promote unset.

// promoteJSON is the status' promote object as JSON keys (nil: none sent).
func promoteJSON(t *testing.T, a *App) map[string]json.RawMessage {
	t.Helper()
	b, err := json.Marshal(a.Status().Project)
	if err != nil {
		t.Fatal(err)
	}
	var ps map[string]json.RawMessage
	if err := json.Unmarshal(b, &ps); err != nil {
		t.Fatal(err)
	}
	raw, ok := ps["promote"]
	if !ok {
		return nil
	}
	var out map[string]json.RawMessage
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func TestPromoteStatusOff(t *testing.T) {
	t.Parallel()
	r := newPromoteRig(t, false)
	r.fx.Workflow = WorkflowOf(r.git) // branches.promote unset
	r.between("h0", "h1", "T-1")
	r.pass()
	if st := r.a.Status().Project; st == nil || st.Promote != nil {
		t.Fatalf("promotion off: status %+v, want a project with no promote", st)
	}
	if js := promoteJSON(t, r.a); js != nil {
		t.Errorf("promotion off: the JSON has promote %v, want no key", js)
	}
}

func TestPromoteStatusWeightAndSuite(t *testing.T) {
	t.Parallel()
	r := newPromoteRig(t, false)
	r.busy()
	r.spec("T-1", "simple")
	r.spec("T-2", "medium")
	r.between("h0", "h1", "T-1", "T-2")
	r.pass()
	if runs, _, _ := r.f.calls(); len(runs) != 0 {
		t.Fatalf("precondition: weight 3 < 6 with the loop busy runs nothing: %q", runs)
	}
	st := r.a.Status().Project.Promote
	if st == nil || strings.Join(st.Tasks, ",") != "T-1,T-2" || st.Weight != 3 || st.Threshold != 6 || st.Merges != 2 {
		t.Fatalf("status promote = %+v, want T-1,T-2 merged since, weight 3 of 6", st)
	}
	if st.Run != nil || st.Red != nil || st.At != 0 {
		t.Errorf("nothing runs, nothing RED, no fact promoted: run %+v, red %+v, at %d; want none", st.Run, st.Red, st.At)
	}
	js := promoteJSON(t, r.a)
	for _, k := range []string{"tasks", "weight", "threshold", "branch", "sha"} {
		if _, ok := js[k]; !ok {
			t.Errorf("the JSON's promote %v lacks %q", js, k)
		}
	}

	// The third merge triggers; the suite runs (held) and the status says so.
	r.spec("T-3", "hard")
	r.git.set("staging", "h2")
	r.between("h0", "h2", "T-1", "T-2", "T-3")
	r.f.block, r.f.began = true, make(chan struct{}, 1)
	ctx, cancel := context.WithCancel(context.Background())
	r.a.promotion(ctx, r.p, r.fx)
	<-r.f.began
	st = r.a.Status().Project.Promote
	cancel()
	r.a.effects.Wait()
	if st == nil || st.Run == nil || st.Run.Step != wire.PromoteSuite || st.Run.SHA != "h2" || st.Run.Since != r.now.Unix() {
		t.Fatalf("status promote while the suite runs = %+v (run %+v), want the suite at h2 since now", st, st.Run)
	}
	if st.Weight != 6 || strings.Join(st.Tasks, ",") != "T-1,T-2,T-3" {
		t.Errorf("status promote = %+v, want T-1,T-2,T-3 at weight 6", st)
	}
}

func TestPromoteStatusWaitsForReview(t *testing.T) {
	t.Parallel()
	r := newPromoteRig(t, true)
	r.between("h0", "h1", "T-1", "T-2")
	r.pass() // due (idle): the review request, no suite (T-990)
	r.pass()
	st := r.a.Status().Project.Promote
	if st == nil || st.Wait != "waiting for review" || st.Run != nil {
		t.Fatalf("the review only requested: status promote %+v, want waiting for review, no run", st)
	}
	if runs, _, _ := r.f.calls(); len(runs) != 0 {
		t.Fatalf("the review only requested: runs %q, want none", runs)
	}
	r.a.mu.Lock()
	q := r.p.folded.review
	r.a.mu.Unlock()
	for _, rec := range []struct {
		step string
		d    ReviewData
	}{{event.StepReviewFire, ReviewData{Request: q.seq}}, {event.StepReviewStarted, ReviewData{ID: "RV-5", Head: "h1"}}} {
		if !r.a.record(r.p, event.Record{Task: event.ReviewTask, Step: rec.step, Kind: event.KindFact}, rec.d) {
			t.Fatal("the log refused the review's fact")
		}
	}
	r.pass()
	if st := r.a.Status().Project.Promote; st == nil || st.Wait != "waiting for review RV-5" || st.Run != nil {
		t.Errorf("RV-5 open: status promote %+v, want waiting for review RV-5, no run", st)
	}
}

func TestPromoteStatusRedThenPromoted(t *testing.T) {
	t.Parallel()
	r := newPromoteRig(t, false)
	r.between("h0", "h1", "T-1", "T-2")
	r.f.verdicts["h1"] = "RED"
	r.pass()
	r.pass()
	if got := r.needs(); len(got) != 1 {
		t.Fatalf("precondition: the RED needs-you is open: %q", got)
	}
	st := r.a.Status().Project.Promote
	if st == nil || st.Red == nil || st.Red.SHA != "h1" || st.Red.Tests != "check 1 failed — TestFlaky" ||
		st.Red.Since != "h0" || !slices.Equal(st.Red.Merges, []string{"T-1", "T-2"}) || st.Red.At != r.now.Unix() {
		t.Fatalf("RED at h1: status promote %+v, want red h1 naming the tests and T-1, T-2 since h0", st)
	}
	if st.Run != nil {
		t.Errorf("RED: run %+v, want none", st.Run)
	}
	r.git.set("staging", "h2")
	r.between("h0", "h2", "T-1", "T-2", "T-3")
	r.pass() // the GREEN run at h2
	r.pass() // its fast-forward
	if got := r.needs(); len(got) != 0 {
		t.Fatalf("precondition: the GREEN promotion ended the needs-you: %q", got)
	}
	r.pass() // after_promote; the pass reads main at h2
	r.pass()
	st = r.a.Status().Project.Promote
	if st == nil || st.Red != nil || st.Run != nil || st.SHA != "h2" || st.At != r.now.Unix() || len(st.Tasks) != 0 ||
		st.Weight != 0 {
		t.Errorf("promoted h2: status promote %+v, want main at h2 promoted now, nothing RED, running or merged since", st)
	}
}
