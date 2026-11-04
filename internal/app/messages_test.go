package app_test

import (
	"context"
	"encoding/json"
	"errors"
	"obor/internal/app/event"
	"obor/internal/app/wire"
	"strings"
	"sync"
	"testing"
	"time"

	"obor/internal/herdr"
	"obor/internal/herdr/herdrtest"
)

// rigInbox is the rig's Poster (T-485): the lead's lines posted, the agents with no inbox record, and how a post
// fails (refused: before a byte, as a dead socket's dial; partial: after one).
type rigInbox struct {
	mu       sync.Mutex
	posts    []string        // "<agent>: <line>" per post that went out (began)
	attempts []string        // every post tried
	none     map[string]bool // agents with no record
	refused  bool
	partial  bool
	once     bool // refused and partial end after the next post (failOnce)
	cutting  bool // the next post waits for serve's ctx and fails with its error (cut)
}

func (in *rigInbox) Post(ctx context.Context, _, agent, line string) (bool, error) {
	in.mu.Lock()
	in.attempts = append(in.attempts, agent+": "+line)
	cut := in.cutting
	if cut {
		in.cutting = false
	}
	in.mu.Unlock()
	if cut { // a post serve's own stop cut before a byte went out (T-660), as its inbox's ctx ends
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
		return false, errors.New("dial unix: connection refused")
	}
	for _, row := range strings.Split(line, "\n") { // a batched post (T-651) counts as one entry per row
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
	in.refused, in.partial, in.once = !partial, partial, false
}

// failOnce makes the next post alone fail (partial false before a byte went out, true after one): the posts after it
// go out. A test that means to see one refusal before the failure rule counts three needs no race with the merge
// loop's passes over the post (T-565).
func (in *rigInbox) failOnce(partial bool) {
	in.mu.Lock()
	defer in.mu.Unlock()
	in.refused, in.partial, in.once = !partial, partial, true
}

func (in *rigInbox) ok() {
	in.mu.Lock()
	defer in.mu.Unlock()
	in.refused, in.partial, in.once = false, false, false
}

// cut makes the next post alone wait for serve's ctx and fail with its error before a byte went out: a delivery serve's
// own stop cut (T-660).
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
	r := newLoopRig(t, "")
	for _, a := range []*herdrtest.Pane{r.f.Agent("w1:p5", "pj-lead", lead, r.p.Root), r.f.Agent("w1:p6", "pj-rev", rev, r.p.Root)} {
		a.Workspace, a.Label = "w1", a.Name // labelled by its launch
	}
	return r
}

