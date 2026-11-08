package app_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"obor/internal/app/event"
	"obor/internal/app/port"
	"obor/internal/app/wire"
	"regexp"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"obor/internal/app"
	"obor/internal/herdr/herdrtest"
)

// Recovery (T-338, RV-37): obor serve dies at any point of a merge-loop step, before its effect or after it with the
// result lost, and the restart takes the task on from there with the in-memory Scratch (app.FakeWorkflow: an effect
// that ran is in it, as it would be in git).

// crashAt makes the app's log refuse the record kind/step and every record after it, and ends the app there (the
// process died: nothing after it runs); the flag says it happened.
func (r *loopRig) crashAt(kind, step string) *atomic.Bool {
	var crashed atomic.Bool
	cancel := r.cancel
	app.Refuse(r.app, r.p.Root, func(rec event.Record) error {
		if crashed.Load() || rec.Kind == kind && rec.Step == step {
			crashed.Store(true)
			cancel()
			return errors.New("crash")
		}
		return nil
	})
	return &crashed
}

// appendIntent writes the intent of step for T-1's attempt 1 to the log of a stopped app: the crash came after the
// intent, before its effect.
func (r *loopRig) appendIntent(step, class string) {
	r.t.Helper()
	if err := app.WriteLog(r.p.Root, time.Now(),
		event.Record{Task: "T-1", Attempt: 1, Step: step, Kind: event.KindIntent, Class: class}); err != nil {
		r.t.Fatal(err)
	}
}

var jobRe = regexp.MustCompile(`^\[obor job ([^\]]+)\] `)

// answers is the integrator as a test plays it: each resolve job is resolved in the scratch (the open conflict
// committed) and answered resolved, each review job accepted (beforeAccept runs once first); only a job whose prompt
// has its result (sent). A verdict the app refuses (its log refused it: a crash) is given again later.
type answers struct {
	r            *loopRig
	beforeAccept func()
	done         map[string]bool
	calls        int
}

// pass answers the jobs not answered yet: each one woken (typed, or carried by the integrator's start) and sent is
// fetched by job_next, then answered.
func (a *answers) pass() {
	if a.done == nil {
		a.done = map[string]bool{}
	}
	for _, job := range a.r.woken("pj-int") {
		if a.done[job] || !a.r.sent(job) {
			continue
		}
		text, ok := a.r.fetch("pj-int", job)
		if !ok {
			continue
		}
		m := []string{"", job}
		p := sent{to: "pj-int", text: text}
		a.calls++
		id := fmt.Sprintf("c%d", a.calls)
		var err error
		if strings.HasSuffix(m[1], "/resolve") || strings.Contains(m[1], "/resolve.") {
			if a.r.scratch.Open() {
				a.r.scratch.Resolve()
			}
			_, err = a.r.verdict(wire.MethodResolved, id, "", "")
		} else {
			if a.beforeAccept != nil {
				a.beforeAccept()
				a.beforeAccept = nil
			}
			_, err = a.r.verdict(wire.MethodAccept, id, a.r.patch(p.text), "")
		}
		if err == nil {
			a.done[m[1]] = true
		}
	}
}

// sent: the prompt of job (<task>/<attempt>/<step>) has its result.
// woken is the jobs whose wake reached agent (typed, or a start's first prompt), each once, in order.
func (r *loopRig) woken(agent string) []string {
	var texts, out []string
	for _, p := range r.prompts() {
		if p.to == agent {
			texts = append(texts, p.text)
		}
	}
	r.mu.Lock()
	for _, l := range r.launches {
		if l.Agent == agent {
			texts = append(texts, l.Prompt+" ")
		}
	}
	r.mu.Unlock()
	seen := map[string]bool{}
	for _, text := range texts {
		if m := jobRe.FindStringSubmatch(text); m != nil && !seen[m[1]] {
			seen[m[1]] = true
			out = append(out, m[1])
		}
	}
	return out
}

// fetch is agent's job_next: the text of job with its tag when job_next hands out job (false: another job, or none).
func (r *loopRig) fetch(agent, job string) (string, bool) {
	role, lane := "worker", "L-01"
	if agent == "pj-int" {
		role, lane = "integrator", ""
	}
	raw, err := wire.Verdict(context.Background(), r.sock, wire.MethodJobNext, wire.Call{Root: r.p.Root, Agent: agent,
		Role: role, Lane: lane, CallID: fmt.Sprintf("rig-%d", r.callIDs.Add(1))})
	var jr wire.JobReply
	if err != nil || json.Unmarshal([]byte(raw), &jr) != nil || jr.Job != job {
		return "", false
	}
	return "[obor job " + job + "] " + jr.Text, true
}

