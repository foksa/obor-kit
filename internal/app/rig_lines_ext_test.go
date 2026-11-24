package app_test

import (
	"cmp"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"obor/internal/app"
	"obor/internal/app/event"
	"obor/internal/app/port"
	"obor/internal/app/wire"
	"obor/internal/config"
	"obor/internal/store"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type sent struct{ to, text string }

// prompts is every prompt the terminal got, to the agent the rig runs in its target (serve addresses a job prompt to
// the agent's pane by its registry: w1:p1 is pj-l-01's, w1:p9 pj-int's).
func (r *loopRig) prompts() []sent {
	var out []sent
	for _, c := range r.prompted() {
		out = append(out, sent{rigAgent(c.Pane), c.Text})
	}
	return out
}

// rigAgent is the agent the rig runs in pane target (target itself when it is no pane of the rig: a name).
func rigAgent(target string) string {
	return cmp.Or(map[string]string{"w1:p1": "pj-l-01", "w1:p9": "pj-int"}[target], target)
}

// cleared is, for each prompt to agent in order, whether it cleared the input line before it typed (a retry does).
func (r *loopRig) cleared(agent string) []bool {
	var out []bool
	for _, c := range r.prompted() {
		if rigAgent(c.Pane) == agent {
			out = append(out, c.Submit)
		}
	}
	return out
}

// waitJob is the text of the prompt to agent for job (waited for).
func (r *loopRig) waitJob(agent, job string) string {
	r.t.Helper()
	r.waitWake(agent, job)
	var text string
	r.waitFor("job "+job+" fetched by "+agent, func() bool {
		var ok bool
		text, ok = r.fetch(agent, job)
		return ok
	})
	return text
}

// waitWake waits until job's wake reached agent: typed into its pane, or carried by its start, and the job's prompt
// has its result (job_next could hand the job out from its intent on, before the start that carries it returns). The
// result is the job's own task's: on a lane that runs task after task, an earlier task's job has the same attempt and
// step.
func (r *loopRig) waitWake(agent, job string) {
	r.t.Helper()
	w := "[obor job " + job + "] call the obor job_next tool now (connected; it returns your job)"
	parts := strings.SplitN(job, "/", 3)
	delivered := func() bool {
		return slices.ContainsFunc(r.records(), func(rec event.Record) bool {
			return rec.Task == parts[0] && strconv.Itoa(rec.Attempt) == parts[1] && rec.Kind == event.KindResult &&
				rec.String() == parts[2]
		})
	}
	r.waitFor("the wake of "+job+" to "+agent, func() bool {
		sent := false
		for _, p := range r.prompts() {
			sent = sent || p.to == agent && p.text == w
		}
		r.mu.Lock()
		for _, l := range r.launches {
			sent = sent || l.Agent == agent && l.Prompt == w
		}
		r.mu.Unlock()
		return sent && delivered()
	})
}

var patchRe = regexp.MustCompile(`Patch hash: ([0-9a-f]{64})`)

// patch is the patch hash a review job names.
func (r *loopRig) patch(text string) string {
	r.t.Helper()
	m := patchRe.FindStringSubmatch(text)
	if m == nil {
		r.t.Fatalf("no patch hash in the review job:\n%s", text)
	}
	return m[1]
}

// verdict is pj-int's verdict on T-1 (no job id: the current job, as the CLI sends it).
func (r *loopRig) verdict(method, callID, patch, note string) (string, error) {
	return r.ask(&wire.VerdictReq{Verb: method,
		Envelope: wire.Envelope{Root: r.p.Root,
			Agent: "pj-int", Role: "integrator", CallID: callID}, Task: "T-1", Patch: patch, Note: note})
}

func (r *loopRig) accept(callID, patch string) {
	r.t.Helper()
	if reply, err := r.verdict(wire.MethodAccept, callID, patch, ""); err != nil || !strings.HasPrefix(reply, "ACCEPT T-1") {
		r.t.Fatalf("accept: %q, %v", reply, err)
	}
}

// mainMerges is how many merges of T-1 the target holds (a publish, or one by hand).
func (r *loopRig) mainMerges() int { return r.scratch.Merges("T-1") }

// conflicting: T-1's lane edits shared.txt and main moves on, and the gate of the lane's merge conflicts in shared.txt
// (scripted: the conflict git finds is internal/checks', TestUnmergedConflictHasNoMismatch).
func (r *loopRig) conflicting() {
	r.work("shared.txt", "lane\n"+sharedLines)
	r.scratch.Move()
	r.scratch.Script("Gate lane", "T-1", port.OutcomeConflict+" shared.txt")
}

// movedConflict: main moves on, and the gate of the lane's merge onto it conflicts (scripted).
func (r *loopRig) movedConflict() {
	r.scratch.Move()
	r.scratch.Script("Gate moved", "T-1", port.OutcomeConflict+" shared.txt")
}

// resolveInScratch is the integrator's resolution of the open conflict, committed in the scratch: its sha.
func (r *loopRig) resolveInScratch() string {
	r.t.Helper()
	sha := r.scratch.Resolve()
	if sha == "" {
		r.t.Fatalf("no conflict open in the scratch: %q", r.steps())
	}
	return sha
}

// published is T-1's candidate the last publish merged.
func (r *loopRig) published() port.Candidate {
	r.t.Helper()
	c, ok := r.scratch.Published("T-1")
	if !ok {
		r.t.Fatalf("T-1 not published: %q", r.steps())
	}
	return c
}

// merged waits for the merge and its after-merge work (its last step, the worker's stop).
func (r *loopRig) merged(attempt int) {
	r.t.Helper()
	r.waitStep(string(rune('0'+attempt)) + " result agent.stop")
}

// noLead: the lead's chat got nothing: every prompt went to the integrator or the worker.
func (r *loopRig) noLead() {
	r.t.Helper()
	for _, p := range r.prompts() {
		if p.to != "pj-int" && p.to != "pj-l-01" {
			r.t.Errorf("a prompt to %s: %q", p.to, p.text)
		}
	}
}

// redLine is the third attempt's red line (its lane test's), as the needs-you names it.
func (r *loopRig) redLine() string {
	var line string
	for _, rec := range r.records() {
		if rec.Kind == event.KindResult && strings.HasPrefix(rec.Step, "test") && rec.Attempt == 3 {
			var g struct{ Line string }
			_ = json.Unmarshal(rec.Data, &g)
			line = g.Line
		}
	}
	return line
}

// setSpec stores text as T-1's spec through serve's socket, as the lead (obor spec set).
func (r *loopRig) setSpec(callID, text string) string {
	r.t.Helper()
	reply, err := r.ask(&wire.SpecSetReq{Envelope: wire.Envelope{Root: r.p.Root,
		Agent: "lead", Role: "lead", CallID: callID}, Task: "T-1", Spec: text})
	if err != nil {
		r.t.Fatalf("spec set: %v", err)
	}
	return reply
}

// specSetLine is a spec-set reply's first line, `<SET|UNCHANGED> <task> rev <n>`; the line after it names the models a
// dispatch would try (specSetModels).
func specSetLine(reply string) string {
	line, _, _ := strings.Cut(reply, "\n")
	return line
}

// watch is a watch request to the rig's serve from the lead (role lead, agent pj-lead).
func (r *loopRig) watch(callID, task string, rm bool) (string, error) {
	return r.ask(&wire.WatchReq{Envelope: wire.Envelope{Root: r.p.Root,
		Role: "lead", Agent: "pj-lead", CallID: callID}, Task: task, Clear: rm})
}

// watchNote is a watch request carrying the lead's note.
func (r *loopRig) watchNote(callID, task, note string) (string, error) {
	return r.ask(&wire.WatchReq{Envelope: wire.Envelope{Root: r.p.Root,
		Role: "lead", Agent: "pj-lead", CallID: callID}, Task: task, Note: note})
}

// watchFacts is the watch and unwatch facts of the project's log; watchLines the lead lines a watch recorded.
func (r *loopRig) watchFacts() []event.Record {
	var out []event.Record
	for _, rec := range r.records() {
		if rec.Task == event.WatchTask && rec.Kind == event.KindFact {
			out = append(out, rec)
		}
	}
	return out
}

func (r *loopRig) watchLines() []string {
	var out []string
	for _, rec := range r.records() {
		var m event.Message
		if rec.Task == event.MsgTask && rec.Step == event.StepMessage && json.Unmarshal(rec.Data, &m) == nil && m.Of.Kind == "watch" {
			out = append(out, m.Line)
		}
	}
	return out
}

// mergedT1 runs T-1 from its task_done to its merge (the integrator accepts the review job's patch).
func (r *loopRig) mergedT1() {
	r.t.Helper()
	r.submit()
	r.accept("c1", r.patch(r.waitJob("pj-int", "T-1/1/review")))
	r.merged(1)
}

// seedFailed writes, to the stopped app's log, T-1's dispatch on L-01 and each of tasks as an open attempt (submitted
// on its own lane, its submit refused: a needs-you, merged once land puts it on main).
func (r *loopRig) seedFailed(tasks ...string) {
	r.t.Helper()
	r.p.Dispatched("L-01")
	var recs []event.Record
	refused, _ := json.Marshal(map[string]any{"refused": "no commit"})
	for i, task := range tasks {
		sub, _ := json.Marshal(map[string]any{"lane": fmt.Sprintf("O-%02d", i+2), "agent": fmt.Sprintf("pj-o%d", i+2)})
		recs = append(recs, event.Record{Task: task, Attempt: 1, Step: event.StepSubmitted, Kind: event.KindFact, Data: sub},
			event.Record{Task: task, Attempt: 1, Step: event.StepSubmit, Kind: event.KindIntent, Class: event.ClassGit},
			event.Record{Task: task, Attempt: 1, Step: event.StepSubmit, Kind: event.KindResult, Data: refused})
	}
	if err := app.WriteLog(r.p.Root, time.Now(), recs...); err != nil {
		r.t.Fatal(err)
	}
}

// land is an obor merge of task on main outside the app: the loop records merged{outside}.
func (r *loopRig) land(task string) {
	r.t.Helper()
	r.scratch.Land(task) // obor merge by hand
	r.waitFor(task+" merged", func() bool { return r.taskStep(task, "fact merged") > 0 })
}

// dropSettled is the lead's drop of task once the failing step seedFailed left running has settled: serve refuses a
// drop while a step runs ("retry when it settles"), and under load the step outlasts the test's first call.
func (r *loopRig) dropSettled(task, note string) {
	r.t.Helper()
	var reply string
	var err error
	r.waitFor("the drop of "+task+" accepted", func() bool {
		reply, err = r.ask(&wire.DropReq{Envelope: wire.Envelope{Root: r.p.Root,
			Role: "lead", CallID: "x1"}, Task: task, Why: note})
		return err == nil || !strings.Contains(err.Error(), "retry when it settles")
	})
	if err != nil || reply != "DROPPED "+task {
		r.t.Fatalf("drop: %q, %v", reply, err)
	}
}

// rigInbox is the rig's Poster: the lead's lines posted, the agents with no inbox record, and how a post
// fails (refused: before a byte, as a dead socket's dial; partial: after one).
type rigInbox struct {
	mu       sync.Mutex
	posts    []string        // "<agent>: <line>" per post that went out (began)
	attempts []string        // every post tried
	none     map[string]bool // agents with no record
	refused  bool
	partial  bool
	once     bool // refused and partial end after the next post (failOnce)
	stale    bool // a refused post marks the agent's record not live, as the poster marks a refused agy record (staleOnRefuse)
	cutting  bool // the next post waits for serve's ctx and fails with its error (cut)
	cancels  int  // the next cancels posts fail at once as a cancellation, serve running
	// gate holds the next post of a line starting with gate.prefix in flight (holdPost): closes started, waits for
	// release, then refuses it before a byte (refuse) or posts it.
	gate *postGate
}

// postGate is one post held in flight (rigInbox.holdPost).
type postGate struct {
	prefix            string
	refuse            bool
	started, released chan struct{}
}

// holdPost holds the next post of a line starting with prefix in flight: the returned started closes once it is held,
// release lets it go (refused before a byte when refuse, else posted); a second call does nothing (a test defers it).
func (in *rigInbox) holdPost(prefix string, refuse bool) (started <-chan struct{}, release func()) {
	g := &postGate{prefix: prefix, refuse: refuse, started: make(chan struct{}), released: make(chan struct{})}
	in.mu.Lock()
	in.gate = g
	in.mu.Unlock()
	var once sync.Once
	return g.started, func() { once.Do(func() { close(g.released) }) }
}

func (in *rigInbox) Post(ctx context.Context, _, agent, line string) (bool, error) {
	in.mu.Lock()
	in.attempts = append(in.attempts, agent+": "+line)
	if g := in.gate; g != nil && strings.HasPrefix(line, g.prefix) {
		in.gate = nil
		in.mu.Unlock()
		close(g.started)
		<-g.released
		if g.refuse {
			return false, errors.New("dial unix: connection refused")
		}
		in.mu.Lock()
	}
	cut := in.cutting
	if cut {
		in.cutting = false
	}
	canceled := in.cancels > 0
	if canceled {
		in.cancels--
	}
	in.mu.Unlock()
	if canceled {
		return false, context.Canceled
	}
	if cut { // a post serve's own stop cut before a byte went out, as its inbox's ctx ends
		<-ctx.Done()
		return false, ctx.Err()
	}
	// A post on an ended ctx fails before a byte goes out, as the real poster's dial does: a retry the stopping serve
	// makes after a cut must not count as posted (a red TestShutdownDeliveryNotRecorded under load: posted twice).
	if err := ctx.Err(); err != nil {
		return false, err
	}
	in.mu.Lock()
	defer in.mu.Unlock()
	switch {
	case in.none[agent]:
		return false, errors.New("no session recorded for " + agent)
	case in.refused:
		if in.once {
			in.refused = false
		}
		if in.stale {
			if in.none == nil {
				in.none = map[string]bool{}
			}
			in.none[agent] = true
		}
		return false, errors.New("dial unix: connection refused")
	}
	for _, row := range strings.Split(line, "\n") { // a batched post counts as one entry per row
		in.posts = append(in.posts, agent+": "+row)
	}
	if in.partial {
		if in.once {
			in.partial = false
		}
		return true, errors.New("broken pipe")
	}
	return true, nil
}

// recorded is Options.Inboxed: agent has an inbox record (naming no session).
func (in *rigInbox) recorded(agent string) (string, bool) {
	in.mu.Lock()
	defer in.mu.Unlock()
	return "", !in.none[agent]
}

func (in *rigInbox) unrecord(agent string) {
	in.mu.Lock()
	defer in.mu.Unlock()
	if in.none == nil {
		in.none = map[string]bool{}
	}
	in.none[agent] = true
}

func (in *rigInbox) record(agent string) {
	in.mu.Lock()
	defer in.mu.Unlock()
	delete(in.none, agent)
}

// fail makes the next posts fail: partial false before a byte went out, true after one; ok ends both.
func (in *rigInbox) fail(partial bool) {
	in.mu.Lock()
	defer in.mu.Unlock()
	in.refused, in.partial, in.once, in.stale = !partial, partial, false, false
}

// failOnce makes the next post alone fail (partial false before a byte went out, true after one): the posts after it
// go out. A test that means to see one refusal before the failure rule counts three needs no race with the merge
// loop's passes over the post.
func (in *rigInbox) failOnce(partial bool) {
	in.mu.Lock()
	defer in.mu.Unlock()
	in.refused, in.partial, in.once, in.stale = !partial, partial, true, false
}

func (in *rigInbox) ok() {
	in.mu.Lock()
	defer in.mu.Unlock()
	in.refused, in.partial, in.once, in.stale = false, false, false, false
}

// cut makes the next post alone wait for serve's ctx and fail with its error before a byte went out: a delivery serve's
// own stop cut.
func (in *rigInbox) cut(on bool) {
	in.mu.Lock()
	defer in.mu.Unlock()
	in.cutting = on
}

// count is how many posts of line to agent went out; tries how many were tried.
func (in *rigInbox) count(agent, line string) int { return in.n(&in.posts, agent+": "+line) }

func (in *rigInbox) tries(agent, line string) int { return in.n(&in.attempts, agent+": "+line) }

func (in *rigInbox) n(list *[]string, s string) int {
	in.mu.Lock()
	defer in.mu.Unlock()
	n := 0
	for _, x := range *list {
		if x == s {
			n++
		}
	}
	return n
}

// lineResults is the results of the project's line deliveries, in order (their data: sent, sent: <error>).
func (r *loopRig) lineResults() []string {
	var out []string
	for _, rec := range r.records() {
		if rec.Task == event.MsgTask && rec.Kind == event.KindResult && strings.HasPrefix(rec.Step, "deliver.") {
			var v string
			_ = json.Unmarshal(rec.Data, &v)
			out = append(out, v)
		}
	}
	return out
}

// lineRig is the loop rig with the lead pj-lead (w1:p5) and the reviewer pj-rev (w1:p6) in the project's workspace,
// each in status lead / rev.
func lineRig(t *testing.T, lead, rev string) *loopRig {
	r := newLoopRig(t, noCheck())
	for _, a := range []port.Pane{agentPane("w1:p5", "pj-lead", r.p.Root), agentPane("w1:p6", "pj-rev", r.p.Root)} {
		a.Status = map[string]string{"w1:p5": lead, "w1:p6": rev}[a.ID]
		r.addPane(a) // labelled by its launch
	}
	return r
}

// agentIs sets the status of the agent in pane and waits until the app sees it.
func (r *loopRig) agentIs(pane, name, status string) {
	r.t.Helper()
	r.setPane(pane, func(p *port.Pane) { p.Status = status })
	r.waitFor(name+" "+status, func() bool {
		for _, a := range r.app.Status().Project.Agents {
			if a.Name == name && a.Status == status {
				return true
			}
		}
		r.push()
		return false
	})
}

// line is a send or brief request to the rig's serve from the lead (role lead, agent pj-lead).
func (r *loopRig) line(callID string, c wire.Req) (string, error) {
	e := c.Env()
	e.Root, e.CallID = r.p.Root, callID
	if e.Role == "" {
		e.Role, e.Agent = "lead", "pj-lead"
	}
	return r.ask(c)
}

// typedTo is the prompts the terminal got for agent (its name, or its pane: typed lines address the pane id, the
// line rig's role panes w1:p5 and w1:p6 count as pj-lead's and pj-rev's) with exactly text.
func (r *loopRig) typedTo(agent, text string) int {
	n := 0
	for _, p := range r.prompts() {
		if (p.to == agent || map[string]string{"w1:p5": "pj-lead", "w1:p6": "pj-rev"}[p.to] == agent) && p.text == text {
			n++
		}
	}
	return n
}

// msgFacts is the facts in force of the project's lines.
func (r *loopRig) msgFacts() []event.Record {
	var out []event.Record
	for _, f := range r.app.Status().Project.Facts {
		if f.Task == event.MsgTask {
			out = append(out, f)
		}
	}
	return out
}

// supersededLine is one undelivered brief line a newer brief ended: its seq and the newer brief's.
type supersededLine struct {
	Seq int64
	By  int64
}

// supersededLines is the brief lines the project's briefed facts in the log supersede, in order.
func (r *loopRig) supersededLines() []supersededLine {
	var out []supersededLine
	for _, rec := range r.records() {
		if rec.Task == event.MsgTask && rec.Kind == event.KindFact && rec.Step == event.StepBriefed {
			var m event.Message
			_ = json.Unmarshal(rec.Data, &m)
			for _, seq := range m.Supersedes {
				out = append(out, supersededLine{Seq: seq, By: rec.Seq})
			}
		}
	}
	return out
}

// waitBriefDelivered waits until the one brief line is delivered: its result in the log and folded (nothing waiting).
// The brief's age counts from that result's time, so the clock moves only after it; the fake inbox's or pane's count
// of the line is earlier than the result record.
func (r *loopRig) waitBriefDelivered() {
	r.t.Helper()
	r.waitFor("the brief's delivery recorded", func() bool { return len(r.lineResults()) >= 1 && len(r.waiting()) == 0 })
}

// waiting is the lines serve has not delivered yet (obor status).
func (r *loopRig) waiting() []wire.MessageStatus { return r.app.Status().Project.Messages }

// sentLine is the line obor send posts in the target project for text from the project from: the sender, the text, and
// the reply in one command.
func sentLine(from, text string) string {
	return "[obor " + from + "] " + text + ` (reply: obor send ` + from + ` "…")`
}

// obor dispatch T-xx --model <ref> on a task its worker works on moves it: serve keeps the lane, branch and
// spec and records the redo dispatch of the next attempt with the new model; the merge loop stops the worker by its
// identity (worker.stop, stopstep_test.go), then the redo's agent.start launches that model with the new attempt's work job. The rig: newLoopRig(t, working()) (T-1 working on
// L-01 as sonnet, its worker pj-l-01 in w1:p1).

// moveTo is the lead's obor dispatch T-1 --model types.
func (r *loopRig) moveTo(callID string, types ...string) (string, error) {
	return r.dispatchCall(wire.DispatchReq{Envelope: wire.Envelope{Root: r.p.Root, Agent: "pj-lead", Role: "lead",
		CallID: callID}, Task: "T-1", Types: types})
}

// workerLaunches is the Agents.Launch calls of pj-l-01, in order.
func (r *loopRig) workerLaunches() []port.Launch {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []port.Launch
	for _, l := range r.launches {
		if l.Agent == "pj-l-01" {
			out = append(out, l)
		}
	}
	return out
}

// dispatchFacts is how many dispatch facts the log has (a move's, any attempt).
func (r *loopRig) dispatchFacts() int {
	n := 0
	for _, rec := range r.records() {
		if rec.Kind == event.KindFact && rec.Step == event.StepDispatched {
			n++
		}
	}
	return n
}

// advance moves the fake clock on by d (0: not at all), then waits for two more passes of serve's loop (wake).
func (r *loopRig) advance(d time.Duration) {
	r.t.Helper()
	n := app.StreamPasses(r.app) // passes in a resubscribe's gap skip the project
	r.clock.Add(d)
	r.waitFor("two more passes", func() bool { return app.StreamPasses(r.app) >= n+2 })
}

// needsLineFacts is the message facts of the project's log that announce a needs-you.
func (r *loopRig) needsLineFacts() []event.Record {
	var out []event.Record
	for _, rec := range r.records() {
		if rec.Task == event.MsgTask && rec.Kind == event.KindFact && rec.Step == event.StepMessage {
			var m event.Message
			if json.Unmarshal(rec.Data, &m) == nil && m.Of.Kind == "needs" {
				out = append(out, rec)
			}
		}
	}
	return out
}

// againLines is the needs-you lines that repeat fact seq: the lines ending in " (again, <age>)".
func (r *loopRig) againLines(seq int64) []string {
	var out []string
	for _, rec := range r.needsLineFacts() {
		var m event.Message
		if json.Unmarshal(rec.Data, &m) == nil && app.NeedsSeq(m.Of) == seq && strings.Contains(m.Line, " (again, ") {
			out = append(out, m.Line)
		}
	}
	return out
}

// refusedRig is the dispatch rig with T-1's lane preparation refused: T-1 has the needs-you "lane: lane_setup in L-01:
// rc 1: boom". It returns that fact's line to the lead, as serve's answers name the commands that answer it.
func refusedRig(t *testing.T) (*loopRig, string) {
	t.Helper()
	r := newLoopRig(t, dispatching())
	r.disp.set(func(d *dispatchFakes) {
		d.fail["prepare"] = func() error { return port.Refused{Line: "lane_setup in L-01: rc 1: boom"} }
	})
	r.start()
	r.mustDispatch("d1")
	r.waitStep("1 fact needs-you{lane: lane_setup in L-01: rc 1: boom}")
	r.waitFor("needs-you fact folded into status", func() bool {
		for _, f := range r.app.Status().Project.Facts {
			if f.Step == event.StepNeedsYou {
				return true
			}
		}
		return false
	})
	var fact event.Record
	for _, f := range r.app.Status().Project.Facts {
		if f.Step == event.StepNeedsYou {
			fact = f
		}
	}
	if fact.Seq == 0 {
		t.Fatalf("precondition: no needs-you in force: %+v", r.app.Status().Project.Facts)
	}
	ans := r.answerOf(fact)
	if ans == "" {
		t.Fatal("precondition: the needs-you has no answer commands")
	}
	return r, "[obor] needs-you T-1 (L-01): lane: lane_setup in L-01: rc 1: boom \u2192 " + ans
}

var (
	green = app.NightlyOut{SHA: "abc1234def", Verdict: "GREEN", Line: "NIGHTLY GREEN abc1234"}
	red   = app.NightlyOut{SHA: "abc1234def", Verdict: "RED", Line: "NIGHTLY RED abc1234"}
)

// startWithAgents starts the app as start does, with agents as its loop's Agents: a real launcher, so the start's
// args reach the terminal.
func (r *loopRig) startWithAgents(agents port.Launcher) {
	r.t.Helper()
	at := r.adapter()
	r.out = &syncBuf{}
	r.app = app.New(app.Options{Root: r.p.Root, Tick: 20 * time.Millisecond, Retry: 5 * time.Millisecond,
		StepRetry: 5 * time.Millisecond, StepRetryMax: 40 * time.Millisecond,
		Now: r.clock.Now, Out: r.out, Info: at.Info, Screen: at.Screen, Procs: quietProcs, Refresh: port.Refreshed,
		Loop: func(root string) (*app.Loop, error) {
			cfg, err := config.Load(root)
			if err != nil {
				return nil, err
			}
			fx := &app.Loop{Cfg: cfg, Workflow: app.WorkflowOf(r.scratch), Agents: agents, Prompter: r.tt}
			r.disp.loop(fx)
			return fx, nil
		}},
		at.Panes, r.lanes)
	ctx, cancel := context.WithCancel(context.Background())
	r.cancel, r.done = cancel, make(chan struct{})
	go func() {
		defer close(r.done)
		_ = r.app.Serve(ctx)
	}()
	r.t.Cleanup(r.stop)
	r.waitFor("the first check", func() bool { checks, _ := app.Counts(r.app); return checks >= 1 })
	r.serve()
}

// pause is a pause request to the rig's serve (role "": the owner).
func (r *loopRig) pause(role, callID string, c wire.PauseReq) (string, error) {
	c.Root, c.Role, c.CallID = r.p.Root, role, callID
	if role != "" {
		c.Agent = "pj-" + role
	}
	return r.ask(&c)
}

// The prompts table: the text of every job, brief and start prompt serve sent is recorded right after
// its fact, by the fact's seq; every distinct text a job's fetch returned gets its own row (the job's newest), and a
// failed write never fails the job.

// promptRows is the project's prompts for q, newest first.
func (r *loopRig) promptRows(t *testing.T, q store.PromptQuery) []store.PromptRow {
	t.Helper()
	var rows []store.PromptRow
	if err := r.withStore(func(db *sql.DB) (err error) {
		rows, err = store.ReadPrompts(db, q, 100)
		return err
	}); err != nil && !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("ReadPrompts %+v: %v", q, err)
	}
	return rows
}