// agentIs sets the status of the agent in pane and waits until the app sees it.
func (r *loopRig) agentIs(pane, name, status string) {
	r.t.Helper()
	r.f.Set(pane, func(p *herdrtest.Pane) { p.Status = status })
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
func (r *loopRig) line(method, callID string, c wire.Call) (string, error) {
	c.Root, c.CallID = r.p.Root, callID
	if c.Role == "" {
		c.Role, c.Agent = "lead", "pj-lead"
	}
	return wire.Verdict(context.Background(), r.sock, method, c)
}

// typedTo is the prompts the fake herdr got for agent (its name, or its pane: typed lines address the pane id, the
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

// supersededLines is the brief lines the project's briefed facts in the log supersede (T-580), in order.
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
// of the line is earlier than the result record (T-702).
func (r *loopRig) waitBriefDelivered() {
	r.t.Helper()
	r.waitFor("the brief's delivery recorded", func() bool { return len(r.lineResults()) >= 1 && len(r.waiting()) == 0 })
}

// waiting is the lines serve has not delivered yet (obor status).
func (r *loopRig) waiting() []wire.MessageStatus { return r.app.Status().Project.Messages }

// sentLine is the line obor send posts in the target project for text from the project from: the sender, the text, and
// the reply in one command (T-466, T-646).
func sentLine(from, text string) string {
	return "[obor " + from + "] " + text + ` (reply: obor send ` + from + ` "…")`
}

// T-485: obor send's line is posted to the lead's Claude session at once, working or idle (no pane, no idle rule, no
// quiet period), exactly once, as "[obor <sender>] <text> (reply: …)": nothing typed into the lead's pane, no keys; its
// delivery result is sent; a repeated call id gets the stored reply and records nothing; a worker cannot send; text and
// sender are required.
func TestSendPostedOnceToTheLead(t *testing.T) {
	t.Parallel()
	r := lineRig(t, "working", "idle")
	r.start()
	for _, c := range []struct {
		call wire.Call
		want string
	}{
		{wire.Call{Role: "worker", Agent: "pj-l-01", Note: "x", From: "other"}, "a lead or the owner"},
		{wire.Call{From: "other"}, "a text is required"},
		{wire.Call{Note: "x"}, "the sender project is required"},
	} {
		if reply, err := r.line(wire.MethodSend, "bad", c.call); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("send %+v: %q, %v; want refused: %s", c.call, reply, err, c.want)
		}
	}
	reply, err := r.line(wire.MethodSend, "s1", wire.Call{Note: "release v0.12 is out", From: "other"})
	if err != nil || reply != "SENT to pj-lead in proj (posted to its session)" {
		t.Fatalf("send: %q, %v", reply, err)
	}
	if reply, err := r.line(wire.MethodSend, "s1", wire.Call{Note: "again", From: "other"}); err != nil || !strings.HasPrefix(reply, "SENT") {
		t.Errorf("the same call id: %q, %v; want the stored reply", reply, err)
	}
	if n := r.taskStep(event.MsgTask, "fact message"); n != 1 {
		t.Fatalf("message facts %d, want 1: %q", n, r.steps())
	}
	line := sentLine("other", "release v0.12 is out")
	r.waitFor("the line posted to the working lead", func() bool { return r.inbox.count("pj-lead", line) == 1 })
	r.waitFor("its delivery result", func() bool { return len(r.waiting()) == 0 })
	r.agentIs("w1:p5", "pj-lead", "idle")
	r.passes()
	r.passes()
	if n := r.inbox.count("pj-lead", line); n != 1 {
		t.Errorf("posted %d times, want once", n)
	}
	if got := r.lineResults(); len(got) != 1 || got[0] != "sent" {
		t.Errorf("delivery results %q, want one sent", got)
	}
	if n := r.typedTo("pj-lead", line); n != 0 {
		t.Errorf("typed %d times into the lead's pane, want 0", n)
	}
	for _, req := range r.srv.Requests() {
		if req.Method == "agent.send_keys" {
			t.Errorf("keys sent: %s", req.Params)
		}
	}
}

// T-485: with no inbox record for the lead, its line stays queued (obor status lists it, nothing is tried) and the
// reply says how the lead registers; across a restart it stays queued; once the record exists it is posted once, and
// a later restart posts it no more.
func TestSendQueuedUntilTheLeadIsRecorded(t *testing.T) {
	t.Parallel()
	r := lineRig(t, "idle", "idle")
	r.inbox.unrecord("pj-lead")
	r.start()
	reply, err := r.line(wire.MethodSend, "s1", wire.Call{Note: "hello", From: "other"})
	if want := "SENT to pj-lead in proj (queued: no session recorded for pj-lead; `obor restart pj-lead` once it is idle registers it, or quit it and run `obor lead`)"; err != nil || reply != want {
		t.Fatalf("send: %q, %v; want %q", reply, err, want)
	}
	r.passes()
	r.stop()
	r.start()
	r.passes()
	if w := r.waiting(); len(w) != 1 || w[0].Line != sentLine("other", "hello") || r.inbox.count("pj-lead", sentLine("other", "hello")) != 0 {
		t.Fatalf("after a restart with no record: waiting %+v, posted %d; want queued, untried", w,
			r.inbox.count("pj-lead", sentLine("other", "hello")))
	}
	if n := r.taskStep(event.MsgTask, "intent deliver.1") + r.typedTo("pj-lead", sentLine("other", "hello")); n != 0 {
		t.Fatalf("tried with no record: %q", r.steps())
	}
	r.inbox.record("pj-lead")
	r.waitFor("the line posted once recorded", func() bool { return r.inbox.count("pj-lead", sentLine("other", "hello")) == 1 })
	r.waitFor("no line waits", func() bool { return len(r.waiting()) == 0 })
	r.stop()
	r.start()
	r.passes()
	if n := r.inbox.count("pj-lead", sentLine("other", "hello")); n != 1 {
		t.Errorf("posted %d times across restarts, want once", n)
	}
}

