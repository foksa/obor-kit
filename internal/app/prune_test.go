package app

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"obor/internal/app/event"
	"obor/internal/app/port"
	"obor/internal/app/wire"
	"obor/internal/store"
)

// The nightly prune (prune.go, docs/research/event-log-pruning.md §4). Each test builds a history in a store,
// loads it as serve does, prunes it and loads it again; the oracle (pruneOracle) compares the two folds. No test
// writes a pruned log by hand.

// pruneNow is the prune's clock: the night the tests prune at.
var pruneNow = time.Date(2026, 11, 9, 2, 0, 0, 0, time.UTC)

// daysAgo is pruneNow n days back.
func daysAgo(n float64) time.Time { return pruneNow.Add(-time.Duration(n * float64(24*time.Hour))) }

// pruneRoot is a merging project (an integrator, a reviewer, two lanes) with no database yet.
func pruneRoot(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".obor", "state"), 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := `{"version":2,"project":{"name":"proj","agent_prefix":"pj"},"lanes":{"count":2},"agents":{"roles":{"integrator":"","reviewer":""}}}`
	if err := os.WriteFile(filepath.Join(root, ".obor", "config.json"), []byte(cfg), 0o644); err != nil {
		t.Fatal(err)
	}
	return root
}

// history writes a project's records at the times a test names, as serve's log would hold them.
type history struct {
	t *testing.T
	l *event.Log
}

// newHistory opens root's store as its writer; the test closes it (done) before serve loads it.
func newHistory(t *testing.T, root string) *history {
	t.Helper()
	db, err := store.OpenRW(event.DBFile(root))
	if err != nil {
		t.Fatal(err)
	}
	l, _, err := event.OpenLog(db)
	if err != nil {
		t.Fatal(err)
	}
	return &history{t: t, l: l}
}

// done closes the history's store.
func (h *history) done() { _ = h.l.Close() }

// add appends one record at at.
func (h *history) add(at time.Time, task string, attempt int, step, kind string, d any) event.Record {
	h.t.Helper()
	r := event.Record{Task: task, Attempt: attempt, Step: step, Kind: kind}
	if d != nil {
		r.Data, _ = json.Marshal(d)
	}
	r, err := h.l.Append(r, at)
	if err != nil {
		h.t.Fatal(err)
	}
	return r
}

// dispatched is task's dispatch of attempt to lane at at, its lane prepared (the lane's row is the task's).
func (h *history) dispatched(task string, attempt int, lane string, at time.Time) {
	h.add(at, task, attempt, event.StepDispatched, event.KindFact,
		dispatchData{Dispatch: port.Dispatch{Lane: lane, Agent: "pj-" + strings.ToLower(lane), Branch: "task/" + task}, Type: "m"})
	h.add(at, task, attempt, event.StepPrepare, event.KindIntent, nil)
	h.add(at, task, attempt, event.StepPrepare, event.KindResult, prepareOut{Base: "base0"})
}

// submitted is task's task_done of attempt from lane at at.
func (h *history) submitted(task string, attempt int, lane string, at time.Time) {
	h.add(at, task, attempt, event.StepSubmitted, event.KindFact,
		submitData{Lane: lane, Agent: "pj-" + strings.ToLower(lane), Report: "Summary: " + task})
}

// merged is task dispatched, submitted and merged at attempt 1 on lane, its merge at at; done: its after-merge
// steps ran.
func (h *history) merged(task, lane string, at time.Time, done bool) {
	h.dispatched(task, 1, lane, at.Add(-2*time.Hour))
	h.submitted(task, 1, lane, at.Add(-time.Hour))
	h.merge(task, 1, at, done)
}

// merge is task's merged fact of attempt at at (head: the task's), its after-merge steps after it when done.
func (h *history) merge(task string, attempt int, at time.Time, done bool) {
	h.add(at, task, attempt, event.StepMerged, event.KindFact, event.MergedData{Head: "h" + task})
	if done {
		for _, s := range afterSteps {
			h.add(at.Add(time.Minute), task, attempt, s, event.KindIntent, nil)
			h.add(at.Add(time.Minute), task, attempt, s, event.KindResult, nil)
		}
	}
}

// supersedeIdleKey is a review request withdrawn at at: a retained end newer than every task's end before it, so none
// of those tasks holds the idle key the prune keeps whole.
func (h *history) supersedeIdleKey(at time.Time) {
	h.add(at, event.ReviewTask, 0, event.StepReviewRequest, event.KindFact, ReviewData{By: "pj-lead"})
	h.add(at, event.ReviewTask, 0, event.StepReviewRm, event.KindFact, ReviewData{By: "pj-lead"})
}

// dropped is task dispatched and submitted at attempt 1 on lane, dropped at at.
func (h *history) dropped(task, lane string, at time.Time) {
	h.dispatched(task, 1, lane, at.Add(-2*time.Hour))
	h.submitted(task, 1, lane, at.Add(-time.Hour))
	h.drop(task, 1, at)
}

// drop is task's dropped fact of attempt at at.
func (h *history) drop(task string, attempt int, at time.Time) {
	h.add(at, task, attempt, event.StepDropped, event.KindFact, event.FactData{Reason: "superseded by " + task + "b"})
}