func (r *loopRig) sent(job string) bool {
	for _, rec := range r.records() {
		if rec.Kind == event.KindResult && app.JobID(event.Key{Task: rec.Task, Attempt: rec.Attempt, Step: rec.Step}) == job {
			return true
		}
	}
	return false
}

// foreign is lane L-02 on task T-20 with its worker pj-l-02 in the project's workspace: no step of T-1 may touch it.
func (r *loopRig) foreign() string {
	r.t.Helper()
	lane := r.p.Lane("L-02", "task/T-20")
	r.scratch.AddLane("L-02", lane, "task/T-20")
	r.p.State("L-02", map[string]any{"lane": "L-02", "task": "T-20", "branch": "task/T-20", "agent": "pj-l-02",
		"base": "main", "dispatched_at": 1})
	w := r.f.Agent("w1:p2", "pj-l-02", "idle", lane)
	w.Workspace, w.Label = "w1", "pj-l-02" // labelled by its launch
	return lane
}

// A merge-loop scenario: how the task gets to its end, and what that end is.
type scenario struct {
	setup     func(r *loopRig) // the lane's work, main's commits and the scripted outcomes, before the app starts
	moveMain  bool             // main moves before the accept (an unrelated commit)
	end       string           // the record that ends it ("" merged)
	publishes int32            // Publish calls when merged
	jobs      int32            // integrator jobs, each its fresh start (T-602; 0: 1)
}

const sharedLines = "2\n3\n4\n5\n6\n7\n8\n9\n10\n"

var scenarios = map[string]scenario{
	"lane":     {setup: func(r *loopRig) { r.work("feature.txt", "feature\n") }, publishes: 1},
	"moved":    {setup: func(r *loopRig) { r.work("feature.txt", "feature\n") }, moveMain: true, publishes: 2},
	"resolved": {setup: (*loopRig).conflicting, publishes: 1, jobs: 2},
	// the resolution's lane test is red
	"resolved red": {setup: func(r *loopRig) {
		r.conflicting()
		r.scratch.Script("Test", "T-1", port.OutcomeRed)
	}, end: "1 fact sendback"},
	// the resolved gate's lane_setup fails
	"resolved fail": {setup: func(r *loopRig) {
		r.conflicting()
		r.scratch.Script("Gate resolved", "T-1", port.OutcomeFail+" FAIL lane_setup (rc 3)")
	}, end: "1 fact needs-you{gate: FAIL lane_setup (rc 3)}"},
}

