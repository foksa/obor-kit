package app

import (
	"context"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"obor/internal/app/event"
	"obor/internal/app/outbox"
	"obor/internal/app/port"
	"obor/internal/app/wire"
)

// obor resume's arguments: --regate and --sendback are its flags (one, anywhere after the task); any
// other nonblank argument is the text, a single word too, a former keyword included, one starting with - or -- too
// after -- every argument is the text; nothing else parses.
func TestResumeArgs(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		args     []string
		to, note string
		ok       bool
	}{
		{[]string{"T-1"}, "", "", true},
		{[]string{"T-1", "yes"}, "", "yes", true},
		{[]string{"T-1", "no"}, "", "no", true},
		{[]string{"T-1", "approved"}, "", "approved", true},
		{[]string{"T-1", "retry"}, "", "retry", true}, // a former keyword is the text
		{[]string{"T-1", "regate"}, "", "regate", true},
		{[]string{"T-1", "yes please"}, "", "yes please", true},
		{[]string{"T-1", "--regate"}, wire.ResumeRegate, "", true},
		{[]string{"T-1", "--sendback", "fix x"}, wire.ResumeSendback, "fix x", true},
		{[]string{"T-1", "fix x", "--sendback"}, wire.ResumeSendback, "fix x", true},
		{[]string{"T-1", "--regate", "--sendback"}, "", "", false},
		{[]string{"T-1", "--reprompt", "yes"}, "", "", false}, // --reprompt is a text: two texts
		{[]string{"T-1", "--regate", "yes", "more"}, "", "", false},
		{[]string{"T-1", "yes", "more"}, "", "", false},
		{[]string{"T-1", " "}, "", "", false},
		{[]string{"T-1", "--regate", ""}, "", "", false},
		{[]string{"--regate", "T-1"}, "", "", false},
		// Only --regate and --sendback are flags; any other argument is the text, -- ends the flags
		{[]string{"T-1", "- use A\n- use B"}, "", "- use A\n- use B", true},
		{[]string{"T-1", "--force is safe"}, "", "--force is safe", true},
		{[]string{"T-1", "--force is safe", "--sendback"}, wire.ResumeSendback, "--force is safe", true},
		{[]string{"T-1", "-x"}, "", "-x", true},
		{[]string{"T-1", "--", "--regate"}, "", "--regate", true},
		{[]string{"T-1", "--regate", "--", "--sendback"}, wire.ResumeRegate, "--sendback", true},
		{[]string{"T-1", "--", "--"}, "", "--", true},
		{[]string{"T-1", "--", "--regate", "yes"}, "", "", false},
		{[]string{"T-1", "--regate", "--regate"}, "", "", false},
		{[]string{""}, "", "", false},
		{nil, "", "", false},
	} {
		got, ok := wire.ResumeArgs(c.args)
		if ok != c.ok || ok && (got.Task != c.args[0] || got.Flag != c.to || got.Text != c.note) {
			t.Errorf("ResumeArgs(%q) = %+v, %v; want to %q, note %q, %v", c.args, got, ok, c.to, c.note, c.ok)
		}
	}
}

// The live session is the one the pane's own SessionStart hook reported, not herdr's stale value: the worker is idle
// with an answer queued, so the answer is posted to that session's inbox, nothing typed, nothing left queued.
func TestAnswerReachesIdleWorkerHerdrSessionStale(t *testing.T) {
	t.Parallel()
	h := staleHerdr(t)
	in, liner := &kindSpy{}, &linerSpy{}
	h.a.deliver(context.Background(), h.proj(), &Loop{Inbox: in, Typer: liner})
	if !slices.Equal(in.calls, []string{"claude pj-l-01: [obor answer] move the helpers down"}) || len(liner.calls) != 0 ||
		len(h.lines()) != 0 {
		t.Errorf("posted %q, typed %q, queued %q; want the answer posted to pj-l-01's inbox", in.calls, liner.calls,
			h.lines())
	}
}