// deliveredSeq is the seq of the log's delivered fact of job.
func (r *loopRig) deliveredSeq(t *testing.T, job string) int64 {
	t.Helper()
	for _, rec := range r.records() {
		if rec.Kind == event.KindFact && rec.Step == event.StepDelivered && rec.Fact().Job == job {
			return rec.Seq
		}
	}
	t.Fatalf("no delivered fact of %s", job)
	return 0
}

// briefedSeq is the seq of the nth briefed fact of pj-rev.
func (r *loopRig) briefedSeq(t *testing.T, n int) int64 {
	t.Helper()
	seen := 0
	for _, rec := range r.records() {
		if rec.Task == event.MsgTask && rec.Kind == event.KindFact && rec.Step == event.StepBriefed {
			if seen++; seen == n {
				return rec.Seq
			}
		}
	}
	t.Fatalf("no briefed fact %d", n)
	return 0
}

// specTexts are the T-1 specs a test sets: a new rev says something else.
const (
	specOne = "# T-1 — Test\n\nDifficulty: medium\n\nDo the thing.\n"
	specTwo = "# T-1 — Test\n\nDifficulty: medium\n\nDo the thing, again.\n"
)

// spec sets spec as T-1's spec, by the lead with call id.
func (r *loopRig) spec(callID, text string) string {
	r.t.Helper()
	reply, err := r.ask(&wire.SpecSetReq{Envelope: wire.Envelope{Root: r.p.Root,
		Agent: "pj-lead", Role: "lead", CallID: callID}, Task: "T-1", Spec: text})
	if err != nil {
		r.t.Fatalf("spec set %s: %v", callID, err)
	}
	return reply
}

