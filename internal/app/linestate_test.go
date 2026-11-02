package app

import (
	"context"
	"obor/internal/app/event"
	"obor/internal/app/port"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
)

// T-748 (RV-90): a queued line's delivery as explicit states. The case → test table: each state a line can be in
// while queued (waiting, sending, sending with its question settled) × each event (delivered, a refusal before a byte,
// an error after a byte, its question settled, its task dropped, replay), one subtest per row, run through the log's
// records on T-1's answer to pj-o-01 (busyAnswered). Sent and retired lines leave the outbox: next names which.

// answerSeq is the seq of h's one queued line (precondition: exactly one).
func answerSeq(t *testing.T, h *harness) int64 {
	t.Helper()
	h.a.mu.Lock()
	defer h.a.mu.Unlock()
	qs := h.proj().queuedLines()
	if len(qs) != 1 || !qs[0].answer {
		t.Fatalf("precondition: one queued answer, got %d lines", len(qs))
	}
	return qs[0].seq
}

// lineAt is the state of line seq in p (ok false: it left the outbox).
func lineAt(h *harness, p *proj, seq int64) (lineState, bool) {
	h.a.mu.Lock()
	defer h.a.mu.Unlock()
	q := p.out.msgs[seq]
	if q == nil {
		return 0, false
	}
	return q.state, true
}

// deliverKey is line seq's delivery key step.
func deliverKey(seq int64) string { return stepDeliver + strconv.FormatInt(seq, 10) }

// lineRec records r with data in h's project (fatal when the log refuses it).
func lineRec(t *testing.T, h *harness, r event.Record, data any) {
	t.Helper()
	if !h.a.record(h.proj(), r, data) {
		t.Fatalf("%s not recorded", r.Step)
	}
}

// claimIntent records line seq's delivery intent: its delivery began (sending).
func claimIntent(t *testing.T, h *harness, seq int64) {
	lineRec(t, h, event.Record{Task: event.MsgTask, Attempt: 1, Step: deliverKey(seq), Kind: event.KindIntent,
		Class: event.ClassPrompt}, lineData{To: "pj-o-01"})
}

// submitT1 records T-1's submission: its question is settled another way.
func submitT1(t *testing.T, h *harness) {
	lineRec(t, h, event.Record{Task: "T-1", Attempt: 1, Step: event.StepSubmitted, Kind: event.KindFact},
		event.FactData{Lane: "O-01", Agent: "pj-o-01", Merge: true})
}

// answerIn is busyAnswered's answer moved into state from, by the log's records.
func answerIn(t *testing.T, from lineState) (*harness, int64) {
	t.Helper()
	h := busyAnswered(t)
	seq := answerSeq(t, h)
	if from != lineWaiting {
		claimIntent(t, h, seq)
	}
	if from == lineSendingSettled {
		submitT1(t, h)
	}
	if s, ok := lineAt(h, h.proj(), seq); !ok || s != from {
		t.Fatalf("precondition: the answer in state %d (queued %v), want %d", s, ok, from)
	}
	return h, seq
}

// replayed is h's project loaded again from its log (an orderly restart).
func replayed(t *testing.T, h *harness) *proj {
	t.Helper()
	h.a.closeLog()
	a := New(Options{Root: h.root, Now: func() time.Time { return h.now }}, h.panes, h.lanes)
	p, err := a.load(h.root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { p.log.Close() })
	return p
}

