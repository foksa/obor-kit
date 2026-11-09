package app_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"obor/internal/app"
	"obor/internal/app/event"
	"obor/internal/app/port"
	"obor/internal/app/wire"
	"obor/internal/herdr/herdrtest"
)

// T-961: the lane test of a candidate (row 18b) at the app boundary: the fake Scratch's Test refuses a dirty lane and
// "checks the candidate out" there while its hook runs, as checks.Scratch does over git (internal/checks'
// TestLaneTest* hold the git side: the checkout, the restore, the dirty refusal).

// A lane with uncommitted changes when its test is due: the test is refused before it touches the lane (never checked
// out, the change kept), counted as a failed step and tried again on later passes, then a needs-you; once the lane is
// clean, obor resume runs it again and the task goes on to its review.
func TestLoopDirtyLaneTestRetried(t *testing.T) {
	t.Parallel()
	r := newLoopRig(t)
	r.work("feature.txt", "feature\n")
	var checkouts atomic.Int32
	r.scratch.OnTest(func(context.Context, string) { checkouts.Add(1) })
	dirty := filepath.Join(r.lane, "scratch-note.txt")
	r.scratch.Arm("Gate", func(context.Context) error { // the worker writes on after its task_done
		return os.WriteFile(dirty, []byte("still writing\n"), 0o644)
	})
	r.start()
	r.submit()
	r.waitPrefix("1 fact needs-you{test failed: lane L-01 dirty")
	if !r.has("1 result gate") || r.has("1 result test") {
		t.Fatalf("precondition: the gate green, no test result: %q", r.steps())
	}
	if n := r.count("1 intent test"); n != 1 {
		t.Errorf("test intents %d, want 1: each try reuses the pending one", n)
	}
	failed := 0
	for _, s := range r.steps() {
		if strings.HasPrefix(s, "1 fact failed{test: lane L-01 dirty") {
			failed++
		}
	}
	if failed != 3 {
		t.Errorf("counted failures %d, want 3 (tried again on the later passes): %q", failed, r.steps())
	}
	if n := checkouts.Load(); n != 0 {
		t.Errorf("the dirty lane was checked out %d times, want never", n)
	}
	if got := r.scratch.LaneAt("L-01"); got != "" {
		t.Errorf("the dirty lane is at %s, want its branch", got)
	}
	if got := r.p.Read(dirty); got != "still writing\n" {
		t.Errorf("the worker's uncommitted file %q, want it kept", got)
	}

	r.scratch.Arm("Gate", nil)
	if err := os.Remove(dirty); err != nil {
		t.Fatal(err)
	}
	if reply, err := r.resume("r1", ""); err != nil || !strings.Contains(reply, "T-1") {
		t.Fatalf("resume: %q, %v", reply, err)
	}
	r.waitJob("pj-int", "T-1/1/review")
	if n := checkouts.Load(); n != 1 {
		t.Errorf("checkouts %d after the resume, want the one test", n)
	}
	if !r.has("1 result test") || r.count("1 intent test") != 1 {
		t.Errorf("the test ran under its pending key once the lane was clean: %q", r.steps())
	}
	if got := r.scratch.LaneAt("L-01"); got != "" {
		t.Errorf("the lane is at %s after its test, want its branch", got)
	}
}

// serve stops while a lane test runs: the cut test is no failure and leaves its intent pending, the lane back on its
// branch; the next serve's replay runs the test again under the same key, and the task goes on to its review.
func TestLoopRestartMidTestRunsItAgain(t *testing.T) {
	t.Parallel()
	r := newLoopRig(t)
	r.work("feature.txt", "feature\n")
	started := make(chan struct{})
	var runs atomic.Int32
	r.scratch.OnTest(func(ctx context.Context, lane string) {
		if runs.Add(1) == 1 {
			close(started)
			<-ctx.Done() // the first test runs until serve stops
		}
	})
	r.start()
	r.submit()
	select {
	case <-started:
	case <-time.After(r.patience()):
		t.Fatalf("no lane test began: %q", r.steps())
	}
	if got := r.scratch.LaneAt("L-01"); got == "" || got != r.scratch.Pin("candidate/T-1") {
		t.Fatalf("precondition: the lane is at the candidate while its test runs, got %q", got)
	}
	r.stop()
	if !r.has("1 intent test") || r.has("1 result test") || r.hasPrefix("1 fact failed{test") {
		t.Fatalf("the cut test: %q; want its intent pending, no result, no failure", r.steps())
	}
	if got := r.scratch.LaneAt("L-01"); got != "" {
		t.Errorf("the lane after the cut test is at %s, want its branch back", got)
	}

	r.start()
	r.waitJob("pj-int", "T-1/1/review")
	if n := runs.Load(); n != 2 {
		t.Errorf("test runs %d, want 2: the cut one and its replay", n)
	}
	if r.count("1 intent test") != 1 || r.count("1 result test") != 1 {
		t.Errorf("the replay ran under the pending key: %q", r.steps())
	}
	if got := r.scratch.LaneAt("L-01"); got != "" {
		t.Errorf("the lane after the replayed test is at %s, want its branch", got)
	}
}

