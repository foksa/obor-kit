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