// rejected is task dispatched, submitted, gated green and its review rejected at at.
func (h *history) rejected(task, lane string, at time.Time) {
	h.dispatched(task, 1, lane, at.Add(-2*time.Hour))
	h.submitted(task, 1, lane, at.Add(-time.Hour))
	h.add(at, task, 1, event.StepGate, event.KindIntent, nil)
	h.add(at, task, 1, event.StepGate, event.KindResult, gateOut{Outcome: outGreen, Candidate: port.Candidate{SHA: "c", Patch: "p"}})
	h.add(at, task, 1, event.StepReview, event.KindIntent, JobData{Patch: "p"})
	h.add(at, task, 1, verdictStep(wire.MethodReject, "j-"+task), event.KindResult,
		verdictResult{Job: JobID(event.Key{Task: task, Attempt: 1, Step: event.StepReview}), Note: "not this"})
}

// session is agent's session fact id at at (transcript "": not known yet; a given one is the session's own confirmation).
func (h *history) session(agent, id, transcript string, at time.Time) {
	h.add(at, store.SessionTask, 0, store.StepSession, event.KindFact,
		store.Session{Agent: agent, ID: id, Transcript: transcript, Started: at.Unix(), Source: "hook"})
}

// pruneApp loads root as serve does at pruneNow.
func pruneApp(t *testing.T, root string) (*App, *proj) {
	t.Helper()
	now := pruneNow
	a := newTestApp(Options{Root: root, Now: func() time.Time { return now }, Loop: func(string) (*Loop, error) { return &Loop{}, nil }},
		&fakePanes{}, &fakeLanes{})
	p, err := a.load(root)
	if err != nil {
		t.Fatal(err)
	}
	a.p = p
	t.Cleanup(func() { p.log.Close() })
	if !p.merging {
		t.Fatal("precondition: the project does not merge")
	}
	return a, p
}

// pruned is the oracle's run: serve's fold before the prune (the state it ran with), the fold of the pruned log
// after a restart, each one's App, the log before and after, and the tasks that lost rows.
type pruned struct {
	a1, a2       *App
	before, then *proj
	recs, kept   []event.Record
	tasks        map[string]bool
}

// pruneOracle loads root, prunes it at pruneNow (batch tasks per transaction), loads it again and reports every
// difference between the two folds (foldDiff) as an error.
func pruneOracle(t *testing.T, root string, batch int) pruned {
	t.Helper()
	r := pruned{tasks: map[string]bool{}}
	r.a1, r.before = pruneApp(t, root)
	r.recs = readLog(t, root)
	r.a1.pruneLogIn(r.before, pruneNow, batch)
	r.kept = readLog(t, root)
	r.a2, r.then = pruneApp(t, root)
	count := map[string]int{}
	for _, rec := range r.kept {
		count[rec.Task]++
	}
	for _, rec := range r.recs {
		count[rec.Task]--
	}
	for task, n := range count {
		if n < 0 && task != store.SessionTask {
			r.tasks[task] = true
		}
	}
	for _, d := range foldDiff(r) {
		t.Error(d)
	}
	return r
}

// foldDiff is every difference between the two folds that the prune must not make: an unpruned task's whole state; a
// pruned one's outcome (state, attempt, head, merged and dropped seqs, the drop's rev and reason, its end); the lanes
// table, the watchdog facts, the rules' did keys, the reviews and their merges, the watches, the idle state, the open
// lines, each agent's current session and start row (its verdicts counted since its start, rolestop's question), the
// pending intents and obor status (a pruned dropped task
// loses its lane, start time and effort there). Per-task leftovers of a pruned task (stored call replies, deliveries, start
// times) differ by design and are not compared.
func foldDiff(r pruned) []string {
	var out []string
	diff := func(what string, a, b any) {
		if !reflect.DeepEqual(a, b) {
			out = append(out, fmt.Sprintf("%s:\n before %+v\n after  %+v", what, a, b))
		}
	}
	p, q := r.before, r.then
	for _, task := range p.loopTasks() {
		a, b := p.folded.loop[task], q.folded.loop[task]
		switch {
		case b == nil:
			out = append(out, "task "+task+" gone after the prune")
		case !r.tasks[task]:
			diff("task "+task, *a, *b)
		default:
			diff("pruned task "+task, outcomeOf(a), outcomeOf(b))
		}
	}
	for _, task := range q.loopTasks() {
		if p.folded.loop[task] == nil {
			out = append(out, "task "+task+" new after the prune")
		}
	}
	diff("lanes table", p.laneTable(), q.laneTable())
	diff("watchdog facts", p.folded.active, q.folded.active)
	diff("did keys", p.folded.did, q.folded.did)
	diff("reviews", p.folded.rv, q.folded.rv)
	diff("watches", p.folded.watches, q.folded.watches)
	diff("idle ack", p.folded.idleAck, q.folded.idleAck)
	diff("brief ends", p.folded.briefEnds, q.folded.briefEnds)
	diff("review ends", []idleEnd{p.folded.reviewIdleEnd, p.folded.reviewEnd}, []idleEnd{q.folded.reviewIdleEnd, q.folded.reviewEnd})
	diff("idle", p.idleNow(), q.idleNow())
	diff("open lines", lines(p), lines(q))
	agents := map[string]bool{}
	for _, s := range p.folded.sessions.All() {
		agents[s.Agent] = true
	}
	for agent := range agents {
		s1, ok1 := p.folded.sessions.Current(agent)
		s2, ok2 := q.folded.sessions.Current(agent)
		diff("current session of "+agent, []any{s1, ok1}, []any{s2, ok2})
	}
	diff("started agents", p.reg.StartedAgents(), q.reg.StartedAgents())
	for _, agent := range p.reg.StartedAgents() { // its verdicts as its reader asks: how many came since its start
		s1, _ := p.reg.Start(agent)
		s2, _ := q.reg.Start(agent)
		s1.Verdicts, s2.Verdicts = p.folded.verdicts-s1.Verdicts, q.folded.verdicts-s2.Verdicts
		diff("start row of "+agent+" (verdicts since its start)", s1, s2)
	}
	diff("pending intents", pendingKeys(p), pendingKeys(q))
	diff("status", statusOf(r.a1, r.tasks), statusOf(r.a2, r.tasks))
	return out
}