// T-485: a post refused before a byte went out (a dead socket's dial) is fact failed and posted again on a later
// pass, once; a post that began and then failed is "sent: <error>" and never posted again.
func TestSendPostRefusedAndPartial(t *testing.T) {
	t.Parallel()
	r := lineRig(t, "idle", "idle")
	r.inbox.fail(false)
	r.start()
	if _, err := r.line(wire.MethodSend, "s1", wire.Call{Note: "one", From: "other"}); err != nil {
		t.Fatal(err)
	}
	r.waitFor("the refused post's failed fact", func() bool {
		for _, rec := range r.records() { // deliver.<the message's seq>
			s := rec.String()
			if rec.Task == event.MsgTask && strings.HasPrefix(s, "failed{deliver.") && strings.HasSuffix(s, ": dial unix: connection refused}") {
				return true
			}
		}
		return false
	})
	if w := r.waiting(); len(w) != 1 {
		t.Fatalf("a refused post: waiting %+v, want still queued", w)
	}
	r.inbox.fail(true)
	r.inbox.ok()
	r.waitFor("the retry posted", func() bool { return len(r.waiting()) == 0 })
	if got := r.lineResults(); len(got) != 1 || got[0] != "sent" {
		t.Errorf("delivery results %q, want one sent", got)
	}

	r.inbox.fail(true)
	if _, err := r.line(wire.MethodSend, "s2", wire.Call{Note: "two", From: "other"}); err != nil {
		t.Fatal(err)
	}
	r.waitFor("the partial post's result", func() bool { return len(r.lineResults()) == 2 })
	r.passes()
	r.passes()
	if got := r.lineResults(); got[1] != "sent: broken pipe" {
		t.Errorf("a post that began then failed: %q, want sent: broken pipe", got[1])
	}
	if n := r.inbox.tries("pj-lead", sentLine("other", "two")); n != 1 {
		t.Errorf("the partial post tried %d times, want once", n)
	}
	if w := r.waiting(); len(w) != 0 {
		t.Errorf("waiting %+v, want none", w)
	}
}

// T-466: a line waiting longer than 24 h (here: the lead has no inbox record) is the project's needs-you (a
// notification, obor status's needs-you with no command to answer it); posted at last, it ends (resumed).
func TestSendWaitingADayNeedsYou(t *testing.T) {
	t.Parallel()
	r := lineRig(t, "idle", "idle")
	r.inbox.unrecord("pj-lead")
	r.start()
	if _, err := r.line(wire.MethodSend, "s1", wire.Call{Note: "hello", From: "other"}); err != nil {
		t.Fatal(err)
	}
	r.later(23 * time.Hour)
	want := "fact needs-you{line to pj-lead from other waiting over 1d for its delivery: " + sentLine("other", "hello") +
		" (held: no inbox recorded for pj-lead: its SessionStart hook records one)}" // why it waits (T-746)
	if n := r.taskStep(event.MsgTask, want); n != 0 {
		t.Fatalf("a needs-you before 24 h: %q", r.steps())
	}
	r.later(2 * time.Hour)
	r.waitFor("the needs-you", func() bool { return r.taskStep(event.MsgTask, want) == 1 })
	facts := r.msgFacts()
	if len(facts) != 1 || facts[0].Step != event.StepNeedsYou || r.answerOf(facts[0]) != "" {
		t.Errorf("facts %+v, want the line's needs-you", facts)
	}
	r.passes()
	if n := r.taskStep(event.MsgTask, want); n != 1 {
		t.Errorf("needs-you recorded %d times, want once", n)
	}
	r.inbox.record("pj-lead")
	// The records the assertions below read: the line posted, the resumed fact in the log and folded out of the facts
	// in force (the log and the status fold are not updated in one step).
	r.waitFor("resumed", func() bool {
		return r.taskStep(event.MsgTask, "fact resumed") == 1 && r.inbox.count("pj-lead", sentLine("other", "hello")) == 1 &&
			len(r.msgFacts()) == 0
	})
	if r.inbox.count("pj-lead", sentLine("other", "hello")) != 1 || len(r.msgFacts()) != 0 {
		t.Errorf("posted %d, facts %+v; want posted once, none in force", r.inbox.count("pj-lead", sentLine("other", "hello")),
			r.msgFacts())
	}
}