func TestLineStateTable(t *testing.T) {
	t.Parallel()
	events := map[string]struct {
		e  lineEvent // the event next sees ("replay": none)
		do func(t *testing.T, h *harness, seq int64)
	}{
		"delivered": {lineDelivered, func(t *testing.T, h *harness, seq int64) {
			lineRec(t, h, event.Record{Task: event.MsgTask, Attempt: 1, Step: deliverKey(seq), Kind: event.KindResult}, "sent")
		}},
		"refused before a byte": {lineRefused, func(t *testing.T, h *harness, seq int64) {
			lineRec(t, h, event.Record{Task: event.MsgTask, Step: event.StepFailed, Kind: event.KindFact},
				event.FactData{Machine: Machine, Reason: deliverKey(seq) + ": dial: connection refused", Step: deliverKey(seq),
					Via: viaInbox})
		}},
		"error after a byte": {lineDelivered, func(t *testing.T, h *harness, seq int64) {
			lineRec(t, h, event.Record{Task: event.MsgTask, Attempt: 1, Step: deliverKey(seq), Kind: event.KindResult},
				"sent: broken pipe")
		}},
		"settled": {lineSettled, func(t *testing.T, h *harness, _ int64) { submitT1(t, h) }},
		"dropped": {lineSettled, func(t *testing.T, h *harness, _ int64) {
			lineRec(t, h, event.Record{Task: "T-1", Attempt: 1, Step: event.StepDropped, Kind: event.KindFact},
				event.FactData{Lane: "O-01", Merge: true})
		}},
	}
	const gone = lineState(255) // the line left the outbox (sent or retired: next says which)
	for _, row := range []struct {
		from  lineState
		event string
		want  lineState
	}{
		{lineWaiting, "delivered", lineSent},
		{lineWaiting, "refused before a byte", lineWaiting},
		{lineWaiting, "error after a byte", lineSent},
		{lineWaiting, "settled", lineRetired},
		{lineWaiting, "dropped", lineRetired},
		{lineWaiting, "replay", lineWaiting},
		{lineSending, "delivered", lineSent},
		{lineSending, "refused before a byte", lineWaiting},
		{lineSending, "error after a byte", lineSent},
		{lineSending, "settled", lineSendingSettled},
		{lineSending, "dropped", lineSendingSettled},
		{lineSending, "replay", lineSending},
		{lineSendingSettled, "delivered", lineSent},
		{lineSendingSettled, "refused before a byte", lineRetired}, // RV-90 #1's race: settled, then refused
		{lineSendingSettled, "error after a byte", lineSent},
		{lineSendingSettled, "settled", lineSendingSettled},
		{lineSendingSettled, "dropped", lineSendingSettled},
		{lineSendingSettled, "replay", lineSendingSettled},
	} {
		name := []string{"waiting", "sending", "sending-settled"}[row.from] + "/" + row.event
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			h, seq := answerIn(t, row.from)
			if row.event == "replay" {
				s, live := lineAt(h, h.proj(), seq)
				if r, ok := lineAt(h, replayed(t, h), seq); !live || !ok || s != row.want || r != row.want {
					t.Errorf("live %d (queued %v), replayed %d (queued %v); want %d both", s, live, r, ok, row.want)
				}
				return
			}
			ev := events[row.event]
			if got := row.from.next(ev.e); got != row.want {
				t.Errorf("next: %d, want %d", got, row.want)
			}
			ev.do(t, h, seq)
			want := row.want
			if want == lineSent || want == lineRetired {
				want = gone
			}
			check := func(when string, p *proj) {
				got, ok := lineAt(h, p, seq)
				if !ok {
					got = gone
				}
				if got != want {
					t.Errorf("%s: state %d, want %d (%d: left the outbox)", when, got, want, gone)
				}
			}
			check("live", h.proj())
			check("replay", replayed(t, h))
		})
	}
}

// Ended is ended: nothing moves a sent or retired line.
func TestLineStateEndedStays(t *testing.T) {
	t.Parallel()
	for _, s := range []lineState{lineSent, lineRetired} {
		for _, e := range []lineEvent{lineClaimed, lineDelivered, lineRefused, lineSettled, lineClosed} {
			if got := s.next(e); got != s {
				t.Errorf("%d on %d: %d, want it to stay", s, e, got)
			}
		}
	}
}

