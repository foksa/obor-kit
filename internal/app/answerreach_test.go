package app

import (
	"context"
	"obor/internal/app/event"
	"obor/internal/app/port"
	"obor/internal/app/wire"
	"slices"
	"strings"
	"testing"
	"time"
)

// T-746 (axis-alies T-1421): the answer to an idle worker reaches it, a line that cannot go out says why, and obor
// resume takes a stalled worker's needs-you.

// staleHerdr is the ident harness with pj-o-01 idle in w1:p1, where herdr reports session s-herdr (stale: launch's
// start row carries it too) while the session its SessionStart hook reported from w1:p1, and its inbox record, is
// s-live; then the lead's answer to T-1's worker recorded (fact answered).
func staleHerdr(t *testing.T) *harness {
	t.Helper()
	h, _ := newIdentHarness(t)
	h.pane("w1:p1").Session = "s-herdr"
	h.started("pj-o-01", "w1:p1", "t1", "s-herdr")
	h.connect()
	h.session("pj-o-01", "s-live", "/tr/s-live.jsonl", 100)
	h.a.opt.Inboxed = func(string) (string, bool) { return "s-live", true }
	if w := h.where("pj-o-01"); !strings.HasPrefix(w, "identified w1:p1 ") {
		t.Fatalf("precondition: pj-o-01 is %q, want identified at w1:p1", w)
	}
	h.a.mu.Lock()
	s, _ := h.proj().session("pj-o-01")
	h.a.mu.Unlock()
	if s.ID != "s-live" || h.pane("w1:p1").Session != "s-herdr" || h.pane("w1:p1").Status != "idle" {
		t.Fatalf("precondition: recorded %q, herdr's %q, status %q; want s-live, s-herdr, idle", s.ID,
			h.pane("w1:p1").Session, h.pane("w1:p1").Status)
	}
	if !h.a.record(h.proj(), event.Record{Task: event.MsgTask, Step: event.StepAnswered, Kind: event.KindFact},
		event.Message{To: "pj-o-01", From: "pj-lead", Line: "[obor answer] move the helpers down", Task: "T-1"}) {
		t.Fatal("answer not recorded")
	}
	return h
}

// The live case: herdr's session for the pane differs from the recorded one, the worker is idle, an answer queued. The
// session the pane's own SessionStart hook reported (the inbox record's) is the one running there: the answer is
// posted to its inbox, nothing typed, nothing left queued.
func TestAnswerReachesIdleWorkerHerdrSessionStale(t *testing.T) {
	t.Parallel()
	h := staleHerdr(t)
	in, liner := &kindSpy{}, &linerSpy{}
	h.a.deliver(context.Background(), h.proj(), &Loop{Inbox: in, Typer: liner})
	if !slices.Equal(in.calls, []string{"claude pj-o-01: [obor answer] move the helpers down"}) || len(liner.calls) != 0 ||
		len(h.lines()) != 0 {
		t.Errorf("posted %q, typed %q, queued %q; want the answer posted to pj-o-01's inbox", in.calls, liner.calls,
			h.lines())
	}
}

// A record reported from another pane (a second session of that name, DD-v0.13 A3) is not the pane's: herdr's view
// decides, the inbox (s-live) is not w1:p1's, and the answer is typed into w1:p1 as the kind takes a typed line.
func TestAnswerTypedWhenInboxIsAnotherPanes(t *testing.T) {
	t.Parallel()
	h := staleHerdr(t)
	h.a.mu.Lock()
	ss := h.proj().folded.sessions
	ss[len(ss)-1].Pane = "w1:p5" // the record came from another pane
	h.a.mu.Unlock()
	in, liner := &kindSpy{}, &linerSpy{}
	h.a.deliver(context.Background(), h.proj(), &Loop{Inbox: in, Typer: liner})
	if len(in.calls) != 0 || !slices.Equal(liner.calls, []string{"w1:p1: [obor answer] move the helpers down"}) ||
		len(h.lines()) != 0 {
		t.Errorf("posted %q, typed %q, queued %q; want the answer typed into w1:p1", in.calls, liner.calls, h.lines())
	}
}