// Recovery: obor serve dies at any point of a merge-loop step, before its effect or after it with the
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
	raw, err := r.ask(&wire.JobNextReq{Envelope: wire.Envelope{Root: r.p.Root,
		Agent: agent, Role: role, CallID: fmt.Sprintf("rig-%d", r.callIDs.Add(1))}, Lane: lane})
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
	r.addPane(agentPane("w1:p2", "pj-l-02", lane)) // labelled by its launch
	return lane
}

const sharedLines = "2\n3\n4\n5\n6\n7\n8\n9\n10\n"

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

// A failure or needs-you an error would record is not recorded when the error is serve's own cancellation (its
// ctx ended: a restart, a stop). serve logged it as "shutting down: <what> not recorded" instead, and the intent stays
// pending, so the next serve's replay runs it as after a crash.

// msgFailed is how many delivery failures of the project's lines are in the log.
func (r *loopRig) msgFailed() int {
	n := 0
	for _, rec := range r.records() {
		if rec.Task == event.MsgTask && rec.Kind == event.KindFact && rec.Step == event.StepFailed {
			n++
		}
	}
	return n
}

// setSpecTask is the lead's obor spec set of task with note, text as its spec.
func (r *loopRig) setSpecTask(callID, task, text, note string) (string, error) {
	return r.ask(&wire.SpecSetReq{Envelope: wire.Envelope{Root: r.p.Root,
		Agent: "lead", Role: "lead", CallID: callID}, Task: task, Spec: text, Changed: note})
}

