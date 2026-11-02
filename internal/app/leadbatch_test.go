package app

import (
	"context"
	"strconv"
	"strings"
	"testing"
	"time"
)

// leadSends queues n obor send lines "[obor other] m<i>" for the lead.
func leadSends(t *testing.T, a *App, p *proj, n int) {
	t.Helper()
	for i := 1; i <= n; i++ {
		id := strconv.Itoa(i)
		if _, err := a.sendCall(Call{Root: p.root, CallID: "s" + id, From: "other", Note: "m" + id, Role: "lead"}); err != nil {
			t.Fatal(err)
		}
	}
}

// delivered counts the delivery results recorded for the lead's lines in p's log.
func deliveredResults(t *testing.T, p *proj) int {
	t.Helper()
	recs, err := ReadLog(p.root)
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, r := range recs {
		if r.Kind == KindResult && r.Task == MsgTask && strings.HasPrefix(r.Step, stepDeliver) {
			n++
		}
	}
	return n
}

// TestLeadLinesOnePost (T-651): the lines queued for the lead go out as one post, one per row in order, each with
// its delivery fact; a worker's line stays its own post.
func TestLeadLinesOnePost(t *testing.T) {
	t.Parallel()
	now := time.Now()
	a, p := lineApp(t, lineRoot(t), &now)
	leadSends(t, a, p, 3)
	if _, err := a.briefCall(Call{Root: p.root, CallID: "b1", To: "pj-rev", Note: "check", Role: "lead"}); err != nil {
		t.Fatal(err)
	}
	if n := len(p.queuedLines()); n != 4 {
		t.Fatalf("precondition: four lines queued, got %d", n)
	}
	l := &linerSpy{}
	a.deliver(context.Background(), p, linerLoop(l))
	if len(l.calls) != 2 || l.calls[1] != "w:rev: [obor owner] check" || !oneLeadPost(l.calls[0]) {
		t.Errorf("posted %q, want one lead post of m1..m3 in order, then the brief", l.calls)
	}
	if n := len(p.queuedLines()); n != 0 {
		t.Errorf("%d lines still queued", n)
	}
	if n := deliveredResults(t, p); n != 4 {
		t.Errorf("%d delivery results, want 4 (three lead lines, one brief)", n)
	}
	a.deliver(context.Background(), p, linerLoop(l))
	if len(l.calls) != 2 {
		t.Errorf("delivered again: %q", l.calls)
	}
}

// TestLeadLinesBatchReplay: a restart finds every line of the batch delivered.
func TestLeadLinesBatchReplay(t *testing.T) {
	t.Parallel()
	now := time.Now()
	a, p := lineApp(t, lineRoot(t), &now)
	leadSends(t, a, p, 3)
	l := &linerSpy{}
	a.deliver(context.Background(), p, linerLoop(l))
	if len(l.calls) != 1 {
		t.Fatalf("precondition: one post, got %q", l.calls)
	}
	b, q := lineReload(t, a, p, &now)
	if n := len(q.queuedLines()); n != 0 {
		t.Errorf("after replay %d lines queued, want none", n)
	}
	b.deliver(context.Background(), q, linerLoop(l))
	if len(l.calls) != 1 {
		t.Errorf("replay posted again: %q", l.calls)
	}
}

// TestLeadLinesFailedPost: a post refused before a byte leaves all lines undelivered and the next pass posts them
// as one.
func TestLeadLinesFailedPost(t *testing.T) {
	t.Parallel()
	now := time.Now()
	a, p := lineApp(t, lineRoot(t), &now)
	leadSends(t, a, p, 3)
	l := &linerSpy{refuse: true}
	a.deliver(context.Background(), p, linerLoop(l))
	if len(l.calls) != 1 {
		t.Fatalf("precondition: one refused post, got %q", l.calls)
	}
	if n := len(p.queuedLines()); n != 3 {
		t.Fatalf("%d lines queued after a refused post, want 3", n)
	}
	if n := deliveredResults(t, p); n != 0 {
		t.Errorf("%d delivery results after a refused post, want 0", n)
	}
	for _, q := range p.queuedLines() {
		if q.typing {
			t.Errorf("line %d still claimed after the refusal", q.seq)
		}
	}
	l.refuse = false
	a.deliver(context.Background(), p, linerLoop(l))
	if len(l.calls) != 2 || !oneLeadPost(l.calls[1]) {
		t.Errorf("retry posted %q, want the three lines as one", l.calls)
	}
	if n := len(p.queuedLines()); n != 0 {
		t.Errorf("%d lines queued after the retry", n)
	}
}

