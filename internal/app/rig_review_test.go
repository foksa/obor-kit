package app

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"obor/internal/app/event"
	"obor/internal/app/port"
	"obor/internal/app/review"
	"obor/internal/app/wire"
	"obor/internal/store"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

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
	r.a.rulePass(context.Background(), rules, withAppCfg(r.a, &Loop{Workflow: WorkflowOf(r.git), Agents: r.agents}))
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
	return r.p.idleNow(cfgAt(r.p.root)).ok
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
		h.a.rulePass(context.Background(), []rule{r}, withAppCfg(h.a, &Loop{}))
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
		addRecord(t, h, task, event.StepSubmitted, event.KindFact, event.Submit{Lane: "L-01"})
		addRecord(t, h, task, event.StepMerged, event.KindFact, event.MergedData{Head: head, Outside: head == ""})
	}
}

// obor review start records a request; a merge-loop pass starts the review once its --after tasks merged and the
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

// agentsSpy is an Agents that records every Launch and starts nothing; launchErr fails each launch, trust (a pane)
// holds each at a trust dialog there (heldAtTrust: launch's result, by resume).
type agentsSpy struct {
	launches  []port.Launch
	launchErr error
	trust     string
	resume    bool // a launch without Fresh resumes a recorded session (heldAtTrust)
}

func (s *agentsSpy) Launch(_ context.Context, l port.Launch) (port.Started, error) {
	s.launches = append(s.launches, l)
	if s.launchErr != nil {
		return port.Started{}, s.launchErr
	}
	if s.trust != "" {
		return heldAtTrust(l, s.trust, "RESTARTED "+l.Agent, s.resume && !l.Fresh)
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
	r.fact(event.ReviewTask, event.StepReviewStarted, review.Data{ID: "RV-4", Head: "1111111", Tasks: []string{"T-9"}})
	r.fact(event.ReviewTask, event.StepReviewClosed, review.Data{ID: "RV-4", Reply: review.ClosedReply + "RV-4 closed · 1111111 · approved · T-9"})
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
	r.a.opt.Loop = func(root string) (*Loop, error) {
		return withCfg(root, &Loop{Workflow: WorkflowOf(r.git), Agents: r.agents}), nil
	}
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
	finishBriefs(r.a, r.p, withAppCfg(r.a, &Loop{Inbox: &inboxSpy{}})) // records the finished line
	if r.p.out.Brief("pj-rev") != nil {
		r.t.Fatal("precondition: the reviewer's brief is still open")
	}
}

// closedBy is a close as serve's close leaves it: fact review.closed of id, its reply naming subject.
func (r *reviewRig) closedBy(id, subject string) {
	r.t.Helper()
	r.fact(event.ReviewTask, event.StepReviewClosed, review.Data{ID: id, Reply: review.ClosedReply + subject})
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
	r.a.reviewPass(context.Background(), r.p, withAppCfg(r.a, &Loop{Workflow: WorkflowOf(r.git), Agents: r.agents}))
}

// tick is one "review due" count.
func (r *reviewRig) tick() {
	r.a.reviewDueTick(context.Background(), r.p, withAppCfg(r.a, &Loop{Workflow: WorkflowOf(r.git), Agents: r.agents}))
}

// call is an obor review start request from the lead.
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
			event.Dispatch{Dispatch: port.Dispatch{Lane: "L-01"}})
	}
	r.a.record(r.p, event.Record{Task: task, Attempt: 1, Step: event.StepSubmitted, Kind: event.KindFact}, event.Submit{Lane: "L-01"})
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
func (r *reviewRig) closedFacts() []review.Data {
	r.t.Helper()
	var out []review.Data
	for _, rec := range readLog(r.t, r.p.root) {
		var d review.Data
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
		review.Data{ID: "RV-5", Head: startHead, Tasks: []string{"T-start"}}) {
		t.Fatal("the log refused the review start")
	}
}

// setSpec stores a new rev of task's spec with line between its title and its first heading.
func (r *reviewRig) setSpec(task, line string) {
	r.t.Helper()
	text := "# " + task + " — fix\n\nDifficulty: medium\n" + line + "\n\n## Do\nfix it\n"
	if _, _, err := store.SetSpec(r.p.log.DB(), task, text, "", "pj-lead", r.now); err != nil {
		r.t.Fatal(err)
	}
}

// fixTask stores task's spec with line between its title and its first heading, then dispatches and submits it.
func (r *reviewRig) fixTask(task, line string) {
	r.t.Helper()
	r.setSpec(task, line)
	if !r.a.record(r.p, event.Record{Task: task, Attempt: 1, Step: event.StepDispatched, Kind: event.KindFact},
		event.Dispatch{}) {
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
	r.a.rulePass(context.Background(), []rule{{name: ruleVerify, when: verifyRule}}, withAppCfg(r.a, &Loop{Workflow: WorkflowOf(r.git), Agents: r.agents}))
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
	r.a.deliver(context.Background(), r.p, withAppCfg(r.a, &Loop{Prompter: &linerSpy{}}))
	r.working(true)
	r.working(false)
	finishBriefs(r.a, r.p, withAppCfg(r.a, &Loop{Prompter: &linerSpy{}}))
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
	r.a.deliver(context.Background(), r.p, withAppCfg(r.a, &Loop{Prompter: &linerSpy{}}))
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
	h.a.opt.Loop = func(root string) (*Loop, error) { return withCfg(root, &Loop{}), nil }
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
