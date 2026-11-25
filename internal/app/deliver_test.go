package app

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"obor/internal/app/event"
	"obor/internal/app/outbox"
	"obor/internal/app/port"
	"obor/internal/app/wire"
)

// leadSends queues n obor send lines "[obor other] m<i>" for the lead.
func leadSends(t *testing.T, a *App, p *proj, n int) {
	t.Helper()
	for i := 1; i <= n; i++ {
		id := strconv.Itoa(i)
		if _, err := a.sendCall(wire.SendReq{Envelope: wire.Envelope{Root: p.root, CallID: "s" + id, Role: "lead"},
			From: "other", Text: "m" + id}); err != nil {
			t.Fatal(err)
		}
	}
}

// delivered counts the delivery results recorded for the lead's lines in p's log.
func deliveredResults(t *testing.T, p *proj) int {
	t.Helper()
	recs, err := event.ReadLog(p.root)
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, r := range recs {
		if r.Kind == event.KindResult && r.Task == event.MsgTask && strings.HasPrefix(r.Step, outbox.StepDeliver) {
			n++
		}
	}
	return n
}

// TestLeadLinesOnePost: the lines queued for the lead go out as one post, one per row in order, each with
// its delivery fact; a worker's line stays its own post.
func TestLeadLinesOnePost(t *testing.T) {
	t.Parallel()
	now := time.Now()
	a, p := lineApp(t, lineRoot(t), &now)
	leadSends(t, a, p, 3)
	if _, err := a.briefCall(wire.BriefReq{Envelope: wire.Envelope{Root: p.root, CallID: "b1", Role: "lead"},
		To: "pj-rev", Text: "check"}); err != nil {
		t.Fatal(err)
	}
	if n := len(p.out.Queued()); n != 4 {
		t.Fatalf("precondition: four lines queued, got %d", n)
	}
	l := &linerSpy{}
	a.deliver(context.Background(), p, linerLoop(l))
	if len(l.calls) != 2 || l.calls[1] != "w:rev: [obor owner] check" || !oneLeadPost(l.calls[0]) {
		t.Errorf("posted %q, want one lead post of m1..m3 in order, then the brief", l.calls)
	}
	if n := len(p.out.Queued()); n != 0 {
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
	if n := len(q.out.Queued()); n != 0 {
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
	if n := len(p.out.Queued()); n != 3 {
		t.Fatalf("%d lines queued after a refused post, want 3", n)
	}
	if n := deliveredResults(t, p); n != 0 {
		t.Errorf("%d delivery results after a refused post, want 0", n)
	}
	for _, q := range p.out.Queued() {
		if q.State() != outbox.Waiting {
			t.Errorf("line %d still claimed after the refusal", q.Seq())
		}
	}
	l.refuse = false
	a.deliver(context.Background(), p, linerLoop(l))
	if len(l.calls) != 2 || !oneLeadPost(l.calls[1]) {
		t.Errorf("retry posted %q, want the three lines as one", l.calls)
	}
	if n := len(p.out.Queued()); n != 0 {
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

// A post's intent names its members and its target; only its result settles them.

// leadSend queues one obor send line "[obor other] <note>" for the lead (as it is now), and is its seq.
func leadSend(t *testing.T, a *App, p *proj, note string) int64 {
	t.Helper()
	if _, err := a.sendCall(wire.SendReq{Envelope: wire.Envelope{Root: p.root, CallID: "s-" + note, Role: "lead"},
		From: "other", Text: note}); err != nil {
		t.Fatal(err)
	}
	qs := p.out.Queued()
	return qs[len(qs)-1].Seq()
}

// deliveryResults counts the delivery results of p's log by their line's seq.
func deliveryResults(t *testing.T, p *proj) map[int64]int {
	t.Helper()
	recs, err := event.ReadLog(p.root)
	if err != nil {
		t.Fatal(err)
	}
	out := map[int64]int{}
	for _, r := range recs {
		if r.Kind == event.KindResult && r.Task == event.MsgTask && strings.HasPrefix(r.Step, outbox.StepDeliver) {
			seq, _ := strconv.ParseInt(strings.TrimPrefix(r.Step, outbox.StepDeliver), 10, 64)
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
	refuseOnce(p, outbox.StepDeliver+strconv.FormatInt(seqA, 10))
	a.deliver(context.Background(), p, linerLoop(l))
	if len(l.calls) != 1 || !postOf(l.calls[0], "pj-lead", "A") {
		t.Fatalf("precondition: one post of A, got %q", l.calls)
	}
	if n := len(p.out.Queued()); n != 1 || deliveryResults(t, p)[seqA] != 0 {
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
	if n := len(p.out.Queued()); n != 0 {
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
	if n := len(q.out.Queued()); n != 0 {
		t.Errorf("%d lines still queued", n)
	}
	if got := deliveryResults(t, q); got[seqA] != 1 || got[seqB] != 1 || len(got) != 2 {
		t.Errorf("delivery results %v, want one for A and one for B", got)
	}
	c, r := lineReload(t, b, q, &now)
	c.deliver(context.Background(), r, linerLoop(l))
	if n := len(r.out.Queued()); n != 0 || len(l.calls) != 3 {
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
	relead(t, a, p, "pj-newlead")
	seqB := leadSend(t, a, p, "B")
	if qs := p.out.Queued(); len(qs) != 2 || qs[0].Msg().To != "pj-lead" || qs[1].Msg().To != "pj-newlead" {
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

// TestLeadLinesOldTargetNoInbox: the old lead has no inbox record: its line waits (the 24 h needs-you as for any
// waiting line); the new lead's line goes out.
func TestLeadLinesOldTargetNoInbox(t *testing.T) {
	t.Parallel()
	now := time.Now()
	a, p := lineApp(t, lineRoot(t), &now)
	a.opt.Inboxed = func(agent string) (string, bool) { return "", agent != "pj-lead" }
	seqA := leadSend(t, a, p, "A")
	relead(t, a, p, "pj-newlead")
	seqB := leadSend(t, a, p, "B")
	l := &linerSpy{}
	a.deliver(context.Background(), p, linerLoop(l))
	if len(l.calls) != 1 || !postOf(l.calls[0], "pj-newlead", "B") {
		t.Errorf("posted %q, want only B to pj-newlead", l.calls)
	}
	if qs := p.out.Queued(); len(qs) != 1 || qs[0].Seq() != seqA {
		t.Errorf("queued %d lines, want A alone", len(qs))
	}
	if got := deliveryResults(t, p); got[seqB] != 1 || len(got) != 1 {
		t.Errorf("delivery results %v, want B's alone", got)
	}
	lineStatus(a, "w:rev", "working", now) // the idle reviewer is no done reminder among these lines
	now = now.Add(msgWait + time.Minute)
	a.deliver(context.Background(), p, linerLoop(l))
	a.msgNeedsYou(p, now)
	recs, err := event.ReadLog(p.root)
	if err != nil {
		t.Fatal(err)
	}
	needs := 0
	for _, r := range recs {
		if r.Task == event.MsgTask && r.Kind == event.KindFact && r.Step == event.StepNeedsYou &&
			strings.Contains(r.Fact().Reason, "line to pj-lead from other") {
			needs++
		}
	}
	if needs != 1 || len(l.calls) != 1 {
		t.Errorf("needs-you of A's line %d (want 1), posted %q", needs, l.calls)
	}
}

// lineKinds are the three kinds of line: an obor send line to the lead, an obor brief text to the reviewer, and the
// lead's finished line of a brief whose reviewer worked and is idle again.
var lineKinds = []string{"send", "brief", "finished"}

// lineTyped: calls is the line of kind typed once (a finished line's age aside; a brief typed into pj-rev's pane).
func lineTyped(calls []string, kind string) bool {
	want := map[string]string{"send": "pj-lead: " + sendLine("other", "hello"), "brief": "w:rev: [obor owner] verify finding 1",
		"finished": "pj-lead: [obor] pj-rev finished (briefed "}[kind]
	return len(calls) == 1 && strings.HasPrefix(calls[0], want)
}

// TestRefusedLineReplay: a line refused before typing stays queued across a restart (orderly and SIGKILL) and is
// typed once after it, for each kind of line.
func TestRefusedLineReplay(t *testing.T) {
	t.Parallel()
	for _, kind := range lineKinds {
		t.Run(kind+"/reload", func(t *testing.T) {
			t.Parallel()
			now := time.Now()
			a, p := lineApp(t, lineRoot(t), &now)
			queueLine(t, a, p, kind, now)
			l := &linerSpy{refuse: true}
			finishBriefs(a, p, linerLoop(l))
			a.deliver(context.Background(), p, linerLoop(l))
			if len(l.calls) != 1 || len(p.out.Queued()) != 1 {
				t.Fatalf("precondition: one refused try of one queued line; calls %v, queued %d", l.calls, len(p.out.Queued()))
			}
			b, q := lineReload(t, a, p, &now)
			if len(q.out.Queued()) != 1 {
				t.Fatalf("the refused line is not queued after reload: %d", len(q.out.Queued()))
			}
			good := &linerSpy{}
			finishBriefs(b, q, linerLoop(good))
			b.deliver(context.Background(), q, linerLoop(good))
			finishBriefs(b, q, linerLoop(good))
			b.deliver(context.Background(), q, linerLoop(good))
			if !lineTyped(good.calls, kind) || len(q.out.Queued()) != 0 {
				t.Fatalf("after reload: want the %s line typed once, got %v (queued %d)", kind, good.calls, len(q.out.Queued()))
			}
			c, s := lineReload(t, b, q, &now)
			finishBriefs(c, s, linerLoop(good))
			c.deliver(context.Background(), s, linerLoop(good))
			if len(good.calls) != 1 {
				t.Fatalf("typed again after a second reload: %v", good.calls)
			}
		})
		t.Run(kind+"/kill", func(t *testing.T) {
			t.Parallel()
			calls := killedLines(t, kind, "refused")
			if !lineTyped(calls, kind) {
				t.Fatalf("after SIGKILL: want the %s line typed once, got %v", kind, calls)
			}
		})
	}
}

// TestRefusedLineRetryKilled: a refused line's retry cut by a SIGKILL once its typing began is delivered once more
// after the restart (a line is never lost; it may reach its agent twice); a line queued without a delivery is
// typed once (control).
func TestRefusedLineRetryKilled(t *testing.T) {
	t.Parallel()
	for _, kind := range lineKinds {
		t.Run(kind, func(t *testing.T) {
			t.Parallel()
			if calls := killedLines(t, kind, "retry"); !lineTyped(calls, kind) {
				t.Fatalf("a retry cut while typing: want the %s line delivered once after the restart, got %v", kind,
					calls)
			}
		})
	}
	t.Run("queued control", func(t *testing.T) {
		t.Parallel()
		if calls := killedLines(t, "send", "queued"); !lineTyped(calls, "send") {
			t.Fatalf("a queued line: want one delivery, got %v", calls)
		}
	})
}

// killedLines runs TestRefusedLineKillChild for kind and mode, kills it with SIGKILL once it is ready, and is the lines
// typed by two reloads after it (two deliver passes each).
func killedLines(t *testing.T, kind, mode string) []string {
	t.Helper()
	root := lineRoot(t)
	child := exec.Command(os.Args[0], "-test.run=^TestRefusedLineKillChild$")
	child.Env = append(os.Environ(), "LINE_ROOT="+root, "LINE_KIND="+kind, "LINE_MODE="+mode)
	if err := child.Start(); err != nil {
		t.Fatal(err)
	}
	defer child.Process.Kill()
	ready := false
	for end := time.Now().Add(10 * time.Second); time.Now().Before(end); time.Sleep(5 * time.Millisecond) {
		if _, err := os.Stat(filepath.Join(root, "kill-ready")); err == nil {
			ready = true
			break
		}
	}
	if !ready {
		t.Fatal("the child did not get ready")
	}
	_ = child.Process.Kill()
	_ = child.Wait()
	if st := child.ProcessState.Sys().(syscall.WaitStatus); !st.Signaled() || st.Signal() != syscall.SIGKILL {
		t.Fatalf("the child did not end by SIGKILL: %v", child.ProcessState)
	}
	now := time.Now()
	l := &linerSpy{}
	a, p := lineApp(t, root, &now)
	if p.out.Lead() != "pj-lead" {
		t.Fatalf("precondition: lead pj-lead, got %s", p.out.Lead())
	}
	finishBriefs(a, p, linerLoop(l))
	a.deliver(context.Background(), p, linerLoop(l))
	finishBriefs(a, p, linerLoop(l))
	a.deliver(context.Background(), p, linerLoop(l))
	b, q := lineReload(t, a, p, &now)
	finishBriefs(b, q, linerLoop(l))
	b.deliver(context.Background(), q, linerLoop(l))
	finishBriefs(b, q, linerLoop(l))
	b.deliver(context.Background(), q, linerLoop(l))
	return l.calls
}

// TestRefusedLineKillChild is killedLines's child: it queues a line of LINE_KIND; refused: its delivery is refused; retry:
// refused, then its retry hangs once typing began; queued: nothing delivered; worked (no line queued): a fresh
// start's brief whose work a pass saw. Then it writes kill-ready and waits.
func TestRefusedLineKillChild(t *testing.T) {
	t.Parallel()
	root := os.Getenv("LINE_ROOT")
	if root == "" {
		t.Skip("killedLines's child only")
	}
	now := time.Now()
	a, p := lineApp(t, root, &now)
	if os.Getenv("LINE_MODE") != "worked" {
		queueLine(t, a, p, os.Getenv("LINE_KIND"), now)
	}
	ready := func() { _ = os.WriteFile(filepath.Join(root, "kill-ready"), []byte("ready"), 0o600) }
	switch os.Getenv("LINE_MODE") {
	case "refused":
		finishBriefs(a, p, &Loop{})
		a.deliver(context.Background(), p, linerLoop(&linerSpy{refuse: true}))
	case "retry":
		finishBriefs(a, p, &Loop{})
		a.deliver(context.Background(), p, linerLoop(&linerSpy{refuse: true}))
		finishBriefs(a, p, &Loop{})
		a.deliver(context.Background(), p, linerLoop(&linerSpy{hang: ready}))
	case "worked":
		if _, err := a.briefCall(wire.BriefReq{Envelope: wire.Envelope{Root: p.root, CallID: "b1", Role: "lead"},
			To: "pj-rev"}); err != nil {
			t.Fatal(err)
		}
		lineStatus(a, "w:rev", "working", now)
		finishBriefs(a, p, &Loop{})
		a.deliver(context.Background(), p, linerLoop(&linerSpy{}))
	}
	ready()
	park()
}

// answerSeq is the seq of h's one queued line (precondition: exactly one).
func answerSeq(t *testing.T, h *harness) int64 {
	t.Helper()
	h.a.mu.Lock()
	defer h.a.mu.Unlock()
	qs := h.proj().out.Queued()
	if len(qs) != 1 || !qs[0].IsAnswer() {
		t.Fatalf("precondition: one queued answer, got %d lines", len(qs))
	}
	return qs[0].Seq()
}

// lineAt is the state of line seq in p (ok false: it left the outbox).
func lineAt(h *harness, p *proj, seq int64) (outbox.State, bool) {
	h.a.mu.Lock()
	defer h.a.mu.Unlock()
	q := p.out.Line(seq)
	if q == nil {
		return 0, false
	}
	return q.State(), true
}

// deliverKey is line seq's delivery key step.
func deliverKey(seq int64) string { return outbox.StepDeliver + strconv.FormatInt(seq, 10) }

// lineRec records r with data in h's project (fatal when the log refuses it).
func lineRec(t *testing.T, h *harness, r event.Record, data any) {
	t.Helper()
	if !h.a.record(h.proj(), r, data) {
		t.Fatalf("%s not recorded", r.Step)
	}
}

// claimIntent records line seq's delivery intent: its delivery began (sending).
func claimIntent(t *testing.T, h *harness, seq int64) {
	lineRec(t, h, event.Record{Task: event.MsgTask, Step: deliverKey(seq), Kind: event.KindIntent,
		Class: event.ClassPrompt}, outbox.LineData{To: "pj-l-01"})
}

// submitT1 records T-1's submission: its question is settled another way.
func submitT1(t *testing.T, h *harness) {
	lineRec(t, h, event.Record{Task: "T-1", Attempt: 1, Step: event.StepSubmitted, Kind: event.KindFact},
		event.FactData{Lane: "L-01", Agent: "pj-l-01", Merge: true})
}

// answerIn is busyAnswered's answer moved into state from, by the log's records.
func answerIn(t *testing.T, from outbox.State) (*harness, int64) {
	t.Helper()
	h := busyAnswered(t)
	seq := answerSeq(t, h)
	if from != outbox.Waiting {
		claimIntent(t, h, seq)
	}
	if from == outbox.SendingSettled {
		submitT1(t, h)
	}
	if s, ok := lineAt(h, h.proj(), seq); !ok || s != from {
		t.Fatalf("precondition: the answer in state %d (queued %v), want %d", s, ok, from)
	}
	return h, seq
}

func TestLineStateTable(t *testing.T) {
	t.Parallel()
	events := map[string]struct {
		e  outbox.Event // the event next sees ("replay": none)
		do func(t *testing.T, h *harness, seq int64)
	}{
		"delivered": {outbox.Delivered, func(t *testing.T, h *harness, seq int64) {
			lineRec(t, h, event.Record{Task: event.MsgTask, Step: deliverKey(seq), Kind: event.KindResult}, "sent")
		}},
		"refused before a byte": {outbox.Refused, func(t *testing.T, h *harness, seq int64) {
			lineRec(t, h, event.Record{Task: event.MsgTask, Step: event.StepFailed, Kind: event.KindFact},
				event.FactData{Machine: Machine, Reason: deliverKey(seq) + ": dial: connection refused", Step: deliverKey(seq),
					Via: outbox.ViaInbox})
		}},
		"error after a byte": {outbox.Delivered, func(t *testing.T, h *harness, seq int64) {
			lineRec(t, h, event.Record{Task: event.MsgTask, Step: deliverKey(seq), Kind: event.KindResult},
				"sent: broken pipe")
		}},
		"settled": {outbox.Settled, func(t *testing.T, h *harness, _ int64) { submitT1(t, h) }},
		"dropped": {outbox.Settled, func(t *testing.T, h *harness, _ int64) {
			lineRec(t, h, event.Record{Task: "T-1", Attempt: 1, Step: event.StepDropped, Kind: event.KindFact},
				event.FactData{Lane: "L-01", Merge: true})
		}},
	}
	const gone = outbox.State(255) // the line left the outbox (sent or retired: next says which)
	for _, row := range []struct {
		from  outbox.State
		event string
		want  outbox.State
	}{
		{outbox.Waiting, "delivered", outbox.Sent},
		{outbox.Waiting, "refused before a byte", outbox.Waiting},
		{outbox.Waiting, "error after a byte", outbox.Sent},
		{outbox.Waiting, "settled", outbox.Retired},
		{outbox.Waiting, "dropped", outbox.Retired},
		{outbox.Waiting, "replay", outbox.Waiting},
		{outbox.Sending, "delivered", outbox.Sent},
		{outbox.Sending, "refused before a byte", outbox.Waiting},
		{outbox.Sending, "error after a byte", outbox.Sent},
		{outbox.Sending, "settled", outbox.SendingSettled},
		{outbox.Sending, "dropped", outbox.SendingSettled},
		{outbox.Sending, "replay", outbox.Sending},
		{outbox.SendingSettled, "delivered", outbox.Sent},
		{outbox.SendingSettled, "refused before a byte", outbox.Retired}, // the refusal-and-settle race: settled, then refused
		{outbox.SendingSettled, "error after a byte", outbox.Sent},
		{outbox.SendingSettled, "settled", outbox.SendingSettled},
		{outbox.SendingSettled, "dropped", outbox.SendingSettled},
		{outbox.SendingSettled, "replay", outbox.SendingSettled},
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
			if got := row.from.Next(ev.e); got != row.want {
				t.Errorf("next: %d, want %d", got, row.want)
			}
			ev.do(t, h, seq)
			want := row.want
			if want == outbox.Sent || want == outbox.Retired {
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
	for _, s := range []outbox.State{outbox.Sent, outbox.Retired} {
		for _, e := range []outbox.Event{outbox.Claimed, outbox.Delivered, outbox.Refused, outbox.Settled, outbox.Closed} {
			if got := s.Next(e); got != s {
				t.Errorf("%d on %d: %d, want it to stay", s, e, got)
			}
		}
	}
}

// A post refused before a byte, then the question settled (task_done):
// the answer retires, the next idle sends nothing, and replay keeps it retired.
func TestRV90RefusedAndSettled(t *testing.T) {
	t.Parallel()
	h := staleHerdr(t)
	in := &linerSpy{refuse: true}
	fx := withAppCfg(h.a, &Loop{Inbox: in, Prompter: &linerSpy{}})
	h.a.deliver(context.Background(), h.proj(), fx)
	seq := answerSeq(t, h)
	if s, _ := lineAt(h, h.proj(), seq); len(in.calls) != 1 || s != outbox.Waiting {
		t.Fatalf("precondition: one refused post, the answer waiting; posts %q, state %d", in.calls, s)
	}
	submitT1(t, h)
	if q := h.lines(); len(q) != 0 {
		t.Errorf("queued %q after task_done; want the refused answer retired", q)
	}
	in.refuse = false
	typer := &linerSpy{}
	h.a.deliver(context.Background(), h.proj(), withAppCfg(h.a, &Loop{Inbox: in, Prompter: typer}))
	if len(in.calls) != 1 || len(typer.calls) != 0 {
		t.Errorf("posts %q, typed %q at the next idle; want the settled answer never sent", in.calls, typer.calls)
	}
	if _, ok := lineAt(h, replayed(t, h), seq); ok {
		t.Error("replay brought the retired answer back")
	}
}

// A refused post keeps its reason (obor status, the needs-you), and the next
// attempt to the idle identified worker types it instead of dialling the dead socket again.
func TestRV90RefusedReason(t *testing.T) {
	t.Parallel()
	h := staleHerdr(t)
	in := &linerSpy{refuse: true}
	h.a.deliver(context.Background(), h.proj(), withAppCfg(h.a, &Loop{Inbox: in}))
	if len(in.calls) != 1 {
		t.Fatalf("precondition: one refused post, got %q", in.calls)
	}
	const why = "its delivery was refused before a byte went out: agent_not_ready: no typing"
	if ms := h.a.Status().Project.Messages; len(ms) != 1 || ms[0].Held != why {
		t.Errorf("status lines %+v; want the answer held %q", ms, why)
	}
	h.now = h.now.Add(msgWait + time.Minute) // nothing types here: the post is tried again, refused again
	h.a.deliver(context.Background(), h.proj(), withAppCfg(h.a, &Loop{Inbox: in}))
	h.a.msgNeedsYou(h.proj(), h.now)
	var reasons []string
	for _, f := range h.a.Status().Project.Facts {
		if f.Task == event.MsgTask && f.Step == event.StepNeedsYou {
			reasons = append(reasons, f.Fact().Reason)
		}
	}
	if len(in.calls) != 2 || len(reasons) != 1 || !strings.HasSuffix(reasons[0], "(undelivered: "+why+")") {
		t.Errorf("posts %q, needs-you %q; want a second refused post and the needs-you naming its refusal", in.calls,
			reasons)
	}
	typer := &linerSpy{}
	h.a.deliver(context.Background(), h.proj(), withAppCfg(h.a, &Loop{Inbox: in, Prompter: typer}))
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
	h.a.deliver(context.Background(), h.proj(), withAppCfg(h.a, &Loop{Inbox: in}))
	seq := answerSeq(t, h)
	r := replayed(t, h)
	h.a.mu.Lock()
	rq := r.out.Line(seq)
	h.a.mu.Unlock()
	if rq == nil || !rq.RefusedPost() || rq.State() != outbox.Waiting || !strings.Contains(rq.Refused(), "agent_not_ready") {
		t.Errorf("replayed line %+v; want it waiting with its refused post and reason", rq)
	}
	h2 := staleHerdr(t)
	in2 := &linerSpy{refuse: true}
	h2.a.deliver(context.Background(), h2.proj(), withAppCfg(h2.a, &Loop{Inbox: in2}))
	h2.status("w1:p1", func(p *port.Pane) { p.Status = "working" })
	typer := &linerSpy{}
	h2.a.deliver(context.Background(), h2.proj(), withAppCfg(h2.a, &Loop{Inbox: in2, Prompter: typer}))
	if len(in2.calls) != 1 || len(typer.calls) != 0 || len(h2.lines()) != 1 {
		t.Fatalf("busy: posts %q, typed %q; want nothing sent to the working worker", in2.calls, typer.calls)
	}
	h2.status("w1:p1", func(p *port.Pane) { p.Status = "idle" })
	h2.a.deliver(context.Background(), h2.proj(), withAppCfg(h2.a, &Loop{Inbox: in2, Prompter: typer}))
	if len(in2.calls) != 1 || len(typer.calls) != 1 || len(h2.lines()) != 0 {
		t.Errorf("idle: posts %q, typed %q, queued %q; want it typed once", in2.calls, typer.calls, h2.lines())
	}
}

// A lead line's refused post keeps its reason too (obor status), and is posted again, never typed.
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
	if len(l.calls) != 2 || !strings.HasPrefix(l.calls[1], "pj-lead: ") || len(p.out.Queued()) != 0 {
		t.Errorf("calls %q; want the lead line posted again to pj-lead (not typed into its pane)", l.calls)
	}
}

// obor drop retires the task's undelivered answer; the lane's worker idle again gets
// nothing. A reject retires it the same way.
func TestRV90Drop(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		rec  event.Record
		data any
	}{
		{"drop", event.Record{Task: "T-1", Attempt: 1, Step: event.StepDropped, Kind: event.KindFact},
			event.FactData{Lane: "L-01", Merge: true}},
		{"reject", event.Record{Task: "T-1", Attempt: 1, Step: "reject.r1", Kind: event.KindResult},
			event.VerdictResult{Job: "T-1/1/review", Reply: "REJECT T-1"}},
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
			h.a.deliver(context.Background(), h.proj(), withAppCfg(h.a, &Loop{Inbox: in, Prompter: typer}))
			if len(in.calls) != 0 || len(typer.calls) != 0 {
				t.Errorf("posted %q, typed %q; want nothing sent to the lane's worker", in.calls, typer.calls)
			}
		})
	}
}

// blockingPoster posts nothing until release closes: started is closed once its first Post began (a delivery whose
// effect is in flight).
type blockingPoster struct {
	started, release chan struct{}
	once             sync.Once
}

func (b *blockingPoster) Post(context.Context, string, string, string) (bool, error) {
	b.once.Do(func() { close(b.started) })
	<-b.release
	return true, nil
}

// closedUnsent: line seq's delivery has result retired in h's log, and no other result.
func closedUnsent(h *harness, seq int64) (closed bool, others []string) {
	for _, r := range h.records() {
		if r.Kind != event.KindResult || r.Step != deliverKey(seq) {
			continue
		}
		if strings.Contains(string(r.Data), outbox.RetiredResult) {
			closed = true
		} else {
			others = append(others, string(r.Data))
		}
	}
	return closed, others
}

// A line settled while its delivery was open (a crash cut it: its intent pending, no result) is never sent again:
// the next pass closes its delivery unsent (result retired), whatever its worker's pane (idle,
// working, not identified, two panes claiming it, its lane released), live and on replay; it leaves obor status and
// raises no needs-you, even past msgWait.
func TestSettledOpenDeliveryClosedUnsent(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name  string
		pane  func(h *harness) // the worker's pane before the pass
		where string           // the precondition: pj-l-01's place in the identity table, as h.where says it (prefix)
	}{
		{"idle", func(h *harness) { h.status("w1:p1", func(p *port.Pane) { p.Status = "idle" }) }, "identified w1:p1"},
		{"working", func(*harness) {}, "identified w1:p1"},
		{"not identified", func(h *harness) { h.remove("w1:p1", port.EventClosed) }, "unavailable"},
		{"ambiguous", func(h *harness) { // a second pane with the same proof (its session)
			tw := h.twin()
			tw.Session = h.pane("w1:p1").Session
			h.add(tw)
		}, "ambiguous"},
		{"lane released", func(h *harness) {
			h.freeLanes()
			h.remove("w1:p1", port.EventExited)
		}, "unavailable"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			h, seq := answerIn(t, outbox.SendingSettled)
			tc.pane(h)
			if w := h.where("pj-l-01"); !strings.HasPrefix(w, tc.where) {
				t.Fatalf("precondition: pj-l-01 is %q, want %s", w, tc.where)
			}
			if ms := h.a.Status().Project.Messages; len(ms) != 1 {
				t.Fatalf("precondition: status lines %+v, want the settled answer shown", ms)
			}
			h.now = h.now.Add(msgWait + time.Minute) // it would raise its needs-you if it stayed
			in, typer := &kindSpy{}, &linerSpy{}
			h.a.closeSettled(context.Background(), h.proj())
			h.a.deliver(context.Background(), h.proj(), withAppCfg(h.a, &Loop{Inbox: in, Prompter: typer}))
			h.a.msgNeedsYou(h.proj(), h.now)
			if len(in.calls) != 0 || len(typer.calls) != 0 {
				t.Errorf("posted %q, typed %q; want the settled answer not sent again", in.calls, typer.calls)
			}
			if _, ok := lineAt(h, h.proj(), seq); ok {
				t.Error("the settled line is still queued after the pass")
			}
			if ms := h.a.Status().Project.Messages; len(ms) != 0 {
				t.Errorf("status lines %+v; want the closed answer gone", ms)
			}
			if n := msgNeeds(h); len(n) != 0 {
				t.Errorf("needs-you of the lines %q; want none for a closed line", n)
			}
			if closed, others := closedUnsent(h, seq); !closed || len(others) != 0 {
				t.Errorf("retired result %v, other results %q; want its delivery closed unsent only", closed, others)
			}
			if _, ok := lineAt(h, replayed(t, h), seq); ok {
				t.Error("replay brought the closed line back")
			}
		})
	}
}

// A serve restart replays the settled open delivery (sending, settled); the new serve's first pass closes it, its
// worker still working, and a replay after that keeps it closed.
func TestSettledOpenDeliveryClosedAfterReplay(t *testing.T) {
	t.Parallel()
	h, seq := answerIn(t, outbox.SendingSettled)
	h.a.closeLog()
	a := newTestApp(Options{Root: h.root, Now: func() time.Time { return h.now }}, h.panes, h.lanes)
	p, err := a.load(h.root)
	if err != nil {
		t.Fatal(err)
	}
	if s, ok := lineAt(&harness{a: a}, p, seq); !ok || s != outbox.SendingSettled {
		t.Fatalf("precondition: replayed line in state %d (queued %v), want sending settled", s, ok)
	}
	h.now = h.now.Add(msgWait + time.Minute)
	in, typer := &kindSpy{}, &linerSpy{}
	a.closeSettled(context.Background(), p)
	a.deliver(context.Background(), p, withAppCfg(a, &Loop{Inbox: in, Prompter: typer}))
	if len(in.calls) != 0 || len(typer.calls) != 0 {
		t.Errorf("posted %q, typed %q; want nothing sent", in.calls, typer.calls)
	}
	if _, ok := lineAt(&harness{a: a}, p, seq); ok {
		t.Error("the replayed settled line is still queued after the pass")
	}
	p.log.Close()
	if closed, others := closedUnsent(h, seq); !closed || len(others) != 0 {
		t.Errorf("retired result %v, other results %q; want its delivery closed unsent only", closed, others)
	}
	if _, ok := lineAt(h, replayed(t, h), seq); ok {
		t.Error("a second replay brought the closed line back")
	}
}

// A delivery whose effect is in flight (its post runs now) when its question is settled is left to its outcome: a
// pass meanwhile closes nothing, and the post's result (sent) is what ends the line.
func TestSettledInFlightDeliveryLeftAlone(t *testing.T) {
	t.Parallel()
	h := busyAnswered(t)
	seq := answerSeq(t, h)
	h.status("w1:p1", func(p *port.Pane) { p.Status = "idle" })
	post := &blockingPoster{started: make(chan struct{}), release: make(chan struct{})}
	done := make(chan struct{})
	go func() {
		defer close(done)
		h.a.deliver(context.Background(), h.proj(), withAppCfg(h.a, &Loop{Inbox: post, Prompter: &linerSpy{}}))
	}()
	<-post.started
	submitT1(t, h)
	if s, ok := lineAt(h, h.proj(), seq); !ok || s != outbox.SendingSettled {
		t.Fatalf("precondition: the in-flight line in state %d (queued %v), want sending settled", s, ok)
	}
	h.status("w1:p1", func(p *port.Pane) { p.Status = "working" })
	h.a.deliver(context.Background(), h.proj(), withAppCfg(h.a, &Loop{Inbox: &kindSpy{}, Prompter: &linerSpy{}}))
	if closed, others := closedUnsent(h, seq); closed || len(others) != 0 {
		t.Errorf("retired result %v, other results %q while its post runs; want none", closed, others)
	}
	if s, ok := lineAt(h, h.proj(), seq); !ok || s != outbox.SendingSettled {
		t.Errorf("the in-flight line in state %d (queued %v); want it left sending settled", s, ok)
	}
	close(post.release)
	<-done
	if closed, others := closedUnsent(h, seq); closed || len(others) != 1 || others[0] != `"sent"` {
		t.Errorf("retired result %v, other results %q; want the post's result sent only", closed, others)
	}
	if _, ok := lineAt(h, h.proj(), seq); ok {
		t.Error("the delivered line is still queued")
	}
}

// relead renames p's lead to lead as a restart with that config would: its outbox folded anew from the log's records
// (the outbox's lead is its configured one).
func relead(t *testing.T, a *App, p *proj, lead string) {
	t.Helper()
	recs, err := event.ReadLog(p.root)
	if err != nil {
		t.Fatal(err)
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	o := outbox.New(lead, p.out.LeadKind())
	for _, r := range recs {
		if settles(r) {
			o.RetireAnswers(r.Task, 0)
		}
		o.Fold(r, p.oldLineOf)
	}
	p.out = o
}