// RV-90 #1 (probe TestRV90RefusedAndSettled): a post refused before a byte, then the question settled (task_done):
// the answer retires, the next idle sends nothing, and replay keeps it retired.
func TestRV90RefusedAndSettled(t *testing.T) {
	t.Parallel()
	h := staleHerdr(t)
	in := &linerSpy{refuse: true}
	fx := &Loop{Inbox: in, Typer: &linerSpy{}}
	h.a.deliver(context.Background(), h.proj(), fx)
	seq := answerSeq(t, h)
	if s, _ := lineAt(h, h.proj(), seq); len(in.calls) != 1 || s != lineWaiting {
		t.Fatalf("precondition: one refused post, the answer waiting; posts %q, state %d", in.calls, s)
	}
	submitT1(t, h)
	if q := h.lines(); len(q) != 0 {
		t.Errorf("queued %q after task_done; want the refused answer retired", q)
	}
	in.refuse = false
	typer := &linerSpy{}
	h.a.deliver(context.Background(), h.proj(), &Loop{Inbox: in, Typer: typer})
	if len(in.calls) != 1 || len(typer.calls) != 0 {
		t.Errorf("posts %q, typed %q at the next idle; want the settled answer never sent", in.calls, typer.calls)
	}
	if _, ok := lineAt(h, replayed(t, h), seq); ok {
		t.Error("replay brought the retired answer back")
	}
}

// RV-90 #2 (probe TestRV90RefusedReason): a refused post keeps its reason (obor status, the needs-you), and the next
// attempt to the idle identified worker types it instead of dialling the dead socket again.
func TestRV90RefusedReason(t *testing.T) {
	t.Parallel()
	h := staleHerdr(t)
	in := &linerSpy{refuse: true}
	h.a.deliver(context.Background(), h.proj(), &Loop{Inbox: in})
	if len(in.calls) != 1 {
		t.Fatalf("precondition: one refused post, got %q", in.calls)
	}
	const why = "its delivery was refused before a byte went out: agent_not_ready: no typing"
	if ms := h.a.Status().Project.Messages; len(ms) != 1 || ms[0].Held != why {
		t.Errorf("status lines %+v; want the answer held %q", ms, why)
	}
	h.now = h.now.Add(msgWait + time.Minute) // nothing types here: the post is tried again, refused again
	h.a.deliver(context.Background(), h.proj(), &Loop{Inbox: in})
	var reasons []string
	for _, f := range h.a.Status().Project.Facts {
		if f.Task == event.MsgTask && f.Step == event.StepNeedsYou {
			reasons = append(reasons, f.Fact().Reason)
		}
	}
	if len(in.calls) != 2 || len(reasons) != 1 || !strings.HasSuffix(reasons[0], "(held: "+why+")") {
		t.Errorf("posts %q, needs-you %q; want a second refused post and the needs-you naming its refusal", in.calls,
			reasons)
	}
	typer := &linerSpy{}
	h.a.deliver(context.Background(), h.proj(), &Loop{Inbox: in, Typer: typer})
	if len(in.calls) != 2 || !slices.Equal(typer.calls, []string{"w1:p1: [obor answer] move the helpers down"}) ||
		len(h.lines()) != 0 {
		t.Errorf("posts %q, typed %q, queued %q; want the answer typed into w1:p1 after the refused post", in.calls,
			typer.calls, h.lines())
	}
}

// The typed fallback re-checks its target: after a refused post, a worker that is busy now gets nothing typed (held
// busy); idle again, the line is typed; replay keeps the refusal, so the restarted pass types it too.
func TestRefusedPostTypedOnlyWhenIdle(t *testing.T) {
	t.Parallel()
	h := staleHerdr(t)
	in := &linerSpy{refuse: true}
	h.a.deliver(context.Background(), h.proj(), &Loop{Inbox: in})
	seq := answerSeq(t, h)
	r := replayed(t, h)
	h.a.mu.Lock()
	rq := r.out.msgs[seq]
	h.a.mu.Unlock()
	if rq == nil || !rq.refusedPost || rq.state != lineWaiting || !strings.Contains(rq.refused, "agent_not_ready") {
		t.Errorf("replayed line %+v; want it waiting with its refused post and reason", rq)
	}
	h2 := staleHerdr(t)
	in2 := &linerSpy{refuse: true}
	h2.a.deliver(context.Background(), h2.proj(), &Loop{Inbox: in2})
	h2.status("w1:p1", func(p *port.Pane) { p.Status = "working" })
	typer := &linerSpy{}
	h2.a.deliver(context.Background(), h2.proj(), &Loop{Inbox: in2, Typer: typer})
	if len(in2.calls) != 1 || len(typer.calls) != 0 || len(h2.lines()) != 1 {
		t.Fatalf("busy: posts %q, typed %q; want nothing sent to the working worker", in2.calls, typer.calls)
	}
	h2.status("w1:p1", func(p *port.Pane) { p.Status = "idle" })
	h2.a.deliver(context.Background(), h2.proj(), &Loop{Inbox: in2, Typer: typer})
	if len(in2.calls) != 1 || len(typer.calls) != 1 || len(h2.lines()) != 0 {
		t.Errorf("idle: posts %q, typed %q, queued %q; want it typed once", in2.calls, typer.calls, h2.lines())
	}
}