// setSpecT is setSpecTask of T-1, fatal on an error.
func (r *loopRig) setSpecT(callID, text, note string) string {
	r.t.Helper()
	reply, err := r.setSpecTask(callID, "T-1", text, note)
	if err != nil {
		r.t.Fatalf("spec set: %v", err)
	}
	return reply
}

// started is T-1's TaskStatus.Started (0: none, or no such task).
func (r *loopRig) started() int64 {
	for _, t := range r.app.Status().Project.Tasks {
		if t.Task == "T-1" {
			return t.Started
		}
	}
	return 0
}

// same fails t when got is not want.
func same[T comparable](t *testing.T, got, want T, what string) {
	t.Helper()
	if got != want {
		t.Errorf("%s = %v, want %v", what, got, want)
	}
}

// stoppedCall is the StopFailure hook's request for agent (role, lane) with call id: a rate_limit, 429.
func (r *loopRig) stoppedCall(agent, role, lane, callID string) (string, error) {
	return r.stoppedCallWith(agent, role, lane, callID, "rate_limit", "429 Too Many Requests")
}

func (r *loopRig) stoppedCallWith(agent, role, lane, callID, errText, details string) (string, error) {
	return r.line(callID, &wire.StoppedReq{Envelope: wire.Envelope{Agent: agent, Role: role}, Lane: lane,
		Error: errText, Details: details})
}