// outcome is what a pruned task keeps of its state.
type outcome struct {
	State, Head, Dropped     string
	Attempt, DroppedRev      int
	MergedSeq, DroppedSeq    int64
	EndedAt                  time.Time
	AfterDone, Paused, Ended bool
}

func outcomeOf(mt *mtask) outcome {
	return outcome{State: mt.state, Head: mt.head, Dropped: mt.dropped, Attempt: mt.attempt, DroppedRev: mt.droppedRev,
		MergedSeq: mt.mergedSeq, DroppedSeq: mt.droppedSeq, EndedAt: mt.endedAt, AfterDone: mt.after >= len(afterSteps),
		Paused: mt.paused(), Ended: states[mt.state].ended}
}

// lines is p's open lines: each one's seq, state and message.
func lines(p *proj) []string {
	var out []string
	for _, l := range p.out.Queued() {
		out = append(out, fmt.Sprintf("%d %v %+v answer=%v", l.Seq(), l.State(), l.Msg(), l.IsAnswer()))
	}
	return out
}

// pendingKeys is p's pending intents' keys, sorted.
func pendingKeys(p *proj) []string {
	var out []string
	for _, r := range p.log.Pending() {
		out = append(out, fmt.Sprintf("%s/%d/%s", r.Task, r.Attempt, r.Step))
	}
	slices.Sort(out)
	return out
}

// statusOf is a's obor status as JSON, its serve start time cleared, and a pruned dropped task's lane, start time and
// effort (its dispatch and start records went).
func statusOf(a *App, pruned map[string]bool) string {
	st := a.Status()
	st.Since = ""
	if ps := st.Project; ps != nil {
		for i, ts := range ps.Tasks {
			if pruned[ts.Task] {
				ps.Tasks[i].Lane, ps.Tasks[i].Started, ps.Tasks[i].Effort = "", 0, ""
			}
		}
	}
	b, _ := json.Marshal(st)
	return string(b)
}

// rowsOf is how many records of task the log holds.
func rowsOf(recs []event.Record, task string) int {
	n := 0
	for _, r := range recs {
		if r.Task == task {
			n++
		}
	}
	return n
}

// onlyTerminal: task's one record in recs is its fact step.
func onlyTerminal(recs []event.Record, task, step string) bool {
	var of []event.Record
	for _, r := range recs {
		if r.Task == task {
			of = append(of, r)
		}
	}
	return len(of) == 1 && of[0].Kind == event.KindFact && of[0].Step == step
}

// The §4 table's first two rows: of six tasks, only the one merged 31 days ago (its after-merge steps done) and the
// one dropped 31 days ago lose rows, each keeping its terminal fact; the folds before and after are equal (the
// oracle).
func TestPruneEndedTasks(t *testing.T) {
	t.Parallel()
	root := pruneRoot(t)
	h := newHistory(t, root)
	h.merged("T-m31", "L-01", daysAgo(31), true)
	h.dropped("T-d31", "L-01", daysAgo(31))
	h.rejected("T-r40", "L-01", daysAgo(40))
	h.merged("T-a31", "L-02", daysAgo(31), false) // after_merge still to run
	h.merged("T-m29", "L-01", daysAgo(29), true)
	h.dispatched("T-open", 1, "L-02", daysAgo(40)) // open: holds L-02
	h.done()
	r := pruneOracle(t, root, pruneBatch)
	if want := map[string]bool{"T-m31": true, "T-d31": true}; !reflect.DeepEqual(r.tasks, want) {
		t.Errorf("pruned %v, want %v", r.tasks, want)
	}
	if !onlyTerminal(r.kept, "T-m31", event.StepMerged) || !onlyTerminal(r.kept, "T-d31", event.StepDropped) {
		t.Errorf("T-m31 keeps %d rows, T-d31 %d; want each its terminal fact alone", rowsOf(r.kept, "T-m31"), rowsOf(r.kept, "T-d31"))
	}
	for _, task := range []string{"T-r40", "T-a31", "T-m29", "T-open"} {
		if rowsOf(r.kept, task) != rowsOf(r.recs, task) || rowsOf(r.recs, task) < 2 {
			t.Errorf("%s: %d of %d rows kept, want all", task, rowsOf(r.kept, task), rowsOf(r.recs, task))
		}
	}
	if mt := r.then.folded.loop["T-m31"]; mt == nil || mt.state != stMerged || mt.head != "hT-m31" || mt.attempt != 1 {
		t.Errorf("T-m31 after the prune: %+v; want merged at hT-m31, attempt 1", mt)
	}
	if mt := r.then.folded.loop["T-d31"]; mt == nil || mt.state != stDropped || mt.dropped != "superseded by T-d31b" {
		t.Errorf("T-d31 after the prune: %+v; want dropped with its reason", mt)
	}
}