// After a move the review job carries the task's own diff Base..S, not the merge onto the moved target: here a spec
// rev the lead set after the first review ends the accept (the one rule that reviews again, row 20), and review.2's
// diff is git diff <fork point> <submitted sha>, the main commit's file absent from it.
func TestLoopReviewDiffIsTheTasksOwnAfterAMove(t *testing.T) {
	t.Parallel()
	r := newLoopRig(t)
	r.work("feature.txt", "feature\n")
	r.start()
	r.submit()
	first := r.waitJob("pj-int", "T-1/1/review")
	submitted := r.scratch.LaneHead("L-01")
	base := r.scratch.LaneBase("L-01") // the lane's fork point
	want := "git diff " + base + " " + submitted
	if !strings.Contains(first, want) {
		t.Fatalf("precondition: the first review job names %q:\n%s", want, first)
	}
	r.spec("sp1", specOne)
	r.scratch.Move() // an unrelated commit on main
	r.accept("c1", r.patch(first))
	second := r.waitJob("pj-int", "T-1/1/review.2")
	diff, _, _ := strings.Cut(second, "Patch hash:")
	if !strings.Contains(diff, want) || !strings.Contains(diff, "feature.txt") || strings.Contains(diff, "other.txt") {
		t.Errorf("review.2's diff part, want the task's own %q (feature.txt, not other.txt):\n%s", want, diff)
	}
	if r.patch(second) != r.patch(first) {
		t.Errorf("review.2's patch %s, want the first's %s (the move keeps it)", r.patch(second), r.patch(first))
	}
}

// pendingRig is T-1 accepted and its publish answered pending (port.PublishPending, T-1029: the merge asked for, not
// done; no workflow returns it yet, the fake is scripted): the task waits in state pending; the candidate it published.
func pendingRig(t *testing.T) (*loopRig, string) {
	t.Helper()
	r, job := reviewed(t)
	cand := r.scratch.Pin("candidate/T-1")
	if cand == "" {
		t.Fatalf("precondition: T-1 has a candidate pin: %q", r.scratch.Pins())
	}
	r.scratch.Script("Publish", "T-1", port.PublishPending)
	r.accept("c1", r.patch(job))
	r.waitFor("T-1 pending", func() bool { return r.stateOf("T-1") == "pending" })
	return r, cand
}

// T-1029: a pending publish is not merged yet: the merge loop's publish step leaves the task pending (no merged fact,
// no second publish, no needs-you), each pass's Landed rule looks again, and once the target holds the merge the task
// is merged at its candidate (not a merge outside the app) and goes on to its after-merge steps.
func TestPublishPendingWaitsForLanded(t *testing.T) {
	t.Parallel()
	r, cand := pendingRig(t)
	r.later(time.Minute)
	r.passes()
	if r.has("1 fact merged") || r.scratch.Publishes.Load() != 1 || r.mainMerges() != 0 || r.hasPrefix("1 fact needs-you") {
		t.Fatalf("pending: want no merge, the one publish, no needs-you; publishes %d, merges %d: %q",
			r.scratch.Publishes.Load(), r.mainMerges(), r.steps())
	}
	if r.stateOf("T-1") != "pending" {
		t.Fatalf("after passes: state %q, want still pending", r.stateOf("T-1"))
	}
	r.scratch.Land("T-1") // the forge merged it
	r.merged(1)
	var merged event.MergedData
	for _, rec := range r.records() {
		if rec.Step == event.StepMerged && rec.Kind == event.KindFact {
			_ = json.Unmarshal(rec.Data, &merged)
		}
	}
	if merged.Head != cand || merged.Outside {
		t.Errorf("merged fact %+v, want head %s (the candidate), not outside", merged, cand)
	}
	if r.scratch.Publishes.Load() != 1 {
		t.Errorf("publishes %d, want the one: the Landed rule merged it, no second publish", r.scratch.Publishes.Load())
	}
}