// turnCall is a worker's UserPromptSubmit or Stop hook telling serve its next turn began or ended.
func (r *loopRig) turnCall() (string, error) {
	return r.line(wire.NewCallID(), &wire.TurnReq{Envelope: wire.Envelope{Agent: "pj-l-01", Role: "worker"},
		Lane: "L-01"})
}

// stopReason is the worker's stop the rig's stoppedCall records.
const stopReason = "pj-l-01 stopped: rate_limit (429 Too Many Requests)"

// stopsInForce is how many needs-you{stopReason} of T-1 are in force.
func (r *loopRig) stopsInForce() int {
	n := 0
	for _, f := range r.app.Status().Project.Facts {
		if f.Task == "T-1" && f.Step == event.StepNeedsYou && f.Fact().Reason == stopReason {
			n++
		}
	}
	return n
}

// posted is the posts that went out ("<agent>: <line>"), in order.
func (in *rigInbox) posted() []string {
	in.mu.Lock()
	defer in.mu.Unlock()
	return append([]string(nil), in.posts...)
}

// staleOnRefuse makes the next post alone refused before a byte went out, the agent's record then not live: the
// poster's mark of a record the kind sends with (an agy record whose token went stale, project.RefuseInbox).
func (in *rigInbox) staleOnRefuse() {
	in.mu.Lock()
	defer in.mu.Unlock()
	in.refused, in.partial, in.once, in.stale = true, false, true, true
}