// Each step's effect at both crash points (before it; done, result lost): the restart ends the task as without the
// crash: merged exactly once (one merge on main, one fact merged, after_merge's effect once: run again only after a
// cut between it and its result, idempotent (T-525), the lane's work committed once), no prompt sent twice, one integrator start per job (T-602), before any job reaches it, and lane L-02 untouched. A red or failed
// resolved gate replays on the same pinned resolution: one sendback, or one needs-you.
func TestLoopRecovery(t *testing.T) {
	t.Parallel()
	for _, row := range []struct{ scenario, step, class string }{
		{"lane", "submit", event.ClassGit},
		{"lane", "gate", event.ClassGit},
		{"lane", "start.review", event.ClassStart},
		{"lane", "publish", event.ClassGit},
		{"lane", "after_merge", event.ClassGit},
		{"lane", "agent.stop", event.ClassStart},
		{"moved", "gate.2", event.ClassGit},
		{"resolved", "gate", event.ClassGit},
		{"resolved", "start.resolve", event.ClassStart},
		{"resolved", "gate.2", event.ClassGit},
		{"resolved red", "gate.2", event.ClassGit},
		{"resolved fail", "gate.2", event.ClassGit},
	} {
		for _, before := range []bool{true, false} {
			point := "result lost"
			if before {
				point = "before the effect"
			}
			t.Run(row.scenario+"/"+row.step+"/"+point, func(t *testing.T) {
				t.Parallel()
				sc := scenarios[row.scenario]
				r := newLoopRig(t)
				r.foreign()
				sc.setup(r)
				var early atomic.Bool // a job prompt reached pj-int before it was started
				r.srv.Before(func(req herdrtest.Request) {
					if req.Method == "agent.prompt" && strings.Contains(string(req.Params), `"pj-int"`) && r.starts.Load() == 0 {
						early.Store(true)
					}
				})
				ans := &answers{r: r}
				if sc.moveMain {
					ans.beforeAccept = func() { r.scratch.Move() } // an unrelated commit on main
				}
				r.start()
				kind := event.KindResult
				if before {
					kind = event.KindIntent
				}
				crashed := r.crashAt(kind, row.step)
				r.submit()
				r.waitFor("the crash at "+kind+" "+row.step, func() bool { ans.pass(); return crashed.Load() })
				r.stop()
				if r.has("1 " + kind + " " + row.step) {
					t.Fatalf("precondition: the %s %s reached the log", row.step, kind)
				}
				if before {
					r.appendIntent(row.step, row.class)
				}
				r.start()
				end := sc.end
				if end == "" {
					end = "1 result agent.stop"
				}
				r.waitFor(end, func() bool {
					ans.pass()
					for _, s := range r.steps() {
						if strings.HasPrefix(s, end) {
							return true
						}
					}
					return false
				})

				jobs := map[string]int{}
				for _, p := range r.prompts() {
					if m := jobRe.FindStringSubmatch(p.text); m != nil {
						jobs[m[1]]++
					}
				}
				for job, n := range jobs {
					if n != 1 {
						t.Errorf("job %s prompted %d times", job, n)
					}
				}
				if want := max(sc.jobs, 1); r.starts.Load() != want || early.Load() {
					t.Errorf("integrator starts %d (want %d: one per job), a job before the start: %v", r.starts.Load(),
						want, early.Load())
				}
				for _, s := range r.workerStops() {
					if s != "pj-l-01 w1:p1" {
						t.Errorf("a stop of %s", s)
					}
				}
				if r.count("1 intent "+row.step) != 1 {
					t.Errorf("%d intents of %s, want 1: %q", r.count("1 intent "+row.step), row.step, r.steps())
				}
				r.waitFor("every intent with its result", func() bool { return len(r.open()) == 0 })
				if sc.end != "" {
					if r.mainMerges() != 0 || r.count("1 result gate.2") != 1 || r.count("1 fact sendback")+
						r.count("1 fact needs-you{gate: FAIL lane_setup (rc 3)}") != 1 {
						t.Errorf("want one resolved gate result, one sendback or needs-you, no merge: %q", r.steps())
					}
					return
				}
				publishes := sc.publishes
				if row.step == "publish" && !before {
					publishes++ // the replay's publish finds the candidate in the target: Already
				}
				runs := 1
				if row.step == "after_merge" && !before {
					runs++ // the replay runs the idempotent after_merge again (its intent is the once, T-525)
				}
				if r.mainMerges() != 1 || r.scratch.Publishes.Load() != publishes || r.count("1 fact merged") != 1 ||
					r.count("1 result after_merge") != 1 || r.scratch.AfterRuns("T-1") != runs {
					t.Errorf("merges %d, publishes %d (want %d), after_merge runs %d (want %d): %q",
						r.mainMerges(), r.scratch.Publishes.Load(), publishes, r.scratch.AfterRuns("T-1"), runs, r.steps())
				}
				if n := r.scratch.Commits("T-1"); n != 1 {
					t.Errorf("the lane has %d submit commits of T-1, want the one", n)
				}
				if r.scratch.LaneHead("L-02") != r.scratch.LaneBase("L-02") || r.scratch.LaneAt("L-02") != "" {
					t.Errorf("lane L-02 was touched: head %s, at %q", r.scratch.LaneHead("L-02"), r.scratch.LaneAt("L-02"))
				}
				if len(r.workerStops()) != 1 {
					t.Errorf("stops %q, want pj-l-01 once", r.stopped())
				}
			})
		}
	}
}

// RV-37 #2 (Sol's probe TestRV37StartIntentBeforeEffect): a start.review intent whose effect never ran, the
// integrator not running on restart: it is started once, carrying the review job's wake (T-524: every start under its
// job carries it), nothing typed.
func TestLoopStartIntentBeforeEffect(t *testing.T) {
	t.Parallel()
	r := newLoopRig(t)
	r.work("feature.txt", "feature\n")
	r.start()
	crashed := r.crashAt(event.KindIntent, "start.review")
	r.submit()
	r.waitFor("the crash", crashed.Load)
	r.stop()
	if r.starts.Load() != 0 || len(r.prompts()) != 0 {
		t.Fatalf("precondition: starts %d, prompts %d before the restart", r.starts.Load(), len(r.prompts()))
	}
	r.appendIntent("start.review", event.ClassStart)
	var early atomic.Bool
	r.srv.Before(func(req herdrtest.Request) {
		if req.Method == "agent.prompt" && r.starts.Load() == 0 {
			early.Store(true)
		}
	})
	r.start()
	r.waitJob("pj-int", "T-1/1/review")
	r.waitStep("1 result review")
	if r.starts.Load() != 1 || early.Load() || len(r.prompts()) != 0 || r.result("review") != `"sent (start prompt)"` {
		t.Errorf("starts %d, prompt before the start %v, prompts %d, review %s; want one start carrying the job",
			r.starts.Load(), early.Load(), len(r.prompts()), r.result("review"))
	}
	if r.count("1 intent start.review") != 1 || r.result("start.review") != `{"line":"STARTED pj-int","agent":"pj-int","pane":"w1:p9","kind":"claude","sid":"int-1"}` || r.has("1 intent start.review.2") {
		t.Errorf("start.review: want its one intent, the start's line as its result, no second key: %q", r.steps())
	}
}