// A record reported from another pane (a second session of that name) is not the pane's: herdr's view
// decides, the inbox (s-live) is not w1:p1's, and the answer is typed into w1:p1 as the kind takes a typed line.
func TestAnswerTypedWhenInboxIsAnotherPanes(t *testing.T) {
	t.Parallel()
	h := staleHerdr(t)
	h.a.mu.Lock()
	ss := h.proj().folded.sessions.All()
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
	const why = "no inbox recorded for the session in pj-l-01's pane w1:p1, and serve types no lines"
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
	if len(reasons) != 1 || !strings.HasSuffix(reasons[0], "(undelivered: "+why+")") {
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
	if len(in.calls) != 0 || len(ms) != 1 || !strings.Contains(ms[0].Held, "pj-l-01 is busy in pane w1:p1") {
		t.Fatalf("working: posted %q, status lines %+v; want the answer waiting, held while busy", in.calls, ms)
	}
	h.status("w1:p1", func(p *port.Pane) { p.Status = "idle" })
	h.a.deliver(context.Background(), h.proj(), fx)
	if !slices.Equal(in.calls, []string{"claude pj-l-01: [obor answer] move the helpers down"}) || len(h.lines()) != 0 {
		t.Errorf("idle again: posted %q, queued %q; want the answer posted once", in.calls, h.lines())
	}
}

// obor resume takes the watchdog's needs-you{stalled} (axis-alies: refused as "no needs-you of the merge loop"): the
// decision goes to the worker as "[obor answer] <decision>", the needs-you ends, a repeated call id gets the stored
// reply; --regate, and no text, are refused (obor resume's line to the worker); with nothing in force the
// refusal is the state's.
func TestResumeStalledWorker(t *testing.T) {
	t.Parallel()
	h, p, fact := answerSeed(t, reasonStalled, "", "", true)
	if fact.Step != event.StepNeedsYou || fact.Fact().Reason != "stalled" || fact.Fact().Merge {
		t.Fatalf("precondition: %+v; want the watchdog's needs-you{stalled}", fact)
	}
	call := func(id, to, note string) (string, error) {
		return h.a.resume(wire.ResumeReq{Envelope: wire.Envelope{Root: h.root, Agent: "pj-lead", Role: "lead",
			CallID: id}, Task: "T-1", Flag: to, Text: note})
	}
	if _, err := call("c0", wire.ResumeRegate, "x"); err == nil || !strings.Contains(err.Error(), "--regate does not fit: obor resume T-1 sends the worker the line") {
		t.Errorf("regate: %v; want refused", err)
	}
	if _, err := call("c0", "", " "); err == nil || !strings.Contains(err.Error(), "say the text") {
		t.Errorf("no decision: %v; want refused", err)
	}
	const want = "RESUMED T-1: reprompt (stalled)"
	if reply, err := call("c1", "", "keep the guard; move the helpers down"); err != nil || reply != want {
		t.Fatalf("resume: %q, %v; want %q", reply, err, want)
	}
	h.a.mu.Lock()
	stalled := p.has("T-1", event.StepNeedsYou, reasonStalled)
	h.a.mu.Unlock()
	if stalled {
		t.Error("needs-you{stalled} still in force after the resume")
	}
	if got := h.lines(); !slices.Equal(got, []string{"pj-l-01: [obor answer] keep the guard; move the helpers down"}) {
		t.Errorf("queued %q; want the decision to pj-l-01", got)
	}
	if reply, err := call("c1", "", "keep the guard; move the helpers down"); err != nil || reply != want {
		t.Errorf("repeated call id: %q, %v; want the stored reply", reply, err)
	}
	if n := len(h.lines()); n != 1 {
		t.Errorf("%d lines queued after the repeat, want 1", n)
	}
	if _, err := call("c2", "", "again"); err == nil || !strings.Contains(err.Error(), "with nothing to resume") {
		t.Errorf("resume with nothing in force: %v; want the refusal as before", err)
	}
	in := &kindSpy{}
	h.a.opt.Inboxed = func(string) (string, bool) { return "", true }
	h.a.deliver(context.Background(), h.proj(), &Loop{Inbox: in, Typer: &linerSpy{}})
	if !slices.Equal(in.calls, []string{"claude pj-l-01: [obor answer] keep the guard; move the helpers down"}) {
		t.Errorf("posted %q; want the decision reach pj-l-01", in.calls)
	}
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

// An undelivered answer ends when its question is settled another way: the worker submits (task_done), asks again
// (a new Blocked: report), or the task is unblocked (obor resume of a merge-loop needs-you); a settled answer is
// never delivered, not even into the session that asks next. One case per trigger.
func TestUndeliveredAnswerEndsWhenSettled(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		rec  event.Record
		data event.FactData
	}{
		{"submit", event.Record{Task: "T-1", Attempt: 1, Step: event.StepSubmitted, Kind: event.KindFact},
			event.FactData{Lane: "L-01", Agent: "pj-l-01", Merge: true}},
		{"re-ask", event.Record{Task: "T-1", Attempt: 1, Step: event.StepNeedsYou, Kind: event.KindFact},
			event.FactData{Lane: "L-01", Agent: "pj-l-01", Reason: reasonAsks + "and the probe too?"}},
		{"unblock", event.Record{Task: "T-1", Attempt: 1, Step: event.StepUnblocked, Kind: event.KindFact},
			event.FactData{Lane: "L-01", Agent: "pj-lead", Reason: wire.ResumeReprompt, Merge: true}},
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

// A line whose delivery is in flight (claimed: typing) may have landed: a settling fact leaves it (as a
// brief's); an answer to another task is not this question's.
func TestSettledAnswerInFlightOrOtherTaskStays(t *testing.T) {
	t.Parallel()
	h := busyAnswered(t)
	if !h.a.record(h.proj(), event.Record{Task: event.MsgTask, Step: event.StepAnswered, Kind: event.KindFact},
		event.Message{To: "pj-l-02", From: "pj-lead", Line: "[obor answer] other", Task: "T-2"}) {
		t.Fatal("answer not recorded")
	}
	h.a.mu.Lock()
	for _, q := range h.proj().out.Queued() {
		if q.Msg().Task == "T-1" {
			h.proj().out.Move(q, outbox.Claimed) // its delivery claimed: sending
		}
	}
	h.a.mu.Unlock()
	h.a.record(h.proj(), event.Record{Task: "T-1", Attempt: 1, Step: event.StepSubmitted, Kind: event.KindFact},
		event.FactData{Lane: "L-01", Merge: true})
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
		event.Message{To: "pj-l-01", From: "pj-lead", Line: "[obor answer] old", Task: "T-1"}) {
		t.Fatal("answer not recorded")
	}
	h.a.record(h.proj(), event.Record{Task: "T-1", Step: event.StepNeedsYou, Kind: event.KindFact},
		event.FactData{Lane: "L-01", Reason: reasonStalled}) // stalled again, the old answer still queued
	if got := h.lines(); !slices.Equal(got, []string{"pj-l-01: [obor answer] old"}) {
		t.Fatalf("precondition: queued %q, want the old answer", got)
	}
	if _, err := h.a.resume(wire.ResumeReq{Envelope: wire.Envelope{Root: h.root, Agent: "pj-lead", Role: "lead",
		CallID: "r1"}, Task: "T-1", Text: "new decision"}); err != nil {
		t.Fatal(err)
	}
	if got := h.lines(); !slices.Equal(got, []string{"pj-l-01: [obor answer] new decision"}) {
		t.Errorf("after the resume: queued %q, want its decision alone", got)
	}
	h.a.record(h.proj(), event.Record{Task: event.MsgTask, Step: event.StepAnswered, Kind: event.KindFact},
		event.Message{To: "pj-l-01", From: "pj-lead", Line: "[obor answer] newer", Task: "T-1"})
	if got := h.lines(); !slices.Equal(got, []string{"pj-l-01: [obor answer] newer"}) {
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
	if q := p.out.Queued(); len(q) != 1 {
		t.Fatalf("precondition: queued %d lines, want the answer", len(q))
	}
	a.record(p, event.Record{Task: "T-1", Attempt: 1, Step: event.StepSubmitted, Kind: event.KindFact},
		event.FactData{Merge: true})
	if q := p.out.Queued(); len(q) != 0 {
		t.Errorf("live: queued %d lines, want none", len(q))
	}
	_, r := lineReload(t, a, p, &now)
	if q := r.out.Queued(); len(q) != 0 {
		t.Errorf("replay: queued %+v, want none", q)
	}
}

// fixStep runs the step pickWorking selects for T-1 (the fix job of the attempt): fatal when there is none or it fails.
func fixStep(t *testing.T, h *harness, f *schedScratch) {
	t.Helper()
	p := h.a.p
	h.a.mu.Lock()
	s := h.a.pickWorking(p, *p.folded.loop["T-1"])
	h.a.mu.Unlock()
	if s == nil || !s(context.Background(), &Loop{Workflow: WorkflowOf(f), Agents: f, Typer: f}) {
		t.Fatal("pickWorking: no step, or it failed")
	}
}

// fixJob is the text of T-1's worker's open job (its job_next call).
func fixJob(t *testing.T, h *harness) wire.JobReply {
	t.Helper()
	return nextJob(t, h, wire.JobNextReq{Envelope: wire.Envelope{Root: h.root, Agent: "pj-l-01", Role: "worker",
		CallID: "n1"}, Lane: "L-01"})
}

// obor resume --regate of a worker's question and its task_done interleaved (hooks at their decisions): one
// is recorded, the other refused by the state the first left, and a late submission never brings the gated task back
// from green to submitted. Both orders: resume waiting on the lock while task_done records (task_done wins, the resume
// is refused: submitted), task_done waiting while the resume records (the regate wins, task_done refused: submitted).
func TestResumeSerialisesWithTaskDone(t *testing.T) {
	t.Parallel()
	report := "Summary: x\nSelf-check: x\nSkipped: x\nOpen questions: none."
	for _, c := range []struct {
		name      string
		submitted bool   // task_done wins (else the regate)
		point     string // the hook point that starts the other call
	}{
		{"resume during task_done's decision", true, hookTaskDoneCheck},
		{"task_done during the resume's decision", false, hookResumeLocked},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			h, p, fact := answerSeed(t, reasonAsks+"re-gate, not my diff", "", "", true)
			h.a.mu.Lock()
			if !isAsk(fact) || !p.asks("T-1") || p.folded.loop["T-1"].state != stWorking && p.folded.loop["T-1"].state != stDispatched {
				h.a.mu.Unlock()
				t.Fatalf("precondition: T-1 %s with its question in force", p.folded.loop["T-1"].state)
			}
			h.a.mu.Unlock()
			resume := func() (string, error) {
				return h.a.resume(wire.ResumeReq{Envelope: wire.Envelope{Root: h.root, Role: "lead", Agent: "pj-lead",
					CallID: "r1"}, Task: "T-1", Flag: wire.ResumeRegate})
			}
			submit := func(id string) (string, error) {
				return h.a.taskDone(wire.TaskDoneReq{Envelope: wire.Envelope{Root: h.root, Role: "worker",
					Agent: "pj-l-01", CallID: id}, Lane: "L-01", Task: "T-1", Report: report})
			}
			from := h.lastSeq()
			done := make(chan error, 1)
			var once sync.Once
			waits := make(chan struct{})
			hook := func(point string) {
				switch {
				case point == hookResumeWaits && c.submitted, point == hookTaskDoneWaits && !c.submitted:
					close(waits)
				case point == c.point:
					once.Do(func() {
						if c.submitted { // the resume starts and waits on a.submitMu, task_done records meanwhile
							go func() { _, err := resume(); done <- err }()
							<-waits
							return
						}
						go func() { _, err := submit("s1"); done <- err }()
						<-waits // task_done waits on a.submitMu (the resume holds it), the resume records meanwhile
					})
				}
			}
			h.a.hooked.Store(&hook)
			var first error
			if c.submitted {
				_, first = submit("s1")
			} else {
				_, first = resume()
			}
			var second error
			select {
			case second = <-done:
			case <-time.After(10 * time.Second):
				t.Fatal("the interleaved call never answered")
			}
			none := func(string) {}
			h.a.hooked.Store(&none)
			doneErr, resumeErr := first, second
			if !c.submitted {
				doneErr, resumeErr = second, first
			}
			facts := h.facts(from)
			submits, regates := 0, 0
			for _, f := range facts {
				switch {
				case strings.Contains(f, "submitted"):
					submits++
				case strings.Contains(f, "unblocked{regate}"):
					regates++
				}
			}
			if c.submitted {
				if doneErr != nil || resumeErr == nil || !strings.Contains(resumeErr.Error(), "T-1 is submitted") {
					t.Errorf("task_done %v, resume %v; want submitted, the resume refused by its new state", doneErr, resumeErr)
				}
				if submits != 1 || regates != 0 {
					t.Errorf("facts %q: want one submitted, no unblocked{regate}", facts)
				}
			} else {
				if resumeErr != nil || doneErr == nil || !strings.Contains(doneErr.Error(), "T-1 is submitted, not open for a submission") {
					t.Errorf("resume %v, task_done %v; want the regate, task_done refused by its new state", resumeErr, doneErr)
				}
				if submits != 0 || regates != 1 {
					t.Errorf("facts %q: want one unblocked{regate}, no submitted", facts)
				}
			}
			h.a.mu.Lock()
			mt := *p.folded.loop["T-1"]
			h.a.mu.Unlock()
			if mt.state != stSubmitted || mt.attempt != 1 {
				t.Fatalf("T-1 %s attempt %d after the race; want submitted in attempt 1", mt.state, mt.attempt)
			}
			addRecord(t, h, "T-1", event.StepSubmit, event.KindResult, submitOut{SHA: "s1"}) // the lane's commit, then its gate
			gate := p.folded.loop["T-1"].stepName()
			addRecord(t, h, "T-1", gate, event.KindIntent, gateIn{Kind: "lane", Lane: "L-01", Input: "s1"})
			addRecord(t, h, "T-1", gate, event.KindResult, gateOut{Outcome: outGreen,
				Candidate: port.Candidate{Task: "T-1", SHA: "c", Target: "b", Patch: "p"}})
			if st := p.folded.loop["T-1"].state; st != stGreen {
				t.Fatalf("precondition: T-1 %s after its gate; want green", st)
			}
			if _, err := submit("s2"); err == nil {
				t.Error("a late task_done of a green task was recorded")
			}
			h.a.mu.Lock()
			defer h.a.mu.Unlock()
			if st := p.folded.loop["T-1"].state; st != stGreen {
				t.Errorf("T-1 %s after a late submission; want green (never back to submitted)", st)
			}
		})
	}
}

// The worker's submission can already be decided when the merge loop discovers
// that the owner merged its task outside obor. That terminal fact must win.
func TestTaskDoneCannotReopenConcurrentOutsideMerge(t *testing.T) {
	t.Parallel()
	h, p, _ := answerSeed(t, reasonAsks+"owner deciding", "", "", true)
	addRecord(t, h, "T-1", event.StepWork, event.KindIntent, promptData{Agent: "pj-l-01"})
	addRecord(t, h, "T-1", event.StepDelivered, event.KindFact, event.FactData{Job: "T-1/1/work"})
	hook := func(point string) {
		if point == hookTaskDoneCheck {
			h.a.drive(context.Background(), p, &Loop{Workflow: WorkflowOf(landedScratch{})}, "T-1")
			if st := p.folded.loop["T-1"].state; st != stMerged {
				t.Fatalf("precondition: outside merge left %s", st)
			}
		}
	}
	h.a.hooked.Store(&hook)
	_, err := h.a.taskDone(wire.TaskDoneReq{Envelope: wire.Envelope{Root: h.root, Role: "worker", Agent: "pj-l-01",
		CallID: "late"}, Lane: "L-01", Task: "T-1",
		Report: "Summary: done\nSelf-check: pass\nSkipped: none\nOpen questions: none"})
	if err == nil {
		t.Error("late submission reported success after the task merged")
	}
	if st := p.folded.loop["T-1"].state; st != stMerged {
		t.Errorf("late task_done reopened task as %s", st)
	}
	// The lane may already have been reused before a legacy late fact lands.
	addRecord(t, h, "T-2", event.StepDispatched, event.KindFact, dispatchData{Lane: "L-01", Agent: "pj-l-01"})
	addRecord(t, h, "T-2", event.StepPrepare, event.KindResult, prepareOut{Base: "new-base"})
	if got := p.laneTask("L-01"); got != "T-2" {
		t.Fatalf("precondition: lane holds %q, want T-2", got)
	}
	addRecord(t, h, "T-1", event.StepSubmitted, event.KindFact, submitData{Lane: "L-01", Agent: "pj-l-01"})
	if st := p.folded.loop["T-1"].state; st != stMerged {
		t.Errorf("late submitted fact reopened task as %s", st)
	}
	if got := p.laneTask("L-01"); got != "T-2" {
		t.Errorf("late submission reclaimed reused lane: %q", got)
	}
	p = reloadProj(t, h)
	if st := p.folded.loop["T-1"].state; st != stMerged {
		t.Errorf("replay reopened task as %s", st)
	}
	if got := p.laneTask("L-01"); got != "T-2" {
		t.Errorf("replay reclaimed reused lane: %q", got)
	}
}

// A busy claim on a task refuses every task verb whose state takes it, with busy's why: a drop being recorded
// (p.ended), a step holding it (p.held), its scratch step running (p.running), its next gate intent open. Each verb on
// a working T-1 (resume of its stalled worker, drop, move, submit) and land on a driving one; submit only by a
// drop, since the step holding a working task is the one that sent the worker its job.
func TestBusyRefusesEveryVerb(t *testing.T) {
	t.Parallel()
	claims := []struct {
		name string
		set  func(h *harness, p *proj)
		why  string
	}{
		{"a drop being recorded", func(h *harness, p *proj) { p.ended["T-1"] = true }, "T-1 has a drop being recorded"},
		{"a step holding it", func(h *harness, p *proj) { p.held["T-1"] = "the fix" }, "T-1 has the fix running"},
		{"its scratch step running", func(h *harness, p *proj) {
			p.running = event.Key{Task: "T-1", Attempt: 1, Step: event.StepGate}
		}, "T-1 has gate running"},
		{"a pending gate intent", func(h *harness, p *proj) {
			h.a.mu.Unlock()
			addRecord(h.t, h, "T-1", event.StepGate, event.KindIntent, gateIn{Kind: "lane", Lane: "L-01"})
			h.a.mu.Lock()
		}, "T-1 has gate pending"},
	}
	verbs := map[string]func(h *harness) error{
		"resume": func(h *harness) error {
			_, err := h.a.resume(wire.ResumeReq{Envelope: wire.Envelope{Root: h.root, Role: "lead", CallID: "r1"},
				Task: "T-1", Text: "go on"})
			return err
		},
		"drop": func(h *harness) error {
			_, err := h.a.drop(wire.DropReq{Envelope: wire.Envelope{Root: h.root, Role: "lead", CallID: "d1"},
				Task: "T-1", Why: "why"})
			return err
		},
		"move": func(h *harness) error {
			_, err := h.a.dispatchCall(context.Background(), wire.DispatchReq{Envelope: wire.Envelope{Root: h.root,
				Role: "lead", CallID: "m1"}, Task: "T-1", Types: []string{"sonnet"}})
			return err
		},
		"submit": func(h *harness) error {
			_, err := h.a.taskDone(wire.TaskDoneReq{Envelope: wire.Envelope{Root: h.root, Role: "worker",
				Agent: "pj-l-01", CallID: "s1"}, Lane: "L-01", Task: "T-1",
				Report: "Summary: x\nSelf-check: x\nSkipped: x\nOpen questions: none."})
			return err
		},
		"land": func(h *harness) error {
			_, err := h.a.landCall(wire.LandReq{Envelope: wire.Envelope{Root: h.root, Role: "lead", CallID: "l1"},
				Task: "T-1", Report: "done"})
			return err
		},
	}
	for _, c := range claims {
		for name, call := range verbs {
			if name == "submit" && c.why != "T-1 has a drop being recorded" {
				continue // a working task's step is the one that sent its job: an early task_done (TestEarlyTaskDone)
			}
			t.Run(c.name+"/"+name, func(t *testing.T) {
				t.Parallel()
				h, p, _ := answerSeed(t, reasonStalled, "", "", true)
				h.a.mu.Lock()
				mt := p.folded.loop["T-1"]
				if name == "land" {
					mt.state, mt.drive = stDriving, true
				}
				if st := mt.state; st != stWorking && st != stDispatched && st != stDriving || !mt.may(map[string]verb{
					"resume": vResume, "drop": vDrop, "move": vMove, "submit": vSubmit, "land": vLand}[name]) {
					h.a.mu.Unlock()
					t.Fatalf("precondition: T-1 is %s, which does not take %s", st, name)
				}
				if p.busy("T-1") != "" {
					h.a.mu.Unlock()
					t.Fatalf("precondition: T-1 busy before the claim: %s", p.busy("T-1"))
				}
				c.set(h, p)
				h.a.mu.Unlock()
				err := call(h)
				if err == nil || !strings.Contains(err.Error(), c.why+"; retry when it settles") {
					t.Errorf("%s with %s: %v; want refused %q", name, c.name, err, c.why)
				}
				h.a.mu.Lock()
				defer h.a.mu.Unlock()
				if st := p.folded.loop["T-1"].state; st != mt.state {
					t.Errorf("%s with %s: a refused verb moved T-1 to %s", name, c.name, st)
				}
			})
		}
	}
}

// The BACKLOG deadlock: a worker's Blocked "re-gate, not my diff" is a question in force; obor resume
// --regate gates the lane as it is (fact unblocked{regate}: submitted, the lane submitted afresh), the question ends;
// with no flag the text is the answer (the question ends, the task stays working), and --sendback does not fit.
func TestResumeRegateOfABlockedReport(t *testing.T) {
	t.Parallel()
	seed := func(t *testing.T) (*harness, *proj) {
		h, p, fact := answerSeed(t, reasonAsks+"re-gate, not my diff", "", "", true)
		h.a.mu.Lock()
		defer h.a.mu.Unlock()
		if !isAsk(fact) || !p.asks("T-1") || p.folded.loop["T-1"].state != stDispatched && p.folded.loop["T-1"].state != stWorking {
			t.Fatalf("precondition: T-1 %s with its question in force: %+v", p.folded.loop["T-1"].state, fact)
		}
		if path := p.answerPath(fact); path != `obor resume T-1 "<answer>" | obor resume T-1 --regate` {
			t.Errorf("the question's answers %q; want the answer and --regate", path)
		}
		return h, p
	}
	resume := func(h *harness, id, to, note string) (string, error) {
		return h.a.resume(wire.ResumeReq{Envelope: wire.Envelope{Root: h.root, Role: "lead", Agent: "pj-lead",
			CallID: id}, Task: "T-1", Flag: to, Text: note})
	}
	t.Run("--regate", func(t *testing.T) {
		t.Parallel()
		h, p := seed(t)
		if _, err := resume(h, "x", wire.ResumeSendback, "fix it"); err == nil ||
			!strings.Contains(err.Error(), "--sendback does not fit: obor resume T-1 sends the worker the answer") {
			t.Errorf("--sendback of a question: %v; want refused naming the answer", err)
		}
		reply, err := resume(h, "r1", wire.ResumeRegate, "")
		if want := "RESUMED T-1: regate (-)"; err != nil || reply != want {
			t.Fatalf("resume --regate: %q, %v; want %q", reply, err, want)
		}
		h.a.mu.Lock()
		mt, asks := *p.folded.loop["T-1"], p.asks("T-1")
		h.a.mu.Unlock()
		if mt.state != stSubmitted || mt.input != "" || mt.attempt != 1 {
			t.Errorf("after --regate: %s, input %q, attempt %d; want submitted afresh in attempt 1", mt.state, mt.input, mt.attempt)
		}
		if asks {
			t.Error("the question is still in force after --regate")
		}
		if again, err := h.a.resume(wire.ResumeReq{Envelope: wire.Envelope{Root: h.root, Role: "lead", CallID: "r1"},
			Task: "T-1", Flag: wire.ResumeRegate}); err != nil ||
			again != reply {
			t.Errorf("a repeated call id: %q, %v; want the stored reply", again, err)
		}
	})
	t.Run("the answer", func(t *testing.T) {
		t.Parallel()
		h, p := seed(t)
		reply, err := resume(h, "a1", "", "yes, it is a flake")
		if want := "ANSWERED T-1: obor serve sends pj-l-01 the answer"; err != nil || !strings.HasPrefix(reply, want) {
			t.Fatalf("resume: %q, %v; want %q", reply, err, want)
		}
		h.a.mu.Lock()
		defer h.a.mu.Unlock()
		if mt := p.folded.loop["T-1"]; p.asks("T-1") || mt.attempt != 1 || mt.state == stSubmitted {
			t.Errorf("after the answer: question %v, %s attempt %d; want none, the same attempt, not submitted",
				p.asks("T-1"), mt.state, mt.attempt)
		}
	})
}

// resumeAct by state: each state that takes no resume is refused by its state; a hold's decision reaches the
// integrator (reprompt, its text required); red past K with neither flag is refused naming both, each flag fits; a
// flag that does not fit names what resume does; a driving task's line goes to the drive agent.
func TestResumeActByState(t *testing.T) {
	t.Parallel()
	withLane := func(mt mtask) mtask { mt.task, mt.attempt, mt.lane, mt.agent = "T-1", 1, "L-01", "pj-l-01"; return mt }
	for _, c := range []struct {
		name string
		mt   mtask
		flag string
		act  string // "": refused
		err  string
	}{
		{"held", mtask{state: stAwaiting, needs: heldOf(event.CauseHeld), attemptVars: attemptVars{job: "review"}}, "", wire.ResumeReprompt, ""},
		{"held --sendback", mtask{state: stAwaiting, needs: heldOf(event.CauseHeld), attemptVars: attemptVars{job: "review"}},
			wire.ResumeSendback, "", "--sendback does not fit: obor resume T-1 sends the integrator its job anew"},
		{"red past K", mtask{state: stRed, needs: heldOf(causeRed)}, "", "", "say --regate or --sendback (red: why)"},
		{"red past K --regate", mtask{state: stRed, needs: heldOf(causeRed)}, wire.ResumeRegate, wire.ResumeRegate, ""},
		{"red past K --sendback", mtask{state: stRed, needs: heldOf(causeRed)}, wire.ResumeSendback, actSendback, ""},
		{"sendbacks past K", mtask{state: stAwaiting, needs: closedOf(causeSendbacks), attemptVars: attemptVars{job: "review"}}, "", "",
			"say --regate or --sendback"},
		{"a failed step", mtask{state: stSubmitted, needs: heldOf("submit" + failedSuffix)}, "", wire.ResumeRetry, ""},
		{"a failed step --regate", mtask{state: stSubmitted, needs: heldOf("submit" + failedSuffix)}, wire.ResumeRegate, "",
			"--regate does not fit: obor resume T-1 retries the failed step"},
		{"a gate failure", mtask{state: stSubmitted, needs: closedOf(causeGate)}, "", wire.ResumeRegate, ""},
		{"a gate failure --regate", mtask{state: stSubmitted, needs: closedOf(causeGate)}, wire.ResumeRegate, wire.ResumeRegate, ""},
		{"a gate failure due", mtask{state: stSubmitted, needs: block{cause: causeGate, why: "gate: x"}}, "", "",
			"T-1 is fail; retry when it settles"},
		{"rejected", mtask{state: stRejected, needs: heldOf(causeRejected)}, "", "", "rejected (rejected: why): obor drop T-1 frees its lane"},
		{"green", mtask{state: stGreen}, "", actSendback, ""},
		{"awaiting", mtask{state: stAwaiting, attemptVars: attemptVars{job: "review"}}, "", actSendback, ""},
		{"awaiting --regate", mtask{state: stAwaiting, attemptVars: attemptVars{job: "review"}}, wire.ResumeRegate, "",
			"--regate does not fit: obor resume T-1 sends it back to its worker"},
		{"driving", mtask{state: stDriving, drive: true}, "", actDrive, ""},
		{"driving --sendback", mtask{state: stDriving, drive: true}, wire.ResumeSendback, "", "sends the drive agent the line"},
		{"a landed drive", mtask{state: stSubmitted, drive: true}, "", "", "T-1 is submitted; retry when it settles"},
		{"a drive dispatched", mtask{state: stDispatched, drive: true}, "", "", "the lead cannot answer a drive until it is driving"},
		{"a drive's needs-you", mtask{state: stDispatched, drive: true, needs: heldOf("lane")}, "", wire.ResumeRetry, ""},
		{"working, nothing asked", mtask{state: stWorking}, "", "", "T-1 is working with nothing to resume"},
		{"gating", mtask{state: stSubmitted}, "", "", "T-1 is submitted; retry when it settles"},
		{"dropped", mtask{state: stDropped}, "", "", "resume: T-1 is dropped"},
		{"a keyword flag", mtask{state: stGreen}, wire.ResumeReprompt, "", "--reprompt is no flag of obor resume"},
	} {
		p := &proj{folded: folded{active: map[string][]event.Record{}}}
		act, err := p.resumeAct(withLane(c.mt), c.flag)
		switch {
		case c.act != "" && (err != nil || act != c.act):
			t.Errorf("%s: %q, %v; want %s", c.name, act, err, c.act)
		case c.act == "" && (err == nil || !strings.Contains(err.Error(), c.err)):
			t.Errorf("%s: %q, %v; want refused %q", c.name, act, err, c.err)
		}
	}
	// a hold's decision is required; a gate failure's is not
	p := &proj{}
	if !p.resumeNeedsText(withLane(mtask{state: stAwaiting, needs: heldOf(event.CauseHeld)}), wire.ResumeReprompt) ||
		p.resumeNeedsText(withLane(mtask{state: stSubmitted, needs: closedOf(causeGate)}), wire.ResumeRegate) {
		t.Error("resumeNeedsText: want a hold's decision required, a regate's not")
	}
}

// The lead's sendback (obor resume --sendback) is serve's own transition: from a green
// candidate, an open job or a needs-you of the owner's choice (red past K) the task goes back to its worker as the next
// attempt, its job, candidate and needs-you ended; a needs-you whose cause takes another action is refused naming it,
// any other state by name, with nothing recorded.
func TestLeadSendback(t *testing.T) {
	t.Parallel()
	sub := func(r *concRig) {
		r.add("pq", "T-6", event.StepSubmitted, event.KindFact, submitData{Lane: "L-06", Agent: "pq-l-06"})
		r.add("pq", "T-6", event.StepSubmit, event.KindResult, submitOut{SHA: "s6"})
	}
	gate := func(r *concRig, outcome string) {
		r.add("pq", "T-6", event.StepGate, event.KindIntent, gateIn{Kind: port.InputLane, Lane: "L-06", Input: "s6"})
		r.add("pq", "T-6", event.StepGate, event.KindResult, gateOut{Outcome: outcome, Line: "gate " + outcome,
			Candidate: port.Candidate{Task: "T-6", SHA: "c-T-6", Target: "b", Patch: "p6"}})
	}
	for _, c := range []struct {
		name  string
		prep  func(r *concRig)
		state string // the precondition
		reply string // "": refused
		err   string
		facts int // facts in force for T-6 before
	}{
		{"green", func(r *concRig) { sub(r); gate(r, outGreen) }, stGreen, "SENDBACK T-6 (attempt 1, green)", "", 0},
		{"awaiting", func(r *concRig) {
			sub(r)
			gate(r, outGreen)
			r.add("pq", "T-6", event.StepReview, event.KindIntent, promptData{Agent: "pq-int", Job: "T-6/1/review", Patch: "p6"})
		}, stAwaiting, "SENDBACK T-6 (job T-6/1/review)", "", 0},
		{"resolving", func(r *concRig) {
			sub(r)
			gate(r, outConflict)
			r.add("pq", "T-6", event.StepResolve, event.KindIntent, promptData{Agent: "pq-int", Job: "T-6/1/resolve"})
		}, stResolving, "SENDBACK T-6 (job T-6/1/resolve)", "", 0},
		{"needs-you after a red", func(r *concRig) {
			sub(r)
			gate(r, outRed)
			r.add("pq", "T-6", event.StepNeedsYou, event.KindFact, event.FactData{Lane: "L-06", Merge: true, Cause: causeRed,
				Reason: "gate red after 2 sendbacks"})
		}, wire.PhaseNeedsYou, "SENDBACK T-6 (attempt 1, needs-you)", "", 1},
		{"needs-you after a hold", func(r *concRig) {
			sub(r)
			gate(r, outGreen)
			r.add("pq", "T-6", event.StepReview, event.KindIntent, promptData{Agent: "pq-int", Job: "T-6/1/review", Patch: "p6"})
			r.add("pq", "T-6", event.StepNeedsYou, event.KindFact, event.FactData{Lane: "L-06", Merge: true, Cause: event.CauseHeld,
				Reason: "held: ask the owner", Job: "T-6/1/review"})
		}, wire.PhaseNeedsYou, "", "resume T-6: --sendback does not fit: obor resume T-6 sends the integrator its job anew", 1},
		{"needs-you after a gate fail", func(r *concRig) {
			sub(r)
			gate(r, outFail)
			r.add("pq", "T-6", event.StepNeedsYou, event.KindFact, event.FactData{Lane: "L-06", Merge: true, Cause: causeGate,
				Reason: "gate: gate fail"})
		}, wire.PhaseNeedsYou, "", "resume T-6: --sendback does not fit: obor resume T-6 gates it again", 1},
		{"needs-you with no lane", func(r *concRig) { // a waiting task: no lane, no worker to send the task to
			r.add("pq", "T-6", event.StepWaiting, event.KindFact, dispatchData{After: []string{"T-7"}})
			r.add("pq", "T-6", event.StepNeedsYou, event.KindFact, event.FactData{Merge: true, Cause: causeAfter,
				Reason: "after: T-6 waits for T-7, which is dropped"})
		}, wire.PhaseNeedsYou, "", "resume T-6: --sendback does not fit: obor resume T-6 retries the failed step", 1},
		{"accepted", func(r *concRig) {
			sub(r)
			gate(r, outGreen)
			r.add("pq", "T-6", event.StepReview, event.KindIntent, promptData{Agent: "pq-int", Job: "T-6/1/review", Patch: "p6"})
			r.add("pq", "T-6", verdictStep(wire.MethodAccept, "a1"), event.KindResult, verdictResult{Job: "T-6/1/review", Patch: "p6"})
		}, stAccepted, "", "resume: T-6 is accepted; retry when it settles", 0},
		{"submitted", sub, stSubmitted, "", "resume: T-6 is submitted; retry when it settles", 0},
		{"no task", func(*concRig) {}, "", "", "resume: T-6 is not open in obor serve", 0},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			r := newConcRig(t)
			c.prep(r)
			if c.state != "" && (r.state("pq", "T-6") != c.state || r.factsOf("pq", "T-6") != c.facts) {
				t.Fatalf("precondition: T-6 %s with %d facts; want %s with %d", r.state("pq", "T-6"),
					r.factsOf("pq", "T-6"), c.state, c.facts)
			}
			got, err := r.leadSendback("pq", "T-6", "s1")
			if c.reply == "" {
				if err == nil || err.Error() != c.err {
					t.Fatalf("sendback: %q, %v; want the refusal %q", got, err, c.err)
				}
				if c.state != "" && (r.state("pq", "T-6") != c.state || r.attempt("pq", "T-6") != 1) {
					t.Errorf("a refused sendback moved T-6 to %s (attempt %d)", r.state("pq", "T-6"), r.attempt("pq", "T-6"))
				}
				if n := r.count("pq", "T-6", event.KindResult, verdictStep(wire.MethodSendback, "s1")); n != 0 {
					t.Errorf("a refused sendback recorded %d results", n)
				}
				return
			}
			if err != nil || got != c.reply {
				t.Fatalf("sendback: %q, %v; want %q", got, err, c.reply)
			}
			if st, n := r.state("pq", "T-6"), r.attempt("pq", "T-6"); st != stWorking || n != 2 {
				t.Errorf("after the sendback T-6 is %s (attempt %d); want working, attempt 2", st, n)
			}
			if n := r.factsOf("pq", "T-6"); n != 0 {
				t.Errorf("the needs-you is still in force: %d facts", n)
			}
			_, err = r.app("pq").verdict(context.Background(),
				wire.VerdictReq{Envelope: wire.Envelope{Root: r.roots["pq"], Role: "integrator", Agent: "pq-int",
					CallID: "v1"}, Verb: wire.MethodAccept, Task: "T-6", Job: "T-6/1/review", Patch: "p6"})
			if err == nil {
				t.Error("the old review is still accepted after the sendback")
			}
			if again, err := r.leadSendback("pq", "T-6", "s1"); err != nil || again != c.reply {
				t.Errorf("a repeated call id: %q, %v; want the stored reply", again, err)
			}

			// A replay keeps the next attempt, and the stored reply.
			r.app("pq").closeLog() // the first serve ends: the replay is the next one
			b := newTestApp(Options{Root: r.roots["pq"], Now: r.a.opt.Now}, &fakePanes{}, &fakeLanes{})
			b.loadProject()
			p := b.p
			if mt := p.folded.loop["T-6"]; mt == nil || mt.state != stWorking || mt.attempt != 2 || mt.needs.fact.Seq != 0 {
				t.Errorf("replay: %+v; want T-6 working, attempt 2, no needs-you", mt)
			}
			if got := p.folded.calls[verdictStep(wire.MethodSendback, "s1")]; got != c.reply {
				t.Errorf("replay: stored reply %q", got)
			}
		})
	}
}