// Dropped at attempt 1, dispatched again, merged at attempt 2, 31 days ago: only the merged fact stays; the task
// folds to merged at attempt 2 and the reviews' merges still name it.
func TestPruneKeepsTheLastTerminalFact(t *testing.T) {
	t.Parallel()
	root := pruneRoot(t)
	h := newHistory(t, root)
	h.dropped("T-x", "L-01", daysAgo(33))
	h.dispatched("T-x", 2, "L-01", daysAgo(32))
	h.submitted("T-x", 2, "L-01", daysAgo(31.5))
	h.merge("T-x", 2, daysAgo(31), true)
	h.supersedeIdleKey(daysAgo(2))
	h.dispatched("T-later", 1, "L-01", daysAgo(1)) // the lane's row is another task's
	h.done()
	r := pruneOracle(t, root, pruneBatch)
	if !onlyTerminal(r.kept, "T-x", event.StepMerged) {
		t.Fatalf("T-x keeps %d rows, want its merged fact alone", rowsOf(r.kept, "T-x"))
	}
	if mt := r.then.folded.loop["T-x"]; mt == nil || mt.state != stMerged || mt.attempt != 2 {
		t.Errorf("T-x after the prune: %+v; want merged at attempt 2", mt)
	}
	if m := r.then.folded.rv.Next().Scope; len(m) != 1 || m[0].Task() != "T-x" || m[0].SHA() != "hT-x" {
		t.Errorf("the reviews' merges %+v, want T-x's", m)
	}
}

// Merged at attempt 1, dispatched again and dropped at attempt 2, 31 days ago. The per-task check
// fails (a merged fact other than the one that would stay): the task stays whole.
func TestPruneKeepsAMergedThenDroppedTaskWhole(t *testing.T) {
	t.Parallel()
	root := pruneRoot(t)
	h := newHistory(t, root)
	h.merged("T-789", "L-01", daysAgo(34), true)
	h.dispatched("T-789", 2, "L-01", daysAgo(33))
	h.drop("T-789", 2, daysAgo(31))
	h.done()
	r := pruneOracle(t, root, pruneBatch)
	if mt := r.before.folded.loop["T-789"]; mt == nil || mt.state != stDropped || !mt.endedAt.Before(daysAgo(30)) {
		t.Fatalf("precondition: T-789 not dropped over 30 days ago: %+v", mt)
	}
	if len(r.tasks) != 0 || len(r.kept) != len(r.recs) {
		t.Errorf("pruned %v (%d of %d rows kept), want T-789 whole", r.tasks, len(r.kept), len(r.recs))
	}
}

// The lead's answer to a task's question, the task since submitted and merged, then pruned: no open line after the
// fold (fact merged settles the question, as the deleted submission did).
func TestPrunedAnswerStaysSettled(t *testing.T) {
	t.Parallel()
	root := pruneRoot(t)
	h := newHistory(t, root)
	h.dispatched("T-q", 1, "L-01", daysAgo(35))
	h.add(daysAgo(34), "T-q", 1, event.StepNeedsYou, event.KindFact, event.FactData{Lane: "L-01", Reason: reasonAsks + "which way?"})
	h.add(daysAgo(34), event.MsgTask, 0, event.StepAnswered, event.KindFact,
		event.Message{To: "pj-l-01", Line: "[obor] lead: left", From: "pj-lead", Task: "T-q", Call: "a1", Reply: "ANSWERED"})
	h.submitted("T-q", 1, "L-01", daysAgo(33))
	h.merge("T-q", 1, daysAgo(32), true)
	h.supersedeIdleKey(daysAgo(2))
	h.dispatched("T-later", 1, "L-01", daysAgo(1))
	h.done()
	r := pruneOracle(t, root, pruneBatch)
	if !r.tasks["T-q"] {
		t.Fatalf("precondition: T-q not pruned (%v)", r.tasks)
	}
	if l := lines(r.then); len(l) != 0 {
		t.Errorf("open lines after the prune: %q, want none", l)
	}
}