// T-466: obor brief types its line into the idle role pane (still through Liner, T-485); once that pane has worked
// and is idle again, the lead gets "[obor] <agent> finished (briefed <age> ago)" posted once. Idle right after the
// typing is no finish; the lead itself cannot be briefed.
func TestBriefFinishedLineToTheLead(t *testing.T) {
	t.Parallel()
	r := lineRig(t, "idle", "idle")
	r.start()
	if _, err := r.line(wire.MethodBrief, "b0", wire.Call{To: "pj-lead", Note: "x"}); err == nil || !strings.Contains(err.Error(), "is the lead") {
		t.Errorf("a brief of the lead: %v, want refused", err)
	}
	reply, err := r.line(wire.MethodBrief, "b1", wire.Call{To: "pj-rev", Note: "verify finding 3"})
	if err != nil || reply != "BRIEFED pj-rev (delivered when its pane is idle; the lead hears when it finishes)" {
		t.Fatalf("brief: %q, %v", reply, err)
	}
	r.waitFor("the brief posted", func() bool { return r.inbox.count("pj-rev", "[obor pj-lead] verify finding 3") == 1 })
	r.waitBriefDelivered()
	r.passes()
	r.passes()
	if n := r.taskStep(event.MsgTask, "fact message"); n != 0 {
		t.Fatalf("a finished line before the reviewer worked: %q", r.steps())
	}
	r.agentIs("w1:p6", "pj-rev", "working")
	r.clock.Add(5 * time.Minute)
	r.passes()
	if n := r.taskStep(event.MsgTask, "fact message"); n != 0 {
		t.Fatalf("a finished line while the reviewer works: %q", r.steps())
	}
	r.agentIs("w1:p6", "pj-rev", "idle")
	const done = "[obor] pj-rev finished (briefed 5m ago)"
	r.waitFor("the finished line posted", func() bool { return r.inbox.count("pj-lead", done) == 1 })
	r.agentIs("w1:p6", "pj-rev", "working")
	r.agentIs("w1:p6", "pj-rev", "idle")
	r.passes()
	if n := r.taskStep(event.MsgTask, "fact message"); n != 1 || r.inbox.count("pj-lead", done) != 1 {
		t.Errorf("finished lines %d (posted %d), want one: the brief ended", n, r.inbox.count("pj-lead", done))
	}
	if typed := append(r.promptsTo("pj-rev"), r.promptsTo("pj-lead")...); len(typed) != 0 {
		t.Errorf("typed %q, want the brief and the finished line posted, none typed (T-499)", typed)
	}
}

// T-466: a brief without a line (a review's start carried it) is recorded while the agent already works on it;
// idle → the lead's finished line.
func TestBriefOfAFreshStart(t *testing.T) {
	t.Parallel()
	r := lineRig(t, "idle", "working")
	r.start()
	reply, err := r.line(wire.MethodBrief, "b1", wire.Call{To: "pj-rev"})
	if err != nil || reply != "BRIEF RECORDED for pj-rev (the lead hears when it finishes)" {
		t.Fatalf("brief: %q, %v", reply, err)
	}
	r.agentIs("w1:p6", "pj-rev", "idle")
	r.waitFor("the finished line recorded", func() bool { return r.taskStep(event.MsgTask, "fact message") == 1 })
	r.waitFor("the finished line posted", func() bool {
		return r.inbox.count("pj-lead", "[obor] pj-rev finished (briefed 0s ago)") == 1
	})
	for _, p := range r.prompts() {
		if p.to == "pj-rev" {
			t.Errorf("typed into pj-rev: %q (its start carried the brief)", p.text)
		}
	}
}