// oneLeadPost: call is one post to the lead of the lines m1, m2, m3 in order, one per row (a send line carries a
// reply hint after its text).
func oneLeadPost(call string) bool {
	rows := strings.Split(strings.TrimPrefix(call, "pj-lead: "), "\n")
	if !strings.HasPrefix(call, "pj-lead: ") || len(rows) != 3 {
		return false
	}
	for i, row := range rows {
		if !strings.HasPrefix(row, "[obor other] m"+strconv.Itoa(i+1)) {
			return false
		}
	}
	return true
}

// T-654 (RV-72 #1 #2): a post's intent names its members and its target; only its result settles them.

// leadSend queues one obor send line "[obor other] <note>" for the lead (as it is now), and is its seq.
func leadSend(t *testing.T, a *App, p *proj, note string) int64 {
	t.Helper()
	if _, err := a.sendCall(Call{Root: p.root, CallID: "s-" + note, From: "other", Note: note, Role: "lead"}); err != nil {
		t.Fatal(err)
	}
	qs := p.queuedLines()
	return qs[len(qs)-1].seq
}

// deliveryResults counts the delivery results of p's log by their line's seq.
func deliveryResults(t *testing.T, p *proj) map[int64]int {
	t.Helper()
	recs, err := ReadLog(p.root)
	if err != nil {
		t.Fatal(err)
	}
	out := map[int64]int{}
	for _, r := range recs {
		if r.Kind == KindResult && r.Task == MsgTask && strings.HasPrefix(r.Step, stepDeliver) {
			seq, _ := strconv.ParseInt(strings.TrimPrefix(r.Step, stepDeliver), 10, 64)
			out[seq]++
		}
	}
	return out
}

// postOf: call is one post to target of exactly the lines notes, in order.
func postOf(call, target string, notes ...string) bool {
	rows := strings.Split(strings.TrimPrefix(call, target+": "), "\n")
	if !strings.HasPrefix(call, target+": ") || len(rows) != len(notes) {
		return false
	}
	for i, row := range rows {
		if !strings.HasPrefix(row, "[obor other] "+notes[i]+" ") {
			return false
		}
	}
	return true
}

// lostLeadResult posts [A] with the result's append refused (kept in p.unwritten), and is A's seq.
func lostLeadResult(t *testing.T, a *App, p *proj, l *linerSpy) int64 {
	t.Helper()
	seqA := leadSend(t, a, p, "A")
	refuseOnce(p, stepDeliver+strconv.FormatInt(seqA, 10))
	a.deliver(context.Background(), p, linerLoop(l))
	if len(l.calls) != 1 || !postOf(l.calls[0], "pj-lead", "A") {
		t.Fatalf("precondition: one post of A, got %q", l.calls)
	}
	if n := len(p.queuedLines()); n != 1 || deliveryResults(t, p)[seqA] != 0 {
		t.Fatalf("precondition: A queued, its result not in the log (queued %d)", n)
	}
	return seqA
}

// TestLeadLateResultNewLine: [A]'s result append fails; B queues; the next pass writes A's kept result (nothing
// transmitted for A) and posts B on its own; each delivered once.
func TestLeadLateResultNewLine(t *testing.T) {
	t.Parallel()
	now := time.Now()
	a, p := lineApp(t, lineRoot(t), &now)
	l := &linerSpy{}
	seqA := lostLeadResult(t, a, p, l)
	seqB := leadSend(t, a, p, "B")
	a.deliver(context.Background(), p, linerLoop(l))
	if len(l.calls) != 2 || !postOf(l.calls[1], "pj-lead", "B") {
		t.Errorf("posted %q, want A's post, then B alone", l.calls)
	}
	if n := len(p.queuedLines()); n != 0 {
		t.Errorf("%d lines still queued", n)
	}
	if got := deliveryResults(t, p); got[seqA] != 1 || got[seqB] != 1 || len(got) != 2 {
		t.Errorf("delivery results %v, want one for A and one for B", got)
	}
	a.deliver(context.Background(), p, linerLoop(l))
	if len(l.calls) != 2 {
		t.Errorf("delivered again: %q", l.calls)
	}
}