// After the prune: a waiting dispatch --after a pruned merged task starts; a dispatch of the pruned merged task is
// refused as before; a dispatch of a pruned dropped task takes the dropped fact's attempt plus one; a watch on either
// gets its line as before.
func TestPrunedTasksOutcomes(t *testing.T) {
	t.Parallel()
	root := pruneRoot(t)
	h := newHistory(t, root)
	h.add(daysAgo(40), "T-w", 1, event.StepWaiting, event.KindFact, dispatchData{Dispatch: port.Dispatch{Lane: "L-02"},
		After: []string{"T-m"}})
	h.merged("T-m", "L-01", daysAgo(35), true)
	h.dispatched("T-d", 1, "L-01", daysAgo(34))
	h.dispatched("T-d", 2, "L-01", daysAgo(33))
	h.drop("T-d", 2, daysAgo(32))
	h.supersedeIdleKey(daysAgo(2))
	h.dispatched("T-later", 1, "L-01", daysAgo(1))
	h.done()
	r := pruneOracle(t, root, pruneBatch)
	if !r.tasks["T-m"] || !r.tasks["T-d"] || r.tasks["T-w"] {
		t.Fatalf("precondition: pruned %v, want T-m and T-d", r.tasks)
	}
	if w := r.then.folded.loop["T-w"]; w == nil || w.state != stWaiting || !r.then.afterMerged(w) {
		t.Errorf("T-w after the prune: %+v; want waiting with its --after merged", w)
	}
	f := newConcFakes("")
	fx := &Loop{Workflow: WorkflowOf(f), Specs: f, Agents: f}
	for _, c := range []struct {
		a *App
		p *proj
	}{{r.a1, r.before}, {r.a2, r.then}} {
		if _, err := c.a.dispatchTask(t.Context(), c.p, fx, wire.DispatchReq{Task: "T-m"}); err == nil ||
			err.Error() != "T-m merged hT-m: a new attempt needs a new task id" {
			t.Errorf("dispatch of the merged T-m: %v; want refused (T-792)", err)
		}
		if mt := c.p.folded.loop["T-d"]; mt == nil || mt.nextAttempt() != 3 {
			t.Errorf("T-d: %+v; want its next dispatch at attempt 3", mt)
		}
		c.a.mu.Lock()
		merged, dropped := c.p.outcome("T-m"), c.p.outcome("T-d")
		c.a.mu.Unlock()
		if merged != "T-m merged hT-m" || dropped != "T-d dropped: superseded by T-db" {
			t.Errorf("watch lines %q, %q; want T-m's merge and T-d's drop", merged, dropped)
		}
	}
}

// A task ended 31 days ago stays whole while a lanes row names it, while it recorded an agent's current start row, and
// while a review is open that started before its dispatch (fixCandidates links it).
func TestPruneKeepsPinnedTasksWhole(t *testing.T) {
	t.Parallel()
	root := pruneRoot(t)
	h := newHistory(t, root)
	h.merged("T-row", "L-02", daysAgo(31), true) // L-02's row: nothing dispatched there since
	h.merged("T-start", "L-01", daysAgo(33), true)
	h.add(daysAgo(33), "T-start", 1, event.StepStartWork, event.KindResult,
		startOut{Agent: "pj-old", Pane: "w9:p1", Terminal: "t9", Kind: "claude", Session: "s9"})
	h.add(daysAgo(36), event.ReviewTask, 0, event.StepReviewStarted, event.KindFact, ReviewData{ID: "RV-1", Head: "h0"})
	h.merged("T-fix", "L-01", daysAgo(32), true) // dispatched after RV-1 started, RV-1 still open
	h.dispatched("T-later", 1, "L-01", daysAgo(1))
	h.done()
	r := pruneOracle(t, root, pruneBatch)
	if row, ok := r.before.reg.Start("pj-old"); !ok || row.Task != "T-start" {
		t.Fatalf("precondition: pj-old's start row %+v, want T-start's", row)
	}
	if rows := r.before.laneTable(); !slices.ContainsFunc(rows, func(l store.LaneRow) bool { return l.Task == "T-row" }) {
		t.Fatalf("precondition: no lanes row names T-row: %+v", rows)
	}
	if len(r.tasks) != 0 || len(r.kept) != len(r.recs) {
		t.Errorf("pruned %v (%d of %d rows kept), want every task whole", r.tasks, len(r.kept), len(r.recs))
	}
}

// A task ended 31 days ago whose start displaced another agent's row (axis-alies' oracle run: pj-x started again on
// T-d's lane, then pj-y's start on that pane took pj-x's row) stays whole: without its start result pj-x's older row
// would come back.
func TestPruneKeepsAStartThatDisplacedARow(t *testing.T) {
	t.Parallel()
	root := pruneRoot(t)
	h := newHistory(t, root)
	start := func(task, agent, pane string, at time.Time) {
		h.add(at, task, 1, event.StepStartWork, event.KindResult, startOut{Agent: agent, Pane: pane, Terminal: "t-" + pane, Kind: "claude"})
	}
	h.dispatched("T-k", 1, "L-01", daysAgo(40)) // open
	start("T-k", "pj-x", "w1:p1", daysAgo(40))
	h.merged("T-d", "L-02", daysAgo(35), true)
	start("T-d", "pj-x", "w1:p2", daysAgo(36))
	h.dispatched("T-e", 1, "L-02", daysAgo(1))
	start("T-e", "pj-y", "w1:p2", daysAgo(1))
	h.done()
	r := pruneOracle(t, root, pruneBatch)
	if _, ok := r.before.reg.Start("pj-x"); ok {
		t.Fatal("precondition: pj-x has a start row; want it displaced by pj-y's start")
	}
	if len(r.tasks) != 0 || rowsOf(r.kept, "T-d") != rowsOf(r.recs, "T-d") {
		t.Errorf("pruned %v (T-d keeps %d of %d rows), want T-d whole", r.tasks, rowsOf(r.kept, "T-d"), rowsOf(r.recs, "T-d"))
	}
}