// The lead cannot type a change into the worker's pane. From a needs-you of the owner's choice (sendbacks past
// K, a red gate; a hold takes the lead's decision, not a sendback) obor resume --sendback ends it and starts the next attempt, the worker's
// fix job carrying the lead's text; the lead's sendback does not increment sendbacks (K counts the gate's and the integrator's).
func TestLeadSendbackFromNeedsYou(t *testing.T) {
	t.Parallel()
	const full = "Summary: done.\nSelf-check: ok.\nSkipped: none."
	for _, c := range []struct {
		name      string
		cause     string
		report    string
		sendbacks int
	}{
		{"sendbacks", causeSendbacks, full, K},
		{"red", causeRed, full, K},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			h, f := seedQuestions(t, c.report)
			if mt := pickGreenStep(t, h, f, false); mt.state != stAwaiting {
				t.Fatalf("precondition: T-1 %s; want the review job out", mt.state)
			}
			h.a.mu.Lock()
			h.a.p.folded.loop["T-1"].sendbacks = c.sendbacks
			h.a.mu.Unlock()
			addRecord(t, h, "T-1", event.StepNeedsYou, event.KindFact, event.FactData{Lane: "L-01", Merge: true, Cause: c.cause,
				Reason: c.cause + ": the integrator's limit"})
			if mt := h.a.p.folded.loop["T-1"]; !mt.onHold() || mt.needs.fact.Fact().Cause != c.cause {
				t.Fatalf("precondition: T-1 %s (%s); want the %s needs-you", mt.state, mt.needs.fact.Fact().Cause, c.cause)
			}
			const why = "use the config store; drop the extra test"
			reply, err := h.a.resume(wire.ResumeReq{Envelope: wire.Envelope{Root: h.root, Agent: "pj-lead",
				Role: "lead", CallID: "s1"}, Task: "T-1", Flag: wire.ResumeSendback, Text: why})
			if err != nil || !strings.HasPrefix(reply, "SENDBACK T-1") {
				t.Fatalf("sendback: %q, %v", reply, err)
			}
			mt := h.a.p.folded.loop["T-1"]
			if mt.state != stWorking || mt.attempt != 2 || mt.sendbacks != c.sendbacks || mt.needs.fact.Seq != 0 {
				t.Errorf("after the sendback: T-1 %s, attempt %d, sendbacks %d, needs %d; want working, attempt 2, %d, none",
					mt.state, mt.attempt, mt.sendbacks, mt.needs.fact.Seq, c.sendbacks)
			}
			fixStep(t, h, f)
			jr := fixJob(t, h)
			if jr.Job != "T-1/2/fix" || !strings.Contains(jr.Text, why) {
				t.Errorf("the fix job %s does not carry the lead's text %q:\n%s", jr.Job, why, jr.Text)
			}
		})
	}
}