// A line that cannot go out says why: with no inbox for the pane's session and nothing that types, the answer stays
// queued, obor status shows the reason with it, and its needs-you (after msgWait) names it.
func TestUndeliverableLineSaysWhy(t *testing.T) {
	t.Parallel()
	h := staleHerdr(t)
	h.a.opt.Inboxed = func(string) (string, bool) { return "s-other", true } // neither the record's nor herdr's
	in := &kindSpy{}
	fx := &Loop{Inbox: in}
	h.a.deliver(context.Background(), h.proj(), fx)
	if len(in.calls) != 0 || len(h.lines()) != 1 {
		t.Fatalf("posted %q, queued %q; want the answer held", in.calls, h.lines())
	}
	ms := h.a.Status().Project.Messages
	const why = "no inbox recorded for the session in pj-o-01's pane w1:p1, and serve types no lines"
	if len(ms) != 1 || ms[0].Held != why {
		t.Errorf("status lines %+v; want the answer with held %q", ms, why)
	}
	h.now = h.now.Add(msgWait + time.Minute)
	h.a.deliver(context.Background(), h.proj(), fx)
	var reasons []string
	for _, f := range h.a.Status().Project.Facts {
		if f.Task == event.MsgTask && f.Step == event.StepNeedsYou {
			reasons = append(reasons, f.Fact().Reason)
		}
	}
	if len(reasons) != 1 || !strings.HasSuffix(reasons[0], "(held: "+why+")") {
		t.Errorf("needs-you of the lines %q; want one naming why the line is held", reasons)
	}
}

// Unchanged: an answer to a working worker waits (status: it is busy) and is delivered at its next idle.
func TestAnswerToWorkingWorkerAtNextIdle(t *testing.T) {
	t.Parallel()
	h := staleHerdr(t)
	h.status("w1:p1", func(p *port.Pane) { p.Status = "working" })
	in := &kindSpy{}
	fx := &Loop{Inbox: in, Typer: &linerSpy{}}
	h.a.deliver(context.Background(), h.proj(), fx)
	ms := h.a.Status().Project.Messages
	if len(in.calls) != 0 || len(ms) != 1 || !strings.Contains(ms[0].Held, "pj-o-01 is busy in pane w1:p1") {
		t.Fatalf("working: posted %q, status lines %+v; want the answer waiting, held while busy", in.calls, ms)
	}
	h.status("w1:p1", func(p *port.Pane) { p.Status = "idle" })
	h.a.deliver(context.Background(), h.proj(), fx)
	if !slices.Equal(in.calls, []string{"claude pj-o-01: [obor answer] move the helpers down"}) || len(h.lines()) != 0 {
		t.Errorf("idle again: posted %q, queued %q; want the answer posted once", in.calls, h.lines())
	}
}

// obor resume takes the watchdog's needs-you{stalled} (axis-alies: refused as "no needs-you of the merge loop"): the
// decision goes to the worker as "[obor answer] <decision>", the needs-you ends, a repeated call id gets the stored
// reply; regate, and no decision, are refused; with no needs-you at all the refusal is as before.
func TestResumeStalledWorker(t *testing.T) {
	t.Parallel()
	h, p, fact := answerSeed(t, reasonStalled, "", "", true)
	if fact.Step != event.StepNeedsYou || fact.Fact().Reason != "stalled" || fact.Fact().Merge {
		t.Fatalf("precondition: %+v; want the watchdog's needs-you{stalled}", fact)
	}
	call := func(id, to, note string) (string, error) {
		return h.a.resume(wire.Call{Root: h.root, Agent: "pj-lead", Role: "lead", CallID: id, Task: "T-1", To: to, Note: note})
	}
	if _, err := call("c0", wire.ResumeRegate, "x"); err == nil || !strings.Contains(err.Error(), "worker stalled: reprompt it") {
		t.Errorf("regate: %v; want refused", err)
	}
	if _, err := call("c0", wire.ResumeReprompt, " "); err == nil || !strings.Contains(err.Error(), "say the decision") {
		t.Errorf("no decision: %v; want refused", err)
	}
	const want = "RESUMED T-1: reprompt (stalled)"
	if reply, err := call("c1", wire.ResumeReprompt, "keep the guard; move the helpers down"); err != nil || reply != want {
		t.Fatalf("resume: %q, %v; want %q", reply, err, want)
	}
	h.a.mu.Lock()
	stalled := p.has("T-1", event.StepNeedsYou, reasonStalled)
	h.a.mu.Unlock()
	if stalled {
		t.Error("needs-you{stalled} still in force after the resume")
	}
	if got := h.lines(); !slices.Equal(got, []string{"pj-o-01: [obor answer] keep the guard; move the helpers down"}) {
		t.Errorf("queued %q; want the decision to pj-o-01", got)
	}
	if reply, err := call("c1", wire.ResumeReprompt, "keep the guard; move the helpers down"); err != nil || reply != want {
		t.Errorf("repeated call id: %q, %v; want the stored reply", reply, err)
	}
	if n := len(h.lines()); n != 1 {
		t.Errorf("%d lines queued after the repeat, want 1", n)
	}
	if _, err := call("c2", "", "again"); err == nil || !strings.Contains(err.Error(), "T-1 has no needs-you of the merge loop") {
		t.Errorf("resume with nothing in force: %v; want the refusal as before", err)
	}
	in := &kindSpy{}
	h.a.opt.Inboxed = func(string) (string, bool) { return "", true }
	h.a.deliver(context.Background(), h.proj(), &Loop{Inbox: in, Typer: &linerSpy{}})
	if !slices.Equal(in.calls, []string{"claude pj-o-01: [obor answer] keep the guard; move the helpers down"}) {
		t.Errorf("posted %q; want the decision reach pj-o-01", in.calls)
	}
}