// The newest rows of the table are the pruned task's: the newest stays, and the next record's seq is the old maximum
// plus one (no seq is handed out again). T-m's end is not the idle key (that task is kept whole): a
// review request's end supersedes it, and the newest row is the receipt of a later spec_set on T-m.
func TestPruneKeepsTheNewestRow(t *testing.T) {
	t.Parallel()
	root := pruneRoot(t)
	h := newHistory(t, root)
	h.merged("T-m", "L-09", daysAgo(33), true) // a lane gone from the config: no row names T-m
	h.supersedeIdleKey(daysAgo(32))
	h.add(daysAgo(31), "T-m", 0, event.StepSpecSet, event.KindFact, event.SpecSet{Call: "c-m", Reply: "SET T-m rev 2"})
	h.done()
	r := pruneOracle(t, root, pruneBatch)
	last := r.recs[len(r.recs)-1]
	if !r.tasks["T-m"] || last.Task != "T-m" {
		t.Fatalf("precondition: pruned %v, newest record %+v", r.tasks, last)
	}
	if got := r.kept[len(r.kept)-1]; got.Seq != last.Seq || rowsOf(r.kept, "T-m") != 2 {
		t.Errorf("newest kept %+v (T-m keeps %d rows); want seq %d and the merged fact", got, rowsOf(r.kept, "T-m"), last.Seq)
	}
	next, err := r.then.log.Append(event.Record{Task: event.LoopTask, Step: "probe", Kind: event.KindFact}, pruneNow)
	if err != nil || next.Seq != last.Seq+1 {
		t.Errorf("next record %+v, %v; want seq %d", next, err, last.Seq+1)
	}
}

// A record of a chosen task appended between the selection and the delete stays; a second prune deletes nothing.
func TestPruneSparesLaterRecordsAndRunsOnce(t *testing.T) {
	t.Parallel()
	root := pruneRoot(t)
	h := newHistory(t, root)
	h.merged("T-m", "L-01", daysAgo(31), true)
	h.supersedeIdleKey(daysAgo(2))
	h.dispatched("T-later", 1, "L-01", daysAgo(1))
	h.done()
	a, p := pruneApp(t, root)
	var late event.Record
	hook := func(point string) {
		if point == hookPruneSelected && late.Seq == 0 {
			r, err := p.log.Append(event.Record{Task: "T-m", Attempt: 1, Step: event.StepFailed, Kind: event.KindFact}, pruneNow)
			if err != nil {
				t.Error(err)
			}
			late = r
		}
	}
	a.hooked.Store(&hook)
	before := readLog(t, root)
	a.pruneLog(p, pruneNow)
	after := readLog(t, root)
	if late.Seq == 0 || !slices.ContainsFunc(after, func(r event.Record) bool { return r.Seq == late.Seq }) {
		t.Errorf("the record appended after the selection (%d) is gone", late.Seq)
	}
	if n := rowsOf(after, "T-m"); n != 2 || rowsOf(before, "T-m") < 4 {
		t.Errorf("T-m keeps %d of %d rows; want its merged fact and the late record", n, rowsOf(before, "T-m"))
	}
	a.pruneLog(p, pruneNow)
	if again := readLog(t, root); len(again) != len(after) {
		t.Errorf("the second prune deleted %d rows, want none", len(after)-len(again))
	}
}

// A failed transaction in the second batch: the first batch's tasks are pruned, the others stay whole.
func TestPruneBatchFailure(t *testing.T) {
	t.Parallel()
	root := pruneRoot(t)
	h := newHistory(t, root)
	for _, task := range []string{"T-1", "T-2", "T-3"} {
		h.merged(task, "L-01", daysAgo(31), true)
	}
	h.dispatched("T-later", 1, "L-01", daysAgo(1))
	h.done()
	a, p := pruneApp(t, root)
	if _, err := p.log.DB().Exec(`CREATE TRIGGER nodelete BEFORE DELETE ON events WHEN old.task = 'T-2'
		BEGIN SELECT RAISE(ABORT, 'disk I/O error'); END`); err != nil {
		t.Fatal(err)
	}
	before := readLog(t, root)
	a.pruneLogIn(p, pruneNow, 1)
	after := readLog(t, root)
	if !onlyTerminal(after, "T-1", event.StepMerged) {
		t.Errorf("T-1 keeps %d rows, want its merged fact alone", rowsOf(after, "T-1"))
	}
	for _, task := range []string{"T-2", "T-3"} {
		if rowsOf(after, task) != rowsOf(before, task) {
			t.Errorf("%s keeps %d of %d rows, want all", task, rowsOf(after, task), rowsOf(before, task))
		}
	}
}

// An agent with three sessions older than 30 days, the newest confirmed (its start and its transcript): the newest
// keeps both records, the others go, and CurrentAt answers the same; a session of another agent written 10 days ago
// stays.
func TestPruneOldSessions(t *testing.T) {
	t.Parallel()
	root := pruneRoot(t)
	h := newHistory(t, root)
	h.session("pj-lead", "s1", "", daysAgo(50))
	h.session("pj-lead", "s2", "/t/s2.jsonl", daysAgo(45))
	h.session("pj-lead", "s3", "", daysAgo(40))
	h.session("pj-lead", "s3", "/t/s3.jsonl", daysAgo(40))
	h.session("pj-l-01", "s4", "", daysAgo(10))
	h.dispatched("T-later", 1, "L-01", daysAgo(1))
	h.done()
	cur, ok := store.CurrentAt(root, "pj-lead")
	if !ok || cur.ID != "s3" || cur.Transcript != "/t/s3.jsonl" {
		t.Fatalf("precondition: pj-lead's current session %+v", cur)
	}
	r := pruneOracle(t, root, pruneBatch)
	var ids []string
	for _, rec := range r.kept {
		if rec.Task == store.SessionTask {
			var s store.Session
			_ = json.Unmarshal(rec.Data, &s)
			ids = append(ids, s.Agent+"/"+s.ID)
		}
	}
	if want := []string{"pj-lead/s3", "pj-lead/s3", "pj-l-01/s4"}; !slices.Equal(ids, want) {
		t.Errorf("session records kept %q, want %q", ids, want)
	}
	if got, ok := store.CurrentAt(root, "pj-lead"); !ok || got != cur {
		t.Errorf("CurrentAt after the prune %+v, %v; want %+v", got, ok, cur)
	}
}