// T-475: a brief to an agent serve identifies no pane of (no label, session or start proves it) is still queued, and
// the reply says so and how to start it; an identified pane's reply is the usual one. The lead's line needs no pane
// (T-485): posted to its session.
func TestLineToAnUnnamedPane(t *testing.T) {
	t.Parallel()
	r := newLoopRig(t, "")
	r.f.Agent("w1:p5", "", "idle", r.p.Root).Workspace = "w1" // the lead's pane, unnamed
	rev := r.f.Agent("w1:p6", "pj-rev", "working", r.p.Root)
	rev.Workspace, rev.Label = "w1", "pj-rev"
	r.start()
	reply, err := r.line(wire.MethodSend, "s1", wire.Call{Note: "hello", From: "other"})
	if want := "SENT to pj-lead in proj (posted to its session)"; err != nil || reply != want {
		t.Errorf("send: %q, %v; want %q", reply, err, want)
	}
	reply, err = r.line(wire.MethodBrief, "b1", wire.Call{To: "pj-sol", Note: "look at x"})
	if want := "BRIEFED pj-sol (queued: no pane runs pj-sol; `obor restart pj-sol` starts it)"; err != nil || reply != want {
		t.Errorf("brief: %q, %v; want %q", reply, err, want)
	}
	reply, err = r.line(wire.MethodBrief, "b2", wire.Call{To: "pj-rev", Note: "verify"})
	if want := "BRIEFED pj-rev (delivered when its pane is idle; the lead hears when it finishes)"; err != nil || reply != want {
		t.Errorf("brief of a named pane: %q, %v; want %q", reply, err, want)
	}
	r.waitFor("the lead's line posted", func() bool { return r.inbox.count("pj-lead", sentLine("other", "hello")) == 1 })
	r.passes()
	if w := r.waiting(); len(w) != 2 || w[0].To != "pj-sol" || w[1].To != "pj-rev" {
		t.Errorf("lines waiting %+v, want pj-sol's (unnamed) and pj-rev's (working) still queued", w)
	}
}

// Live 2026-10-28 (T-480): herdr dropped a role pane's agent name and kept its label. A brief to that agent finds the
// pane by its label: its line is typed into the pane (by id: herdr has no name to address; a Codex pane, so typed,
// T-499), its work and idle end the brief, and the lead gets the finished line.
func TestBriefToALabelledPaneWithoutAName(t *testing.T) {
	t.Parallel()
	r := newLoopRig(t, "")
	r.f.Agent("w1:p5", "pj-lead", "idle", r.p.Root).Workspace = "w1"
	sol := r.f.Agent("w1:p6", "", "idle", r.p.Root)
	sol.Workspace, sol.Label, sol.Kind = "w1", "pj-sol", "codex"
	r.start()
	reply, err := r.line(wire.MethodBrief, "b1", wire.Call{To: "pj-sol", Note: "look at x"})
	if err != nil || reply != "BRIEFED pj-sol (delivered when its pane is idle; the lead hears when it finishes)" {
		t.Fatalf("brief of the labelled pane: %q, %v; want the named pane's reply", reply, err)
	}
	r.waitFor("the brief typed into w1:p6", func() bool { return r.typedTo("w1:p6", "[obor pj-lead] look at x") == 1 })
	r.waitBriefDelivered()
	r.f.Set("w1:p6", func(p *herdrtest.Pane) { p.Status = "working" })
	r.srv.Push(herdr.EventStatus, map[string]any{"pane_id": "w1:p6", "agent_status": "working", "agent": "codex"})
	r.waitFor("its work logged", func() bool { return r.taskStep(event.MsgTask, "fact worked") == 1 })
	r.clock.Add(2 * time.Minute)
	r.f.Set("w1:p6", func(p *herdrtest.Pane) { p.Status = "idle" })
	r.srv.Push(herdr.EventStatus, map[string]any{"pane_id": "w1:p6", "agent_status": "idle", "agent": "codex"})
	const done = "[obor] pj-sol finished (briefed 2m ago)"
	r.waitFor("the finished line posted to the lead", func() bool { return r.inbox.count("pj-lead", done) == 1 })
}