// busyAnswered is staleHerdr with pj-o-01 working: one pass holds the queued answer (precondition).
func busyAnswered(t *testing.T) *harness {
	t.Helper()
	h := staleHerdr(t)
	h.status("w1:p1", func(p *port.Pane) { p.Status = "working" })
	in := &kindSpy{}
	h.a.deliver(context.Background(), h.proj(), &Loop{Inbox: in, Typer: &linerSpy{}})
	if len(in.calls) != 0 || len(h.lines()) != 1 || len(h.a.Status().Project.Messages) != 1 {
		t.Fatalf("precondition: posted %q, queued %q; want the answer waiting for the busy worker", in.calls, h.lines())
	}
	return h
}

// settledThenIdle checks that after the question was settled the answer is in neither the queue nor obor status, and
// that the worker idle again gets nothing.
func settledThenIdle(t *testing.T, h *harness) {
	t.Helper()
	if q, ms := h.lines(), h.a.Status().Project.Messages; len(q) != 0 || len(ms) != 0 {
		t.Errorf("queued %q, status lines %+v; want the answer ended", q, ms)
	}
	h.status("w1:p1", func(p *port.Pane) { p.Status = "idle" })
	in, liner := &kindSpy{}, &linerSpy{}
	h.a.deliver(context.Background(), h.proj(), &Loop{Inbox: in, Typer: liner})
	if len(in.calls) != 0 || len(liner.calls) != 0 {
		t.Errorf("posted %q, typed %q at the next idle; want the settled answer never delivered", in.calls, liner.calls)
	}
}

// An undelivered answer ends when its question is settled another way (T-746: axis-alies' 23-min-old answer was to be
// delivered late into the working session): the worker submits (task_done), asks again (a new Blocked: report), or the
// task is unblocked (obor resume of a merge-loop needs-you). One case per trigger.
func TestUndeliveredAnswerEndsWhenSettled(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		rec  event.Record
		data event.FactData
	}{
		{"submit", event.Record{Task: "T-1", Attempt: 1, Step: event.StepSubmitted, Kind: event.KindFact},
			event.FactData{Lane: "O-01", Agent: "pj-o-01", Merge: true}},
		{"re-ask", event.Record{Task: "T-1", Attempt: 1, Step: event.StepNeedsYou, Kind: event.KindFact},
			event.FactData{Lane: "O-01", Agent: "pj-o-01", Reason: reasonAsks + "and the probe too?"}},
		{"unblock", event.Record{Task: "T-1", Attempt: 1, Step: event.StepUnblocked, Kind: event.KindFact},
			event.FactData{Lane: "O-01", Agent: "pj-lead", Reason: wire.ResumeReprompt, Merge: true}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			h := busyAnswered(t)
			if !h.a.record(h.proj(), tc.rec, tc.data) {
				t.Fatal("fact not recorded")
			}
			settledThenIdle(t, h)
		})
	}
}