// The backup failed: nothing is pruned. Nightly without serve (wire.Backup's direct copy): nothing is pruned. The
// backup of a serve that loaded the project prunes after the backup is written, which holds every pruned row.
func TestPruneRunsOnlyAfterServesBackup(t *testing.T) {
	t.Parallel()
	root := pruneRoot(t)
	h := newHistory(t, root)
	h.merged("T-m", "L-01", daysAgo(31), true)
	h.supersedeIdleKey(daysAgo(2))
	h.dispatched("T-later", 1, "L-01", daysAgo(1))
	h.done()
	whole := readLog(t, root)
	// No serve listens on the socket: the call ends at once, not at the deadline table's 15 s.
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	if _, err := wire.Backup(ctx, filepath.Join(t.TempDir(), "none.sock"), root, pruneNow); err != nil {
		t.Fatal(err)
	}
	if got := readLog(t, root); len(got) != len(whole) {
		t.Errorf("the direct backup deleted %d rows, want none", len(whole)-len(got))
	}
	a, _ := pruneApp(t, root)
	dir := wire.BackupDir(root)
	if err := os.RemoveAll(dir); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dir, []byte("not a directory"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := a.backup(wire.BackupReq{Envelope: wire.Envelope{Root: root}}); err == nil {
		t.Fatal("precondition: the backup did not fail")
	}
	if got := readLog(t, root); len(got) != len(whole) {
		t.Errorf("a failed backup pruned %d rows, want none", len(whole)-len(got))
	}
	if err := os.Remove(dir); err != nil {
		t.Fatal(err)
	}
	reply, err := a.backup(wire.BackupReq{Envelope: wire.Envelope{Root: root}})
	if err != nil {
		t.Fatal(err)
	}
	if got := readLog(t, root); !onlyTerminal(got, "T-m", event.StepMerged) {
		t.Errorf("after serve's backup T-m keeps %d rows, want its merged fact", rowsOf(got, "T-m"))
	}
	if recs, err := event.ReadLog(backupRoot(t, strings.TrimPrefix(reply, "BACKUP "))); err != nil || len(recs) != len(whole) {
		t.Errorf("the night's backup holds %d records, %v; want all %d", len(recs), err, len(whole))
	}
}

// idleOld is T-old merged 40 days ago on a lane gone from the config (no row names it), its after-merge steps done,
// then later (records after it: newer seqs) what extra writes; ack: the lead's idle.ack names T-old's last after-merge
// record (its idle key) when no end follows.
func idleOld(t *testing.T, extra func(h *history), ack bool) (string, int64) {
	t.Helper()
	root := pruneRoot(t)
	h := newHistory(t, root)
	h.merged("T-old", "L-09", daysAgo(40), true)
	key := h.l.LastSeq()
	if extra != nil {
		extra(h)
	}
	if ack {
		h.add(daysAgo(39), event.IdleTask, 0, event.StepIdleAck, event.KindFact, idleAck{Key: key, Why: "nothing next", By: "pj-lead"})
	}
	h.done()
	return root, key
}

// idleOf is p's idleNow answer and whether the idle rule would post its line now (under mu).
func idleOf(a *App, p *proj) (idle, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	return p.idleOpen()
}

// An idle project whose last merged task, 40 days old, holds the idle key the lead acknowledged:
// the prune keeps it whole, the key is the same after the prune and a restart, and the idle line does not come again.
func TestPruneKeepsTheIdleKeysTask(t *testing.T) {
	t.Parallel()
	root, key := idleOld(t, nil, true)
	r := pruneOracle(t, root, pruneBatch)
	n1, open1 := idleOf(r.a1, r.before)
	if !n1.ok || n1.end != key || open1 || r.before.out.Lead() == "" || r.before.folded.idleAck.Key != key {
		t.Fatalf("precondition: idle %+v (open %t, lead %q, ack %d); want idle at key %d, acknowledged", n1, open1,
			r.before.out.Lead(), r.before.folded.idleAck.Key, key)
	}
	if len(r.tasks) != 0 || rowsOf(r.kept, "T-old") != rowsOf(r.recs, "T-old") {
		t.Errorf("pruned %v (T-old keeps %d of %d rows), want T-old whole", r.tasks, rowsOf(r.kept, "T-old"), rowsOf(r.recs, "T-old"))
	}
	n2, open2 := idleOf(r.a2, r.then)
	if n2.end != n1.end || !n2.ok {
		t.Errorf("idle key after the prune and replay %+v, want %d", n2, n1.end)
	}
	if _, _, ok := idleRule(t.Context(), r.a2, r.then, nil, pruneNow); ok || open2 {
		t.Errorf("the idle rule would post a new line after the prune (key %d, ack %d)", n2.end, r.then.folded.idleAck.Key)
	}
}

// A later end the prune keeps supersedes T-old's: T-old is pruned to its merged fact and the idle key is
// the same before and after.
func TestPruneIdleKeySuperseded(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		name  string
		extra func(h *history)
	}{
		{"a newer merge", func(h *history) { h.merged("T-new", "L-01", daysAgo(2), true) }},
		{"a drop", func(h *history) { h.dropped("T-new", "L-01", daysAgo(2)) }},
		{"a review request's end", func(h *history) {
			h.add(daysAgo(3), event.ReviewTask, 0, event.StepReviewRequest, event.KindFact, ReviewData{By: "pj-lead"})
			h.add(daysAgo(2), event.ReviewTask, 0, event.StepReviewRm, event.KindFact, ReviewData{By: "pj-lead"})
		}},
		{"a brief's end", func(h *history) {
			h.add(daysAgo(2), event.MsgTask, 0, event.StepMessage, event.KindFact,
				event.Message{To: "pj-lead", From: "obor", Ends: "pj-rev", Line: "[obor] pj-rev finished"})
		}},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			root, key := idleOld(t, c.extra, false)
			r := pruneOracle(t, root, pruneBatch)
			n1, _ := idleOf(r.a1, r.before)
			if !n1.ok || n1.end <= key {
				t.Fatalf("precondition: idle %+v; want idle at a key newer than T-old's %d", n1, key)
			}
			if !r.tasks["T-old"] || !onlyTerminal(r.kept, "T-old", event.StepMerged) {
				t.Errorf("pruned %v (T-old keeps %d rows), want T-old pruned to its merged fact", r.tasks, rowsOf(r.kept, "T-old"))
			}
			if n2, _ := idleOf(r.a2, r.then); n2.end != n1.end || !n2.ok {
				t.Errorf("idle key after the prune and replay %+v, want %d", n2, n1.end)
			}
		})
	}
}