// T-575: a role agent with no inbox: brief A waits, then a fresh start's brief B (no line: the start carried it) ends
// A; the inbox's arrival delivers nothing for A, before or after a restart of serve.
func TestBriefSupersedesAFreshStartsOldLine(t *testing.T) {
	t.Parallel()
	r := lineRig(t, "idle", "working")
	r.inbox.unrecord("pj-rev")
	r.start()
	const a = "[obor pj-lead] first"
	if _, err := r.line(wire.MethodBrief, "b1", wire.Call{To: "pj-rev", Note: "first"}); err != nil {
		t.Fatal(err)
	}
	if w := r.waiting(); len(w) != 1 || w[0].Line != a {
		t.Fatalf("waiting %+v, want the first brief's line alone", w)
	}
	reply, err := r.line(wire.MethodBrief, "b2", wire.Call{To: "pj-rev"})
	if err != nil || !strings.HasSuffix(reply, "(replaces 1 undelivered brief)") {
		t.Fatalf("the fresh start's brief: %q, %v; want it to say it replaces the waiting one", reply, err)
	}
	if w := r.waiting(); len(w) != 0 {
		t.Errorf("waiting %+v after the fresh start's brief, want none: the old line is superseded", w)
	}
	sup := r.supersededLines()
	if len(sup) != 1 || sup[0].By == 0 || sup[0].Seq >= sup[0].By {
		t.Fatalf("superseded lines %+v, want the old line replaced by the fresh start's brief", sup)
	}
	var by event.Message
	for _, rec := range r.records() {
		if rec.Seq == sup[0].By {
			_ = json.Unmarshal(rec.Data, &by)
		}
	}
	if by.Line != "" {
		t.Errorf("the newer brief carried line %q, want none (its start carried it)", by.Line)
	}
	// The inbox appears: the old line is not delivered, before or after a restart.
	r.inbox.record("pj-rev")
	r.agentIs("w1:p6", "pj-rev", "idle")
	r.passes()
	r.stop()
	r.start()
	r.passes()
	if n := r.inbox.count("pj-rev", a) + r.typedTo("pj-rev", a); n != 0 {
		t.Errorf("the superseded line was delivered %d times, want none", n)
	}
	if n := len(r.supersededLines()); n != 1 {
		t.Errorf("superseded lines after a restart: %d, want the one", n)
	}
}

// T-575: brief A waits; obor brief B ends it and is delivered once, and A is never delivered.
func TestBriefSupersedesAQueuedBrief(t *testing.T) {
	t.Parallel()
	r := lineRig(t, "idle", "working") // the reviewer is busy: brief A waits
	r.start()
	if _, err := r.line(wire.MethodBrief, "b1", wire.Call{To: "pj-rev", Note: "first"}); err != nil {
		t.Fatal(err)
	}
	const a = "[obor pj-lead] first"
	r.waitFor("the first brief queued", func() bool { return len(r.waiting()) == 1 })
	reply, err := r.line(wire.MethodBrief, "b2", wire.Call{To: "pj-rev", Note: "second"})
	if err != nil || !strings.HasSuffix(reply, "(replaces 1 undelivered brief)") {
		t.Fatalf("the second brief: %q, %v", reply, err)
	}
	const b = "[obor pj-lead] second"
	if w := r.waiting(); len(w) != 1 || w[0].Line != b {
		t.Fatalf("waiting %+v, want the second brief's line alone", w)
	}
	r.agentIs("w1:p6", "pj-rev", "idle")
	r.waitFor("the second brief delivered", func() bool { return r.inbox.count("pj-rev", b) == 1 })
	r.passes()
	if n := r.inbox.count("pj-rev", a) + r.typedTo("pj-rev", a); n != 0 {
		t.Errorf("the superseded brief was delivered %d times, want none", n)
	}
	if n := r.inbox.count("pj-rev", b) + r.typedTo("pj-rev", b); n != 1 {
		t.Errorf("the second brief delivered %d times, want once", n)
	}
}