// K still counts the integrator's sendbacks: one more below K starts the next attempt and increments the count;
// at K a further sendback goes to the owner, no attempt.
func TestIntegratorSendbackStillCounts(t *testing.T) {
	t.Parallel()
	const full = "Summary: done.\nSelf-check: ok.\nSkipped: none."
	for _, c := range []struct {
		name      string
		sendbacks int
		state     string
		attempt   int
	}{
		{"below K", K - 1, stWorking, 2},
		{"at K", K, "fail", 1}, // a due block: the owner decides
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			h, f := seedQuestions(t, full)
			if mt := pickGreenStep(t, h, f, false); mt.state != stAwaiting {
				t.Fatalf("precondition: T-1 %s; want the review job out", mt.state)
			}
			h.a.mu.Lock()
			h.a.p.folded.loop["T-1"].sendbacks = c.sendbacks
			h.a.mu.Unlock()
			reply, err := h.a.verdict(context.Background(), wire.VerdictReq{Envelope: wire.Envelope{Root: h.root,
				Agent: "pj-int", Role: "integrator", CallID: "s1"}, Verb: wire.MethodSendback, Job: "T-1/1/review",
				Task: "T-1", Note: "fix it"})
			if err != nil || !strings.HasPrefix(reply, "SENDBACK T-1 (job T-1/1/review)") {
				t.Fatalf("sendback: %q, %v", reply, err)
			}
			mt := h.a.p.folded.loop["T-1"]
			if mt.shown() != c.state || mt.attempt != c.attempt || mt.sendbacks != K {
				t.Errorf("after the integrator's sendback: T-1 %s, attempt %d, sendbacks %d; want %s, attempt %d, %d",
					mt.shown(), mt.attempt, mt.sendbacks, c.state, c.attempt, K)
			}
			if c.state == "fail" && (!mt.needs.due() || mt.needs.cause != causeSendbacks || mt.state != stAwaiting) {
				t.Errorf("at K: %s, block %+v; want a due block of %q on awaiting-accept (the owner decides)", mt.state,
					mt.needs, causeSendbacks)
			}
			if n := countStep(t, h, event.StepFix); n != 0 {
				t.Errorf("%d fix jobs; want none before the loop sends one", n)
			}
		})
	}
}