// T-1029: obor status shows a pending task as it shows a published one: the publish phase and stage, its state
// pending.
func TestPublishPendingStatusLikePublished(t *testing.T) {
	t.Parallel()
	r, _ := pendingRig(t)
	got := r.task("T-1")
	if got.State != "pending" || got.Phase != wire.PhasePublish || got.Stage != wire.StagePublish || got.Ended {
		t.Errorf("status of a pending T-1: state %q phase %q stage %q ended %v; want pending, publish, publish, open",
			got.State, got.Phase, got.Stage, got.Ended)
	}
}

// T-1106: the publish and after_merge intents carry no data: no reader reads it (their effects replay from the folded
// candidate mt.cand; T-1091 had stored its identity there). The review job reads the gate result's copy instead.
func TestMergeIntentsCarryNoData(t *testing.T) {
	t.Parallel()
	r := newLoopRig(t)
	r.start()
	r.mergedTask()
	cand := r.published()
	if cand.Diff == "" || cand.Stat == "" { // precondition: there is data the old intents stored and no reader read
		t.Fatalf("precondition: the merged candidate has a diff and a stat: %+v", cand)
	}
	intents := 0
	for _, rec := range r.records() {
		if rec.Kind != event.KindIntent || (rec.Step != event.StepPublish && rec.Step != event.StepAfterMerge) {
			continue
		}
		intents++
		if len(rec.Data) != 0 {
			t.Errorf("%s intent carries data no reader reads: %s", rec.Step, rec.Data)
		}
	}
	if intents != 2 {
		t.Errorf("%d publish and after_merge intents, want one of each: %q", intents, r.steps())
	}
}

// T-758 (RV-93 #1), T-852: obor drop answers at once; the merge loop's worker.stop stops the task's worker through
// serve's identity of it (stopAgent, as the post-merge stop), never by a label lookup of the stop's own. One case
// table: the worker's pane as serve resolves it before the call, what the stop does, and its outcome. A stop that fails
// is a needs-you (RV-98 #2: else a live agent stays in the lane): nothing goes on until obor resume.
func TestWorkerStopByIdentity(t *testing.T) {
	t.Parallel()
	type outcome = stopOutcome
	for _, c := range []struct {
		name  string
		where string           // whereIs before the call (its prefix)
		arm   func(r *loopRig) // the state before the call
		want  outcome
	}{{
		name:  "live labelled worker: stopped by pane",
		where: "identified w1:p1",
		arm:   func(r *loopRig) {},
		want:  outcome{ok: true, stops: []string{"pj-l-01 w1:p1"}},
	}, {
		name:  "live unlabelled worker: stopped by pane, not not running",
		where: "identified w1:p1 session",
		arm: func(r *loopRig) {
			r.f.Set("w1:p1", func(p *herdrtest.Pane) { p.Label = "" })
			r.paneCreated("w1:p1", "")
		},
		want: outcome{ok: true, stops: []string{"pj-l-01 w1:p1"}},
	}, {
		name:  "worker gone: not running, the next step goes on",
		where: "unavailable",
		arm: func(r *loopRig) {
			r.f.Set("w1:p1", func(p *herdrtest.Pane) { p.Kind, p.Name, p.Status, p.Transcript = "", "", "", "" })
		},
		want: outcome{ok: true},
	}, {
		name:  "ambiguous: refused, nothing stopped",
		where: "ambiguous w1:p1 w1:p3",
		arm: func(r *loopRig) {
			var transcript string
			r.f.Set("w1:p1", func(p *herdrtest.Pane) { transcript = p.Transcript })
			r.f.Agent("w1:p3", "pj-l-01", "idle", r.lane)
			r.f.Set("w1:p3", func(p *herdrtest.Pane) { p.Label, p.Transcript = "pj-l-01", transcript })
			r.paneCreated("w1:p3", "pj-l-01")
		},
		want: outcome{why: "pj-l-01 not stopped: not identified: panes w1:p1 and w1:p3 run pj-l-01"},
	}, {
		name:  "herdr disconnected: nothing stopped, the stop waits",
		where: "unavailable not connected",
		arm: func(r *loopRig) {
			r.srv.Fail("session.snapshot", "unavailable", "herdr is down")
			r.srv.DropConns()
		},
		want: outcome{waits: true},
	}, {
		name:  "the stop of an identified pane fails: refused",
		where: "identified w1:p1",
		arm:   func(r *loopRig) { r.onStop = func() error { return errors.New("herdr: pane busy") } },
		want: outcome{why: "pj-l-01 not stopped: herdr: pane busy; obor resume T-1 retries",
			stops: []string{"pj-l-01 w1:p1"}},
	}, {
		name:  "the stop says not found while identity still finds the worker: refused",
		where: "identified w1:p1",
		arm:   func(r *loopRig) { r.onStop = func() error { return notFound{} } },
		want: outcome{why: "pj-l-01 not stopped: agent_not_found: pj-l-01; obor resume T-1 retries",
			stops: []string{"pj-l-01 w1:p1"}},
	}} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			r := newLoopRig(t, working())
			r.waitFor("pj-l-01 identified at w1:p1 (labelled)", func() bool {
				return strings.HasPrefix(app.WhereOf(r.app, r.p.Root, "pj-l-01"), "identified w1:p1")
			})
			r.mu.Lock()
			c.arm(r)
			r.mu.Unlock()
			if !strings.HasSuffix(c.where, "not connected") {
				// Wait for serve's stream after the arm's change, so the stop decides on it and never lands in a
				// resubscribe's gap (T-759). The case whose stream cannot come back (herdr down) has no such wait.
				r.connectedAgain()
			}
			var where string
			// No push here: a push is a refresh, and one landing after the barrier would cut serve off mid-call.
			r.waitFor("whereIs: "+c.where, func() bool { // connected (the barrier above), unless herdr is cut off
				where = app.WhereOf(r.app, r.p.Root, "pj-l-01")
				return strings.HasPrefix(where, c.where) &&
					(r.app.Status().Connected || strings.HasSuffix(c.where, "not connected"))
			})
			if c.where == "identified w1:p1 session" { // the label is not what holds it
				var label string
				r.f.Set("w1:p1", func(p *herdrtest.Pane) { label = p.Label })
				if label != "" {
					t.Fatalf("precondition: w1:p1 still labelled %q", label)
				}
			}
			checkStop(t, r, c.want, len(r.workerStops()))
		})
	}
}