// T-575: the lead's lines are never superseded: a send to the lead and another brief's finished line stay queued
// across a brief to a different role agent.
func TestBriefLeavesLeadLinesQueued(t *testing.T) {
	t.Parallel()
	r := lineRig(t, "idle", "working")
	sol := r.f.Agent("w1:p7", "pj-sol", "working", r.p.Root)
	sol.Workspace, sol.Label = "w1", "pj-sol"
	r.inbox.unrecord("pj-lead") // its lines wait
	r.start()
	if _, err := r.line(wire.MethodBrief, "s1", wire.Call{To: "pj-sol"}); err != nil { // a fresh start's brief: sol works
		t.Fatal(err)
	}
	r.agentIs("w1:p7", "pj-sol", "idle")
	const done = "[obor] pj-sol finished (briefed 0s ago)"
	r.waitFor("the finished line queued for the lead", func() bool {
		for _, m := range r.waiting() {
			if m.To == "pj-lead" && m.Line == done {
				return true
			}
		}
		return false
	})
	if _, err := r.line(wire.MethodSend, "x1", wire.Call{Note: "hello", From: "other"}); err != nil {
		t.Fatal(err)
	}
	if _, err := r.line(wire.MethodBrief, "b1", wire.Call{To: "pj-rev", Note: "first"}); err != nil {
		t.Fatal(err)
	}
	if _, err := r.line(wire.MethodBrief, "b2", wire.Call{To: "pj-rev", Note: "second"}); err != nil {
		t.Fatal(err)
	}
	got := map[string]bool{}
	for _, m := range r.waiting() {
		got[m.Line] = true
	}
	for _, line := range []string{done, sentLine("other", "hello"), "[obor pj-lead] second"} {
		if !got[line] {
			t.Errorf("waiting %+v lacks %q", r.waiting(), line)
		}
	}
	if got["[obor pj-lead] first"] {
		t.Errorf("the superseded brief still waits: %+v", r.waiting())
	}
	if len(got) != 3 {
		t.Errorf("waiting %d lines, want the two lead lines and the second brief", len(got))
	}
}

// DD-v0.12 A3: obor send and obor brief to a project without a merge loop (no integrator role) are refused as obor
// dispatch is: nothing there would ever deliver the line.
func TestLinesRefusedWithoutAMergeLoop(t *testing.T) {
	t.Parallel()
	r := newLoopRig(t, "")
	r.setConfig("roles", map[string]any{"reviewer": ""})
	r.start()
	if st := r.app.Status().Project; st.Merging {
		t.Fatalf("precondition: %s merges", st.Name)
	}
	for _, c := range []struct {
		method string
		call   wire.Call
	}{
		{wire.MethodSend, wire.Call{Note: "hello", From: "other"}},
		{wire.MethodBrief, wire.Call{To: "pj-rev", Note: "verify"}},
	} {
		reply, err := r.line(c.method, c.method+"1", c.call)
		if err == nil || err.Error() != "merge loop off for proj (no integrator role)" || reply != "" {
			t.Errorf("%s: %q, %v; want the merge loop's refusal", c.method, reply, err)
		}
	}
	if n := len(r.waiting()); n != 0 || r.taskStep(event.MsgTask, "fact message")+r.taskStep(event.MsgTask, "fact briefed") != 0 {
		t.Errorf("recorded while refused: %d lines, %q", n, r.steps())
	}
}