// The reviewer's probe: the lead sends back T-4 with its review job out; the integrator's accept of the old
// candidate is refused, nothing is published, and the worker gets its fix job from serve.
func TestLeadSendbackEndsTheReview(t *testing.T) {
	t.Parallel()
	r := newConcRig(t)
	if r.state("pr", "T-4") != stAwaiting {
		t.Fatalf("precondition: T-4 %s; want awaiting-accept", r.state("pr", "T-4"))
	}
	if got, err := r.leadSendback("pr", "T-4", "s1"); err != nil || got != "SENDBACK T-4 (job T-4/1/review)" {
		t.Fatalf("sendback: %q, %v", got, err)
	}
	a, p := r.app("pr"), r.proj("pr")
	a.mu.Lock()
	p.setLanes([]port.Lane{{Name: "L-04", Task: "T-4", Agent: "pr-l-04", State: "working"}})
	a.mu.Unlock()
	_, err := r.app("pr").verdict(context.Background(), wire.VerdictReq{Envelope: wire.Envelope{Root: r.roots["pr"],
		Agent: "pr-int", Role: "integrator", CallID: "old-review"}, Verb: wire.MethodAccept, Task: "T-4",
		Job: "T-4/1/review", Patch: "p"})
	if err == nil || !strings.Contains(err.Error(), "no job open for T-4") {
		t.Errorf("accept of the old candidate after the sendback: %v; want refused", err)
	}
	if st := r.state("pr", "T-4"); st != stWorking {
		t.Errorf("T-4 is %s; want working", st)
	}

	// The loop hands the task back: the fix job, never a publish.
	a.mu.Lock()
	p.folded.loop["T-4"].agent = "pr-l-04"
	a.mu.Unlock()
	r.worker("pr", "w3:p4", "pr-l-04")
	r.run()
	defer r.stop()
	r.waitFor("T-4's fix job", func() bool { return r.count("pr", "T-4", event.KindIntent, event.StepFix) == 1 })
	if n := r.fakes["pr"].count(r.fakes["pr"].pubs, "T-4"); n != 0 || r.count("pr", "T-4", event.KindIntent, event.StepPublish) != 0 {
		t.Errorf("the old candidate was published (%d)", n)
	}
}