// RV-37 #1 (Sol's probe TestRV37AfterMergeCrashThenLaneReused): after_merge ran, its result was lost, and while obor
// serve was down the lane took T-06 (its dispatch in the log). The replay stops nothing: T-06's worker stays.
func TestLoopAfterMergeCrashThenLaneReused(t *testing.T) {
	t.Parallel()
	r := newLoopRig(t)
	r.work("feature.txt", "feature\n")
	r.start()
	crashed := r.crashAt(event.KindResult, "after_merge")
	r.submit()
	r.accept("c1", r.patch(r.waitJob("pj-int", "T-1/1/review")))
	r.waitFor("the crash", crashed.Load)
	r.stop()
	// T-06's dispatch onto L-01 and its lane.prepare, in the log: its row takes the lane (T-439).
	d, _ := json.Marshal(map[string]any{"lane": "L-01", "dir": r.lane, "agent": "pj-l-01", "branch": "task/T-06",
		"target": "main", "reply": "DISPATCHED T-06 → L-01 (queued)"})
	if err := app.WriteLog(r.p.Root, time.Now(),
		event.Record{Task: "T-06", Attempt: 1, Step: event.StepDispatched, Kind: event.KindFact, Data: d},
		event.Record{Task: "T-06", Attempt: 1, Step: event.StepPrepare, Kind: event.KindIntent, Class: event.ClassGit},
		event.Record{Task: "T-06", Attempt: 1, Step: event.StepPrepare, Kind: event.KindResult,
			Data: json.RawMessage(`{"base":"main"}`)}); err != nil {
		t.Fatal(err)
	}
	r.setLane(func(l *port.Lane) { l.Task = "T-06" })
	r.start()
	r.merged(1)
	if len(r.workerStops()) != 0 || r.f.Pane("w1:p1").Name != "pj-l-01" {
		t.Errorf("the lane's worker was stopped: %q", r.stopped())
	}
	for _, step := range []string{"agent.stop"} {
		if res := r.result(step); res != `"skipped: lane L-01 holds T-06"` {
			t.Errorf("%s's result %s, want skipped", step, res)
		}
	}
}

// open is the steps of the log's intents that have no result.
func (r *loopRig) open() []string {
	pending := map[string]bool{}
	var order []string
	for _, rec := range r.records() {
		key := fmt.Sprint(rec.Task, " ", rec.Attempt, " ", rec.Step)
		switch rec.Kind {
		case event.KindIntent:
			pending[key] = true
			order = append(order, key)
		case event.KindResult:
			delete(pending, key)
		}
	}
	var out []string
	for _, k := range order {
		if pending[k] {
			out = append(out, k)
		}
	}
	return out
}

// result is the data of T-1's attempt-1 result of step.
func (r *loopRig) result(step string) string {
	for _, rec := range r.records() {
		if rec.Kind == event.KindResult && rec.Attempt == 1 && rec.Step == step {
			return string(rec.Data)
		}
	}
	return ""
}

// RV-37 #4 (Sol's probe TestRV37AfterMergeLockFailure): after_merge fails: no result, the step stays pending (one
// intent), the owner is told after three tries and it is not tried again (T-339: it would hold up every other task
// each pass), not even once it would pass; obor resume retries it (T-340's default for a failed step), and the
// needs-you ends.
func TestLoopAfterMergeFailure(t *testing.T) {
	t.Parallel()
	r := newLoopRig(t)
	r.work("feature.txt", "feature\n")
	heal := r.armScratch("AfterMerge")
	r.start()
	r.submit()
	r.accept("c1", r.patch(r.waitJob("pj-int", "T-1/1/review")))
	r.waitPrefix("1 fact needs-you{after_merge failed: git I/O: index.lock exists")
	r.waitFor("the needs-you in force", func() bool { return len(r.app.Status().Project.Facts) == 1 })
	r.later(time.Minute)
	heal()
	r.later(time.Minute)
	if n := r.scratch.AfterMerges.Load(); n != 3 || r.has("1 result after_merge") || r.count("1 intent after_merge") != 1 {
		t.Fatalf("after %d tries: want 3, no result, one intent: %q", n, r.steps())
	}
	r.mustResume("r1", "", "RESUMED T-1: retry (after_merge failed)")
	r.merged(1)
	if r.count("1 result after_merge") != 1 || r.count("1 intent after_merge") != 1 ||
		r.countPrefix("1 fact needs-you{after_merge") != 1 {
		t.Errorf("want one after_merge intent and result, one needs-you: %q", r.steps())
	}
	r.waitFor("the needs-you to end", func() bool { return len(r.app.Status().Project.Facts) == 0 })
}