// A lead line's refused post keeps its reason too (obor status), and is posted again, never typed (T-748).
func TestLeadLineRefusedReason(t *testing.T) {
	t.Parallel()
	now := time.Now()
	a, p := lineApp(t, lineRoot(t), &now)
	leadSends(t, a, p, 1)
	l := &linerSpy{refuse: true}
	a.deliver(context.Background(), p, linerLoop(l))
	if len(l.calls) != 1 {
		t.Fatalf("precondition: one refused post, got %q", l.calls)
	}
	ms := a.Status().Project.Messages
	if len(ms) != 1 || !strings.Contains(ms[0].Held, "refused before a byte went out: agent_not_ready") {
		t.Errorf("status lines %+v; want the lead line held with its refusal", ms)
	}
	l.refuse = false
	a.deliver(context.Background(), p, linerLoop(l))
	if len(l.calls) != 2 || !strings.HasPrefix(l.calls[1], "pj-lead: ") || len(p.queuedLines()) != 0 {
		t.Errorf("calls %q; want the lead line posted again to pj-lead (not typed into its pane)", l.calls)
	}
}

// RV-90 #3 (probe TestRV90Drop): obor drop retires the task's undelivered answer; the lane's worker idle again gets
// nothing. A reject retires it the same way.
func TestRV90Drop(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		rec  event.Record
		data any
	}{
		{"drop", event.Record{Task: "T-1", Attempt: 1, Step: event.StepDropped, Kind: event.KindFact},
			event.FactData{Lane: "O-01", Merge: true}},
		{"reject", event.Record{Task: "T-1", Attempt: 1, Step: "reject.r1", Kind: event.KindResult},
			verdictResult{Job: "T-1/1/review", Reply: "REJECT T-1"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			h := busyAnswered(t)
			lineRec(t, h, tc.rec, tc.data)
			if q := h.lines(); len(q) != 0 {
				t.Errorf("queued %q; want the answer retired", q)
			}
			h.status("w1:p1", func(p *port.Pane) { p.Status = "idle" })
			in, typer := &kindSpy{}, &linerSpy{}
			h.a.deliver(context.Background(), h.proj(), &Loop{Inbox: in, Typer: typer})
			if len(in.calls) != 0 || len(typer.calls) != 0 {
				t.Errorf("posted %q, typed %q; want nothing sent to the lane's worker", in.calls, typer.calls)
			}
		})
	}
}

// A line settled while its delivery was open (a crash cut it: its intent pending, no result) is never sent again:
// the next pass closes its delivery unsent (result retired), live and on replay.
func TestSettledOpenDeliveryClosedUnsent(t *testing.T) {
	t.Parallel()
	h, seq := answerIn(t, lineSendingSettled)
	h.status("w1:p1", func(p *port.Pane) { p.Status = "idle" })
	in, typer := &kindSpy{}, &linerSpy{}
	h.a.deliver(context.Background(), h.proj(), &Loop{Inbox: in, Typer: typer})
	if len(in.calls) != 0 || len(typer.calls) != 0 {
		t.Errorf("posted %q, typed %q; want the settled answer not sent again", in.calls, typer.calls)
	}
	if _, ok := lineAt(h, h.proj(), seq); ok {
		t.Error("the settled line is still queued after the pass")
	}
	closed := false
	for _, r := range h.records() {
		closed = closed || r.Kind == event.KindResult && r.Step == deliverKey(seq) && strings.Contains(string(r.Data), retiredResult)
	}
	if !closed {
		t.Error("no retired result closes its delivery")
	}
	if _, ok := lineAt(h, replayed(t, h), seq); ok {
		t.Error("replay brought the closed line back")
	}
}