// stopOutcome is the worker.stop's outcome in TestWorkerStopByIdentity.
type stopOutcome struct {
	ok    bool     // the stop done (stopped, or no agent): the drop goes on to dropped
	waits bool     // serve is cut off from herdr: no pass, so the stop waits (nothing stopped, no needs-you)
	why   string   // a refused stop's needs-you reason (its prefix): its line is the lead's one (no stop line)
	stops []string // the worker's Agents.Stop calls since the call: "<agent> <pane>"
}

// checkStop runs obor drop T-1 (T-775: serve frees its lane L-01), answered at once, and checks the merge loop's
// worker.stop: done (the drop dropped), waiting, or refused (a needs-you, the task still dropping, its stop pending).
func checkStop(t *testing.T, r *loopRig, want stopOutcome, before int) {
	t.Helper()
	reply, err := wire.Verdict(context.Background(), r.sock, &wire.DropReq{Envelope: wire.Envelope{Root: r.p.Root,
		Agent: "pj-lead", Role: "lead", CallID: "x1"}, Task: "T-1", Why: "not needed"})
	if want := "DROPPING T-1 from L-01 (not needed; stopping pj-l-01)"; err != nil || reply != want {
		t.Fatalf("drop: %q, %v; want %q", reply, err, want)
	}
	switch {
	case want.waits:
		if ts := r.task("T-1"); ts.Stopping != "pj-l-01" || ts.Phase == wire.PhaseNeedsYou {
			t.Errorf("T-1 %+v with serve cut off; want its stop pending, no needs-you", ts)
		}
	case want.ok:
		r.waitFor("T-1 dropped", func() bool { return r.stateOf("T-1") == "dropped" })
	default:
		r.waitFor("the stop's needs-you", func() bool { return r.task("T-1").Phase == wire.PhaseNeedsYou })
		if ts := r.task("T-1"); ts.Stopping != "pj-l-01" || r.has("1 fact dropped") {
			t.Errorf("refused, yet T-1 %+v (dropped %t): want its stop pending", ts, r.has("1 fact dropped"))
		}
		if why := r.needsYou().Fact().Reason; !strings.HasPrefix(why, want.why) {
			t.Errorf("needs-you %q, want %q", why, want.why)
		}
	}
	if stops := r.workerStops()[before:]; !slices.Equal(stops, want.stops) {
		t.Errorf("worker stops %q, want %q", stops, want.stops)
	}
	if !want.waits { // cut off from herdr, serve makes no pass
		r.passes()
	}
	if lines := r.stopLines(); len(lines) != 0 { // T-943: a done stop sends none, a refused one has its needs-you's
		t.Errorf("the lead's lines %q, want none", lines)
	}
}