// A line whose delivery is in flight (claimed: typing) may have landed: a settling fact leaves it (as T-575 keeps a
// brief's); an answer to another task is not this question's.
func TestSettledAnswerInFlightOrOtherTaskStays(t *testing.T) {
	t.Parallel()
	h := busyAnswered(t)
	if !h.a.record(h.proj(), event.Record{Task: event.MsgTask, Step: event.StepAnswered, Kind: event.KindFact},
		event.Message{To: "pj-o-02", From: "pj-lead", Line: "[obor answer] other", Task: "T-2"}) {
		t.Fatal("answer not recorded")
	}
	h.a.mu.Lock()
	for _, q := range h.proj().out.msgs {
		if q.msg.Task == "T-1" {
			q.state = lineSending // its delivery claimed
		}
	}
	h.a.mu.Unlock()
	h.a.record(h.proj(), event.Record{Task: "T-1", Attempt: 1, Step: event.StepSubmitted, Kind: event.KindFact},
		event.FactData{Lane: "O-01", Merge: true})
	if q := h.lines(); len(q) != 2 {
		t.Errorf("queued %q; want the in-flight answer and T-2's answer kept", q)
	}
}

// A stalled worker's resume ends an older undelivered answer to the task but not its own decision; a later answer (or
// resume) of the task ends that one in turn.
func TestResumeStalledEndsOlderAnswer(t *testing.T) {
	t.Parallel()
	h, _, _ := answerSeed(t, reasonStalled, "", "", true)
	if !h.a.record(h.proj(), event.Record{Task: event.MsgTask, Step: event.StepAnswered, Kind: event.KindFact},
		event.Message{To: "pj-o-01", From: "pj-lead", Line: "[obor answer] old", Task: "T-1"}) {
		t.Fatal("answer not recorded")
	}
	h.a.record(h.proj(), event.Record{Task: "T-1", Step: event.StepNeedsYou, Kind: event.KindFact},
		event.FactData{Lane: "O-01", Reason: reasonStalled}) // stalled again, the old answer still queued
	if got := h.lines(); !slices.Equal(got, []string{"pj-o-01: [obor answer] old"}) {
		t.Fatalf("precondition: queued %q, want the old answer", got)
	}
	if _, err := h.a.resume(wire.Call{Root: h.root, Agent: "pj-lead", Role: "lead", CallID: "r1", Task: "T-1",
		Note: "new decision"}); err != nil {
		t.Fatal(err)
	}
	if got := h.lines(); !slices.Equal(got, []string{"pj-o-01: [obor answer] new decision"}) {
		t.Errorf("after the resume: queued %q, want its decision alone", got)
	}
	h.a.record(h.proj(), event.Record{Task: event.MsgTask, Step: event.StepAnswered, Kind: event.KindFact},
		event.Message{To: "pj-o-01", From: "pj-lead", Line: "[obor answer] newer", Task: "T-1"})
	if got := h.lines(); !slices.Equal(got, []string{"pj-o-01: [obor answer] newer"}) {
		t.Errorf("after a newer answer: queued %q, want it alone", got)
	}
}

// The end is folded: on replay the settled answer stays ended.
func TestSettledAnswerEndsOnReplay(t *testing.T) {
	t.Parallel()
	now := time.Now()
	a, p := lineApp(t, lineRoot(t), &now)
	lineStatus(a, "w:rev", "working", now)
	a.record(p, event.Record{Task: event.MsgTask, Step: event.StepAnswered, Kind: event.KindFact},
		event.Message{To: "pj-rev", From: "pj-lead", Line: "[obor answer] yes", Task: "T-1"})
	if q := p.queuedLines(); len(q) != 1 {
		t.Fatalf("precondition: queued %d lines, want the answer", len(q))
	}
	a.record(p, event.Record{Task: "T-1", Attempt: 1, Step: event.StepSubmitted, Kind: event.KindFact},
		event.FactData{Merge: true})
	if q := p.queuedLines(); len(q) != 0 {
		t.Errorf("live: queued %d lines, want none", len(q))
	}
	_, r := lineReload(t, a, p, &now)
	if q := r.queuedLines(); len(q) != 0 {
		t.Errorf("replay: queued %+v, want none", q)
	}
}