// A sendback while the task's gate runs is refused until it settles; the worker and the integrator may not send a
// task back as the lead, and a sendback says why.
func TestLeadSendbackRefusals(t *testing.T) {
	t.Parallel()
	r := newConcRig(t)
	r.run()
	defer r.stop()
	if task := r.entered("pj"); task != "T-1" {
		t.Fatalf("pj's first gate: %s", task)
	}
	_, err := r.a.resume(wire.ResumeReq{Envelope: wire.Envelope{Root: r.roots["pj"], Agent: "pj-lead", Role: "lead",
		CallID: "g1"}, Task: "T-1", Flag: wire.ResumeSendback, Text: "why"})
	if err == nil || !strings.Contains(err.Error(), "retry when it settles") {
		t.Errorf("sendback while the gate runs: %v", err)
	}
	if st := r.state("pj", "T-1"); st != stSubmitted {
		t.Errorf("a refused sendback moved T-1 to %s", st)
	}
	close(r.fakes["pj"].release)

	r.waitFor("T-4 awaiting with no step held", func() bool {
		a := r.app("pr")
		a.mu.Lock()
		defer a.mu.Unlock()
		p := a.p
		return p.folded.loop["T-4"].state == stAwaiting && p.held["T-4"] == ""
	})
	_, err = r.app("pr").resume(wire.ResumeReq{Envelope: wire.Envelope{Root: r.roots["pr"], Agent: "pr-l-04",
		Role: "worker", CallID: "w1"}, Task: "T-4", Flag: wire.ResumeSendback, Text: "why"})
	if err == nil || !strings.Contains(err.Error(), "the lead or the owner resumes a task") {
		t.Errorf("sendback by a worker: %v", err)
	}
	_, err = r.app("pr").resume(wire.ResumeReq{Envelope: wire.Envelope{Root: r.roots["pr"], Agent: "pr-lead",
		Role: "lead", CallID: "w2"}, Task: "T-4", Flag: wire.ResumeSendback, Text: " "})
	if err == nil || !strings.Contains(err.Error(), `say the text: obor resume T-4 "<text>" sends it back to its worker`) {
		t.Errorf("sendback without a why: %v", err)
	}
	// the socket's sendback is the integrator's only (its MCP verb): the lead's is refused there
	_, err = r.app("pr").verdict(context.Background(), wire.VerdictReq{Envelope: wire.Envelope{Root: r.roots["pr"],
		Agent: "pr-lead", Role: "lead", CallID: "w3"}, Verb: wire.MethodSendback, Task: "T-4", Note: "why"})
	if err == nil || !strings.Contains(err.Error(), "only the integrator (pr-int) gives verdicts") {
		t.Errorf("the lead's socket sendback: %v; want the integrator's only", err)
	}
	if st := r.state("pr", "T-4"); st != stAwaiting {
		t.Errorf("refused sendbacks moved T-4 to %s", st)
	}
}