// After the first case's prune, a pass of the idle rule and an hour: the ping rule does not fire (no line went
// out on a moved key, so none is due again).
func TestPruneIdleNoPing(t *testing.T) {
	t.Parallel()
	root, key := idleOld(t, nil, true)
	r := pruneOracle(t, root, pruneBatch)
	if n, open := idleOf(r.a1, r.before); !n.ok || n.end != key || open {
		t.Fatalf("precondition: idle %+v (open %t) before the prune; want idle at key %d, acknowledged", n, open, key)
	}
	r.a2.rulePass(t.Context(), []rule{{name: ruleIdle, when: idleRule}}, &Loop{})
	r.a2.mu.Lock()
	dids := len(r.then.folded.did)
	r.a2.mu.Unlock()
	if dids != 0 {
		t.Errorf("the idle rule posted %d lines after the prune, want none", dids)
	}
	if _, act, ok := idlePingRule(t.Context(), r.a2, r.then, nil, pruneNow.Add(time.Hour)); ok {
		t.Errorf("the ping fired after the prune: %q", act.line)
	}
}

// A project needs-you held at the prune suppresses idle but does not end the acknowledged
// idle period: T-old, whose record is its key, stays whole, and once the needs-you is cleared (its resumed fact is no
// idle end) the key is the acknowledged one, the same after a reload.
func TestPruneIdleKeyHeldThroughNeedsYou(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		name, task, reason string
		clear              func(a *App, p *proj) bool
	}{
		{"a nightly RED cleared by GREEN", event.NightlyTask, "full-check RED abc123",
			func(a *App, p *proj) bool { return a.settleNeeds(p, event.NightlyTask, "") }},
		{"a loop error cleared by a passing pass", event.LoopTask, "loop: lanes: boom",
			func(a *App, p *proj) bool { a.loopStatus(t.Context(), p, &p.badLoop, nil); return true }},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			root, key := idleOld(t, nil, true)
			h := newHistory(t, root)
			h.add(daysAgo(1), c.task, 0, event.StepNeedsYou, event.KindFact, event.FactData{Reason: c.reason})
			h.done()
			recs := readLog(t, root)
			a, p := pruneApp(t, root)
			if n, _ := idleOf(a, p); n.ok || len(p.folded.active[c.task]) == 0 || p.folded.idleAck.Key != key {
				t.Fatalf("precondition: idle %+v, %s needs-you %v, ack %d; want idle suppressed by the needs-you, ack %d",
					n, c.task, p.folded.active[c.task], p.folded.idleAck.Key, key)
			}
			a.pruneLog(p, pruneNow)
			if kept := readLog(t, root); rowsOf(kept, "T-old") != rowsOf(recs, "T-old") {
				t.Errorf("T-old keeps %d of %d rows after the prune, want it whole", rowsOf(kept, "T-old"), rowsOf(recs, "T-old"))
			}
			if !c.clear(a, p) {
				t.Fatal("the needs-you was not cleared")
			}
			n1, open1 := idleOf(a, p)
			if !n1.ok || n1.end != key || open1 {
				t.Fatalf("precondition: after the clear idle %+v (open %t); want idle at the acknowledged key %d", n1, open1, key)
			}
			a2, q := pruneApp(t, root)
			if n2, open2 := idleOf(a2, q); !n2.ok || n2.end != key || open2 {
				t.Errorf("idle after the reload %+v (open %t), want the acknowledged key %d", n2, open2, key)
			}
		})
	}
}