// TestLeadLateResultNewLineReplay: the same with a restart between: A's post (its intent pending, may have landed) goes
// again with exactly its member, B on its own; each settled by one result, nothing queued, nothing posted again.
func TestLeadLateResultNewLineReplay(t *testing.T) {
	t.Parallel()
	now := time.Now()
	a, p := lineApp(t, lineRoot(t), &now)
	l := &linerSpy{}
	seqA := lostLeadResult(t, a, p, l)
	b, q := lineReload(t, a, p, &now)
	seqB := leadSend(t, b, q, "B")
	for range 3 {
		b.deliver(context.Background(), q, linerLoop(l))
	}
	if len(l.calls) != 3 || !postOf(l.calls[1], "pj-lead", "A") || !postOf(l.calls[2], "pj-lead", "B") {
		t.Errorf("posted %q, want A, A again alone (its result never reached the log), then B alone", l.calls)
	}
	if n := len(q.queuedLines()); n != 0 {
		t.Errorf("%d lines still queued", n)
	}
	if got := deliveryResults(t, q); got[seqA] != 1 || got[seqB] != 1 || len(got) != 2 {
		t.Errorf("delivery results %v, want one for A and one for B", got)
	}
	c, r := lineReload(t, b, q, &now)
	c.deliver(context.Background(), r, linerLoop(l))
	if n := len(r.queuedLines()); n != 0 || len(l.calls) != 3 {
		t.Errorf("after a second replay: %d queued, posted %q", n, l.calls)
	}
}

// TestLeadLinesTwoTargets: a line queued for the old lead and one for the new: two posts, each to its own target with
// its own line.
func TestLeadLinesTwoTargets(t *testing.T) {
	t.Parallel()
	now := time.Now()
	a, p := lineApp(t, lineRoot(t), &now)
	seqA := leadSend(t, a, p, "A")
	a.mu.Lock()
	p.out.leadAgent = "pj-newlead"
	a.mu.Unlock()
	seqB := leadSend(t, a, p, "B")
	if qs := p.queuedLines(); len(qs) != 2 || qs[0].msg.To != "pj-lead" || qs[1].msg.To != "pj-newlead" {
		t.Fatalf("precondition: a line to each lead, got %d", len(qs))
	}
	l := &linerSpy{}
	a.deliver(context.Background(), p, linerLoop(l))
	if len(l.calls) != 2 || !postOf(l.calls[0], "pj-lead", "A") || !postOf(l.calls[1], "pj-newlead", "B") {
		t.Errorf("posted %q, want A to pj-lead, B to pj-newlead", l.calls)
	}
	if got := deliveryResults(t, p); got[seqA] != 1 || got[seqB] != 1 || len(got) != 2 {
		t.Errorf("delivery results %v, want one each", got)
	}
}

// TestLeadLinesOldTargetNoInbox: the old lead has no inbox record: its line waits (the 24 h needs-you as before); the
// new lead's line goes out.
func TestLeadLinesOldTargetNoInbox(t *testing.T) {
	t.Parallel()
	now := time.Now()
	a, p := lineApp(t, lineRoot(t), &now)
	a.opt.Inboxed = func(agent string) (string, bool) { return "", agent != "pj-lead" }
	seqA := leadSend(t, a, p, "A")
	a.mu.Lock()
	p.out.leadAgent = "pj-newlead"
	a.mu.Unlock()
	seqB := leadSend(t, a, p, "B")
	l := &linerSpy{}
	a.deliver(context.Background(), p, linerLoop(l))
	if len(l.calls) != 1 || !postOf(l.calls[0], "pj-newlead", "B") {
		t.Errorf("posted %q, want only B to pj-newlead", l.calls)
	}
	if qs := p.queuedLines(); len(qs) != 1 || qs[0].seq != seqA {
		t.Errorf("queued %d lines, want A alone", len(qs))
	}
	if got := deliveryResults(t, p); got[seqB] != 1 || len(got) != 1 {
		t.Errorf("delivery results %v, want B's alone", got)
	}
	now = now.Add(msgWait + time.Minute)
	a.deliver(context.Background(), p, linerLoop(l))
	recs, err := ReadLog(p.root)
	if err != nil {
		t.Fatal(err)
	}
	needs := 0
	for _, r := range recs {
		if r.Task == MsgTask && r.Kind == KindFact && r.Step == StepNeedsYou &&
			strings.Contains(r.Fact().Reason, "line to pj-lead from other") {
			needs++
		}
	}
	if needs != 1 || len(l.calls) != 1 {
		t.Errorf("needs-you of A's line %d (want 1), posted %q", needs, l.calls)
	}
}
